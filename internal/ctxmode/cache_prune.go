package ctxmode

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"reasonix/internal/config"
)

const aliveFile = ".pid"

var ctxDirName = regexp.MustCompile(`^[0-9a-f]{16}$`)

// PruneOrphanCache removes ctxmode session dirs whose owning process is gone.
// Dirs marked with a live .pid are kept. Returns the number of dirs removed.
func PruneOrphanCache() (int, error) {
	base, err := ctxCacheRoot()
	if err != nil || base == "" {
		return 0, err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, ent := range entries {
		if !ent.IsDir() || !ctxDirName.MatchString(ent.Name()) {
			continue
		}
		dir := filepath.Join(base, ent.Name())
		if cacheDirActive(dir) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return removed, fmt.Errorf("prune %s: %w", dir, err)
		}
		removed++
	}
	return removed, nil
}

// CountCacheDirs reports session store directories under the ctxmode cache root.
func CountCacheDirs() int {
	base, err := ctxCacheRoot()
	if err != nil || base == "" {
		return 0
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0
	}
	n := 0
	for _, ent := range entries {
		if ent.IsDir() && ctxDirName.MatchString(ent.Name()) {
			n++
		}
	}
	return n
}

func ctxCacheRoot() (string, error) {
	base := config.CacheDir()
	if base == "" {
		return "", nil
	}
	return filepath.Join(base, "ctxmode"), nil
}

func markCacheAlive(dir string) {
	if dir == "" {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, aliveFile), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
}

func cacheDirActive(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, aliveFile))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return false
	}
	return processAlive(pid)
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
