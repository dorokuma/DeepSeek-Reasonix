package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
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
		"cache_hit_billed_tokens",
		"cache_miss_billed_tokens",
		"steps",
	} {
		if _, ok := got[key]; !ok {
			t.Fatalf("metrics JSON missing %q: %s", key, string(b))
		}
	}
}

func TestMetricsSinkDualCache口径(t *testing.T) {
	s := &metricsSink{inner: event.Discard}
	// Opaque usage: provider cache 0/0, billed miss = prompt.
	s.Emit(event.Event{
		Kind:    event.Usage,
		Usage:   &provider.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
		Pricing: &provider.Pricing{Input: 1, Output: 2, Currency: "¥"},
	})
	if s.m.CacheHitTokens != 0 || s.m.CacheMissTokens != 0 {
		t.Fatalf("provider口径 = %d/%d want 0/0", s.m.CacheHitTokens, s.m.CacheMissTokens)
	}
	if s.m.CacheHitBilledTokens != 0 || s.m.CacheMissBilledTokens != 100 {
		t.Fatalf("billed口径 = %d/%d want 0/100", s.m.CacheHitBilledTokens, s.m.CacheMissBilledTokens)
	}
	if s.m.Currency != "¥" || s.m.Cost <= 0 {
		t.Fatalf("cost=%v currency=%q", s.m.Cost, s.m.Currency)
	}
}
