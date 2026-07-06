package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteMetrics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.json")
	if err := writeMetrics(path, RunMetrics{
		PromptTokens:     10,
		CompletionTokens: 3,
		CacheHitTokens:   7,
		CacheMissTokens:  3,
		Steps:            2,
	}); err != nil {
		t.Fatalf("writeMetrics: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{
		"prompt_tokens",
		"completion_tokens",
		"cache_hit_tokens",
		"cache_miss_tokens",
		"steps",
	} {
		if _, ok := got[key]; !ok {
			t.Fatalf("metrics JSON missing %q: %s", key, string(b))
		}
	}
}
