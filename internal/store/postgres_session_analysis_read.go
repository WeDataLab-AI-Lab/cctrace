package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
)

// SessionAnalysisAccess is the already-authenticated dashboard caller's read scope.
type SessionAnalysisAccess struct {
	UserID                  string
	ProfileEmail            string
	LoginEmail              string
	OwnerUserID             string
	OwnerProfileEmail       string
	DashboardUserID         int64
	User                    bool
	RecordFeatureEnabled    bool
	RecordAdminEnabled      bool
	RecordRuntimeKey        string
	RecordDisclosureVersion string
	RecordRuntimeAvailable  bool
	RecordRuntimeReason     string
	RecordTimezone          string
	RepoStatusOnly          bool
}

var ErrSessionAnalysisNotFound = errors.New("session analysis not found")

var sessionAnalysisVisibleSQL = `NOT EXISTS (SELECT 1 FROM excluded_sessions x WHERE x.session_id=f.session_id)
 AND NOT EXISTS (SELECT 1 FROM jsonb_to_recordset(f.account_keys)
 AS a(login_email text,billing_provider text,account_id text) WHERE NOT (` + excludedAccountPredicateSQL("a") + `))`

func analysisForeignIdentity(ctx context.Context, tx pgx.Tx, id string, access SessionAnalysisAccess) (bool, error) {
	if !access.User {
		return false, nil
	}
	column, owner := "user_id", access.UserID
	if access.ProfileEmail != "" {
		column, owner = "profile_email", access.ProfileEmail
	}
	if owner == "" || owner == "__no_access__" {
		return true, nil
	}
	var foreign bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM visible_session_records r WHERE r.session_id=$1 AND r.`+column+` IS DISTINCT FROM $2)
 OR EXISTS(SELECT 1 FROM visible_events e WHERE e.session_id=$1 AND e.event_name='api_request' AND e.`+column+` IS DISTINCT FROM $2)
 OR EXISTS(SELECT 1 FROM session_snapshots ss WHERE `+sessionFactSnapshotVisibleSQL+` AND ss.`+column+` IS DISTINCT FROM $2)`, id, owner).Scan(&foreign)
	return foreign, err
}

func analysisFact(ctx context.Context, tx pgx.Tx, id string, access SessionAnalysisAccess) (*sessionfacts.Session, error) {
	var f sessionfacts.Session
	var detail []byte
	var ownerUID, ownerEmail *string
	var userIDs, profiles, logins []string
	var computed time.Time
	var coverage string
	err := tx.QueryRow(ctx, `SELECT f.agent,f.coverage,f.user_ids,f.profile_emails,f.login_emails,f.owner_user_id,f.owner_profile_email,
 f.login_email_inferred,f.project_hash,f.repository_key,f.branch,f.model,f.transcript,f.title,f.title_source,f.title_state,
 f.first_activity_at,f.last_activity_at,f.active_seconds,f.turn_boundaries,f.turn_boundaries_reason,f.turns,f.requests,f.inputs,
 f.abandoned_turns,f.api_requests,f.input_tokens,f.output_tokens,f.cache_read_tokens,f.cache_create_tokens,f.cost_usd,f.cost_source,
 f.last_turn_key,f.last_turn_status,f.last_context_tokens,f.last_context_window,f.last_context_source,f.large_window_seen,f.open_questions,f.open_question_turn_key,f.has_plan_file,
 f.used_plan_mode,f.detail,f.rules_version,f.computed_at
 FROM session_analysis_facts f WHERE f.session_id=$1 AND `+sessionAnalysisVisibleSQL+`
 AND ($2::text='' OR f.owner_user_id=$2) AND ($3::text='' OR f.owner_profile_email=$3)
 AND ($4::text='' OR $4=ANY(f.profile_emails)) AND ($5::text='' OR $5=ANY(f.login_emails))
 AND ($6::text='' OR $6=ANY(f.user_ids))`, id,
		func() string {
			if access.User && access.ProfileEmail == "" {
				return access.UserID
			}
			return ""
		}(),
		func() string {
			if access.User {
				return access.ProfileEmail
			}
			return ""
		}(),
		func() string {
			if !access.User && access.UserID == "" {
				return access.ProfileEmail
			}
			return ""
		}(), func() string {
			if access.User || access.UserID != "" || access.ProfileEmail != "" {
				return ""
			}
			return access.LoginEmail
		}(), func() string {
			if !access.User {
				return access.UserID
			}
			return ""
		}()).
		Scan(&f.Agent, &coverage, &userIDs, &profiles, &logins, &ownerUID, &ownerEmail, &f.LoginEmailInferred,
			&f.ProjectHash, &f.RepositoryKey, &f.Branch, &f.Model, &f.Transcript, &f.Title, &f.TitleSource, &f.TitleState,
			&f.FirstActivityAt, &f.LastActivityAt, &f.ActiveSeconds, &f.TurnBoundaries, &f.TurnBoundariesReason,
			&f.Turns, &f.Requests, &f.Inputs, &f.AbandonedTurns, &f.APIRequests, &f.InputTokens, &f.OutputTokens, &f.CacheReadTokens,
			&f.CacheCreateTokens, &f.CostUSD, &f.CostSource, &f.LastTurnKey, &f.LastTurnStatus, &f.LastContextTokens, &f.LastContextWindow, &f.LastContextSource,
			&f.LargeWindowSeen, &f.OpenQuestions, &f.OpenQuestionTurnKey, &f.HasPlanFile, &f.UsedPlanMode,
			&detail, &f.RulesVersion, &computed)
	if err != nil {
		return nil, err
	}
	f.Coverage = coverage
	f.UserIDs = userIDs
	f.ProfileEmails = profiles
	f.LoginEmails = logins
	f.OwnerUserID = ownerUID
	f.OwnerProfileEmail = ownerEmail
	f.ComputedAt = computed
	if err = json.Unmarshal(detail, &f.Detail); err != nil {
		return nil, err
	}
	return &f, nil
}

func analysisScope(access SessionAnalysisAccess) (string, string) {
	if access.User {
		if access.ProfileEmail != "" {
			return "profile", access.ProfileEmail
		}
		return "user", access.UserID
	}
	if access.UserID != "" {
		return "user", access.UserID
	}
	if access.ProfileEmail != "" {
		return "profile", access.ProfileEmail
	}
	if access.LoginEmail != "" {
		return "login", access.LoginEmail
	}
	return "all", ""
}

func analysisRollup(ctx context.Context, tx pgx.Tx, id string, access SessionAnalysisAccess) (map[string]interface{}, string, error) {
	scope, value := analysisScope(access)
	var agent, model, project string
	var start, end time.Time
	var input, output int64
	var cost float64
	err := tx.QueryRow(ctx, `SELECT r.agent,r.model,r.project_hash,r.start_time,r.end_time,r.input_tokens,r.output_tokens,r.cost_usd
 FROM session_overview_rollups r WHERE r.session_id=$1 AND r.scope_type=$2 AND r.scope_value=$3
 AND (r.has_api_request OR r.has_sync) AND NOT EXISTS(SELECT 1 FROM excluded_sessions x WHERE x.session_id=r.session_id)`, id, scope, value).
		Scan(&agent, &model, &project, &start, &end, &input, &output, &cost)
	if err != nil {
		return nil, "", err
	}
	header := map[string]interface{}{"agent": agent, "model": model, "project": map[string]interface{}{"hash": project},
		"started_at": start, "last_activity_at": end, "tokens": map[string]interface{}{"input": input, "output": output, "basis": "rollup"},
		"cost": map[string]interface{}{"usd": cost}}
	return header, agent, nil
}

func analysisTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}
func analysisString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
func analysisPercent(tokens *int64, window *int) interface{} {
	if tokens == nil || window == nil || *window <= 0 {
		return nil
	}
	return float64(*tokens) * 100 / float64(*window)
}

func analysisTurn(t sessionfacts.Turn) map[string]interface{} {
	d := t.Detail
	if d.Edited.Sample == nil {
		d.Edited.Sample = []string{}
	}
	headers := make([]interface{}, 0, len(d.Questions))
	for _, q := range d.Questions {
		if q.Headers == nil {
			q.Headers = []string{}
		}
		headers = append(headers, map[string]interface{}{"tool_use_id": q.ToolUseID, "headers": q.Headers, "status": q.Status})
	}
	return map[string]interface{}{"key": t.TurnKey, "no": t.Ordinal, "request_no": t.RequestNo, "branch": t.Branch,
		"anchor_ts": t.StartedAt, "started_at": t.StartedAt, "ended_at": t.EndedAt, "kind": t.InputKind, "is_request": t.IsRequest,
		"extra_inputs": d.ExtraInputs, "status": t.Status, "status_reason": analysisString(t.StatusReason),
		"preview": map[string]interface{}{"text": t.Preview, "state": t.PreviewState}, "images": t.ImageCount, "mode": t.PermissionMode,
		"denied": t.Denied, "login_email": t.LoginEmail, "api_requests": t.APIRequests,
		"tokens":  map[string]interface{}{"input": t.InputTokens, "output": t.OutputTokens, "cache_read": t.CacheReadTokens, "cache_create": t.CacheCreateTokens},
		"cost":    map[string]interface{}{"usd": t.CostUSD, "source": t.CostSource, "reason": analysisString(t.CostReason)},
		"context": map[string]interface{}{"tokens": t.ContextTokens, "window": t.ContextWindow, "percent": analysisPercent(t.ContextTokens, t.ContextWindow), "source": t.ContextSource},
		"tools":   map[string]interface{}{"calls": t.ToolCalls, "subagents": t.Subagents}, "question_headers": headers,
		"edited":  map[string]interface{}{"count": d.Edited.Count, "sample": d.Edited.Sample, "abandoned": d.Edited.Abandoned},
		"commits": []interface{}{}, "tasks": []interface{}{},
		"prs": []interface{}{}, "last_assistant_message_id": t.LastAssistantMessageID, "opener_copied": t.OpenerCopied}
}

type analysisInputImage struct {
	uuid string
	ts   time.Time
	row  map[string]interface{}
}

type analysisInputImageResult struct {
	matches int
	count   int
	known   bool
}

func analysisInputImageKey(uuid string, ts time.Time) string {
	return uuid + "\x00" + ts.UTC().Format(time.RFC3339Nano)
}

func analysisInputImageCount(recordType string, imagePasteCount, attachmentImageCount, collected, dropped, withheld []byte, redacted string) (int, bool) {
	if redacted == "[redacted by cctrace client]" {
		return 0, false
	}
	field, value := "imagePasteCount", imagePasteCount
	if recordType == "attachment" {
		field, value = "attachment", attachmentImageCount
	} else if recordType != "user" {
		return 0, false
	}
	if len(value) > 0 && string(value) != "null" {
		var count int
		if json.Unmarshal(value, &count) != nil || count < 0 {
			return 0, false
		}
		return count, true
	}
	if analysisInputImageFieldListed(dropped, field) || analysisInputImageFieldListed(withheld, field) || len(collected) == 0 || string(collected) == "null" {
		return 0, false
	}
	return 0, true
}

func analysisInputImageFieldListed(raw []byte, field string) bool {
	var fields []string
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for _, name := range fields {
		if name == field || (field == "attachment" && strings.HasPrefix(name, "attachment.")) {
			return true
		}
	}
	return false
}

func analysisInputImageCounts(ctx context.Context, tx pgx.Tx, id string, access SessionAnalysisAccess, inputs []analysisInputImage) (map[string]int, error) {
	counts := map[string]int{}
	uuids := make([]string, 0, len(inputs))
	seen := map[string]struct{}{}
	for _, input := range inputs {
		if input.uuid == "" || input.ts.IsZero() {
			continue
		}
		if _, ok := seen[input.uuid]; ok {
			continue
		}
		seen[input.uuid] = struct{}{}
		uuids = append(uuids, input.uuid)
	}
	if len(uuids) == 0 {
		return counts, nil
	}
	clause, value := analysisRecordScope(access)
	rows, err := tx.Query(ctx, `SELECT r.uuid,r.ts,r.record_type,r.raw->'imagePasteCount',r.raw->'attachment'->'imageCount',
		r.raw->'cctraceCollect',r.raw->'cctraceDropped',r.raw->'cctraceWithheld',COALESCE(r.raw->>'redacted','')
		FROM visible_session_records r WHERE r.session_id=$1 AND r.uuid=ANY($2::text[])
		AND r.record_type IN ('user','attachment') AND ($3::text='' OR r.`+clause+`=$3)
		ORDER BY r.uuid,r.ts,r.id`, id, uuids, value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := map[string]analysisInputImageResult{}
	for rows.Next() {
		var uuid, recordType, redacted string
		var ts time.Time
		var imagePasteCount, attachmentImageCount, collected, dropped, withheld []byte
		if err := rows.Scan(&uuid, &ts, &recordType, &imagePasteCount, &attachmentImageCount, &collected, &dropped, &withheld, &redacted); err != nil {
			return nil, err
		}
		key := analysisInputImageKey(uuid, ts)
		result := results[key]
		result.matches++
		if result.matches == 1 {
			result.count, result.known = analysisInputImageCount(recordType, imagePasteCount, attachmentImageCount, collected, dropped, withheld, redacted)
		} else {
			result.known = false
		}
		results[key] = result
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for key, result := range results {
		if result.matches == 1 && result.known {
			counts[key] = result.count
		}
	}
	return counts, nil
}

// ReadSessionAnalysis keeps identity, visibility, rollup and fact reads on one snapshot.
func (s *PgStore) ReadSessionAnalysis(parent context.Context, id string, access SessionAnalysisAccess) (map[string]interface{}, error) {
	if id == "" {
		return nil, ErrSessionAnalysisNotFound
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout = '5s'`); err != nil {
		return nil, err
	}
	var deleted bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deleted_sessions WHERE session_id=$1)`, id).Scan(&deleted); err != nil {
		return nil, err
	}
	if deleted {
		return nil, ErrSessionAnalysisNotFound
	}
	foreign, err := analysisForeignIdentity(ctx, tx, id, access)
	if err != nil {
		return nil, err
	}
	if foreign {
		return nil, ErrSessionAnalysisNotFound
	}
	f, err := analysisFact(ctx, tx, id, access)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	owner := access.User
	if f != nil && !access.User {
		owner = (access.OwnerUserID != "" && f.OwnerUserID != nil && *f.OwnerUserID == access.OwnerUserID) || (access.OwnerProfileEmail != "" && f.OwnerProfileEmail != nil && *f.OwnerProfileEmail == access.OwnerProfileEmail)
	}
	response := map[string]interface{}{"session_id": id, "short_id": id, "owner_view": owner}
	if len(id) > 8 {
		response["short_id"] = id[:8]
	}
	// The conversation frame needs only repository status. This read neither
	// loads analysis bodies nor requests a missing analysis computation.
	if access.RepoStatusOnly {
		if f == nil {
			_, agent, err := analysisRollup(ctx, tx, id, access)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrSessionAnalysisNotFound
			} else if err != nil {
				return nil, err
			}
			response["analysis_state"], err = analysisMissingState(ctx, tx, id, agent)
			if err != nil {
				return nil, err
			}
		} else if owner && f.Coverage != "truncated" {
			response["header"] = analysisHeader(f)
			response["outputs"] = map[string]interface{}{}
			if err := analysisSnapshot(ctx, tx, id, response); err != nil {
				return nil, err
			}
			delete(response, "outputs")
		}
		if f != nil {
			response["analysis_state"] = analysisFactState(f)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return response, nil
	}
	contextVersion, err := readTurnContextVersion(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if contextVersion.state() == "ready" {
		response["turn_context_revision"], err = s.turnContextRevision(id, access, contextVersion)
		if err != nil {
			return nil, err
		}
	}
	if f == nil || f.Coverage == "truncated" {
		header, agent, rollErr := analysisRollup(ctx, tx, id, access)
		if rollErr != nil && !errors.Is(rollErr, pgx.ErrNoRows) {
			return nil, rollErr
		}
		if f == nil && rollErr != nil {
			return nil, ErrSessionAnalysisNotFound
		}
		if f != nil {
			response["state"] = analysisFactState(f)
			response["rules_version"] = f.RulesVersion
			response["computed_at"] = f.ComputedAt
			response["identities"] = analysisIdentities(f)
			if header == nil {
				header = map[string]interface{}{"agent": f.Agent, "started_at": analysisTime(f.FirstActivityAt), "last_activity_at": analysisTime(f.LastActivityAt)}
			} else {
				header["started_at"] = analysisTime(f.FirstActivityAt)
				header["last_activity_at"] = analysisTime(f.LastActivityAt)
			}
		} else {
			response["state"], err = analysisMissingState(ctx, tx, id, agent)
			if err != nil {
				return nil, err
			}
		}
		if header != nil {
			response["header"] = header
		}
		commitState, taskState := "not_collected", "not_collected"
		commitTruncated := false
		if f != nil {
			p4State, stateErr := readSessionAnalysisP4State(ctx, tx, id)
			if stateErr != nil {
				return nil, stateErr
			}
			commitState, taskState = p4State.CommitsState, p4State.TasksState
			if commitState == "" {
				commitState = "undeterminable"
			}
			if taskState == "" {
				taskState = "undeterminable"
			}
			commitTruncated = p4State.CommitsTruncated
		}
		response["commits"] = map[string]interface{}{"state": commitState, "total": nil, "truncated": commitTruncated, "items": []interface{}{}}
		response["tasks"] = map[string]interface{}{"state": taskState, "items": []interface{}{}}
		if access.User {
			delete(response, "identities")
		}
		if err = addSessionRecordRead(ctx, tx, id, f, access, owner, response); err != nil {
			return nil, err
		}
		if err = analysisPending(ctx, tx, id, response); err != nil {
			return nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return response, nil
	}
	response["state"] = analysisFactState(f)
	response["rules_version"] = f.RulesVersion
	response["computed_at"] = f.ComputedAt
	response["identities"] = analysisIdentities(f)
	if access.User {
		delete(response, "identities")
	}
	if err = analysisPending(ctx, tx, id, response); err != nil {
		return nil, err
	}
	response["header"] = analysisHeader(f)
	p4State, err := readSessionAnalysisP4State(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	response["commits"] = map[string]interface{}{"state": p4State.CommitsState, "total": p4State.CommitCount, "truncated": p4State.CommitsTruncated, "items": []interface{}{}}
	response["tasks"] = map[string]interface{}{"state": p4State.TasksState, "items": []interface{}{}}
	if err = analysisProject(ctx, tx, f, response); err != nil {
		return nil, err
	}
	if owner {
		response["signals"] = analysisSignals(f.Detail.Signals, f.LastTurnStatus)
		if err = analysisLive(ctx, tx, id, f, access, response); err != nil {
			return nil, err
		}
	}
	edited := f.Detail.EditedFiles
	if edited.Items == nil {
		edited.Items = []sessionfacts.EditedFile{}
	}
	if len(edited.Items) > 100 {
		edited.Items = edited.Items[:100]
		edited.Truncated = true
	}
	response["outputs"] = map[string]interface{}{"edited_files": map[string]interface{}{"state": edited.State, "active_total": edited.ActiveTotal, "abandoned_total": edited.AbandonedTotal, "truncated": edited.Truncated, "items": edited.Items}, "lines": nil, "prs": map[string]interface{}{"items": []interface{}{}, "truncated": false}}
	if err = analysisSnapshot(ctx, tx, id, response); err != nil {
		return nil, err
	}
	response["subagents"] = map[string]interface{}{"unattributed_requests": f.Detail.SubagentUnattributed.Requests, "unattributed_tokens": f.Detail.SubagentUnattributed.Tokens}
	trunc := map[string]bool{"turns": false, "abandoned": false, "inputs": false, "questions": false, "plans": false, "recaps": false, "bodies": false}
	active, abandoned := []interface{}{}, []interface{}{}
	inputs := []interface{}{}
	pendingInputImages := []analysisInputImage{}
	questions := []interface{}{}
	ids := make(map[int64]struct{})
	type pendingQuestion struct {
		ref  sessionfacts.QuestionReference
		turn string
		ts   time.Time
	}
	refs := []pendingQuestion{}
	rows, err := tx.Query(ctx, `SELECT turn_key,ordinal,request_no,branch,opener_copied,started_at,ended_at,input_kind,is_request,login_email,status,status_reason,permission_mode,denied,image_count,preview,preview_state,api_requests,input_tokens,output_tokens,cache_read_tokens,cache_create_tokens,cost_usd,cost_source,cost_reason,context_tokens,context_window,context_source,tool_calls,subagents,last_assistant_message_id,detail FROM (SELECT t.*, row_number() OVER (PARTITION BY branch ORDER BY ordinal) AS branch_row FROM session_turn_facts t WHERE session_id=$1) t WHERE (branch='abandoned' AND branch_row<=501) OR (branch<>'abandoned' AND branch_row<=2001) ORDER BY ordinal`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t sessionfacts.Turn
		var body []byte
		err = rows.Scan(&t.TurnKey, &t.Ordinal, &t.RequestNo, &t.Branch, &t.OpenerCopied, &t.StartedAt, &t.EndedAt, &t.InputKind, &t.IsRequest, &t.LoginEmail, &t.Status, &t.StatusReason, &t.PermissionMode, &t.Denied, &t.ImageCount, &t.Preview, &t.PreviewState, &t.APIRequests, &t.InputTokens, &t.OutputTokens, &t.CacheReadTokens, &t.CacheCreateTokens, &t.CostUSD, &t.CostSource, &t.CostReason, &t.ContextTokens, &t.ContextWindow, &t.ContextSource, &t.ToolCalls, &t.Subagents, &t.LastAssistantMessageID, &body)
		if err != nil {
			break
		}
		if err = json.Unmarshal(body, &t.Detail); err != nil {
			break
		}
		selected := true
		if t.Branch == "abandoned" {
			if len(abandoned) < 500 {
				abandoned = append(abandoned, analysisTurn(t))
			} else {
				trunc["abandoned"] = true
				selected = false
			}
		} else {
			if len(active) < 2000 {
				active = append(active, analysisTurn(t))
			} else {
				trunc["turns"] = true
				selected = false
			}
		}
		if !selected {
			if len(t.Detail.Inputs) > 0 {
				trunc["inputs"] = true
			}
			if len(t.Detail.Questions) > 0 {
				trunc["questions"] = true
			}
			continue
		}
		for _, in := range t.Detail.Inputs {
			if len(inputs) < 4000 {
				input := map[string]interface{}{"uuid": in.UUID, "ts": in.TS, "n": in.N, "kind": in.Kind, "turn_key": t.TurnKey}
				inputs = append(inputs, input)
				pendingInputImages = append(pendingInputImages, analysisInputImage{uuid: in.UUID, ts: in.TS, row: input})
			} else {
				trunc["inputs"] = true
			}
		}
		for _, q := range t.Detail.Questions {
			if len(refs) < 200 {
				refs = append(refs, pendingQuestion{q, t.TurnKey, t.StartedAt})
				ids[q.SrecID] = struct{}{}
				ids[q.AnswerSrecID] = struct{}{}
			} else {
				trunc["questions"] = true
			}
		}
	}
	rows.Close()
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		return nil, err
	}
	inputImages, err := analysisInputImageCounts(ctx, tx, id, access, pendingInputImages)
	if err != nil {
		return nil, err
	}
	for _, input := range pendingInputImages {
		if count, ok := inputImages[analysisInputImageKey(input.uuid, input.ts)]; ok {
			input.row["images"] = count
		}
	}
	commitFacts, taskFacts, turnCommits, turnTasks, err := analysisSessionP4(ctx, tx, id, p4State)
	if err != nil {
		return nil, err
	}
	response["commits"], response["tasks"] = commitFacts, taskFacts
	attachSessionP4Turns(active, turnCommits, turnTasks)
	attachSessionP4Turns(abandoned, turnCommits, turnTasks)
	plans := []interface{}{}
	for i, p := range f.Detail.Plans {
		if i >= 50 {
			trunc["plans"] = true
			break
		}
		ids[p.SrecID] = struct{}{}
		ids[p.ResultSrecID] = struct{}{}
		ids[p.AttachmentSrecID] = struct{}{}
	}
	raw, err := analysisBodies(ctx, tx, id, access, ids)
	if err != nil {
		return nil, err
	}
	budget := 512 * 1024
	for _, q := range refs {
		if len(questions) >= 200 {
			trunc["questions"] = true
			break
		}
		questions = append(questions, analysisQuestion(q.ref, q.turn, q.ts, raw, &budget, trunc))
	}
	for i, p := range f.Detail.Plans {
		if i >= 50 {
			break
		}
		plans = append(plans, analysisPlan(p, raw, &budget, trunc))
	}
	recaps, err := analysisRecaps(ctx, tx, id, access, f.Detail.RecapHint, &budget, trunc)
	if err != nil {
		return nil, err
	}
	response["turns"] = active
	response["abandoned"] = abandoned
	response["inputs"] = inputs
	response["questions"] = questions
	response["plans"] = plans
	response["recaps"] = recaps
	response["truncated"] = trunc
	if err = analysisCopy(ctx, tx, id, access, response); err != nil {
		return nil, err
	}
	if owner && access.DashboardUserID > 0 && (access.OwnerUserID != "" || access.OwnerProfileEmail != "") {
		state, text, stored, e := personalStateRow(ctx, tx, access.DashboardUserID, id)
		if e != nil {
			return nil, e
		}
		verified, e := personalSessionOwnerRead(ctx, tx, id, access)
		if e != nil {
			return nil, e
		}
		if verified != nil && personalOwnerMatches(verified, stored) && state.Revision > 0 && state.ExpiresAt.After(time.Now()) {
			if text != "" {
				response["note"] = map[string]interface{}{"text": text, "updated_at": state.Note.UpdatedAt}
			} else {
				response["note"] = nil
			}
			response["classification"] = state.Classification
		}
	}
	if err = addSessionRecordRead(ctx, tx, id, f, access, owner, response); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return response, nil
}

func analysisProject(ctx context.Context, tx pgx.Tx, f *sessionfacts.Session, response map[string]interface{}) error {
	if f.ProjectHash == "" {
		return nil
	}
	var name string
	err := tx.QueryRow(ctx, `SELECT project_name FROM projects WHERE project_hash=$1`, f.ProjectHash).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	header := response["header"].(map[string]interface{})
	header["project"].(map[string]interface{})["name"] = name
	return nil
}

func analysisLive(ctx context.Context, tx pgx.Tx, id string, f *sessionfacts.Session, access SessionAnalysisAccess, response map[string]interface{}) error {
	owner, err := personalSessionOwnerRead(ctx, tx, id, access)
	if err != nil {
		return err
	}
	if owner == nil {
		return nil
	}
	var name, nameSource string
	if err := tx.QueryRow(ctx, `SELECT name,name_source FROM session_live_states
		WHERE session_id=$1 AND user_id=$2 AND lower(profile_email)=lower($3)
		AND identity_provenance='personal_token' AND name<>'' AND status<>'gone'
		AND heartbeat_at>=now()-interval '3 minutes' AND heartbeat_at<=now()+interval '1 minute'
		ORDER BY heartbeat_at DESC LIMIT 1`, id, owner.userID, owner.email).Scan(&name, &nameSource); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if name != "" {
		response["header"].(map[string]interface{})["name"] = map[string]interface{}{"text": name, "source": nameSource}
	}
	in, err := loadSessionActivity(ctx, tx, id, owner)
	if err != nil {
		return err
	}
	now := time.Now()
	decision := evaluateSessionActivity(now, in)
	var latest time.Time
	if in.lastActivityAt != nil {
		latest = *in.lastActivityAt
	}
	instances := []interface{}{}
	rows, err := tx.Query(ctx, `SELECT pid,status,status_source,waiting_kind,heartbeat_at,name,status_changed_at,
		io_epoch,io_as_of,io_source,io_mode,io_partial,io_failure_code,
		input_bytes,input_lines,output_bytes,output_lines,stdout_bytes,stdout_lines,stderr_bytes,stderr_lines
		FROM session_live_states
		WHERE session_id=$1 AND user_id=$2 AND lower(profile_email)=lower($3)
		AND identity_provenance='personal_token' AND status<>'gone'
		AND heartbeat_at>=now()-interval '3 minutes' AND heartbeat_at<=now()+interval '1 minute'
		ORDER BY heartbeat_at DESC LIMIT 101`, id, owner.userID, owner.email)
	if err != nil {
		return err
	}
	for rows.Next() {
		var pid int64
		var status, statusSource, waiting, name, ioEpoch, ioSource, ioMode, ioFailure string
		var heartbeat time.Time
		var changed, ioAsOf *time.Time
		var ioPartial bool
		var inputBytes, inputLines, outputBytes, outputLines *int64
		var stdoutBytes, stdoutLines, stderrBytes, stderrLines *int64
		if err = rows.Scan(&pid, &status, &statusSource, &waiting, &heartbeat, &name, &changed,
			&ioEpoch, &ioAsOf, &ioSource, &ioMode, &ioPartial, &ioFailure,
			&inputBytes, &inputLines, &outputBytes, &outputLines, &stdoutBytes, &stdoutLines, &stderrBytes, &stderrLines); err != nil {
			break
		}
		if len(instances) >= 100 {
			break
		}
		stale := (status == "busy" || status == "waiting" || waiting != "") && changed != nil && in.lastActivityAt != nil &&
			changed.Before(now.Add(-6*time.Hour)) && in.lastActivityAt.Before(now.Add(-6*time.Hour))
		ioFreshness := "unknown"
		if ioEpoch != "" && ioAsOf != nil {
			ioFreshness = "fresh"
			if ioPartial {
				ioFreshness = "partial"
			} else if ioAsOf.Before(now.Add(-30*time.Second)) || stale {
				ioFreshness = "stale"
			}
		}
		process := map[string]interface{}{
			"pid": pid, "input_lines": inputLines, "output_lines": outputLines,
			"input_bytes": inputBytes, "output_bytes": outputBytes,
			"stdout_lines": stdoutLines, "stdout_bytes": stdoutBytes,
			"stderr_lines": stderrLines, "stderr_bytes": stderrBytes,
			"io_source": "unknown", "io_scope": "instance", "io_freshness": ioFreshness,
			"io_as_of": analysisTime(ioAsOf), "io_epoch": analysisString(ioEpoch), "io_mode": analysisString(ioMode),
			"io_partial": ioPartial, "io_failure_code": analysisString(ioFailure),
		}
		if ioEpoch != "" && ioAsOf != nil {
			process["io_source"] = ioSource
		}
		instances = append(instances, map[string]interface{}{"status": status, "status_source": statusSource, "waiting_kind": waiting, "heartbeat_at": heartbeat, "name": name, "stale": stale, "process": process})
		if heartbeat.After(latest) {
			latest = heartbeat
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	var collectorHeartbeat, lastStored *time.Time
	if err = tx.QueryRow(ctx, `SELECT max(heartbeat_at),max(last_stored_at) FROM collector_heartbeats
		WHERE user_id=$1 AND lower(profile_email)=lower($2)`, owner.userID, owner.email).Scan(&collectorHeartbeat, &lastStored); err != nil {
		return err
	}
	if collectorHeartbeat != nil && collectorHeartbeat.After(latest) {
		latest = *collectorHeartbeat
	}
	if in.endAt != nil && in.endAt.After(latest) {
		latest = *in.endAt
	}
	if in.allGoneAt != nil && in.allGoneAt.After(latest) {
		latest = *in.allGoneAt
	}
	if latest.IsZero() {
		latest = now
	}
	header := response["header"].(map[string]interface{})
	header["category"] = map[string]interface{}{"key": decision.category, "as_of": latest.UTC().Format(time.RFC3339Nano)}
	if decision.selected != nil && decision.category == "waiting" {
		header["category"].(map[string]interface{})["waiting_kind"] = decision.selected.waitingKind
	}
	transcriptIO := analysisTranscriptIO(f, owner, response["pending_since"] != nil)
	response["live"] = map[string]interface{}{
		"instances":     instances,
		"transcript_io": transcriptIO,
		"collector":     map[string]interface{}{"heartbeat_at": analysisTime(collectorHeartbeat), "last_stored_at": analysisTime(lastStored)},
	}
	return nil
}

type sessionGitSnapshot struct {
	ChangedAt              time.Time `json:"changed_at"`
	EditedFiles            *int      `json:"edited_files"`
	UncommittedEditedFiles *int      `json:"uncommitted_edited_files"`
	Repositories           *int      `json:"repositories"`
	Unresolved             *int      `json:"unresolved"`
	Partial                bool      `json:"partial"`
}

func sessionGitRepoStatus(git *sessionGitSnapshot, truncated []string, branch interface{}) map[string]interface{} {
	branchState, branchSource := "undeterminable", "unknown"
	if value, ok := branch.(string); ok && value != "" {
		branchState, branchSource = "observed", "session_record_git_branch"
	}
	status := map[string]interface{}{
		"branch": branch, "branch_state": branchState, "branch_source": branchSource,
		"uncommitted_edited_files": nil, "uncommitted_state": "undeterminable",
		"uncommitted_source": "unknown", "uncommitted_scope": "session_edited_files",
		"uncommitted_reason": "no_git_snapshot", "changed_at": nil,
	}
	truncatedGit := false
	for _, field := range truncated {
		if field == "git" {
			truncatedGit = true
			break
		}
	}
	if git == nil {
		return status
	}
	switch {
	case truncatedGit:
		status["uncommitted_reason"] = "truncated_snapshot"
	case git.Partial:
		status["uncommitted_reason"] = "partial_snapshot"
	case git.EditedFiles == nil || git.UncommittedEditedFiles == nil || git.Repositories == nil || git.Unresolved == nil:
		status["uncommitted_reason"] = "invalid_git_snapshot"
	case *git.EditedFiles < 0 || *git.UncommittedEditedFiles < 0 || *git.Repositories < 0 || *git.Unresolved < 0:
		status["uncommitted_reason"] = "invalid_git_snapshot"
	case *git.Unresolved > 0:
		status["uncommitted_reason"] = "unresolved_git_paths"
	default:
		status["uncommitted_edited_files"] = *git.UncommittedEditedFiles
		status["uncommitted_state"] = "observed"
		status["uncommitted_source"] = "session_git_snapshot"
		status["uncommitted_reason"] = nil
		if !git.ChangedAt.IsZero() {
			status["changed_at"] = git.ChangedAt.UTC()
		}
	}
	return status
}

func readSessionGitStatuses(ctx context.Context, tx pgx.Tx, ids []string, userID, profileEmail string) (map[string]map[string]interface{}, error) {
	result := make(map[string]map[string]interface{}, len(ids))
	if len(ids) == 0 || userID == "" && profileEmail == "" {
		return result, nil
	}
	for _, id := range ids {
		result[id] = sessionGitRepoStatus(nil, nil, nil)
	}
	ownerPredicate := `f.owner_user_id=$2 AND ss.user_id=f.owner_user_id AND lower(ss.profile_email)=lower(f.owner_profile_email)`
	identity := userID
	if userID == "" {
		ownerPredicate = `lower(f.owner_profile_email)=lower($2) AND lower(ss.profile_email)=lower(f.owner_profile_email)`
		identity = profileEmail
	}
	query := `SELECT DISTINCT ON (ss.session_id) ss.session_id,ss.snapshot,f.branch
		FROM session_snapshots ss JOIN session_analysis_facts f USING(session_id)
		WHERE ss.session_id=ANY($1::text[]) AND NOT EXISTS (SELECT 1 FROM excluded_sessions e WHERE e.session_id=ss.session_id)
		AND f.coverage<>'truncated' AND ` + sessionAnalysisVisibleSQL + ` AND ` + excludedAccountPredicateSQL("ss") + ` AND ` + ownerPredicate + `
		ORDER BY ss.session_id,ss.observed_at DESC`
	rows, err := tx.Query(ctx, query, ids, identity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var raw []byte
		var branch string
		if err := rows.Scan(&id, &raw, &branch); err != nil {
			return nil, err
		}
		var snapshot struct {
			Git       *sessionGitSnapshot `json:"git"`
			Truncated []string            `json:"truncated"`
		}
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return nil, err
		}
		result[id] = sessionGitRepoStatus(snapshot.Git, snapshot.Truncated, branch)
	}
	return result, rows.Err()
}

func analysisSnapshot(ctx context.Context, tx pgx.Tx, id string, response map[string]interface{}) error {
	var data []byte
	err := tx.QueryRow(ctx, `SELECT ss.snapshot FROM session_snapshots ss WHERE `+sessionFactSnapshotVisibleSQL+` ORDER BY ss.observed_at DESC LIMIT 1`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var snapshot struct {
		Git        *sessionGitSnapshot `json:"git"`
		CostStates []struct {
			LinesAdded   int64 `json:"lines_added"`
			LinesRemoved int64 `json:"lines_removed"`
		} `json:"cost_states"`
		PRLinks []struct {
			URL         string    `json:"url"`
			Number      int64     `json:"number"`
			Repository  string    `json:"repository"`
			FirstSeenAt time.Time `json:"first_seen_at"`
		} `json:"pr_links"`
		Truncated   []string `json:"truncated"`
		CustomTitle *string  `json:"custom_title"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	if response["owner_view"] == true {
		header := response["header"].(map[string]interface{})
		branch := interface{}(nil)
		if value, ok := header["branch"].(string); ok && value != "" {
			branch = value
		}
		header["repo_status"] = sessionGitRepoStatus(snapshot.Git, snapshot.Truncated, branch)
	}
	if response["owner_view"] == true && response["header"].(map[string]interface{})["name"] == nil && snapshot.CustomTitle != nil && *snapshot.CustomTitle != "" && *snapshot.CustomTitle != "[redacted by cctrace client]" {
		response["header"].(map[string]interface{})["name"] = map[string]interface{}{"text": *snapshot.CustomTitle, "source": "custom_title"}
	}
	outputs := response["outputs"].(map[string]interface{})
	if len(snapshot.CostStates) > 0 {
		var added, removed int64
		for _, cost := range snapshot.CostStates {
			added += cost.LinesAdded
			removed += cost.LinesRemoved
		}
		partial := false
		for _, field := range snapshot.Truncated {
			if field == "cost_states" {
				partial = true
			}
		}
		outputs["lines"] = map[string]interface{}{"added": added, "removed": removed, "source": "cost_state", "partial": partial}
	}
	prs := []interface{}{}
	prsTruncated := len(snapshot.PRLinks) > 100
	for _, field := range snapshot.Truncated {
		if field == "pr_links" {
			prsTruncated = true
		}
	}
	for i, pr := range snapshot.PRLinks {
		if i >= 100 {
			break
		}
		prs = append(prs, map[string]interface{}{"url": pr.URL, "number": pr.Number, "repository": pr.Repository, "first_seen_at": pr.FirstSeenAt, "turn_key": nil})
	}
	outputs["prs"] = map[string]interface{}{"items": prs, "truncated": prsTruncated}
	return nil
}

