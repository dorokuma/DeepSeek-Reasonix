// Package tool defines the Tool abstraction and a Registry. Built-in tools live
// in tool/builtin and self-register via init(); plugin-provided tools are added
// to a runtime Registry alongside the enabled built-ins. The agent sees only a
// *Registry, never the global built-in set directly.
package tool

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"reasonix/internal/diff"
	"reasonix/internal/provider"
)

// Tool is a capability the model can invoke.
type Tool interface {
	Name() string
	Description() string
	// Schema returns the JSON Schema for the tool's parameters.
	Schema() json.RawMessage
	// Execute parses the model-generated raw JSON args and returns result text
	// to feed back to the model.
	Execute(ctx context.Context, args json.RawMessage) (string, error)
	// ReadOnly reports whether the tool has no observable side effects on the
	// host. The agent parallelises a batch of tool calls only when every call
	// in the batch is ReadOnly; mixed batches stay sequential so write/read
	// ordering is preserved. bash and plugin tools must return false because
	// their effects can't be inferred statically from args.
	ReadOnly() bool
}

// Concurrenter is an optional capability a Tool may implement to signal that
// it is safe to run concurrently with other tools even though it is not
// ReadOnly.
type Concurrenter interface {
	Concurrent() bool
}

// OmitFromModelSchema marks tools that stay in the registry (so history validators
// and Execute can resolve the name) but must never be advertised in provider tool
// schemas. Prefer not introducing phantom tools at all — history should not train
// models to invent calls.
type OmitFromModelSchema interface {
	OmitFromModelSchema() bool
}

// omitFromModel reports whether t must stay out of model-facing schemas/lists.
func omitFromModel(t Tool) bool {
	if t == nil {
		return false
	}
	if o, ok := t.(OmitFromModelSchema); ok && o.OmitFromModelSchema() {
		return true
	}
	return false
}

// Previewer is an optional capability a writer Tool may implement: given the
// same raw JSON args Execute would receive, compute the file change the call
// *would* make — without touching disk. A front-end uses it to show an approval
// card or a changed-files panel before the call runs (the permission gate, not
// Preview, decides whether it may proceed). Type-assert a Tool to Previewer to
// discover support; the file-writing built-ins implement it, most tools do not.
type Previewer interface {
	Preview(args json.RawMessage) (diff.Change, error)
}

// PostCallGuidance is an optional capability a Tool may implement. When a
// successful call returns a non-empty string from PostCallGuidance, the agent
// appends it (as a block) to the tool result the model sees — so the model is
// taught what it must do next: re-read a sidecar, include the result in the
// final answer, call a follow-up tool, etc. The method receives the raw JSON
// args so guidance can reference concrete values (file paths, ids, names).
// A tool without PostCallGuidance leaves no trace in the result.
type PostCallGuidance interface {
	PostCallGuidance(args json.RawMessage) string
}

// PostCallGuidanceWithResult is an optional extension to PostCallGuidance.
// When implemented, the agent calls PostCallGuidanceAfter with the successful
// Execute return value instead of PostCallGuidance alone, so guidance can cite
// dynamic ids.
type PostCallGuidanceWithResult interface {
	PostCallGuidanceAfter(args json.RawMessage, result string) string
}

// GuidancePrefixer is an optional extension to PostCallGuidance. When
// implemented by the same tool, the agent uses the returned prefix
// instead of the default "⚠ **Post-call requirements**". Return ""
// to keep the default.
type GuidancePrefixer interface {
	GuidancePrefix() string
}

// PreviewChange returns the change a writer tool would make for args, or ok=false
// when there's nothing renderable: t is read-only, doesn't implement Previewer,
// the preview errored (the edit will likely fail too), or the file is binary.
func PreviewChange(t Tool, args json.RawMessage) (diff.Change, bool) {
	if t == nil || t.ReadOnly() {
		return diff.Change{}, false
	}
	pv, ok := t.(Previewer)
	if !ok {
		return diff.Change{}, false
	}
	ch, err := pv.Preview(args)
	if err != nil || ch.Binary {
		return diff.Change{}, false
	}
	return ch, true
}

// --- process-global built-in set (populated by builtin subpackage init) ---

var (
	builtinsMu sync.RWMutex
	builtins   = map[string]Tool{}
)

// RegisterBuiltin registers a compile-time built-in tool. Intended for init().
// It panics on a duplicate name, which is a compile-time wiring mistake.
func RegisterBuiltin(t Tool) {
	builtinsMu.Lock()
	defer builtinsMu.Unlock()
	name := t.Name()
	if _, dup := builtins[name]; dup {
		panic("tool: duplicate built-in " + name)
	}
	builtins[name] = t
}

