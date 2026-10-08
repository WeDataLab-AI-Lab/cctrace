package syncer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// FileState tracks the sync position for a single JSONL file.
type FileState struct {
	Offset                   int64     `json:"offset"`
	LastSynced               time.Time `json:"last_synced"`
	CWD                      string    `json:"cwd,omitempty"`
	Model                    string    `json:"model,omitempty"`
	LastAttributionSkill     string    `json:"last_attribution_skill,omitempty"`
	TokenUsageScanned        bool      `json:"token_usage_scanned,omitempty"`
	HasTotalTokenUsage       bool      `json:"has_total_token_usage,omitempty"`
	TotalInputTokens         int       `json:"total_input_tokens,omitempty"`
	TotalCachedInputTokens   int       `json:"total_cached_input_tokens,omitempty"`
	TotalOutputTokens        int       `json:"total_output_tokens,omitempty"`
	CodexForkMetadataScanned bool      `json:"codex_fork_metadata_scanned,omitempty"`
	CodexSubagentFork        bool      `json:"codex_subagent_fork,omitempty"`
	CodexForkHistoryCopied   bool      `json:"codex_fork_history_copied,omitempty"`
	CodexForkBoundaryReached bool      `json:"codex_fork_boundary_reached,omitempty"`
	CodexForkHasTriggerTurn  bool      `json:"codex_fork_has_trigger_turn,omitempty"`
	// CodexHasTokenUsageRecord remembers that this file carries Codex's own
	// token_usage_record rows. The scanner learns it by reading one, and an
	// incremental pass only reads the new tail -- so without keeping it here the
	// answer changes with whichever slice of the file a pass happened to see.
	CodexHasTokenUsageRecord bool `json:"codex_has_token_usage_record,omitempty"`
	// CodexOriginator is what launched the session, from the session_meta that
	// opens the rollout. Every record's entrypoint is derived from it, and a
	// pass that resumes past that line never reads it again.
	CodexOriginator string `json:"codex_originator,omitempty"`
	// CodexOriginatorScanned records that the head of the rollout has been read
	// for its originator. Until then CodexOriginator is only what the scans of
	// this file covered: nothing for an entry saved before the field existed,
	// a later session_meta's when that is all a tail held. It is set whatever
	// the head said, so each file is read for it once and not on every pass.
	CodexOriginatorScanned bool `json:"codex_originator_scanned,omitempty"`
	// GjcSessionID persists the session id a gjc subagent transcript's header
	// carried, across incremental scans. Subagent transcript filenames are an
	// arbitrary subagentId, not a UUID, so the id only exists in the header line
	// (type "session"). Once an incremental scan has moved the offset past that
	// header, the next pass has no filename-derived id to fall back on, so this
	// field is the only way to keep tagging the subagent's records with the
	// right session.
	GjcSessionID string `json:"gjc_session_id,omitempty"`
	// SkippedAtFirstSync records how many bytes of this file were already on disk
	// when the first sync ran and were therefore never collected. It is written
	// once, at the skip, and never cleared: the content stays missing for the life
	// of the session, so the record of it has to outlive the sync pass that made
	// it. sync.log carries the same fact but rotates at 10MB, and this line is
	// written exactly once per file — on an active machine the only evidence would
	// age out. Zero means nothing was skipped.
	SkippedAtFirstSync int64 `json:"skipped_at_first_sync,omitempty"`
	// BlockedByBodyLimit records that this file stopped at a record the server
	// refused as too large, and how many records were in the group when the split
	// ran out of halves. Nothing is lost while it is set: the bytes are still on
	// disk and the offset is still in front of them, so raising the server limit
	// and syncing again collects everything. That only stays true if the fact
	// reaches a person, and the sync log rotates -- so it is recorded here, where
	// `cctrace status` reads it. Cleared when the file syncs cleanly.
	BlockedByBodyLimit bool `json:"blocked_by_body_limit,omitempty"`
	// ConflictTail carries the microsecond assignment that keeps same-timestamp
	// records off each other's dedup key across a scan boundary. See
	// ConflictNudger: it describes the file prefix Offset has consumed, so the
	// two move together or the assignment stops being reproducible. Absent means
	// nobody counted this file's prefix -- which is what every state file written
	// before this field existed says -- and ConflictSeed reads that as a reason
	// to give up the nudge, never as a count of zero.
	ConflictTail *ConflictTail `json:"conflict_tail,omitempty"`
	// CWDUnknown records that the cwd the consumed bytes ended in could not be
	// established: the offset moved without the records being read (the first
	// sync's skip, a shrink reset) and no line before it carries one. A Claude
	// file's CWD is that last cwd; lines without their own continue from it, so
	// an empty CWD with this unset means only that nobody has looked yet.
	CWDUnknown bool `json:"cwd_unknown,omitempty"`
	// Held records, per cwd, the runs of this file's pending tail that are not
	// being sent because their repository could not be established ("" is the
	// run whose cwd is unknown). Nothing is lost while a cwd is here: the
	// offset stays in front of its records. Recorded rather than only logged
	// so `cctrace status` can say a session is waiting, and so the time a hold
	// has been seen failing survives a restart.
	Held map[string]HeldRun `json:"held,omitempty"`
	// HoldExpired records the last time a hold of this file lasted heldExpiry
	// and its records were dropped unsent. Written by the save that moves the
	// offset past them, and never cleared: the records stay missing, the same
	// reason SkippedAtFirstSync is kept.
	HoldExpired *HoldExpired `json:"hold_expired,omitempty"`
	// ExcludedSeen records that some of this file's records were consumed
	// unsent under a repository allowlist: their repository was excluded, may
	// have been (see Taint), or could never be established. Never cleared --
	// the file keeps those lines, and anything that re-reads it from the start
	// (re-enrichment) would send them.
	ExcludedSeen bool `json:"excluded_seen,omitempty"`
}

