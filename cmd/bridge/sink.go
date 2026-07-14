package main

import (
	"log"
	"fmt"
	"strings"
	"sync"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// sinkState holds per-turn and cumulative state for the Sink.
type sinkState struct {
	mu sync.Mutex

	// Per-turn state (reset on TurnStarted)
	draftID       int64
	dialogueUI    bool // true if this turn should produce draft UI
	autoReentry   bool // internal multi-agent wake — not a user-facing turn
	showThinking  bool // render reasoning as MarkdownV2 spoiler
	reasoningText strings.Builder
	answerText    strings.Builder
	noticeText    strings.Builder
	lastContent   string // last rendered draft content
	// partialTools tracks early ToolDispatch (Partial) so we don't double-count start.
	partialTools map[string]bool

	// Cumulative state (persists across turns)
	lastUsage    *provider.Usage
	sessionCost  float64
	sessionCache struct{ hit, miss int }

	// Callbacks (set by bridge). Invoked WITHOUT holding mu.
	onDraftUpdate  func(chatID int64, draftID int64, text string)
	onDraftClose   func(chatID int64, draftID int64, finalText string)
	onDraftDismiss func(chatID int64, draftID int64)
	onAsk          func(chatID int64, ask event.Ask)
	onApprove      func(chatID int64, approval event.Approval)
	onToolCall     func(chatID int64, toolName, args, status string, elapsed time.Duration)
	onTurnError    func(chatID int64, err error)
	onNotice       func(chatID int64, text string)
	chatID         int64

	// delivered is set when a user-visible Telegram message was successfully sent
	// for the current turn (draft/close/approval/error). Used by handleSubmit's
	// history fallback to avoid double-sending.
	delivered bool
}

func newSinkState(chatID int64) *sinkState {
	return &sinkState{chatID: chatID}
}

// wireCallbacks replaces all delivery callbacks. Safe to call between turns.
func (s *sinkState) wireCallbacks(
	onUpdate func(chatID int64, draftID int64, text string),
	onClose func(chatID int64, draftID int64, finalText string),
	onDismiss func(chatID int64, draftID int64),
	onAsk func(chatID int64, ask event.Ask),
	onApprove func(chatID int64, approval event.Approval),
	onToolCall func(chatID int64, toolName, args, status string, elapsed time.Duration),
	onTurnError func(chatID int64, err error),
	onNotice func(chatID int64, text string),
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onDraftUpdate = onUpdate
	s.onDraftClose = onClose
	s.onDraftDismiss = onDismiss
	s.onAsk = onAsk
	s.onApprove = onApprove
	s.onToolCall = onToolCall
	s.onTurnError = onTurnError
	s.onNotice = onNotice
}

// Emit satisfies event.Sink. Per §4.9: must never block waiting on user.
// Callbacks run after releasing mu so Telegram I/O cannot deadlock the agent.
func (s *sinkState) Emit(e event.Event) {
	var (
		doUpdate    bool
		updateText  string
		draftID     int64
		doClose     bool
		closeText   string
		doDismiss   bool
		dismissID   int64
		doAsk       bool
		ask         event.Ask
		doApprove   bool
		approval    event.Approval
		doTool      bool
		toolName    string
		toolArgs    string
		toolStatus  string
		toolElapsed time.Duration
		doErr       bool
		turnErr     error
		doNotice    bool
		noticePlain string
	)

	s.mu.Lock()
	chatID := s.chatID

	switch e.Kind {
	case event.TurnStarted:
		s.resetTurn()
		s.autoReentry = e.AutoReentry
		// Auto-reentry (empty wake after sub-agent completion) is not a new
		// user turn: no draft bubble, no second final/usage blast.
		if e.AutoReentry {
			s.dialogueUI = false
			break
		}
		s.dialogueUI = true
		s.draftID++
		dismissID = s.draftID - 1
		doDismiss = true
		// Immediately open a live draft bubble so the user sees typewriter
		// even before the first token (Bot API sendRichMessageDraft).
		doUpdate, updateText, draftID = true, "…", s.draftID

	case event.Reasoning:
		if s.dialogueUI {
			s.reasoningText.WriteString(e.Text)
			if text, ok := s.draftIfChanged(); ok {
				doUpdate, updateText, draftID = true, text, s.draftID
			}
		}

	case event.Text:
		if s.dialogueUI {
			s.answerText.WriteString(e.Text)
			if text, ok := s.draftIfChanged(); ok {
				doUpdate, updateText, draftID = true, text, s.draftID
			}
		}

	case event.Message:
		// §4.9: Message overwrites final answer (includes full reasoning if needed)
		if s.dialogueUI && strings.TrimSpace(e.Text) != "" {
			s.answerText.Reset()
			s.answerText.WriteString(e.Text)
			if e.Reasoning != "" {
				s.reasoningText.Reset()
				s.reasoningText.WriteString(e.Reasoning)
			}
			if text, ok := s.draftIfChanged(); ok {
				doUpdate, updateText, draftID = true, text, s.draftID
			}
		}

	case event.ToolDispatch:
		if s.dialogueUI {
			if s.partialTools == nil {
				s.partialTools = make(map[string]bool)
			}
			// Partial start: log once, don't spam draft; full dispatch updates line.
			if e.Tool.Partial {
				if !s.partialTools[e.Tool.ID] {
					s.partialTools[e.Tool.ID] = true
					doTool = true
					toolName, toolArgs, toolStatus = e.Tool.Name, e.Tool.Args, "started"
				}
				break
			}
			if text, ok := s.draftIfChanged(); ok {
				doUpdate, updateText, draftID = true, text, s.draftID
			}
			if !s.partialTools[e.Tool.ID] {
				doTool = true
				toolName, toolArgs, toolStatus = e.Tool.Name, e.Tool.Args, "started"
			}
			s.partialTools[e.Tool.ID] = true
		}

	case event.ToolResult:
		if s.dialogueUI {
			elapsed := time.Duration(e.Tool.DurationMs) * time.Millisecond
			if text, ok := s.draftIfChanged(); ok {
				doUpdate, updateText, draftID = true, text, s.draftID
			}
			doTool = true
			toolName, toolArgs, toolStatus, toolElapsed = e.Tool.Name, e.Tool.Output, "done", elapsed
		}

	case event.ToolProgress:
		if s.dialogueUI {
			if text, ok := s.draftIfChanged(); ok {
				doUpdate, updateText, draftID = true, text, s.draftID
			}
		}

	case event.AgentStatus:
		if s.dialogueUI {
			if text, ok := s.draftIfChanged(); ok {
				doUpdate, updateText, draftID = true, text, s.draftID
			}
		}

	case event.Usage:
		s.lastUsage = e.Usage
		if e.SessionCost > 0 {
			s.sessionCost = e.SessionCost
		}
		if e.CacheDiagnostics != nil {
			s.sessionCache.hit += e.CacheDiagnostics.CacheHitTokens
			s.sessionCache.miss += e.CacheDiagnostics.CacheMissTokens
		}

	case event.Notice:
		if s.dialogueUI {
			s.noticeText.WriteString(e.Text)
			if text, ok := s.draftIfChanged(); ok {
				doUpdate, updateText, draftID = true, text, s.draftID
			}
		} else if strings.TrimSpace(e.Text) != "" {
			// Management slash / !shell notices — no TurnStarted, still show user.
			doNotice = true
			noticePlain = e.Text
		}

	case event.Phase:
		// Lightweight, no draft update needed

	case event.CompactionStarted:
		if s.dialogueUI {
			s.noticeText.WriteString("正在压缩上下文…")
			if text, ok := s.draftIfChanged(); ok {
				doUpdate, updateText, draftID = true, text, s.draftID
			}
		}

	case event.CompactionDone:
		if s.dialogueUI {
			s.noticeText.WriteString("压缩完成")
			if text, ok := s.draftIfChanged(); ok {
				doUpdate, updateText, draftID = true, text, s.draftID
			}
		}

	case event.Retrying:
		if s.dialogueUI {
			msg := "正在重试…"
			if e.RetryAttempt > 0 {
				msg = "正在重试 (" + itoa(e.RetryAttempt) + "/" + itoa(e.RetryMax) + ")…"
			}
			s.noticeText.WriteString(msg)
			if text, ok := s.draftIfChanged(); ok {
				doUpdate, updateText, draftID = true, text, s.draftID
			}
		}

	case event.ApprovalRequest:
		doApprove = true
		approval = e.Approval

	case event.AskRequest:
		doAsk = true
		ask = e.Ask

	case event.TurnDone:
		if s.autoReentry {
			// Internal wake: only surface a real new answer; never a second usage-only bubble.
			ans := strings.TrimSpace(s.answerText.String())
			s.dialogueUI = false
			s.autoReentry = false
			if ans != "" {
				// Rare: model produced user-visible text on reentry — deliver answer only.
				doClose, closeText, draftID = true, ans, s.draftID
			}
			break
		}
		if s.dialogueUI {
			// User-facing turn: final answer (not tool dump) + one usage footer.
			content := s.renderFinal()
			draftID = s.draftID
			s.dialogueUI = false
			if content != "" {
				doClose, closeText = true, content
			} else {
				doDismiss, dismissID = true, draftID
			}
			if e.Err != nil && content == "" {
				doErr, turnErr = true, e.Err
			} else if e.Err != nil {
				// Answer delivered; still surface error as a separate notice line if useful.
				// Prefer not to spam on cancel/interrupt.
				if !isBenignTurnErr(e.Err) {
					doErr, turnErr = true, e.Err
				}
			}
		} else if e.Err != nil && !isBenignTurnErr(e.Err) {
			doErr, turnErr = true, e.Err
		}
	}

	onUpdate := s.onDraftUpdate
	onClose := s.onDraftClose
	onDismiss := s.onDraftDismiss
	onAskCb := s.onAsk
	onApproveCb := s.onApprove
	onToolCb := s.onToolCall
	onErrCb := s.onTurnError
	onNoticeCb := s.onNotice
	s.mu.Unlock()

	if doDismiss && onDismiss != nil {
		onDismiss(chatID, dismissID)
	}
	if doUpdate && onUpdate != nil {
		log.Printf("[chat %d] sink draft update draft_id=%d len=%d", chatID, draftID, len([]rune(updateText)))
		onUpdate(chatID, draftID, updateText)
	}
	if doTool && onToolCb != nil {
		onToolCb(chatID, toolName, toolArgs, toolStatus, toolElapsed)
	}
	if doApprove {
		if onApproveCb == nil {
			log.Printf("[chat %d] APPROVAL REQUEST dropped: onApprove nil (sink not wired) id=%s tool=%s", chatID, approval.ID, approval.Tool)
		} else {
			onApproveCb(chatID, approval)
		}
	}
	if doAsk && onAskCb != nil {
		onAskCb(chatID, ask)
	}
	if doNotice && onNoticeCb != nil {
		onNoticeCb(chatID, noticePlain)
	}
	if doClose && onClose != nil {
		onClose(chatID, draftID, closeText)
	}
	if doErr && onErrCb != nil {
		onErrCb(chatID, turnErr)
	}
}

func isBenignTurnErr(err error) bool {
	if err == nil {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "context cancelled") ||
		strings.Contains(msg, "interrupted")
}

func (s *sinkState) resetTurn() {
	s.reasoningText.Reset()
	s.answerText.Reset()
	s.noticeText.Reset()
	s.lastContent = ""
	s.partialTools = nil
	// autoReentry set by TurnStarted; leave draftID across reentry
}

// draftIfChanged returns rendered content when it changed. Caller holds mu.
func (s *sinkState) draftIfChanged() (string, bool) {
	content := s.renderContent()
	if content == s.lastContent {
		return "", false
	}
	s.lastContent = content
	return content, true
}

// renderContent builds the live-draft / stream body as RAW markdown for
// sendRichMessageDraft (never MarkdownV2-escape — that kills rich syntax).
func (s *sinkState) renderContent() string {
	var b strings.Builder

	// Thinking block for rich draft typewriter (Bot API tg-thinking)
	if s.showThinking && s.reasoningText.Len() > 0 {
		b.WriteString("<tg-thinking>")
		b.WriteString(s.reasoningText.String())
		b.WriteString("</tg-thinking>\n")
	}

	if s.noticeText.Len() > 0 {
		b.WriteString(s.noticeText.String())
		b.WriteString("\n")
	}
	b.WriteString(s.answerText.String())

	return b.String()
}

// plainAnswer returns unescaped answer text for plain-text Telegram fallback.
func (s *sinkState) plainAnswer() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.answerText.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// renderFinal builds finalize body as RAW markdown for sendRichMessage.
// User-facing: answer first; tool/status lines stay out of the final bubble
// (they already streamed in draft). One usage footer at most.
func (s *sinkState) renderFinal() string {
	var b strings.Builder

	if s.showThinking && s.reasoningText.Len() > 0 {
		b.WriteString("<tg-thinking>")
		b.WriteString(s.reasoningText.String())
		b.WriteString("</tg-thinking>\n")
	}
	ans := strings.TrimSpace(s.answerText.String())
	if ans != "" {
		b.WriteString(ans)
	}
	if s.lastUsage != nil {
		u := s.lastUsage
		uu := *u
		uu.NormalizeCache()
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		line := fmt.Sprintf("📊 输入 %d · 输出 %d · 总计 %d", uu.PromptTokens, uu.CompletionTokens, uu.TotalTokens)
		if s.sessionCost > 0 {
			line += fmt.Sprintf(" · 成本 %.6f", s.sessionCost)
		}
		b.WriteString(line)
	}
	return b.String()
}

// SetShowThinking enables or disables thinking spoiler rendering for this turn.
func (s *sinkState) SetShowThinking(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.showThinking = on
}

func (s *sinkState) resetDelivery() {
	s.mu.Lock()
	s.delivered = false
	s.mu.Unlock()
}

func (s *sinkState) GetDraftState() (string, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renderFinal(), s.draftID
}

func (s *sinkState) ResetForInlineApproval() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetTurn()
	s.dialogueUI = false
}

func (s *sinkState) ResetTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetTurn()
}

func (s *sinkState) markDelivered() {
	s.mu.Lock()
	s.delivered = true
	s.mu.Unlock()
}

func (s *sinkState) wasDelivered() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delivered
}

// GetLastUsage returns the most recent token usage snapshot.
func (s *sinkState) GetLastUsage() *provider.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUsage
}

// GetSessionCost returns the cumulative session cost.
func (s *sinkState) GetSessionCost() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionCost
}

// GetCacheStats returns the cumulative cache hit and miss token counts.
func (s *sinkState) GetCacheStats() (hit, miss int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionCache.hit, s.sessionCache.miss
}
