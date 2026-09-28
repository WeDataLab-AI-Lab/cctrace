// Package omosyncer scans omo (senpi) session JSONL logs and sends the
// records they carry to the cctrace server. It mirrors internal/codexsyncer's
// structure; see that package for the shared reasoning behind the first-run
// and progress-tracking mechanics repeated here.
package omosyncer

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"cctrace/internal/gitctx"
	"cctrace/internal/omolog"
	"cctrace/internal/projecthash"
	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

// OmoSyncer scans omo session JSONL files and sends records to the cctrace server.
type OmoSyncer struct {
	// omoDirs lists every omo (senpi) home directory to scan for session JSONL
	// files: normally just omolog.DefaultOmoDir(), plus whatever
	// options.omo_dirs adds. It is a list rather than a single path because omo
	// already keeps sessions under more than one tree, and the one time a root
	// went unscanned it hid 56 records / 7.26M tokens (#247, #250) while every
	// screen still looked healthy.
	omoDirs      []string
	profileEmail string
	userID       string
	state        *syncer.State
	client       *syncer.Client
	// collectPrefixes, when non-empty, restricts syncing to repositories whose
	// normalized id starts with one of these prefixes. Empty collects all.
	collectPrefixes []string
	// lastScanSummary holds the previous file-count summary so watch mode logs
	// it only when it changes, instead of every poll interval.
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
	// shrunkFiles marks files currently found shrunk below their stored
	// offset, so the "stalled" log fires once per stall episode instead of
	// every pass. Cleared once the file's size catches back up to the offset.
	shrunkFiles map[string]bool
}

// New creates a new OmoSyncer. omoDirs lists the omo home directories to scan.
// collectPrefixes restricts which repositories are synced; pass nil to
// collect every repository.
func New(omoDirs []string, profileEmail, userID string, state *syncer.State, client *syncer.Client, collectPrefixes []string) *OmoSyncer {
	return &OmoSyncer{
		omoDirs:         omoDirs,
		profileEmail:    profileEmail,
		userID:          userID,
		state:           state,
		client:          client,
		collectPrefixes: collectPrefixes,
		shrunkFiles:     make(map[string]bool),
	}
}

// DefaultStatePath returns ~/.cctrace/omo-sync-state.json
func DefaultStatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cctrace", "omo-sync-state.json")
}

