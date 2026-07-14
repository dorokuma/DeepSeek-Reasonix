package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"reasonix/internal/ctxmode"
	"reasonix/internal/event"
	"reasonix/internal/instruction"
	"reasonix/internal/memory"
	"reasonix/internal/multiagent"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func (a *Agent) executeBatch(ctx context.Context, calls []provider.ToolCall) []string {
	// Early exit if already cancelled.
	if ctx.Err() != nil {
		results := make([]string, len(calls))
		for i := range results {
			results[i] = fmt.Sprintf("cancelled: %v", ctx.Err())
		}
		return results
	}

	// Create a cancellable child context for tool execution. When the parent
	// context is cancelled this propagates to all in-flight tools so they can
	// exit early (tools must check ctx.Err()).
	toolCtx, toolCancel := context.WithCancel(ctx)
	defer toolCancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			toolCancel()
		case <-done:
		}
	}()

	for _, c := range calls {
		t, ok := a.tools.Get(c.Name)
		ev := event.Tool{ID: c.ID, Name: c.Name, Args: c.Arguments, ReadOnly: ok && t.ReadOnly()}
		if ok {
			if ch, ok := tool.PreviewChange(t, json.RawMessage(c.Arguments)); ok {
				ev.FileDiff = event.FileDiff{Diff: ch.Diff, Added: ch.Added, Removed: ch.Removed}
			}
			if pr, ok := t.(interface {
				ResolveProfile(json.RawMessage) *event.Profile
			}); ok {
				ev.Profile = pr.ResolveProfile(json.RawMessage(c.Arguments))
			}
		}
		a.sink.Emit(event.Event{Kind: event.ToolDispatch, Tool: ev})
	}

	results := make([]string, len(calls))
	outcomes := make([]toolOutcome, len(calls))
	durations := make([]int64, len(calls))
	trun := func(i int) {
		start := time.Now()
		outcomes[i] = a.executeOne(toolCtx, calls[i])
		durations[i] = time.Since(start).Milliseconds()
		results[i] = outcomes[i].output
	}

	for _, batch := range partitionToolCalls(a.tools, calls) {
		if batch.parallel && batch.end-batch.start > 1 {
			runParallel(toolCtx, batch.start, batch.end, trun, func(idx int, msg string) {
				outcomes[idx] = toolOutcome{errMsg: msg, output: msg}
				results[idx] = msg
			})
			continue
		}
		for i := batch.start; i < batch.end; i++ {
			trun(i)
		}
	}

	for i, c := range calls {
		o := outcomes[i]
		t, ok := a.tools.Get(c.Name)
		a.sink.Emit(event.Event{Kind: event.ToolResult, Tool: event.Tool{
			ID:         c.ID,
			Name:       c.Name,
			Args:       c.Arguments,
			Output:     o.output,
			Err:        o.errMsg,
			ReadOnly:   ok && t.ReadOnly(),
			Truncated:  o.truncated,
			DurationMs: durations[i],
		}})
		if o.truncated && o.truncMsg != "" {
			a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: o.truncMsg})
		}
	}
	a.applyStormBreaker(calls, outcomes, results)
	return results
}

type toolCallBatch struct {
	start    int
	end      int
	parallel bool
}

// partitionToolCalls keeps provider order while letting contiguous known
// read-only tools run together. Unknown and writer tools are single-call serial
// batches so they cannot reorder around reads or produce surprising errors.
func partitionToolCalls(r *tool.Registry, calls []provider.ToolCall) []toolCallBatch {
	var batches []toolCallBatch
	for i := 0; i < len(calls); {
		if parallelisable(r, calls[i].Name) {
			start := i
			i++
			for i < len(calls) && parallelisable(r, calls[i].Name) {
				i++
			}
			batches = append(batches, toolCallBatch{start: start, end: i, parallel: true})
			continue
		}
		batches = append(batches, toolCallBatch{start: i, end: i + 1})
		i++
	}
	return batches
}

