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

func TestCommitBackgroundJobResultSyntheticTailTurn(t *testing.T) {
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
		t.Fatal("CompleteBackgroundJob should commit synthetic tail turn")
	}
	// Spawn Started stub is permanent — never rewritten.
	if !IsStartedTaskPlaceholder(sess.Messages[1].Content) {
		t.Fatalf("Started stub must remain unchanged, got %q", sess.Messages[1].Content)
	}
	if sess.Messages[1].Content == "ANSWER-7" {
		t.Fatal("regression: mid-history patch must not return")
	}
	// Tail: assistant tool_calls + tool result, properly paired.
	if len(sess.Messages) < 5 {
		t.Fatalf("want spawn + wait + delivery pair, got %d messages", len(sess.Messages))
	}
	asst := sess.Messages[len(sess.Messages)-2]
	tool := sess.Messages[len(sess.Messages)-1]
	deliveryID := BackgroundDeliveryCallID(job.ID)
	if asst.Role != provider.RoleAssistant || len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != deliveryID {
		t.Fatalf("want synthetic assistant tool_call %q, got %+v", deliveryID, asst)
	}
	if asst.ToolCalls[0].Name != "task" {
		t.Fatalf("tool name = %q, want task", asst.ToolCalls[0].Name)
	}
	if !strings.Contains(asst.ToolCalls[0].Arguments, job.ID) {
		t.Fatalf("args missing job id: %q", asst.ToolCalls[0].Arguments)
	}
	if tool.Role != provider.RoleTool || tool.ToolCallID != deliveryID || tool.Content != "ANSWER-7" {
		t.Fatalf("want paired tool result, got %+v", tool)
	}
	// Must survive SanitizeToolPairing (orphan tool tails do not).
	sanitized := provider.SanitizeToolPairing(sess.Messages)
	found := false
	for _, m := range sanitized {
		if m.Role == provider.RoleTool && m.ToolCallID == deliveryID && m.Content == "ANSWER-7" {
			found = true
		}
	}
	if !found {
		t.Fatal("synthetic delivery must survive SanitizeToolPairing")
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
	deliveryID := BackgroundDeliveryCallID(job.ID)
	for _, m := range sess.Messages {
		if m.Role == provider.RoleTool && m.ToolCallID == deliveryID {
			deliveries++
		}
	}
	if deliveries != 1 {
		t.Fatalf("want 1 delivery tool row, got %d", deliveries)
	}
}

func TestSyntheticDeliverySurvivesSanitizeToolPairing(t *testing.T) {
	sess := agentNewSessionWithStartedTask("call-s", "task-9")
	sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "waiting"})
	if !sess.AppendBackgroundTaskDelivery("task-9", "task", "BODY") {
		t.Fatal("append failed")
	}
	out := provider.SanitizeToolPairing(sess.Messages)
	found := false
	for _, m := range out {
		if m.Role == provider.RoleTool && m.ToolCallID == BackgroundDeliveryCallID("task-9") && m.Content == "BODY" {
			found = true
		}
	}
	if !found {
		t.Fatal("synthetic paired delivery must survive SanitizeToolPairing")
	}
	// Started stub still present and unchanged.
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

func TestToolCallIDForStartedTaskLine(t *testing.T) {
	s := agentNewSessionWithStartedTask("c9", "task-9")
	if got := s.ToolCallIDForStartedTaskLine("task-9"); got != "c9" {
		t.Fatalf("got %q", got)
	}
}
