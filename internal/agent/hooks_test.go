package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// stubHooks blocks PreToolUse for named tools and records what it saw.
type stubHooks struct {
	blockPre      map[string]bool
	preSeen       []string
	postSeen      []string
	preCompactOut string   // returned from PreCompact (extra summary guidance)
	hasPostLLM    bool     // whether HasPostLLMCall reports a PostLLMCall hook
	postLLMOut    string   // replacement returned from PostLLMCall (when hasPostLLM)
	postLLMSeen   []string // reasoning text each PostLLMCall received
	postLLMTurns  []int    // turn number each PostLLMCall received
}

func (h *stubHooks) PreToolUse(_ context.Context, name string, _ json.RawMessage) (bool, string, json.RawMessage) {
	h.preSeen = append(h.preSeen, name)
	if h.blockPre[name] {
		return true, "blocked by test hook", nil
	}
	return false, "", nil
}

func (h *stubHooks) PostToolUse(_ context.Context, name string, _ json.RawMessage, _ string) {
	h.postSeen = append(h.postSeen, name)
}
func (h *stubHooks) PreCompact(context.Context, string) string { return h.preCompactOut }

func (h *stubHooks) PostLLMCall(_ context.Context, reasoning string, turn int) string {
	h.postLLMSeen = append(h.postLLMSeen, reasoning)
	h.postLLMTurns = append(h.postLLMTurns, turn)
	if h.hasPostLLM && h.postLLMOut != "" {
		return h.postLLMOut
	}
	return reasoning
}

func (h *stubHooks) HasPostLLMCall() bool { return h.hasPostLLM }

// TestPreToolUseHookBlocks proves a gating PreToolUse hook refuses a tool call
// (returning a blocked result, never running the tool or its PostToolUse), while
// an unblocked call runs and fires PostToolUse.
func TestPreToolUseHookBlocks(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "bash", readOnly: false})
	reg.Add(fakeTool{name: "read_file", readOnly: true})

	h := &stubHooks{blockPre: map[string]bool{"bash": true}}
	a := New(nil, reg, NewSession(""), Options{Hooks: h}, event.Discard)

	blocked := a.executeOne(context.Background(), provider.ToolCall{Name: "bash", Arguments: `{"command":"x"}`})
	if !blocked.blocked || !strings.HasPrefix(blocked.output, "blocked:") {
		t.Errorf("PreToolUse block should yield a blocked result, got %+v", blocked)
	}
	if !strings.Contains(blocked.output, "blocked by test hook") {
		t.Errorf("block reason should be surfaced to the model, got %q", blocked.output)
	}

	ok := a.executeOne(context.Background(), provider.ToolCall{Name: "read_file", Arguments: `{"path":"/a"}`})
	if ok.blocked || !strings.Contains(ok.output, "done") {
		t.Errorf("unblocked call should run, got %+v", ok)
	}

	if got := strings.Join(h.preSeen, ","); got != "bash,read_file" {
		t.Errorf("PreToolUse should fire for both calls, saw %q", got)
	}
	// PostToolUse fires only for the call that actually ran.
	if got := strings.Join(h.postSeen, ","); got != "read_file" {
		t.Errorf("PostToolUse should fire only for the run tool, saw %q", got)
	}
}
