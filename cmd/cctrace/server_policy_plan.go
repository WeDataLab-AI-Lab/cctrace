package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"cctrace/internal/codexconfig"
	"cctrace/internal/codexlog"
	"cctrace/internal/configtxn"
	"cctrace/internal/envgen"
	"cctrace/internal/profile"
	"cctrace/internal/serverpolicy"
	"cctrace/internal/store"
)

type serverPolicyRecord struct {
	Sequence uint64    `json:"sequence"`
	Status   string    `json:"status"`
	Reason   string    `json:"reason,omitempty"`
	At       time.Time `json:"at"`
}

type serverPolicyState struct {
	BootstrapEndpoint string               `json:"bootstrap_endpoint,omitempty"`
	Sequence          uint64               `json:"sequence"`
	Digest            string               `json:"digest,omitempty"`
	ManagedCA         string               `json:"managed_ca,omitempty"`
	ManagedCASHA256   string               `json:"managed_ca_sha256,omitempty"`
	UserCAFile        string               `json:"user_ca_file,omitempty"`
	Status            string               `json:"status"`
	Reason            string               `json:"reason,omitempty"`
	History           []serverPolicyRecord `json:"history,omitempty"`
	// SharedWrites are moves of Codex configs other profiles also write, left out
	// of the committed transaction to be carried out afterwards.
	SharedWrites []sharedPolicyWrite `json:"shared_writes,omitempty"`
}

// sharedPolicyWrite records one shared Codex config's move. The blocks carry
// the token, so only their digests are kept; the blocks are rebuilt from the
// current profile and these old values when the write is carried out.
type sharedPolicyWrite struct {
	Path        string `json:"path"`
	OldSHA256   string `json:"old_sha256"`
	NewSHA256   string `json:"new_sha256"`
	OldEndpoint string `json:"old_endpoint"`
	OldCAFile   string `json:"old_ca_file,omitempty"`
}

func policyPath(name, file string) (string, error) {
	if name != "" {
		if err := profile.ValidateName(name); err != nil {
			return "", err
		}
	}
	return canonicalPolicyPath(filepath.Join(syncProfileDir(name), file))
}

// Resolve existing parent aliases (notably macOS /var) but never follow a
// symlink at the file being replaced. configtxn independently rechecks paths.
func canonicalPolicyPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("policy target is a symlink: %s", abs)
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(abs)
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Join(resolved, filepath.Base(abs)), nil
		}
		if !os.IsNotExist(err) || parent == filepath.Dir(parent) {
			return "", err
		}
		suffix = append(suffix, filepath.Base(parent))
		parent = filepath.Dir(parent)
	}
}

// policyStatFn is how directories are compared; tests replace it to stand in for
// a disk that treats names differing in case as one directory.
var policyStatFn = os.Stat

// samePolicyEntry reports whether two canonical paths name one directory entry:
// the same name in the same directory. Strings miss a case-insensitive disk,
// where ~/.codex and ~/.CODEX are one directory, so the directories are compared.
// The files are not: a policy replaces a file by rename, which leaves a hard
// link's other name on the old content, so two names of one file are two files to
// write. Directories that do not exist are told apart by their names alone.
func samePolicyEntry(a, b string) bool {
	if a == b {
		return true
	}
	if filepath.Base(a) != filepath.Base(b) {
		return false
	}
	da, errA := policyStatFn(filepath.Dir(a))
	db, errB := policyStatFn(filepath.Dir(b))
	return errA == nil && errB == nil && os.SameFile(da, db)
}

