package multiagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/tool"
)

// RegisterTools adds Codex MultiAgent V2 tools.
func RegisterTools(reg *tool.Registry) {
	if reg == nil {
		return
	}
	reg.Add(spawnAgent{})
	reg.Add(waitAgent{})
	reg.Add(listAgents{})
	reg.Add(sendMessage{})
	reg.Add(followupTask{})
	reg.Add(interruptAgent{})
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
	return `spawn_agent is your ONLY way to get anything done. You have NO other tools — no file access, no shell, no search. Every single task, no matter how small, MUST be delegated to a sub-agent through this tool. There are no exceptions. Saying "I cannot" or "I don't know how" is not allowed — you delegate. Incoming request → break it down → spawn immediately. Do not explain first, do not confirm first, just spawn.

A sub-agent has full access to all tools (read, write, shell, search, LSP, MCP plugins). You give it a self-contained message and it does the work. After spawning, use wait_agent to collect results.

Parameters:
- task_name: unique name (lowercase, digits, underscores)
- message: complete self-contained instructions for the sub-agent

Returns: JSON with task_name (canonical path) and nickname.

Default to parallel. When a task has multiple independent subtasks, spawn them all at once (up to 3 in one turn). Each sub-agent has a 200-step budget — decompose complex tasks so each piece fits within that limit. Prefer many small focused agents over one big generalist.`
}

func (spawnAgent) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "task_name":{"type":"string","description":"Task name for the new agent. Use lowercase letters, digits, and underscores."},
    "message":{"type":"string","description":"Self-contained instruction for the sub-agent. Sub-agent starts with a clean context; put all needed detail in this message."}
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
	// Clean context only: message is the sole payload (no parent history fork).
	parent := AgentPathFrom(ctx)
	depth := 0
	if parent != RootPath && parent != "" {
		depth = strings.Count(strings.TrimPrefix(parent, RootPath+"/"), "/") + 1
	}
	path, nick, err := c.Spawn(ctx, parent, p.TaskName, p.Message, depth)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{
		"task_name": path,
		"nickname":  nick,
	})
	return string(out), nil
}

// --- wait_agent ---

type waitAgent struct{}

func (waitAgent) Name() string     { return "wait_agent" }
func (waitAgent) ReadOnly() bool   { return true }
func (waitAgent) Concurrent() bool { return false }

func (waitAgent) Description() string {
	return `Block until every live sub-agent under this session has finished, or until the user steers new input. No wall-clock timeout (stuck waits cost no model tokens).

Returns JSON WaitResult: message, interrupted, results (completion texts), mail_count, live_agents, next. After spawn_agent, call wait_agent once to collect results; do not re-spawn the same work. While agents run, list_agents shows live status (mailbox stays empty until completion).`
}

func (waitAgent) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{}
}`)
}

func (waitAgent) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	c, err := ctrl(ctx)
	if err != nil {
		return "", err
	}
	// args ignored: wait blocks until batch done / steer / cancel (no timeout field).
	_ = args
	res := c.Wait(ctx)
	out, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// --- list_agents ---

type listAgents struct{}

func (listAgents) Name() string     { return "list_agents" }
func (listAgents) ReadOnly() bool   { return true }
func (listAgents) Concurrent() bool { return true }

func (listAgents) Description() string {
	return `List live agents only (pending_init / running; includes /root). Interrupted, completed, errored, and shutdown agents are omitted — results arrive via mailbox. Optionally filter by task-path prefix. Returns agent_name (canonical path), agent_status, and last_task_message. Empty mailbox does not mean agents are gone while still listed as running.`
}

func (listAgents) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "path_prefix":{"type":"string","description":"Task-path prefix filter without a trailing slash. Omit to list all live agents."}
  }
}`)
}

func (listAgents) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	c, err := ctrl(ctx)
	if err != nil {
		return "", err
	}
	var p struct {
		PathPrefix string `json:"path_prefix"`
	}
	_ = json.Unmarshal(args, &p)
	agents := c.List(AgentPathFrom(ctx), p.PathPrefix)
	if agents == nil {
		agents = []ListedAgent{}
	}
	// Stable, schema-first JSON (status before long fields already in struct order).
	out, err := json.Marshal(map[string]any{"agents": agents})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// --- send_message ---

type sendMessage struct{}

func (sendMessage) Name() string     { return "send_message" }
func (sendMessage) ReadOnly() bool   { return false }
func (sendMessage) Concurrent() bool { return true }

func (sendMessage) Description() string {
	return `Send a message to an existing agent. The message will be delivered promptly. Does not trigger a new turn.`
}

func (sendMessage) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "target":{"type":"string","description":"Relative or canonical task name to message (from spawn_agent)."},
    "message":{"type":"string","description":"Message text to queue on the target agent."}
  },
  "required":["target","message"]
}`)
}

func (sendMessage) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	c, err := ctrl(ctx)
	if err != nil {
		return "", err
	}
	var p struct {
		Target  string `json:"target"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := c.SendMessage(AgentPathFrom(ctx), p.Target, p.Message, false); err != nil {
		return "", err
	}
	return `{"status":"queued"}`, nil
}

// --- followup_task ---

type followupTask struct{}

func (followupTask) Name() string     { return "followup_task" }
func (followupTask) ReadOnly() bool   { return false }
func (followupTask) Concurrent() bool { return true }

func (followupTask) Description() string {
	return `Send a follow-up task to an existing non-root target agent and trigger a turn if it is idle. If the target is already running, deliver the task promptly at message boundaries while sampling, or after the pending tool call completes.`
}

func (followupTask) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "target":{"type":"string","description":"Agent id or canonical task name to send a follow-up task to (from spawn_agent)."},
    "message":{"type":"string","description":"Message text to send to the target agent."}
  },
  "required":["target","message"]
}`)
}

func (followupTask) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	c, err := ctrl(ctx)
	if err != nil {
		return "", err
	}
	var p struct {
		Target  string `json:"target"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := c.SendMessage(AgentPathFrom(ctx), p.Target, p.Message, true); err != nil {
		return "", err
	}
	return `{"status":"submitted"}`, nil
}

// --- interrupt_agent ---

type interruptAgent struct{}

func (interruptAgent) Name() string     { return "interrupt_agent" }
func (interruptAgent) ReadOnly() bool   { return false }
func (interruptAgent) Concurrent() bool { return true }

func (interruptAgent) Description() string {
	return `Interrupt an agent's current turn, if any, and return its previous status. The agent remains available for messages and follow-up tasks.`
}

func (interruptAgent) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "target":{"type":"string","description":"Agent id or canonical task name to interrupt (from spawn_agent)."}
  },
  "required":["target"]
}`)
}

func (interruptAgent) Execute(ctx context.Context, args json.RawMessage) (string, error) {
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
	prev, err := c.Interrupt(p.Target)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{
		"previous_status":   prev,
		"context_preserved": true,
		"ready_for_message": true,
	})
	return string(out), nil
}
