package skill

import (
	"fmt"
	"strings"
)

// IndexMaxChars caps the pinned skills-index block so it can't bloat the
// cache-stable system-prompt prefix; bodies never enter the prefix.
const IndexMaxChars = 4000

const missingDescPlaceholder = `(no description — frontmatter is missing a "description:" line; tell the user to add one)`

// indexHeader introduces the skills block in the system prompt.
const indexHeader = "# Skills — playbooks you can invoke\n\n" +
	"One-liner index of Skills only (not the Memory section). Memory slugs and `[label](slug.md)` lines under # Memory are auto-memory facts — use `recall`, never run_skill/read_skill with those names. " +
	"Before non-trivial work, scan this Skills list: if a skill is even plausibly relevant, invoke it with `run_skill({ name: \"<skill-name>\", arguments: \"...\" })` — `name` is JUST the identifier. The skill body is inlined into your context. " +
	"For isolated background work, use the **task** tool with a self-contained prompt. The user can also invoke a skill via `/<name>`."

// ApplyIndex appends the skills index to basePrompt, or returns it unchanged
// when there are no skills. Only names + descriptions are listed; bodies load
// on demand via run_skill.
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

// indexLine renders one skill as "- name — description", clipped to a stable width.
func indexLine(sk Skill) string {
	desc := strings.TrimSpace(strings.ReplaceAll(sk.Description, "\n", " "))
	if desc == "" {
		desc = missingDescPlaceholder
	}
	max := 130 - len([]rune(sk.Name))
	clipped := clipRunes(desc, max)
	if clipped == "" {
		return "- " + sk.Name
	}
	return "- " + sk.Name + " — " + clipped
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
