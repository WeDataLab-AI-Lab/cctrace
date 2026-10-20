package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"cctrace/internal/sessionrecord"
)

type SessionRecordSearchMatch struct {
	Field   string `json:"field"`
	Text    string `json:"text"`
	TurnKey string `json:"turn_key,omitempty"`
	ItemKey string `json:"item_key,omitempty"`
}

type SessionRecordSearchResult struct {
	SessionID      string                     `json:"session_id"`
	Title          *string                    `json:"title"`
	LastActivityAt *time.Time                 `json:"last_activity_at"`
	Matches        []SessionRecordSearchMatch `json:"matches"`
}

type SessionRecordRangeStatus struct {
	State         string         `json:"state"`
	Written       *int           `json:"written,omitempty"`
	Total         int            `json:"total"`
	Stale         int            `json:"stale"`
	Writing       int            `json:"writing"`
	Blocked       map[string]int `json:"blocked"`
	WaitingBudget bool           `json:"waiting_budget"`
}

type SessionRecordVocabulary struct {
	RepositoryKey    string   `json:"repository_key"`
	Components       []string `json:"components"`
	Screens          []string `json:"screens"`
	ComponentsClosed bool     `json:"components_closed"`
	ScreensClosed    bool     `json:"screens_closed"`
}

var (
	ErrSessionRecordRateLimited = errors.New("session record regeneration rate limited")
	ErrSessionRecordBusy        = errors.New("session record generation is already running")
	ErrSessionRecordNotEligible = errors.New("session is not eligible for a session record request")
	ErrSessionRecordDisabled    = errors.New("session records are disabled")
)

func (s *PgStore) SessionRecordConsent(ctx context.Context, dashboardUserID int64, runtimeKey, disclosure string) (bool, *time.Time, error) {
	var version string
	var consentedAt time.Time
	err := s.pool.QueryRow(ctx, `SELECT disclosure_version,consented_at FROM session_ai_record_consents
		WHERE dashboard_user_id=$1 AND runtime_key=$2`, dashboardUserID, runtimeKey).Scan(&version, &consentedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("read session record consent: %w", err)
	}
	if version != disclosure {
		return false, nil, nil
	}
	return true, &consentedAt, nil
}

