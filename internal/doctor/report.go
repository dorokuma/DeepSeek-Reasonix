// Package doctor collects local, redacted diagnostics for issue reports.
package doctor

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/codegraph"
	"reasonix/internal/config"
	"reasonix/internal/netclient"
	"reasonix/internal/ctxmode"
	"reasonix/internal/rtk"
	"reasonix/internal/sandbox"
)

type Options struct {
	Version string
	Config  *config.Config
}

type Report struct {
	Version    string           `json:"version"`
	OS         string           `json:"os"`
	Arch       string           `json:"arch"`
	CWD        string           `json:"cwd,omitempty"`
	Config     ConfigReport     `json:"config"`
	Providers  []ProviderReport `json:"providers"`
	Plugins    []PluginReport   `json:"plugins,omitempty"`
	Codegraph  CodegraphReport  `json:"codegraph"`
	LSP        LSPReport        `json:"lsp"`
	Sessions   SessionsReport   `json:"sessions"`
	Sandbox    SandboxReport    `json:"sandbox"`
	RTK        RTKReport        `json:"rtk"`
	Ctx        CtxReport        `json:"ctx"`
	Network    NetworkReport    `json:"network"`
	Permission PermissionReport `json:"permission"`
	Warnings   []string         `json:"warnings,omitempty"`
}

type ConfigReport struct {
	SourcePath   string `json:"source_path,omitempty"`
	UserPath     string `json:"user_path,omitempty"`
	DefaultModel string `json:"default_model"`
}

type ProviderReport struct {
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	BaseURLHost   string   `json:"base_url_host,omitempty"`
	Model         string   `json:"model,omitempty"`
	Models        []string `json:"models,omitempty"`
	APIKeyEnv     string   `json:"api_key_env,omitempty"`
	KeyPresent    bool     `json:"key_present"`
	IsDefault     bool     `json:"is_default"`
	ContextWindow int      `json:"context_window,omitempty"`
}

type PluginReport struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	AutoStart bool   `json:"auto_start"`
	Target    string `json:"target,omitempty"`
}

type CodegraphReport struct {
	Enabled     bool   `json:"enabled"`
	AutoInstall bool   `json:"auto_install"`
	Version     string `json:"version"`
	CacheDir    string `json:"cache_dir,omitempty"`
	Resolved    bool   `json:"resolved"`
	Path        string `json:"path,omitempty"`
}

type LSPReport struct {
	Enabled bool `json:"enabled"`
	Servers int  `json:"servers"`
}

type SessionsReport struct {
	Dir   string `json:"dir,omitempty"`
	Count int    `json:"count"`
	Bytes int64  `json:"bytes"`
	Error string `json:"error,omitempty"`
}

type CtxReport struct {
	Active        bool              `json:"active"`
	Threshold     int               `json:"threshold"`
	JournalOK     bool              `json:"journal_ok"`
	CacheDirs     int               `json:"cache_dirs,omitempty"`
	OrphansPruned int               `json:"orphans_pruned,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
}

type RTKReport struct {
	Mode      string            `json:"mode"`
	Path      string            `json:"path,omitempty"`
	Version   string            `json:"version,omitempty"`
	RewriteOK bool              `json:"rewrite_ok"`
	GrepOK    bool              `json:"grep_ok"`
	PipeOK    bool              `json:"pipe_ok"`
	ReadLimit int               `json:"read_limit,omitempty"`
	Timeout   string            `json:"timeout,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Sample    string            `json:"sample,omitempty"`
	Warning   string            `json:"warning,omitempty"`
}

type SandboxReport struct {
	Bash       string   `json:"bash"`
	Network    bool     `json:"network"`
	WriteRoots []string `json:"write_roots,omitempty"`
	// Available is whether an OS sandbox actually backs an "enforce" request on
	// this host (bwrap/seatbelt present). Without it "enforce" runs unconfined —
	// e.g. always on Windows, where there is no OS sandbox.
	Available bool `json:"available"`
}

type NetworkReport struct {
	ProxyMode string `json:"proxy_mode"`
	Proxy     string `json:"proxy"`
	NoProxy   bool   `json:"no_proxy"`
}

