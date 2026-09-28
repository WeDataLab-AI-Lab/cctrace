package chatruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// modelsTimeout is how long we cache the models list before refetching.
const modelsTimeout = 1 * time.Hour

// nvidiaHardcodedModels is the list of models that NVIDIA has verified.
var nvidiaHardcodedModels = []string{
	"z-ai/glm-5.3",
}

// modelsCacheEntry holds a cached models list with its expiration time.
type modelsCacheEntry struct {
	models    []string
	expiresAt time.Time
}

// ListModels returns the list of models available to the configured API key.
// For NVIDIA, it returns only hardcoded models. For LiteLLM, it returns the
// full response from /v1/models, allowing proxies to decide what's available.
func (r *Runtime) ListModels(ctx context.Context) ([]string, error) {
	key, err := r.apiKey(ctx)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, fmt.Errorf("API key is not set")
	}

	// Check cache first
	r.modelsMu.Lock()
	if cached, ok := r.models[key]; ok && time.Now().Before(cached.expiresAt) {
		r.modelsMu.Unlock()
		return cached.models, nil
	}
	r.modelsMu.Unlock()

	// Use singleflight to avoid concurrent /v1/models requests for the same key
	val, err, _ := r.modelsFlight.Do(key, func() (any, error) {
		models, err := r.fetchModels(ctx, key)
		if err != nil {
			return nil, err
		}

		// Cache the result
		r.modelsMu.Lock()
		r.models[key] = modelsCacheEntry{
			models:    models,
			expiresAt: time.Now().Add(modelsTimeout),
		}
		r.modelsMu.Unlock()

		return models, nil
	})
	if err != nil {
		return nil, err
	}
	return val.([]string), nil
}

func (r *Runtime) fetchModels(ctx context.Context, key string) ([]string, error) {
	raw, err := r.do(ctx, key, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("invalid models response: %v", err)
	}

	models := make([]string, 0, len(resp.Data))
	for _, item := range resp.Data {
		if item.ID == "" {
			continue
		}
		models = append(models, item.ID)
	}

	// For NVIDIA, filter to hardcoded list
	if r.cfg.ProviderName == "nvidia" {
		models = filterByHardcoded(models, nvidiaHardcodedModels)
	}

	return models, nil
}

// filterByHardcoded returns only the items in available that are also in hardcoded.
func filterByHardcoded(available, hardcoded []string) []string {
	hardcodedMap := make(map[string]bool, len(hardcoded))
	for _, h := range hardcoded {
		hardcodedMap[h] = true
	}

	var result []string
	for _, a := range available {
		if hardcodedMap[a] {
			result = append(result, a)
		}
	}
	return result
}

// ModelsCache returns a read-only view of the current models cache.
// This is for testing and debugging only.
func (r *Runtime) ModelsCache() map[string][]string {
	r.modelsMu.Lock()
	defer r.modelsMu.Unlock()

	result := make(map[string][]string, len(r.models))
	for key, entry := range r.models {
		if time.Now().Before(entry.expiresAt) {
			result[key] = append([]string(nil), entry.models...)
		}
	}
	return result
}
