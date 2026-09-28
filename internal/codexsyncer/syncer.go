package codexsyncer

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cctrace/internal/codexauth"
	"cctrace/internal/codexlog"
	"cctrace/internal/gitctx"
	"cctrace/internal/projecthash"
	"cctrace/internal/projectrule"
	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

const ruleScanTTL = 30 * time.Second

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
	var re *syncer.RetryableError
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

// CodexSyncer scans Codex JSONL session files and sends records to the cctrace server.
type CodexSyncer struct {
	// codexDirs lists every Codex home directory to scan for session JSONL files.
	codexDirs    []string
	profileEmail string
	userID       string
	state        *syncer.State
	client       *syncer.Client
	// collectPrefixes, when non-empty, restricts syncing to repositories whose
	// normalized id starts with one of these prefixes. Empty collects all.
	collectPrefixes []string
	rulesSkipUntil  map[string]time.Time
	// rulesUnsupported is set once the server reports no project-rules endpoint,
	// so we stop scanning/sending (and logging) for the rest of the session.
	rulesUnsupported bool
	// quotaUnsupported latches once an older server has answered 404 for the
	// quota history route, so it is asked once rather than every pass.
	quotaUnsupported bool

	// lastScanSummary holds the previous per-home file-count summary so watch
	// mode logs it only when it changes, instead of every poll interval.
	lastScanSummary string
	// lastPassSummary throttles the end-of-pass summary the same way.
	lastPassSummary string
	// passLagging counts files that grew on disk but whose scan advanced the
	// offset by zero bytes during the current pass; passLagSample keeps the
	// first such file for the summary line. A file stuck here is invisible
	// otherwise: it produces no error and no state write, so collection simply
	// stops for it while everything else looks healthy.
	passLagging   int
	passLagSample string
	// shrunkFiles marks files currently found shrunk below their stored offset,
	// so the "stalled" log fires once per stall episode instead of every pass
	// (this state is deliberately not rewound - see
	// TestCodexSyncer_SyncOnce_DoesNotRewindShrunkFile - so without suppression
	// it would repeat on every poll forever). Cleared once the file's size
	// catches back up to the offset.
	shrunkFiles map[string]bool
	// accounts drops the records of excluded accounts before they are sent.
	accounts *syncer.AccountFilter
}

// New creates a new CodexSyncer. codexDirs lists the Codex home directories to
// scan; files found in them are merged into a single set. collectPrefixes
// restricts which repositories are synced; pass nil to collect every repository.
func New(codexDirs []string, profileEmail, userID string, state *syncer.State, client *syncer.Client, collectPrefixes []string) *CodexSyncer {
	return &CodexSyncer{
		codexDirs:       codexDirs,
		profileEmail:    profileEmail,
		userID:          userID,
		state:           state,
		client:          client,
		collectPrefixes: collectPrefixes,
		rulesSkipUntil:  make(map[string]time.Time),
		shrunkFiles:     make(map[string]bool),
		accounts:        syncer.NewAccountFilter(client, state),
	}
}

// findAllFiles returns the union of session JSONL files across every configured
// Codex home, deduplicated by absolute path. A home that fails to scan is
// logged and skipped so one broken home cannot stop the whole pass.
func (s *CodexSyncer) findAllFiles() ([]string, error) {
	seen := make(map[string]struct{})
	var all []string
	parts := make([]string, 0, len(s.codexDirs))
	for _, dir := range s.codexDirs {
		files, err := codexlog.FindJSONLFiles(dir)
		if err != nil {
			log.Printf("[codex-syncer] scan %s: %v", dir, err)
			parts = append(parts, fmt.Sprintf("%s=error", dir))
			continue
		}
		added := 0
		for _, f := range files {
			if _, ok := seen[f]; ok {
				continue
			}
			seen[f] = struct{}{}
			all = append(all, f)
			added++
		}
		parts = append(parts, fmt.Sprintf("%s=%d", dir, added))
	}

	// Log which homes were scanned and how many files each contributed. Without
	// this a misconfigured or unreachable home is indistinguishable from one
	// that simply has no new sessions. Only log on change so watch mode does not
	// repeat the same line every poll interval.
	summary := fmt.Sprintf("%d home(s), %d file(s): %s", len(s.codexDirs), len(all), strings.Join(parts, ", "))
	if summary != s.lastScanSummary {
		log.Printf("[codex-syncer] scan %s", summary)
		s.lastScanSummary = summary
	}
	return all, nil
}

