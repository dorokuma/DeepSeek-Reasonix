package memory

import (
	"context"
	"strings"
	"testing"
)

func TestMemoryGetRejectsSkillNamespace(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	isSkill := func(id string) bool { return id == "init" }
	tl := NewRecallTool(store, isSkill)
	_, err := tl.Execute(context.Background(), []byte(`{"memory":"skill/init"}`))
	if err == nil || !strings.Contains(err.Error(), "SKILL") {
		t.Fatalf("expected skill redirect error, got %v", err)
	}
	_, err = tl.Execute(context.Background(), []byte(`{"memory":"init"}`))
	if err == nil || !strings.Contains(err.Error(), "skill/") {
		t.Fatalf("expected skill collision error when memory missing, got %v", err)
	}
}

func TestPromptIndexConvertsLegacyLinks(t *testing.T) {
	raw := "# Memory\n\n- [Prefers tabs](tabs-rule.md) — use tabs\n"
	got := PromptIndex(raw)
	if !strings.Contains(got, "memory/tabs-rule") {
		t.Fatalf("want namespaced line, got:\n%s", got)
	}
	if strings.Contains(got, "](tabs-rule.md)") {
		t.Fatalf("legacy link must not remain:\n%s", got)
	}
}
