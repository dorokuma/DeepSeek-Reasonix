// Package netclient builds HTTP clients that share Reasonix's user-facing proxy
// settings. It is intentionally not used by web_fetch, whose dial-time SSRF guard
// has a different security boundary from ordinary provider/update traffic.
package netclient

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpproxy"

	"reasonix/internal/netutil"
	"reasonix/internal/sysproxy"
)

const (
	ModeAuto   = "auto"
	ModeEnv    = "env"
	ModeCustom = "custom"
	ModeOff    = "off"
)

// globalCACerts is set once by SetGlobalCACerts and read by NewTransport.
// Guarded by globalCACertsOnce so test/edge re-initialization panics early.
var (
	globalCACerts     *x509.CertPool
	globalCACertsOnce sync.Once
)

// SetGlobalCACerts sets the global CA certificate pool used by all HTTP
// clients. It panics if called more than once (catches accidental re-init).
// Called during boot; passing nil is safe (uses system pool).
func SetGlobalCACerts(pool *x509.CertPool) {
	globalCACertsOnce.Do(func() {
		globalCACerts = pool
	})
	if globalCACerts != pool {
		panic("netclient: SetGlobalCACerts called more than once")
	}
}

// GlobalCACerts returns the global CA certificate pool, or nil if not set.
func GlobalCACerts() *x509.CertPool { return globalCACerts }

// ProxySpec is the resolved proxy configuration used by network clients. URL is
// an advanced override; otherwise Type/Server/Port/Credentials are composed into a
// proxy URL. NoProxy is honored for custom proxies. DirectHosts always bypass the
// proxy in every mode (the caller derives them, e.g. from no_proxy providers).
type ProxySpec struct {
	Mode        string
	URL         string
	NoProxy     string
	Type        string
	Server      string
	Port        int
	Username    string
	Password    string
	DirectHosts []string
}

// TransportOptions lets callers keep their existing network timeouts while
// sharing proxy behavior.
type TransportOptions struct {
	DialTimeout           time.Duration
	KeepAlive             time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	// RootCAs is an optional cert pool for TLS connections. When nil, the system
	// pool is used (default). Set via network.ca_cert_path in config.
	RootCAs *x509.CertPool
	// InsecureSkipVerify, when true, disables TLS certificate verification.
	// Use only for testing with self-signed certs.
	InsecureSkipVerify bool
}

// NormalizeMode maps empty and unknown modes to auto, preserving a fail-open
// default for older configs.
func NormalizeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ModeEnv:
		return ModeEnv
	case ModeCustom:
		return ModeCustom
	case ModeOff:
		return ModeOff
	default:
		return ModeAuto
	}
}

// Validate reports whether spec can be used. Non-custom modes have no required
// fields; custom needs either a complete URL or a structured server+port.
func Validate(spec ProxySpec) error {
	_, err := proxyFunc(spec)
	return err
}

// NewHTTPClient returns an HTTP client with Reasonix proxy settings applied.
func NewHTTPClient(spec ProxySpec, opts TransportOptions) (*http.Client, error) {
	tr, err := NewTransport(spec, opts)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Transport:     tr,
		CheckRedirect: safeRedirect(),
	}, nil
}

