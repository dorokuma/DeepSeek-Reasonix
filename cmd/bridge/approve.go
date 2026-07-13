// approve.go — Approval interaction for inline-keyboard-driven tool approval.
package main

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"reasonix/internal/event"
)

type approvalState struct {
	MessageID int
	ChatID    int64
}

var (
	approvalStatesMu sync.Mutex
	approvalStates   = make(map[string]*approvalState) // approvalID → state
)

// handleApprove renders an inline-keyboard approval message.
// Must never silently drop: if Telegram send fails, deny the gate so the turn
// unblocks with a visible error instead of hanging until context cancel.
func (b *Bridge) handleApprove(chatID int64, approval event.Approval) {
	log.Printf("[chat %d] APPROVAL POPUP id=%s tool=%q subject=%q scope=%q",
		chatID, approval.ID, approval.Tool, approval.Subject, approval.Scope)

	var buttons [][]InlineKeyboardButton
	if approval.Scope == "task" {
		buttons = [][]InlineKeyboardButton{
			NewInlineKeyboardRow(
				NewInlineKeyboardButtonData("▶️ 启动", "approve:"+approval.ID+":once"),
				NewInlineKeyboardButtonData("❌ 取消", "approve:"+approval.ID+":deny"),
			),
		}
	} else {
		buttons = [][]InlineKeyboardButton{
			NewInlineKeyboardRow(
				NewInlineKeyboardButtonData("✅ 允许一次", "approve:"+approval.ID+":once"),
				NewInlineKeyboardButtonData("🔁 会话允许", "approve:"+approval.ID+":session"),
			),
			NewInlineKeyboardRow(
				NewInlineKeyboardButtonData("📌 持久允许", "approve:"+approval.ID+":persist"),
				NewInlineKeyboardButtonData("❌ 拒绝", "approve:"+approval.ID+":deny"),
			),
		}
	}
	markup := NewInlineKeyboardMarkup(buttons...)

	// Plain text only — no ParseMode. MarkdownV2 parse failures were a silent
	// path to "no popup + hung approval".
	subject := strings.TrimSpace(approval.Subject)
	if subject == "" {
		subject = strings.TrimSpace(approval.Preview)
	}
	if len([]rune(subject)) > 800 {
		subject = string([]rune(subject)[:800]) + "…"
	}
	text := fmt.Sprintf("🔐 %s\n%s", approval.Tool, subject)

	msg := NewMessage(chatID, text)
	msg.ReplyMarkup = markup
	// Explicitly no ParseMode
	msg.ParseMode = ""

	sent, err := b.client.Send(b.ctx, msg)
	if err != nil {
		log.Printf("[chat %d] APPROVAL POPUP send failed: %v — plain retry", chatID, err)
		msg2 := NewMessage(chatID, text)
		msg2.ReplyMarkup = markup
		sent, err = b.client.Send(b.ctx, msg2)
	}
	if err != nil {
		log.Printf("[chat %d] APPROVAL POPUP FAILED id=%s: %v — denying to unblock turn", chatID, approval.ID, err)
		b.sendMessage(chatID, fmt.Sprintf("❌ 审批失败已拒 %s: %v", approval.Tool, err))
		if ctrl := b.sm.ControllerFor(chatID); ctrl != nil {
			ctrl.Approve(approval.ID, false, false, false)
		}
		return
	}

	log.Printf("[chat %d] APPROVAL POPUP sent id=%s msg_id=%d", chatID, approval.ID, sent.MessageID)
	approvalStatesMu.Lock()
	approvalStates[approval.ID] = &approvalState{MessageID: sent.MessageID, ChatID: chatID}
	approvalStatesMu.Unlock()
}

// handleApproveCallback processes an approval inline-keyboard callback.
// Data format: "approve:<id>:<action>" where action is once/session/persist/deny.
func (b *Bridge) handleApproveCallback(chatID int64, data string) {
	parts := strings.Split(data, ":")
	if len(parts) < 3 || parts[0] != "approve" {
		log.Printf("[chat %d] approve callback bad data %q", chatID, data)
		return
	}

	approvalID := parts[1]
	action := parts[2]
	log.Printf("[chat %d] APPROVAL CLICK id=%s action=%s", chatID, approvalID, action)

	approvalStatesMu.Lock()
	state, exists := approvalStates[approvalID]
	approvalStatesMu.Unlock()

	ctrl := b.sm.ControllerFor(chatID)
	if ctrl == nil {
		log.Printf("[chat %d] approve click: no controller", chatID)
		b.sendMessage(chatID, "❌ 无会话，审批取消")
		return
	}

	if !exists || state.ChatID != chatID {
		// Process may have restarted or message lost tracking — still try Approve
		// so a stuck gate can release if the id is still pending.
		log.Printf("[chat %d] approve click: no tracked state for id=%s (still forwarding)", chatID, approvalID)
	}

	var answerText string
	switch action {
	case "once":
		ctrl.Approve(approvalID, true, false, false)
		answerText = "✅ 允一次"
	case "session":
		ctrl.Approve(approvalID, true, true, false)
		answerText = "🔁 已设置为会话允许"
	case "persist":
		ctrl.Approve(approvalID, true, false, true)
		answerText = "📌 已设置为持久允许"
	case "deny":
		ctrl.Approve(approvalID, false, false, false)
		answerText = "❌ 拒绝"
	default:
		log.Printf("[chat %d] approve unknown action %q", chatID, action)
		return
	}

	if exists && state != nil && state.MessageID > 0 {
		b.deleteMessage(chatID, state.MessageID)
	}
	approvalStatesMu.Lock()
	delete(approvalStates, approvalID)
	approvalStatesMu.Unlock()

	b.sendMessage(chatID, answerText)
}

func (b *Bridge) deleteMessage(chatID int64, msgID int) {
	_, err := b.client.Send(b.ctx, NewDeleteMessage(chatID, msgID))
	if err != nil {
		log.Printf("deleteMessage error (chat %d, msg %d): %v", chatID, msgID, err)
	}
}
