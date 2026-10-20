package desktop

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gofrs/flock"

	"cctrace/internal/atomicfile"
	"cctrace/internal/profile"
)

const maxQuotaAccounts = 256
const maxAccountStoreBytes = 1 << 20

var accountOpaque = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

const accountStoreFile = "quota-accounts.json"

type quotaAccountProfile struct {
	ExclusionState string `json:"exclusion_state,omitempty"`
	ID             string `json:"id"`
	Name           string `json:"name"`
	Collecting     *bool  `json:"collecting"`
	Excluded       *bool  `json:"excluded"`
}
type quotaAccountView struct {
	ConfigHomes      []string              `json:"config_homes,omitempty"`
	ConfigHome       string                `json:"config_home,omitempty"`
	ID               string                `json:"id"`
	Ref              string                `json:"account_ref"`
	Generation       uint64                `json:"generation"`
	Provider         string                `json:"provider"`
	Source           string                `json:"source"`
	Enabled          bool                  `json:"enabled"`
	Label            string                `json:"label"`
	IdentityState    string                `json:"identity_state"`
	Personal         bool                  `json:"personal"`
	RetentionPending bool                  `json:"retention_pending"`
	Profiles         []quotaAccountProfile `json:"profiles"`
	Error            *ErrorObject          `json:"error"`
}
type quotaAccountRecord struct {
	IdentityMissing  bool                  `json:"-"`
	Epoch            string                `json:"-"`
	IdentityKey      string                `json:"identity_key,omitempty"`
	PersonalHomes    []string              `json:"personal_homes,omitempty"`
	Automatic        bool                  `json:"automatic"`
	PriorEnabled     bool                  `json:"prior_enabled"`
	Aliases          []string              `json:"aliases,omitempty"`
	Definitions      map[string]string     `json:"definitions,omitempty"`
	ID               string                `json:"id"`
	Provider         string                `json:"provider"`
	Home             string                `json:"home"`
	Label            string                `json:"label"`
	Enabled          bool                  `json:"enabled"`
	Personal         bool                  `json:"personal"`
	Revision         uint64                `json:"revision"`
	Generation       uint64                `json:"generation"`
	RetentionPending bool                  `json:"retention_pending"`
	Ref              string                `json:"-"`
	Proof            string                `json:"-"`
	BillingID        string                `json:"-"`
	Profiles         []quotaAccountProfile `json:"-"`
	Definition       string                `json:"-"`
}
type quotaAccountStore struct {
	LegacyMigrated bool                 `json:"legacy_migrated"`
	Version        int                  `json:"version"`
	Revision       uint64               `json:"revision"`
	Accounts       []quotaAccountRecord `json:"accounts"`
}
type quotaAccountIssue struct {
	ProfileID string `json:"profile_id"`
	Code      string `json:"code"`
}
type quotaAccountsList struct {
	ObservedAt   string              `json:"observed_at"`
	Complete     bool                `json:"complete"`
	PollInterval int                 `json:"poll_interval_seconds"`
	Accounts     []quotaAccountView  `json:"accounts"`
	Errors       []quotaAccountIssue `json:"errors"`
}

