package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/tool"
)

// Workspace builds a built-in tool set bound to a working directory, so several
// agents can run concurrently with independent path roots — a desktop front-end
// opening one tab per project, say. The process working directory is global and
// cannot be made per-agent (os.Chdir is process-wide), so each tool instead
// resolves relative paths against this directory and bash runs in it.
//
// Dir is that directory (empty yields process-cwd tools, byte-identical to the
// compile-time built-ins). WriteRoots is ignored (sandbox disabled).
type Workspace struct {
	Dir         string
	BashTimeout time.Duration
	Search      SearchSpec
}

// Tools returns the built-in tools bound to the workspace, ready to Add to a
// per-run tool.Registry. An empty enabled list yields every built-in; otherwise
// only the named ones are returned (unknown names are ignored). This is the
// per-workspace analogue of the cli's process-cwd assembly — a desktop driver
// calls it once per agent instead of relying on the global working directory.
func (w Workspace) Tools(enabled ...string) []tool.Tool {

	overrides := map[string]tool.Tool{
		"read_file":     readFile{workDir: w.Dir},
		"write_file":    writeFile{workDir: w.Dir},
		"edit_file":     editFile{workDir: w.Dir},
		"multi_edit":    multiEdit{workDir: w.Dir},
		"move_file":     moveFile{workDir: w.Dir},
		"notebook_edit": notebookEdit{workDir: w.Dir},
		"delete_range":  deleteRange{workDir: w.Dir},
		"delete_symbol": deleteSymbol{workDir: w.Dir},
		"bash":          bash{workDir: w.Dir, timeout: w.BashTimeout},
		"ls":            listDir{workDir: w.Dir},
		"glob":          globTool{workDir: w.Dir},
		"grep":          grepTool{workDir: w.Dir, search: w.Search},
		"ctx_run":       ctxRun{workDir: w.Dir},
		"ctx_index":     ctxIndex{workDir: w.Dir},
	}
	all := tool.Builtins()
	if len(enabled) == 0 {
		for i, t := range all {
			if bound, ok := overrides[t.Name()]; ok {
				all[i] = bound
			}
		}
		return all
	}
	want := make(map[string]bool, len(enabled))
	for _, n := range enabled {
		want[n] = true
	}
	out := make([]tool.Tool, 0, len(enabled))
	for _, t := range all {
		if want[t.Name()] {
			if bound, ok := overrides[t.Name()]; ok {
				t = bound
			}
			out = append(out, t)
		}
	}
	return out
}

// resolveIn maps a tool's path/pattern argument into a working directory. With
// an empty workDir it returns p unchanged — the process-cwd behavior the
// compile-time built-ins have always had, so existing callers are unaffected.
// Otherwise a relative p is joined onto workDir; an absolute p is returned as-is
// (an explicit absolute path is honored verbatim — the write-confiner, not this,
// enforces the workspace boundary). An empty p resolves to workDir itself, so a
// defaulted "." (ls/grep) targets the workspace root.
func resolveIn(workDir, p string) string {
	if workDir == "" {
		return p
	}
	if p == "" || p == "." {
		return workDir
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(workDir, p)
}

// checkInWorkDir rejects paths that escape workDir when a workspace is bound.
// Empty workDir means process-cwd tools (unconfined). Absolute paths,
// cleaned ".." joins, and symlink escapes are all checked so read_file
// cannot open /etc/passwd and a symlink inside the workspace cannot tunnel out.
func checkInWorkDir(workDir, path string) error {
	if workDir == "" || path == "" {
		return nil
	}
	absW, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	absP, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	rel, err := filepath.Rel(absW, absP)
	if err != nil {
		return fmt.Errorf("%s is outside the allowed workspace %s", path, workDir)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s is outside the allowed workspace %s", path, workDir)
	}
	// Resolve symlinks and re-check. A symlink like workspace/link -> /etc
	// would pass the Abs check above (workspace/link/passwd is inside the
	// workspace prefix) but actually reads /etc/passwd. Walk up the path to
	// find the deepest existing ancestor (EvalSymlinks requires the path or
	// at least a prefix to exist) and verify the resolved target is still
	// inside the workspace.
	if resolved, ok := resolveExistingAncestor(absP); ok {
		if rel, err := filepath.Rel(absW, resolved); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("symlink target %s is outside the allowed workspace %s", resolved, absW)
		}
	}
	return nil
}

// resolveExistingAncestor walks up from absPath to find the deepest path
// component that exists, resolves symlinks on it via EvalSymlinks, and
// re-appends the remaining non-existent tail. Returns ("", false) when even
// the root doesn't exist (nothing to resolve).
func resolveExistingAncestor(absPath string) (string, bool) {
	// Collect the tail components we need to re-attach after resolution.
	var tail []string
	p := absPath
	for {
		_, err := os.Lstat(p)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(p)
			if err != nil {
				return "", false
			}
			// Walk back down through the collected tail.
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, true
		}
		if !os.IsNotExist(err) {
			return "", false
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", false // reached root without finding anything
		}
		tail = append(tail, filepath.Base(p))
		p = parent
	}
}

// vendorDirs are directory names grep and glob skip during a recursive walk:
// dependency, VCS, and build-cache trees that almost never hold the searched
// source and would otherwise dominate the walk (node_modules alone can be 100k+
// files) and fill the result cap with noise. Only skipped when nested — a walk
// rooted directly at one (an explicit `grep node_modules`) still searches it.
var vendorDirs = map[string]bool{
	".git": true, ".svn": true, ".hg": true, ".jj": true,
	"node_modules": true, "vendor": true, ".venv": true,
	"__pycache__": true, ".mypy_cache": true, ".pytest_cache": true,
}

// skipWalkDir reports whether a directory should be pruned from a recursive walk
// rooted at root. The root itself is never pruned, so explicitly targeting a
// vendor dir still works.
func skipWalkDir(root, path, name string) bool {
	if path == root {
		return false
	}
	return vendorDirs[name] || isProtectedDir(absClean(path))
}
