// Package gjcsyncer scans gjc session JSONL files and sends records to the
// cctrace server. It mirrors internal/codexsyncer closely - gjc is the
// second-newest agent kind added the same way, after codexsyncer generalized
// the pattern first laid down by internal/syncer. Deliberately not sharing
// code with those two packages: that abstraction is deferred until a fourth
// example exists to generalize from. The one exception is
// syncer.ConflictNudger, which all three use: it encodes one invariant about
// the server's dedup key, and three private copies of it could drift apart
// without any of them looking wrong on its own.
package gjcsyncer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"cctrace/internal/gitctx"
	"cctrace/internal/gjclog"
	"cctrace/internal/projecthash"
	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

// GjcSyncer scans gjc session JSONL files and sends records to the cctrace server.
type GjcSyncer struct {
	// gjcDirs lists every gjc home directory to scan for session JSONL files.
	gjcDirs      []string
	profileEmail string
	userID       string
	state        *syncer.State
	client       *syncer.Client
	// collectPrefixes, when non-empty, restricts syncing to repositories whose
	// normalized id starts with one of these prefixes. Empty collects all.
	collectPrefixes []string
	// lastScanSummary holds the previous per-home file-count summary so watch
	// mode logs it only when it changes, instead of every poll interval.
	lastScanSummary string
	// lastPassSummary throttles the end-of-pass summary the same way.
	lastPassSummary string
	// passLagging counts files that grew on disk but whose scan advanced the
	// offset by zero bytes during the current pass; passLagSample keeps the
	// first such file for the summary line.
	passLagging   int
	passLagSample string
	// shrunkFiles marks files currently found shrunk below their stored
	// offset, so the "stalled" log fires once per stall episode instead of
	// every pass. Cleared once the file's size catches back up to the offset.
	shrunkFiles map[string]bool
}

// New creates a new GjcSyncer. gjcDirs lists the gjc home directories to
// scan; files found in them are merged into a single set. collectPrefixes
// restricts which repositories are synced; pass nil to collect every
// repository.
func New(gjcDirs []string, profileEmail, userID string, state *syncer.State, client *syncer.Client, collectPrefixes []string) *GjcSyncer {
	return &GjcSyncer{
		gjcDirs:         gjcDirs,
		profileEmail:    profileEmail,
		userID:          userID,
		state:           state,
		client:          client,
		collectPrefixes: collectPrefixes,
		shrunkFiles:     make(map[string]bool),
	}
}

