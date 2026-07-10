package config

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var dotenvKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// loadDotEnv loads KEY=value files into the process environment without
// overriding variables that are already set (first file to set a key wins).
// Order: a project ./.env (read-only back-compat, so a manual project override
// takes precedence), then the reasonix-owned global credentials file in the user
// config dir (where `reasonix setup` writes keys, so they resolve from any
// directory without ever touching a project's own .env), then ~/.env as a legacy
// fallback (the settings UI writes there). Existing environment variables always
// win over all three.
func loadDotEnv() {
	loadDotEnvForRoot(".")
}

// loadDotEnvForRoot loads a root's .env file (if present) before the home .env
// fallback. When root is "." it behaves like loadDotEnv().
func loadDotEnvForRoot(root string) {
	dotEnvPath := ".env"
	if root != "" && root != "." {
		dotEnvPath = filepath.Join(root, ".env")
	}
	loadDotEnvFile(dotEnvPath)
	if p := UserCredentialsPath(); p != "" {
		loadDotEnvFile(p)
	}
	if home, err := os.UserHomeDir(); err == nil {
		loadDotEnvFile(filepath.Join(home, ".env"))
	}
}

// loadDotEnvFile reads one .env file (if present) and sets any keys not already
// present in the environment. Lenient, zero-dependency parsing.
//
// Supported: unquoted values, single/double quotes, export PREFIX, # comments,
// and basic escapes inside double quotes (\n \t \r \\ \" \'). Nested shell
// expansions, multi-line values, and command substitution are not supported.
func loadDotEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = parseDotEnvValue(strings.TrimSpace(val))
		if key == "" || !dotenvKeyRe.MatchString(key) {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
}

// parseDotEnvValue unwraps quotes and applies a small escape set for double-quoted values.
func parseDotEnvValue(val string) string {
	if len(val) >= 2 {
		switch val[0] {
		case '"':
			if val[len(val)-1] == '"' {
				return unescapeDotEnv(val[1 : len(val)-1])
			}
		case '\'':
			if val[len(val)-1] == '\'' {
				// Single-quoted: no escape expansion (shell-like).
				return val[1 : len(val)-1]
			}
		}
	}
	// Unquoted: strip optional inline comment.
	if i := strings.Index(val, " #"); i >= 0 {
		val = strings.TrimSpace(val[:i])
	}
	return val
}

func unescapeDotEnv(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\', '"', '\'':
			b.WriteByte(s[i])
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
