package builtin

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestCtxRun_javascript(t *testing.T) {
	t.Setenv("REASONIX_CTX", "on")
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	r := ctxRun{workDir: t.TempDir()}
	out, err := r.Execute(context.Background(), json.RawMessage(`{
		"language":"javascript",
		"code":"console.log('ok', 2+2);"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ok 4") {
		t.Fatalf("out = %q", out)
	}
}

func TestCtxRun_requiresCtx(t *testing.T) {
	t.Setenv("REASONIX_CTX", "off")
	r := ctxRun{workDir: t.TempDir()}
	_, err := r.Execute(context.Background(), json.RawMessage(`{"language":"bash","code":"echo hi"}`))
	if err == nil || !strings.Contains(err.Error(), "REASONIX_CTX") {
		t.Fatalf("err = %v", err)
	}
}