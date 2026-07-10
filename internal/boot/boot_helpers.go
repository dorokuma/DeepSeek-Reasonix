package boot

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/event"
	"reasonix/internal/lsp"
	"reasonix/internal/netclient"
	"reasonix/internal/plugin"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
	"reasonix/internal/tool/builtin"
)

func migrateLegacySessionSources(sink event.Sink) {
	dest := config.SessionDir()
	if strings.TrimSpace(dest) == "" {
		return
	}
	type legacySource struct {
		dir     string
		label   string
		migrate func(srcDir, destDir string) (int, error)
	}
	var sources []legacySource
	if home, herr := os.UserHomeDir(); herr == nil {
		sources = append(sources, legacySource{
			dir:     filepath.Join(home, ".reasonix", "sessions"),
			label:   "~/.reasonix/sessions",
			migrate: agent.MigrateLegacySessions,
		})
	}
	// Back-fill v0.x sessions from the current user config session directory as
	// well. This covers users whose platform config root was redirected before the
	// Go rewrite; their event logs can already live where v2 stores sessions.
	sources = append(sources, legacySource{
		dir:     dest,
		label:   dest,
		migrate: agent.MigrateLegacySessionsFromConfigDir,
	})

	seen := map[string]bool{}
	for _, src := range sources {
		if strings.TrimSpace(src.dir) == "" {
			continue
		}
		key := filepath.Clean(src.dir)
		if seen[key] {
			continue
		}
		seen[key] = true
		if n, serr := src.migrate(src.dir, dest); serr == nil && n > 0 {
			sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: fmt.Sprintf("imported %d past session(s) from %s — resume them with --resume or the history panel", n, src.label)})
		}
	}
}

func rememberPermissionRule(workspaceRoot, rule string) {
	path := rememberPermissionConfigPath(workspaceRoot)
	edit := config.LoadForEdit(path)
	if err := edit.AddPermissionRule("allow", rule); err != nil {
		slog.Warn("persist permission rule", "rule", rule, "err", err)
		return
	}
	if err := edit.SaveTo(path); err != nil {
		slog.Warn("save config after permission rule", "err", err)
	}
}

func rememberPermissionConfigPath(workspaceRoot string) string {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot != "" {
		return filepath.Join(workspaceRoot, "reasonix.toml")
	}
	path := config.SourcePath()
	if path == "" {
		path = "reasonix.toml" // match Config.Save() fallback
	}
	return path
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func subagentModelRef(cfg *config.Config, role string) string {
	if cfg != nil {
		for _, key := range subagentModelKeys(role) {
			if m := strings.TrimSpace(cfg.Agent.SubagentModels[key]); m != "" {
				return m
			}
		}
	}
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Agent.SubagentModel)
}

func subagentEffortRef(cfg *config.Config, role string) string {
	if cfg != nil {
		for _, key := range subagentModelKeys(role) {
			if e := strings.TrimSpace(cfg.Agent.SubagentEfforts[key]); e != "" {
				return e
			}
		}
	}
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Agent.SubagentEffort)
}

func subagentModelKeys(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	keys := []string{name}
	for _, alias := range []string{
		strings.ReplaceAll(name, "-", "_"),
		strings.ReplaceAll(name, "_", "-"),
	} {
		if alias == "" {
			continue
		}
		seen := false
		for _, key := range keys {
			if key == alias {
				seen = true
				break
			}
		}
		if !seen {
			keys = append(keys, alias)
		}
	}
	return keys
}

// NewProvider builds a provider.Provider from a configured entry. Exported so
// custom assemblers (e.g. the ACP per-session factory) can reuse it without
// going through the full Build.
func NewProvider(e *config.ProviderEntry) (provider.Provider, error) {
	return NewProviderWithProxy(e, netclient.ProxySpec{Mode: netclient.ModeAuto})
}

// NewProviderWithProxy builds a provider.Provider with the configured ordinary
// network proxy settings.
func NewProviderWithProxy(e *config.ProviderEntry, proxy netclient.ProxySpec) (provider.Provider, error) {
	return provider.New(e.Kind, provider.Config{
		Name:    e.Name,
		BaseURL: e.BaseURL,
		Model:   e.Model,
		APIKey:  e.APIKey(),
		// Pass the key's env var so auth failures can name where to fix it, plus
		// provider-kind-specific knobs. EffectiveEffort applies a configured
		// default_effort when the user has not explicitly selected /effort.
		Extra: map[string]any{
			"api_key_env": e.APIKeyEnv,
			"thinking":    e.Thinking,
			"effort":      config.EffectiveEffort(e),
			"proxy_spec":  proxy,
		},
	})
}

