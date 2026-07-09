package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"

	"reasonix/internal/jobs"
)

func findRunningDuplicateTask(jm *jobs.Manager, label, prompt string) string {
	if jm == nil {
		return ""
	}
	// Goal-primary: same prompt/goal collides across skill/task labels and job kinds.
	_ = label
	key := taskDispatchFingerprint("", prompt)
	if key == "" {
		return ""
	}
	semKey := taskDispatchSemanticKey("", prompt)
	pol := jm.SemanticDedupPolicy()
	for _, v := range jm.Running() {
		if v.Kind != "task" && v.Kind != "skill" {
			continue
		}
		if jm.DispatchDigest(v.ID) == key {
			return v.ID
		}
	}
	if !pol.Enabled || semKey == "" {
		return ""
	}
	for _, v := range jm.Running() {
		if v.Kind != "task" && v.Kind != "skill" {
			continue
		}
		other := jm.DispatchSemantic(v.ID)
		if other == "" {
			continue
		}
		if semanticSimilarity(semKey, other) >= pol.Threshold {
			return v.ID
		}
	}
	return ""
}

// CheckBackgroundDuplicate reports an error when a matching or similar job is already running.
func CheckBackgroundDuplicate(jm *jobs.Manager, label, prompt string) error {
	if dupID := findRunningDuplicateTask(jm, label, prompt); dupID != "" {
		return fmt.Errorf("background task %s is already running with the same or similar goal. Wait for its result at the conversation tail; do not dispatch a duplicate task", dupID)
	}
	return nil
}

// RegisterBackgroundDispatchMeta stores exact + semantic keys for duplicate detection.
func RegisterBackgroundDispatchMeta(jm *jobs.Manager, jobID, label, prompt string) {
	if jm == nil || jobID == "" {
		return
	}
	// Labels are display-only; dedup keys are prompt/goal only so explore+task
	// (or two skills) cannot double-book the same work.
	_ = label
	jm.SetDispatchDigest(jobID, taskDispatchFingerprint("", prompt))
	jm.SetDispatchSemantic(jobID, taskDispatchSemanticKey("", prompt))
}

func taskDispatchFingerprint(label, prompt string) string {
	_ = label
	p := normalizeTaskPrompt(prompt)
	if p == "" {
		return ""
	}
	// Hash the full prompt (no prefix truncation) so long prompts that share a
	// common head never collide as false exact-duplicates.
	sum := sha256.Sum256([]byte(p))
	return "fp:" + hex.EncodeToString(sum[:8])
}

func taskDispatchSemanticKey(label, prompt string) string {
	_ = label
	return normalizeTaskPromptSemantic(prompt)
}

func normalizeTaskPrompt(prompt string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(strings.ToLower(prompt))), " ")
}

var taskPromptFillers = map[string]bool{
	"please": true, "pls": true, "kindly": true, "help": true, "me": true,
	"the": true, "a": true, "an": true, "to": true, "for": true, "and": true,
	"请": true, "帮我": true, "帮忙": true, "一下": true, "看看": true, "查": true,
}

func normalizeTaskPromptSemantic(prompt string) string {
	p := normalizeTaskPrompt(prompt)
	if p == "" {
		return ""
	}
	words := strings.Fields(p)
	out := make([]string, 0, len(words))
	for _, w := range words {
		w = trimPunct(w)
		if w == "" || taskPromptFillers[w] {
			continue
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

func trimPunct(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
}

func semanticSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}
	setA := wordSet(a)
	setB := wordSet(b)
	if len(setA) == 0 || len(setB) == 0 {
		return 0
	}
	inter := 0
	for w := range setA {
		if setB[w] {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		if w != "" {
			m[w] = true
		}
	}
	return m
}
