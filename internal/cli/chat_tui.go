package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/command"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/hook"
	"reasonix/internal/i18n"
	"reasonix/internal/plugin"
	"reasonix/internal/provider"
	"reasonix/internal/skill"
)

// chatTUI is a bubbletea Model that normally owns the terminal with an
// alt-screen transcript viewport. Android terminals are the exception: enabling
// mouse mode prevents taps from raising the soft keyboard, so they stay in the
// normal buffer and commit finalized output to native scrollback via tea.Println.
type chatTUI struct {
	ctrl    *control.Controller
	label   string
	missing string // missing-key warning surfaced once in the banner, "" when ready

	width  int
	height int
	// nativeScrollback enables the Conduit-friendly mode on Android terminals:
	// alt-screen is active (so swipe gestures translate to arrow keys), but mouse
	// mode stays off so taps still raise the soft keyboard. Arrow keys scroll the
	// transcript viewport instead of recalling input history when scrolled up.
	nativeScrollback bool
	// ForceNativeScrollback overrides auto-detection when set to true (e.g. via
	// the --native-scrollback CLI flag).
	ForceNativeScrollback bool

	input   textarea.Model
	spinner spinner.Model

	submittedInputs      []string
	submittedInputCursor int
	submittedInputDraft  string
	pastedBlocks         []pastedBlock
	nextPasteID          int

	state    tuiState
	runStart time.Time
	elapsed  int
	// retryAttempt/retryMax drive the transient "retrying (n/m)" indicator while
	// the provider re-attempts the connection; cleared by the next stream event.
	retryAttempt int
	retryMax     int
	// turnTokens accumulates this turn's output tokens (summed from per-step Usage
	// events) for the live "↓N" readout in the running status line.
	turnTokens int

	// sessCost is the cumulative conversation cost (e.g. ¥0.1234), summed from
	// every Usage event's Pricing.Cost(). Displayed in the status data line.
	sessCost     float64
	sessCurrency string // currency symbol from the last Pricing event (e.g. "¥")

	// balance is the last-fetched wallet-balance readout (e.g. "¥110.00"), "" when
	// the provider declares no balance_url or a fetch failed. Refreshed async on
	// startup and after each turn so the status line stays roughly current without
	// blocking the event loop.
	balance string

	// Persists across turns until the work completes or a new session starts.

	// permMode is the config-level permissions writer-fallback mode ("ask",
	// "allow", or "deny"). When "allow", the TUI shows "YOLO" because writer
	// tools never prompt — the same effective behaviour as the runtime bypass flag.
	permMode string

	// pendingInterject queues input typed while a turn runs; each TurnDone
	// dequeues the front and submits it as the next turn.
	pendingInterject []string
	// queueEditCursor tracks which queued message the user is currently
	// browsing/editing via ↑/↓ during tuiRunning. -1 means "not browsing".
	queueEditCursor int
	// queueEditDraft saves the in-progress input text when the user first
	// presses ↑ to browse the queue, so it can be restored when the cursor
	// moves past the end.
	queueEditDraft string

	// history is a resumed session's messages, committed to scrollback once on
	// the first WindowSizeMsg so a reopened chat shows its prior transcript.
	history []provider.Message

	// reasoning accumulates the in-progress thinking stream (dim); pending
	// accumulates the in-progress answer (raw markdown). They are committed to
	// scrollback (reasoning collapsed by default, answer markdown-rendered) when they
	// finalize — at a tool/usage boundary or turn end — not previewed live, so
	// the bottom region stays a stable height. pendingCommit queues finalized
	// lines so a single Update emits exactly one ordered tea.Println.
	reasoning     *strings.Builder
	pending       *strings.Builder
	pendingCommit *[]string
	renderer      *mdRenderer
	showReasoning bool // Ctrl+O / /verbose: show raw thinking text in the CLI
	cfg           *config.Config
	// reasoningLineIdx is the transcript index of the live "▎ thinking…" marker
	// while a reasoning block streams; it's rewritten to "▎ thought for Ns" when
	// the block closes. -1 when no block is open. transcriptDirty forces a
	// viewport re-feed after that in-place rewrite (length is unchanged).
	reasoningLineIdx int
	// reasoningTextIdx is the transcript index of the live reasoning text block
	// (the block right after the marker), streamed in as the model thinks and
	// removed when the block collapses (kept only in verbose mode). -1 when none.
	reasoningTextIdx int
	// reasoningView is a bounded trailing window (≤ reasoningViewMax bytes) of the
	// streaming thought, rendered live; the full text stays in reasoning for verbose.
	reasoningView []byte
	// reasoningNative is the Android/native-scrollback path: reasoning is buffered
	// without a live transcript block, then appended once as a final summary.
	reasoningNative bool
	thinkStart      time.Time
	// answerIdx is the transcript index of the streaming answer block (rewritten in
	// place as completed paragraphs arrive); -1 when none is open. answerFlushed is
	// how many bytes of pending have already been rendered into it, so a Text packet
	// that doesn't close a new paragraph re-renders nothing.
	answerIdx     int
	answerFlushed int
	// toolStreamIdx is the transcript index of a running tool's live-output block
	// (streamed via ToolProgress under the tool card); -1 when none. toolStreamID
	// is the call ID it belongs to. Only a bounded tail is kept — the last few
	// complete lines (toolTail) plus the in-progress one (toolPartial) — so a
	// high-output command can't balloon memory or cost O(n²) re-splitting;
	// toolLineCount feeds the collapse summary.
	toolStreamIdx int
	toolStreamID  string
	toolTail      []string
	toolPartial   string
	toolLineCount int
	// shellOutputs stores the full accumulated output of each shell command
	// (tool IDs with "shell-" prefix), so the first 10 lines can be shown after
	// collapse and Ctrl+B can toggle the complete output.
	shellOutputs  map[string]string
	shellExpanded map[string]bool
	// shellTranscriptIdx maps a shell tool ID to the transcript index of its
	// collapsed output block, so Ctrl+B can rewrite it in place.
	shellTranscriptIdx map[string]int
	// toolStreamStart / toolStreamFrame drive the "⎿ working · Ns" line shown
	// under a dispatched tool that hasn't produced output yet, so a slow tool
	// (e.g. context) reads as making progress rather than frozen.
	toolStreamStart time.Time
	toolStreamFrame int
	transcriptDirty bool
	eventCh         chan event.Event
	started         bool // banner + resumed history committed once

	// transcript holds every finalized line commitLine emits; the viewport
	// renders a scrollable window of it (alt-screen owns the grid, so there's no
	// native terminal scrollback). sel is the live left-drag text selection.
	transcript   []string
	wrappedLines []string // transcript wrapped to viewport width (rendered each frame)
	viewport     viewport.Model
	sel          selection
	// autoScroll drives edge-drag scrolling: -1 up, +1 down, 0 off. dragX is the
	// column the drag is held at, so the ticker can extend the selection head.
	autoScroll int
	dragX      int

	// The user bubble is echoed to scrollback immediately on Enter (bubbleStartIdx
	// marks where in the transcript it landed). It stays "un-sendable" until the
	// first response packet arrives: pressing Esc/Ctrl+C before then pops those
	// lines back off the transcript and restores the text to the input box, leaving
	// no trace. bubblePending is true from startTurn until the first packet confirms
	// the send or it's un-sent; turnDiscarded then swallows the turn's
	// already-buffered events until its TurnDone settles.
	pendingRestore string
	pendingPastes  []string
	bubbleStartIdx int
	bubblePending  bool
	turnDiscarded  bool

	// pendingApproval holds the tool-call approval currently shown in the banner
	// (nil when none). While set, the controller's run goroutine is blocked
	// awaiting ctrl.Approve and key input is captured to answer it.
	pendingApproval *event.Approval

	// chooser holds the `ask` tool's question card (nil when none). While set, the
	// run goroutine is blocked awaiting ctrl.AnswerQuestion and keys drive the card.
	chooser *chooser

	// rewind holds the Esc-Esc / "/rewind" picker (nil when closed); while set,
	// keys drive it and it renders as an overlay. lastEsc times the double-Esc
	// gesture that opens it on an empty composer.
	rewind *rewindPicker
	// resumePick is the interactive "/resume" session picker overlay. Non-nil
	// while the user browses saved sessions with ↑/↓ and confirms with Enter.
	resumePick *resumePicker
	lastEsc    time.Time

	// mcp is the interactive "/mcp" manager overlay. mcpDisabled tracks servers
	// turned off only for this chat session, matching the desktop connector
	// toggle's non-persistent semantics.
	mcp         *mcpManager
	mcpDisabled map[string]bool

	// lastCtrlCAt records when Ctrl+C was pressed while idle on an empty
	// composer, enabling a "press again to quit" confirmation pattern (1.5s
	// window). Reset when Ctrl+C clears non-empty input instead.
	lastCtrlCAt time.Time

	// mcpImport holds the interactive cc-switch MCP import picker (nil when
	// closed). It writes selected servers to config and hot-connects the ones that
	// can start successfully.
	mcpImport *mcpImportPicker

	// host is the running MCP servers (nil when no plugins). The TUI reads
	// prompts (slash commands), resources (@-references), and server status
	// (/mcp) from it.
	host *plugin.Host

	// commands are custom slash commands loaded from .reasonix/commands; each renders
	// its template with the typed args and sends the result as a turn.
	commands []command.Command

	// skills are the discoverable skills (built-in + user/project); each is offered
	// in the slash menu as "/<name>" and managed via /skills.
	skills []skill.Skill

	// skillPick is the interactive skill picker overlay for /skills. nil when closed.
	skillPick *skillPicker

	// buildController builds a fresh controller on a model ref, carrying prior
	// history across and pinning auto-save to resumePath so the continued
	// conversation stays in one file (set by chatREPL; it must NOT touch this
	// model — the swap happens in runModelSubcommand on the running copy). nil
	// disables /model. modelRef is the active "provider/model" ref, marked
	// current in the picker.
	buildController func(ref string, carry []provider.Message, resumePath string) (*control.Controller, error)
	modelRef        string
	effortLevel     string // "" when the current provider/model has no configurable effort

	// outputStyle is the active output-style name (config agent.output_style),
	// shown as the current entry in the /output-style listing. "" = default.
	outputStyle string

	// statuslineCmd is the user's custom status-line command (config
	// [statusline].command); "" disables it. statuslineOut caches its latest
	// one-line stdout, refreshed at startup and after each turn and rendered in
	// place of the built-in data row.
	statuslineCmd string
	statuslineOut string
	gitStatus     gitStatus

	// modelSwitchPending is true while an async /model build is in flight.
	modelSwitchPending bool
	// pendingModelSwitch holds the tea.Cmd that triggers the async build.
	pendingModelSwitch tea.Cmd
	// oldControllers accumulates controllers retired by /model switches.
	// They cannot be closed during the switch (Close runs SessionEnd hooks
	// and kills plugin subprocesses, both of which corrupt the terminal's
	// raw mode). Instead they are closed at process exit when the terminal
	// is already being restored.
	oldControllers []*control.Controller

	// completion is the live autocomplete menu (slash commands; @-refs later).
	completion completion
	// fileSearchCache memoizes fileref.Search by query so the bounded walk runs
	// once per @token fragment, not on every keystroke that re-renders the menu.
	fileSearchCache map[string][]string
}

