package multiagent

import "strings"

// Status mirrors codex_protocol::protocol::AgentStatus.
type Status string

const (
	StatusPendingInit Status = "pending_init"
	StatusRunning     Status = "running"
	StatusInterrupted Status = "interrupted"
	StatusShutdown    Status = "shutdown"
	StatusNotFound    Status = "not_found"
	StatusCompleted   Status = "completed"
	StatusErrored     Status = "errored"
)

// IsFinal matches Codex is_final: interrupted is NOT final — the thread stays
// available for send_input. Wait ends on completed / errored / shutdown / not_found.
func IsFinal(s Status) bool {
	switch s {
	case StatusPendingInit, StatusRunning, StatusInterrupted:
		return false
	default:
		return true
	}
}

// IsTurnActive is true while a turn is in flight.
func IsTurnActive(s Status) bool {
	return s == StatusPendingInit || s == StatusRunning
}

// IsOpen is true while the agent still occupies a concurrency slot (Codex:
// completed agents remain open until close_agent).
func IsOpen(s Status) bool {
	switch s {
	case StatusShutdown, StatusNotFound:
		return false
	default:
		return true
	}
}

// IsListLive is true for any open agent (HTTP list / diagnostics).
func IsListLive(s Status) bool {
	return IsOpen(s)
}

// StatusJSON matches Codex list agent_status oneOf (string enum or completed/errored object).
func StatusJSON(s Status, completedMsg, errMsg string) any {
	switch s {
	case StatusCompleted:
		var msg any
		if strings.TrimSpace(completedMsg) == "" {
			msg = nil
		} else {
			msg = completedMsg
		}
		return map[string]any{"completed": msg}
	case StatusErrored:
		if errMsg == "" {
			errMsg = "error"
		}
		return map[string]any{"errored": errMsg}
	default:
		return string(s)
	}
}

// NormalizePathSegment keeps task_name segments Codex-like.
func NormalizePathSegment(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		case r == '-' || r == ' ':
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "task"
	}
	return out
}

const RootPath = "/root"

// JoinPath builds canonical path: parent + "/" + segment.
func JoinPath(parent, segment string) string {
	parent = strings.TrimSuffix(strings.TrimSpace(parent), "/")
	if parent == "" {
		parent = RootPath
	}
	return parent + "/" + NormalizePathSegment(segment)
}

// ParentPath returns parent of a canonical path, or "".
func ParentPath(path string) string {
	path = strings.TrimSpace(path)
	i := strings.LastIndex(path, "/")
	if i <= 0 {
		return ""
	}
	return path[:i]
}

// LeafName returns the last segment.
func LeafName(path string) string {
	path = strings.TrimSpace(path)
	if i := strings.LastIndex(path, "/"); i >= 0 && i+1 < len(path) {
		return path[i+1:]
	}
	return path
}

// ResolveRelative resolves a path_prefix relative to current agent path (Codex AgentPath::resolve).
func ResolveRelative(current, prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return ""
	}
	if strings.HasPrefix(prefix, "/") {
		return strings.TrimSuffix(prefix, "/")
	}
	cur := strings.TrimSpace(current)
	if cur == "" {
		cur = RootPath
	}
	return JoinPath(cur, prefix)
}
