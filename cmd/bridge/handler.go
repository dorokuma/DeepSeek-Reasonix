package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// streamState owns native Telegram live-draft streaming for one chat turn.
// Process path: Bot API sendMessageDraft (typewriter bubble, same draft_id).
// Final path: sendMessage to persist; draft is ephemeral and must not fan out
// into many real messages. Token events only overwrite pending text; one worker
// serializes API calls.
type streamState struct {
	mu       sync.Mutex
	draftID  int64
	pending  string
	hasPend  bool
	running  bool
	closing  bool
	// draftOK: nil=unknown, true=native draft works, false=fall back to edit path
	draftOK *bool
	// fallbackMsgID only used if native draft API is unavailable
	fallbackMsgID int
	idle          chan struct{} // closed while worker is not running; recreated on start
}

// Bridge manages the Telegram bot lifecycle.
type Bridge struct {
	cfg    *Config
	sm     *SessionManager
	client *TelegramClient
	cron   *CronManager
	ctx    context.Context
	cancel context.CancelFunc

	mu sync.Mutex
	// sinks maps chatID → sinkState for active turns.
	sinks map[int64]*sinkState
	// showThinking tracks per-chat /showthinking toggle.
	showThinking map[int64]bool
	// streams: one process-message pipeline per chat (never parallel first-sends).
	streams map[int64]*streamState
	// submitMu serializes handleSubmit per chat so concurrent updates cannot
	// cancel each other's in-flight turns mid-stream.
	submitMu map[int64]*sync.Mutex
}

