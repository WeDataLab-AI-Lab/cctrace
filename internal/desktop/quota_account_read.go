package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cctrace/internal/codexappserver"
	"cctrace/internal/usage"
)

type accountSample struct {
	Epoch   string
	Quota   quotaResult
	Proof   string
	Billing string
}
type accountReading struct {
	Ref        string `json:"account_ref"`
	Generation uint64 `json:"generation"`
	Source     string `json:"source"`
	quotaResult
}
type accountFlight struct {
	selectedHome string
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	sample       accountSample
	err          *ErrorObject
	waiters      int
}

func (s *Service) accountResult(a quotaAccountRecord, q quotaResult) accountReading {
	return accountReading{Ref: a.Ref, Generation: a.Generation, Source: s.quotaSource(a.Provider), quotaResult: q}
}
func (s *Service) readQuotaAccount(ctx context.Context, raw json.RawMessage) (any, *ErrorObject) {
	return s.readQuotaAccountSelected(ctx, raw, "")
}
func (s *Service) readQuotaAccountSelected(ctx context.Context, raw json.RawMessage, selectedHome string) (any, *ErrorObject) {
	_, ref, gen, e := accountPayload(raw, "")
	if e != nil {
		return nil, e
	}
	s.accountsMu.Lock()
	if e = s.loadAccountsLocked(); e != nil {
		s.accountsMu.Unlock()
		return nil, e
	}
	i := s.accountIndexLocked(ref, gen)
	if i < 0 {
		s.accountsMu.Unlock()
		return nil, accountError("account_mismatch")
	}
	a := s.accounts.Accounts[i]
	if selectedHome != "" {
		if a.Home != selectedHome {
			s.accountsMu.Unlock()
			return nil, accountError("account_mismatch")
		}
		a.Aliases = nil
	}
	s.accountsMu.Unlock()
	if !a.Enabled || a.RetentionPending {
		return s.accountResult(a, quotaUnavailable(a.Provider, "provider_disabled", safeProviderErrorMessage("provider_disabled"))), nil
	}
	if !s.accountDefinitionsCurrent(a) {
		return nil, accountError("account_mismatch")
	}
	if _, err := s.accountHome(a.Home); err != nil {
		return s.accountResult(a, quotaUnavailable(a.Provider, "provider_not_configured", safeProviderErrorMessage("provider_not_configured"))), nil
	}
	s.accountFlightMu.Lock()
	if s.accountFlights == nil {
		s.accountFlights = map[string]*accountFlight{}
		s.accountRetry = map[string]time.Time{}
		s.accountSlots = make(chan struct{}, 4)
	}
	if s.now().Before(s.accountRetry[a.ID]) {
		s.accountFlightMu.Unlock()
		return s.accountResult(a, quotaUnavailable(a.Provider, "rate_limited", safeProviderErrorMessage("rate_limited"))), nil
	}
	flight := s.accountFlights[ref]
	if flight != nil && flight.ctx.Err() != nil {
		flight = nil
	}
	if flight != nil && selectedHome != "" && flight.selectedHome != selectedHome {
		s.accountFlightMu.Unlock()
		return nil, accountError("quota_busy")
	}
	if flight == nil {
		fctx, cancel := context.WithTimeout(context.Background(), providerReadTimeout)
		if len(s.accountFlights) >= maxConcurrentQuotaRequests {
			cancel()
			s.accountFlightMu.Unlock()
			return nil, accountError("quota_busy")
		}
		flight = &accountFlight{ctx: fctx, cancel: cancel, done: make(chan struct{}), selectedHome: selectedHome}
		s.accountFlights[ref] = flight
		go s.runAccountRead(a, flight)
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
		return nil, flight.err
	}
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	i = s.accountIndexLocked(ref, gen)
	if i < 0 {
		return nil, accountError("account_mismatch")
	}
	if _, err := s.accountHome(a.Home); err != nil {
		return nil, accountError("account_unavailable")
	}
	if !s.accountDefinitionsCurrent(a) {
		return nil, accountError("account_mismatch")
	}
	if selectedHome != "" && s.accounts.Accounts[i].Home != selectedHome {
		return nil, accountError("account_mismatch")
	}
	sample := flight.sample
	if sample.Quota.Error != nil && sample.Quota.Error.Code != "rate_limited" {
		s.accounts.Accounts[i].Proof = ""
	}
	if sample.Quota.Error != nil && sample.Quota.Error.Code == "account_changed" {
		next := s.accountsCloneLocked()
		changed := &next.Accounts[i]
		changed.Proof = ""
		changed.IdentityKey = ""
		changed.Aliases = nil
		changed.BillingID = ""
		if rotateAccount(changed) != nil {
			return nil, accountError("account_unavailable")
		}
		if e := s.commitAccountsLocked(next); e != nil {
			return nil, e
		}
		return nil, accountError("account_mismatch")
	}
	if sample.Epoch != "" {
		current := s.accounts.Accounts[i]
		if current.Epoch != "" && current.Epoch != sample.Epoch {
			next := s.accountsCloneLocked()
			row := &next.Accounts[i]
			row.Epoch = sample.Epoch
			if rotateAccount(row) != nil {
				return nil, accountError("account_unavailable")
			}
			if e := s.commitAccountsLocked(next); e != nil {
				return nil, e
			}
			return nil, accountError("account_mismatch")
		}
		s.accounts.Accounts[i].Epoch = sample.Epoch
	}
	if sample.Proof != "" {
		if e = s.applyAccountProofLocked(i, sample.Proof, sample.Billing); e != nil {
			return nil, e
		}
		i = s.accountIndexLocked(ref, gen)
		if i < 0 {
			return nil, accountError("account_mismatch")
		}
	}
	return s.accountResult(s.accounts.Accounts[i], sample.Quota), nil
}
func (s *Service) runAccountRead(a quotaAccountRecord, f *accountFlight) {
	defer func() {
		s.accountFlightMu.Lock()
		if s.accountFlights[a.Ref] == f {
			delete(s.accountFlights, a.Ref)
		}
		close(f.done)
		s.accountFlightMu.Unlock()
		f.cancel()
	}()
	select {
	case s.accountSlots <- struct{}{}:
		defer func() { <-s.accountSlots }()
	case <-f.ctx.Done():
		f.err = accountError("request_cancelled")
		return
	}
	var sample accountSample
	var err error
	for _, home := range append([]string{a.Home}, a.Aliases...) {
		if _, e := s.accountHome(home); e != nil {
			err = errCodexIdentityUnavailable
		} else if s.accountReader != nil {
			sample, err = s.accountReader(f.ctx, a.Provider, home)
		} else {
			sample, err = s.liveAccountRead(f.ctx, a.Provider, home)
		}
		if err == nil {
			break
		}
		if providerQuotaErrorCode(err) != "authentication_required" {
			break
		}
	}
	if err == nil && len(a.Aliases) > 0 && s.accountReader == nil {
		for _, home := range append([]string{a.Home}, a.Aliases...) {
			proof, _, e := s.accountMetadataProof(f.ctx, a.Provider, home)
			if e != nil && providerQuotaErrorCode(e) == "authentication_required" {
				continue
			}
			if e != nil || proof != sample.Proof {
				err = errCodexIdentityChanged
				sample = accountSample{}
				break
			}
		}
	}
	if err == nil && a.IdentityKey != "" {
		sum := sha256.Sum256([]byte(sample.Proof))
		if sample.Proof == "" || hex.EncodeToString(sum[:]) != a.IdentityKey {
			sample = accountSample{}
			err = errCodexIdentityChanged
		}
	}

	if err != nil {
		var rate *usage.RateLimitError
		if errors.As(err, &rate) {
			delay := rate.RetryAfter
			if delay <= 0 {
				delay = time.Minute
			}
			s.accountFlightMu.Lock()
			s.accountRetry[a.ID] = s.now().Add(delay)
			s.accountFlightMu.Unlock()
		}
		code := providerQuotaErrorCode(err)
		sample.Quota = quotaUnavailable(a.Provider, code, safeProviderErrorMessage(code))
	}
	if f.ctx.Err() != nil {
		f.err = accountError("request_cancelled")
		return
	}
	if sample.Quota.State == "fresh" {
		active := []quotaWindow{}
		for _, w := range sample.Quota.Windows {
			reset, e := time.Parse(time.RFC3339Nano, w.ResetsAt)
			if e == nil && reset.After(s.now().UTC()) {
				active = append(active, w)
			}
		}
		sample.Quota.Windows = active
		if sample.Quota.Provider != a.Provider || !validAccountQuota(sample.Quota, s.now().UTC()) {
			sample = accountSample{Quota: quotaUnavailable(a.Provider, "provider_unavailable", safeProviderErrorMessage("provider_unavailable"))}
		}
	}
	f.sample = sample
}
func validAccountQuota(q quotaResult, now time.Time) bool {
	if q.LastSuccessAt == nil {
		return false
	}
	t, e := time.Parse(time.RFC3339Nano, *q.LastSuccessAt)
	if e != nil || t.After(now) || now.Sub(t) > quotaStaleAfter || len(q.Windows) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, w := range q.Windows {
		if (w.Name != "five_hour" && w.Name != "seven_day") || seen[w.Name] {
			return false
		}
		seen[w.Name] = true
		if math.IsNaN(w.Utilization) || math.IsInf(w.Utilization, 0) || w.Utilization < 0 || w.Utilization > 100 {
			return false
		}
		reset, e := time.Parse(time.RFC3339Nano, w.ResetsAt)
		if e != nil || !reset.After(now) {
			return false
		}
	}
	return true
}
func (s *Service) liveAccountRead(ctx context.Context, provider, home string) (accountSample, error) {
	if !s.providerMode {
		return accountSample{Quota: s.readFixtureQuota(provider)}, nil
	}
	var proof, billing, credential string
	var sampled time.Time
	var windows []quotaWindow
	if provider == "claude" {
		reader, ok := s.providerReader.(interface {
			ReadClaudeAccount(context.Context, string) (*usage.AccountResponse, error)
		})
		if !ok {
			return accountSample{}, errors.New("account identity unavailable")
		}
		result, e := reader.ReadClaudeAccount(ctx, home)
		if e != nil {
			return accountSample{}, e
		}
		if result == nil || result.Usage == nil {
			return accountSample{}, errInvalidProviderResponse
		}
		var err error
		windows, err = claudeQuotaWindows(result.Usage)
		if err != nil {
			return accountSample{}, err
		}
		sampled = result.Usage.FetchedAt
		credential = result.Usage.TokenFingerprint
		if result.AccountID != "" && result.OrganizationID != "" {
			proof = "claude\x00" + result.AccountID + "\x00" + result.OrganizationID + "\x00all-model-five-seven"
			billing = result.AccountID
		}
	} else {
		id, epoch, e := boundedCodexAccount(home)
		credential = epoch
		if e != nil {
			return accountSample{}, e
		}
		// Account proof requires a fresh app-server response, never a home-only cache.
		var snapshot *codexappserver.Snapshot
		if _, live := s.providerReader.(liveProviderQuotaReader); live {
			snapshot, e = codexappserver.FetchUncached(ctx, home)
		} else {
			snapshot, e = s.providerReader.ReadCodexQuota(ctx, home)
		}
		if e != nil {
			return accountSample{}, e
		}
		after, afterEpoch, e := boundedCodexAccount(home)
		if e != nil || afterEpoch != epoch || after != id {
			return accountSample{}, errCodexIdentityChanged
		}
		windows, e = codexQuotaWindows(snapshot)
		if e != nil {
			return accountSample{}, e
		}
		sampled = snapshot.FetchedAt
		if id != "" {
			proof = "codex\x00" + id + "\x00codex-bucket"
			billing = id
		}
	}
	stamp := sampled.UTC().Format(time.RFC3339Nano)
	return accountSample{Quota: quotaResult{Provider: provider, State: "fresh", LastSuccessAt: &stamp, Windows: windows}, Proof: proof, Billing: billing, Epoch: credential}, nil
}
func (liveProviderQuotaReader) ReadClaudeAccount(ctx context.Context, home string) (*usage.AccountResponse, error) {
	return usage.FetchAccountContext(ctx, home)
}
func boundedCodexAccount(home string) (string, string, error) {
	path := filepath.Join(home, "auth.json")
	info, e := os.Lstat(path)
	if e != nil || info.Mode().Perm() != 0600 {
		return "", "", errCodexIdentityUnavailable
	}
	b, f := readDaemonFile(path, maxCodexAuthBytes)
	if f != nil || !lifecycleJSONHasUniqueKeys(b) {
		return "", "", errCodexIdentityUnavailable
	}
	var wire struct {
		Tokens struct {
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(b, &wire) != nil {
		return "", "", errCodexIdentityUnavailable
	}
	id := wire.Tokens.AccountID
	if id != "" && (len(id) > 128 || strings.ContainsAny(id, "/\\@ \n\r")) {
		return "", "", errCodexIdentityUnavailable
	}
	sum := sha256.Sum256(b)
	return id, hex.EncodeToString(sum[:]), nil
}
func (s *Service) cancelAccountFlight(ref string) bool {
	s.accountFlightMu.Lock()
	defer s.accountFlightMu.Unlock()
	cancelled := false
	if f := s.accountFlights[ref]; f != nil {
		f.cancel()
		cancelled = true
	}
	for _, f := range s.recoveryFlights {
		if f.ref == ref {
			f.cancel()
			cancelled = true
		}
	}
	return cancelled
}
func (s *Service) cancelAllAccountFlights() {
	s.accountFlightMu.Lock()
	defer s.accountFlightMu.Unlock()
	for _, f := range s.accountFlights {
		f.cancel()
	}
	for _, f := range s.recoveryFlights {
		f.cancel()
	}
}
func (s *Service) cancelQuotaAccount(raw json.RawMessage) (any, *ErrorObject) {
	_, ref, gen, e := accountPayload(raw, "")
	if e != nil {
		return nil, e
	}
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	if e = s.loadAccountsLocked(); e != nil {
		return nil, e
	}
	if s.accountIndexLocked(ref, gen) < 0 {
		return nil, accountError("account_mismatch")
	}
	return struct {
		Cancelled bool `json:"cancelled"`
	}{s.cancelAccountFlight(ref)}, nil
}
func (s *Service) applyAccountProofLocked(i int, proof, billing string) *ErrorObject {
	sum := sha256.Sum256([]byte(proof))
	key := hex.EncodeToString(sum[:])
	current := s.accounts.Accounts[i]
	if current.IdentityKey != "" && current.IdentityKey != key {
		next := s.accountsCloneLocked()
		a := &next.Accounts[i]
		a.Proof = ""
		a.IdentityKey = ""
		a.BillingID = ""
		a.Aliases = nil
		if rotateAccount(a) != nil {
			return accountError("account_unavailable")
		}
		if e := s.commitAccountsLocked(next); e != nil {
			return e
		}
		return accountError("account_mismatch")
	}
	next := s.accountsCloneLocked()
	a := &next.Accounts[i]
	a.Proof = proof
	a.IdentityKey = key
	a.BillingID = billing
	partner := -1
	for j, b := range next.Accounts {
		if j != i && b.Provider == a.Provider && b.IdentityKey == key && b.Proof == proof {
			partner = j
			break
		}
	}
	if partner < 0 {
		return s.commitAccountsLocked(next)
	}
	j := partner
	left, right := next.Accounts[i], next.Accounts[j]
	winner := left
	if right.ID < left.ID {
		winner = right
	}
	latest := left
	if right.Revision > left.Revision {
		latest = right
	}
	winner.Enabled = latest.Enabled
	winner.Revision = latest.Revision
	winner.Personal = left.Personal || right.Personal
	winner.Automatic = left.Automatic || right.Automatic
	winner.PersonalHomes = append([]string{}, left.PersonalHomes...)
	for _, h := range right.PersonalHomes {
		if !containsHome(winner.PersonalHomes, h) {
			winner.PersonalHomes = append(winner.PersonalHomes, h)
		}
	}
	homes := []string{}
	for _, b := range []quotaAccountRecord{left, right} {
		for _, h := range append([]string{b.Home}, b.Aliases...) {
			if !containsHome(homes, h) {
				homes = append(homes, h)
			}
		}
	}
	sort.Strings(homes)
	winner.Home = homes[0]
	winner.Aliases = append([]string{}, homes[1:]...)
	winner.Proof = proof
	winner.BillingID = billing
	winner.IdentityKey = key
	winner.Profiles = mergeQuotaProfileLinks(append(append([]quotaAccountProfile{}, left.Profiles...), right.Profiles...))
	winner.Definitions = map[string]string{}
	for _, b := range []quotaAccountRecord{left, right} {
		for k, v := range b.Definitions {
			winner.Definitions[k] = v
		}
	}
	if rotateAccount(&winner) != nil {
		return accountError("account_unavailable")
	}
	retained := []quotaAccountRecord{}
	for k, b := range next.Accounts {
		if k != i && k != j {
			retained = append(retained, b)
		}
	}
	next.Accounts = append(retained, winner)
	if e := s.commitAccountsLocked(next); e != nil {
		return e
	}
	s.cancelAccountFlight(left.Ref)
	s.cancelAccountFlight(right.Ref)
	return accountError("account_mismatch")
}

func (s *Service) waitForAccountReads() {
	s.accountFlightMu.Lock()
	done := []chan struct{}{}
	for _, f := range s.accountFlights {
		done = append(done, f.done)
	}
	for _, f := range s.recoveryFlights {
		done = append(done, f.done)
	}
	s.accountFlightMu.Unlock()
	for _, ch := range done {
		<-ch
	}
}
