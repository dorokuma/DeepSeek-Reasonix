package config

import (
	"testing"

	"reasonix/internal/provider"
)

func TestStampUsdCnyRates(t *testing.T) {
	// Nil config must not panic.
	StampUsdCnyRates(nil)

	// Config with no providers — no-op.
	cfg := &Config{UsdCnyRate: 7.2}
	StampUsdCnyRates(cfg)
	if cfg.UsdCnyRate != 7.2 {
		t.Fatalf("UsdCnyRate changed to %f", cfg.UsdCnyRate)
	}

	// Config with providers.
	cfg = &Config{
		UsdCnyRate: 7.3,
		Providers: []ProviderEntry{
			{
				Name: "openai",
				Price: &provider.Pricing{
					Input:  2.5,
					Output: 10.0,
				},
				ModelPrices: map[string]provider.Pricing{
					"gpt-4o": {
						Input:  5.0,
						Output: 20.0,
					},
				},
			},
			{
				Name: "no-price",
				// Price is nil — should not panic.
			},
			{
				Name: "anthropic",
				Price: &provider.Pricing{
					Input:  3.0,
					Output: 15.0,
				},
				ModelPrices: map[string]provider.Pricing{
					"claude-3-opus": {
						Input:  10.0,
						Output: 30.0,
					},
					"claude-3-sonnet": {
						Input:  3.0,
						Output: 15.0,
					},
				},
			},
		},
	}
	StampUsdCnyRates(cfg)

	for i, p := range cfg.Providers {
		if p.Price != nil && p.Price.UsdCnyRate != 7.3 {
			t.Errorf("provider[%d] Price.UsdCnyRate = %f, want 7.3", i, p.Price.UsdCnyRate)
		}
		for k, mp := range p.ModelPrices {
			if mp.UsdCnyRate != 7.3 {
				t.Errorf("provider[%d].ModelPrices[%q].UsdCnyRate = %f, want 7.3", i, k, mp.UsdCnyRate)
			}
		}
	}
}

func TestStampUsdCnyRatesDefaultRate(t *testing.T) {
	// When config.UsdCnyRate is 0, should use provider.DefaultUsdCnyRate.
	cfg := &Config{
		Providers: []ProviderEntry{
			{
				Name: "openai",
				Price: &provider.Pricing{
					Input:  2.5,
					Output: 10.0,
				},
				ModelPrices: map[string]provider.Pricing{
					"gpt-4o": {
						Input:  5.0,
						Output: 20.0,
					},
				},
			},
		},
	}
	StampUsdCnyRates(cfg)

	want := provider.DefaultUsdCnyRate
	if cfg.Providers[0].Price.UsdCnyRate != want {
		t.Errorf("Price.UsdCnyRate = %f, want default %f", cfg.Providers[0].Price.UsdCnyRate, want)
	}
	for k, mp := range cfg.Providers[0].ModelPrices {
		if mp.UsdCnyRate != want {
			t.Errorf("ModelPrices[%q].UsdCnyRate = %f, want %f", k, mp.UsdCnyRate, want)
		}
	}
}
