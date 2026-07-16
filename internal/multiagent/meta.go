package multiagent

// MetaToolNames are Codex multi-agent V1 orchestration tools.
func MetaToolNames() []string {
	return []string{
		"spawn_agent",
		"send_input",
		"wait_agent",
		"close_agent",
		"resume_agent",
	}
}

// IsMetaTool reports whether name is a multi-agent orchestration tool.
func IsMetaTool(name string) bool {
	switch name {
	case "spawn_agent", "send_input", "wait_agent", "close_agent", "resume_agent":
		return true
	default:
		return false
	}
}
