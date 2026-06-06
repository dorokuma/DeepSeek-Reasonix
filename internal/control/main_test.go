package control

import (
	"os"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("REASONIX_CTX", "off")
	goleak.VerifyTestMain(m)
}
