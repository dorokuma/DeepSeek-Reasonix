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
// inherited process environment. Callers should declare needed credentials
// explicitly instead of relying on inheritance.
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
			upper == "TOKEN" || upper == "SECRET" || upper == "PASSWORD" ||
			upper == "AUTHORIZATION" || upper == "BEARER" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// SetEnvValue sets a key=value entry, replacing any existing entry with the
// same key. The new entry is placed at the position of the last existing
// occurrence, or appended if absent.
func SetEnvValue(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if ok && envKeyEqual(k, key) {
			if !replaced {
				out = append(out, key+"="+value)
				replaced = true
			}
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, key+"="+value)
	}
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
