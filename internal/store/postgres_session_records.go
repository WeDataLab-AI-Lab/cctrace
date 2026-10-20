package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionlog"
)

// sanitizeJSONNUL replaces the JSON escape for NUL, which jsonb refuses (22P02),
// with the escape for U+FFFD. Every writer of client JSON into jsonb goes through
// it: one value carrying the escape fails the whole batch, which the client then
// resends forever.
//
// Escape text for escape text, never the raw character: the match can sit after a
// backslash that escapes a literal "\u0000" in the text, and only a six-byte
// escape keeps that backslash escaping something valid. The literal then reads
// "\uFFFD" -- a changed string, but a stored one.
func sanitizeJSONNUL(raw []byte) []byte {
	return bytes.ReplaceAll(raw, []byte(`\u0000`), []byte(`\uFFFD`))
}

// sessionRecordMessageIDSQL is the message_id a session_records row is stored
// with: the message.id of a Claude assistant line, or the empty string when the
// line has none, and NULL on every other row -- no request key reads the column
// there.
//
// One expression over three operands -- %[1]s the record type, %[2]s the agent,
// %[3]s the raw -- because two writers need it. InsertSessionRecords applies it
// to its normalised parameters and BackfillSessionRecordMessageIDs to the stored
// columns, which hold those same normalised values, so a row filled by the
// backfill carries exactly what the insert would have given it. ->> answers text
// whatever JSON type the id has, and nothing casts it: an id is compared, never
// computed with.
const sessionRecordMessageIDSQL = `CASE WHEN %[1]s = 'assistant' AND %[2]s = 'claude'
				THEN COALESCE((%[3]s::jsonb)->'message'->>'id', '') END`

func (s *PgStore) InsertSessionRecords(ctx context.Context, records []*SessionRecord) error {
	if len(records) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return err
	}
	live, err := liveSessionIDs(ctx, tx, sessionRecordSessionIDs(records))
	if err != nil {
		return err
	}
	records = filterLiveSessionRecords(records, live)
	if len(records) == 0 {
		return tx.Commit(ctx)
	}

	claimPeers, err := prepareSessionClaimMutation(ctx, tx, sessionRecordSessionIDs(records))
	if err != nil {
		return err
	}

	// The least id each session gained here: where the worker's copy scan of the
	// session starts (p1b-design §3.16). A row the conflict skipped returns no id.
	newFrom := map[string]int64{}
	for _, r := range records {
		raw := r.Raw
		if raw == nil {
			raw = json.RawMessage("{}")
		}
		sanitized := sanitizeJSONNUL(raw)

		agent := r.Agent
		if agent == "" {
			agent = "claude"
		}
		r.RecordType = codexRecordType(agent, r.RecordType, sanitized)

		taskType, classifierVersion := "", ""
		if r.RecordType == "user" && s.classifier != nil && IsTypedTurn(s.classifier, agent, sanitized) {
			taskType = s.classifier.Classify(s.classifier.PromptFromRaw(sanitized))
			classifierVersion = s.classifier.Version()
		}
		r.TaskType = taskType
		bp := r.BillingProvider
		if bp == "" {
			bp = "anthropic"
		}
		codexTurnID := safeCodexTurnMetadataID(r.CodexTurnID)
		codexRootTurnID := safeCodexTurnMetadataID(r.CodexRootTurnID)
		// message_id is computed from $4, $15 and $13 -- the record type after
		// codexRecordType, the agent after its default, the raw after
		// sanitizeJSONNUL -- which are the values the row is stored with. That is
		// what lets the backfill apply the same expression to stored columns.
		var id int64
		err := tx.QueryRow(ctx,
			`INSERT INTO session_records
				(ts, session_id, project_hash, record_type, profile_email, login_email, user_id, model,
				 input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, raw, command_name,
				 agent, billing_provider, repository_id, repository_name, repo_subpath, commit_sha, branch,
				 uuid, parent_uuid, is_sidechain, agent_id, forked_from_session, forked_from_uuid, source_file, tool_use_id,
				 is_compact_summary, is_meta, prompt_source, entrypoint, cctrace_version, account_id, task_type, command_source, command_kind, command_invoke, classifier_version,
				 tool_name, tool_call_id, message_id, codex_turn_id, codex_root_turn_id, codex_turn_metadata_known)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,
				 $22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,
				 `+fmt.Sprintf(sessionRecordMessageIDSQL, "$4", "$15", "$13")+`,$43,$44,$45)
			ON CONFLICT (session_id, ts, record_type, profile_email, uuid) DO UPDATE SET
				codex_turn_id=EXCLUDED.codex_turn_id,
				codex_root_turn_id=EXCLUDED.codex_root_turn_id,
				codex_turn_metadata_known=EXCLUDED.codex_turn_metadata_known
			WHERE EXCLUDED.agent='codex' AND EXCLUDED.codex_turn_metadata_known
			AND (NOT session_records.codex_turn_metadata_known
				OR session_records.codex_turn_id IS DISTINCT FROM EXCLUDED.codex_turn_id
				OR session_records.codex_root_turn_id IS DISTINCT FROM EXCLUDED.codex_root_turn_id)
			RETURNING id`,
			r.Ts, r.SessionID, r.ProjectHash, r.RecordType,
			r.ProfileEmail, r.LoginEmail, r.UserID, r.Model, r.InputTokens, r.OutputTokens,
			r.CacheReadTokens, r.CacheCreateTokens, sanitized, r.CommandName,
			agent, bp, r.RepositoryID, r.RepositoryName, r.RepoSubpath, r.CommitSHA, r.Branch,
			r.UUID, r.ParentUUID, r.IsSidechain, r.AgentID, r.ForkedFromSession, r.ForkedFromUUID, r.SourceFile, r.ToolUseID,
			r.IsCompactSummary, r.IsMeta, r.PromptSource, r.Entrypoint, r.CctraceVersion, r.AccountID, taskType, r.CommandSource, r.CommandKind, r.CommandInvoke, classifierVersion,
			// Same log lines as raw, same NUL the TEXT type rejects -- and one rejected
			// row fails the batch the client keeps resending.
			strings.ReplaceAll(r.ToolName, "\x00", "�"), strings.ReplaceAll(r.ToolCallID, "\x00", "�"),
			codexTurnID, codexRootTurnID, r.CodexTurnMetadataKnown,
		).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if from, ok := newFrom[r.SessionID]; !ok || id < from {
			newFrom[r.SessionID] = id
		}
	}
	sessionIDs := sessionRecordSessionIDs(records)
	if err := refreshSessionOverviewRollups(ctx, tx, sessionIDs); err != nil {
		return err
	}
	if err := refreshPluginInvocationFacts(ctx, tx, pluginFactSessionIDs(records)); err != nil {
		return err
	}
	if err := refreshTaskSegmentFacts(ctx, tx, sessionIDs); err != nil {
		return err
	}
	// Every session the rollups were refreshed for, as p1b-design §5.2 has it, new
	// rows or not; one that gained none has no new row to scan for copies.
	scanFrom := make([]*int64, len(sessionIDs))
	for i, id := range sessionIDs {
		if from, ok := newFrom[id]; ok {
			scanFrom[i] = &from
		}
	}
	if err := markClaimSourceFactsDirty(ctx, tx, sessionIDs, scanFrom, dirtySync, claimPeers); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PostgreSQL text cannot store NUL. A malformed lifecycle ID is unknown for
// attribution; never rewrite it into a different ID that could match a turn.
func safeCodexTurnMetadataID(id string) string {
	if strings.IndexByte(id, 0) >= 0 {
		return ""
	}
	return id
}

func sessionRecordSessionIDs(records []*SessionRecord) []string {
	seen := make(map[string]struct{}, len(records))
	ids := make([]string, 0, len(records))
	for _, record := range records {
		if record.SessionID == "" {
			continue
		}
		if _, ok := seen[record.SessionID]; ok {
			continue
		}
		seen[record.SessionID] = struct{}{}
		ids = append(ids, record.SessionID)
	}
	return ids
}

