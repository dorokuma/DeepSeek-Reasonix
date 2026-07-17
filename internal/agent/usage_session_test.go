package agent

import (
	"testing"

	"reasonix/internal/provider"
)

func TestNormalizeUsageAndSessionCacheAdd(t *testing.T) {
	norm := normalizeUsage(&provider.Usage{PromptTokens: 1000, CacheHitTokens: 600})
	if norm.CacheMissTokens != 400 {
		t.Fatalf("normalize miss=%d want 400", norm.CacheMissTokens)
	}
	hit, miss, reported := sessionCacheAdd(norm)
	if hit != 600 || miss != 400 || !reported {
		t.Fatalf("sessionCacheAdd reported split = %d/%d reported=%v", hit, miss, reported)
	}

	opaque := normalizeUsage(&provider.Usage{PromptTokens: 500, CompletionTokens: 5})
	hit, miss, reported = sessionCacheAdd(opaque)
	if hit != 0 || miss != 500 || reported {
		t.Fatalf("opaque = %d/%d reported=%v", hit, miss, reported)
	}
	// Provider view must stay 0/0 for turn display.
	if opaque.CacheHitTokens+opaque.CacheMissTokens != 0 {
		t.Fatalf("opaque normalize invented split: %+v", opaque)
	}
}

func TestSessionUsageFromAPIUsesPlannerPricing(t *testing.T) {
	u := &provider.Usage{PromptTokens: 1_000_000, CacheHitTokens: 0, CacheMissTokens: 1_000_000, CompletionTokens: 0}
	p := &provider.Pricing{CacheHit: 0.02, Input: 1, Output: 2, Currency: "¥"}
	d := sessionUsageFromAPI(u, p)
	if d.Hit != 0 || d.Miss != 1_000_000 || !d.Reported {
		t.Fatalf("delta cache = %+v", d)
	}
	if d.Cost != 1.0 || d.Currency != "¥" {
		t.Fatalf("delta cost = %v %q", d.Cost, d.Currency)
	}
	// Opaque: no invent into Hit/Miss on this path.
	opaque := &provider.Usage{PromptTokens: 100, CompletionTokens: 0}
	d2 := sessionUsageFromAPI(opaque, p)
	if d2.Hit != 0 || d2.Miss != 0 || d2.Reported || d2.Prompt != 100 {
		t.Fatalf("opaque fromAPI = %+v", d2)
	}
	if d2.Cost <= 0 {
		t.Fatal("opaque still bills via CostInCNY")
	}
}

func TestRecordSessionUsageNormalizesAndAlignsOpaqueMiss(t *testing.T) {
	a := &Agent{pricing: &provider.Pricing{CacheHit: 0.025, Input: 3, Output: 6, Currency: "¥"}}

	a.recordSessionUsage(&provider.Usage{PromptTokens: 1000, CacheHitTokens: 600, CompletionTokens: 10, TotalTokens: 1010})
	if hit, miss := a.SessionCache(); hit != 600 || miss != 400 {
		t.Fatalf("after partial breakdown: hit/miss=%d/%d want 600/400", hit, miss)
	}
	if !a.SessionCacheReported() {
		t.Fatal("expected SessionCacheReported after real split")
	}
	if u := a.LastUsage(); u == nil || u.CacheMissTokens != 400 {
		t.Fatalf("LastUsage miss=%v want 400", u)
	}

	a2 := &Agent{pricing: &provider.Pricing{CacheHit: 0.025, Input: 3, Output: 6, Currency: "¥"}}
	a2.recordSessionUsage(&provider.Usage{PromptTokens: 500, CompletionTokens: 5, TotalTokens: 505})
	if hit, miss := a2.SessionCache(); hit != 0 || miss != 500 {
		t.Fatalf("opaque: hit/miss=%d/%d want 0/500", hit, miss)
	}
	if a2.SessionCacheReported() {
		t.Fatal("opaque usage must not mark SessionCacheReported")
	}
	if u := a2.LastUsage(); u == nil || u.CacheHitTokens+u.CacheMissTokens != 0 {
		t.Fatalf("LastUsage must not invent turn split, got hit=%d miss=%d", u.CacheHitTokens, u.CacheMissTokens)
	}
	cost, cur := a2.SessionCost()
	if cur != "¥" || cost <= 0 {
		t.Fatalf("SessionCost=%v %q want positive ¥", cost, cur)
	}
	want := provider.RoundCost(0.00153)
	if cost != want {
		t.Fatalf("SessionCost=%v want %v", cost, want)
	}
}

func TestAddSessionUsagePropagatesReported(t *testing.T) {
	parent := &Agent{}
	parent.AddSessionUsage(SessionUsageDelta{
		Hit: 10, Miss: 5, Prompt: 15, Total: 20,
		Cost: 0.01, Currency: "¥", Reported: true,
	})
	if !parent.SessionCacheReported() {
		t.Fatal("parent should inherit reported flag from sub-agent")
	}
	hit, miss := parent.SessionCache()
	if hit != 10 || miss != 5 {
		t.Fatalf("hit/miss=%d/%d", hit, miss)
	}
}

func TestCacheBilledTokens(t *testing.T) {
	u := &provider.Usage{PromptTokens: 100, CacheHitTokens: 40, CacheMissTokens: 60}
	h, m := CacheBilledTokens(u)
	if h != 40 || m != 60 {
		t.Fatalf("reported billed=%d/%d", h, m)
	}
	opaque := &provider.Usage{PromptTokens: 80}
	h, m = CacheBilledTokens(opaque)
	if h != 0 || m != 80 {
		t.Fatalf("opaque billed=%d/%d", h, m)
	}
}