func parallelisable(r *tool.Registry, name string) bool {
	t, ok := r.Get(name)
	if !ok {
		return false
	}
	if t.ReadOnly() {
		return true
	}
	if c, ok := t.(tool.Concurrenter); ok {
		return c.Concurrent()
	}
	return false
}

// runParallel runs indices [start,end) with a bounded worker pool.
// On ctx cancel it stops launching new workers and waits for in-flight ones
// (they still observe ctx via the run closure if they check it). Already-started
// run(i) calls are not force-killed by this helper.
func runParallel(ctx context.Context, start, end int, run func(int), onPanic func(int, string)) {
	const maxParallel = 8
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i := start; i < end; i++ {
		i := i
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			// Wait for already-launched goroutines to finish before returning
			// so we don't leak them or leave the semaphore in an inconsistent state.
			wg.Wait()
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("tool goroutine panicked",
						"index", i,
						"panic", r,
					)
					if onPanic != nil {
						onPanic(i, fmt.Sprintf("internal error: tool panicked: %v", r))
					}
				}
			}()
			// Skip execution if context was cancelled while waiting for the
			// semaphore — tools that check ctx.Err() would return immediately
			// anyway, but this avoids the overhead of building their context.
			if ctx.Err() != nil {
				return
			}
			run(i)
		}()
	}
	wg.Wait()
}

// stormBreakThreshold is how many times in a row the same tool may fail the same
// way before the loop stops echoing the raw error back and instead returns a
// directive to change approach. Two natural self-corrections are healthy; the
// third identical failure is a death-spiral — the dominant case being a tool call
// whose arguments are truncated at the output-token ceiling, which the model then
// re-emits (re-worded but still over-long), truncating the same way again.
const stormBreakThreshold = 3

// repeatSuccessBreakThreshold is how many identical write-like successes the
// agent allows before refusing another copy in the same user turn. Two gives the
// model room for a natural self-correction; the third repeat is usually a
// no-op/write loop and should be redirected to a different tool or final answer.
const repeatSuccessBreakThreshold = 2

// applyStormBreaker detects a run of identically-failing turns and, past the
// threshold, rewrites the model-facing result (results[0]) into a directive to
// change approach. It keys on each call's (tool, error) — not its args — because a
// stuck model reworks the arguments cosmetically while failing identically (see
// the stormSig field doc). A turn is a fixation candidate only when every one of
// its calls errored and none was merely blocked by permissions (those
// carry a clear, distinct message the model can already act on). Any success, any
// block, or a different batch shape is varied work, so it resets the counter. This
// covers both the single-call spiral and a repeated multi-call batch. The hard
// maxSteps guard remains the ultimate backstop; this just keeps the loop from
// burning that whole budget bouncing off the same failure.
func (a *Agent) applyStormBreaker(calls []provider.ToolCall, outcomes []toolOutcome, results []string) {
	sig, ok := batchStormSignature(calls, outcomes)
	if !ok {
		a.stormSig, a.stormCount = "", 0
		return
	}
	if sig != a.stormSig {
		a.stormSig, a.stormCount = sig, 1
		return
	}
	a.stormCount++
	if a.stormCount < stormBreakThreshold {
		return
	}
	subject := fmt.Sprintf("%q", calls[0].Name)
	short := calls[0].Name
	if len(calls) > 1 {
		subject = fmt.Sprintf("this batch of %d tool calls", len(calls))
		short = fmt.Sprintf("a batch of %d calls", len(calls))
	}
	results[0] = outcomes[0].output + fmt.Sprintf(
		"\n\n[loop guard] %s has now failed %d times in a row with the same error. Re-sending it — even with the wording changed — will not help: the calls keep failing the same way. Change approach: if an argument is being truncated, write less in one call and split the work into several smaller calls; otherwise fix the arguments, use a different tool, or explain the blocker in your final answer.",
		subject, a.stormCount)
	a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: fmt.Sprintf(
		"loop guard: %s failed %d× the same way — nudging the model to change approach",
		short, a.stormCount)})
}

