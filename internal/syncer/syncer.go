package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cctrace/internal/claudeauth"
	"cctrace/internal/commandclass"
	"cctrace/internal/gitctx"
	"cctrace/internal/projectrule"
	"cctrace/internal/sessionlog"
	"cctrace/internal/store"
)

const batchSize = 200
const metaTTL = 1 * time.Hour
const ruleScanTTL = 30 * time.Second

// lookupRetryInterval is how long a git lookup that settled nothing stands for
// the callers that can wait -- records sent without an allowlist, and the idle
// metadata refresh -- and how long a file with a held run is left alone. A
// hung mount costs commandTimeout per lookup, and the watch loop polls every
// second.
//
// It is not a bound per cwd. Under an allowlist a file with new records that
// is not itself waiting looks its cwds up on its own pass, whatever another
// file learned about them a second ago: a new session in a broken cwd costs
// one lookup before it starts waiting too.
const lookupRetryInterval = 30 * time.Second

// RuleForbiddenTTL parks a repository the server refuses to accept rules for.
//
// Exported because internal/codexsyncer needs the same value. It had its own
// handler on the ordinary scan TTL, so the fix below reached only half the
// clients: production took 23k-41k refused rule posts a day for at least ten
// days after this constant was introduced (#619). One constant, one meaning.
//
// Access denied is durable in a way the other failures are not: a token that
// cannot read a repository will not start being able to on the next pass. On
// the ordinary 30s TTL that costs one request and one log line every 30
// seconds for the life of the daemon, and the cost lands where it hurts most --
// sync.log is where update failures and collection errors have to be found
// afterwards, and this message crowds them out. On one client 200 log lines
// covered 30 minutes and the update error the diagnosis needed had already
// scrolled away (#458).
const RuleForbiddenTTL = 1 * time.Hour

// maxRuleRetryAfter bounds how long a server-supplied Retry-After may park a
// repository, so a mistaken or hostile value cannot stop rule collection for the
// rest of the daemon's life.
const maxRuleRetryAfter = time.Hour

// nowFn is the package clock, overridable so parking windows are testable
// without sleeping.
var nowFn = time.Now

// ruleParkDelay returns how long a failed send parks its repository: the
// server's own Retry-After when it gave one, otherwise the ordinary TTL. A
// shorter Retry-After never shortens the TTL — the scan cost is the reason for
// the TTL and it does not go away because the server is willing to be asked
// again sooner.
func ruleParkDelay(err error) time.Duration {
	var re *RetryableError
	if errors.As(err, &re) && re.RetryAfter > ruleScanTTL {
		if re.RetryAfter > maxRuleRetryAfter {
			return maxRuleRetryAfter
		}
		return re.RetryAfter
	}
	return ruleScanTTL
}

// projectRuleScanTimeout bounds a single repository rule-file walk. The walk
// issues readdir syscalls that can block forever on a stalled network or
// cloud-sync mount, and it runs inline in the sync pass: without a bound one bad
// repository stops collection for every agent (a production daemon was found
// parked in fdopendir with the whole pass behind it). 10s is well above a warm
// local walk of a large monorepo yet short enough that a hung tree costs a
// bounded amount, once per TTL, instead of the whole pass.
//
// The bound only holds because the scan runs through projectrule.RunBounded: a
// deadline alone is cooperative and a parked syscall never observes it. Var, not
// const, so tests can shrink it.
var projectRuleScanTimeout = 10 * time.Second

// projectRuleScan is the rule scanner, indirected for tests.
var projectRuleScan = projectrule.Scan

// resolveGit reads a cwd's git metadata, indirected for tests. The error wraps
// gitctx.ErrUncertain when the lookup did not hear git's answer.
var resolveGit = gitctx.ResolveChecked

type projectMeta struct {
	repositoryRoot     string
	projectName        string
	gitRemoteURL       string
	repositoryID       string
	repositoryIDSource string
	repositoryName     string
	repoSubpath        string
	repoSubpathPresent bool
	commitSHA          string
	branch             string
	cachedAt           time.Time
	// uncertain is set when the lookup did not hear git's answer
	// (gitctx.ErrUncertain): the identity above is a fallback that says
	// nothing about the cwd. retryAt is when git may be asked again.
	uncertain bool
	retryAt   time.Time
	// hold is set, under an allowlist only, when this lookup cannot be used
	// to judge the cwd's records: the reason they are held (see the HeldReason
	// constants). Such a lookup is never put in cwdCache.
	hold string
}

// Syncer orchestrates scanning JSONL files and sending records to the server.
type Syncer struct {
	claudeDir    string
	cmdClass     *commandclass.Cache
	profileEmail string
	userID       string
	state        *State
	client       *Client
	// collectPrefixes, when non-empty, restricts syncing to repositories whose
	// normalized id starts with one of these prefixes. Empty collects all.
	collectPrefixes []string
	mu              sync.Mutex
	cwdCache        map[string]*projectMeta
	metaSent        map[string]time.Time
	rulesSkipUntil  map[string]time.Time
	// rulesUnsupported is set once the server reports no project-rules endpoint,
	// so we stop scanning/sending (and logging) for the rest of the session.
	rulesUnsupported bool
	// shrunkFiles marks files currently found shrunk below their stored offset,
	// so the "stalled" log fires once per stall episode instead of every pass
	// (this state is deliberately not rewound - see the no-rewind note in
	// syncFile - so without suppression it would repeat on every poll forever).
	// Cleared once the file's size catches back up to the offset.
	shrunkFiles map[string]bool
	// pass holds the result of the most recent SyncOnce. A pass reports how much
	// got through as its return value, but "nothing got through" and "nothing was
	// attempted" are the same 0 there, and only the failure count separates them
	// (#712). Reset at the start of every pass, read through LastPass.
	pass PassStats
	// lastPassSummary suppresses an unchanged end-of-pass line, the same way the
	// Codex syncer does: watch mode polls every second under the installed hook,
	// and a summary that has not changed carries nothing new.
	lastPassSummary string
	// accounts drops the records of excluded accounts before they are sent.
	accounts *AccountFilter
	// passMeta holds every lookup made in the current pass, so files sharing a
	// cwd share one lookup. Cleared at the start of each SyncOnce.
	passMeta map[string]*projectMeta
	// uncertainMeta holds each cwd's last lookup that settled nothing --
	// uncertain, or held under an allowlist -- which stands until its retryAt
	// so a git that keeps failing is not asked every pass.
	uncertainMeta map[string]*projectMeta
	// heldRetryAt is when a file with a held run in its tail is looked at
	// again. The entry stays for as long as the file keeps holding.
	heldRetryAt map[string]time.Time
	// stateDirty marks a state change that is saved at the end of the pass
	// rather than where it was made (see flushState).
	stateDirty bool
}

