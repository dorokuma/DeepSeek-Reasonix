package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const multiPartSendGap = 80 * time.Millisecond

// typingRefreshInterval is how often we re-send sendChatAction(typing).
// Telegram clears the indicator after ~5s of inactivity.
const typingRefreshInterval = 4 * time.Second

// Telegram Bot API: max length of a single text message (UTF-8 code points / runes).
const telegramMaxMessageRunes = 4096

// telegramMaxFormattedRunes is the safe truncation limit for formatted text,
// reserving ~10% headroom for MarkdownV2 escaping expansion.
const telegramMaxFormattedRunes = 3700

// Hard cap on streamed reply size before finalize (OOM guard).
const maxFinalizeBytes = 512 << 10
const maxMediaSize = 50 << 20 // 50 MB max for media uploads
const telegramMaxCaptionRunes = 1024 // Telegram Bot API caption limit

// isMediaFilePath detects media file type from extension.
func isMediaFilePath(path string) (mediaType string, ok bool) {
	if idx := strings.LastIndex(path, "."); idx >= 0 {
		ext := strings.ToLower(path[idx:])
		if qi := strings.Index(ext, "?"); qi >= 0 {
			ext = ext[:qi]
		}
		switch ext {
		case ".jpg", ".jpeg", ".png", ".webp", ".bmp":
			return "photo", true
		case ".gif":
			return "animation", true
		case ".mp4", ".webm", ".mov", ".avi", ".mkv":
			return "video", true
		case ".mp3", ".ogg", ".wav", ".flac", ".m4a":
			return "audio", true
		}
	}
	return "", false
}

// sendNativeMedia sends a local file as Telegram native media.

// sendDocument sends a file as native Telegram document.

// extractAndSendMedia scans text for file paths and sends matching files as native media.
// Only files under the workdir or /tmp are sent to prevent information disclosure.

// isSafeMediaPath checks if a file path is safe to send as a Telegram attachment.
// Only files under the workdir or /tmp are allowed, preventing information disclosure
// of sensitive files like /etc/passwd, session data, or API keys.
// Resolves symlinks to prevent bypass via symlinked paths.

// splitTelegramText splits s into chunks of at most maxRunes, preferring line breaks.
func splitTelegramText(s string, maxRunes int) []string {
	if maxRunes <= 0 {
		maxRunes = telegramMaxMessageRunes
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return []string{s}
	}
	var parts []string
	for start := 0; start < len(runes); {
		remain := len(runes) - start
		if remain <= maxRunes {
			parts = append(parts, string(runes[start:]))
			break
		}
		end := start + maxRunes
		window := runes[start:end]
		cut := len(window)
		// Prefer breaking at newline in the last 25% of the chunk.
		searchFrom := cut * 3 / 4
		breakAt := -1
		for i := cut - 1; i >= searchFrom; i-- {
			if window[i] == '\n' {
				breakAt = i
				break
			}
		}
		if breakAt > 0 {
			parts = append(parts, string(window[:breakAt+1]))
			start += breakAt + 1
			continue
		}
		parts = append(parts, string(window))
		start = end
	}
	return parts
}

// telegramPreviewTail returns the last maxRunes of text for draft preview (no cut marker).
func telegramPreviewTail(text string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = telegramMaxMessageRunes
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[len(runes)-maxRunes:])
}

// logPreview shortens text for logs (no user-visible [cut] marker).
func logPreview(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return fmt.Sprintf("[%d bytes] %q", len(s), s)
	}
	truncated := trimUTF8Bytes(s, maxBytes)
	return fmt.Sprintf("[%d bytes] %q...", len(s), truncated)
}

// trimUTF8Bytes trims s to at most maxBytes without breaking a UTF-8 code point.
// Uses DecodeLastRuneInString for O(1) rune-boundary finding instead of
// byte-by-byte retry with O(n) ValidString on each iteration.
func trimUTF8Bytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	b := []byte(s[:maxBytes])
	for len(b) > 0 {
		if utf8.Valid(b) {
			return string(b)
		}
		_, size := utf8.DecodeLastRuneInString(string(b))
		b = b[:len(b)-size]
	}
	return string(b)
}

