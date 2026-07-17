package cli

import (
	"encoding/json"
	"os"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// RunMetrics is the machine-readable token/cache/cost summary `run --metrics`
// writes, so a benchmark harness can read a run's cost without scraping stdout.
//
// Two cache口径:
//   - cache_hit_tokens / cache_miss_tokens: provider-normalized only (no synthetic
//     full-prompt miss). Safe to compare against API fields.
//   - cache_hit_billed_tokens / cache_miss_billed_tokens: session-aligned
//     accounting (opaque prompt counted as miss, same as status-line spend).
//
// Cost/Currency match the TUI session total: CNY after Pricing.CostInCNY.
type RunMetrics struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CacheHitTokens   int `json:"cache_hit_tokens"`
	CacheMissTokens  int `json:"cache_miss_tokens"`
	// Billed cache counters (session口径). Sum of hit_billed+miss_billed tracks
	// spendable prompt tokens even when the provider omitted a split.
	CacheHitBilledTokens  int     `json:"cache_hit_billed_tokens"`
	CacheMissBilledTokens int     `json:"cache_miss_billed_tokens"`
	Steps                 int     `json:"steps"` // model calls (one per stream, incl. tool rounds)
	Cost                  float64 `json:"cost"`
	Currency              string  `json:"currency"` // always "¥" (CNY) after CostInCNY
	Compactions           int     `json:"compactions"`
}

// metricsSink forwards every event to the real sink and accumulates the per-call
// Usage events into a RunMetrics. Cache totals are summed per call (not read from
// the cumulative SessionHit/Miss). Spend uses CostInCNY so harness numbers match
// the status-line 花销.
type metricsSink struct {
	inner event.Sink
	m     RunMetrics
}

func (s *metricsSink) Emit(e event.Event) {
	if e.Kind == event.Usage && e.Usage != nil {
		// Dual口径 via the same helpers the agent uses for session accounting.
		norm := *e.Usage
		norm.NormalizeCache()
		s.m.PromptTokens += norm.PromptTokens
		s.m.CompletionTokens += norm.CompletionTokens
		s.m.CacheHitTokens += norm.CacheHitTokens
		s.m.CacheMissTokens += norm.CacheMissTokens
		// Session-aligned billed split (exported helper mirror — keep logic in agent).
		hitB, missB := agent.CacheBilledTokens(&norm)
		s.m.CacheHitBilledTokens += hitB
		s.m.CacheMissBilledTokens += missB
		s.m.Steps++
		if p := e.Pricing; p != nil {
			s.m.Cost = provider.RoundCost(s.m.Cost + p.CostInCNY(&norm))
			s.m.Currency = provider.CNYSymbol()
		}
	}
	if e.Kind == event.CompactionStarted {
		s.m.Compactions++
	}
	s.inner.Emit(e)
}

func writeMetrics(path string, m RunMetrics) error {
	m.Cost = provider.RoundCost(m.Cost)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
