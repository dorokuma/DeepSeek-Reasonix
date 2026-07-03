package permission

import "strings"

// normalizeBashSafeRedirectsForMatch returns a copy of subject with redirect
// syntax removed only when the redirect cannot write to a real file. It is used
// for permission matching, never for execution.
//
// Supported safe forms are fd duplication/close (`2>&1`, `>&2`, `2>&-`,
// `0<&1`) and output redirects to a null sink (`>/dev/null`, `>$null`,
// `>nul`, `2> /dev/null`, `>>/dev/null`, `&>/dev/null`, `&>>/dev/null`).
// The shell execution layer normalizes these null-sink spellings to the actual
// sink for the resolved shell. Other redirections are left unnormalized so the
// usual shell-syntax guard keeps prefix/read-only matching conservative.
func normalizeBashSafeRedirectsForMatch(subject string) (string, bool) {
	var (
		out        strings.Builder
		quote      byte
		parenDepth int
		backtick   bool
		tokenStart = true
		removed    bool
	)

	write := func(c byte) {
		out.WriteByte(c)
		tokenStart = isBashMatchSpace(c)
	}
	noteRemoved := func() {
		removed = true
		if out.Len() > 0 && !lastByteIsHorizontalSpace(out.String()) {
			out.WriteByte(' ')
		}
		tokenStart = true
	}

	for i := 0; i < len(subject); {
		c := subject[i]

		switch {
		case quote != 0:
			write(c)
			i++
			if c == '\\' && quote == '"' && i < len(subject) {
				write(subject[i])
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		case parenDepth > 0:
			write(c)
			i++
			switch c {
			case '(':
				parenDepth++
			case ')':
				parenDepth--
			}
			continue
		case backtick:
			write(c)
			i++
			if c == '`' {
				backtick = false
			}
			continue
		}

		switch c {
		case '\'', '"':
			quote = c
			write(c)
			i++
		case '`':
			backtick = true
			write(c)
			i++
		case '\\':
			write(c)
			i++
			if i < len(subject) {
				write(subject[i])
				i++
			}
		case '$':
			if i+1 < len(subject) && subject[i+1] == '(' {
				parenDepth = 1
				write(c)
				write('(')
				i += 2
				continue
			}
			write(c)
			i++
		case '&':
			if next, ok := consumeSafeBashRedirect(subject, i); ok {
				noteRemoved()
				i = next
				continue
			}
			write(c)
			i++
		case '>', '<':
			if c == '<' && i+1 < len(subject) && subject[i+1] == '(' {
				parenDepth = 1
				write(c)
				write('(')
				i += 2
				continue
			}
			if c == '>' && i+1 < len(subject) && subject[i+1] == '(' {
				parenDepth = 1
				write(c)
				write('(')
				i += 2
				continue
			}
			if next, ok := consumeSafeBashRedirect(subject, i); ok {
				noteRemoved()
				i = next
				continue
			}
			return "", false
		default:
			if tokenStart && isBashMatchDigit(c) {
				if next, ok := consumeSafeBashRedirect(subject, i); ok {
					noteRemoved()
					i = next
					continue
				}
			}
			write(c)
			i++
		}
	}

	if !removed {
		return subject, true
	}
	return strings.TrimSpace(out.String()), true
}

func consumeSafeBashRedirect(s string, start int) (int, bool) {
	i := start
	for i < len(s) && isBashMatchDigit(s[i]) {
		i++
	}
	if i >= len(s) {
		return start, false
	}

	switch {
	case strings.HasPrefix(s[i:], ">&"), strings.HasPrefix(s[i:], "<&"):
		return consumeBashFDDup(s, i+2)
	case strings.HasPrefix(s[i:], "&>>"):
		return consumeBashNullRedirect(s, i+3)
	case strings.HasPrefix(s[i:], "&>"):
		return consumeBashNullRedirect(s, i+2)
	case strings.HasPrefix(s[i:], ">>"):
		return consumeBashNullRedirect(s, i+2)
	case s[i] == '>':
		return consumeBashNullRedirect(s, i+1)
	default:
		return start, false
	}
}

func consumeBashFDDup(s string, i int) (int, bool) {
	i = skipBashMatchHorizontalSpace(s, i)
	if i >= len(s) {
		return i, false
	}
	if s[i] == '-' {
		next := i + 1
		if next < len(s) && !isBashRedirectWordEnd(s[next]) {
			return next, false
		}
		return next, true
	}
	start := i
	for i < len(s) && isBashMatchDigit(s[i]) {
		i++
	}
	if i == start {
		return i, false
	}
	if i < len(s) && !isBashRedirectWordEnd(s[i]) {
		return i, false
	}
	return i, true
}

func consumeBashNullRedirect(s string, i int) (int, bool) {
	i = skipBashMatchHorizontalSpace(s, i)
	for _, sink := range []string{"/dev/null", "$null", "nul"} {
		next, ok := consumeBashNullSink(s, i, sink)
		if ok {
			return next, true
		}
	}
	return i, false
}

func consumeBashNullSink(s string, i int, sink string) (int, bool) {
	if i+len(sink) > len(s) {
		return i, false
	}
	got := s[i : i+len(sink)]
	if sink == "/dev/null" {
		if got != sink {
			return i, false
		}
	} else if !strings.EqualFold(got, sink) {
		return i, false
	}
	next := i + len(sink)
	if next < len(s) && !isBashRedirectWordEnd(s[next]) {
		return next, false
	}
	return next, true
}

func skipBashMatchHorizontalSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

func isBashRedirectWordEnd(c byte) bool {
	return isBashMatchSpace(c) || strings.ContainsRune(";|&<>", rune(c))
}

func isBashMatchSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func isBashMatchDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func lastByteIsHorizontalSpace(s string) bool {
	if s == "" {
		return false
	}
	c := s[len(s)-1]
	return c == ' ' || c == '\t'
}
