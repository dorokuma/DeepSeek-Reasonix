package rtk

import "strings"

// sideEffectCommands are never transparently rewritten — they perform network or
// remote actions where RTK output compaction must not alter the invocation.
var sideEffectCommands = map[string]struct{}{
	"curl": {}, "wget": {}, "nc": {}, "netcat": {},
	"ssh": {}, "scp": {}, "rsync": {}, "telnet": {},
}

// DeclineRewrite reports whether cmd must run verbatim (no RTK rewrite).
func DeclineRewrite(cmd string) bool {
	tok := firstShellWord(strings.TrimSpace(cmd))
	if tok == "" {
		return false
	}
	_, ok := sideEffectCommands[strings.ToLower(tok)]
	return ok
}

func firstShellWord(cmd string) string {
	quote := byte(0)
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if c == ' ' || c == '\t' {
			if i == 0 {
				continue
			}
			return cmd[:i]
		}
	}
	return cmd
}
