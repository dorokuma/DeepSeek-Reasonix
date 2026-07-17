// Package envutil provides environment variable manipulation utilities
// used across the codebase, including credential filtering for subprocesses.
package envutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// StripCredentialEnv removes env vars likely to carry secrets from the
// inherited process environment. It matches by naming convention (suffixes
// like _KEY, _TOKEN, _SECRET, _PASSWORD, etc.) and by well-known names
// (GITHUB_TOKEN, OPENAI_API_KEY, etc.).
//
// IMPORTANT: This is a heuristic filter. Custom env vars that carry secrets
// but don't match these patterns (e.g. MYAPP_AUTH_DATA) will NOT be stripped.
// Plugin authors should always declare needed credentials explicitly in their
// plugin config's env block rather than relying on inheritance. When in doubt,
// add the var name to the explicit blocklist inside this function.
//
// Callers should declare needed credentials explicitly instead of relying on
// inheritance.
//
// isAllowedURL returns true for env var names that are known to contain
// non-secret URLs (public endpoints, app URLs, etc.).
func isAllowedURL(upper string) bool {
	switch upper {
	case "PUBLIC_URL", "WEBSITE_URL", "APP_URL", "HOMEPAGE_URL",
		"BASE_URL", "API_URL", "SITE_URL", "FRONTEND_URL",
		"BACKEND_URL", "SERVER_URL", "CLIENT_URL",
		"REASONIX_BASE_URL", "OPENCODE_API_URL":
		return true
	}
	return false
}

// isAllowedDSN returns true for env var names that are known to contain
// non-secret DSNs (public identifiers, not credentials).
func isAllowedDSN(upper string) bool {
	switch upper {
	case "SENTRY_DSN":
		return true
	}
	return false
}

func StripCredentialEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(k)
		if strings.HasSuffix(upper, "_KEY") ||
			strings.HasSuffix(upper, "_TOKEN") ||
			strings.HasSuffix(upper, "_SECRET") ||
			strings.HasSuffix(upper, "_PASSWORD") ||
			strings.HasSuffix(upper, "_PASS") ||
			strings.HasSuffix(upper, "_CREDENTIALS") ||
			strings.HasSuffix(upper, "_CREDENTIAL") ||
			strings.HasSuffix(upper, "_APIKEY") ||
			strings.HasSuffix(upper, "_APITOKEN") ||
			strings.HasSuffix(upper, "_ACCESS_KEY") ||
			strings.HasSuffix(upper, "_AUTH") ||
			strings.HasSuffix(upper, "_CERT") ||
			strings.HasSuffix(upper, "_SIGNATURE") ||
			strings.HasSuffix(upper, "_SIGNING_KEY") ||
			strings.HasSuffix(upper, "_SIGNKEY") ||
			strings.HasSuffix(upper, "_ENCRYPTION_KEY") ||
			strings.HasSuffix(upper, "_SESSION_KEY") ||
			strings.HasSuffix(upper, "_PRIVATE_KEY") ||
			strings.HasSuffix(upper, "_PRIVKEY") ||
			strings.HasSuffix(upper, "_API_SECRET") ||
			strings.HasSuffix(upper, "_CLIENT_SECRET") ||
			strings.HasSuffix(upper, "_MASTER_KEY") ||
			strings.HasSuffix(upper, "_REFRESH_TOKEN") ||
			strings.HasSuffix(upper, "_ACCESS_TOKEN") ||
			strings.HasSuffix(upper, "_BEARER_TOKEN") ||
			strings.HasSuffix(upper, "_SSH_KEY") ||
			strings.HasSuffix(upper, "_TLS_KEY") ||
			strings.HasSuffix(upper, "_OAUTH") ||
			strings.HasSuffix(upper, "_SALT") ||
			strings.HasSuffix(upper, "_PWD") ||
			strings.HasSuffix(upper, "_PAT") ||
			strings.HasSuffix(upper, "_JWT") ||
			strings.HasSuffix(upper, "_PASSPHRASE") ||
			(strings.HasSuffix(upper, "_DSN") && !isAllowedDSN(upper)) ||
			(strings.HasSuffix(upper, "_URI") && !isAllowedURL(upper)) ||
			(strings.HasSuffix(upper, "_URL") && !isAllowedURL(upper)) ||
			strings.HasSuffix(upper, "_KEY_ID") ||
			strings.HasSuffix(upper, "_ACCESS_KEY_ID") ||
			strings.HasSuffix(upper, "_CONNECTION_STRING") ||
			strings.HasSuffix(upper, "_CONNSTR") ||
			strings.HasPrefix(upper, "PG") && (strings.HasSuffix(upper, "PASSWORD") || strings.HasSuffix(upper, "PASS") || strings.HasSuffix(upper, "HOST") || strings.HasSuffix(upper, "DATABASE") || strings.HasSuffix(upper, "USER")) ||
			strings.HasPrefix(upper, "AWS_") && strings.HasSuffix(upper, "_ID") ||
			upper == "TOKEN" || upper == "SECRET" || upper == "PASSWORD" ||
			upper == "AUTHORIZATION" || upper == "BEARER" ||
			upper == "PGPASSWORD" || upper == "PGPASS" ||
			upper == "NPM_TOKEN" || upper == "GITHUB_TOKEN" ||
			upper == "GITLAB_TOKEN" || upper == "SLACK_TOKEN" ||
			upper == "DISCORD_TOKEN" || upper == "TELEGRAM_BOT_TOKEN" ||
			upper == "OPENAI_API_KEY" || upper == "ANTHROPIC_API_KEY" ||
			upper == "DEEPSEEK_API_KEY" || upper == "CF_TOKEN" ||
			upper == "CLOUDFLARE_API_TOKEN" || upper == "HF_TOKEN" ||
			upper == "HUGGINGFACE_TOKEN" || upper == "BOT_TOKEN" ||
			upper == "WEBHOOK_SECRET" || upper == "COOKIE" ||
			strings.HasSuffix(upper, "_COOKIE") ||
			strings.HasSuffix(upper, "_WEBHOOK") ||
			strings.HasSuffix(upper, "_BOT_TOKEN") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// SetEnvValue sets a key=value entry, replacing any existing entry with the
// same key. The new entry is placed at the position of the last existing
// occurrence, or appended if absent. When multiple entries with the same key
// exist (unusual but possible in a hand-crafted slice), only the last
// occurrence is replaced and duplicates are preserved.
func SetEnvValue(env []string, key, value string) []string {
	// Find the last occurrence index.
	lastIdx := -1
	for i := len(env) - 1; i >= 0; i-- {
		k, _, ok := strings.Cut(env[i], "=")
		if ok && envKeyEqual(k, key) {
			lastIdx = i
			break
		}
	}
	if lastIdx < 0 {
		// Not found — append.
		return append(append([]string(nil), env...), key+"="+value)
	}
	// Replace the last occurrence in-place.
	out := make([]string, len(env))
	copy(out, env)
	out[lastIdx] = key + "=" + value
	return out
}

// EnvValue returns the value of the last occurrence of key in env.
func EnvValue(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		k, v, ok := strings.Cut(env[i], "=")
		if ok && envKeyEqual(k, key) {
			return v, true
		}
	}
	return "", false
}

func envKeyEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// KeyEqual reports whether two env var names are equal (case-insensitive on
// Windows, case-sensitive elsewhere).
func KeyEqual(a, b string) bool { return envKeyEqual(a, b) }

// MergePathLists combines two PATH-style strings, deduplicating and preferring
// entries from primary.
func MergePathLists(primary, secondary string) string {
	var out []string
	seen := map[string]bool{}
	for _, path := range []string{primary, secondary} {
		for _, dir := range filepath.SplitList(path) {
			if dir == "" || seen[dir] {
				continue
			}
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return strings.Join(out, string(os.PathListSeparator))
}
