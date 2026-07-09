package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"reasonix/internal/tool"
)

// InstalledHook fires after install_skill writes a new file, so a host can
// refresh UI (e.g. a skills sidebar) without a reload. nil is fine.
type InstalledHook func(name, path string, scope Scope)

// NameExists reports whether an id is claimed by another namespace (auto-memory).
type NameExists func(id string) bool

// --- run_skill ---

type runSkillTool struct {
	store    *Store
	isMemory NameExists
}

// NewRunSkillTool builds the skill-invocation tool. All skills are inline
// playbooks; background sub-agents use the separate `task` tool only.
// isMemory, if set, rejects ids that only exist as auto-memory.
func NewRunSkillTool(store *Store, isMemory ...NameExists) tool.Tool {
	t := &runSkillTool{store: store}
	if len(isMemory) > 0 {
		t.isMemory = isMemory[0]
	}
	return t
}

func (*runSkillTool) Name() string { return "run_skill" }

// ReadOnly is false: following an inlined playbook may lead to writer tools.
func (*runSkillTool) ReadOnly() bool { return false }

func (*runSkillTool) Description() string {
	return "Invoke a Skills-index playbook (skill/<id> only). Required parameter: skill (e.g. \"init\" or \"skill/init\"). " +
		"Never pass a memory/* id — those use memory_get. Body is inlined into your context. Background work uses task."
}

func (*runSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "skill":{"type":"string","description":"Skill id from the Skills index (skill/<id> or bare <id>). Not a memory id."},
  "arguments":{"type":"string","description":"Free-form arguments appended as an 'Arguments:' line."}
},
"required":["skill"]
}`)
}

func (t *runSkillTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	name, raw, err := parseSkillArg(args)
	if err != nil {
		return "", err
	}
	if err := t.rejectForeignNamespace(name, raw); err != nil {
		return "", err
	}
	sk, ok := t.store.Read(name)
	if !ok {
		if t.isMemory != nil && t.isMemory(name) {
			return "", fmt.Errorf("%q is a MEMORY entry (memory/%s) — use memory_get({memory:%q}), not run_skill", name, name, name)
		}
		return "", fmt.Errorf("unknown skill %q — available: %s", name, availableNames(t.store))
	}
	var p struct {
		Arguments string `json:"arguments"`
	}
	_ = json.Unmarshal(args, &p)
	return renderInline(sk, strings.TrimSpace(p.Arguments)), nil
}

// readSkillTool loads an inline skill body into context without running anything.
type readSkillTool struct {
	store    *Store
	isMemory NameExists
}

// NewReadSkillTool builds a read-only skill loader. isMemory rejects memory ids.
func NewReadSkillTool(store *Store, isMemory ...NameExists) tool.Tool {
	t := &readSkillTool{store: store}
	if len(isMemory) > 0 {
		t.isMemory = isMemory[0]
	}
	return t
}

func (*readSkillTool) Name() string { return "read_skill" }

// ReadOnly is true: read_skill only renders a skill body (no side effects), so
// it is allowed in plan mode where run_skill is not.
func (*readSkillTool) ReadOnly() bool { return true }

func (*readSkillTool) Description() string {
	return "Load a Skills-index playbook (skill/<id> only) WITHOUT executing a sub-agent — body returns as a tool result. " +
		"Required parameter: skill. Read-only (works in plan mode). " +
		"For auto-memory facts use memory_get({memory:\"…\"}) — never read_skill on memory/* ids."
}

func (*readSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "skill":{"type":"string","description":"Skill id from the Skills index (skill/<id> or bare <id>). Not a memory id."},
  "arguments":{"type":"string","description":"Optional free-form arguments, appended as an 'Arguments:' line."}
},
"required":["skill"]
}`)
}

func (t *readSkillTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	name, raw, err := parseSkillArg(args)
	if err != nil {
		return "", fmt.Errorf("read_skill: %w", err)
	}
	if err := t.rejectForeignNamespace(name, raw); err != nil {
		return "", err
	}
	sk, ok := t.store.ReadInline(name)
	if !ok {
		if t.isMemory != nil && t.isMemory(name) {
			return "", fmt.Errorf("%q is a MEMORY entry (memory/%s) — use memory_get({memory:%q}), not read_skill", name, name, name)
		}
		return "", fmt.Errorf("unknown skill %q — available: %s", name, availableInlineNames(t.store))
	}
	var p struct {
		Arguments string `json:"arguments"`
	}
	_ = json.Unmarshal(args, &p)
	return renderInline(sk, strings.TrimSpace(p.Arguments)), nil
}

