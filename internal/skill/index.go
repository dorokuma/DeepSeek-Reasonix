package skill

import (
	"fmt"
	"strings"
)

// IndexMaxChars caps the pinned skills-index block so it can't bloat the
// cache-stable system-prompt prefix; bodies never enter the prefix.
const IndexMaxChars = 4000

const missingDescPlaceholder = `(no description — frontmatter is missing a "description:" line; tell the user to add one)`

// SkillNamespace prefixes skill ids in the prompt index so they cannot be
// confused with auto-memory lines (memory/<id>).
const SkillNamespace = "skill/"

// indexHeader introduces the skills block in the system prompt.
const indexHeader = "# Skills (namespace skill/* only)\n\n" +
	"Playbook index — completely separate from Saved memories (memory/*). " +
	"Every skill id looks like skill/<id>. Invoke with run_skill({skill:\"<id>\"}) or read_skill({skill:\"<id>\"}) — parameter is \"skill\", never \"memory\". " +
	"Never use memory_get/memory_save/memory_forget on skill/* ids. " +
	"Bodies are inlined into your context. For isolated background work use the task tool. Users may also type /<id>."

// ApplyIndex appends the skills index to basePrompt, or returns it unchanged
// when there are no skills. Only namespaced ids + descriptions are listed;
// bodies load on demand via run_skill/read_skill.
func ApplyIndex(basePrompt string, skills []Skill) string {
	if len(skills) == 0 {
		return basePrompt
	}
	lines := make([]string, 0, len(skills))
	for _, sk := range skills {
		lines = append(lines, indexLine(sk))
	}
	joined := strings.Join(lines, "\n")
	if r := []rune(joined); len(r) > IndexMaxChars {
		joined = string(r[:IndexMaxChars]) + fmt.Sprintf("\n… (truncated %d chars)", len(r)-IndexMaxChars)
	}
	return basePrompt + "\n\n" + indexHeader + "\n\n```\n" + joined + "\n```"
}

// indexLine renders one skill as "skill/<id> — description".
func indexLine(sk Skill) string {
	desc := strings.TrimSpace(strings.ReplaceAll(sk.Description, "\n", " "))
	if desc == "" {
		desc = missingDescPlaceholder
	}
	id := SkillNamespace + sk.Name
	max := 130 - len([]rune(id))
	clipped := clipRunes(desc, max)
	if clipped == "" {
		return id
	}
	return id + " — " + clipped
}

// clipRunes truncates s to at most max runes (ellipsis included), never
// splitting a multi-byte rune.
func clipRunes(s string, max int) string {
	if max < 1 {
		max = 1
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max-1 < 1 {
		return string(r[:1])
	}
	return string(r[:max-1]) + "…"
}
