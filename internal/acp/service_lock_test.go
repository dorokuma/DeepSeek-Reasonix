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