// findAllFiles returns the union of main session files and subagent
// transcript files across every configured gjc home, deduplicated by
// absolute path. A home that fails to scan is logged and skipped so one
// broken home cannot stop the whole pass.
func (s *GjcSyncer) findAllFiles() ([]string, error) {
	seen := make(map[string]struct{})
	var all []string
	parts := make([]string, 0, len(s.gjcDirs))
	for _, dir := range s.gjcDirs {
		added := 0
		for _, find := range []func(string) ([]string, error){gjclog.FindSessionFiles, gjclog.FindSubagentFiles} {
			files, err := find(dir)
			if err != nil {
				log.Printf("[gjc-syncer] scan %s: %v", dir, err)
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
		}
		parts = append(parts, fmt.Sprintf("%s=%d", dir, added))
	}

	// Log which homes were scanned and how many files each contributed. Only
	// log on change so watch mode does not repeat the same line every poll
	// interval.
	summary := fmt.Sprintf("%d home(s), %d file(s): %s", len(s.gjcDirs), len(all), strings.Join(parts, ", "))
	if summary != s.lastScanSummary {
		log.Printf("[gjc-syncer] scan %s", summary)
		s.lastScanSummary = summary
	}
	return all, nil
}

func (s *GjcSyncer) SyncOnce(ctx context.Context) (int, error) {
	files, err := s.findAllFiles()
	if err != nil {
		return 0, fmt.Errorf("find gjc jsonl files: %w", err)
	}

	// Capture the first-run flag once for the whole pass. The first Save()
	// clears state.IsNew(), so re-reading it per file would skip only the
	// first file and backfill the rest of the history.
	firstRun := s.state.IsNew()
	if firstRun {
		// gjc has no lifecycle hook of any kind, so enabling the integration is
		// always itself the first run. Without this the window never closes on
		// a pass that records nothing, and the first session created
		// afterwards is skipped whole.
		if err := s.state.Save(); err != nil {
			log.Printf("[gjc-syncer] failed to save state: %v", err)
		}
	}
	total := 0
	s.passLagging = 0
	s.passLagSample = ""
	for i, file := range files {
		// Stop promptly when the watch context is cancelled (graceful stop),
		// rather than scanning every remaining file first. Log where the pass
		// stopped: a pass that consistently ends early leaves later homes in
		// the scan order permanently unsynced, which is silent without this
		// line.
		if err := ctx.Err(); err != nil {
			log.Printf("[gjc-syncer] pass stopped before file %d/%d: %v", i+1, len(files), err)
			return total, err
		}
		n, err := s.syncFile(ctx, file, firstRun)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				log.Printf("[gjc-syncer] pass stopped at file %d/%d (%s): %v", i+1, len(files), file, ctxErr)
				return total, ctxErr
			}
			log.Printf("[gjc-syncer] %s: %v", file, err)
			continue
		}
		total += n
	}

	// End-of-pass summary. Throttled to changes so watch mode does not repeat
	// it every poll, but a stuck file keeps `lagging` non-zero and therefore
	// stays visible once the count or the sample changes.
	summary := fmt.Sprintf("files=%d synced=%d lagging=%d", len(files), total, s.passLagging)
	if s.passLagSample != "" {
		summary += " | " + s.passLagSample
	}
	if summary != s.lastPassSummary {
		log.Printf("[gjc-syncer] pass %s", summary)
		s.lastPassSummary = summary
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
func (s *GjcSyncer) resetIfShrunk(filePath string, offset int64) int64 {
	fi, err := os.Stat(filePath)
	if err != nil || fi.Size() >= offset {
		return offset
	}
	if !s.shrunkFiles[filePath] {
		s.shrunkFiles[filePath] = true
		log.Printf("[gjc-syncer] %s: shrunk, offset reset %d -> %d (%d bytes no longer on disk)", filePath, offset, fi.Size(), offset-fi.Size())
	}
	s.state.SetOffset(filePath, fi.Size())
	// The bytes the ledger described are not on disk any more, so what it says
	// about the prefix ending at the new offset is unknown. An unknown ledger is
	// worse than none: it would nudge past records that may no longer exist.
	s.state.SetConflictTail(filePath, nil)
	if err := s.state.Save(); err != nil {
		log.Printf("[gjc-syncer] failed to save state: %v", err)
	}
	return fi.Size()
}

// noteScanProgress records files whose scan advanced the offset by zero bytes
// this pass. See internal/codexsyncer's counterpart for the full reasoning:
// a file whose on-disk size exceeds the offset but produced no state write is
// otherwise silently invisible, and a shrunk file has its offset reset to the
// file's current size rather than rewound to zero.
func (s *GjcSyncer) noteScanProgress(filePath string, offset, newOffset int64, records int) {
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

func (s *GjcSyncer) syncFile(ctx context.Context, filePath string, firstRun bool) (int, error) {
	// On first run (new state), skip existing files to EOF. See
	// internal/codexsyncer/syncer.go and internal/syncer/syncer.go for the
	// shared reasoning: gjc has no lifecycle hook, so enabling the integration
	// is itself the first run and every session already on disk would
	// otherwise be skipped whole with no explanation.
	if !s.state.HasFile(filePath) && firstRun {
		fi, err := os.Stat(filePath)
		if err == nil {
			meta, err := gjclog.ScanMetadataContext(ctx, filePath, fi.Size())
			if err != nil {
				return 0, err
			}
			setStateFileMetadata(s.state, filePath, fi.Size(), meta)
			if fi.Size() > 0 {
				// The log line rotates away; the state copy is what `cctrace
				// status` can still read later.
				s.state.Files[filePath].SkippedAtFirstSync = fi.Size()
			}
			_ = s.state.Save()
			if fi.Size() > 0 {
				log.Printf("[gjc-syncer] %s: skipped (pre-existing at first sync - earlier content is not collected) bytes=%d", filePath, fi.Size())
			}
		}
		return 0, nil
	}

	sessionID := gjclog.SessionIDFromPath(filePath)
	offset := s.state.GetOffset(filePath)
	offset = s.resetIfShrunk(filePath, offset)
	// Asked before the scan, while offset still names the prefix the ledger
	// claims to describe. An untrusted answer means this file counts from zero
	// this pass and records nothing, the way it did before the ledger existed.
	seed, ledgerTrusted := s.state.ConflictSeed(filePath, offset)
	meta := s.fileMetadata(filePath)

	records, newOffset, scanMeta, err := gjclog.ScanFileWithMetadataContext(ctx, filePath, offset, sessionID, meta)
	if err != nil {
		return 0, err
	}
	s.noteScanProgress(filePath, offset, newOffset, len(records))
	meta = mergeMetadata(meta, scanMeta)

	// gjc emits an assistant record and its sibling tool_call records from one
	// source line, so they share Timestamp, and multiple tool_call siblings on
	// one assistant message additionally share RecordType. Without the nudge the
	// server's dedup key drops the siblings.
	//
	// Built before the no-records return so that return can carry the ledger
	// across the bytes it consumed: they held nothing to count, but the offset
	// the counts describe still moved.
	nudger := syncer.NewConflictNudger(seed)
	if len(records) == 0 {
		if newOffset != offset {
			s.state.AdvanceConflictTail(filePath, offset, newOffset, ledgerTrusted, nudger)
			setStateFileMetadata(s.state, filePath, newOffset, meta)
			_ = s.state.Save()
		}
		return 0, nil
	}

	storeRecords := make([]*store.SessionRecord, 0, len(records))
	sourceFile := filepath.Base(filePath)
	for _, r := range records {
		sr := toStoreRecord(r, s.profileEmail, s.userID, sourceFile)
		if sr != nil {
			nudger.Apply(sr)
			storeRecords = append(storeRecords, sr)
		}
	}

	if len(storeRecords) == 0 {
		s.state.AdvanceConflictTail(filePath, offset, newOffset, ledgerTrusted, nudger)
		setStateFileMetadata(s.state, filePath, newOffset, meta)
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
		setStateFileMetadata(s.state, filePath, newOffset, meta)
		_ = s.state.Save()
		return 0, nil
	}

	if _, err := s.client.Send(ctx, "gjc", s.profileEmail, s.userID, projectHash, projectName,
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
	setStateFileMetadata(s.state, filePath, newOffset, meta)
	if err := s.state.Save(); err != nil {
		log.Printf("[gjc-syncer] save state: %v", err)
	}
	return len(storeRecords), nil
}

// fileMetadata reconstructs a gjclog.Metadata from the persisted FileState so
// an incremental scan resumes with the same session id and cwd a prior pass
// recovered from the header line.
func (s *GjcSyncer) fileMetadata(filePath string) gjclog.Metadata {
	if fs := s.state.Files[filePath]; fs != nil {
		return gjclog.Metadata{SessionID: fs.GjcSessionID, CWD: fs.CWD}
	}
	return gjclog.Metadata{}
}

// setStateFileMetadata persists the offset plus the cwd and session id a scan
// recovered from the file's header, so a later resumed scan - past the point
// where the header line was last read - can restore both.
func setStateFileMetadata(state *syncer.State, filePath string, offset int64, meta gjclog.Metadata) {
	state.SetOffsetWithMetadata(filePath, offset, meta.CWD, "")
	if fs := state.Files[filePath]; fs != nil && meta.SessionID != "" {
		fs.GjcSessionID = meta.SessionID
	}
}

// mergeMetadata folds a freshly scanned gjclog.Metadata onto a base one,
// keeping the base value wherever the new scan found nothing (an empty
// header field, or a resumed scan past the header line).
func mergeMetadata(base, next gjclog.Metadata) gjclog.Metadata {
	if next.SessionID != "" {
		base.SessionID = next.SessionID
	}
	if next.CWD != "" {
		base.CWD = next.CWD
	}
	if next.Title != "" {
		base.Title = next.Title
	}
	return base
}

// normalizeGjcBillingProvider maps gjc's per-record provider onto the
// canonical billing_provider vocabulary: exactly "anthropic", "openai", or
// "other", as defined by classifyAgent in internal/otelrecv/logs.go. That
// vocabulary is not "which API served the request" - "openai" specifically
// means Codex (ChatGPT-subscription-billed), so an unrecognized or unrelated
// source string (amazon-bedrock, anything else, empty) must fall to "other"
// rather than pass through unchanged, or it corrupts the column with values
// that read the same but mean something different. The original source
// provider string is not lost: it survives in the record's Raw field.
//
// Unlike codex, gjc cannot pin one provider per agent: anthropic,
// openai-codex, and amazon-bedrock all appear inside a single gjc session, so
// the mapping happens per record rather than once at construction.
func normalizeGjcBillingProvider(provider string) string {
	switch provider {
	case "anthropic":
		return "anthropic"
	case "openai-codex":
		return "openai"
	default:
		return "other"
	}
}

// promptSourceForAttribution maps gjc's attribution field onto the canonical
// prompt_source vocabulary the session classifier reads (see
// sessionInteractiveCol in internal/store/postgres_session_records.go): gjc's
// attribution is its prompt_source equivalent, and "typed" is the canonical
// value that query recognizes as a genuine human turn. Only "user" (a
// human-typed turn) maps to it. "agent" (gjc generating a turn for its own
// task work) and any other value, including one not yet observed, must NOT
// be optimistically mapped to a human value -- a false "typed" would mark
// automated work as interactive, and that error is invisible downstream.
func promptSourceForAttribution(attribution string) string {
	switch attribution {
	case "user":
		return "typed"
	default:
		return ""
	}
}

// toStoreRecord converts a gjclog.Record to a store.SessionRecord. r.Raw
// already carries the full parsed usage/cost payload, so nothing beyond the
// token counts below is lost by SessionRecord having no cost field of its
// own.
func toStoreRecord(r *gjclog.Record, profileEmail, userID, sourceFile string) *store.SessionRecord {
	if r.RecordType == "" {
		return nil
	}
	// Attribution only carries meaning on user turns -- gate on RecordType so
	// a stray attribution value surviving onto an assistant or tool_result
	// record (which should never carry one) cannot masquerade as prompt_source.
	var promptSource string
	if r.RecordType == "user" {
		promptSource = promptSourceForAttribution(r.Attribution)
	}
	var toolName, toolCallID string
	if r.RecordType == "tool_call" || r.RecordType == "tool_result" {
		toolName, toolCallID = r.ToolName, r.ToolCallID
	}
	return &store.SessionRecord{
		Ts:                r.Timestamp,
		SourceFile:        sourceFile,
		SessionID:         r.SessionID,
		RecordType:        r.RecordType,
		ProfileEmail:      profileEmail,
		UserID:            userID,
		Model:             r.Model,
		Agent:             "gjc",
		BillingProvider:   normalizeGjcBillingProvider(r.Provider),
		PromptSource:      promptSource,
		ToolName:          toolName,
		ToolCallID:        toolCallID,
		InputTokens:       r.InputTokens,
		OutputTokens:      r.OutputTokens,
		CacheReadTokens:   r.CacheReadTokens,
		CacheCreateTokens: r.CacheWriteTokens,
		Raw:               r.Raw,
	}
}

// projectHashFromPath is a fallback for files whose header carried no cwd.
func projectHashFromPath(filePath string) string {
	h := sha256.Sum256([]byte(filePath))
	return fmt.Sprintf("gjc-%x", h[:8])
}