// PassStats is the result of one scan-and-send pass.
//
// Sent and FilesFailed are read together: a pass that failed some files while
// others got through proves the path to the server is open, and only
// FilesFailed > 0 with Sent == 0 means nothing left the machine at all.
type PassStats struct {
	Sent        int
	FilesFailed int
	LastErr     error
	// Refused is set when any file in the pass was refused with a 429. The
	// class of a stall is decided by it rather than by LastErr, which is only
	// whichever file the directory walk reached last.
	Refused bool
	// RetryAfter is the longest wait any 429 in the pass asked for. Per-file
	// refusals never reach SyncOnce's error, so this is the only way the watch
	// loop learns the server's schedule instead of retrying on its own.
	RetryAfter time.Duration
}

// LastPass returns the result of the most recent SyncOnce. Read it from the
// goroutine that ran the pass: the field is not guarded, and the watch loop is
// its only reader.
func (s *Syncer) LastPass() PassStats {
	return s.pass
}

// sendError marks a failure that happened on the way to the server, as opposed
// to one reading a session file. Only these say anything about the path: an
// unreadable file on this disk left one session behind, and counting it as a
// failed send backed off collection for the whole machine.
type sendError struct{ err error }

func (e *sendError) Error() string { return "send batch: " + e.err.Error() }
func (e *sendError) Unwrap() error { return e.err }

// New creates a new Syncer. collectPrefixes restricts which repositories are
// synced; pass nil to collect every repository.
func New(claudeDir, profileEmail, userID string, state *State, client *Client, collectPrefixes []string) *Syncer {
	return &Syncer{
		claudeDir:       claudeDir,
		cmdClass:        commandclass.NewCache(claudeDir),
		profileEmail:    profileEmail,
		userID:          userID,
		state:           state,
		client:          client,
		collectPrefixes: collectPrefixes,
		cwdCache:        make(map[string]*projectMeta),
		passMeta:        make(map[string]*projectMeta),
		uncertainMeta:   make(map[string]*projectMeta),
		heldRetryAt:     make(map[string]time.Time),
		metaSent:        make(map[string]time.Time),
		rulesSkipUntil:  make(map[string]time.Time),
		shrunkFiles:     make(map[string]bool),
		accounts:        NewAccountFilter(client, state),
	}
}

// observeAccount records which Anthropic account is active now for this
// Syncer's Claude home, once per pass rather than per file. The value comes from
// oauthAccount.accountUuid, which survives the constant unrelated rewrites of
// .claude.json, so only a real account switch appends a new observation -- the
// file's mtime says nothing and is never consulted.
//
// Unlike Codex, one Syncer serves exactly one home (sessionlog.DefaultClaudeDir
// returns CLAUDE_CONFIG_DIR *instead of* the default rather than in addition to
// it), so there is no set of homes to iterate here. The observation still
// carries its home, because separate profiles run separate Syncers over the same
// state file.
func (s *Syncer) observeAccount() {
	accountUUID, err := claudeauth.ReadAccountUUID(s.claudeDir)
	if err != nil {
		log.Printf("[syncer] read account uuid from %s: %v", s.claudeDir, err)
		return
	}
	if s.state.ObserveClaudeAccount(time.Now(), normalizeHome(s.claudeDir), accountUUID) {
		if err := s.state.Save(); err != nil {
			log.Printf("[syncer] save state: %v", err)
		}
	}
}

// normalizeHome returns the absolute, cleaned form of a configured Claude home.
// Observations are keyed by it, so every writer must agree on one spelling of
// the same directory.
func normalizeHome(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	return abs
}

// SyncOnce performs a single scan-and-send pass over all JSONL files.
// Returns the number of records sent.
func (s *Syncer) SyncOnce(ctx context.Context) (int, error) {
	// Reset before anything can return, so an early exit never leaves the
	// previous pass's result looking like this one's.
	s.pass = PassStats{}
	s.mu.Lock()
	clear(s.passMeta)
	s.mu.Unlock()
	defer s.flushState()
	files, err := sessionlog.FindJSONLFiles(s.claudeDir)
	if err != nil {
		return 0, fmt.Errorf("find jsonl files: %w", err)
	}

	// Capture the first-run flag once for the whole pass. The first Save() clears
	// state.IsNew(), so re-reading it per file would skip only the first file and
	// backfill the rest of the history.
	firstRun := s.state.IsNew()
	if firstRun {
		// Close the first-run window even when this pass has nothing to record.
		// The window exists to skip history that predates the install, and that
		// justification ends when this pass ends: whatever appears afterwards is
		// new. Until this Save the state stayed "new" indefinitely, so a user who
		// ran `cctrace init` before starting an agent — the order the guide leads
		// them to — had their first session skipped whole, described in the log as
		// "pre-existing" although it had not existed.
		if err := s.state.Save(); err != nil {
			log.Printf("[syncer] failed to save state: %v", err)
		}
	}
	s.observeAccount()
	s.accounts.BeginPass()

	total := 0
	for _, file := range files {
		// Stop promptly when the watch context is cancelled (graceful stop),
		// rather than scanning every remaining file first.
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := s.syncFile(ctx, file, firstRun)
		// Counted before the error is looked at: a file that fails part-way
		// has still sent what it sent, and a pass that got anything through
		// is not a stalled one (see recordPassOutcome).
		total += n
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return total, ctxErr
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return total, err
			}
			log.Printf("[syncer] %s: %v", file, err)
			s.notePassFailure(err)
			continue
		}
	}
	s.pass.Sent = total
	s.recordPassOutcome()
	s.logPassSummary(len(files))
	return total, nil
}

