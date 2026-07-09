package skill

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSkillInline(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".reasonix/skills/note.md", "---\ndescription: take a note\n---\nDo the thing.")
	tl := NewRunSkillTool(New(Options{HomeDir: home, DisableBuiltins: true}))

	out, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"note","arguments":"with args"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.HasPrefix(out, "<skill-pin name=\"note\">") || !strings.HasSuffix(out, "</skill-pin>") {
		t.Errorf("inline skill should be skill-pin wrapped:\n%s", out)
	}
	if !strings.Contains(out, "Do the thing.") || !strings.Contains(out, "Arguments: with args") {
		t.Errorf("body/args missing:\n%s", out)
	}
}

func TestRunSkillUnknown(t *testing.T) {
	tl := NewRunSkillTool(New(Options{HomeDir: t.TempDir(), DisableBuiltins: true}))
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"nope"}`)); err == nil {
		t.Error("unknown skill should error")
	}
}

func TestRunSkillIgnoresUnknownFrontmatter(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".reasonix/skills/dig.md", "---\ndescription: dig\nrunAs: ignored\n---\nbody")
	tl := NewRunSkillTool(New(Options{HomeDir: home, DisableBuiltins: true}))
	out, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"dig","arguments":"go"}`))
	if err != nil {
		t.Fatalf("skill with unknown frontmatter should still load/inline: %v", err)
	}
	if !strings.Contains(out, "body") {
		t.Fatalf("expected body inlined, got %s", out)
	}
}

func TestCleanSkillName(t *testing.T) {
	cases := map[string]string{
		"explore":        "explore",
		"explore [note]": "explore",
		"[note] explore": "explore",
		" review ":       "review",
		"[only a tag]":   "",
		"":               "",
	}
	for in, want := range cases {
		if got := cleanSkillName(in); got != want {
			t.Errorf("cleanSkillName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInstallSkill(t *testing.T) {
	home := t.TempDir()
	st := New(Options{HomeDir: home, DisableBuiltins: true})
	tl := NewInstallSkillTool(st, nil)

	out, err := tl.Execute(context.Background(), json.RawMessage(
		`{"name":"deploy","description":"ship it","body":"steps"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Errorf("expected ok result, got %s", out)
	}
	var res struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result JSON: %v", err)
	}
	wantPath := filepath.Join(home, ".reasonix", "skills", "deploy", SkillFile)
	if res.Path != wantPath {
		t.Fatalf("install_skill should report canonical path %s, got %s", wantPath, res.Path)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("install_skill should write canonical SKILL.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".reasonix", "skills", "deploy.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install_skill should not write legacy flat deploy.md, stat err=%v", err)
	}
	if _, ok := st.Read("deploy"); !ok {
		t.Fatal("installed skill not readable")
	}
	// Refuses overwrite.
	if _, err := tl.Execute(context.Background(), json.RawMessage(
		`{"name":"deploy","description":"again","body":"x"}`)); err == nil {
		t.Error("install_skill should refuse to overwrite")
	}
	// Requires description.
	if _, err := tl.Execute(context.Background(), json.RawMessage(
		`{"name":"x","description":"","body":"b"}`)); err == nil {
		t.Error("install_skill should require a description")
	}
}

func TestReadSkillLoadsInlineAndIsReadOnly(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".reasonix/skills/note.md", "---\ndescription: take a note\n---\nDo the thing.")
	tl := NewReadSkillTool(New(Options{HomeDir: home, DisableBuiltins: true}))

	if !tl.ReadOnly() {
		t.Fatal("read_skill must be ReadOnly so it works in plan mode")
	}
	out, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"note","arguments":"with args"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "Do the thing.") {
		t.Errorf("body missing:\n%s", out)
	}
}