// NewBridge creates a new Bridge.
func NewBridge(cfg *Config) (*Bridge, error) {
	client, err := NewTelegramClient(cfg.BotToken)
	if err != nil {
		return nil, fmt.Errorf("NewTelegramClient: %w", err)
	}
	sm := NewSessionManager(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	return &Bridge{
		cfg:          cfg,
		sm:           sm,
		client:       client,
		cron:         NewCronManager(sm, client, cfg, ctx),
		ctx:          ctx,
		cancel:       cancel,
		sinks:        make(map[int64]*sinkState),
		showThinking: make(map[int64]bool),
		streams:      make(map[int64]*streamState),
		submitMu:     make(map[int64]*sync.Mutex),
	}, nil
}

// chatSubmitMu returns the per-chat mutex that serializes submissions.
func (b *Bridge) chatSubmitMu(chatID int64) *sync.Mutex {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, ok := b.submitMu[chatID]
	if !ok {
		m = &sync.Mutex{}
		b.submitMu[chatID] = m
	}
	return m
}

// Start begins long-polling for updates.

// ensureChatSink returns a stable per-chat sink and registers it on the session
// manager so ensureController never builds with a nil sink.
func (b *Bridge) ensureChatSink(chatID int64) *sinkState {
	b.mu.Lock()
	s, ok := b.sinks[chatID]
	if !ok {
		s = newSinkState(chatID)
		b.sinks[chatID] = s
	}
	b.mu.Unlock()
	b.sm.SetChatSink(chatID, s)
	return s
}


func (b *Bridge) Start() error {
	log.Printf("starting bridge, bot=%s", b.client.Self.UserName)
	offset := 0
	for {
		select {
		case <-b.ctx.Done():
			return b.ctx.Err()
		default:
		}

		updates, err := b.client.GetUpdates(b.ctx, offset, 60)
		if err != nil {
			// Context cancellation is the only non-retryable error.
			if b.ctx.Err() != nil {
				return b.ctx.Err()
			}
			log.Printf("GetUpdates error: %v", err)
			select {
			case <-b.ctx.Done():
				return b.ctx.Err()
			case <-time.After(3 * time.Second):
			}
			continue
		}

		for _, upd := range updates {
			offset = upd.UpdateID + 1
			if upd.Message != nil {
				log.Printf("[chat %d] update received, offset=%d", upd.Message.Chat.ID, offset)
				go b.handleMessage(upd.Message)
			}
			if upd.CallbackQuery != nil {
				go b.handleCallbackQuery(upd.CallbackQuery)
			}
		}
	}
}

// handleMessage processes an incoming message.
func (b *Bridge) handleMessage(msg *Message) {
	if msg.From == nil || msg.Chat == nil {
		return
	}

	chatID := msg.Chat.ID
	userID := msg.From.ID

	// Permission check
	if !b.isAllowed(userID) {
		log.Printf("blocked user %d (chat %d)", userID, chatID)
		b.sendMessage(chatID, "🚫 无权限")
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	// Route commands
	if strings.HasPrefix(text, "/") {
		b.handleCommand(chatID, userID, text)
		return
	}

	log.Printf("[chat %d] received: %s", chatID, text)

	// Otherwise submit as a regular message
	b.handleSubmit(chatID, text)
}

// handleCommand routes slash commands.
func (b *Bridge) handleCommand(chatID, userID int64, text string) {
	parts := strings.Fields(text)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/stop":
		b.sm.Stop(chatID)
		b.sendMessage(chatID, "⏹ 已停")

	case "/restart":
		b.sendMessage(chatID, "♻️ 重启中")
		go func() {
			time.Sleep(300 * time.Millisecond)
			requestRestart("user /restart")
		}()

	case "/showthinking":
		b.mu.Lock()
		on := !b.showThinking[chatID]
		b.showThinking[chatID] = on
		b.mu.Unlock()
		if on {
			b.sendMessage(chatID, "🧠 思考开")
		} else {
			b.sendMessage(chatID, "🫥 思考关")
		}

	case "/status":
		b.ensureChatSink(chatID)
		ctrl := b.sm.ControllerFor(chatID)
		if ctrl == nil {
			b.sendMessage(chatID, "💤 无会话")
			return
		}
		statusText := "闲置"
		if ctrl.Running() {
			statusText = "运行中"
		}
		label := ctrl.Label()
		turn := ctrl.Turn()

		// Same口径 as TUI status line: Controller.LastUsage / SessionCache / SessionCost
		// (includes main + rolled-up sub-agent usage via Agent.AddSessionUsage).
		var usageStr, costStr, cacheStr string
		if u := ctrl.LastUsage(); u != nil {
			uu := *u
			uu.NormalizeCache()
			usageStr = fmt.Sprintf("入%d 缓%d 新%d · 出%d · 推%d · 计%d",
				uu.PromptTokens, uu.CacheHitTokens, uu.CacheMissTokens,
				uu.CompletionTokens, uu.ReasoningTokens, uu.TotalTokens)
			// Turn hit rate (TUI "now")
			if d := uu.CacheHitTokens + uu.CacheMissTokens; d > 0 {
				cacheStr = fmt.Sprintf("轮%.0f%%", float64(uu.CacheHitTokens)*100/float64(d))
			}
		} else {
			usageStr = "暂无"
		}
		if hit, miss := ctrl.SessionCache(); hit+miss > 0 {
			avg := fmt.Sprintf("会话%.0f%% (%d/%d)",
				float64(hit)*100/float64(hit+miss), hit, miss)
			if cacheStr != "" {
				cacheStr = cacheStr + " · " + avg
			} else {
				cacheStr = avg
			}
		}
		if cacheStr == "" {
			cacheStr = "暂无"
		}
		if cost, cur := ctrl.SessionCost(); cost > 0 {
			if cur == "" {
				cur = "¥"
			}
			costStr = fmt.Sprintf("%s%.4f", cur, cost)
		} else {
			costStr = "暂无"
		}
		if prompt, total := ctrl.SessionTokens(); total > 0 {
			usageStr += fmt.Sprintf("\n累计 入%d 总%d", prompt, total)
		}
		msg := fmt.Sprintf("📊 %s · 轮%d · %s\n🪙 %s\n💾 %s\n💰 %s",
			label, turn, statusText, usageStr, cacheStr, costStr)
		b.sendMessage(chatID, msg)

	case "/model":
		b.handleModel(chatID, strings.TrimSpace(strings.TrimPrefix(text, "/model")))

	case "/new":
		// Core path: Submit("/new") then sync chat→path index (design §5.1).
		b.handleSubmit(chatID, "/new")
		b.sm.SyncSessionPath(chatID)
		return

	case "/help":
		help := "⏹/stop 停  🧠/showthinking 思考\n" +
			"📊/status 状态  🤖/model 模型\n" +
			"🆕/new 新对话  ♻️/restart 重启\n" +
			"其它斜杠/! 同主程序"
		b.sendMessage(chatID, help)

	default:
		// Unrecognized slash commands are treated as normal input (验收 #9, #10).
		b.handleSubmit(chatID, text)
	}
}

// handleSubmit routes a non-command message to the controller.
// Submissions for one chat are serialized: wait for the previous turn to finish
// before starting the next, so a second message cannot cancel the first mid-stream.
// After the turn ends, if the event sink never delivered a visible reply, we
// fall back to sending the last assistant text from session history (plain).
func (b *Bridge) handleSubmit(chatID int64, text string) {
	mu := b.chatSubmitMu(chatID)
	mu.Lock()
	defer mu.Unlock()

	s := b.ensureChatSink(chatID)
	b.mu.Lock()
	showThinking := b.showThinking[chatID]
	b.mu.Unlock()

	s.SetShowThinking(showThinking)
	s.resetDelivery()
	b.resetStream(chatID)
	s.wireCallbacks(
		func(cid int64, draftID int64, content string) {
			if strings.TrimSpace(content) == "" {
				return
			}
			// Tail preview for long streams (official draft limit).
			content = telegramPreviewTail(content, telegramMaxMessageRunes)
			// Coalesce + serial worker → sendRichMessageDraft typewriter.
			b.enqueueStreamUpdate(cid, draftID, content)
			s.markDelivered()
		},
		func(cid int64, draftID int64, finalText string) {
			if strings.TrimSpace(finalText) == "" {
				go b.dismissStream(cid)
				return
			}
			// Mark first so Wait/fallback does not double-send while we flush Telegram.
			s.markDelivered()
			// Finalize must be synchronous enough relative to Wait: run and join
			// so sendRichMessage lands before we declare the turn done.
			if err := b.closeStream(cid, draftID, finalText); err != nil {
				log.Printf("[chat %d] closeStream failed: %v — plain send", cid, err)
				b.sendMessage(cid, stripMdv2Escapes(finalText))
			}
		},
		func(cid int64, draftID int64) {
			b.dismissStream(cid)
		},
		func(cid int64, ask event.Ask) {
			b.handleAsk(cid, ask)
			s.markDelivered() // user-visible keyboard
		},
		func(cid int64, approval event.Approval) {
			b.handleApprove(cid, approval)
			s.markDelivered()
		},
		func(cid int64, toolName, args, status string, elapsed time.Duration) {
			log.Printf("[chat %d] tool: %s %s (%v)", cid, toolName, status, elapsed)
		},
		func(cid int64, err error) {
			if err == nil {
				return
			}
			log.Printf("[chat %d] turn error: %v", cid, err)
			if !isBenignTurnErr(err) {
				b.sendMessage(cid, fmt.Sprintf("❌ %v", err))
				s.markDelivered()
			}
		},
		func(cid int64, text string) {
			if text != "" && !hasLeadingEmoji(text) {
				text = "ℹ️ " + text
			}
			b.sendMessage(cid, text)
			s.markDelivered()
		},
	)

	// Bind sink before ensureController so first Build captures it.
	b.sm.SetChatSink(chatID, s)

	log.Printf("[chat %d] submitting: %s", chatID, text)

	// Show "typing…" for the whole turn (refreshed until stop).
	stopTyping := b.beginTyping(b.ctx, chatID)
	defer stopTyping()

	if err := b.sm.Submit(b.ctx, chatID, text); err != nil {
		log.Printf("sm.Submit error (chat %d): %v", chatID, err)
		b.sendMessage(chatID, fmt.Sprintf("❌ %v", err))
		return
	}

	ctrl := b.sm.ControllerFor(chatID)
	if ctrl == nil {
		log.Printf("[chat %d] no controller after Submit", chatID)
		b.sendMessage(chatID, "💥 会话未建")
		return
	}

	// Block until this turn finishes (we hold the per-chat mutex).
	// Hard cap: a hung model/approval/Telegram path must not freeze the chat forever.
	done := make(chan struct{})
	go func() {
		ctrl.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Minute):
		log.Printf("[chat %d] turn wait timed out (8m) — cancel", chatID)
		ctrl.Cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			log.Printf("[chat %d] cancel still not releasing Wait", chatID)
		}
		b.sendMessage(chatID, "⏰ 超时，重试或 /new")
		s.markDelivered()
		return
	}

	if s.wasDelivered() {
		log.Printf("[chat %d] turn done (sink delivered)", chatID)
		return
	}

	// Hard fallback: model/session often has the answer even when the event sink
	// never pushed to Telegram (nil sink at Build, swallowed draft errors, etc.).
	answer := lastAssistantContent(ctrl.History())
	if answer == "" {
		log.Printf("[chat %d] turn done with no deliverable answer", chatID)
		b.sendMessage(chatID, "😶 无回复，再发或 /new")
		return
	}
	log.Printf("[chat %d] fallback deliver PLAIN answer len=%d", chatID, len([]rune(answer)))
	// Plain text only for reliability — formatMessage on partial streams produced 话说不完.
	body := answer
	if u := s.GetLastUsage(); u != nil {
		body += fmt.Sprintf("\n\n📊 输入 %d · 输出 %d · 总计 %d", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
		if c := s.GetSessionCost(); c > 0 {
			body += fmt.Sprintf(" · 成本 %.6f", c)
		}
	}
	b.sendMessage(chatID, body)
	s.markDelivered()
}

