package diag

import (
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"
)

var (
	enabled bool
	mu      sync.Mutex
	f       *os.File
)

func Init() {
	var err error
	fname := fmt.Sprintf("/tmp/reasonix-diag-%d.log", os.Getpid())
	f, err = os.Create(fname)
	if err != nil {
		return
	}
	enabled = true
	fmt.Fprintf(os.Stderr, "[diag] logging to %s\n", fname)
}

func Close() {
	mu.Lock()
	defer mu.Unlock()
	if f != nil {
		f.Close()
		f = nil
	}
}

// LogHex records a hex dump of text from a named source point.
func LogHex(source, text string) {
	if !enabled {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	b := []byte(text)
	preview := ""
	if len(b) <= 200 {
		preview = hex.EncodeToString(b)
	} else {
		preview = hex.EncodeToString(b[:120]) + "..." + hex.EncodeToString(b[len(b)-40:])
	}
	fmt.Fprintf(f, "%s [%s] len=%d hex=%s\n", time.Now().Format("15:04:05.000000"), source, len(b), preview)
}

// LogFull records the full text without truncation, for large payloads like SSE JSON.
func LogFull(source, text string) {
	if !enabled {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	b := []byte(text)
	fmt.Fprintf(f, "%s [%s] len=%d hex=%s\n", time.Now().Format("15:04:05.000000"), source, len(b), hex.EncodeToString(b))
}