// policyCodexConfigPaths lists the Codex configs a policy rewrites for p, one
// canonical path per directory entry, in scan order.
func policyCodexConfigPaths(p *profile.Profile) ([]string, error) {
	if p.Options.CodexSyncDeclined {
		return nil, nil
	}
	var paths []string
	for _, dir := range codexlog.ResolveScanDirs(p.Options.CodexDirs) {
		path, err := canonicalPolicyPath(filepath.Join(dir, "config.toml"))
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(paths, func(q string) bool { return samePolicyEntry(q, path) }) {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func readServerPolicyState(name string) (serverPolicyState, error) {
	s := serverPolicyState{Status: "unconfigured"}
	path, err := policyPath(name, "server-policy-state.json")
	if err != nil {
		return s, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

// policyCAPath is where a profile keeps the trust bundle a policy installs.
func policyCAPath(name string, pol serverpolicy.Policy) (string, error) {
	return policyPath(name, filepath.Join("server-policy-ca", fmt.Sprintf("%d-%s.pem", pol.Sequence, pol.CASHA256)))
}

func policyEndpoints(p *profile.Profile) serverpolicy.Endpoints {
	protocol := p.Server.Protocol
	if protocol == "" {
		protocol = "grpc"
	}
	return serverpolicy.Endpoints{Sync: profileHTTPAPIEndpoint(p), OTEL: p.Server.Endpoint, Protocol: protocol}
}

func serverPolicySequence(pol serverpolicy.Policy, digest string, state serverPolicyState, current serverpolicy.Endpoints) (bool, error) {
	if pol.Sequence < state.Sequence {
		return false, fmt.Errorf("policy sequence %d is older than applied sequence %d", pol.Sequence, state.Sequence)
	}
	if pol.Sequence == state.Sequence {
		if digest != state.Digest || current != pol.To {
			return false, fmt.Errorf("applied policy changed; require a higher policy sequence or explicit manual recovery")
		}
		return true, nil
	}
	return false, nil
}

func appendPolicyRecord(state *serverPolicyState, seq uint64, status, reason string) {
	state.Status, state.Reason = status, reason
	appendPolicyHistory(state, seq, status, reason)
}

func appendPolicyHistory(state *serverPolicyState, seq uint64, status, reason string) {
	state.History = append(state.History, serverPolicyRecord{Sequence: seq, Status: status, Reason: reason, At: time.Now().UTC()})
	if len(state.History) > 32 {
		state.History = state.History[len(state.History)-32:]
	}
}

func planServerPolicy(p *profile.Profile, name string, pol serverpolicy.Policy, digest string, state serverPolicyState) ([]configtxn.Change, serverPolicyState, error) {
	return planServerPolicyUnderLocks(p, name, pol, digest, state, nil)
}

// planServerPolicyUnderLocks is planServerPolicy for a caller that holds locks on
// the settings in locked. A settings file whose lock it could not take is not
// written; with locked nil the caller makes no claim, as a preview does not.
func planServerPolicyUnderLocks(p *profile.Profile, name string, pol serverpolicy.Policy, digest string, state serverPolicyState, locked map[string]bool) ([]configtxn.Change, serverPolicyState, error) {
	shared, err := ensureExclusivePolicySettings(p, name, pol)
	if err != nil {
		return nil, state, err
	}
	profilePath, err := policyPath(name, "profile.json")
	if err != nil {
		return nil, state, err
	}
	profileSource, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, state, err
	}
	if !p.SourceMatches(profileSource) {
		return nil, state, fmt.Errorf("profile changed while fetching policy; retry")
	}
	if state.ManagedCA != "" && p.Server.CACertFile == state.ManagedCA {
		data, err := os.ReadFile(state.ManagedCA)
		if err != nil {
			return nil, state, err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != state.ManagedCASHA256 {
			return nil, state, fmt.Errorf("managed CA was edited; policy deferred")
		}
	}
	statePath, err := policyPath(name, "server-policy-state.json")
	if err != nil {
		return nil, state, err
	}
	stateSource, err := os.ReadFile(statePath)
	stateExists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, state, err
	}
	if stateExists {
		var stored serverPolicyState
		if err = json.Unmarshal(stateSource, &stored); err != nil {
			return nil, state, err
		}
		if !reflect.DeepEqual(stored, state) {
			return nil, state, fmt.Errorf("policy state changed; retry")
		}
	}
	next := *p
	next.Server.SyncEndpoint, next.Server.Endpoint, next.Server.Protocol = pol.To.Sync, pol.To.OTEL, pol.To.Protocol
	caPath, err := policyCAPath(name, pol)
	if err != nil {
		return nil, state, err
	}
	caBytes := []byte(pol.CAPEM)
	// A file explicitly supplied by the user may also trust their company proxy.
	// Preserve those roots in our derived file and keep their original untouched.
	if p.Server.CACertFile != state.ManagedCA {
		state.UserCAFile = p.Server.CACertFile
	}
	if state.UserCAFile != "" {
		if _, err := serverTransport(state.UserCAFile); err != nil {
			return nil, state, err
		}
		userCA, err := os.ReadFile(state.UserCAFile)
		if err != nil {
			return nil, state, err
		}
		caBytes = append(append(userCA, '\n'), caBytes...)
	}
	next.Server.CACertFile = caPath
	next.UpdatedAt = time.Now().UTC()
	var changes []configtxn.Change
	add := func(path string, before []byte, exists bool, after []byte) error {
		path, err := canonicalPolicyPath(path)
		if err != nil {
			return err
		}
		changes = append(changes, configtxn.Change{Path: path, Before: before, BeforeExists: exists, After: after, Mode: 0600})
		return nil
	}
	caSource, err := os.ReadFile(caPath)
	caExists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, state, err
	}
	if err := add(caPath, caSource, caExists, caBytes); err != nil {
		return nil, state, err
	}
	profileBytes, err := policyProfileBytes(profileSource, &next)
	if err != nil {
		return nil, state, err
	}
	if err := add(profilePath, profileSource, true, profileBytes); err != nil {
		return nil, state, err
	}
	claudePath, err := envgen.ClaudeSettingsPath(p)
	if err != nil {
		return nil, state, err
	}
	claudeBytes, err := os.ReadFile(claudePath)
	claudeExists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, state, err
	}
	claudeAfter, err := envgen.PlanServerPolicySettings(claudeBytes, p, &next)
	if err != nil {
		return nil, state, err
	}
	if err := add(claudePath, claudeBytes, claudeExists, claudeAfter); err != nil {
		return nil, state, err
	}
	codexPaths, err := policyCodexConfigPaths(p)
	if err != nil {
		return nil, state, err
	}
	var notes []string
	for _, path := range codexPaths {
		content, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, state, err
		}
		// Profiles sharing it move one at a time, and a transaction that wrote it
		// would be undone by a sibling's session in between. The committed state
		// records the move instead, to be carried out after the commit.
		if i := slices.IndexFunc(shared, func(s sharedCodexPath) bool { return s.path == path }); i >= 0 {
			kept, err := recordSharedPolicyWrite(&state, p, name, pol, shared[i], content)
			if err != nil {
				return nil, state, err
			}
			if kept {
				notes = append(notes, defaultCodexKeptNote(path))
			}
			continue
		}
		updated, err := codexconfig.PlanServerPolicyConfig(content, p.Server.Endpoint, next.Server.Endpoint, p.Server.AuthToken, p.Server.CACertFile, caPath)
		if err != nil {
			return nil, state, fmt.Errorf("%s: %w", path, err)
		}
		if err := add(path, content, true, updated); err != nil {
			return nil, state, err
		}
	}
	// The profile's explicit home is its own, so its block moves in this
	// transaction: no sibling's session can undo it, and nothing is left for later.
	if home, ok := policyExplicitCodexHome(p, codexPaths); ok {
		before, after, verdict := planExplicitCodexHome(p, name, pol, state.UserCAFile, home, caPath, locked)
		switch verdict {
		case explicitHomeMoved:
			if err := add(home.config, before, true, after); err != nil {
				return nil, state, err
			}
		case explicitHomeKept:
			notes = append(notes, explicitCodexHomeKeptNote)
		}
	}
	if state.BootstrapEndpoint == "" {
		for _, old := range pol.From {
			if policyBootstrapAllowed(old.Sync, pol.To.Sync) {
				state.BootstrapEndpoint = old.Sync
				break
			}
		}
	}
	state.Sequence, state.Digest, state.ManagedCA = pol.Sequence, digest, caPath
	sum := sha256.Sum256(caBytes)
	state.ManagedCASHA256 = hex.EncodeToString(sum[:])
	// An applied record's reason is a note: what the policy left alone.
	appendPolicyRecord(&state, pol.Sequence, "applied", strings.Join(notes, "; "))
	stateBytes, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, state, err
	}
	if err := add(statePath, stateSource, stateExists, stateBytes); err != nil {
		return nil, state, err
	}
	return changes, state, nil
}

