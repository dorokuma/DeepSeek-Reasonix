package jobs

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/event"
)

func TestCompletedJobPersistsOutputAndReleasesMemory(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	m := NewManager(event.Discard)
	defer m.Close()
	m.SetActiveSessionPath("session", sessionPath)

	j := m.StartForSession("session", "bash", "persist", func(_ context.Context, out io.Writer) (string, error) {
		_, _ = io.WriteString(out, strings.Repeat("x", defaultTailBytes+1024))
		return "", nil
	})
	<-j.done

	j.mu.Lock()
	tailLen := len(j.tail)
	result := j.result
	artifactPath := j.artifactPath
	j.mu.Unlock()

	if tailLen != 0 {
		t.Fatalf("completed artifact-backed job kept %d tail bytes, want 0", tailLen)
	}
	if result != "" {
		t.Fatalf("completed artifact-backed job kept result %q, want empty", result)
	}
	if artifactPath == "" {
		t.Fatal("artifact path should be set")
	}

	res := m.WaitForSession(context.Background(), "session", []string{j.ID}, 1)
	if len(res) != 1 || len(res[0].Output) != defaultTailBytes+1024 {
		t.Fatalf("wait output len = %d, want %d", len(res[0].Output), defaultTailBytes+1024)
	}
}

func TestRestoreSessionArtifactsAndAdvanceSequence(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	first := NewManager(event.Discard)
	first.SetActiveSessionPath("session", sessionPath)
	j := first.StartForSession("session", "task", "answer", func(context.Context, io.Writer) (string, error) {
		return "persisted answer", nil
	})
	<-j.done
	first.Close()

	second := NewManager(event.Discard)
	defer second.Close()
	second.SetActiveSessionPath("session", sessionPath)

	res := second.WaitForSession(context.Background(), "session", []string{j.ID}, 1)
	if len(res) != 1 || !strings.Contains(res[0].Output, "persisted answer") {
		t.Fatalf("restored wait = %+v, want persisted answer", res)
	}
	if got := second.WaitForSession(context.Background(), "session", nil, 1); len(got) != 0 {
		t.Fatalf("wait without ids should ignore restored completed artifacts, got %+v", got)
	}

	next := second.StartForSession("session", "bash", "next", func(context.Context, io.Writer) (string, error) {
		return "", nil
	})
	<-next.done
	if next.ID == j.ID {
		t.Fatalf("new job reused restored id %q", next.ID)
	}
}

func TestFinishDestroySessionPurgesOwnedJobs(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	j := m.StartForSession("session", "task", "done", func(context.Context, io.Writer) (string, error) {
		return "answer", nil
	})
	<-j.done

	done := m.DestroySession("session")
	if len(done) != 0 {
		t.Fatalf("finished job should not need destroy wait, got %d handles", len(done))
	}
	m.FinishDestroySession("session")

	if _, _, ok := m.OutputForSession("session", j.ID); ok {
		t.Fatalf("destroyed session job %s should be purged", j.ID)
	}
}
