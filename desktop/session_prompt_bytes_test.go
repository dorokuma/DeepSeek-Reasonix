package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// capturingProvider records the exact message list of every request it
// receives, marshaled at capture time, so tests can compare request bytes.
type capturingProvider struct {
	mu       sync.Mutex
	requests [][]byte
}

func (p *capturingProvider) Name() string { return "capturing" }

func (p *capturingProvider) Stream(_ context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	b, err := json.Marshal(req.Messages)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.requests = append(p.requests, b)
	p.mu.Unlock()
	ch := make(chan provider.Chunk, 1)
	ch <- provider.Chunk{Type: provider.ChunkText, Text: "ok"}
	close(ch)
	return ch, nil
}

func (p *capturingProvider) lastRequestMessages(t *testing.T) []provider.Message {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		t.Fatal("provider captured no requests")
	}
	var msgs []provider.Message
	if err := json.Unmarshal(p.requests[len(p.requests)-1], &msgs); err != nil {
		t.Fatalf("unmarshal captured request: %v", err)
	}
	return msgs
}

func marshalMessages(t *testing.T, msgs []provider.Message) []byte {
	t.Helper()
	b, err := json.Marshal(msgs)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	return b
}

// TestRebindKeepsRequestPrefixBytes is the desktop-level byte-stability guard
// for the provider prefix cache: after the desktop's rebind path (load the
// persisted transcript, swap in the freshly composed system prompt via
// sessionWithFreshSystemPrompt, Resume on a new controller — the shape of
// tabs.go's build/restore), the next request's leading messages must be
// byte-identical to the request sent before the rebind. Any drift here means
// every rebind cold-starts the conversation's provider cache at 10x miss
// pricing (#2945, #5614).
func TestRebindKeepsRequestPrefixBytes(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	const systemPrompt = "SYSPROMPT stable bytes"

	prov := &capturingProvider{}
	exec := agent.New(prov, tool.NewRegistry(), agent.NewSession(systemPrompt), agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{Runner: exec, Executor: exec, SystemPrompt: systemPrompt, SessionDir: dir, SessionPath: path, Label: "test", Sink: event.Discard})

	if err := ctrl.RunTurn(context.Background(), "first question"); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	before := prov.lastRequestMessages(t)
	beforeBytes := marshalMessages(t, before)
	if err := ctrl.Snapshot(); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Desktop rebind: a NEW controller composes its (identical) system prompt,
	// the persisted transcript is loaded, the fresh prompt is swapped in, and
	// the controller resumes the session — tabs.go's restore shape.
	prov2 := &capturingProvider{}
	exec2 := agent.New(prov2, tool.NewRegistry(), agent.NewSession(systemPrompt), agent.Options{}, event.Discard)
	ctrl2 := control.New(control.Options{Runner: exec2, Executor: exec2, SystemPrompt: systemPrompt, SessionDir: dir, SessionPath: path, Label: "test", Sink: event.Discard})
	loaded, err := agent.LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	ctrl2.Resume(sessionWithFreshSystemPrompt(loaded, systemPromptFrom(ctrl2.History())), path)

	if err := ctrl2.RunTurn(context.Background(), "second question"); err != nil {
		t.Fatalf("post-rebind turn: %v", err)
	}
	after := prov2.lastRequestMessages(t)
	if len(after) <= len(before) {
		t.Fatalf("post-rebind request has %d messages, want more than the %d sent before rebind", len(after), len(before))
	}
	prefixBytes := marshalMessages(t, after[:len(before)])
	if string(prefixBytes) != string(beforeBytes) {
		t.Fatalf("rebind changed the request prefix bytes — the provider prefix cache is invalidated:\nbefore: %s\nafter:  %s", beforeBytes, prefixBytes)
	}
}

// TestRebindWithDriftedPromptBreaksRequestPrefix pins the failure mode the
// guard above protects against: when the freshly composed prompt differs from
// the one the transcript was recorded with, the swap rewrites the first
// message and the request prefix diverges. If a future change moves the swap
// policy to keep the persisted prompt for resumed conversations, this test
// should be updated to assert the prefix survives instead.
func TestRebindWithDriftedPromptBreaksRequestPrefix(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	prov := &capturingProvider{}
	exec := agent.New(prov, tool.NewRegistry(), agent.NewSession("SYSPROMPT v1"), agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{Runner: exec, Executor: exec, SystemPrompt: "SYSPROMPT v1", SessionDir: dir, SessionPath: path, Label: "test", Sink: event.Discard})
	if err := ctrl.RunTurn(context.Background(), "first question"); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	before := prov.lastRequestMessages(t)
	beforeBytes := marshalMessages(t, before)
	if err := ctrl.Snapshot(); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	prov2 := &capturingProvider{}
	exec2 := agent.New(prov2, tool.NewRegistry(), agent.NewSession("SYSPROMPT v2 drifted"), agent.Options{}, event.Discard)
	ctrl2 := control.New(control.Options{Runner: exec2, Executor: exec2, SystemPrompt: "SYSPROMPT v2 drifted", SessionDir: dir, SessionPath: path, Label: "test", Sink: event.Discard})
	loaded, err := agent.LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	ctrl2.Resume(sessionWithFreshSystemPrompt(loaded, systemPromptFrom(ctrl2.History())), path)
	if err := ctrl2.RunTurn(context.Background(), "second question"); err != nil {
		t.Fatalf("post-rebind turn: %v", err)
	}
	after := prov2.lastRequestMessages(t)
	if len(after) <= len(before) {
		t.Fatalf("post-rebind request has %d messages, want more than %d", len(after), len(before))
	}
	if string(marshalMessages(t, after[:len(before)])) == string(beforeBytes) {
		t.Fatal("drifted prompt unexpectedly kept the request prefix — the swap policy changed; update these guards")
	}
}
