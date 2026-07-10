package agent

import (
	"os"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// Most agent tests don't exercise ctxmode; disabling avoids leaking sqlite
	// connectionOpener goroutines from per-agent session journals.
	// TestMain has no *testing.T, so save/restore instead of t.Setenv.
	prev, had := os.LookupEnv("REASONIX_CTX")
	_ = os.Setenv("REASONIX_CTX", "off")
	goleak.VerifyTestMain(m)
	if had {
		_ = os.Setenv("REASONIX_CTX", prev)
	} else {
		_ = os.Unsetenv("REASONIX_CTX")
	}
}
