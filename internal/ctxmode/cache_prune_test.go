package ctxmode

import (
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/config"
)

func TestPruneOrphanCache_removesDead(t *testing.T) {
	base := t.TempDir()
	t.Setenv("REASONIX_CACHE_DIR", base)
	t.Setenv("XDG_CACHE_HOME", base)

	dead := filepath.Join(base, "ctxmode", "deadbeefdeadbeef")
	if err := os.MkdirAll(dead, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dead, aliveFile), []byte("999999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// NewStore marks itself alive and prunes dead siblings opportunistically.
	liveStore := NewStore()
	defer liveStore.Remove()
	if liveStore.dir == "" {
		t.Fatal("want on-disk store dir")
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatal("dead dir should be pruned on NewStore")
	}
	if _, err := os.Stat(liveStore.dir); err != nil {
		t.Fatalf("live dir should remain: %v", err)
	}
}

func TestCountCacheDirs(t *testing.T) {
	base := t.TempDir()
	t.Setenv("REASONIX_CACHE_DIR", base)
	store := NewStore()
	defer store.Remove()
	if n := CountCacheDirs(); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
	_ = config.CacheDir()
}