type PermissionReport struct {
	Mode       string `json:"mode"`
	AllowRules int    `json:"allow_rules"`
	AskRules   int    `json:"ask_rules"`
	DenyRules  int    `json:"deny_rules"`
}

func Collect(opts Options) Report {
	cfg := opts.Config
	var warnings []string
	if cfg == nil {
		var err error
		cfg, err = config.Load()
		if err != nil {
			warnings = append(warnings, err.Error())
			cfg = config.Default()
		}
	}
	cwd, _ := os.Getwd()
	report := Report{
		Version: opts.Version,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		CWD:     redactHome(cwd),
		Config: ConfigReport{
			SourcePath:   redactHome(config.SourcePath()),
			UserPath:     redactHome(config.UserConfigPath()),
			DefaultModel: cfg.DefaultModel,
		},
		Codegraph: CodegraphReport{
			Enabled:     cfg.Codegraph.Enabled,
			AutoInstall: cfg.Codegraph.AutoInstall,
			Version:     codegraph.Version,
			CacheDir:    redactHome(codegraph.CacheDir()),
		},
		LSP: LSPReport{
			Enabled: cfg.LSP.Enabled,
			Servers: len(cfg.LSP.Servers),
		},
		Sessions: collectSessions(config.SessionDir()),
		Sandbox: SandboxReport{
			Bash:       cfg.BashMode(),
			Network:    cfg.Sandbox.Network,
			WriteRoots: redactHomeAll(cfg.WriteRoots()),
			Available:  sandbox.Available(),
		},
		RTK: collectRTK(),
		Ctx: collectCtx(),
		Network: NetworkReport{
			ProxyMode: cfg.NetworkProxyMode(),
			Proxy:     netclient.Summary(cfg.NetworkProxySpec()),
			NoProxy:   strings.TrimSpace(cfg.Network.NoProxy) != "",
		},
		Permission: PermissionReport{
			Mode:       cfg.Permissions.Mode,
			AllowRules: len(cfg.Permissions.Allow),
			AskRules:   len(cfg.Permissions.Ask),
			DenyRules:  len(cfg.Permissions.Deny),
		},
		Warnings: warnings,
	}
	report.Sessions.Dir = redactHome(report.Sessions.Dir)
	if p, ok := codegraph.Resolve(cfg.Codegraph.Path); ok {
		report.Codegraph.Resolved = true
		report.Codegraph.Path = redactHome(p)
	}
	for i := range cfg.Providers {
		p := cfg.Providers[i]
		models := p.ModelList()
		report.Providers = append(report.Providers, ProviderReport{
			Name:          p.Name,
			Kind:          p.Kind,
			BaseURLHost:   hostOnly(p.BaseURL),
			Model:         p.Model,
			Models:        models,
			APIKeyEnv:     p.APIKeyEnv,
			KeyPresent:    p.Configured(),
			IsDefault:     p.Name == cfg.DefaultModel,
			ContextWindow: p.ContextWindow,
		})
	}
	for _, p := range cfg.Plugins {
		transport := p.Type
		if transport == "" {
			transport = "stdio"
		}
		report.Plugins = append(report.Plugins, PluginReport{
			Name:      p.Name,
			Transport: transport,
			AutoStart: p.ShouldAutoStart(),
			Target:    pluginTarget(p),
		})
	}
	if report.RTK.Warning != "" && !report.RTK.RewriteOK {
		report.Warnings = append(report.Warnings, "rtk: "+report.RTK.Warning)
	}
	return report
}