// HeldRun is one cwd's hold on a file's tail.
//
// Observed is how long the hold has been seen failing: each retry adds the
// time since the one before, capped at two retry intervals. Time since Since
// would count a night's sleep or a stopped daemon as a day of retrying, and
// drop the records on the first pass after it.
type HeldRun struct {
	Since        time.Time     `json:"since"`
	Reason       string        `json:"reason"`
	Observed     time.Duration `json:"observed"`
	LastFailedAt time.Time     `json:"last_failed_at"`
}

// HoldExpired is a run of records dropped because its hold never ended.
type HoldExpired struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
	CWD    string    `json:"cwd,omitempty"`
}

// CodexAccountObservation records that a given Codex billing account was the
// active one for a given Codex home at a point in time.
//
// Home is the absolute path of the Codex home the account was read from. Each
// home logs in separately, so an account id is only meaningful together with
// the home it came from. Entries written before this field existed have an
// empty Home and attribute nothing: which home they described is unknown, and
// guessing is exactly the mis-attribution this field prevents.
type CodexAccountObservation struct {
	ObservedAt time.Time `json:"observed_at"`
	Home       string    `json:"home,omitempty"`
	AccountID  string    `json:"account_id"`
}

// ClaudeAccountObservation records that a given Anthropic account was the
// active one for a given Claude home at a point in time.
//
// Home is the absolute path of the Claude home the account was read from. Each
// home logs in separately, so an account uuid is only meaningful together with
// the home it came from.
type ClaudeAccountObservation struct {
	ObservedAt  time.Time `json:"observed_at"`
	Home        string    `json:"home,omitempty"`
	AccountUUID string    `json:"account_uuid"`
}