func (s *CodexSyncer) SyncOnce(ctx context.Context) (int, error) {
	files, err := s.findAllFiles()
	if err != nil {
		return 0, fmt.Errorf("find codex jsonl files: %w", err)
	}

	// Capture the first-run flag once for the whole pass. The first Save() clears
	// state.IsNew(), so re-reading it per file would skip only the first file and
	// backfill the rest of the history. Read it before observeAccount, which may
	// save.
	firstRun := s.state.IsNew()
	if firstRun {
		// See the Claude-side note: without this the window never closes on a pass
		// that records nothing, and the first rollout to appear afterwards is
		// skipped whole. Codex is the likelier victim — enabling the integration is
		// itself the first run, so turning it on before starting codex loses the
		// opening session entirely.
		if err := s.state.Save(); err != nil {
			log.Printf("[codex-syncer] failed to save state: %v", err)
		}
	}
	s.observeAccount()
	s.accounts.BeginPass()
	total := 0
	s.passLagging = 0
	s.passLagSample = ""
	for i, file := range files {
		// Stop promptly when the watch context is cancelled (graceful stop),
		// rather than scanning every remaining file first. Log where the pass
		// stopped: a pass that consistently ends early leaves later homes in the
		// scan order permanently unsynced, which is silent without this line.
		if err := ctx.Err(); err != nil {
			log.Printf("[codex-syncer] pass stopped before file %d/%d: %v", i+1, len(files), err)
			return total, err
		}
		n, err := s.syncFile(ctx, file, firstRun)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				log.Printf("[codex-syncer] pass stopped at file %d/%d (%s): %v", i+1, len(files), file, ctxErr)
				return total, ctxErr
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				log.Printf("[codex-syncer] pass stopped at file %d/%d (%s): %v", i+1, len(files), file, err)
				return total, err
			}
			log.Printf("[codex-syncer] %s: %v", file, err)
			continue
		}
		total += n
	}

	// End-of-pass summary. Throttled to changes so watch mode does not repeat it
	// every poll, but a stuck file keeps `lagging` non-zero and therefore stays
	// visible once the count or the sample changes.
	summary := fmt.Sprintf("files=%d synced=%d lagging=%d", len(files), total, s.passLagging)
	if s.passLagSample != "" {
		summary += " | " + s.passLagSample
	}
	if summary != s.lastPassSummary {
		log.Printf("[codex-syncer] pass %s", summary)
		s.lastPassSummary = summary
	}
	return total, nil
}

// observeAccount records which Codex billing account is active now for every
// configured home, once per pass rather than per file. The value comes from
// auth.json, whose account_id survives token refreshes unchanged, so only a
// real account switch appends a new observation — the file's mtime says nothing
// and is never consulted.
//
// Every home is read, not just the first one that is logged in: homes are
// separate logins (CODEX_HOME is added on top of the default home, see
// codexlog.ResolveScanDirs), so stopping at the first would stamp every home's
// records with one home's account.
func (s *CodexSyncer) observeAccount() {
	changed := false
	for _, dir := range s.codexDirs {
		accountID, err := codexauth.ReadAccountID(dir)
		if err != nil {
			log.Printf("[codex-syncer] read account id from %s: %v", dir, err)
			continue
		}
		if s.state.ObserveCodexAccount(time.Now(), normalizeHome(dir), accountID) {
			changed = true
		}
	}
	if changed {
		if err := s.state.Save(); err != nil {
			log.Printf("[codex-syncer] save state: %v", err)
		}
	}
}

// normalizeHome returns the absolute, cleaned form of a configured Codex home.
// Observations are keyed by it and file paths are matched against it, so both
// sides must agree on one spelling of the same directory.
func normalizeHome(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	return abs
}

