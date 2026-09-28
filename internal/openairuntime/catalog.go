package openairuntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"cctrace/internal/airuntime"
)

const (
	// modelCacheTTL: what a key may use changes with the account, not from one
	// admin page load to the next.
	modelCacheTTL = 10 * time.Minute
	// failureCacheTTL keeps a bad key or an outage to one request a minute.
	failureCacheTTL = time.Minute
	// modelsRequestTimeout bounds one /models fetch, which is a small list and
	// sits in front of the admin screen and every run start.
	modelsRequestTimeout = 15 * time.Second
)

var effortDescriptions = map[string]string{
	"none":   "No reasoning; fastest",
	"low":    "Light reasoning",
	"medium": "Balanced reasoning",
	"high":   "Deeper reasoning",
	"xhigh":  "Extended reasoning for long agentic work",
	"max":    "Maximum reasoning",
}

func efforts(names ...string) []airuntime.ReasoningEffortOption {
	out := make([]airuntime.ReasoningEffortOption, 0, len(names))
	for _, n := range names {
		out = append(out, airuntime.ReasoningEffortOption{ReasoningEffort: n, Description: effortDescriptions[n]})
	}
	return out
}

func lookupModel(id string) *airuntime.Model {
	for i := range catalog {
		if catalog[i].ID == id {
			return &catalog[i]
		}
	}
	return nil
}

// catalog is the models a report run may pick, in picker order. /models says
// only which ids the key can use, so display names and reasoning efforts come
// from the model pages (platform.openai.com/docs/models/<id>, checked
// 2026-09-15): medium is each model's default effort, and GPT-6 Astra has no
// none (a 400).
var catalog = []airuntime.Model{
	{ID: "gpt-6-astra", DisplayName: "GPT-6 Astra", Description: "Most capable model, for the hardest work",
		DefaultReasoningEffort: "medium", SupportedReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max")},
	{ID: "gpt-5.6-sol", DisplayName: "GPT-5.6 Sol", Description: "Flagship GPT-5.6 model",
		DefaultReasoningEffort: "medium", SupportedReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max")},
	{ID: "gpt-5.6-terra", DisplayName: "GPT-5.6 Terra", Description: "Balances intelligence and cost",
		DefaultReasoningEffort: "medium", SupportedReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max")},
	{ID: "gpt-5.6-luna", DisplayName: "GPT-5.6 Luna", Description: "Lowest cost and latency",
		DefaultReasoningEffort: "medium", SupportedReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max")},
}

// cachedModels is tied to the key it was fetched with, so a key changed in the
// admin screen is not answered from the old key's catalog.
type cachedModels struct {
	keyHash   [sha256.Size]byte
	models    []airuntime.Model
	err       error
	fetchedAt time.Time
	ttl       time.Duration
}

// Models lists the catalog entries the key can use. Callers asking for the
// same key share one fetch; each waits only as long as its own ctx allows.
func (r *Runtime) Models(ctx context.Context) ([]airuntime.Model, error) {
	key, err := r.apiKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: could not read the OpenAI API key", airuntime.ErrUnavailable)
	}
	if key == "" {
		return nil, fmt.Errorf("%w: OpenAI API key is not set", airuntime.ErrNotConfigured)
	}
	hash := sha256.Sum256([]byte(key))

	r.modelsMu.Lock()
	c := r.models
	r.modelsMu.Unlock()
	if c != nil && c.keyHash == hash && r.now().Sub(c.fetchedAt) < c.ttl {
		return cloneModels(c.models), c.err
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%w: %v", airuntime.ErrUnavailable, ctx.Err())
	}
	// The fetch runs on its own deadline, so a caller that gives up neither
	// cancels it for the others nor keeps its failure out of the cache.
	ch := r.modelsFlight.DoChan(hex.EncodeToString(hash[:]), func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.modelsTimeout)
		defer cancel()
		now := r.now()
		models, err := r.listModels(fetchCtx, key)
		entry := &cachedModels{keyHash: hash, models: models, fetchedAt: now, ttl: modelCacheTTL}
		if err != nil {
			entry = &cachedModels{keyHash: hash, err: err, fetchedAt: now, ttl: failureCacheTTL}
		}
		r.modelsMu.Lock()
		r.models = entry
		r.modelsMu.Unlock()
		return models, err
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return cloneModels(res.Val.([]airuntime.Model)), nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %v", airuntime.ErrUnavailable, ctx.Err())
	}
}

// cloneModels copies the catalog down to the effort lists, so a caller that
// edits what it got does not edit the cache.
func cloneModels(in []airuntime.Model) []airuntime.Model {
	if in == nil {
		return nil
	}
	out := make([]airuntime.Model, len(in))
	for i, m := range in {
		m.SupportedReasoningEfforts = append([]airuntime.ReasoningEffortOption(nil), m.SupportedReasoningEfforts...)
		out[i] = m
	}
	return out
}

func (r *Runtime) listModels(ctx context.Context, key string) ([]airuntime.Model, error) {
	raw, err := r.do(ctx, key, http.MethodGet, "/models", nil)
	var he *httpError
	switch {
	case errors.As(err, &he) && he.status == http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: OpenAI rejected the API key", airuntime.ErrNotLoggedIn)
	case he != nil && he.status == http.StatusForbidden:
		return nil, fmt.Errorf("%w: OpenAI API key is not permitted to list models", airuntime.ErrUnavailable)
	case err != nil:
		return nil, fmt.Errorf("%w: OpenAI API is unavailable", airuntime.ErrUnavailable)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%w: OpenAI model list could not be read", airuntime.ErrUnavailable)
	}
	usable := map[string]bool{}
	for _, m := range list.Data {
		usable[m.ID] = true
	}
	models := []airuntime.Model{}
	for _, m := range catalog {
		if !usable[m.ID] {
			continue
		}
		m.IsDefault = m.ID == r.cfg.DefaultModel
		m.SupportedReasoningEfforts = append([]airuntime.ReasoningEffortOption(nil), m.SupportedReasoningEfforts...)
		models = append(models, m)
	}
	return models, nil
}

// Status is Models' outcome: a key alone does not make the runtime available,
// and neither does a key that cannot use the default model.
// An API key has no account email, plan or rate-limit meter to report.
func (r *Runtime) Status(ctx context.Context) airuntime.Status {
	models, err := r.Models(ctx)
	switch {
	case errors.Is(err, airuntime.ErrNotConfigured):
		return airuntime.Status{Reason: "OpenAI API key is not set"}
	case err != nil:
		return airuntime.Status{Configured: true, Reason: err.Error()}
	case len(models) == 0:
		return airuntime.Status{Configured: true, Reason: "OpenAI API key cannot use any supported model"}
	}
	for _, m := range models {
		if m.ID == r.cfg.DefaultModel {
			return airuntime.Status{Configured: true, Available: true}
		}
	}
	return airuntime.Status{Configured: true, Reason: "OpenAI API key cannot use the default model " + r.cfg.DefaultModel}
}