// hasLeadingEmoji reports whether s already starts with a non-ASCII symbol
// (emoji / CJK punctuation used as bullet). Used to avoid double-prefixing.
func hasLeadingEmoji(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	r := []rune(s)[0]
	// ASCII letters/digits/punct → no emoji yet
	if r < 0x80 {
		return false
	}
	return true
}

// lastAssistantContent returns the most recent non-empty assistant text.
func lastAssistantContent(hist []provider.Message) string {
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Role == provider.RoleAssistant {
			if t := strings.TrimSpace(hist[i].Content); t != "" {
				return t
			}
		}
	}
	return ""
}

// handleCallbackQuery processes inline keyboard callbacks.
func (b *Bridge) handleCallbackQuery(cq *CallbackQuery) {
	if cq.Message == nil || cq.From == nil {
		return
	}
	chatID := cq.Message.Chat.ID
	userID := cq.From.ID

	if !b.isAllowed(userID) {
		b.answerCallback(cq, "🚫 未授权")
		return
	}

	data := strings.TrimSpace(cq.Data)
	if data == "" {
		b.answerCallback(cq, "")
		return
	}

	parts := strings.Fields(data)
	if len(parts) < 1 {
		b.answerCallback(cq, "")
		return
	}

	cmd := strings.ToLower(parts[0])

	// Route "ask:" prefixed callbacks (generated by ask.go) to the ask handler.
	if strings.HasPrefix(cmd, "ask:") {
		b.handleAskCallback(chatID, cq, data)
		return
	}

	// Route "approve:" prefixed callbacks (generated by approve.go) to the approve handler.
	if strings.HasPrefix(cmd, "approve:") {
		b.handleApproveCallback(chatID, data)
		b.answerCallback(cq, "")
		return
	}

	switch cmd {

	case "/ask":
		if len(parts) < 4 {
			b.answerCallback(cq, "⚠️ 参数少")
			return
		}
		askID := parts[1]
		questionID := parts[2]
		answer := strings.Join(parts[3:], " ")
		ctrl := b.sm.ControllerFor(chatID)
		if ctrl == nil {
			b.answerCallback(cq, "💤 无会话")
			return
		}
		ctrl.AnswerQuestion(askID, []event.AskAnswer{
			{QuestionID: questionID, Selected: []string{answer}},
		})
		b.answerCallback(cq, "✅ 已提交")

	case "/dismiss":
		b.dismissStream(chatID)
		b.answerCallback(cq, "✅ 已关闭")

	default:
		b.answerCallback(cq, "")
	}
}