// findAllFiles returns the session files across every configured omo home,
// deduplicated by path. A home that fails to scan is logged and skipped rather
// than returned as an error: a configured dir may sit on a volume that is not
// mounted right now, and letting that one failure abort the pass would turn a
// partial gap into a total one.
func (s *OmoSyncer) findAllFiles() ([]string, error) {
	seen := make(map[string]struct{})
	var all []string
	parts := make([]string, 0, len(s.omoDirs))
	for _, dir := range s.omoDirs {
		added := 0
		files, err := omolog.FindSessionFiles(dir)
		if err != nil {
			log.Printf("[omo-syncer] scan %s: %v", dir, err)
			parts = append(parts, fmt.Sprintf("%s=skipped", dir))
			continue
		}
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

	// Log which homes were scanned and how many files each contributed. Only on
	// change, so watch mode does not repeat the line every poll interval.
	summary := fmt.Sprintf("%d home(s), %d file(s): %s", len(s.omoDirs), len(all), strings.Join(parts, ", "))
	if summary != s.lastScanSummary {
		log.Printf("[omo-syncer] scan %s", summary)
		s.lastScanSummary = summary
	}
	return all, nil
}

func (s *OmoSyncer) SyncOnce(ctx context.Context) (int, error) {
	files, err := s.findAllFiles()
	if err != nil {
		return 0, fmt.Errorf("find omo session files: %w", err)
	}

	// Capture the first-run flag once for the whole pass. The first Save()
	// clears state.IsNew(), so re-reading it per file would skip only the
	// first file and backfill the rest of the history. See
	// internal/codexsyncer/syncer.go:161-175: omo has no SessionStart-style
	// lifecycle hook either, so enabling the integration is itself the first
	// run, and without this Save on a zero-file pass the window never closes
	// and the first session created afterwards is skipped whole.
	firstRun := s.state.IsNew()
	if firstRun {
		if err := s.state.Save(); err != nil {
			log.Printf("[omo-syncer] failed to save state: %v", err)
		}
	}

	total := 0
	s.passLagging = 0
	s.passLagSample = ""
	for i, file := range files {
		// Stop promptly when the watch context is cancelled (graceful stop),
		// rather than scanning every remaining file first. Log where the pass
		// stopped: a pass that consistently ends early leaves later files in
		// the scan order permanently unsynced, which is silent without this
		// line.
		if err := ctx.Err(); err != nil {
			log.Printf("[omo-syncer] pass stopped before file %d/%d: %v", i+1, len(files), err)
			return total, err
		}
		n, err := s.syncFile(ctx, file, firstRun)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				log.Printf("[omo-syncer] pass stopped at file %d/%d (%s): %v", i+1, len(files), file, ctxErr)
				return total, ctxErr
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				log.Printf("[omo-syncer] pass stopped at file %d/%d (%s): %v", i+1, len(files), file, err)
				return total, err
			}
			log.Printf("[omo-syncer] %s: %v", file, err)
			continue
		}
		total += n
	}

	// End-of-pass summary. Throttled to changes so watch mode does not repeat
	// it every poll, but a stuck file keeps `lagging` non-zero and therefore
	// stays visible once the count or the sample changes.
	passSummary := fmt.Sprintf("files=%d synced=%d lagging=%d", len(files), total, s.passLagging)
	if s.passLagSample != "" {
		passSummary += " | " + s.passLagSample
	}
	if passSummary != s.lastPassSummary {
		log.Printf("[omo-syncer] pass %s", passSummary)
		s.lastPassSummary = passSummary
	}
	return total, nil
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
func (s *OmoSyncer) resetIfShrunk(filePath string, offset int64) int64 {
	fi, err := os.Stat(filePath)
	if err != nil || fi.Size() >= offset {
		return offset
	}
	if !s.shrunkFiles[filePath] {
		s.shrunkFiles[filePath] = true
		log.Printf("[omo-syncer] %s: shrunk, offset reset %d -> %d (%d bytes no longer on disk)", filePath, offset, fi.Size(), offset-fi.Size())
	}
	s.state.SetOffset(filePath, fi.Size())
	// The bytes the ledger described are not on disk any more, so what it says
	// about the prefix ending at the new offset is unknown. An unknown ledger is
	// worse than none: it would nudge past records that may no longer exist.
	s.state.SetConflictTail(filePath, nil)
	if err := s.state.Save(); err != nil {
		log.Printf("[omo-syncer] failed to save state: %v", err)
	}
	return fi.Size()
}

