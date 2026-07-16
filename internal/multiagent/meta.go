package multiagent

import "strings"

// MetaToolNames are Codex multi-agent V1 orchestration tools.
// Reasonix is hard one-layer: only the root agent may use these tools.
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

// IsRootAgentPath is true only for the session root (empty or RootPath).
// Non-root paths are sub-agents and must never receive multi-agent tools or spawn.
func IsRootAgentPath(path string) bool {
	path = strings.TrimSpace(path)
	return path == "" || path == RootPath
}

// PathDepth counts segments under RootPath: /root → 0, /root/a → 1, /root/a/b → 2.
func PathDepth(path string) int {
	path = strings.TrimSuffix(strings.TrimSpace(path), "/")
	if IsRootAgentPath(path) {
		return 0
	}
	if !strings.HasPrefix(path, RootPath+"/") {
		// Unknown shape: treat as non-root so spawn is denied.
		return 1
	}
	rest := strings.TrimPrefix(path, RootPath+"/")
	if rest == "" {
		return 0
	}
	return strings.Count(rest, "/") + 1
}