// NewTransport clones net/http's default transport and overlays the requested
// proxy and timeout knobs. Cloning preserves defaults such as HTTP/2 support,
// connection pooling, and environment-proxy behavior for auto/env modes. When
// opts.RootCAs is set, it is used instead of the system pool; when
// opts.InsecureSkipVerify is true, verification is disabled.
func NewTransport(spec ProxySpec, opts TransportOptions) (*http.Transport, error) {
	tr := defaultTransport()
	proxy, err := proxyFunc(spec)
	if err != nil {
		return nil, err
	}
	tr.Proxy = proxy
	if opts.DialTimeout != 0 || opts.KeepAlive != 0 {
		d := &net.Dialer{Timeout: opts.DialTimeout, KeepAlive: opts.KeepAlive}
		tr.DialContext = d.DialContext
	}
	if opts.TLSHandshakeTimeout != 0 {
		tr.TLSHandshakeTimeout = opts.TLSHandshakeTimeout
	}
	if opts.ResponseHeaderTimeout != 0 {
		tr.ResponseHeaderTimeout = opts.ResponseHeaderTimeout
	}
	if opts.RootCAs != nil || opts.InsecureSkipVerify {
		if tr.TLSClientConfig == nil {
			tr.TLSClientConfig = &tls.Config{}
		}
		tr.TLSClientConfig.RootCAs = opts.RootCAs
		tr.TLSClientConfig.InsecureSkipVerify = opts.InsecureSkipVerify
		if opts.InsecureSkipVerify {
			// Loud warning: skipping TLS verification is for local/self-signed tests only.
			slog.Warn("netclient: InsecureSkipVerify=true — TLS certificate verification is disabled")
		}
	} else if globalCACerts != nil {
		// Use the globally configured CA pool (from network.ca_cert_path).
		if tr.TLSClientConfig == nil {
			tr.TLSClientConfig = &tls.Config{}
		}
		tr.TLSClientConfig.RootCAs = globalCACerts
	}
	return tr, nil
}

// safeRedirect returns a CheckRedirect function that limits redirects to 5 hops
// and rejects redirects to private/internal IP addresses (SSRF protection).
func safeRedirect() func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects (max 5)")
		}
		// Reject redirects to private IP ranges. DNS resolution failure
		// is treated as unsafe (fail-closed) to prevent DNS rebinding.
		host := req.URL.Hostname()
		ips, err := net.DefaultResolver.LookupNetIP(req.Context(), "ip", host)
		if err != nil {
			return fmt.Errorf("redirect to %s: DNS resolution failed (SSRF protection): %w", host, err)
		}
		for _, ip := range ips {
			if isPrivateIP(ip) {
				return fmt.Errorf("redirect to private IP %s rejected (SSRF protection)", ip)
			}
		}
		return nil
	}
}

// isPrivateIP reports whether ip is in a private, link-local, CGNAT, or
// unspecified range — all addresses an SSRF redirect must not reach.
func isPrivateIP(ip netip.Addr) bool {
	return ip.IsPrivate() ||
		ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		func() bool {
			if !ip.Is4() {
				return false
			}
			a := ip.As4()
			return netutil.CGNATRange.Contains(a[:])
		}()
}

// Summary returns a redacted, user-facing description for diagnostics.
func Summary(spec ProxySpec) string {
	switch NormalizeMode(spec.Mode) {
	case ModeOff:
		return "off (direct)"
	case ModeEnv:
		return "env"
	case ModeCustom:
		u, err := customProxyURL(spec)
		if err != nil {
			return "custom (invalid)"
		}
		return "custom (" + redactURL(u) + ")"
	default:
		return "auto (env)"
	}
}

func defaultTransport() *http.Transport {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		return base.Clone()
	}
	return &http.Transport{Proxy: http.ProxyFromEnvironment}
}

func proxyFunc(spec ProxySpec) (func(*http.Request) (*url.URL, error), error) {
	base, err := baseProxyFunc(spec)
	if err != nil {
		return nil, err
	}
	return withDirectHosts(base, spec.DirectHosts), nil
}

func baseProxyFunc(spec ProxySpec) (func(*http.Request) (*url.URL, error), error) {
	switch NormalizeMode(spec.Mode) {
	case ModeOff:
		return nil, nil
	case ModeCustom:
		u, err := customProxyURL(spec)
		if err != nil {
			return nil, err
		}
		cfg := &httpproxy.Config{
			HTTPProxy:  u.String(),
			HTTPSProxy: u.String(),
			NoProxy:    strings.TrimSpace(spec.NoProxy),
		}
		pf := cfg.ProxyFunc()
		return func(req *http.Request) (*url.URL, error) { return pf(req.URL) }, nil
	case ModeEnv:
		return environmentProxyFunc(), nil
	default:
		return autoProxyFunc(), nil
	}
}