// Plugin attribution historically included blank session IDs. Preserve that edge
// case separately from overviews, where an empty ID is intentionally not a session.
func pluginFactSessionIDs(records []*SessionRecord) []string {
	seen := make(map[string]struct{}, len(records))
	ids := make([]string, 0, len(records))
	for _, record := range records {
		if _, ok := seen[record.SessionID]; ok {
			continue
		}
		seen[record.SessionID] = struct{}{}
		ids = append(ids, record.SessionID)
	}
	return ids
}

// reenrichMergeObject is the part of a record's fresh raw ReenrichSessionRecords
// hands to jsonb: the collected keys alone (sessionlog.CollectedKeys), each value
// decoded and written back out by Go, so what reaches jsonb is what Go writes
// rather than what the client did. That is what turns a lone surrogate escape --
// which jsonb refuses, failing the whole batch, and Go decodes to U+FFFD -- into
// text jsonb takes, as the session observation handler does for snapshots.
// Numbers keep their digits (UseNumber), and the NUL escape gets the insert
// path's treatment. Every other key, message included should a client still send
// one, is dropped before its value is read. A record from any agent but Claude,
// or a raw that is not an object, merges as {}, which adds nothing and leaves the
// column updates beside it to apply.
func reenrichMergeObject(agent string, raw json.RawMessage) []byte {
	return reenrichMergeObjectForType(agent, "", raw)
}

func reenrichMergeObjectForType(agent, recordType string, raw json.RawMessage) []byte {
	if agent != "" && agent != "claude" {
		return []byte("{}")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return []byte("{}")
	}
	kept := make(map[string]any, len(sessionlog.CollectedKeys))
	keys := append([]string{}, sessionlog.CollectedKeys...)
	if recordType == "api-request-blob" {
		keys = append(keys, "message")
	}
	for _, key := range keys {
		value, ok := top[key]
		if !ok {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(value))
		dec.UseNumber()
		var decoded any
		if err := dec.Decode(&decoded); err != nil {
			return []byte("{}")
		}
		kept[key] = decoded
	}
	out, err := json.Marshal(kept)
	if err != nil {
		return []byte("{}")
	}
	return sanitizeJSONNUL(out)
}

// reenrichMergedRaw is the stored raw with what the fresh one adds: every
// collected key it lacks (fresh.kept || raw), and one level down, for the two
// collected objects whose members arrive in more than one pass, every member the
// stored object lacks. A default re-enrichment creates attachment and
// toolUseResult with their signal members only; a later --include-content run
// can then add the content members under them. In jsonb || the right operand
// wins, so at both levels whatever the row already holds stays as stored,
// redaction markers included.
const reenrichMergedRaw = `((fresh.kept || raw) || COALESCE((
						SELECT jsonb_object_agg(k, (fresh.kept -> k) || (raw -> k))
						FROM unnest(ARRAY['toolUseResult', 'attachment']) AS k
						WHERE jsonb_typeof(fresh.kept -> k) = 'object' AND jsonb_typeof(raw -> k) = 'object'
					), '{}'::jsonb))`

