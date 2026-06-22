// Package agent wires a Provider, a tool Registry, and a Session into the
// harness loop that drives a coding task to completion.
package agent

import (
	"fmt"
	"regexp"
	"sync"

	"reasonix/internal/provider"
)

// Session holds the conversation history for one task. The run loop (one turn at
// a time) is the only writer, but a frontend can read History/Save from another
// goroutine while a turn appends, so mu guards Messages. Direct Messages reads on
// the run-loop goroutine stay lock-free (serial with its own writes); cross-
// goroutine access goes through Snapshot.
type Session struct {
	mu             sync.RWMutex
	Messages       []provider.Message
	rewriteVersion int // bumped each time the log is rewritten (compact/fold)
}

// NewSession initializes a session with an optional system prompt.
func NewSession(system string) *Session {
	s := &Session{}
	if system != "" {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleSystem, Content: system})
	}
	return s
}

// Add appends a message.
func (s *Session) Add(m provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, m)
}

// AddUserNudge appends content to the last message if it's a user message,
// otherwise adds a new user message.
func (s *Session) AddUserNudge(content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.Messages)
	if n > 0 && s.Messages[n-1].Role == provider.RoleUser {
		s.Messages[n-1].Content += "\n\n" + content
	} else {
		s.Messages = append(s.Messages, provider.Message{
			Role:    provider.RoleUser,
			Content: content,
		})
	}
}

// Replace swaps the whole message log — used by compaction, which rewrites the
// middle of the history.
func (s *Session) Replace(msgs []provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = msgs
}

// Snapshot returns a copy of the messages, safe to read from another goroutine
// while a turn appends. Frontends (History, Save) use it instead of touching the
// live slice.
func (s *Session) Snapshot() []provider.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]provider.Message(nil), s.Messages...)
}

// RewriteVersion returns the current rewrite version.
func (s *Session) RewriteVersion() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rewriteVersion
}

// IncrementRewrite bumps the rewrite version by 1.
func (s *Session) IncrementRewrite() {
	s.mu.Lock()
	s.rewriteVersion++
	s.mu.Unlock()
}

// HasContent returns true when the session carries at least one user,
// assistant, or tool message — i.e. more than just a system prompt. An
// "empty" conversation that has never been used should not be persisted.
func (s *Session) HasContent() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.Messages {
		if m.Role != provider.RoleSystem {
			return true
		}
	}
	return false
}

// Fragment represents a tagged, replaceable content block within a message.
type Fragment struct {
	ID      string
	Type    string // "config", "state", "code-context"
	Content string
}

var fragmentRegex = regexp.MustCompile(`(?s)<fragment\s+id="([^"]+)"\s+type="([^"]+)">(.*?)</fragment>`)

// fragmentReplaceCache caches compiled replacement regexps keyed by fragment ID,
// avoiding regexp.MustCompile on every iteration of CalculateDiffAndFilter.
// NOTE: unbounded; a production implementation should add an eviction strategy.
var fragmentReplaceCache sync.Map

// fragmentReplacePattern returns a compiled regex for replacing a specific
// fragment tag. The pattern matches the full <fragment ...>...</fragment>
// block for the given ID.
func fragmentReplacePattern(fragID string) *regexp.Regexp {
	if cached, ok := fragmentReplaceCache.Load(fragID); ok {
		return cached.(*regexp.Regexp)
	}
	re := regexp.MustCompile(fmt.Sprintf(`(?s)<fragment\s+id="%s"\s+type="[^"]+">(.*?)</fragment>`, regexp.QuoteMeta(fragID)))
	fragmentReplaceCache.Store(fragID, re)
	return re
}

// ExtractFragments scans a message content string and returns all parsed fragments.
func ExtractFragments(content string) []Fragment {
	matches := fragmentRegex.FindAllStringSubmatch(content, -1)
	var frags []Fragment
	for _, m := range matches {
		if len(m) == 4 {
			frags = append(frags, Fragment{
				ID:      m[1],
				Type:    m[2],
				Content: m[3],
			})
		}
	}
	return frags
}

// CalculateDiffAndFilter replaces already-sent fragments in currentMsgs with
// lightweight <fragment-ref> tags, comparing against the previouslySent map.
// previouslySent tracks fragment ID → last-sent content. The returned messages
// are a transient copy — the caller must NOT persist them back to the Session.
func CalculateDiffAndFilter(currentMsgs []provider.Message, previouslySent map[string]string) ([]provider.Message, map[string]string) {
	nextSent := make(map[string]string, len(previouslySent))
	for k, v := range previouslySent {
		nextSent[k] = v
	}

	filtered := make([]provider.Message, len(currentMsgs))
	copy(filtered, currentMsgs)

	for i := range filtered {
		content := filtered[i].Content

		// 1. Process regular string content
		fragments := ExtractFragments(content)
		for _, frag := range fragments {
			lastVal, exists := nextSent[frag.ID]
			if exists && lastVal == frag.Content {
				refTag := fmt.Sprintf(`<fragment-ref id="%s" status="unchanged" />`, frag.ID)
				content = fragmentReplacePattern(frag.ID).ReplaceAllString(content, refTag)
			} else {
				nextSent[frag.ID] = frag.Content
			}
		}

		// 2. Process multimodal parts (text-type only)
		var filteredParts []provider.ContentPart
		if len(filtered[i].Parts) > 0 {
			filteredParts = make([]provider.ContentPart, len(filtered[i].Parts))
			for j, part := range filtered[i].Parts {
				if part.Type == provider.PartTypeText {
					partContent := part.Text
					frags := ExtractFragments(partContent)
					for _, frag := range frags {
						lastVal, exists := nextSent[frag.ID]
						if exists && lastVal == frag.Content {
							refTag := fmt.Sprintf(`<fragment-ref id="%s" status="unchanged" />`, frag.ID)
							partContent = fragmentReplacePattern(frag.ID).ReplaceAllString(partContent, refTag)
						} else {
							nextSent[frag.ID] = frag.Content
						}
					}
					part.Text = partContent
				}
				filteredParts[j] = part
			}
		}

		filtered[i].Content = content
		filtered[i].Parts = filteredParts
	}

	return filtered, nextSent
}
