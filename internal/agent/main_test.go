package agent

import (
	"os"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// Most agent tests don't exercise ctxmode; disabling avoids leaking sqlite
	// connectionOpener goroutines from per-agent session journals.
	_ = os.Setenv("REASONIX_CTX", "off")
	goleak.VerifyTestMain(m)
}
