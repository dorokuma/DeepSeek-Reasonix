package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

func TestNeedsReasoningRoundTrip(t *testing.T) {
	t.Parallel()
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "go"},
		{Role: provider.RoleAssistant, ReasoningContent: "plan", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "bash"}}},
		{Role: provider.RoleTool, Content: "ok", ToolCallID: "c1", Name: "bash"},
		{Role: provider.RoleAssistant, ReasoningContent: "done thinking", Content: "done"},
		{Role: provider.RoleUser, Content: "next"},
		{Role: provider.RoleAssistant, Content: "plain chat"},
	}
	cases := []struct {
		idx  int
		want bool
	}{
		{idx: 1, want: true},  // tool-call assistant
		{idx: 2, want: false}, // tool result
		{idx: 3, want: true},  // final answer after tools
		{idx: 5, want: false}, // chat-only assistant
	}
	for _, tc := range cases {
		if got := needsReasoningRoundTrip(msgs, tc.idx); got != tc.want {
			t.Fatalf("idx=%d got=%v want=%v", tc.idx, got, tc.want)
		}
	}
}

func TestBuildRequestDropsReasoningForPureChat(t *testing.T) {
	c := &client{model: "deepseek-v4-flash", deepseek: true}
	req := c.buildRequest(provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "explain"},
			{Role: provider.RoleAssistant, Content: "the answer", ReasoningContent: "SECRET-CHAIN-OF-THOUGHT"},
			{Role: provider.RoleUser, Content: "thanks"},
		},
	})
	b, err := json.Marshal(req.Messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(b)
	if strings.Contains(body, "reasoning_content") {
		t.Errorf("pure chat must not round-trip reasoning_content: %s", body)
	}
	if strings.Contains(body, "SECRET-CHAIN-OF-THOUGHT") {
		t.Errorf("chain-of-thought leaked into request: %s", body)
	}
}

func TestBuildRequestRoundTripsReasoningForToolCalls(t *testing.T) {
	c := &client{model: "deepseek-v4-flash", deepseek: true}
	req := c.buildRequest(provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "read"},
			{Role: provider.RoleAssistant, ReasoningContent: "need file", ToolCalls: []provider.ToolCall{
				{ID: "call_1", Name: "read_file", Arguments: `{"path":"x"}`},
			}},
			{Role: provider.RoleTool, Content: "data", ToolCallID: "call_1", Name: "read_file"},
			{Role: provider.RoleUser, Content: "summarize"},
		},
	})
	b, err := json.Marshal(req.Messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(b)
	if !strings.Contains(body, `"reasoning_content":"need file"`) {
		t.Errorf("tool-call reasoning missing from wire request: %s", body)
	}
}

func TestBuildRequestRoundTripsEmptyReasoningForToolCalls(t *testing.T) {
	c := &client{model: "deepseek-v4-flash", deepseek: true}
	req := c.buildRequest(provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "time"},
			{Role: provider.RoleAssistant, Content: "", ToolCalls: []provider.ToolCall{
				{ID: "call_1", Name: "get_time", Arguments: `{}`},
			}},
		},
	})
	b, err := json.Marshal(req.Messages[1])
	if err != nil {
		t.Fatalf("marshal assistant: %v", err)
	}
	body := string(b)
	if !strings.Contains(body, `"reasoning_content":""`) {
		t.Errorf("empty reasoning must serialize explicitly for tool calls: %s", body)
	}
}

func TestBuildRequestRoundTripsReasoningForPostToolAnswer(t *testing.T) {
	c := &client{model: "deepseek-v4-flash", deepseek: true}
	req := c.buildRequest(provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "weather"},
			{Role: provider.RoleAssistant, ReasoningContent: "call tool", ToolCalls: []provider.ToolCall{
				{ID: "c1", Name: "web_search", Arguments: `{}`},
			}},
			{Role: provider.RoleTool, Content: "sunny", ToolCallID: "c1", Name: "web_search"},
			{Role: provider.RoleAssistant, ReasoningContent: "summarize", Content: "sunny today"},
		},
	})
	final := req.Messages[len(req.Messages)-1]
	if final.ReasoningContent == nil || *final.ReasoningContent != "summarize" {
		t.Fatalf("post-tool final assistant reasoning not round-tripped: %+v", final.ReasoningContent)
	}
}