// homeForFile returns the configured Codex home that filePath lives under, or
// "" when it belongs to none. The longest match wins so a home nested inside
// another is not swallowed by it. A file with no home is left unattributed
// rather than billed to a guess.
func (s *CodexSyncer) homeForFile(filePath string) string {
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return ""
	}
	best := ""
	for _, dir := range s.codexDirs {
		home := normalizeHome(dir)
		if !strings.HasPrefix(abs, home+string(filepath.Separator)) {
			continue
		}
		if len(home) > len(best) {
			best = home
		}
	}
	return best
}

// resetIfShrunk moves a shrunk file's offset down to the file's current size
// and returns the offset to scan from.
//
// Not rewound to zero: that would re-read bytes already sent and duplicate the
// metadata rows a session file carries. Not left in place either, which is what
// stranded later growth — the scan seeks past EOF and returns nothing, every
// pass, until the file grows back past the stored offset. For a session
// rewritten down to a metadata remnant and then resumed, that is the whole
// resumed conversation.
//
// The bytes still on disk were either already sent (a truncation, where they
// are the prefix of what was sent) or are a rewrite remnant whose rows reached
// the server before the rewrite. So the current size is the correct resume
// point. Logged once per episode so the reset stays visible.
//
// Called before the scan, not after: callers persist the post-scan offset on
// paths this reset would otherwise be overwritten by.
func (s *CodexSyncer) resetIfShrunk(filePath string, offset int64) int64 {
	fi, err := os.Stat(filePath)
	if err != nil || fi.Size() >= offset {
		return offset
	}
	if !s.shrunkFiles[filePath] {
		s.shrunkFiles[filePath] = true
		log.Printf("[codex-syncer] %s: shrunk, offset reset %d -> %d (%d bytes no longer on disk)", filePath, offset, fi.Size(), offset-fi.Size())
	}
	s.state.SetOffset(filePath, fi.Size())
	// The bytes the ledger described are not on disk any more, so what it says
	// about the prefix ending at the new offset is unknown. An unknown ledger is
	// worse than none: it would nudge past records that may no longer exist.
	s.state.SetConflictTail(filePath, nil)
	if err := s.state.Save(); err != nil {
		log.Printf("[codex-syncer] failed to save state: %v", err)
	}
	return fi.Size()
}

// noteScanProgress records files whose scan advanced the offset by zero bytes
// this pass. Two shapes are distinguished: a file whose on-disk size exceeds
// the offset (bytes are waiting but the pass consumed none of them - no error,
// no state written, so it silently stops being collected while the daemon
// keeps looking busy) is counted as "lagging"; a file whose on-disk size has
// dropped below the offset (shrunk) is logged as deliberately stalled instead,
// since rewinding it is unsafe (see TestCodexSyncer_SyncOnce_DoesNotRewindShrunkFile).
func (s *CodexSyncer) noteScanProgress(filePath string, offset, newOffset int64, records int) {
	if newOffset != offset {
		return
	}
	fi, err := os.Stat(filePath)
	if err != nil {
		return
	}

	// A shrunk file has its offset reset to the file's current size, not rewound
	// to zero and not left where it was. Rewinding would re-send bytes already
	// collected (see TestCodexSyncer_SyncOnce_DoesNotRewindShrunkFile);
	// leaving the offset above the size strands everything written afterwards
	// until the file grows back past it. The bytes still on disk were either
	// already sent or are a rewrite remnant, so the current size is the correct
	// resume point. Logged once per episode so the reset stays visible.
	// A shrunk file was already reset before the scan (see resetIfShrunk), so
	// reaching here with a size below the offset is not expected.
	if fi.Size() < offset {
		return
	}
	delete(s.shrunkFiles, filePath)

	if fi.Size() <= offset {
		return
	}
	s.passLagging++
	if s.passLagSample == "" {
		s.passLagSample = fmt.Sprintf("no progress: %s offset=%d size=%d lag=%d records=%d",
			filePath, offset, fi.Size(), fi.Size()-offset, records)
	}
}