// ReenrichSessionRecords intentionally never writes cctrace_version. Re-enrichment is a
// later-version client re-reading old JSONL, so the reenriching client's version is not
// the version that originally collected the row. The "fill only if empty" pattern used
// for uuid/prompt_source below is wrong here: for those fields empty means "unknown", but
// for cctrace_version empty is confirmed information ("collected before
// CctraceVersionSince"). Overwriting it — even only when empty — would turn that confirmed
// fact into a false "collected by a version that supports this column". Do not add
// cctrace_version to the SET list below; TestPgStore_ReenrichSessionRecords_preservesCctraceVersion
// pins this.
func (s *PgStore) ReenrichSessionRecords(ctx context.Context, records []*SessionRecord) (int, error) {
	if len(records) == 0 {
		return 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return 0, err
	}
	live, err := liveSessionIDs(ctx, tx, sessionRecordSessionIDs(records))
	if err != nil {
		return 0, err
	}
	records = filterLiveSessionRecords(records, live)
	if len(records) == 0 {
		return 0, tx.Commit(ctx)
	}

	claimPeers, err := prepareSessionClaimMutation(ctx, tx, sessionRecordSessionIDs(records))
	if err != nil {
		return 0, err
	}

	updated := 0
	for _, r := range records {
		r.RecordType = codexRecordType(r.Agent, r.RecordType, r.Raw)
		codexTurnID := safeCodexTurnMetadataID(r.CodexTurnID)
		codexRootTurnID := safeCodexTurnMetadataID(r.CodexRootTurnID)
		remoteAuthority, clearSubpath, fillSubpath := sessionRecordIdentityFlags(r)
		// The client's freshly built raw, merged below. A record without raw (a
		// client that does not send it, and most of the tests here) merges as {},
		// which changes nothing.
		freshRaw := reenrichMergeObjectForType(r.Agent, r.RecordType, r.Raw)
		tag, err := tx.Exec(ctx, `WITH fresh AS (
				-- reenrichMergeObject has already cut the object down to the keys a
				-- collecting client adds (sessionlog.CollectedKeys), the one place
				-- that list is applied: message, and anything else a stored row
				-- happens to lack, is not re-enrichment's to fill in. What is left to
				-- decide here needs the row: pre-v3 clients collected top-level content
				-- only for away_summary recaps. V3 also collects system messages; other
				-- record types still cannot merge conversation content through this path.
				SELECT COALESCE(jsonb_object_agg(key, value), '{}'::jsonb) AS kept
				FROM jsonb_each($28::jsonb)
				WHERE key <> 'content' OR ($3 = 'system' AND ($28::jsonb ->> 'subtype' = 'away_summary'
				 OR (jsonb_typeof($28::jsonb -> 'cctraceCollect') = 'number' AND $28::jsonb -> 'cctraceCollect' >= '3'::jsonb)))
			)
			UPDATE session_records
			SET uuid = CASE WHEN $5 <> '' THEN $5 ELSE uuid END,
				parent_uuid = CASE WHEN $6 <> '' THEN $6 ELSE parent_uuid END,
				is_sidechain = is_sidechain OR $7,
				agent_id = CASE WHEN $8 <> '' THEN $8 ELSE agent_id END,
				forked_from_session = CASE WHEN $9 <> '' THEN $9 ELSE forked_from_session END,
				forked_from_uuid = CASE WHEN $10 <> '' THEN $10 ELSE forked_from_uuid END,
				source_file = CASE WHEN $11 <> '' THEN $11 ELSE source_file END,
				tool_use_id = CASE WHEN $12 <> '' THEN $12 ELSE tool_use_id END,
				is_compact_summary = is_compact_summary OR $13,
				is_meta = is_meta OR $14,
				prompt_source = CASE WHEN $15 <> '' THEN $15 ELSE prompt_source END,
				entrypoint = CASE WHEN $16 <> '' THEN $16 ELSE entrypoint END,
				codex_turn_id = CASE WHEN $31 AND COALESCE(NULLIF(agent,''),'claude')='codex' THEN $29 ELSE codex_turn_id END,
				codex_root_turn_id = CASE WHEN $31 AND COALESCE(NULLIF(agent,''),'claude')='codex' THEN $30 ELSE codex_root_turn_id END,
				codex_turn_metadata_known = codex_turn_metadata_known OR ($31 AND COALESCE(NULLIF(agent,''),'claude')='codex'),
				-- Everything a v0.7.22 client learned to derive. These are the only route
				-- back for history: the insert path is ON CONFLICT DO NOTHING, so simply
				-- re-syncing an old session leaves its rows exactly as they were, and the
				-- whole point of re-enrichment is to carry newly-derivable facts onto rows
				-- that predate the deriving.
				--
				-- Fill-only, never blank, same as every field above. A client that could
				-- not classify something sends '' and must not erase what an earlier pass
				-- already established -- and '' is itself meaningful for command_source:
				-- it marks a client that never looked, distinct from one that looked and
				-- could not tell.
				command_source = CASE WHEN $17 <> '' THEN $17 ELSE command_source END,
				command_kind = CASE WHEN $18 <> '' THEN $18 ELSE command_kind END,
				command_invoke = CASE WHEN $19 <> '' THEN $19 ELSE command_invoke END,
				repository_id = CASE WHEN $25 AND $20 <> '' THEN $20 ELSE repository_id END,
				repository_name = CASE WHEN $25 AND $21 <> '' THEN $21 ELSE repository_name END,
				repo_subpath = CASE
					WHEN $26 THEN $22
					WHEN $27 AND $22 <> '' AND repo_subpath = '' THEN $22
					ELSE repo_subpath
				END,
				account_id = CASE WHEN $23 <> '' THEN $23 ELSE account_id END,
				-- A client-observed account_id overwriting a server-filled one must
				-- take its provenance back to observed ('') too, or an attribute pass
				-- later reads the stale 'quota-inferred' left behind and never
				-- revisits a value that just changed under it.
				account_id_source = CASE WHEN $23 <> '' THEN '' ELSE account_id_source END,
				task_type = CASE WHEN $24 <> '' THEN $24 ELSE task_type END,
				-- The fields a newer client keeps on a line (sessionlog.CollectVersion)
				-- reach rows stored before it only through here (reenrichMergedRaw).
				-- Every key the stored raw already has stays as stored, the markers
				-- of a redaction that was on when the row was collected included;
				-- only what it lacks is added. That alone does not stop a relaxed
				-- policy from un-redacting a row: a row collected before these
				-- fields lacks them, and they are added under the policy in force
				-- now. The client sends the content fields only when asked to
				-- (sync reenrich --include-content), and warns when it does.
				--
				-- Claude rows only: the collected keys are fields of a Claude Code
				-- line, and no other agent's client collects them. Only into an
				-- object: on anything else || builds an array and the row would
				-- stop reading as a line. And only when something is
				-- actually added: assigning raw to itself keeps the stored (TOASTed)
				-- value as it is, where assigning an equal new value would rewrite
				-- it -- for every row the merge has nothing for, which is every Codex
				-- row, every metadata line, and every row on any pass after the first.
				raw = CASE
					WHEN COALESCE(NULLIF(agent, ''), 'claude') = 'claude'
						AND jsonb_typeof(raw) = 'object'
						AND `+reenrichMergedRaw+` <> raw
					THEN `+reenrichMergedRaw+`
					ELSE raw
				END
				-- tool_name/tool_call_id are deliberately absent: uuid='' rows (Codex)
				-- match on (session_id, ts, record_type, profile_email) alone, so
				-- same-millisecond parallel calls can receive each other's call_id,
				-- and fill-only would make the swap permanent. Identity comes from the
				-- original insert only.
			FROM fresh
			WHERE session_id = $1
				AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = session_records.session_id)
				AND ts = $2
				AND record_type = $3
				AND profile_email = $4
				-- A partly re-collected session can hold both the legacy uuid='' row and
				-- its already-enriched twin in the same slot. Writing $5 onto the legacy
				-- row would collide with the twin on uniq_session_record_v2 and abort the
				-- whole pass, so only fill uuid when no twin exists; the enriched row is
				-- the authoritative one and is updated on its own.
				AND (uuid = $5
					OR (uuid = '' AND NOT EXISTS (
						SELECT 1 FROM session_records x
						WHERE x.session_id = $1
							AND x.ts = $2
							AND x.record_type = $3
							AND x.profile_email = $4
							AND x.uuid = $5)))`,
			r.SessionID, r.Ts, r.RecordType, r.ProfileEmail,
			r.UUID, r.ParentUUID, r.IsSidechain, r.AgentID,
			r.ForkedFromSession, r.ForkedFromUUID, r.SourceFile, r.ToolUseID,
			r.IsCompactSummary, r.IsMeta, r.PromptSource, r.Entrypoint,
			r.CommandSource, r.CommandKind, r.CommandInvoke,
			r.RepositoryID, r.RepositoryName, r.RepoSubpath,
			r.AccountID, r.TaskType,
			remoteAuthority, clearSubpath, fillSubpath,
			freshRaw, codexTurnID, codexRootTurnID, r.CodexTurnMetadataKnown,
		)
		if err != nil {
			return updated, fmt.Errorf("reenrich session record: %w", err)
		}
		updated += int(tag.RowsAffected())
	}
	sessionIDs := sessionRecordSessionIDs(records)
	if err := refreshSessionOverviewRollups(ctx, tx, sessionIDs); err != nil {
		return updated, err
	}
	if err := refreshPluginInvocationFacts(ctx, tx, pluginFactSessionIDs(records)); err != nil {
		return updated, err
	}
	if err := refreshTaskSegmentFacts(ctx, tx, sessionIDs); err != nil {
		return updated, err
	}
	// The whole batch, every row of it rescanned for copies: a fill can give an
	// old row the uuid a copy is keyed on (p1b-design §3.16).
	if err := markClaimSourceFactsDirty(ctx, tx, sessionIDs, nil, dirtyReenrich, claimPeers); err != nil {
		return updated, err
	}
	if err := tx.Commit(ctx); err != nil {
		return updated, err
	}
	return updated, nil
}

func (s *PgStore) ListSessionRecords(ctx context.Context, filter SessionRecordFilter) ([]*SessionRecord, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	// uuid tie-breaks the ts sort so keyset/offset pages are deterministic: ts ties are
	// pervasive (many records share a microsecond) and a tie straddling a page boundary
	// would otherwise drop or duplicate a record across pages.

	var conds []string
	var args []interface{}
	n := 0

	if filter.SessionID != "" {
		n++
		conds = append(conds, fmt.Sprintf("session_id = $%d", n))
		args = append(args, filter.SessionID)
	}
	if filter.UserID != "" {
		n++
		conds = append(conds, fmt.Sprintf("user_id = $%d", n))
		args = append(args, filter.UserID)
	}
	if filter.ProfileEmail != "" {
		n++
		conds = append(conds, fmt.Sprintf("profile_email = $%d", n))
		args = append(args, filter.ProfileEmail)
	}
	if filter.LoginEmail != "" {
		n++
		conds = append(conds, fmt.Sprintf("login_email = $%d", n))
		args = append(args, filter.LoginEmail)
	}
	if filter.Conversation {
		conds = append(conds, ConversationProjectionPredicate())
	}
	conds, args = appendSessionInspectionFilters(filter, conds, args)
	start, err := s.sessionRecordsStart(ctx, filter, conds, args)
	if err != nil {
		return nil, err
	}
	if start.Offset < 0 {
		return []*SessionRecord{}, nil
	}
	keyPredicate, pageArgs, pageOffset := sessionRecordPageStart(start, args)
	args = pageArgs
	offset := pageOffset
	if keyPredicate != "" {
		conds = append(conds, keyPredicate)
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	// uuid tie-breaks ts (pervasive ts collisions) so keyset/offset paging is deterministic.
	// asc = oldest-first for the session detail's lazy-load (renders the conversation start
	// immediately, then pages newer records on scroll); default is newest-first.
	orderBy := "ts DESC, uuid DESC, id DESC"
	if filter.Order == "asc" {
		orderBy = "ts ASC, uuid ASC, id ASC"
	}

	q := fmt.Sprintf(`SELECT ts, session_id, project_hash, repository_id, repository_name, repo_subpath, commit_sha, branch,
			record_type, profile_email, model, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, raw,
			uuid, parent_uuid, is_sidechain, agent_id, forked_from_session, forked_from_uuid, source_file, tool_use_id,
			is_compact_summary, is_meta, prompt_source, entrypoint, cctrace_version, task_type,
			agent, tool_name, tool_call_id, COALESCE(message_id, '')
		FROM visible_session_records
		%s
		ORDER BY %s
		LIMIT %d OFFSET %d`, where, orderBy, limit, offset)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*SessionRecord, 0)
	for rows.Next() {
		rec := &SessionRecord{}
		var rawJSON []byte
		if err := rows.Scan(
			&rec.Ts, &rec.SessionID, &rec.ProjectHash, &rec.RepositoryID, &rec.RepositoryName,
			&rec.RepoSubpath, &rec.CommitSHA, &rec.Branch, &rec.RecordType,
			&rec.ProfileEmail, &rec.Model, &rec.InputTokens, &rec.OutputTokens,
			&rec.CacheReadTokens, &rec.CacheCreateTokens, &rawJSON,
			&rec.UUID, &rec.ParentUUID, &rec.IsSidechain, &rec.AgentID,
			&rec.ForkedFromSession, &rec.ForkedFromUUID, &rec.SourceFile, &rec.ToolUseID,
			&rec.IsCompactSummary, &rec.IsMeta, &rec.PromptSource, &rec.Entrypoint, &rec.CctraceVersion, &rec.TaskType,
			&rec.Agent, &rec.ToolName, &rec.ToolCallID, &rec.MessageID,
		); err != nil {
			return nil, err
		}
		rec.Raw = json.RawMessage(rawJSON)
		result = append(result, rec)
	}
	return result, rows.Err()
}