// Builtins returns all registered built-in tools, sorted by name.
func Builtins() []Tool {
	builtinsMu.RLock()
	defer builtinsMu.RUnlock()
	names := make([]string, 0, len(builtins))
	for n := range builtins {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Tool, 0, len(names))
	for _, n := range names {
		out = append(out, builtins[n])
	}
	return out
}

type ctrlKey struct{}

func WithCtrl(ctx context.Context, ctrl any) context.Context {
	return context.WithValue(ctx, ctrlKey{}, ctrl)
}

func CtrlFromContext(ctx context.Context) (any, bool) {
	c := ctx.Value(ctrlKey{})
	return c, c != nil
}

type callIDKey struct{}

func WithCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callIDKey{}, id)
}

func CallIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(callIDKey{}).(string)
	return id, ok
}

// LookupBuiltin returns a registered built-in by name.
func LookupBuiltin(name string) (Tool, bool) {
	builtinsMu.RLock()
	defer builtinsMu.RUnlock()
	t, ok := builtins[name]
	return t, ok
}

// --- per-run registry instance ---

// Registry is a per-run set of tools: enabled built-ins plus plugin tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
	canon map[string]json.RawMessage
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}, canon: map[string]json.RawMessage{}}
}

// Add inserts (or replaces) a tool, preserving first-seen order. The schema is
// canonicalized once here — it never changes after registration, so Schemas()
// (called every turn) reuses the result instead of re-marshaling.
func (r *Registry) Add(t Tool) {
	if t == nil {
		return
	}
	name := t.Name()
	if name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.tools[name]; !ok {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
	r.canon[name] = provider.CanonicalizeSchema(t.Schema())
}

// MCPNamePrefix is the namespace every MCP tool name carries: the
// model-visible name is "mcp_<server>_<tool>".
const MCPNamePrefix = "mcp_"

// mcpPrefixes holds the list of registered MCP server tool-name prefixes
// (e.g. "mcp_jina_", "mcp_codegraph_"). Loaded by SplitMCPName for dynamic
// prefix routing. The atomic.Value stores a []string.
var mcpPrefixes atomic.Value

// RegisterMCPPrefixes sets the list of active MCP server prefixes used by
// SplitMCPName to resolve tool names. Call this after any server connect or
// disconnect so the prefix list stays in sync. Passing nil or an empty slice
// disables all MCP name resolution.
func RegisterMCPPrefixes(prefixes []string) {
	mcpPrefixes.Store(prefixes)
}

// SplitMCPName splits a model-visible MCP tool name "mcp_<server>_<tool>" into
// its server and tool parts using the registered prefix list. ok is false for
// non-MCP (built-in) names and for names that don't match any registered
// prefix.
func SplitMCPName(name string) (server, tool string, ok bool) {
	if !strings.HasPrefix(name, MCPNamePrefix) {
		return "", "", false
	}
	raw := mcpPrefixes.Load()
	prefixes, ok := raw.([]string)
	if !ok || len(prefixes) == 0 {
		return "", "", false
	}
	// Find the longest matching prefix
	best := ""
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) && len(p) > len(best) {
			best = p
		}
	}
	if best == "" {
		return "", "", false
	}
	tool = strings.TrimPrefix(name, best)
	if tool == "" {
		return "", "", false
	}
	server = best[len(MCPNamePrefix) : len(best)-1]
	return server, tool, true
}

// RemovePrefix unregisters every tool whose name starts with prefix — used to
// drop an MCP server's "mcp_<server>_" namespace when it's disconnected — and
// returns the count removed.
func (r *Registry) RemovePrefix(prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	kept := r.order[:0]
	removed := 0
	for _, name := range r.order {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
			delete(r.canon, name)
			removed++
			continue
		}
		kept = append(kept, name)
	}
	r.order = kept
	return removed
}

// Get looks up a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.tools[name]
	return t, ok
}

// Len returns the number of registered tools.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.order)
}

