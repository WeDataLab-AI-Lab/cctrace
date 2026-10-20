package desktop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"cctrace/internal/profile"
)

func accountHomeMatches(a quotaAccountRecord, home string) bool {
	if a.Home == home {
		return true
	}
	for _, h := range a.Aliases {
		if h == home {
			return true
		}
	}
	return false
}
func (s *Service) reconcileAccountsLocked() (issues []quotaAccountIssue, problem *ErrorObject) {
	issues = []quotaAccountIssue{}
	// A failed census cannot erase a previously established profile link or turn
	// its missing observations into an all-profiles exclusion success.
	defer func() {
		if problem != nil || len(issues) == 0 || s.accounts == nil {
			return
		}
		failed := map[string]bool{}
		for _, issue := range issues {
			failed[issue.ProfileID] = true
		}
		for i := range s.accounts.Accounts {
			account := &s.accounts.Accounts[i]
			present := map[string]bool{}
			for _, link := range account.Profiles {
				present[link.ID] = true
			}
			for id := range account.Definitions {
				if present[id] || !failed[id] && !failed["unknown"] {
					continue
				}
				name := id
				if id != "default" {
					if !strings.HasPrefix(id, "named:") {
						continue
					}
					name = strings.TrimPrefix(id, "named:")
					if profile.ValidateName(name) != nil {
						continue
					}
				}
				account.Profiles = append(account.Profiles, quotaAccountProfile{ID: id, Name: name})
			}
			sort.Slice(account.Profiles, func(i, j int) bool { return account.Profiles[i].ID < account.Profiles[j].ID })
		}
	}()
	if err := s.migrateLegacyAccountsLocked(); err != nil {
		return nil, err
	}
	if s.registry == nil {
		return issues, nil
	}
	if s.registry.HomeTransactionPending() {
		return nil, accountError("storage_error")
	}
	if !daemonDirectorySafe(s.registry.Root) || !accountDefinitionDirectorySafe(s.registry.Root) {
		return []quotaAccountIssue{{ProfileID: "unknown", Code: "unsafe_storage"}}, nil
	}
	names := []string{"default"}
	profilesDir := filepath.Join(s.registry.Root, "profiles")
	if info, e := os.Lstat(profilesDir); e == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return []quotaAccountIssue{{ProfileID: "unknown", Code: "unsafe_storage"}}, nil
		}
		f, e := os.Open(profilesDir)
		if e != nil {
			return nil, accountError("storage_error")
		}
		entries, e := f.ReadDir(maxDaemonProfiles + 1)
		f.Close()
		if e != nil && e != io.EOF && len(entries) == 0 {
			return nil, accountError("storage_error")
		}
		if len(entries) > maxDaemonProfiles {
			return []quotaAccountIssue{{ProfileID: "unknown", Code: "input_limit_exceeded"}}, nil
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || profile.ValidateName(entry.Name()) != nil {
				issues = append(issues, quotaAccountIssue{ProfileID: "unknown", Code: "unsafe_storage"})
				continue
			}
			names = append(names, entry.Name())
		}
	} else if !os.IsNotExist(e) {
		return nil, accountError("storage_error")
	}
	sort.Strings(names)
	if len(names) > maxDaemonProfiles {
		issues = append(issues, quotaAccountIssue{ProfileID: "unknown", Code: "input_limit_exceeded"})
		names = names[:maxDaemonProfiles-1]
	}
	next := s.accountsCloneLocked()
	oldProfiles := map[string]bool{}
	for i := range next.Accounts {
		a := &next.Accounts[i]
		oldProfiles[a.ID] = len(a.Profiles) > 0 || a.Automatic
		a.Profiles = []quotaAccountProfile{}
		a.IdentityMissing = false
	}
	current := map[string]*bool{}
	if s.daemonRoot != "" {
		status, managed := s.readSupervisorStatus()
		if managed && status.Complete && status.Daemon != nil && (status.Daemon.State == "running" || status.Daemon.State == "stopped") {
			for _, p := range status.Profiles {
				if p.Job != nil {
					v := status.Daemon.State == "running" && p.SyncEnabled != nil && *p.SyncEnabled && (p.Job.State == "healthy" || p.Job.State == "collecting" || p.Job.State == "blocked" || p.Job.State == "retrying" || p.Job.State == "error")
					current[p.ID] = &v
				}
			}
		}
	}
	budget := daemonReadBudget{remaining: maxDaemonTotalInput, complete: true}
	for _, name := range names {
		path, _ := s.registry.Path(name)
		id := profile.ProfileID(name)
		if !daemonDirectorySafe(filepath.Dir(path)) || !accountDefinitionDirectorySafe(filepath.Dir(path)) {
			if name == "default" {
				continue
			}
			issues = append(issues, quotaAccountIssue{ProfileID: id, Code: "unsafe_storage"})
			continue
		}
		info, e := os.Lstat(path)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil || info.Mode().Perm() != 0600 {
			issues = append(issues, quotaAccountIssue{ProfileID: id, Code: "unsafe_storage"})
			continue
		}
		data, code := s.readInput(path, &budget)
		var p profile.Profile
		if code != "" || !lifecycleJSONHasUniqueKeys(data) || json.Unmarshal(data, &p) != nil || p.ValidateHomes() != nil {
			issues = append(issues, quotaAccountIssue{ProfileID: id, Code: "invalid_profile"})
			continue
		}
		if p.ID != "" && p.ID != id {
			issues = append(issues, quotaAccountIssue{ProfileID: id, Code: "invalid_profile"})
			continue
		}
		complete := p.Options.LocalAnalysisEnabled || ((p.User.ID != "" || p.User.Email != "") && (p.Server.Endpoint != "" || p.Server.SyncEndpoint != ""))
		if p.HomesVersion == 0 && (p.RegistrationOnly || !complete) {
			continue
		}
		sum := sha256.Sum256(data)
		definition := hex.EncodeToString(sum[:])
		for _, provider := range []string{"claude", "codex"} {
			home, configured := s.accountConfiguredHome(&p, provider, name)
			memberships := p.ProviderHomes(provider, home)
			if p.HomesVersion == 0 && !configured {
				continue
			}
			for _, membership := range memberships {
				home = membership.Home
				valid, e := s.accountHome(home)
				if e != nil {
					issues = append(issues, quotaAccountIssue{ProfileID: id, Code: "selection_unavailable"})
					continue
				}
				index := -1
				for i, a := range next.Accounts {
					if a.Provider == provider && accountHomeMatches(a, valid) {
						index = i
						break
					}
				}
				if index < 0 {
					if len(next.Accounts) >= maxQuotaAccounts {
						issues = append(issues, quotaAccountIssue{ProfileID: id, Code: "input_limit_exceeded"})
						continue
					}
					ref, e := newSelectionReference()
					if e != nil {
						return nil, accountError("storage_error")
					}
					newid, e := newSelectionReference()
					if e != nil {
						return nil, accountError("storage_error")
					}
					next.Accounts = append(next.Accounts, quotaAccountRecord{ID: "account_" + newid, Provider: provider, Home: valid, Label: name, Enabled: membership.QuotaEnabled, Ref: ref, Generation: 1, Profiles: []quotaAccountProfile{}})
					index = len(next.Accounts) - 1
				}
				a := &next.Accounts[index]
				a.Automatic = true
				excluded := s.localAccountExcluded(provider, valid, p.Options.ExcludeAccounts)
				if p.HomesVersion == 1 && excluded == nil {
					a.IdentityMissing = true
				}
				if p.HomesVersion == 1 {
					v := membership.Transmission != "allow"
					if excluded != nil {
						v = v || *excluded
					}
					excluded = &v
				}
				collecting := current[id]
				if p.HomesVersion != 0 && (!p.CollectionEnabled(provider) || membership.Transmission != "allow") {
					stopped := false
					collecting = &stopped
				}
				a.Profiles = mergeQuotaProfileLinks(append(a.Profiles, quotaAccountProfile{ID: id, Name: name, Collecting: collecting, Excluded: excluded}))
				if a.Definitions == nil {
					a.Definitions = map[string]string{}
				}
				if old, ok := a.Definitions[id]; ok && old != definition {
					if rotateAccount(a) != nil {
						return nil, accountError("account_unavailable")
					}
					a.Proof = ""
					a.BillingID = ""
				}
				a.Definitions[id] = definition
			}
		}
	}
	if len(issues) == 0 {
		for i := range next.Accounts {
			a := &next.Accounts[i]
			currentDefs := map[string]string{}
			for _, link := range a.Profiles {
				currentDefs[link.ID] = a.Definitions[link.ID]
			}
			oldCount := len(a.Definitions)
			membershipChanged := oldCount != len(currentDefs)
			if !membershipChanged {
				for k := range a.Definitions {
					if _, ok := currentDefs[k]; !ok {
						membershipChanged = true
					}
				}
			}
			if membershipChanged && oldCount > 0 && len(currentDefs) > 0 {
				if rotateAccount(a) != nil {
					return nil, accountError("account_unavailable")
				}
			}
			a.Definitions = currentDefs
		}
	}
	// Incomplete discovery is absence of evidence, never a removal decision.
	if len(issues) == 0 {
		for i := range next.Accounts {
			a := &next.Accounts[i]
			if oldProfiles[a.ID] && !a.Personal && len(a.Profiles) == 0 && !a.RetentionPending {
				a.RetentionPending = true
				a.PriorEnabled = a.Enabled
				a.Enabled = false
				if rotateAccount(a) != nil {
					return nil, accountError("account_unavailable")
				}
				s.cancelAccountFlight(s.accounts.Accounts[i].Ref)
			}
		}
	}
	before, _ := json.Marshal(s.accounts)
	after, _ := json.Marshal(next)
	if string(before) != string(after) {
		if e := s.commitAccountsLocked(next); e != nil {
			return nil, e
		}
	} else {
		s.accounts = next
	}
	if len(issues) > maxQuotaAccounts {
		issues = append(issues[:maxQuotaAccounts-1], quotaAccountIssue{ProfileID: "unknown", Code: "input_limit_exceeded"})
	}
	return issues, nil
}
func (s *Service) localAccountBillingID(provider, home string) string {
	id := ""
	if provider == "codex" {
		id, _, _ = boundedCodexAccount(home)
	} else {
		path := filepath.Join(home, ".claude.json")
		base, _ := os.UserHomeDir()
		def := filepath.Clean(filepath.Join(base, ".claude"))
		same := filepath.Clean(home) == def
		if runtime.GOOS == "windows" {
			same = strings.EqualFold(filepath.Clean(home), def)
		}
		if same {
			path = filepath.Join(base, ".claude.json")
		}
		b, f := readDaemonFile(path, maxCodexAuthBytes)
		if f == nil && lifecycleJSONHasUniqueKeys(b) {
			var p struct {
				OAuth struct {
					AccountID string `json:"accountUuid"`
				} `json:"oauthAccount"`
			}
			if json.Unmarshal(b, &p) == nil {
				id = p.OAuth.AccountID
			}
		}
	}
	if !safeBillingID(id) {
		return ""
	}
	return id
}
func (s *Service) localAccountExcluded(provider, home string, entries []string) *bool {
	id := s.localAccountBillingID(provider, home)
	if id == "" {
		return nil
	}
	v := localExcludes(entries, provider, id)
	return &v
}
func localExcludes(entries []string, provider, id string) bool {
	ns := "anthropic"
	if provider == "codex" {
		ns = "openai"
	}
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == id {
			return true
		}
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) == 2 && strings.ToLower(strings.TrimSpace(parts[0])) == ns && strings.TrimSpace(parts[1]) == id {
			return true
		}
	}
	return false
}
func (s *Service) retainQuotaAccount(raw json.RawMessage) (any, *ErrorObject) {
	fields, ref, gen, e := accountPayload(raw, "keep")
	if e != nil {
		return nil, e
	}
	var keep bool
	if json.Unmarshal(fields["keep"], &keep) != nil || string(fields["keep"]) == "null" {
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
	a := &next.Accounts[i]
	if !a.RetentionPending {
		return nil, accountError("account_unavailable")
	}
	if keep {
		a.Personal = true
		a.PersonalHomes = append([]string{a.Home}, a.Aliases...)
		a.RetentionPending = false
		a.Enabled = a.PriorEnabled
		if rotateAccount(a) != nil {
			return nil, accountError("account_unavailable")
		}
	} else {
		next.Accounts = append(next.Accounts[:i], next.Accounts[i+1:]...)
	}
	if e = s.commitAccountsLocked(next); e != nil {
		return nil, e
	}
	return s.accountsViewLocked(), nil
}

func (s *Service) migrateLegacyAccountsLocked() *ErrorObject {
	if s.accounts.LegacyMigrated {
		return nil
	}
	next := s.accountsCloneLocked()
	next.LegacyMigrated = true
	info, err := os.Lstat(filepath.Join(s.appSupportRoot, quotaSelectionStoreName))
	if os.IsNotExist(err) {
		return s.commitAccountsLocked(next)
	}
	if err != nil || info.Mode().Perm() != 0600 {
		return accountError("storage_error")
	}
	preferences, err := s.readQuotaSelectionPreferences()
	if err != nil {
		return accountError("storage_error")
	}
	for _, provider := range []string{"claude", "codex"} {
		pref := preferences[provider]
		home := s.claudeHome
		if provider == "codex" {
			home = s.codexHome
		}
		if s.registry != nil {
			entry, e := s.registry.Load(pref.Profile)
			if e != nil {
				continue
			}
			home = entry.Profile.ClaudeConfigDir
			if provider == "codex" {
				home = entry.Profile.CodexHome
			}
			if home == "" && pref.Profile == "default" {
				home = s.claudeHome
				if provider == "codex" {
					home = s.codexHome
				}
			}
		}
		valid, e := s.accountHome(home)
		if e != nil {
			if !filepath.IsAbs(home) {
				continue
			}
			valid = filepath.Clean(home)
		}
		if len(next.Accounts) >= maxQuotaAccounts {
			return accountError("storage_error")
		}
		id, e := newSelectionReference()
		if e != nil {
			return accountError("storage_error")
		}
		ref, e := newSelectionReference()
		if e != nil {
			return accountError("storage_error")
		}
		next.Revision++
		next.Accounts = append(next.Accounts, quotaAccountRecord{ID: "account_" + id, Ref: ref, Generation: 1, Provider: provider, Home: valid, Label: pref.Profile, Enabled: pref.Enabled, Personal: true, PersonalHomes: []string{valid}, Revision: next.Revision, Profiles: []quotaAccountProfile{}})
	}
	return s.commitAccountsLocked(next)
}
func (s *Service) accountDefinitionsCurrent(a quotaAccountRecord) bool {
	if a.Automatic && !a.Personal && len(a.Profiles) == 0 {
		return false
	}
	if s.registry == nil {
		return len(a.Profiles) == 0
	}
	for _, link := range a.Profiles {
		path, e := s.registry.Path(link.Name)
		if e != nil {
			return false
		}
		b, f := readDaemonFile(path, maxDaemonInputFile)
		if f != nil {
			return false
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != a.Definitions[link.ID] {
			return false
		}
	}
	return true
}

func accountDefinitionDirectorySafe(path string) bool {
	canonical, e := filepath.EvalSymlinks(path)
	return e == nil && profile.CheckCodexHome(canonical) == nil
}

func (s *Service) accountConfiguredHome(p *profile.Profile, provider, name string) (string, bool) {
	if p.HomesVersion != 0 || p.Homes != nil {
		homes := p.ProviderHomes(provider, "")
		if len(homes) > 0 {
			return homes[0].Home, true
		}
		return "", false
	}
	home, configured := p.ClaudeConfigDir, p.Options.SyncEnabled
	if provider == "codex" {
		home, configured = p.CodexHome, p.Options.CodexSyncEnabled
	}
	if name == "default" && home == "" {
		home = s.claudeHome
		if provider == "codex" {
			home = s.codexHome
		}
	}
	return home, configured
}
