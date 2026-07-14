// Package openai implements the OpenAI-compatible /chat/completions provider.
// It self-registers under the "openai" kind, so DeepSeek, MiMo, and any other
// OpenAI-compatible endpoint are just config instances rather than code.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"reasonix/internal/diag"
	"reasonix/internal/netclient"
	"reasonix/internal/provider"
)

// defaultStreamIdleTimeout caps how long a started SSE stream may go without any
// bytes before it's treated as a dropped connection. A half-open TCP connection
// (e.g. a proxy switched mid-stream) sends no RST, so scanner.Scan() would block
// forever; this turns that hang into a recoverable error. Generous on purpose —
// live streams emit tokens/keepalives far more often. Stored per-client
// (client.idleTimeout) so a test can shorten it without a shared global that
// would race other streams' watchdogs.
const defaultStreamIdleTimeout = 120 * time.Second

func init() {
	provider.Register("openai", New)
}

// New builds an OpenAI-compatible provider from a resolved config.
func New(cfg provider.Config) (provider.Provider, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("openai: base_url is required for provider %q", cfg.Name)
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("openai: model is required for provider %q", cfg.Name)
	}
	name := cfg.Name
	if name == "" {
		name = "openai"
	}
	keyEnv, _ := cfg.Extra["api_key_env"].(string) // for actionable auth errors
	effort, _ := cfg.Extra["effort"].(string)

	supportsVision := inferSupportsVision(cfg.Model)
	if v, ok := cfg.Extra["supports_vision"]; ok {
		switch val := v.(type) {
		case bool:
			supportsVision = val
		case string:
			if b, err := strconv.ParseBool(val); err == nil {
				supportsVision = b
			}
		}
	}
	deepseek := isDeepSeekBaseURL(cfg.BaseURL) || isDeepSeekModel(cfg.Model)
	minimax := false // MiniMax not supported in this fork
	if deepseek {
		effort = strings.ToLower(strings.TrimSpace(effort))
		switch effort {
		case "", "off": // "off" is a retired level (disabled thinking); fall back to the default depth
			effort = "high"
		case "high", "max":
		default:
			return nil, fmt.Errorf("openai: provider %q uses DeepSeek thinking; effort must be high or max", name)
		}
	} else if effort != "" {
		// Non-DeepSeek backends use OpenAI's reasoning_effort scale (low/medium/
		// high). "max" is a DeepSeek-ism; MiMo et al. reject with 400.
		effort = strings.ToLower(strings.TrimSpace(effort))
		switch effort {
		case "max":
			effort = "high"
		case "low", "medium", "high":
		default:
			return nil, fmt.Errorf("openai: provider %q: effort must be low, medium, or high", name)
		}
	}
	httpClient, err := newHTTPClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("openai: network: %w", err)
	}
	return &client{
		name:           name,
		apiKey:         cfg.APIKey,
		keyEnv:         keyEnv,
		baseURL:        strings.TrimRight(cfg.BaseURL, "/"),
		model:          cfg.Model,
		deepseek:       deepseek,
		minimax:        minimax,
		effort:         effort,
		supportsVision: supportsVision,
		http:           httpClient,
		idleTimeout:    defaultStreamIdleTimeout,
	}, nil
}

func newHTTPClient(cfg provider.Config) (*http.Client, error) {
	spec, ok := cfg.Extra["proxy_spec"].(netclient.ProxySpec)
	if !ok && cfg.Extra["proxy_spec"] != nil {
		return nil, fmt.Errorf("openai: proxy_spec has unexpected type %T", cfg.Extra["proxy_spec"])
	}
	return netclient.NewHTTPClient(spec, netclient.TransportOptions{
		DialTimeout:           30 * time.Second,
		KeepAlive:             30 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second, // models can think for a while before the first token
	})
}

type client struct {
	name           string
	apiKey         string
	keyEnv         string // api_key_env name, surfaced in auth errors
	baseURL        string
	model          string
	http           *http.Client
	deepseek       bool
	minimax        bool          // true for api.minimaxi.com — emits MiniMax-M3's thinking knob instead of reasoning_effort
	effort         string        // reasoning_effort for OpenAI; thinking.type for MiniMax; "" = auto/provider default
	supportsVision bool          // whether the model accepts image_url content blocks
	idleTimeout    time.Duration // SSE stall watchdog window; defaultStreamIdleTimeout unless a test overrides
}

func (c *client) Name() string { return c.name }

