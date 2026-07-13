// Package permission decides, per tool call, whether to allow it, deny it, or
// ask the user first. The core is a pure Policy (rule evaluation, no I/O); a
// Gate wraps a Policy with an optional interactive Approver and is what the
// agent consults at execute time. Keeping rule evaluation pure makes it
// trivially testable and keeps the agent independent of how "ask" is resolved.
package permission

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// Decision is the outcome of evaluating a tool call against a Policy.
type Decision int

const (
	// Allow runs the tool without prompting.
	Allow Decision = iota
	// Ask defers to an interactive Approver (or, with none, resolves to Allow).
	Ask
	// Deny blocks the tool in every mode.
	Deny
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	case Deny:
		return "deny"
	default:
		return "unknown"
	}
}

// ParseDecision maps a config string to a Decision. Unknown / empty input
// defaults to Ask — the conservative posture for a writer fallback.
func ParseDecision(s string) Decision {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return Allow
	case "deny":
		return Deny
	default:
		return Ask
	}
}

// Rule matches tool calls. Tool is the tool name; Subject, when non-empty,
// constrains the call's subject. A glob Subject (see matchGlob) matches by
// wildcard; a Literal Subject matches by exact string equality. An empty Subject
// matches every call to Tool.
type Rule struct {
	Tool    string
	Subject string
	// Literal matches Subject by exact equality rather than as a glob, so a
	// remembered concrete command keeps any '*'/'?' as ordinary characters
	// instead of turning them into wildcards.
	Literal bool
}

// ParseRule parses "ToolName", "ToolName(glob)", or "ToolName=literal".
// Surrounding whitespace is trimmed. The "=literal" form (taken when the '='
// precedes any '(') matches the rest of the string verbatim — no globbing —
// which is how remembered approvals are stored so a command's punctuation can't
// widen the rule. ok is false for a malformed entry (empty tool name) so the
// caller can warn rather than silently install a rule that matches nothing.
func ParseRule(s string) (Rule, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Rule{}, false
	}
	if eq := strings.IndexByte(s, '='); eq > 0 {
		if paren := strings.IndexByte(s, '('); paren < 0 || eq < paren {
			tool := strings.TrimSpace(s[:eq])
			if tool == "" {
				return Rule{}, false
			}
			return Rule{Tool: tool, Subject: s[eq+1:], Literal: true}, true
		}
	}
	if i := strings.IndexByte(s, '('); i >= 0 && strings.HasSuffix(s, ")") {
		tool := strings.TrimSpace(s[:i])
		if tool == "" {
			return Rule{}, false
		}
		return Rule{Tool: tool, Subject: s[i+1 : len(s)-1]}, true
	}
	return Rule{Tool: s}, true
}

func parseRules(ss []string) []Rule {
	var out []Rule
	for _, s := range ss {
		if r, ok := ParseRule(s); ok {
			out = append(out, r)
		}
	}
	return out
}

// Policy is a set of rules plus the writer fallback mode. It is the pure,
// I/O-free heart of the permission layer.
type Policy struct {
	// Mode is the fallback decision for writer tools when no rule matches.
	// Read-only tools always fall back to Allow.
	Mode  Decision
	Allow []Rule
	Ask   []Rule
	Deny  []Rule
}

// New builds a Policy from config string slices and a mode string ("ask" by
// default). Malformed rule strings are dropped.
func New(mode string, allow, ask, deny []string) Policy {
	return Policy{
		Mode:  ParseDecision(mode),
		Allow: parseRules(allow),
		Ask:   parseRules(ask),
		Deny:  parseRules(deny),
	}
}

// toolCategories maps a category name to the set of concrete tool names it
// covers. A permission rule can use a category name (e.g. "Edit") in place of a
// concrete tool name so that one rule can gate every tool in the category.
var toolCategories = map[string][]string{
	"Edit": {"move_file", "write_file"},
}

// resolveToolNames expands a tool or category name into the set of concrete
// tool names it matches. A bare tool name returns itself; a category name
// returns all tools in that category.
func resolveToolNames(name string) []string {
	if tools, ok := toolCategories[name]; ok {
		return tools
	}
	return []string{name}
}

// categoriesFor returns all category names that include the given tool name.
func categoriesFor(toolName string) []string {
	var cats []string
	for cat, tools := range toolCategories {
		for _, t := range tools {
			if t == toolName {
				cats = append(cats, cat)
				break
			}
		}
	}
	return cats
}