func accountError(code string) *ErrorObject {
	message := "Account operation could not be verified. Refresh accounts and retry."
	switch code {
	case "account_verification_failed":
		message = "Account identity could not be verified. Sign in with the provider and retry."
	case "profile_conflict":
		message = "Collection profiles changed. Refresh accounts before retrying."
	case "exclusion_not_applicable":
		message = "This account has no linked collection profiles."
	case "exclusion_ambiguous":
		message = "The exclusion applies to more than one provider. Review profile exclusion settings before changing it."
	case "storage_error":
		message = "Account settings are unavailable. Review local settings and retry."
	case "invalid_home":
		message = "Choose an existing user-owned provider folder."
	}
	return &ErrorObject{Code: code, Message: message}
}
func validAccountLabel(s string) bool {
	if strings.TrimSpace(s) != s || s == "" || utf8.RuneCountInString(s) > 64 {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '/' || r == '\\' || r == '@' {
			return false
		}
	}
	return true
}
func (s *Service) accountHome(home string) (string, error) {
	if home == "" {
		return "", errors.New("missing home")
	}
	h, e := s.validateHome(home)
	if e != nil {
		return "", e
	}
	if s.registry != nil && pathsOverlap(h, s.registry.Root) {
		return "", errors.New("profile storage overlap")
	}
	if e = profile.CheckCodexHome(h); e != nil {
		return "", e
	}
	return h, nil
}
func (s *Service) loadAccountsLocked() *ErrorObject {
	if s.accounts != nil {
		actual, e := accountStoreRevisionAt(filepath.Join(s.appSupportRoot, accountStoreFile))
		if e != nil {
			return e
		}
		if actual == s.accountStoreRevision {
			return nil
		}
		for _, a := range s.accounts.Accounts {
			s.cancelAccountFlight(a.Ref)
		}
		s.accounts = nil
	}
	s.accountStoreRevision = ""
	store := &quotaAccountStore{Version: 1, Accounts: []quotaAccountRecord{}}
	path := filepath.Join(s.appSupportRoot, accountStoreFile)
	info, e := os.Lstat(path)
	if !os.IsNotExist(e) {
		if e != nil || info.Mode().Perm() != 0600 {
			return accountError("storage_error")
		}
		data, failure := readDaemonFile(path, maxAccountStoreBytes)
		s.accountStoreRevision = consentDigest(data)
		if failure != nil || !lifecycleJSONHasUniqueKeys(data) || decodePayload(data, store) != nil || store.Version != 1 || len(store.Accounts) > maxQuotaAccounts {
			return accountError("storage_error")
		}
	}
	ids := map[string]bool{}
	for i := range store.Accounts {
		a := &store.Accounts[i]
		if !accountOpaque.MatchString(a.ID) || ids[a.ID] || (a.Provider != "claude" && a.Provider != "codex") || !validAccountLabel(a.Label) || !filepath.IsAbs(a.Home) || a.Generation == 0 {
			return accountError("storage_error")
		}
		if len(a.Aliases) > maxQuotaAccounts || len(a.PersonalHomes) > maxQuotaAccounts {
			return accountError("storage_error")
		}
		for _, h := range append(append([]string{}, a.Aliases...), a.PersonalHomes...) {
			if !filepath.IsAbs(h) || filepath.Clean(h) != h {
				return accountError("storage_error")
			}
		}
		if a.IdentityKey != "" {
			if len(a.IdentityKey) != 64 {
				return accountError("storage_error")
			}
			for _, c := range a.IdentityKey {
				if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
					return accountError("storage_error")
				}
			}
		}
		ids[a.ID] = true
		r, e := newSelectionReference()
		if e != nil {
			return accountError("storage_error")
		}
		a.Ref = r
		a.Profiles = []quotaAccountProfile{}
	}
	s.accounts = store
	return nil
}
func (s *Service) withAccountStoreLock(action func() *ErrorObject) *ErrorObject {
	path := filepath.Join(s.appSupportRoot, accountStoreFile+".lock")
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return accountError("storage_error")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return accountError("storage_error")
	}
	lock := flock.New(path, flock.SetPermissions(0600))
	if err := lock.Lock(); err != nil {
		return accountError("storage_error")
	}
	defer lock.Unlock()
	return action()
}
func (s *Service) commitAccountsLocked(next *quotaAccountStore) *ErrorObject {
	return s.withAccountStoreLock(func() *ErrorObject { return s.commitAccountStoreCAS(next) })
}
func (s *Service) commitAccountStoreCAS(next *quotaAccountStore) *ErrorObject {
	b, e := json.Marshal(next)
	if e != nil || len(b) > maxAccountStoreBytes || len(next.Accounts) > maxQuotaAccounts {
		return accountError("storage_error")
	}
	path := filepath.Join(s.appSupportRoot, accountStoreFile)
	if info, e := os.Lstat(path); e == nil && (info.Mode().Perm() != 0600 || !info.Mode().IsRegular()) {
		return accountError("storage_error")
	} else if e != nil && !os.IsNotExist(e) {
		return accountError("storage_error")
	}
	actual, problem := accountStoreRevisionAt(path)
	if problem != nil {
		return problem
	}
	if actual != s.accountStoreRevision {
		s.accounts = nil
		return accountError("account_mismatch")
	}
	if atomicfile.Write(path, b, 0600) != nil {
		return accountError("storage_error")
	}
	s.accounts = next
	s.accountStoreRevision = consentDigest(b)
	return nil
}
func (s *Service) accountsCloneLocked() *quotaAccountStore {
	next := *s.accounts
	next.Accounts = append([]quotaAccountRecord{}, s.accounts.Accounts...)
	for i := range next.Accounts {
		a := &next.Accounts[i]
		a.Definitions = map[string]string{}
		for k, v := range s.accounts.Accounts[i].Definitions {
			a.Definitions[k] = v
		}
		a.Aliases = append([]string{}, a.Aliases...)
	}
	return &next
}
func (s *Service) accountsViewLocked() quotaAccountsList {
	result := quotaAccountsList{ObservedAt: s.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), Complete: true, PollInterval: quotaPollInterval, Accounts: []quotaAccountView{}, Errors: []quotaAccountIssue{}}
	for _, a := range s.accounts.Accounts {
		identity := "unverified"
		if a.Proof != "" {
			identity = "verified"
		}
		links := append([]quotaAccountProfile{}, a.Profiles...)
		if !s.multiHomeProfiles.Load() {
			for i := range links {
				links[i].ExclusionState = ""
			}
		}
		if links == nil {
			links = []quotaAccountProfile{}
		}
		result.Accounts = append(result.Accounts, quotaAccountView{ID: a.ID, Ref: a.Ref, Generation: a.Generation, Provider: a.Provider, Source: s.quotaSource(a.Provider), Enabled: a.Enabled, Label: a.Label, IdentityState: identity, Personal: a.Personal, RetentionPending: a.RetentionPending, Profiles: links})
		if a.IdentityMissing && a.Proof == "" {
			result.Accounts[len(result.Accounts)-1].Error = accountError("account_verification_failed")
		}
		if home, err := s.accountHome(a.Home); err != nil {
			result.Accounts[len(result.Accounts)-1].Error = accountError("account_unavailable")
		} else if s.quotaAccountHomes {
			result.Accounts[len(result.Accounts)-1].ConfigHome = home
			if s.multiHomeProfiles.Load() {
				homes := []string{}
				for _, raw := range append([]string{a.Home}, a.Aliases...) {
					if valid, e := s.accountHome(raw); e == nil && !containsHome(homes, valid) {
						homes = append(homes, valid)
					}
				}
				sort.Strings(homes)
				result.Accounts[len(result.Accounts)-1].ConfigHomes = homes
			}
		}
	}
	sort.Slice(result.Accounts, func(i, j int) bool {
		a, b := result.Accounts[i], result.Accounts[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if ar, br := quotaAccountDisplayRank(a), quotaAccountDisplayRank(b); ar != br {
			return ar < br
		}
		if a.Label != b.Label {
			return a.Label < b.Label
		}
		return a.ID < b.ID
	})
	return result
}

