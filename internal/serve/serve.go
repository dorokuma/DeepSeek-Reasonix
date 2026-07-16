// Package serve exposes a control.Controller over HTTP: the typed event stream
// as Server-Sent Events, and the commands as small JSON POST endpoints. It is a
// second frontend alongside the chat TUI — proof that the controller is
// transport-agnostic, and the basis for a browser/desktop client. One server
// drives one session; multiple browser tabs share it.
package serve

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/multiagent"
)

// Server wires a controller to its HTTP surface. The Broadcaster must be the
// same sink the controller was constructed with, so events reach SSE clients.
type Server struct {
	mu   sync.RWMutex // guards ctrl, which switchModel swaps at runtime
	ctrl *control.Controller
	bc   *Broadcaster
	auth *authGate // nil when auth is disabled
}

// New builds a Server. bc must be the controller's event sink.
// serveCfg controls authentication (none, token, or password).
func New(ctrl *control.Controller, bc *Broadcaster, serveCfg config.ServeConfig) *Server {
	return &Server{
		ctrl: ctrl,
		bc:   bc,
		auth: newAuthGate(serveCfg),
	}
}

// ctl returns the current controller. Handlers must read it through here, never
// the field directly, because switchModel replaces it under the write lock.
func (s *Server) ctl() *control.Controller {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ctrl
}

// Ctl returns the current controller.
func (s *Server) Ctl() *control.Controller {
	return s.ctl()
}

// AuthToken returns the pre-shared token when in token mode, or "" otherwise.
func (s *Server) AuthToken() string {
	if s.auth == nil {
		return ""
	}
	return s.auth.Token()
}

// AuthMode returns the authentication mode: "none", "token", or "password".
func (s *Server) AuthMode() string {
	if s.auth == nil {
		return "none"
	}
	return s.auth.Mode()
}

// switchModel rebuilds the controller with a new model, carrying over the
// conversation history. This replicates the TUI/desktop model-switch path.
// The write lock is held only for state reads and the final swap; the
// expensive boot.Build runs outside the lock so HTTP handlers are not blocked.
func (s *Server) switchModel(ctx context.Context, ref string) error {
	// Phase 1: under lock, snapshot current state and mark as switching.
	s.mu.Lock()
	cur := s.ctrl
	if cur.Running() {
		s.mu.Unlock()
		return fmt.Errorf("cannot switch model while a turn is running")
	}
	prevPath := cur.SessionPath()
	if err := cur.Snapshot(); err != nil {
		slog.Warn("serve: snapshot before model switch", "err", err)
	}
	carried := cur.History()
	s.mu.Unlock()

	// Phase 2: expensive I/O outside the lock (network, plugin handshake).
	newCtrl, err := boot.Build(ctx, boot.Options{
		Model:            ref,
		Sink:             s.bc,
		Stderr:           os.Stderr,
		SkipModelRefresh: true,
	})
	if err != nil {
		return fmt.Errorf("switch model: %w", err)
	}

	// Phase 3: under lock, swap the controller atomically.
	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check: a turn may have started while we were building.
	if s.ctrl.Running() {
		newCtrl.Close()
		return fmt.Errorf("cannot switch model: a turn started during rebuild")
	}
	// Keep the carried conversation in its existing file so the switch doesn't
	// orphan a duplicate (#2807).
	newPath := agent.ContinueSessionPath(prevPath, newCtrl.SessionDir(), newCtrl.Label())
	if len(carried) > 0 {
		newCtrl.Resume(&agent.Session{Messages: carried}, newPath)
		// Transfer cumulative cost from the old controller so model switch
		// preserves the session's running spend.
		if cost, currency := cur.SessionCost(); cost > 0 {
			newCtrl.SetSessionCost(cost, currency)
		}
	} else if newPath != "" {
		newCtrl.SetSessionPath(newPath)
	}

	s.ctrl = newCtrl
	cur.Close()
	return nil
}

