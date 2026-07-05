package agent

import (
	"context"
	"io"
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
	if sess.Messages[1].Content != "ANSWER-7" {
		t.Fatalf("patched tool = %q", sess.Messages[1].Content)
	}
	foundEnvelope := false
	for _, m := range sess.Messages {
		if m.Role == provider.RoleUser && containsSubstr(m.Content, "ANSWER-7") && containsSubstr(m.Content, "background-task-result") {
			foundEnvelope = true
		}
	}
	if !foundEnvelope {
		t.Fatal("expected background-task-result envelope")
	}
}

func agentNewSessionWithStartedTask(toolCallID, jobID string) *Session {
	s := NewSession("")
	s.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: toolCallID, Name: "task"}}})
	s.Add(provider.Message{Role: provider.RoleTool, ToolCallID: toolCallID, Name: "task", Content: "Started task " + jobID + " (label)"})
	return s
}

func containsSubstr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && stringIndex(s, sub) >= 0)
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestToolCallIDForStartedTaskLine(t *testing.T) {
	s := agentNewSessionWithStartedTask("c9", "task-9")
	if got := s.ToolCallIDForStartedTaskLine("task-9"); got != "c9" {
		t.Fatalf("got %q", got)
	}
	if got := s.ToolCallIDForStartedTaskLine("task-nope"); got != "" {
		t.Fatalf("got %q", got)
	}
}

