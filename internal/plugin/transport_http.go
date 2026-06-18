package plugin

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"reasonix/internal/netclient"
	"reasonix/internal/netutil"
)

// maxHTTPBody caps how much of a JSON / SSE response body we read, so a
// misbehaving server can't make us buffer without bound.
const maxHTTPBody = 16 << 20 // 16 MiB

// httpTransport speaks MCP's Streamable HTTP transport: every JSON-RPC message
// is an HTTP POST to the server URL. The server replies with either
// application/json (one response) or text/event-stream (an SSE stream carrying
// the response plus any server notifications). The Mcp-Session-Id header, once
// the server assigns one, is echoed on every subsequent request.
//
// The mutex serialises a request and its response. That means concurrent tool
// calls to the *same* server run one at a time; calls to different servers use
// different transports and stay concurrent. Correctness over latency for P1 —
// it also keeps nextID and the session id race-free.
type httpTransport struct {
	name    string
	url     string
	headers map[string]string
	client  *http.Client

	mu      sync.Mutex
	nextID  int
	session string // Mcp-Session-Id, captured from responses

	done chan struct{} // closed in close()
}

func newHTTPTransport(s Spec) (*httpTransport, error) {
	if s.URL == "" {
		return nil, fmt.Errorf("http plugin %q: url is required", s.Name)
	}
	base := &http.Transport{
		DialContext: ssrfGuardDial,
	}
	// Apply global CA pool when configured; nil leaves base.TLSClientConfig
	// unset so the standard library uses the system root pool.
	if caPool := netclient.GlobalCACerts(); caPool != nil {
		base.TLSClientConfig = &tls.Config{RootCAs: caPool}
	}
	return &httpTransport{
		name:    s.Name,
		url:     s.URL,
		headers: s.Headers,
		client:  &http.Client{Transport: base, Timeout: 60 * time.Second},
		done:    make(chan struct{}),
	}, nil
}

// ssrfGuardDial is a net.Dialer-style function that blocks connections to
// private, link-local, CGNAT, and unspecified addresses — the SSRF surface an
// MCP server pointing at a cloud metadata service or internal network would
// target. Loopback is allowed: the plugin host already communicates over
// localhost for stdio-based plugins, and the user explicitly configured this
// URL. The check runs at dial time on the resolved IP, so a hostname that
// DNS-rebinds to an internal address is caught.
func ssrfGuardDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if blockedMCPIP(ip.IP) {
			return nil, fmt.Errorf("refusing to connect to internal address %s (resolves to %s)", host, ip.IP)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses resolved for %s", host)
	}
	// Dial the vetted IP, not the hostname, so the connection can't re-resolve
	// to a different (internal) address (DNS rebinding).
	var d net.Dialer
	return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

// blockedMCPIP reports whether ip is an address the MCP HTTP transport must
// not connect to. Loopback is allowed (user-configured MCP servers often run
// locally).
func blockedMCPIP(ip net.IP) bool {
	if ip.IsLoopback() {
		return false
	}
	return netutil.BlockedIP(ip)
}

func (t *httpTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.nextID++
	id := t.nextID
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}

	resp, err := t.do(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("plugin %q: %s: %w", t.name, method, err)
	}
	defer resp.Body.Close()
	t.captureSession(resp)

	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(b))
		if isHTTPSessionExpiredResponse(resp.StatusCode, b) {
			t.session = ""
			return nil, fmt.Errorf("plugin %q: %s: %w", t.name, method, &httpSessionExpiredError{
				status: resp.StatusCode,
				body:   msg,
			})
		}
		return nil, fmt.Errorf("plugin %q: %s: http %d: %s", t.name, method, resp.StatusCode, msg)
	}

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return t.readSSEResponse(resp.Body, id)
	}
	return decodeRPCResult(resp.Body, t.name)
}

func (t *httpTransport) notify(ctx context.Context, method string, params any) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	resp, err := t.do(ctx, body)
	if err != nil {
		return fmt.Errorf("plugin %q: %s: %w", t.name, method, err)
	}
	defer resp.Body.Close()
	t.captureSession(resp)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxHTTPBody))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("plugin %q: %s: http %d", t.name, method, resp.StatusCode)
	}
	return nil
}

func (t *httpTransport) Done() <-chan struct{} { return t.done }

func (t *httpTransport) close() {
	t.client.CloseIdleConnections()
	close(t.done)
}

// do POSTs one JSON-RPC body with the standard MCP headers, the configured
// static headers, and the session id (once known). Caller holds t.mu.
func (t *httpTransport) do(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	if t.session != "" {
		req.Header.Set("Mcp-Session-Id", t.session)
	}
	return t.client.Do(req)
}

func (t *httpTransport) captureSession(resp *http.Response) {
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.session = sid
	}
}

type httpSessionExpiredError struct {
	status int
	body   string
}

func (e *httpSessionExpiredError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("http %d: MCP session expired", e.status)
	}
	return fmt.Sprintf("http %d: %s", e.status, e.body)
}

func isHTTPSessionExpiredResponse(status int, body []byte) bool {
	if status != http.StatusNotFound {
		return false
	}
	var resp rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(body), &resp); err != nil || resp.Error == nil {
		return false
	}
	return resp.Error.Code == -32001 && strings.Contains(strings.ToLower(resp.Error.Message), "session not found")
}

// readSSEResponse scans an SSE stream for the JSON-RPC response matching id,
// skipping server notifications and any other-id messages. Per the SSE spec,
// consecutive data: lines within one event are joined with "\n" and an event is
// dispatched on the blank line that terminates it.
func (t *httpTransport) readSSEResponse(body io.Reader, id int) (json.RawMessage, error) {
	sc := bufio.NewScanner(io.LimitReader(body, maxHTTPBody))
	sc.Buffer(make([]byte, 0, 64*1024), maxHTTPBody)

	var data strings.Builder
	// match reports whether the accumulated event data is our response; it
	// returns (result, matched, error).
	match := func() (json.RawMessage, bool, error) {
		if data.Len() == 0 {
			return nil, false, nil
		}
		payload := data.String()
		data.Reset()
		var resp rpcResponse
		if err := json.Unmarshal([]byte(payload), &resp); err != nil {
			return nil, false, nil // not a JSON-RPC message we care about
		}
		if resp.ID != id {
			return nil, false, nil // a notification or another call's response
		}
		if resp.Error != nil {
			return nil, false, fmt.Errorf("plugin %q: %w", t.name, resp.Error)
		}
		return resp.Result, true, nil
	}

	for sc.Scan() {
		line := sc.Text()
		if line == "" { // event boundary
			if res, ok, err := match(); err != nil || ok {
				return res, err
			}
			continue
		}
		if v, found := strings.CutPrefix(line, "data:"); found {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(v, " "))
		}
		// event:, id:, retry: and comments (":") are ignored
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("plugin %q: read SSE: %w", t.name, err)
	}
	if res, ok, err := match(); err != nil || ok { // stream ended on a final unterminated event
		return res, err
	}
	return nil, fmt.Errorf("plugin %q: SSE stream ended without a response to id %d", t.name, id)
}

// decodeRPCResult parses a single application/json JSON-RPC response body.
func decodeRPCResult(body io.Reader, name string) (json.RawMessage, error) {
	b, err := io.ReadAll(io.LimitReader(body, maxHTTPBody))
	if err != nil {
		return nil, fmt.Errorf("plugin %q: read response: %w", name, err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(b), &resp); err != nil {
		return nil, fmt.Errorf("plugin %q: decode response: %w", name, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("plugin %q: %w", name, resp.Error)
	}
	return resp.Result, nil
}
