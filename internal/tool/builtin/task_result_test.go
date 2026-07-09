package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/tool"
)

func TestTaskResultOmittedFromModelSchema(t *testing.T) {
	reg := tool.NewRegistry()
	// task_result self-registers via init into builtins; re-add like boot does.
	for _, b := range tool.Builtins() {
		if b.Name() == "task_result" {
			reg.Add(b)
		}
	}
	if _, ok := reg.Get("task_result"); !ok {
		t.Fatal("task_result must remain registered for history/Execute resolution")
	}
	for _, s := range reg.Schemas() {
		if s.Name == "task_result" {
			t.Fatal("task_result must not appear in provider Schemas")
		}
	}
	for _, n := range reg.Names() {
		if n == "task_result" {
			t.Fatal("task_result must not appear in model-facing Names")
		}
	}
	if _, ok := reg.Suggest("task_result"); ok {
		// Suggest may return exact if not omitted — should not for system tools.
		t.Fatal("Suggest must not recommend task_result")
	}
	tl, ok := reg.Get("task_result")
	if !ok {
		t.Fatal("missing task_result")
	}
	_, err := tl.Execute(context.Background(), json.RawMessage(`{"job_id":"task-1"}`))
	if err == nil || !strings.Contains(err.Error(), "not a callable tool") {
		t.Fatalf("Execute should refuse model calls, got %v", err)
	}
}