func (t *runSkillTool) rejectForeignNamespace(name, raw string) error {
	return rejectMemoryNamespace(name, raw, t.isMemory, "run_skill")
}

func (t *readSkillTool) rejectForeignNamespace(name, raw string) error {
	return rejectMemoryNamespace(name, raw, t.isMemory, "read_skill")
}

func rejectMemoryNamespace(name, raw string, isMemory NameExists, toolName string) error {
	if strings.HasPrefix(strings.TrimSpace(raw), "memory/") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "memory/") {
		return fmt.Errorf("%s cannot load memory/* — %q is a memory id; call memory_get({memory:%q})", toolName, raw, name)
	}
	if isMemory != nil && isMemory(name) {
		// Only hard-fail when it is NOT also a real skill (memory wins on collision for this guard
		// only when skill is missing — Execute checks store.Read first for skill tools after this).
	}
	return nil
}

// parseSkillArg requires "skill" (accepts legacy "name" only to emit a clear error preference).
func parseSkillArg(args json.RawMessage) (id, raw string, err error) {
	var p struct {
		Skill  string `json:"skill"`
		Name   string `json:"name"`
		Memory string `json:"memory"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Memory) != "" && strings.TrimSpace(p.Skill) == "" && strings.TrimSpace(p.Name) == "" {
		return "", p.Memory, fmt.Errorf("parameter \"memory\" is invalid here — use memory_get for memory/*; skills require parameter \"skill\"")
	}
	raw = strings.TrimSpace(p.Skill)
	if raw == "" {
		raw = strings.TrimSpace(p.Name)
	}
	if raw == "" {
		return "", "", fmt.Errorf("requires parameter \"skill\" (skill/<id> from the Skills index)")
	}
	id = cleanSkillName(raw)
	if id == "" {
		return "", raw, fmt.Errorf("invalid skill id %q", raw)
	}
	return id, raw, nil
}

// --- install_skill ---

type installSkillTool struct {
	store       *Store
	onInstalled InstalledHook
}

// NewInstallSkillTool builds the skill-authoring tool. onInstalled may be nil.
func NewInstallSkillTool(store *Store, onInstalled InstalledHook) tool.Tool {
	return &installSkillTool{store: store, onInstalled: onInstalled}
}

func (*installSkillTool) Name() string   { return "install_skill" }
func (*installSkillTool) ReadOnly() bool { return false }

func (t *installSkillTool) Description() string {
	scope := "'global' (only option — no project workspace) writes to ~/.reasonix/skills/."
	if t.store.HasProjectScope() {
		scope = "'project' (default) writes to <repo>/.reasonix/skills/ (this workspace only); 'global' writes to ~/.reasonix/skills/ (every project)."
	}
	return "Author and save a new skill — a reusable playbook future turns invoke via run_skill (or /<name>). Runnable immediately this turn; appears in the pinned Skills index on the next launch. " + scope
}

func (*installSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "name":{"type":"string","description":"Identifier — letters/digits/_/-/., 1-64 chars, starts alphanumeric. Becomes the skill folder name under ~/.reasonix/skills/<name>/SKILL.md."},
  "description":{"type":"string","description":"≤120-char one-liner shown in the pinned Skills index — future agents read it to decide whether to invoke."},
  "body":{"type":"string","description":"Markdown playbook inlined into the parent turn when invoked."},
  "scope":{"type":"string","enum":["project","global"],"description":"Where to write. Defaults to project when a workspace exists, else global."}
},
"required":["name","description","body"]
}`)
}