func isDeepSeekBaseURL(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "api.deepseek.com" || strings.HasSuffix(host, ".deepseek.com")
}

func isDeepSeekModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "deepseek")
}

// inferSupportsVision heuristically identifies known non-vision models by name.
// Returns false when the model is known to lack vision support, true otherwise.
// Callers should override with an explicit supports_vision config when the
// heuristic is wrong.
func inferSupportsVision(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))

	// DeepSeek non-vision models: deepseek-v4*, deepseek-v3*, deepseek-chat,
	// deepseek-reasoner. DeepSeek models that accept image_url are named
	// deepseek-vl* (vision-language) or deepseek-vision*.
	if strings.HasPrefix(m, "deepseek") {
		if strings.HasPrefix(m, "deepseek-chat") || strings.HasPrefix(m, "deepseek-reasoner") {
			return false
		}
		// deepseek-v4*, deepseek-v3* (but not deepseek-vl*)
		if (strings.Contains(m, "v4") || strings.Contains(m, "v3")) && !strings.Contains(m, "vl") {
			return false
		}
		return true // deepseek-vl*, deepseek-vision*, etc. are vision-capable
	}

	// GLM non-vision models: glm-5.1*, glm-5.2*. glm-4v, glm-4v-plus are vision.
	if strings.HasPrefix(m, "glm-5.1") || strings.HasPrefix(m, "glm-5.2") {
		return false
	}

	return true // default: assume vision-capable
}

// bufPool reuses byte buffers for JSON-marshalled request bodies. Each turn
// allocates a buffer, marshals the request, and sends it — pooling avoids the
// GC churn from repeated alloc/free of ~10-100KB buffers. The pool is
// provider-level (not global) so OpenAI and Anthropic don't compete.
var bufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

func (c *client) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	buf, ok := bufPool.Get().(*bytes.Buffer)
	if !ok {
		return nil, fmt.Errorf("%s: bufPool type mismatch: got %T, want *bytes.Buffer", c.name, buf)
	}
	buf.Reset()
	if err := json.NewEncoder(buf).Encode(c.buildRequest(req)); err != nil {
		bufPool.Put(buf)
		return nil, fmt.Errorf("%s: marshal request: %w", c.name, err)
	}
	body := make([]byte, buf.Len())
	copy(body, buf.Bytes())
	bufPool.Put(buf)

	newReq := func(ctx context.Context) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		httpReq.Header.Set("Accept", "text/event-stream")
		return httpReq, nil
	}
	resp, err := provider.SendWithRetry(ctx, c.http, c.name, c.keyEnv, c.apiKey, newReq)
	if err != nil {
		return nil, err
	}

	out := make(chan provider.Chunk)
	cr := provider.NewChunkRing(ctx, out, 256)
	go c.streamWithReconnect(ctx, resp, newReq, cr)
	return out, nil
}

// maxStreamReconnects bounds how many times a mid-stream connection drop is
// replayed from scratch before the error is surfaced — each replay re-runs the
// whole request (cheap under prompt caching, but not free).
const maxStreamReconnects = 3

// streamWithReconnect drives readStream and, when the connection is cut before
// any model output has been forwarded, replays the request rather than failing
// the turn. Once a token (reasoning/text/tool-call) has been emitted, a replay
// would duplicate output, so the error is surfaced instead.
func (c *client) streamWithReconnect(ctx context.Context, resp *http.Response, newReq func(context.Context) (*http.Request, error), cr *provider.ChunkRing) {
	defer cr.Close()
	for attempt := 0; ; attempt++ {
		emitted, err := c.readStream(ctx, resp, cr)
		if err == nil {
			return
		}
		if !provider.IsConnReset(err) {
			cr.Send(ctx, provider.Chunk{Type: provider.ChunkError, Err: err})
			return
		}
		if emitted {
			cr.Send(ctx, provider.Chunk{Type: provider.ChunkError, Err: &provider.StreamInterruptedError{Err: err}})
			return
		}
		if attempt >= maxStreamReconnects {
			cr.Send(ctx, provider.Chunk{Type: provider.ChunkError, Err: err})
			return
		}
		next, rerr := provider.SendWithRetry(ctx, c.http, c.name, c.keyEnv, c.apiKey, newReq)
		if rerr != nil {
			cr.Send(ctx, provider.Chunk{Type: provider.ChunkError, Err: rerr})
			return
		}
		resp = next
	}
}

