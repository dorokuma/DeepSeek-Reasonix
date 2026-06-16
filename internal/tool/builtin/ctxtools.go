package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"reasonix/internal/ctxmode"
	"reasonix/internal/tool"
)

func init() {
	tool.RegisterBuiltin(ctxRead{})
	tool.RegisterBuiltin(ctxSearch{})
}

type ctxRead struct{}

func (ctxRead) Name() string { return "ctx_read" }

func (ctxRead) Description() string {
	return "Page through tool output previously compacted by ctxmode (read_file, grep, MCP, etc.). Use the ref from the [ctx] summary (e.g. ctx-1)."
}

func (ctxRead) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "ref":{"type":"string","description":"Sandbox ref from [ctx] summary (e.g. ctx-1)"},
  "offset":{"type":"integer","description":"0-based line offset (default 0)","minimum":0},
  "limit":{"type":"integer","description":"Max lines to return (default 80, max 200)","minimum":1,"maximum":200}
},
"required":["ref"]
}`)
}

func (ctxRead) ReadOnly() bool { return true }

func (ctxRead) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Ref    string `json:"ref"`
		Offset int    `json:"offset,omitempty"`
		Limit  int    `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	store, ok := ctxmode.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("context store is not available in this session")
	}
	return store.Read(p.Ref, p.Offset, p.Limit)
}

type ctxSearch struct{}

func (ctxSearch) Name() string { return "ctx_search" }

func (ctxSearch) Description() string {
	return "Search sandboxed tool output by substring. Use the ref from a prior [ctx] summary."
}

func (ctxSearch) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "ref":{"type":"string","description":"Sandbox ref (e.g. ctx-1)"},
  "pattern":{"type":"string","description":"Case-sensitive substring to find"},
  "limit":{"type":"integer","description":"Max matching lines (default 40, max 100)","minimum":1,"maximum":100}
},
"required":["ref","pattern"]
}`)
}

func (ctxSearch) ReadOnly() bool { return true }

func (ctxSearch) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Ref     string `json:"ref"`
		Pattern string `json:"pattern"`
		Limit   int    `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	if p.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	store, ok := ctxmode.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("context store is not available in this session")
	}
	return store.Search(p.Ref, p.Pattern, p.Limit)
}