// Patch only policy-owned fields so newer/extra profile fields survive.
func policyProfileBytes(source []byte, next *profile.Profile) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(source, &doc); err != nil {
		return nil, err
	}
	var server map[string]json.RawMessage
	if err := json.Unmarshal(doc["server"], &server); err != nil {
		return nil, err
	}
	if server == nil {
		return nil, fmt.Errorf("profile server must be an object")
	}
	for key, value := range map[string]string{"endpoint": next.Server.Endpoint, "sync_endpoint": next.Server.SyncEndpoint, "protocol": next.Server.Protocol, "ca_cert_file": next.Server.CACertFile} {
		server[key], _ = json.Marshal(value)
	}
	doc["server"], _ = json.Marshal(server)
	doc["updated_at"], _ = json.Marshal(next.UpdatedAt)
	return json.MarshalIndent(doc, "", "  ")
}

// sharedSettingsConflict is one profile that writes a settings file this profile
// also writes: kind is "claude" or "codex".
type sharedSettingsConflict struct {
	profile string
	kind    string
}

// Shared homes cannot represent two independently targeted profiles. Defer the
// whole change so another profile's startup cannot silently undo half of it,
// unless every other writer of a shared Codex config ends up at the same server
// under this policy. Shared Claude settings always defer: relaxing them would
// race on all of settings.json and compare headers that carry the token.
//
// The default Codex folder is the exception: every profile rewrites its config
// from its own sessions, whatever a policy does, so no profile can hold it still
// for the others. A sibling that shares only that file, moving or not, does not
// hold the policy back; the config is left to its writers (see keepsOther).
//
// It returns the shared Codex configs, which the caller writes after its commit.
//
// Every shared profile is collected before deferring, so the owner sees them all
// at once instead of one per run. The default profile comes first and ListNamed
// returns names sorted, so the conflicts already read in name order, Claude
// before Codex for the same profile.
func ensureExclusivePolicySettings(p *profile.Profile, name string, pol serverpolicy.Policy) ([]sharedCodexPath, error) {
	siblings, err := sharedPolicySettings(p, name)
	var conflicts []sharedSettingsConflict
	var moving, staying []policySibling
	for _, s := range siblings {
		if s.claude {
			conflicts = append(conflicts, sharedSettingsConflict{s.name, "claude"})
		}
		if len(s.codex) == 0 {
			continue
		}
		switch {
		case codexSiblingMoves(pol, s):
			moving = append(moving, s)
		case slices.ContainsFunc(s.codex, func(path string) bool { return !isDefaultCodexConfig(path) }):
			conflicts = append(conflicts, sharedSettingsConflict{s.name, "codex"})
		default:
			staying = append(staying, s)
		}
	}
	if len(conflicts) > 0 {
		return nil, errors.New(sharedSettingsReason(conflicts))
	}
	if err != nil {
		return nil, err
	}
	shared := sharedCodexPaths(moving, staying)
	for _, s := range shared {
		if err := checkSharedCodexBlock(p, name, pol, s); err != nil {
			return nil, err
		}
	}
	return shared, nil
}