func (s *PgStore) GetSessionRecordVocabulary(ctx context.Context, repositoryKey string) (SessionRecordVocabulary, error) {
	result := SessionRecordVocabulary{RepositoryKey: repositoryKey, Components: []string{}, Screens: []string{}}
	rows, err := s.pool.Query(ctx, `SELECT axis,term FROM session_ai_record_vocab_pins WHERE repository_key=$1 ORDER BY axis,position,term`, repositoryKey)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var axis, term string
		if err := rows.Scan(&axis, &term); err != nil {
			return result, err
		}
		if axis == "component" {
			result.Components = append(result.Components, term)
		}
		if axis == "screen" {
			result.Screens = append(result.Screens, term)
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	result.ComponentsClosed = len(result.Components) > 0
	result.ScreensClosed = len(result.Screens) > 0
	return result, nil
}

func (s *PgStore) SetSessionRecordVocabulary(ctx context.Context, repositoryKey, axis string, terms []string, actor string) error {
	if strings.TrimSpace(repositoryKey) == "" || len(repositoryKey) > 256 || axis != "component" && axis != "screen" || len(terms) > 100 {
		return fmt.Errorf("invalid session record vocabulary")
	}
	canonical := make([]string, 0, len(terms))
	seen := map[string]bool{}
	for _, term := range terms {
		normalized, ok := sessionrecord.NormalizeTag(term, true)
		if !ok {
			return fmt.Errorf("invalid vocabulary term")
		}
		key := strings.ToLower(normalized)
		if !seen[key] {
			seen[key] = true
			canonical = append(canonical, normalized)
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('session_overview_retention'))`); err != nil {
		return err
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_ai_record_vocab_pins WHERE repository_key=$1 AND axis=$2`, repositoryKey, axis); err != nil {
		return err
	}
	for i, term := range canonical {
		if _, err := tx.Exec(ctx, `INSERT INTO session_ai_record_vocab_pins(repository_key,axis,term,position,created_by) VALUES($1,$2,$3,$4,$5)`, repositoryKey, axis, term, i, actor); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PgStore) SetSessionRecordVocabularies(ctx context.Context, repositoryKey string, components, screens []string, actor string) error {
	if strings.TrimSpace(repositoryKey) == "" || len(repositoryKey) > 256 {
		return fmt.Errorf("invalid session record repository")
	}
	componentTerms, err := normalizeSessionRecordTerms(components)
	if err != nil {
		return err
	}
	screenTerms, err := normalizeSessionRecordTerms(screens)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('session_overview_retention'))`); err != nil {
		return err
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_ai_record_vocab_pins WHERE repository_key=$1`, repositoryKey); err != nil {
		return err
	}
	for _, group := range []struct {
		axis  string
		terms []string
	}{{"component", componentTerms}, {"screen", screenTerms}} {
		for position, term := range group.terms {
			if _, err := tx.Exec(ctx, `INSERT INTO session_ai_record_vocab_pins(repository_key,axis,term,position,created_by) VALUES($1,$2,$3,$4,$5)`, repositoryKey, group.axis, term, position, actor); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func normalizeSessionRecordTerms(terms []string) ([]string, error) {
	if len(terms) > 100 {
		return nil, fmt.Errorf("too many vocabulary terms")
	}
	canonical := make([]string, 0, len(terms))
	seen := map[string]bool{}
	for _, term := range terms {
		normalized, ok := sessionrecord.NormalizeTag(term, true)
		if !ok {
			return nil, fmt.Errorf("invalid vocabulary term")
		}
		key := strings.ToLower(normalized)
		if !seen[key] {
			seen[key] = true
			canonical = append(canonical, normalized)
		}
	}
	return canonical, nil
}

func (s *PgStore) MergeSessionRecordVocabulary(ctx context.Context, repositoryKey, axis, from, to, actor string) error {
	if strings.TrimSpace(repositoryKey) == "" || axis != "component" && axis != "screen" {
		return fmt.Errorf("invalid session record vocabulary")
	}
	from, okFrom := sessionrecord.NormalizeTag(from, true)
	to, okTo := sessionrecord.NormalizeTag(to, true)
	if !okFrom || !okTo || strings.EqualFold(from, to) {
		return fmt.Errorf("invalid vocabulary merge")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('session_overview_retention'))`); err != nil {
		return err
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return err
	}
	type recordTags struct {
		id                  string
		components, screens []string
		content             []byte
	}
	rows, err := tx.Query(ctx, `SELECT session_id,components,screens,content FROM session_ai_records WHERE repository_key=$1 AND outcome='ok'`, repositoryKey)
	if err != nil {
		return err
	}
	values := make([]recordTags, 0)
	for rows.Next() {
		var item recordTags
		if err := rows.Scan(&item.id, &item.components, &item.screens, &item.content); err != nil {
			rows.Close()
			return err
		}
		values = append(values, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range values {
		var content sessionrecord.RecordContent
		if len(item.content) > 0 {
			if err := json.Unmarshal(item.content, &content); err != nil {
				return err
			}
		}
		if axis == "component" {
			item.components = mergeVocabularyTerm(item.components, from, to)
			content.NewTerms.Component = mergeVocabularyTerm(content.NewTerms.Component, from, to)
		}
		if axis == "screen" {
			item.screens = mergeVocabularyTerm(item.screens, from, to)
			content.NewTerms.Screen = mergeVocabularyTerm(content.NewTerms.Screen, from, to)
		}
		encoded, err := json.Marshal(content)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE session_ai_records SET components=$2,screens=$3,content=$4::jsonb WHERE session_id=$1`, item.id, item.components, item.screens, encoded); err != nil {
			return err
		}
	}
	var position int
	if err := tx.QueryRow(ctx, `SELECT position FROM session_ai_record_vocab_pins WHERE repository_key=$1 AND axis=$2 AND term=$3`, repositoryKey, axis, from).Scan(&position); err != nil && err != pgx.ErrNoRows {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO session_ai_record_vocab_pins(repository_key,axis,term,position,created_by)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT(repository_key,axis,term) DO NOTHING`, repositoryKey, axis, to, position, actor); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_ai_record_vocab_pins WHERE repository_key=$1 AND axis=$2 AND term=$3`, repositoryKey, axis, from); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func mergeVocabularyTerm(values []string, from, to string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		if strings.EqualFold(value, from) {
			value = to
		}
		key := strings.ToLower(value)
		if !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	return out
}

func (s *PgStore) GrantSessionRecordConsent(ctx context.Context, dashboardUserID int64, runtimeKey, disclosure string) error {
	if dashboardUserID <= 0 || strings.TrimSpace(runtimeKey) == "" || strings.TrimSpace(disclosure) == "" {
		return fmt.Errorf("invalid session record consent")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('session_overview_retention'))`); err != nil {
		return err
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT is_active FROM dashboard_users WHERE id=$1 FOR UPDATE`, dashboardUserID).Scan(&active); err != nil {
		return err
	}
	if !active {
		return fmt.Errorf("inactive dashboard user cannot consent")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO session_ai_record_consents(dashboard_user_id,runtime_key,disclosure_version,consented_at)
		VALUES($1,$2,$3,now()) ON CONFLICT(dashboard_user_id,runtime_key) DO UPDATE SET
		disclosure_version=EXCLUDED.disclosure_version,consented_at=now()`, dashboardUserID, runtimeKey, disclosure); err != nil {
		return fmt.Errorf("write session record consent: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *PgStore) RevokeSessionRecordConsent(ctx context.Context, dashboardUserID int64, runtimeKey string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('session_overview_retention'))`); err != nil {
		return err
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_ai_record_consents WHERE dashboard_user_id=$1 AND runtime_key=$2`, dashboardUserID, runtimeKey); err != nil {
		return fmt.Errorf("revoke session record consent: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *PgStore) RequestSessionRecord(ctx context.Context, sessionID, ownerID, profileEmail string, useUserID bool,
	dashboardUserID int64, runtimeKey, disclosure string, force bool, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('session_overview_retention'))`); err != nil {
		return err
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return err
	}
	if err := checkSessionRecordRequestPolicy(ctx, tx, ownerID, profileEmail, useUserID, dashboardUserID, runtimeKey, disclosure); err != nil {
		return err
	}
	const eligibleSQL = `SELECT EXISTS(SELECT 1 FROM session_analysis_facts f WHERE f.session_id=$7 AND ` + sessionRecordOwnerScopeSQL + `
		AND $4::timestamptz IS NOT NULL AND $5::int > 0 AND $6::bigint > 0 AND ` + sessionRecordCandidatePredicateSQL + `)`
	args := []any{ownerID, profileEmail, useUserID, now, sessionrecord.PromptVersion, dashboardUserID, sessionID}
	var eligible bool
	if err := tx.QueryRow(ctx, eligibleSQL, args...).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return ErrSessionRecordNotEligible
	}
	var recent bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_ai_records WHERE session_id=$1 AND generated_at>$2::timestamptz-interval '10 minutes')
		OR EXISTS(SELECT 1 FROM session_ai_record_jobs WHERE session_id=$1 AND requested_at>$2::timestamptz-interval '10 minutes')`, sessionID, now).Scan(&recent); err != nil {
		return err
	}
	if recent {
		return ErrSessionRecordRateLimited
	}
	result, err := tx.Exec(ctx, `INSERT INTO session_ai_record_jobs(session_id,lane,requested_at,requested_by,force,retry_at,last_error,updated_at)
		VALUES($1,'manual',$2,$3,$4,NULL,'',$2)
		ON CONFLICT(session_id) DO UPDATE SET lane='manual',requested_at=EXCLUDED.requested_at,requested_by=EXCLUDED.requested_by,
		force=EXCLUDED.force,retry_at=NULL,last_error='',updated_at=EXCLUDED.updated_at
		WHERE session_ai_record_jobs.claim IS NULL`, sessionID, now, dashboardUserID, force)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrSessionRecordBusy
	}
	return tx.Commit(ctx)
}

func (s *PgStore) RequestSessionRecordRange(ctx context.Context, dashboardUserID int64, ownerID, profileEmail string, useUserID bool,
	runtimeKey, disclosure string, from, to, now time.Time) error {
	if dashboardUserID <= 0 || from.IsZero() || !from.Before(to) || to.Sub(from) > 31*24*time.Hour {
		return ErrSessionCalendarRange
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('session_overview_retention'))`); err != nil {
		return err
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return err
	}
	if err := checkSessionRecordRequestPolicy(ctx, tx, ownerID, profileEmail, useUserID, dashboardUserID, runtimeKey, disclosure); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO session_ai_record_range_requests(dashboard_user_id,range_from,range_to,requested_at,served,finished_at)
		VALUES($1,$2,$3,$4,0,NULL) ON CONFLICT(dashboard_user_id) DO UPDATE SET range_from=EXCLUDED.range_from,
		range_to=EXCLUDED.range_to,requested_at=EXCLUDED.requested_at,served=0,finished_at=NULL`, dashboardUserID, from, to, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) SessionRecordRangeStatus(ctx context.Context, dashboardUserID int64, ownerID, profileEmail string, useUserID bool,
	runtimeKey, disclosure string, from, to, now time.Time) (SessionRecordRangeStatus, error) {
	status := SessionRecordRangeStatus{State: "fresh", Blocked: map[string]int{}}
	if from.IsZero() || !from.Before(to) || to.Sub(from) > 31*24*time.Hour {
		return status, ErrSessionCalendarRange
	}
	settings, err := s.GetSessionAIRecordSettings(ctx)
	if err != nil {
		return status, err
	}
	consented, _, err := s.SessionRecordConsent(ctx, dashboardUserID, runtimeKey, disclosure)
	if err != nil {
		return status, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return status, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='2s'`); err != nil {
		return status, err
	}
	query := `SELECT f.session_id,f.agent,f.coverage,f.turn_boundaries,COALESCE(f.requests,0),f.project_hash,f.last_activity_at,f.output_hash,
		r.outcome,r.prompt_version,r.covers_until,r.facts_hash,
		(j.claim IS NOT NULL OR j.requested_at IS NOT NULL),COALESCE(j.attempts,0),
		EXISTS(SELECT 1 FROM privacy_settings p WHERE (p.user_id=$1 OR lower(p.profile_email)=lower($2))
			AND (p.scope_type='user' OR (p.scope_type='session' AND p.scope_value=f.session_id)
				OR (p.scope_type='project' AND p.scope_value=f.project_hash))),
		EXISTS(SELECT 1 FROM visible_session_records sr WHERE sr.session_id=f.session_id
			AND (($3::boolean AND sr.user_id IS DISTINCT FROM $1) OR (NOT $3::boolean AND lower(sr.profile_email)<>lower($2))))
		OR EXISTS(SELECT 1 FROM visible_events e WHERE e.session_id=f.session_id AND e.event_name='api_request'
			AND (($3::boolean AND e.user_id IS DISTINCT FROM $1) OR (NOT $3::boolean AND lower(e.profile_email)<>lower($2))))
		OR EXISTS(SELECT 1 FROM session_snapshots n WHERE n.session_id=f.session_id
			AND (($3::boolean AND n.user_id IS DISTINCT FROM $1) OR (NOT $3::boolean AND lower(n.profile_email)<>lower($2))))
		FROM session_analysis_facts f
		LEFT JOIN session_ai_records r ON r.session_id=f.session_id AND ` + recordVisibilityPredicate("r", "f") + `
		LEFT JOIN session_ai_record_jobs j ON j.session_id=f.session_id
		WHERE ` + sessionRecordOwnerScopeSQL + ` AND f.last_activity_at >= $4::timestamptz AND f.last_activity_at <= $5::timestamptz
		AND ` + sessionAnalysisVisibleSQL + `
		ORDER BY f.last_activity_at DESC`
	rows, err := tx.Query(ctx, query, ownerID, profileEmail, useUserID, from, to)
	if err != nil {
		return status, err
	}
	type statusRow struct {
		agent, coverage, boundaries, project string
		requests                             int
		activity                             time.Time
		outputHash                           []byte
		outcome                              *string
		promptVersion                        *int
		covers                               *time.Time
		factsHash                            []byte
		working                              bool
		attempts                             int
		private, foreign                     bool
	}
	values := make([]statusRow, 0)
	for rows.Next() {
		var row statusRow
		var outcome pgtype.Text
		var version pgtype.Int4
		var covers pgtype.Timestamptz
		var factsHash []byte
		if err := rows.Scan(new(string), &row.agent, &row.coverage, &row.boundaries, &row.requests, &row.project, &row.activity, &row.outputHash,
			&outcome, &version, &covers, &factsHash, &row.working, &row.attempts, &row.private, &row.foreign); err != nil {
			rows.Close()
			return status, err
		}
		if outcome.Valid {
			v := outcome.String
			row.outcome = &v
		}
		if version.Valid {
			v := int(version.Int32)
			row.promptVersion = &v
		}
		if covers.Valid {
			v := covers.Time
			row.covers = &v
		}
		row.factsHash = factsHash
		values = append(values, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return status, err
	}
	rows.Close()
	for _, row := range values {
		status.Total++
		if row.private {
			status.Blocked["private_scope"]++
			continue
		}
		if row.boundaries != "observed" || row.outcome != nil && *row.outcome == "redacted" && bytes.Equal(row.factsHash, row.outputHash) {
			status.Blocked["redacted"]++
			continue
		}
		if row.foreign || row.agent != "claude" && row.agent != "codex" || row.coverage != "full" || row.requests <= 0 {
			continue
		}
		if !settings.Enabled {
			status.Blocked["disabled"]++
			continue
		}
		if !consented {
			status.Blocked["consent_required"]++
			continue
		}
		if row.working {
			status.Writing++
			continue
		}
		fresh := row.outcome != nil && *row.outcome == "ok" && row.promptVersion != nil && *row.promptVersion == sessionrecord.PromptVersion && row.covers != nil && !row.covers.Before(row.activity)
		if !fresh {
			status.Stale++
		}
	}
	var rangePending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_ai_record_range_requests WHERE dashboard_user_id=$1 AND finished_at IS NULL)`, dashboardUserID).Scan(&rangePending); err != nil {
		return status, err
	}
	if rangePending && status.Writing == 0 {
		status.Writing = 1
	}
	if status.Writing > 0 {
		status.State = "writing"
	} else if status.Stale > 0 {
		status.State = "stale"
	}
	var written pgtype.Int4
	if err := tx.QueryRow(ctx, `SELECT served FROM session_ai_record_range_requests WHERE dashboard_user_id=$1`, dashboardUserID).Scan(&written); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return status, err
	}
	if written.Valid {
		v := int(written.Int32)
		status.Written = &v
	}
	var cap pgtype.Int8
	if settings.DailyTokenCap != nil {
		cap.Int64 = *settings.DailyTokenCap
		cap.Valid = true
	}
	var used, reserved int64
	day := now.UTC().Format("2006-01-02")
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(input_tokens+output_tokens+estimated_cap_tokens),0) FROM session_ai_record_usage WHERE day=$1::date`, day).Scan(&used); err != nil {
		return status, err
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(reserved_tokens),0) FROM session_ai_record_budget_reservations WHERE day=$1::date`, day).Scan(&reserved); err != nil {
		return status, err
	}
	limit := int64(1_000_000)
	if cap.Valid {
		limit = cap.Int64
	}
	status.WaitingBudget = limit <= 0 || used+reserved >= limit
	return status, tx.Commit(ctx)
}

func checkSessionRecordRequestPolicy(ctx context.Context, tx pgx.Tx, ownerID, profileEmail string, useUserID bool,
	dashboardUserID int64, runtimeKey, disclosure string) error {
	var enabled bool
	var configuredRuntime, weeklyRuntime string
	if err := tx.QueryRow(ctx, `SELECT record_enabled,record_runtime,runtime FROM ai_settings WHERE id=1`).Scan(&enabled, &configuredRuntime, &weeklyRuntime); err != nil && err != pgx.ErrNoRows {
		return err
	}
	if !enabled {
		return ErrSessionRecordDisabled
	}
	if !strings.Contains(runtimeKey, ":") {
		return ErrSessionRecordNotEligible
	}
	runtime := strings.SplitN(runtimeKey, ":", 2)[0]
	if configuredRuntime != "" && configuredRuntime != runtime || configuredRuntime == "" && weeklyRuntime != "" && weeklyRuntime != runtime {
		return ErrSessionRecordNotEligible
	}
	var consented bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_ai_record_consents c JOIN dashboard_users u ON u.id=c.dashboard_user_id
		WHERE c.dashboard_user_id=$1 AND u.is_active AND c.runtime_key=$2 AND c.disclosure_version=$3
		AND (($4::boolean AND u.cctrace_user_id=$5) OR (NOT $4::boolean AND lower(u.email)=lower($6))))`,
		dashboardUserID, runtimeKey, disclosure, useUserID, ownerID, profileEmail).Scan(&consented); err != nil {
		return err
	}
	if !consented {
		return ErrSessionRecordNotEligible
	}
	var identityMatches bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dashboard_users WHERE id=$1 AND is_active
		AND (($2::boolean AND cctrace_user_id=$3) OR (NOT $2::boolean AND lower(email)=lower($4))))`, dashboardUserID, useUserID, ownerID, profileEmail).Scan(&identityMatches); err != nil {
		return err
	}
	if !identityMatches {
		return ErrSessionRecordNotEligible
	}
	return nil
}

// DeleteOwnSessionAIRecords removes derived records and work state for exactly
// the caller's current identity. Consent and usage accounting are separate data.
func (s *PgStore) DeleteOwnSessionAIRecords(ctx context.Context, ownerID, profileEmail string, useUserID bool) (int64, error) {
	if useUserID && strings.TrimSpace(ownerID) == "" || !useUserID && strings.TrimSpace(profileEmail) == "" {
		return 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('session_overview_retention'))`); err != nil {
		return 0, err
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return 0, err
	}
	const owned = `(($3::boolean AND NULLIF($1::text,'') IS NOT NULL AND f.owner_user_id=$1)
		OR (NOT $3::boolean AND NULLIF($2::text,'') IS NOT NULL AND lower(f.owner_profile_email)=lower($2)))`
	if _, err := tx.Exec(ctx, `DELETE FROM session_ai_record_jobs j USING session_analysis_facts f
		WHERE j.session_id=f.session_id AND `+owned, ownerID, profileEmail, useUserID); err != nil {
		return 0, fmt.Errorf("delete owned session record jobs: %w", err)
	}
	result, err := tx.Exec(ctx, `DELETE FROM session_ai_records r USING session_analysis_facts f
		WHERE r.session_id=f.session_id AND `+owned, ownerID, profileEmail, useUserID)
	if err != nil {
		return 0, fmt.Errorf("delete owned session records: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// SearchMySessionRecords searches only the caller's own facts and visible derived
// records. It never reads session_records.raw or personal note storage.
func (s *PgStore) SearchMySessionRecords(ctx context.Context, query, ownerID, profileEmail string, useUserID, includeRecords bool, limit int) ([]SessionRecordSearchResult, error) {
	query = strings.TrimSpace(query)
	if len([]rune(query)) < 2 || len([]rune(query)) > 100 {
		return nil, fmt.Errorf("search query must be 2 to 100 characters")
	}
	if useUserID && strings.TrimSpace(ownerID) == "" || !useUserID && strings.TrimSpace(profileEmail) == "" {
		return []SessionRecordSearchResult{}, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='2s'`); err != nil {
		return nil, err
	}
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
	pattern := "%" + escaped + "%"
	owner := `(($3::boolean AND NULLIF($1::text,'') IS NOT NULL AND f.owner_user_id=$1)
		OR (NOT $3::boolean AND NULLIF($2::text,'') IS NOT NULL AND lower(f.owner_profile_email)=lower($2)))`
	visible := `NOT EXISTS(SELECT 1 FROM excluded_sessions x WHERE x.session_id=f.session_id)
		AND NOT EXISTS(SELECT 1 FROM jsonb_to_recordset(f.account_keys) AS k(login_email text,billing_provider text,account_id text)
			WHERE NOT (` + excludedAccountPredicateSQL("k") + `))
		AND NOT EXISTS(SELECT 1 FROM privacy_settings p WHERE (p.user_id=$1 OR lower(p.profile_email)=lower($2))
			AND (p.scope_type='user' OR (p.scope_type='session' AND p.scope_value=f.session_id)
				OR (p.scope_type='project' AND p.scope_value=f.project_hash)))
		AND NOT EXISTS(SELECT 1 FROM visible_session_records sr WHERE sr.session_id=f.session_id
			AND (($3::boolean AND sr.user_id IS DISTINCT FROM $1) OR (NOT $3::boolean AND lower(sr.profile_email)<>lower($2))))
		AND NOT EXISTS(SELECT 1 FROM visible_events e WHERE e.session_id=f.session_id AND e.event_name='api_request'
			AND (($3::boolean AND e.user_id IS DISTINCT FROM $1) OR (NOT $3::boolean AND lower(e.profile_email)<>lower($2))))
		AND NOT EXISTS(SELECT 1 FROM session_snapshots n WHERE n.session_id=f.session_id
			AND (($3::boolean AND n.user_id IS DISTINCT FROM $1) OR (NOT $3::boolean AND lower(n.profile_email)<>lower($2))))`
	hit := `SELECT f.session_id,1 AS rank FROM session_analysis_facts f WHERE ` + owner + ` AND ` + visible + `
		AND f.title_state IN ('ok','withheld') AND f.title ILIKE $4 ESCAPE E'\\'`
	if includeRecords {
		hit += ` UNION ALL SELECT r.session_id,2 FROM session_ai_records r JOIN session_analysis_facts f USING(session_id)
			WHERE ` + owner + ` AND ` + visible + ` AND r.outcome='ok' AND r.search_text ILIKE $4 ESCAPE E'\\'
			AND ` + recordVisibilityPredicate("r", "f")
	}
	var hasCommits, hasTasks bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('session_commit_facts') IS NOT NULL,to_regclass('session_task_facts') IS NOT NULL`).Scan(&hasCommits, &hasTasks); err != nil {
		return nil, err
	}
	if hasCommits {
		hit += ` UNION ALL SELECT c.session_id,3 FROM session_commit_facts c JOIN session_analysis_facts f USING(session_id)
			WHERE ` + owner + ` AND ` + visible + ` AND c.subject ILIKE $4 ESCAPE E'\\'`
	}
	if hasTasks {
		hit += ` UNION ALL SELECT t.session_id,3 FROM session_task_facts t JOIN session_analysis_facts f USING(session_id)
			WHERE ` + owner + ` AND ` + visible + ` AND (t.title ILIKE $4 ESCAPE E'\\' OR t.display_id ILIKE $4 ESCAPE E'\\' OR t.item_key ILIKE $4 ESCAPE E'\\')`
	}
	rows, err := tx.Query(ctx, `WITH hit AS (`+hit+`), matched AS (
		SELECT f.session_id,MIN(hit.rank) AS rank,f.title,f.last_activity_at
		FROM hit JOIN session_analysis_facts f USING(session_id)
		GROUP BY f.session_id,f.title,f.last_activity_at)
		SELECT session_id,rank,title,last_activity_at FROM matched ORDER BY rank,last_activity_at DESC LIMIT $5`, ownerID, profileEmail, useUserID, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("search own session records: %w", err)
	}
	type searchHit struct {
		result SessionRecordSearchResult
		rank   int
	}
	hits := make([]searchHit, 0)
	for rows.Next() {
		var hit searchHit
		if err := rows.Scan(&hit.result.SessionID, &hit.rank, &hit.result.Title, &hit.result.LastActivityAt); err != nil {
			rows.Close()
			return nil, err
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	results := make([]SessionRecordSearchResult, 0, len(hits))
	for _, hit := range hits {
		result := hit.result
		result.Matches = []SessionRecordSearchMatch{}
		if result.Title != nil && strings.Contains(strings.ToLower(*result.Title), strings.ToLower(query)) {
			result.Matches = append(result.Matches, SessionRecordSearchMatch{Field: "title", Text: *result.Title})
		}
		if includeRecords && hit.rank == 2 && len(result.Matches) < 3 {
			matches, err := sessionRecordSearchMatches(ctx, tx, result.SessionID, query)
			if err != nil {
				return nil, err
			}
			result.Matches = append(result.Matches, matches...)
		}
		if hit.rank == 3 && len(result.Matches) < 3 {
			matches, err := p4SessionSearchMatches(ctx, tx, result.SessionID, query, hasCommits, hasTasks)
			if err != nil {
				return nil, err
			}
			result.Matches = append(result.Matches, matches...)
		}
		if len(result.Matches) > 3 {
			result.Matches = result.Matches[:3]
		}
		results = append(results, result)
	}
	return results, tx.Commit(ctx)
}

func sessionRecordSearchMatches(ctx context.Context, tx pgx.Tx, id, query string) ([]SessionRecordSearchMatch, error) {
	var headline string
	var body []byte
	err := tx.QueryRow(ctx, `SELECT r.headline,r.content FROM session_ai_records r JOIN session_analysis_facts f USING(session_id)
		WHERE r.session_id=$1 AND r.outcome='ok' AND `+recordVisibilityPredicate("r", "f"), id).Scan(&headline, &body)
	if err != nil {
		return nil, err
	}
	var content sessionrecord.RecordContent
	if err := json.Unmarshal(body, &content); err != nil {
		return nil, err
	}
	matches := make([]SessionRecordSearchMatch, 0, 3)
	add := func(field, text, turn string) {
		if len(matches) < 3 && strings.Contains(strings.ToLower(text), strings.ToLower(query)) {
			matches = append(matches, SessionRecordSearchMatch{Field: field, Text: text, TurnKey: turn})
		}
	}
	add("headline", headline, "")
	for _, bullet := range content.Bullets {
		add("bullet", bullet.Text, firstString(bullet.TurnKeys))
	}
	for _, review := range content.Review {
		add("review", review.Text, review.TurnKey)
	}
	for _, work := range content.Work {
		add("work", work.Text, work.TurnKey)
		if work.Decision != nil {
			add("work", *work.Decision, work.TurnKey)
		}
		if work.Note != nil {
			add("work", *work.Note, work.TurnKey)
		}
		for _, child := range work.Children {
			add("work", child.Text, work.TurnKey)
		}
	}
	return matches, nil
}

func p4SessionSearchMatches(ctx context.Context, tx pgx.Tx, id, query string, commits, tasks bool) ([]SessionRecordSearchMatch, error) {
	matches := make([]SessionRecordSearchMatch, 0, 3)
	if commits {
		rows, err := tx.Query(ctx, `SELECT subject,turn_key FROM session_commit_facts WHERE session_id=$1 AND subject ILIKE $2 ESCAPE E'\\' ORDER BY committed_at DESC LIMIT 3`, id, "%"+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)+"%")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var text, turn string
			if err := rows.Scan(&text, &turn); err != nil {
				rows.Close()
				return nil, err
			}
			matches = append(matches, SessionRecordSearchMatch{Field: "commit", Text: text, TurnKey: turn})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	if tasks && len(matches) < 3 {
		rows, err := tx.Query(ctx, `SELECT item_key,display_id,title FROM session_task_facts WHERE session_id=$1
			AND (title ILIKE $2 ESCAPE E'\\' OR display_id ILIKE $2 ESCAPE E'\\' OR item_key ILIKE $2 ESCAPE E'\\') ORDER BY first_ts LIMIT $3`,
			id, "%"+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)+"%", 3-len(matches))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key, id, text string
			if err := rows.Scan(&key, &id, &text); err != nil {
				rows.Close()
				return nil, err
			}
			text = strings.TrimSpace(id + " " + text)
			matches = append(matches, SessionRecordSearchMatch{Field: "task", Text: text, ItemKey: key})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return matches, nil
}

func firstString(values []string) string {
	if len(values) > 0 {
		return values[0]
	}
	return ""
}