type tuiState int

const (
	tuiIdle tuiState = iota
	tuiRunning
)

// agentEventMsg is one typed event from the agent's run loop.
type agentEventMsg event.Event

// maxEventDrain caps how many buffered events one Update coalesces before
// yielding to render, so a sustained output flood still shows live progress.
const maxEventDrain = 512

// compactDoneMsg reports that an async /compact pass returned. The card was
// already drawn from the CompactionDone event; this only surfaces a failure and
// snapshots on success.
type compactDoneMsg struct{ err error }

// elapsedTickMsg fires once a second while a turn runs, driving the "thinking
// Ns" counter in the status line.
type elapsedTickMsg struct{}

// balanceMsg carries the result of an async wallet-balance fetch; text is the
// formatted readout ("" when none/failed).
type balanceMsg struct{ text string }

// statuslineMsg carries the latest custom status-line output (one line, ""
// when none/failed).
type statuslineMsg struct{ out string }

// gitStatusMsg carries the latest lightweight git readout for the built-in
// status line. Empty means "not a git worktree" or "git unavailable".
type gitStatusMsg struct{ status gitStatus }

// runStatusline runs the user's custom status-line command off the event loop,
// feeding it a small JSON context on stdin and returning its first stdout line.
// A no-op (nil) when no command is configured. Tight timeout so a slow script
// can't stall the UI; failures collapse to an empty line rather than an error.
func (m chatTUI) runStatusline() tea.Cmd {
	cmd := m.statuslineCmd
	if cmd == "" {
		return nil
	}
	used, window := m.ctrl.ContextSnapshot()
	cwd, _ := os.Getwd()
	payload, _ := json.Marshal(map[string]any{
		"model":         m.label,
		"contextUsed":   used,
		"contextWindow": window,
		"cwd":           cwd,
	})
	return func() tea.Msg { return statuslineMsg{out: runStatuslineCmd(cmd, string(payload))} }
}

