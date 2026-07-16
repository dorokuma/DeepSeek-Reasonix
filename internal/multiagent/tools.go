package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"reasonix/internal/tool"
)

// RegisterTools adds Codex multi-agent V1 tools only.
// Sub-agents do not receive multi-agent tools; spawning is restricted to the root agent.
func RegisterTools(reg *tool.Registry) {
	if reg == nil {
		return
	}
	reg.Add(spawnAgent{})
	reg.Add(sendInput{})
	reg.Add(waitAgent{})
	reg.Add(closeAgent{})
	reg.Add(resumeAgent{})
}

func ctrl(ctx context.Context) (*Control, error) {
	c, ok := FromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("multi-agent control is not available in this context")
	}
	return c, nil
}

// --- spawn_agent ---

type spawnAgent struct{}

func (spawnAgent) Name() string     { return "spawn_agent" }
func (spawnAgent) ReadOnly() bool   { return false }
func (spawnAgent) Concurrent() bool { return true }

func (spawnAgent) Description() string {
	// Aligned with Codex V1 spawn_agent guidance; Reasonix wording where product differs.
	return `Spawn a sub-agent for a well-scoped task. Returns the spawned agent path (task_name) plus a nickname.

This tool starts a background sub-agent that inherits the session environment. Follow the rules below.

Do not spawn sub-agents unless the user or project instructions (REASONIX.md / skills) explicitly ask for sub-agents, delegation, or parallel agent work.
Requests for depth, thoroughness, research, investigation, or detailed codebase analysis do not count as permission to spawn. Sub-agents cannot spawn further agents.

### When to delegate vs. do the subtask yourself
- First, quickly analyze the overall user task and form a succinct high-level plan. Identify which tasks are immediate blockers on the critical path, and which tasks are sidecar tasks that are needed but can run in parallel without blocking the next local step. As part of that plan, explicitly decide what immediate task you should do locally right now. Do this planning step before delegating so you do not hand off the immediate blocking task and then waste time waiting on it.
- Use a sub-agent when a subtask is easy enough for it to handle and can run in parallel with your local work. Prefer concrete, bounded sidecar tasks that materially advance the main task without blocking your immediate next local step.
- Do not delegate urgent blocking work when your immediate next step depends on that result. If the very next action is blocked on that task, do it yourself to keep the critical path moving.
- Keep work local when the subtask is too difficult to delegate well, or when it is tightly coupled, urgent, or likely to block your immediate next step.

### Designing delegated subtasks
- Subtasks must be concrete, well-defined, and self-contained.
- Delegated subtasks must materially advance the main task.
- Do not duplicate work between yourself and delegated subtasks.
- Avoid issuing multiple spawn calls for the same unresolved work unless the new task is genuinely different and necessary.
- Narrow the delegated ask to the concrete output you need next.
- For coding tasks, prefer concrete code-change work over open-ended exploration when the sub-agent can make a bounded change in a clear write scope.
- When delegating coding work, instruct the sub-agent to edit files directly and list the paths it changed in the final answer.
- For code-edit subtasks, decompose so each delegated task has a disjoint write set.

### After you delegate
- Call wait_agent very sparingly. Only when you need the result immediately for the next critical-path step and you are blocked until it returns.
- Do not redo delegated sub-agent tasks yourself; focus on integrating results or non-overlapping work.
- While the sub-agent is running in the background, do meaningful non-overlapping work immediately.
- Do not repeatedly wait by reflex.
- When a delegated coding task returns, quickly review the changes, then integrate or refine them.
- To continue or redirect the same agent, use send_input (reuse the thread). Do not spawn a replacement for the same work.
- When finished with an agent, call close_agent. Completed agents stay open and count toward the concurrency limit until closed.

### Parallel patterns
- Run multiple independent information-seeking subtasks in parallel when you have distinct questions.
- Split implementation into disjoint slices and spawn multiple agents when write scopes do not overlap.
- Delegate verification only when it can run in parallel with ongoing work and is likely to catch a concrete risk.

### Reasonix limits
- First turn starts with a clean context: put everything the sub-agent needs in message.`
}

func (spawnAgent) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "task_name":{"type":"string","description":"Task name for the new agent. Use lowercase letters, digits, and underscores."},
    "message":{"type":"string","description":"Self-contained instruction for the sub-agent. First turn has a clean context; put all needed detail here."}
  },
  "required":["task_name","message"]
}`)
}

func (spawnAgent) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	c, err := ctrl(ctx)
	if err != nil {
		return "", err
	}
	var p struct {
		TaskName string `json:"task_name"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	// Root-only spawn: never use child AgentPath as parent (hard no-nesting).
	if !IsRootAgentPath(AgentPathFrom(ctx)) {
		return "", fmt.Errorf("Agent depth limit reached. Solve the task yourself.")
	}
	path, nick, err := c.Spawn(ctx, RootPath, p.TaskName, p.Message)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{
		"task_name": path,
		"nickname":  nick,
	})
	return string(out), nil
}

// --- send_input ---

type sendInput struct{}

func (sendInput) Name() string     { return "send_input" }
func (sendInput) ReadOnly() bool   { return false }
func (sendInput) Concurrent() bool { return true }

