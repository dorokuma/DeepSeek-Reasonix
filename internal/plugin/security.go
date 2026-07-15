package plugin

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"reasonix/internal/mcpcatalog"
	"reasonix/internal/mcptrust"
	"reasonix/internal/sandbox"
	"reasonix/internal/secrets"
)

// specIdentityFingerprint resolves a secret-free identity before a server is
// trusted. For stdio this pins the real executable path and file content; for
// HTTP it normalizes the endpoint while retaining only header key names.
func specIdentityFingerprint(ctx context.Context, s Spec) (string, error) {
	transport := strings.ToLower(strings.TrimSpace(s.Type))
	if transport == "" {
		transport = "stdio"
	}
	identity := mcptrust.Identity{
		Server: s.Name, Transport: transport, ConfigSource: s.ConfigSource,
		Dir: s.Dir, Args: append([]string(nil), effectiveLaunchArgs(s)...),
		EnvKeys: sortedMapKeys(s.Env), HeaderKeys: sortedMapKeys(s.Headers),
		Network: s.ReaderSandbox.Network || s.WriterSandbox.Network,
		WriteRoots: append(append(append([]string(nil), s.ReaderSandbox.WriteRoots...),
			s.ReaderSandbox.AppContainerWriteRoots...), s.WriterSandbox.WriteRoots...),
		ReadRoots:       append(append([]string(nil), s.ReaderSandbox.ReadRoots...), s.WriterSandbox.ReadRoots...),
		IsolationPolicy: isolationPolicy(s),
		PackageDigest:   s.PackageDigest,
		LauncherDigest:  s.LauncherDigest,
	}
	if strings.TrimSpace(s.OfficialCatalogEntryID) != "" {
		// Official trust is global across workspaces. The signed package digest
		// pins executable code and the catalog pins the server definition, so
		// workspace-expanded args and write-root paths are excluded here.
		identity.Args = nil
		identity.Dir = ""
		identity.WriteRoots = nil
		identity.ConfigSource = "official_catalog:" + s.OfficialCatalogEntryID
	}
	switch transport {
	case "stdio":
		if strings.TrimSpace(s.Command) == "" {
			return "", fmt.Errorf("stdio plugin %q: command is required", s.Name)
		}
		env := mergeEnv(secrets.ProcessEnv(), s.Env)
		exe, _, err := resolveStdioExecutable(ctx, s, env)
		if err != nil {
			return "", err
		}
		if abs, err := filepath.Abs(exe); err == nil {
			exe = abs
		}
		identity.CommandPath = exe
		identity.CommandSHA256, err = mcptrust.FileSHA256(exe)
		if err != nil {
			return "", fmt.Errorf("hash MCP executable %q: %w", exe, err)
		}
	case "http", "streamable-http", "streamable_http":
		identity.Transport = "http"
		identity.URL = normalizeIdentityURL(s.URL)
	default:
		identity.URL = normalizeIdentityURL(s.URL)
	}
	return mcptrust.IdentityFingerprint(identity)
}

// MCPStateDir returns a stable, server-scoped host directory outside the
// workspace. MCP sandboxes permit writes only here in the reader lane.
func MCPStateDir(reasonixHome, workspace, server string) string {
	if strings.TrimSpace(reasonixHome) == "" {
		return ""
	}
	workspaceID := mcptrust.WorkspaceFingerprint(workspace)
	if len(workspaceID) > 16 {
		workspaceID = workspaceID[:16]
	}
	if workspaceID == "" {
		workspaceID = "global"
	}
	return filepath.Join(reasonixHome, "mcp-state", workspaceID, normalizeName(server))
}

func isolationPolicy(s Spec) string {
	transport := strings.ToLower(strings.TrimSpace(s.Type))
	if transport == "http" || transport == "streamable-http" || transport == "streamable_http" {
		return string(mcptrust.IsolationNotApplicable)
	}
	if !s.ReaderSandbox.Enforce() && !s.WriterSandbox.Enforce() {
		return "off"
	}
	if sandbox.Available() {
		return string(mcptrust.IsolationEnforced)
	}
	return string(mcptrust.IsolationUnavailableUnconfined)
}

