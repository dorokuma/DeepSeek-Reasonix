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
	if key == "" {
		return ""
	}
	for _, v := range jm.Running() {
		if v.Kind != "task" && v.Kind != "skill" {
			continue
		}
		if jm.DispatchDigest(v.ID) == key {
			return v.ID
		}
	}
	return ""
}

func taskDispatchFingerprint(label, prompt string) string {
	l := strings.TrimSpace(strings.ToLower(label))
	p := normalizeTaskPrompt(prompt)
	if p == "" && l == "" {
		return ""
	}
	payload := l + "\x00" + p
	if len(payload) > 600 {
		payload = payload[:600]
	}
	sum := sha256.Sum256([]byte(payload))
	return "fp:" + hex.EncodeToString(sum[:8])
}

func normalizeTaskPrompt(prompt string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(strings.ToLower(prompt))), " ")
}
