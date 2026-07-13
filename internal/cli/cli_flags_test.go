package cli

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
)

func TestSplitAllowedToolRules(t *testing.T) {
	got, err := splitAllowedToolRules([]string{
		"Bash(git *) Edit,read_file",
		"Bash(go test ./...) Edit(docs/**)",
		"Edit",
	})
	if err != nil {
		t.Fatalf("splitAllowedToolRules: %v", err)
	}
	want := []string{"Bash(git *)", "Edit", "read_file", "Bash(go test ./...)", "Edit(docs/**)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %#v, want %#v", got, want)
	}
}

func TestSplitAllowedToolRulesRejectsUnbalancedParentheses(t *testing.T) {
	for _, input := range []string{"Bash(git *", "Bash(git *))"} {
		if _, err := splitAllowedToolRules([]string{input}); err == nil {
			t.Fatalf("splitAllowedToolRules(%q) unexpectedly succeeded", input)
		}
	}
}

func TestNormalizeOptionalResumeArg(t *testing.T) {
	got := normalizeOptionalResumeArg([]string{"--model", "x", "--resume", "session-id", "--copy"})
	want := []string{"--model", "x", "--resume=session-id", "--copy"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized args = %#v, want %#v", got, want)
	}
	got = normalizeOptionalResumeArg([]string{"-r", "--copy"})
	if !reflect.DeepEqual(got, []string{"-r", "--copy"}) {
		t.Fatalf("bare resume args = %#v", got)
	}
}

func TestResolveSessionQueryByIDAndPreview(t *testing.T) {
	dir := t.TempDir()
	first := saveQueryTestSession(t, dir, "alpha-session.jsonl", "fix provider configuration")
	_ = saveQueryTestSession(t, dir, "beta-session.jsonl", "improve terminal picker")

	got, err := resolveSessionQuery(dir, "alpha-session")
	if err != nil || got != first {
		t.Fatalf("resolve by ID = (%q, %v), want %q", got, err, first)
	}
	got, err = resolveSessionQuery(dir, "provider configuration")
	if err != nil || got != first {
		t.Fatalf("resolve by preview = (%q, %v), want %q", got, err, first)
	}
	if _, err := resolveSessionQuery(dir, "session"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous query error = %v", err)
	}
	if _, err := resolveSessionQuery(dir, "missing"); err == nil || !strings.Contains(err.Error(), "no session") {
		t.Fatalf("missing query error = %v", err)
	}
}

func saveQueryTestSession(t *testing.T, dir, name, prompt string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	session := agent.NewSession("")
	session.Add(provider.Message{Role: provider.RoleUser, Content: prompt})
	session.Add(provider.Message{Role: provider.RoleAssistant, Content: "done"})
	if err := session.Save(path); err != nil {
		t.Fatal(err)
	}
	return path
}