func (s *CodexSyncer) syncFile(ctx context.Context, filePath string, firstRun bool) (int, error) {
	// On first run (new state), skip existing files to EOF.
	//
	// Codex has no SessionStart hook, so enabling the integration is itself the
	// first run and every rollout already on disk is skipped whole. `cctrace sync`
	// then reports "Synced 0 records" against a dashboard that stays empty, with
	// nothing to explain it. The Claude-side counterpart in internal/syncer carries
	// the shared reasoning: there the loss is partial and therefore quieter, but
	// the silence is the same defect.
	if !s.state.HasFile(filePath) && firstRun {
		fi, err := os.Stat(filePath)
		if err == nil {
			meta, err := codexlog.ScanMetadataContext(ctx, filePath, fi.Size())
			if err != nil {
				return 0, err
			}
			setStateFileMetadata(s.state, filePath, fi.Size(), meta)
			if fi.Size() > 0 {
				// See the Claude-side note: the log copy of this fact rotates away,
				// the state copy is what `cctrace status` can still read later.
				s.state.Files[filePath].SkippedAtFirstSync = fi.Size()
			}
			_ = s.state.Save()
			if fi.Size() > 0 {
				log.Printf("[codex-syncer] %s: skipped (pre-existing at first sync — earlier content is not collected) bytes=%d", filePath, fi.Size())
			}
		}
		return 0, nil
	}

	sessionID := codexlog.SessionIDFromPath(filePath)
	offset := s.state.GetOffset(filePath)
	offset = s.resetIfShrunk(filePath, offset)
	// Asked before the scan, while offset still names the prefix the ledger
	// claims to describe. An untrusted answer is not an error: it means this
	// file counts from zero this pass and records nothing, the way it did
	// before the ledger existed.
	seed, ledgerTrusted := s.state.ConflictSeed(filePath, offset)
	meta := s.fileMetadata(filePath)
	metadataUpdated := false
	// Existing sync-state files predate fork metadata. Rescan their consumed
	// prefix once so a growing fork does not resume mid-history without its gate.
	if offset > 0 && (meta.CWD == "" || meta.Model == "" || !meta.TokenUsageScanned || !meta.ForkMetadataScanned) {
		if scanned, err := codexlog.ScanMetadataContext(ctx, filePath, offset); err == nil {
			old := meta
			meta = mergeMetadata(meta, scanned)
			metadataUpdated = metadataChanged(old, meta)
		}
	}
	records, rateLimits, newOffset, scanMeta, err := codexlog.ScanFileWithSamplesContext(ctx, filePath, offset, sessionID, meta)
	if err != nil {
		return 0, err
	}
	s.noteScanProgress(filePath, offset, newOffset, len(records))

	// Sent here, ahead of every early return below, because a rate-limit
	// reading is account-scoped and none of those returns are about the
	// account. A file whose new bytes hold only a token_count would otherwise
	// exit at the len(records)==0 branch with the reading discarded, and the
	// repository allowlist further down is about session content — the 5h and
	// 7d windows are global to the account, so filtering them by repository
	// could not hide anything and would only make the series sparser and the
	// weighted average quietly wrong.
	// Rewound rather than handled at each save site: the offset is written in
	// four places below and a fifth would be added without this one being
	// remembered. Holding newOffset at the old value makes every one of them
	// re-read the same bytes on the next pass, which is exactly what the session
	// send already gets by returning before its save. The records in the range
	// are re-sent and deduplicated server-side -- the price a failed session send
	// has always paid, now paid for the reading that only these bytes carry.
	if err := s.sendQuotaSamples(ctx, s.homeForFile(filePath), filePath, rateLimits); err != nil {
		newOffset = offset
	}

	meta = mergeMetadata(meta, scanMeta)
	cwd, model := meta.CWD, meta.Model
	// Built before the no-records return so that return can carry the ledger
	// across the bytes it consumed. Those bytes held nothing to count, so the
	// counts are unchanged -- but the offset they describe is not, and a ledger
	// left pointing at the old one stops matching on the next pass.
	nudger := syncer.NewConflictNudger(seed)
	if len(records) == 0 {
		if cwd != "" {
			projectHash := projecthash.FromPath(cwd)
			if projectHash == "" {
				projectHash = projectHashFromPath(filePath)
			}
			gitMeta := gitctx.Resolve(cwd)
			if err := s.sendProjectRules(ctx, projectHash, projecthash.NameFromPath(cwd), gitMeta); err != nil {
				log.Printf("[codex-syncer] project rules: %v", err)
			}
		}
		if newOffset != offset || metadataUpdated {
			s.state.AdvanceConflictTail(filePath, offset, newOffset, ledgerTrusted, nudger)
			setStateFileMetadata(s.state, filePath, newOffset, meta)
			_ = s.state.Save()
		}
		return 0, nil
	}

	storeRecords := make([]*store.SessionRecord, 0, len(records))
	sourceFile := filepath.Base(filePath)
	home := s.homeForFile(filePath)
	for _, r := range records {
		if r.CWD != "" {
			cwd = r.CWD
		}
		if r.Model != "" {
			model = r.Model
		}
		if r.CWD == "" {
			r.CWD = cwd
		}
		if r.Model == "" {
			r.Model = model
		}
		sr := toStoreRecord(r, s.profileEmail, s.userID, sessionID, sourceFile, entrypointFromOriginator(meta.Originator, meta.IsSubagentFork))
		if sr != nil {
			// Attribute by the record's own home and timestamp: a record
			// written before its home's account was first observed stays empty
			// rather than inheriting today's account or another home's.
			sr.AccountID = s.state.CodexAccountAt(home, sr.Ts)
			nudger.Apply(sr)
			storeRecords = append(storeRecords, sr)
		}
	}

	if len(storeRecords) == 0 {
		meta.CWD, meta.Model = cwd, model
		s.state.AdvanceConflictTail(filePath, offset, newOffset, ledgerTrusted, nudger)
		setStateFileMetadata(s.state, filePath, newOffset, meta)
		_ = s.state.Save()
		return 0, nil
	}

	projectHash := projecthash.FromPath(cwd)
	if projectHash == "" {
		projectHash = projectHashFromPath(filePath)
	}
	projectName := projecthash.NameFromPath(cwd)
	gitMeta := gitctx.Resolve(cwd)

	// Drop repositories outside the configured allowlist before sending. Advance
	// the offset so the dropped records are not rescanned every pass.
	if !gitctx.AllowsRepository(gitMeta.RepositoryID, s.collectPrefixes) {
		meta.CWD, meta.Model = cwd, model
		// The ledger tracks the consumed prefix, not what was sent: records
		// dropped here still occupy the keys a later same-timestamp record has
		// to be nudged past, exactly as a from-zero rescan would count them.
		s.state.AdvanceConflictTail(filePath, offset, newOffset, ledgerTrusted, nudger)
		setStateFileMetadata(s.state, filePath, newOffset, meta)
		_ = s.state.Save()
		return 0, nil
	}

	// An excluded account's records are consumed like the allowlist drop above:
	// the offset and the ledger move past them, so they are neither retried
	// every pass nor left in front of the records that follow them.
	storeRecords, err = s.accounts.Drop(ctx, "openai", storeRecords)
	if err != nil {
		return 0, err
	}
	if len(storeRecords) == 0 {
		meta.CWD, meta.Model = cwd, model
		s.state.AdvanceConflictTail(filePath, offset, newOffset, ledgerTrusted, nudger)
		setStateFileMetadata(s.state, filePath, newOffset, meta)
		_ = s.state.Save()
		return 0, nil
	}

	if _, err := s.client.Send(ctx, "codex", s.profileEmail, s.userID, projectHash, projectName,
		syncer.ProjectIdentity{
			GitRemoteURL:       gitMeta.GitRemoteURL,
			RepositoryID:       gitMeta.RepositoryID,
			RepositoryIDSource: gitMeta.RepositoryIDSource,
			RepositoryName:     gitMeta.RepositoryName,
			RepoSubpath:        gitMeta.RepoSubpath,
			RepoSubpathPresent: gitMeta.RepoSubpathPresent,
			CommitSHA:          gitMeta.CommitSHA,
			Branch:             gitMeta.Branch,
		}, storeRecords); err != nil {
		return 0, err
	}
	if err := s.sendProjectRules(ctx, projectHash, projectName, gitMeta); err != nil {
		log.Printf("[codex-syncer] project rules: %v", err)
	}

	meta.CWD, meta.Model = cwd, model
	s.state.AdvanceConflictTail(filePath, offset, newOffset, ledgerTrusted, nudger)
	setStateFileMetadata(s.state, filePath, newOffset, meta)
	if err := s.state.Save(); err != nil {
		log.Printf("[codex-syncer] save state: %v", err)
	}
	return len(storeRecords), nil
}

