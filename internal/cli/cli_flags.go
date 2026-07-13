package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"reasonix/internal/agent"
)

const resumePickerSentinel = "__reasonix_resume_picker__"

func splitAllowedToolRules(values []string) ([]string, error) {
	var rules []string
	for _, value := range values {
		start := -1
		depth := 0
		flush := func(end int) {
			if start < 0 {
				return
			}
			if rule := strings.TrimSpace(value[start:end]); rule != "" {
				rules = append(rules, rule)
			}
			start = -1
		}
		for i, r := range value {
			switch r {
			case '(':
				if start < 0 {
					start = i
				}
				depth++
			case ')':
				if depth == 0 {
					return nil, fmt.Errorf("invalid --allowed-tools value %q: unexpected ')'", value)
				}
				depth--
			default:
				if depth == 0 && (r == ',' || unicode.IsSpace(r)) {
					flush(i)
					continue
				}
				if start < 0 {
					start = i
				}
			}
		}
		if depth != 0 {
			return nil, fmt.Errorf("invalid --allowed-tools value %q: unclosed '('", value)
		}
		flush(len(value))
	}
	return uniqueStrings(rules), nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// normalizeOptionalResumeArg gives pflag the optional-value behavior Claude's
// --resume [value] exposes. Interactive sessions have no positional arguments,
// so a following non-flag token is unambiguously the resume query.
func normalizeOptionalResumeArg(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if (arg == "--resume" || arg == "-r") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			out = append(out, arg+"="+args[i+1])
			i++
			continue
		}
		out = append(out, arg)
	}
	return out
}

func resolveSessionQuery(dir, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" || query == resumePickerSentinel {
		return "", nil
	}
	if info, err := os.Stat(query); err == nil && !info.IsDir() {
		abs, absErr := filepath.Abs(query)
		if absErr != nil {
			return "", absErr
		}
		return abs, nil
	}
	sessions, err := agent.ListSessions(dir)
	if err != nil {
		return "", fmt.Errorf("list sessions: %w", err)
	}
	lower := strings.ToLower(query)
	var exact []string
	var partial []string
	for _, session := range sessions {
		id := agent.BranchID(session.Path)
		base := filepath.Base(session.Path)
		if query == id || query == base || query == session.Path {
			exact = append(exact, session.Path)
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{id, base, session.CustomTitle, session.TopicTitle, session.Preview}, "\n"))
		if strings.Contains(haystack, lower) {
			partial = append(partial, session.Path)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = partial
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session matches %q", query)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("session query %q is ambiguous (%d matches)", query, len(matches))
	}
}
