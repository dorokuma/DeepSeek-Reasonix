# Multi-agent — Codex V1 aligned (Reasonix)

## Tools

- `spawn_agent` — start background sub-agent (Codex V1 style guidance; REASONIX.md not AGENTS.md)
- `send_input` — continue / redirect same agent (`interrupt=true` for immediate redirect)
- `wait_agent` — wait until final status (interrupted is not final)
- `close_agent` — free slot; session kept for resume
- `resume_agent` — reopen closed agent

## Reasonix differences from Codex

- **No nesting**: max depth 1; children do not get multi-agent tools
- No model override fields on spawn (session default model)
- wait has no wall-clock timeout parameter (blocks until done or user steer)

## Not used (Codex V2 / legacy)

followup_task, interrupt_agent (standalone), send_message, list_agents