// ---------------------------------------------------------------------------
// Process stream (one Telegram message per turn; coalesce + serialize)
// ---------------------------------------------------------------------------

func (b *Bridge) chatStream(chatID int64) *streamState {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.streams[chatID]
	if !ok {
		st = &streamState{idle: make(chan struct{})}
		close(st.idle) // start idle
		b.streams[chatID] = st
	}
	return st
}

func (b *Bridge) resetStream(chatID int64) {
	st := b.chatStream(chatID)
	st.mu.Lock()
	st.draftID = 0
	st.pending = ""
	st.hasPend = false
	st.closing = false
	st.fallbackMsgID = 0
	// keep draftOK across turns once probed
	st.mu.Unlock()
}

// enqueueStreamUpdate records the latest process text and ensures one worker
// flushes it via sendMessageDraft. Concurrent token events only overwrite pending.
func (b *Bridge) enqueueStreamUpdate(chatID int64, draftID int64, content string) {
	st := b.chatStream(chatID)
	st.mu.Lock()
	if st.closing {
		st.mu.Unlock()
		return
	}
	if draftID == 0 {
		draftID = 1
	}
	st.draftID = draftID
	st.pending = content
	st.hasPend = true
	if st.running {
		st.mu.Unlock()
		return
	}
	st.running = true
	st.idle = make(chan struct{})
	st.mu.Unlock()
	go b.drainStream(chatID, st)
}