func (s *CodexSyncer) sendProjectRules(ctx context.Context, projectHash, projectName string, gitMeta gitctx.Context) error {
	if !gitctx.AllowsRepository(gitMeta.RepositoryID, s.collectPrefixes) {
		return nil
	}
	// Only a real git repository root is scannable. Falling back to the session
	// cwd promoted non-repo directories to scan roots: real Codex sessions carry
	// cwds like "/" and "$HOME", which walk an unbounded tree (the daemon was
	// found parked inside exactly such a walk) and merge unrelated repositories'
	// rule files under one project hash.
	root := strings.TrimSpace(gitMeta.RepositoryRoot)
	if root == "" {
		return nil
	}
	repositoryKey := firstNonEmpty(gitMeta.RepositoryID, projectHash)
	if repositoryKey == "" {
		return nil
	}
	if s.rulesUnsupported {
		return nil
	}
	cacheKey := "codex|" + repositoryKey + "|" + root
	if until, ok := s.rulesSkipUntil[cacheKey]; ok && nowFn().Before(until) {
		return nil
	}

	scanCtx, cancel := context.WithTimeout(ctx, projectRuleScanTimeout)
	defer cancel()
	rules, err := projectrule.RunBounded(scanCtx, projectrule.ScanOptions{
		Agent:              projectrule.AgentCodex,
		RepositoryRoot:     root,
		IncludeMissingRoot: true,
	}, projectRuleScan)
	if err != nil {
		// Park the repository on the existing per-repository TTL whatever the
		// failure was. Every session file in that repo hits this path, so without
		// suppression one bad tree burns the same doomed walk thousands of times
		// and the pass still never finishes. A stalled mount fails the root stat
		// with EIO/ETIMEDOUT, not a context error, so keying the suppression on
		// context errors alone left the most expensive case unparked.
		s.rulesSkipUntil[cacheKey] = nowFn().Add(ruleScanTTL)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return fmt.Errorf("scan %s timed out after %s; skipping until TTL expires: %w", root, projectRuleScanTimeout, err)
		}
		return fmt.Errorf("scan %s failed; skipping until TTL expires: %w", root, err)
	}
	if len(rules) == 0 {
		// The walk still traversed the whole tree to find nothing; park it too, or
		// a rule-less repository re-walks itself for every session file it owns.
		s.rulesSkipUntil[cacheKey] = nowFn().Add(ruleScanTTL)
		return nil
	}

	_, err = s.client.SendProjectRules(ctx, &store.ProjectRuleIngestRequest{
		ProfileEmail:   s.profileEmail,
		UserID:         s.userID,
		Agent:          projectrule.AgentCodex,
		ProjectHash:    projectHash,
		ProjectName:    projectName,
		RepositoryID:   gitMeta.RepositoryID,
		RepositoryKey:  repositoryKey,
		RepositoryName: gitMeta.RepositoryName,
		CommitSHA:      gitMeta.CommitSHA,
		Branch:         gitMeta.Branch,
		Rules:          rules,
	})
	if err != nil {
		if errors.Is(err, syncer.ErrProjectRulesUnsupported) {
			s.rulesUnsupported = true
			log.Printf("[codexsyncer] project rules: server has no /api/project-rules endpoint; disabling rule sync for this session")
			return nil
		}
		if errors.Is(err, syncer.ErrProjectRulesForbidden) {
			// syncer.RuleForbiddenTTL, not ruleScanTTL. Access denied is durable
			// -- the server decides it from session history, which does not
			// appear because we asked again 30 seconds later. On the scan TTL
			// this posted 23k-41k refused requests a day and filled sync.log
			// with the one line that crowds out the update errors a diagnosis
			// needs (#619, and #458 for the same failure on the Claude path).
			s.rulesSkipUntil[cacheKey] = nowFn().Add(syncer.RuleForbiddenTTL)
			// Recorded, not just parked. The park stops the retrying and dies with
			// the process; this is what survives to tell a person that the
			// repository's rules are not being collected at all.
			if s.state.NoteRulesDenied(repositoryKey, nowFn()) {
				_ = s.state.Save()
			}
			log.Printf("[codexsyncer] project rules: access denied for %s; skipping for %s", repositoryKey, syncer.RuleForbiddenTTL)
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
		s.rulesSkipUntil[cacheKey] = nowFn().Add(ruleParkDelay(err))
		return err
	}
	s.rulesSkipUntil[cacheKey] = nowFn().Add(ruleScanTTL)
	if s.state.ClearRulesDenied(repositoryKey) {
		_ = s.state.Save()
	}
	return nil
}