// batchStormSignature returns a per-turn fixation signature — each call's
// (name, error) in order — and ok=true only when every call errored and none was
// merely blocked. ok=false (any success or block) means the turn made varied
// progress, so the caller resets the counter. Keying on the error rather than the
// args is deliberate: a stuck model reworks the arguments while failing the same
// way, so identical-args matching would miss the loop.
func batchStormSignature(calls []provider.ToolCall, outcomes []toolOutcome) (string, bool) {
	if len(calls) == 0 {
		return "", false
	}
	var sb strings.Builder
	for i := range calls {
		if outcomes[i].errMsg == "" || outcomes[i].blocked {
			return "", false
		}
		sb.WriteString(calls[i].Name)
		sb.WriteByte(0)
		sb.WriteString(outcomes[i].errMsg)
		sb.WriteByte(0)
	}
	return sb.String(), true
}

// toolOutcome is one tool call's result, split into the model-facing output and
// the display-facing notice bits. errMsg is the short failure reason (empty on
// success) — a refused call, an unknown tool, or an execution error — so a sink
// renders the result as failed ("⊘ name <errMsg>" / a red card) instead of OK;
// blocked narrows that to a refusal (permission policy). truncMsg is set
// (without the "· " prefix) when the output was head+tailed.
type toolOutcome struct {
	output    string
	blocked   bool
	errMsg    string
	truncated bool
	truncMsg  string
}

// executeOne runs a single tool call. It is pure with respect to the event sink
// — the caller emits ToolDispatch/ToolResult — so it is safe to invoke from
// parallel goroutines.

