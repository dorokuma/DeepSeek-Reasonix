package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/event"
)

// TestSessionLeaseHelpersConcurrentAccess hammers the sessionLeaseMu helpers
// (ensure/take/adopt/release/key) from concurrent goroutines. Run with -race:
// any residual raw access to tab.sessionLease shows up as a data race, and the
// final acquire asserts no lease leaked through an interleaving.
func TestSessionLeaseHelpersConcurrentAccess(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	path := filepath.Join(dir, "lease-helper-hammer.jsonl")
	key := sessionRuntimeKey(path)
	tabA := &WorkspaceTab{ID: "a"}
	tabB := &WorkspaceTab{ID: "b"}

	const iterations = 300
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = tabA.ensureSessionLease(path)
			_ = tabA.sessionLeaseRuntimeKey()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			// The applyRuntimeTab transfer shape: move A's lease to B and back.
			tabB.adoptSessionLease(tabA.takeSessionLease())
			tabA.adoptSessionLease(tabB.takeSessionLease())
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			tabA.releaseSessionLease()
		}
	}()
	wg.Wait()

	tabA.releaseSessionLease()
	tabB.releaseSessionLease()
	lease, err := agent.TryAcquireSessionLease(key)
	if err != nil {
		t.Fatalf("lease leaked through concurrent helper interleavings: %v", err)
	}
	lease.Release()
}

// TestDetachRuntimeForReplacementTransfersLease asserts the detach clone takes
// lease ownership through the locked helpers and the visible tab keeps none.
func TestDetachRuntimeForReplacementTransfersLease(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	path := filepath.Join(dir, "detach-transfer.jsonl")
	key := sessionRuntimeKey(path)
	tab := &WorkspaceTab{ID: "tab", Scope: "global", SessionPath: path}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}}
	if err := tab.ensureSessionLease(path); err != nil {
		t.Fatalf("ensureSessionLease: %v", err)
	}
	t.Cleanup(func() {
		tab.releaseSessionLease()
		for _, d := range app.detachedSessions {
			d.releaseSessionLease()
		}
	})

	if !app.detachRuntimeForReplacement(tab) {
		t.Fatal("detachRuntimeForReplacement failed for a live tab")
	}
	detached := app.detachedSessions[key]
	if detached == nil {
		t.Fatal("detached runtime was not registered")
	}
	if got := detached.sessionLeaseRuntimeKey(); got != key {
		t.Fatalf("detached clone lease key = %q, want %q", got, key)
	}
	if got := tab.sessionLeaseRuntimeKey(); got != "" {
		t.Fatalf("visible tab still holds lease key %q after detach", got)
	}
}

// TestDetachRuntimeForReplacementSkipsRemovedTab: a tab that DeleteSession /
// CloseTab already unlinked must not be re-published into detachedSessions
// (the "session resurrects" class, #4384).
func TestDetachRuntimeForReplacementSkipsRemovedTab(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	path := filepath.Join(dir, "detach-removed.jsonl")

	removed := &WorkspaceTab{ID: "removed", SessionPath: path, removed: true}
	app := &App{tabs: map[string]*WorkspaceTab{"removed": removed}}
	if app.detachRuntimeForReplacement(removed) {
		t.Fatal("removed tab was detached into the background registry")
	}
	if len(app.detachedSessions) != 0 {
		t.Fatal("removed tab left an entry in detachedSessions")
	}

	orphan := &WorkspaceTab{ID: "orphan", SessionPath: path}
	if app.detachRuntimeForReplacement(orphan) {
		t.Fatal("tab absent from a.tabs was detached into the background registry")
	}
	if len(app.detachedSessions) != 0 {
		t.Fatal("orphan tab left an entry in detachedSessions")
	}
}

// TestTabEventSinkEmitConcurrentRebind: Emit keeps running on the controller
// goroutine while detach/reattach rebinds the sink's tab routing. Run with
// -race; before setBinding existed the tabID write raced every Emit.
func TestTabEventSinkEmitConcurrentRebind(t *testing.T) {
	sink := &tabEventSink{tabID: "before"}
	const iterations = 500
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			sink.Emit(event.Event{Kind: event.Notice, Text: "hammer"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			sink.setBinding(fmt.Sprintf("tab-%d", i%2), nil)
			sink.clearContext()
		}
	}()
	wg.Wait()
	if tabID, _ := sink.binding(); tabID == "" {
		t.Fatal("sink lost its tab binding")
	}
}

// TestTabBuildSupersededByRebindGeneration: a session rebind bumps
// buildGeneration to strand any in-flight async build, so its swap (and every
// mid-build field write) is rejected; synchronous rebuilds pass generation 0
// and rely on runtimeRebuildMu instead.
func TestTabBuildSupersededByRebindGeneration(t *testing.T) {
	tab := &WorkspaceTab{ID: "tab"}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}}
	app.mu.Lock()
	tab.buildGeneration = 3
	app.mu.Unlock()
	if app.tabBuildSuperseded(tab, 3) {
		t.Fatal("build with the current generation must not be superseded")
	}
	app.mu.Lock()
	tab.buildGeneration++ // the rebind-side invalidation
	app.mu.Unlock()
	if !app.tabBuildSuperseded(tab, 3) {
		t.Fatal("stale-generation build must be superseded after a rebind bump")
	}
	if app.tabBuildSuperseded(tab, 0) {
		t.Fatal("synchronous rebuild (generation 0) must not be superseded by generation bumps")
	}
	app.mu.Lock()
	tab.removed = true
	app.mu.Unlock()
	if !app.tabBuildSuperseded(tab, 0) {
		t.Fatal("removed tab must supersede every build, including synchronous ones")
	}
}

// TestMetaForTabConcurrentWithBuildSwap polls MetaForTab (the frontend's boot
// probe) while a fake build goroutine flips Ready/Label/StartupErr/model under
// a.mu — the write pattern of buildTabControllerWithContext. Run with -race.
func TestMetaForTabConcurrentWithBuildSwap(t *testing.T) {
	isolateDesktopUserDirs(t)
	tab := &WorkspaceTab{ID: "tab", Scope: "project", WorkspaceRoot: t.TempDir()}
	app := &App{
		tabs:        map[string]*WorkspaceTab{"tab": tab},
		tabOrder:    []string{"tab"},
		activeTabID: "tab",
	}

	const iterations = 100
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			app.mu.Lock()
			tab.Ready = !tab.Ready
			tab.Label = fmt.Sprintf("model-%d", i)
			tab.StartupErr = ""
			tab.model = fmt.Sprintf("provider/m%d", i)
			tab.goal = fmt.Sprintf("goal-%d", i)
			tab.tokenMode = "full"
			app.mu.Unlock()
		}
	}()
	for i := 0; i < iterations; i++ {
		meta := app.MetaForTab("tab")
		if meta.EventChannel == "" {
			t.Fatal("MetaForTab returned zero meta for a live tab")
		}
	}
	<-done
}