// notePassFailure counts a file's failure toward the pass, if it is the kind
// that says something about the path to the server.
//
// Two kinds are left out. A local read failure never reached the network. A
// body-limit hold is one oversized record in one session: it already has its
// own flag (BlockedByBodyLimit) and its own status notice with a remedy scoped
// to that file, and counting it reported the server as refusing a machine whose
// other sessions were all getting through.
func (s *Syncer) notePassFailure(err error) {
	var se *sendError
	if !errors.As(err, &se) || errors.Is(err, ErrBodyTooLarge) {
		return
	}
	s.pass.FilesFailed++
	s.pass.LastErr = err
	var retryable *RetryableError
	if errors.As(err, &retryable) {
		s.pass.Refused = true
		if retryable.RetryAfter > s.pass.RetryAfter {
			s.pass.RetryAfter = retryable.RetryAfter
		}
	}
}

// recordPassOutcome writes down, or withdraws, the fact that nothing is getting
// through.
//
// Three passes look different and only one of them is a stall: a pass that
// failed nothing is idle or fine, a pass that failed some files while others
// were accepted proves the path to the server is open, and a pass that
// attempted sends and got nothing through is the state #712 hid for two days.
// Deciding from the failure count rather than from elapsed time is what keeps a
// machine whose user is away from reporting a stall.
func (s *Syncer) recordPassOutcome() {
	if s.pass.FilesFailed == 0 || s.pass.Sent > 0 {
		if s.state.ClearTransportFailure() {
			if err := s.state.Save(); err != nil {
				log.Printf("[syncer] failed to save state: %v", err)
			}
		}
		return
	}
	text := ""
	if s.pass.LastErr != nil {
		text = s.pass.LastErr.Error()
	}
	class := TransportFailureClassTransport
	if s.pass.Refused {
		class = TransportFailureClassServer
	}
	if s.state.NoteTransportFailure(nowFn(), class, s.pass.FilesFailed, text) {
		if err := s.state.Save(); err != nil {
			log.Printf("[syncer] failed to save state: %v", err)
		}
	}
}

// logPassSummary reports what the pass just did, once per change.
//
// The watch loop's own line counts records since the process started, so a pass
// that sent nothing printed the same number as the pass before it -- and the log
// deduper, correctly, folded the repeat away. Two days of total failure were
// indistinguishable from two days of quiet that way (#712). The failure count is
// the part that has to be on the line.
func (s *Syncer) logPassSummary(files int) {
	summary := fmt.Sprintf("files=%d synced=%d failed=%d", files, s.pass.Sent, s.pass.FilesFailed)
	if s.pass.LastErr != nil {
		summary += " | " + s.pass.LastErr.Error()
	}
	if summary == s.lastPassSummary {
		return
	}
	s.lastPassSummary = summary
	log.Printf("[syncer] pass %s", summary)
}

