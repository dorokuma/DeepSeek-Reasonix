package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"reasonix/internal/event"
	"reasonix/internal/multiagent"
	"reasonix/internal/tool"
)

// MultiAgentRunner adapts TaskTool/runSub to multiagent.Runner with
// Codex-style per-path session persistence (same thread keeps conversation).
// Closed-agent sessions can be saved under SessionDir for resume across process restarts.
type MultiAgentRunner struct {
	Tool    *TaskTool
	Control *multiagent.Control
	// SessionDir holds on-disk sub-agent sessions (JSONL). Empty → under user config.
	SessionDir string

	mu       sync.Mutex
	sessions map[string]*Session // path → session
	live     map[string]*Agent   // path → currently running agent (for InjectInput)
}

// Run executes one turn on the agent thread at path. First call creates a
// session; later calls reuse it so send_input continues the same context.
func (r *MultiAgentRunner) Run(ctx context.Context, path, message string) (string, error) {
	if r == nil || r.Tool == nil {
		return "", context.Canceled
	}
	// Hard no-nesting: strip multi-agent tools and never pass Control to children.
	subReg := r.Tool.buildSubReg(nil, false)

	sess := r.sessionFor(path)

	bgCtx := ctx
	if parentAgent := AgentFromContext(ctx); parentAgent != nil {
		bgCtx = WithAgent(bgCtx, parentAgent)
	}
	if opts := OptionsFromContext(ctx); opts != nil {
		// Copy options; force non-root path and nil multi-agent control.
		cp := *opts
		cp.MultiAgent = nil
		cp.AgentPath = path
		bgCtx = WithOptions(bgCtx, &cp)
	}
	bgCtx = multiagent.WithAgentPath(bgCtx, path)
	bgCtx = multiagent.WithControl(bgCtx, nil)
	bgCtx = WithSession(bgCtx, sess)

	defer r.clearLive(path)
	return r.Tool.runSubOnSession(bgCtx, message, subReg, sess, event.Discard, 0, r.Tool.sysPrompt, "task", "", "",
		func(sub *Agent) {
			r.setLive(path, sub)
		})
}

// DropSession implements multiagent.SessionDropper / SessionKeeper.
func (r *MultiAgentRunner) DropSession(path string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.sessions != nil {
		delete(r.sessions, path)
	}
	if r.live != nil {
		delete(r.live, path)
	}
	r.mu.Unlock()
	_ = os.Remove(r.sessionFile(path))
	_ = os.Remove(r.metaFile(path))
}

// SaveSession writes the in-memory session (and a small meta marker) to disk.
func (r *MultiAgentRunner) SaveSession(path string) error {
	if r == nil {
		return fmt.Errorf("nil runner")
	}
	r.mu.Lock()
	sess := r.sessions[path]
	r.mu.Unlock()
	if sess == nil {
		// Nothing in memory — still write a marker so resume can find the agent id.
		return r.writeMetaMarker(path)
	}
	if err := sess.Save(r.sessionFile(path)); err != nil {
		return err
	}
	return r.writeMetaMarker(path)
}

// LoadSession restores a session from disk into the runner map (if present).
func (r *MultiAgentRunner) LoadSession(path string) error {
	if r == nil {
		return fmt.Errorf("nil runner")
	}
	fp := r.sessionFile(path)
	loaded, err := LoadSession(fp)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // meta-only close is ok
		}
		return err
	}
	r.mu.Lock()
	if r.sessions == nil {
		r.sessions = make(map[string]*Session)
	}
	r.sessions[path] = loaded
	r.mu.Unlock()
	return nil
}

// HasPersistedSession reports whether disk still has a closed-agent marker/session.
func (r *MultiAgentRunner) HasPersistedSession(path string) bool {
	if r == nil {
		return false
	}
	if _, err := os.Stat(r.metaFile(path)); err == nil {
		return true
	}
	if _, err := os.Stat(r.sessionFile(path)); err == nil {
		return true
	}
	return false
}

// Steer implements multiagent.Steerer: soft-queue into a running turn.
// Returns false if no live agent or the inject queue is full.
func (r *MultiAgentRunner) Steer(path, message string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	a := r.live[path]
	r.mu.Unlock()
	if a == nil {
		return false
	}
	return a.InjectInput(message)
}

func (r *MultiAgentRunner) setLive(path string, a *Agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.live == nil {
		r.live = make(map[string]*Agent)
	}
	r.live[path] = a
}

func (r *MultiAgentRunner) clearLive(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.live != nil {
		delete(r.live, path)
	}
}

func (r *MultiAgentRunner) sessionFor(path string) *Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions == nil {
		r.sessions = make(map[string]*Session)
	}
	if s, ok := r.sessions[path]; ok && s != nil {
		return s
	}
	// Try disk (resume after process restart).
	if loaded, err := LoadSession(r.sessionFile(path)); err == nil && loaded != nil {
		r.sessions[path] = loaded
		return loaded
	}
	sys := r.Tool.sysPrompt
	if sys == "" {
		sys = DefaultTaskSystemPrompt
	}
	s := NewSession(sys)
	r.sessions[path] = s
	return s
}

func (r *MultiAgentRunner) storeDir() string {
	if r != nil && strings.TrimSpace(r.SessionDir) != "" {
		return r.SessionDir
	}
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "reasonix", "subagent-sessions")
}

func (r *MultiAgentRunner) pathKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:16])
}

func (r *MultiAgentRunner) sessionFile(path string) string {
	return filepath.Join(r.storeDir(), r.pathKey(path)+".jsonl")
}

func (r *MultiAgentRunner) metaFile(path string) string {
	return filepath.Join(r.storeDir(), r.pathKey(path)+".meta")
}

func (r *MultiAgentRunner) writeMetaMarker(path string) error {
	dir := r.storeDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// path\n leaf — enough for resume discovery
	body := path + "\n"
	return os.WriteFile(r.metaFile(path), []byte(body), 0o600)
}

// Ensure interface compliance.
var (
	_ multiagent.Runner         = (*MultiAgentRunner)(nil)
	_ multiagent.SessionDropper = (*MultiAgentRunner)(nil)
	_ multiagent.SessionKeeper  = (*MultiAgentRunner)(nil)
	_ multiagent.Steerer        = (*MultiAgentRunner)(nil)
)

var _ = tool.NewRegistry
