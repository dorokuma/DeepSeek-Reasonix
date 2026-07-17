package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
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
	mu      sync.Mutex
	draftID int64
	pending string
	hasPend bool
	running bool
	closing bool
	// draftOK: nil=unknown, true=native draft works, false=fall back to edit path
	draftOK *bool
	// fallbackMsgID only used if native draft API is unavailable
	fallbackMsgID int
	idle          chan struct{} // closed while worker is not running; recreated on start
	lastPush      time.Time
	primed        bool  // first draft frame of this turn already sent
	toolMsgIDs    []int // track tool-call message IDs for cleanup before final reply
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
	submitMu    map[int64]*sync.Mutex
	sem         chan struct{}
	updateQueue chan Update
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
		sem:          make(chan struct{}, 100),
		updateQueue:  make(chan Update, 500),
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

// registerBotCommands replaces Telegram slash menu with current local commands.
func (b *Bridge) registerBotCommands() {
	cmds := []BotCommand{
		{Command: "new", Description: "新对话"},
		{Command: "stop", Description: "停止当前回合"},
		{Command: "status", Description: "会话状态与用量"},
		{Command: "model", Description: "查看/切换模型"},
		{Command: "showthinking", Description: "开关思考过程"},
		{Command: "restart", Description: "重启桥"},
		{Command: "help", Description: "帮助"},
	}
	if _, err := b.client.Request(b.ctx, NewSetMyCommands(cmds...)); err != nil {
		log.Printf("setMyCommands: %v", err)
		return
	}
	log.Printf("setMyCommands: registered %d commands", len(cmds))
}