// noteScanProgress records files whose scan advanced the offset by zero bytes
// this pass. See internal/codexsyncer/syncer.go's copy of this function for
// the full reasoning: a growing file that made no progress is distinguished
// from a shrunk one, whose offset is reset to the file's current size.
func (s *OmoSyncer) noteScanProgress(filePath string, offset, newOffset int64, records int) {
	if newOffset != offset {
		return
	}
	fi, err := os.Stat(filePath)
	if err != nil {
		return
	}

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

func (s *OmoSyncer) syncFile(ctx context.Context, filePath string, firstRun bool) (int, error) {
	// On first run (new state), skip existing files to EOF. omo has no
	// SessionStart-style lifecycle hook, so enabling the integration is
	// itself the first run and every session already on disk would otherwise
	// be skipped whole with nothing to explain it. See the Codex-side note in
	// internal/codexsyncer/syncer.go:321-349 for the full reasoning; the
	// SkippedAtFirstSync bookkeeping here mirrors it so `cctrace status` can
	// surface the skip.
	if !s.state.HasFile(filePath) && firstRun {
		fi, err := os.Stat(filePath)
		if err == nil {
			meta, err := omolog.ScanMetadataContext(ctx, filePath, fi.Size())
			if err != nil {
				return 0, err
			}
			s.state.SetOffsetWithMetadata(filePath, fi.Size(), meta.CWD, "")
			if fi.Size() > 0 {
				// See the Codex-side note: the log copy of this fact rotates
				// away, the state copy is what `cctrace status` can still
				// read later.
				s.state.Files[filePath].SkippedAtFirstSync = fi.Size()
			}
			_ = s.state.Save()
			if fi.Size() > 0 {
				log.Printf("[omo-syncer] %s: skipped (pre-existing at first sync -- earlier content is not collected) bytes=%d", filePath, fi.Size())
			}
		}
		return 0, nil
	}

	sessionID := omolog.SessionIDFromPath(filePath)
	offset := s.state.GetOffset(filePath)
	offset = s.resetIfShrunk(filePath, offset)
	// Asked before the scan, while offset still names the prefix the ledger
	// claims to describe. An untrusted answer means this file counts from zero
	// this pass and records nothing, the way it did before the ledger existed.
	seed, ledgerTrusted := s.state.ConflictSeed(filePath, offset)
	initial := omolog.Metadata{CWD: s.fileCWD(filePath)}
	records, newOffset, meta, err := omolog.ScanFileWithMetadataContext(ctx, filePath, offset, sessionID, initial)
	if err != nil {
		return 0, err
	}
	s.noteScanProgress(filePath, offset, newOffset, len(records))

	// omo usually fills uuid with the message id, which completes the server's
	// dedup key on its own. A line that carries no id leaves uuid empty, and
	// then the nudge is the only thing keeping same-timestamp records apart.
	//
	// Built before the no-records return so that return can carry the ledger
	// across the bytes it consumed: they held nothing to count, but the offset
	// the counts describe still moved.
	nudger := syncer.NewConflictNudger(seed)
	if len(records) == 0 {
		if newOffset != offset || meta.CWD != initial.CWD {
			s.state.AdvanceConflictTail(filePath, offset, newOffset, ledgerTrusted, nudger)
			s.state.SetOffsetWithMetadata(filePath, newOffset, meta.CWD, "")
			_ = s.state.Save()
		}
		return 0, nil
	}

	storeRecords := make([]*store.SessionRecord, 0, len(records))
	sourceFile := filepath.Base(filePath)
	entrypoint := entrypointForPath(filePath)
	for _, r := range records {
		sr := toStoreRecord(r, s.profileEmail, s.userID, sessionID, sourceFile, entrypoint)
		if sr != nil {
			nudger.Apply(sr)
			storeRecords = append(storeRecords, sr)
		}
	}

	if len(storeRecords) == 0 {
		s.state.AdvanceConflictTail(filePath, offset, newOffset, ledgerTrusted, nudger)
		s.state.SetOffsetWithMetadata(filePath, newOffset, meta.CWD, "")
		_ = s.state.Save()
		return 0, nil
	}

	cwd := meta.CWD
	projectHash := projecthash.FromPath(cwd)
	if projectHash == "" {
		projectHash = projectHashFromPath(filePath)
	}
	projectName := projecthash.NameFromPath(cwd)
	gitMeta := gitctx.Resolve(cwd)

	// Drop repositories outside the configured allowlist before sending.
	// Advance the offset so the dropped records are not rescanned every pass.
	if !gitctx.AllowsRepository(gitMeta.RepositoryID, s.collectPrefixes) {
		// The ledger tracks the consumed prefix, not what was sent: records
		// dropped here still occupy the keys a later same-timestamp record has
		// to be nudged past, exactly as a from-zero rescan would count them.
		s.state.AdvanceConflictTail(filePath, offset, newOffset, ledgerTrusted, nudger)
		s.state.SetOffsetWithMetadata(filePath, newOffset, meta.CWD, "")
		_ = s.state.Save()
		return 0, nil
	}

	if _, err := s.client.Send(ctx, "omo", s.profileEmail, s.userID, projectHash, projectName,
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

	s.state.AdvanceConflictTail(filePath, offset, newOffset, ledgerTrusted, nudger)
	s.state.SetOffsetWithMetadata(filePath, newOffset, meta.CWD, "")
	if err := s.state.Save(); err != nil {
		log.Printf("[omo-syncer] save state: %v", err)
	}
	return len(storeRecords), nil
}

// fileCWD returns the cwd previously recorded for filePath, so a resumed scan
// that starts mid-file (after the header line was already consumed) still
// knows the session's working directory.
func (s *OmoSyncer) fileCWD(filePath string) string {
	if fs := s.state.Files[filePath]; fs != nil {
		return fs.CWD
	}
	return ""
}

// usageIsDoubleCounted reports whether provider's usage/cost must be dropped
// before forwarding, because it is already collected through another agent's
// log and forwarding it here would count it twice.
//
// omo mixes two kinds of provider within one session log, and only one of
// them is double-counted:
//
//   - "claude-sdk-oauth": omo drives these turns by spawning the real `claude`
//     binary. Claude Code writes its own session log for that same work, and
//     cctrace already ingests it under agent="claude" through the ordinary
//     Claude syncer. Forwarding usage/cost for these omo records as well would
//     double-count tokens already billed once through that path. The record
//     itself (identity, thread, utterance) is still wanted, so it is still
//     sent -- only the usage is stripped.
//   - every other provider (e.g. "openai-codex"): these call the provider API
//     directly and spawn no subprocess, so they appear in no other log
//     anywhere. This was verified on real data: 264 such records, 37.6M
//     tokens, $23.34 that exist only in omo's own log and are absent from
//     ~/.codex/sessions. Their usage must be forwarded or it is lost forever.
//
// The claude-sdk-oauth case is the exception, not the rule: every other
// provider defaults to forwarding. The comparison is case-insensitive because
// omo's provider strings are not guaranteed stable in case, and an empty
// provider is treated as NOT double-counted -- fail toward collecting, since
// losing usage is unrecoverable while a wrongly-forwarded double-count is at
// least visible and fixable in aggregate cost.
func usageIsDoubleCounted(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), "claude-sdk-oauth")
}