func (s *Syncer) syncFile(ctx context.Context, filePath string, firstRun bool) (int, error) {
	// If state was freshly created (no prior state file), skip existing files to EOF.
	// But if state already existed and this is just a new file (new session), sync from the start.
	//
	// The skip is deliberate — a fresh install must not backfill every old session —
	// but it also catches the new user's own first session. `cctrace init` is
	// normally run from inside a live Claude Code session, so when the hook starts
	// the daemon that session's file is already on disk, and everything written up
	// to that moment is dropped. The session still uploads, which is what makes
	// this bad: the user gets a truncated session with nothing indicating a gap.
	// The dropped opening turn is also the one carrying prompt_source=typed, so the
	// session then reads as having no genuine human turn and lands under headless,
	// outside the default view.
	//
	// So say what was dropped. Logged at the moment of the skip; the next pass finds
	// the file in state, so this cannot become a per-cycle refrain.
	if !s.state.HasFile(filePath) && firstRun {
		fi, err := os.Stat(filePath)
		if err == nil {
			s.state.SetOffset(filePath, fi.Size())
			s.resetLastCWD(filePath, fi.Size())
			if fi.Size() > 0 {
				// Recorded in state as well as logged: sync.log rotates at 10MB and
				// this line is written once per file, so on an active machine the
				// log copy ages out. `cctrace status` reads the state copy.
				s.state.Files[filePath].SkippedAtFirstSync = fi.Size()
			}
			if err := s.state.Save(); err != nil {
				log.Printf("[syncer] failed to save state: %v", err)
			}
			if fi.Size() > 0 {
				log.Printf("[syncer] %s: skipped (pre-existing at first sync — earlier content is not collected) bytes=%d", filePath, fi.Size())
			}
		}
		return 0, nil
	}

	if until, holding := s.heldRetryAt[filePath]; holding && nowFn().Before(until) {
		return 0, nil
	}

	offset := s.state.GetOffset(filePath)

	// A shrunk file has its offset reset to the file's current size - not
	// rewound to zero, and not left where it was.
	//
	// Rewinding to zero would re-read bytes that were already sent, duplicating
	// the metadata rows a session file carries (see
	// TestSyncer_SyncOnce_DoesNotRewindShrunkFile). But leaving the offset above
	// the file's size strands everything written afterwards: the scan seeks past
	// EOF and returns nothing, every pass, until the file grows back past the
	// stored offset. For a session rewritten from megabytes down to a metadata
	// remnant and then resumed, that is the whole resumed conversation.
	//
	// Resetting to the current size is what both cases actually want. The bytes
	// still on disk were either already sent (a truncation, where they are the
	// prefix of what was sent) or are the remnant of a rewrite, whose rows are
	// already on the server from before the rewrite. Everything appended from
	// here on is collected normally.
	if fi, statErr := os.Stat(filePath); statErr == nil && fi.Size() < offset {
		if !s.shrunkFiles[filePath] {
			s.shrunkFiles[filePath] = true
			log.Printf("[syncer] %s: shrunk, offset reset %d -> %d (%d bytes no longer on disk)", filePath, offset, fi.Size(), offset-fi.Size())
		}
		offset = fi.Size()
		s.state.SetOffset(filePath, offset)
		s.resetLastCWD(filePath, offset)
		// Whatever was holding this file's tail is no longer on disk.
		s.state.Files[filePath].Held = nil
		if err := s.state.Save(); err != nil {
			log.Printf("[syncer] failed to save state: %v", err)
		}
	} else {
		delete(s.shrunkFiles, filePath)
	}

	s.pruneTaints(filePath, offset)

	records, newOffset, skipped, err := sessionlog.ScanFileWithSkips(ctx, filePath, offset)
	if err != nil {
		return 0, err
	}
	projectHash := sessionlog.ProjectHash(s.claudeDir, filePath)
	if len(s.collectPrefixes) > 0 && len(records) > 0 {
		return s.syncJudgedTail(ctx, filePath, projectHash, records, skipped, offset, newOffset)
	}
	tailCWD, tailUnknown := lastScanCWD(records, skipped)

	// Extract project metadata from CWD. Records are sent with this pass's
	// lookup (see recordMeta); only the idle metadata refresh below is served
	// from the hour cache.
	var meta projectMeta
	var cwd string
	for _, r := range records {
		if r.CWD != "" {
			cwd = r.CWD
			meta = *s.recordMeta(cwd)
			break
		}
	}

	// If no CWD in new records, check metaSent cache before falling back to PeekCWD
	if meta.projectName == "" && projectHash != "" {
		s.mu.Lock()
		sent, cached := s.metaSent[projectHash]
		s.mu.Unlock()
		if cached && time.Since(sent) < metaTTL {
			// Cache hit: skip PeekCWD and metadata HTTP send
			if len(records) == 0 {
				s.persistAdvancedOffset(filePath, offset, newOffset, tailCWD, tailUnknown)
				return 0, nil
			}
		} else {
			if cwd = sessionlog.PeekCWD(filePath); cwd != "" {
				if len(records) > 0 {
					meta = *s.recordMeta(cwd)
				} else {
					meta = *s.resolveProjectMeta(cwd)
				}
			}
		}
	}

	// Drop repositories outside the configured allowlist before sending. Advance
	// the offset only when the repository id is known, so a transient failure to
	// resolve git metadata is retried rather than silently dropped. Under an
	// allowlist only a tail without records reaches this point; records are
	// judged in syncJudgedTail.
	if !gitctx.AllowsRepository(meta.repositoryID, s.collectPrefixes) {
		if meta.repositoryID != "" {
			s.persistAdvancedOffset(filePath, offset, newOffset, tailCWD, tailUnknown)
		}
		return 0, nil
	}

	if len(records) == 0 {
		// No new records, but still update project metadata if available
		if projectHash != "" && (meta.projectName != "" || meta.gitRemoteURL != "" || meta.repositoryID != "") {
			_, _ = s.client.Send(ctx, "claude", s.profileEmail, s.userID, projectHash,
				meta.projectName, ProjectIdentity{
					GitRemoteURL:       meta.gitRemoteURL,
					RepositoryID:       meta.repositoryID,
					RepositoryIDSource: meta.repositoryIDSource,
					RepositoryName:     meta.repositoryName,
					RepoSubpath:        meta.repoSubpath,
					RepoSubpathPresent: meta.repoSubpathPresent,
					CommitSHA:          meta.commitSHA,
					Branch:             meta.branch,
				}, nil)
			if err := s.sendProjectRules(ctx, projectHash, &meta, cwd); err != nil {
				log.Printf("[syncer] project rules: %v", err)
			}
			s.mu.Lock()
			s.metaSent[projectHash] = time.Now()
			s.mu.Unlock()
		}
		// The scan may still have consumed bytes without producing records (e.g. an
		// oversized line was skipped). Persist the advanced offset, otherwise the
		// same bytes are re-drained on every pass.
		s.persistAdvancedOffset(filePath, offset, newOffset, tailCWD, tailUnknown)
		return 0, nil
	}

	// Convert to store.SessionRecord
	storeRecords := make([]*store.SessionRecord, 0, len(records))
	convert := s.newRecordConverter(filePath, projectHash)
	activeAttributionSkill := s.activeAttributionSkill(filePath)
	for _, r := range records {
		attributionCommand := attributionSkillInvocationName(r, &activeAttributionSkill)
		if sr := convert(r, attributionCommand); sr != nil {
			storeRecords = append(storeRecords, sr)
		}
	}

	sent, err := s.sendRecords(ctx, filePath, projectHash, &meta, cwd, storeRecords)
	if err != nil {
		return sent, err
	}

	// Update state only after successful send
	s.advanceOffset(filePath, newOffset, tailCWD, tailUnknown).LastAttributionSkill = activeAttributionSkill
	if err := s.state.Save(); err != nil {
		log.Printf("[syncer] failed to save state: %v", err)
	}

	return sent, nil
}

