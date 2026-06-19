// Package config — auto-scraped OpenCode Go pricing.
//
// OpenCode Go (opencode.ai/docs/go) exposes ~14 open-source coding models
// through a single OpenAI-compatible endpoint. Each model has its own USD
// per-token pricing published in an HTML table on the docs page.
//
// Rather than hardcoding the table or requiring a manual JSON file, this
// package fetches the docs page at startup, parses the pricing table and
// the model-ID mapping table, and populates ModelPrices automatically.
// If the page format changes or the fetch fails, the system degrades
// gracefully — the user can still supply model_prices in reasonix.toml.
package config

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"reasonix/internal/provider"
)

const opencodeDocsURL = "https://opencode.ai/docs/go"

// opencodeScrapeTimeout bounds the HTTP request to the docs page.
const opencodeScrapeTimeout = 15 * time.Second

// ---------------------------------------------------------------------------
// ScrapeOpenCodePricing fetches the OpenCode Go docs page and extracts per-
// model pricing. It parses two HTML tables:
//
//  1. Pricing table:  Model | Input | Output | Cached Read | Cached Write
//  2. Model-ID table: Model | Model ID | Endpoint | AI SDK Package
//
// The second table maps display names (e.g. "DeepSeek V4 Pro") to API model
// IDs (e.g. "deepseek-v4-pro"). Returns a map from model ID → Pricing (USD).
// The ctx is used for the HTTP request only; parsing uses the body deadline.
func ScrapeOpenCodePricing(ctx context.Context) (map[string]provider.Pricing, error) {
	cli := &http.Client{Timeout: opencodeScrapeTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opencodeDocsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("scrape opencode: build request: %w", err)
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrape opencode: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scrape opencode: status %d", resp.StatusCode)
	}

	// Parse both tables from the HTML using a tokenizer.
	type table struct {
		headers []string
		rows    [][]string
	}
	var tables []table

	z := html.NewTokenizer(io.LimitReader(resp.Body, 1<<20)) // 1 MiB
	var cur table
	var inHead, inBody bool
	var row []string
	var cell strings.Builder
	inCell := false

	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		tagName, hasAttrs := z.TagName()
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			switch string(tagName) {
			case "table":
				cur = table{}
				inHead, inBody = false, false
			case "thead":
				inHead, inBody = true, false
			case "tbody":
				inHead, inBody = false, true
			case "tr":
				row = nil
			case "th":
				inCell = true
				cell.Reset()
				for hasAttrs {
					_, _, hasAttrs = z.TagAttr()
				}
			case "td":
				inCell = true
				cell.Reset()
			}
		case html.EndTagToken:
			switch string(tagName) {
			case "th", "td":
				if inCell {
					row = append(row, strings.TrimSpace(cell.String()))
					inCell = false
				}
			case "tr":
				if len(row) > 0 {
					if inHead {
						cur.headers = row
					} else if inBody {
						cur.rows = append(cur.rows, row)
					}
				}
				row = nil
			case "thead":
				inHead = false
			case "tbody":
				inBody = false
			case "table":
				if len(cur.headers) > 0 {
					tables = append(tables, cur)
				}
			}
		case html.TextToken:
			if inCell {
				cell.WriteString(string(z.Text()))
			}
		}
	}

	// Separate the pricing table from the model-ID table by header pattern.
	var pricingRows, idRows [][]string
	for _, t := range tables {
		hasInput := false
		hasOutput := false
		hasModelID := false
		for _, h := range t.headers {
			hl := strings.ToLower(strings.TrimSpace(h))
			if hl == "input" {
				hasInput = true
			}
			if hl == "output" {
				hasOutput = true
			}
			if hl == "model id" {
				hasModelID = true
			}
		}
		if hasInput && hasOutput {
			pricingRows = t.rows
		}
		if hasModelID {
			idRows = t.rows
		}
	}

	if len(pricingRows) == 0 {
		return nil, fmt.Errorf("scrape opencode: pricing table not found")
	}
	if len(idRows) == 0 {
		return nil, fmt.Errorf("scrape opencode: model-id table not found")
	}

	// Build display-name → model-ID mapping from the model-ID table.
	// Expected columns: Model (display name), Model ID, Endpoint, …
	displayToID := make(map[string]string, len(idRows))
	for _, row := range idRows {
		if len(row) < 2 {
			continue
		}
		display := normDisplay(row[0])
		modelID := strings.TrimSpace(row[1])
		if display != "" && modelID != "" {
			displayToID[display] = modelID
		}
	}

	// Parse the pricing table and map display names to model IDs.
	// Expected columns: Model (display name), Input, Output, Cached Read, …
	out := make(map[string]provider.Pricing, len(pricingRows))
	for _, row := range pricingRows {
		if len(row) < 4 {
			continue
		}
		display := normDisplay(row[0])
		if display == "" {
			continue
		}
		modelID, ok := displayToID[display]
		if !ok {
			// Try without common suffixes like "code", "free".
			if trimmed := strings.TrimSuffix(display, "code"); trimmed != display {
				modelID, ok = displayToID[strings.TrimSpace(trimmed)]
			}
		}
		if !ok {
			// Fallback: try the display name itself as a model ID.
			modelID = display
		}
		// If this model already has a price (e.g. one tier of a multi-tier
		// model like Qwen3.7 Plus), keep the first entry — it's the common
		// ≤256K tier.
		if _, exists := out[modelID]; exists {
			continue
		}
		input := parseDollar(row[1])
		output := parseDollar(row[2])
		cached := parseDollar(row[3])
		out[modelID] = provider.Pricing{
			Input:    input,
			Output:   output,
			CacheHit: cached,
			Currency: "USD",
		}
	}
	return out, nil
}