func analysisPending(ctx context.Context, tx pgx.Tx, id string, response map[string]interface{}) error {
	var pending time.Time
	err := tx.QueryRow(ctx, `SELECT first_dirty_at FROM session_fact_dirty WHERE session_id=$1`, id).Scan(&pending)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	response["analysis_pending"] = err == nil
	if err == nil {
		response["pending_since"] = pending.UTC().Format(time.RFC3339Nano)
	} else {
		response["pending_since"] = nil
	}
	return nil
}

func analysisMissingState(ctx context.Context, tx pgx.Tx, id, agent string) (string, error) {
	if agent != "claude" && agent != "codex" {
		return "unsupported", nil
	}
	var empty bool
	err := tx.QueryRow(ctx, `SELECT `+sessionAnalysisNoneSQL("$1"), id).Scan(&empty)
	if err != nil {
		return "", err
	}
	if empty {
		return "none", nil
	}
	return "building", nil
}

// Missing facts mean empty only after the current build finished and this
// session has no queued work, including exhausted failures and future retries.
func sessionAnalysisNoneSQL(id string) string {
	_, done := sessionFactBuildMarkers()
	return `EXISTS(SELECT 1 FROM schema_backfills WHERE name='` + done + `')
		AND NOT EXISTS(SELECT 1 FROM session_fact_dirty pending WHERE pending.session_id=` + id + `)`
}