// State holds per-file sync positions.
type State struct {
	Files map[string]*FileState `json:"files"`
	// CodexAccountObservations is an append-only log of which Codex billing
	// account was seen active, and when. It exists so records collected before
	// the first observation stay unattributed instead of being stamped with
	// today's account.
	CodexAccountObservations []CodexAccountObservation `json:"codex_account_observations,omitempty"`

	// ClaudeAccountObservations is the same log for Claude homes, keyed on
	// oauthAccount.accountUuid. Kept as a separate array rather than merged with
	// the Codex one so existing state files keep parsing unchanged.
	ClaudeAccountObservations []ClaudeAccountObservation `json:"claude_account_observations,omitempty"`

	// RulesDenied records repositories whose project rules the server refuses,
	// keyed by repository key, holding when the refusal was last seen.
	//
	// The refusal is durable -- the server decides it from session history -- and
	// the consequence is silent: that repository's CLAUDE.md and AGENTS.md are
	// never collected and nothing says so. The in-memory park that stops the
	// retrying (see rulesSkipUntil) dies with the process, so the fact has to
	// live here, where `cctrace status` reads it. Cleared when the repository's
	// rules are accepted.
	RulesDenied map[string]time.Time `json:"rules_denied,omitempty"`

	// TransportFailure records that passes are attempting sends and none are
	// getting through, for the same reason RulesDenied is here: the consequence
	// is silence, and sync.log rotates. In #712 a daemon failed every send for
	// two days while its own log said "34079 records synced" once a minute, and
	// the only surviving trace after rotation was this file's untouched mtime.
	//
	// It outlives the daemon on purpose. The runtime file is deleted when the
	// watcher exits, so a stall that ends in an exit would take the evidence with
	// it. Cleared by the first pass that gets anything through.
	TransportFailure *TransportFailure `json:"transport_failure,omitempty"`

	// ServerExcludedAccounts records accounts the server said are excluded from
	// collection, keyed provider:account_id, holding when it last said so. Their
	// records are consumed without being sent, which is silent by design; this is
	// where `cctrace status` reads it back. An account leaves when the server
	// answers that it is no longer excluded.
	ServerExcludedAccounts map[string]time.Time `json:"server_excluded_accounts,omitempty"`

	// CWDIdentity records, per working directory, the repository the last
	// certain git lookup found there, and Taints, per session file, the parts
	// that were unsent when one of them changed (see provenance.go). Both are
	// written only under a repository allowlist. Kept here rather than in memory
	// because the tail they are asked about can be older than the process.
	CWDIdentity map[string]*CWDIdentity `json:"cwd_identity,omitempty"`
	Taints      map[string][]Taint      `json:"taints,omitempty"`

	path  string
	isNew bool // true when loaded from a non-existent file
}

// Failure classes for TransportFailure, exported because `cctrace status` gives
// each one a different remedy. The distinction is not "what broke" but "does
// restarting the daemon stand a chance": a 429 needs waiting, and replacing the
// process discards the backoff that was honouring the Retry-After. (A 413 is
// not a stall at all -- it holds one file, which BlockedByBodyLimit reports.)
const (
	TransportFailureClassTransport = "transport"
	TransportFailureClassServer    = "server"
)

// transportFailureRefresh bounds how often a continuing failure rewrites the
// state file. The installed hook polls once a second and the file is about a
// megabyte on a working machine, so the record is refreshed on an interval
// rather than per pass. Var, not const, so tests can shrink it.
var transportFailureRefresh = 5 * time.Minute

// transportFailureGap is how many refresh intervals without a recorded failure
// end a stretch. A running daemon refreshes at least every interval (its backoff
// caps at five minutes too), so three missed ones means nothing was trying.
const transportFailureGap = 3

// transportFailureErrorMax caps the stored error text. The value is read by a
// person out of a status line, and the state file should not grow by the length
// of a server's prose.
const transportFailureErrorMax = 200