// newRecordConverter returns the conversion of one session file's records to
// store.SessionRecord, with what is the same for every record of the file
// worked out once. The conversion returns nil for a record that is not sent.
func (s *Syncer) newRecordConverter(filePath, projectHash string) func(r *sessionlog.Record, attributionCommand string) *store.SessionRecord {
	home := normalizeHome(s.claudeDir)
	sourceFile := filepath.Base(filePath)
	// Subagent files carry a sibling <name>.meta.json whose toolUseId links back to
	// the main-thread Task/Agent tool_use that spawned this sidechain.
	toolUseID := subagentToolUseID(filePath)
	return func(r *sessionlog.Record, attributionCommand string) *store.SessionRecord {
		sr := toStoreRecord(r, s.profileEmail, s.userID, projectHash, attributionCommand)
		s.classifyCommand(sr, r.CWD)
		if sr == nil {
			return nil
		}
		sr.SourceFile = sourceFile
		if toolUseID != "" {
			sr.ToolUseID = toolUseID
		}
		// Per record, not per payload: a record older than the first
		// observation must stay blank, so an envelope-level value would be
		// wrong by definition for exactly those rows.
		sr.AccountID = s.state.ClaudeAccountAt(home, sr.Ts)
		return sr
	}
}

// sendRecords sends storeRecords under meta's identity in batches, then the
// repository's rules, and reports how many records the server took. A nil
// error means every record is accounted for and the caller may advance past
// them; that includes the records of excluded accounts, which are dropped here.
func (s *Syncer) sendRecords(ctx context.Context, filePath, projectHash string, meta *projectMeta, cwd string, storeRecords []*store.SessionRecord) (int, error) {
	// An excluded account's records are consumed like sent ones: the offset
	// moves past them, so they are neither retried every pass nor left in
	// front of the records that follow them.
	storeRecords, err := s.accounts.Drop(ctx, "anthropic", storeRecords)
	if err != nil {
		return 0, &sendError{err: err}
	}
	if len(storeRecords) == 0 {
		return 0, nil
	}

	// Send in batches
	sent := 0
	for i := 0; i < len(storeRecords); i += batchSize {
		end := i + batchSize
		if end > len(storeRecords) {
			end = len(storeRecords)
		}
		n, err := s.client.Send(ctx, "claude", s.profileEmail, s.userID, projectHash,
			meta.projectName, ProjectIdentity{
				GitRemoteURL:       meta.gitRemoteURL,
				RepositoryID:       meta.repositoryID,
				RepositoryIDSource: meta.repositoryIDSource,
				RepositoryName:     meta.repositoryName,
				RepoSubpath:        meta.repoSubpath,
				RepoSubpathPresent: meta.repoSubpathPresent,
				CommitSHA:          meta.commitSHA,
				Branch:             meta.branch,
			}, storeRecords[i:end])
		// Before the error is looked at: a batch split after a 413 can have
		// its first half accepted and its second fail, and the client says
		// how many got through either way. A group whose first half is
		// accepted again on every retry therefore counts as progress each
		// time, although its offset does not move.
		sent += n
		if err != nil {
			// The offset is deliberately left where it is by the caller, so the
			// refused bytes stay collectable once the limit is raised. Recorded
			// rather than only logged: this is the one state a person has to act
			// on, and sync.log rotates.
			if errors.Is(err, ErrBodyTooLarge) {
				if fs := s.state.Files[filePath]; fs != nil && !fs.BlockedByBodyLimit {
					fs.BlockedByBodyLimit = true
					if saveErr := s.state.Save(); saveErr != nil {
						log.Printf("[syncer] failed to save state: %v", saveErr)
					}
				}
				log.Printf("[syncer] %s: a record is larger than the server accepts; sync is holding here so raising the limit recovers it", filePath)
			}
			return sent, &sendError{err: err}
		}
	}
	// Every batch got through, so whatever the server was refusing is refused no
	// longer -- the operator raised the limit, or the run of large records ended.
	// Left set, the notice would keep asking for an action with nothing behind
	// it, which is worse than saying nothing: the next real one is not believed.
	if fs := s.state.Files[filePath]; fs != nil && fs.BlockedByBodyLimit {
		fs.BlockedByBodyLimit = false
	}
	if err := s.sendProjectRules(ctx, projectHash, meta, cwd); err != nil {
		log.Printf("[syncer] project rules: %v", err)
	}

	// Record successful metadata send
	if projectHash != "" {
		s.mu.Lock()
		s.metaSent[projectHash] = time.Now()
		s.mu.Unlock()
	}
	return sent, nil
}

// persistAdvancedOffset saves newOffset when the scan actually moved forward.
// Scans that skip an oversized line consume bytes but yield no records, so the
// early-return paths must still persist the offset or the file is re-drained on
// every pass.
func (s *Syncer) persistAdvancedOffset(filePath string, offset, newOffset int64, lastCWD string, cwdUnknown bool) {
	if newOffset == offset {
		return
	}
	s.advanceOffset(filePath, newOffset, lastCWD, cwdUnknown)
	if err := s.state.Save(); err != nil {
		log.Printf("[syncer] failed to save state: %v", err)
	}
}