func (b *Bridge) drainStream(chatID int64, st *streamState) {
	defer func() {
		st.mu.Lock()
		st.running = false
		if st.idle != nil {
			select {
			case <-st.idle:
			default:
				close(st.idle)
			}
		}
		if st.hasPend && !st.closing {
			st.running = true
			st.idle = make(chan struct{})
			st.mu.Unlock()
			go b.drainStream(chatID, st)
			return
		}
		st.mu.Unlock()
	}()

	for {
		st.mu.Lock()
		if !st.hasPend || st.closing {
			st.mu.Unlock()
			return
		}
		text := st.pending
		st.pending = ""
		st.hasPend = false
		draftID := st.draftID
		if draftID == 0 {
			draftID = 1
		}
		st.mu.Unlock()

		// RAW markdown for rich draft — do not strip escapes if already plain.
		body := strings.TrimSpace(text)
		if body == "" {
			continue
		}
		// Official typewriter: sendRichMessageDraft first, plain draft fallback.
		if err := b.client.PushDraft(b.ctx, chatID, draftID, body); err != nil {
			log.Printf("[chat %d] PushDraft FAILED draft_id=%d: %v", chatID, draftID, err)
			continue
		}
		log.Printf("[chat %d] PushDraft ok draft_id=%d len=%d", chatID, draftID, len([]rune(body)))
	}
}

// waitStreamIdle blocks until the drain worker is not running (or timeout).
func (b *Bridge) waitStreamIdle(st *streamState, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		st.mu.Lock()
		running := st.running
		idle := st.idle
		st.mu.Unlock()
		if !running {
			return
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			return
		}
		select {
		case <-idle:
			return
		case <-time.After(remain):
			return
		}
	}
}

