package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"cctrace/internal/airuntime"
)

const (
	// modelCacheTTL: the catalog moves with codex releases and account changes,
	// not from one admin page load to the next.
	modelCacheTTL = 10 * time.Minute
	// maxModelPages bounds the cursor walk against a server that never ends it.
	maxModelPages = 20
)

type cachedModels struct {
	models    []airuntime.Model
	err       error
	fetchedAt time.Time
	ttl       time.Duration
}

// modelCache is keyed on the Codex home, like Fetch's cache: two homes can
// hold accounts that see different catalogs.
var (
	modelCacheMu sync.Mutex
	modelCache   = map[string]*cachedModels{}
)

// Models lists the models a report run may ask for. Hidden models are left
// out, as in codex's own picker.
func (r *CodexRuntime) Models(ctx context.Context) ([]airuntime.Model, error) {
	if r.cfg.Home == "" {
		return nil, fmt.Errorf("%w: codex home is not set", airuntime.ErrNotConfigured)
	}
	if AuthMode(r.cfg.Home, r.cfg.APIKey) == airuntime.AuthModeNone {
		return nil, fmt.Errorf("%w: no API key and no auth.json in the codex home", airuntime.ErrNotLoggedIn)
	}
	models, err := ListModels(ctx, r.cfg.Home)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", airuntime.ErrUnavailable, err)
	}
	return models, nil
}

// ListModels returns the home's model catalog from cache, refreshing it with
// one short app-server process. As in Fetch the lock spans the process, so
// concurrent callers share one child, and a failure is cached for
// failureCacheTTL so a broken install costs one process per interval.
func ListModels(ctx context.Context, codexHome string) ([]airuntime.Model, error) {
	modelCacheMu.Lock()
	defer modelCacheMu.Unlock()

	now := nowFn()
	if c := modelCache[codexHome]; c != nil && now.Sub(c.fetchedAt) < c.ttl {
		return c.models, c.err
	}
	models, err := listModels(ctx, codexHome)
	if err != nil {
		// The caller's own cancel says nothing about the catalog.
		if ctx.Err() == nil {
			modelCache[codexHome] = &cachedModels{err: err, fetchedAt: now, ttl: failureCacheTTL}
		}
		return nil, err
	}
	modelCache[codexHome] = &cachedModels{models: models, fetchedAt: now, ttl: modelCacheTTL}
	return models, nil
}

type rawModel struct {
	ID                        string `json:"id"`
	DisplayName               string `json:"displayName"`
	Description               string `json:"description"`
	Hidden                    bool   `json:"hidden"`
	IsDefault                 bool   `json:"isDefault"`
	DefaultReasoningEffort    string `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []struct {
		ReasoningEffort string `json:"reasoningEffort"`
		Description     string `json:"description"`
	} `json:"supportedReasoningEfforts"`
}

func listModels(ctx context.Context, codexHome string) ([]airuntime.Model, error) {
	bin, err := lookPathFn("codex")
	if err != nil {
		return nil, fmt.Errorf("codex CLI not found on PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	cmd := exec.Command(bin, "app-server")
	cmd.Env = childEnv(RuntimeConfig{Home: codexHome}, "")
	cmd.Stderr = nil
	setProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	// Fetch's teardown: kill the group before reaping, on every path.
	defer func() {
		_ = stdin.Close()
		killGroup(cmd.Process)
		_ = cmd.Wait()
	}()
	c := newConn(stdin, stdout, defaultMaxLineBytes, maxResponseBytes)
	defer c.Close()

	models, err := exchangeModels(ctx, c)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("codex app-server did not answer model/list within %s", fetchTimeout)
	}
	return models, err
}

func exchangeModels(ctx context.Context, c *conn) ([]airuntime.Model, error) {
	if _, err := c.Call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": clientName, "version": clientVersion},
	}); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if err := c.Notify("initialized", nil); err != nil {
		return nil, err
	}
	models := []airuntime.Model{}
	cursor := ""
	for page := 0; page < maxModelPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.Call(ctx, "model/list", params)
		if err != nil {
			return nil, fmt.Errorf("model/list: %w", err)
		}
		var resp struct {
			Data       []rawModel `json:"data"`
			NextCursor *string    `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("decode model/list: %w", err)
		}
		for _, m := range resp.Data {
			if m.Hidden || m.ID == "" {
				continue
			}
			out := airuntime.Model{
				ID: m.ID, DisplayName: m.DisplayName, Description: m.Description,
				IsDefault: m.IsDefault, DefaultReasoningEffort: m.DefaultReasoningEffort,
				SupportedReasoningEfforts: []airuntime.ReasoningEffortOption{},
			}
			for _, e := range m.SupportedReasoningEfforts {
				out.SupportedReasoningEfforts = append(out.SupportedReasoningEfforts, airuntime.ReasoningEffortOption{ReasoningEffort: e.ReasoningEffort, Description: e.Description})
			}
			models = append(models, out)
		}
		if resp.NextCursor == nil || *resp.NextCursor == "" {
			return models, nil
		}
		cursor = *resp.NextCursor
	}
	return nil, fmt.Errorf("model/list did not end within %d pages", maxModelPages)
}
