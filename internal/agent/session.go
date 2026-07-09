// Package agent wires a Provider, a tool Registry, and a Session into the
// harness loop that drives a coding task to completion.
package agent

import (
	"fmt"
	"regexp"
	"strings"
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

// ToolCallIDForStartedTaskLine finds the tool call id for a started-task placeholder. Fallback when job meta was lost.
func (s *Session) ToolCallIDForStartedTaskLine(jobID string) string {
	if jobID == "" {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.Messages) - 1; i >= 0; i-- {
		m := s.Messages[i]
		if m.Role == provider.RoleTool && m.ToolCallID != "" && TaskToolContentReferencesJob(m.Content, jobID) {
			return m.ToolCallID
		}
	}
	return ""
}

// ToolNameForCallID returns the tool name associated with a tool_call_id
// (from an existing tool result row or the assistant tool call). Empty if unknown.
func (s *Session) ToolNameForCallID(toolCallID string) string {
	if s == nil || toolCallID == "" {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.Messages) - 1; i >= 0; i-- {
		m := s.Messages[i]
		if m.Role == provider.RoleTool && m.ToolCallID == toolCallID && m.Name != "" {
			return m.Name
		}
	}
	for i := len(s.Messages) - 1; i >= 0; i-- {
		m := s.Messages[i]
		if m.Role != provider.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.ID == toolCallID && tc.Name != "" {
				return tc.Name
			}
		}
	}
	return ""
}

// HasBackgroundTaskDelivery reports whether the synthetic completion turn for jobID
// is already in the session (assistant tool_calls + tool result at the tail).
func (s *Session) HasBackgroundTaskDelivery(jobID string) bool {
	if s == nil || jobID == "" {
		return false
	}
	deliveryID := BackgroundDeliveryCallID(jobID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.Messages) - 1; i >= 0; i-- {
		m := s.Messages[i]
		if m.Role == provider.RoleTool && m.ToolCallID == deliveryID {
			return true
		}
		if m.Role == provider.RoleAssistant {
			for _, tc := range m.ToolCalls {
				if tc.ID == deliveryID {
					return true
				}
			}
		}
	}
	return false
}

// AppendBackgroundTaskDelivery appends a properly paired completion turn at the
// conversation tail. The original Started stub is left untouched — it already
// answered the spawn tool_call. Completion is a new tool round the model sees last.
func (s *Session) AppendBackgroundTaskDelivery(jobID, toolName, output string) bool {
	if s == nil || jobID == "" || strings.TrimSpace(output) == "" {
		return false
	}
	if s.HasBackgroundTaskDelivery(jobID) {
		return true
	}
	if toolName == "" {
		toolName = "task"
	}
	const max = 12000
	if len(output) > max {
		output = output[:max] + "\n…[truncated]"
	}
	deliveryID := BackgroundDeliveryCallID(jobID)
	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check under write lock.
	for i := len(s.Messages) - 1; i >= 0; i-- {
		m := s.Messages[i]
		if m.Role == provider.RoleTool && m.ToolCallID == deliveryID {
			return true
		}
	}
	s.Messages = append(s.Messages,
		provider.Message{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{
				ID:        deliveryID,
				Name:      toolName,
				Arguments: FormatCompletedTaskCallArgs(jobID),
			}},
		},
		provider.Message{
			Role:       provider.RoleTool,
			Name:       toolName,
			ToolCallID: deliveryID,
			Content:    output,
		},
	)
	return true
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
// A sync.RWMutex guards the map; when the cache exceeds maxFragReplaceEntries
// the map is cleared entirely — simple, correct, and avoids unbounded growth.
const maxFragReplaceEntries = 256

var (
	fragmentReplaceCache   = make(map[string]*regexp.Regexp, 64)
	fragmentReplaceCacheMu sync.RWMutex
)

// fragmentReplacePattern returns a compiled regex for replacing a specific
// fragment tag. The pattern matches the full <fragment ...>...</fragment>
// block for the given ID.
func fragmentReplacePattern(fragID string) *regexp.Regexp {
	fragmentReplaceCacheMu.RLock()
	cached, ok := fragmentReplaceCache[fragID]
	fragmentReplaceCacheMu.RUnlock()
	if ok {
		return cached
	}

	re := regexp.MustCompile(fmt.Sprintf(`(?s)<fragment\s+id="%s"\s+type="[^"]+">(.*?)</fragment>`, regexp.QuoteMeta(fragID)))

	fragmentReplaceCacheMu.Lock()
	// Double-check after acquiring write lock.
	if cached, ok := fragmentReplaceCache[fragID]; ok {
		fragmentReplaceCacheMu.Unlock()
		return cached
	}
	if len(fragmentReplaceCache) >= maxFragReplaceEntries {
		// Evict all to keep memory bounded. The next call to CalculateDiffAndFilter
		// will repopulate the cache with whatever fragment IDs appear in the
		// current message snapshot.
		fragmentReplaceCache = make(map[string]*regexp.Regexp, 64)
	}
	fragmentReplaceCache[fragID] = re
	fragmentReplaceCacheMu.Unlock()
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
// previouslySent tracks fragment ID → last-sent content. The caller must pass a
// copy (e.g. from Snapshot) — currentMsgs is modified in-place to avoid an extra
// allocation. The returned messages are the same slice; the caller must NOT
// persist them back to the Session.
func CalculateDiffAndFilter(currentMsgs []provider.Message, previouslySent map[string]string) ([]provider.Message, map[string]string) {
	nextSent := make(map[string]string, len(previouslySent))
	for k, v := range previouslySent {
		nextSent[k] = v
	}

	for i := range currentMsgs {
		content := currentMsgs[i].Content

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
		if len(currentMsgs[i].Parts) > 0 {
			filteredParts = make([]provider.ContentPart, len(currentMsgs[i].Parts))
			for j, part := range currentMsgs[i].Parts {
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

		currentMsgs[i].Content = content
		currentMsgs[i].Parts = filteredParts
	}

	return currentMsgs, nextSent
}