// codexSiblingMoves reports whether a profile sharing a Codex config ends up at
// this policy's server. One already there writes the new server's block itself.
// Any other must be targeted at endpoints the policy names and have a live sync
// daemon able to take the policy over (the same conditions its own apply checks):
// without one, its next session rewrites the shared block to its old server.
// What cannot be read counts as not moving, so it defers rather than fails.
func codexSiblingMoves(pol serverpolicy.Policy, s policySibling) bool {
	at := policyEndpoints(s.profile)
	if at == pol.To {
		return true
	}
	target, err := serverPolicyTarget(s.name)
	if err != nil || !pol.Covers(target) || !slices.Contains(pol.From, at) {
		return false
	}
	rt, ok, err := policyReadRuntimeFn(s.name)
	if err != nil || !ok || rt.Interval <= 0 || rt.Local || rt.EndpointOverride != "" {
		return false
	}
	if same, err := runtimeClaudeDirMatches(s.profile, rt); err != nil || !same {
		return false
	}
	// A crashed daemon leaves its record behind; only a held lock is live.
	free, err := policySyncFreeFn(s.name)
	return err == nil && !free
}

// sharedCodexPath is a Codex config this profile shares with siblings: only
// moving ones, unless it is the default Codex folder's.
type sharedCodexPath struct {
	path string
	// siblings are the moving profiles sharing it, whose blocks it may hold.
	siblings []policySibling
	// keepsOther says a block that is none of theirs is left as it is, not
	// deferred on: the config is the default Codex folder's.
	keepsOther bool
}

