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
	return `Spawns an agent to work on the specified task. If your current task is ` + "`/root/task1`" + ` and you spawn_agent with task_name "task_3" the agent will have canonical task name ` + "`/root/task1/task_3`" + `.
You are then able to refer to this agent as ` + "`task_3`" + ` or ` + "`/root/task1/task_3`" + ` interchangeably.
The spawned agent will have the same tools as you and the ability to spawn its own subagents.
Only call this tool for a concrete, bounded subtask that can run independently alongside useful local work; otherwise continue locally.
It will be able to send you and other running agents messages, and its final answer will be provided to you when it finishes (via mailbox; use wait_agent / list_agents).
The new agent's canonical task name will be provided to it along with the message.
Returns JSON: task_name (canonical path) and nickname.
Use list_agents to see live agents and agent_status while they run (mailbox stays empty until completion).`
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
	return `Wait for a mailbox update from any live agent, including queued messages and final-status notifications. The wait also ends early when new user input is steered into the active turn. Does not return the content; returns either a summary of which agents have updates (if any), an interruption summary for steered input, or a timeout summary if no activity arrives before the deadline. While agents are still running, mailbox may be empty — use list_agents for live agent_status.`
}

func (waitAgent) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "timeout_ms":{"type":"integer","description":"Max wait milliseconds. Defaults to 600000. Clamped to [1000, 3600000]."}
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
	if len(args) > 0 && string(args) != "null" {
		_ = json.Unmarshal(args, &p)
	}
	var ms int64 = DefaultWaitTimeoutMs
	if p.TimeoutMs != nil {
		ms = *p.TimeoutMs
	}
	msg, timedOut := c.Wait(ctx, ms)
	out, _ := json.Marshal(map[string]any{
		"message":   msg,
		"timed_out": timedOut,
	})
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
	out, _ := json.Marshal(map[string]any{"previous_status": prev})
	return string(out), nil
}
