package serve

import "reasonix/internal/event"

// wireEvent is the JSON shape an event.Event takes on the SSE stream. It uses
// explicit lowercase tags (a clean contract for a JS client) and flattens the
// few non-JSON-friendly bits — the Kind enum becomes a string, the TurnDone
// error becomes a message — so a browser frontend renders the same typed stream
// the TUI does.
type wireEvent struct {
	Kind      string        `json:"kind"`
	Text      string        `json:"text,omitempty"`
	Reasoning string        `json:"reasoning,omitempty"`
	Level     string        `json:"level,omitempty"`
	Tool      *wireTool     `json:"tool,omitempty"`
	Usage     *wireUsage    `json:"usage,omitempty"`
	Approval  *wireApproval `json:"approval,omitempty"`
	Err       string        `json:"err,omitempty"`
}

type wireTool struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Args      string `json:"args,omitempty"`
	Output    string `json:"output,omitempty"`
	Err       string `json:"err,omitempty"`
	ReadOnly  bool   `json:"readOnly"`
	Truncated bool   `json:"truncated,omitempty"`
}

type wireUsage struct {
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	TotalTokens      int     `json:"totalTokens"`
	CacheHitTokens   int     `json:"cacheHitTokens"`
	CacheMissTokens  int     `json:"cacheMissTokens"`
	ReasoningTokens  int     `json:"reasoningTokens,omitempty"`
	CostUSD          float64 `json:"costUsd,omitempty"`
}

type wireApproval struct {
	ID      string `json:"id"`
	Tool    string `json:"tool"`
	Subject string `json:"subject"`
}

// kindNames maps the event.Kind enum to stable wire strings.
var kindNames = map[event.Kind]string{
	event.TurnStarted:     "turn_started",
	event.Reasoning:       "reasoning",
	event.Text:            "text",
	event.Message:         "message",
	event.ToolDispatch:    "tool_dispatch",
	event.ToolResult:      "tool_result",
	event.Usage:           "usage",
	event.Notice:          "notice",
	event.Phase:           "phase",
	event.ApprovalRequest: "approval_request",
	event.TurnDone:        "turn_done",
}

// toWire converts an event.Event into its JSON wire form.
func toWire(e event.Event) wireEvent {
	w := wireEvent{Kind: kindNames[e.Kind], Text: e.Text, Reasoning: e.Reasoning}
	switch e.Kind {
	case event.Notice:
		if e.Level == event.LevelWarn {
			w.Level = "warn"
		} else {
			w.Level = "info"
		}
	case event.ToolDispatch, event.ToolResult:
		w.Tool = &wireTool{
			ID: e.Tool.ID, Name: e.Tool.Name, Args: e.Tool.Args,
			Output: e.Tool.Output, Err: e.Tool.Err,
			ReadOnly: e.Tool.ReadOnly, Truncated: e.Tool.Truncated,
		}
	case event.Usage:
		if u := e.Usage; u != nil {
			w.Usage = &wireUsage{
				PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
				TotalTokens: u.TotalTokens, CacheHitTokens: u.CacheHitTokens,
				CacheMissTokens: u.CacheMissTokens, ReasoningTokens: u.ReasoningTokens,
			}
			if e.Pricing != nil {
				w.Usage.CostUSD = e.Pricing.Cost(u)
			}
		}
	case event.ApprovalRequest:
		w.Approval = &wireApproval{ID: e.Approval.ID, Tool: e.Approval.Tool, Subject: e.Approval.Subject}
	case event.TurnDone:
		if e.Err != nil {
			w.Err = e.Err.Error()
		}
	}
	return w
}
