// Package boot assembles a ready-to-drive control.Controller from configuration:
// it loads config, resolves the model(s), builds the tool registry (built-ins +
// plugins), wires the permission gate, and constructs the executor — optionally
// wrapping it in a two-model Coordinator. It is the one place that turns "what the
// user configured" into "a Controller a frontend can drive", so every frontend —
// the terminal TUI, the HTTP/SSE server, the desktop webview — shares the exact
// same assembly instead of each re-deriving it. Frontends pass only a sink and a
// couple of run knobs; everything else comes from config.
package boot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/command"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/hook"
	"reasonix/internal/installsource"
	"reasonix/internal/instruction"
	"reasonix/internal/lsp"
	"reasonix/internal/memory"
	"reasonix/internal/multiagent"
	"reasonix/internal/netclient"
	"reasonix/internal/permission"
	"reasonix/internal/plugin"
	"reasonix/internal/provider"
	"reasonix/internal/shell"
	"reasonix/internal/skill"
	"reasonix/internal/tool"
	"reasonix/internal/tool/builtin"
	"reasonix/internal/tool/sessiontool"
)

// ErrUnknownModel is returned by Build when the configured model can't be
// resolved to a provider — e.g. a default_model left over from a renamed or
// removed provider. Callers can detect it (errors.Is) to re-run setup.
var ErrUnknownModel = errors.New("unknown model")

// Options carries the per-run knobs a frontend chooses; everything else is read
// from configuration. Model "" falls back to the configured default_model;
// MaxSteps 0 uses the config/default. RequireKey forces the executor's API key to
// be present (run/serve pass true so a missing key fails fast; chat/desktop pass
// false so the UI is reachable before a key is set). Sink receives the agent's
// typed event stream.
type Options struct {
	Model      string
	MaxSteps   int
	RequireKey bool
	Sink       event.Sink
	// EffortOverride is a session-local reasoning effort override. Nil means use
	// the resolved provider config; a non-nil empty string means provider default.
	EffortOverride *string
	// Stderr is the writer for diagnostic warnings and plugin subprocess
	// stderr output. When nil, defaults to os.Stderr. Set to io.Discard
	// during model switch inside a bubbletea session to prevent any output
	// from corrupting the TUI's terminal raw mode.
	Stderr io.Writer
	// WorkspaceRoot is the project root directory for config, skills, memory,
	// commands, hooks, and tool confinement. When empty, the current working
	// directory is used (CLI default). Desktop tabs pass their project root here
	// so each tab loads its own config/skills/hooks without changing the process
	// cwd — enabling concurrent multi-project sessions.
	WorkspaceRoot string
	// ConfigRoot is the directory from which reasonix.toml is loaded. When empty,
	// WorkspaceRoot (or its fallback) is used. This decouples config injection
	// (e.g. a bridge-controlled reasonix.toml) from the tool workspace — the
	// agent operates in WorkspaceRoot but reads config from ConfigRoot.
	ConfigRoot string

	// SkipModelRefresh skips the live GET /models probe per provider.
	// Used by serve.switchModel to avoid network I/O while holding its
	// write lock; the initial boot (TUI, desktop, serve creation) does
	// refresh so the model list is current once.
	SkipModelRefresh bool
}