// runStatuslineCmd runs a status-line command with the JSON context on stdin and
// returns its first stdout line (status lines are a single row). A tight timeout
// keeps a slow script from stalling the UI; any failure collapses to "".
func runStatuslineCmd(cmd, stdinPayload string) string {
	res := hook.DefaultSpawner(context.Background(), hook.SpawnInput{
		Command: cmd,
		Stdin:   stdinPayload + "\n",
		Timeout: 2 * time.Second,
	})
	out := strings.TrimSpace(res.Stdout)
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = strings.TrimSpace(out[:i])
	}
	return out
}

func (m chatTUI) refreshGitStatus() tea.Cmd {
	if m.statuslineCmd != "" {
		return nil
	}
	return fetchGitStatus()
}

// modelSwitchMsg carries the result of an async /model switch. A nil err means
// the new controller is ready in ctrl; label/commands/skills/host mirror the
// fields that runModelSubcommand used to set synchronously. oldCtrl is the
// previous controller that must be closed after the switch — its cleanup
// (SessionEnd hooks, plugin subprocess kill) is deferred to a tea.Cmd so it
// runs after the render completes, avoiding corruption of the terminal's raw
// mode that would occur if Close() were called from the build goroutine.
type modelSwitchMsg struct {
	ref      string
	ctrl     *control.Controller
	oldCtrl  *control.Controller
	label    string
	commands []command.Command
	skills   []skill.Skill
	host     *plugin.Host
	err      error
}

// fetchBalance queries the provider's wallet balance off the event loop. It's a
// no-op readout ("") when the provider declares no balance_url or the fetch
// fails, so the status line stays quiet rather than surfacing an error.
func fetchBalance(ctrl *control.Controller) tea.Cmd {
	return func() tea.Msg {
		b, err := ctrl.Balance(context.Background())
		if err != nil || b == nil {
			return balanceMsg{}
		}
		return balanceMsg{text: b.Display()}
	}
}

// promptResolvedMsg carries the result of fetching an MCP prompt (an async
// prompts/get). display is the command line echoed as the user bubble; sent is
// the rendered prompt text that becomes the model turn.
type promptResolvedMsg struct {
	display string
	sent    string
	err     error
}

// refsResolvedMsg carries the result of resolving the @references in a
// submitted line (async file reads / MCP resources/read).
type refsResolvedMsg struct {
	sent    string
	display string
	restore string
	block   string
	errs    []string
}

type clipboardImageMsg struct {
	path string
	err  error
}

type clipboardPasteMsg struct {
	path string
	text string
	err  error
}

// newChatTUI assembles the initial model. The controller has already been wired
// with an event sink that feeds eventCh; the TUI issues commands to it and
// renders the events it emits. Label, history, host, and commands are read from
// the controller, so a resumed session pre-populates scrollback.
func newChatTUI(ctrl *control.Controller, missing string, eventCh chan event.Event, termW int) chatTUI {
	ti := textarea.New()
	configureChatTextarea(&ti)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = themeStyle(activeCLITheme.accent)

	commitBuf := []string{}
	nativeScrollback := detectAndroidTerminal()
	return chatTUI{
		ctrl:                 ctrl,
		label:                ctrl.Label(),
		missing:              missing,
		nativeScrollback:     nativeScrollback,
		input:                ti,
		spinner:              sp,
		submittedInputCursor: -1,
		queueEditCursor:      -1,
		nextPasteID:          1,
		reasoningLineIdx:     -1,
		reasoningTextIdx:     -1,
		answerIdx:            -1,
		toolStreamIdx:        -1,
		reasoning:            &strings.Builder{},
		pending:              &strings.Builder{},
		pendingCommit:        &commitBuf,
		renderer:             newMarkdownRenderer(termW - 5), // -5: reserve 4 for "▸" prefix + 1 for scrollbar
		showReasoning:        nativeScrollback,
		shellOutputs:         make(map[string]string),
		shellExpanded:        make(map[string]bool),
		shellTranscriptIdx:   make(map[string]int),
		eventCh:              eventCh,
		history:              ctrl.History(),
		host:                 ctrl.Host(),
		commands:             ctrl.Commands(),
		skills:               ctrl.Skills(),
		viewport:             viewport.New(viewport.WithWidth(termW - 1)),
	}
}

func configureChatTextarea(ti *textarea.Model) {
	ti.Prompt = ""
	ti.CharLimit = 16384
	ti.DynamicHeight = true
	ti.MinHeight = 1
	ti.MaxHeight = maxInputRows
	ti.MaxContentHeight = ti.CharLimit
	ti.SetHeight(1)
	ti.ShowLineNumbers = false
	applyTextareaTheme(ti)
	// Use the real terminal cursor (not a styled virtual one) so View can place
	// it at the insertion point and IME candidate windows anchor to the input.
	ti.SetVirtualCursor(false)
	// Plain Enter submits (the chatTUI handler intercepts it), so the textarea's
	// own InsertNewline binding moves to Alt+Enter / Ctrl+J / Shift+Enter.
	ti.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j", "shift+enter"))
	ti.Focus()
}

func isAndroidTerminal() bool {
	// 环境变量强制启用（用于 SSH 从触屏客户端连接的场景）
	if v := os.Getenv("REASONIX_NATIVE_SCROLLBACK"); v == "1" || v == "true" {
		return true
	}
	// 标准 Termux 环境变量
	if os.Getenv("TERMUX_VERSION") != "" || os.Getenv("TERMUX_APP_PID") != "" || os.Getenv("TERMUX_PREFIX") != "" {
		return true
	}
	// PREFIX 路径匹配（兼容 Termux 变体）
	prefix := os.Getenv("PREFIX")
	if strings.Contains(prefix, "/com.termux/") {
		return true
	}
	// Android 系统环境变量（非 Termux 的 Android 终端通常也有这些）
	if os.Getenv("ANDROID_ROOT") != "" || os.Getenv("ANDROID_DATA") != "" {
		return true
	}
	// Termux 数据目录 fallback
	if _, err := os.Stat("/data/data/com.termux/files"); err == nil {
		return true
	}
	// 通用 Android 路径探测
	if _, err := os.Stat("/system/build.prop"); err == nil {
		return true
	}
	return false
}