// Decide evaluates a tool call. readOnly is the tool's own classification; args
// is the raw JSON the model sent, from which the call's subject is extracted
// for glob matching. Precedence: deny > ask > allow > fallback (Allow for
// readers, Mode for writers).
func (p Policy) Decide(toolName string, readOnly bool, args json.RawMessage) Decision {
	// Resolve the concrete tool names this call maps to (including category expansion).
	concreteNames := resolveToolNames(toolName)
	// Also include any categories that cover this tool name.
	concreteNames = append(concreteNames, categoriesFor(toolName)...)

	subject := Subject(args)
	subjects := Subjects(args)
	if len(subjects) == 0 {
		subjects = []string{subject}
	}

	// Deny: if any (rule, toolName, subject) triple matches, deny immediately.
	for _, name := range concreteNames {
		for _, s := range subjects {
			if matchAny(p.Deny, name, s) {
				return Deny
			}
		}
	}

	// Ask: if any subject triggers Ask and none trigger Deny.
	for _, name := range concreteNames {
		for _, s := range subjects {
			if matchAny(p.Ask, name, s) {
				return Ask
			}
		}
	}

	// Allow: every subject must match either an Allow rule or have no subject
	// (bare tool name match). For multi-subject tools, each subject needs its
	// own permission.
	if len(subjects) > 0 {
		allAllowed := true
		for _, s := range subjects {
			matched := false
			for _, name := range concreteNames {
				if matchAny(p.Allow, name, s) {
					matched = true
					break
				}
			}
			if !matched && (s != "" || !matchAny(p.Allow, toolName, "")) {
				allAllowed = false
				break
			}
		}
		if allAllowed {
			return Allow
		}
	} else if matchAny(p.Allow, toolName, subject) {
		return Allow
	}

	if readOnly {
		return Allow
	}
	return p.Mode
}

// matchAny reports whether any rule matches the (toolName, subject) pair. A
// subject-specific rule cannot match a call that exposes no subject.
func matchAny(rules []Rule, toolName, subject string) bool {
	for _, r := range rules {
		if r.Tool != toolName {
			continue
		}
		if r.Subject == "" {
			return true
		}
		if subject == "" {
			continue
		}
		if r.Literal {
			if r.Subject == subject {
				return true
			}
			continue
		}
		if matchGlob(r.Subject, subject) {
			return true
		}
	}
	return false
}

// subjectKeys are the JSON argument keys, in priority order, that carry a tool
// call's "subject" — the thing a Subject glob matches against. Generic so tools
// need not implement a permission-specific method: bash exposes command, the
// file tools expose path / file_path, grep & glob expose pattern.
// task_name/message cover spawn_agent (and similar) so approval UIs get a
// readable subject instead of an empty string.
var subjectKeys = []string{"source_path", "command", "file_path", "path", "pattern", "task_name", "message"}

// Subject extracts the matchable subject string from a call's raw JSON args,
// returning "" when none of the known keys is present (such a call only matches
// bare "ToolName" rules).
func Subject(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return ""
	}
	for _, k := range subjectKeys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// subjectMultiKeys are JSON argument keys that may carry multiple subject values
// for tools that operate on more than one path (e.g. move_file).
var subjectMultiKeys = []string{"source_path", "destination_path"}

// Subjects extracts every subject value from a call's raw JSON args. For most
// tools this returns a single-element slice (the same value as Subject). For
// tools that reference multiple paths (e.g. move_file with source_path and
// destination_path), it returns all of them so each path is checked against
// permission rules independently.
func Subjects(args json.RawMessage) []string {
	if len(args) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return nil
	}
	// First check multi-key subjects.
	var subs []string
	for _, k := range subjectMultiKeys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				subs = append(subs, s)
			}
		}
	}
	if len(subs) > 0 {
		return subs
	}
	// Fall back to the single subject keys.
	for _, k := range subjectKeys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return []string{s}
			}
		}
	}
	return nil
}

// truncate returns s truncated to max runes, appending "..." if truncated.
func truncate(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "..."
}

// Preview generates a human-readable one-line summary of a tool call for
// approval dialogs. Different tools use different fields; for unknown tools
// it falls back to Subject(args).
func Preview(tool string, args json.RawMessage) string {
	if len(args) == 0 {
		return tool + " 调用"
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return tool + " 调用"
	}
	switch tool {
	case "bash":
		if v, ok := m["command"]; ok {
			if s, ok := v.(string); ok {
				return truncate(s, 200)
			}
		}
		return "bash 调用"
	case "spawn_agent":
		if v, ok := m["task_name"]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
		if v, ok := m["message"]; ok {
			if s, ok := v.(string); ok {
				return "任务: " + truncate(s, 150)
			}
		}
		return "spawn_agent 调用"
	case "write_file":
		if v, ok := m["file_path"]; ok {
			if s, ok := v.(string); ok && s != "" {
				return "写入: " + s
			}
		}
		return "write_file 调用"
	case "edit_file":
		if v, ok := m["file_path"]; ok {
			if s, ok := v.(string); ok && s != "" {
				return "编辑: " + s
			}
		}
		return "edit_file 调用"
	case "multi_edit":
		if v, ok := m["edits"]; ok {
			if edits, ok := v.([]any); ok && len(edits) > 0 {
				if first, ok := edits[0].(map[string]any); ok {
					if p, ok := first["file_path"]; ok {
						if s, ok := p.(string); ok && s != "" {
							return "批量编辑: " + s
						}
					}
				}
				return fmt.Sprintf("批量编辑 %d 处", len(edits))
			}
		}
		return "multi_edit 调用"
	default:
		s := Subject(args)
		if s != "" {
			return s
		}
		return tool + " 调用"
	}
}

