package agent

import "reasonix/internal/provider"

// SessionUsageDelta is a rollup of cache/token/cost counters to merge into a
// parent session (sub-agent finish, planner merge, etc.).
type SessionUsageDelta struct {
	Hit, Miss, Prompt, Total int64
	Cost                     float64
	Currency                 string
	// Reported is true when Hit/Miss came from a real provider cache split
	// (so UIs may show session-average hit rate).
	Reported bool
}

// normalizeUsage returns a copy of u with NormalizeCache applied.
// A nil input yields a zero Usage.
func normalizeUsage(u *provider.Usage) provider.Usage {
	if u == nil {
		return provider.Usage{}
	}
	out := *u
	out.NormalizeCache()
	return out
}

// usageHasBreakdown reports whether u (already normalized) carries a real
// provider cache hit/miss split.
func usageHasBreakdown(u provider.Usage) bool {
	return u.CacheHitTokens+u.CacheMissTokens > 0 || u.CacheBreakdownKnown
}

// sessionCacheAdd returns the hit/miss to add to session counters for one call.
// When the provider omitted a split, miss is the full prompt (aligned with
// Pricing.Cost) and reported is false so UIs do not show a fake 0% hit rate.
// When a split is present, hit/miss are the provider numbers and reported is true.
func sessionCacheAdd(u provider.Usage) (hit, miss int64, reported bool) {
	if usageHasBreakdown(u) {
		return int64(u.CacheHitTokens), int64(u.CacheMissTokens), true
	}
	if u.PromptTokens > 0 {
		return 0, int64(u.PromptTokens), false
	}
	return 0, 0, false
}

// CacheBilledTokens returns session-aligned hit/miss for one usage record
// (caller should NormalizeCache first). Opaque prompts count as miss so totals
// match Pricing.Cost; harnesses use this as the "billed" cache口径.
func CacheBilledTokens(u *provider.Usage) (hit, miss int) {
	if u == nil {
		return 0, 0
	}
	h, m, _ := sessionCacheAdd(*u)
	return int(h), int(m)
}

// sessionUsageFromAPI builds a SessionUsageDelta from one provider Usage and a
// price table. Hit/Miss follow provider-normalized fields only (no synthetic
// miss) — used by the planner merge path which historically did not invent
// miss into parent counters. Cost still uses CostInCNY (which bills opaque
// prompt as miss). For main-agent stream accounting that invents miss into
// counters, use recordSessionUsage / sessionCacheAdd instead.
func sessionUsageFromAPI(u *provider.Usage, pricing *provider.Pricing) SessionUsageDelta {
	norm := normalizeUsage(u)
	d := SessionUsageDelta{
		Hit:      int64(norm.CacheHitTokens),
		Miss:     int64(norm.CacheMissTokens),
		Prompt:   int64(norm.PromptTokens),
		Total:    int64(norm.TotalTokens),
		Reported: usageHasBreakdown(norm),
	}
	if pricing != nil {
		d.Cost = pricing.CostInCNY(&norm)
		d.Currency = provider.CNYSymbol()
	}
	return d
}