var detectAndroidTerminal = isAndroidTerminal

func (m chatTUI) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		waitForAgentEvent(m.eventCh),
		fetchBalance(m.ctrl),
		m.runStatusline(), // nil (no-op) unless a custom status line is configured
		m.refreshGitStatus(),
	)
}

func (m chatTUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	wasAtBottom := m.viewport.AtBottom()
	prevLines := len(m.transcript)
	prevWidth := m.width
	prevYOff := m.viewport.YOffset()
	prevHeight := m.viewport.Height()

	next, cmd := m.update(msg)
	cm, ok := next.(chatTUI)
	if !ok {
		return next, cmd
	}

	contentW := cm.width - 1 // last column is the scrollbar
	if contentW < 1 {
		contentW = 1
	}
	cm.viewport.SetWidth(contentW)
	cm.viewport.SetHeight(cm.transcriptHeight())
	// Re-feed only when the content grew or the width changed (re-wrapping is
	// the expensive part); a bare scroll or spinner tick keeps the offset.
	contentChanged := len(cm.transcript) != prevLines || cm.width != prevWidth || cm.transcriptDirty
	if contentChanged {
		wrapped := wrapTranscript(strings.Join(cm.transcript, "\n"), contentW)
		cm.viewport.SetContent(wrapped)
		cm.wrappedLines = strings.Split(wrapped, "\n")
	}
	// Tail-follow: stay pinned to newest output. Also re-pin on viewport
	// resize (e.g. approval/chooser panel appears/disappears) when the
	// user was at the bottom so they don't get stranded with blank space.
	if wasAtBottom && (contentChanged || cm.viewport.Height() != prevHeight) {
		cm.viewport.GotoBottom()
	}
	cm.transcriptDirty = false
	// Any viewport scroll (wheel, PgUp/PgDn, edge auto-scroll, or tail-follow to
	// newest output) shifts the whole window. Some terminals (Warp) mishandle
	// the renderer's scroll/insert-line optimization and strand stale rows, so
	// force a full clear+redraw whenever the offset actually moved.
	if cm.viewport.YOffset() != prevYOff {
		return cm, tea.Batch(tea.ClearScreen, cmd)
	}
	return cm, cmd
}

