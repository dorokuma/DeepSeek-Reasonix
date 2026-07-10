package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"reasonix/internal/event"
	"reasonix/internal/tool"
)

// Renderer redraws the assistant's final-answer text as styled output. It is
// applied only after a turn's text stream completes, so the user sees raw
// markdown stream live, then a single redraw replaces it with formatted
// output. The renderer is intentionally interface-shaped so the agent stays
// independent of the cli's markdown library choice. Consumed by TextSink.
type Renderer interface {
	Render(text string) string
}

// Asker puts structured multiple-choice questions to the user and blocks for the
// answers. The agent consults it for the `ask` tool. It is interface-shaped so
// the agent stays independent of the frontend; a nil asker means no interactive
// user (headless runs), where `ask` returns a "decide for yourself" result. The
// interactive frontends wire the controller in as the Asker.
type Asker interface {
	Ask(ctx context.Context, questions []event.AskQuestion) ([]event.AskAnswer, error)
}

// ctrlKey carries the ControllerBridge in the tool call context.
type ctrlKey struct{}

// withCtrl stamps ctx with the ControllerBridge so tools (notably the `task`
// tool) can register job metadata during Execute.
func withCtrl(ctx context.Context, c ControllerBridge) context.Context {
	cctx := context.WithValue(ctx, ctrlKey{}, c)
	return tool.WithCtrl(cctx, c)
}

// CtrlFromContext extracts the ControllerBridge from the context, if any.
func CtrlFromContext(ctx context.Context) (ControllerBridge, bool) {
	cc, ok := ctx.Value(ctrlKey{}).(ControllerBridge)
	return cc, ok
}

// callContextKey carries the executing tool call's identity into Execute.
type callContextKey struct{}

// callContext is the per-call context a tool can read. parentID is the call being
// executed and sink is the agent's event sink (the `task` tool uses both to nest
// a sub-agent's events under this call); asker lets the `ask` tool reach the user.
type callContext struct {
	parentID string
	sink     event.Sink
	asker    Asker
}

// withCallContext stamps ctx with the executing call's ID, the agent's sink, and
// the asker. executeOne sets this before every Execute; `task` reads it (via
// CallContext) to nest sub-agent events, and `ask` reads the asker to prompt.
func withCallContext(ctx context.Context, parentID string, sink event.Sink, asker Asker) context.Context {
	cctx := context.WithValue(ctx, callContextKey{}, callContext{parentID: parentID, sink: sink, asker: asker})
	return tool.WithCallID(cctx, parentID)
}

// CallContext returns the executing call's ID, the agent's sink, and the asker,
// if the context was set by an agent's executeOne. ok is false for a plain
// context (headless tool tests, calls made outside the run loop).
func CallContext(ctx context.Context) (parentID string, sink event.Sink, asker Asker, ok bool) {
	cc, ok := ctx.Value(callContextKey{}).(callContext)
	if !ok {
		return "", nil, nil, false
	}
	return cc.parentID, cc.sink, cc.asker, true
}

// Gate decides, per tool call, whether it may run. The agent consults it at
// execute time (after the gate). It is interface-shaped so the agent
// stays independent of the permission package and of how "ask" is resolved
// (silently in headless runs, interactively in the chat TUI). A nil gate means
// no gating — every call runs, preserving behaviour for callers that don't wire
// one in. reason is fed back to the model when allow is false; a non-nil err
// (e.g. ctx cancelled awaiting approval) is treated as a block for that call.
type Gate interface {
	Check(ctx context.Context, toolName string, args json.RawMessage, readOnly bool) (allow bool, reason string, err error)
}

// ToolHooks fires user-configured shell hooks around each tool call. PreToolUse
// runs before the call and may block it (block=true; message is the reason fed
// back to the model); when modified is non-nil the caller MUST use those args
// instead of the original. PostToolUse runs after and only surfaces output to
// the user (it can't block). It is interface-shaped so the agent stays
// independent of the hook package — a nil hooks field disables hook firing
// entirely.
type ToolHooks interface {
	PreToolUse(ctx context.Context, name string, args json.RawMessage) (block bool, message string, modified json.RawMessage)
	PostToolUse(ctx context.Context, name string, args json.RawMessage, result string)
	// PostLLMCall fires after each model turn completes (streaming finishes)
	// but before reasoning_content is stored. It returns the (possibly
	// translated) reasoning string — the original when no hook is configured.
	// HasPostLLMCall reports whether such a hook exists, so the agent keeps
	// streaming reasoning live when none is wired up.
	PostLLMCall(ctx context.Context, reasoning string, turn int) string
	HasPostLLMCall() bool
	PreCompact(ctx context.Context, trigger string) string
}

// PostToolRewriter is an optional extension to ToolHooks. When the hook
// implementation also satisfies this interface, PostToolRewrite is called
// after PostToolUse and may transform the tool result string before it is
// fed back to the model. Panics are recovered; on panic the original result
// is kept.
type PostToolRewriter interface {
	PostToolRewrite(ctx context.Context, name string, args json.RawMessage, result string) string
}

// sessionCostInfo bundles the cumulative cost and its currency for atomic storage.
type sessionCostInfo struct {
	cost     float64
	currency string
}

// ControllerBridge is the interface the Controller implements so the Agent can
// check for pending tool results and consume job metadata without a direct
// import dependency on the control package.
type ControllerBridge interface {
	// PendingToolResult reports whether a completed background task is waiting
	// to be drained (peek only; does not clear the flag).
	PendingToolResult() bool
	// PendingToolResultCAS atomically compares-and-swaps the pendingToolResult
	// flag. Returns true when the swap succeeded (old value matched).
	PendingToolResultCAS(old, new bool) bool
	// SetPendingToolResult sets the pending-tool-result flag (e.g. re-arm after
	// a drain race).
	SetPendingToolResult(v bool)
}

// subControllerBridge is a lightweight ControllerBridge implementation for
// headless sub-agents (no session-scoped background jobs).
type subControllerBridge struct {
	pendingToolResult atomic.Bool
}

func newSubControllerBridge() *subControllerBridge {
	return &subControllerBridge{
	}
}

func (c *subControllerBridge) PendingToolResult() bool {
	return c.pendingToolResult.Load()
}

func (c *subControllerBridge) PendingToolResultCAS(old, new bool) bool {
	return c.pendingToolResult.CompareAndSwap(old, new)
}

func (c *subControllerBridge) SetPendingToolResult(v bool) {
	c.pendingToolResult.Store(v)
}



// MakeOnComplete implements OnCompleteProvider. Always nil: sub-agents cannot
// spawn async grandchildren, and parent completion uses SetOnCompletion only.
func (c *subControllerBridge) MakeOnComplete() func(jobID string) {
	return nil
}

// MakeOnMessage is retired.
func (c *subControllerBridge) MakeOnMessage() func(jobID string) {
	return nil
}

// Ask implements Asker. Sub-agents are headless — they cannot prompt the user.
func (c *subControllerBridge) Ask(_ context.Context, _ []event.AskQuestion) ([]event.AskAnswer, error) {
	return nil, fmt.Errorf("sub-agent does not support interactive prompts")
}