func (a *Agent) executeOne(ctx context.Context, call provider.ToolCall) toolOutcome {
	t, ok := a.tools.Get(call.Name)
	if !ok {
		// Tool not found — auto-correct via fuzzy matching instead of just
		// reporting an error. Prevents the model from looping on hallucinated
		// tool names (e.g. context vs mcp_context).
		if suggestion, found := a.tools.Suggest(call.Name); found {
			slog.Info("tool auto-correct", "from", call.Name, "to", suggestion)
			t, ok = a.tools.Get(suggestion)
		}
	}
	if !ok {
		errMsg := fmt.Sprintf(
			"error: unknown tool %q. Available tools: %v. "+
				"Pick the correct tool and retry.",
			call.Name, a.tools.Names())
		return toolOutcome{
			output: errMsg,
			errMsg: fmt.Sprintf("unknown tool %q", call.Name),
		}
	}
	if sub, ok := t.(tool.OnlyForSubAgent); ok && sub.OnlyForSubAgent() && NestingDepthFrom(ctx) == 0 {
		return toolOutcome{
			output: fmt.Sprintf("error: tool %q is only available to sub-agents", call.Name),
			errMsg: fmt.Sprintf("tool %q sub-agent only", call.Name),
		}
	}
	if a.toolsDynamic != nil && a.toolsDynamic[call.Name] && NestingDepthFrom(ctx) == 0 && !a.diagnosticRequested.Load() {
		return toolOutcome{
			output:  fmt.Sprintf("permission denied: tool %q is not currently available", call.Name),
			blocked: true,
			errMsg:  fmt.Sprintf("permission denied: tool %q not available", call.Name),
		}
	}
	// Main-agent whitelist: when nesting depth is 0 (root/main agent),
	// only allow explicitly permitted tools (if the option is set).
	if allow := a.mainAgentAllowed; allow != nil && NestingDepthFrom(ctx) == 0 && !allow[call.Name] && !(a.toolsDynamic != nil && a.toolsDynamic[call.Name] && a.diagnosticRequested.Load()) {
		return toolOutcome{
			output:  fmt.Sprintf("permission denied: tool %q not allowed for main agent", call.Name),
			blocked: true,
			errMsg:  fmt.Sprintf("permission denied: tool %q not allowed for main agent", call.Name),
		}
	}
	// Main-agent readonly calls limit: when nesting depth is 0 (root/main agent),
	// enforce maximum limit of readonly tool calls.
	if t.ReadOnly() && NestingDepthFrom(ctx) == 0 && a.maxMainAgentReadonlyCalls > 0 {
		count := a.readonlyCallsCount.Add(1)
		if count > int64(a.maxMainAgentReadonlyCalls) {
			return toolOutcome{
				output:  fmt.Sprintf("permission denied: main agent readonly call limit reached (%d)", a.maxMainAgentReadonlyCalls),
				blocked: true,
				errMsg:  fmt.Sprintf("main agent readonly call limit reached (%d)", a.maxMainAgentReadonlyCalls),
			}
		}
	}
	if out, blocked := a.repeatedSuccessBlock(call, t); blocked {
		return toolOutcome{
			output:  out,
			blocked: true,
			errMsg:  "blocked by loop guard",
		}
	}
	if a.gate != nil {
		allow, reason, err := a.gate.Check(ctx, call.Name, json.RawMessage(call.Arguments), t.ReadOnly())
		if err != nil {
			return toolOutcome{
				output:  fmt.Sprintf("blocked: %s (%v)", reason, err),
				blocked: true,
				errMsg:  fmt.Sprintf("blocked: %v", err),
			}
		}
		if !allow {
			return toolOutcome{
				output:  "blocked: " + reason,
				blocked: true,
				errMsg:  "blocked by permission policy",
			}
		}
	}
	// PreToolUse hooks run after permission is granted but before the call: a
	// gating hook (exit 2) refuses it, surfaced to the model like a gate denial.
	// A hook may also emit replacement args on stdout; if so the tool executes
	// with those instead of the model's original Arguments.
	effectiveArgs := json.RawMessage(call.Arguments)
	if a.hooks != nil {
		if block, msg, modified := a.hooks.PreToolUse(ctx, call.Name, effectiveArgs); block {
			if msg == "" {
				msg = "blocked by a PreToolUse hook"
			}
			return toolOutcome{
				output:  "blocked: " + msg,
				blocked: true,
				errMsg:  "blocked by PreToolUse hook",
			}
		} else if modified != nil {
			effectiveArgs = modified
		}
	}
	// Checkpoint the file this writer is about to change, so the turn can be
	// rewound. Fires after all gating (the edit is cleared to run) and only for
	// tools that can describe their change; a Preview error means the edit will
	// likely fail anyway, so we skip rather than snapshot a stale state.
	if a.onPreEdit != nil && !t.ReadOnly() {
		if pv, ok := t.(tool.Previewer); ok {
			if change, perr := pv.Preview(effectiveArgs); perr == nil {
				a.onPreEdit(change)
			}
		}
	}
	cctx := withCallContext(ctx, call.ID, a.sink, a.asker)
	cctx = WithSession(cctx, a.session)
	cctx = WithAgent(cctx, a)
	if len(a.projectChecks) > 0 {
		cctx = instruction.WithChecks(cctx, a.projectChecks)
	}
	if a.multiAgent != nil {
		cctx = multiagent.WithControl(cctx, a.multiAgent)
		path := a.agentPath
		if path == "" {
			path = multiagent.RootPath
		}
		cctx = multiagent.WithAgentPath(cctx, path)
	}
	if a.ctrl != nil {
		cctx = withCtrl(cctx, a.ctrl)
		if p, ok := a.ctrl.(OnCompleteProvider); ok {
			cctx = WithOnCompleteProvider(cctx, p)
		}
	}
	if a.memQueue != nil {
		cctx = memory.WithQueue(cctx, a.memQueue)
	}
	if a.ctxStore != nil {
		cctx = ctxmode.WithStore(cctx, a.ctxStore)
	}
	if p, ok := a.asker.(OnCompleteProvider); ok {
		cctx = WithOnCompleteProvider(cctx, p)
	}
	callID := call.ID
	cctx = tool.WithProgress(cctx, func(chunk string) {
		a.sink.Emit(event.Event{Kind: event.ToolProgress, Tool: event.Tool{ID: callID, Output: chunk}})
	})
	// Write diagnostic metadata before tool execution.
	var diagMeta *multiagent.Metadata
	if c, ok := multiagent.FromContext(cctx); ok {
		if rec := c.Meta(multiagent.AgentPathFrom(cctx)); rec != nil {
			rec.StartTool(call.Name)
			diagMeta = rec
		}
	}

	if ctx.Err() != nil {
		return toolOutcome{output: "", errMsg: fmt.Sprintf("cancelled: %v", ctx.Err())}
	}
	result, err := t.Execute(cctx, effectiveArgs)

	// Clear diagnostic metadata after tool execution.
	if diagMeta != nil {
		diagMeta.EndTool()
	}
	if err == nil {
		filePath := TryExtractPath(effectiveArgs)
		if filePath != "" {
			if t.ReadOnly() {
				globalFileStateRegistry.RecordRead(a.session, filePath)
			} else {
				globalFileStateRegistry.RecordWrite(a.session, filePath)
			}
		}
	}
	if a.ctxStore != nil {
		ctxmode.RecordTool(a.ctxStore.Journal(), call.Name, effectiveArgs, result, err)
	}

	// PostToolUse hooks observe the result (they can't block); fired whether the
	// call succeeded or errored, since the tool did run. We pass the original
	// args here (not effectiveArgs) so the hook sees what the model intended, not
	// what a previous hook rewrote it to.
	if a.hooks != nil {
		a.hooks.PostToolUse(ctx, call.Name, json.RawMessage(call.Arguments), result)
	}
	// PostToolRewrite: optional hook-level result transformation.
	// Panics are recovered; on panic the original result is kept.
	if a.hooks != nil {
		if rewriter, ok := a.hooks.(PostToolRewriter); ok {
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Warn("PostToolRewriter panicked, using original result",
							"tool", call.Name,
							"panic", r,
						)
					}
				}()
				result = rewriter.PostToolRewrite(ctx, call.Name, json.RawMessage(call.Arguments), result)
			}()
		}
	}
	if err != nil {
		detail := result
		// Malformed-args failures are a transient model JSON glitch (e.g. options
		// written as ["a":"b"] → "invalid character ':' after array element"). The
		// args can't be safely re-parsed, but echoing the tool's schema makes the
		// retry land valid instead of repeating the same broken shape.
		if !json.Valid([]byte(call.Arguments)) {
			detail = strings.TrimRight(detail, "\n") + "\nThe arguments were not valid JSON. Re-emit them exactly per this schema:\n" + string(t.Schema())
		}
		body, truncMsg := compactToolOutput(a.ctxStore, call.Name, json.RawMessage(call.Arguments), fmt.Sprintf("error: %v\n%s", err, detail))
		return toolOutcome{output: body, errMsg: firstLine(err.Error()), truncated: truncMsg != "" || strings.Contains(body, "[truncated "), truncMsg: truncMsg}
	}
	a.recordRepeatSuccess(call, t)
	body, truncMsg := compactToolOutput(a.ctxStore, call.Name, json.RawMessage(call.Arguments), result)
	// PostCallGuidance: if the tool teaches a post-call workflow, append it
	// to the result so the model is explicitly reminded what to do next.
	if pg, ok := t.(tool.PostCallGuidance); ok {
		prefix := "⚠ **Post-call requirements**"
		if gp, ok := t.(tool.GuidancePrefixer); ok {
			if p := strings.TrimSpace(gp.GuidancePrefix()); p != "" {
				prefix = p
			}
		}
		var guidance string
		if pgr, ok := t.(tool.PostCallGuidanceWithResult); ok {
			guidance = strings.TrimSpace(pgr.PostCallGuidanceAfter(json.RawMessage(call.Arguments), result))
		} else {
			guidance = strings.TrimSpace(pg.PostCallGuidance(json.RawMessage(call.Arguments)))
		}
		if guidance != "" {
			body += "\n\n---\n" + prefix + "\n" + guidance
		}
	}
	return toolOutcome{output: body, truncated: truncMsg != "" || strings.Contains(body, "[truncated "), truncMsg: truncMsg}
}