func isolationStateForSpec(s Spec) mcptrust.IsolationState {
	switch isolationPolicy(s) {
	case string(mcptrust.IsolationEnforced):
		return mcptrust.IsolationEnforced
	case string(mcptrust.IsolationUnavailableUnconfined):
		return mcptrust.IsolationUnavailableUnconfined
	default:
		return mcptrust.IsolationNotApplicable
	}
}

func normalizeIdentityURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	u.Host = host
	u.Fragment = ""
	return u.String()
}

func validatePersistentTransportTrust(s Spec) error {
	transport := strings.ToLower(strings.TrimSpace(s.Type))
	switch transport {
	case "http", "streamable-http", "streamable_http", "sse":
		u, err := url.Parse(strings.TrimSpace(s.URL))
		if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
			return fmt.Errorf("persistent trust for remote MCP server %q requires an HTTPS URL; use session trust for this connection", s.Name)
		}
	}
	return nil
}

func sortedMapKeys[V any](values map[string]V) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		if key = strings.TrimSpace(key); key != "" {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func capabilityOf(s Spec, raw mcpTool, schema []byte) mcptrust.Capability {
	visible := raw.Name
	if s.StripRawPrefix != "" {
		visible = strings.TrimPrefix(visible, s.StripRawPrefix)
	}
	hinted := raw.Annotations != nil && raw.Annotations.ReadOnlyHint
	destructive := raw.Annotations != nil && raw.Annotations.DestructiveHint
	return mcptrust.Capability{
		RawName: raw.Name, ModelName: toolName(s.Name, visible),
		InputSchema: schema, OutputSchema: raw.OutputSchema,
		ReadOnly:    hinted || s.toolReadOnlyOverride(raw.Name, visible),
		Destructive: destructive,
	}
}

func evaluateSpecTrust(s Spec, identity string, capabilities []mcptrust.Capability) (mcptrust.Evaluation, error) {
	manager := s.TrustManager
	if manager == nil {
		trusted := map[string]bool{}
		for _, cap := range capabilities {
			// Direct plugin-package users without a composition-root manager keep
			// the historical annotation behavior. Every product frontend injects
			// a manager, where third-party hints require a receipt.
			if cap.ReadOnly {
				trusted[cap.RawName] = true
			}
		}
		return mcptrust.Evaluation{State: mcptrust.TrustUntrusted, TrustedReaders: trusted}, nil
	}
	configSource := trustConfigSource(s)
	if s.OfficialCatalogEntryID != "" && mcpcatalog.RuntimeEntryRevoked(s.OfficialCatalogEntryID) {
		return untrustedEvaluation(), nil
	}
	if s.OfficialCatalogEntryID != "" {
		denied, err := manager.OfficialDenied(s.OfficialCatalogEntryID)
		if err != nil {
			return untrustedEvaluation(), err
		}
		if denied {
			return untrustedEvaluation(), nil
		}
	}
	hasReceipt, err := manager.HasReceipt(s.Name, configSource)
	if err != nil {
		return untrustedEvaluation(), err
	}
	if !hasReceipt {
		if s.OfficialCatalogEntryID != "" {
			if err := manager.TrustOfficial(s.Name, configSource, identity, s.OfficialCatalogEntryID, capabilities, s.OfficialReaderNames); err != nil {
				return untrustedEvaluation(), err
			}
			return manager.Evaluate(s.Name, configSource, identity, capabilities)
		}
		legacyConfigured := len(s.ReadOnlyToolNames) > 0 || len(s.ReadOnlyModelToolNames) > 0
		legacyImported, err := manager.LegacyImported(s.Name, configSource)
		if err != nil {
			return untrustedEvaluation(), err
		}
		if legacyConfigured && !legacyImported {
			legacy := make([]mcptrust.Capability, 0)
			for _, cap := range capabilities {
				visible := strings.TrimPrefix(cap.ModelName, ToolPrefix(s.Name))
				if s.toolReadOnlyOverride(cap.RawName, visible) && cap.ReadOnly && !cap.Destructive {
					legacy = append(legacy, cap)
				}
			}
			if len(legacy) > 0 {
				if err := manager.Trust(mcptrust.ScopeWorkspace, mcptrust.SourceLegacyImport, s.Name, configSource, identity, "", legacy); err != nil {
					return untrustedEvaluation(), err
				}
			}
			if err := manager.MarkLegacyImported(s.Name, configSource); err != nil {
				return untrustedEvaluation(), err
			}
		}
	}
	eval, err := manager.Evaluate(s.Name, configSource, identity, capabilities)
	if err != nil {
		return untrustedEvaluation(), err
	}
	return eval, nil
}

func trustConfigSource(s Spec) string {
	if s.OfficialCatalogEntryID != "" {
		return "official_catalog:" + s.OfficialCatalogEntryID
	}
	return s.ConfigSource
}

func capabilityFingerprint(cap mcptrust.Capability) string {
	fp, _ := mcptrust.CapabilityFingerprint(cap)
	return fp
}

// CachedToolTrust is the host-local safety classification for one tool in an
// identity-matched schema cache. It is used by strict read-only sessions to
// decide whether an unconnected MCP reader may start without granting trust or
// invoking the server during resolution.
type CachedToolTrust struct {
	TrustedReader         bool
	Destructive           bool
	CapabilityFingerprint string
	TrustState            mcptrust.TrustState
}

// CachedToolTrustForSpec evaluates an already-established receipt against the
// exact cached server identity and capability snapshot. It never creates or
// upgrades trust: first trust and drift review remain parent-session actions.
func CachedToolTrustForSpec(ctx context.Context, s Spec, rawName string) (CachedToolTrust, bool, error) {
	cs, ok := LoadCachedSchema(s.Name, SpecFingerprint(s))
	if !ok {
		return CachedToolTrust{}, false, nil
	}
	caps := make([]mcptrust.Capability, 0, len(cs.Tools))
	var target *mcptrust.Capability
	for _, cached := range cs.Tools {
		visible := cached.Name
		if s.StripRawPrefix != "" {
			visible = strings.TrimPrefix(visible, s.StripRawPrefix)
		}
		cap := mcptrust.Capability{
			RawName: cached.Name, ModelName: toolName(s.Name, visible),
			InputSchema: cached.Schema, OutputSchema: cached.OutputSchema,
			ReadOnly:    cached.ReadOnly || s.toolReadOnlyOverride(cached.Name, visible),
			Destructive: cached.Destructive,
		}
		caps = append(caps, cap)
		if cached.Name == rawName {
			copy := cap
			target = &copy
		}
	}
	if target == nil {
		return CachedToolTrust{}, false, nil
	}
	result := CachedToolTrust{
		Destructive:           target.Destructive,
		CapabilityFingerprint: capabilityFingerprint(*target),
		TrustState:            mcptrust.TrustUntrusted,
	}
	if s.TrustManager == nil {
		result.TrustedReader = target.ReadOnly && !target.Destructive
		return result, true, nil
	}
	if s.OfficialCatalogEntryID != "" && mcpcatalog.RuntimeEntryRevoked(s.OfficialCatalogEntryID) {
		return result, true, nil
	}
	if s.OfficialCatalogEntryID != "" {
		denied, err := s.TrustManager.OfficialDenied(s.OfficialCatalogEntryID)
		if err != nil {
			return result, true, err
		}
		if denied {
			return result, true, nil
		}
	}
	hasReceipt, err := s.TrustManager.HasReceipt(s.Name, trustConfigSource(s))
	if err != nil {
		return result, true, err
	}
	if !hasReceipt {
		return result, true, nil
	}
	locked, err := applyStoredLauncherLock(s)
	if err != nil {
		return result, true, err
	}
	identity, err := specIdentityFingerprint(ctx, locked)
	if err != nil {
		return result, true, err
	}
	eval, err := s.TrustManager.Evaluate(s.Name, trustConfigSource(s), identity, caps)
	if err != nil {
		return result, true, err
	}
	result.TrustState = eval.State
	result.TrustedReader = eval.TrustedReaders[target.RawName] && !target.Destructive
	return result, true, nil
}