// resolveProjectMeta returns cwd's metadata from the metaTTL cache, looking git
// up only when the cache has nothing current. It serves what does not have to
// be this pass's answer: the idle metadata refresh and re-enrichment.
func (s *Syncer) resolveProjectMeta(cwd string) *projectMeta {
	s.mu.Lock()
	defer s.mu.Unlock()

	if m, ok := s.cwdCache[cwd]; ok && time.Since(m.cachedAt) < metaTTL {
		return m
	}
	if u := s.standingUncertain(cwd); u != nil {
		return u
	}
	return s.lookupLocked(cwd)
}

// standingUncertain returns cwd's last lookup that settled nothing while it is
// too early to ask git again, or nil. Called with s.mu held.
func (s *Syncer) standingUncertain(cwd string) *projectMeta {
	if u, ok := s.uncertainMeta[cwd]; ok && nowFn().Before(u.retryAt) {
		return u
	}
	return nil
}

// freshProjectMeta returns cwd's metadata as of this pass: this pass's lookup
// when there is one, otherwise a new lookup. The metaTTL cache is never the
// answer.
func (s *Syncer) freshProjectMeta(cwd string) *projectMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.passMeta[cwd]; ok {
		return m
	}
	return s.lookupLocked(cwd)
}

// recordMeta returns the metadata a file's new records are sent with when no
// allowlist judges them.
//
// That is this pass's lookup, used whole: the repository identity, commit and
// branch a record goes out with all come from now, so a backlog is not filed
// under a commit -- or a repository -- cached up to metaTTL ago. Two lookups
// are not believed over a warm cache entry (see warmEntry), and git that keeps
// failing is asked again once per lookupRetryInterval, not once per pass.
func (s *Syncer) recordMeta(cwd string) *projectMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.passMeta[cwd]
	if !ok {
		if m = s.standingUncertain(cwd); m == nil {
			m = s.lookupLocked(cwd)
		}
	}
	if c := s.warmEntry(cwd, m); c != nil {
		return c
	}
	return m
}

// warmEntry returns the metaTTL cache entry that stands in for the lookup m
// when no allowlist is configured, or nil when m is to be used.
//
// Before records looked git up every pass, a cwd resolved once kept that
// identity for metaTTL whatever git said in between. Two answers would now
// replace it at once and file the records under a local fallback: a lookup
// that did not hear git at all, and a certain "no repository here" from a cwd
// that resolved a moment ago (a volume not remounted yet, a directory deleted
// under a running session). Neither is believed over a certain entry still
// inside metaTTL; that entry ages out when it would have before.
//
// Nothing else stands in. A certain lookup that resolves -- to the cached
// repository or to another one -- is used whole: identity, commit and branch
// come from the same moment. A cwd that now holds a different repository is
// therefore sent under it at once rather than when the cache ages out, which
// is a change from the cache, and the intended one: the cached identity with
// the new repository's commit would be data about neither.
//
// With an allowlist nothing stands in: what git says now is what is judged.
// Called with s.mu held.
func (s *Syncer) warmEntry(cwd string, m *projectMeta) *projectMeta {
	if len(s.collectPrefixes) > 0 {
		return nil
	}
	c, ok := s.cwdCache[cwd]
	if !ok || c.uncertain || time.Since(c.cachedAt) >= metaTTL {
		return nil
	}
	if m.uncertain || (c.repositoryIDSource == "resolved" && m.repositoryIDSource == "fallback") {
		return c
	}
	return nil
}

// lookupLocked runs git for cwd and records the result for the pass and in the
// metaTTL cache. Under an allowlist it is also where a replaced repository is
// noticed (observeIdentity): every lookup that can judge records passes here,
// so none of them judges by the recorded identity without looking.
// Called with s.mu held.
func (s *Syncer) lookupLocked(cwd string) *projectMeta {
	m := &projectMeta{
		projectName: filepath.Base(cwd),
		cachedAt:    time.Now(),
	}

	g, err := resolveGit(cwd)
	m.uncertain = err != nil
	// repositoryRoot stays empty when git reports no repository: it is the rule
	// scan root (see sendProjectRules), and a non-repo cwd is not scannable.
	m.repositoryRoot = g.RepositoryRoot
	if g.GitRemoteURL != "" {
		m.gitRemoteURL = g.GitRemoteURL
	}
	if g.RepositoryID != "" {
		m.repositoryID = g.RepositoryID
	}
	m.repositoryIDSource = g.RepositoryIDSource
	if g.RepositoryName != "" {
		m.repositoryName = g.RepositoryName
	}
	m.repoSubpath = g.RepoSubpath
	m.repoSubpathPresent = g.RepoSubpathPresent
	m.commitSHA = g.CommitSHA
	m.branch = g.Branch

	s.passMeta[cwd] = m
	if len(s.collectPrefixes) > 0 {
		// Only under an allowlist: nothing is judged without one, and the
		// default path gains neither state writes nor file listings.
		if m.uncertain {
			m.hold = HeldReasonGitUncertain
		} else {
			m.hold = s.observeIdentity(cwd, g)
		}
	}
	// A lookup that settled nothing stands for lookupRetryInterval, so the
	// callers that may wait do not ask git again every pass.
	if m.uncertain || m.hold != "" {
		m.retryAt = nowFn().Add(lookupRetryInterval)
		s.uncertainMeta[cwd] = m
	} else {
		delete(s.uncertainMeta, cwd)
	}
	if len(s.collectPrefixes) > 0 {
		// A lookup that judges nothing is not cached as the cwd's identity.
		if m.hold == "" {
			s.cwdCache[cwd] = m
		}
		return m
	}
	// With nothing certain to keep, an uncertain fallback is cached like any
	// other answer, as it always was: the idle refresh then leaves a broken cwd
	// alone for metaTTL instead of asking git on every pass.
	if s.warmEntry(cwd, m) == nil {
		s.cwdCache[cwd] = m
	}
	return m
}