func (s *CodexSyncer) fileMetadata(filePath string) codexlog.Metadata {
	if fs := s.state.Files[filePath]; fs != nil {
		return codexlog.Metadata{
			CWD:                    fs.CWD,
			Model:                  fs.Model,
			TokenUsageScanned:      fs.TokenUsageScanned,
			HasTotalTokenUsage:     fs.HasTotalTokenUsage,
			TotalInputTokens:       fs.TotalInputTokens,
			TotalCachedInputTokens: fs.TotalCachedInputTokens,
			TotalOutputTokens:      fs.TotalOutputTokens,
			ForkMetadataScanned:    fs.CodexForkMetadataScanned,
			IsSubagentFork:         fs.CodexSubagentFork,
			ForkHistoryCopied:      fs.CodexForkHistoryCopied,
			ForkBoundaryReached:    fs.CodexForkBoundaryReached,
			ForkHasTriggerTurn:     fs.CodexForkHasTriggerTurn,
			HasTokenUsageRecord:    fs.CodexHasTokenUsageRecord,
		}
	}
	return codexlog.Metadata{}
}

func setStateFileMetadata(state *syncer.State, filePath string, offset int64, meta codexlog.Metadata) {
	state.SetOffsetWithMetadata(filePath, offset, meta.CWD, meta.Model)
	if fs := state.Files[filePath]; fs != nil {
		fs.TokenUsageScanned = meta.TokenUsageScanned
		fs.HasTotalTokenUsage = meta.HasTotalTokenUsage
		fs.TotalInputTokens = meta.TotalInputTokens
		fs.TotalCachedInputTokens = meta.TotalCachedInputTokens
		fs.TotalOutputTokens = meta.TotalOutputTokens
		fs.CodexForkMetadataScanned = meta.ForkMetadataScanned
		fs.CodexSubagentFork = meta.IsSubagentFork
		fs.CodexForkHistoryCopied = meta.ForkHistoryCopied
		fs.CodexForkBoundaryReached = meta.ForkBoundaryReached
		fs.CodexForkHasTriggerTurn = meta.ForkHasTriggerTurn
		fs.CodexHasTokenUsageRecord = meta.HasTokenUsageRecord
	}
}