// billingProviderFor maps omo's own provider string onto cctrace's canonical
// billing_provider vocabulary: exactly "anthropic", "openai", or "other" (see
// classifyAgent in internal/otelrecv/logs.go:212-225, the OTEL ingest path
// that vocabulary comes from). Unlike codexsyncer (always "openai") and the
// Claude syncer (always "anthropic"), omo cannot be pinned to one billing
// provider per syncer instance: a single omo session mixes providers turn by
// turn, so the mapping is applied per record instead of once per syncer.
//
// This is deliberately an explicit switch with a default of "other", not a
// pass-through of the source string: omo's own vocabulary collides with the
// canonical one. In that canonical vocabulary "openai" means Codex
// subscription usage (classifyAgent returns ("codex", "openai") for every
// Codex event, regardless of how it is billed). omo, however, emits a
// provider literally named "openai" for OpenAI API-key metered usage, which
// is NOT the same billing kind. Passing it through unchanged would merge two
// different kinds of usage into one column value -- nothing errors, a cost
// query filtered on "openai" just silently sums two unrelated things. So the
// bare source string "openai" is mapped to "other" here, and only omo's own
// "openai-codex" provider (real Codex, driven through omo the same way
// claude-sdk-oauth drives real Claude Code) maps to canonical "openai".
// Do not "fix" the "openai" -> "other" line back to a pass-through; that is
// the bug this comment exists to prevent.
//
// The source provider string itself is not lost: it survives in the record's
// Raw field, so no separate column is needed to recover it later.
//
// This mapping is independent of usageIsDoubleCounted, which keys off the
// SOURCE provider string, not this mapped value. Both "claude-sdk-oauth" and
// a bare "anthropic" map to "anthropic" here, but only claude-sdk-oauth's
// usage is double-counted -- keep the two functions separate.
func billingProviderFor(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude-sdk-oauth", "anthropic":
		return "anthropic"
	case "openai-codex":
		return "openai"
	default:
		// Covers omo's own "openai" (API-key metered, not Codex subscription),
		// "agent-memory", any other/unknown provider, and empty.
		return "other"
	}
}