// sharedCodexPaths groups the Codex configs this profile shares with the moving
// siblings, and adds the default folder's config where only staying ones share it.
func sharedCodexPaths(moving, staying []policySibling) []sharedCodexPath {
	var shared []sharedCodexPath
	at := func(path string) int {
		i := slices.IndexFunc(shared, func(c sharedCodexPath) bool { return c.path == path })
		if i < 0 {
			i = len(shared)
			shared = append(shared, sharedCodexPath{path: path, keepsOther: isDefaultCodexConfig(path)})
		}
		return i
	}
	for _, s := range moving {
		for _, path := range s.codex {
			i := at(path)
			shared[i].siblings = append(shared[i].siblings, s)
		}
	}
	for _, s := range staying {
		for _, path := range s.codex {
			at(path)
		}
	}
	return shared
}

// isDefaultCodexConfig reports whether path is the config of the default Codex
// folder, the one every profile scans and none can claim for itself.
func isDefaultCodexConfig(path string) bool {
	def, err := canonicalPolicyPath(filepath.Join(codexlog.ExpandHome(codexlog.DefaultCodexDir()), "config.toml"))
	return err == nil && samePolicyEntry(path, def)
}

// defaultCodexKeptNote is what the history, the status and the report say when a
// policy moves a profile and leaves the default Codex config as it found it.
func defaultCodexKeptNote(path string) string {
	return redactPolicyReason("default Codex config kept: " + path)
}

// joinPolicyNotes adds a note to the ones an applied record already carries.
func joinPolicyNotes(notes, add string) string {
	switch {
	case add == "" || strings.Contains(notes, add):
		return notes
	case notes == "":
		return add
	}
	return notes + "; " + add
}

