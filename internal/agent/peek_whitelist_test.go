package agent

import (
	"context"
	"encoding/json"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

type stubPeekTool struct{}

func (stubPeekTool) Name() string        { return "peek-job" }
func (stubPeekTool) Description() string { return "peek" }
func (stubPeekTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string"}},"required":["job_id"]}`)
}
func (stubPeekTool) Execute(context.Context, json.RawMessage) (string, error) {
	return `{"ok":true}`, nil
}
func (stubPeekTool) ReadOnly() bool { return true }

func TestPeekJobBypassesMainAgentWhitelistWhenExposed(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(stubPeekTool{})
	allow := map[string]bool{"task": true}
	dynamic := map[string]bool{"peek-job": true}
	sess := NewSession("")
	ag := New(nil, reg, sess, Options{MainAgentAllowed: allow, ToolsDynamic: dynamic}, event.Discard)
	ag.SetDiagnosticRequested(true)
	out := ag.executeOne(context.Background(), provider.ToolCall{ID: "c1", Name: "peek-job", Arguments: `{"job_id":"task-1"}`})
	if out.blocked || out.errMsg != "" {
		t.Fatalf("peek-job should run when diagnostic exposed: blocked=%v err=%q out=%q", out.blocked, out.errMsg, out.output)
	}
	if out.output != `{"ok":true}` {
		t.Fatalf("output = %q", out.output)
	}
}
