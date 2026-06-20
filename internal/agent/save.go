package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"reasonix/internal/fileutil"
	"reasonix/internal/provider"
	"reasonix/internal/sessioncrypt"
)

// SessionEncryptionEnabled controls whether session files are encrypted at rest.
// Set via [agent] encrypt_sessions in config. Default false for backward compat.
var SessionEncryptionEnabled bool

// Save writes the session's messages to path in JSONL — one provider.Message
// per line — so a user can resume the conversation later. When
// SessionEncryptionEnabled is true, the file is AES-256-GCM encrypted at rest.
func (s *Session) Save(path string) error {
	if path == "" {
		return fmt.Errorf("empty session path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	// Write to a sibling tmp file then rename, so a crash mid-write can't
	// leave a partial file that won't reload.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session.*.tmp")
	if err != nil {
		return fmt.Errorf("create session tmp: %w", err)
	}
	tmpPath := tmp.Name()

	if SessionEncryptionEnabled {
		var plaintext []byte
		enc := json.NewEncoder(&plaintextBuffer{&plaintext})
		for _, m := range s.Snapshot() {
			if err := enc.Encode(m); err != nil {
				tmp.Close()
				os.Remove(tmpPath)
				return fmt.Errorf("encode message: %w", err)
			}
		}
		encrypted, err := sessioncrypt.Encrypt(plaintext)
		if err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("encrypt session: %w", err)
		}
		if _, err := tmp.Write(encrypted); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("write encrypted session: %w", err)
		}
	} else {
		enc := json.NewEncoder(tmp)
		for _, m := range s.Snapshot() { // copy under the lock — a turn may be appending
			if err := enc.Encode(m); err != nil {
				tmp.Close()
				os.Remove(tmpPath)
				return fmt.Errorf("encode message: %w", err)
			}
		}
	}

	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return fileutil.ReplaceFile(tmpPath, path)
}

// LoadSession reads a JSONL file written by Save into a fresh Session value.
// Missing files surface as os.IsNotExist so callers can fall through to a
// new session. It auto-detects encrypted files and decrypts transparently.
func LoadSession(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if sessioncrypt.IsEncrypted(data) {
		plain, err := sessioncrypt.Decrypt(data)
		if err != nil {
			return nil, fmt.Errorf("decrypt %s: %w", path, err)
		}
		data = plain
	}

	s := &Session{}
	// Decode a stream of JSON values rather than scanning lines: a single
	// message (e.g. a multi-MiB bash output) can exceed any line-buffer cap, and
	// Save's json.Encoder has no such limit — a Scanner here made sessions that
	// saved fine fail to reload.
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var m provider.Message
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		s.Messages = append(s.Messages, m)
	}
	return s, nil
}

// SessionInfo summarises a saved session for the --resume picker: where it is on
// disk, when it was created/last active, the first user message as a preview, and
// a rough turn count.
type SessionInfo struct {
	Path           string
	CreatedAt      time.Time
	LastActivityAt time.Time
	ModTime        time.Time // compatibility alias for LastActivityAt
	Preview        string
	Turns          int
	Scope          string
	WorkspaceRoot  string
	TopicID        string
	TopicTitle     string
}

