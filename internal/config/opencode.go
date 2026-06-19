// Package config — external pricing data and OpenCode Go auto-population.
//
// OpenCode Go (opencode.ai/docs/go) exposes ~14 open-source coding models
// through a single base URL, each with its own USD per-token pricing.
// Rather than hardcoding the table in Go source, pricing is loaded from an
// external JSON URL (Config.PricingURL) at startup, so the data can be
// updated without a code release.
//
// JSON format (keys = provider name, values = model → pricing):
//
//	{
//	  "opencode-go": {
//	    "glm-5.2":              {"input": 1.40, "output": 4.40, "cache_hit": 0.26},
//	    "kimi-k2.7":            {"input": 0.95, "output": 4.00, "cache_hit": 0.19},
//	    "deepseek-v4-pro":      {"input": 1.74, "output": 3.48, "cache_hit": 0.0145},
//	    "deepseek-v4-flash":    {"input": 0.14, "output": 0.28, "cache_hit": 0.0028}
//	  }
//	}
//
// Prices are in USD; the system converts to CNY using Config.UsdCnyRate.
// Per-model overrides in ProviderEntry.ModelPrices (set in reasonix.toml)
// take precedence over URL-sourced data.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"reasonix/internal/provider"
)

// pricingJSON is the top-level structure fetched from PricingURL.
type pricingJSON map[string]map[string]struct {
	Input    float64 `json:"input"`
	Output   float64 `json:"output"`
	CacheHit float64 `json:"cache_hit"`
}

// isOpenCodeGoProvider returns true when the base_url identifies an OpenAI-
// compatible endpoint served by OpenCode Go.
func isOpenCodeGoProvider(baseURL string) bool {
	return strings.Contains(baseURL, "opencode.ai/zen/go")
}

// FetchPricingFromURL downloads and decodes the pricing JSON from url.
// A GET with a 10s timeout is used. An empty url returns (nil, nil).
func FetchPricingFromURL(url string) (map[string]map[string]provider.Pricing, error) {
	if strings.TrimSpace(url) == "" {
		return nil, nil
	}
	cli := &http.Client{Timeout: 10 * time.Second}
	resp, err := cli.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch pricing: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch pricing: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10)) // 512 KiB
	if err != nil {
		return nil, fmt.Errorf("fetch pricing: read: %w", err)
	}
	var raw pricingJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("fetch pricing: decode: %w", err)
	}
	out := make(map[string]map[string]provider.Pricing, len(raw))
	for provName, models := range raw {
		m := make(map[string]provider.Pricing, len(models))
		for modelID, p := range models {
			m[modelID] = provider.Pricing{
				Input:    p.Input,
				Output:   p.Output,
				CacheHit: p.CacheHit,
				Currency: "USD",
			}
		}
		out[provName] = m
	}
	return out, nil
}

// ApplyOpenCodePricing scans cfg.Providers for OpenCode Go providers and
// populates their ModelPrices from the pricing map (keyed by provider name).
// USD prices are converted to CNY using the configured exchange rate.
// Existing ModelPrices entries are not overwritten so the user can override
// individual models in reasonix.toml.
//
// When pricing is nil (no URL configured, or fetch failed), the function
// is a no-op — the user must supply model_prices manually in reasonix.toml.
func ApplyOpenCodePricing(cfg *Config, pricing map[string]map[string]provider.Pricing) {
	if cfg == nil || pricing == nil {
		return
	}
	rate := cfg.UsdCnyRate
	if rate == 0 {
		rate = 7.0
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if !isOpenCodeGoProvider(p.BaseURL) {
			continue
		}
		provPricing, ok := pricing[p.Name]
		if !ok {
			continue
		}
		if p.ModelPrices == nil {
			p.ModelPrices = make(map[string]provider.Pricing, len(provPricing))
		}
		for _, m := range p.ModelList() {
			usd, ok := provPricing[m]
			if !ok {
				continue
			}
			// Don't overwrite user-supplied overrides.
			if _, exists := p.ModelPrices[m]; exists {
				continue
			}
			p.ModelPrices[m] = provider.Pricing{
				CacheHit: usd.CacheHit * rate,
				Input:    usd.Input * rate,
				Output:   usd.Output * rate,
				Currency: "¥",
			}
		}
		// Also set the default model's price as the shared Price so that
		// code paths which only look at entry.Price still get a sensible value.
		defaultModel := p.DefaultModel()
		if defaultModel != "" {
			if mp, ok := p.ModelPrices[defaultModel]; ok {
				p.Price = &mp
			}
		}
	}
}
