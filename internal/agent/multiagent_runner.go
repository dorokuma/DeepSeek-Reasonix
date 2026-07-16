package agent

import (
	"context"
	"sync"

	"reasonix/internal/event"
	"reasonix/internal/multiagent"
	"reasonix/internal/tool"
)

// MultiAgentRunner adapts TaskTool/runSub to multiagent.Runner with
// Codex-style per-path session persistence (same thread keeps conversation).
type MultiAgentRunner struct {
	Tool    *TaskTool
	Control *multiagent.Control

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
	// One layer only: children do not get multi-agent tools (no nested spawn).
	subReg := r.Tool.buildSubReg(nil, false)

	sess := r.sessionFor(path)

	bgCtx := ctx
	if parentAgent := AgentFromContext(ctx); parentAgent != nil {
		bgCtx = WithAgent(bgCtx, parentAgent)
	}
	if opts := OptionsFromContext(ctx); opts != nil {
		bgCtx = WithOptions(bgCtx, opts)
	}
	// Path for diagnostics only; Control not given to children (no nested multi-agent).
	bgCtx = multiagent.WithAgentPath(bgCtx, path)
	bgCtx = multiagent.WithControl(bgCtx, nil)
	bgCtx = WithSession(bgCtx, sess)

	defer r.clearLive(path)
	return r.Tool.runSubOnSession(bgCtx, message, subReg, sess, event.Discard, 0, r.Tool.sysPrompt, "task", "", "",
		func(sub *Agent) {
			r.setLive(path, sub)
		})
}

// DropSession implements multiagent.SessionDropper (close_agent).
func (r *MultiAgentRunner) DropSession(path string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions != nil {
		delete(r.sessions, path)
	}
	if r.live != nil {
		delete(r.live, path)
	}
}

// Steer implements multiagent.Steerer: soft-queue into a running turn.
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
	a.InjectInput(message)
	return true
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
	sys := r.Tool.sysPrompt
	if sys == "" {
		sys = DefaultTaskSystemPrompt
	}
	s := NewSession(sys)
	r.sessions[path] = s
	return s
}

// Ensure interface compliance.
var (
	_ multiagent.Runner         = (*MultiAgentRunner)(nil)
	_ multiagent.SessionDropper = (*MultiAgentRunner)(nil)
	_ multiagent.Steerer        = (*MultiAgentRunner)(nil)
)

var _ = tool.NewRegistry
