package config

import (
	"testing"

	"reasonix/internal/provider"
)

func TestApplyOpenCodePricingFiltersModels(t *testing.T) {
	cfg := &Config{
		UsdCnyRate: 7.0,
		Providers: []ProviderEntry{
			{
				Name:    "opencode-go",
				BaseURL: "https://opencode.ai/zen/go/v1",
				Model:   "deepseek-v4-flash",
				// Simulate live-fetched models with some undocumented ones
				fetchedModels: []string{
					"deepseek-v4-flash",
					"deepseek-v4-pro",
					"glm-5",          // undocumented
					"glm-5.1",
					"glm-5.2",
					"hy3-preview",    // undocumented
					"kimi-k2.5",      // undocumented
					"kimi-k2.6",
					"kimi-k2.7-code",
					"mimo-v2.5",
					"mimo-v2.5-pro",
					"minimax-m2.5",
					"minimax-m2.7",
					"minimax-m3",
					"qwen3.5-plus",   // undocumented
					"qwen3.6-plus",
					"qwen3.7-max",
					"qwen3.7-plus",
				},
			},
		},
	}

	// Simulate scraped pricing from docs page (14 models)
	pricing := map[string]provider.Pricing{
		"deepseek-v4-flash": {Input: 0.14, Output: 0.28, Currency: "USD"},
		"deepseek-v4-pro":   {Input: 1.74, Output: 3.48, Currency: "USD"},
		"glm-5.1":           {Input: 1.40, Output: 4.40, Currency: "USD"},
		"glm-5.2":           {Input: 1.40, Output: 4.40, Currency: "USD"},
		"kimi-k2.6":         {Input: 0.95, Output: 4.00, Currency: "USD"},
		"kimi-k2.7-code":    {Input: 0.95, Output: 4.00, Currency: "USD"},
		"mimo-v2.5":         {Input: 0.14, Output: 0.28, Currency: "USD"},
		"mimo-v2.5-pro":     {Input: 1.74, Output: 3.48, Currency: "USD"},
		"minimax-m2.5":      {Input: 0.30, Output: 1.20, Currency: "USD"},
		"minimax-m2.7":      {Input: 0.30, Output: 1.20, Currency: "USD"},
		"minimax-m3":        {Input: 0.30, Output: 1.20, Currency: "USD"},
		"qwen3.6-plus":      {Input: 0.50, Output: 3.00, Currency: "USD"},
		"qwen3.7-max":       {Input: 2.50, Output: 7.50, Currency: "USD"},
		"qwen3.7-plus":      {Input: 0.40, Output: 1.60, Currency: "USD"},
	}

	ApplyOpenCodePricing(cfg, pricing)

	// Verify undocumented models were filtered out
	models := cfg.Providers[0].ModelList()
	got := make(map[string]bool)
	for _, m := range models {
		got[m] = true
	}

	// These should be present
	for _, m := range []string{"deepseek-v4-flash", "deepseek-v4-pro", "glm-5.1", "glm-5.2", "kimi-k2.6", "kimi-k2.7-code"} {
		if !got[m] {
			t.Errorf("expected model %q to be present", m)
		}
	}

	// These should be filtered out
	for _, m := range []string{"glm-5", "hy3-preview", "kimi-k2.5", "qwen3.5-plus"} {
		if got[m] {
			t.Errorf("undocumented model %q should have been filtered out", m)
		}
	}

	if len(models) != 14 {
		t.Errorf("expected 14 models, got %d: %v", len(models), models)
	}
}

func TestApplyOpenCodePricingNoFilterWhenScrapeFails(t *testing.T) {
	// When pricing is nil (scrape failed), models should not be filtered
	cfg := &Config{
		UsdCnyRate: 7.0,
		Providers: []ProviderEntry{
			{
				Name:    "opencode-go",
				BaseURL: "https://opencode.ai/zen/go/v1",
				Model:   "deepseek-v4-flash",
				fetchedModels: []string{"deepseek-v4-flash", "glm-5", "hy3-preview"},
			},
		},
	}

	ApplyOpenCodePricing(cfg, nil)

	models := cfg.Providers[0].ModelList()
	if len(models) != 3 {
		t.Errorf("expected 3 models when pricing is nil, got %d", len(models))
	}
}