// closeStream: flush last rich draft frame, then sendRichMessage to persist.
// Never promotes draft by edit — Bot API has no promote; final must be a real send.
func (b *Bridge) closeStream(chatID int64, draftID int64, finalText string) error {
	st := b.chatStream(chatID)

	// Let in-flight process frames finish, then force one last draft with final body.
	b.waitStreamIdle(st, 3*time.Second)

	st.mu.Lock()
	if draftID == 0 {
		draftID = st.draftID
	}
	if draftID == 0 {
		draftID = 1
	}
	st.draftID = draftID
	st.mu.Unlock()

	// finalText is RAW markdown from renderFinal
	body := strings.TrimSpace(finalText)
	if body == "" {
		body = strings.TrimSpace(stripMdv2Escapes(finalText))
	}
	if body == "" {
		return fmt.Errorf("empty final text")
	}

	// Last typewriter frame (even if no intermediate tokens arrived).
	if err := b.client.PushDraft(b.ctx, chatID, draftID, body); err != nil {
		log.Printf("[chat %d] closeStream final PushDraft: %v", chatID, err)
	} else {
		log.Printf("[chat %d] closeStream final PushDraft ok draft_id=%d", chatID, draftID)
	}

	st.mu.Lock()
	st.closing = true
	st.hasPend = false
	st.pending = ""
	st.draftID = 0
	st.mu.Unlock()

	// Persist via sendRichMessage; fall back to plain sendMessage chunks.
	if mid, err := b.client.SendRichMessage(b.ctx, chatID, body); err != nil {
		log.Printf("[chat %d] sendRichMessage failed: %v — plain sendMessage", chatID, err)
		return b.sendLongMessagePlain(chatID, stripMdv2Escapes(body))
	} else {
		log.Printf("[chat %d] sendRichMessage ok msg_id=%d", chatID, mid)
	}
	return nil
}

func (b *Bridge) dismissStream(chatID int64) {
	st := b.chatStream(chatID)
	st.mu.Lock()
	st.closing = true
	st.hasPend = false
	st.pending = ""
	draftID := st.draftID
	st.draftID = 0
	st.mu.Unlock()
	b.waitStreamIdle(st, 2*time.Second)
	if draftID > 0 {
		// Empty draft clears when supported.
		if err := b.client.SendMessageDraft(b.ctx, chatID, draftID, ""); err != nil {
			log.Printf("[chat %d] dismissStream: %v", chatID, err)
		}
	}
}

// sendLongMessagePlain sends plain text parts (no MarkdownV2).
func (b *Bridge) sendLongMessagePlain(chatID int64, text string) error {
	parts := splitTelegramText(text, telegramMaxMessageRunes)
	for _, part := range parts {
		msg := NewMessage(chatID, part)
		msg.ParseMode = ""
		if _, err := b.client.Send(b.ctx, msg); err != nil {
			log.Printf("sendLongMessagePlain error (chat %d): %v", chatID, err)
			return err
		}
	}
	return nil
}

// sendMarkdownOrPlain tries MarkdownV2 first, then plain text.
func (b *Bridge) sendMarkdownOrPlain(chatID int64, text string) (*Message, error) {
	msg := NewMessage(chatID, text)
	msg.ParseMode = ModeMarkdownV2
	sent, err := b.client.Send(b.ctx, msg)
	if err == nil {
		return sent, nil
	}
	if telegramErrorIsParseEntities(err) {
		log.Printf("sendMarkdownOrPlain: mdv2 failed (chat %d): %v — plain fallback", chatID, err)
		plain := NewMessage(chatID, stripMdv2Escapes(text))
		return b.client.Send(b.ctx, plain)
	}
	// Network / other: one more try as plain.
	log.Printf("sendMarkdownOrPlain: send failed (chat %d): %v — plain retry", chatID, err)
	plain := NewMessage(chatID, stripMdv2Escapes(text))
	return b.client.Send(b.ctx, plain)
}

