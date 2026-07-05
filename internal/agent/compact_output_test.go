package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/ctxmode"
)

func TestCompactToolOutput_underCap(t *testing.T) {
	in := "small"
	out, notice := compactToolOutput(nil, "bash", nil, in)
	if out != in || notice != "" {
		t.Fatalf("under cap unchanged: out=%q notice=%q", out, notice)
	}
}

func TestCompactToolOutput_overCapTruncated(t *testing.T) {
	in := strings.Repeat("x", maxToolOutputBytes+100)
	out, notice := compactToolOutput(nil, "bash", nil, in)
	// truncation adds a message; allow a small margin over the cap
	if len(out) > maxToolOutputBytes+200 {
		t.Fatalf("output far over cap: %d", len(out))
	}
	if notice != "" {
		t.Fatalf("truncation notice should be empty, got %q", notice)
	}
	if !strings.Contains(out, "[truncated ") {
		t.Fatal("truncated output should contain truncation marker")
	}
}

func TestCompactToolOutput_readFileSandboxed(t *testing.T) {
	t.Setenv("REASONIX_CTX", "on")
	t.Setenv("REASONIX_CTX_THRESHOLD", "512")
	store := ctxmode.NewStore()
	defer store.Remove()
	body := strings.Repeat("content line\n", 400)
	args := json.RawMessage(`{"path":"big.txt"}`)
	out, notice := compactToolOutput(store, "read_file", args, body)
	// We no longer emit a user-facing notice for ctx sandbox store.
	if notice != "" {
		t.Fatalf("want no user notice for ctx sandbox, got %q", notice)
	}
	if strings.Contains(out, strings.Repeat("content line\n", 100)) {
		t.Fatal("full read_file body should not reach model context")
	}
	if !strings.Contains(out, "ctx_read") {
		t.Fatalf("want ctx_read hint, got %q", out)
	}
}

func TestCompactToolOutput_grepCtx(t *testing.T) {
	t.Setenv("REASONIX_CTX", "on")
	t.Setenv("REASONIX_CTX_THRESHOLD", "512")
	store := ctxmode.NewStore()
	defer store.Remove()
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("/tmp/f.go:" + itoa(i) + ":match line " + itoa(i) + " content here\n")
	}
	body := b.String()
	args := json.RawMessage(`{"pattern":"match"}`)
	out, notice := compactToolOutput(store, "grep", args, body)
	// ctx sandbox should not produce a user-facing notice.
	if strings.Contains(notice, "ctxmode") || strings.Contains(notice, "sandboxed via ctxmode") {
		t.Fatalf("do not want ctx sandbox notice in user-facing notice, got %q", notice)
	}
	if !strings.Contains(out, "ref=ctx-") {
		t.Fatalf("want ctx ref in summary, got %q", out)
	}
}

// itoa is a simple int-to-string helper to avoid importing fmt in tests.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for n := i; n > 0; n /= 10 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
	}
	return string(digits)
}
