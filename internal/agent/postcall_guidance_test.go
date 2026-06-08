package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// testPGTool implements PostCallGuidance.
type testPGTool struct{}
func (testPGTool) Name() string        { return "test_pg_tool" }
func (testPGTool) Description() string  { return "mock with PG" }
func (testPGTool) ReadOnly() bool       { return true }
func (testPGTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (testPGTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "result data", nil
}
func (testPGTool) PostCallGuidance(_ json.RawMessage) string {
	return "Include this in your final answer."
}

// testPGTPrefixTool also implements GuidancePrefixer.
type testPGTPrefixTool struct{ testPGTool }
func (testPGTPrefixTool) GuidancePrefix() string { return "CUSTOM:" }

// simpleTool is a bare tool without PostCallGuidance.
type simpleTool struct{}
func (simpleTool) Name() string        { return "simple_tool" }
func (simpleTool) Description() string  { return "simple no PG" }
func (simpleTool) ReadOnly() bool       { return true }
func (simpleTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (simpleTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "plain result", nil
}

func TestPostCallGuidance_AppendsToToolResult(t *testing.T) {
	a := &Agent{tools: tool.NewRegistry()}
	a.tools.Add(testPGTool{})

	out := a.executeOne(context.Background(), provider.ToolCall{
		ID:        "call_1",
		Name:      "test_pg_tool",
		Arguments: `{}`,
	})
	if out.errMsg != "" {
		t.Fatalf("unexpected error: %s", out.errMsg)
	}
	if !strings.Contains(out.output, "Post-call requirements") {
		t.Fatalf("default prefix missing in:\n%s", out.output)
	}
	if !strings.Contains(out.output, "Include this in your final answer.") {
		t.Fatalf("guidance missing in:\n%s", out.output)
	}
}

func TestPostCallGuidance_CustomPrefix(t *testing.T) {
	a := &Agent{tools: tool.NewRegistry()}
	a.tools.Add(testPGTPrefixTool{})

	out := a.executeOne(context.Background(), provider.ToolCall{
		ID:        "call_1",
		Name:      "test_pg_tool",
		Arguments: `{}`,
	})
	if out.errMsg != "" {
		t.Fatalf("unexpected error: %s", out.errMsg)
	}
	if !strings.Contains(out.output, "CUSTOM:") {
		t.Fatalf("custom prefix missing in:\n%s", out.output)
	}
	if !strings.Contains(out.output, "Include this in your final answer.") {
		t.Fatalf("guidance missing in:\n%s", out.output)
	}
}

func TestPostCallGuidance_NonPGToolUnchanged(t *testing.T) {
	a := &Agent{tools: tool.NewRegistry()}
	a.tools.Add(simpleTool{})

	out := a.executeOne(context.Background(), provider.ToolCall{
		ID:        "call_1",
		Name:      "simple_tool",
		Arguments: `{}`,
	})
	if out.errMsg != "" {
		t.Fatalf("unexpected error: %s", out.errMsg)
	}
	if strings.Contains(out.output, "Post-call") || strings.Contains(out.output, "requirements") {
		t.Fatalf("non-PG tool output should NOT contain guidance, got:\n%s", out.output)
	}
	if !strings.Contains(out.output, "plain result") {
		t.Fatalf("expected unchanged result, got: %q", out.output)
	}
}
