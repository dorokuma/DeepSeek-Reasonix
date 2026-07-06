package rtk

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// PipeFilters is the exact allowlist RTK documents for `rtk pipe -f`.
// Reasonix only pipes when the filter is in this set — never guess.
var PipeFilters = map[string]struct{}{
	"cargo-test":  {},
	"pytest":      {},
	"go-test":     {},
	"go-build":    {},
	"tsc":         {},
	"vitest":      {},
	"grep":        {},
	"rg":          {},
	"find":        {},
	"fd":          {},
	"git-log":     {},
	"git-diff":    {},
	"git-status":  {},
	"log":         {},
	"mypy":        {},
	"ruff-check":  {},
	"ruff-format": {},
	"prettier":    {},
}

// PipeCompact runs stdin through `rtk pipe -f <filter>`. Returns ErrNotRewritten
// when RTK is off, the filter is unknown, or compaction is declined.
func PipeCompact(filter, input string) (string, error) {
	if !Active() {
		return "", ErrNotRewritten
	}
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return "", ErrNotRewritten
	}
	if _, ok := PipeFilters[filter]; !ok {
		return "", ErrNotRewritten
	}
	if strings.TrimSpace(input) == "" {
		return "", ErrNotRewritten
	}
	ctx, cancel := context.WithTimeout(context.Background(), rewriteTimeout()*4)
	defer cancel()
	c := exec.CommandContext(ctx, binPath, "pipe", "-f", filter)
	c.Stdin = strings.NewReader(input)
	out, err := c.Output()
	if err != nil {
		LogFail("pipe", filter, err)
		return "", fmt.Errorf("rtk pipe -f %s: %w", filter, err)
	}
	compacted := string(out)
	if len(compacted) >= len(input) {
		return "", ErrNotRewritten
	}
	return compacted, nil
}

// PipeFilterForShell asks rewrite for cmd and maps the rewritten RTK invocation
// to a pipe filter. Returns ("", false) when rewrite declines or no safe filter.
func PipeFilterForShell(cmd string) (string, bool) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", false
	}
	rewritten := Rewrite(cmd)
	if rewritten == "" {
		return "", false
	}
	return PipeFilterForRewrite(rewritten)
}

// PipeFilterForRewrite maps an accepted rewrite (e.g. "rtk git log -5") to a
// pipe filter. Mapping is conservative: only filters with known output shapes.
func PipeFilterForRewrite(rewritten string) (string, bool) {
	rewritten = strings.TrimSpace(rewritten)
	if !strings.HasPrefix(rewritten, "rtk ") {
		return "", false
	}
	tokens := strings.Fields(rewritten[4:])
	if len(tokens) == 0 {
		return "", false
	}
	switch tokens[0] {
	case "git":
		if len(tokens) < 2 {
			return "", false
		}
		switch tokens[1] {
		case "status":
			return "git-status", true
		case "log":
			return "git-log", true
		case "diff":
			return "git-diff", true
		}
	case "grep":
		return "grep", true
	case "find":
		return "find", true
	case "pytest":
		return "pytest", true
	case "vitest":
		return "vitest", true
	case "cargo":
		if len(tokens) >= 2 && tokens[1] == "test" {
			return "cargo-test", true
		}
	case "go":
		if len(tokens) >= 2 {
			switch tokens[1] {
			case "test":
				return "go-test", true
			case "build":
				return "go-build", true
			}
		}
	case "tsc":
		return "tsc", true
	case "mypy":
		return "mypy", true
	case "ruff":
		if len(tokens) >= 2 {
			switch tokens[1] {
			case "check":
				return "ruff-check", true
			case "format":
				return "ruff-format", true
			}
		}
	case "prettier":
		return "prettier", true
	case "log":
		return "log", true
	}
	return "", false
}