func (b *Bridge) Start() error {
	log.Printf("starting bridge, bot=%s", b.client.Self.UserName)
	b.registerBotCommands()

	defer close(b.updateQueue)

	// Queue consumer: processes buffered updates when semaphore allows.
	go func() {
		for u := range b.updateQueue {
			b.sem <- struct{}{}
			go func(u Update) {
				defer func() { <-b.sem }()
				if u.Message != nil {
					b.handleMessage(u.Message)
				} else if u.CallbackQuery != nil {
					b.handleCallbackQuery(u.CallbackQuery)
				}
			}(u)
		}
	}()

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
				// Acquire semaphore; if full, queue for later processing.
				select {
				case b.sem <- struct{}{}:
					go func(msg *Message) {
						defer func() { <-b.sem }()
						b.handleMessage(msg)
					}(upd.Message)
				default:
					// semaphore full → attempt buffered queue; if also full, drain oldest until space.
					drained := 0
					for len(b.updateQueue) >= cap(b.updateQueue) {
						select {
						case <-b.updateQueue:
							drained++
						default:
						}
					}
					if drained > 0 {
						log.Printf("rate limit: queue full, drained %d oldest update(s)", drained)
					}
					b.updateQueue <- upd
				}
			}
			if upd.CallbackQuery != nil {
				select {
				case b.sem <- struct{}{}:
					go func(cq *CallbackQuery) {
						defer func() { <-b.sem }()
						b.handleCallbackQuery(cq)
					}(upd.CallbackQuery)
				default:
					select {
					case b.updateQueue <- upd:
					default:
						// semaphore full → attempt buffered queue; if also full, drain oldest until space.
						drained := 0
						for len(b.updateQueue) >= cap(b.updateQueue) {
							select {
							case <-b.updateQueue:
								drained++
							default:
							}
						}
						if drained > 0 {
							log.Printf("rate limit: queue full, drained %d oldest update(s)", drained)
						}
						b.updateQueue <- upd
					}
				}
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
		if err := b.sendMessage(chatID, "🚫 无权限"); err != nil {
			log.Printf("failed to send message: %v", err)
		}
		return
	}

	// Route commands
	if msg.Text != "" && strings.HasPrefix(strings.TrimSpace(msg.Text), "/") {
		b.handleCommand(chatID, userID, strings.TrimSpace(msg.Text))
		return
	}

	var promptBuilder strings.Builder

	// 1. Handle quote (Reply)
	if msg.ReplyToMessage != nil {
		var replyUser string
		if msg.ReplyToMessage.From != nil {
			if msg.ReplyToMessage.From.UserName != "" {
				replyUser = "@" + msg.ReplyToMessage.From.UserName
			} else {
				replyUser = fmt.Sprintf("用户(ID:%d)", msg.ReplyToMessage.From.ID)
			}
		}
		quotedText := msg.ReplyToMessage.Text
		if quotedText == "" {
			quotedText = msg.ReplyToMessage.Caption
		}
		var quotedFormatted string
		if quotedText != "" {
			var quotedLines []string
			for _, line := range strings.Split(quotedText, "\n") {
				quotedLines = append(quotedLines, "> "+line)
			}
			quotedFormatted = strings.Join(quotedLines, "\n")
		} else {
			quotedFormatted = "> [媒体/空消息]"
		}
		promptBuilder.WriteString(fmt.Sprintf("> 💬 引用自 %s 的消息：\n%s\n\n", replyUser, quotedFormatted))
	}

	// 2. Handle Forward
	isForward := false
	var forwardFrom string
	if msg.ForwardFrom != nil {
		isForward = true
		if msg.ForwardFrom.UserName != "" {
			forwardFrom = "@" + msg.ForwardFrom.UserName
		} else {
			forwardFrom = fmt.Sprintf("用户(ID:%d)", msg.ForwardFrom.ID)
		}
	} else if msg.ForwardFromChat != nil {
		isForward = true
		forwardFrom = fmt.Sprintf("频道(ID:%d)", msg.ForwardFromChat.ID)
	} else if msg.ForwardSenderName != "" {
		isForward = true
		forwardFrom = msg.ForwardSenderName
	} else if msg.ForwardDate > 0 {
		isForward = true
		forwardFrom = "未知"
	}

	if isForward {
		promptBuilder.WriteString(fmt.Sprintf("> ➡️ 转发自：%s\n", forwardFrom))

		// Process forward media files
		if len(msg.Photo) > 0 {
			photo := msg.Photo[len(msg.Photo)-1]
			file, err := b.client.GetFile(b.ctx, photo.FileID)
			if err == nil && file.FilePath != "" {
				data, err := b.client.DownloadFile(b.ctx, file.FilePath)
				if err == nil {
					base64Data := base64.StdEncoding.EncodeToString(data)
					promptBuilder.WriteString(fmt.Sprintf("[REASONIX_IMAGE:data:image/jpeg;base64,%s]\n", base64Data))
				} else {
					log.Printf("Download forward image failed: %v", err)
				}
			} else {
				log.Printf("Get forward image metadata failed: %v", err)
			}
		}

		if msg.Video != nil {
			path, size, err := b.saveTelegramFile(b.ctx, msg.Video.FileID, "video.mp4")
			if err == nil {
				promptBuilder.WriteString(fmt.Sprintf("\n[已下载转发视频文件，路径: %s, 大小: %d 字节]\n", path, size))
			} else {
				log.Printf("Save forward video failed: %v", err)
			}
		}

		if msg.Animation != nil {
			path, size, err := b.saveTelegramFile(b.ctx, msg.Animation.FileID, "animation.mp4")
			if err == nil {
				promptBuilder.WriteString(fmt.Sprintf("\n[已下载转发动画文件，路径: %s, 大小: %d 字节]\n", path, size))
			} else {
				log.Printf("Save forward animation failed: %v", err)
			}
		}

		if msg.Audio != nil {
			path, size, err := b.saveTelegramFile(b.ctx, msg.Audio.FileID, "audio.mp3")
			if err == nil {
				promptBuilder.WriteString(fmt.Sprintf("\n[已下载转发音频文件，路径: %s, 大小: %d 字节]\n", path, size))
			} else {
				log.Printf("Save forward audio failed: %v", err)
			}
		}

		if msg.Document != nil {
			fileName := msg.Document.FileName
			if fileName == "" {
				fileName = "document"
			}
			path, size, err := b.saveTelegramFile(b.ctx, msg.Document.FileID, fileName)
			if err == nil {
				promptBuilder.WriteString(fmt.Sprintf("\n[已下载转发文档文件，路径: %s, 大小: %d 字节]\n", path, size))
			} else {
				log.Printf("Save forward document failed: %v", err)
			}
		}
	}

	// 3. Append current text/caption
	currentText := msg.Text
	if currentText == "" {
		currentText = msg.Caption
	}
	promptBuilder.WriteString(currentText)

	prompt := strings.TrimSpace(promptBuilder.String())
	if prompt == "" {
		return
	}

	log.Printf("[chat %d] received: %s", chatID, prompt)
	b.handleSubmit(chatID, prompt)
}