// matchGlob reports whether name matches pattern, where '*' matches any run of
// characters (including separators) and '?' matches exactly one. Unlike
// path.Match, '*' is not stopped by '/', which is what command-line and path
// prefixes ("rm -rf*", "/etc/*") intuitively expect. Linear time with
// backtracking, byte-oriented.
func matchGlob(pattern, name string) bool {
	var px, nx, starPx, starNx int
	starPx = -1
	for nx < len(name) {
		switch {
		case px < len(pattern) && (pattern[px] == '?' || pattern[px] == name[nx]):
			px++
			nx++
		case px < len(pattern) && pattern[px] == '*':
			starPx = px
			starNx = nx
			px++
		case starPx != -1:
			px = starPx + 1
			starNx++
			nx = starNx
		default:
			return false
		}
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}

// Approver resolves an Ask decision interactively. Implementations live in the
// front-end (the chat TUI); a non-interactive run passes a nil Approver, which
// the Gate treats as "allow" to preserve autonomous behaviour.
type Approver interface {
	// Approve asks the user about a pending call. It returns whether to allow
	// it and whether to remember that choice as a new rule. A non-nil err (e.g.
	// the context was cancelled while waiting) aborts the turn.
	Approve(ctx context.Context, toolName, subject string, args json.RawMessage) (allow, remember bool, err error)
}

// Gate is what the agent consults at execute time: a Policy plus an optional
// Approver. It satisfies the agent's Gate interface structurally.
type Gate struct {
	Policy   Policy
	Approver Approver

	// OnRemember, when set, is invoked with a new allow rule the user chose to
	// remember (e.g. "bash=go build"), so the front-end can persist it.
	OnRemember func(rule string)

	// BlockedTools maps tool name → custom block message. When set, Check()
	// returns blocked with the custom message before policy evaluation, so the
	// LLM gets a context-specific reason instead of a generic "denied".
	BlockedTools map[string]string
}

// NewGate wires a Policy to an Approver (nil for non-interactive use).
func NewGate(p Policy, a Approver) *Gate { return &Gate{Policy: p, Approver: a} }

// SetApprover replaces the Approver on an existing Gate. Used to inject
// interactive approval into sub-agent gates that were created headless.
func (g *Gate) SetApprover(a Approver) {
	g.Approver = a
}


// SetBlockedTools replaces the BlockedTools map on the Gate. The controller
// calls this before each turn to block dynamic tools when they
// are not supposed to be visible.
func (g *Gate) SetBlockedTools(blocked map[string]string) {
	if g == nil {
		return
	}
	g.BlockedTools = blocked
}
// Check decides whether a tool call may run. It is the method the agent's Gate
// interface expects. A denied or refused call returns allow=false with a short
// reason the agent feeds back to the model.
func (g *Gate) Check(ctx context.Context, toolName string, args json.RawMessage, readOnly bool) (bool, string, error) {
	if toolName == "bash" && !readOnly {
		subject := Subject(args)
		if isReadOnlyBashSubject(subject) {
			readOnly = true
		}
	}

	// Dynamic block: checked before policy rules so custom messages take priority.
	if g.BlockedTools != nil {
		if msg, ok := g.BlockedTools[toolName]; ok {
			return false, msg, nil
		}
	}
	switch g.Policy.Decide(toolName, readOnly, args) {
	case Deny:
		return false, "denied by permission policy — this tool/command is on the deny list. Do not retry it; choose another approach or stop and explain.", nil
	case Ask:
		if g.Approver == nil {
			slog.Warn("Ask decision silently allowed (no Approver)", "tool", toolName)
			return true, "", nil // non-interactive: preserve autonomy
		}
		subject := Subject(args)
		allow, remember, err := g.Approver.Approve(ctx, toolName, subject, args)
		if err != nil {
			return false, "approval aborted", err
		}
		if !allow {
			return false, "the user declined this tool call — do not retry it; ask how they would like to proceed or choose another approach.", nil
		}
		if remember && g.OnRemember != nil {
			// "Always allow" is tool-wide: persist the bare tool name so any
			// later subject (a different file / command) is allowed without
			// re-prompting. Deny rules still take precedence on every call.
			g.OnRemember(toolName)
			// Also add the rule to the in-memory Policy immediately so it
			// takes effect in the current session without requiring a restart.
			// The session-level grant (controller.granted) already covers the
			// Approver path, but any code path that consults Policy.Decide()
			// directly would miss the rule until the next controller build.
			if rule, ok := ParseRule(toolName); ok {
				g.Policy.Allow = append(g.Policy.Allow, rule)
			}
		}
		return true, "", nil
	default:
		return true, "", nil
	}
}
