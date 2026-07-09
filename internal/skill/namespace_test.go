package skill

import (
	"context"
	"strings"
	"testing"
)

func TestRunSkillRejectsMemoryID(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".reasonix/skills/note.md", "---\ndescription: take a note\n---\nbody")
	tl := NewRunSkillTool(New(Options{HomeDir: home, DisableBuiltins: true}))

	_, err := tl.Execute(context.Background(), []byte(`{"skill":"memory/prefers-tabs"}`))
	if err == nil || !strings.Contains(err.Error(), "recall") {
		t.Fatalf("expected memory redirect via recall, got %v", err)
	}
	// Real skill still works (bare id and skill/ prefix).
	out, err := tl.Execute(context.Background(), []byte(`{"skill":"note"}`))
	if err != nil || !strings.Contains(out, "body") {
		t.Fatalf("skill note should load, out=%q err=%v", out, err)
	}
	out, err = tl.Execute(context.Background(), []byte(`{"skill":"skill/note"}`))
	if err != nil || !strings.Contains(out, "body") {
		t.Fatalf("skill/note should load, out=%q err=%v", out, err)
	}
}

func TestIndexUsesSkillNamespace(t *testing.T) {
	out := ApplyIndex("BASE", []Skill{{Name: "init", Description: "bootstrap"}})
	if !strings.Contains(out, "skill/init") {
		t.Fatalf("index must use skill/ namespace:\n%s", out)
	}
	if !strings.Contains(out, "run_skill") || !strings.Contains(out, "recall") {
		t.Fatalf("header should name skill + memory tools:\n%s", out)
	}
	if strings.Contains(out, "read_skill") || strings.Contains(out, "memory_get") {
		t.Fatalf("removed tool names must not appear:\n%s", out)
	}
}
