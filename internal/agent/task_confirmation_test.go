package agent

import (
	"context"
	"errors"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// newTestAgent creates a minimal Agent with a session containing the given
// user messages, suitable as a parent agent for checkUserConfirmation tests.
func newTestAgent(messages ...string) *Agent {
	session := NewSession("test-system")
	for _, msg := range messages {
		session.Add(provider.Message{Role: provider.RoleUser, Content: msg})
	}
	return New(nil, tool.NewRegistry(), session, Options{}, event.Discard)
}

// newConfirmationTool returns a TaskTool configured with the given confirmation
// settings. When semanticFallback is true, prov is used for the streaming call.
func newConfirmationTool(check bool, keywords []string, semanticFallback bool, prov provider.Provider) *TaskTool {
	return &TaskTool{
		confirmationCheck:            check,
		confirmationKeywords:         keywords,
		confirmationSemanticFallback: semanticFallback,
		prov: prov,
	}
}

// withCancel returns a cancellable context that is cancelled via t.Cleanup,
// ensuring any background goroutines in MockProvider are cleaned up.
func withCancel(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx, cancel
}

// ---------------------------------------------------------------------------
// TestCheckUserConfirmation_Disabled
// ---------------------------------------------------------------------------

func TestCheckUserConfirmation_Disabled(t *testing.T) {
	tt := newConfirmationTool(false, nil, false, nil)
	// No parent agent in context — should still pass because check is disabled.
	ctx := context.Background()
	if err := tt.checkUserConfirmation(ctx); err != nil {
		t.Fatalf("expected nil error when disabled, got: %v", err)
	}

	// Even with a parent agent and a non-confirming message, disabled should pass.
	agent := newTestAgent("帮我改bug")
	ctx = WithAgent(ctx, agent)
	if err := tt.checkUserConfirmation(ctx); err != nil {
		t.Fatalf("expected nil error when disabled (even with parent), got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestCheckUserConfirmation_NoParentAgent
// ---------------------------------------------------------------------------

func TestCheckUserConfirmation_NoParentAgent(t *testing.T) {
	tt := newConfirmationTool(true, nil, false, nil)
	ctx := context.Background()
	// No parent agent in context → should pass (nil error).
	if err := tt.checkUserConfirmation(ctx); err != nil {
		t.Fatalf("expected nil error when no parent agent, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestCheckUserConfirmation_EmptyMessage
// ---------------------------------------------------------------------------

func TestCheckUserConfirmation_EmptyMessage(t *testing.T) {
	tt := newConfirmationTool(true, nil, false, nil)

	// Session exists but has no user messages → GetLastUserMessage returns "".
	agent := newTestAgent() // no messages
	ctx := WithAgent(context.Background(), agent)
	if err := tt.checkUserConfirmation(ctx); err != nil {
		t.Fatalf("expected nil error when no user message, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestCheckUserConfirmation_KeywordMatch
// ---------------------------------------------------------------------------

func TestCheckUserConfirmation_KeywordMatch(t *testing.T) {
	tests := []struct {
		name     string
		msg      string
		keywords []string
		wantErr  bool
	}{
		{"exact keyword", "对", []string{"对", "可以"}, false},
		{"substring match", "可以，继续", []string{"对", "可以"}, false},
		{"no match", "帮我改bug", []string{"对", "可以"}, true},
		{"case insensitive keyword", "GO AHEAD", []string{"对", "可以", "go ahead"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := newConfirmationTool(true, tc.keywords, false, nil)
			agent := newTestAgent(tc.msg)
			ctx := WithAgent(context.Background(), agent)
			err := tt.checkUserConfirmation(ctx)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected nil error, got: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCheckUserConfirmation_KeywordNoMatch_SemanticDisabled
// ---------------------------------------------------------------------------

func TestCheckUserConfirmation_KeywordNoMatch_SemanticDisabled(t *testing.T) {
	tt := newConfirmationTool(true, []string{"对", "可以"}, false, nil)
	agent := newTestAgent("帮我改bug")
	ctx := WithAgent(context.Background(), agent)

	err := tt.checkUserConfirmation(ctx)
	if err == nil {
		t.Fatal("expected error for non-matching message when semantic fallback is disabled")
	}
}

// ---------------------------------------------------------------------------
// TestCheckUserConfirmation_SemanticFallback_Yes
// ---------------------------------------------------------------------------

func TestCheckUserConfirmation_SemanticFallback_Yes(t *testing.T) {
	ctx, cancel := withCancel(t)
	defer cancel()

	mp := testutil.NewMock("test", testutil.Turn{Chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "YES"},
		{Type: provider.ChunkDone},
	}})
	tt := newConfirmationTool(true, []string{"对"}, true, mp)
	agent := newTestAgent("没问题，按这个来")
	ctx = WithAgent(ctx, agent)

	if err := tt.checkUserConfirmation(ctx); err != nil {
		t.Fatalf("expected nil error (semantic YES), got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestCheckUserConfirmation_SemanticFallback_No
// ---------------------------------------------------------------------------

func TestCheckUserConfirmation_SemanticFallback_No(t *testing.T) {
	ctx, cancel := withCancel(t)
	defer cancel()

	mp := testutil.NewMock("test", testutil.Turn{Chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "NO"},
		{Type: provider.ChunkDone},
	}})
	tt := newConfirmationTool(true, []string{"对"}, true, mp)
	agent := newTestAgent("帮我改bug，但不确定")
	ctx = WithAgent(ctx, agent)

	if err := tt.checkUserConfirmation(ctx); err == nil {
		t.Fatal("expected error for semantic NO, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestCheckUserConfirmation_SemanticFallback_YesWithPunctuation
//
// Validates the HasPrefix fix: "YES.", "YES\n", "YES, confirmed" all pass.
// ---------------------------------------------------------------------------

func TestCheckUserConfirmation_SemanticFallback_YesWithPunctuation(t *testing.T) {
	cases := []string{
		"YES.",
		"YES\n",
		"YES, confirmed",
		"YES, 可以",
	}
	for _, text := range cases {
		t.Run(text, func(t *testing.T) {
			ctx, cancel := withCancel(t)
			defer cancel()

			mp := testutil.NewMock("test", testutil.Turn{Chunks: []provider.Chunk{
				{Type: provider.ChunkText, Text: text},
				{Type: provider.ChunkDone},
			}})
			tt := newConfirmationTool(true, []string{"对"}, true, mp)
			agent := newTestAgent("可以，开始吧")
			ctx = WithAgent(ctx, agent)

			if err := tt.checkUserConfirmation(ctx); err != nil {
				t.Fatalf("expected nil error for response %q, got: %v", text, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCheckUserConfirmation_SemanticFallback_StreamError
// ---------------------------------------------------------------------------

func TestCheckUserConfirmation_SemanticFallback_StreamError(t *testing.T) {
	ctx, cancel := withCancel(t)
	defer cancel()

	mp := testutil.NewMock("test", testutil.Turn{
		StreamError: errors.New("network failure"),
	})
	tt := newConfirmationTool(true, []string{"对"}, true, mp)
	agent := newTestAgent("可以，开始吧")
	ctx = WithAgent(ctx, agent)

	if err := tt.checkUserConfirmation(ctx); err == nil {
		t.Fatal("expected error when Stream returns an error, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestCheckUserConfirmation_SemanticFallback_ChunkError
// ---------------------------------------------------------------------------

func TestCheckUserConfirmation_SemanticFallback_ChunkError(t *testing.T) {
	ctx, cancel := withCancel(t)
	defer cancel()

	mp := testutil.NewMock("test", testutil.Turn{Chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "YE"},
		{Type: provider.ChunkError, Err: errors.New("mid-stream failure")},
	}})
	tt := newConfirmationTool(true, []string{"对"}, true, mp)
	agent := newTestAgent("可以，开始吧")
	ctx = WithAgent(ctx, agent)

	if err := tt.checkUserConfirmation(ctx); err == nil {
		t.Fatal("expected error when chunk error occurs, got nil")
	}
}
