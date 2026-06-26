package openai

import (
	"log"
	"strings"
)

const (
	thinkOpen            = "<think>"
	thinkClose           = "</think>"
	maxProbeBufSize      = 1 << 12 // 4 KiB: cap probe state buffer to prevent unbounded growth
	maxTextThinkLen      = 4000    // auto-close text-based thinking after this many bytes
)

type thinkState int

const (
	thinkProbe thinkState = iota
	thinkInside           // inside <think>...</think> tags
	textThinkInside       // inside text-based thinking (no close tag)
	thinkPassthrough
)

// thinkingOpeners are known chain-of-thought sentence starters that indicate
// the model is reasoning aloud in the text channel (not the reasoning_content
// channel). These are used by thinkSplitter in "text thinking" mode to detect
// thinking leaked into delta.content without <think> tags.
var thinkingOpeners = []string{
	// English (matching bridge-level detection)
	"let me", "let's", "i need to", "i need", "i'll", "i should",
	"i have to", "i want to", "i'm going to", "first,", "now i",
	"looking at", "checking", "let me check", "i'll check",
	"let's see", "okay, let", "ok, let", "alright, let",
	"i should check", "i should look", "i should verify",
	"let me look", "let me see", "let me think",
	"i'll look", "i'll see", "i'll think",
	"i need to check", "i need to look", "i need to verify",
	"let me verify", "let me confirm", "let me understand",
	// Chinese — self-talk, not user-facing
	"让我先", "让我看看", "让我想", "让我分析",
	"让我查", "让我确认", "让我验证",
	"我先看", "我先检查", "我先分析", "我先确认",
	"需要先看看", "需要先检查", "需要先确认",
}

// thinkSplitter peels a leading <think>...</think> block out of the content
// stream into reasoning text. MiniMax-M3 inlines its chain-of-thought this way
// instead of populating reasoning_content. It only arms on a <think> at the very
// start of the turn, so an answer that merely mentions the tag is never hijacked.
//
// When textOpeners is non-empty, the splitter also checks the start of the
// content stream against known thinking sentence starters. If matched, the
// content is redirected to the reasoning channel (no close tag needed — the
// agent's empty-text recovery handles the missing reply).
type thinkSplitter struct {
	state thinkState
	buf   string
	textOpeners []string // if non-nil, check these text patterns in probe mode
}

func (t *thinkSplitter) push(s string) (reasoning, text string) {
	switch t.state {
	case thinkPassthrough:
		return "", s
	case thinkInside:
		return t.scanClose(s)
	case textThinkInside:
		return t.scanTextClose(s)
	}

	t.buf += s
	if len(t.buf) > maxProbeBufSize {
		return "", t.drainPassthrough()
	}
	trimmed := strings.TrimLeft(t.buf, " \t\r\n")
	if len(trimmed) < len(thinkOpen) {
		if strings.HasPrefix(thinkOpen, trimmed) {
			return "", "" // still could become <think> once more arrives
		}
		// Not a <think> start. If text openers enabled, check if trimmed
		// could be a prefix of any opener before draining.
		if len(t.textOpeners) > 0 {
			lower := strings.ToLower(trimmed)
			for _, opener := range t.textOpeners {
				if strings.HasPrefix(opener, lower) {
					return "", "" // wait for more before deciding
				}
			}
		}
		return "", t.drainPassthrough()
	}
	if strings.HasPrefix(trimmed, thinkOpen) {
		t.state = thinkInside
		t.buf = ""
		return t.scanClose(trimmed[len(thinkOpen):])
	}
	// Check text-based thinking openers (when enabled and no reasoning_content seen).
	if len(t.textOpeners) > 0 {
		lower := strings.ToLower(trimmed)
		for _, opener := range t.textOpeners {
			if strings.HasPrefix(lower, opener) {
				t.state = textThinkInside
				t.buf = ""
				return t.scanTextClose(trimmed)
			}
		}
		// No opener matched, but trimmed might be a prefix fragment of an
		// opener arriving across multiple chunks (e.g. "Let " → "me check").
		for _, opener := range t.textOpeners {
			if strings.HasPrefix(opener, lower) {
				return "", "" // wait for more before deciding
			}
		}
	}
	return "", t.drainPassthrough()
}

func (t *thinkSplitter) scanClose(s string) (reasoning, text string) {
	t.buf += s
	if idx := strings.Index(t.buf, thinkClose); idx >= 0 {
		r := t.buf[:idx]
		rest := strings.TrimLeft(t.buf[idx+len(thinkClose):], " \t\r\n")
		t.buf = ""
		t.state = thinkPassthrough
		return r, rest
	}
	keep := markerSuffixLen(t.buf, thinkClose)
	r := t.buf[:len(t.buf)-keep]
	t.buf = t.buf[len(t.buf)-keep:]
	return r, ""
}

// scanTextClose accumulates reasoning text without a closing tag.
// Content is accumulated silently and returned by flush(). If the buffer
// exceeds maxTextThinkLen, auto-close and return the overflow as text so
// the actual reply (if any) still reaches the user.
func (t *thinkSplitter) scanTextClose(s string) (reasoning, text string) {
	if len(t.buf)+len(s) > maxTextThinkLen {
		allow := maxTextThinkLen - len(t.buf)
		if allow < 0 {
			allow = 0
		}
		r := t.buf + s[:allow]
		rest := s[allow:]
		log.Printf("scanTextClose truncation: buf=%q, allow=%d, reasoning=%q, rest=%q", t.buf, allow, r, rest)
		t.buf = ""
		t.state = thinkPassthrough
		return r, rest
	}
	log.Printf("scanTextClose accumulate: buf=%q, add=%q", t.buf, s)
	t.buf += s
	return "", "" // accumulate; flush() returns everything
}

// flush emits whatever is buffered when the stream ends mid-decision: an
// unterminated <think> block or text thinking is reasoning; anything else is text.
func (t *thinkSplitter) flush() (reasoning, text string) {
	if t.buf == "" {
		return "", ""
	}
	out := t.buf
	t.buf = ""
	if t.state == thinkInside || t.state == textThinkInside {
		return out, ""
	}
	return "", out
}

func (t *thinkSplitter) drainPassthrough() string {
	t.state = thinkPassthrough
	out := t.buf
	t.buf = ""
	return out
}

// markerSuffixLen returns the length of the longest proper suffix of s that is a
// prefix of marker — the tail to hold back in case the rest of the tag arrives
// in the next delta.
func markerSuffixLen(s, marker string) int {
	max := len(marker) - 1
	if max > len(s) {
		max = len(s)
	}
	for n := max; n > 0; n-- {
		if strings.HasPrefix(marker, s[len(s)-n:]) {
			return n
		}
	}
	return 0
}