// truncateForButton ensures text fits Telegram's 64-byte inline keyboard button text limit.
func truncateForButton(text string) string {
	const maxBtnBytes = 64
	if len(text) <= maxBtnBytes {
		return text
	}
	return trimUTF8Bytes(text, maxBtnBytes-3) + "…"
}

func telegramErrorIsNotModified(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "message is not modified")
}

func telegramErrorIsMessageTooLong(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "MESSAGE_TOO_LONG")
}

func telegramErrorIsParseEntities(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "can't parse entities") ||
		strings.Contains(s, "cant parse entities") ||
		strings.Contains(s, "can't find end tag")
}

func telegramErrorIsFlood(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "retry after") ||
		strings.Contains(s, "flood") ||
		strings.Contains(s, "too many requests")
}

// telegramEditOK reports whether an edit failure can be treated as success.
func telegramEditOK(err error) bool {
	return err == nil || telegramErrorIsNotModified(err)
}

// streamContinuationText returns the portion of final not already visible in the
// streamed preview. When the preview was a tail slice, final does not start with
// visiblePrefix — return the full final so fallback send delivers the answer.
func streamContinuationText(final, visiblePrefix string) string {
	final = strings.TrimSpace(final)
	visiblePrefix = strings.TrimSpace(visiblePrefix)
	if final == "" {
		return ""
	}
	if visiblePrefix == "" {
		return final
	}
	if strings.HasPrefix(final, visiblePrefix) {
		return strings.TrimSpace(final[len(visiblePrefix):])
	}
	return final
}

// capTelegramMessage trims text to fit one Telegram message, reserving ~10%
// headroom for MarkdownV2 escaping expansion (leaves 3700 of the 4096 limit).
func capTelegramMessage(text string) string {
	const safeMaxRunes = 3700
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= safeMaxRunes {
		return text
	}
	const suffix = "\n…（已截断）"
	suffixRunes := len([]rune(suffix))
	if safeMaxRunes <= suffixRunes {
		return string(runes[:safeMaxRunes])
	}
	return string(runes[:safeMaxRunes-suffixRunes]) + suffix
}

// newMessage creates a MessageConfig with link preview disabled (Hermes parity).
func newMessage(chatID int64, text string) MessageConfig {
	msg := NewMessage(chatID, text)
	msg.DisableWebPagePreview = true
	return msg
}

// sendTextParts delivers text as one or more Telegram messages (≤4096 runes each).
// Tries Telegram MarkdownV2 first; on entity-parse failure retries as plain text
// (with the MDV2 escape backslashes and formatting markers stripped via _stripMdv2,
// Hermes pattern). If editFirstMsgID != nil and *editFirstMsgID > 0, the first
// part updates that message.





// editOverflowSplit edits the first chunk in-place, then sends continuations as
// reply-threaded messages (Hermes Telegram _edit_overflow_split, lightweight).


// sendWithRetry sends any Chattable with retry for flood/network errors.


// messageContentKey returns a string uniquely identifying the content of a
// Chattable message for dedup hashing. Returns "" for types where dedup is
// not applicable (media, documents, etc.).
func messageContentKey(msg Chattable) string {
	switch v := msg.(type) {
	case MessageConfig:
		return "text:" + v.Text
	case EditMessageTextConfig:
		return fmt.Sprintf("edit:%d:%s", v.MessageID, v.Text)
	default:
		// For other types (media, documents, etc.), use the full struct
		// representation as a conservative fallback.
		return fmt.Sprintf("%T:%+v", msg, msg)
	}
}

// tryRichMessage attempts to send text via sendRichMessage with raw markdown.
// If editMsgID > 0, it edits the existing message via editMessageText with rich_message.
// Returns the message ID on success, 0 on failure. For edits, returns editMsgID[0].

// marshalAPI JSON-encodes v, returning an empty string on failure with the error.
func marshalAPI(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func telegramPreviewHead(text string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = telegramMaxMessageRunes
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes])
}