func (sendInput) Description() string {
	// Codex V1 send_input (reasonix: same semantics).
	return `Send a message to an existing agent. Use interrupt=true to redirect work immediately. You should reuse the agent by send_input if you believe your assigned task is highly dependent on the context of a previous task.

interrupt=false or omitted: if the agent is running, queue the message for delivery at a message boundary; if the agent is idle (completed, interrupted, or errored but not closed), start a new turn on the same thread with preserved context.
interrupt=true: stop the current turn, then handle this message on the same agent.

Prefer send_input over spawn_agent when continuing or correcting work on an open agent.`
}

func (sendInput) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "target":{"type":"string","description":"Agent id to message (from spawn_agent)."},
    "message":{"type":"string","description":"Message text to send to the agent."},
    "interrupt":{"type":"boolean","description":"True interrupts the current task and handles this message immediately; false or omitted queues it (or starts a turn when idle)."}
  },
  "required":["target","message"]
}`)
}

func (sendInput) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	c, err := ctrl(ctx)
	if err != nil {
		return "", err
	}
	var p struct {
		Target    string `json:"target"`
		Message   string `json:"message"`
		Interrupt bool   `json:"interrupt"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	path, err := c.SendInput(p.Target, p.Message, p.Interrupt)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{
		"target":        path,
		"submission_id": path,
	})
	return string(out), nil
}

// --- wait_agent ---

type waitAgent struct{}

func (waitAgent) Name() string     { return "wait_agent" }
func (waitAgent) ReadOnly() bool   { return true }
func (waitAgent) Concurrent() bool { return false }

func (waitAgent) Description() string {
	// Codex V1 wait_agent + reasonix notes (interrupted not final; mailbox; timeout).
	return `Wait for agents to reach a final status. Completed statuses may include the agent's final message. Returns timed_out when the deadline elapses before agents finish. Once an agent reaches a final status, a notification message is also available (mailbox).

Interrupted is not a final status — the agent remains open for send_input. After an interrupt, wait keeps blocking until a later turn finishes, the agent errors, you close_agent, or timeout_ms elapses.

Call wait_agent sparingly: only when you need the result for the next critical-path step and you are blocked until it returns. Do not wait by reflex.

The wait also ends early when new user input is steered into the active turn.

timeout_ms: optional wait deadline in milliseconds (default 600000 = 10 minutes; min 1000; max 3600000).`
}

func (waitAgent) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "timeout_ms":{"type":"integer","description":"Max wait time in milliseconds before returning timed_out (default 600000, min 1000, max 3600000)."}
  }
}`)
}

func (waitAgent) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	c, err := ctrl(ctx)
	if err != nil {
		return "", err
	}
	var p struct {
		TimeoutMs *int64 `json:"timeout_ms"`
	}
	_ = json.Unmarshal(args, &p)
	var res WaitResult
	if p.TimeoutMs != nil {
		res = c.WaitTimeout(ctx, *p.TimeoutMs)
	} else {
		res = c.Wait(ctx)
	}
	out, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// --- close_agent ---

type closeAgent struct{}

func (closeAgent) Name() string     { return "close_agent" }
func (closeAgent) ReadOnly() bool   { return false }
func (closeAgent) Concurrent() bool { return true }

func (closeAgent) Description() string {
	// Codex V1 close_agent; Sub-agents cannot spawn further agents so no "descendants".
	return `Close an agent when it is no longer needed, and return the target agent's previous status before shutdown was requested. Completed agents remain open and count toward the concurrency limit until closed. Don't keep agents open for too long if they are not needed anymore.

After close, resume_agent can reopen the same id so it can receive send_input and wait_agent again. Prefer close_agent over leaving finished agents open until the concurrency limit is hit.`
}

func (closeAgent) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "target":{"type":"string","description":"Agent id to close (from spawn_agent)."}
  },
  "required":["target"]
}`)
}

func (closeAgent) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	c, err := ctrl(ctx)
	if err != nil {
		return "", err
	}
	var p struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	prev, path, err := c.CloseAgent(p.Target)
	if err != nil {
		// Close already completed; persist failure is a warning, not a failed close.
		if errors.Is(err, ErrSessionPersist) {
			out, _ := json.Marshal(map[string]any{
				"target":          path,
				"previous_status": prev,
				"closed":          true,
				"persist_warning": err.Error(),
			})
			return string(out), nil
		}
		return "", err
	}
	out, _ := json.Marshal(map[string]any{
		"target":          path,
		"previous_status": prev,
		"closed":          true,
	})
	return string(out), nil
}

// --- resume_agent ---

type resumeAgent struct{}

func (resumeAgent) Name() string     { return "resume_agent" }
func (resumeAgent) ReadOnly() bool   { return false }
func (resumeAgent) Concurrent() bool { return true }

func (resumeAgent) Description() string {
	// Codex resume_agent.
	return `Resume a previously closed agent by id so it can receive send_input and wait_agent calls.

Use this when you closed an agent but still need its thread and context. After resume the agent is open again but idle until you send_input. If the agent is already open, this reports its current status.`
}

func (resumeAgent) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "id":{"type":"string","description":"Agent id to resume."}
  },
  "required":["id"]
}`)
}

func (resumeAgent) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	c, err := ctrl(ctx)
	if err != nil {
		return "", err
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	st, path, err := c.ResumeAgent(p.ID)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{
		"id":     path,
		"status": st,
	})
	return string(out), nil
}
