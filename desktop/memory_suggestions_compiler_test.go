package main

import (
	"context"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/memory"
	"reasonix/internal/memorycompiler"
)

func seedCompilerFailures(t *testing.T, workspaceRoot string, turns int, errText string) {
	t.Helper()
	rt := memorycompiler.New(config.MemoryCompilerDir(workspaceRoot))
	if rt == nil {
		t.Fatal("memory compiler dir did not resolve under isolated dirs")
	}
	for i := 0; i < turns; i++ {
		_, turn := rt.StartTurn(context.Background(), "fix a bug", nil)
		turn.RecordToolResults([]memorycompiler.ToolRecord{
			{Name: "bash", Error: errText},
			{Name: "bash", Error: errText},
		})
		turn.Finish(nil)
	}
}

func TestMemorySuggestionsIncludeCompilerCandidates(t *testing.T) {
	isolateDesktopUserDirs(t)
	userDir := t.TempDir()
	cwd := t.TempDir()
	sessionDir := t.TempDir()
	store := memory.StoreFor(userDir, cwd)
	seedCompilerFailures(t, cwd, 2, "cannot find module providing package foo")

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{
		Memory:     &memory.Set{Store: store, CWD: cwd, UserDir: userDir},
		SessionDir: sessionDir,
	}), "test-model")
	app.tabs["test"].WorkspaceRoot = cwd

	view := app.MemorySuggestions()
	var candidate MemorySuggestion
	for _, item := range view.Memories {
		if strings.HasPrefix(item.Name, "memory-v5-") {
			candidate = item
			break
		}
	}
	if candidate.ID == "" {
		t.Fatalf("memories = %+v, want a memory-v5 candidate", view.Memories)
	}
	if candidate.Type != string(memory.TypeProject) {
		t.Fatalf("candidate type = %q, want project", candidate.Type)
	}
	if !strings.Contains(candidate.Description, "cannot find module") {
		t.Fatalf("candidate description missing pattern: %q", candidate.Description)
	}
	if !strings.Contains(candidate.Reason, "Memory v5") {
		t.Fatalf("candidate reason missing provenance: %q", candidate.Reason)
	}

	path, err := app.AcceptMemorySuggestion(candidate)
	if err != nil {
		t.Fatalf("AcceptMemorySuggestion: %v", err)
	}
	if path == "" {
		t.Fatal("AcceptMemorySuggestion returned empty path")
	}
	saved := store.List()
	if len(saved) != 1 || !strings.Contains(saved[0].Body, "cannot find module") {
		t.Fatalf("saved memories = %+v, want compiler candidate body", saved)
	}

	// Once accepted, the same pattern is covered by an existing memory and
	// must not be suggested again.
	again := app.MemorySuggestions()
	for _, item := range again.Memories {
		if strings.HasPrefix(item.Name, "memory-v5-") {
			t.Fatalf("accepted pattern suggested again: %+v", item)
		}
	}
}

func TestCompilerCandidatesRequireStablePattern(t *testing.T) {
	isolateDesktopUserDirs(t)
	userDir := t.TempDir()
	cwd := t.TempDir()
	sessionDir := t.TempDir()
	store := memory.StoreFor(userDir, cwd)
	// A pattern seen in only one turn is not stable enough to suggest.
	seedCompilerFailures(t, cwd, 1, "cannot find module providing package foo")

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{
		Memory:     &memory.Set{Store: store, CWD: cwd, UserDir: userDir},
		SessionDir: sessionDir,
	}), "test-model")
	app.tabs["test"].WorkspaceRoot = cwd

	view := app.MemorySuggestions()
	for _, item := range view.Memories {
		if strings.HasPrefix(item.Name, "memory-v5-") {
			t.Fatalf("unstable pattern was suggested: %+v", item)
		}
	}
}