func quotaAccountDisplayRank(a quotaAccountView) int {
	allExcluded, isDefault := len(a.Profiles) > 0, false
	for _, p := range a.Profiles {
		isDefault = isDefault || p.ID == "default"
		allExcluded = allExcluded && p.Excluded != nil && *p.Excluded
	}
	if allExcluded {
		return 3
	}
	if isDefault {
		return 0
	}
	if len(a.Profiles) > 0 {
		return 1
	}
	return 2
}

func (s *Service) listQuotaAccounts(raw json.RawMessage) (any, *ErrorObject) {
	if !emptyPayload(raw) {
		return nil, payloadError()
	}
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	if e := s.loadAccountsLocked(); e != nil {
		return nil, e
	}
	issues, e := s.reconcileAccountsLocked()
	if e != nil {
		return nil, e
	}
	result := s.accountsViewLocked()
	result.Errors = issues
	result.Complete = len(issues) == 0
	return result, nil
}

func accountPayload(raw json.RawMessage, extra string) (map[string]json.RawMessage, string, uint64, *ErrorObject) {
	var fields map[string]json.RawMessage
	if decodePayload(raw, &fields) != nil || len(fields) != 2+boolInt(extra != "") {
		return nil, "", 0, payloadError()
	}
	ref, ok := requiredString(fields, "account_ref")
	var gen uint64
	if !ok || ref == "" || json.Unmarshal(fields["generation"], &gen) != nil || gen == 0 {
		return nil, "", 0, payloadError()
	}
	if extra != "" {
		if _, ok := fields[extra]; !ok {
			return nil, "", 0, payloadError()
		}
	}
	return fields, ref, gen, nil
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func (s *Service) accountIndexLocked(ref string, gen uint64) int {
	for i, a := range s.accounts.Accounts {
		if a.Ref == ref && a.Generation == gen {
			return i
		}
	}
	return -1
}
func rotateAccount(a *quotaAccountRecord) error {
	if a.Generation == math.MaxUint64 {
		return errors.New("generation exhausted")
	}
	ref, e := newSelectionReference()
	if e != nil {
		return e
	}
	a.Generation++
	a.Ref = ref
	return nil
}
func (s *Service) addQuotaAccount(raw json.RawMessage) (any, *ErrorObject) {
	var p struct {
		Provider string `json:"provider"`
		Label    string `json:"label"`
		Home     string `json:"home"`
	}
	if decodePayload(raw, &p) != nil || (p.Provider != "claude" && p.Provider != "codex") || !validAccountLabel(p.Label) {
		return nil, payloadError()
	}
	home, e := s.accountHome(p.Home)
	if e != nil {
		return nil, accountError("invalid_home")
	}
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	if err := s.loadAccountsLocked(); err != nil {
		return nil, err
	}
	next := s.accountsCloneLocked()
	found := false
	for i := range next.Accounts {
		a := &next.Accounts[i]
		if a.Provider == p.Provider && a.Home == home {
			a.Personal = true
			if !containsHome(a.PersonalHomes, home) {
				a.PersonalHomes = append(a.PersonalHomes, home)
			}
			a.Label = p.Label
			if rotateAccount(a) != nil {
				return nil, accountError("account_unavailable")
			}
			found = true
			break
		}
	}
	if !found {
		id, e := newSelectionReference()
		if e != nil {
			return nil, accountError("storage_error")
		}
		ref, e := newSelectionReference()
		if e != nil {
			return nil, accountError("storage_error")
		}
		next.Accounts = append(next.Accounts, quotaAccountRecord{ID: "account_" + id, Ref: ref, Generation: 1, Provider: p.Provider, Home: home, Label: p.Label, Personal: true, PersonalHomes: []string{home}, Profiles: []quotaAccountProfile{}})
	}
	if err := s.commitAccountsLocked(next); err != nil {
		return nil, err
	}
	return s.accountsViewLocked(), nil
}
func (s *Service) setQuotaAccount(raw json.RawMessage) (any, *ErrorObject) {
	fields, ref, gen, e := accountPayload(raw, "enabled")
	if e != nil {
		return nil, e
	}
	var enabled bool
	if json.Unmarshal(fields["enabled"], &enabled) != nil || strings.TrimSpace(string(fields["enabled"])) == "null" {
		return nil, payloadError()
	}
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	if e = s.loadAccountsLocked(); e != nil {
		return nil, e
	}
	i := s.accountIndexLocked(ref, gen)
	if i < 0 {
		return nil, accountError("account_mismatch")
	}
	next := s.accountsCloneLocked()
	if next.Revision == math.MaxUint64 {
		return nil, accountError("account_unavailable")
	}
	next.Revision++
	a := &next.Accounts[i]
	if a.RetentionPending {
		return nil, accountError("account_unavailable")
	}
	a.Enabled = enabled
	a.Revision = next.Revision
	if rotateAccount(a) != nil {
		return nil, accountError("account_unavailable")
	}
	if e = s.commitAccountsLocked(next); e != nil {
		return nil, e
	}
	s.cancelAccountFlight(ref)
	return s.accountsViewLocked(), nil
}
func (s *Service) removeQuotaAccount(raw json.RawMessage) (any, *ErrorObject) {
	_, ref, gen, e := accountPayload(raw, "")
	if e != nil {
		return nil, e
	}
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	if e = s.loadAccountsLocked(); e != nil {
		return nil, e
	}
	i := s.accountIndexLocked(ref, gen)
	if i < 0 {
		return nil, accountError("account_mismatch")
	}
	next := s.accountsCloneLocked()
	a := &next.Accounts[i]
	if !a.Personal {
		return nil, accountError("account_unavailable")
	}
	if len(a.Profiles) > 0 {
		a.Personal = false
		a.PersonalHomes = nil
		if !s.accountDefinitionsCurrent(*a) {
			return nil, accountError("account_mismatch")
		}
		homes := []string{}
		for _, link := range a.Profiles {
			entry, err := s.registry.Load(link.Name)
			if err != nil {
				return nil, accountError("profile_conflict")
			}
			home, configured := s.accountConfiguredHome(entry.Profile, a.Provider, entry.Name)
			if !configured {
				return nil, accountError("account_mismatch")
			}
			valid, err := s.accountHome(home)
			if err != nil {
				return nil, accountError("account_unavailable")
			}
			if !containsHome(homes, valid) {
				homes = append(homes, valid)
			}
		}
		sort.Strings(homes)
		if len(homes) == 0 {
			return nil, accountError("exclusion_not_applicable")
		}
		a.Home = homes[0]
		a.Aliases = append([]string{}, homes[1:]...)
		a.Epoch = ""
		if rotateAccount(a) != nil {
			return nil, accountError("account_unavailable")
		}
	} else {
		next.Accounts = append(next.Accounts[:i], next.Accounts[i+1:]...)
	}
	if e = s.commitAccountsLocked(next); e != nil {
		return nil, e
	}
	s.cancelAccountFlight(ref)
	return s.accountsViewLocked(), nil
}

func containsHome(homes []string, home string) bool {
	for _, h := range homes {
		if h == home {
			return true
		}
	}
	return false
}

func accountStoreRevisionAt(path string) (string, *ErrorObject) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxAccountStoreBytes {
		return "", accountError("storage_error")
	}
	data, failure := readDaemonFile(path, maxAccountStoreBytes)
	if failure != nil {
		return "", accountError("storage_error")
	}
	return consentDigest(data), nil
}
