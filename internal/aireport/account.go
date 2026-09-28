package aireport

import (
	"context"
	"errors"

	"cctrace/internal/airuntime"
)

var (
	// ErrRunInProgress refuses an account change or a runtime switch while a
	// report run uses the runtime.
	ErrRunInProgress = errors.New("a report run is in progress")
	// ErrAccountChanging refuses a run while the runtime's login is changing.
	ErrAccountChanging = errors.New("the ai runtime account is changing")
)

// accountRuntime is the first built runtime whose account the admin screen can
// change, with its key; nil when there is none.
func (s *Service) accountRuntime() (airuntime.AccountManager, string) {
	for _, rt := range s.runtimes {
		if m, ok := rt.(airuntime.AccountManager); ok {
			return m, rt.Info().Key
		}
	}
	return nil, ""
}

// Accounts is the account management of the runtime that has one, whichever
// runtime is active. With none it answers like a missing runtime:
// airuntime.ErrNotConfigured.
//
// Runs on that runtime and changes share its home, so neither overlaps the
// other: a change is refused while a run on it is active, and a run on it while
// a change is executing or a device login is pending. Runs on other runtimes
// share nothing with that home and are not held back.
func (s *Service) Accounts() (airuntime.AccountManager, error) {
	m, key := s.accountRuntime()
	if m == nil {
		return nil, airuntime.ErrNotConfigured
	}
	return &accountGuard{AccountManager: m, s: s, key: key}, nil
}

type accountGuard struct {
	airuntime.AccountManager
	s   *Service
	key string
}

// begin claims a change, or refuses it while a run on the runtime is active.
// end releases it.
func (g *accountGuard) begin() (end func(), err error) {
	g.s.mu.Lock()
	defer g.s.mu.Unlock()
	for _, a := range g.s.active {
		if a.runtime == g.key {
			return nil, ErrRunInProgress
		}
	}
	g.s.changing++
	return func() {
		g.s.mu.Lock()
		g.s.changing--
		g.s.mu.Unlock()
	}, nil
}

func (g *accountGuard) StartDeviceLogin(ctx context.Context) (airuntime.DeviceLogin, error) {
	end, err := g.begin()
	if err != nil {
		return airuntime.DeviceLogin{}, err
	}
	defer end()
	l, err := g.AccountManager.StartDeviceLogin(ctx)
	if err == nil {
		// Recorded before end, so no run slips in between.
		g.s.mu.Lock()
		g.s.loginID = l.ID
		g.s.mu.Unlock()
	}
	return l, err
}

func (g *accountGuard) LoginAPIKey(ctx context.Context, key string) error {
	end, err := g.begin()
	if err != nil {
		return err
	}
	defer end()
	return g.AccountManager.LoginAPIKey(ctx, key)
}

func (g *accountGuard) Logout(ctx context.Context) error {
	end, err := g.begin()
	if err != nil {
		return err
	}
	defer end()
	return g.AccountManager.Logout(ctx)
}

// accountChanging reports a change a new run on runtime key must not overlap.
// s.mu is held.
func (s *Service) accountChanging(key string) bool {
	m, managerKey := s.accountRuntime()
	if m == nil || key != managerKey {
		return false
	}
	if s.changing > 0 {
		return true
	}
	if s.loginID == "" {
		return false
	}
	l, err := m.DeviceLogin(s.loginID)
	return err == nil && l.State == airuntime.LoginPending
}
