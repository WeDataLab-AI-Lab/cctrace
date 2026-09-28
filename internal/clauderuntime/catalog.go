package clauderuntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"cctrace/internal/airuntime"
)

const (
	modelsTTL        = 10 * time.Minute
	modelsFailureTTL = time.Minute
	// modelsRequestTimeout bounds one /v1/models fetch, which is a small list
	// and sits in front of the admin screen and every run start.
	modelsRequestTimeout = 15 * time.Second
)

// curatedModel is one model the report may run on. Efforts are the
// output_config.effort values the effort docs list for it; Haiku 4.5 rejects
// effort, so it has none. Adaptive thinking is on by default on Opus 5, Sonnet
// 5 and Fable 5.1 but must be requested on Opus 4.8; Haiku 4.5 has no adaptive
// mode.
type curatedModel struct {
	id          string
	description string
	efforts     []string
	adaptive    bool
}

var allEfforts = []string{"low", "medium", "high", "xhigh", "max"}

var curatedModels = []curatedModel{
	{id: "claude-fable-5-1", description: "Most capable; highest cost", efforts: allEfforts, adaptive: true},
	{id: "claude-opus-5", description: "Deep reasoning and long agentic work", efforts: allEfforts, adaptive: true},
	{id: "claude-sonnet-5", description: "Balance of speed and intelligence", efforts: allEfforts, adaptive: true},
	{id: "claude-opus-4-8", description: "Previous-generation Opus", efforts: allEfforts, adaptive: true},
	{id: "claude-haiku-4-5-20251001", description: "Fastest and cheapest; no effort setting"},
}

var effortDescriptions = map[string]string{
	"low":    "Fewest tokens, fastest",
	"medium": "Lower cost with good results",
	"high":   "API default",
	"xhigh":  "Long-horizon agentic work",
	"max":    "Highest capability, highest cost",
}

func lookupModel(id string) (curatedModel, bool) {
	for _, m := range curatedModels {
		if m.id == id {
			return m, true
		}
	}
	return curatedModel{}, false
}

// Models lists the curated models the key can use. Success is cached ten
// minutes and failure one minute, so Status polling costs little.
func (r *Runtime) Models(ctx context.Context) ([]airuntime.Model, error) {
	key, err := r.apiKey(ctx)
	if errors.Is(err, errNoKey) {
		return nil, fmt.Errorf("%w: %v", airuntime.ErrNotConfigured, err)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", airuntime.ErrUnavailable, err)
	}
	// The cache belongs to one key, so a replaced key is checked at once.
	sum := sha256.Sum256([]byte(key))
	fingerprint := hex.EncodeToString(sum[:])
	r.mu.Lock()
	if r.modKey == fingerprint && r.now().Before(r.modTill) {
		models, err := cloneModels(r.models), r.modErr
		r.mu.Unlock()
		return models, err
	}
	r.mu.Unlock()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%w: %v", airuntime.ErrUnavailable, ctx.Err())
	}
	// Callers asking for the same key share one fetch. It runs on its own
	// deadline, so a caller that gives up neither cancels it for the others
	// nor waits past its own ctx.
	ch := r.modelsFlight.DoChan(fingerprint, func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.modelsTimeout)
		defer cancel()
		models, err := r.fetchModels(fetchCtx, key)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.models, r.modErr, r.modKey = models, err, fingerprint
		if err != nil {
			r.models, r.modTill = nil, r.now().Add(modelsFailureTTL)
			return nil, err
		}
		r.modTill = r.now().Add(modelsTTL)
		return models, nil
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

func (r *Runtime) fetchModels(ctx context.Context, key string) ([]airuntime.Model, error) {
	names := map[string]string{}
	after := ""
	for {
		q := url.Values{"limit": {"1000"}}
		if after != "" {
			q.Set("after_id", after)
		}
		var page struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := r.do(ctx, key, http.MethodGet, "/v1/models?"+q.Encode(), nil, &page); err != nil {
			return nil, modelsError(err)
		}
		for _, m := range page.Data {
			names[m.ID] = m.DisplayName
		}
		if !page.HasMore || page.LastID == "" || page.LastID == after {
			break
		}
		after = page.LastID
	}

	var out []airuntime.Model
	for _, c := range curatedModels {
		display, ok := names[c.id]
		if !ok {
			continue
		}
		if display == "" {
			display = c.id
		}
		m := airuntime.Model{ID: c.id, DisplayName: display, Description: c.description, IsDefault: c.id == r.cfg.DefaultModel}
		for _, e := range c.efforts {
			m.SupportedReasoningEfforts = append(m.SupportedReasoningEfforts, airuntime.ReasoningEffortOption{ReasoningEffort: e, Description: effortDescriptions[e]})
		}
		if len(c.efforts) > 0 {
			m.DefaultReasoningEffort = "high"
		}
		out = append(out, m)
	}
	return out, nil
}

func modelsError(err error) error {
	var he *httpError
	if errors.As(err, &he) && he.status == 401 {
		return fmt.Errorf("%w: Anthropic API rejected the API key", airuntime.ErrNotLoggedIn)
	}
	return fmt.Errorf("%w: %s", airuntime.ErrUnavailable, runError(err).Message)
}

func (r *Runtime) Status(ctx context.Context) airuntime.Status {
	if _, err := r.apiKey(ctx); errors.Is(err, errNoKey) {
		return airuntime.Status{Reason: "Anthropic API key is not set"}
	}
	st := airuntime.Status{Configured: true}
	models, err := r.Models(ctx)
	switch {
	case err != nil:
		st.Reason = err.Error()
		return st
	case len(models) == 0:
		st.Reason = "Anthropic API key cannot use any supported model"
		return st
	}
	// A key that cannot use the default model cannot run a report as configured.
	for _, m := range models {
		if m.ID == r.cfg.DefaultModel {
			st.Available = true
			return st
		}
	}
	st.Reason = "Anthropic API key cannot use the default model " + r.cfg.DefaultModel
	return st
}