// update runs the model's message handling. Update wraps it to keep the
// transcript viewport sized, fed, and tail-following after every message.
func (m chatTUI) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.SetWidth(msg.Width - 4)
		m.renderer = newMarkdownRenderer(msg.Width - 5) // -5: reserve 4 for "▸" prefix + 1 for scrollbar
		// Commit the banner — and a resumed session's transcript — once, now
		// that the width is known.
		if !m.started {
			m.started = true
			var b strings.Builder
			b.WriteString(renderTUIBanner(m.label, m.missing, msg.Width))
			if len(m.history) > 0 {
				r := newMarkdownRenderer(msg.Width - 1)
				for _, sec := range replaySectionsFor(m.history, msg.Width, r) {
					b.WriteString(sec)
				}
				m.history = nil
			}
			m.commitLine(strings.TrimRight(b.String(), "\n"))
		}

	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.viewport.ScrollUp(3)
		case tea.MouseWheelDown:
			m.viewport.ScrollDown(3)
		}
		return m, nil

	case tea.MouseClickMsg:
		// Right-click copies the active selection (Windows Terminal convention);
		// left-press in the transcript region begins a text selection — unless
		// the click lands on a shell-output hint line, which toggles expand.
		if msg.Button == tea.MouseRight && m.sel.active && !m.sel.empty() {
			text := m.selectedText()
			m.sel = selection{}
			return m, tea.Batch(copyToClipboard(text), finalize(m, cmds))
		}
		if msg.Button == tea.MouseLeft && msg.Y < m.viewport.Height() {
			// Check if the clicked line is a shell-output hint.
			lineIdx := m.viewport.YOffset() + msg.Y
			if lineIdx >= 0 && lineIdx < len(m.wrappedLines) {
				clicked := m.wrappedLines[lineIdx]
				if strings.Contains(clicked, "more lines") && strings.Contains(clicked, "Ctrl+B") {
					m.toggleShellOutput()
					return m, finalize(m, cmds)
				}
			}
			at := m.transcriptCaret(msg.X, msg.Y)
			m.sel = selection{active: true, anchor: at, head: at}
			m.autoScroll = 0
		}
		return m, nil

	case tea.MouseMotionMsg:
		// Drag extends the live selection (CellMotion only reports motion while
		// a button is held, so this is a drag). A drag held against the top or
		// bottom edge starts an auto-scroll ticker so the selection can run past
		// the visible window.
		if m.sel.active {
			m.sel.head = m.transcriptCaret(msg.X, msg.Y)
			m.dragX = msg.X
			prev := m.autoScroll
			m.autoScroll = edgeScrollDir(msg.Y, m.viewport.Height())
			if m.autoScroll != 0 && prev == 0 {
				return m, autoScrollTick()
			}
		}
		return m, nil

	case autoScrollMsg:
		// One edge-scroll step: scroll a single line, drag the selection head to
		// the edge row, and keep ticking until the drag ends, leaves the edge, or
		// the viewport can't scroll further (so it can't run away to the end).
		if !m.sel.active || m.autoScroll == 0 {
			return m, nil
		}
		edgeY := 0
		if m.autoScroll > 0 {
			m.viewport.ScrollDown(1)
			edgeY = m.viewport.Height() - 1
		} else {
			m.viewport.ScrollUp(1)
		}
		m.sel.head = m.transcriptCaret(m.dragX, edgeY)
		// Stop at the boundary so a held edge can't run away to the very end.
		if (m.autoScroll > 0 && m.viewport.AtBottom()) || (m.autoScroll < 0 && m.viewport.AtTop()) {
			m.autoScroll = 0
			return m, nil
		}
		return m, autoScrollTick()

	case tea.MouseReleaseMsg:
		// Release finalizes the selection; the highlight stays on as the visual
		// "what's selected" cue and a right-click copies it. A plain click (no
		// drag) clears any prior selection.
		m.autoScroll = 0 // stop edge auto-scroll
		if msg.Button == tea.MouseLeft && m.sel.active && m.sel.empty() {
			m.sel = selection{}
		}
		return m, nil

	case tea.PasteMsg:
		if m.state != tuiRunning && m.attachPastedImages(msg.Content) {
			return m, finalize(m, cmds)
		}
		if ref, ok := pastedFileRef(msg.Content); ok {
			m.input.InsertString(ref + " ")
			m.growInputToFit()
			m.updateCompletion()
			return m, finalize(m, cmds)
		}
		if !m.chooserTyping() && m.pendingApproval == nil && m.rewind == nil && m.resumePick == nil && m.mcp == nil && m.mcpImport == nil && m.skillPick == nil && m.shouldFoldPaste(msg.Content) {
			m.insertFoldedPaste(msg.Content)
			m.growInputToFit()
			m.updateCompletion()
			return m, finalize(m, cmds)
		}

	case tea.KeyPressMsg:
		// Any keystroke dismisses a finished selection (copy is a right-click),
		// except Ctrl+C/Super+C/Meta+C which may copy the selection to clipboard.
		sel := m.sel
		m.sel = selection{}
		// Transcript scroll keys work in any state (PgUp/PgDn are never text).
		switch msg.String() {
		case "pgup":
			m.viewport.PageUp()
			return m, finalize(m, cmds)
		case "pgdown":
			m.viewport.PageDown()
			return m, finalize(m, cmds)
		case "ctrl+home":
			m.viewport.GotoTop()
			return m, finalize(m, cmds)
		case "ctrl+end":
			m.viewport.GotoBottom()
			return m, finalize(m, cmds)
		case "ctrl+z":
			return m, tea.Suspend
		}
		// A question card is modal: keys drive it. In its free-text ("Type
		// something") mode, the keystroke goes to the textarea — Enter confirms the
		// custom answer, Esc backs out of typing — so input/IME work as usual.
		if m.chooser != nil {
			if m.chooser.typing {
				switch msg.String() {
				case "enter":
					val := strings.TrimSpace(m.input.Value())
					m.input.Reset()
					m.chooser.typing = false
					if val == "" {
						return m, finalize(m, cmds)
					}
					m.chooser.custom[m.chooser.tab] = val
					m.chooser.sel[m.chooser.tab] = map[int]bool{}
					return m.chooserAdvance()
				case "esc":
					m.chooser.typing = false
					m.input.Reset()
					return m, finalize(m, cmds)
				}
				var ic tea.Cmd
				m.input, ic = m.input.Update(msg)
				cmds = append(cmds, ic)
				m.growInputToFit()
				return m, finalize(m, cmds)
			}
			return m.handleChooserKey(msg)
		}
		// The rewind picker is modal while open: keys navigate it.
		if m.rewind != nil {
			return m.handleRewindKey(msg)
		}
		// The MCP import picker is modal while open: keys select candidates.
		if m.mcpImport != nil {
			return m.handleMCPImportKey(msg)
		}
		// The resume picker is modal while open: keys navigate it.
		if m.resumePick != nil {
			return m.handleResumePickerKey(msg)
		}
		// The MCP manager is modal while open: keys navigate it.
		if m.mcp != nil {
			return m.handleMCPManagerKey(msg)
		}
		// The skill picker is modal while open: keys navigate it.
		if m.skillPick != nil {
			return m.handleSkillPickerKey(msg)
		}
		// A pending tool approval is modal: keystrokes answer it (y/a/n, Enter,
		// Esc) rather than reaching the input.
		if m.pendingApproval != nil {
			return m.handleApprovalKey(msg)
		}
		// While the autocomplete menu is open it captures navigation/accept keys
		// (↑/↓ move, Tab/Enter accept, Esc close); everything else falls through
		// to the textarea and re-filters the menu at the end of Update.
		if m.completion.active {
			switch msg.String() {
			case "up":
				m.moveCompletion(-1)
				return m, nil
			case "down":
				m.moveCompletion(1)
				return m, nil
			case "tab", "enter":
				if msg.String() == "enter" && (m.completionExactLabel() || m.completionBareOverlayCommand()) {
					m.completion = completion{}
					break // fall through to regular Enter and submit the command
				}
				// When Enter is pressed and the selected completion is already fully
				// present in the input, close the menu and submit instead of accepting
				// the same item again (/resume 1 still has /resume 10 as a prefix match).
				if msg.String() == "enter" && m.completionSelectedInsertPresent() {
					m.completion = completion{}
					break // fall through to regular Enter
				}
				m.acceptCompletion()
				return m, nil
			case "esc":
				m.completion = completion{}
				if m.state == tuiRunning {
					break // a turn is running — also cancel it via the main Esc handler
				}
				return m, nil
			}
		}
		switch msg.String() {
		case "up":
			if m.state == tuiRunning {
				if m.navigateQueue(-1) {
					return m, nil
				}
			} else {
				// nativeScrollback: arrow keys scroll the viewport when there is
				// content above the visible area; only recall input history when
				// already at the bottom. This lets Conduit's swipe→arrow gesture
				// scroll the transcript instead of cycling through history.
				if m.nativeScrollback && !m.viewport.AtBottom() {
					m.viewport.ScrollUp(3)
					return m, nil
				}
				if m.recallSubmittedInput(-1) {
					return m, nil
				}
			}
		case "down":
			if m.state == tuiRunning {
				if m.navigateQueue(1) {
					return m, nil
				}
			} else {
				if m.nativeScrollback && !m.viewport.AtBottom() {
					m.viewport.ScrollDown(3)
					return m, nil
				}
				if m.recallSubmittedInput(1) {
					return m, nil
				}
			}
		case "enter":
			// Don't reset queue navigation — the Enter handler below needs
			// queueEditCursor to decide whether to save an edit or enqueue.
		default:
			m.resetSubmittedInputRecall()
			m.resetQueueNavigation()
		}
		switch msg.String() {
		case "esc":
			// "Back out" of the most specific in-progress state: un-send a just-sent
			// turn (server not yet replied), cancel a streaming turn, or clear
			// typed-but-unsent input. Mode switches (normal/plan/YOLO) are
			// exclusively driven by Shift+Tab — Esc must not silently flip a
			// session from plan or YOLO back to a less-permissive mode. PR #3051
			// removed the YOLO half of this; plan mode was missed and is fixed
			// here. Scrollback is the terminal's now, so there's no viewport to
			// dismiss.
			switch {
			case m.state == tuiRunning && m.bubblePending:
				m.unsendPending()
			case m.state == tuiRunning:
				m.ctrl.Cancel()
				// Defensive: if the controller is no longer running (cancel
				// completed synchronously, e.g. for shell commands), transition
				// to idle immediately instead of waiting for TurnDone.
				if !m.ctrl.Running() {
					m.state = tuiIdle
					m.confirmBubbleSent()
				}
			default:
				if m.ctrl.Running() {
					m.ctrl.Cancel()
					break
				}
				// Idle (any mode): a double-Esc on an empty composer opens the
				// rewind picker (Claude Code's gesture); a first Esc just arms
				// it. Non-empty input clears as before.
				if strings.TrimSpace(m.input.Value()) == "" {
					if !m.lastEsc.IsZero() && time.Since(m.lastEsc) < 600*time.Millisecond {
						m.lastEsc = time.Time{}
						m.openRewind()
					} else {
						m.lastEsc = time.Now()
					}
				} else {
					m.input.Reset()
					m.pastedBlocks = nil
				}
			}
			return m, nil
		case "ctrl+c", "super+c", "meta+c":
			if m.state == tuiRunning {
				if m.bubblePending {
					m.unsendPending() // server not yet replied — restore text, leave no trace
				} else {
					m.ctrl.Cancel()
				}
				return m, nil
			}
			// Idle: an active text selection takes precedence over the
			// composer-clear / double-press-quit gestures. Standard terminal
			// convention is "Ctrl+C copies the selection" — the user can still
			// clear the input with a second Ctrl+C once the selection is gone.
			// Hoisting this branch above the clear branch also stops the
			// previous behaviour where Ctrl+C would dismiss a selection AND
			// wipe any draft text the user was typing — felt like the
			// selection was being silently lost.
			if sel.active && !sel.empty() {
				m.sel = sel // restore so selectedText() can read it
				text := m.selectedText()
				m.sel = selection{}
				return m, tea.Batch(copyToClipboard(text), finalize(m, cmds))
			}
			// No selection: if the composer has text, a single press clears it
			// (like Esc); on an empty composer a double-press within 1.5s quits.
			if strings.TrimSpace(m.input.Value()) != "" {
				m.input.Reset()
				m.pastedBlocks = nil
				m.lastCtrlCAt = time.Time{}
				return m, nil
			}
			if !m.lastCtrlCAt.IsZero() && time.Since(m.lastCtrlCAt) < 1500*time.Millisecond {
				return m, tea.Quit
			}
			m.lastCtrlCAt = time.Now()
			m.notice(i18n.M.CtrlCQuitHint)
			return m, finalize(m, nil)
		case "ctrl+d":
			return m, tea.Quit
		case "ctrl+v", "ctrl+shift+v", "super+v", "meta+v":
			if m.state == tuiRunning {
				return m, nil
			}
			cmds = append(cmds, pasteClipboard())
			return m, finalize(m, cmds)
		case "ctrl+y":
			if m.state == tuiRunning {
				return m, nil
			}
			cmds = append(cmds, pasteClipboardImage())
			return m, finalize(m, cmds)
		case "shift+tab":
			if m.state == tuiRunning {
				return m, nil
			}
			m.ctrl.SetBypass(!m.ctrl.Bypass())
			return m, nil
		case "ctrl+o":
			m.toggleVerboseReasoning(m.state != tuiRunning)
			return m, finalize(m, cmds)
		case "ctrl+b":
			m.toggleShellOutput()
			return m, finalize(m, cmds)
		case "enter":
			if m.state == tuiRunning {
				line := strings.TrimSpace(m.input.Value())
				if line == "" {
					return m, nil
				}
				if m.queueEditCursor >= 0 && m.queueEditCursor < len(m.pendingInterject) {
					// Save the edited text back to the queue slot.
					m.pendingInterject[m.queueEditCursor] = line
					m.notice(fmt.Sprintf("queue [%d] updated", m.queueEditCursor+1))
					m.queueEditCursor = -1
					m.queueEditDraft = ""
				} else {
					m.ctrl.Steer(line)
					m.notice("steered")
					m.queueEditCursor = -1
					m.queueEditDraft = ""
				}
				m.input.Reset()
				m.pastedBlocks = nil
				return m, finalize(m, cmds)
			}
			if m.modelSwitchPending {
				return m, nil // ignore Enter while /model switch is building
			}
			line := strings.TrimSpace(m.input.Value())

			if line == "" {
				m.viewport.GotoBottom()
				return m, nil
			}

			if m.queueEditCursor >= 0 && m.queueEditCursor < len(m.pendingInterject) {
				// Save the edited text back to the queue slot.
				m.pendingInterject[m.queueEditCursor] = line
				m.notice(fmt.Sprintf("queue [%d] updated", m.queueEditCursor+1))
				m.queueEditCursor = -1
				m.queueEditDraft = ""
				m.input.Reset()
				m.pastedBlocks = nil
				return m, finalize(m, cmds)
			}
			if line == "exit" || line == "quit" || line == ":q" {
				_ = m.ctrl.Snapshot()
				return m, tea.Quit
			}
			m.rememberSubmittedInput(line)

			// "!<cmd>" runs a shell command directly, bypassing the model.
			if strings.HasPrefix(line, "!") {
				cmd := strings.TrimPrefix(line, "!")
				if strings.TrimSpace(cmd) == "" {
					m.input.Reset()
					m.pastedBlocks = nil
					m.notice(i18n.M.ShellExecEmpty)
					return m, finalize(m, cmds)
				}
				m.input.Reset()
				m.pastedBlocks = nil
				m.state = tuiRunning
				m.runStart = time.Now()
				m.elapsed = 0
				m.turnTokens = 0
				m.pendingRestore = line
				m.bubbleStartIdx = len(m.transcript)
				m.commitLine("")
				m.commitLine(renderUserBubble(line, m.width))
				m.bubblePending = true
				m.turnDiscarded = false
				m.confirmBubbleSent() // shell events arrive instantly
				m.ctrl.RunShell(cmd)
				return m, tea.Batch(m.spinner.Tick, elapsedTick())
			}

			// Slash commands run locally without going through the model. A
			// '/'-leading line that's actually a dragged file path is an attachment,
			// not a command, so it's rewritten to an @reference instead.
			if strings.HasPrefix(line, "/") {
				if ref, ok := control.FileRefLine(line); ok {
					line = ref
				} else {
					m.input.Reset()
					m.pastedBlocks = nil
					cmds = append(cmds, m.runSlashCommand(line))
					return m, finalize(m, cmds)
				}
			}

			sentLine := m.expandPastedBlocks(line)
			m.input.Reset()

			// @references (local files / MCP resources, including inline image
			// attachments) are resolved off the event loop by the controller; the turn
			// starts when they resolve (refsResolvedMsg).
			if m.ctrl.HasRefs(sentLine) {
				cmds = append(cmds, m.resolveRefs(sentLine, sentLine, line))
				return m, finalize(m, cmds)
			}

			cmds = append(cmds, m.startTurnWithRaw(sentLine, sentLine, line, line))
			return m, finalize(m, cmds)
		}

	case agentEventMsg:
		e := event.Event(msg)
		// Controller-initiated auto-reentry (background task done) does not pass
		// through startTurn; enter tuiRunning when TurnStarted arrives while idle.
		if e.Kind == event.TurnStarted && m.state != tuiRunning && !m.turnDiscarded {
			m.state = tuiRunning
			m.runStart = time.Now()
			m.elapsed = 0
			m.turnTokens = 0
			cmds = append(cmds, m.spinner.Tick, elapsedTick())
		}
		m.ingestEvent(e)
		turnDone := e.Kind == event.TurnDone
		gitMaybeChanged := e.Kind == event.ToolResult && !e.Tool.ReadOnly
		// Coalesce a burst: the goroutine that produced this event has already
		// exited (a Cmd reads the channel once), so it's safe to drain the events
		// already buffered and ingest them now. One re-wrap then covers the whole
		// batch instead of one per event — bounds the O(transcript) re-render cost
		// when bash output or reasoning floods in. Capped so a sustained flood
		// still yields to render periodically.
	drain:
		for drained := 0; drained < maxEventDrain; drained++ {
			select {
			case e2 := <-m.eventCh:
				m.ingestEvent(e2)
				if e2.Kind == event.TurnDone {
					turnDone = true
				}
				if e2.Kind == event.ToolResult && !e2.Tool.ReadOnly {
					gitMaybeChanged = true
				}
			default:
				break drain
			}
		}
		cmds = append(cmds, waitForAgentEvent(m.eventCh))
		// A turn just spent tokens (and money) — refresh the balance readout and
		// the custom status line (its context/cost inputs just changed).
		if turnDone {
			cmds = append(cmds, fetchBalance(m.ctrl))
			if c := m.runStatusline(); c != nil {
				cmds = append(cmds, c)
			}
			if len(m.pendingInterject) > 0 {
				interject := m.pendingInterject[0]
				m.pendingInterject = m.pendingInterject[1:]
				// Reset queue navigation — the indices shifted.
				m.queueEditCursor = -1
				m.queueEditDraft = ""
				cmds = append(cmds, m.startTurn(interject, interject, interject))
			}
		}
		if turnDone || gitMaybeChanged {
			if c := m.refreshGitStatus(); c != nil {
				cmds = append(cmds, c)
			}
		}

	case balanceMsg:
		m.balance = msg.text

	case statuslineMsg:
		m.statuslineOut = msg.out

	case gitStatusMsg:
		m.gitStatus = msg.status

	case compactDoneMsg:
		if msg.err != nil {
			m.notice(fmt.Sprintf("%s: %v", i18n.M.SlashCompactFailed, msg.err))
		} else {
			_ = m.ctrl.Snapshot()
		}

	case modelSwitchMsg:
		m.modelSwitchPending = false
		m.pendingModelSwitch = nil
		if msg.err != nil {
			m.notice("model: " + msg.err.Error())
			// Build failed — no old controller to retire.
		} else {
			m.ctrl = msg.ctrl
			m.label = msg.label
			m.commands = msg.commands
			m.skills = msg.skills
			m.host = msg.host
			m.modelRef = msg.ref
			m.refreshEffortStatus()
			// Stash the old controller for cleanup at exit. It cannot be
			// closed here or in the build goroutine — Close() runs
			// SessionEnd hooks and kills plugin subprocesses, both of
			// which corrupt bubbletea's terminal raw mode.
			if msg.oldCtrl != nil {
				m.oldControllers = append(m.oldControllers, msg.oldCtrl)
			}
			m.notice(fmt.Sprintf(i18n.M.ModelSwitchedFmt, m.label))
			cmds = append(cmds, fetchBalance(m.ctrl))
			if c := m.runStatusline(); c != nil {
				cmds = append(cmds, c)
			}
			// Do NOT re-issue waitForAgentEvent here — the goroutine from the
			// last agentEventMsg handler is still blocked on the same channel.
			// Starting a second one creates a race: two goroutines compete on
			// p.Send (unbuffered), and the receiver may read them out of order,
			// garbling the streamed text (words appear reordered).
		}

	case promptResolvedMsg:
		switch {
		case msg.err != nil:
			m.commitLine(wrapForViewport(i18n.M.ErrorPrefix+" "+msg.err.Error(), m.width, activeCLITheme.warn))
		case strings.TrimSpace(msg.sent) == "":
			m.notice(i18n.M.SlashPromptEmpty)
		default:
			cmds = append(cmds, m.startTurn(msg.sent, msg.display, msg.display))
		}

	case mcpExternalDoneMsg:
		if msg.err != nil {
			m.notice(msg.label + ": " + msg.err.Error())
		} else if msg.target != "" {
			m.notice(msg.label + ": " + msg.target)
		}

	case refsResolvedMsg:
		for _, e := range msg.errs {
			m.notice(e) // surface a fetch failure but still send the turn
		}
		sent := msg.sent
		if msg.block != "" {
			sent = "Referenced context:\n\n" + msg.block + "\n\n" + msg.sent
		}
		cmds = append(cmds, m.startTurnWithRaw(sent, msg.display, msg.restore, msg.restore))

	case clipboardImageMsg:
		if msg.err != nil {
			m.notice("paste image: " + msg.err.Error())
			break
		}
		m.insertImageRef(msg.path)

	case clipboardPasteMsg:
		switch {
		case msg.err != nil:
			m.notice("paste: " + msg.err.Error())
		case msg.path != "":
			m.insertImageRef(msg.path)
		case msg.text != "":
			if m.attachPastedImages(msg.text) {
				return m, finalize(m, cmds)
			}
			if ref, ok := pastedFileRef(msg.text); ok {
				m.input.InsertString(ref + " ")
			} else if m.shouldFoldPaste(msg.text) {
				m.insertFoldedPaste(msg.text)
			} else {
				m.input.InsertString(msg.text)
			}
			m.growInputToFit()
			m.updateCompletion()
			return m, finalize(m, cmds)
		}

	case elapsedTickMsg:
		if m.state == tuiRunning {
			m.elapsed = int(time.Since(m.runStart).Seconds())
			m.tickToolRunning()
			cmds = append(cmds, elapsedTick())
		}

	case spinner.TickMsg:
		if m.state == tuiRunning {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	var ic tea.Cmd
	m.input, ic = m.input.Update(msg)
	cmds = append(cmds, ic)
	m.growInputToFit()
	// Re-filter the autocomplete menu against the freshly-edited input.
	if _, ok := msg.(tea.KeyPressMsg); ok {
		m.updateCompletion()
	}

	return m, finalize(m, cmds)
}

