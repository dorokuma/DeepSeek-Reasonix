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

// NewRunSkillTool builds the skill-invocation tool. Skills are inline playbooks
// only; background sub-agents use spawn_agent. There is no read_skill —
// loading a skill means invoking it.
func NewRunSkillTool(store *Store) tool.Tool {
	return &runSkillTool{store: store}
}

func (*runSkillTool) Name() string { return "run_skill" }

// ReadOnly is false: following an inlined playbook may lead to writer tools.
func (*runSkillTool) ReadOnly() bool { return false }

func (*runSkillTool) Description() string {
	return "Invoke a Skills-index playbook. Required parameter: skill (skill/<id> or bare <id> from the Skills list). " +
		"This is the only skill tool — it inlines the playbook so you follow it this turn. " +
		"Not for auto-memory (memory/* uses recall/remember/forget). Background isolation uses task."
}

func (*runSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "skill":{"type":"string","description":"Skill id from the Skills index only (skill/<id> or bare <id>). Never a memory/* id."},
  "arguments":{"type":"string","description":"Free-form arguments appended as an 'Arguments:' line."}
},
"required":["skill"]
}`)
}

func (t *runSkillTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Skill     string `json:"skill"`
		Name      string `json:"name"` // ignored if skill set; not documented
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	raw := strings.TrimSpace(p.Skill)
	if raw == "" {
		raw = strings.TrimSpace(p.Name)
	}
	if raw == "" {
		return "", fmt.Errorf("run_skill requires parameter \"skill\" (skill/<id> from the Skills index)")
	}
	if strings.HasPrefix(raw, "memory/") {
		return "", fmt.Errorf("parameter skill=%q is not a skill id (memory/* belongs to recall/remember/forget)", raw)
	}
	name := cleanSkillName(raw)
	if name == "" {
		return "", fmt.Errorf("invalid skill id %q", raw)
	}
	sk, ok := t.store.Read(name)
	if !ok {
		return "", fmt.Errorf("unknown skill %q — available: %s", name, availableNames(t.store))
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
	return "Author and save a new skill playbook (skill/<id>). Invoke later via run_skill({skill:\"<id>\"}) or /<id>. Not for durable facts — those use remember. " + scope
}

func (*installSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "name":{"type":"string","description":"Skill id — letters/digits/_/-/., 1-64 chars. Becomes skill/<id> in the Skills index."},
  "description":{"type":"string","description":"≤120-char one-liner shown in the Skills index."},
  "body":{"type":"string","description":"Markdown playbook inlined when run_skill is used."},
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
		"note":  "Callable via run_skill({skill:\"" + name + "\"}) or /" + name + ". Listed as skill/" + name + ".",
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
// followed by the body and any arguments. Used when a user invokes a skill via
// "/<name>"; run_skill wraps the same text in a skill-pin sentinel.
func Render(sk Skill, args string) string {
	var b strings.Builder
	b.WriteString("# Skill: " + sk.Name + "\n\n")
	if sk.Description != "" {
		b.WriteString(sk.Description + "\n\n")
	}
	if sk.Path != "" && sk.Path != "(builtin)" {
		b.WriteString("Source: " + sk.Path + "\n\n")
	}
	b.WriteString(sk.Body)
	if args != "" {
		b.WriteString("\n\nArguments: " + args)
	}
	return b.String()
}

func renderInline(sk Skill, args string) string {
	return "<skill-pin name=" + strconv.Quote(sk.Name) + ">\n" + Render(sk, args) + "\n</skill-pin>"
}

var bracketTagRe = regexp.MustCompile(`\[[^\]]*\]`)

// cleanSkillName extracts the bare skill id from skill/<id>, bare id, or paste noise.
func cleanSkillName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "memory/") {
		return ""
	}
	raw = strings.TrimPrefix(raw, SkillNamespace)
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

// collapseSpaces turns any run of whitespace into a single space.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// availableNames lists discoverable skill names for error messages.
func availableNames(store *Store) string {
	skills := store.List()
	if len(skills) == 0 {
		return "(none — no skills defined)"
	}
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = SkillNamespace + s.Name
	}
	return strings.Join(names, ", ")
}