// handleCommand routes slash commands.
func (b *Bridge) handleCommand(chatID, userID int64, text string) {
	parts := strings.Fields(text)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/stop":
		b.sm.Stop(chatID)
		if err := b.sendMessage(chatID, "⏹ 已停"); err != nil {
			log.Printf("failed to send message: %v", err)
		}

	case "/restart":
		msgID, err := b.sendMessageID(chatID, "♻️ 重启中…")
		if err != nil {
			log.Printf("restart notify send: %v", err)
		}
		if err := markRestartNotify(chatID, msgID); err != nil {
			log.Printf("restart notify mark: %v", err)
		}
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
			if err := b.sendMessage(chatID, "🧠 思考开"); err != nil {
				log.Printf("failed to send message: %v", err)
			}
		} else {
			if err := b.sendMessage(chatID, "🫥 思考关"); err != nil {
				log.Printf("failed to send message: %v", err)
			}
		}

	case "/status":
		b.ensureChatSink(chatID)
		ctrl := b.sm.ControllerFor(chatID)
		if ctrl == nil {
			if err := b.sendMessage(chatID, "💤 无会话"); err != nil {
				log.Printf("failed to send message: %v", err)
			}
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
		if hit, miss, ok := ctrl.SessionCacheRate(); ok {
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
		if cost, currency := ctrl.SessionCost(); cost > 0 {
			if amt := formatCostAmount(cost); amt != "" {
				symbol := mapCurrencySymbol(currency)
				costStr = symbol + amt
			} else {
				costStr = "暂无"
			}
		} else {
			costStr = "暂无"
		}
		if prompt, total := ctrl.SessionTokens(); total > 0 {
			usageStr += fmt.Sprintf("\n累计 入%d 总%d", prompt, total)
		}
		msg := fmt.Sprintf("📊 %s · 轮%d · %s\n🪙 %s\n💾 %s\n💰 %s",
			label, turn, statusText, usageStr, cacheStr, costStr)
		if err := b.sendMessage(chatID, msg); err != nil {
			log.Printf("failed to send message: %v", err)
		}

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
		if err := b.sendMessage(chatID, help); err != nil {
			log.Printf("failed to send message: %v", err)
		}

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
			// Skip wait placeholders — only official chat "typing…" until real tokens.
			if content == "…" || content == "..." || content == "···" {
				return
			}
			content = telegramPreviewHead(content, telegramMaxMessageRunes)
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
				if err := b.sendMessage(cid, stripMdv2Escapes(finalText)); err != nil {
					log.Printf("failed to send message: %v", err)
				}
			}
		},
		func(cid int64, draftID int64) {
			// Only clear the previous draft bubble — must NOT set stream closing,
			// or all mid-turn PushDrafts are silently dropped.
			b.dismissDraftID(cid, draftID)
		},
		func(cid int64, ask event.Ask) {
			b.handleAsk(cid, ask)
			s.markDelivered() // user-visible keyboard
		},
		func(cid int64, approval event.Approval) {
			s.mu.Lock()
			content := s.renderContent()
			draftID := s.draftID
			s.mu.Unlock()
			if content != "" {
				if err := b.closeStream(cid, draftID, content); err != nil {
					log.Printf("[chat %d] closeStream failed in approve callback: %v", cid, err)
				}
				s.mu.Lock()
				s.resetTurn()
				s.mu.Unlock()
				b.handleApprove(cid, approval)
			} else {
				b.handleApprove(cid, approval)
			}
			s.markDelivered()
		},
		func(cid int64, toolName, args, status string, elapsed time.Duration) {
			line := "🔧 " + toolName
			if args != "" {
				line += "(" + truncate(args, 60) + ")"
			}
			msgID, err := b.sendMessageID(cid, line)
			if err == nil && msgID > 0 {
				b.mu.Lock()
				st, ok := b.streams[cid]
				b.mu.Unlock()
				if ok {
					st.mu.Lock()
					st.toolMsgIDs = append(st.toolMsgIDs, msgID)
					st.mu.Unlock()
				}
			}
		},
		func(cid int64, err error) {
			if err == nil {
				return
			}
			log.Printf("[chat %d] turn error: %v", cid, err)
			if !isBenignTurnErr(err) {
				if err := b.sendMessage(cid, fmt.Sprintf("❌ %v", err)); err != nil {
					log.Printf("failed to send message: %v", err)
				}
				s.markDelivered()
			}
		},
		func(cid int64, text string) {
			if text != "" && !hasLeadingEmoji(text) {
				text = "ℹ️ " + text
			}
			if err := b.sendMessage(cid, text); err != nil {
				log.Printf("failed to send message: %v", err)
			}
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
		if err := b.sendMessage(chatID, fmt.Sprintf("❌ %v", err)); err != nil {
			log.Printf("failed to send message: %v", err)
		}
		return
	}

	ctrl := b.sm.ControllerFor(chatID)
	if ctrl == nil {
		log.Printf("[chat %d] no controller after Submit", chatID)
		if err := b.sendMessage(chatID, "💥 会话未建"); err != nil {
			log.Printf("failed to send message: %v", err)
		}
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
		if err := b.sendMessage(chatID, "⏰ 超时，重试或 /new"); err != nil {
			log.Printf("failed to send message: %v", err)
		}
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
		if err := b.sendMessage(chatID, "😶 无回复，再发或 /new"); err != nil {
			log.Printf("failed to send message: %v", err)
		}
		return
	}
	log.Printf("[chat %d] fallback deliver PLAIN answer len=%d", chatID, len([]rune(answer)))
	// Plain text only for reliability — formatMessage on partial streams produced 话说不完.
	body := answer
	if u := s.GetLastUsage(); u != nil {
		body += fmt.Sprintf("\n\n📊 输入 %d · 输出 %d · 总计 %d", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
		if amt := formatCostAmount(s.GetSessionCost()); amt != "" {
			body += " · 成本 ¥" + amt
		}
	}
	if err := b.sendMessage(chatID, body); err != nil {
		log.Printf("failed to send message: %v", err)
	}
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
		b.handleApproveCallback(chatID, cq, data)
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
	st.running = false
	st.fallbackMsgID = 0
	st.primed = false
	st.toolMsgIDs = nil
	st.lastPush = time.Time{}
	st.idle = make(chan struct{})
	close(st.idle)
	// keep draftOK across turns once probed
	st.mu.Unlock()
}

