package acp

import (
	"context"
	"testing"
	"time"

	"reasonix/internal/control"
)

type snapshotLockProbeController struct {
	*control.Controller
	onSnapshot func()
}

func (c *snapshotLockProbeController) Snapshot() error {
	if c.onSnapshot != nil {
		c.onSnapshot()
	}
	return nil
}

func expectACPSessionMutexAvailableDuringSnapshot(t *testing.T, sess *acpSession, checks chan<- struct{}) func() {
	t.Helper()
	return func() {
		acquired := make(chan struct{})
		go func() {
			sess.mu.Lock()
			sess.mu.Unlock()
			close(acquired)
		}()
		select {
		case <-acquired:
		case <-time.After(500 * time.Millisecond):
			t.Error("Snapshot ran while holding ACP session mutex")
		}
		if checks == nil {
			return
		}
		select {
		case checks <- struct{}{}:
		default:
		}
	}
}

func TestACPPersistAfterTurnSnapshotsWithoutSessionLock(t *testing.T) {
	sess := &acpSession{id: "sess-lock"}
	checks := make(chan struct{}, 1)
	sess.ctrl = &snapshotLockProbeController{
		Controller: control.New(control.Options{}),
		onSnapshot: expectACPSessionMutexAvailableDuringSnapshot(t, sess, checks),
	}

	sess.persistAfterTurn("hello from acp")

	select {
	case <-checks:
	case <-time.After(time.Second):
		t.Fatal("session was not snapshotted after turn")
	}
	if sess.title == "" {
		t.Fatal("session title was not updated after turn")
	}
}

func TestACPRebuildSessionSnapshotsWithoutSessionLock(t *testing.T) {
	sink := newUpdateSink(&fakeNotifier{}, "sess-lock")
	sess := &acpSession{
		id:    "sess-lock",
		sink:  sink,
		cwd:   t.TempDir(),
		model: "fast",
	}
	checks := make(chan struct{}, 1)
	oldCtrl := &snapshotLockProbeController{
		Controller: control.New(control.Options{}),
		onSnapshot: expectACPSessionMutexAvailableDuringSnapshot(t, sess, checks),
	}
	sess.ctrl = oldCtrl
	svc := &service{
		factory:  &configurableFactory{},
		sessions: map[string]*acpSession{sess.id: sess},
	}

	if err := svc.rebuildSession(context.Background(), sess, SessionConfigState{Model: "pro"}); err != nil {
		t.Fatalf("rebuildSession: %v", err)
	}
	select {
	case <-checks:
	case <-time.After(time.Second):
		t.Fatal("session was not snapshotted before rebuild")
	}
	if sess.ctrl == oldCtrl {
		t.Fatal("session controller was not replaced")
	}
	if sess.model != "pro" {
		t.Fatalf("session model = %q, want pro", sess.model)
	}
}

type blockingConfigFactory struct {
	configurableFactory
	started      chan string
	releaseFirst chan struct{}
}

func (f *blockingConfigFactory) NewSession(ctx context.Context, p SessionParams) (*control.Controller, error) {
	select {
	case f.started <- p.Model:
	default:
	}
	f.mu.Lock()
	buildNumber := len(f.builds) + 1
	f.mu.Unlock()
	if buildNumber == 1 {
		select {
		case <-f.releaseFirst:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.configurableFactory.NewSession(ctx, p)
}

func TestACPRebuildSessionAppliesPendingConfigAfterMaintenance(t *testing.T) {
	sink := newUpdateSink(&fakeNotifier{}, "sess-lock")
	sess := &acpSession{
		id:    "sess-lock",
		sink:  sink,
		cwd:   t.TempDir(),
		model: "fast",
		ctrl:  control.New(control.Options{}),
	}
	factory := &blockingConfigFactory{
		started:      make(chan string, 2),
		releaseFirst: make(chan struct{}),
	}
	svc := &service{
		factory:  factory,
		sessions: map[string]*acpSession{sess.id: sess},
	}

	errs := make(chan error, 1)
	go func() {
		errs <- svc.rebuildSession(context.Background(), sess, SessionConfigState{Model: "pro"})
	}()
	select {
	case got := <-factory.started:
		if got != "pro" {
			t.Fatalf("first rebuild model = %q, want pro", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first rebuild did not start")
	}

	if err := svc.rebuildSession(context.Background(), sess, SessionConfigState{Model: "fast"}); err != nil {
		t.Fatalf("queue pending rebuild: %v", err)
	}
	close(factory.releaseFirst)
	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("first rebuild: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first rebuild did not finish")
	}
	if sess.model != "fast" {
		t.Fatalf("session model = %q, want pending fast", sess.model)
	}
	if got := factory.buildCount(); got != 2 {
		t.Fatalf("factory builds = %d, want 2", got)
	}
}

// TestACPCtrlReadPathsDoNotRaceWithRebuild drives the lock-free read surfaces
// that used to read sess.ctrl outside sess.mu — info(), service.sessionDir(),
// sendAvailableCommands, and resolveSlashPrompt — while a rebuild goroutine
// keeps swapping the controller. Under -race this fails without currentCtrl().
func TestACPCtrlReadPathsDoNotRaceWithRebuild(t *testing.T) {
	sink := newUpdateSink(&fakeNotifier{}, "sess-race")
	sess := &acpSession{
		id:    "sess-race",
		sink:  sink,
		cwd:   t.TempDir(),
		model: "fast",
		ctrl:  control.New(control.Options{}),
	}
	factory := &configurableFactory{}
	svc := &service{
		factory:  factory,
		sessions: map[string]*acpSession{sess.id: sess},
	}

	const rebuilds = 50
	models := [...]string{"pro", "fast"}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < rebuilds; i++ {
			if err := svc.rebuildSession(context.Background(), sess, SessionConfigState{Model: models[i%len(models)]}); err != nil {
				t.Errorf("rebuildSession %d: %v", i, err)
				return
			}
		}
	}()

	for rebuilding := true; rebuilding; {
		select {
		case <-done:
			rebuilding = false
		default:
		}
		if got := sess.info().SessionID; got != sess.id {
			t.Fatalf("info().SessionID = %q, want %q", got, sess.id)
		}
		_ = svc.sessionDir()
		svc.sendAvailableCommands(sess)
		if got := svc.resolveSlashPrompt(context.Background(), sess, "/no-such-command args"); got != "/no-such-command args" {
			t.Fatalf("resolveSlashPrompt rewrote unknown command to %q", got)
		}
	}

	if sess.currentCtrl() == nil {
		t.Fatal("session controller is nil after rebuilds")
	}
	if got := factory.buildCount(); got != rebuilds {
		t.Fatalf("factory builds = %d, want %d", got, rebuilds)
	}
}
