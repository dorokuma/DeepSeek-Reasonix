package rtk

import "testing"

func TestLogFunctionsCompile(t *testing.T) {
	// Smoke test: ensure logging functions don't panic.
	LogFail("test", "cmd", nil)
	LogMissBuiltin("test", "cmd", "testing")
	LogMissBash("cmd", "testing")
	LogMissPipe("tool", "filter", 100, "testing")
}
