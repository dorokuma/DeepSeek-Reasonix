package ctxmode

import (
	"fmt"
	"sort"
	"strings"
)

type CompactEvent struct {
	Priority int
	Category string
	Summary  string
	File     string
}

type CompactSnapshot struct {
	events []CompactEvent
}

func (cs *CompactSnapshot) AddEvent(e CompactEvent) {
	if cs == nil {
		return
	}
	cs.events = append(cs.events, e)
}

func (cs *CompactSnapshot) Build() string {
	if cs == nil || len(cs.events) == 0 {
		return ""
	}
	sort.Slice(cs.events, func(i, j int) bool {
		if cs.events[i].Priority != cs.events[j].Priority {
			return cs.events[i].Priority < cs.events[j].Priority
		}
		return i > j
	})
	var b strings.Builder
	b.WriteString("<compaction_context>\n")
	p1 := filterByPriority(cs.events, 1)
	if len(p1) > 0 {
		b.WriteString("## Key decisions & corrections\n")
		for _, e := range p1 {
			b.WriteString(fmt.Sprintf("- [%s] %s", e.Category, e.Summary))
			if e.File != "" {
				b.WriteString(fmt.Sprintf(" (%s)", e.File))
			}
			b.WriteString("\n")
		}
	}
	p2 := filterByPriority(cs.events, 2)
	if len(p2) > 0 {
		b.WriteString("## Active context\n")
		seen := map[string]bool{}
		for _, e := range p2 {
			if e.File != "" && !seen[e.File] {
				seen[e.File] = true
				b.WriteString(fmt.Sprintf("- %s: %s\n", e.File, e.Summary))
			} else if e.File == "" {
				b.WriteString(fmt.Sprintf("- [%s] %s\n", e.Category, e.Summary))
			}
		}
	}
	p3 := filterByPriority(cs.events, 3)
	if len(p3) > 0 && b.Len() < 1800 {
		b.WriteString("## Recent activity\n")
		for _, e := range p3 {
			line := fmt.Sprintf("- %s\n", e.Summary)
			if b.Len()+len(line) > 2000 {
				b.WriteString("... (truncated)\n")
				break
			}
			b.WriteString(line)
		}
	}
	b.WriteString("</compaction_context>")
	return b.String()
}

func filterByPriority(events []CompactEvent, p int) []CompactEvent {
	var out []CompactEvent
	for _, e := range events {
		if e.Priority == p {
			out = append(out, e)
		}
	}
	return out
}

func (cs *CompactSnapshot) Reset() {
	if cs == nil {
		return
	}
	cs.events = cs.events[:0]
}

func (cs *CompactSnapshot) RecordFileEdit(path, summary string) {
	cs.AddEvent(CompactEvent{Priority: 2, Category: "file_edit", Summary: summary, File: path})
}

func (cs *CompactSnapshot) RecordDecision(summary string) {
	cs.AddEvent(CompactEvent{Priority: 1, Category: "decision", Summary: summary})
}

func (cs *CompactSnapshot) RecordError(summary, file string) {
	cs.AddEvent(CompactEvent{Priority: 1, Category: "error", Summary: summary, File: file})
}