func RenderText(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "reasonix %s doctor\n", r.Version)
	fmt.Fprintf(&b, "  system       %s/%s\n", r.OS, r.Arch)
	if r.CWD != "" {
		fmt.Fprintf(&b, "  cwd          %s\n", r.CWD)
	}
	fmt.Fprintf(&b, "  config       %s\n", valueOr(r.Config.SourcePath, "not found - using defaults"))
	fmt.Fprintf(&b, "  user config  %s\n", valueOr(r.Config.UserPath, "unavailable"))
	fmt.Fprintf(&b, "  model        %s\n", valueOr(r.Config.DefaultModel, "(none)"))

	// Warnings (e.g. a config that failed to parse and fell back to defaults) go
	// up top, not buried under the full report where they read as "all fine".
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "  warning: %s\n", w)
	}

	fmt.Fprintf(&b, "\nproviders\n")
	for _, p := range r.Providers {
		key := "missing"
		if p.KeyPresent {
			key = "present"
		}
		marker := ""
		if p.IsDefault {
			marker = " default"
		}
		fmt.Fprintf(&b, "  %-16s %-8s %-24s key:%s%s\n", p.Name, p.Kind, valueOr(p.BaseURLHost, "(no host)"), key, marker)
	}

	fmt.Fprintf(&b, "\nplugins\n")
	if len(r.Plugins) == 0 {
		fmt.Fprintf(&b, "  none configured\n")
	} else {
		for _, p := range r.Plugins {
			fmt.Fprintf(&b, "  %-16s %-8s %s\n", p.Name, p.Transport, valueOr(p.Target, "(redacted)"))
		}
	}

	resolved := "missing"
	if r.Codegraph.Resolved {
		resolved = "resolved"
	}
	fmt.Fprintf(&b, "\ncodegraph\n")
	fmt.Fprintf(&b, "  enabled      %v\n", r.Codegraph.Enabled)
	fmt.Fprintf(&b, "  auto_install %v\n", r.Codegraph.AutoInstall)
	fmt.Fprintf(&b, "  version      %s\n", r.Codegraph.Version)
	fmt.Fprintf(&b, "  resolved     %s\n", resolved)

	fmt.Fprintf(&b, "\nlsp\n")
	fmt.Fprintf(&b, "  enabled      %v\n", r.LSP.Enabled)
	fmt.Fprintf(&b, "  servers      %d configured overrides\n", r.LSP.Servers)

	fmt.Fprintf(&b, "\nsessions\n")
	fmt.Fprintf(&b, "  dir          %s\n", valueOr(r.Sessions.Dir, "unavailable"))
	fmt.Fprintf(&b, "  saved        %d\n", r.Sessions.Count)
	fmt.Fprintf(&b, "  bytes        %d\n", r.Sessions.Bytes)
	if r.Sessions.Error != "" {
		fmt.Fprintf(&b, "  warning      %s\n", r.Sessions.Error)
	}

	fmt.Fprintf(&b, "\nrtk\n")
	fmt.Fprintf(&b, "  mode         %s\n", r.RTK.Mode)
	if r.RTK.Path != "" {
		fmt.Fprintf(&b, "  path         %s\n", redactHome(r.RTK.Path))
	}
	if r.RTK.Version != "" {
		fmt.Fprintf(&b, "  version      %s\n", r.RTK.Version)
	}
	if r.RTK.RewriteOK {
		fmt.Fprintf(&b, "  rewrite      ok (%s)\n", r.RTK.Sample)
	} else if r.RTK.Warning != "" {
		fmt.Fprintf(&b, "  warning      %s\n", r.RTK.Warning)
	}
	if r.RTK.GrepOK {
		fmt.Fprintf(&b, "  grep         ok (builtin → rtk grep)\n")
	}
	if r.RTK.PipeOK {
		fmt.Fprintf(&b, "  pipe         ok (large output compaction)\n")
	}
	if r.RTK.Timeout != "" {
		fmt.Fprintf(&b, "  timeout      %s\n", r.RTK.Timeout)
	}
	if r.RTK.ReadLimit > 0 && r.RTK.ReadLimit != 2000 {
		fmt.Fprintf(&b, "  read_limit   %d lines (rtk mode)\n", r.RTK.ReadLimit)
	}
	for _, key := range []string{"REASONIX_RTK", "REASONIX_RTK_TIMEOUT", "REASONIX_RTK_READ_LIMIT", "REASONIX_RTK_LOG"} {
		if r.RTK.Env == nil {
			break
		}
		if v, ok := r.RTK.Env[key]; ok {
			fmt.Fprintf(&b, "  %-14s %s\n", key, v)
		}
	}

	fmt.Fprintf(&b, "\nctx\n")
	fmt.Fprintf(&b, "  active       %v\n", r.Ctx.Active)
	if r.Ctx.Threshold > 0 {
		fmt.Fprintf(&b, "  threshold    %d bytes\n", r.Ctx.Threshold)
	}
	if r.Ctx.JournalOK {
		fmt.Fprintf(&b, "  journal      ok (FTS5 compaction resume)\n")
	}
	if r.Ctx.CacheDirs > 0 {
		fmt.Fprintf(&b, "  cache_dirs   %d\n", r.Ctx.CacheDirs)
	}
	if r.Ctx.OrphansPruned > 0 {
		fmt.Fprintf(&b, "  pruned       %d orphan cache dirs\n", r.Ctx.OrphansPruned)
	}
	for _, key := range []string{"REASONIX_CTX", "REASONIX_CTX_THRESHOLD", "REASONIX_CTX_LOG"} {
		if r.Ctx.Env == nil {
			break
		}
		if v, ok := r.Ctx.Env[key]; ok {
			fmt.Fprintf(&b, "  %-14s %s\n", key, v)
		}
	}

	fmt.Fprintf(&b, "\nsandbox\n")
	bashLine := r.Sandbox.Bash
	if r.Sandbox.Bash == "enforce" && !r.Sandbox.Available {
		bashLine += " (inactive: no OS sandbox on this host — bash runs unconfined)"
	}
	fmt.Fprintf(&b, "  bash         %s\n", bashLine)
	fmt.Fprintf(&b, "  network      %v\n", r.Sandbox.Network)
	fmt.Fprintf(&b, "  write_roots  %s\n", strings.Join(r.Sandbox.WriteRoots, ", "))

	fmt.Fprintf(&b, "\nnetwork\n")
	fmt.Fprintf(&b, "  proxy_mode   %s\n", r.Network.ProxyMode)
	fmt.Fprintf(&b, "  proxy        %s\n", r.Network.Proxy)
	fmt.Fprintf(&b, "  no_proxy     %v\n", r.Network.NoProxy)

	fmt.Fprintf(&b, "\npermissions\n")
	fmt.Fprintf(&b, "  mode         %s\n", valueOr(r.Permission.Mode, "ask"))
	fmt.Fprintf(&b, "  rules        allow:%d ask:%d deny:%d\n", r.Permission.AllowRules, r.Permission.AskRules, r.Permission.DenyRules)
	return b.String()
}

