package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/provider"
	"reasonix/internal/tool"

	_ "reasonix/internal/tool/builtin"
)

func TestTodoProgressGuardPausesSemanticToolDrift(t *testing.T) {
	turns := []testutil.Turn{{ToolCalls: []provider.ToolCall{{
		ID: "todo", Name: "todo_write",
		Arguments: `{"todos":[{"content":"finish the task","status":"in_progress"}]}`,
	}}}}
	for i := 0; i < maxTodoStallRounds; i++ {
		turns = append(turns, testutil.Turn{ToolCalls: []provider.ToolCall{{
			ID: fmt.Sprintf("read-%d", i), Name: "inspect", Arguments: `{}`,
		}}})
	}

	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "inspect", readOnly: true})
	reg.Add(mustBuiltinTool(t, "todo_write"))
	mp := testutil.NewMock("m", turns...)
	a := New(mp, reg, NewSession(""), Options{}, event.Discard)

	err := a.Run(context.Background(), "work until the todo is complete")
	var pause *todoStallPause
	if !errors.As(err, &pause) {
		t.Fatalf("Run error = %v, want todoStallPause", err)
	}
	if mp.CallCount() != maxTodoStallRounds+1 {
		t.Fatalf("provider calls = %d, want %d", mp.CallCount(), maxTodoStallRounds+1)
	}
}

func TestCanonicalTodoProgressIgnoresTitleAndPendingListChurn(t *testing.T) {
	a := &Agent{todoState: []evidence.TodoItem{
		{Content: "finish the task", Status: "in_progress"},
		{Content: "write tests", Status: "pending"},
	}}
	before, tracking := a.canonicalTodoProgress()
	if !tracking {
		t.Fatal("incomplete todo list should be tracked")
	}
	a.setTodoState([]evidence.TodoItem{
		{Content: "finish the task carefully", Status: "in_progress"},
		{Content: "write tests", Status: "pending"},
		{Content: "update docs", Status: "pending"},
	})
	after, tracking := a.canonicalTodoProgress()
	if !tracking || after != before {
		t.Fatalf("title/pending churn changed progress from %d to %d", before, after)
	}
}

func TestMaxStepsGraceSummaryBypassesIncompleteTodoReadiness(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(mustBuiltinTool(t, "todo_write"))
	reg.Add(fakeTool{name: "write_file", readOnly: false})
	mp := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{
			{ID: "todo", Name: "todo_write", Arguments: `{"todos":[{"content":"unfinished","status":"in_progress"}]}`},
			{ID: "write", Name: "write_file", Arguments: `{"path":"unfinished.txt"}`},
		}},
		testutil.Turn{Text: "Progress saved; the todo remains unfinished."},
	)
	a := New(mp, reg, NewSession(""), Options{MaxSteps: 1, DeliveryProfile: true}, event.Discard)

	err := a.Run(context.Background(), "start a long task")
	var pause *maxStepsPause
	if !errors.As(err, &pause) {
		t.Fatalf("Run error = %v, want maxStepsPause instead of final-readiness retries", err)
	}
	if mp.CallCount() != 2 {
		t.Fatalf("provider calls = %d, want tool round plus one summary round", mp.CallCount())
	}
}

func mustBuiltinTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	builtin, ok := tool.LookupBuiltin(name)
	if !ok {
		t.Fatalf("builtin %q is not registered", name)
	}
	return builtin
}