func (a *Agent) repeatedSuccessBlock(call provider.ToolCall, t tool.Tool) (string, bool) {
	sig, ok := repeatSuccessSignature(call, t)
	if !ok {
		return "", false
	}
	a.repeatSuccessMu.Lock()
	count := a.repeatSuccessCounts[sig]
	a.repeatSuccessMu.Unlock()
	if count < repeatSuccessBreakThreshold {
		return "", false
	}
	return fmt.Sprintf(
		"blocked: [loop guard] %q has already succeeded %d times with the same write-like arguments in this user turn. Re-running it is unlikely to help and may burn tokens or repeat file writes. Change approach: use a file editing tool for file changes, verify with a read/test command, or explain the blocker in your final answer.",
		call.Name, count), true
}

func (a *Agent) recordRepeatSuccess(call provider.ToolCall, t tool.Tool) {
	sig, ok := repeatSuccessSignature(call, t)
	if !ok {
		return
	}
	a.repeatSuccessMu.Lock()
	if a.repeatSuccessCounts == nil {
		a.repeatSuccessCounts = make(map[string]int)
	}
	a.repeatSuccessCounts[sig]++
	a.repeatSuccessMu.Unlock()
}

func repeatSuccessSignature(call provider.ToolCall, t tool.Tool) (string, bool) {
	if t.ReadOnly() {
		return "", false
	}
	switch call.Name {
	case "write_file", "edit_file", "multi_edit", "move_file", "notebook_edit":
		return call.Name + "\x00" + canonicalToolArgs(call.Arguments), true
	case "bash":
		var p struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(call.Arguments), &p); err != nil {
			return "", false
		}
		if !isShellFileWriteCommand(p.Command) {
			return "", false
		}
		return "bash\x00" + normalizeShellCommand(p.Command), true
	default:
		return "", false
	}
}