// sendProjectRules scans and sends the rules of meta's repository. cachedCWD is
// set when meta came from the metaTTL cache for that cwd: the cached value then
// only decides whether a scan is due, and a due scan resolves git afresh.
func (s *Syncer) sendProjectRules(ctx context.Context, projectHash string, meta *projectMeta, cachedCWD string) error {
	// Only a real git repository root is scannable. repositoryRoot is empty for a
	// non-repo cwd, and scanning that cwd would walk an unbounded tree ("/",
	// "$HOME", a directory holding a hundred checkouts) and merge unrelated
	// repositories' rule files under one project hash.
	if meta == nil || meta.repositoryRoot == "" {
		return nil
	}
	repositoryKey := firstNonEmpty(meta.repositoryID, projectHash)
	if repositoryKey == "" {
		return nil
	}

	cacheKey := "claude|" + repositoryKey + "|" + meta.repositoryRoot
	s.mu.Lock()
	if s.rulesUnsupported {
		s.mu.Unlock()
		return nil
	}
	if until, ok := s.rulesSkipUntil[cacheKey]; ok && nowFn().Before(until) {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if cachedCWD != "" {
		// The cwd may hold another repository, or HEAD may have moved, since the
		// metadata was cached. The scan reads the tree as it is now, so the
		// allowlist and the identity sent with it must come from now as well.
		// This runs at most once per repository per ruleScanTTL, not per file:
		// the fresh value replaces the cached one, so the next file of the same
		// cwd finds the scan parked under the fresh key, or, when the fresh
		// repository is excluded, is dropped by syncFile's allowlist check.
		fresh := s.freshProjectMeta(cachedCWD)
		if !gitctx.AllowsRepository(fresh.repositoryID, s.collectPrefixes) {
			return nil
		}
		return s.sendProjectRules(ctx, projectHash, fresh, "")
	}

	scanCtx, cancel := context.WithTimeout(ctx, projectRuleScanTimeout)
	defer cancel()
	rules, err := projectrule.RunBounded(scanCtx, projectrule.ScanOptions{
		Agent:              projectrule.AgentClaude,
		RepositoryRoot:     meta.repositoryRoot,
		IncludeMissingRoot: true,
	}, projectRuleScan)
	if err != nil {
		// Park the repository on the existing per-repository TTL whatever the
		// failure was. Every session file in that repo hits this path, so without
		// suppression one bad tree burns the same doomed walk thousands of times
		// and the pass still never finishes. A stalled mount fails the root stat
		// with EIO/ETIMEDOUT, not a context error, so keying the suppression on
		// context errors alone left the most expensive case unparked.
		s.mu.Lock()
		s.rulesSkipUntil[cacheKey] = nowFn().Add(ruleScanTTL)
		s.mu.Unlock()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return fmt.Errorf("scan %s timed out after %s; skipping until TTL expires: %w", meta.repositoryRoot, projectRuleScanTimeout, err)
		}
		return fmt.Errorf("scan %s failed; skipping until TTL expires: %w", meta.repositoryRoot, err)
	}
	if len(rules) == 0 {
		// The walk still traversed the whole tree to find nothing; park it too, or
		// a rule-less repository re-walks itself for every session file it owns.
		s.mu.Lock()
		s.rulesSkipUntil[cacheKey] = nowFn().Add(ruleScanTTL)
		s.mu.Unlock()
		return nil
	}

	_, err = s.client.SendProjectRules(ctx, &store.ProjectRuleIngestRequest{
		ProfileEmail:   s.profileEmail,
		UserID:         s.userID,
		Agent:          projectrule.AgentClaude,
		ProjectHash:    projectHash,
		ProjectName:    meta.projectName,
		RepositoryID:   meta.repositoryID,
		RepositoryKey:  repositoryKey,
		RepositoryName: meta.repositoryName,
		CommitSHA:      meta.commitSHA,
		Branch:         meta.branch,
		Rules:          rules,
	})
	if err != nil {
		if errors.Is(err, ErrProjectRulesUnsupported) {
			s.mu.Lock()
			s.rulesUnsupported = true
			s.mu.Unlock()
			log.Printf("[syncer] project rules: server has no /api/project-rules endpoint; disabling rule sync for this session")
			return nil
		}
		if errors.Is(err, ErrProjectRulesForbidden) {
			s.mu.Lock()
			s.rulesSkipUntil[cacheKey] = nowFn().Add(RuleForbiddenTTL)
			s.mu.Unlock()
			// Recorded, not just parked. The park stops the retrying and dies with
			// the process; this is what survives to tell a person that the
			// repository's rules are not being collected at all.
			if s.state.NoteRulesDenied(repositoryKey, nowFn()) {
				_ = s.state.Save()
			}
			log.Printf("[syncer] project rules: access denied for %s; skipping for %s", repositoryKey, RuleForbiddenTTL)
			return nil
		}
		// Park a failed send like every other failure above. This path was the
		// exception, and it is the one that costs most: the scan has already
		// walked the tree, and the caller retries per session file per pass. An
		// unreachable server therefore re-walked and re-posted for every file the
		// repository owns, on every pass, for as long as it stayed unreachable —
		// 192,236 copies of one such failure in a local log.
		//
		// A 429 states how long to stay away, and that deadline wins: parking it
		// on the ordinary TTL would retry in 30s and keep the limit tripped,
		// which is the behaviour Retry-After exists to prevent.
		s.mu.Lock()
		s.rulesSkipUntil[cacheKey] = nowFn().Add(ruleParkDelay(err))
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	s.rulesSkipUntil[cacheKey] = nowFn().Add(ruleScanTTL)
	s.mu.Unlock()
	if s.state.ClearRulesDenied(repositoryKey) {
		_ = s.state.Save()
	}
	return nil
}

func (s *Syncer) activeAttributionSkill(filePath string) string {
	if fs := s.state.Files[filePath]; fs != nil {
		return fs.LastAttributionSkill
	}
	return ""
}

func attributionSkillInvocationName(r *sessionlog.Record, activeSkill *string) string {
	if r.Type == "user" && !r.IsToolResultUser() {
		*activeSkill = ""
	}
	skill := strings.TrimPrefix(strings.TrimSpace(r.AttributionSkill), "/")
	if skill == "" {
		return ""
	}
	if *activeSkill == skill {
		return ""
	}
	*activeSkill = skill
	return skill
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// subagentToolUseID reads the sibling <name>.meta.json of a subagent JSONL file
// and returns its toolUseId — the main-thread Task/Agent tool_use that spawned the
// sidechain. Returns "" for non-subagent files or when no meta/toolUseId exists.
func subagentToolUseID(filePath string) string {
	if !strings.Contains(filepath.ToSlash(filePath), "/subagents/") {
		return ""
	}
	data, err := os.ReadFile(strings.TrimSuffix(filePath, ".jsonl") + ".meta.json")
	if err != nil {
		return ""
	}
	var meta struct {
		ToolUseID string `json:"toolUseId"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return ""
	}
	return meta.ToolUseID
}

// toStoreRecord converts a sessionlog.Record to a store.SessionRecord.
// Returns nil for record types that should be skipped.
func toStoreRecord(r *sessionlog.Record, profileEmail, userID, projectHash string, commandOverride ...string) *store.SessionRecord {
	raw, err := json.Marshal(r)
	if err != nil {
		return nil
	}

	// A record's storage key is (session_id, ts, record_type, profile_email, uuid).
	// Metadata lines -- last-prompt, mode, ai-title, permission-mode and friends,
	// 15.2% of lines in a real sample -- carry neither a timestamp nor a uuid, and
	// filling either from the clock makes the key differ every time the same line is
	// read. A rescan then inserts a second row for a line already stored (#57).
	//
	// So both come from the line itself when the line does not supply them: the
	// timestamp is pinned to the zero instant and the identity is a digest of the
	// content. Re-reading the same bytes lands on the same row.
	ts := r.Timestamp
	derivedUUID := r.UUID
	if ts.IsZero() || derivedUUID == "" {
		// The digest reads the original line, not the re-marshalled record: the
		// struct drops fields it has no home for, so two different metadata lines
		// marshal back to the same bytes.
		identityBytes := r.RawLine
		if len(identityBytes) == 0 {
			identityBytes = raw
		}
		digest := sha256.Sum256(append([]byte(r.SessionID+"\x00"), identityBytes...))
		if derivedUUID == "" {
			derivedUUID = "content-" + hex.EncodeToString(digest[:16])
		}
		if ts.IsZero() {
			// Not time.Now(): the same line must produce the same key on every read.
			ts = time.Unix(0, 0).UTC()
		}
	}

	sr := &store.SessionRecord{
		Ts:               ts,
		SessionID:        r.SessionID,
		ProjectHash:      projectHash,
		RecordType:       r.Type,
		ProfileEmail:     profileEmail,
		UserID:           userID,
		Raw:              raw,
		UUID:             derivedUUID,
		ParentUUID:       r.ParentUUID,
		IsSidechain:      r.IsSidechain,
		AgentID:          r.AgentID,
		IsCompactSummary: r.IsCompactSummary,
		IsMeta:           r.IsMeta,
		PromptSource:     r.PromptSource,
		Entrypoint:       r.Entrypoint,
	}
	if r.ForkedFrom != nil {
		sr.ForkedFromSession = r.ForkedFrom.SessionID
		sr.ForkedFromUUID = r.ForkedFrom.MessageUUID
	}

	// Where the name came from is also how it was invoked. A <command-name> line
	// exists because somebody typed a slash command. An attributionSkill is written
	// by Claude Code when a skill is running that nobody typed -- the model matched
	// it to the work. Reading the Skill tool_use as well would count that same
	// invocation twice: every one of them also carries the attribution (#57).
	if len(commandOverride) > 0 && commandOverride[0] != "" {
		sr.CommandName = commandOverride[0]
		sr.CommandInvoke = "implicit"
	} else if r.Type == "user" {
		if cmd := r.ParseCommandName(); cmd != "" {
			sr.CommandName = cmd
			sr.CommandInvoke = "explicit"
		}
	}

	// Extract model and token data from assistant records
	if r.Type == "assistant" {
		if am := r.ParseAssistantMessage(); am != nil {
			sr.Model = am.Model
			if am.Usage.InputTokens > 0 {
				v := am.Usage.InputTokens
				sr.InputTokens = &v
			}
			if am.Usage.OutputTokens > 0 {
				v := am.Usage.OutputTokens
				sr.OutputTokens = &v
			}
			if am.Usage.CacheReadInputTokens > 0 {
				v := am.Usage.CacheReadInputTokens
				sr.CacheReadTokens = &v
			}
			if am.Usage.CacheCreationInputTokens > 0 {
				v := am.Usage.CacheCreationInputTokens
				sr.CacheCreateTokens = &v
			}
		}
	}

	return sr
}

// classifyCommand records what the slash command was, resolved against this
// machine's directories while they are still the ones that ran it.
//
// The server cannot repeat this: plugins are upgraded and removed, and a name alone
// does not say whether it was a builtin, a project command, or a plugin's skill.
func (s *Syncer) classifyCommand(sr *store.SessionRecord, cwd string) {
	if sr == nil || sr.CommandName == "" {
		return
	}
	c := s.cmdClass.Classify(cwd, sr.CommandName)
	sr.CommandSource = string(c.Source)
	sr.CommandKind = string(c.Kind)
}