func analysisFactState(f *sessionfacts.Session) string {
	if f.Agent != "claude" && f.Agent != "codex" {
		return "unsupported"
	}
	if f.Coverage == "truncated" {
		return "truncated"
	}
	if f.RulesVersion != sessionfacts.RulesVersion {
		return "stale"
	}
	return "ready"
}

func analysisIdentities(f *sessionfacts.Session) map[string]interface{} {
	return map[string]interface{}{"user_ids": f.UserIDs, "profile_emails": f.ProfileEmails, "login_emails": f.LoginEmails, "mixed": len(f.UserIDs) > 1 || len(f.ProfileEmails) > 1}
}
func analysisHeader(f *sessionfacts.Session) map[string]interface{} {
	name := interface{}(nil)
	header := map[string]interface{}{"title": map[string]interface{}{"text": f.Title, "source": f.TitleSource, "state": f.TitleState}, "name": name,
		"agent": f.Agent, "model": f.Model, "branch": f.Branch, "project": map[string]interface{}{"hash": f.ProjectHash, "name": nil},
		"accounts":   map[string]interface{}{"login_emails": f.LoginEmails, "inferred": f.LoginEmailInferred},
		"started_at": analysisTime(f.FirstActivityAt), "last_activity_at": analysisTime(f.LastActivityAt), "duration_seconds": interface{}(nil), "active_seconds": f.ActiveSeconds,
		"counts":     map[string]interface{}{"requests": f.Requests, "inputs": f.Inputs, "turns": f.Turns, "abandoned_turns": f.AbandonedTurns, "api_requests": f.APIRequests, "turn_boundaries": map[string]interface{}{"state": f.TurnBoundaries, "reason": analysisString(f.TurnBoundariesReason)}},
		"tokens":     map[string]interface{}{"input": f.InputTokens, "output": f.OutputTokens, "cache_read": f.CacheReadTokens, "cache_create": f.CacheCreateTokens, "basis": "request_set"},
		"cost":       map[string]interface{}{"usd": f.CostUSD, "source": f.CostSource, "turn_attached_usd": f.Detail.Cost.TurnAttachedUSD, "unattached_turn_usd": f.Detail.Cost.UnattachedTurnUSD, "note": analysisString(f.Detail.Cost.Note)},
		"context":    map[string]interface{}{"tokens": f.LastContextTokens, "window": f.LastContextWindow, "percent": analysisPercent(f.LastContextTokens, f.LastContextWindow), "source": f.LastContextSource, "large_window_seen": f.LargeWindowSeen, "mix": f.Detail.ContextMix},
		"tools":      map[string]interface{}{"calls": f.Detail.ToolCalls, "subagents_running": f.Detail.SubagentsRunning},
		"tags":       map[string]interface{}{"change_area": map[string]interface{}{"value": analysisString(f.Detail.ChangeArea.Value), "state": f.Detail.ChangeArea.State}, "task_type": nil, "rules_files": []interface{}{}},
		"plan_flags": map[string]interface{}{"has_plan_file": f.HasPlanFile, "used_plan_mode": f.UsedPlanMode}}
	if f.FirstActivityAt != nil && f.LastActivityAt != nil {
		header["duration_seconds"] = int(f.LastActivityAt.Sub(*f.FirstActivityAt).Seconds())
	}
	if f.Detail.TaskType.Value != "" {
		header["tags"].(map[string]interface{})["task_type"] = map[string]interface{}{"value": f.Detail.TaskType.Value, "share": f.Detail.TaskType.Share, "basis": "classifier"}
	}
	return header
}
func analysisSignals(signals sessionfacts.Signals, lastTurnStatus string) map[string]interface{} {
	out := map[string]interface{}{}
	for key, s := range map[string]sessionfacts.Signal{"turn": signals.Turn, "question": signals.Question, "background": signals.Background, "todo": signals.Todo} {
		if s.State == "" || s.State == "not_applicable" {
			continue
		}
		var value interface{}
		if s.Value != nil {
			value = *s.Value
		}
		if key == "turn" && s.State == "observed" {
			value = lastTurnStatus
		}
		entry := map[string]interface{}{"state": s.State, "value": value}
		if s.Reason != "" {
			entry["reason"] = s.Reason
		}
		if key == "background" && s.State == "undeterminable" {
			entry["parts"] = nil
		}
		out[key] = entry
	}
	return out
}