// ListSessions returns every *.jsonl session under dir, most-recently-active
// first, each with a preview line so the picker can show something the user
// recognises. A missing directory is not an error — it just means there's
// nothing to resume yet.
func ListSessions(dir string) ([]SessionInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []SessionInfo
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		full := filepath.Join(dir, e.Name())
		preview, turns := previewSession(full)
		if turns == 0 {
			// Skip sessions that have never had user interaction — they are
			// empty conversations that should not appear in the history panel
			// or the resume picker.
			continue
		}
		createdAt := info.ModTime()
		lastActivityAt := info.ModTime()
		scope := "global"
		workspaceRoot := ""
		topicID := ""
		topicTitle := ""
		if meta, ok, err := LoadBranchMeta(full); err == nil && ok {
			if !meta.CreatedAt.IsZero() {
				createdAt = meta.CreatedAt
			}
			if !meta.UpdatedAt.IsZero() {
				lastActivityAt = meta.UpdatedAt
			}
			scope = meta.DefaultScope()
			workspaceRoot = meta.WorkspaceRoot
			topicID = meta.TopicID
			topicTitle = meta.TopicTitle
		}
		out = append(out, SessionInfo{
			Path:           full,
			CreatedAt:      createdAt,
			LastActivityAt: lastActivityAt,
			ModTime:        lastActivityAt,
			Preview:        preview,
			Turns:          turns,
			Scope:          scope,
			WorkspaceRoot:  workspaceRoot,
			TopicID:        topicID,
			TopicTitle:     topicTitle,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastActivityAt.Equal(out[j].LastActivityAt) {
			return out[i].Path < out[j].Path
		}
		return out[i].LastActivityAt.After(out[j].LastActivityAt)
	})
	return out, nil
}

// plaintextBuffer is a bytes.Buffer-like type that implements io.Writer for
// json.Encoder, writing into a []byte without the allocation overhead of bytes.Buffer.
type plaintextBuffer struct{ dst *[]byte }

func (b *plaintextBuffer) Write(p []byte) (int, error) {
	*b.dst = append(*b.dst, p...)
	return len(p), nil
}

// previewSession returns the first user message (truncated) and the number of
// user-role messages so the picker can show "5 turns · 'help me debug the…'".
// Errors are swallowed — a malformed file just shows up with an empty preview.
func previewSession(path string) (string, int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0
	}
	if sessioncrypt.IsEncrypted(data) {
		plain, err := sessioncrypt.Decrypt(data)
		if err != nil {
			return "", 0
		}
		data = plain
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	first := ""
	turns := 0
	for {
		var m provider.Message
		if err := dec.Decode(&m); err != nil {
			break // EOF or a malformed tail — return the preview gathered so far
		}
		if m.Role == provider.RoleUser {
			turns++
			if first == "" {
				s := strings.TrimSpace(m.Content)
				if r := []rune(s); len(r) > 80 {
					s = string(r[:77]) + "…"
				}
				first = s
			}
		}
	}
	return first, turns
}

// ContinueSessionPath returns where a conversation carried into a rebuilt
// controller (model switch, config change) should keep auto-saving: its existing
// file when it has one, so the continued session stays a single file instead of
// the old one being orphaned as an identical duplicate (#2807). A session with no
// file yet gets a fresh path; "" when persistence is disabled.
func ContinueSessionPath(prevPath, dir, model string) string {
	if prevPath != "" {
		return prevPath
	}
	if dir == "" {
		return ""
	}
	return NewSessionPath(dir, model)
}

// NewSessionPath returns the path to use for a fresh session, namespaced by
// the model so the filename hints at what the conversation was with. dir is
// typically config.SessionDir().
func NewSessionPath(dir, model string) string {
	safe := strings.NewReplacer("/", "-", "\\", "-").Replace(model)
	if safe == "" {
		safe = "session"
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%s.jsonl", time.Now().UTC().Format("20060102-150405.000000000"), safe))
}

// SessionOrderInfo holds basic session metadata for sorting.
type SessionOrderInfo struct {
	Path           string
	LastActivityAt time.Time
}

// ListSessionOrder lists visible sessions in dir, ordered by modification time desc.
func ListSessionOrder(dir string) ([]SessionOrderInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []SessionOrderInfo
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, SessionOrderInfo{
			Path:           filepath.Join(dir, e.Name()),
			LastActivityAt: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastActivityAt.After(out[j].LastActivityAt)
	})
	return out, nil
}

// SessionPreview returns the preview string and turn count for a session.
func SessionPreview(path string) (string, int) {
	return previewSession(path)
}

// IsCleanupPending returns true if the session is marked for deletion.
func IsCleanupPending(path string) bool {
	return false
}
