package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"cctrace/internal/usage"
)

type sessionRecoveryFlight struct {
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	ref     string
	err     error
	waiters int
}

// recoverQuotaAccount is invoked only by the negotiated explicit recover op.
func (s *Service) recoverQuotaAccount(ctx context.Context, raw json.RawMessage) (any, *ErrorObject) {
	if ctx.Err() != nil {
		return nil, accountError("request_cancelled")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, ref, gen, e := accountPayload(raw, "")
	if e != nil {
		return nil, e
	}
	if !s.providerMode || !usage.ClaudeSessionRecoverySupported() {
		return nil, accountError("recovery_blocked")
	}
	s.accountsMu.Lock()
	a, e := s.recoverableAccountLocked(ref, gen, "")
	s.accountsMu.Unlock()
	if e != nil {
		return nil, e
	}
	s.accountFlightMu.Lock()
	if s.recoveryFlights == nil {
		s.recoveryFlights = map[string]*sessionRecoveryFlight{}
		s.recoveryRetry = map[string]time.Time{}
	}
	now := s.now()
	for home, until := range s.recoveryRetry {
		if !until.After(now) {
			delete(s.recoveryRetry, home)
		}
	}
	flight := s.recoveryFlights[a.Home]
	if flight != nil && flight.ctx.Err() != nil {
		s.accountFlightMu.Unlock()
		return nil, accountError("recovery_blocked")
	}
	if flight == nil {
		if now.Before(s.recoveryRetry[a.Home]) || len(s.recoveryRetry) >= maxQuotaAccounts || len(s.recoveryFlights) >= maxConcurrentQuotaRequests {
			s.accountFlightMu.Unlock()
			return nil, accountError("recovery_blocked")
		}
		fctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		flight = &sessionRecoveryFlight{ctx: fctx, cancel: cancel, done: make(chan struct{}), ref: ref}
		s.recoveryFlights[a.Home] = flight
		s.recoveryRetry[a.Home] = now.Add(20 * time.Second)
		if prior := s.accountFlights[ref]; prior != nil {
			prior.cancel()
		}
		go s.runSessionRecovery(a, flight)
	}
	flight.waiters++
	s.accountFlightMu.Unlock()
	defer func() {
		s.accountFlightMu.Lock()
		flight.waiters--
		if flight.waiters == 0 {
			flight.cancel()
		}
		s.accountFlightMu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return nil, accountError("request_cancelled")
	case <-flight.done:
	}
	if flight.err != nil {
		if errors.Is(flight.err, context.Canceled) {
			return nil, accountError("request_cancelled")
		}
		if errors.Is(flight.err, context.DeadlineExceeded) {
			return nil, accountError("recovery_blocked")
		}
		return nil, accountError(providerQuotaErrorCode(flight.err))
	}
	s.accountsMu.Lock()
	_, e = s.recoverableAccountLocked(ref, gen, a.Home)
	if e == nil {
		// Only this explicit owner operation can establish a new credential epoch.
		// Stable identity proof remains mandatory in runAccountRead.
		i := s.accountIndexLocked(ref, gen)
		s.accounts.Accounts[i].Epoch = ""
	}
	s.accountsMu.Unlock()
	if e != nil {
		return nil, e
	}
	return s.readQuotaAccountSelected(ctx, raw, a.Home)
}

func (s *Service) runSessionRecovery(a quotaAccountRecord, f *sessionRecoveryFlight) {
	home := a.Home
	defer func() {
		s.accountFlightMu.Lock()
		delete(s.recoveryFlights, home)
		close(f.done)
		s.accountFlightMu.Unlock()
		f.cancel()
	}()
	s.accountsMu.Lock()
	_, invalid := s.recoverableAccountLocked(a.Ref, a.Generation, a.Home)
	s.accountsMu.Unlock()
	if invalid != nil {
		f.err = usage.ErrRecoveryBlocked
		return
	}
	if f.ctx.Err() != nil {
		f.err = f.ctx.Err()
		return
	}
	recover := s.recoverClaudeSession
	if recover == nil {
		recover = usage.RecoverClaudeSession
	}
	f.err = recover(f.ctx, home, s.appSupportRoot)
	if f.ctx.Err() != nil {
		f.err = f.ctx.Err()
	}
}

// Caller holds accountsMu. Revalidate the exact home, never an alias.
func (s *Service) recoverableAccountLocked(ref string, gen uint64, home string) (quotaAccountRecord, *ErrorObject) {
	if e := s.loadAccountsLocked(); e != nil {
		return quotaAccountRecord{}, e
	}
	i := s.accountIndexLocked(ref, gen)
	if i < 0 {
		return quotaAccountRecord{}, accountError("account_mismatch")
	}
	a := s.accounts.Accounts[i]
	if a.Provider != "claude" || !a.Enabled || a.RetentionPending {
		return a, accountError("recovery_blocked")
	}
	if home != "" && a.Home != home || !s.accountDefinitionsCurrent(a) {
		return a, accountError("account_mismatch")
	}
	if actual, err := s.accountHome(a.Home); err != nil || actual != a.Home {
		return a, accountError("account_mismatch")
	}
	return a, nil
}
