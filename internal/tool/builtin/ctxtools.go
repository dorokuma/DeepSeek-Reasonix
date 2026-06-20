package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/ctxmode"
	"reasonix/internal/tool"
)

func init() {
	tool.RegisterBuiltin(ctxRead{})
	tool.RegisterBuiltin(ctxSearch{})
	tool.RegisterBuiltin(ctxIndex{})
}

type ctxRead struct{}

func (ctxRead) Name() string { return "ctx_read" }

func (ctxRead) Description() string {
	return "Page through tool output previously compacted by ctxmode (read_file, grep, MCP, etc.). Use the ref from the [ctx] summary (e.g. ctx-1)."
}

func (ctxRead) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "ref":{"type":"string","description":"Sandbox ref from [ctx] summary (e.g. ctx-1)"},
  "offset":{"type":"integer","description":"0-based line offset (default 0)","minimum":0},
  "limit":{"type":"integer","description":"Max lines to return (default 80, max 200)","minimum":1,"maximum":200}
},
"required":["ref"]
}`)
}

func (ctxRead) ReadOnly() bool { return true }

func (ctxRead) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Ref    string `json:"ref"`
		Offset int    `json:"offset,omitempty"`
		Limit  int    `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	store, ok := ctxmode.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("context store is not available in this session")
	}
	return store.Read(p.Ref, p.Offset, p.Limit)
}

type ctxSearch struct{}

func (ctxSearch) Name() string { return "ctx_search" }

func (ctxSearch) Description() string {
	return "Search sandboxed tool output by substring. Use the ref from a prior [ctx] summary. If ref is omitted, searches all actively indexed files in the global session store."
}

func (ctxSearch) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "ref":{"type":"string","description":"Sandbox ref (e.g. ctx-1). Optional if searching global active index."},
  "pattern":{"type":"string","description":"Case-sensitive substring to find"},
  "limit":{"type":"integer","description":"Max matching lines (default 40, max 100)","minimum":1,"maximum":100}
},
"required":["pattern"]
}`)
}

func (ctxSearch) ReadOnly() bool { return true }

func (ctxSearch) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Ref     string `json:"ref"`
		Pattern string `json:"pattern"`
		Limit   int    `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	store, ok := ctxmode.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("context store is not available in this session")
	}
	if p.Ref == "" {
		if p.Limit <= 0 {
			p.Limit = 5
		}
		return store.SearchGlobal(p.Pattern, p.Limit)
	}
	return store.Search(p.Ref, p.Pattern, p.Limit)
}

type ctxIndex struct{ workDir string }

func (ctxIndex) Name() string { return "ctx_index" }

func (ctxIndex) Description() string {
	return "Index a text file or directory recursively into the session context store, so you can search and query snippets without feeding the entire large file to the model context window (skips dependency and VCS directories)."
}

func (ctxIndex) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"File or directory path to index"}
},
"required":["path"]
}`)
}

func (ctxIndex) ReadOnly() bool { return true }

func (idx ctxIndex) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	store, ok := ctxmode.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("context store is not available in this session")
	}

	target := resolveIn(idx.workDir, p.Path)
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", target, err)
	}

	indexedCount := 0
	if info.IsDir() {
		err = filepath.WalkDir(target, func(path string, d os.DirEntry, wErr error) error {
			if wErr != nil {
				return nil
			}
			if d.IsDir() {
				if skipWalkDir(target, path, d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if isProbablyBinary(d.Name()) {
				return nil
			}
			fInfo, err := d.Info()
			if err != nil || fInfo.Size() > 1*1024*1024 {
				return nil
			}
			rel, err := filepath.Rel(idx.workDir, path)
			if err != nil {
				rel = path
			}
			if err := store.IndexFile(rel, path); err == nil {
				indexedCount++
			}
			return nil
		})
	} else {
		if isProbablyBinary(info.Name()) || info.Size() > 1*1024*1024 {
			return "", fmt.Errorf("file %s is binary or too large (> 1MB)", target)
		}
		rel, err := filepath.Rel(idx.workDir, target)
		if err != nil {
			rel = target
		}
		err = store.IndexFile(rel, target)
		if err == nil {
			indexedCount = 1
		}
	}

	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully indexed %d file(s) into context store.", indexedCount), nil
}

func isProbablyBinary(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".webp", ".bmp",
		".pdf", ".zip", ".tar", ".gz", ".7z", ".rar", ".exe", ".dll", ".so", ".dylib",
		".bin", ".o", ".a", ".db", ".sqlite", ".class", ".jar", ".war", ".mp3", ".mp4",
		".wav", ".flac", ".avi", ".mkv", ".mov", ".woff", ".woff2", ".ttf", ".eot":
		return true
	}
	return false
}