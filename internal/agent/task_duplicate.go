package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"reasonix/internal/jobs"
)

func findRunningDuplicateTask(jm *jobs.Manager, label, prompt string) string {
	if jm == nil {
		return ""
	}
	key := taskDispatchFingerprint(label, prompt)
	for _, v := range jm.Running() {
		if v.Kind != "" && v.Kind != "task" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(v.Label), strings.TrimSpace(label)) && strings.TrimSpace(label) != "" && label != "task" {
			return v.ID
		}
		if key != "" && jm.DispatchDigest(v.ID) == key {
			return v.ID
		}
	}
	return ""
}

func taskDispatchFingerprint(label, prompt string) string {
	l := strings.TrimSpace(strings.ToLower(label))
	if l != "" && l != "task" {
		return "label:" + l
	}
	p := normalizeTaskPrompt(prompt)
	if p == "" {
		return ""
	}
	if len(p) > 512 {
		p = p[:512]
	}
	sum := sha256.Sum256([]byte(p))
	return "prompt:" + hex.EncodeToString(sum[:8])
}

func normalizeTaskPrompt(prompt string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(strings.ToLower(prompt))), " ")
}
