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

// --- run_skill ---

type runSkillTool struct {
	store *Store
}

// NewRunSkillTool builds the skill-invocation tool. All skills are inline
// playbooks; background sub-agents use the separate `task` tool only.
func NewRunSkillTool(store *Store) tool.Tool {
	return &runSkillTool{store: store}
}

func (*runSkillTool) Name() string { return "run_skill" }

// ReadOnly is false: following an inlined playbook may lead to writer tools.
func (*runSkillTool) ReadOnly() bool { return false }

func (*runSkillTool) Description() string {
	return "Invoke a playbook from the Skills index. Pass `name` as the BARE skill identifier — never a Memory slug or `[label](slug.md)` stem. The skill body is inlined into your context as a tool result you read and follow. For isolated background work use the `task` tool (not skills)."
}

func (*runSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "name":{"type":"string","description":"Skill identifier as it appears in the pinned Skills index. Case-sensitive bare name only."},
  "arguments":{"type":"string","description":"Free-form arguments appended as an 'Arguments:' line; the skill's own instructions decide how to use them."}
},
"required":["name"]
}`)
}

func (t *runSkillTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	name := cleanSkillName(p.Name)
	if name == "" {
		return "", fmt.Errorf("run_skill requires a 'name' argument (got %q, which is just a marker/tag)", p.Name)
	}
	sk, ok := t.store.Read(name)
	if !ok {
		return "", fmt.Errorf("unknown skill %q — available: %s", name, availableNames(t.store))
	}
	return renderInline(sk, strings.TrimSpace(p.Arguments)), nil
}

// readSkillTool loads an inline skill body into context without running anything.
type readSkillTool struct {
	store *Store
}

// NewReadSkillTool builds a read-only inline-skill loader. Unlike run_skill it
// stays available in plan mode, so a plan can consult inline playbooks.
func NewReadSkillTool(store *Store) tool.Tool { return &readSkillTool{store: store} }

func (*readSkillTool) Name() string { return "read_skill" }

// ReadOnly is true: read_skill only renders a skill body (no side effects), so
// it is allowed in plan mode where run_skill is not.
func (*readSkillTool) ReadOnly() bool { return true }

func (*readSkillTool) Description() string {
	return "Load a skill playbook from the Skills index into your context WITHOUT running anything — the body returns as a tool result you read and follow. Read-only, so it works in plan mode (unlike run_skill). Pass `name` as the BARE skill identifier — not a Memory slug. To read a saved memory fact, use `recall` instead. Background sub-agents use `task`, not skills."
}

func (*readSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "name":{"type":"string","description":"Skill identifier as it appears in the pinned Skills index."},
  "arguments":{"type":"string","description":"Optional free-form arguments, appended as an 'Arguments:' line; the skill's own instructions decide how to use them."}
},
"required":["name"]
}`)
}

func (t *readSkillTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	name := cleanSkillName(p.Name)
	if name == "" {
		return "", fmt.Errorf("read_skill requires a 'name' argument (got %q, which is just a marker/tag)", p.Name)
	}
	sk, ok := t.store.ReadInline(name)
	if !ok {
		return "", fmt.Errorf("unknown skill %q — available: %s", name, availableInlineNames(t.store))
	}
	return renderInline(sk, strings.TrimSpace(p.Arguments)), nil
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
		"note":  "Callable now via run_skill({name}) or /" + name + ". Appears in the pinned Skills index on the next launch. For background sub-agents use the task tool.",
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

// cleanSkillName extracts the bare identifier from a possibly-decorated name:
// models sometimes paste index lines with bracket notes into the `name` arg.
// Drop any [..] tag, then take the first token starting alphanumeric.
func cleanSkillName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	stripped := strings.TrimSpace(bracketTagRe.ReplaceAllString(raw, " "))
	for _, tok := range strings.Fields(stripped) {
		if c := tok[0]; (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			return tok
		}
	}
	return ""
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