// switchEffort persists a new reasoning-effort level for the active provider and
// rebuilds via switchModel (which takes the write lock).
func (s *Server) switchEffort(ctx context.Context, level string) error {
	cur := s.ctl()
	if cur.Running() {
		return fmt.Errorf("cannot change effort while a turn is running")
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ref := cur.Label()
	entry, ok := cfg.ResolveModel(ref)
	if !ok {
		return fmt.Errorf("cannot resolve current provider %q", ref)
	}
	if !config.EffortCapabilityForEntry(entry).Supported {
		return fmt.Errorf("effort is not configurable for %s", entry.Name)
	}
	effort, err := config.NormalizeEffort(entry, level)
	if err != nil {
		return err
	}
	editPath := config.UserConfigPath()
	if editPath == "" {
		return fmt.Errorf("no config file found")
	}
	edit := config.LoadForEdit(editPath)
	if err := applyEffortEdit(edit, entry, effort); err != nil {
		return err
	}
	if err := edit.SaveTo(editPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return s.switchModel(ctx, entry.Name+"/"+entry.Model)
}

// applyEffortEdit writes effort onto entry within edit, mirroring CLI/desktop
// SetEffort: upsert the provider when the user config has no block for it yet, and
// enable adaptive thinking for Anthropic so the effort knob actually engages.
func applyEffortEdit(edit *config.Config, entry *config.ProviderEntry, effort string) error {
	if _, ok := edit.Provider(entry.Name); !ok {
		if err := edit.UpsertProvider(*entry); err != nil {
			return err
		}
	}
	if entry.Kind == "anthropic" && effort != "" && entry.Thinking == "" {
		if err := edit.SetProviderThinking(entry.Name, "adaptive"); err != nil {
			return err
		}
	}
	return edit.SetProviderEffort(entry.Name, effort)
}

// Handler returns the HTTP routes: GET / (a minimal browser client), GET /events
// (SSE), GET /history, GET /context, and POST command endpoints.
// CORS is NOT applied by default — same-origin policy protects the unauthenticated
// agent endpoints. Call HandlerWithCORS to opt in for local development.
func (s *Server) Handler() http.Handler {
	return s.handler()
}

func writeTimeoutMiddleware(next http.Handler, timeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/events" {
			next.ServeHTTP(w, r)
			return
		}
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Now().Add(timeout))
		next.ServeHTTP(w, r)
	})
}

// ---- agent API response types ----

