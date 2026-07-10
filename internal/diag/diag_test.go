package diag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCloseAndLog(t *testing.T) {
	// Init may write under /tmp; exercise the locked path and log helpers.
	Init()
	LogHex("test", "hello")
	LogFull("test-full", "payload")
	Close()

	// Second Close is safe.
	Close()

	// Re-init after close.
	Init()
	defer Close()
	LogHex("again", strings.Repeat("x", 300))
}

func TestLogWhenDisabled(t *testing.T) {
	// Ensure Log* no-ops cleanly when never Init'd (or after Close).
	Close()
	enabled = false
	f = nil
	LogHex("x", "y")
	LogFull("x", "y")
}

func TestInitIdempotent(t *testing.T) {
	Init()
	defer Close()
	first := f
	Init()
	if f != first {
		t.Fatal("second Init should keep the same file when already enabled")
	}
	_ = filepath.Base(os.TempDir())
}
