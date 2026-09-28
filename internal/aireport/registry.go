package aireport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cctrace/internal/airuntime"
)

// Runtime keys beside DefaultRuntimeKey. RuntimeKeys is the order the admin
// screen lists them.
const (
	RuntimeOpenAI  = "openai-api"
	RuntimeClaude  = "claude-api"
	RuntimeNVIDIA  = "nvidia-api"
	RuntimeLiteLLM = "litellm-api"
)

var RuntimeKeys = []string{DefaultRuntimeKey, RuntimeOpenAI, RuntimeClaude, RuntimeNVIDIA, RuntimeLiteLLM}

// RuntimeProviders maps each runtime to the provider its data goes to. Only the
// API runtimes use a keyring key; Codex keeps its own login.
var RuntimeProviders = map[string]string{
	DefaultRuntimeKey: "codex",
	RuntimeOpenAI:     ProviderOpenAI,
	RuntimeClaude:     ProviderAnthropic,
	RuntimeNVIDIA:     ProviderNVIDIA,
	RuntimeLiteLLM:    ProviderLiteLLM,
}

var (
	ErrUnknownRuntime = errors.New("unknown ai runtime")
	// ErrRuntimeChanging refuses a run while an admin's runtime switch is saved.
	ErrRuntimeChanging = errors.New("the ai runtime is being switched")
	// ErrRuntimeUnimplemented refuses a runtime this build cannot drive end to
	// end. Distinct from ErrNotConfigured: a key would not help.
	ErrRuntimeUnimplemented = errors.New("ai runtime is not implemented in this build")
)

// unimplementedRuntimes are known runtimes this build does not drive end to
// end. They stay in RuntimeKeys so the admin screen can still account for them
// and stored settings keep resolving; they are refused wherever a runtime is
// chosen. Removing a key from here is the whole of re-enabling it.
//
// nvidia-api: the chatruntime defect that used to block this -- the assistant
// content the provider returned was echoed back, so NVIDIA's vLLM gateway
// refused the next request ("Empty content is not allowed for assistant
// messages") -- was fixed in #743. What remains unmeasured is whether any model
// offered there holds up the report's end of the bargain: it must call the tools
// when told to (z-ai/glm-5.3 answered without ever calling read_segment) and
// honour a strict json_schema (diffusiongemma-26b ignored json_schema,
// nvext.guided_json and top-level guided_json alike -- 15 attempts, zero bare
// JSON). Until a model there is measured on both, the key stays. See the
// tracking issue.
var unimplementedRuntimes = map[string]bool{
	RuntimeNVIDIA: true,
}

// Unimplemented reports whether this build refuses key as a runtime choice.
func Unimplemented(key string) bool { return unimplementedRuntimes[key] }

// Why the selection cannot run reports, set by RuntimeViews.
const (
	ReasonRuntimeNotSelected = "runtime_not_selected"
	ReasonRuntimeUnavailable = "runtime_unavailable"
)

// Selection is the runtime chosen explicitly and by whom. Key is "" with
// SourceDefault when neither an admin nor CCTRACE_AI_RUNTIME_DEFAULT chose one.
// RuntimeViews sets ReasonCode and Reason when nothing is chosen or the chosen
// runtime cannot run here.
type Selection struct {
	Key        string
	Source     string
	EnvRuntime string
	ReasonCode string
	Reason     string
}

func knownRuntime(key string) bool {
	for _, k := range RuntimeKeys {
		if k == key {
			return true
		}
	}
	return false
}

// Selection resolves the chosen runtime: the admin's choice, then
// CCTRACE_AI_RUNTIME_DEFAULT. A choice this server cannot run stays the choice; it
// never falls through to another runtime, whose provider nobody chose.
func (s *Service) Selection(ctx context.Context) (Selection, error) {
	_, sel, err := s.activeRuntime(ctx)
	return sel, err
}

// activeRuntime returns the chosen runtime, nil when there is no choice or this
// server did not build the one chosen.
func (s *Service) activeRuntime(ctx context.Context) (airuntime.Runtime, Selection, error) {
	sel := Selection{Source: SourceDefault, EnvRuntime: s.cfg.EnvRuntime}
	choice, err := s.st.GetAIRuntimeChoice(ctx)
	switch {
	case err != nil:
		return nil, sel, err
	case choice != nil:
		sel.Key, sel.Source = choice.Runtime, SourceAdmin
	case s.cfg.EnvRuntime != "":
		sel.Key, sel.Source = s.cfg.EnvRuntime, SourceEnv
	default:
		return nil, sel, nil
	}
	// The screen refuses an unimplemented runtime, and the environment must not
	// be a way around it: CCTRACE_AI_RUNTIME_DEFAULT is read here without any
	// check of its own, so without this a deployment naming one would keep
	// running reports on it while the screen said it could not be chosen.
	if Unimplemented(sel.Key) {
		return nil, sel, nil
	}
	return s.byKey[sel.Key], sel, nil
}

