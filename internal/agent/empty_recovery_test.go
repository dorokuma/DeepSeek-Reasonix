package agent

import (
	"context"
	"strings"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

type textCollector struct {
	lines *[]string
}

func (t textCollector) Emit(e event.Event) {
	if e.Kind == event.Text || e.Kind == event.Message {
		*t.lines = append(*t.lines, e.Text)
	}
}

func TestDecideEmptyRecoveryPrefill(t *testing.T) {
	t.Parallel()
	st := &emptyRecoveryState{}
	action, notice := decideEmptyRecovery(st, "", "structured reasoning", nil)
	if action != emptyRecoveryContinuePrefill {
		t.Fatalf("action = %v, want prefill", action)
	}
	if st.thinkingPrefillRetries != 1 {
		t.Fatalf("prefill retries = %d, want 1", st.thinkingPrefillRetries)
	}
	if notice == "" {
		t.Fatal("expected notice")
	}
}

func TestDecideEmptyRecoveryExhaustedAfterPrefillAndRetries(t *testing.T) {
	t.Parallel()
	st := &emptyRecoveryState{thinkingPrefillRetries: maxThinkingPrefillRetries}
	for i := 0; i < maxEmptyContentRetries; i++ {
		action, _ := decideEmptyRecovery(st, "", "still reasoning", nil)
		if action != emptyRecoveryContinueRetry {
			t.Fatalf("retry %d: action = %v, want retry", i+1, action)
		}
	}
	action, _ := decideEmptyRecovery(st, "", "still reasoning", nil)
	if action != emptyRecoveryExhausted {
		t.Fatalf("action = %v, want exhausted", action)
	}
}

func TestDecideEmptyRecoveryPostToolNudge(t *testing.T) {
	t.Parallel()
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "go"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read_file"}}},
		{Role: provider.RoleTool, Content: "ok", ToolCallID: "c1", Name: "read_file"},
	}
	st := &emptyRecoveryState{}
	action, _ := decideEmptyRecovery(st, "", "", msgs)
	if action != emptyRecoveryContinueNudge {
		t.Fatalf("action = %v, want nudge", action)
	}
	if !st.postToolEmptyRetried {
		t.Fatal("expected postToolEmptyRetried")
	}
}

func TestReasoningOnlyPrefillSucceeds(t *testing.T) {
	prov := testutil.NewMock("deepseek-v4-flash",
		testutil.Turn{Reasoning: "structured reasoning answer"},
		testutil.Turn{Text: "Here is the actual answer."},
	)
	var lines []string
	sink := textCollector{lines: &lines}
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{}, sink)
	if err := a.Run(context.Background(), "answer me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if prov.CallCount() != 2 {
		t.Fatalf("api calls = %d, want 2", prov.CallCount())
	}
	got := strings.Join(lines, "")
	if !strings.Contains(got, "Here is the actual answer.") {
		t.Fatalf("text events = %q", got)
	}
	last := a.session.Snapshot()[len(a.session.Snapshot())-1]
	if last.Content != "Here is the actual answer." {
		t.Fatalf("final assistant content = %q", last.Content)
	}
	for i := 1; i < len(a.session.Snapshot()); i++ {
		prev, cur := a.session.Snapshot()[i-1], a.session.Snapshot()[i]
		if prev.Role == provider.RoleAssistant && cur.Role == provider.RoleAssistant {
			t.Fatalf("consecutive assistant messages at %d", i)
		}
	}
}

func TestReasoningOnlyPrefillExhaustedDeliversFallback(t *testing.T) {
	turns := make([]testutil.Turn, 1+maxThinkingPrefillRetries+maxEmptyContentRetries)
	for i := range turns {
		turns[i] = testutil.Turn{Reasoning: "structured reasoning only"}
	}
	prov := testutil.NewMock("deepseek-v4-flash", turns...)
	var lines []string
	sink := textCollector{lines: &lines}
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{}, sink)
	if err := a.Run(context.Background(), "answer me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	wantCalls := len(turns)
	if prov.CallCount() != wantCalls {
		t.Fatalf("api calls = %d, want %d", prov.CallCount(), wantCalls)
	}
	got := strings.Join(lines, "")
	if !strings.Contains(got, emptyResponseUserMessage) {
		t.Fatalf("expected fallback message in events, got %q", got)
	}
	last := a.session.Snapshot()[len(a.session.Snapshot())-1]
	if last.Content != emptyResponseUserMessage {
		t.Fatalf("final session content = %q", last.Content)
	}
}

func TestTrulyEmptyRetriesThenFallback(t *testing.T) {
	turns := make([]testutil.Turn, 1+maxEmptyContentRetries)
	prov := testutil.NewMock("deepseek-v4-flash", turns...)
	var lines []string
	sink := textCollector{lines: &lines}
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{}, sink)
	if err := a.Run(context.Background(), "answer me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if prov.CallCount() != len(turns) {
		t.Fatalf("api calls = %d, want %d", prov.CallCount(), len(turns))
	}
	if !strings.Contains(strings.Join(lines, ""), emptyResponseUserMessage) {
		t.Fatal("expected fallback message")
	}
}

func TestPostToolEmptyNudgeSucceeds(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(NewAskTool())
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{toolCallChunk("c1", "ask", `{"questions":["q":1]}`), {Type: provider.ChunkDone}},
		{{Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "continued after nudge"}, {Type: provider.ChunkDone}},
	}}
	var lines []string
	sink := textCollector{lines: &lines}
	a := New(prov, reg, NewSession(""), Options{}, sink)
	if err := a.Run(context.Background(), "keep going"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if prov.call != 3 {
		t.Fatalf("api calls = %d, want 3", prov.call)
	}
	if !strings.Contains(strings.Join(lines, ""), "continued after nudge") {
		t.Fatal("expected continued text")
	}
}

func TestTrimEmptyResponseScaffolding(t *testing.T) {
	a := New(testutil.NewMock("m"), tool.NewRegistry(), NewSession(""), Options{}, event.Discard)
	a.session.Add(provider.Message{Role: provider.RoleUser, Content: "hi"})
	a.session.Add(provider.Message{Role: provider.RoleAssistant, ReasoningContent: "thinking"})
	a.trimEmptyResponseScaffolding()
	a.session.Add(provider.Message{Role: provider.RoleAssistant, Content: "final"})
	msgs := a.session.Snapshot()
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	if msgs[1].Content != "final" {
		t.Fatalf("last content = %q", msgs[1].Content)
	}
}