// enqueueStreamUpdate records the latest process text and ensures one worker
// flushes it via draft API. Concurrent token events only overwrite pending.
// The first frame of each turn is pushed synchronously so the typewriter starts immediately.
func (b *Bridge) enqueueStreamUpdate(chatID int64, draftID int64, content string) {
	st := b.chatStream(chatID)
	st.mu.Lock()
	if st.closing {
		st.mu.Unlock()
		log.Printf("[chat %d] enqueueStreamUpdate skipped: stream closing", chatID)
		return
	}
	if draftID == 0 {
		draftID = 1
	}
	st.draftID = draftID

	// First frame: push now (sync) so user sees the bubble before the model finishes thinking.
	if !st.primed {
		st.primed = true
		st.lastPush = time.Now()
		st.mu.Unlock()
		body := strings.TrimSpace(content)
		if body != "" {
			if err := b.client.PushDraft(context.Background(), chatID, draftID, body); err != nil {
				log.Printf("[chat %d] PushDraft prime FAILED draft_id=%d: %v", chatID, draftID, err)
				st.mu.Lock()
				st.primed = false
				st.mu.Unlock()
			} else {
				log.Printf("[chat %d] PushDraft prime ok draft_id=%d len=%d", chatID, draftID, len([]rune(body)))
			}
		}
		st.mu.Lock()
		if st.closing {
			st.mu.Unlock()
			return
		}
		// After prime, still queue latest content for worker (may be newer than prime body).
	}

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
	const minPushGap = 80 * time.Millisecond
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
		if !st.lastPush.IsZero() {
			wait := minPushGap - time.Since(st.lastPush)
			if wait > 0 {
				st.mu.Unlock()
				time.Sleep(wait)
				st.mu.Lock()
				if !st.hasPend || st.closing {
					st.mu.Unlock()
					return
				}
			}
		}
		text := st.pending
		st.pending = ""
		st.hasPend = false
		draftID := st.draftID
		if draftID == 0 {
			draftID = 1
		}
		st.lastPush = time.Now()
		st.mu.Unlock()

		body := strings.TrimSpace(text)
		if body == "" {
			continue
		}
		if err := b.client.PushDraft(context.Background(), chatID, draftID, body); err != nil {
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
	if err := b.client.PushDraft(context.Background(), chatID, draftID, body); err != nil {
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

	// Delete tool-call messages before sending final reply
	st.mu.Lock()
	toolIDs := st.toolMsgIDs
	st.toolMsgIDs = nil
	st.mu.Unlock()
	for _, mid := range toolIDs {
		b.deleteMessage(chatID, mid)
	}

	// Persist via sendRichMessage; fall back to plain sendMessage chunks.
	if mid, err := b.client.SendRichMessage(b.ctx, chatID, body); err != nil {
		log.Printf("[chat %d] sendRichMessage failed: %v — plain sendMessage", chatID, err)
		return b.sendLongMessagePlain(chatID, stripMdv2Escapes(body))
	} else {
		log.Printf("[chat %d] sendRichMessage ok msg_id=%d", chatID, mid)
	}
	return nil
}

// dismissDraftID clears one ephemeral draft bubble without blocking the live stream.
func (b *Bridge) dismissDraftID(chatID int64, draftID int64) {
	if draftID <= 0 {
		return
	}
	if err := b.client.SendMessageDraft(context.Background(), chatID, draftID, ""); err != nil {
		log.Printf("[chat %d] dismissDraftID %d: %v", chatID, draftID, err)
	}
}

// dismissStream aborts the live typewriter pipeline (stop/empty final).
func (b *Bridge) dismissStream(chatID int64) {
	st := b.chatStream(chatID)
	st.mu.Lock()
	st.closing = true
	st.hasPend = false
	st.pending = ""
	draftID := st.draftID
	st.draftID = 0
	st.mu.Unlock()
	st.mu.Lock()
	toolIDs := st.toolMsgIDs
	st.toolMsgIDs = nil
	st.mu.Unlock()
	for _, mid := range toolIDs {
		b.deleteMessage(chatID, mid)
	}
	b.waitStreamIdle(st, 2*time.Second)
	if draftID > 0 {
		b.dismissDraftID(chatID, draftID)
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

func (b *Bridge) sendMessage(chatID int64, text string) error {
	_, err := b.sendMessageID(chatID, text)
	return err
}

// sendMessageID sends plain text and returns Telegram message_id (0 on failure).
func (b *Bridge) sendMessageID(chatID int64, text string) (int, error) {
	msg, err := b.client.Send(b.ctx, NewMessage(chatID, text))
	if err != nil {
		log.Printf("sendMessage error (chat %d): %v", chatID, err)
		return 0, err
	}
	if msg == nil {
		return 0, nil
	}
	return msg.MessageID, nil
}

// editMessageText updates an existing bubble in place.
func (b *Bridge) editMessageText(chatID int64, messageID int, text string) error {
	if messageID <= 0 {
		_, err := b.sendMessageID(chatID, text)
		return err
	}
	_, err := b.client.Send(b.ctx, NewEditMessageText(chatID, messageID, text))
	if err != nil {
		// Message gone or too old: fall back to a single new line.
		log.Printf("editMessageText chat %d msg %d: %v — fallback send", chatID, messageID, err)
		_, err2 := b.sendMessageID(chatID, text)
		return err2
	}
	return nil
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

// mapCurrencySymbol returns the currency symbol for display.
func mapCurrencySymbol(currency string) string {
	switch currency {
	case "CNY", "¥":
		return "¥"
	case "USD", "$":
		return "$"
	default:
		if currency != "" {
			return currency + " "
		}
		return ""
	}
}

// Shutdown gracefully shuts down the bridge.
func (b *Bridge) Shutdown() {
	log.Println("bridge shutting down")
	b.cancel()
	b.sm.Shutdown()
}

func (b *Bridge) saveTelegramFile(ctx context.Context, fileID string, targetName string) (string, int64, error) {
	file, err := b.client.GetFile(ctx, fileID)
	if err != nil {
		return "", 0, fmt.Errorf("failed to get file metadata: %w", err)
	}
	if file.FilePath == "" {
		return "", 0, fmt.Errorf("file path is empty for file ID: %s", fileID)
	}
	data, err := b.client.DownloadFile(ctx, file.FilePath)
	if err != nil {
		return "", 0, fmt.Errorf("failed to download file: %w", err)
	}
	targetPath := fmt.Sprintf("/tmp/tg_%s_%s", fileID, targetName)
	if err := os.WriteFile(targetPath, data, 0644); err != nil {
		return "", 0, fmt.Errorf("failed to write file: %w", err)
	}
	return targetPath, int64(len(data)), nil
}