// sharedCodexPolicyBlocks are the blocks a policy moves this profile between in
// a shared Codex config, and the blocks its siblings there may write, before or
// after they move.
func sharedCodexPolicyBlocks(p *profile.Profile, name string, pol serverpolicy.Policy, siblings []policySibling) (old, next codexconfig.PolicyBlock, others []codexconfig.PolicyBlock, err error) {
	caPath, err := policyCAPath(name, pol)
	if err != nil {
		return old, next, nil, err
	}
	old = codexconfig.PolicyBlock{Endpoint: p.Server.Endpoint, Token: p.Server.AuthToken, CA: p.Server.CACertFile}
	next = codexconfig.PolicyBlock{Endpoint: pol.To.OTEL, Token: p.Server.AuthToken, CA: caPath}
	for _, s := range siblings {
		caPath, err := policyCAPath(s.name, pol)
		if err != nil {
			return old, next, nil, err
		}
		token := s.profile.Server.AuthToken
		others = append(others, codexconfig.PolicyBlock{Endpoint: s.profile.Server.Endpoint, Token: token, CA: s.profile.Server.CACertFile}, codexconfig.PolicyBlock{Endpoint: pol.To.OTEL, Token: token, CA: caPath})
	}
	return old, next, others, nil
}

// errSharedCodexEdited defers a move through a shared Codex config whose block no
// moving profile writes: someone edited it, and it is not the policy's to
// overwrite. Planning reports it, and so does every run while a recorded write
// waits on it. The default Codex folder's config never does: see keepsOther.
var errSharedCodexEdited = errors.New("shared Codex settings edited; policy deferred")

// recordSharedPolicyWrite stages a shared Codex config's move in the state the
// policy commits, replacing an earlier record for the same file. A config whose
// block none of the profiles writes is kept as it is: nothing is staged, and it
// says so with kept.
func recordSharedPolicyWrite(state *serverPolicyState, p *profile.Profile, name string, pol serverpolicy.Policy, shared sharedCodexPath, content []byte) (kept bool, err error) {
	plan, old, err := planSharedCodexMove(p, name, pol, shared, content)
	if err != nil {
		return false, err
	}
	var writes []sharedPolicyWrite
	for _, w := range state.SharedWrites {
		if w.Path != shared.path {
			writes = append(writes, w)
		}
	}
	state.SharedWrites = writes
	if plan.Holds == codexconfig.SharedBlockOther {
		return true, nil
	}
	state.SharedWrites = append(writes, sharedPolicyWrite{Path: shared.path, OldSHA256: plan.OldSHA256, NewSHA256: plan.NewSHA256, OldEndpoint: old.Endpoint, OldCAFile: old.CA})
	return false, nil
}

