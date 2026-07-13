// ask.go — Ask interaction for inline-keyboard-driven multi-question forms.
package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"reasonix/internal/event"
)

// ---------------------------------------------------------------------------
// askState — per-ask session state
// ---------------------------------------------------------------------------

type askState struct {
	ID         string
	ChatID     int64
	Questions  []event.AskQuestion
	CurrentIdx int             // which question we're on
	Selected   []map[int]bool // selections per question (index = question index)
	MessageID  int            // telegram message ID of the latest ask message
}

var (
	askStatesMu sync.Mutex
	askStates   = make(map[string]*askState)
)

// ---------------------------------------------------------------------------
// Public API called from handler.go
// ---------------------------------------------------------------------------

// handleAsk begins an ask interaction by rendering the first question.
// Called from the sink's onAsk callback.
func (b *Bridge) handleAsk(chatID int64, ask event.Ask) {
	state := &askState{
		ID:         ask.ID,
		ChatID:     chatID,
		Questions:  ask.Questions,
		CurrentIdx: 0,
		Selected:   make([]map[int]bool, len(ask.Questions)),
	}
	for i := range state.Selected {
		state.Selected[i] = make(map[int]bool)
	}

	askStatesMu.Lock()
	askStates[ask.ID] = state
	askStatesMu.Unlock()

	b.renderAskQuestion(chatID, state)
}

// handleAskCallback processes a callback from an ask inline keyboard.
// Data format: "ask:<id>:<qIdx>:<optIdx>" or "ask:<id>:<qIdx>:confirm"
func (b *Bridge) handleAskCallback(chatID int64, cq *CallbackQuery, data string) {
	parts := strings.Split(data, ":")
	if len(parts) < 4 || parts[0] != "ask" {
		b.answerCallback(cq, "")
		return
	}

	askID := parts[1]
	qIdx, err := strconv.Atoi(parts[2])
	if err != nil {
		b.answerCallback(cq, "⚠️ 参数错误")
		return
	}
	action := parts[3]

	askStatesMu.Lock()
	state, exists := askStates[askID]
	askStatesMu.Unlock()
	if !exists {
		b.answerCallback(cq, "⌛ 会话已过期")
		return
	}

	if qIdx < 0 || qIdx >= len(state.Questions) {
		b.answerCallback(cq, "⚠️ 参数错误")
		return
	}
	q := state.Questions[qIdx]

	// --- confirm action (multi-select only) ---
	if action == "confirm" {
		state.CurrentIdx++
		if state.CurrentIdx >= len(state.Questions) {
			b.answerCallback(cq, "✅ 已提交")
			b.finalizeAsk(chatID, state)
		} else {
			b.answerCallback(cq, "")
			b.renderAskQuestion(chatID, state)
		}
		return
	}

	// --- option click ---
	optIdx, err := strconv.Atoi(action)
	if err != nil {
		b.answerCallback(cq, "⚠️ 参数错误")
		return
	}
	if optIdx < 0 || optIdx >= len(q.Options) {
		b.answerCallback(cq, "⚠️ 参数错误")
		return
	}

	if q.Multi {
		// Toggle selection and re-render the same question
		state.Selected[qIdx][optIdx] = !state.Selected[qIdx][optIdx]
		b.answerCallback(cq, "")
		b.renderAskQuestion(chatID, state)
	} else {
		// Single-select — select and advance immediately
		state.Selected[qIdx][optIdx] = true
		state.CurrentIdx++
		if state.CurrentIdx >= len(state.Questions) {
			b.answerCallback(cq, "✅ 已提交")
			b.finalizeAsk(chatID, state)
		} else {
			b.answerCallback(cq, "")
			b.renderAskQuestion(chatID, state)
		}
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// renderAskQuestion sends or edits the Telegram message for the current question.
func (b *Bridge) renderAskQuestion(chatID int64, state *askState) {
	q := state.Questions[state.CurrentIdx]

	// Build question text
	var text string
	if len(state.Questions) > 1 {
		text = fmt.Sprintf("📋 问题 %d/%d · %s\n%s",
			state.CurrentIdx+1, len(state.Questions), q.Header, q.Prompt)
	} else {
		text = fmt.Sprintf("📋 %s\n%s", q.Header, q.Prompt)
	}

	// Build option buttons
	var buttons [][]InlineKeyboardButton
	for i, opt := range q.Options {
		label := opt.Label
		if q.Multi {
			if state.Selected[state.CurrentIdx][i] {
				label = "☑ " + label
			} else {
				label = "☐ " + label
			}
		}
		data := fmt.Sprintf("ask:%s:%d:%d", state.ID, state.CurrentIdx, i)
		buttons = append(buttons, NewInlineKeyboardRow(
			NewInlineKeyboardButtonData(label, data),
		))
	}

	// For multi-select: add a confirm button
	if q.Multi {
		confirmData := fmt.Sprintf("ask:%s:%d:confirm", state.ID, state.CurrentIdx)
		buttons = append(buttons, NewInlineKeyboardRow(
			NewInlineKeyboardButtonData("✅ 确认选择", confirmData),
		))
	}

	markup := NewInlineKeyboardMarkup(buttons...)

	if state.MessageID == 0 {
		// First time — send a new message
		msg := NewMessage(chatID, text)
		msg.ReplyMarkup = markup
		sent, err := b.client.Send(b.ctx, msg)
		if err != nil {
			log.Printf("handleAsk send error (chat %d): %v", chatID, err)
			return
		}
		state.MessageID = sent.MessageID
	} else {
		// Subsequent — edit the existing message text and markup
		edit := NewEditMessageText(chatID, state.MessageID, text)
		edit.ReplyMarkup = markup
		_, err := b.client.Send(b.ctx, edit)
		if err != nil && !telegramErrorIsNotModified(err) {
			log.Printf("handleAsk edit error (chat %d): %v", chatID, err)
		}
	}
}

// finalizeAsk collects all answers and submits them via the controller.
func (b *Bridge) finalizeAsk(chatID int64, state *askState) {
	var answers []event.AskAnswer
	for i, q := range state.Questions {
		var selected []string
		for j, opt := range q.Options {
			if state.Selected[i][j] {
				selected = append(selected, opt.Label)
			}
		}
		answers = append(answers, event.AskAnswer{
			QuestionID: q.ID,
			Selected:   selected,
		})
	}

	ctrl := b.sm.ControllerFor(chatID)
	if ctrl != nil {
		ctrl.AnswerQuestion(state.ID, answers)
	}

	askStatesMu.Lock()
	delete(askStates, state.ID)
	askStatesMu.Unlock()
}

// ClearPendingAsks safely clears all cached ask states for the given chat ID.
func ClearPendingAsks(chatID int64) {
	askStatesMu.Lock()
	defer askStatesMu.Unlock()
	for id, state := range askStates {
		if state.ChatID == chatID {
			delete(askStates, id)
		}
	}
}