func canonicalToolArgs(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return strings.TrimSpace(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, b); err != nil {
		return string(b)
	}
	return compact.String()
}

func normalizeShellCommand(command string) string {
	return strings.Join(strings.Fields(command), " ")
}

func isShellFileWriteCommand(command string) bool {
	lower := strings.ToLower(command)
	switch {
	case shellPythonOpenWrites(lower):
		return true
	case strings.Contains(lower, "set-content") || strings.Contains(lower, "add-content") || strings.Contains(lower, "out-file"):
		return true
	case strings.Contains(lower, "sed -i") || strings.Contains(lower, "perl -pi"):
		return true
	case hasShellWriteRedirect(command):
		return true
	default:
		return false
	}
}

func shellPythonOpenWrites(lower string) bool {
	if !strings.Contains(lower, "open(") {
		return false
	}
	if strings.Contains(lower, ".write(") {
		return true
	}
	for _, marker := range []string{", 'w", `, "w`, ", 'a", `, "a`, ", 'x", `, "x`, "mode='w", `mode="w`, "mode='a", `mode="a`, "mode='x", `mode="x`} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func hasShellWriteRedirect(command string) bool {
	var quote rune
	var prev rune
	for _, r := range command {
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			prev = r
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			prev = r
			continue
		}
		if r == '>' {
			if prev == '2' {
				prev = r
				continue
			}
			return true
		}
		prev = r
	}
	return false
}

// toolReadOnly reports a tool's ReadOnly classification by name (false for an
// unknown tool), for stamping early ToolDispatch events.
func (a *Agent) toolReadOnly(name string) bool {
	t, ok := a.tools.Get(name)
	return ok && t.ReadOnly()
}

// firstLine returns s up to its first newline — a one-line failure summary for
// the display Err, while the full error stays in the model-facing output.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// truncateToolOutput head+tails s when it exceeds maxToolOutputBytes, slicing
// on rune boundaries so we never split a multibyte glyph. Returns the possibly
// trimmed body (which includes an internal "[truncated ...]" marker).
// The one-line user-facing notice is suppressed (not emitted as Notice event
// to avoid chat spam); truncation events are always logged via slog for debugging.
func truncateToolOutput(s string) (string, string) {
	if len(s) <= maxToolOutputBytes {
		return s, ""
	}
	keep := maxToolOutputBytes / 2
	head := snapToRuneBoundary(s, 0, keep)
	tail := snapToRuneBoundary(s, len(s)-keep, len(s))
	omitted := len(s) - len(head) - len(tail)
	slog.Info("tool output truncated", "omitted", omitted, "total", len(s))
	body := head + fmt.Sprintf("\n\n…[truncated %d of %d bytes — rerun with narrower args to see the middle]…\n\n", omitted, len(s)) + tail
	return body, ""
}