// normDisplay normalises a display name from an HTML table for comparison.
// It lowercases, trims, removes parenthesised context notes, and replaces
// spaces with hyphens so that "MiMo V2.5" matches "MiMo-V2.5".
func normDisplay(s string) string {
	s = strings.TrimSpace(s)
	// Remove context notes like "(≤ 256K tokens)", "(> 256K tokens)"
	if idx := strings.Index(s, "("); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	return s
}

// parseDollar extracts a float from a "$1.40" or "-" string.
func parseDollar(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "$")
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

// ---------------------------------------------------------------------------
// OpenCode Go provider detection and pricing application.

// isOpenCodeGoProvider returns true when the base_url identifies an OpenAI-
// compatible endpoint served by OpenCode Go.
func isOpenCodeGoProvider(baseURL string) bool {
	return strings.Contains(baseURL, "opencode.ai/zen/go")
}

// ApplyOpenCodePricing scans cfg.Providers for OpenCode Go providers and
// populates their ModelPrices from the scraped pricing map (modelID → USD
// Pricing). USD prices are converted to CNY using the configured exchange
// rate. Existing ModelPrices entries are not overwritten so the user can
// override individual models in reasonix.toml.
//
// When pricing is nil (scrape failed or no OpenCode provider detected), the
// function is a no-op.
func ApplyOpenCodePricing(cfg *Config, pricing map[string]provider.Pricing) {
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
		// Filter live-fetched models to only those documented on the OpenCode
		// Go docs page. The /v1/models API may return stale or undocumented
		// model IDs (e.g. glm-5, hy3-preview); the docs page is authoritative.
		if p.fetchedModels != nil && len(pricing) > 0 {
			filtered := make([]string, 0, len(pricing))
			for _, m := range p.fetchedModels {
				if _, ok := pricing[m]; ok {
					filtered = append(filtered, m)
				}
			}
			p.fetchedModels = filtered
		}
		if p.ModelPrices == nil {
			p.ModelPrices = make(map[string]provider.Pricing, len(pricing))
		}
		for _, m := range p.ModelList() {
			usd, ok := pricing[m]
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
