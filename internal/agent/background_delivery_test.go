package agent

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/provider"
)

func TestCommitBackgroundJobResultWithoutRegisterJobMeta(t *testing.T) {
	sink := event.Discard
	jm := jobs.NewManager(sink)
	defer jm.Close()

	job, err := jm.Start(context.Background(), "task", "x", func(_ context.Context, _ io.Writer) (string, error) {
		return "ANSWER-7", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sess := agentNewSessionWithStartedTask("call-7", job.ID)
	// Simulate main-agent chatter after the Started stub so the patch sits mid-history.
	sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "I'll wait for the background task."})
	ag := New(nil, nil, sess, Options{Jobs: jm}, sink)
	ag.SetControllerBridge(newSubControllerBridge())

	deadline := time.After(2 * time.Second)
	for {
		if n, ok := jm.CompletedResult(job.ID); ok && n.Output == "ANSWER-7" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("job did not complete")
		case <-time.After(5 * time.Millisecond):
		}
	}

	if !ag.CompleteBackgroundJob(job.ID) {
		t.Fatal("CompleteBackgroundJob should commit via Started line fallback")
	}
	// Mid-history tool row must be patched (API tool-call pairing).
	if sess.Messages[1].Content != "ANSWER-7" {
		t.Fatalf("started row should become answer, got %q", sess.Messages[1].Content)
	}
	// Tail envelope is mandatory — pure mid-patch is the historical regression.
	last := sess.Messages[len(sess.Messages)-1]
	if last.Role != provider.RoleUser || !strings.Contains(last.Content, "background-task-result") {
		t.Fatalf("want tail user envelope, got role=%s content=%q", last.Role, last.Content)
	}
	if !strings.Contains(last.Content, "ANSWER-7") {
		t.Fatalf("tail envelope missing answer: %q", last.Content)
	}
	// Orphan RoleTool at tail would be dropped by SanitizeToolPairing — ensure we didn't add one.
	for i, m := range sess.Messages {
		if i <= 1 {
			continue
		}
		if m.Role == provider.RoleTool && m.Content == "ANSWER-7" {
			t.Fatalf("orphan tail tool message at index %d would be dropped by SanitizeToolPairing", i)
		}
	}
}

// TestCommitBackgroundJobResultIdempotent rejects double envelopes under races.
func TestCommitBackgroundJobResultIdempotent(t *testing.T) {
	sink := event.Discard
	jm := jobs.NewManager(sink)
	defer jm.Close()

	job, err := jm.Start(context.Background(), "task", "x", func(_ context.Context, _ io.Writer) (string, error) {
		return "ONCE", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sess := agentNewSessionWithStartedTask("call-once", job.ID)
	ag := New(nil, nil, sess, Options{Jobs: jm}, sink)
	ag.SetControllerBridge(newSubControllerBridge())
	deadline := time.After(2 * time.Second)
	for {
		if n, ok := jm.CompletedResult(job.ID); ok && n.Output == "ONCE" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("job did not complete")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !ag.commitJobResult(job.ID, "ONCE") {
		t.Fatal("first commit failed")
	}
	if !ag.commitJobResult(job.ID, "ONCE") {
		t.Fatal("second commit should still report success (idempotent)")
	}
	var envelopes int
	for _, m := range sess.Messages {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "background-task-result") {
			envelopes++
		}
	}
	if envelopes != 1 {
		t.Fatalf("want 1 envelope, got %d", envelopes)
	}
}

// TestTailEnvelopeSurvivesSanitizeToolPairing locks the delivery shape against
// the regression where append-as-tool was silently dropped at request build.
func TestTailEnvelopeSurvivesSanitizeToolPairing(t *testing.T) {
	sess := agentNewSessionWithStartedTask("call-s", "task-9")
	sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "waiting"})
	sess.Add(provider.Message{
		Role:    provider.RoleUser,
		Content: "<background-task-result job=\"task-9\">\nBODY\n</background-task-result>",
	})
	out := provider.SanitizeToolPairing(sess.Messages)
	found := false
	for _, m := range out {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "BODY") {
			found = true
		}
		if m.Role == provider.RoleTool && m.Content == "BODY" {
			t.Fatal("unexpected tool-shaped delivery in sanitized history")
		}
	}
	if !found {
		t.Fatal("user tail envelope must survive SanitizeToolPairing")
	}
}

func agentNewSessionWithStartedTask(toolCallID, jobID string) *Session {
	s := NewSession("")
	s.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: toolCallID, Name: "task"}}})
	s.Add(provider.Message{Role: provider.RoleTool, ToolCallID: toolCallID, Name: "task", Content: FormatStartedTaskResult(jobID, "label")})
	return s
}

func TestToolCallIDForStartedTaskLine(t *testing.T) {
	s := agentNewSessionWithStartedTask("c9", "task-9")
	if got := s.ToolCallIDForStartedTaskLine("task-9"); got != "c9" {
		t.Fatalf("got %q", got)
	}
}