// withDirectHosts makes the listed hosts (and their subdomains) bypass the proxy
// in every mode. The caller decides which hosts are direct — netclient stays
// provider-agnostic. A China-only endpoint reached through a foreign-exit proxy
// resets the TLS handshake (SSL_ERROR_SYSCALL, #2803), so its provider marks it.
func withDirectHosts(pf func(*http.Request) (*url.URL, error), hosts []string) func(*http.Request) (*url.URL, error) {
	if pf == nil || len(hosts) == 0 {
		return pf
	}
	norm := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			norm = append(norm, h)
		}
	}
	return func(req *http.Request) (*url.URL, error) {
		host := strings.ToLower(req.URL.Hostname())
		for _, h := range norm {
			if host == h || strings.HasSuffix(host, "."+h) {
				return nil, nil
			}
		}
		return pf(req)
	}
}

func environmentProxyFunc() func(*http.Request) (*url.URL, error) {
	cfg := httpproxy.FromEnvironment()
	pf := cfg.ProxyFunc()
	return func(req *http.Request) (*url.URL, error) { return pf(req.URL) }
}

// autoProxyFunc honors environment proxy vars first, then falls back to the OS
// system proxy (Windows IE/PAC/WPAD) so corporate Windows machines work without
// any manual HTTP_PROXY setup. Non-Windows resolves to env-only.
func autoProxyFunc() func(*http.Request) (*url.URL, error) {
	pf := httpproxy.FromEnvironment().ProxyFunc()
	return func(req *http.Request) (*url.URL, error) {
		if u, err := pf(req.URL); err != nil || u != nil {
			return u, err
		}
		return sysproxy.ForURL(req.URL)
	}
}

func customProxyURL(spec ProxySpec) (*url.URL, error) {
	if raw := strings.TrimSpace(spec.URL); raw != "" {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("network proxy_url: %w", err)
		}
		if err := validateProxyURL(u); err != nil {
			return nil, err
		}
		return u, nil
	}
	typ := strings.ToLower(strings.TrimSpace(spec.Type))
	if typ == "" {
		typ = "http"
	}
	switch typ {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("network proxy type %q: must be http|https|socks5|socks5h", spec.Type)
	}
	server := strings.TrimSpace(spec.Server)
	if server == "" {
		return nil, fmt.Errorf("network proxy server is required when proxy_mode = custom")
	}
	if spec.Port <= 0 || spec.Port > 65535 {
		return nil, fmt.Errorf("network proxy port must be 1..65535")
	}
	u := &url.URL{Scheme: typ, Host: net.JoinHostPort(server, strconv.Itoa(spec.Port))}
	if spec.Username != "" {
		if spec.Password != "" {
			u.User = url.UserPassword(spec.Username, spec.Password)
		} else {
			u.User = url.User(spec.Username)
		}
	}
	return u, nil
}

func validateProxyURL(u *url.URL) error {
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("network proxy_url scheme %q: must be http|https|socks5|socks5h", u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("network proxy_url host is required")
	}
	return nil
}

func redactURL(u *url.URL) string {
	cp := *u
	if cp.User != nil {
		if name := cp.User.Username(); name != "" {
			cp.User = url.UserPassword(name, "***")
		} else {
			cp.User = nil
		}
	}
	return cp.String()
}

// LoadCACert reads a PEM-encoded CA certificate file and returns a
// x509.CertPool containing it alongside the system root CAs. Returns nil when
// path is empty (no error), so callers can pass it unconditionally.
func LoadCACert(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load CA cert %s: %w", path, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		slog.Warn("netclient: system cert pool unavailable, using only user CA", "err", err)
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("load CA cert %s: no valid PEM block found", path)
	}
	return pool, nil
}
