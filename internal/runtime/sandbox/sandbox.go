package sandbox

import (
	"errors"
	"fmt"
	"time"
)

type SandboxContext struct {
	MaxSteps      int `json:"max_steps"`
	MaxTimeMs     int `json:"max_time_ms"`
	MemoryLimit   int `json:"memory_limit"`
	ToolCallLimit int `json:"tool_call_limit"`
}

type Execution struct {
	cfg          SandboxContext
	startedAt    time.Time
	steps        int
	toolCalls    int
	killReason   string
	terminatedAt time.Time
}

func DefaultContext() SandboxContext {
	return SandboxContext{
		MaxSteps:      12,
		MaxTimeMs:     10 * 60 * 1000,
		MemoryLimit:   300,
		ToolCallLimit: 20,
	}
}

func Start(cfg SandboxContext, now time.Time) *Execution {
	cfg = normalize(cfg)
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return &Execution{cfg: cfg, startedAt: now.UTC()}
}

func (e *Execution) Step(now time.Time) error {
	if err := e.Check(now); err != nil {
		return err
	}
	e.steps++
	if e.steps > e.cfg.MaxSteps {
		e.kill("max steps exceeded", now)
		return fmt.Errorf("sandbox blocked execution: max steps exceeded (%d>%d)", e.steps, e.cfg.MaxSteps)
	}
	return nil
}

func (e *Execution) AddToolCalls(n int, now time.Time) error {
	if n <= 0 {
		return e.Check(now)
	}
	if err := e.Check(now); err != nil {
		return err
	}
	e.toolCalls += n
	if e.toolCalls > e.cfg.ToolCallLimit {
		e.kill("tool call limit exceeded", now)
		return fmt.Errorf("sandbox blocked execution: tool call limit exceeded (%d>%d)", e.toolCalls, e.cfg.ToolCallLimit)
	}
	return nil
}

func (e *Execution) Check(now time.Time) error {
	if e == nil {
		return errors.New("sandbox execution is nil")
	}
	if e.killReason != "" {
		return fmt.Errorf("sandbox execution terminated: %s", e.killReason)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if elapsed := now.UTC().Sub(e.startedAt); elapsed > time.Duration(e.cfg.MaxTimeMs)*time.Millisecond {
		e.kill("max time exceeded", now)
		return fmt.Errorf("sandbox blocked execution: max time exceeded (%s>%dms)", elapsed, e.cfg.MaxTimeMs)
	}
	return nil
}

func (e *Execution) Kill(reason string, now time.Time) {
	if reason == "" {
		reason = "manual kill switch"
	}
	e.kill(reason, now)
}

func (e *Execution) Snapshot() ExecutionSnapshot {
	if e == nil {
		return ExecutionSnapshot{}
	}
	return ExecutionSnapshot{
		Context:      e.cfg,
		StartedAt:    e.startedAt,
		Steps:        e.steps,
		ToolCalls:    e.toolCalls,
		KillReason:   e.killReason,
		TerminatedAt: e.terminatedAt,
	}
}

type ExecutionSnapshot struct {
	Context      SandboxContext `json:"context"`
	StartedAt    time.Time      `json:"started_at"`
	Steps        int            `json:"steps"`
	ToolCalls    int            `json:"tool_calls"`
	KillReason   string         `json:"kill_reason,omitempty"`
	TerminatedAt time.Time      `json:"terminated_at,omitempty"`
}

func (e *Execution) kill(reason string, now time.Time) {
	if e.killReason != "" {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	e.killReason = reason
	e.terminatedAt = now.UTC()
}

func normalize(cfg SandboxContext) SandboxContext {
	def := DefaultContext()
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = def.MaxSteps
	}
	if cfg.MaxTimeMs <= 0 {
		cfg.MaxTimeMs = def.MaxTimeMs
	}
	if cfg.MemoryLimit <= 0 {
		cfg.MemoryLimit = def.MemoryLimit
	}
	if cfg.ToolCallLimit <= 0 {
		cfg.ToolCallLimit = def.ToolCallLimit
	}
	return cfg
}