func analysisBodies(ctx context.Context, tx pgx.Tx, id string, access SessionAnalysisAccess, ids map[int64]struct{}) (map[int64]json.RawMessage, error) {
	out := map[int64]json.RawMessage{}
	list := make([]int64, 0, len(ids))
	for n := range ids {
		if n > 0 {
			list = append(list, n)
		}
	}
	if len(list) == 0 {
		return out, nil
	}
	clause, value := analysisRecordScope(access)
	rows, err := tx.Query(ctx, `SELECT r.id,r.raw FROM visible_session_records r WHERE r.session_id=$1 AND r.id=ANY($2::bigint[]) AND ($3::text='' OR r.`+clause+`=$3)`, id, list, value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key int64
		var body json.RawMessage
		if err = rows.Scan(&key, &body); err != nil {
			return nil, err
		}
		out[key] = body
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
func analysisRecordScope(access SessionAnalysisAccess) (string, string) {
	if !access.User {
		return "user_id", ""
	}
	if access.ProfileEmail != "" {
		return "profile_email", access.ProfileEmail
	}
	return "user_id", access.UserID
}
func analysisSpend(s string, budget *int, trunc map[string]bool, limit int) string {
	if len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
		trunc["bodies"] = true
	}
	if len(s) > *budget {
		trunc["bodies"] = true
		*budget = 0
		return ""
	}
	*budget -= len(s)
	return s
}

func analysisNormalizeQuestionLabel(label string) string {
	label = strings.TrimSpace(label)
	const recommended = "(Recommended)"
	if len(label) >= len(recommended) && strings.EqualFold(label[len(label)-len(recommended):], recommended) {
		label = strings.TrimSpace(label[:len(label)-len(recommended)])
	}
	return label
}

func analysisQuestionChoiceLabel(selected string, choices []interface{}) (string, bool) {
	var exact string
	exactMatches := 0
	for _, choice := range choices {
		option, _ := choice.(map[string]interface{})
		label, _ := option["label"].(string)
		if label == selected {
			exact, exactMatches = label, exactMatches+1
		}
	}
	if exactMatches == 1 {
		return exact, true
	}
	if exactMatches > 1 {
		return "", false
	}

	normalized := analysisNormalizeQuestionLabel(selected)
	if normalized == "" {
		return "", false
	}
	var matched string
	matches := 0
	for _, choice := range choices {
		option, _ := choice.(map[string]interface{})
		label, _ := option["label"].(string)
		if analysisNormalizeQuestionLabel(label) == normalized {
			matched, matches = label, matches+1
		}
	}
	if matches != 1 {
		return "", false
	}
	return matched, true
}

func analysisMultiSelectLabels(selected string, choices []interface{}) ([]string, bool) {
	if selected == "" || len(selected) > 512*1024 || strings.Count(selected, ",") > 64 {
		return nil, false
	}
	labels := make([]string, 0, len(choices))
	for _, choice := range choices {
		option, _ := choice.(map[string]interface{})
		label, _ := option["label"].(string)
		if label != "" {
			labels = append(labels, label)
		}
	}

	parseLabels := func(normalize bool) [][]string {
		memo := map[int][][]string{}
		var parse func(int) [][]string
		parse = func(start int) [][]string {
			if start == len(selected) {
				return [][]string{{}}
			}
			if parsed, ok := memo[start]; ok {
				return parsed
			}
			var matches [][]string
			for _, label := range labels {
				if normalize && analysisNormalizeQuestionLabel(label) == "" {
					continue
				}
				for end := start; end <= len(selected); end++ {
					if end != len(selected) && selected[end] != ',' {
						continue
					}
					selectedLabel := selected[start:end]
					matchesLabel := selectedLabel == label
					if normalize {
						matchesLabel = analysisNormalizeQuestionLabel(selectedLabel) == analysisNormalizeQuestionLabel(label)
					}
					if !matchesLabel {
						continue
					}
					if end == len(selected) {
						matches = append(matches, []string{label})
					} else {
						next := end + 1
						for next < len(selected) && selected[next] == ' ' {
							next++
						}
						if next == len(selected) {
							continue
						}
						for _, tail := range parse(next) {
							sequence := make([]string, 0, len(tail)+1)
							sequence = append(sequence, label)
							sequence = append(sequence, tail...)
							matches = append(matches, sequence)
							if len(matches) > 1 {
								break
							}
						}
					}
					if len(matches) > 1 {
						break
					}
				}
				if len(matches) > 1 {
					break
				}
			}
			memo[start] = matches
			return matches
		}
		return parse(0)
	}

	// Exact option-label sequences win; multiple exact parses stay unresolved.
	// Suffix-normalized matching is only tried when no exact parse exists.
	exact := parseLabels(false)
	if len(exact) == 1 && len(exact[0]) > 0 {
		return exact[0], true
	}
	if len(exact) > 1 {
		return nil, false
	}
	normalized := parseLabels(true)
	if len(normalized) != 1 || len(normalized[0]) == 0 {
		return nil, false
	}
	return normalized[0], true
}

func analysisQuestion(q sessionfacts.QuestionReference, turn string, ts time.Time, raw map[int64]json.RawMessage, budget *int, trunc map[string]bool) map[string]interface{} {
	items := []interface{}{}
	questionBudget := *budget
	questionTrunc := map[string]bool{}
	state := q.State
	if state == "observed" {
		data := raw[q.SrecID]
		if len(data) == 0 {
			state = "withheld"
		}
		var record map[string]interface{}
		_ = json.Unmarshal(data, &record)
		if record["redacted"] == "[redacted by cctrace client]" {
			state = "redacted"
		}
		if state == "observed" {
			message, _ := record["message"].(map[string]interface{})
			blocks, _ := message["content"].([]interface{})
			for _, block := range blocks {
				if state == "redacted" {
					break
				}
				b, _ := block.(map[string]interface{})
				if b["id"] != q.ToolUseID {
					continue
				}
				arg, _ := b["input"].(map[string]interface{})
				if b["input"] == "[redacted by cctrace client]" || arg["redacted"] == "[redacted by cctrace client]" {
					state = "redacted"
					items = []interface{}{}
					break
				}
				qs, _ := arg["questions"].([]interface{})
				for _, item := range qs {
					v, _ := item.(map[string]interface{})
					text, _ := v["question"].(string)
					header, _ := v["header"].(string)
					if text == "[redacted by cctrace client]" || header == "[redacted by cctrace client]" {
						state = "redacted"
						items = []interface{}{}
						break
					}
					opts := []interface{}{}
					choices, _ := v["options"].([]interface{})
					for _, choice := range choices {
						c, _ := choice.(map[string]interface{})
						label, _ := c["label"].(string)
						description, _ := c["description"].(string)
						if label == "[redacted by cctrace client]" || description == "[redacted by cctrace client]" {
							state = "redacted"
							items = []interface{}{}
							break
						}
						recommended, _ := c["recommended"].(bool)
						opts = append(opts, map[string]interface{}{"label": analysisSpend(label, &questionBudget, questionTrunc, 512*1024), "description": analysisSpend(description, &questionBudget, questionTrunc, 512*1024), "recommended": recommended})
					}
					if state == "redacted" {
						break
					}
					var answer interface{}
					if result := raw[q.AnswerSrecID]; len(result) > 0 {
						var answerRecord map[string]interface{}
						_ = json.Unmarshal(result, &answerRecord)
						toolResult, _ := answerRecord["toolUseResult"].(map[string]interface{})
						if answerRecord["redacted"] == "[redacted by cctrace client]" || toolResult["answers"] == "[redacted by cctrace client]" {
							state = "redacted"
							items = []interface{}{}
							break
						}
						var answerData struct {
							ToolUseResult struct {
								Answers     json.RawMessage `json:"answers"`
								Annotations json.RawMessage `json:"annotations"`
							} `json:"toolUseResult"`
						}
						var selected string
						hasSelected := false
						notes := ""
						if json.Unmarshal(result, &answerData) == nil {
							var answers map[string]string
							if json.Unmarshal(answerData.ToolUseResult.Answers, &answers) == nil {
								selected, hasSelected = answers[text]
							}
							var annotations map[string]struct {
								Notes string `json:"notes"`
							}
							if json.Unmarshal(answerData.ToolUseResult.Annotations, &annotations) == nil {
								if annotation, ok := annotations[text]; ok && annotation.Notes != "[redacted by cctrace client]" {
									notes = annotation.Notes
								}
							}
						}
						if hasSelected && selected == "[redacted by cctrace client]" {
							state = "redacted"
							items = []interface{}{}
							break
						}
						if hasSelected || notes != "" {
							labels := []string{}
							freeText := ""
							if hasSelected {
								if v["multiSelect"] == true {
									if selectedLabels, ok := analysisMultiSelectLabels(selected, choices); ok {
										labels = selectedLabels
									} else {
										freeText = selected
									}
								} else {
									if label, ok := analysisQuestionChoiceLabel(selected, choices); ok {
										labels = append(labels, analysisSpend(label, &questionBudget, questionTrunc, 512*1024))
									} else {
										freeText = selected
									}
								}
							}
							if notes != "" {
								if freeText == "" || freeText == notes {
									freeText = notes
								} else {
									freeText += "\n" + notes
								}
							}
							var freeTextValue interface{}
							if freeText != "" {
								freeTextValue = analysisSpend(freeText, &questionBudget, questionTrunc, 512*1024)
							}
							answer = map[string]interface{}{"labels": labels, "free_text": freeTextValue}
						}
					}
					items = append(items, map[string]interface{}{"header": analysisSpend(header, &questionBudget, questionTrunc, 512*1024), "question": analysisSpend(text, &questionBudget, questionTrunc, 512*1024), "multi_select": v["multiSelect"] == true, "options": opts, "answer": answer})
				}
			}
			if len(items) == 0 && state == "observed" {
				state = "withheld"
			}
		}
	}
	if state == "observed" && len(items) > 0 {
		*budget = questionBudget
		if questionTrunc["bodies"] {
			trunc["bodies"] = true
		}
	}
	return map[string]interface{}{"tool_use_id": q.ToolUseID, "turn_key": turn, "ts": ts, "status": q.Status, "state": state, "items": items}
}
func analysisPlan(p sessionfacts.PlanReference, raw map[int64]json.RawMessage, budget *int, trunc map[string]bool) map[string]interface{} {
	state := "withheld"
	var body interface{}
	if data := raw[p.SrecID]; len(data) > 0 {
		var record map[string]interface{}
		_ = json.Unmarshal(data, &record)
		if record["redacted"] == "[redacted by cctrace client]" {
			state = "redacted"
		} else {
			message, _ := record["message"].(map[string]interface{})
			blocks, _ := message["content"].([]interface{})
			for _, block := range blocks {
				b, _ := block.(map[string]interface{})
				if b["id"] == p.ToolUseID {
					input, _ := b["input"].(map[string]interface{})
					if b["input"] == "[redacted by cctrace client]" || input["redacted"] == "[redacted by cctrace client]" {
						state = "redacted"
						break
					}
					if text, ok := input["plan"].(string); ok {
						if text == "[redacted by cctrace client]" {
							state = "redacted"
						} else {
							body = analysisSpend(text, budget, trunc, 16*1024)
							state = "observed"
						}
					}
				}
			}
		}
	}
	if body == nil && state == "withheld" && p.AttachmentSrecID != 0 {
		if data := raw[p.AttachmentSrecID]; len(data) > 0 {
			var record struct {
				Redacted   string `json:"redacted"`
				Attachment struct {
					Type        string `json:"type"`
					PlanContent string `json:"planContent"`
				} `json:"attachment"`
			}
			if json.Unmarshal(data, &record) == nil {
				switch {
				case record.Redacted == "[redacted by cctrace client]" || record.Attachment.PlanContent == "[redacted by cctrace client]":
					state = "redacted"
				case record.Attachment.Type == "plan_file_reference" && record.Attachment.PlanContent != "":
					body = analysisSpend(record.Attachment.PlanContent, budget, trunc, 16*1024)
					state = "observed"
				}
			}
		}
	}
	return map[string]interface{}{"turn_key": p.TurnKey, "ts": p.TS, "file_path": analysisString(p.FilePath), "decision": p.Decision, "body": body, "state": state}
}
func analysisRecaps(ctx context.Context, tx pgx.Tx, id string, access SessionAnalysisAccess, hint sessionfacts.RecapHint, budget *int, trunc map[string]bool) (map[string]interface{}, error) {
	clause, value := analysisRecordScope(access)
	rows, err := tx.Query(ctx, `SELECT r.ts,r.raw FROM visible_session_records r WHERE r.session_id=$1 AND r.record_type='system' AND r.raw->>'subtype'='away_summary' AND ($2::text='' OR r.`+clause+`=$2)
 AND r.ts > COALESCE((SELECT max(c.ts) FROM session_record_copies c WHERE c.session_id=$1
 AND EXISTS (SELECT 1 FROM schema_backfills WHERE name='session_fact_copies_v1')),'-infinity'::timestamptz)
 ORDER BY r.ts LIMIT 51`, id, value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []interface{}{}
	redacted, withheld := false, false
	var previous string
	seen := 0
	for rows.Next() {
		seen++
		if seen > 50 {
			trunc["recaps"] = true
			break
		}
		var ts time.Time
		var raw json.RawMessage
		if err = rows.Scan(&ts, &raw); err != nil {
			return nil, err
		}
		var entry map[string]interface{}
		_ = json.Unmarshal(raw, &entry)
		text, _ := entry["summary"].(string)
		if text == "" {
			text, _ = entry["content"].(string)
		}
		if text == "[redacted by cctrace client]" {
			redacted = true
			continue
		}
		if text == "" {
			if fields, ok := entry["cctraceWithheld"].([]interface{}); ok {
				for _, withheldField := range fields {
					if withheldField == "content" {
						withheld = true
					}
				}
			}
			continue
		}
		text = strings.TrimSuffix(text, "(disable recaps in /config)")
		text = strings.TrimSpace(text)
		if text == previous && len(items) > 0 {
			items[len(items)-1].(map[string]interface{})["repeats"] = items[len(items)-1].(map[string]interface{})["repeats"].(int) + 1
			continue
		}
		previous = text
		items = append(items, map[string]interface{}{"ts": ts, "text": analysisSpend(text, budget, trunc, 512*1024), "repeats": 1})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	state := "observed"
	reason := interface{}(nil)
	if len(items) == 0 {
		state = "none"
		reason = "not_away"
		if redacted {
			state, reason = "redacted", "redacted"
		} else if withheld {
			state, reason = "withheld", "withheld"
		} else if !hint.SystemRowsCollected {
			state, reason = "pre_collect", "pre_collect"
		} else if hint.Headless {
			reason = "headless"
		} else if hint.Turns < 2 {
			reason = "too_few_turns"
		}
	}
	return map[string]interface{}{"state": state, "items": items, "absent_reason": reason}, nil
}
func analysisCopy(ctx context.Context, tx pgx.Tx, id string, access SessionAnalysisAccess, response map[string]interface{}) error {
	var original string
	var count int
	var until *time.Time
	err := tx.QueryRow(ctx, `SELECT original_session_id,count(*),max(ts) FROM session_record_copies WHERE session_id=$1
 AND EXISTS (SELECT 1 FROM schema_backfills WHERE name='session_fact_copies_v1')
 GROUP BY original_session_id ORDER BY count(*) DESC LIMIT 1`, id).Scan(&original, &count, &until)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	originalAccess := access
	if !access.User {
		originalAccess.UserID = ""
		originalAccess.ProfileEmail = ""
		originalAccess.LoginEmail = ""
	}
	foreign, err := analysisForeignIdentity(ctx, tx, original, originalAccess)
	if err != nil {
		return err
	}
	visible := false
	if !foreign {
		_, err = analysisFact(ctx, tx, original, originalAccess)
		visible = err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	var originalID interface{}
	if visible {
		originalID = original
	}
	response["copies"] = map[string]interface{}{"state": "judged", "original_session_id": originalID, "original_visible": visible, "copied_lines": count, "copied_until": analysisTime(until)}
	return nil
}
func personalSessionOwnerRead(ctx context.Context, tx pgx.Tx, id string, access SessionAnalysisAccess) (*personalOwner, error) {
	return personalSourceOwner(ctx, tx, id, access.OwnerProfileEmail, access.OwnerUserID, false)
}