// stripMdv2Escapes removes backslash escapes added by escapeMdv2 for plain send.
func stripMdv2Escapes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Message helpers
// ---------------------------------------------------------------------------

func (b *Bridge) sendMessage(chatID int64, text string) {
	_, err := b.client.Send(b.ctx, NewMessage(chatID, text))
	if err != nil {
		log.Printf("sendMessage error (chat %d): %v", chatID, err)
	}
}

// sendTyping shows the client "typing…" indicator (best-effort, never blocks long).
func (b *Bridge) sendTyping(chatID int64) {
	done := make(chan struct{}, 1)
	go func() {
		// Request, not Send: sendChatAction returns bool, not Message.
		if _, err := b.client.Request(context.Background(), NewChatAction(chatID, ChatTyping)); err != nil {
			log.Printf("[chat %d] sendChatAction typing: %v", chatID, err)
		}
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Printf("[chat %d] sendChatAction typing timed out (5s)", chatID)
	}
}

// beginTyping shows Telegram "typing…" until the returned stop function runs.
func (b *Bridge) beginTyping(parentCtx context.Context, chatID int64) (stop func()) {
	ctx, cancel := context.WithCancel(parentCtx)
	send := func() { b.sendTyping(chatID) }
	send()
	go func() {
		ticker := time.NewTicker(typingRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				send()
			case <-ctx.Done():
				return
			}
		}
	}()
	return cancel
}

func (b *Bridge) sendMessageWithMarkup(chatID int64, text string, markup *InlineKeyboardMarkup) {
	msg := NewMessage(chatID, text)
	msg.ReplyMarkup = markup
	_, err := b.client.Send(b.ctx, msg)
	if err != nil {
		log.Printf("sendMessageWithMarkup error (chat %d): %v", chatID, err)
	}
}

func (b *Bridge) sendMessageWithHTML(chatID int64, text string) {
	msg := NewMessage(chatID, text)
	msg.ParseMode = "HTML"
	_, err := b.client.Send(b.ctx, msg)
	if err != nil {
		log.Printf("sendMessageWithHTML error (chat %d): %v", chatID, err)
	}
}

func (b *Bridge) sendLongMessage(chatID int64, text string) error {
	parts := splitTelegramText(text, telegramMaxMessageRunes)
	for _, part := range parts {
		if _, err := b.sendMarkdownOrPlain(chatID, part); err != nil {
			log.Printf("sendLongMessage error (chat %d): %v", chatID, err)
			return err
		}
		time.Sleep(multiPartSendGap)
	}
	return nil
}

func (b *Bridge) answerCallback(cq *CallbackQuery, text string) {
	_, err := b.client.Send(b.ctx, NewCallback(cq.ID, text))
	if err != nil {
		log.Printf("answerCallback error: %v", err)
	}
}

// isAllowed checks if the user is in the allowed list.
func (b *Bridge) isAllowed(userID int64) bool {
	if len(b.cfg.AllowedUsers) == 0 {
		return true // no restrictions
	}
	for _, uid := range b.cfg.AllowedUsers {
		if uid == userID {
			return true
		}
	}
	return false
}

// chatWorkdirSubdir is used by persist.go for state subdirectory.
const chatWorkdirSubdir = "workdir"

// redactSecrets replaces known secrets in s with "***".
func redactSecrets(s string, secrets []string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		// Simple string replacement
		for i := 0; i < len(s)-len(secret); i++ {
			if s[i:i+len(secret)] == secret {
				s = s[:i] + "***" + s[i+len(secret):]
			}
		}
	}
	return scrubSecretPatterns(s)
}

// Shutdown gracefully shuts down the bridge.
func (b *Bridge) Shutdown() {
	log.Println("bridge shutting down")
	b.cancel()
	b.sm.Shutdown()
}