// status is the runtime's own status, except that a registered key this server
// cannot decrypt leaves the runtime unconfigured with the re-register reason.
func (s *Service) status(ctx context.Context, rt airuntime.Runtime) airuntime.Status {
	st := rt.Status(ctx)
	p := RuntimeProviders[rt.Info().Key]
	if p == ProviderOpenAI || p == ProviderAnthropic || p == ProviderNVIDIA || p == ProviderLiteLLM {
		if info, err := s.keyring.Credential(ctx, p); err == nil && info.Reason != "" {
			return airuntime.Status{Reason: info.Reason}
		}
	}
	return st
}

// RuntimeView is one runtime as the admin screen shows it. Built false means
// this server has no such runtime; Status.Reason then says why.
type RuntimeView struct {
	Key        string
	Provider   string
	Built      bool
	Selected   bool
	Info       airuntime.Info
	Status     airuntime.Status
	Credential *CredentialInfo
}

// RuntimeViews lists every known runtime in RuntimeKeys order, with the
// selection they were marked against, so a caller reads the choice once. The
// selection carries why its runtime cannot run, if it cannot.
func (s *Service) RuntimeViews(ctx context.Context) ([]RuntimeView, Selection, error) {
	sel, err := s.Selection(ctx)
	if err != nil {
		return nil, sel, err
	}
	views := make([]RuntimeView, 0, len(RuntimeKeys))
	for _, key := range RuntimeKeys {
		v := RuntimeView{Key: key, Provider: RuntimeProviders[key], Info: airuntime.Info{Key: key, AuthMode: airuntime.AuthModeNone}}
		if rt := s.byKey[key]; rt != nil {
			v.Built, v.Selected = true, key == sel.Key
			v.Info, v.Status = rt.Info(), s.status(ctx, rt)
		} else {
			v.Status.Reason = s.cfg.Missing[key]
			if v.Status.Reason == "" {
				v.Status.Reason = "이 서버에 구성되지 않은 런타임입니다"
			}
		}
		if v.Provider == ProviderOpenAI || v.Provider == ProviderAnthropic || v.Provider == ProviderNVIDIA || v.Provider == ProviderLiteLLM {
			info, err := s.keyring.Credential(ctx, v.Provider)
			if err != nil {
				return nil, sel, err
			}
			v.Credential = &info
		}
		if v.Key == sel.Key && !v.Status.Configured {
			sel.ReasonCode, sel.Reason = ReasonRuntimeUnavailable, "선택한 런타임을 사용할 수 없습니다: "+v.Status.Reason
		}
		views = append(views, v)
	}
	switch {
	case sel.Key == "":
		sel.ReasonCode, sel.Reason = ReasonRuntimeNotSelected, "런타임이 선택되지 않았습니다. 사용할 런타임을 먼저 선택하세요"
	case !knownRuntime(sel.Key):
		sel.ReasonCode, sel.Reason = ReasonRuntimeUnavailable, "선택한 런타임을 사용할 수 없습니다: 알 수 없는 런타임입니다 ("+sel.Key+")"
	case Unimplemented(sel.Key):
		// Reached when the environment names it; the screen cannot choose one.
		sel.ReasonCode, sel.Reason = ReasonRuntimeUnavailable, "선택한 런타임을 사용할 수 없습니다: 아직 구현되지 않았습니다 ("+sel.Key+")"
	}
	return views, sel, nil
}

// SelectRuntime saves the admin's runtime; "" clears the choice. The runtime
// must be built and configured, and no report may be running.
func (s *Service) SelectRuntime(ctx context.Context, key, actor string) (Selection, error) {
	key = strings.TrimSpace(key)
	if key != "" {
		if !knownRuntime(key) {
			return Selection{}, fmt.Errorf("%w: %s", ErrUnknownRuntime, key)
		}
		// Before anything is asked about keys or availability: answering
		// "not configured" would invite an admin to register a key and retry,
		// which cannot help here.
		if Unimplemented(key) {
			return Selection{}, fmt.Errorf("%w: %s", ErrRuntimeUnimplemented, key)
		}
		rt := s.byKey[key]
		if rt == nil {
			return Selection{}, fmt.Errorf("%w: %s is not built on this server", airuntime.ErrNotConfigured, key)
		}
		if st := s.status(ctx, rt); !st.Configured {
			return Selection{}, fmt.Errorf("%w: %s", airuntime.ErrNotConfigured, st.Reason)
		}
	}
	end, err := s.beginSwitch(true)
	if err != nil {
		return Selection{}, err
	}
	defer end()
	if err := s.st.SetAIRuntimeChoice(ctx, key, actor); err != nil {
		return Selection{}, err
	}
	return s.Selection(ctx)
}

// beginSwitch marks a change to what Start may run on; call end when its write
// is done. The generation moves before and after the write: a Start that read
// before either move finds a different generation when it would become active,
// and one during the write is refused by switching. refuseActive refuses the
// change while any report runs.
func (s *Service) beginSwitch(refuseActive bool) (end func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if refuseActive && len(s.active) > 0 {
		return nil, ErrRunInProgress
	}
	s.switching++
	s.runtimeGen++
	return func() {
		s.mu.Lock()
		s.switching--
		s.runtimeGen++
		s.mu.Unlock()
	}, nil
}

// Keyring holds the provider API keys the API runtimes read.
func (s *Service) Keyring() *Keyring { return s.keyring }
