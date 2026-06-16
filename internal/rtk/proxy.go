package rtk

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"reasonix/internal/envutil"
	"reasonix/internal/shell"
)

// ErrNotRewritten means rtk rewrite declined this command — callers must use
// the native tool path and must not invoke RTK directly.
var ErrNotRewritten = errors.New("rtk: command not rewritten")

// ShellQuote wraps s for a POSIX sh -c argument.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// RipgrepShell builds the ripgrep invocation Reasonix grep uses before rewrite.
func RipgrepShell(pattern, path string) string {
	if path == "" {
		path = "."
	}
	return fmt.Sprintf("rg --no-heading --line-number --with-filename --color never --regexp %s -- %s",
		ShellQuote(pattern), ShellQuote(path))
}

// LsShell builds a plain ls invocation for rewrite probing.
func LsShell(path string) string {
	if path == "" {
		path = "."
	}
	return "ls " + ShellQuote(path)
}

// TreeShell builds a tree invocation for recursive directory listing.
func TreeShell(path string) string {
	if path == "" {
		path = "."
	}
	return "tree -L 4 " + ShellQuote(path)
}

// FindNameShell builds find -name for glob patterns RTK supports.
func FindNameShell(dir, namePattern string) (string, bool) {
	namePattern = strings.TrimSpace(namePattern)
	if namePattern == "" {
		return "", false
	}
	if dir == "" {
		dir = "."
	}
	// find -name only handles filename globs, not full ** paths.
	if strings.Contains(namePattern, "/") || strings.Contains(namePattern, "\\") {
		return "", false
	}
	return fmt.Sprintf("find %s -name %s", ShellQuote(dir), ShellQuote(namePattern)), true
}

// RunShellIfRewritten asks rtk rewrite for cmd; only runs the rewritten shell
// when rewrite accepts it. surface names the builtin (grep, ls) for miss logs.
func RunShellIfRewritten(ctx context.Context, workDir, cmd, surface string) (string, error) {
	if !Active() {
		return "", ErrNotRewritten
	}
	rewritten := Rewrite(strings.TrimSpace(cmd))
	if rewritten == "" {
		if surface != "" {
			LogMissBuiltin(surface, cmd, "rewrite_declined")
		}
		return "", ErrNotRewritten
	}
	return execShell(ctx, workDir, rewritten, surface)
}

func execShell(ctx context.Context, workDir, cmd, surface string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := rewriteTimeout() * 4
	if timeout < 10*time.Second {
		timeout = 10 * time.Second
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Use the same shell resolution as the bash tool, not hardcoded /bin/sh.
	sh := shell.ResolveShell()
	argv := sh.Argv(cmd)
	c := exec.CommandContext(tctx, argv[0], argv[1:]...)
	if workDir != "" {
		c.Dir = workDir
	}
	// Strip credential env vars so RTK subprocesses don't inherit API keys.
	c.Env = envutil.StripCredentialEnv(os.Environ())
	out, err := c.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		// ripgrep/rtk grep: exit 1 with no stdout means "no matches", not failure.
		if surface == "grep" && exitCode == 1 && text == "" {
			return "(no matches)", nil
		}
		if text != "" {
			return "", fmt.Errorf("%w: %s", err, text)
		}
		return "", err
	}
	return text, nil
}