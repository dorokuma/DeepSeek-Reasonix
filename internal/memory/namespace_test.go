package memory

import (
	"context"
	"strings"
	"testing"
)

func TestRecallRejectsSkillNamespace(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	tl := NewRecallTool(store)
	_, err := tl.Execute(context.Background(), []byte(`{"memory":"skill/init"}`))
	if err == nil || !strings.Contains(err.Error(), "run_skill") {
		t.Fatalf("expected skill redirect to run_skill, got %v", err)
	}
}

func TestRememberRejectsSkillNamespace(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	tl := NewRememberTool(store)
	_, err := tl.Execute(context.Background(), []byte(`{"memory":"skill/init","description":"d","body":"b"}`))
	if err == nil || !strings.Contains(err.Error(), "skill/") {
		t.Fatalf("expected skill/* rejection, got %v", err)
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