type agentResp struct {
	Ok    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// agentItem is one entry in the GET /agents response.
type agentItem struct {
	Path      string `json:"path"`
	Nickname  string `json:"nickname"`
	Status    any    `json:"status"`
	Task      any    `json:"task,omitempty"`
	ElapsedMs int64  `json:"elapsed_ms,omitempty"`
}

func writeAgentJSON(w http.ResponseWriter, status int, ok bool, data any, errMsg string) {
	w.WriteHeader(status)
	writeJSON(w, agentResp{Ok: ok, Data: data, Error: errMsg})
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", s.events)
	mux.HandleFunc("GET /context", s.context)
	mux.HandleFunc("GET /status", s.status)
	mux.HandleFunc("POST /submit", s.submit)
	mux.HandleFunc("POST /cancel", s.cancel)
	mux.HandleFunc("POST /steer", s.steer)
	mux.HandleFunc("POST /approve", s.approve)
	mux.HandleFunc("POST /answer", s.answer)
	mux.HandleFunc("GET /agents", s.agents)
	mux.HandleFunc("POST /agents/{path}/interrupt", s.agentsInterrupt)
	mux.HandleFunc("POST /agents/{path}/send", s.agentsSend)
	return logMiddleware(securityHeadersMiddleware(s.auth.middleware(csrfGuard(writeTimeoutMiddleware(mux, 60*time.Second)))))
}

// securityHeadersMiddleware sets standard security headers on every response.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; form-action 'self'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

// csrfGuard rejects state-changing requests that don't carry a JSON content type
// AND don't come from a same-origin context. The command endpoints have no auth
// and bind to localhost, so a page the user visits could otherwise drive them
// with a simple cross-origin POST (text/plain, no preflight) — submitting
// prompts or auto-approving tool calls. Requiring application/json forces a CORS
// preflight the unauthenticated server never answers, blocking cross-site
// requests; the same-origin frontend (which always sends JSON) is unaffected.
// Additionally, the Origin/Referer check prevents requests from pages served
// on other localhost ports.
func csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			ct := r.Header.Get("Content-Type")
			if i := strings.IndexByte(ct, ';'); i >= 0 {
				ct = ct[:i]
			}
			if !strings.EqualFold(strings.TrimSpace(ct), "application/json") {
				http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			// Enforce a 1 MiB body limit on all POST endpoints to prevent
			// memory exhaustion attacks. The frontend never sends more than
			// a few KB per request.
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			// Origin/Referer check: reject requests from cross-origin pages.
			origin := r.Header.Get("Origin")
			if origin != "" {
				allowedOrigin := "http://" + r.Host
				if r.TLS != nil {
					allowedOrigin = "https://" + r.Host
				}
				if origin != allowedOrigin {
					http.Error(w, "cross-origin request rejected", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Run serves until the process is killed. Interactive approval is enabled so
// "ask" decisions surface as approval_request events answered via POST /approve.

// RunGraceful serves with graceful shutdown. It listens for SIGINT/SIGTERM on
// the provided context and drains active connections before returning. On
// shutdown it first cancels any in-flight turn, waits briefly for it to
// complete (so the SSE stream delivers TurnDone to clients), then signals SSE
// handlers to exit, and finally calls http.Server.Shutdown with a generous
// timeout so long-lived streaming connections are not cut off mid-response.
func (s *Server) RunGraceful(ctx context.Context, addr string) error {
	ctrl := s.ctl()
	ctrl.EnableInteractiveApproval()
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if s.AuthMode() == "none" {
		slog.Warn("⚠  WARNING: Reasonix serve is running WITHOUT authentication.\n    Anyone with access to " + addr + " can control the agent.\n    Set auth.mode = \"token\" in your config to enable authentication.")
	} else {
		slog.Info("agent API listening", "addr", addr)
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("serve: shutting down gracefully")

		// Step 1: cancel the in-flight turn so it can emit TurnDone.
		ctrl.Cancel()

		// Step 2: wait for the turn to actually finish (timeout 5s).
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer waitCancel()
		done := make(chan struct{})
		go func() {
			ctrl.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-waitCtx.Done():
			slog.Warn("serve: timed out waiting for turn to finish")
		}

		// Step 3: shut down the HTTP server with a generous timeout.
		// The 60-second window covers any remaining active requests
		// (non-SSE) that need to complete.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("serve: graceful shutdown failed", "err", err)
		}

		// Step 4: close the broadcaster so SSE handlers exit cleanly.
		s.bc.Close()

		// Step 5: stop the rate-limit cleanup goroutine.
		if s.auth != nil {
			s.auth.rateLimit.Close()
		}

		return <-errCh
	}
}

const sseKeepaliveInterval = 15 * time.Second

// events streams the controller's event flow as SSE until the client
// disconnects. Each event is one `data:` frame of the JSON wire form.
// Supports ?offset=N or Last-Event-ID header for reconnection replay.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	// Determine the starting sequence: query param ?offset= overrides
	// Last-Event-ID (the standard browser EventSource reconnection header).
	var afterSeq int64 = -1
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if n, err := strconv.ParseInt(offsetStr, 10, 64); err == nil {
			afterSeq = n
		}
	}
	if afterSeq < 0 {
		if lastID := r.Header.Get("Last-Event-ID"); lastID != "" {
			if n, err := strconv.ParseInt(lastID, 10, 64); err == nil {
				afterSeq = n
			}
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsubscribe := s.bc.Subscribe(afterSeq)
	defer unsubscribe()

	fmt.Fprint(w, ": connected\n\n") // open the stream immediately
	flusher.Flush()

	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()

	for {
		select {
		case data, ok := <-ch:
			if !ok {
				return
			}
			// Emit the SSE id field so the browser can reconnect with Last-Event-ID.
			var ev wireEvent
			if err := json.Unmarshal(data, &ev); err == nil && ev.Seq > 0 {
				fmt.Fprintf(w, "id: %d\n", ev.Seq)
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			eventType := ev.Kind
			if eventType == "" {
				eventType = "unknown"
			}
			slog.Debug("SSE event sent", "type", eventType, "bytes", len(data))
			flusher.Flush()
		case <-keepalive.C:
			// SSE comment lines start with `:` and are ignored by the
			// client. Emit one every sseKeepaliveInterval so the
			// upstream socket stays warm; without this, a long quiet
			// turn (e.g. a model thinking) lets a proxy like nginx
			// or an ALB close the idle connection and the next
			// event arrives on a half-closed stream.
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// submit runs raw user input as a turn (slash commands and @-references
// resolved by the controller). Returns 202 — output arrives on the event stream.
func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Input string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Input == "" {
		http.Error(w, "missing input", http.StatusBadRequest)
		return
	}
	trimmed := strings.TrimSpace(body.Input)
	// Intercept /model <ref> for runtime model switching (the controller's
	// Submit path only lists models — switching is frontend-specific).
	if strings.HasPrefix(trimmed, "/model ") {
		ref := strings.TrimSpace(strings.TrimPrefix(trimmed, "/model"))
		if ref != "" {
			if err := s.switchModel(r.Context(), ref); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	// Intercept /effort <level> for reasoning effort switching.
	if strings.HasPrefix(trimmed, "/effort ") {
		level := strings.TrimSpace(strings.TrimPrefix(trimmed, "/effort"))
		if level != "" {
			if err := s.switchEffort(r.Context(), level); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	s.mu.RLock()
	s.ctrl.Submit(body.Input)
	s.mu.RUnlock()
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) cancel(w http.ResponseWriter, _ *http.Request) {
	s.ctl().Cancel()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) steer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Input string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Input == "" {
		http.Error(w, "input must not be empty", http.StatusBadRequest)
		return
	}
	s.ctl().Steer(body.Input)
	w.WriteHeader(http.StatusAccepted)
}


func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string `json:"id"`
		Allow   bool   `json:"allow"`
		Session bool   `json:"session"`
		Persist bool   `json:"persist"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	s.ctl().Approve(body.ID, body.Allow, body.Session, body.Persist)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) context(w http.ResponseWriter, r *http.Request) {
	used, window := s.ctl().ContextSnapshot()
	writeJSONCached(w, r, map[string]int{"used": used, "window": window})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("serve: writeJSON encode failed", "err", err)
	}
}

// writeJSONCached encodes v as JSON, computes a weak ETag from the body, and
// returns 304 Not Modified if the client's If-None-Match matches. This avoids
// re-sending unchanged history/context payloads on every reconnect.
func writeJSONCached(w http.ResponseWriter, r *http.Request, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Warn("serve: writeJSONCached marshal failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	etag := fmt.Sprintf(`"%x"`, sha256.Sum256(body))
	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	_, _ = w.Write(body)
}

// logMiddleware logs each request's method, path, and status.
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		slog.Info("serve: request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration", time.Since(start).String(),
		)
	})
}

// responseWriter captures the status code for logging.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Flush delegates to the underlying ResponseWriter if it supports flushing
// (required for SSE /events). Without this the type assertion in the events
// handler fails and the stream endpoint returns 500.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// answer responds to an ask_request.
func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string            `json:"id"`
		Answers []event.AskAnswer `json:"answers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	s.ctl().AnswerQuestion(body.ID, body.Answers)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) agents(w http.ResponseWriter, _ *http.Request) {
	mac := s.ctl().MultiAgentControl()
	if mac == nil {
		writeAgentJSON(w, http.StatusOK, true, []agentItem{}, "")
		return
	}
	listed := mac.List("", "")
	items := make([]agentItem, 0, len(listed))
	for _, a := range listed {
		items = append(items, agentItem{
			Path:     a.AgentName,
			Nickname: multiagent.LeafName(a.AgentName),
			Status:   a.AgentStatus,
			Task:     nil,
			ElapsedMs: func() int64 {
				if a.StartedAt.IsZero() {
					return 0
				}
				return time.Since(a.StartedAt).Milliseconds()
			}(),
		})
	}
	writeAgentJSON(w, http.StatusOK, true, items, "")
}

func (s *Server) agentsInterrupt(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	if path == "" {
		writeAgentJSON(w, http.StatusBadRequest, false, nil, "missing agent path")
		return
	}
	mac := s.ctl().MultiAgentControl()
	if mac == nil {
		writeAgentJSON(w, http.StatusNotFound, false, nil, "multi-agent control not available")
		return
	}
	prev, err := mac.Interrupt(path)
	if err != nil {
		writeAgentJSON(w, http.StatusInternalServerError, false, nil, err.Error())
		return
	}
	writeAgentJSON(w, http.StatusOK, true, map[string]any{"previous_status": prev}, "")
}

func (s *Server) agentsSend(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	if path == "" {
		writeAgentJSON(w, http.StatusBadRequest, false, nil, "missing agent path")
		return
	}
	var body struct {
		Message   string `json:"message"`
		Interrupt bool   `json:"interrupt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAgentJSON(w, http.StatusBadRequest, false, nil, "invalid JSON body")
		return
	}
	if body.Message == "" {
		writeAgentJSON(w, http.StatusBadRequest, false, nil, "missing message")
		return
	}
	mac := s.ctl().MultiAgentControl()
	if mac == nil {
		writeAgentJSON(w, http.StatusNotFound, false, nil, "multi-agent control not available")
		return
	}
	if _, err := mac.SendInput(path, body.Message, body.Interrupt); err != nil {
		writeAgentJSON(w, http.StatusInternalServerError, false, nil, err.Error())
		return
	}
	writeAgentJSON(w, http.StatusOK, true, map[string]any{
		"status":    "sent",
		"interrupt": body.Interrupt,
	}, "")
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	used, window := s.ctl().ContextSnapshot()
	hit, miss := s.ctl().SessionCache()
	sess := map[string]any{
		"label":     s.ctl().Label(),
		"running":   s.ctl().Running(),
		"cwd":       s.ctl().SessionDir(),
		"used":      used,
		"window":    window,
		"cacheHit":  hit,
		"cacheMiss": miss,
	}
	if u := s.ctl().LastUsage(); u != nil {
		sess["lastUsage"] = u
	}
	if b, err := s.ctl().Balance(r.Context()); err == nil && b != nil {
		sess["balance"] = b
	}
	writeJSON(w, sess)
}