// finalize drains the committed-line queue and batches the turn's commands.
// In alt-screen mode the queue is already mirrored in m.transcript which feeds
// the viewport; this just clears the buffer. (Native-scrollback mode previously
// emitted lines via tea.Println, but now it uses alt-screen + viewport too.)
func finalize(m chatTUI, cmds []tea.Cmd) tea.Cmd {
	*m.pendingCommit = (*m.pendingCommit)[:0]
	return tea.Batch(cmds...)
}

// scrollChunkHeight is the largest block (in lines) finalize prints at once in
// native-scrollback mode, leaving room for the pinned bottom frame.
func (m chatTUI) scrollChunkHeight() int {
	if m.height <= 0 {
		return 100
	}
	if n := m.height - m.bottomRows(); n > 1 {
		return n
	}
	return 1
}

// chunkLines splits s into blocks of at most n lines each, preserving order and
// line content. A single block is returned when it already fits.
func chunkLines(s string, n int) []string {
	if n < 1 {
		n = 1
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return []string{s}
	}
	var out []string
	for i := 0; i < len(lines); i += n {
		end := i + n
		if end > len(lines) {
			end = len(lines)
		}
		out = append(out, strings.Join(lines[i:end], "\n"))
	}
	return out
}

// clampWidth hard-breaks any line wider than width so no scrollback line wraps
// in the terminal. bubbletea's inline renderer estimates how far to scroll for
// each printed block from each line's width (insertAbove: offset += width/w); an
// over-wide line that the terminal wraps throws that estimate off and drifts the
// pinned input box off-screen. Lines already within width are left byte-for-byte
// untouched (chunkByWidth preserves content and ANSI), so rendered tables and the
// wrapped answer — which the markdown renderer already fit to width — are safe;
// only stray long lines (tool-dispatch args, unwrapped code) get broken.
func clampWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	// ansi.Hardwrap breaks any line over `width` visible cols on grapheme
	// boundaries, preserving ANSI and counting wide chars — exactly what we want,
	// and lines already within width pass through unchanged.
	return ansi.Hardwrap(s, width, false)
}

