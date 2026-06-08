package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"reasonix/internal/evidence"
	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(todoWrite{}) }

// todoWrite records the agent's running task list. It has no host side effects —
// the full list lives in the call's args, which a frontend renders as a checklist.
// Execute validates the shape, acks with a count, and — when merge=true — updates
// only the provided items while keeping the rest untouched, so the model can flip
// a single item's status without rewriting the entire list.
//
// Correct usage flow:
//   1. Call todo_write with the full plan (merge=false, the default).
//   2. As each step finishes, call complete_step to sign it off with evidence.
//   3. Then call todo_write with merge=true, sending only the item(s) whose
//      status changed (e.g. marking the finished step "completed" and advancing
//      the next to "in_progress").  The list stays in sync — no need to repeat
//      items whose status hasn't changed.
//   4. When all items are "completed" the frontend hides the panel automatically.
type todoWrite struct{}

type todoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm,omitempty"`
	Level      int    `json:"level,omitempty"`
}

func (todoWrite) Name() string { return "todo_write" }

func (todoWrite) Description() string {
	return "Record and update a structured task list for the current work. Send the COMPLETE list every call — it replaces the previous one. Use it to plan multi-step work and show progress: keep exactly one item in_progress at a time, and flip an item to completed the moment it's done (don't batch completions). Skip it for trivial single-step tasks. The list is two-level: a `level` 0 item is a PHASE (a milestone) and the `level` 1 items after it are its concrete sub-steps; omit `level` (0) for a flat list. Each item has `content` (imperative, e.g. \"Add the parser\"), `status` (pending|in_progress|completed), `activeForm` (present-continuous shown while in progress, e.g. \"Adding the parser\"), and optional `level` (0 phase | 1 sub-step).\n\nCorrect flow:\n  1. todo_write todos=[...] (merge=false, default) — create the plan.\n  2. complete_step for the in_progress item — sign it off with evidence.\n  3. todo_write merge=true todos=[{content, status}] — only the changed items.\n  Repeat 2-3 for each step. When all are completed the panel disappears."
}

func (todoWrite) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "merge":{
    "type":"boolean",
    "description":"When true, update only the provided items (matched by content) and keep the rest unchanged. When false or omitted, replace the entire list. Use merge=true after each complete_step to flip just the finished item to completed and advance the next to in_progress."
  },
  "todos":{
    "type":"array",
    "description":"The complete task list, in order. Replaces any previous list.",
    "items":{
      "type":"object",
      "properties":{
        "content":{"type":"string","description":"Imperative description of the task."},
        "status":{"type":"string","enum":["pending","in_progress","completed"],"description":"Task state. Keep at most one in_progress."},
        "activeForm":{"type":"string","description":"Present-continuous form shown while the task is in progress (e.g. \"Running tests\")."},
        "level":{"type":"integer","enum":[0,1],"description":"Nesting level: 0 = phase/milestone, 1 = a sub-step of the phase above it. Omit for a flat list."}
      },
      "required":["content","status"]
    }
  }
},
"required":["todos"]
}`)
}

// ReadOnly is true: todo_write only records a list (no filesystem or process
// effect), so it never needs approval and stays available in plan mode — where
// laying out a plan as todos is exactly the point.
func (todoWrite) ReadOnly() bool { return true }

func (todoWrite) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Merge bool       `json:"merge"`
		Todos []todoItem `json:"todos"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if len(p.Todos) == 0 {
		return "", fmt.Errorf("todos is required")
	}
	var done, active, pending int
	for i, t := range p.Todos {
		if t.Content == "" {
			return "", fmt.Errorf("todo %d: content is required", i+1)
		}
		if t.Level < 0 || t.Level > 1 {
			return "", fmt.Errorf("todo %d: invalid level %d (want 0 phase | 1 sub-step)", i+1, t.Level)
		}
		switch t.Status {
		case "completed":
			done++
		case "in_progress":
			active++
		case "pending", "":
			pending++
		default:
			return "", fmt.Errorf("todo %d: invalid status %q (want pending|in_progress|completed)", i+1, t.Status)
		}
	}
	if err := verifyTodoCompletionTransitions(ctx, p.Todos, p.Merge); err != nil {
		return "", err
	}
	return fmt.Sprintf("Todos updated: %d total — %d completed, %d in progress, %d pending.",
		len(p.Todos), done, active, pending), nil
}

func verifyTodoCompletionTransitions(ctx context.Context, todos []todoItem, merge bool) error {
	ledger, ok := evidence.FromContext(ctx)
	if !ok {
		return nil
	}
	missing, hasBaseline := ledger.UnverifiedCompletedTodos(toEvidenceTodos(todos))
	if hasBaseline {
		if !merge {
			dropped, _ := ledger.DroppedSinceLastTodo(toEvidenceTodos(todos))
			missing = append(missing, dropped...)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if len(missing) == 1 {
		m := missing[0]
		return fmt.Errorf("todo %d %q is newly completed but has no matching successful complete_step receipt in this turn", m.Index, m.Content)
	}
	return fmt.Errorf("%d todos are newly completed but have no matching successful complete_step receipts in this turn", len(missing))
}

func toEvidenceTodos(todos []todoItem) []evidence.TodoItem {
	out := make([]evidence.TodoItem, 0, len(todos))
	for _, t := range todos {
		out = append(out, evidence.TodoItem{
			Content:    t.Content,
			Status:     t.Status,
			ActiveForm: t.ActiveForm,
			Level:      t.Level,
		})
	}
	return out
}
