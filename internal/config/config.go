// Package config loads Reasonix's runtime configuration from TOML. Resolution order:
// flag > project ./reasonix.toml > user ~/.config/reasonix/config.toml > built-in defaults.
// Secrets come from the environment via api_key_env and are never stored in
// config files.
package config

import (
	"fmt"
	"log/slog"
	"sync"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"

	"reasonix/internal/netclient"
	"reasonix/internal/provider"
)

var validSkillName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// IsValidSkillName reports whether name is a usable skill identifier.
func IsValidSkillName(name string) bool { return validSkillName.MatchString(name) }

// SkillNameKey normalizes a skill identifier for config comparisons.
func SkillNameKey(name string) string {
	name = strings.TrimSpace(name)
	if !IsValidSkillName(name) {
		return ""
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(name)
	}
	return name
}

// Config is Reasonix's runtime configuration.
type Config struct {
	ConfigVersion int                 `toml:"config_version"`
	DefaultModel  string              `toml:"default_model"`
	Language      string              `toml:"language"` // ui/model language tag (e.g. "zh"); empty = auto-detect from $LANG / $REASONIX_LANG
	UI            UIConfig            `toml:"ui"`
	Notifications NotificationsConfig `toml:"notifications"`
	Agent         AgentConfig         `toml:"agent"`
	Providers     []ProviderEntry     `toml:"providers"`
	Tools         ToolsConfig         `toml:"tools"`
	Permissions   PermissionsConfig   `toml:"permissions"`
	Network       NetworkConfig       `toml:"network"`
	Plugins       []PluginEntry       `toml:"plugins"`
	Skills        SkillsConfig        `toml:"skills"`
	Statusline    StatuslineConfig    `toml:"statusline"`
	LSP           LSPConfig           `toml:"lsp"`
	Codegraph     CodegraphConfig     `toml:"codegraph"`
	Serve         ServeConfig         `toml:"serve"`
	// UsdCnyRate is the exchange rate used to convert OpenCode Go USD pricing
	// to CNY for display and cost calculation. 0 means use the built-in default
	// (7.0). Only meaningful when a provider with usd-denominated pricing (e.g.
	// OpenCode Go) is configured.
	UsdCnyRate float64 `toml:"usd_cny_rate"`
	// NativeScrollback forces native terminal scrollback mode (keeps soft
	// keyboard on touch devices connected over SSH). Equivalent to setting
	// the REASONIX_NATIVE_SCROLLBACK=1 env var or the --native-scrollback CLI
	// flag. Lower priority than both env var and CLI flag.
	NativeScrollback bool `toml:"native_scrollback"`
}

// UIConfig controls CLI presentation-only settings.
type UIConfig struct {
	Theme         string `toml:"theme"`          // auto|dark|light; empty resolves to auto
	ThemeStyle    string `toml:"theme_style"`    // graphite|ember|aurora|midnight|sandstone|porcelain|linen|glacier
	CloseBehavior string `toml:"close_behavior"` // quit|background; window close behavior
}

// NotificationsConfig controls optional system notifications for CLI chat/run.
type NotificationsConfig struct {
	Enabled         bool `toml:"enabled"`
	TurnDone        bool `toml:"turn_done"`
	ApprovalRequest bool `toml:"approval_request"`
	AskRequest      bool `toml:"ask_request"`
}

// UITheme normalizes ui.theme to a supported value.
func (c *Config) UITheme() string {
	switch strings.ToLower(strings.TrimSpace(c.UI.Theme)) {
	case "dark":
		return "dark"
	case "light":
		return "light"
	default:
		return "auto"
	}
}

// UIThemeStyle normalizes ui.theme_style. Empty means "pick the default style
// for the resolved light/dark shell".
func (c *Config) UIThemeStyle() string {
	return normalizeThemeStyle(c.UI.ThemeStyle)
}

func normalizeThemeStyle(style string) string {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "graphite", "ember", "aurora", "midnight", "sandstone", "porcelain", "linen", "glacier":
		return strings.ToLower(strings.TrimSpace(style))
	default:
		return ""
	}
}

func normalizeCloseBehavior(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "quit", "exit":
		return "quit"
	default:
		return "background"
	}
}

// ReasoningLanguage returns the model-facing reasoning language from [agent].
// Empty means no preference (model default).
func (c *Config) ReasoningLanguage() string {
	switch strings.ToLower(strings.TrimSpace(c.Agent.ReasoningLanguage)) {
	case "en":
		return "en"
	case "zh":
		return "zh"
	default:
		return ""
	}
}

// LSPConfig governs the optional Language Server Protocol tools (lsp_definition,
// lsp_references, lsp_hover, lsp_diagnostics). Enabled defaults to true; the
// servers themselves are never bundled — each resolves on PATH and the tool
// returns an install hint when it is missing, so the capability is dormant until
// the user installs a server. Servers overrides or extends the built-in language
// → server map, keyed by language id (e.g. "go", "rust", "python").
type LSPConfig struct {
	Enabled bool                 `toml:"enabled"`
	Servers map[string]LSPServer `toml:"servers"`
}

// UICloseBehavior normalizes the window close behavior. It maps to ui.close_behavior.
func (c *Config) UICloseBehavior() string {
	return normalizeCloseBehavior(c.UI.CloseBehavior)
}

// CodegraphConfig controls the built-in code intelligence MCP server.
type CodegraphConfig struct {
	Enabled bool   `toml:"enabled"`
	Path    string `toml:"path"`
	Tier    string `toml:"tier"`
}

// LSPServer overrides a built-in language's server or, when keyed by a new
// language, adds one. An empty field falls back to the built-in default for that
// language; Extensions is required when adding a language the built-ins don't
// cover (e.g. ".ex" for Elixir) so files route to it.
type LSPServer struct {
	Command     string            `toml:"command"`
	Args        []string          `toml:"args"`
	Env         map[string]string `toml:"env"`
	LanguageID  string            `toml:"language_id"`
	Extensions  []string          `toml:"extensions"`
	InstallHint string            `toml:"install_hint"`
}

// StatuslineConfig configures a custom status line. Command, when set, is run at
// startup and after each turn; its first line of stdout replaces the built-in
// status data row. A JSON payload (model, context tokens, cwd) is fed on stdin.
type StatuslineConfig struct {
	Command string `toml:"command"`
}