// commitLine queues one finalized block for the next scrollback flush.
func (m *chatTUI) commitLine(s string) {
	*m.pendingCommit = append(*m.pendingCommit, s)
	m.transcript = append(m.transcript, s)
}

// commitSpacer separates the next block (a thinking marker or a tool line) from
// the previous one with a single blank line, skipping it at the top of the
// transcript or when a blank already trails so spacers never double up.
func (m *chatTUI) commitSpacer() {
	if n := len(m.transcript); n > 0 && strings.TrimSpace(m.transcript[n-1]) != "" {
		m.commitLine("")
	}
}

// bottomRows is the terminal-row height of the pinned bottom region: any open
// bottom panels (approval / chooser / rewind / completion), the composer
// when visible, and the two fixed status rows. Full-screen managers such as MCP
// and skills render inside the main transcript area.
func (m chatTUI) bottomRows() int {
	rows := 0
	for _, s := range []string{
		m.renderApprovalBanner(),
		m.renderChooser(),
		m.renderRewind(),
		m.renderMCPImport(),
		m.renderResumePicker(),
		m.renderCompletion(),
	} {
		if s != "" {
			rows += strings.Count(s, "\n") + 1
		}
	}
	if m.state == tuiRunning {
		rows++ // the working spinner line above the box
	}
	if footer := m.renderMainManagerFooter(); footer != "" {
		rows += strings.Count(footer, "\n") + 1
	}
	if !m.hideComposer() {
		qi := m.renderQueueIndicator()
		if qi != "" {
			rows += strings.Count(qi, "\n") + 1
		}
		rows += m.input.Height() + 2
	}
	return rows + 2
}

// hideComposer is the single ownership gate for the bottom composer.
//
// Rule for new CLI panels:
//   - If a panel is modal and keystrokes navigate/confirm/cancel the panel, hide
//     the composer so users do not see an inactive chat input.
//   - If a panel is input-owned (autocomplete, or chooser free-text mode), keep
//     the composer visible because the textarea is the active control.
//
// Whenever a new slash-command overlay or approval-style prompt is added, update
// this function and the modal layout tests together. Otherwise the panel may
// reserve rows for a composer that cannot receive input, leaving a confusing
// blank/bordered area at the bottom of the TUI.
func (m chatTUI) hideComposer() bool {
	if m.mcp != nil || m.mcpImport != nil || m.skillPick != nil || m.resumePick != nil || m.rewind != nil || m.pendingApproval != nil {
		return true
	}
	return m.chooser != nil && !m.chooser.typing
}

// transcriptHeight is the row budget left for the transcript viewport once the
// pinned bottom region is accounted for (at least one row).
func (m chatTUI) transcriptHeight() int {
	if h := m.height - m.bottomRows(); h > 1 {
		return h
	}
	return 1
}