// snapToRuneBoundary returns s[lo:hi] with the bounds nudged outward until
// both land on rune-start positions.
func snapToRuneBoundary(s string, lo, hi int) string {
	for lo > 0 && !utf8.RuneStart(s[lo]) {
		lo--
	}
	for hi < len(s) && !utf8.RuneStart(s[hi]) {
		hi++
	}
	return s[lo:hi]
}

// finishReasonMessage maps an abnormal finish_reason to a one-line warning,
// returning ok=false for the normal terminations ("stop", "tool_calls") and a
// nil usage. The sink renders the message; the "! " prefix is presentation.
func finishReasonMessage(u *provider.Usage) (string, bool) {
	if u == nil {
		return "", false
	}
	switch u.FinishReason {
	case "length":
		return "response truncated: hit max output tokens", true
	case "content_filter":
		return "response blocked by content filter", true
	case "repetition_truncation":
		return "response truncated: model repetition detected", true
	default:
		return "", false
	}
}

// parseMultimodalInput scans the input text for [REASONIX_IMAGE:data:...]
// markers, extracts each as a ContentPart with Type PartTypeImage, and returns
// the cleaned text (with markers removed) together with the extracted parts.
// If no markers are found, returns the original text and nil parts.
func parseMultimodalInput(input string) (string, []provider.ContentPart) {
	const prefix = "[REASONIX_IMAGE:"
	const suffix = "]"

	var parts []provider.ContentPart
	cleaned := input

	for {
		start := strings.Index(cleaned, prefix)
		if start < 0 {
			break
		}
		end := strings.Index(cleaned[start+len(prefix):], suffix)
		if end < 0 {
			break
		}
		end = start + len(prefix) + end + len(suffix)

		// Extract the data URL between markers
		dataURL := cleaned[start+len(prefix) : end-len(suffix)]

		// Parse the data URL into MIME type and base64 data
		mime, data := parseDataURL(dataURL)
		if mime != "" && data != "" {
			parts = append(parts, provider.ContentPart{
				Type: provider.PartTypeImage,
				Image: &provider.ImagePart{
					Data: data,
					Mime: mime,
				},
			})
		}

		// Remove the marker from the text (including the newline around it)
		cleaned = cleaned[:start] + cleaned[end:]
	}

	// Clean up leftover blank lines from marker removal
	cleaned = strings.TrimSpace(cleaned)

	if len(parts) == 0 {
		return input, nil
	}
	return cleaned, parts
}

// parseDataURL extracts the MIME type and base64 data from a data URL.
// Input: "data:image/jpeg;base64,/9j/4AAQ..."
// Output: "image/jpeg", "/9j/4AAQ..."
func parseDataURL(dataURL string) (mime, data string) {
	rest := strings.TrimPrefix(dataURL, "data:")
	if rest == dataURL {
		return "", ""
	}
	idx := strings.Index(rest, ";base64,")
	if idx < 0 {
		return "", ""
	}
	mime = rest[:idx]
	data = rest[idx+len(";base64,"):]
	return
}

// sessionHasUnreadTaskResult is true when the conversation tail is a runtime
// background delivery that the main agent has not yet answered.
// sessionHasUnreadTaskResult reports pending multiagent mailbox work for the parent model.
func (a *Agent) sessionHasUnreadTaskResult() bool {
	if a.multiAgent != nil && a.multiAgent.Mailbox().HasPendingFor(a.agentPath) {
		return true
	}
	if a.session == nil {
		return false
	}
	msgs := a.session.Snapshot()
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		switch m.Role {
		case provider.RoleUser:
			return strings.Contains(m.Content, "[multi_agent_mailbox]")
		case provider.RoleAssistant:
			if strings.TrimSpace(m.Content) != "" || len(m.ToolCalls) > 0 {
				return false
			}
		}
	}
	return false
}
