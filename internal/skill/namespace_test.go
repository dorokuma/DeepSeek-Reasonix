package skill

import (
	"context"
	"strings"
	"testing"
)

func TestReadSkillRejectsMemoryID(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".reasonix/skills/note.md", "---\ndescription: take a note\n---\nbody")
	isMemory := func(id string) bool { return id == "prefers-tabs" }
	tl := NewReadSkillTool(New(Options{HomeDir: home, DisableBuiltins: true}), isMemory)

	_, err := tl.Execute(context.Background(), []byte(`{"skill":"memory/prefers-tabs"}`))
	if err == nil || !strings.Contains(err.Error(), "memory_get") {
		t.Fatalf("expected memory redirect, got %v", err)
	}
	_, err = tl.Execute(context.Background(), []byte(`{"skill":"prefers-tabs"}`))
	if err == nil || !strings.Contains(err.Error(), "memory/") {
		t.Fatalf("expected memory collision error, got %v", err)
	}
	// Real skill still works.
	out, err := tl.Execute(context.Background(), []byte(`{"skill":"note"}`))
	if err != nil || !strings.Contains(out, "body") {
		t.Fatalf("skill note should load, out=%q err=%v", out, err)
	}
}

func TestIndexUsesSkillNamespace(t *testing.T) {
	out := ApplyIndex("BASE", []Skill{{Name: "init", Description: "bootstrap"}})
	if !strings.Contains(out, "skill/init") {
		t.Fatalf("index must use skill/ namespace:\n%s", out)
	}
	if !strings.Contains(out, "memory_get") || !strings.Contains(out, "skill/") {
		t.Fatalf("header should separate namespaces:\n%s", out)
	}
}