// checkSharedCodexBlock defers when the shared config holds a block that no
// profile moving under this policy writes: someone edited it, and it is not
// this policy's to overwrite.
func checkSharedCodexBlock(p *profile.Profile, name string, pol serverpolicy.Policy, shared sharedCodexPath) error {
	content, err := os.ReadFile(shared.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	_, _, err = planSharedCodexMove(p, name, pol, shared, content)
	return err
}

// planSharedCodexMove works out this profile's move in a shared Codex config
// holding content, and defers when the config holds a block no profile moving
// under this policy writes, unless the config keeps such a block: the plan then
// holds SharedBlockOther and is not carried out.
func planSharedCodexMove(p *profile.Profile, name string, pol serverpolicy.Policy, shared sharedCodexPath, content []byte) (codexconfig.SharedBlockPlan, codexconfig.PolicyBlock, error) {
	old, next, others, err := sharedCodexPolicyBlocks(p, name, pol, shared.siblings)
	if err != nil {
		return codexconfig.SharedBlockPlan{}, old, err
	}
	plan, err := codexconfig.PlanSharedServerPolicyConfig(content, old, next, others)
	if err != nil {
		// cctrace writes single-line blocks only, so a multi-line root key is no
		// profile's block: other content, which the default folder keeps.
		if shared.keepsOther && errors.Is(err, codexconfig.ErrMultilineRootOtelKey) {
			plan.Holds = codexconfig.SharedBlockOther
			return plan, old, nil
		}
		return plan, old, fmt.Errorf("%s: %w", shared.path, err)
	}
	if plan.Holds == codexconfig.SharedBlockOther && !shared.keepsOther {
		return plan, old, errSharedCodexEdited
	}
	return plan, old, nil
}

// policySibling is another profile that writes settings this profile's policy
// would rewrite: its Claude settings, the Codex configs in codex, or both.
type policySibling struct {
	name    string
	profile *profile.Profile
	claude  bool
	codex   []string
}

// sharedPolicySettings lists the profiles sharing settings with p, the default
// first and then by name, along with the first error met reading them. That
// error counts only when nothing is shared: a profile that cannot be read is
// passed over, so it must not hide the conflicts found among the others, which
// wait on a person, as a failure.
func sharedPolicySettings(p *profile.Profile, name string) ([]policySibling, error) {
	claude, err := envgen.ClaudeSettingsPath(p)
	if err != nil {
		return nil, err
	}
	claude, err = canonicalPolicyPath(claude)
	if err != nil {
		return nil, err
	}
	dirs, err := policyCodexConfigPaths(p)
	if err != nil {
		return nil, err
	}
	names, err := profile.ListNamed()
	if err != nil {
		return nil, err
	}
	var siblings []policySibling
	var firstErr error
	for _, otherName := range append([]string{""}, names...) {
		if otherName == name {
			continue
		}
		s, err := sharedSettingsWith(otherName, claude, dirs)
		if s.claude || len(s.codex) > 0 {
			siblings = append(siblings, s)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return siblings, firstErr
}

// sharedSettingsWith says what profile otherName shares with the profile being
// checked, whose Claude settings are claude and whose Codex configs are dirs. On
// an error it returns what it found before it.
func sharedSettingsWith(otherName, claude string, dirs []string) (policySibling, error) {
	s := policySibling{name: otherName}
	var err error
	if otherName == "" {
		s.profile, err = profile.Load()
	} else {
		s.profile, err = profile.LoadNamed(otherName)
	}
	// A name with no profile behind it, such as an empty directory among the named
	// profiles, shares nothing. LoadNamed wraps its not-found error, which
	// os.IsNotExist does not see through.
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	otherClaude, err := envgen.ClaudeSettingsPath(s.profile)
	if err != nil {
		return s, err
	}
	otherClaude, err = canonicalPolicyPath(otherClaude)
	if err != nil {
		return s, err
	}
	s.claude = samePolicyEntry(otherClaude, claude)
	// A profile that syncs no Codex shares no Codex file, whatever the other has.
	if len(dirs) > 0 && !s.profile.Options.CodexSyncDeclined {
		for _, dir := range codexlog.ResolveScanDirs(s.profile.Options.CodexDirs) {
			path, err := canonicalPolicyPath(filepath.Join(dir, "config.toml"))
			if err != nil {
				return s, err
			}
			// Named as this profile names it, once even when two of its dirs
			// are one file.
			if i := slices.IndexFunc(dirs, func(d string) bool { return samePolicyEntry(d, path) }); i >= 0 && !slices.Contains(s.codex, dirs[i]) {
				s.codex = append(s.codex, dirs[i])
			}
		}
	}
	return s, nil
}

// sharedSettingsReason names the conflicts within what the report keeps. Both the
// server and the client cut a longer reason from the end. The result is classified
// before the cut, but the operator who reads the reason would lose its suffix and
// its last entries, so the entries that do not fit are counted instead.
func sharedSettingsReason(conflicts []sharedSettingsConflict) string {
	items := make([]string, len(conflicts))
	for i, c := range conflicts {
		items[i] = fmt.Sprintf("%q (%s)", c.profile, c.kind)
	}
	reason := fmt.Sprintf("shared settings with profiles %s; policy deferred", strings.Join(items, ", "))
	for shown := len(items) - 1; shown > 0 && utf8.RuneCountInString(reason) > store.ClientPolicyReasonMax; shown-- {
		reason = fmt.Sprintf("shared settings with profiles %s and %d more; policy deferred", strings.Join(items[:shown], ", "), len(items)-shown)
	}
	return reason
}
