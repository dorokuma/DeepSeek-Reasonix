package main

import (
	"io"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
)

// Patterns that may appear in third-party library logs (e.g. telegram-bot-api
// log.Println(err) with a full api.telegram.org/bot<token>/... URL) even when
// our own call sites already call redactSecrets.
var (
	// Full Telegram Bot API URL path including token.
	reTelegramBotURL = regexp.MustCompile(`(?i)(https?://api\.telegram\.org/bot)[^\s"'\\]+`)
	// bot<id>:<secret> as seen in URLs and error strings.
	reBotTokenInLog = regexp.MustCompile(`(?i)bot[0-9]{5,}:[A-Za-z0-9_-]{20,}`)
	// Bare token without "bot" prefix (id:secret).
	reBareBotToken = regexp.MustCompile(`\b[0-9]{6,}:[A-Za-z0-9_-]{20,}\b`)
	reJinaKeyInLog = regexp.MustCompile(`(?i)\bjina_[A-Za-z0-9]{16,}\b`)
)

// globalLogSecrets is updated after loadConfig; the log writer always reads the
// latest list so early install (pattern-only) and post-config install both work.
var (
	globalLogSecretsMu sync.RWMutex
	globalLogSecrets   []string
)

// installSecretLogRedaction routes the standard logger through a writer that
// strips known secrets and common secret shapes. Safe to call more than once;
// later calls refresh the secret list.
func installSecretLogRedaction(secrets []string) {
	sec := make([]string, 0, len(secrets))
	for _, v := range secrets {
		if v = strings.TrimSpace(v); v != "" {
			sec = append(sec, v)
		}
	}
	globalLogSecretsMu.Lock()
	globalLogSecrets = sec
	globalLogSecretsMu.Unlock()
	// Always re-bind so nothing can replace our writer later without us noticing
	// on the next install; first call installs the filter for package log.
	log.SetOutput(secretLogWriter{w: os.Stderr})
}

func currentLogSecrets() []string {
	globalLogSecretsMu.RLock()
	defer globalLogSecretsMu.RUnlock()
	if len(globalLogSecrets) == 0 {
		return nil
	}
	out := make([]string, len(globalLogSecrets))
	copy(out, globalLogSecrets)
	return out
}

// secretLogWriter redacts before writing. Write always reports len(p) on success
// so the log package does not treat a shorter redacted line as a short write.
type secretLogWriter struct {
	w io.Writer
}

func (s secretLogWriter) Write(p []byte) (int, error) {
	out := redactSecrets(string(p), currentLogSecrets())
	if _, err := io.WriteString(s.w, out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// scrubSecretPatterns removes well-known secret shapes that may not be in the
// explicit secrets list (or may appear partially encoded).
func scrubSecretPatterns(s string) string {
	s = reTelegramBotURL.ReplaceAllString(s, "${1}***/")
	s = reBotTokenInLog.ReplaceAllString(s, "bot***:***")
	s = reBareBotToken.ReplaceAllString(s, "***:***")
	s = reJinaKeyInLog.ReplaceAllString(s, "jina_***")
	return s
}
