package openai

import "testing"

func runSplitter(deltas []string) (reasoning, text string) {
	var t thinkSplitter
	for _, d := range deltas {
		r, txt := t.push(d)
		reasoning += r
		text += txt
	}
	r, txt := t.flush()
	return reasoning + r, text + txt
}

func TestThinkSplitter(t *testing.T) {
	// Helper that enables text thinking detection.
	runSplitterWithText := func(deltas []string) (reasoning, text string) {
		var t thinkSplitter
		t.textOpeners = thinkingOpeners
		for _, d := range deltas {
			r, txt := t.push(d)
			reasoning += r
			text += txt
		}
		r, txt := t.flush()
		return reasoning + r, text + txt
	}

	cases := []struct {
		name      string
		deltas    []string
		reasoning string
		text      string
	}{
		{
			name:      "whole block in one delta",
			deltas:    []string{"<think>reasoning here</think>the answer"},
			reasoning: "reasoning here",
			text:      "the answer",
		},
		{
			name:      "open tag split across deltas",
			deltas:    []string{"<th", "ink>chain", " of thought</think>answer"},
			reasoning: "chain of thought",
			text:      "answer",
		},
		{
			name:      "close tag split across deltas",
			deltas:    []string{"<think>thinking</thi", "nk>done"},
			reasoning: "thinking",
			text:      "done",
		},
		{
			name:      "leading whitespace before think is dropped",
			deltas:    []string{"\n\n  <think>r</think>\n\nanswer"},
			reasoning: "r",
			text:      "answer",
		},
		{
			name:      "no think tag passes through as text",
			deltas:    []string{"just a normal ", "answer"},
			reasoning: "",
			text:      "just a normal answer",
		},
		{
			name:      "think mentioned mid-answer is not hijacked",
			deltas:    []string{"the model emits <think> tags around its reasoning"},
			reasoning: "",
			text:      "the model emits <think> tags around its reasoning",
		},
		{
			name:      "unterminated think block flushes as reasoning",
			deltas:    []string{"<think>still thinking when the stream ended"},
			reasoning: "still thinking when the stream ended",
			text:      "",
		},
		{
			name:      "per-character streaming",
			deltas:    []string{"<", "t", "h", "i", "n", "k", ">", "a", "</", "think>", "b"},
			reasoning: "a",
			text:      "b",
		},
	}

	// Tag-based thinking tests (textOpeners = nil) — existing behavior unchanged.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, txt := runSplitter(tc.deltas)
			if r != tc.reasoning {
				t.Errorf("reasoning = %q, want %q", r, tc.reasoning)
			}
			if txt != tc.text {
				t.Errorf("text = %q, want %q", txt, tc.text)
			}
		})
	}

	// Text-based thinking tests (textOpeners enabled).
	textCases := []struct {
		name      string
		deltas    []string
		reasoning string
		text      string
	}{
		{
			name:      "english thinking opener detected",
			deltas:    []string{"Let me check the git log"},
			reasoning: "Let me check the git log",
			text:      "",
		},
		{
			name:      "english opener split across chunks",
			deltas:    []string{"Let ", "me check", " the file"},
			reasoning: "Let me check the file",
			text:      "",
		},
		{
			name:      "chinese thinking opener detected",
			deltas:    []string{"让我先看看代码"},
			reasoning: "让我先看看代码",
			text:      "",
		},
		{
			name:      "normal english no opener",
			deltas:    []string{"hello world"},
			reasoning: "",
			text:      "hello world",
		},
		{
			name:      "normal chinese no opener",
			deltas:    []string{"编译通过，测试通过"},
			reasoning: "",
			text:      "编译通过，测试通过",
		},
	}

	for _, tc := range textCases {
		t.Run("text/"+tc.name, func(t *testing.T) {
			r, txt := runSplitterWithText(tc.deltas)
			if r != tc.reasoning {
				t.Errorf("reasoning = %q, want %q", r, tc.reasoning)
			}
			if txt != tc.text {
				t.Errorf("text = %q, want %q", txt, tc.text)
			}
		})
	}
}
