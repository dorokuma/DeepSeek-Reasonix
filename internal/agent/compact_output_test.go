package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"reasonix/internal/ctxmode"
	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/rtk"
)

func TestPipeFilterHint_skipsMCP(t *testing.T) {
	_, ok := pipeFilterHint("mcp__fs__read", nil, nil)
	if ok {
		t.Fatal("MCP tools must not get pipe filters")
	}
}

func TestPipeFilterHint_grep(t *testing.T) {
	f, ok := pipeFilterHint("grep", json.RawMessage(`{"pattern":"foo"}`), nil)
	if !ok || f != "grep" {
		t.Fatalf("grep hint = (%q, %v)", f, ok)
	}
}

func TestCompactToolOutput_underCap(t *testing.T) {
	in := "small"
	out, notice := compactToolOutput(nil, "bash", nil, nil, in)
	if out != in || notice != "" {
		t.Fatalf("under cap unchanged: out=%q notice=%q", out, notice)
	}
}

func TestCompactToolOutput_bashGitLog(t *testing.T) {
	if !rtk.Available() {
		t.Skip("rtk not on PATH")
	}
	t.Setenv("REASONIX_RTK", "rewrite")
	var b strings.Builder
	for i := 0; i < 400; i++ {
		b.WriteString("commit abc\nAuthor: x\nDate: 2024\n\n    msg\n\n")
	}
	in := b.String()
	if len(in) <= maxToolOutputBytes {
		in = strings.Repeat(in, 2)
	}
	args := json.RawMessage(`{"command":"git log -400"}`)
	out, notice := compactToolOutput(nil, "bash", args, nil, in)
	if len(out) > maxToolOutputBytes {
		t.Fatalf("output still over cap: %d", len(out))
	}
	if notice == "" || !strings.Contains(notice, "rtk pipe") {
		t.Fatalf("want pipe notice, got %q", notice)
	}
}

func TestCompactToolOutput_bashOutputUsesJobLabel(t *testing.T) {
	if !rtk.Available() {
		t.Skip("rtk not on PATH")
	}
	t.Setenv("REASONIX_RTK", "rewrite")
	jm := jobs.NewManager(event.Discard)
	j := jm.Start("bash", "git log -400", func(_ context.Context, _ io.Writer) (string, error) {
		return "", nil
	})
	var b strings.Builder
	for i := 0; i < 400; i++ {
		b.WriteString("commit abc\nAuthor: x\nDate: 2024\n\n    msg\n\n")
	}
	in := b.String()
	if len(in) <= maxToolOutputBytes {
		in = strings.Repeat(in, 2)
	}
	args, _ := json.Marshal(map[string]string{"job_id": j.ID})
	out, notice := compactToolOutput(nil, "bash_output", args, jm, in)
	if len(out) > maxToolOutputBytes {
		t.Fatalf("output still over cap: %d", len(out))
	}
	if notice == "" || !strings.Contains(notice, "git-log") {
		t.Fatalf("want git-log pipe notice, got %q", notice)
	}
}

func TestCompactToolOutput_readFileSandboxed(t *testing.T) {
	t.Setenv("REASONIX_CTX", "on")
	t.Setenv("REASONIX_CTX_THRESHOLD", "512")
	store := ctxmode.NewStore()
	defer store.Remove()
	body := strings.Repeat("content line\n", 400)
	args := json.RawMessage(`{"path":"big.txt"}`)
	out, notice := compactToolOutput(store, "read_file", args, nil, body)
	if notice == "" || !strings.Contains(notice, "ctxmode") {
		t.Fatalf("want sandbox notice, got %q", notice)
	}
	if strings.Contains(out, strings.Repeat("content line\n", 100)) {
		t.Fatal("full read_file body should not reach model context")
	}
	if !strings.Contains(out, "ctx_read") {
		t.Fatalf("want ctx_read hint, got %q", out)
	}
}

func TestCompactToolOutput_grepCtxAndPipe(t *testing.T) {
	if !rtk.Available() {
		t.Skip("rtk not on PATH")
	}
	t.Setenv("REASONIX_RTK", "rewrite")
	t.Setenv("REASONIX_CTX", "on")
	t.Setenv("REASONIX_CTX_THRESHOLD", "512")
	store := ctxmode.NewStore()
	defer store.Remove()
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "/tmp/f.go:%d:match line %d content here\n", i, i)
	}
	body := b.String()
	args := json.RawMessage(`{"pattern":"match"}`)
	out, notice := compactToolOutput(store, "grep", args, nil, body)
	if !strings.Contains(notice, "ctxmode") {
		t.Fatalf("want ctx notice, got %q", notice)
	}
	if !strings.Contains(out, "ref=ctx-") {
		t.Fatalf("want ctx ref in summary, got %q", out)
	}
	if strings.Contains(out, "/tmp/f.go:199:") && !strings.Contains(out, "RTK compact") {
		t.Fatalf("raw tail should not appear without compact section when pipe shrinks, got %q", out)
	}
}