// addBuiltins adds enabled built-in tools to reg. An empty list means all of
// them. writeRoots confines the file-writing built-ins to the workspace: after
// the (unconfined) defaults are added, each enabled writer is replaced by an
// instance bound to writeRoots (preserving registry order).
// When workDir is non-empty, tools resolve relative paths against it instead of
// the process cwd, enabling concurrent multi-project sessions.
func addBuiltins(reg *tool.Registry, enabled, writeRoots []string, bashTimeout time.Duration, searchSpec builtin.SearchSpec, stderr io.Writer, workDir string) {
	// If a workspace directory is set, use workspace-bound tools that resolve
	// paths relative to that directory. Otherwise fall back to the process-cwd
	// compile-time builtins.
	if workDir != "" {
		ws := builtin.Workspace{Dir: workDir, BashTimeout: bashTimeout, Search: searchSpec}
		for _, t := range ws.Tools(enabled...) {
			reg.Add(t)
		}
		return
	}

	if len(enabled) == 0 {
		for _, t := range tool.Builtins() {
			reg.Add(t)
		}
	} else {
		for _, name := range enabled {
			if t, ok := tool.LookupBuiltin(name); ok {
				reg.Add(t)
			} else {
				fmt.Fprintf(stderr, "warning: unknown built-in tool %q\n", name)
			}
		}
	}
	// Replace the unconfined defaults with confined instances (registry order is
	// preserved on replace): file-writers bound to the workspace.
	// Only replace tools actually enabled/present.
	confined := builtin.ConfineWriters(writeRoots)
	for _, t := range confined {
		if _, ok := reg.Get(t.Name()); ok {
			reg.Add(t)
		}
	}
}

// partitionByTier splits configured plugin entries into the three startup
// buckets — eager (block boot until ready), lazy (placeholder until first
// model use), background (placeholder + start spawn now). Entries with an
// empty tier land in background; unrecognised non-empty tiers land in lazy so a
// typo never triggers unexpected background work.
func partitionByTier(entries []config.PluginEntry) (eager, lazy, bg []config.PluginEntry) {
	for _, e := range entries {
		switch e.ResolvedTier() {
		case "eager":
			eager = append(eager, e)
		case "background":
			bg = append(bg, e)
		default:
			lazy = append(lazy, e)
		}
	}
	return eager, lazy, bg
}

// PluginSpecs maps configured plugin entries to plugin.Spec, expanding ${VAR}
// references. Exported so custom assemblers can connect the config's plugins
// alongside their own (e.g. ACP's per-session MCP servers).
func PluginSpecs(entries []config.PluginEntry) []plugin.Spec {
	specs := make([]plugin.Spec, len(entries))
	for i, e := range entries {
		e = e.ExpandedPlugin() // resolve ${VAR} / ${VAR:-default} from the environment
		specs[i] = plugin.Spec{
			Name:           e.Name,
			Type:           e.Type,
			Command:        e.Command,
			Args:           e.Args,
			Env:            e.Env,
			URL:            e.URL,
			Headers:        e.Headers,
			StripRawPrefix: e.Name + "_",
		}
	}
	return specs
}

// MCPStartupNotice formats the warning shown when configured MCP servers failed
// to connect, naming the first few; ok is false when none failed.
func MCPStartupNotice(failures []plugin.Failure) (text string, ok bool) {
	if len(failures) == 0 {
		return "", false
	}
	names := make([]string, 0, min(len(failures), 3))
	for i, f := range failures {
		if i >= 3 {
			break
		}
		names = append(names, f.Name)
	}
	more := ""
	if len(failures) > len(names) {
		more = fmt.Sprintf(" (+%d more)", len(failures)-len(names))
	}
	return fmt.Sprintf("%d MCP server(s) failed to start: %s%s — run /mcp for details",
		len(failures), strings.Join(names, ", "), more), true
}

// LSPSpecs returns the language → server map: the built-in defaults overlaid with
// any user overrides. A user entry may set only the fields it wants to change;
// empty fields keep the default for that language.
func LSPSpecs(cfg config.LSPConfig) map[string]lsp.ServerSpec {
	specs := lsp.DefaultSpecs()
	for lang, s := range cfg.Servers {
		spec := specs[lang]
		if s.Command != "" {
			spec.Command = s.Command
		}
		if s.Args != nil {
			spec.Args = s.Args
		}
		if s.Env != nil {
			spec.Env = s.Env
		}
		if s.LanguageID != "" {
			spec.LanguageID = s.LanguageID
		}
		if s.Extensions != nil {
			spec.Extensions = s.Extensions
		}
		if s.InstallHint != "" {
			spec.InstallHint = s.InstallHint
		}
		if spec.LanguageID == "" {
			spec.LanguageID = lang
		}
		specs[lang] = spec
	}
	return specs
}

func providerNames(cfg *config.Config) string {
	names := make([]string, len(cfg.Providers))
	for i, p := range cfg.Providers {
		names[i] = p.Name
	}
	return strings.Join(names, "/")
}
