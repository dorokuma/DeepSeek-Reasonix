package ctxmode

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"reasonix/internal/config"
)

// Entry metadata for one stored tool result.
type Entry struct {
	ID      string `json:"id"`
	Tool    string `json:"tool"`
	Subject string `json:"subject,omitempty"`
	Bytes   int    `json:"bytes"`
	Lines   int    `json:"lines"`
}

type stored struct {
	meta Entry
	body string // in-memory fallback when cache dir unavailable
	path string // on-disk body path when set
}

// Store holds sandboxed tool output for one agent session.
type Store struct {
	mu      sync.Mutex
	dir     string
	next    int
	data    map[string]stored
	indexed map[string]string // Key: relative path, Value: file content
	journal *Journal
}

// NewStore creates a session-local store under the reasonix cache dir.
func NewStore() *Store {
	s := &Store{
		data:    map[string]stored{},
		indexed: map[string]string{},
	}
	base := config.CacheDir()
	if base != "" {
		var slug [8]byte
		if _, err := rand.Read(slug[:]); err != nil {
			slog.Warn("ctx store slug rand failed; using pid fallback", "err", err)
			p := uint64(os.Getpid())
			for i := range slug {
				slug[i] = byte(p >> (8 * i))
			}
		} else {
			// Mix pid to reduce cross-process slug collision risk even on good rand
			// (addresses weak 8-byte naming without changing dir name length or prune regex).
			p := uint64(os.Getpid())
			for i := range slug {
				slug[i] ^= byte(p >> (8 * i))
			}
		}
		s.dir = filepath.Join(base, "ctxmode", hex.EncodeToString(slug[:]))
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			slog.Warn("ctx store mkdir failed; falling back to memory", "dir", s.dir, "err", err)
			s.dir = ""
		} else {
			markCacheAlive(s.dir)
			_, _ = PruneOrphanCache()
		}
	}
	if j, err := openJournal(s.dir); err != nil {
		LogJournalErr("open", err)
	} else {
		s.journal = j
	}
	return s
}

// Journal returns the session event index (may be nil).
func (s *Store) Journal() *Journal {
	if s == nil {
		return nil
	}
	return s.journal
}

// Put saves body and returns the new ref id (e.g. ctx-1).
func (s *Store) Put(tool, subject, body string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("context store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := fmt.Sprintf("ctx-%d", s.next)
	lines := 0
	if body == "" {
		lines = 0
	} else {
		lines = strings.Count(body, "\n") + 1
	}
	st := stored{
		meta: Entry{
			ID:      id,
			Tool:    tool,
			Subject: subject,
			Bytes:   len(body),
			Lines:   lines,
		},
		body: body,
	}
	if s.dir != "" {
		path := filepath.Join(s.dir, id+".txt")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return "", err
		}
		st.path = path
		st.body = ""
		meta, _ := json.Marshal(st.meta)
		if err := os.WriteFile(filepath.Join(s.dir, id+".json"), meta, 0o600); err != nil {
			slog.Error("ctxmode: write meta file, entry not reloadable after restart", "id", id, "err", err)
		}
	}
	s.data[id] = st
	return id, nil
}

func (s *Store) loadBody(id string) (string, Entry, error) {
	s.mu.Lock()
	st, ok := s.data[id]
	s.mu.Unlock()
	if !ok {
		return "", Entry{}, fmt.Errorf("unknown ref %q", id)
	}
	if st.body != "" {
		return st.body, st.meta, nil
	}
	if st.path == "" {
		return "", st.meta, fmt.Errorf("ref %q has no stored body", id)
	}
	b, err := os.ReadFile(st.path)
	if err != nil {
		return "", st.meta, err
	}
	return string(b), st.meta, nil
}

// Read returns a slice of lines from a stored ref (0-based offset, max limit lines).
func (s *Store) Read(id string, offset, limit int) (string, error) {
	body, ent, err := s.loadBody(id)
	if err != nil {
		return "", err
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 80
	}
	if limit > 200 {
		limit = 200
	}
	lines := strings.Split(body, "\n")
	if offset >= len(lines) {
		return fmt.Sprintf("[ctx] ref=%s (%s, %d lines): offset %d past end\n", id, ent.Tool, ent.Lines, offset), nil
	}
	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[ctx] ref=%s tool=%s subject=%q lines=%d showing %d-%d\n\n",
		id, ent.Tool, ent.Subject, ent.Lines, offset+1, end)
	for i := offset; i < end; i++ {
		fmt.Fprintf(&b, "%5d→%s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "\n… %d more lines — ctx_read(ref=%q, offset=%d, limit=%d)\n", len(lines)-end, id, end, limit)
	}
	return b.String(), nil
}

// Search finds lines containing pattern (case-sensitive substring) in a stored ref.
func (s *Store) Search(id, pattern string, limit int) (string, error) {
	body, ent, err := s.loadBody(id)
	if err != nil {
		return "", err
	}
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if limit <= 0 {
		limit = 40
	}
	if limit > 100 {
		limit = 100
	}
	lines := strings.Split(body, "\n")
	var b strings.Builder
	matches := 0
	fmt.Fprintf(&b, "[ctx] search ref=%s pattern=%q\n\n", id, pattern)
	for i, line := range lines {
		if !strings.Contains(line, pattern) {
			continue
		}
		fmt.Fprintf(&b, "%5d→%s\n", i+1, line)
		matches++
		if matches >= limit {
			fmt.Fprintf(&b, "\n… truncated at %d matches — narrow pattern or ctx_read pages\n", limit)
			break
		}
	}
	if matches == 0 {
		fmt.Fprintf(&b, "(no matches in %s, %d lines)\n", ent.Tool, ent.Lines)
	}
	return b.String(), nil
}

// Remove deletes on-disk artefacts for this session store.
func (s *Store) Remove() {
	if s == nil {
		return
	}
	if s.journal != nil {
		s.journal.Close()
		s.journal = nil
	}
	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
}

// IndexFile reads a single file and stores it in the session-local store.
func (s *Store) IndexFile(relPath, absPath string) error {
	if s == nil {
		return fmt.Errorf("context store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(absPath)
	if err != nil {
		return err
	}
	s.indexed[relPath] = string(data)
	return nil
}

// SearchGlobal searches for pattern in all indexed files, returning matching snippets.
func (s *Store) SearchGlobal(pattern string, limit int) (string, error) {
	if s == nil {
		return "", fmt.Errorf("context store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var results []string
	patternLower := strings.ToLower(pattern)

	var paths []string
	for p := range s.indexed {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	totalMatches := 0
	for _, rel := range paths {
		content := s.indexed[rel]
		if !strings.Contains(strings.ToLower(content), patternLower) {
			continue
		}
		lines := strings.Split(content, "\n")
		var snippet []string
		matchedInFile := 0
		for idx, line := range lines {
			if strings.Contains(strings.ToLower(line), patternLower) {
				snippet = append(snippet, fmt.Sprintf("%5d→%s", idx+1, line))
				matchedInFile++
				totalMatches++
				if matchedInFile >= 5 {
					snippet = append(snippet, "     …")
					break
				}
			}
		}
		results = append(results, fmt.Sprintf("Matches in file %s:\n%s", rel, strings.Join(snippet, "\n")))
	}

	if len(results) == 0 {
		return fmt.Sprintf("No matches found for %q", pattern), nil
	}

	resText := strings.Join(results, "\n\n")
	if len(resText) > 40000 {
		resText = resText[:40000] + "\n\n... (truncated search results)"
	}
	return resText, nil
}