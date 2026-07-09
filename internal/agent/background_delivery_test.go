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
	if asst.ToolCalls[0].Name != BackgroundDeliveryToolName {
		t.Fatalf("tool name = %q, want %s", asst.ToolCalls[0].Name, BackgroundDeliveryToolName)
	}
	if tool.Name != BackgroundDeliveryToolName {
		t.Fatalf("tool result name = %q, want %s", tool.Name, BackgroundDeliveryToolName)
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
	if !sess.AppendBackgroundTaskDelivery("task-9", BackgroundDeliveryToolName, "BODY") {
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

func TestHasUndeliveredAutoJobsIgnoresRunning(t *testing.T) {
	jm := jobs.NewManager(event.Discard)
	defer jm.Close()
	// Long-running task still in map.
	_, err := jm.Start(context.Background(), jobs.KindTask, "slow", func(ctx context.Context, _ io.Writer) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ag := New(nil, nil, NewSession(""), Options{Jobs: jm}, event.Discard)
	if ag.hasUndeliveredAutoJobs() {
		t.Fatal("running task must not count as undelivered")
	}
	// Instant completed task still present → undelivered until RemoveJob.
	done, err := jm.Start(context.Background(), jobs.KindTask, "fast", func(_ context.Context, _ io.Writer) (string, error) {
		return "ok", nil
	}, nil)
	if err != nil {
		// may hit concurrency if slow holds slot — wait and retry once
		time.Sleep(50 * time.Millisecond)
		done, err = jm.Start(context.Background(), jobs.KindTask, "fast", func(_ context.Context, _ io.Writer) (string, error) {
			return "ok", nil
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.After(2 * time.Second)
	for {
		if _, ok := jm.CompletedResult(done.ID); ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("fast job did not complete")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !ag.hasUndeliveredAutoJobs() {
		t.Fatal("completed task still in map should count as undelivered")
	}
	jm.RemoveJob(done.ID)
	if ag.hasUndeliveredAutoJobs() {
		t.Fatal("after RemoveJob should be clear")
	}
}

func TestSessionHasUnreadTaskResult(t *testing.T) {
	ag := New(nil, nil, NewSession(""), Options{}, event.Discard)
	if ag.sessionHasUnreadTaskResult() {
		t.Fatal("empty session")
	}
	ag.session.AppendBackgroundTaskDelivery("task-1", BackgroundDeliveryToolName, "BODY")
	if !ag.sessionHasUnreadTaskResult() {
		t.Fatal("want unread after delivery")
	}
	ag.session.Add(provider.Message{Role: provider.RoleAssistant, Content: "summarized"})
	if ag.sessionHasUnreadTaskResult() {
		t.Fatal("assistant answer clears unread")
	}
}

func TestEmptyBackgroundWakeAbortsWithoutWork(t *testing.T) {
	jm := jobs.NewManager(event.Discard)
	defer jm.Close()
	sess := NewSession("sys")
	ag := New(nil, nil, sess, Options{Jobs: jm}, event.Discard)
	bridge := newSubControllerBridge()
	bridge.SetPendingToolResult(true)
	ag.SetControllerBridge(bridge)
	// No undelivered jobs, no task_result — wake should no-op without panicking.
	if err := ag.Run(context.Background(), ""); err != nil {
		t.Fatalf("empty wake: %v", err)
	}
	if bridge.PendingToolResult() {
		t.Fatal("pending flag should clear on empty wake")
	}
}

// TestBashJobDoesNotAutoDeliver locks the product split: shell jobs stay peek-only.
func TestBashJobDoesNotAutoDeliver(t *testing.T) {
	sink := event.Discard
	jm := jobs.NewManager(sink)
	defer jm.Close()

	// Wire controller-style completion that only handles AutoDelivers kinds.
	var delivered bool
	jm.SetOnCompletion(func(id string) {
		kind, _ := jm.Kind(id)
		if jobs.AutoDelivers(kind) {
			delivered = true
		}
	})

	job, err := jm.Start(context.Background(), jobs.KindBash, "echo", func(_ context.Context, w io.Writer) (string, error) {
		_, _ = w.Write([]byte("shell-out\n"))
		return "", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		if n, ok := jm.CompletedResult(job.ID); ok {
			_ = n
			break
		}
		select {
		case <-deadline:
			t.Fatal("bash job did not complete")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if delivered {
		t.Fatal("bash completion must not mark auto-delivery")
	}
	sess := NewSession("")
	ag := New(nil, nil, sess, Options{Jobs: jm}, sink)
	if ag.CompleteBackgroundJob(job.ID) {
		t.Fatal("CompleteBackgroundJob must refuse bash jobs")
	}
	// Job still peekable.
	if _, err := jm.Peek(job.ID); err != nil {
		t.Fatalf("bash job should remain for peek: %v", err)
	}
	text, st, ok := jm.Output(job.ID)
	if !ok || st != jobs.Done || !strings.Contains(text, "shell-out") {
		t.Fatalf("want shell output via peek path, got text=%q status=%s ok=%v", text, st, ok)
	}
}
