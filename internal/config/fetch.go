// fetch.go — model auto-discovery via the OpenAI-compatible GET /models API.
package config

import (
	"context"
	"fmt"

	"reasonix/internal/provider/openai"
)

// FetchModels queries the provider's OpenAI-compatible GET /models endpoint and
// returns the available model IDs, sorted alphabetically.
func (e *ProviderEntry) FetchModels(ctx context.Context) ([]string, error) {
	if e.BaseURL == "" {
		return nil, fmt.Errorf("fetch models: provider %q has no base_url", e.Name)
	}
	key := e.APIKey()
	if key == "" {
		return nil, fmt.Errorf("fetch models: provider %q has no API key (set %s in .env)", e.Name, e.APIKeyEnv)
	}
	url := e.ModelsURL
	if url == "" {
		url = e.BaseURL + "/models"
	}
	return openai.FetchModels(ctx, url, key)
}

// RefreshModels fetches the live model list from the provider's OpenAI-compatible
// GET /models endpoint and caches it in fetchedModels. After a successful call,
// ModelList returns the live list instead of the static Models/Model config fields.
//
// A failed fetch (network error, auth, or empty response) leaves the cached list
// unchanged so the static fallback remains available. Callers should log the error
// but not treat it as fatal. Providers without a base_url or API key are silently
// skipped.
func (e *ProviderEntry) RefreshModels(ctx context.Context) error {
	if e.BaseURL == "" {
		return nil
	}
	key := e.APIKey()
	if key == "" {
		return nil
	}
	url := e.ModelsURL
	if url == "" {
		url = e.BaseURL + "/models"
	}
	models, err := openai.FetchModels(ctx, url, key)
	if err != nil {
		return err
	}
	e.fetchedModels = models
	return nil
}