// entrypointForPath maps an omo session file's root to the entrypoint
// vocabulary: the value it produces names the launcher that started the
// session, truthfully, independent of how any particular query happens to
// read that value today. omo emits no per-record human-turn signal at all --
// unlike gjc's attribution field, its user messages carry only
// content/role/timestamp -- so the session root a file was found under is
// the only available evidence. This is necessarily coarser than gjc's
// per-record attribution: it classifies a whole session, not individual
// turns.
//
//   - sessions/ (not an agent-session path) is the human-facing conversation,
//     so it gets "cli", the same value Codex uses for its human TUI.
//   - agent/sessions/ holds sessions omo spawned for its own task work. It
//     maps to "omo_agent", following Codex's precedent of tool-prefixed
//     launcher names (codex_exec, codex_sdk_ts): this names omo's own task
//     runner as the launcher, which is what actually happened.
//
// "sdk-cli" was considered and rejected for the automated case: that value is
// documented in three places (store.go:102, store.go:534,
// web/lib/types.ts:269) as meaning specifically "claude -p", a Claude Code
// launcher. An omo session is not that. Reusing it would have made the
// column state a false fact for a query convenience, and checking the truth
// table shows it buys nothing: under the current ELSE branch a truthful
// value like "omo_agent" already evaluates to headless (has_enriched true,
// has_genuine permanently false for omo), the same outcome "sdk-cli" would
// have forced -- and under a future Codex-style arm keyed on agent='omo',
// "omo_agent" simply falls outside the human set, headless again. The
// session classifier's current treatment of any of these values is a
// downstream consequence of what they name, not the reason to pick one.
func entrypointForPath(path string) string {
	if omolog.IsAgentSessionPath(path) {
		return "omo_agent"
	}
	return "cli"
}

func toStoreRecord(r *omolog.Record, profileEmail, userID, sessionID, sourceFile, entrypoint string) *store.SessionRecord {
	if r.RecordType == "" {
		return nil
	}
	sr := &store.SessionRecord{
		Ts:              r.Timestamp,
		SourceFile:      sourceFile,
		Entrypoint:      entrypoint,
		SessionID:       sessionID,
		RecordType:      r.RecordType,
		ProfileEmail:    profileEmail,
		UserID:          userID,
		Model:           r.Model,
		Agent:           "omo",
		BillingProvider: billingProviderFor(r.Provider),
		UUID:            r.MessageID,
		ParentUUID:      r.ParentID,
		Raw:             r.Raw,
	}
	if r.RecordType == "tool_result" {
		sr.ToolName = r.ToolName
		sr.ToolCallID = r.ToolCallID
	}
	if !usageIsDoubleCounted(r.Provider) {
		sr.InputTokens = r.InputTokens
		sr.OutputTokens = r.OutputTokens
		sr.CacheReadTokens = r.CacheReadTokens
		sr.CacheCreateTokens = r.CacheWriteTokens
	}
	return sr
}

// projectHashFromPath is a fallback for files that have no CWD.
func projectHashFromPath(filePath string) string {
	h := sha256.Sum256([]byte(filePath))
	return fmt.Sprintf("omo-%x", h[:8])
}