// TransportFailure is a stretch of passes that attempted sends and sent nothing.
//
// LastFailedAt is the last failure that was written down, not the last one that
// happened: the refresh interval is measured from it.
type TransportFailure struct {
	Class         string    `json:"class"`
	FirstFailedAt time.Time `json:"first_failed_at"`
	LastFailedAt  time.Time `json:"last_failed_at"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	FilesFailed   int       `json:"files_failed"`
	LastError     string    `json:"last_error,omitempty"`
}

// NoteTransportFailure records a pass that attempted sends and sent nothing, and
// reports whether the caller should save.
//
// A continuing failure of the same class inside the refresh interval reports
// false and leaves the record untouched, so LastFailedAt keeps meaning "as of
// the last write" and the interval cannot be outrun by the poll rate.
func (s *State) NoteTransportFailure(now time.Time, class string, filesFailed int, lastErr string) bool {
	if class == "" {
		class = TransportFailureClassTransport
	}
	text := lastErr
	if len(text) > transportFailureErrorMax {
		text = text[:transportFailureErrorMax]
	}
	rec := s.TransportFailure
	// A gap much longer than the refresh interval means nobody was trying --
	// the machine slept, or no daemon ran -- so the failure seen now starts a
	// new stretch. Carrying FirstFailedAt across it attributed a night's sleep
	// to the daemon ("failing for 8 hours") thirty seconds after it woke.
	if rec != nil && now.Sub(rec.LastFailedAt) > transportFailureGap*transportFailureRefresh {
		rec = nil
	}
	if rec == nil {
		s.TransportFailure = &TransportFailure{
			Class:         class,
			FirstFailedAt: now,
			LastFailedAt:  now,
			LastSuccessAt: s.lastSyncedAt(),
			FilesFailed:   filesFailed,
			LastError:     text,
		}
		return true
	}
	// The in-memory record always follows the latest pass: the stall exit reads
	// the class from here, and a stale "transport" would let a 429 stall end the
	// daemon. A class change is also written at once, because `cctrace status`
	// reads only the file and would otherwise name the wrong remedy for up to
	// an interval. What bounds the write rate is the watch loop's backoff, not
	// how often the class changes: every failing pass arms it, so once it caps
	// there is at most one pass -- and one write -- every four to six minutes,
	// however the class flips. Everything else
	// waits for the interval, and LastFailedAt moves only with a write so it
	// keeps meaning "as of the last write".
	classChanged := rec.Class != class
	rec.Class = class
	rec.FilesFailed = filesFailed
	rec.LastError = text
	if !classChanged && now.Sub(rec.LastFailedAt) < transportFailureRefresh {
		return false
	}
	rec.LastFailedAt = now
	return true
}

// NoteServerExclusions records the server's answer about asked: the accounts
// in excluded are noted, the rest are forgotten.
func (s *State) NoteServerExclusions(asked, excluded []AccountRef, at time.Time) {
	for _, a := range asked {
		delete(s.ServerExcludedAccounts, a.Key())
	}
	for _, a := range excluded {
		if s.ServerExcludedAccounts == nil {
			s.ServerExcludedAccounts = make(map[string]time.Time)
		}
		s.ServerExcludedAccounts[a.Key()] = at
	}
}

// ClearTransportFailure forgets a stall once sending works, and reports whether
// anything was cleared.
func (s *State) ClearTransportFailure() bool {
	if s.TransportFailure == nil {
		return false
	}
	s.TransportFailure = nil
	return true
}

// lastSyncedAt returns the most recent successful send across every tracked
// file -- the answer to "when did collection last work", which a stall notice
// has to carry.
func (s *State) lastSyncedAt() time.Time {
	var latest time.Time
	for _, fs := range s.Files {
		if fs == nil {
			continue
		}
		if fs.LastSynced.After(latest) {
			latest = fs.LastSynced
		}
	}
	return latest
}

// ObserveCodexAccount appends an observation for home and reports whether it
// did. An empty home or account id is ignored.
//
// Repeating the most recent account id *of that home* is a no-op: a token
// refresh rewrites auth.json without changing the account, so only a changed
// value is a real account switch. (This is also why the file's mtime must never
// be used here.) The comparison is per home, so alternating between two homes
// with different accounts does not append on every pass.
func (s *State) ObserveCodexAccount(now time.Time, home, accountID string) bool {
	if home == "" || accountID == "" {
		return false
	}
	for i := len(s.CodexAccountObservations) - 1; i >= 0; i-- {
		obs := s.CodexAccountObservations[i]
		if obs.Home != home {
			continue
		}
		if obs.AccountID == accountID {
			return false
		}
		break
	}
	s.CodexAccountObservations = append(s.CodexAccountObservations, CodexAccountObservation{
		ObservedAt: now,
		Home:       home,
		AccountID:  accountID,
	})
	return true
}

// CodexAccountAt returns the account id of the latest observation of home made
// at or before ts, or "" when ts precedes every observation of that home (or
// the home was never observed). A record older than its own home's first
// observation is deliberately unattributed rather than guessed, and another
// home's account is never a fallback.
func (s *State) CodexAccountAt(home string, ts time.Time) string {
	if home == "" {
		return ""
	}
	accountID := ""
	for _, obs := range s.CodexAccountObservations {
		if obs.Home != home {
			continue
		}
		if obs.ObservedAt.After(ts) {
			break
		}
		accountID = obs.AccountID
	}
	return accountID
}

// ObserveClaudeAccount appends an observation for home and reports whether it
// did. An empty home or account uuid is ignored.
//
// Repeating the most recent account uuid *of that home* is a no-op: Claude Code
// rewrites .claude.json constantly for reasons unrelated to the account (caches,
// project paths, onboarding flags), so only a changed value is a real account
// switch. This is why the file's mtime must never be used here. The comparison
// is per home, so alternating between two homes does not append on every pass.
func (s *State) ObserveClaudeAccount(now time.Time, home, accountUUID string) bool {
	if home == "" || accountUUID == "" {
		return false
	}
	for i := len(s.ClaudeAccountObservations) - 1; i >= 0; i-- {
		obs := s.ClaudeAccountObservations[i]
		if obs.Home != home {
			continue
		}
		if obs.AccountUUID == accountUUID {
			return false
		}
		break
	}
	s.ClaudeAccountObservations = append(s.ClaudeAccountObservations, ClaudeAccountObservation{
		ObservedAt:  now,
		Home:        home,
		AccountUUID: accountUUID,
	})
	return true
}

// ClaudeAccountAt returns the account uuid of the latest observation of home
// made at or before ts, or "" when ts precedes every observation of that home
// (or the home was never observed). A record older than its own home's first
// observation is deliberately unattributed rather than guessed, and another
// home's account is never a fallback.
func (s *State) ClaudeAccountAt(home string, ts time.Time) string {
	if home == "" {
		return ""
	}
	accountUUID := ""
	for _, obs := range s.ClaudeAccountObservations {
		if obs.Home != home {
			continue
		}
		if obs.ObservedAt.After(ts) {
			break
		}
		accountUUID = obs.AccountUUID
	}
	return accountUUID
}

// DefaultStatePath returns ~/.cctrace/sync-state.json
func DefaultStatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cctrace", "sync-state.json")
}

// LoadState reads the sync state from disk. Returns an empty state if the file doesn't exist.
// NoteRulesDenied records that the server refused rule ingest for repositoryKey.
// Reports whether this is new information, so a caller can avoid saving state for
// a refusal it already knows about.
func (s *State) NoteRulesDenied(repositoryKey string, at time.Time) bool {
	if repositoryKey == "" {
		return false
	}
	if s.RulesDenied == nil {
		s.RulesDenied = make(map[string]time.Time)
	}
	_, known := s.RulesDenied[repositoryKey]
	s.RulesDenied[repositoryKey] = at
	return !known
}

// ClearRulesDenied forgets a refusal after the server accepts that repository's
// rules. Reports whether anything was cleared.
func (s *State) ClearRulesDenied(repositoryKey string) bool {
	if repositoryKey == "" || s.RulesDenied == nil {
		return false
	}
	if _, ok := s.RulesDenied[repositoryKey]; !ok {
		return false
	}
	delete(s.RulesDenied, repositoryKey)
	return true
}

func LoadState(path string) (*State, error) {
	s := &State{Files: make(map[string]*FileState), path: path}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		s.isNew = true
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		// Corrupt state: treat as new to avoid re-sending historical sessions
		s.isNew = true
		s.Files = make(map[string]*FileState)
		return s, nil
	}
	s.path = path
	if s.Files == nil {
		s.Files = make(map[string]*FileState)
	}
	return s, nil
}

// Save persists the state to disk atomically.
// After a successful save, isNew is cleared so new files are synced from offset 0.
func (s *State) Save() error {
	// Without this, an empty path makes filepath.Dir("") resolve to "." and the
	// atomic write lands a stray ".tmp" in the working directory before
	// Rename(".tmp", "") fails. Callers that discard the error -- most of them,
	// because a failed state save must not stop collection -- then leave that
	// file behind with nothing pointing at the cause. A State with no path is a
	// construction mistake, so it says so instead of writing somewhere.
	if s.path == "" {
		return errors.New("state has no path")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.isNew = false
	return nil
}

// GetOffset returns the last synced byte offset for a file.
func (s *State) GetOffset(path string) int64 {
	if f, ok := s.Files[path]; ok {
		return f.Offset
	}
	return 0
}

// SetOffset updates the byte offset and last sync time for a file.
// SetOffset advances the recorded position for a file, leaving everything else
// about it alone. It used to replace the whole entry, which quietly discarded
// any field the caller was not thinking about — SkippedAtFirstSync is one such
// field, and it must survive every later pass because the content it accounts
// for stays missing.
func (s *State) SetOffset(path string, offset int64) {
	f := s.Files[path]
	if f == nil {
		f = &FileState{}
		s.Files[path] = f
	}
	f.Offset = offset
	f.LastSynced = time.Now()
}

// SetOffsetWithMetadata updates the byte offset and preserves agent-specific
// metadata needed when a growing JSONL file is later scanned from the middle.
func (s *State) SetOffsetWithMetadata(path string, offset int64, cwd, model string) {
	f := s.Files[path]
	if f == nil {
		f = &FileState{}
		s.Files[path] = f
	}
	f.Offset = offset
	f.LastSynced = time.Now()
	if cwd != "" {
		f.CWD = cwd
	}
	if model != "" {
		f.Model = model
	}
}

// ConflictTail returns the conflict ledger recorded for a file, or nil when the
// file has none — a file the state has never seen, one whose offset was reset,
// or one last written by a cctrace that predates the ledger.
//
// Callers scanning a file want ConflictSeed instead: a ledger being present is
// not the same as it describing the bytes about to be skipped.
func (s *State) ConflictTail(path string) *ConflictTail {
	if f, ok := s.Files[path]; ok {
		return f.ConflictTail
	}
	return nil
}

// ConflictSeed answers the two questions a scan of path starting at offset has
// about the conflict ledger: what to count from, and whether it may record what
// it counted.
//
// The ledger is only ever as good as the proof that it describes the prefix in
// front of the scan. There are exactly two such proofs. A scan starting at byte
// zero skips nothing, so counting from zero describes the prefix by definition.
// A scan resuming behind a ledger whose Offset is this offset and whose Anchor
// still matches the bytes there is reading the continuation of the prefix that
// ledger counted. Everything else — a state written before the ledger existed,
// a file skipped to EOF at first sync, an offset reset by a shrink, a file
// rewritten underneath a ledger — leaves a prefix that nobody counted.
//
// For those the answer is (nil, false): count from zero like the pre-ledger
// code and record nothing. That keeps the loss this ledger was built to fix,
// for those files, and that is the deliberate trade. The loss is a record the
// server drops because the key is taken, which is where it already was. Guessing
// instead puts records on keys a from-zero scan would not choose, and then the
// next re-send stores them a second time — turning an old silent loss into a
// new silent duplicate, in a system whose recovery from every retry, timeout and
// full rescan depends on re-sending being idempotent.
func (s *State) ConflictSeed(path string, offset int64) (*ConflictTail, bool) {
	if offset == 0 {
		return nil, true
	}
	f, ok := s.Files[path]
	if !ok || f.ConflictTail == nil || f.ConflictTail.Offset != offset {
		return nil, false
	}
	if f.ConflictTail.Anchor != ConflictAnchor(path, offset) {
		return nil, false
	}
	return f.ConflictTail, true
}

// SetConflictTail records the ledger describing the prefix the file's offset has
// consumed. Write it only where the offset itself advances: a ledger ahead of
// the offset double-counts the records in between, a ledger behind it drops
// them.
func (s *State) SetConflictTail(path string, tail *ConflictTail) {
	f := s.Files[path]
	if f == nil {
		f = &FileState{}
		s.Files[path] = f
	}
	f.ConflictTail = tail
}

// AdvanceConflictTail records a scan's ledger for a file whose offset moved
// from offset to newOffset. A pass that did not advance is one whose records
// will be read again next time -- a rewound offset after a failed send -- and
// its ledger has to stay where it was so the re-read re-derives the same
// microseconds instead of adding another round of them.
//
// trusted is ConflictSeed's verdict on the prefix this scan started behind. An
// untrusted scan counted from zero over a prefix nobody counted, so its totals
// are a suffix's totals wearing a file's name. Writing them would hand the next
// scan a ledger that looks exactly like a real one, which is how a single
// unproven start spreads forward into every later pass -- so the stale ledger
// is dropped instead and the file stays on the pre-ledger path until something
// reads it from byte zero again.
func (s *State) AdvanceConflictTail(path string, offset, newOffset int64, trusted bool, n *ConflictNudger) {
	if !trusted {
		if f, ok := s.Files[path]; ok {
			f.ConflictTail = nil
		}
		return
	}
	if newOffset == offset || n == nil {
		return
	}
	tail := n.Tail()
	tail.Offset = newOffset
	tail.Anchor = ConflictAnchor(path, newOffset)
	s.SetConflictTail(path, tail)
}

// IsNew returns true when the state was loaded from a non-existent file.
func (s *State) IsNew() bool {
	return s.isNew
}

// HasFile returns true when the state has an entry for the given file path.
func (s *State) HasFile(path string) bool {
	_, ok := s.Files[path]
	return ok
}