// ListSessionLineage returns every record in the session's lineage, chronologically.
// Subagent files (/btw, Task) already share the anchor session_id, so they come along
// with a plain session_id match; only /branch and /clear create a new session_id, which
// is linked upward via forked_from_session. The same access scope (user_id/profile_email)
// is applied at every hop so lineage never crosses into another user's sessions.
func (s *PgStore) ListSessionLineage(ctx context.Context, filter SessionRecordFilter) ([]*SessionRecord, error) {
	if filter.SessionID == "" {
		return s.ListSessionRecords(ctx, filter)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 5000 {
		limit = 5000
	}
	// Scope predicates are identical for lineage discovery and final rows.
	scope := []struct{ column, value string }{{"user_id", filter.UserID}, {"profile_email", filter.ProfileEmail}, {"login_email", filter.LoginEmail}}
	appendScope := func(q string, args []interface{}) (string, []interface{}) {
		for _, v := range scope {
			if v.value != "" {
				args = append(args, v.value)
				q += " AND " + v.column + fmt.Sprintf(" = $%d", len(args))
			}
		}
		return q, args
	}

	// Walk up via forked_from_session to collect the branch/clear ancestors — the
	// shared prefix the anchor inherited. Descendants are NOT pulled in: a branch is
	// a separate session shown on its own, with the shared history prepended.
	// Subagent files already share a session_id, so they need no walk.
	chain := make([]string, 0, 4)
	seen := map[string]bool{}
	queue := []string{filter.SessionID}
	for len(queue) > 0 && len(chain) < 100 {
		sid := queue[0]
		queue = queue[1:]
		if seen[sid] {
			continue
		}
		seen[sid] = true
		chain = append(chain, sid)

		{
			q := "SELECT DISTINCT forked_from_session FROM visible_session_records WHERE session_id = $1 AND forked_from_session <> ''"
			args := []interface{}{sid}
			q, args = appendScope(q, args)
			neighbors, err := s.pool.Query(ctx, q, args...)
			if err != nil {
				return nil, err
			}
			var found []string
			for neighbors.Next() {
				var nb string
				if err := neighbors.Scan(&nb); err != nil {
					neighbors.Close()
					return nil, err
				}
				if nb != "" && !seen[nb] {
					found = append(found, nb)
				}
			}
			neighbors.Close()
			if err := neighbors.Err(); err != nil {
				return nil, err
			}
			queue = append(queue, found...)
		}
	}

	q := `SELECT ts, session_id, project_hash, repository_id, repository_name, repo_subpath, commit_sha, branch,
			record_type, profile_email, model, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, raw,
			uuid, parent_uuid, is_sidechain, agent_id, forked_from_session, forked_from_uuid, source_file, tool_use_id,
			is_compact_summary, is_meta, prompt_source, entrypoint, cctrace_version,
			agent, tool_name, tool_call_id, COALESCE(message_id, '')
		FROM visible_session_records
		WHERE session_id = ANY($1)`
	args := []interface{}{chain}
	q, args = appendScope(q, args)
	if filter.Conversation {
		q += " AND " + ConversationProjectionPredicate()
	}
	// Reuse the same access predicates for locating the start and reading its page.
	startConds := []string{"session_id = ANY($1)"}
	for _, v := range scope {
		if v.value != "" {
			startConds = append(startConds, fmt.Sprintf("%s = $%d", v.column, len(startConds)+1))
		}
	}
	extra, inspectedArgs := appendSessionInspectionFilters(filter, nil, args)
	if len(extra) > 0 {
		q += " AND " + strings.Join(extra, " AND ")
		startConds = append(startConds, extra...)
	}
	args = inspectedArgs
	if filter.Conversation {
		startConds = append(startConds, ConversationProjectionPredicate())
	}
	start, startErr := s.sessionRecordsStart(ctx, filter, startConds, args)
	if startErr != nil {
		return nil, startErr
	}
	if start.Offset < 0 {
		return []*SessionRecord{}, nil
	}
	keyPredicate, pageArgs, pageOffset := sessionRecordPageStart(start, args)
	args = pageArgs
	offset := pageOffset
	if keyPredicate != "" {
		q += " AND " + keyPredicate
	}
	q += fmt.Sprintf(" ORDER BY ts ASC, uuid ASC, id ASC LIMIT %d OFFSET %d", limit, offset)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*SessionRecord, 0)
	for rows.Next() {
		rec := &SessionRecord{}
		var rawJSON []byte
		if err := rows.Scan(
			&rec.Ts, &rec.SessionID, &rec.ProjectHash, &rec.RepositoryID, &rec.RepositoryName,
			&rec.RepoSubpath, &rec.CommitSHA, &rec.Branch, &rec.RecordType,
			&rec.ProfileEmail, &rec.Model, &rec.InputTokens, &rec.OutputTokens,
			&rec.CacheReadTokens, &rec.CacheCreateTokens, &rawJSON,
			&rec.UUID, &rec.ParentUUID, &rec.IsSidechain, &rec.AgentID,
			&rec.ForkedFromSession, &rec.ForkedFromUUID, &rec.SourceFile, &rec.ToolUseID,
			&rec.IsCompactSummary, &rec.IsMeta, &rec.PromptSource, &rec.Entrypoint, &rec.CctraceVersion,
			&rec.Agent, &rec.ToolName, &rec.ToolCallID, &rec.MessageID,
		); err != nil {
			return nil, err
		}
		rec.Raw = json.RawMessage(rawJSON)
		result = append(result, rec)
	}
	return result, rows.Err()
}

