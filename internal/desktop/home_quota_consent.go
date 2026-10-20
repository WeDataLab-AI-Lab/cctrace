package desktop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"

	"cctrace/internal/atomicfile"
	"cctrace/internal/profile"
)

type quotaConsentRecovery struct {
	Existed bool   `json:"existed"`
	After   string `json:"after"`
}

func consentDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Caller holds accountsMu. Profile transaction's pending barrier remains active
// until these explicit opt-ins (including existing disabled rows) are durable.
func (s *Service) finalizeHomeQuotaConsent(directory string, updates []profile.HomeUpdate) error {
	path := filepath.Join(s.appSupportRoot, accountStoreFile)
	original, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if existed {
		if err = atomicfile.Write(filepath.Join(directory, "quota-before.json"), original, 0600); err != nil {
			return err
		}
	}
	next := s.accountsCloneLocked()
	for _, u := range updates {
		for _, h := range u.Homes {
			present := false
			for _, a := range next.Accounts {
				if a.Provider == h.Provider && accountHomeMatches(a, h.Home) {
					present = true
					break
				}
			}
			if present {
				continue
			}
			if len(next.Accounts) >= maxQuotaAccounts {
				return errors.New("quota account limit")
			}
			id, e := newSelectionReference()
			if e != nil {
				return e
			}
			ref, e := newSelectionReference()
			if e != nil {
				return e
			}
			next.Accounts = append(next.Accounts, quotaAccountRecord{ID: "account_" + id, Ref: ref, Generation: 1, Provider: h.Provider, Home: h.Home, Label: u.Name, Enabled: h.QuotaEnabled, Automatic: true, Profiles: []quotaAccountProfile{}})
		}
	}
	if next.Revision == math.MaxUint64 {
		return errors.New("quota revision exhausted")
	}
	next.Revision++
	for i := range next.Accounts {
		a := &next.Accounts[i]
		chosen, enabled := false, false
		for _, u := range updates {
			for _, h := range u.Homes {
				if a.Provider == h.Provider && accountHomeMatches(*a, h.Home) {
					chosen = true
					enabled = enabled || h.QuotaEnabled
				}
			}
		}
		if chosen {
			a.Enabled = enabled
			a.Revision = next.Revision
			if rotateAccount(a) != nil {
				return errors.New("quota reference unavailable")
			}
		}
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	receipt, _ := json.Marshal(quotaConsentRecovery{Existed: existed, After: consentDigest(encoded)})
	if err = atomicfile.Write(filepath.Join(directory, "quota-recovery.json"), receipt, 0600); err != nil {
		return err
	}
	if e := s.commitAccountsLocked(next); e != nil {
		return errors.New(e.Code)
	}
	return nil
}
func (s *Service) recoverHomeConsent(raw json.RawMessage) (any, *ErrorObject) {
	var p struct {
		Confirm bool `json:"confirm"`
	}
	if decodePayload(raw, &p) != nil || !p.Confirm || s.registry == nil {
		return nil, payloadError()
	}
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	err := s.registry.RecoverHomeTransactionWithFinalizer(func(directory string) error {
		if err := recoverHomeProviderConsent(directory); err != nil {
			return err
		}
		var quotaErr error
		lockErr := s.withAccountStoreLock(func() *ErrorObject {
			quotaErr = s.recoverQuotaConsent(directory)
			if quotaErr != nil {
				return accountError("storage_error")
			}
			return nil
		})
		if quotaErr != nil {
			return quotaErr
		}
		if lockErr != nil {
			return errors.New(lockErr.Code)
		}
		return nil
	})
	if err != nil {
		return nil, sharedProfileError(err)
	}
	s.accounts = nil
	return struct {
		Recovered bool `json:"recovered"`
	}{true}, nil
}

func (s *Service) recoverQuotaConsent(directory string) error {
	receipt, e := os.ReadFile(filepath.Join(directory, "quota-recovery.json"))
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	var r quotaConsentRecovery
	if json.Unmarshal(receipt, &r) != nil {
		return profile.ErrUnsafe
	}
	path := filepath.Join(s.appSupportRoot, accountStoreFile)
	current, e := os.ReadFile(path)
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	before, e := os.ReadFile(filepath.Join(directory, "quota-before.json"))
	if r.Existed && e != nil {
		return e
	}
	if !slices.Equal(current, before) && consentDigest(current) != r.After {
		return profile.ErrConflict
	}
	if r.Existed {
		return atomicfile.Write(path, before, 0600)
	}
	if e = os.Remove(path); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return nil
}
