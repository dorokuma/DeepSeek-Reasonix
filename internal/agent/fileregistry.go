package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
)

type sessionKey struct{}

// WithSession associates a Session with the context.
func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// SessionFromContext retrieves the Session from context.
func SessionFromContext(ctx context.Context) *Session {
	s, _ := ctx.Value(sessionKey{}).(*Session)
	return s
}

// FileStateRegistry tracks reads and writes for sessions.
type FileStateRegistry struct {
	mu     sync.RWMutex
	reads  map[*Session]map[string]bool
	writes map[*Session]map[string]bool
}

var globalFileStateRegistry = &FileStateRegistry{
	reads:  make(map[*Session]map[string]bool),
	writes: make(map[*Session]map[string]bool),
}

// RecordRead records a read for a session.
func (r *FileStateRegistry) RecordRead(sess *Session, path string) {
	if sess == nil || path == "" {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reads[sess] == nil {
		r.reads[sess] = make(map[string]bool)
	}
	r.reads[sess][abs] = true
}

// RecordWrite records a write for a session.
func (r *FileStateRegistry) RecordWrite(sess *Session, path string) {
	if sess == nil || path == "" {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writes[sess] == nil {
		r.writes[sess] = make(map[string]bool)
	}
	r.writes[sess][abs] = true
}

// GetReads returns all read paths.
func (r *FileStateRegistry) GetReads(sess *Session) []string {
	if sess == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var paths []string
	for p := range r.reads[sess] {
		paths = append(paths, p)
	}
	return paths
}

// GetWrites returns all written paths.
func (r *FileStateRegistry) GetWrites(sess *Session) []string {
	if sess == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var paths []string
	for p := range r.writes[sess] {
		paths = append(paths, p)
	}
	return paths
}

// TryExtractPath extracts path or target_file from args.
func TryExtractPath(args json.RawMessage) string {
	var p struct {
		Path       string `json:"path"`
		TargetFile string `json:"target_file"`
	}
	if err := json.Unmarshal(args, &p); err == nil {
		if p.Path != "" {
			return p.Path
		}
		if p.TargetFile != "" {
			return p.TargetFile
		}
	}
	return ""
}
