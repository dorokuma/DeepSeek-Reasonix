package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/ctxmode"
	"reasonix/internal/sandbox"
	"reasonix/internal/tool"
)

const (
	ctxRunTimeout   = 60 * time.Second
	ctxRunMaxOutput = 16 * 1024
)

func init() { tool.RegisterBuiltin(ctxRun{}) }

// ctxRun executes a short script and returns stdout only — the Think-in-Code path.
// Large stdout can still be sandboxed by ctxmode on the way into model context.
type ctxRun struct {
	sb      sandbox.Spec
	workDir string
}

func (ctxRun) Name() string { return "ctx_run" }

func (ctxRun) Description() string {
	return "Run a short script (javascript/python/bash) and return stdout only. Use for aggregations and scans instead of reading many files into context. Requires REASONIX_CTX=on."
}

func (ctxRun) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "language":{"type":"string","enum":["javascript","python","bash"],"description":"Runtime to execute"},
  "code":{"type":"string","description":"Source code; use console.log/print for results"}
},
"required":["language","code"]
}`)
}

func (ctxRun) ReadOnly() bool { return false }

func (c ctxRun) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if !ctxmode.Active() {
		return "", fmt.Errorf("ctx_run requires REASONIX_CTX=on")
	}
	var p struct {
		Language string `json:"language"`
		Code     string `json:"code"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	p.Language = strings.ToLower(strings.TrimSpace(p.Language))
	p.Code = strings.TrimSpace(p.Code)
	if p.Language == "" {
		return "", fmt.Errorf("language is required")
	}
	if p.Code == "" {
		return "", fmt.Errorf("code is required")
	}

	scriptPath, shellCmd, cleanup, err := c.prepareScript(p.Language, p.Code)
	if err != nil {
		return "", err
	}
	if cleanup != nil {
		defer cleanup()
	}

	sh := sandbox.ResolveShell()
	argv, _ := sandbox.Command(c.sb, sh, shellCmd)
	runCtx, cancel := context.WithTimeout(ctx, ctxRunTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	if c.workDir != "" {
		cmd.Dir = c.workDir
	}
	cmd.Env = os.Environ()
	if scriptPath != "" {
		// Script already on disk; argv runs the shell command referencing it.
		_ = scriptPath
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if out := strings.TrimSpace(stdout.String()); out != "" {
			msg = out + "\n" + msg
		}
		return strings.TrimSpace(stdout.String()), fmt.Errorf("ctx_run failed: %s", msg)
	}
	out := stdout.String()
	if len(out) > ctxRunMaxOutput {
		out = out[:ctxRunMaxOutput] + fmt.Sprintf("\n…[ctx_run stdout truncated at %d bytes]…\n", ctxRunMaxOutput)
	}
	return out, nil
}

func (c ctxRun) prepareScript(lang, code string) (scriptPath, shellCmd string, cleanup func(), err error) {
	base := c.workDir
	if base == "" {
		base, err = os.Getwd()
		if err != nil {
			return "", "", nil, err
		}
	}
	dir := filepath.Join(base, ".reasonix", "ctx-run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", nil, err
	}
	var ext, run string
	switch lang {
	case "javascript", "js", "node":
		ext = ".mjs"
		run = "node"
	case "python", "py", "python3":
		ext = ".py"
		run = "python3"
	case "bash", "sh", "shell":
		ext = ".sh"
		run = "bash"
	default:
		return "", "", nil, fmt.Errorf("unsupported language %q (use javascript, python, or bash)", lang)
	}
	f, err := os.CreateTemp(dir, "run-*"+ext)
	if err != nil {
		return "", "", nil, err
	}
	path := f.Name()
	if _, err := f.WriteString(code); err != nil {
		f.Close()
		os.Remove(path)
		return "", "", nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(path)
		return "", "", nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", "", nil, err
	}
	cleanup = func() { _ = os.Remove(path) }
	// Quote path for shell — spaces rare in temp names but safe regardless.
	q := "'" + strings.ReplaceAll(path, "'", `'"'"'`) + "'"
	return path, run + " " + q, cleanup, nil
}