func (c *client) buildRequest(req provider.Request) chatRequest {
	// Repair tool-call pairing before sending: an interrupted/resumed history can
	// carry an assistant tool_calls turn whose results never landed, which DeepSeek
	// rejects with a 400 ("must be followed by tool messages …").
	src := provider.SanitizeToolPairing(req.Messages)
	msgs := make([]chatMessage, len(src))
	for i, m := range src {
		cm := chatMessage{
			Role:       string(m.Role),
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		// DeepSeek thinking mode: tool-call turns must round-trip reasoning_content
		// exactly as returned — even when it is an empty string. Omitting the field
		// (Go's omitempty) triggers HTTP 400: "reasoning_content … must be passed
		// back to the API." Pure chat turns between user messages may omit it.
		//
		// 此外，如果不是工具链的 needsReasoningRoundTrip，但是消息角色为 assistant 且 Content 为空而
		// ReasoningContent 不为空（例如普通对话中被截断、无 content 只有 reasoning 时），也必须回传其
		// reasoning_content，以便 DeepSeek 在后续请求中能识别已有的推理，并在此推理基础上继续输出正文。
		if c.deepseek && (needsReasoningRoundTrip(src, i) || (m.Role == provider.RoleAssistant && m.Content == "" && m.ReasoningContent != "")) {
			rc := m.ReasoningContent
			cm.ReasoningContent = &rc
		}
		for _, tc := range m.ToolCalls {
			wire := chatToolCall{ID: tc.ID, Type: "function"}
			wire.Function.Name = tc.Name
			wire.Function.Arguments = tc.Arguments
			cm.ToolCalls = append(cm.ToolCalls, wire)
		}
		if len(m.Parts) > 0 {
			var parts []openaiContentPart
			for _, p := range m.Parts {
				switch p.Type {
				case provider.PartTypeText:
					parts = append(parts, openaiContentPart{Type: "text", Text: p.Text})
				case provider.PartTypeImage:
					if !c.supportsVision {
						// Non-vision model: replace image with placeholder text.
						parts = append(parts, openaiContentPart{Type: "text", Text: "[image]"})
					} else {
						url := p.ImageURL
						if url == "" && p.Image != nil {
							url = p.Image.URL
						}
						if url != "" {
							parts = append(parts, openaiContentPart{Type: "image_url", ImageURL: &openaiImageURL{URL: url, Detail: p.Image.Detail}})
						} else if p.Image != nil && p.Image.Data != "" {
							// Inline base64 data – OpenAI supports data: URIs.
							parts = append(parts, openaiContentPart{Type: "image_url", ImageURL: &openaiImageURL{URL: "data:" + p.Image.Mime + ";base64," + p.Image.Data}})
						}
					}
				case provider.PartTypeAudio:
					if !c.supportsVision {
						// Non-vision model: replace audio with placeholder text.
						parts = append(parts, openaiContentPart{Type: "text", Text: "[audio]"})
					}
					// When supportsVision is true, OpenAI may not support inline audio; skip.
				}
			}
			cm.Content = parts
		} else if m.Content != "" || m.Role != provider.RoleAssistant || len(cm.ToolCalls) == 0 {
			content := m.Content
			cm.Content = &content
		} else {
			cm.Content = nil
		}
		msgs[i] = cm
	}

	var tools []chatTool
	for _, t := range req.Tools {
		tools = append(tools, chatTool{
			Type:     "function",
			Function: chatFunction{Name: t.Name, Description: t.Description, Parameters: t.Parameters},
		})
	}

	out := chatRequest{
		Model:           c.model,
		Messages:        msgs,
		Tools:           tools,
		Stream:          true,
		StreamOptions:   &streamOptions{IncludeUsage: true},
		Temperature:     req.Temperature,
		MaxTokens:       req.MaxTokens,
		ReasoningEffort: c.effort,
	}
	if c.deepseek {
		out.Thinking = &thinkingMode{Type: "enabled"}
	}
	return out
}

// readStream parses one SSE response into chunks: text deltas stream live,
// tool-call fragments accumulate by index and emit complete on [DONE], and a
// ChunkToolCallStart fires the moment a call's name is known. It returns whether
// any model output was forwarded (so the caller can decide a replay is safe) and
// the first fatal error — a nil error means the stream reached [DONE].
func (c *client) readStream(ctx context.Context, resp *http.Response, cr *provider.ChunkRing) (emitted bool, _ error) {
	defer resp.Body.Close()

	// Close the response body when the context is canceled (user interrupt) or the
	// stream stalls past c.idleTimeout, so scanner.Scan() unblocks instead of
	// hanging on a half-open connection. done lets the watchdog exit on a normal
	// return — otherwise it outlives the call and blocks forever on a non-cancellable
	// context whose Done() is nil. The watchdog owns the timer; the read loop only
	// pings the buffered activity channel, so there's no Timer.Reset race.
	idleTimeout := c.idleTimeout
	if idleTimeout <= 0 { // zero-value client (constructed without New)
		idleTimeout = defaultStreamIdleTimeout
	}
	done := make(chan struct{})
	defer close(done)
	activity := make(chan struct{}, 1)
	var stalled atomic.Bool
	go func() {
		idle := time.NewTimer(idleTimeout)
		defer idle.Stop()
		for {
			select {
			case <-ctx.Done():
				resp.Body.Close()
				return
			case <-idle.C:
				log.Printf("[readStream] idle timeout fired after %v — closing body", idleTimeout)
				stalled.Store(true)
				resp.Body.Close()
				return
			case <-activity:
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(idleTimeout)
			case <-done:
				return
			}
		}
	}()

	acc := map[int]*provider.ToolCall{}
	started := map[int]bool{}
	var order []int
	var lastFinishReason string
	var sawDone bool
	// Accumulate one final Usage: OpenAI usage chunk + optional OpenCode
	// inference-cost (authoritative cost + normalizedUsage). Emit once so the
	// agent does not double-count tokens/cost.
	var pendingUsage *provider.Usage

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		select { // ping the idle watchdog only on valid SSE data
		case activity <- struct{}{}:
		default:
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		diag.LogFull("sse-raw", data)
		if data == "[DONE]" {
			sawDone = true
			break
		}

		// OpenCode proprietary cost frame (may have empty choices).
		if u, ok := parseOpenCodeInferenceCost(data); ok {
			pendingUsage = mergeUsagePreferOpenCode(pendingUsage, u)
			continue
		}

		var sr streamResponse
		if err := json.Unmarshal([]byte(data), &sr); err != nil {
			return emitted, fmt.Errorf("%s: decode stream: %w", c.name, err)
		}
		if sr.Error != nil {
			return emitted, fmt.Errorf("%s: %s", c.name, sr.Error.Message)
		}
		if len(sr.Choices) > 0 && sr.Choices[0].FinishReason != nil && *sr.Choices[0].FinishReason != "" {
			lastFinishReason = *sr.Choices[0].FinishReason
		}
		if sr.Usage != nil {
			u := normaliseUsage(sr.Usage)
			u.FinishReason = lastFinishReason
			pendingUsage = mergeUsagePreferOpenCode(pendingUsage, u)
		}
		if len(sr.Choices) == 0 {
			continue
		}

		delta := sr.Choices[0].Delta
		if delta.ReasoningContent != "" {
			emitted = true
			if !cr.Send(ctx, provider.Chunk{Type: provider.ChunkReasoning, Text: delta.ReasoningContent}) {
				return emitted, ctx.Err()
			}
		}
		if delta.Content != "" {
			emitted = true
			if !cr.Send(ctx, provider.Chunk{Type: provider.ChunkText, Text: delta.Content}) {
				return emitted, ctx.Err()
			}
		}
		for _, tc := range delta.ToolCalls {
			cur, ok := acc[tc.Index]
			if !ok {
				cur = &provider.ToolCall{}
				acc[tc.Index] = cur
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Function.Name != "" {
				cur.Name = tc.Function.Name
			}
			cur.Arguments += tc.Function.Arguments
			// Signal the call's start the moment its name is known, so a frontend
			// can show the tool card immediately rather than only after its
			// (possibly large) arguments finish streaming.
			if !started[tc.Index] && cur.Name != "" {
				started[tc.Index] = true
				emitted = true
				if !cr.Send(ctx, provider.Chunk{Type: provider.ChunkToolCallStart, ToolCall: &provider.ToolCall{ID: cur.ID, Name: cur.Name}}) {
					return emitted, ctx.Err()
				}
			}
		}
	}

	if stalled.Load() {
		return emitted, fmt.Errorf("%s: stream stalled — no data for %s, connection likely dropped", c.name, idleTimeout)
	}
	if err := scanner.Err(); err != nil {
		return emitted, fmt.Errorf("%s: read stream: %w", c.name, err)
	}
	if !sawDone {
		err := io.ErrUnexpectedEOF
		if emitted {
			return emitted, &provider.StreamInterruptedError{Err: fmt.Errorf("%s: read stream: %w", c.name, err)}
		}
		return emitted, fmt.Errorf("%s: read stream: %w", c.name, err)
	}

	// Single usage emission after stream body (merged OpenAI + OpenCode cost).
	if pendingUsage != nil {
		if pendingUsage.FinishReason == "" {
			pendingUsage.FinishReason = lastFinishReason
		}
		pendingUsage.NormalizeCache()
		emitted = true
		if !cr.Send(ctx, provider.Chunk{Type: provider.ChunkUsage, Usage: pendingUsage}) {
			return emitted, ctx.Err()
		}
	}

	sort.Ints(order)
	for _, idx := range order {
		tc := acc[idx]
		if tc.ID == "" {
			// Some OpenAI-compatible gateways stream tool calls by index with no id.
			// Synthesize a stable one so the result can be paired back to its call —
			// an empty tool_call_id collapses multi-tool turns downstream.
			tc.ID = fmt.Sprintf("call_%d", idx)
		}
		if !cr.Send(ctx, provider.Chunk{Type: provider.ChunkToolCall, ToolCall: tc}) {
			return emitted, ctx.Err()
		}
	}
	if !cr.Send(ctx, provider.Chunk{Type: provider.ChunkDone}) {
		return emitted, ctx.Err()
	}
	return emitted, nil
}

// normaliseUsage folds the two cache-hit shapes the OpenAI-compatible ecosystem
// uses into a single Usage: DeepSeek puts prompt_cache_{hit,miss}_tokens at the
// top of usage; OpenAI and MiMo put it nested under prompt_tokens_details.
// Whichever side reports non-zero wins; miss is derived when only hit is given.
// Reasoning tokens land in completion_tokens_details on thinking-mode models.
func normaliseUsage(u *wireUsage) *provider.Usage {
	hit := u.PromptCacheHitTokens
	miss := u.PromptCacheMissTokens
	if hit == 0 && u.PromptTokensDetails != nil {
		hit = u.PromptTokensDetails.CachedTokens
		miss = 0 // 使用嵌套字段时，强制 miss 从同一来源推导
	}
	if miss == 0 && hit > 0 && u.PromptTokens > hit {
		miss = u.PromptTokens - hit
	}
	// Do NOT invent miss=prompt when both zero — that fakes cache rate.
	// OpenCode usually sends prompt_cache_* ; if only totals arrive, leave hit/miss 0.
	reasoning := 0
	if u.CompletionTokensDetails != nil {
		reasoning = u.CompletionTokensDetails.ReasoningTokens
	}
	out := &provider.Usage{
		PromptTokens:          u.PromptTokens,
		CompletionTokens:      u.CompletionTokens,
		TotalTokens:           u.TotalTokens,
		CacheHitTokens:        hit,
		CacheMissTokens:       miss,
		ReasoningTokens:       reasoning,
		CacheBreakdownKnown:   hit+miss > 0,
	}
	out.NormalizeCache()
	return out
}

// openCodeInferenceCost is the proprietary SSE frame after chat.completion usage:
//
//	{"choices":[],"x-opencode-type":"inference-cost","cost":"0.00020880",
//	 "normalizedUsage":{"inputTokens":88,"outputTokens":16,"reasoningTokens":16,
//	 "cacheReadTokens":0,"cacheWrite5mTokens":0,"cacheWrite1hTokens":0}}
type openCodeInferenceCost struct {
	Type            string `json:"x-opencode-type"`
	Cost            string `json:"cost"`
	NormalizedUsage *struct {
		InputTokens        int `json:"inputTokens"`
		OutputTokens       int `json:"outputTokens"`
		ReasoningTokens    int `json:"reasoningTokens"`
		CacheReadTokens    int `json:"cacheReadTokens"`
		CacheWrite5mTokens int `json:"cacheWrite5mTokens"`
		CacheWrite1hTokens int `json:"cacheWrite1hTokens"`
	} `json:"normalizedUsage"`
}

func parseOpenCodeInferenceCost(data string) (*provider.Usage, bool) {
	if !strings.Contains(data, "inference-cost") && !strings.Contains(data, "x-opencode-type") {
		return nil, false
	}
	var ice openCodeInferenceCost
	if err := json.Unmarshal([]byte(data), &ice); err != nil {
		return nil, false
	}
	if ice.Type != "inference-cost" {
		return nil, false
	}
	u := &provider.Usage{}
	if ice.NormalizedUsage != nil {
		n := ice.NormalizedUsage
		u.PromptTokens = n.InputTokens
		u.CompletionTokens = n.OutputTokens
		u.ReasoningTokens = n.ReasoningTokens
		u.CacheHitTokens = n.CacheReadTokens
		write := n.CacheWrite5mTokens + n.CacheWrite1hTokens
		u.CacheWriteTokens = write
		// Uncached input = input - cache read (clamp).
		if n.InputTokens > n.CacheReadTokens {
			u.CacheMissTokens = n.InputTokens - n.CacheReadTokens
		}
		if n.CacheReadTokens > 0 || u.CacheMissTokens > 0 {
			u.CacheBreakdownKnown = true
		}
		u.TotalTokens = n.InputTokens + n.OutputTokens
	}
	if c, err := strconv.ParseFloat(strings.TrimSpace(ice.Cost), 64); err == nil && c > 0 {
		u.ReportedCost = c
		// OpenCode zen bills in USD on the wire; keep symbol explicit.
		u.ReportedCurrency = "$"
	}
	u.NormalizeCache()
	return u, true
}

// mergeUsagePreferOpenCode folds b into a. OpenCode-reported cost and any
// non-zero token fields from b win; earlier OpenAI usage fills gaps.
func mergeUsagePreferOpenCode(a, b *provider.Usage) *provider.Usage {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := *a
	if b.PromptTokens > 0 {
		out.PromptTokens = b.PromptTokens
	}
	if b.CompletionTokens > 0 {
		out.CompletionTokens = b.CompletionTokens
	}
	if b.TotalTokens > 0 {
		out.TotalTokens = b.TotalTokens
	}
	if b.ReasoningTokens > 0 {
		out.ReasoningTokens = b.ReasoningTokens
	}
	if b.CacheBreakdownKnown || b.CacheHitTokens+b.CacheMissTokens > 0 {
		out.CacheHitTokens = b.CacheHitTokens
		out.CacheMissTokens = b.CacheMissTokens
		out.CacheWriteTokens = b.CacheWriteTokens
		out.CacheBreakdownKnown = true
	}
	if b.ReportedCost > 0 {
		out.ReportedCost = b.ReportedCost
		if b.ReportedCurrency != "" {
			out.ReportedCurrency = b.ReportedCurrency
		}
	}
	if b.FinishReason != "" {
		out.FinishReason = b.FinishReason
	}
	out.NormalizeCache()
	return &out
}

// --- OpenAI-compatible wire protocol ---

type chatRequest struct {
	Model           string         `json:"model"`
	Messages        []chatMessage  `json:"messages"`
	Tools           []chatTool     `json:"tools,omitempty"`
	Stream          bool           `json:"stream"`
	StreamOptions   *streamOptions `json:"stream_options,omitempty"`
	Temperature     float64        `json:"temperature,omitempty"`
	MaxTokens       int            `json:"max_tokens,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	Thinking        *thinkingMode  `json:"thinking,omitempty"`
}

type thinkingMode struct {
	Type string `json:"type"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role string `json:"role"`
	// content holds either a plain string or an array of content parts for
	// multimodal messages. When m.Parts is non-empty, buildRequest sets this
	// to []openaiContentPart; otherwise it falls back to the Content string.
	Content    interface{}    `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
	// Pointer so an intentional empty string serializes as "reasoning_content":""
	// for tool-call turns; omitempty on a plain string would drop the field and
	// DeepSeek returns HTTP 400.
	ReasoningContent *string `json:"reasoning_content,omitempty"`
}

// openaiContentPart is a single content block within a message's content array.
type openaiContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openaiImageURL `json:"image_url,omitempty"`
}

// openaiImageURL is the image_url object for an image content part.
type openaiImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type chatToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type streamResponse struct {
	Choices []struct {
		Delta struct {
			Content          string         `json:"content"`
			ReasoningContent string         `json:"reasoning_content"`
			ToolCalls        []chatToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// wireUsage covers both DeepSeek's top-level cache fields and the
// OpenAI/MiMo nested details — normaliseUsage chooses whichever side
// reports values.
type wireUsage struct {
	PromptTokens          int `json:"prompt_tokens"`
	CompletionTokens      int `json:"completion_tokens"`
	TotalTokens           int `json:"total_tokens"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
	PromptTokensDetails   *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// IsModelFetchEndpointMiss returns true when the error indicates a 404 (endpoint not found).
func IsModelFetchEndpointMiss(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "status 404")
}
