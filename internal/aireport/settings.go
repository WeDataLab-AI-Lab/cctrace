package aireport

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"cctrace/internal/airuntime"
	"cctrace/internal/store"
)

// DefaultModel runs Codex reports when neither an admin nor
// CCTRACE_AI_MODEL_DEFAULT chose.
const DefaultModel = "gpt-5.6-terra"

// DefaultModels is each runtime's model when neither an admin nor its
// environment variable chose. LiteLLM has no default: the proxy decides the model.
var DefaultModels = map[string]string{
	DefaultRuntimeKey: DefaultModel,
	RuntimeOpenAI:     "gpt-5.6-terra",
	RuntimeClaude:     "claude-sonnet-5",
	RuntimeNVIDIA:     "z-ai/glm-5.3",
	RuntimeLiteLLM:    "",
}

// Where an effective setting came from, per item.
const (
	SourceAdmin   = "admin"
	SourceEnv     = "env"
	SourceDefault = "default"
)

var (
	ErrInvalidModel           = errors.New("model is not in the runtime's catalog")
	ErrInvalidReasoningEffort = errors.New("reasoning effort is not supported by the model")
	ErrInvalidBaseURL         = errors.New("base_url is invalid")
)

// validateBaseURL checks that the URL is valid for API endpoints:
// empty string is allowed (use runtime default), http/https only, no credentials.
func validateBaseURL(baseURL string) error {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("%w: invalid URL format", ErrInvalidBaseURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: only http and https schemes are allowed", ErrInvalidBaseURL)
	}
	if u.User != nil {
		return fmt.Errorf("%w: credentials in URL are not allowed", ErrInvalidBaseURL)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: host is required", ErrInvalidBaseURL)
	}
	return nil
}

// RuntimeEnv is one runtime's model and effort from the environment.
type RuntimeEnv struct {
	Model           string
	ReasoningEffort string
}

// Settings is what the next run on Runtime uses and why. An empty
// ReasoningEffort with SourceDefault leaves the choice to the model's own default.
type Settings struct {
	Runtime               string
	Model                 string
	ModelSource           string
	ReasoningEffort       string
	ReasoningEffortSource string
	BaseURL               string
	BaseURLSource         string
	EnvModel              string
	EnvReasoningEffort    string
	EnvBaseURL            string
}

// ResolveSettings applies admin > environment > default to the model and to
// the effort separately. Blank values count as unset.
func ResolveSettings(admin *store.AISettings, env RuntimeEnv, defaultModel string) Settings {
	s := Settings{EnvModel: strings.TrimSpace(env.Model), EnvReasoningEffort: strings.TrimSpace(env.ReasoningEffort)}
	var adminModel, adminEffort, adminBaseURL string
	if admin != nil {
		adminModel, adminEffort, adminBaseURL = strings.TrimSpace(admin.Model), strings.TrimSpace(admin.ReasoningEffort), strings.TrimSpace(admin.BaseURL)
	}
	s.Model, s.ModelSource = pick(adminModel, s.EnvModel, defaultModel)
	s.ReasoningEffort, s.ReasoningEffortSource = pick(adminEffort, s.EnvReasoningEffort, "")
	s.BaseURL, s.BaseURLSource = pick(adminBaseURL, s.EnvBaseURL, "")
	return s
}

func pick(admin, env, def string) (string, string) {
	switch {
	case admin != "":
		return admin, SourceAdmin
	case env != "":
		return env, SourceEnv
	default:
		return def, SourceDefault
	}
}

// Settings reads the active runtime's effective settings now, so a saved
// change applies to the next run without a restart.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	return s.SettingsFor(ctx, "")
}

// SettingsFor reads runtime key's effective settings; "" is the active runtime.
func (s *Service) SettingsFor(ctx context.Context, key string) (Settings, error) {
	key, err := s.runtimeKeyOrActive(ctx, key)
	if err != nil {
		return Settings{}, err
	}
	admin, err := s.st.GetAISettings(ctx, key)
	if err != nil {
		return Settings{}, err
	}
	st := ResolveSettings(admin, s.cfg.Env[key], DefaultModels[key])
	st.Runtime = key
	return st, nil
}

func (s *Service) runtimeKeyOrActive(ctx context.Context, key string) (string, error) {
	if key == "" {
		sel, err := s.Selection(ctx)
		switch {
		case err != nil:
			return "", err
		case !knownRuntime(sel.Key):
			return "", fmt.Errorf("%w: no runtime is chosen", airuntime.ErrNotConfigured)
		}
		return sel.Key, nil
	}
	if !knownRuntime(key) {
		return "", fmt.Errorf("%w: %s", ErrUnknownRuntime, key)
	}
	return key, nil
}

// Models lists runtime key's catalog; "" is the active runtime. Errors wrap
// ErrUnknownRuntime, airuntime.ErrNotConfigured, ErrNotLoggedIn or ErrUnavailable.
func (s *Service) Models(ctx context.Context, key string) ([]airuntime.Model, error) {
	key, err := s.runtimeKeyOrActive(ctx, key)
	if err != nil {
		return nil, err
	}
	catalog, ok := s.byKey[key].(airuntime.ModelCatalog)
	if !ok {
		return nil, airuntime.ErrNotConfigured
	}
	return catalog.Models(ctx)
}

// SetSettings saves the admin's override for runtime key ("" is the active
// runtime) after checking what the next run would use against that runtime's
// catalog: the resolved model must be listed, and the resolved effort -- the
// admin's, or the environment's it inherits -- must be one that model
// supports. Empty values clear the override and need no catalog, so an admin
// can fall back to the environment while the catalog is unreachable.
func (s *Service) SetSettings(ctx context.Context, key, model, effort, baseURL, actor string) (Settings, error) {
	key, err := s.runtimeKeyOrActive(ctx, key)
	if err != nil {
		return Settings{}, err
	}
	model, effort, baseURL = strings.TrimSpace(model), strings.TrimSpace(effort), strings.TrimSpace(baseURL)
	if err := validateBaseURL(baseURL); err != nil {
		return Settings{}, err
	}
	if model != "" || effort != "" {
		models, err := s.Models(ctx, key)
		if err != nil {
			return Settings{}, err
		}
		run := ResolveSettings(&store.AISettings{Model: model, ReasoningEffort: effort}, s.cfg.Env[key], DefaultModels[key])
		var found *airuntime.Model
		for i := range models {
			if models[i].ID == run.Model {
				found = &models[i]
			}
		}
		switch {
		case found == nil:
			return Settings{}, fmt.Errorf("%w: %s (%s)", ErrInvalidModel, run.Model, run.ModelSource)
		case run.ReasoningEffort != "" && !found.SupportsEffort(run.ReasoningEffort):
			return Settings{}, fmt.Errorf("%w: %s does not support %s (%s)", ErrInvalidReasoningEffort, run.Model, run.ReasoningEffort, run.ReasoningEffortSource)
		}
	}
	if err := s.st.SetAISettings(ctx, key, model, effort, baseURL, actor); err != nil {
		return Settings{}, err
	}
	return s.SettingsFor(ctx, key)
}