// ServeConfig configures the HTTP serve frontend (reasonix serve).
type ServeConfig struct {
	// AuthMode selects the authentication mode for the HTTP serve frontend.
	// "none" (default): no authentication.
	// "token": a pre-shared token in the URL query string.
	// "password": a login page with bcrypt password verification.
	AuthMode string `toml:"auth_mode"`
	// Token is a pre-shared token for auth_mode = "token". When empty, a
	// cryptographically random token is generated at startup and printed.
	Token string `toml:"token"`
	// PasswordHash is a bcrypt hash of the password for auth_mode = "password".
	// Generate one with: reasonix serve --hash-password <password>
	PasswordHash string `toml:"password_hash"`
	// BehindProxy indicates the server sits behind a trusted reverse proxy
	// (nginx, Caddy, Cloudflare, etc.) that sets X-Forwarded-For and
	// X-Forwarded-Proto headers. When true, those headers are used for
	// rate-limiting and Secure-cookie decisions. When false (default), they
	// are ignored — an attacker can otherwise forge them.
	BehindProxy bool `toml:"behind_proxy"`
}

// NetworkConfig controls ordinary outbound HTTP traffic such as model providers,
// wallet-balance lookups, and updater checks. It intentionally
// does not apply to web_fetch, which keeps its own SSRF-guarded dialer.
type NetworkConfig struct {
	// ProxyMode is "auto" (default; environment proxy for now), "env", "custom",
	// or "off". auto leaves room for OS proxy detection later without changing the
	// config shape.
	ProxyMode string `toml:"proxy_mode"`
	// ProxyURL is an advanced custom override such as "socks5://127.0.0.1:7890".
	// When set and proxy_mode = "custom", it wins over the structured proxy table.
	ProxyURL string `toml:"proxy_url"`
	// NoProxy is honored for custom proxies. Env/auto modes use NO_PROXY from the
	// process environment instead.
	NoProxy string             `toml:"no_proxy"`
	Proxy   NetworkProxyConfig `toml:"proxy"`
	// CACertPath is an optional path to a PEM-encoded CA certificate file. When
	// set, this CA is appended to the system root CAs for all outbound HTTP
	// connections (provider APIs, balance lookups, updater, etc.). web_fetch uses
	// its own SSRF-guarded transport and does NOT inherit this setting.
	CACertPath string `toml:"ca_cert_path"`
}

