package capdiag

import (
	"strings"
	"testing"
)

func TestSanitizeErrTextRedactsSecretsAndPaths(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	in := "stdio plugin \"x\": command \"npx\" not found on PATH; PATH=\"" + home + "/bin:/usr/bin\" Bearer sk-secret-token " +
		ws + "/secret.env stderr: Authorization=Bearer abc.def"
	out := sanitizeErrTextWithPaths(in, ws, home)
	if strings.Contains(out, home) {
		t.Fatalf("home path leaked: %q", out)
	}
	if strings.Contains(out, "sk-secret") || strings.Contains(out, "abc.def") {
		t.Fatalf("token leaked: %q", out)
	}
	if strings.Contains(out, "PATH=\""+home) {
		t.Fatalf("PATH value leaked: %q", out)
	}
	if !strings.Contains(out, "<redacted>") && !strings.Contains(out, "Bearer <redacted>") {
		t.Fatalf("expected redaction markers in %q", out)
	}
}
