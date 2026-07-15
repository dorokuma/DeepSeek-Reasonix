package main

import (
	"context"
	"path/filepath"
	"testing"

	"reasonix/internal/repair"
)

// TestShutdownDoesNotBlessStartupBeforeReady pins the recovery contract that a
// clean exit before the window ever reached domReady keeps the incomplete
// startup record: quitting a build that boots but never paints must not reset
// the crash-loop counter (nor bless a probationary update), or repeated
// attempts would never reach the Guard recovery threshold and the rollback
// backups would be deleted under a broken release.
func TestShutdownDoesNotBlessStartupBeforeReady(t *testing.T) {
	isolateDesktopUserDirs(t)
	tracker := repair.NewStartupTracker(filepath.Join(t.TempDir(), "startup-state.json"))
	if _, err := tracker.Begin("test-version", false); err != nil {
		t.Fatal(err)
	}
	a := NewApp()
	a.startupTracker = tracker

	a.shutdown(context.Background())
	state, err := tracker.Read()
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != "starting" {
		t.Fatalf("pre-ready shutdown must keep the incomplete phase, got %q", state.Phase)
	}

	a.startupReady.Store(true)
	a.shutdown(context.Background())
	state, err = tracker.Read()
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != "clean-exit" {
		t.Fatalf("post-ready shutdown must mark clean-exit, got %q", state.Phase)
	}
}