// Names returns the registered tool names in insertion order.
// System-only tools (OmitFromModelSchema) are excluded so "available tools"
// errors never advertise names the model must not call.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.order))
	for _, name := range r.order {
		if t := r.tools[name]; omitFromModel(t) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// Suggest finds the closest registered tool name when an exact match fails.
// It normalises the input (lowercase, underscores) and falls back to edit-distance
// matching. Returns ("", false) when no tool is close enough.
func (r *Registry) Suggest(name string) (string, bool) {
	// Common English verb-to-tool mappings for natural language expressions.
	verbMap := map[string]string{
		"read": "read_file", "view": "read_file", "look": "read_file", "show": "read_file",
		"write": "write_file", "create": "write_file", "save": "write_file",
		"edit": "edit_file", "change": "edit_file", "modify": "edit_file", "update": "edit_file",
		"find": "grep", "search": "grep", "locate": "grep",
		"run": "bash", "execute": "bash", "shell": "bash",
		"list": "ls", "dir": "ls",
		"install": "install_skill", "delete": "delete_range", "remove": "delete_range",
		"remember": "remember", "forget": "forget", "recall": "recall",
		"memory_save": "remember", "memory_forget": "forget", "memory_get": "recall",
		"memory": "recall",
	}

	// Normalise: lowercase, collapse whitespace, replace non-alnum with underscores
	norm := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	var lastUnderscore bool
	for _, r := range norm {
		if r == ' ' || r == '-' || r == '_' {
			if !lastUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				lastUnderscore = true
			}
		} else if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
			lastUnderscore = false
		}
	}
	normalized := strings.TrimSuffix(b.String(), "_")

	r.mu.RLock()
	defer r.mu.RUnlock()

	// Try exact match on normalized name first (never suggest system-only tools).
	if t, ok := r.tools[normalized]; ok && !omitFromModel(t) {
		return normalized, true
	}

	// Verb-map check: if the first word maps to a registered tool, return it.
	inputWords := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	if len(inputWords) > 0 {
		if mapped, ok := verbMap[inputWords[0]]; ok {
			if t, exists := r.tools[mapped]; exists && !omitFromModel(t) {
				return mapped, true
			}
		}
	}

	// Build a bag of significant words from the input (skip very short words)
	var sigInput []string
	for _, w := range inputWords {
		if len(w) > 2 {
			sigInput = append(sigInput, w)
		}
	}

	// Score each registered tool: count overlapping significant words + edit distance
	type scored struct {
		name  string
		score int
	}
	var best scored
	bestSet := false

	for candidate, t := range r.tools {
		if omitFromModel(t) {
			continue
		}
		candWords := strings.FieldsFunc(candidate, func(r rune) bool {
			return r == '_' || !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
		})

		// Word overlap bonus
		overlap := 0
		for _, iw := range sigInput {
			for _, cw := range candWords {
				if iw == cw {
					overlap += 4
				} else if (len(iw) >= 3 && len(cw) >= 3) && (strings.Contains(cw, iw) || strings.Contains(iw, cw)) {
					overlap += 2
				} else if (len(iw) >= 4 || len(cw) >= 4) && (strings.HasPrefix(cw, iw[:min(3, len(iw))]) || strings.HasPrefix(iw, cw[:min(3, len(cw))])) {
					overlap += 1
				}
			}
		}

		// Penalty: edit distance, and modest bonus for shorter tool names
		dist := levenshtein(normalized, candidate)
		shortBonus := 0
		if len(candidate) <= 4 {
			shortBonus = 2 // small bonus for very short names like ls, grep
		}
		score := overlap*8 - dist + shortBonus

		if !bestSet || score > best.score {
			best = scored{candidate, score}
			bestSet = true
		}
	}

	// Threshold for accepting a suggestion: must share at least one meaningful
	// character with the input and pass the score threshold.
	if bestSet && best.score > -10 {
		// Sanity: the suggestion must share at least one word-level substring
		// with the input (avoid suggesting "ls" or "note" for complete gibberish).
		for _, iw := range sigInput {
			for _, cw := range strings.FieldsFunc(best.name, func(r rune) bool {
				return r == '_' || !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
			}) {
				if strings.Contains(iw, cw) || strings.Contains(cw, iw) {
					return best.name, true
				}
			}
		}
	}
	return "", false
}

// levenshtein computes the edit distance between two strings.
func levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	// Use single-row DP for O(min(la,lb)) memory
	if la > lb {
		a, b = b, a
		la, lb = lb, la
	}
	prev := make([]int, la+1)
	for i := range prev {
		prev[i] = i
	}
	for j := 1; j <= lb; j++ {
		curr := make([]int, la+1)
		curr[0] = j
		for i := 1; i <= la; i++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			curr[i] = min(curr[i-1]+1, min(prev[i]+1, prev[i-1]+cost))
		}
		prev = curr
	}
	return prev[la]
}

// Schemas exports tool definitions in stable name order for the provider.
// Tools implementing OmitFromModelSchema are kept in the registry but omitted
// here so the model never sees them as invocable.
func (r *Registry) Schemas() []provider.ToolSchema {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, len(r.order))
	copy(names, r.order)
	sort.Strings(names)

	out := make([]provider.ToolSchema, 0, len(names))
	for _, name := range names {
		t := r.tools[name]
		if t == nil || omitFromModel(t) {
			continue
		}
		out = append(out, provider.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  r.canon[name],
		})
	}
	return out
}
