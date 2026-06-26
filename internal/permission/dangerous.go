package permission

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
)

// DangerousGate creates a Gate that intercepts dangerous bash commands.
// Matching commands are routed to the package-level approver (set via
// SetSubAgentApprover); non-matching commands are allowed through immediately.
// When no approver is registered (headless mode), dangerous commands are denied.
func DangerousGate(patterns []string) *dangerousGate {
	return &dangerousGate{patterns: patterns}
}

type dangerousGate struct {
	patterns []string
}

// Check implements agent.Gate. For non-bash tools it always allows. For bash
// commands matching a configured dangerous pattern it consults the package-level
// approver; when no approver is set (headless mode) it denies the call.
func (g *dangerousGate) Check(ctx context.Context, toolName string, args json.RawMessage, readOnly bool) (bool, string, error) {
	if toolName != "bash" {
		return true, "", nil
	}
	subject := Subject(args)
	if !matchDangerousCommand(g.patterns, subject) {
		return true, "", nil
	}
	// Dangerous command — consult the approver.
	approver := getSubAgentApprover()
	if approver == nil {
		return false, "dangerous command denied — no interactive approval available in headless mode", nil
	}
	allow, _, err := approver.Approve(ctx, toolName, subject, args)
	if err != nil {
		return false, "approval aborted", err
	}
	if !allow {
		return false, "the user declined this tool call — do not retry it", nil
	}
	return true, "", nil
}

// matchDangerousCommand checks whether the given command subject matches
// any of the given dangerous patterns.
func matchDangerousCommand(patterns []string, subject string) bool {
	for _, pat := range patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if matched, _ := filepath.Match(pat, subject); matched {
			return true
		}
	}
	return false
}

// Package-level approver for sub-agent dangerous-command gates. Set once the
// controller is ready with its interactive approval sink.
var (
	subAgentApproverMu sync.Mutex
	subAgentApprover   Approver
)

// SetSubAgentApprover sets the approver used by DangerousGate instances
// for sub-agent dangerous command checks. Call this once the controller
// is ready with its interactive approval sink.
func SetSubAgentApprover(a Approver) {
	subAgentApproverMu.Lock()
	defer subAgentApproverMu.Unlock()
	subAgentApprover = a
}

// getSubAgentApprover returns the currently registered sub-agent approver.
func getSubAgentApprover() Approver {
	subAgentApproverMu.Lock()
	defer subAgentApproverMu.Unlock()
	return subAgentApprover
}
