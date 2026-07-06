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
		t.Fatalf("started row should become answer, got %q", sess.Messages[1].Content)
	}
	if len(sess.Messages) > 3 {
		t.Fatalf("unexpected extra messages: %d", len(sess.Messages))
	}
	for _, m := range sess.Messages {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "background-task-result") {
			t.Fatalf("unexpected user envelope: %q", m.Content)
		}
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
