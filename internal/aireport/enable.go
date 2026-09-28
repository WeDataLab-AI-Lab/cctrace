package aireport

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrRuntimeDisabled refuses runs and consent while AI reports are off.
	ErrRuntimeDisabled = errors.New("ai reports are disabled")
	// ErrNoRuntimeChosen refuses turning reports on with no runtime to run them.
	ErrNoRuntimeChosen = errors.New("no ai runtime is chosen")
	// ErrRuntimeNotConfigured refuses turning reports on while the chosen
	// runtime is built but cannot run, such as an API runtime without a key.
	ErrRuntimeNotConfigured = errors.New("the chosen ai runtime is not configured")
)

// Enablement is whether AI reports run and who decided. EnvEnabled is the
// environment's word, nil when it has none, shown beside an admin override.
type Enablement struct {
	Enabled    bool
	Source     string
	EnvEnabled *bool
}

// Enablement resolves the switch: the admin's, then CCTRACE_AI_ENABLED_DEFAULT,
// then a set CCTRACE_AI_RUNTIME_DEFAULT, which is how reports were turned on
// before the switch
// existed. Otherwise reports are off, so an upgrade or a key or codex CLI on
// the server never starts sending data by itself.
func (s *Service) Enablement(ctx context.Context) (Enablement, error) {
	e := Enablement{Source: SourceDefault, EnvEnabled: s.cfg.EnvEnabled}
	if e.EnvEnabled == nil && s.cfg.EnvRuntime != "" {
		on := true
		e.EnvEnabled = &on
	}
	choice, err := s.st.GetAIEnabledChoice(ctx)
	switch {
	case err != nil:
		return e, err
	case choice != nil:
		e.Enabled, e.Source = choice.Enabled, SourceAdmin
	case e.EnvEnabled != nil:
		e.Enabled, e.Source = *e.EnvEnabled, SourceEnv
	}
	return e, nil
}

// SetEnabled saves the admin's switch; nil clears it. Turning reports on needs
// a chosen runtime this server built and configured. Like a runtime switch it
// moves the generation, so a Start already past the check fails instead of
// running. A switch that leaves reports off also cancels the runs already
// going, so no more data leaves once it has answered.
func (s *Service) SetEnabled(ctx context.Context, enabled *bool, actor string) (Enablement, error) {
	if enabled != nil && *enabled {
		rt, sel, err := s.activeRuntime(ctx)
		if err != nil {
			return Enablement{}, err
		}
		if rt == nil {
			return Enablement{}, fmt.Errorf("%w: %q", ErrNoRuntimeChosen, sel.Key)
		}
		if st := s.status(ctx, rt); !st.Configured {
			return Enablement{}, fmt.Errorf("%w: %s", ErrRuntimeNotConfigured, st.Reason)
		}
	}
	end, err := s.beginSwitch(false)
	if err != nil {
		return Enablement{}, err
	}
	defer end()
	if err := s.st.SetAIEnabledChoice(ctx, enabled, actor); err != nil {
		return Enablement{}, err
	}
	e, err := s.Enablement(ctx)
	if (enabled != nil && !*enabled) || (err == nil && !e.Enabled) {
		s.cancelActive("runtime_disabled")
	}
	return e, err
}