func collectRTK() RTKReport {
	p := rtk.CollectProbe()
	return RTKReport{
		Mode:      p.Mode,
		Path:      redactHome(p.Path),
		Version:   p.Version,
		RewriteOK: p.RewriteOK,
		GrepOK:    p.GrepOK,
		PipeOK:    p.PipeOK,
		ReadLimit: p.ReadLimit,
		Timeout:   p.Timeout,
		Env:       p.Env,
		Sample:    p.Sample,
		Warning:   p.Warning,
	}
}

func collectCtx() CtxReport {
	p := ctxmode.CollectProbe()
	pruned, _ := ctxmode.PruneOrphanCache()
	return CtxReport{
		Active:        p.Active,
		Threshold:     p.Threshold,
		JournalOK:     p.JournalOK,
		CacheDirs:     ctxmode.CountCacheDirs(),
		OrphansPruned: pruned,
		Env:           p.Env,
	}
}

func collectSessions(dir string) SessionsReport {
	r := SessionsReport{Dir: dir}
	if dir == "" {
		return r
	}
	sessions, err := agent.ListSessions(dir)
	if err != nil {
		r.Error = err.Error()
	}
	r.Count = len(sessions)
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		if info, statErr := d.Info(); statErr == nil {
			r.Bytes += info.Size()
		}
		return nil
	}); err != nil && !os.IsNotExist(err) {
		r.Error = err.Error()
	}
	return r
}

func pluginTarget(p config.PluginEntry) string {
	if p.URL != "" {
		return hostOnly(p.URL)
	}
	if p.Command == "" {
		return ""
	}
	return filepath.Base(p.Command)
}

func hostOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	if port := u.Port(); port != "" {
		return u.Hostname() + ":" + port
	}
	return u.Hostname()
}

func valueOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// redactHome rewrites a path under the user's home directory to start with "~",
// so a shared diagnostics report doesn't carry the account name. Paths outside
// home are returned unchanged.
func redactHome(p string) string {
	if p == "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if sep := string(os.PathSeparator); strings.HasPrefix(p, home+sep) {
		return "~" + sep + p[len(home)+1:]
	}
	return p
}

func redactHomeAll(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = redactHome(p)
	}
	return out
}
