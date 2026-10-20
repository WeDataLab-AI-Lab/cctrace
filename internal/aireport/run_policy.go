package aireport

import (
	"context"
	"errors"
	"fmt"
)

// ErrInvalidRunPolicy refuses a limit outside 1..MaxConcurrentLimit.
var ErrInvalidRunPolicy = errors.New("invalid run policy")

// MaxConcurrentLimit is the most runs an admin may let go at once. Every run is
// a runtime process and a stream of provider calls on one account, so the range
// stays where one server and one subscription can carry it.
const MaxConcurrentLimit = 10

// DefaultMaxConcurrent is the limit when neither CCTRACE_AI_MAX_CONCURRENT nor
// an admin sets one.
const DefaultMaxConcurrent = 2

// RunPolicy is how report runs are admitted, resolved against the defaults,
// with the layer each part came from so the screen can mark a value the admin
// has not set.
type RunPolicy struct {
	// ConsentRequired makes a run wait for the user's own consent. Off by
	// default: the admin already chose to send records when turning reports on
	// and picking the runtime, and asking each user again only kept their weekly
	// run from starting by itself.
	ConsentRequired       bool
	ConsentRequiredSource string
	// MaxConcurrent is how many runs go at once; the rest wait their turn.
	// DefaultMaxConcurrent is the server's own value, which stands until an
	// admin sets one.
	MaxConcurrent        int
	MaxConcurrentSource  string
	DefaultMaxConcurrent int
}

// RunPolicy resolves the admin's run policy against the defaults.
func (s *Service) RunPolicy(ctx context.Context) (RunPolicy, error) {
	p := RunPolicy{
		ConsentRequiredSource: SourceDefault,
		MaxConcurrent:         s.cfg.MaxConcurrent, MaxConcurrentSource: SourceDefault, DefaultMaxConcurrent: s.cfg.MaxConcurrent,
	}
	saved, err := s.st.GetAIRunPolicy(ctx)
	if err != nil || saved == nil {
		return p, err
	}
	if saved.ConsentRequired != nil {
		p.ConsentRequired, p.ConsentRequiredSource = *saved.ConsentRequired, SourceAdmin
	}
	// SetRunPolicy refuses a value out of range, so one here was written by
	// hand; 0 would admit no run at all, and it reads as no admin word.
	if n := saved.MaxConcurrent; n != nil && *n >= 1 && *n <= MaxConcurrentLimit {
		p.MaxConcurrent, p.MaxConcurrentSource = *n, SourceAdmin
	}
	return p, nil
}

// LoadRunLimit hands the saved limit to the run queue. The server calls it
// once at boot, before any run can start; after that only SetRunPolicy moves
// the limit. A start that read it again could land after a save it raced and
// put the old limit back.
func (s *Service) LoadRunLimit(ctx context.Context) error {
	p, err := s.RunPolicy(ctx)
	if err != nil {
		return err
	}
	s.queue.setLimit(p.MaxConcurrent)
	return nil
}

// SetRunPolicy saves the admin's run policy; a nil field clears that one and
// hands it back to the default. A new limit applies to the runs already
// waiting, and the answer is the policy now in effect.
func (s *Service) SetRunPolicy(ctx context.Context, consentRequired *bool, maxConcurrent *int, actor string) (RunPolicy, error) {
	if maxConcurrent != nil && (*maxConcurrent < 1 || *maxConcurrent > MaxConcurrentLimit) {
		return RunPolicy{}, fmt.Errorf("%w: max_concurrent %d", ErrInvalidRunPolicy, *maxConcurrent)
	}
	if err := s.st.SetAIRunPolicy(ctx, consentRequired, maxConcurrent, actor); err != nil {
		return RunPolicy{}, err
	}
	p, err := s.RunPolicy(ctx)
	if err != nil {
		return p, err
	}
	s.queue.setLimit(p.MaxConcurrent)
	return p, nil
}