func mergeMetadata(base, next codexlog.Metadata) codexlog.Metadata {
	if next.CWD != "" {
		base.CWD = next.CWD
	}
	if next.Model != "" {
		base.Model = next.Model
	}
	if next.Originator != "" {
		base.Originator = next.Originator
	}
	if next.TokenUsageScanned {
		base.TokenUsageScanned = true
		base.HasTotalTokenUsage = next.HasTotalTokenUsage
		base.TotalInputTokens = next.TotalInputTokens
		base.TotalCachedInputTokens = next.TotalCachedInputTokens
		base.TotalOutputTokens = next.TotalOutputTokens
	}
	if next.ForkMetadataScanned {
		base.ForkMetadataScanned = true
	}
	if next.IsSubagentFork {
		base.IsSubagentFork = true
	}
	if next.ForkHistoryCopied {
		base.ForkHistoryCopied = true
	}
	if next.ForkBoundaryReached {
		base.ForkBoundaryReached = true
	}
	if next.ForkHasTriggerTurn {
		base.ForkHasTriggerTurn = true
	}
	// Sticky, like the fork flags above: a later pass that reads a tail with no
	// usage record must not unlearn that the file has them.
	if next.HasTokenUsageRecord {
		base.HasTokenUsageRecord = true
	}
	return base
}

