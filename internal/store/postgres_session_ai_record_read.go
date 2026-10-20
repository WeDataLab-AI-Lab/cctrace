package store

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"cctrace/internal/sessionfacts"
	"cctrace/internal/sessionrecord"
)

var confirmedCommitHash = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

type sessionRecordRead struct {
	Outcome, Reason, Runtime, Model, Language, Headline         string
	PromptVersion                                               int
	FactsHash, BasisHash                                        []byte
	CoversUntil, GeneratedAt                                    *time.Time
	Content                                                     []byte
	Kinds, Components, Screens                                  []string
	CoveredRequests, WorkOpen, WorkUnsettled, UncoveredRequests int
}

func addSessionRecordRead(ctx context.Context, tx pgx.Tx, id string, f *sessionfacts.Session, access SessionAnalysisAccess,
	owner bool, response map[string]interface{}) error {
	if !access.RecordFeatureEnabled {
		return nil
	}
	state, reason := "none", "facts_missing"
	if f != nil {
		switch {
		case f.TurnBoundaries != "observed":
			state, reason = "redacted", "turn_boundaries"
		case f.Coverage == "truncated":
			state, reason = "none", "too_large"
		case f.Requests == nil || *f.Requests == 0:
			state, reason = "none", "no_requests"
		}
	}
	var saved *sessionRecordRead
	if f != nil {
		var currentFactsHash []byte
		if err := tx.QueryRow(ctx, `SELECT output_hash FROM session_analysis_facts WHERE session_id=$1`, id).Scan(&currentFactsHash); err != nil {
			return err
		}
		row, err := loadSessionRecordRead(ctx, tx, id)
		if err != nil {
			return err
		}
		saved = row
		if saved != nil && saved.Outcome == "redacted" && sameRecordHash(saved.FactsHash, currentFactsHash) {
			state, reason = "redacted", "prompt_policy"
		}
	}
	private := false
	if f != nil {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM privacy_settings p
			WHERE (p.user_id=$1 OR lower(p.profile_email)=lower($2)) AND
			(p.scope_type='user' OR (p.scope_type='session' AND p.scope_value=$3)
				OR (p.scope_type='project' AND p.scope_value=$4)))`,
			valueString(f.OwnerUserID), valueString(f.OwnerProfileEmail), id, f.ProjectHash).Scan(&private); err != nil {
			return err
		}
	}
	allRedacted := false
	if f != nil && f.TurnBoundaries == "observed" && f.Requests != nil && *f.Requests > 0 {
		var requestCount, redactedCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE is_request),count(*) FILTER(WHERE is_request AND preview_state='redacted')
			FROM session_turn_facts WHERE session_id=$1 AND branch='active'`, id).Scan(&requestCount, &redactedCount); err != nil {
			return err
		}
		allRedacted = requestCount > 0 && redactedCount == requestCount
	}
	consented := false
	if access.RecordRuntimeKey != "" && access.RecordDisclosureVersion != "" && f != nil && (f.OwnerUserID != nil || f.OwnerProfileEmail != nil) {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_ai_record_consents c JOIN dashboard_users u ON u.id=c.dashboard_user_id
			WHERE u.is_active AND c.runtime_key=$1 AND c.disclosure_version=$2
			AND (($3::text IS NOT NULL AND u.cctrace_user_id=$3) OR ($4::text IS NOT NULL AND lower(u.email)=lower($4))))`,
			access.RecordRuntimeKey, access.RecordDisclosureVersion, f.OwnerUserID, f.OwnerProfileEmail).Scan(&consented); err != nil {
			return err
		}
	}
	var jobClaim pgtype.Int8
	var requestedAt, updatedAt pgtype.Timestamptz
	var attempts int
	var lastError string
	if err := tx.QueryRow(ctx, `SELECT claim,requested_at,updated_at,attempts,last_error FROM session_ai_record_jobs WHERE session_id=$1`, id).
		Scan(&jobClaim, &requestedAt, &updatedAt, &attempts, &lastError); err != nil && err != pgx.ErrNoRows {
		return err
	}
	if f != nil {
		switch {
		case f.TurnBoundaries != "observed":
			state, reason = "redacted", "turn_boundaries"
		case saved != nil && saved.Outcome == "redacted" && state == "redacted":
			state, reason = "redacted", "prompt_policy"
		case allRedacted:
			state, reason = "redacted", "prompt_policy"
		case private:
			state, reason = "withheld", "private_scope"
		case f.OwnerUserID == nil && f.OwnerProfileEmail == nil:
			state, reason = "withheld", "mixed_identity"
		case saved != nil && saved.Outcome == "ok" && saved.PromptVersion == sessionrecord.PromptVersion && saved.CoversUntil != nil && f.LastActivityAt != nil && !saved.CoversUntil.Before(*f.LastActivityAt):
			state, reason = "ready", ""
		case saved != nil && saved.Outcome == "ok" && saved.PromptVersion < sessionrecord.PromptVersion && saved.PromptVersion >= sessionrecord.MinServablePromptVersion:
			state, reason = "stale", "prompt_version"
		case saved != nil && saved.Outcome == "ok" && saved.PromptVersion >= sessionrecord.MinServablePromptVersion:
			state, reason = "stale", "new_activity"
		case jobClaim.Valid || requestedAt.Valid:
			state, reason = "generating", ""
		case attempts > 0:
			state, reason = "failed", lastError
		case f.Coverage == "truncated":
			state, reason = "none", "too_large"
		case f.Requests == nil || *f.Requests == 0:
			state, reason = "none", "no_requests"
		case !access.RecordAdminEnabled:
			state, reason = "withheld", "disabled"
		case !access.RecordRuntimeAvailable:
			state, reason = "withheld", recordRuntimeReason(access.RecordRuntimeReason)
		case !consented:
			state, reason = "withheld", "consent_required"
		default:
			quiet, err := sessionRecordWaitingQuiet(ctx, tx, id, f)
			if err != nil {
				return err
			}
			if quiet {
				state, reason = "none", "waiting_quiet"
			} else {
				waiting, err := sessionRecordBudgetWaiting(ctx, tx, access.RecordTimezone)
				if err != nil {
					return err
				}
				if waiting {
					state, reason = "none", "waiting_budget"
				} else {
					state, reason = "none", "queued"
				}
			}
		}
	}
	generatedAt := (*time.Time)(nil)
	coversUntil := (*time.Time)(nil)
	if saved != nil {
		generatedAt = saved.GeneratedAt
		coversUntil = saved.CoversUntil
	} else if updatedAt.Valid {
		t := updatedAt.Time
		generatedAt = &t
	}
	record := map[string]interface{}{"state": state, "reason": nullableRecordReason(reason), "generated_at": analysisTime(generatedAt), "covers_until": analysisTime(coversUntil)}
	if state == "ready" || state == "stale" {
		if saved == nil || len(saved.Content) == 0 {
			return fmt.Errorf("visible session record is missing content")
		}
		var content sessionrecord.RecordContent
		if err := json.Unmarshal(saved.Content, &content); err != nil {
			return err
		}
		built, err := recordContentResponse(ctx, tx, id, f, content, saved)
		if err != nil {
			return err
		}
		for key, value := range built {
			record[key] = value
		}
	}
	response["record"] = record
	if owner {
		if f != nil {
			response["judgement"] = sessionRecordJudgement(f, state, reason, saved, response)
		} else {
			response["judgement"] = noFactSessionRecordJudgement()
		}
	}
	return nil
}

func noFactSessionRecordJudgement() map[string]interface{} {
	checks := []interface{}{}
	for _, key := range []string{"turn_ended", "no_pending_reply", "no_background", "committed"} {
		checks = append(checks, map[string]interface{}{"key": key, "state": "undeterminable", "reason": "no_record", "source": "session_analysis_facts: row unavailable"})
	}
	checks = append(checks, map[string]interface{}{"key": "work_done", "state": "undeterminable", "reason": "no_record"})
	return map[string]interface{}{"status": "unknown", "bucket": nil, "checks": checks}
}

func addSessionRecordOverview(ctx context.Context, tx pgx.Tx, f SessionOverviewFilter, rows []*SessionOverview) error {
	ids := make([]string, 0, len(rows))
	byID := make(map[string]*SessionOverview, len(rows))
	for _, row := range rows {
		ids = append(ids, row.SessionID)
		byID[row.SessionID] = row
		if row.Analysis == nil {
			row.Analysis = map[string]interface{}{}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	gitStatuses, err := readSessionGitStatuses(ctx, tx, ids, f.PersonalUserID, f.PersonalEmail)
	if err != nil {
		return err
	}
	for _, row := range rows {
		row.Analysis["headline"] = nil
		row.Analysis["tags"] = map[string]interface{}{"kinds": []string{}, "components": []string{}, "screens": []string{}, "related": []interface{}{}}
		row.Analysis["record_state"] = "none"
		row.Analysis["record_reason"] = "no_record"
		row.Analysis["repo_status"] = gitStatuses[row.SessionID]
		row.Analysis["judgement"] = rowSessionRecordJudgement(row.Analysis, false)
	}
	owner := `(($2::text<>'' AND p5f.owner_user_id=$2) OR ($2::text='' AND $3::text<>'' AND lower(p5f.owner_profile_email)=lower($3)))`
	query := `SELECT p5r.session_id,p5r.outcome,p5r.prompt_version,p5r.headline,p5r.kinds,p5r.components,p5r.screens,p5r.covers_until,
		p5f.last_activity_at,p5r.work_open,p5r.work_unsettled
		FROM session_ai_records p5r JOIN session_analysis_facts p5f USING(session_id)
		WHERE p5r.session_id=ANY($1::text[]) AND ` + owner + ` AND ` + recordVisibilityPredicate("p5r", "p5f") + `
		AND p5r.owner_user_id IS NOT DISTINCT FROM p5f.owner_user_id
		AND lower(COALESCE(p5r.owner_profile_email,''))=lower(COALESCE(p5f.owner_profile_email,''))`
	result, err := tx.Query(ctx, query, ids, f.PersonalUserID, f.PersonalEmail)
	if err != nil {
		return err
	}
	for result.Next() {
		var id, outcome string
		var version, open, unsettled int
		var headline *string
		var kinds, components, screens []string
		var covers, last *time.Time
		if err := result.Scan(&id, &outcome, &version, &headline, &kinds, &components, &screens, &covers, &last, &open, &unsettled); err != nil {
			result.Close()
			return err
		}
		row := byID[id]
		if row == nil {
			continue
		}
		if outcome == "redacted" {
			row.Analysis["record_state"] = "redacted"
			row.Analysis["record_reason"] = "prompt_policy"
			row.Analysis["judgement"] = rowSessionRecordJudgement(row.Analysis, false)
			continue
		}
		state := "stale"
		if version == sessionrecord.PromptVersion && covers != nil && last != nil && !covers.Before(*last) {
			state = "ready"
		}
		if version < sessionrecord.MinServablePromptVersion {
			state = "none"
		}
		row.Analysis["record_state"] = state
		row.Analysis["record_reason"] = nil
		row.Analysis["headline"] = headline
		row.Analysis["tags"] = map[string]interface{}{"kinds": emptyStrings(kinds), "components": emptyStrings(components), "screens": emptyStrings(screens), "related": []interface{}{}}
		row.Analysis["stale"] = state == "stale"
		row.Analysis["work_open"] = open
		row.Analysis["work_unsettled"] = unsettled
		row.Analysis["judgement"] = rowSessionRecordJudgement(row.Analysis, true)
	}
	if err := result.Err(); err != nil {
		result.Close()
		return err
	}
	result.Close()
	return nil
}

func emptyStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func addCalendarSessionRecords(ctx context.Context, tx pgx.Tx, result *SessionCalendar, f SessionCalendarFilter) error {
	ids := []string{}
	byID := map[string]SessionCalendarSession{}
	for i := range result.Sessions {
		item := result.Sessions[i]
		if _, owner := item.Colors["status"]; !owner {
			continue
		}
		item.Colors["components"] = []string{}
		item.Colors["screens"] = []string{}
		item.Colors["kinds"] = []string{}
		item.Colors["status"] = "unknown"
		result.Sessions[i] = item
		ids = append(ids, item.SessionID)
		byID[item.SessionID] = item
	}
	if len(ids) == 0 {
		return nil
	}
	gitStatuses, err := readSessionGitStatuses(ctx, tx, ids, f.PersonalUserID, f.PersonalEmail)
	if err != nil {
		return err
	}
	activities, err := groupBatchActivity(ctx, tx, f.SessionOverviewFilter, ids)
	if err != nil {
		return err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return err
	}
	turns := make(map[string]map[string]interface{}, len(ids))
	factRows, err := tx.Query(ctx, `SELECT f.session_id,f.last_turn_status,f.detail FROM session_analysis_facts f
		WHERE f.session_id=ANY($1::text[]) AND `+sessionAnalysisVisibleSQL+`
		AND (($2<>'' AND f.owner_user_id=$2) OR ($2='' AND $3<>'' AND lower(f.owner_profile_email)=lower($3)))`,
		ids, f.PersonalUserID, f.PersonalEmail)
	if err != nil {
		return err
	}
	for factRows.Next() {
		var id, lastTurnStatus string
		var raw []byte
		if err = factRows.Scan(&id, &lastTurnStatus, &raw); err != nil {
			break
		}
		var detail sessionfacts.SessionDetail
		if err = json.Unmarshal(raw, &detail); err != nil {
			break
		}
		turns[id] = analysisSignals(detail.Signals, lastTurnStatus)
	}
	if err == nil {
		err = factRows.Err()
	}
	factRows.Close()
	if err != nil {
		return err
	}
	for id, item := range byID {
		analysis := map[string]interface{}{"signals": turns[id], "repo_status": gitStatuses[id]}
		judgement := rowSessionRecordJudgement(analysis, false)
		candidate := &groupCandidate{id: id}
		observedGroupCandidate(candidate, activities[id], now)
		status := "unknown"
		if candidate.category == "waiting" || candidate.category == "running" || candidate.category == "idle" {
			status = "running"
		} else {
			switch judgement["bucket"] {
			case "done":
				status = "done"
			case "leftover":
				status = "leftover"
			case "ended_midway":
				status = "midway"
			}
		}
		item.Colors["status"] = status
		byID[id] = item
	}
	owner := `(($2::text<>'' AND p5f.owner_user_id=$2) OR ($2::text='' AND $3::text<>'' AND lower(p5f.owner_profile_email)=lower($3)))`
	query := `SELECT p5r.session_id,p5r.kinds,p5r.components,p5r.screens
		FROM session_ai_records p5r JOIN session_analysis_facts p5f USING(session_id)
		WHERE p5r.session_id=ANY($1::text[]) AND ` + owner + ` AND p5r.outcome='ok' AND ` + recordVisibilityPredicate("p5r", "p5f") + `
		AND p5r.owner_user_id IS NOT DISTINCT FROM p5f.owner_user_id
		AND lower(COALESCE(p5r.owner_profile_email,''))=lower(COALESCE(p5f.owner_profile_email,''))`
	rows, err := tx.Query(ctx, query, ids, f.UserID, f.ProfileEmail)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var kinds, components, screens []string
		if err := rows.Scan(&id, &kinds, &components, &screens); err != nil {
			rows.Close()
			return err
		}
		item := byID[id]
		item.Colors["kinds"] = emptyStrings(kinds)
		item.Colors["components"] = emptyStrings(components)
		item.Colors["screens"] = emptyStrings(screens)
		byID[id] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for i := range result.Sessions {
		if item, ok := byID[result.Sessions[i].SessionID]; ok {
			result.Sessions[i] = item
		}
	}
	return nil
}

func rowSessionRecordJudgement(analysis map[string]interface{}, hasRecord bool) map[string]interface{} {
	states := []string{"undeterminable", "undeterminable", "undeterminable", "undeterminable", "undeterminable"}
	analysisState, hasAnalysisState := analysis["state"].(string)
	factsUsable := !hasAnalysisState || analysisState == "ready" || analysisState == "stale"
	if signals, ok := analysis["signals"].(map[string]interface{}); ok && factsUsable {
		if value, ok := signals["turn"].(string); ok && value != "" {
			states[0] = turnJudgementState(value)
		} else if signal, ok := signals["turn"].(map[string]interface{}); ok && signal["state"] == "observed" {
			if value, ok := signal["value"].(string); ok && value != "" {
				states[0] = turnJudgementState(value)
			}
		}
		for i, key := range []string{"question", "background"} {
			value := signals[key]
			if signal, ok := value.(map[string]interface{}); ok {
				if signal["state"] != "observed" {
					continue
				}
				value = signal["value"]
			}
			if value, ok := judgementNumber(value); ok {
				if value == 0 {
					states[i+1] = "pass"
				} else {
					states[i+1] = "fail"
				}
			}
		}
	}
	committedSource := analysis["repo_status"]
	if !factsUsable {
		committedSource = nil
	}
	committed := committedJudgementSignal(committedSource)
	states[3] = committed["state"].(string)
	workReason := "no_record"
	if hasRecord {
		workReason = "unvalidated_model"
	}
	bucket := sessionJudgementBucket(states)
	status := bucket
	if category, ok := analysis["category"].(string); ok && (category == "waiting" || category == "running" || category == "idle") {
		turnStatus := ""
		if signals, ok := analysis["signals"].(map[string]interface{}); ok {
			turnStatus, _ = signals["turn"].(string)
			if signal, ok := signals["turn"].(map[string]interface{}); ok {
				turnStatus, _ = signal["value"].(string)
			}
		}
		if turnStatus == "interrupted" {
			status = "interrupted"
		} else {
			status = "running"
		}
	}
	return map[string]interface{}{"status": status, "bucket": bucketValue(bucket), "checks": states, "work_done_reason": workReason,
		"committed_reason": committed["reason"], "committed_source": committed["source"],
		"committed_as_of": committed["as_of"], "committed_count": committed["count"]}
}

func turnJudgementState(status string) string {
	if status == "ended" {
		return "pass"
	}
	return "fail"
}

func judgementNumber(value interface{}) (int64, bool) {
	switch n := value.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if n == float64(int64(n)) {
			return int64(n), true
		}
	case json.Number:
		parsed, err := n.Int64()
		return parsed, err == nil
	}
	return 0, false
}

func committedJudgementSignal(value interface{}) map[string]interface{} {
	const source = "session_snapshots.snapshot.git.uncommitted_edited_files"
	status, ok := value.(map[string]interface{})
	if !ok {
		return map[string]interface{}{"key": "committed", "state": "undeterminable", "reason": "no_git_snapshot", "source": source}
	}
	if status["uncommitted_state"] != "observed" {
		reason, _ := status["uncommitted_reason"].(string)
		if reason == "" {
			reason = "no_git_snapshot"
		}
		return map[string]interface{}{"key": "committed", "state": "undeterminable", "reason": reason, "source": source}
	}
	count, valid := judgementNumber(status["uncommitted_edited_files"])
	if !valid || count < 0 {
		return map[string]interface{}{"key": "committed", "state": "undeterminable", "reason": "invalid_git_snapshot", "source": source}
	}
	result := map[string]interface{}{"key": "committed", "source": source, "as_of": status["changed_at"], "count": count}
	if count == 0 {
		result["state"] = "pass"
	} else {
		result["state"] = "fail"
		result["count"] = count
	}
	return result
}

func sessionJudgementBucket(states []string) string {
	if len(states) != 5 {
		return "unknown"
	}
	if states[0] == "fail" {
		return "ended_midway"
	}
	allPass := true
	for _, state := range states {
		if state == "fail" {
			return "leftover"
		}
		if state != "pass" {
			allPass = false
		}
	}
	if allPass {
		return "done"
	}
	return "unknown"
}

func bucketValue(bucket string) interface{} {
	if bucket == "unknown" {
		return nil
	}
	return bucket
}

func loadSessionRecordRead(ctx context.Context, tx pgx.Tx, id string) (*sessionRecordRead, error) {
	query := `SELECT r.outcome,r.reason,r.runtime,r.model,r.language,r.headline,r.prompt_version,r.facts_hash,r.basis_hash,
		r.covers_until,r.generated_at,r.content,r.kinds,r.components,r.screens,r.covered_requests,r.work_open,r.work_unsettled,r.uncovered_requests
		FROM session_ai_records r JOIN session_analysis_facts f USING(session_id)
		WHERE r.session_id=$1 AND ` + recordVisibilityPredicate("r", "f") + `
		AND r.owner_user_id IS NOT DISTINCT FROM f.owner_user_id
		AND lower(COALESCE(r.owner_profile_email,''))=lower(COALESCE(f.owner_profile_email,''))`
	var row sessionRecordRead
	err := tx.QueryRow(ctx, query, id).Scan(&row.Outcome, &row.Reason, &row.Runtime, &row.Model, &row.Language, &row.Headline, &row.PromptVersion, &row.FactsHash, &row.BasisHash,
		&row.CoversUntil, &row.GeneratedAt, &row.Content, &row.Kinds, &row.Components, &row.Screens, &row.CoveredRequests, &row.WorkOpen, &row.WorkUnsettled, &row.UncoveredRequests)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func nullableRecordReason(reason string) interface{} {
	if reason == "" {
		return nil
	}
	return reason
}
func valueString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func recordRuntimeReason(reason string) string {
	if reason == "runtime_unsupported" {
		return reason
	}
	return "no_runtime"
}

func sessionRecordWaitingQuiet(ctx context.Context, tx pgx.Tx, id string, f *sessionfacts.Session) (bool, error) {
	if f.LastActivityAt == nil {
		return true, nil
	}
	var ended bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_observation_events WHERE session_id=$1 AND kind IN ('session_end','gone') AND observed_at>= $2)`, id, *f.LastActivityAt).Scan(&ended); err != nil {
		return false, err
	}
	delay := 30 * time.Minute
	if ended {
		delay = 3 * time.Minute
	} else if f.LastTurnStatus == "open" {
		delay = 6 * time.Hour
	}
	return time.Since(*f.LastActivityAt) < delay, nil
}