func (s *PgStore) ListSessionSummaries(ctx context.Context, profileEmail string, userID string, limit int) ([]*SessionSummary, error) {
	if limit <= 0 {
		limit = 100
	}
	conds := []string{"sr.session_id != ''"}
	args := []interface{}{}
	if profileEmail != "" {
		args = append(args, profileEmail)
		conds = append(conds, fmt.Sprintf("sr.profile_email = $%d", len(args)))
	}
	if userID != "" {
		args = append(args, userID)
		conds = append(conds, fmt.Sprintf("sr.user_id = $%d", len(args)))
	}
	args = append(args, limit)
	filter := strings.Join(conds, " AND ")
	// The tokens count requests. One pass over the lines the filter keeps --
	// attributedLinesSQL, every request line at 0 -- gives each session's totals,
	// as the query did before the request set, and picks the sessions by their
	// latest line. Only then are their requests folded (claudeRequestHeadsSQL)
	// and their tokens added: the window sorts the listed sessions' request
	// lines, never the filter's whole history. The filter is the lineWhere of
	// both, so the requests are folded from the lines it keeps, and the sum is
	// attributedRowsSQL's.
	//
	// The listed sessions reach the fragments as an array, so each chunk is read
	// through its index on session_id. As IN (SELECT ...) they could also be met
	// by a semi-join above a parallel scan of the filter's every row (P1t-8: 0.97
	// million rows read again to keep 0.14 million).
	//
	// The representative profile is the one that produced the most tokens, and the
	// account count says whether the session's totals span more than one account.
	// MAX() chose the profile by string order and reported nothing about the
	// account, so a session split across two accounts looked like one account's.
	listed := "sr.session_id = ANY(ARRAY(SELECT session_id FROM totals))"
	q := `WITH totals AS (
    SELECT sr.session_id,
        MAX(sr.ts) AS last_ts,
        MAX(sr.project_hash) AS project_hash,
        COALESCE(MAX(p.project_name), '') AS project_name,
        COALESCE(MAX(CASE WHEN sr.model != '' THEN sr.model ELSE NULL END), '') AS model,
        ` + sessionStartExpr("sr.ts") + ` AS start_time,
        ` + sessionEndExpr("sr.ts") + ` AS end_time,
        COALESCE(SUM(sr.req_input_tokens), 0) AS input_tokens,
        COALESCE(SUM(sr.req_output_tokens), 0) AS output_tokens,
        COUNT(*) AS record_count
    FROM (` + attributedLinesSQL("visible_session_records", filter,
		[]string{"session_id", "ts", "agent", "project_hash", "model"}) + `) sr
    LEFT JOIN projects p ON p.agent = COALESCE(NULLIF(sr.agent, ''), 'claude') AND p.project_hash = sr.project_hash
    GROUP BY sr.session_id
    ORDER BY MAX(sr.ts) DESC
    LIMIT $` + fmt.Sprint(len(args)) + `
), dominant AS (
    SELECT DISTINCT ON (p2.session_id) p2.session_id, p2.profile_email
    FROM (
        SELECT l.session_id, l.profile_email, l.req_input_tokens, l.req_output_tokens
        FROM (` + attributedLinesSQL("visible_session_records", listed+" AND sr.profile_email <> ''",
		[]string{"session_id", "profile_email"}) + `) l
        UNION ALL
        SELECT h.session_id, h.profile_email, h.input_tokens, h.output_tokens
        FROM (` + claudeRequestHeadsSQL("visible_session_records", listed+" AND sr.profile_email <> ''",
		[]string{"session_id", "profile_email"}) + `) h
    ) p2
    GROUP BY p2.session_id, p2.profile_email
    ORDER BY p2.session_id, SUM(COALESCE(p2.req_input_tokens,0) + COALESCE(p2.req_output_tokens,0)) DESC, p2.profile_email
), request_tokens AS (
    SELECT h.session_id, SUM(h.input_tokens) AS input_tokens, SUM(h.output_tokens) AS output_tokens
    FROM (` + claudeRequestHeadsSQL("visible_session_records", listed+" AND "+filter, []string{"session_id"}) + `) h
    GROUP BY h.session_id
)
SELECT
    t.session_id,
    COALESCE(d.profile_email, '') as profile_email,
    (SELECT COUNT(DISTINCT NULLIF(CASE WHEN COALESCE(a2.agent,'claude') = 'codex'
                                       THEN a2.account_id ELSE a2.login_email END, ''))
       FROM visible_session_records a2 WHERE a2.session_id = t.session_id) as account_count,
    t.project_hash,
    t.project_name,
    t.model,
    t.start_time,
    t.end_time,
    t.input_tokens + COALESCE(rt.input_tokens, 0) as input_tokens,
    t.output_tokens + COALESCE(rt.output_tokens, 0) as output_tokens,
    t.record_count
FROM totals t
LEFT JOIN dominant d ON d.session_id = t.session_id
LEFT JOIN request_tokens rt ON rt.session_id = t.session_id
ORDER BY t.last_ts DESC`

	rows, err := s.pool.Query(ctx, q, append([]any{plannedPerCall}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*SessionSummary, 0)
	for rows.Next() {
		ss := &SessionSummary{}
		if err := rows.Scan(&ss.SessionID, &ss.ProfileEmail, &ss.AccountCount, &ss.ProjectHash,
			&ss.ProjectName, &ss.Model, &ss.StartTime, &ss.EndTime,
			&ss.InputTokens, &ss.OutputTokens, &ss.RecordCount); err != nil {
			return nil, err
		}
		result = append(result, ss)
	}
	return result, rows.Err()
}

// CountSessionOverviews returns the total number of sessions matching the same scope
// and source filter as ListSessionOverviews (ignoring limit/offset), so the UI can show
// "N / total" progress against a paged, infinitely-scrolled list.
// sessionInteractiveCol is the interactive test in column form, evaluated over a grouped
// CTE that exposes entrypoint / has_enriched / has_genuine (see the aggregate definitions
// in Count/ListSessionOverviews). A session is NOT interactive when it is headless
// (claude -p, entrypoint=sdk-cli) or when it is enriched (post-update, has source_file)
// yet has no real human turn — e.g. omc warmup/summary one-shots that are queue/SDK
// injected (all prompt_source empty). Legacy sessions (no source_file, so entrypoint
// defaults to "") stay interactive. Computing these as aggregates in the single GROUP BY
// scan avoids the per-session correlated sub-queries that fanned out across every
// TimescaleDB chunk (the dominant cost of the session list query).
// Codex is classified on entrypoint alone: it emits no prompt_source, so has_genuine is
// always false and the Claude test would bucket every enriched Codex session as headless
// while un-enriched ones slipped into interactive through the legacy escape hatch. The
// syncer derives entrypoint from Codex's originator — 'cli' is the human TUI, anything
// else names an automated launcher (codex_exec, codex_sdk_ts, a delegating harness).
// An empty entrypoint is a pre-originator rollout whose launcher is unknown, so it keeps
// the legacy benefit of the doubt until it is reenriched.
//
// gjc and omo fall through to the ELSE branch and land in headless, always. That is a
// decision, not an oversight, and it is deliberately not fixed here. The ELSE test asks
// for a genuine human turn once a session is enriched, which assumes an agent that emits
// prompt_source; gjc reports one (its syncer maps attribution=user to 'typed') but omo has
// no per-record human-turn signal at all, so has_genuine can never become true for it.
// A correct fix means changing the classification axis for every agent, and that was
// weighed and declined: these two tools are collected to keep their token and cost figures
// from going missing, not to be browsed as conversations. Reclassifying them would put
// Claude and Codex behavior at risk for a use nobody has asked for.
// So: do not "repair" this by special-casing gjc or omo. If session browsing for them ever
// becomes a real requirement, that is a scope decision to reopen, not a bug to patch.

// appendAgentFilter adds the harness-scope predicate for a session list, if any.
//
// The scope is the harness that ran the session -- Claude Code, Codex, or one of the
// others -- and NOT who was billed for it. Those are independent: Claude Code and Codex
// are harnesses you can plug another model into, so a claude session can be billed to a
// compatible provider, and a single gjc or omo session can mix anthropic, openai and
// other billing inside itself. Billing is filtered separately, on billing_provider.
//
// "other" is every harness that is not one of the two named ones, expressed as NOT IN
// rather than a list of the rest: a harness added later belongs there the day it starts
// syncing, without anyone remembering to extend an enumeration. Today that is gjc and
// omo -- an enumeration would have had to be edited for each of them, and this file has
// already been the place where an enumeration was missed.
//
// Shared by Count and List so the paged list and the total it is shown next to cannot
// disagree about what the selection means.
func appendAgentFilter(agent string, n *int, args *[]interface{}) string {
	return appendAgentScope("agent", agent, n, args)
}

// appendAgentScope is appendAgentFilter's rule over a named column, for the queries
// that reach the same agent column through a table alias or a per-arm format string.
//
// It exists because the scope vocabulary and the stored vocabulary are not the same
// one: the pills say "other", the rows say "omo" or "gjc", and nothing is ever stored
// as "other". Every query that compared f.Agent directly therefore answered zero for
// that scope while the session list, which went through appendAgentFilter, answered
// with real sessions -- the same data reported two ways depending on which path the
// page happened to take. Route every agent-scope predicate through here so the
// translation exists in one place and a harness added later needs no second edit.
func appendAgentScope(col, agent string, n *int, args *[]interface{}) string {
	switch agent {
	case "":
		return ""
	case agentScopeOther:
		// weekly is named too: it is the server's report runs, not a harness anyone ran.
		return fmt.Sprintf("%s NOT IN ('%s', '%s', '%s')", col, agentClaude, agentCodex, agentWeekly)
	default:
		*n++
		*args = append(*args, agent)
		return fmt.Sprintf("%s = $%d", col, *n)
	}
}

const (
	agentScopeOther = "other"
	agentClaude     = "claude"
	agentCodex      = "codex"
)

func sessionInteractiveCol() string {
	return `CASE WHEN agent = 'codex'
		THEN entrypoint IN ('', 'cli')
		ELSE (entrypoint <> 'sdk-cli' AND (NOT has_enriched OR has_genuine))
	END`
}

// sessionOverviewFilters returns the classification predicates (over the grouped CTE) for
// the source filter, shared so Count and List classify identically.
func sessionOverviewFilters(source string) []string {
	switch source {
	case "interactive":
		return []string{sessionInteractiveCol()}
	case "headless":
		return []string{"NOT " + sessionInteractiveCol()}
	}
	return nil
}

func (s *PgStore) CountSessionOverviews(ctx context.Context, f SessionOverviewFilter) (int, error) {
	if f.IncludeAnalysis {
		return s.countSessionOverviewAnalysis(ctx, f)
	}
	if f.Classification != "" && !validPersonalClass(f.Classification) {
		return 0, ErrPersonalInvalid
	}
	if f.Since == nil && f.Until == nil {
		ready, err := s.sessionOverviewRollupsReady(ctx)
		if err != nil {
			return 0, err
		}
		if ready {
			return s.countSessionOverviewRollups(ctx, f)
		}
	}
	return s.countSessionOverviewsPeriod(ctx, f)
}

func (s *PgStore) countSessionOverviewsPeriod(ctx context.Context, f SessionOverviewFilter) (int, error) {
	q, args := sessionOverviewPeriodCountQuery(f, false)
	if q == "" {
		return 0, nil
	}
	var cnt int
	if err := s.pool.QueryRow(ctx, q, args...).Scan(&cnt); err != nil {
		return 0, err
	}
	return cnt, nil
}

func sessionOverviewPeriodCountQuery(f SessionOverviewFilter, buckets bool) (string, []interface{}) {
	var conds, srConds []string
	var args []interface{}
	n := 0
	if f.UserID != "" {
		n++
		conds = append(conds, fmt.Sprintf("e.user_id = $%d", n))
		srConds = append(srConds, fmt.Sprintf("sr.user_id = $%d", n))
		args = append(args, f.UserID)
	} else if f.ProfileEmail != "" {
		n++
		conds = append(conds, fmt.Sprintf("e.profile_email = $%d", n))
		srConds = append(srConds, fmt.Sprintf("sr.profile_email = $%d", n))
		args = append(args, f.ProfileEmail)
	} else if f.LoginEmail != "" && !f.OnlyUnattributed {
		// Skipped when counting unattributed sessions: they match no account, so
		// the equality would return zero and answer a different question.
		n++
		conds = append(conds, fmt.Sprintf("e.login_email = $%d", n))
		srConds = append(srConds, fmt.Sprintf("sr.login_email = $%d", n))
		args = append(args, f.LoginEmail)
	}
	if f.Since != nil {
		n++
		conds = append(conds, fmt.Sprintf("e.ts >= $%d", n))
		srConds = append(srConds, fmt.Sprintf("sr.ts >= $%d", n))
		args = append(args, *f.Since)
	}
	if f.Until != nil {
		n++
		conds = append(conds, fmt.Sprintf("e.ts < $%d", n))
		srConds = append(srConds, fmt.Sprintf("sr.ts < $%d", n))
		args = append(args, *f.Until)
	}
	// The events arm has to refuse deleted sessions itself.
	//
	// The list is a UNION of visible_session_records and visible_events. The first
	// already excludes them -- it anti-joins excluded_sessions, which is where a delete
	// writes -- but visible_events does not, so a deleted session walked back into the
	// list through its own telemetry and the delete looked like it had done nothing.
	//
	// Filtered here rather than inside visible_events: that view feeds every cost and
	// usage rollup in the product, and putting the anti-join there measured 72ms -> 161ms
	// on a seven-day aggregate. This is the one query that has to be exact, because it
	// is the one a person watches for the row to disappear.
	whereClause := "WHERE e.session_id != '' AND NOT EXISTS (" +
		"SELECT 1 FROM excluded_sessions xs WHERE xs.session_id = e.session_id)"
	if len(conds) > 0 {
		whereClause += " AND " + strings.Join(conds, " AND ")
	}
	srWhere := "WHERE sr.session_id != ''"
	if len(srConds) > 0 {
		srWhere += " AND " + strings.Join(srConds, " AND ")
	}
	// Project and agent are filtered server-side (on the grouped project_hash/agent) so the
	// count matches the paged list — a client-side filter would only see the loaded pages.
	var filters []string
	if f.FoldLineage {
		filters = append(filters, "NOT has_fork")
	}
	// Sessions with neither an api_request (real OTEL usage) nor any session_records
	// (JSONL sync) are empty shells (e.g. only hook_registered/plugin_loaded startup
	// telemetry) with nothing to show; excluded here so the count matches the list below.
	filters = append(filters, "(has_api_request OR has_sync)")
	if f.OnlyUnattributed {
		filters = append(filters, "NOT has_account")
	}
	filters = append(filters, sessionOverviewFilters(f.Source)...)
	if len(f.ProjectHashes) > 0 {
		n++
		filters = append(filters, fmt.Sprintf("project_hash = ANY($%d)", n))
		args = append(args, f.ProjectHashes)
	}
	if clause := appendAgentFilter(f.Agent, &n, &args); clause != "" {
		filters = append(filters, clause)
	}
	where := ""
	if len(filters) > 0 {
		where = "WHERE " + strings.Join(filters, " AND ")
	}
	// Classify/filter with aggregates in the single GROUP BY scan (has_enriched / has_genuine
	// / entrypoint / project_hash / agent) instead of correlated sub-queries per session.
	base := fmt.Sprintf(`WITH session_union AS (
			SELECT session_id, ''::text AS source_file, ''::text AS prompt_source, ''::text AS entrypoint, ''::text AS project_hash, COALESCE(agent,'claude') AS agent, ''::text AS forked_from_session, ''::text AS src, event_name, COALESCE(login_email,'') AS login_email FROM visible_events e %s
			UNION ALL
			SELECT session_id, COALESCE(source_file,''), COALESCE(prompt_source,''), COALESCE(entrypoint,''), COALESCE(project_hash,''), COALESCE(agent,'claude'), COALESCE(forked_from_session,''), 'srec'::text, ''::text, COALESCE(login_email,'') FROM visible_session_records sr %s
		),
		grouped AS (
			SELECT session_id,
				bool_or(source_file <> '') AS has_enriched,
				bool_or(prompt_source IN ('typed','paste','queued','suggestion_accepted')) AS has_genuine,
				COALESCE(max(entrypoint) FILTER (WHERE entrypoint <> ''), '') AS entrypoint,
				COALESCE(max(project_hash) FILTER (WHERE project_hash <> ''), '') AS project_hash,
				COALESCE(max(agent), 'claude') AS agent,
				bool_or(forked_from_session <> '') AS has_fork,
				bool_or(src = 'srec') AS has_sync,
				bool_or(event_name = 'api_request') AS has_api_request,
				-- An inferred account counts as an account; the same expression in
				-- postgres_session_overview_rollup.go carries the argument. The two
				-- have to agree: this counts what the rollup path lists.
				bool_or(login_email <> '') AS has_account
			FROM session_union GROUP BY session_id
		)`, whereClause, srWhere)
	if f.Classification == "" && !buckets {
		return base + ` SELECT count(*) FROM grouped ` + where, args
	}
	personalSQL, personalArgs := personalOverviewClassificationSQL(f, n+1)
	if personalSQL == "" {
		return "", nil
	}
	args = append(args, personalArgs...)
	q := base + `, candidate AS (SELECT session_id FROM grouped ` + where + `)` + personalSQL
	if buckets {
		return q + ` SELECT classification,count(*) FROM personal_classified GROUP BY classification`, args
	}
	args = append(args, f.Classification)
	return q + fmt.Sprintf(` SELECT count(*) FROM personal_classified WHERE classification=$%d`, len(args)), args
}

func personalPeriodBucketsQuery(f SessionOverviewFilter) (string, []interface{}) {
	return sessionOverviewPeriodCountQuery(f, true)
}

func sessionOverviewPeriodListQuery(f SessionOverviewFilter) (string, []interface{}, bool) {
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	var conds []string
	var srConds []string
	var args []interface{}
	n := 0

	if f.UserID != "" {
		n++
		conds = append(conds, fmt.Sprintf("e.user_id = $%d", n))
		srConds = append(srConds, fmt.Sprintf("sr.user_id = $%d", n))
		args = append(args, f.UserID)
	} else if f.ProfileEmail != "" {
		n++
		conds = append(conds, fmt.Sprintf("e.profile_email = $%d", n))
		srConds = append(srConds, fmt.Sprintf("sr.profile_email = $%d", n))
		args = append(args, f.ProfileEmail)
	} else if f.LoginEmail != "" {
		n++
		conds = append(conds, fmt.Sprintf("e.login_email = $%d", n))
		srConds = append(srConds, fmt.Sprintf("sr.login_email = $%d", n))
		args = append(args, f.LoginEmail)
	}
	if f.Since != nil {
		n++
		conds = append(conds, fmt.Sprintf("e.ts >= $%d", n))
		srConds = append(srConds, fmt.Sprintf("sr.ts >= $%d", n))
		args = append(args, *f.Since)
	}
	if f.Until != nil {
		n++
		conds = append(conds, fmt.Sprintf("e.ts < $%d", n))
		srConds = append(srConds, fmt.Sprintf("sr.ts < $%d", n))
		args = append(args, *f.Until)
	}

	// The events arm has to refuse deleted sessions itself.
	//
	// The list is a UNION of visible_session_records and visible_events. The first
	// already excludes them -- it anti-joins excluded_sessions, which is where a delete
	// writes -- but visible_events does not, so a deleted session walked back into the
	// list through its own telemetry and the delete looked like it had done nothing.
	//
	// Filtered here rather than inside visible_events: that view feeds every cost and
	// usage rollup in the product, and putting the anti-join there measured 72ms -> 161ms
	// on a seven-day aggregate. This is the one query that has to be exact, because it
	// is the one a person watches for the row to disappear.
	whereClause := "WHERE e.session_id != '' AND NOT EXISTS (" +
		"SELECT 1 FROM excluded_sessions xs WHERE xs.session_id = e.session_id)"
	if len(conds) > 0 {
		whereClause += " AND " + strings.Join(conds, " AND ")
	}
	// The session records count requests. Their arms of session_union are
	// attributedLinesUnionSQL's, every request line at 0, so the union keeps its one
	// parallel scan; request_tokens below is each request's tokens, from its head
	// line (claudeRequestHeadsSQL), and acct and grouped add them back. Together
	// they sum to attributedRowsSQL's tokens. The identity and period predicates are
	// the lineWhere of both, so the requests are folded from the lines the filter
	// keeps, and only those lines are sorted to fold them.
	srLineWhere := "TRUE"
	if len(srConds) > 0 {
		srLineWhere = strings.Join(srConds, " AND ")
	}
	srSource := attributedLinesUnionSQL("visible_session_records", srLineWhere, []string{
		"session_id", "profile_email", "user_id", "login_email", "model", "agent", "ts", "source_file",
		"prompt_source", "entrypoint", "forked_from_session", "project_hash", "cctrace_version",
		"account_id", "login_email_source"},
		`session_id, profile_email, COALESCE(user_id,''), COALESCE(login_email,''), COALESCE(model,''), COALESCE(agent,'claude'), ts,
			'srec', COALESCE(source_file,''), COALESCE(prompt_source,''), COALESCE(entrypoint,''), COALESCE(forked_from_session,''), COALESCE(project_hash,''),
			req_input_tokens, req_output_tokens, 0::double precision, ''::text AS event_name,
			COALESCE(cctrace_version,''), ''::text AS claude_version, COALESCE(account_id,''),
			COALESCE(login_email_source,'')`,
		"sr.session_id != ''")
	srHeads := claudeRequestHeadsSQL("visible_session_records", srLineWhere, []string{"session_id", "login_email"})

	pageClause := ""
	if !f.IncludeAnalysis {
		n++
		limitClause := fmt.Sprintf("$%d", n)
		args = append(args, limit)
		n++
		offsetClause := fmt.Sprintf("$%d", n)
		args = append(args, offset)
		pageClause = "LIMIT " + limitClause + " OFFSET " + offsetClause
	}

	// Assembled list mode folds branch/clear children into their root; the source filter
	// keeps headless/automated one-shots from crowding the LIMIT so interactive sessions
	// aren't squeezed out. Both are computed as aggregates in the grouped CTE (has_fork /
	// entrypoint / has_enriched / has_genuine) and applied in the outer WHERE, so the LIMIT
	// counts only the sessions that will be shown — without a correlated sub-query per
	// session (the old form fanned out across every TimescaleDB chunk, ~4s of the query).
	var wheres []string
	if f.FoldLineage {
		wheres = append(wheres, "NOT has_fork")
	}
	// Sessions with neither an api_request (real OTEL usage) nor any session_records
	// (JSONL sync) are empty shells (e.g. only hook_registered/plugin_loaded startup
	// telemetry) with nothing to show; excluded here so the list matches the count above.
	wheres = append(wheres, "(has_api_request OR has_sync)")
	wheres = append(wheres, sessionOverviewFilters(f.Source)...)
	// Project/agent server-side (grouped project_hash/agent) so the paged list matches the
	// count and doesn't miss a project's sessions that sit past the loaded pages.
	if len(f.ProjectHashes) > 0 {
		n++
		wheres = append(wheres, fmt.Sprintf("project_hash = ANY($%d)", n))
		args = append(args, f.ProjectHashes)
	}
	if clause := appendAgentFilter(f.Agent, &n, &args); clause != "" {
		wheres = append(wheres, clause)
	}
	whereFilter := ""
	if len(wheres) > 0 {
		whereFilter = "WHERE " + strings.Join(wheres, " AND ")
	}

	// One pass over the union (otel_events via unified_events + session_records), tagged by
	// `src`, computes everything as aggregates: classification booleans, entrypoint, and the
	// token fallback. Token fallback = otel sum, else session_records sum (COALESCE of two
	// FILTERed sums — never a double count; matches the prior otel-first semantics). cost is
	// otel-only. project_name is resolved with a LATERAL LIMIT 1 (projects is 1:1 on
	// agent+project_hash, but LATERAL keeps the row count exact regardless).
	q := fmt.Sprintf(`WITH session_union AS (
		SELECT session_id, profile_email, user_id, login_email, model, agent, ts,
			'otel' AS src, ''::text AS source_file, ''::text AS prompt_source, ''::text AS entrypoint, ''::text AS forked_from_session, ''::text AS project_hash,
			input_tokens, output_tokens, cost_usd::double precision AS cost_usd, event_name,
			''::text AS cctrace_version, service_version AS claude_version, ''::text AS account_id,
			-- otel_events has no provenance column and needs none: a row here IS the
			-- observation, so it can never be 'inferred'.
			''::text AS login_email_source
		FROM visible_events e %s
		UNION ALL
		%s
	),
	-- The requests' tokens, by session and the head line's login: a request line
	-- is a Claude row, so its account is that login.
	request_tokens AS (
		SELECT session_id, COALESCE(login_email,'') AS login_email,
			SUM(input_tokens) AS input_tokens, SUM(output_tokens) AS output_tokens,
			COALESCE(SUM(input_tokens),0) + COALESCE(SUM(output_tokens),0) AS toks
		FROM (%s) h WHERE h.session_id != ''
		GROUP BY 1, 2
	),
	-- bigint, as grouped's sums of the rows' integers are: a sum of sums is
	-- numeric, and the list's two token columns would come back numeric with it.
	session_request_tokens AS (
		SELECT session_id, SUM(input_tokens)::bigint AS input_tokens, SUM(output_tokens)::bigint AS output_tokens
		FROM request_tokens GROUP BY session_id
	),
	-- Account identity per agent. Codex has no Anthropic login, so its identity
	-- is the account uuid; Claude's is the login email, which OTEL always carries
	-- while synced rows may also carry the same account's uuid. Counting distinct
	-- values across both columns would report one Claude account as two.
	--
	-- A request's tokens join the group of its head's login. session_union's
	-- request lines carry 0 and keep every other column, so that group is already
	-- here: the join adds tokens, never a group or a login, and a request whose
	-- head has no login meets no group, as its head line meets none.
	acct AS (
		SELECT a.session_id, a.account_key, a.toks + COALESCE(rt.toks, 0) AS toks, a.login_email
		FROM (
			SELECT session_id,
				CASE WHEN agent = 'codex' THEN account_id ELSE login_email END AS account_key,
				SUM(COALESCE(input_tokens,0) + COALESCE(output_tokens,0)) AS toks,
				MAX(login_email) AS login_email
			FROM session_union
			WHERE COALESCE(NULLIF(CASE WHEN agent = 'codex' THEN account_id ELSE login_email END, ''), '') <> ''
			GROUP BY session_id, 2
		) a
		LEFT JOIN request_tokens rt ON rt.session_id = a.session_id AND rt.login_email = a.account_key
	),
	-- The representative account is the one that used the most tokens. MAX() chose
	-- by string order instead, which is arbitrary and moves on an unrelated rename.
	top_acct AS (
		SELECT DISTINCT ON (session_id) session_id, login_email
		FROM acct ORDER BY session_id, toks DESC, account_key
	),
	acct_count AS (
		SELECT session_id, COUNT(*) AS account_count FROM acct GROUP BY session_id
	),
	grouped AS (
		SELECT
			s.session_id,
			MAX(s.profile_email) AS profile_email,
			COALESCE(MAX(s.user_id), '') AS user_id,
			COALESCE(MAX(ta.login_email), '') AS login_email,
			COALESCE(MAX(ac.account_count), 0) AS account_count,
			COALESCE(MAX(CASE WHEN s.model != '' THEN s.model END), '') AS model,
			COALESCE(MAX(s.agent), 'claude') AS agent,
			`+sessionStartExpr("s.ts")+` AS start_time,
			`+sessionEndExpr("s.ts")+` AS end_time,
			-- The session-record sum is its rows' plus its requests'. A session with
			-- request tokens has its request lines among its rows, at 0, so the rows'
			-- SUM is not NULL there and the addition loses nothing.
			COALESCE(NULLIF(SUM(s.input_tokens) FILTER (WHERE s.src = 'otel'), 0),
				SUM(s.input_tokens) FILTER (WHERE s.src = 'srec') + COALESCE(MAX(rt.input_tokens), 0), 0) AS input_tokens,
			COALESCE(NULLIF(SUM(s.output_tokens) FILTER (WHERE s.src = 'otel'), 0),
				SUM(s.output_tokens) FILTER (WHERE s.src = 'srec') + COALESCE(MAX(rt.output_tokens), 0), 0) AS output_tokens,
			COALESCE(SUM(s.cost_usd) FILTER (WHERE s.src = 'otel'), 0) AS cost_usd,
			COUNT(*) AS event_count,
			bool_or(s.src = 'srec') AS has_sync,
			bool_or(s.event_name = 'api_request') AS has_api_request,
			COALESCE(MAX(s.project_hash) FILTER (WHERE s.project_hash <> ''), '') AS project_hash,
			COALESCE(MAX(s.entrypoint) FILTER (WHERE s.entrypoint <> ''), '') AS entrypoint,
			bool_or(s.source_file <> '') AS has_enriched,
			bool_or(s.prompt_source IN ('typed','paste','queued','suggestion_accepted')) AS has_genuine,
			bool_or(s.forked_from_session <> '') AS has_fork,
			COALESCE(MAX(s.cctrace_version) FILTER (WHERE s.cctrace_version <> ''), '') AS cctrace_version,
			COALESCE(MAX(s.claude_version) FILTER (WHERE s.claude_version <> ''), '') AS claude_version,
			-- Any inferred row makes the session's account inferred; see SessionOverview.
			`+loginEmailInferredExpr+` AS login_email_inferred
		FROM session_union s
		LEFT JOIN top_acct ta ON ta.session_id = s.session_id
		LEFT JOIN acct_count ac ON ac.session_id = s.session_id
		LEFT JOIN session_request_tokens rt ON rt.session_id = s.session_id
		GROUP BY s.session_id
	)
	SELECT
		g.session_id, g.profile_email, g.user_id, g.login_email, g.account_count, g.model, g.agent,
		g.start_time, g.end_time, g.input_tokens, g.output_tokens, g.cost_usd,
		g.event_count, g.has_sync, g.project_hash,
		COALESCE(pn.project_name, '') AS project_name,
		g.entrypoint, g.has_enriched, g.cctrace_version, g.claude_version,
		g.login_email_inferred
	FROM grouped g
	LEFT JOIN LATERAL (
		SELECT p.project_name FROM projects p
		WHERE p.project_hash = g.project_hash AND p.agent = COALESCE(NULLIF(g.agent, ''), 'claude') AND g.project_hash <> ''
		LIMIT 1
	) pn ON true
	%s
	ORDER BY g.end_time DESC, g.session_id DESC
	%s`, whereClause, srSource, srHeads, whereFilter, pageClause)
	if f.Classification != "" {
		personalSQL, personalArgs := personalOverviewClassificationSQL(f, n+1)
		if personalSQL == "" {
			return "", nil, false
		}
		q = strings.Replace(q, "\n\tSELECT\n\t\tg.session_id", "\n\t,candidate AS (SELECT session_id FROM grouped g "+whereFilter+")"+personalSQL+"\n\tSELECT\n\t\tg.session_id", 1)
		args = append(args, personalArgs...)
		args = append(args, f.Classification)
		q = strings.Replace(q, "\n\tFROM grouped g\n\tLEFT JOIN LATERAL", fmt.Sprintf("\n\tFROM personal_classified pc JOIN grouped g ON g.session_id=pc.session_id AND pc.classification=$%d\n\tLEFT JOIN LATERAL", len(args)), 1)
	}
	return q, append([]any{plannedPerCall}, args...), true
}

func (s *PgStore) ListSessionOverviews(ctx context.Context, f SessionOverviewFilter) ([]*SessionOverview, error) {
	if f.Classification != "" && !validPersonalClass(f.Classification) {
		return nil, ErrPersonalInvalid
	}
	if f.IncludeAnalysis {
		return s.listSessionOverviewAnalysis(ctx, f)
	}
	if f.Since == nil && f.Until == nil {
		ready, err := s.sessionOverviewRollupsReady(ctx)
		if err != nil {
			return nil, err
		}
		if ready {
			return s.listSessionOverviewRollups(ctx, f)
		}
	}
	q, args, ok := sessionOverviewPeriodListQuery(f)
	if !ok {
		return []*SessionOverview{}, nil
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*SessionOverview, 0)
	for rows.Next() {
		so := &SessionOverview{}
		if err := rows.Scan(
			&so.SessionID, &so.ProfileEmail, &so.UserID, &so.LoginEmail, &so.AccountCount, &so.Model, &so.Agent,
			&so.StartTime, &so.EndTime,
			&so.InputTokens, &so.OutputTokens, &so.CostUSD,
			&so.EventCount, &so.HasSync,
			&so.ProjectHash, &so.ProjectName, &so.Entrypoint, &so.HasEnriched,
			&so.CctraceVersion, &so.ClaudeVersion, &so.LoginEmailInferred,
		); err != nil {
			return nil, err
		}
		result = append(result, so)
	}
	return result, rows.Err()
}