// Build loads config, resolves the model(s), and returns a Controller wrapping a
// single Agent, or a two-model Coordinator when agent.planner_model is set. The
// returned controller owns plugin subprocesses; call Close (via Controller.Close)
// to release them.
func Build(ctx context.Context, opts Options) (*control.Controller, error) {
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	root := opts.WorkspaceRoot
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		}
	}
	// Config root: when ConfigRoot is set, load reasonix.toml from there
	// instead of from WorkspaceRoot, so config injection (tool lockdown, etc.)
	// is decoupled from the tool workspace.
	cfgRoot := opts.ConfigRoot
	if cfgRoot == "" {
		cfgRoot = root
	}
	// One-time import of v1/v0.5 legacy config — runs before Load so the freshly
	// written config + ~/.env are picked up this same boot. CLI Run also calls this
	// before config-only commands; this call stays as the shared frontend fallback.
	migrated, migErr := config.MigrateLegacyIfNeeded()
	cfg, err := config.LoadForRoot(cfgRoot)
	if err != nil {
		return nil, err
	}

	// Refresh live model lists from provider APIs. Providers with both a
	// base_url and an API key get their model list fetched with a 3s timeout.
	// On failure the static Models/Model config fields serve as fallback.
	// Each provider is refreshed in parallel to bound the total delay at 3s
	// regardless of how many providers are configured.
	if !opts.SkipModelRefresh {
		var wg sync.WaitGroup
		for i := range cfg.Providers {
			p := &cfg.Providers[i]
			if p.BaseURL == "" || p.APIKey() == "" {
				continue
			}
			wg.Add(1)
			go func(p *config.ProviderEntry) {
				defer wg.Done()
				ictx, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				if err := p.RefreshModels(ictx); err != nil {
					slog.Debug("live model refresh failed", "provider", p.Name, "error", err)
				}
			}(p)
		}
		wg.Wait()
	}

	if !opts.SkipModelRefresh {
		// Auto-populate per-model pricing for OpenCode Go providers by scraping
		// the official docs page. Falls back gracefully — user can still supply
		// model_prices manually in reasonix.toml.
		for i := range cfg.Providers {
			if strings.Contains(cfg.Providers[i].BaseURL, "opencode.ai/zen/go") {
				ictx, cancel := context.WithTimeout(ctx, 15*time.Second)
				scraped, scrapeErr := config.ScrapeOpenCodePricing(ictx)
				cancel()
				if scrapeErr != nil {
					slog.Debug("opencode pricing scrape failed", "error", scrapeErr)
				} else {
					config.ApplyOpenCodePricing(cfg, scraped)
				}
				break
			}
		}
	}

	modelName := opts.Model
	if modelName == "" {
		modelName = cfg.DefaultModel
	}
	entry, ok := cfg.ResolveModel(modelName)
	if !ok {
		return nil, fmt.Errorf("%w %q (configured: %s); note: defining [[providers]] replaces the built-in presets, so add a [[providers]] entry for it or use a configured name, or run `reasonix setup` to reconfigure", ErrUnknownModel, modelName, providerNames(cfg))
	}
	if opts.EffortOverride != nil {
		entry.Effort = *opts.EffortOverride
		if entry.Kind == "anthropic" && strings.TrimSpace(entry.Effort) != "" && strings.TrimSpace(entry.Thinking) == "" {
			entry.Thinking = "adaptive"
		}
	}
	if opts.RequireKey {
		if err := cfg.Validate(modelName); err != nil {
			return nil, err
		}
	}

	// Serialize the frontend's sink once so concurrent multiagent emissions
	// share one synchronized sink with the running turn.
	sink := event.Sync(opts.Sink)

	agent.SessionEncryptionEnabled = cfg.Agent.EncryptSessions

	if migErr != nil {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "config migration from ~/.reasonix failed: " + migErr.Error()})
	} else if migrated != nil {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: migrated.Notice()})
	}
	migrateLegacySessionSources(sink)

	// A resolvable model whose API key env is unset would otherwise build fine
	// (RequireKey is false so the UI stays reachable) and then fail silently on the
	// first request, showing as an empty/dead model. Surface the cause up front.
	if !opts.RequireKey && entry.APIKeyEnv != "" && entry.APIKey() == "" {
		sink.Emit(event.Event{Kind: event.Notice, Text: fmt.Sprintf("model %q is selected but its API key %s is not set — requests will fail until you set it", modelName, entry.APIKeyEnv)})
	}
	proxySpec := cfg.NetworkProxySpec()
	if err := netclient.Validate(proxySpec); err != nil {
		return nil, err
	}

	caPool, err := netclient.LoadCACert(cfg.Network.CACertPath)
	if err != nil {
		return nil, err
	}
	netclient.SetGlobalCACerts(caPool)
	trOpts := netclient.TransportOptions{RootCAs: caPool}

	balanceClient, err := netclient.NewHTTPClient(proxySpec, trOpts)
	if err != nil {
		return nil, err
	}

	execProv, err := NewProviderWithProxy(entry, proxySpec)
	if err != nil {
		return nil, err
	}

	sysPrompt, err := cfg.ResolveSystemPrompt()
	if err != nil {
		return nil, err
	}

	// Persistent memory (REASONIX.md / AGENTS.md hierarchy + auto-memory index)
	// folds into the system prompt exactly here, once: it becomes part of the
	// durable, cache-stable prefix every turn reuses, so memory costs nothing per
	// turn. Mid-session changes never touch this prefix — they ride the
	// controller's transient turn-injection and fold in on the next session.
	mem := memory.Load(memory.Options{CWD: root, UserDir: config.MemoryUserDir()})
	projectChecks := instruction.ExtractHostChecks(mem.Docs)
	sysPrompt = memory.Compose(sysPrompt, mem)

	// Skills: discover playbooks (built-in + project/custom/global) and fold their
	// one-liner index into the same cache-stable prefix — names + descriptions
	// only; bodies load on demand via run_skill or "/<name>". Bodies never enter
	// the prefix, so the index costs a fixed, small amount per turn.
	skillStore := skill.New(skill.Options{
		ProjectRoot:   root,
		CustomPaths:   cfg.SkillCustomPaths(),
		ExcludedPaths: cfg.SkillExcludedPaths(),
		DisabledNames: cfg.DisabledSkillNames(),
		MaxDepth:      cfg.SkillMaxDepth(),
		Stderr:        opts.Stderr,
	})
	skills := skillStore.List()
	allSkillStore := skill.New(skill.Options{ProjectRoot: root, CustomPaths: cfg.SkillCustomPaths(), ExcludedPaths: cfg.SkillExcludedPaths(), MaxDepth: cfg.SkillMaxDepth(), Stderr: io.Discard})
	allSkills := allSkillStore.List()
	sysPrompt = skill.ApplyIndex(sysPrompt, skills)

	// Cache-stable policy blocks: tool-use enforcement, visibility rules, and
	// (when no concrete UI language is set) the auto language-matching policy.
	sysPrompt = config.AppendSystemPolicies(sysPrompt, cfg)

	reg := tool.NewRegistry()
	if shell.ResolveShell().Kind == shell.ShellPowerShell {
		fmt.Fprintln(stderr, "warning: bash not found on PATH; the shell tool will run commands under Windows PowerShell. Install Git for Windows or WSL to use bash.")
	}
	searchSpec := builtin.ResolveSearch(cfg.Tools.Search.Engine, cfg.Tools.Search.RgPath, stderr)
	bashTimeout := time.Duration(cfg.BashTimeoutSeconds()) * time.Second
	addBuiltins(reg, cfg.Tools.Enabled, cfg.WriteRootsForRoot(root), bashTimeout, searchSpec, stderr, root)

	// Built-in codegraph MCP server: when enabled, registers as a background
	// plugin so session start is not blocked. Init runs with a short timeout
	// (and never longer than remaining Build ctx) so a wedged helper cannot
	// hang boot or tests indefinitely.
	if cfg.Codegraph.Enabled && cfg.Codegraph.Path != "" {
		sink.Emit(event.Event{Kind: event.Notice, Text: "preparing code-intelligence tools in the background"})
		initCtx, initCancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := exec.CommandContext(initCtx, cfg.Codegraph.Path, "init", root)
		if err := cmd.Run(); err != nil {
			slog.Warn("codegraph: init failed", "error", err)
		}
		initCancel()
		tier := cfg.Codegraph.Tier
		if tier == "" {
			tier = "background"
		}
		cfg.Plugins = append([]config.PluginEntry{{
			Name:    "codegraph",
			Command: cfg.Codegraph.Path,
			Tier:    tier,
		}}, cfg.Plugins...)
	}

	// Always construct a host, even with no plugins configured, so the controller's
	// host pointer is stable for the session and `/mcp add` can hot-add into it.
	pluginHost := plugin.NewHost()
	pluginHost.SetRegistry(reg)

	// Partition configured plugins by tier so eager/lazy/background can each
	// take the path that fits them. User entries default to background: the
	// session starts immediately while enabled MCP servers warm up.
	eagerEntries, lazyEntries, bgEntries := partitionByTier(cfg.AutoStartPlugins())

	// Auto-demote: any eager plugin that has been chronically slow (recent
	// samples repeatedly hit the blocking startup budget) drops to lazy
	// for this session. The user keeps eager intent, just doesn't pay for it
	// on a server that's been misbehaving. A notice surfaces the demotion.
	var demoteMessages []string
	budget := plugin.DefaultStartupBudget()
	kept := eagerEntries[:0]
	for _, e := range eagerEntries {
		rec := plugin.Recommend(e.Name, budget, 0)
		if rec.Demote {
			demoteMessages = append(demoteMessages, rec.Reason)
			lazyEntries = append(lazyEntries, e)
			continue
		}
		kept = append(kept, e)
	}
	eagerEntries = kept

	eagerSpecs := PluginSpecs(eagerEntries)
	lazySpecs := PluginSpecs(lazyEntries)
	bgSpecs := PluginSpecs(bgEntries)

	if opts.Stderr != nil {
		for i := range eagerSpecs {
			eagerSpecs[i].Stderr = opts.Stderr
		}
		for i := range lazySpecs {
			lazySpecs[i].Stderr = opts.Stderr
		}
		for i := range bgSpecs {
			bgSpecs[i].Stderr = opts.Stderr
		}
	}

	// Eager: block until handshake. Failures show up in /mcp.
	if len(eagerSpecs) > 0 {
		host, ptools := plugin.StartAvailable(ctx, eagerSpecs)
		host.SetRegistry(reg)
		pluginHost = host
		for _, t := range ptools {
			reg.Add(t)
		}
		// PhaseB (prompts + resources) runs on the boot ctx — which is the
		// controller's session-scoped PluginCtx — so the auxiliary surfaces
		// keep streaming in after Start returns without holding up the agent.
		go host.StartPhaseB(ctx, sink)
		if text, ok := MCPStartupNotice(host.Failures()); ok {
			sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: text})
		}
	}

	// Lazy / background: register placeholder tools now; the real spawn waits
	// for either the first model call (lazy) or a goroutine kicked off here
	// (background). Both share the same pluginHost so /mcp status, hot-add,
	// and Close see one cohesive set of servers regardless of tier.
	registerDeferred := func(specs []plugin.Spec, kick bool) {
		for _, s := range specs {
			cs, _ := plugin.LoadCachedSchema(s.Name, plugin.SpecFingerprint(s))
			for _, t := range plugin.LazyToolset(s, cs, pluginHost, ctx, kick) {
				reg.Add(t)
			}
		}
	}
	registerDeferred(lazySpecs, false)
	registerDeferred(bgSpecs, true)

	cleanup := pluginHost.Close

	// LSP tools resolve their servers on PATH and spawn lazily on first query, so
	// registering them is cheap even when no server is installed (a query then
	// returns an install hint). The manager is session-scoped; chain its shutdown
	// into the controller's cleanup so servers stop with the session, not the turn.
	if cfg.LSP.Enabled {
		lspMgr := lsp.NewManager(root, LSPSpecs(cfg.LSP))
		for _, t := range lsp.Tools(lspMgr) {
			reg.Add(t)
		}
		prev := cleanup
		cleanup = func() { prev(); lspMgr.Close() }
	}

	maxSteps := cfg.Agent.MaxSteps
	if opts.MaxSteps > 0 {
		maxSteps = opts.MaxSteps
	}

	// Permission policy gates every tool call. The headless gate (no Approver)
	// resolves "ask" to allow — preserving `reasonix run` autonomy — while deny
	// rules hard-block in every mode. Interactive frontends (chat, desktop) swap
	// in an interactive gate later via Controller.EnableInteractiveApproval.
	// Sub-agents always run headless: they have no UI to answer a prompt, so they
	// inherit this same gate.
	policy := permission.New(cfg.Permissions.Mode, cfg.Permissions.Allow, cfg.Permissions.Ask, cfg.Permissions.Deny)

	// Hooks: load the global settings.json plus the project's (only when trusted —
	// project hooks run arbitrary shell commands, so cloning a repo must not
	// silently execute them). Non-blocking hook output is surfaced to the user as
	// a Notice through the shared sink. The runner fires PreToolUse/PostToolUse in
	// the agent loop and UserPromptSubmit/Stop at the controller's turn boundary.
	hooksTrusted := hook.IsTrusted(root, "")
	hookRunner := hook.NewRunner(
		hook.Load(hook.LoadOptions{ProjectRoot: root, Trusted: hooksTrusted}),
		root, nil,
		func(msg string) { sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: msg}) },
	)
	if hook.ProjectDefinesHooks(root) && !hooksTrusted {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
			Text: "this project defines hooks but they are not trusted — run /hooks trust to enable them"})
	}

	// The `task` tool spawns sub-agents that reuse the parent's provider and
	// tool registry. Wired here after the built-ins / plugins are loaded so
	// sub-agents inherit the full tool set (minus `task` itself, to keep
	// nesting out of the picture). It registers into the same reg the
	// executor uses, so the model surfaces it like any other tool.
	resolveSubagentProviderForTask := func(role, modelRef, effort string) (provider.Provider, *provider.Pricing, int, error) {
		if strings.TrimSpace(role) == "" {
			role = "task"
		}
		// Freeform task only. Config keys off role name (usually "task").
		if strings.TrimSpace(modelRef) == "" {
			modelRef = subagentModelRef(cfg, role)
		}
		if strings.TrimSpace(modelRef) == "" {
			return nil, nil, 0, fmt.Errorf("subagent_model not configured for %q", role)
		}
		resolved, ok := cfg.ResolveModel(modelRef)
		if !ok {
			return nil, nil, 0, fmt.Errorf("unknown subagent model %q", modelRef)
		}
		me := *resolved
		if strings.TrimSpace(effort) == "" {
			effort = subagentEffortRef(cfg, role)
		}
		if strings.TrimSpace(effort) != "" {
			normalized, err := config.NormalizeEffort(&me, effort)
			if err != nil {
				return nil, nil, 0, err
			}
			me.Effort = normalized
			if me.Kind == "anthropic" && strings.TrimSpace(me.Effort) != "" && strings.TrimSpace(me.Thinking) == "" {
				me.Thinking = "adaptive"
			}
		}
		p, err := NewProviderWithProxy(&me, proxySpec)
		if err != nil {
			return nil, nil, 0, err
		}
		return p, me.Price, me.ContextWindow, nil
	}

	// extractSharedSections reads the reasonix-system.md prompt and extracts
	// sections marked with [≡ shared] for injection into sub-agent system prompts.
	extractSharedSections := func() string {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		data, err := os.ReadFile(filepath.Join(home, ".config", "reasonix", "prompts", "reasonix-system.md"))
		if err != nil {
			return ""
		}
		lines := strings.Split(string(data), "\n")
		var out strings.Builder
		inShared := false
		for _, line := range lines {
			if strings.HasPrefix(line, "#") && strings.Contains(line, "[≡ shared]") {
				inShared = true
				out.WriteString(line + "\n")
				continue
			}
			if inShared {
				if strings.HasPrefix(line, "#") {
					inShared = false
					continue
				}
				out.WriteString(line + "\n")
			}
		}
		return out.String()
	}

	subAgentGate := permission.NewGate(policy, nil)
	// Codex multi-agent V1: spawn / send_input / wait / close / resume (no nesting).
	maCtrl := multiagent.NewControl()
	multiagent.RegisterTools(reg)
	taskTool := agent.NewTaskTool(execProv, entry.Price, reg,
		entry.ContextWindow, cfg.Agent.SoftCompactRatio, cfg.Agent.CompactRatio, cfg.Agent.CompactForceRatio,
		cfg.Agent.Temperature, config.ArchiveDir(), agent.DefaultTaskSystemPrompt+"\n\n"+extractSharedSections(), subAgentGate,
		resolveSubagentProviderForTask, hookRunner)
	maRunner := &agent.MultiAgentRunner{
		Tool:       taskTool,
		Control:    maCtrl,
		SessionDir: filepath.Join(config.SessionDir(), "subagent-sessions"),
	}
	maCtrl.SetRunner(maRunner)

	// Session history tools let the AI discover and read past conversations.
	// `list_sessions` returns all saved session files; `read_session` loads one
	// and renders the full conversation as readable text.
	reg.Add(sessiontool.NewListSessionsTool(config.SessionDir()))
	reg.Add(sessiontool.NewReadSessionTool(config.SessionDir()))

	// Memory tools (recall / remember / forget): memory/* namespace only.
	// Skills use run_skill with skill/* — separate tools, params, and index shape.
	reg.Add(memory.NewRememberTool(mem.Store))
	reg.Add(memory.NewForgetTool(mem.Store))
	reg.Add(memory.NewRecallTool(mem.Store))

	// The `ask` tool puts structured multiple-choice questions to the user. It
	// reaches them through the Asker on the call context, which interactive
	// frontends wire to the controller (EnableInteractiveApproval); a headless run
	// has none, so ask resolves to "decide for yourself".
	reg.Add(agent.NewAskTool())

	var mainAgentAllowed map[string]bool
	if len(cfg.Permissions.MainAgentAllowed) > 0 {
		mainAgentAllowed = make(map[string]bool)
		// Map brief-lived memory_* renames back to canonical recall/remember/forget.
		alias := map[string]string{
			"memory_get": "recall", "memory_save": "remember", "memory_forget": "forget",
		}
		for _, name := range cfg.Permissions.MainAgentAllowed {
			if name == "read_skill" {
				continue // removed tool
			}
			mainAgentAllowed[name] = true
			if canon, ok := alias[name]; ok {
				mainAgentAllowed[canon] = true
			}
		}
	}
	var toolsDynamic map[string]bool
	if len(cfg.Permissions.ToolsDynamic) > 0 {
		toolsDynamic = make(map[string]bool)
		for _, name := range cfg.Permissions.ToolsDynamic {
			toolsDynamic[name] = true
		}
	}

	agentOpts := agent.Options{
		MaxSteps:                  maxSteps,
		Temperature:               cfg.Agent.Temperature,
		Pricing:                   entry.Price,
		Gate:                      subAgentGate,
		Hooks:                     hookRunner,
		MultiAgent:                maCtrl,
		ProjectChecks:             projectChecks,
		ContextWindow:             entry.ContextWindow,
		SoftCompactRatio:          cfg.Agent.SoftCompactRatio,
		CompactRatio:              cfg.Agent.CompactRatio,
		CompactForceRatio:         cfg.Agent.CompactForceRatio,
		ArchiveDir:                config.ArchiveDir(),
		MainAgentAllowed:          mainAgentAllowed,
		ToolsDynamic:              toolsDynamic,
		MaxMainAgentReadonlyCalls: cfg.Agent.MaxMainAgentReadonlyCalls,
	}

	// Skill tools: run_skill (invoke only) + install_skill. No read_skill.
	// Background sub-agents: Codex multi-agent V1 (spawn_agent family).
	reg.Add(skill.NewRunSkillTool(skillStore))
	reg.Add(skill.NewInstallSkillTool(skillStore, nil))
	reg.Add(installsource.NewTool(installsource.Options{
		ProjectRoot: root,
		HTTPClient:  balanceClient,
		ConnectMCP: func(e config.PluginEntry) (installsource.MCPConnectResult, error) {
			exp := e.ExpandedPlugin()
			spec := plugin.Spec{
				Name:    exp.Name,
				Type:    exp.Type,
				Command: exp.Command,
				Args:    exp.Args,
				Env:     exp.Env,
				URL:     exp.URL,
				Headers: exp.Headers,
			}
			if opts.Stderr != nil {
				spec.Stderr = opts.Stderr
			}
			tools, err := pluginHost.Add(ctx, spec)
			if err != nil {
				return installsource.MCPConnectResult{}, err
			}
			// Disconnect closes the server and drops its namespaced tools.
			// Used by the install_source rollback path when SaveTo fails.
			disconnect := func() {
				pluginHost.Remove(spec.Name)
			}
			return installsource.MCPConnectResult{
				ToolCount:  len(tools),
				Disconnect: disconnect,
			}, nil
		},
		OnDisconnect: func(serverName string) bool {
			_, ok := pluginHost.Remove(serverName)
			return ok
		},
	}))
	// Sub-agents: Codex multi-agent V1 tools (spawn_agent family).

	execSess := agent.NewSession(sysPrompt)
	executor := agent.New(execProv, reg, execSess, agentOpts, sink)

	// Custom slash commands (.reasonix/commands + user dir). Best-effort: a malformed
	// file is skipped, and a load error never blocks the session.
	cmds, _ := command.Load(config.CommandDirsForRoot(root)...)

	// Expose the loaded slash commands (skills + custom commands) to the model via
	// the slash_command tool, so it can invoke a project playbook by name the way a
	// user types "/name". Skills are added first, then commands, so a command wins
	// a name clash — matching the prompt's command-over-skill precedence.
	var slashEntries []command.SlashEntry
	for _, sk := range skills {
		sk := sk
		slashEntries = append(slashEntries, command.SlashEntry{
			Name:        sk.Name,
			Description: sk.Description,
			Render:      func(args []string) string { return skill.Render(sk, strings.Join(args, " ")) },
		})
	}
	for _, cmd := range cmds {
		cmd := cmd
		slashEntries = append(slashEntries, command.SlashEntry{
			Name:        cmd.Name,
			Description: cmd.Description,
			ArgHint:     cmd.ArgHint,
			Render:      func(args []string) string { return cmd.Render(args) },
		})
	}
	// /tools slash command: lists all registered tools with their descriptions.
	reg.Add(command.NewSlashCommandTool(slashEntries))

	var runner agent.Runner = executor
	label := entry.Model

	// Two-model collaboration: a distinct planner_model wraps the executor in a
	// Coordinator with its own session, kept separate for cache stability. The
	// planner gets the same standing memory context and a filtered read-only
	// research tool set, so it can inspect rules/code without side effects.
	if pm := cfg.Agent.PlannerModel; pm != "" {
		pe, ok := cfg.ResolveModel(pm)
		if !ok {
			return nil, fmt.Errorf("planner_model %q is not a configured provider", pm)
		}
		if pe.Model != entry.Model {
			plannerProv, err := NewProviderWithProxy(pe, proxySpec)
			if err != nil {
				return nil, fmt.Errorf("planner %q: %w", pm, err)
			}
			plannerSess := agent.NewSession(agent.PlannerPromptWithContext(mem.Block()))
			plannerTools := agent.PlannerToolRegistry(reg)
			runner = agent.NewCoordinator(plannerProv, plannerSess, pe.Price, plannerTools, agent.Options{
				MaxSteps:                  cfg.Agent.PlannerMaxSteps,
				MaxStepsKey:               "agent.planner_max_steps",
				Gate:                      nil,
				ContextWindow:             pe.ContextWindow,
				SoftCompactRatio:          cfg.Agent.SoftCompactRatio,
				CompactRatio:              cfg.Agent.CompactRatio,
				CompactForceRatio:         cfg.Agent.CompactForceRatio,
				ArchiveDir:                config.ArchiveDir(),
				MainAgentAllowed:          mainAgentAllowed,
				ToolsDynamic:              toolsDynamic,
				MaxMainAgentReadonlyCalls: cfg.Agent.MaxMainAgentReadonlyCalls,
			}, executor, cfg.Agent.Temperature, sink, nil)
			label = entry.Model + " + planner " + pe.Model
		}
	}

	ctrlOpts := control.Options{
		Runner:        runner,
		Executor:      executor,
		Sink:          sink,
		Policy:        policy,
		SubAgentGate:  subAgentGate,
		Label:         label,
		SystemPrompt:  sysPrompt,
		SessionDir:    config.SessionDir(),
		Host:          pluginHost,
		Commands:      cmds,
		Skills:        skills,
		AllSkills:     allSkills,
		SkillStore:    skillStore,
		AllSkillStore: allSkillStore,
		Hooks:         hookRunner,
		Memory:        mem,
		Cleanup:       cleanup,
		BalanceURL:    entry.BalanceURL,
		BalanceKey:    entry.APIKey(),
		BalanceClient: balanceClient,
		Registry:      reg,
		PluginCtx:     ctx,
		WorkspaceRoot: root,
		Config:        cfg,
		OnRemember: func(rule string) {
			rememberPermissionRule(opts.WorkspaceRoot, rule)
		},
	}
	return control.New(ctrlOpts), nil
}
