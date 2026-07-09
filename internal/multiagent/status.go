package multiagent

import "strings"

// Status mirrors Codex AgentStatus for MultiAgent V2.
type Status string

const (
	StatusPendingInit Status = "pending_init"
	StatusRunning     Status = "running"
	StatusInterrupted Status = "interrupted"
	StatusShutdown    Status = "shutdown"
	StatusNotFound    Status = "not_found"
	// StatusCompleted and StatusErrored are final; payload is in Agent.LastAnswer / LastError.
	StatusCompleted Status = "completed"
	StatusErrored   Status = "errored"
)

// IsFinal reports whether status is terminal (Codex is_final).
func IsFinal(s Status) bool {
	switch s {
	case StatusPendingInit, StatusRunning, StatusInterrupted:
		return false
	default:
		return true
	}
}

// StatusJSON is the list/interrupt previous_status shape (Codex oneOf simplified).
func StatusJSON(s Status, completedMsg, errMsg string) any {
	switch s {
	case StatusCompleted:
		return map[string]any{"completed": completedMsg}
	case StatusErrored:
		return map[string]any{"errored": errMsg}
	default:
		return string(s)
	}
}

// NormalizePathSegment keeps task_name segments Codex-like: lowercase, digits, underscore.
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

// JoinPath builds canonical path: parent + "/" + segment. Root is "/root".
func JoinPath(parent, segment string) string {
	parent = strings.TrimSuffix(strings.TrimSpace(parent), "/")
	if parent == "" {
		parent = "/root"
	}
	segment = NormalizePathSegment(segment)
	return parent + "/" + segment
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
