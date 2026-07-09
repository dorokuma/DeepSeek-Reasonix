package memory

import (
	"regexp"
	"strings"
)

// MemoryNamespace is the prompt/tool namespace prefix that separates auto-memory
// ids from skill ids. Index lines and tool args use "memory/<slug>".
const MemoryNamespace = "memory/"

var (
	// legacy index: - [Title](slug.md) — desc
	legacyIndexLinkRe = regexp.MustCompile(`^\s*-\s*\[([^\]]*)\]\(([^)]+)\.md\)\s*(?:—|-)?\s*(.*)$`)
	// current index: memory/slug — Title — desc  (title optional)
	memoryIndexLineRe = regexp.MustCompile(`^\s*-?\s*memory/([A-Za-z0-9._-]+)\s*(?:—|-)\s*(.*)$`)
)

// NormalizeMemoryID strips optional "memory/" prefix and slugifies.
func NormalizeMemoryID(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, MemoryNamespace)
	// Models sometimes paste "[Title](slug.md)" or "slug.md"
	if m := regexp.MustCompile(`\]\(([^)]+)\.md\)`).FindStringSubmatch(raw); len(m) >= 2 {
		raw = m[1]
	}
	raw = strings.TrimSuffix(raw, ".md")
	return slug(raw)
}

// FormatMemoryIndexLine is the canonical on-disk + prompt line for one fact.
// Distinct from skills ("skill/<id> — …") so the two indexes never look alike.
func FormatMemoryIndexLine(id, title, description string) string {
	id = NormalizeMemoryID(id)
	title = oneLine(title)
	description = oneLine(description)
	if title == "" {
		title = displayTitle("", id)
	}
	if description == "" {
		return MemoryNamespace + id + " — " + title
	}
	return MemoryNamespace + id + " — " + title + " — " + description
}

// PromptIndex rewrites a raw MEMORY.md body into the namespaced prompt form,
// accepting both legacy markdown-link lines and current memory/ lines.
func PromptIndex(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			// Drop the "# Memory" heading from the file; Block() already has a section title.
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			out = append(out, line)
			continue
		}
		if pl, ok := promptIndexLine(line); ok {
			out = append(out, pl)
			continue
		}
		// Keep hand-edited freeform lines but never look like skill bullets.
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func promptIndexLine(line string) (string, bool) {
	if m := legacyIndexLinkRe.FindStringSubmatch(line); len(m) >= 4 {
		return FormatMemoryIndexLine(m[2], m[1], m[3]), true
	}
	if m := memoryIndexLineRe.FindStringSubmatch(line); len(m) >= 3 {
		// already namespaced; normalize spacing via Format when possible
		rest := strings.TrimSpace(m[2])
		// rest may be "Title — desc" or just desc
		title, desc := "", rest
		if i := strings.Index(rest, " — "); i >= 0 {
			title = strings.TrimSpace(rest[:i])
			desc = strings.TrimSpace(rest[i+len(" — "):])
		}
		return FormatMemoryIndexLine(m[1], title, desc), true
	}
	return "", false
}

// IndexKeyFromLine extracts the memory id from a managed index line (either format).
func IndexKeyFromLine(line string) (string, bool) {
	if m := legacyIndexLinkRe.FindStringSubmatch(line); len(m) >= 3 {
		return m[2], true
	}
	if m := memoryIndexLineRe.FindStringSubmatch(line); len(m) >= 2 {
		return m[1], true
	}
	return "", false
}