// NetworkProxyConfig is the structured custom-proxy editor shape. Password is
// optional and supports ${VAR} expansion, so users can avoid storing it literally.
type NetworkProxyConfig struct {
	Type     string `toml:"type"` // http|https|socks5|socks5h
	Server   string `toml:"server"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
}

// NetworkProxySpec returns the expanded proxy settings used by netclient.
func (c *Config) NetworkProxySpec() netclient.ProxySpec {
	return netclient.ProxySpec{
		Mode:        c.Network.ProxyMode,
		URL:         ExpandVars(c.Network.ProxyURL),
		NoProxy:     ExpandVars(c.Network.NoProxy),
		Type:        c.Network.Proxy.Type,
		Server:      ExpandVars(c.Network.Proxy.Server),
		Port:        c.Network.Proxy.Port,
		Username:    ExpandVars(c.Network.Proxy.Username),
		Password:    ExpandVars(c.Network.Proxy.Password),
		DirectHosts: c.directProxyHosts(),
	}
}

// directProxyHosts collects the base_url hosts of providers marked no_proxy, so
// netclient bypasses the proxy for them without knowing any provider by name.
//
// Only for an auto-detected proxy (auto/env): that proxy is typically a
// GFW-circumvention one not meant for domestic endpoints (e.g. mimo), so keep
// them direct. An explicit proxy_mode = "custom" is the user saying "route
// everything through this" — e.g. a mandatory corporate proxy — so honor it for
// every provider; a custom-proxy user who wants a host direct uses
// network.no_proxy instead (#3635).
func (c *Config) directProxyHosts() []string {
	if c.NetworkProxyMode() == netclient.ModeCustom {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range c.Providers {
		if !p.NoProxy {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(p.BaseURL))
		if err != nil {
			continue
		}
		if h := u.Hostname(); h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// NetworkProxyMode normalizes network.proxy_mode to a known value.
func (c *Config) NetworkProxyMode() string {
	return netclient.NormalizeMode(c.Network.ProxyMode)
}

// SkillsConfig configures skill discovery. Paths adds extra "custom"-scope skill
// roots — each a directory of SKILL.md / <name>.md playbooks — scanned between
// the project roots (.reasonix/.agents/.agent/.claude under the workspace) and
// the global roots. ExcludedPaths hides matching discovery roots without deleting
// folders. ~, relative paths, and ${VAR} expansion are supported. DisabledSkills
// hides named skills from the agent prompt, slash invocation, and skill tools
// while keeping them manageable.
type SkillsConfig struct {
	Paths          []string `toml:"paths"`
	ExcludedPaths  []string `toml:"excluded_paths"`
	DisabledSkills []string `toml:"disabled_skills"`
	MaxDepth       int      `toml:"max_depth"`
}

// SkillCustomPaths returns the configured custom skill roots with ${VAR}
// expanded; empty entries are dropped.
func (c *Config) SkillCustomPaths() []string {
	var out []string
	for _, p := range c.Skills.Paths {
		if p = ExpandVars(p); strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// SkillExcludedPaths returns configured skill roots that should be hidden from
// discovery, with ${VAR} expanded and empty entries dropped.
func (c *Config) SkillExcludedPaths() []string {
	var out []string
	for _, p := range c.Skills.ExcludedPaths {
		if p = ExpandVars(p); strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// SkillMaxDepth bounds nested skill discovery. Depth 3 favors bundled skill
// packs while Store keeps nested markdown safe by requiring descriptions.
func (c *Config) SkillMaxDepth() int {
	const (
		defaultDepth = 3
		maxDepth     = 5
	)
	if c == nil || c.Skills.MaxDepth == 0 {
		return defaultDepth
	}
	if c.Skills.MaxDepth < 1 {
		return 1
	}
	if c.Skills.MaxDepth > maxDepth {
		return maxDepth
	}
	return c.Skills.MaxDepth
}

// DisabledSkillNames returns valid disabled skill identifiers, preserving the
// first spelling and dropping duplicates/empty entries.
func (c *Config) DisabledSkillNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range c.Skills.DisabledSkills {
		name = strings.TrimSpace(name)
		if !IsValidSkillName(name) {
			continue
		}
		key := SkillNameKey(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
	}
	return out
}

// IsSkillDisabled reports whether name is configured as disabled.
func (c *Config) IsSkillDisabled(name string) bool {
	key := SkillNameKey(name)
	if key == "" {
		return false
	}
	for _, disabled := range c.DisabledSkillNames() {
		if SkillNameKey(disabled) == key {
			return true
		}
	}
	return false
}

// SandboxConfig bounds the blast radius of tool calls (Phase 0: file-writer
// confinement). WorkspaceRoot is the directory the built-in file writers
// (write_file / edit_file / multi_edit / move_file) may modify; empty means the current
// working directory, so writes stay inside the project by default. AllowWrite
// lists extra directories writers may also touch (e.g. a sibling repo or a temp
// dir). Both support ${VAR} / ${VAR:-default} expansion. Reads are unrestricted;
// confining `bash` is Phase 1 (OS-level sandbox).

// WriteRoots returns the directories file-writer tools may modify: the
// workspace root (defaulting to the current working directory when unset) plus
// any AllowWrite extras, with ${VAR} expanded. The roots are returned as given
// (relative or absolute); the confiner resolves them to absolute, symlink-free
// paths. The result is always non-empty, so confinement is on by default.
func (c *Config) WriteRoots() []string {
	return c.WriteRootsForRoot(".")
}

// WriteRootsForRoot returns the workspace root for file-writer confinement.
func (c *Config) WriteRootsForRoot(fallbackRoot string) []string {
	root := fallbackRoot
	if root == "" || root == "." {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		} else {
			root = "."
		}
	}
	return []string{root}
}

// BashMode returns the bash mode (sandbox disabled, always empty).
func (c *Config) BashMode() string {
	return ""
}

// AgentConfig configures the harness loop. PlannerModel is optional: when set
// to another provider's name it enables two-model collaboration, where the
// planner handles low-frequency planning in its own session (kept separate so
// each model's prompt prefix stays cache-stable). SubagentModel is the optional
// default for task sub-agents; SubagentModels overrides it per role name (usually "task").
type AgentConfig struct {
	SystemPrompt              string            `toml:"system_prompt"`
	SystemPromptFile          string            `toml:"system_prompt_file"`
	MaxSteps                  int               `toml:"max_steps"`         // tool-call rounds per turn; 0 = unlimited
	PlannerMaxSteps           int               `toml:"planner_max_steps"` // planner read-only tool-call rounds; 0 = unlimited
	Temperature               float64           `toml:"temperature"`
	PlannerModel              string            `toml:"planner_model"`
	SubagentModel             string            `toml:"subagent_model"`
	SubagentModels            map[string]string `toml:"subagent_models"`
	SubagentEffort            string            `toml:"subagent_effort"`
	SubagentEfforts           map[string]string `toml:"subagent_efforts"`
	MaxMainAgentReadonlyCalls int               `toml:"max_main_agent_readonly_calls"`
	// OutputStyle selects a persona/tone block folded into the system prompt at
	// startup (a built-in like "explanatory"/"learning"/"concise", or a custom
	// .reasonix/output-styles/<name>.md). Empty = the unmodified prompt.
	OutputStyle string `toml:"output_style"`
	// Compaction window fractions: soft = notice only, compact = trigger, force = hard ceiling.
	SoftCompactRatio  float64 `toml:"soft_compact_ratio"`
	CompactRatio      float64 `toml:"compact_ratio"`
	CompactForceRatio float64 `toml:"compact_force_ratio"`
	// EncryptSessions controls whether session files are encrypted at rest with
	// AES-256-GCM. The key is auto-generated and stored in the user config
	// directory. Default true — the decryption is transparent on load, so old
	// plaintext sessions remain readable.
	EncryptSessions bool `toml:"encrypt_sessions"`
	// ReasoningLanguage sets the language the model should use for chain-of-thought
	// reasoning (e.g. "zh" for Chinese, "en" for English). Empty = no preference.
	ReasoningLanguage string `toml:"reasoning_language"`
	// LogLevel controls the agent's diagnostic log verbosity: debug|info|warn|error.
	// Default "info" — debug logs are only visible when explicitly set.
	LogLevel string `toml:"log_level"`
}


// ProviderEntry declares a model provider instance. ContextWindow is the model's
// token budget; the harness compacts older history as a turn's prompt approaches
// it (see agent compaction). 0 disables compaction for the instance.
type ProviderEntry struct {
	Name      string   `toml:"name"`
	Kind      string   `toml:"kind"`
	BaseURL   string   `toml:"base_url"`
	Model     string   `toml:"model"`      // a single model (back-compat)
	Models    []string `toml:"models"`     // a vendor's model list (one base_url/key, many models); fallback when live fetch is unavailable
	ModelsURL string   `toml:"models_url"` // auto-fetch models from this URL on startup

	// fetchedModels holds the live model list retrieved from the provider API at
	// startup/refresh. When non-nil and non-empty, ModelList returns this instead
	// of the static Models/Model field, so the system uses the provider's current
	// SKUs for the lifetime of the process. Refresh only happens at boot or on
	// explicit doctor --json; provider additions require a restart.
	fetchedModels []string          `toml:"-" json:"-"` // live-fetched model list; non-nil = use instead of Models
	Default       string            `toml:"default"`    // default model when Models is set (else Models[0])
	APIKeyEnv     string            `toml:"api_key_env"`
	BalanceURL    string            `toml:"balance_url"` // optional; a provider-specific wallet-balance endpoint (DeepSeek: https://api.deepseek.com/user/balance). Empty = no balance readout.
	ContextWindow int               `toml:"context_window"`
	Price         *provider.Pricing `toml:"price"`
	// ModelPrices holds per-model pricing overrides. When non-nil and the
	// resolved model has an entry here, ResolveModel copies it into Price so
	// the executor uses model-specific rates rather than the shared Price.
	// Built-in defaults have no ModelPrices; OpenCode Go providers get theirs
	// auto-populated from the upstream pricing table.
	ModelPrices map[string]provider.Pricing `toml:"model_prices,omitempty"`
	// Thinking / Effort are provider-kind-specific knobs forwarded to the provider
	// via Config.Extra. The anthropic provider reads Thinking="adaptive" to enable
	// extended thinking and Effort ("low".."max") to tune depth. The
	// openai-compatible provider forwards Effort as reasoning_effort for
	// thinking-capable models; DeepSeek accepts high|max.
	// Empty = provider default.
	Thinking string `toml:"thinking"`
	Effort   string `toml:"effort"`
	// SupportedEfforts lists the /effort levels this provider/model exposes.
	// When non-empty, it overrides the built-in defaults derived from
	// Kind/BaseURL and makes /effort configurable. "auto" is the implicit
	// prefix — always accepted. DefaultEffort resolves it; omit DefaultEffort
	// (or set one outside this list) to fall back to SupportedEfforts[0].
	SupportedEfforts []string `toml:"supported_efforts"`
	// DefaultEffort is the /effort level used when the user picks "auto" or
	// has not set Effort. Ignored when SupportedEfforts is empty.
	DefaultEffort string `toml:"default_effort"`
	// NoProxy reaches this provider's base_url directly, never through the proxy.
	// For China-only endpoints a foreign-exit proxy resets the TLS handshake (#2803).
	NoProxy bool `toml:"no_proxy"`
}

// ModelList returns the models this provider exposes: the live-fetched list
// (when available), the explicit `models` list, or the single `model` as a
// one-element list (back-compat). Empty if none set.
func (e *ProviderEntry) ModelList() []string {
	if e.fetchedModels != nil {
		return e.fetchedModels
	}
	if len(e.Models) > 0 {
		return e.Models
	}
	if e.Model != "" {
		return []string{e.Model}
	}
	return nil
}

// DefaultModel returns the provider's default model: the explicit `default`, else
// the first of ModelList.
func (e *ProviderEntry) DefaultModel() string {
	if e.Default != "" {
		return e.Default
	}
	if l := e.ModelList(); len(l) > 0 {
		return l[0]
	}
	return ""
}

// HasModel reports whether m is one of the provider's models.
func (e *ProviderEntry) HasModel(m string) bool {
	for _, x := range e.ModelList() {
		if x == m {
			return true
		}
	}
	return false
}

// ToolsConfig selects which built-in tools are enabled. Empty means all of them.
type ToolsConfig struct {
	Enabled            []string     `toml:"enabled"`
	BashTimeoutSeconds *int         `toml:"bash_timeout_seconds"`
	Search             SearchConfig `toml:"search"`
}

const defaultBashTimeoutSeconds = 120

// BashTimeoutSeconds returns the foreground bash timeout in seconds. An omitted
// config keeps the historical 120s safety cap, explicit 0 disables the
// tool-local cap, and positive values set a custom cap. Negative values fall
// back to the default so a typo cannot silently remove the safety net.
func (c *Config) BashTimeoutSeconds() int {
	if c.Tools.BashTimeoutSeconds == nil || *c.Tools.BashTimeoutSeconds < 0 {
		return defaultBashTimeoutSeconds
	}
	return *c.Tools.BashTimeoutSeconds
}

// SearchConfig tunes the grep tool's engine. Engine is "auto" (default —
// ripgrep when on PATH, else native Go; RTK grep via bash or engine="rtk"),
// "rtk", "rg", or "native".
// RgPath optionally points at a specific ripgrep binary instead of a PATH lookup.
type SearchConfig struct {
	Engine string `toml:"engine"`
	RgPath string `toml:"rg_path"`
}

// PermissionsConfig declares the per-call permission policy (see
// internal/permission). Mode is the fallback decision for writer tools when no
// rule matches ("ask" | "allow" | "deny"; default "ask"); read-only tools always
// fall back to allow. Allow/Ask/Deny are rule lists of the form "ToolName" or
// "ToolName(glob)". Precedence: deny > ask > allow > fallback.
type PermissionsConfig struct {
	Mode             string   `toml:"mode"`
	Allow            []string `toml:"allow"`
	Ask              []string `toml:"ask"`
	Deny             []string `toml:"deny"`
	MainAgentAllowed []string `toml:"main_agent_allowed"`
	ToolsDynamic     []string `toml:"tools_dynamic"`
}

// PluginEntry declares an external MCP server. Type selects the transport:
// "stdio" (default) launches Command/Args/Env as a subprocess; "http"
// (a.k.a. streamable-http) and "sse" connect to a remote URL with optional
// static Headers. String fields support ${VAR} / ${VAR:-default} expansion so
// secrets (bearer tokens, keys) come from the environment, not the file. The
// fields mirror Claude Code's mcpServers spec, so entries can come from either
// reasonix.toml's [[plugins]] or a project-root .mcp.json (see loadMCPJSON).
type PluginEntry struct {
	Name    string            `toml:"name"`
	Hash    string            `toml:"hash,omitempty"` // sha256:<hex> content integrity hash for remote sources
	Type    string            `toml:"type"`           // "stdio" (default) | "http" | "sse"
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
	URL     string            `toml:"url"`
	Headers map[string]string `toml:"headers"`
	// AutoStart controls whether the server connects during session startup.
	// Nil preserves historical behavior: configured servers start automatically.
	AutoStart *bool `toml:"auto_start"`
	// Tier selects how aggressively the server is connected at boot:
	//   "eager"      — blocks startup until the handshake completes; required for
	//                  servers whose tools the system prompt depends on.
	//   "lazy"       — registers placeholder tools immediately (from on-disk
	//                  schema cache when available) and only spawns the real
	//                  subprocess on first model use. Kept for legacy configs.
	//   "background" — placeholder + spawn fired at boot but not waited on;
	//                  swap happens once the spawn finishes.
	// Empty defaults to "background" so enabled MCPs connect automatically
	// without blocking chat. Unknown non-empty values fall back to "lazy".
	Tier string `toml:"tier"`
}

func (e PluginEntry) ShouldAutoStart() bool {
	return e.AutoStart == nil || *e.AutoStart
}

// ResolvedTier returns the normalized tier ("eager"|"lazy"|"background") with
// the project default applied. Unknown values fall back to "lazy" so a typo
// never forces a slow boot.
func (e PluginEntry) ResolvedTier() string {
	return resolvedMCPTier(e.Tier)
}

func resolvedMCPTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "eager":
		return "eager"
	case "background":
		return "background"
	case "":
		return "background"
	default:
		return "lazy"
	}
}

func (c *Config) AutoStartPlugins() []PluginEntry {
	out := make([]PluginEntry, 0, len(c.Plugins))
	for _, p := range c.Plugins {
		if p.ShouldAutoStart() {
			out = append(out, p)
		}
	}
	return out
}

// DefaultSystemPrompt is used when config provides none.
const DefaultSystemPrompt = `You are Reasonix, a coding agent focused on executing code tasks.
Use the provided tools to read and write files and run shell commands.
Principles: understand the request before acting; verify with tools instead of
guessing; keep changes minimal and correct; briefly summarize what you did.
When the request leaves a real choice to the user — which approach or library,
the scope, or a consequential or ambiguous decision — call the ask tool to offer
2-4 concrete options rather than guessing or burying the question in prose. Skip
it when there's an obvious default; don't ask just to confirm.`

// LanguagePolicy is the auto fallback appended to the system prompt when no
// concrete UI language is resolved. It is static English text, so it stays part
// of the cache-stable prefix and avoids per-turn language injection.
const LanguagePolicy = `Reply in the same language the user is using in their most recent message: ` +
	`if they write in Chinese answer in Chinese, in English answer in English, and switch ` +
	`whenever they switch. Let this also guide the language you think in. Always keep code, ` +
	`identifiers, file paths, shell commands, and technical terms in their original form — never translate them.`

// VisibilityPolicy defines what the user can see in chat UIs (TUI, Telegram, SSE).
// Tool results and the system prompt exist only in the model's context — a common
// failure mode is claiming "as above" / "内容如上" after read_file without quoting
// anything in the assistant message. Static English text for cache stability.
const VisibilityPolicy = `User visibility: The user sees only your assistant messages in the chat UI — ` +
	`not tool results, not the system prompt, not memory loaded in your context, not reasoning. ` +
	`"Above" in your context is invisible to the user. Summarizing that a file or rules are ` +
	`"loaded" or listing section names is not showing content. Calling any tool does ` +
	`not display anything until you paste the text in your reply. Never say "above", "as shown", ` +
	`"都在上面", or "内容如上". When the user asks to see a file or rules, quote the full text ` +
	`in your message.`

// ToolUseEnforcementPolicy ensures every assistant turn delivers progress or a result.
// Inspired by Hermes Agent's TOOL_USE_ENFORCEMENT_GUIDANCE — the model MUST either
// invoke tools or hand off a concrete deliverable; pure status updates are forbidden.
const ToolUseEnforcementPolicy = `CRITICAL: You MUST use your tools to take action — do not describe what ` +
	`you would do without actually doing it. Every response should either (a) contain tool ` +
	`calls that make progress, or (b) deliver a final result to the user.`

// AppendSystemPolicies folds the static, cache-stable policy blocks onto the
// system prompt. Tool-use and visibility always apply. LanguagePolicy is only
// added when no concrete UI language is configured (empty language = auto).
func AppendSystemPolicies(prompt string, c *Config) string {
	base := strings.TrimSpace(prompt)
	parts := []string{base, ToolUseEnforcementPolicy, VisibilityPolicy}
	if c == nil || strings.TrimSpace(c.Language) == "" {
		parts = append(parts, LanguagePolicy)
	}
	return strings.Join(parts, "\n\n")
}

// Default returns the built-in default configuration (DeepSeek + MiMo presets).
func Default() *Config {
	return &Config{
		ConfigVersion: 2,
		DefaultModel:  "deepseek-flash",
		UI:            UIConfig{Theme: "auto"},
		Notifications: NotificationsConfig{
			Enabled:         false,
			TurnDone:        true,
			ApprovalRequest: true,
			AskRequest:      true,
		},
		Agent: AgentConfig{
			SystemPrompt: DefaultSystemPrompt,
			// 0 = no step cap: the agent loops until the model gives a final answer,
			// the user cancels, or the provider errors. Context stays bounded by
			// compaction, not by a round count. Set a positive agent.max_steps only
			// if you want a hard guard against runaway.
			MaxSteps:          0,
			PlannerMaxSteps:   12,
			LogLevel:          "info", // debug|info|warn|error; empty also means info at CLI
			SoftCompactRatio:  0.5,
			CompactRatio:      0.8,
			CompactForceRatio: 0.9,
			EncryptSessions:   false, // plain JSONL for debug; set true only if you need at-rest encrypt
		},
		// Mode "ask" with no rules keeps `reasonix run` autonomous (no TTY → ask
		// resolves to allow) while `reasonix chat` prompts before writers. Users add
		// deny/allow rules to harden or quiet specific tools.
		Permissions: PermissionsConfig{Mode: "ask"},
		// Sandbox on by default: bash is jailed (macOS), network allowed so
		// builds/downloads work. Set bash = "off" to disable. Network=true here
		// so an absent [sandbox] in a user's file keeps egress (zero value would
		// wrongly deny it).
		// LSP tools on by default, but dormant until a language server is on PATH;
		// a missing server yields an install hint rather than an error.
		LSP:        LSPConfig{Enabled: true},
		Network:    NetworkConfig{ProxyMode: netclient.ModeAuto},
		UsdCnyRate: 7.0,
		Providers: []ProviderEntry{
			{Name: "deepseek-flash", Kind: "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-flash", APIKeyEnv: "DEEPSEEK_API_KEY", BalanceURL: "https://api.deepseek.com/user/balance", ContextWindow: 1_000_000, Price: &provider.Pricing{CacheHit: 0.02, Input: 1, Output: 2, Currency: "¥"}},
			{Name: "deepseek-pro", Kind: "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-pro", APIKeyEnv: "DEEPSEEK_API_KEY", BalanceURL: "https://api.deepseek.com/user/balance", ContextWindow: 1_000_000, Price: &provider.Pricing{CacheHit: 0.025, Input: 3, Output: 6, Currency: "¥"}},
			{Name: "mimo-pro", Kind: "openai", BaseURL: "https://token-plan-cn.xiaomimimo.com/v1", Model: "mimo-v2.5-pro", APIKeyEnv: "MIMO_API_KEY", ContextWindow: 1_000_000, Price: &provider.Pricing{CacheHit: 0.025, Input: 3, Output: 6, Currency: "¥"}, NoProxy: true},
			{Name: "mimo-flash", Kind: "openai", BaseURL: "https://token-plan-cn.xiaomimimo.com/v1", Model: "mimo-v2.5", APIKeyEnv: "MIMO_API_KEY", ContextWindow: 1_000_000, Price: &provider.Pricing{CacheHit: 0.02, Input: 1, Output: 2, Currency: "¥"}, NoProxy: true},
		},
	}
}

// Load builds the configuration: defaults, then user config, then project
// config, then MCP servers from Claude Code's .mcp.json, then (lowest priority)
// the v0.x ~/.reasonix/config.json's mcpServers. A .env in the working directory
// is loaded first so api_key_env can resolve.
func Load() (*Config, error) {
	return LoadForRoot(".")
}

// LoadForRoot builds the configuration with project files resolved from root
// instead of the current working directory. When root is "" or ".", it behaves
// like Load(). This is the workspace-aware entry point: workspace panels use it so
// each project's reasonix.toml + .env + .mcp.json are resolved independently
// without changing the process cwd.
func LoadForRoot(root string) (*Config, error) {
	root = resolveRoot(root)
	loadDotEnvForRoot(root)
	cfg := Default()

	projectTOML := "reasonix.toml"
	if root != "." {
		projectTOML = filepath.Join(root, "reasonix.toml")
	}

	var tomlSources []string
	if uc := userConfigPath(); uc != "" {
		tomlSources = append(tomlSources, uc)
	}
	tomlSources = append(tomlSources, projectTOML)
	for _, path := range tomlSources {
		if _, err := os.Stat(path); err == nil {
			if err := migrateLegacyMCPTiersFile(path); err != nil {
				slog.Warn("config: legacy mcp tier migration failed", "path", path, "err", err)
			}
		}
		if err := mergeFile(cfg, path); err != nil {
			return nil, err
		}
	}
	// toml.DecodeFile replaces [[plugins]] wholesale, so cfg.Plugins now holds
	// only the last file's. Re-merge by name across all sources (later wins) so a
	// project reasonix.toml doesn't drop the global config's MCP servers.
	plugins, err := mergeTOMLPlugins(tomlSources)
	if err != nil {
		return nil, err
	}
	cfg.Plugins = plugins

	// Claude Code's .mcp.json (project root) is read last and merged into
	// [[plugins]], so a server configured for Claude works here unchanged.
	// reasonix.toml wins on a name collision (see mergeMCPJSON).
	mcpFile := mcpJSONFile
	if root != "." {
		mcpFile = filepath.Join(root, mcpJSONFile)
	}
	entries, err := loadMCPJSON(mcpFile)
	if err != nil {
		return nil, err
	}
	cfg.mergeMCPJSON(entries)

	// Lowest priority: the v0.x ~/.reasonix/config.json's mcpServers, so upgrading
	// from the TypeScript line keeps MCP servers without rewriting them. Anything
	// the v2 config or .mcp.json already declared wins on a name collision.
	cfg.mergeMCPJSON(loadLegacyMCP(legacyConfigPath()))
	normalizePluginCommandLines(cfg)
	normalizeLegacyEffort(cfg)
	normalizeLegacyMCPTiers(cfg)
	normalizeEffortConfig(cfg)
	backfillDeepSeekPro(cfg)

	return cfg, nil
}

// backfillDeepSeekPro restores deepseek-pro for configs the pre-fix setup wizard
// wrote with only deepseek-v4-flash: a keyless /models probe used to drop the Pro
// SKU, leaving users unable to switch to it. In-memory only — the user's file is
// untouched. Narrowly scoped to the official DeepSeek endpoint (which is known to
// serve pro) so a custom flash-only deployment isn't given an entry that 404s.
func backfillDeepSeekPro(c *Config) {
	const flashModel, proModel = "deepseek-v4-flash", "deepseek-v4-pro"
	var flash *ProviderEntry
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.Name == "deepseek-pro" {
			return
		}
		for _, m := range p.ModelList() {
			switch m {
			case proModel:
				return // pro already reachable
			case flashModel:
				if strings.Contains(p.BaseURL, "api.deepseek.com") {
					flash = p
				}
			}
		}
	}
	if flash == nil {
		return
	}
	for _, bp := range Default().Providers {
		if bp.Name == "deepseek-pro" {
			bp.APIKeyEnv = flash.APIKeyEnv
			c.Providers = append(c.Providers, bp)
			return
		}
	}
}

func resolveRoot(root string) string {
	if root == "" || root == "." {
		return "."
	}
	return filepath.Clean(root)
}

// normalizeLegacyEffort migrates the retired DeepSeek effort="off" (the old
// /thinking off that disabled thinking) to the provider default, so a config
// written by an older version keeps loading instead of erroring on a value the
// provider no longer accepts.
func normalizeLegacyEffort(c *Config) {
	for i := range c.Providers {
		if strings.EqualFold(strings.TrimSpace(c.Providers[i].Effort), "off") {
			c.Providers[i].Effort = ""
		}
	}
}

// mergeTOMLPlugins merges [[plugins]] across TOML sources by name (later source wins).
func mergeTOMLPlugins(paths []string) ([]PluginEntry, error) {
	var merged []PluginEntry
	index := map[string]int{}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		var f Config
		if _, err := toml.DecodeFile(path, &f); err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
		for _, p := range f.Plugins {
			p, _ = NormalizePluginCommandLine(p)
			if i, ok := index[p.Name]; ok {
				merged[i] = p
				continue
			}
			index[p.Name] = len(merged)
			merged = append(merged, p)
		}
	}
	return merged, nil
}

// LoadForEdit returns a config to seed the `reasonix setup` wizard when reconfiguring:
// the built-in defaults with the file at path (if present) decoded on top, so a
// reconfigure preserves the user's existing providers and agent settings instead
// of resetting to defaults. .env is loaded so api_key_env resolution works while
// the wizard decides which keys are still missing.
func LoadForEdit(path string) *Config {
	loadDotEnv()
	cfg := Default()
	if _, err := os.Stat(path); err == nil {
		if err := migrateLegacyMCPTiersFile(path); err != nil {
			slog.Warn("config: legacy mcp tier migration failed", "path", path, "err", err)
		}
	}
	if err := mergeFile(cfg, path); err != nil {
		slog.Warn("config: load for edit failed, using defaults", "path", path, "err", err)
	}
	normalizePluginCommandLines(cfg)
	normalizeLegacyEffort(cfg)
	normalizeLegacyMCPTiers(cfg)
	normalizeEffortConfig(cfg)
	return cfg
}

// mergeFile decodes a TOML file onto cfg if it exists. An absent file is not an error.
func mergeFile(cfg *Config, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if mode := info.Mode().Perm(); mode&0o022 != 0 {
		// Group/other writable config can be tampered with; refuse to load.
		return fmt.Errorf("config %s: permissions %o are group/other-writable; chmod 600 required", path, mode)
	} else if mode&0o044 != 0 {
		slog.Warn("config file is group/other-readable; prefer chmod 600", "path", path, "perm", fmt.Sprintf("%o", mode))
	}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	return nil
}

// normalizeLegacyMCPTiers keeps loaded legacy config files on the new product
// behavior: enabled MCP servers connect in the background by default, and the
// retired per-server startup tier is no longer a user-facing setting.
func normalizeLegacyMCPTiers(c *Config) {
	if c == nil {
		return
	}
	for i := range c.Plugins {
		c.Plugins[i].Tier = ""
	}
}

func migrateLegacyMCPTiersFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	next, changed := stripLegacyMCPTierLines(string(raw))
	if !changed {
		return nil
	}
	return os.WriteFile(path, []byte(next), info.Mode().Perm())
}

func stripLegacyMCPTierLines(raw string) (string, bool) {
	lines := strings.Split(raw, "\n")
	section := ""
	changed := false
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if header := tomlSectionHeader(line); header != "" {
			section = header
		}
		if section == "plugins" && isTOMLKeyAssignment(line, "tier") {
			changed = true
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), changed
}

func tomlSectionHeader(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return ""
	}
	if i := strings.Index(trimmed, "#"); i >= 0 {
		trimmed = strings.TrimSpace(trimmed[:i])
	}
	switch trimmed {
	case "[[plugins]]":
		return "plugins"
	default:
		return "other"
	}
}

func isTOMLKeyAssignment(line, key string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") || !strings.HasPrefix(trimmed, key) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, key))
	return strings.HasPrefix(rest, "=")
}

func userConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "reasonix", "config.toml")
}

// UserConfigPath is the user-global config file (~/.config/reasonix/config.toml),
// or "" when the user config dir can't be resolved.
func UserConfigPath() string { return userConfigPath() }

// UserCredentialsPath is the reasonix-owned global secrets file, beside
// config.toml in the user config dir (e.g. ~/.config/reasonix/credentials). It
// holds KEY=value lines loaded into the environment by loadDotEnv. The setup
// wizard writes API keys here, deliberately NOT named .env: keys never land in a
// project's own .env (which can't be selectively gitignored), never get
// committed, and resolve from any working directory. "" when the user config dir
// can't be resolved.
func UserCredentialsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "reasonix", "credentials")
}

// ArchiveDir is where compacted conversation history is archived for
// traceability (one timestamped .jsonl per compaction). Empty if the user config
// directory cannot be resolved, in which case archiving is skipped.
func ArchiveDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "reasonix", "archive")
}

// sessionDirOverride is set by SetSessionDir (e.g., from --session-dir CLI flag).
var sessionDirOverride string
var mu sync.RWMutex

// SetSessionDir overrides the default session directory. Called from CLI flag
// parsing so --session-dir on serve/run/chat takes effect before any resume
// resolution. The REASONIX_SESSION_DIR environment variable takes precedence
// over this override.
func SetSessionDir(dir string) {
	mu.Lock()
	defer mu.Unlock()
	sessionDirOverride = dir
}

// SessionDir returns the directory where chat sessions are persisted (one .jsonl
// per session). Priority:
//  1. REASONIX_SESSION_DIR environment variable
//  2. SetSessionDir override (from --session-dir CLI flag)
//  3. Default: ~/.config/reasonix/sessions/
//
// Used by reasonix chat --continue / --resume to find the recent ones. Empty
// if the user config dir can't be resolved — sessions then aren't saved.
func SessionDir() string {
	if env := os.Getenv("REASONIX_SESSION_DIR"); env != "" {
		return env
	}
	mu.RLock()
	if sessionDirOverride != "" {
		mu.RUnlock()
		return sessionDirOverride
	}
	mu.RUnlock()
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "reasonix", "sessions")
}

// CacheDir is the per-user cache root for derived/regenerable artefacts: MCP
// handshake snapshots, plugin startup-latency telemetry. Lives beside the
// existing dirs (UserConfigDir/reasonix/...) so the whole reasonix state tree
// shares one root the user can wipe in a single rm. Empty when the OS dir is
// unavailable — callers must tolerate that (caching is best-effort).
func CacheDir() string {
	if base := strings.TrimSpace(os.Getenv("REASONIX_CACHE_DIR")); base != "" {
		return base
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "reasonix", "cache")
}

// MemoryUserDir returns the reasonix user config root (…/reasonix), under which
// the user-global REASONIX.md and the per-project auto-memory store live. Empty
// when the user config dir can't be resolved, which disables user-scoped memory.
func MemoryUserDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "reasonix")
}

// ConventionDirs are the parent directories scanned for agent assets (skills,
// commands), in canonical-first order. .reasonix is ours; .agents / .agent /
// .claude let users drop in assets authored for other agent tools without moving
// files. Shared so skills (internal/skill) and commands (CommandDirs) discover
// the same set. Note: hooks are NOT scanned across these — a .claude/settings.json
// uses a different hook schema that can't be parsed as ours, so hooks stay in
// .reasonix/settings.json (see internal/hook).
var ConventionDirs = []string{".reasonix", ".agents", ".agent", ".claude"}

// conventionSubdirsAsc joins sub under each ConventionDir of base, in ascending
// priority (reverse of ConventionDirs) so the canonical .reasonix ends up the
// highest-priority entry — command.Load lets a later directory win on a clash.
func conventionSubdirsAsc(base, sub string) []string {
	out := make([]string, 0, len(ConventionDirs))
	for i := len(ConventionDirs) - 1; i >= 0; i-- {
		out = append(out, filepath.Join(base, ConventionDirs[i], sub))
	}
	return out
}

// CommandDirs returns the directories scanned for custom slash commands, lowest
// priority first, so a later (more specific) directory overrides an earlier one
// on a name clash. Order: home-dir convention dirs (~/.claude/commands … ~/.reasonix/commands),
// the legacy XDG user dir (~/.config/reasonix/commands), then the project's
// convention dirs (.claude/commands … .reasonix/commands). Scanning the .claude /
// .agents / .agent dirs lets commands authored for other agent tools (same .md +
// frontmatter format) work here unchanged.
func CommandDirs() []string {
	return CommandDirsForRoot(".")
}

// CommandDirsForRoot is like CommandDirs but resolves the project convention
// dirs under root instead of the current working directory. Global (home/XDG)
// dirs are unchanged — they are always user-scoped.
func CommandDirsForRoot(root string) []string {
	root = resolveRoot(root)
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, conventionSubdirsAsc(home, "commands")...)
	}
	if dir, err := os.UserConfigDir(); err == nil {
		dirs = append(dirs, filepath.Join(dir, "reasonix", "commands")) // legacy XDG user dir
	}
	dirs = append(dirs, conventionSubdirsAsc(root, "commands")...)
	return dirs
}

// SourcePath returns the highest-priority config file that exists, or "" if none.
func SourcePath() string {
	return SourcePathForRoot(".")
}

// SourcePathForRoot returns the highest-priority config file that exists under
// root, or "" if none. Equivalent to SourcePath() when root is ".".
func SourcePathForRoot(root string) string {
	root = resolveRoot(root)
	projectTOML := "reasonix.toml"
	if root != "." {
		projectTOML = filepath.Join(root, "reasonix.toml")
	}
	if _, err := os.Stat(projectTOML); err == nil {
		return projectTOML
	}
	if uc := userConfigPath(); uc != "" {
		if _, err := os.Stat(uc); err == nil {
			return uc
		}
	}
	return ""
}

// WriteFile writes the configuration to path as annotated TOML.
func (c *Config) WriteFile(path string) error {
	return os.WriteFile(path, []byte(RenderTOMLForScope(c, renderScopeForPath(path))), 0o600)
}

// Provider returns the named provider entry.
func (c *Config) Provider(name string) (*ProviderEntry, bool) {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i], true
		}
	}
	return nil, false
}

// ResolveModel resolves a model reference to a provider entry whose Model is the
// selected model string (a copy, so the config's lists stay intact). It accepts:
//   - "provider/model" — that exact model under that provider;
//   - a provider name   — the provider's default model;
//   - a bare model name — the (first) provider that lists it.
//
// The returned entry is ready to build a provider from (NewProvider reads .Model),
// so a single "vendor with many models" entry yields one instance per model
// without duplicating base_url/api_key_env. Single-`model` entries still resolve
// by provider name, keeping older configs working unchanged.
func (c *Config) ResolveModel(ref string) (*ProviderEntry, bool) {
	if ref == "" {
		return nil, false
	}
	// "provider/model"
	if prov, model, ok := strings.Cut(ref, "/"); ok {
		if e, found := c.Provider(prov); found && e.HasModel(model) {
			return e.resolveWithPrice(model), true
		}
	}
	// a provider name → its default model
	if e, found := c.Provider(ref); found {
		return e.resolveWithPrice(e.DefaultModel()), true
	}
	// a bare model name → the provider that lists it
	for i := range c.Providers {
		if c.Providers[i].HasModel(ref) {
			return c.Providers[i].resolveWithPrice(ref), true
		}
	}
	return nil, false
}

// resolveWithPrice returns a copy of the entry with Model set and Price
// overridden from ModelPrices when a per-model price exists for that model.
func (e ProviderEntry) resolveWithPrice(model string) *ProviderEntry {
	cp := e
	cp.Model = model
	if cp.ModelPrices != nil {
		if mp, ok := cp.ModelPrices[model]; ok {
			cp.Price = &mp
		}
	}
	return &cp
}

// APIKey resolves the entry's API key from its api_key_env.
// If {api_key_env}_FILE is set, the key is read from that file instead,
// which avoids exposing the secret in /proc/PID/environ.
func (e *ProviderEntry) APIKey() string {
	if e.APIKeyEnv == "" {
		return ""
	}
	// Prefer the _FILE variant (secret passed via temp file path).
	// Falls back to the plain env var if the file is missing or unreadable.
	if file := os.Getenv(e.APIKeyEnv + "_FILE"); file != "" {
		b, err := os.ReadFile(file)
		if err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return os.Getenv(e.APIKeyEnv)
}

// Configured reports whether the provider's api_key_env is set — the same check
// Validate enforces, so pickers can filter on it.
func (e *ProviderEntry) Configured() bool {
	return e.APIKey() != ""
}

// ResolveSystemPrompt returns the system prompt, reading system_prompt_file if set.
// The file must be a relative path resolving within the project or home directory.
func (c *Config) ResolveSystemPrompt() (string, error) {
	if c.Agent.SystemPromptFile != "" {
		path := c.Agent.SystemPromptFile
		// Reject absolute paths to prevent arbitrary file reads.
		if filepath.IsAbs(path) {
			return "", fmt.Errorf("system_prompt_file must be a relative path, got absolute: %s", path)
		}
		// Prevent path traversal outside the current directory.
		cleaned := filepath.Clean(path)
		if cleaned != path {
			return "", fmt.Errorf("system_prompt_file must not contain traversal components: %s", path)
		}
		if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/../") || strings.HasSuffix(cleaned, "/..") {
			return "", fmt.Errorf("system_prompt_file must not contain \"..\": %s", path)
		}
		// Also handle Windows-style backslash traversal.
		if strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || strings.Contains(cleaned, string(filepath.Separator)+".."+string(filepath.Separator)) || cleaned == ".." {
			return "", fmt.Errorf("system_prompt_file must not traverse upward: %s", path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("system_prompt_file: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if strings.TrimSpace(c.Agent.SystemPrompt) == "" {
		return DefaultSystemPrompt, nil
	}
	return c.Agent.SystemPrompt, nil
}

// Validate checks that the selected model's provider is usable.

// ValidatePermissionsOverlap rejects tools listed in both main_agent_allowed and tools_dynamic.
func (c *Config) ValidatePermissionsOverlap() error {
	if c == nil || len(c.Permissions.ToolsDynamic) == 0 || len(c.Permissions.MainAgentAllowed) == 0 {
		return nil
	}
	allow := make(map[string]struct{}, len(c.Permissions.MainAgentAllowed))
	for _, name := range c.Permissions.MainAgentAllowed {
		allow[name] = struct{}{}
	}
	for _, name := range c.Permissions.ToolsDynamic {
		if _, ok := allow[name]; ok {
			return fmt.Errorf("tool %q cannot be in both main_agent_allowed and tools_dynamic", name)
		}
	}
	return nil
}

func (c *Config) Validate(model string) error {
	e, ok := c.ResolveModel(model)
	if !ok {
		return fmt.Errorf("unknown model %q (configured: %s)", model, c.providerNames())
	}
	if e.Kind == "" {
		return fmt.Errorf("provider %q: kind is required", model)
	}
	if e.BaseURL == "" {
		return fmt.Errorf("provider %q: base_url is required", model)
	}
	if e.APIKey() == "" {
		return fmt.Errorf("provider %q: missing env %s", model, e.APIKeyEnv)
	}
	return nil
}

func (c *Config) providerNames() string {
	names := make([]string, len(c.Providers))
	for i, p := range c.Providers {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}

// BuildModelFetchURLs builds candidate URLs for fetching available models from a provider.
func BuildModelFetchURLs(baseURL, apiVersion string) ([]string, error) {
	root := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(root, "/v1") {
		return []string{root + "/models"}, nil
	}
	return []string{root + "/models", root + "/v1/models"}, nil
}