func sessionRecordBudgetWaiting(ctx context.Context, tx pgx.Tx, timezone string) (bool, error) {
	location := time.UTC
	if timezone != "" {
		if loaded, err := time.LoadLocation(timezone); err == nil {
			location = loaded
		}
	}
	var cap pgtype.Int8
	if err := tx.QueryRow(ctx, `SELECT record_daily_token_cap FROM ai_settings WHERE id=1`).Scan(&cap); err != nil && err != pgx.ErrNoRows {
		return false, err
	}
	limit := int64(1_000_000)
	if cap.Valid {
		limit = cap.Int64
	}
	if limit <= 0 {
		return true, nil
	}
	day := time.Now().In(location).Format("2006-01-02")
	var used, reserved int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(input_tokens+output_tokens+estimated_cap_tokens),0) FROM session_ai_record_usage WHERE day=$1::date`, day).Scan(&used); err != nil {
		return false, err
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(reserved_tokens),0) FROM session_ai_record_budget_reservations WHERE day=$1::date`, day).Scan(&reserved); err != nil {
		return false, err
	}
	return used+reserved >= limit, nil
}

func recordContentResponse(ctx context.Context, tx pgx.Tx, id string, f *sessionfacts.Session, content sessionrecord.RecordContent, saved *sessionRecordRead) (map[string]interface{}, error) {
	turns := map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT turn_key FROM session_turn_facts WHERE session_id=$1 AND branch='active'`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, err
		}
		turns[key] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	commits, tasks, related, err := sessionRecordP4Links(ctx, tx, id, turns)
	if err != nil {
		return nil, err
	}
	bullets := make([]interface{}, 0, len(content.Bullets))
	for _, item := range content.Bullets {
		shorts := []string{}
		for _, key := range item.TurnKeys {
			shorts = appendUniqueStrings(shorts, commits[key]...)
		}
		bullets = append(bullets, map[string]interface{}{"text": item.Text, "commits": shorts})
	}
	review := make([]interface{}, 0, len(content.Review))
	for _, item := range content.Review {
		row := map[string]interface{}{"kind": item.Kind, "text": item.Text}
		if turns[item.TurnKey] {
			row["turn_key"] = item.TurnKey
		}
		review = append(review, row)
	}
	work := make([]interface{}, 0, len(content.Work))
	for _, item := range content.Work {
		shorts := appendUniqueStrings([]string{}, commits[item.TurnKey]...)
		taskIDs, taskKeys := []string{}, []string{}
		for _, task := range tasks[item.TurnKey] {
			if len(task) >= 2 {
				taskKeys = appendUniqueStrings(taskKeys, task[0])
				taskIDs = appendUniqueStrings(taskIDs, task[1])
			}
		}
		row := map[string]interface{}{"id": item.ID, "kind": item.Kind, "done": item.Done, "text": item.Text, "commits": shorts, "tasks": taskIDs, "task_keys": taskKeys, "children": item.Children}
		if item.Decision != nil {
			row["decision"] = *item.Decision
		}
		if item.Note != nil {
			row["note"] = *item.Note
		}
		if turns[item.TurnKey] {
			row["turn_key"] = item.TurnKey
		}
		work = append(work, row)
	}
	return map[string]interface{}{"headline": saved.Headline, "bullets": bullets, "review": review, "tags": map[string]interface{}{"kinds": emptyStrings(saved.Kinds), "components": emptyStrings(saved.Components), "screens": emptyStrings(saved.Screens), "related": related}, "work": work, "gaps": emptyStrings(content.Gaps)}, nil
}

func appendUniqueStrings(dst []string, values ...string) []string {
	for _, value := range values {
		if value == "" {
			continue
		}
		seen := false
		for _, old := range dst {
			if old == value {
				seen = true
				break
			}
		}
		if !seen {
			dst = append(dst, value)
		}
	}
	return dst
}

type recordP4Task struct {
	Key, Kind, DisplayID, Title, TitleState, URL string
	Turns                                        []string
}

func sessionRecordP4Links(ctx context.Context, tx pgx.Tx, id string, turns map[string]bool) (map[string][]string, map[string][][]string, []interface{}, error) {
	commits := map[string][]string{}
	tasks := map[string][][]string{}
	related := []interface{}{}
	var hasCommits, hasTasks bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('session_commit_facts') IS NOT NULL,to_regclass('session_task_facts') IS NOT NULL`).Scan(&hasCommits, &hasTasks); err != nil {
		return nil, nil, nil, err
	}
	if hasCommits {
		rows, err := tx.Query(ctx, `SELECT turn_key,hash,short FROM session_commit_facts WHERE session_id=$1 AND turn_key<>'' ORDER BY committed_at`, id)
		if err != nil {
			return nil, nil, nil, err
		}
		for rows.Next() {
			var turn, hash, short string
			if err := rows.Scan(&turn, &hash, &short); err != nil {
				rows.Close()
				return nil, nil, nil, err
			}
			if turns[turn] && confirmedCommitHash.MatchString(hash) {
				if short == "" {
					short = hash[:7]
				}
				commits[turn] = appendUniqueStrings(commits[turn], short)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		rows.Close()
	}
	if hasTasks {
		rows, err := tx.Query(ctx, `SELECT item_key,kind,display_id,title,title_state,url,touches FROM session_task_facts WHERE session_id=$1 ORDER BY first_ts,item_key`, id)
		if err != nil {
			return nil, nil, nil, err
		}
		items := []recordP4Task{}
		for rows.Next() {
			var item recordP4Task
			var raw []byte
			if err := rows.Scan(&item.Key, &item.Kind, &item.DisplayID, &item.Title, &item.TitleState, &item.URL, &raw); err != nil {
				rows.Close()
				return nil, nil, nil, err
			}
			var touches []struct {
				TurnKey string `json:"turn_key"`
			}
			if err := json.Unmarshal(raw, &touches); err != nil {
				rows.Close()
				return nil, nil, nil, err
			}
			for _, touch := range touches {
				if turns[touch.TurnKey] {
					item.Turns = appendUniqueStrings(item.Turns, touch.TurnKey)
				}
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		rows.Close()
		for _, item := range items {
			if len(item.Turns) == 0 {
				continue
			}
			for _, turn := range item.Turns {
				tasks[turn] = append(tasks[turn], []string{item.Key, item.DisplayID})
			}
			ref := map[string]interface{}{"kind": item.Kind, "id": item.DisplayID, "item_key": item.Key}
			if item.URL != "" {
				ref["url"] = item.URL
			}
			related = append(related, ref)
		}
	}
	return commits, tasks, related, nil
}

func sessionRecordJudgement(f *sessionfacts.Session, recordState, recordReason string, saved *sessionRecordRead, response map[string]interface{}) map[string]interface{} {
	checks := []map[string]interface{}{}
	turn := judgementSignal(f.Detail.Signals.Turn, "turn_ended")
	if turn["state"] == "pass" && f.LastTurnStatus != "ended" {
		turn = map[string]interface{}{"key": "turn_ended", "state": "fail"}
	} else {
		turn["key"] = "turn_ended"
	}
	checks = append(checks, turn)
	checks = append(checks, judgementCountSignal(f.Detail.Signals.Question, "no_pending_reply"))
	checks = append(checks, judgementCountSignal(f.Detail.Signals.Background, "no_background"))
	var repoStatus interface{}
	if header, ok := response["header"].(map[string]interface{}); ok {
		repoStatus = header["repo_status"]
	}
	checks = append(checks, committedJudgementSignal(repoStatus))
	work := map[string]interface{}{"key": "work_done", "state": "undeterminable", "reason": "no_record"}
	switch recordState {
	case "withheld":
		work["reason"] = "withheld"
	case "redacted":
		work["reason"] = "redacted"
	case "ready", "stale":
		work["reason"] = "unvalidated_model"
		work["source"] = "P1.5 사람 평가 검증 기준이 충족되지 않음"
	default:
		work["reason"] = "no_record"
	}
	checks = append(checks, work)
	states := make([]string, len(checks))
	for i, check := range checks {
		states[i], _ = check["state"].(string)
	}
	bucket := sessionJudgementBucket(states)
	status := bucket
	if liveSessionRecord(response) {
		if f.LastTurnStatus == "interrupted" {
			status = "interrupted"
		} else {
			status = "running"
		}
	}
	return map[string]interface{}{"status": status, "bucket": bucketValue(bucket), "checks": checks}
}

func judgementSignal(signal sessionfacts.Signal, key string) map[string]interface{} {
	if signal.State != "observed" || signal.Value == nil {
		return map[string]interface{}{"key": key, "state": "undeterminable", "reason": judgementReason(signal.Reason), "source": "session_analysis_facts.detail.signals"}
	}
	return map[string]interface{}{"key": key, "state": "pass"}
}
func judgementCountSignal(signal sessionfacts.Signal, key string) map[string]interface{} {
	if signal.State != "observed" || signal.Value == nil {
		reason := judgementReason(signal.Reason)
		if signal.State == "not_applicable" {
			reason = "not_applicable"
		}
		return map[string]interface{}{"key": key, "state": "undeterminable", "reason": reason, "source": "session_analysis_facts.detail.signals"}
	}
	if *signal.Value > 0 {
		return map[string]interface{}{"key": key, "state": "fail", "count": *signal.Value}
	}
	return map[string]interface{}{"key": key, "state": "pass"}
}
func judgementReason(reason string) string {
	if reason == "" {
		return "no_list"
	}
	return reason
}
func liveSessionRecord(response map[string]interface{}) bool {
	live, ok := response["live"].(map[string]interface{})
	if !ok {
		return false
	}
	instances, _ := live["instances"].([]interface{})
	return len(instances) > 0
}
