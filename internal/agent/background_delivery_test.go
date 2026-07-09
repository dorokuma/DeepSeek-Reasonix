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

func TestCommitBackgroundJobResultObservationTail(t *testing.T) {
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
	// Mid-conversation chatter after Started — Started row must stay frozen.
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
		t.Fatal("CompleteBackgroundJob should commit observation at tail")
	}
	// Spawn Started stub is permanent — never rewritten.
	if !IsStartedTaskPlaceholder(sess.Messages[1].Content) {
		t.Fatalf("Started stub must remain unchanged, got %q", sess.Messages[1].Content)
	}
	if sess.Messages[1].Content == "ANSWER-7" {
		t.Fatal("regression: mid-history patch must not return")
	}
	// Tail: user-role observation envelope (NOT a tool call).
	last := sess.Messages[len(sess.Messages)-1]
	if last.Role != provider.RoleUser || !IsBackgroundTaskResultMessage(last.Content) {
		t.Fatalf("want user-role background-task-result observation, got %+v", last)
	}
	if BackgroundTaskResultJobID(last.Content) != job.ID {
		t.Fatalf("job id = %q, want %q", BackgroundTaskResultJobID(last.Content), job.ID)
	}
	if !strings.Contains(last.Content, "ANSWER-7") {
		t.Fatalf("answer missing from observation: %q", last.Content)
	}
	// Must not introduce a fake tool name.
	for _, m := range sess.Messages {
		if m.Name == "task_result" {
			t.Fatal("task_result tool name must not appear in session")
		}
		for _, tc := range m.ToolCalls {
			if tc.Name == "task_result" {
				t.Fatal("task_result tool_call must not appear in session")
			}
		}
	}
	// Survives pairing sanitizer (user rows are never stripped).
	sanitized := provider.SanitizeToolPairing(sess.Messages)
	found := false
	for _, m := range sanitized {
		if m.Role == provider.RoleUser && IsBackgroundTaskResultMessage(m.Content) && strings.Contains(m.Content, "ANSWER-7") {
			found = true
		}
	}
	if !found {
		t.Fatal("observation delivery must survive SanitizeToolPairing")
	}
}

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
	var deliveries int
	for _, m := range sess.Messages {
		if m.Role == provider.RoleUser && IsBackgroundTaskResultMessage(m.Content) &&
			BackgroundTaskResultJobID(m.Content) == job.ID {
			deliveries++
		}
	}
	if deliveries != 1 {
		t.Fatalf("want 1 delivery observation, got %d", deliveries)
	}
}

func TestObservationDeliverySurvivesSanitizeToolPairing(t *testing.T) {
	sess := agentNewSessionWithStartedTask("call-s", "task-9")
	sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "waiting"})
	if !sess.AppendBackgroundTaskDelivery("task-9", "", "BODY") {
		t.Fatal("append failed")
	}
	out := provider.SanitizeToolPairing(sess.Messages)
	found := false
	for _, m := range out {
		if m.Role == provider.RoleUser && IsBackgroundTaskResultMessage(m.Content) && strings.Contains(m.Content, "BODY") {
			found = true
		}
	}
	if !found {
		t.Fatal("observation delivery must survive SanitizeToolPairing")
	}
	if !IsStartedTaskPlaceholder(sess.Messages[1].Content) {
		t.Fatal("Started stub must not be rewritten")
	}
}

func agentNewSessionWithStartedTask(toolCallID, jobID string) *Session {
	s := NewSession("")
	s.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: toolCallID, Name: "task"}}})
	s.Add(provider.Message{Role: provider.RoleTool, ToolCallID: toolCallID, Name: "task", Content: FormatStartedTaskResult(jobID, "label")})
	return s
}