func (t *installSkillTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Body        string `json:"body"`
		Scope       string `json:"scope"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	name := strings.TrimSpace(p.Name)
	desc := strings.TrimSpace(collapseSpaces(p.Description))
	if name == "" {
		return "", fmt.Errorf("install_skill requires a non-empty 'name'")
	}
	if desc == "" {
		return "", fmt.Errorf("install_skill requires a non-empty 'description' — it is what appears in the Skills index")
	}
	if strings.TrimSpace(p.Body) == "" {
		return "", fmt.Errorf("install_skill requires a non-empty 'body' — the playbook the skill executes")
	}

	scope := ScopeGlobal
	switch strings.TrimSpace(p.Scope) {
	case "global":
		scope = ScopeGlobal
	case "project":
		scope = ScopeProject
	default:
		if t.store.HasProjectScope() {
			scope = ScopeProject
		}
	}
	if scope == ScopeProject && !t.store.HasProjectScope() {
		return "", fmt.Errorf("install_skill: scope='project' requires a workspace — use scope='global'")
	}

	content := renderSkillFile(name, desc, p.Body)
	path, err := t.store.CreateWithContent(name, scope, content)
	if err != nil {
		return "", err
	}
	if t.onInstalled != nil {
		t.onInstalled(name, path, scope)
	}
	res, _ := json.Marshal(map[string]any{
		"ok":    true,
		"name":  name,
		"scope": string(scope),
		"path":  path,
		"note":  "Callable now via run_skill({skill:\"" + name + "\"}) or /" + name + ". Listed as skill/" + name + " in the Skills index. Background work uses the task tool.",
	})
	return string(res), nil
}

// renderSkillFile assembles a skill file's frontmatter + body (always inline).
func renderSkillFile(name, desc, body string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n" +
		strings.TrimRight(body, " \t\r\n") + "\n"
}

// --- shared helpers ---

// Render builds a skill's invocation text: a header (name, description, source)
// followed by the body and any arguments. Used directly when a user invokes a
// skill via "/<name>" (sent as a turn); the run_skill tool wraps the same text
// in a skill-pin sentinel (see renderInline).
func Render(sk Skill, args string) string {
	var b strings.Builder
	b.WriteString("# Skill: " + sk.Name)
	if sk.Description != "" {
		b.WriteString("\n> " + sk.Description)
	}
	b.WriteString("\n(scope: " + string(sk.Scope) + " · " + sk.Path + ")\n\n")
	b.WriteString(sk.Body)
	if args != "" {
		b.WriteString("\n\nArguments: " + args)
	}
	return b.String()
}

// renderInline wraps Render's output in a skill-pin sentinel so context
// compaction preserves the body verbatim instead of paraphrasing it.
func renderInline(sk Skill, args string) string {
	return "<skill-pin name=" + strconv.Quote(sk.Name) + ">\n" + Render(sk, args) + "\n</skill-pin>"
}

var bracketTagRe = regexp.MustCompile(`\[[^\]]*\]`)

// cleanSkillName extracts the bare skill id from a possibly-decorated arg.
// Accepts "skill/foo", "foo", or index paste with bracket notes; rejects nothing
// here (namespace checks happen in callers).
func cleanSkillName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.TrimPrefix(raw, SkillNamespace)
	// memory/ must not become a skill id silently — leave the prefix so callers can reject.
	if strings.HasPrefix(raw, "memory/") {
		return strings.TrimPrefix(raw, "memory/")
	}
	stripped := strings.TrimSpace(bracketTagRe.ReplaceAllString(raw, " "))
	for _, tok := range strings.Fields(stripped) {
		if c := tok[0]; (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			return tok
		}
	}
	return ""
}

// Exists reports whether a skill id is registered.
func (s *Store) Exists(name string) bool {
	if s == nil {
		return false
	}
	_, ok := s.Read(cleanSkillName(name))
	return ok
}

// collapseSpaces turns any run of whitespace (incl. newlines) into a single
// space, so a multi-line description stays a one-liner in the index.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// availableNames lists the discoverable skill names for an error message.
func availableNames(store *Store) string {
	skills := store.List()
	if len(skills) == 0 {
		return "(none — no skills defined)"
	}
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}

func availableInlineNames(store *Store) string {
	skills := store.List()
	if len(skills) == 0 {
		return "(none — no skills defined)"
	}
	var names []string
	for _, s := range skills {
		names = append(names, s.Name)
	}
	if len(names) == 0 {
		return "(none — no skills)"
	}
	return strings.Join(names, ", ")
}