func metadataChanged(a, b codexlog.Metadata) bool {
	return a.CWD != b.CWD ||
		a.Model != b.Model ||
		a.TokenUsageScanned != b.TokenUsageScanned ||
		a.HasTotalTokenUsage != b.HasTotalTokenUsage ||
		a.TotalInputTokens != b.TotalInputTokens ||
		a.TotalCachedInputTokens != b.TotalCachedInputTokens ||
		a.TotalOutputTokens != b.TotalOutputTokens ||
		a.ForkMetadataScanned != b.ForkMetadataScanned ||
		a.IsSubagentFork != b.IsSubagentFork ||
		a.ForkHistoryCopied != b.ForkHistoryCopied ||
		a.ForkBoundaryReached != b.ForkBoundaryReached ||
		a.ForkHasTriggerTurn != b.ForkHasTriggerTurn ||
		a.HasTokenUsageRecord != b.HasTokenUsageRecord
}

// toStoreRecord takes sourceFile as a parameter rather than letting callers fill
// SessionRecord.SourceFile afterwards: the realtime path once silently skipped
// that step while reenrich did it, so the compiler now forces every call site.
//
// UUID/ParentUUID/IsSidechain/AgentID stay empty on purpose — codexlog.Record has
// no such fields. Codex JSONL is a rollout event stream with no message lineage,
// so populating them would require a new lineage model, not a mapping change.
// entrypointFromOriginator maps Codex's originator onto the entrypoint vocabulary the
// session classifier reads. Only the human TUI becomes "cli"; every other launcher —
// codex exec, the TS SDK, or a delegating harness such as Claude Code — keeps its own
// name, so an unrecognised originator is never mistaken for a human turn. An empty
// originator (pre-originator rollouts) stays empty and falls to the legacy path.
//
// subagentFork overrides all of it. A rollout the model forked off a session inherits
// that session's originator verbatim — measured on a real run, three subagent rollouts
// all carried {"originator":"codex-tui","thread_source":"subagent"} — so originator
// cannot distinguish "a person typed this" from "the model spawned this". Without the
// override every subagent thread lands on "cli" and joins the sessions a human had,
// which is the opposite of what the classifier exists to do. The override also beats
// the empty case: a rollout we know an agent spawned must not inherit the benefit of
// the doubt reserved for files predating the originator field.
func entrypointFromOriginator(originator string, subagentFork bool) string {
	if subagentFork {
		return "codex-subagent"
	}
	if originator == "" {
		return ""
	}
	if originator == "codex-tui" {
		return "cli"
	}
	return strings.ReplaceAll(strings.ToLower(originator), " ", "-")
}

func toStoreRecord(r *codexlog.Record, profileEmail, userID, sessionID, sourceFile, entrypoint string) *store.SessionRecord {
	if r.RecordType == "" {
		return nil
	}
	toolName, toolCallID := "", ""
	if r.RecordType == "tool_call" || r.RecordType == "tool_output" {
		toolName, toolCallID = r.ToolName, r.CallID
	}
	return &store.SessionRecord{
		Ts:              r.Timestamp,
		SourceFile:      sourceFile,
		Entrypoint:      entrypoint,
		SessionID:       sessionID,
		RecordType:      r.RecordType,
		ProfileEmail:    profileEmail,
		UserID:          userID,
		Model:           r.Model,
		Agent:           "codex",
		BillingProvider: "openai",
		InputTokens:     r.InputTokens,
		OutputTokens:    r.OutputTokens,
		CacheReadTokens: r.CacheReadTokens,
		Raw:             r.Raw,
		CommandName:     parseSkillName(r),
		ToolName:        toolName,
		ToolCallID:      toolCallID,
	}
}

func parseSkillName(r *codexlog.Record) string {
	if r.RecordType != "user" || r.Content == "" || !strings.Contains(r.Content, "<skill") {
		return ""
	}
	start := strings.Index(r.Content, "<name>")
	end := strings.Index(r.Content, "</name>")
	if start == -1 || end == -1 || end <= start {
		return ""
	}
	name := strings.TrimSpace(r.Content[start+len("<name>") : end])
	return strings.TrimPrefix(name, "/")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// projectHashFromPath is a fallback for old-format files that have no CWD.
func projectHashFromPath(filePath string) string {
	h := sha256.Sum256([]byte(filePath))
	return fmt.Sprintf("codex-%x", h[:8])
}
