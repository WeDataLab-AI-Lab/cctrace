package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var (
	ErrPersonalNotFound = errors.New("personal session not found")
	ErrPersonalConflict = errors.New("personal revision conflict")
	ErrPersonalInvalid  = errors.New("invalid personal state")
)

const personalLifetime = 90 * 24 * time.Hour

// PersonalSessionState is private to the authenticated dashboard identity.
type PersonalSessionState struct {
	SessionID      string        `json:"session_id"`
	Classification string        `json:"classification"`
	Note           *PersonalNote `json:"note"`
	Revision       int64         `json:"revision"`
	ExpiresAt      time.Time     `json:"expires_at"`
}

type PersonalNote struct {
	Text      string    `json:"text"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PersonalGlobalNote struct {
	Text      string    `json:"text"`
	UpdatedAt time.Time `json:"updated_at"`
	Revision  int64     `json:"revision"`
	ExpiresAt time.Time `json:"expires_at"`
}

// PersonalOverviewValues contains only the fields displayed on an overview row.
type PersonalOverviewValues struct {
	Classification string
	FirstLine      *string
}

type PersonalSessionNoteSummary struct {
	SessionID string    `json:"session_id"`
	ShortID   string    `json:"short_id"`
	Title     string    `json:"title"`
	FirstLine string    `json:"first_line"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PersonalNotePage struct {
	Items      []PersonalSessionNoteSummary `json:"items"`
	NextCursor string                       `json:"next_cursor,omitempty"`
	Truncated  bool                         `json:"truncated"`
}

type PersonalClassificationCounts struct {
	Scope  string           `json:"scope"`
	Total  int64            `json:"total"`
	Counts map[string]int64 `json:"counts"`
}

// PersonalCountFilter uses the same browsing dimensions as session overview.
// Identity is always supplied separately from the authenticated dashboard user.
type PersonalCountFilter struct {
	Since, Until   *time.Time
	ProjectHashes  []string
	Agent, Source  string
	Classification string
	FoldLineage    bool
}

type personalOwner struct {
	userID, email, proof string
	keys                 []personalIdentity
	verified             []personalVerifiedIdentity
}

type personalIdentity struct{ userID, email string }

type personalVerifiedIdentity struct {
	identity personalIdentity
	proof    string
}

func validPersonalText(text string) bool { return utf8.ValidString(text) && len(text) <= 16384 }

func validPersonalClass(class string) bool {
	switch class {
	case "unsorted", "consulting", "working", "later", "done":
		return true
	}
	return false
}

// personalSourceOwner checks every source row, not merely the one that matches the
// caller. An untrusted shared-key observation can veto ownership but cannot prove it.
func personalSessionOwner(ctx context.Context, tx pgx.Tx, sessionID, callerEmail, callerUserID string) (*personalOwner, error) {
	return personalSourceOwner(ctx, tx, sessionID, callerEmail, callerUserID, true)
}

func personalSourceOwner(ctx context.Context, tx pgx.Tx, sessionID, callerEmail, callerUserID string, lockSession bool) (*personalOwner, error) {
	if sessionID == "" || (callerEmail == "" && callerUserID == "") {
		return nil, nil
	}
	if lockSession {
		if err := lockSessionDeletionTargets(ctx, tx, []string{sessionID}); err != nil {
			return nil, err
		}
	}
	var deleted bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deleted_sessions WHERE session_id=$1)`, sessionID).Scan(&deleted)
	if err != nil {
		return nil, err
	}
	if deleted {
		return nil, nil
	}
	var hidden bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM excluded_sessions WHERE session_id=$1)`, sessionID).Scan(&hidden); err != nil {
		return nil, err
	}
	if hidden {
		return nil, nil
	}
	// The same account exclusion predicate is applied to records and observations.
	// Any hidden source closes the entire session rather than leaking the other rows.
	rows, err := tx.Query(ctx, `SELECT user_id,profile_email,provenance,allowed FROM (
		SELECT r.user_id,r.profile_email,'session_record'::text AS provenance,
			(`+excludedAccountPredicateSQL("r")+`) AND NOT EXISTS
			(SELECT 1 FROM blocked_projects bp WHERE bp.project_hash=r.project_hash) AS allowed
		FROM session_records r WHERE r.session_id=$1
		UNION ALL SELECT o.user_id,o.profile_email,o.identity_provenance,
			(`+excludedAccountPredicateSQL("o")+`) AND NOT EXISTS
			(SELECT 1 FROM blocked_projects bp WHERE bp.project_hash=o.project_hash)
		FROM session_snapshots o WHERE o.session_id=$1
		UNION ALL SELECT o.user_id,o.profile_email,o.identity_provenance,
			(`+excludedAccountPredicateSQL("o")+`) AND NOT EXISTS
			(SELECT 1 FROM blocked_projects bp WHERE bp.project_hash=o.project_hash)
		FROM session_live_states o WHERE o.session_id=$1
		UNION ALL SELECT o.user_id,o.profile_email,o.identity_provenance,
			(`+excludedAccountPredicateSQL("o")+`) AND NOT EXISTS
			(SELECT 1 FROM blocked_projects bp WHERE bp.project_hash=o.project_hash)
		FROM session_observation_events o WHERE o.session_id=$1
	) sources`, sessionID)
	if err != nil {
		return nil, err
	}
	owner := &personalOwner{}
	seen := make(map[personalIdentity]bool)
	var proved, any, matched bool
	var observedUID, observedEmail string
	for rows.Next() {
		var uid, email, provenance string
		var allowed bool
		if err = rows.Scan(&uid, &email, &provenance, &allowed); err != nil {
			break
		}
		if !allowed {
			hidden = true
		}
		if uid == "" && email == "" {
			continue
		}
		any = true
		if uid != "" && observedUID != "" && observedUID != uid {
			hidden = true
		}
		if email != "" && observedEmail != "" && observedEmail != email {
			hidden = true
		}
		if uid != "" {
			observedUID = uid
		}
		if email != "" {
			observedEmail = email
		}
		if provenance == "session_record" || provenance == "personal_token" {
			proved = true
			key := personalIdentity{uid, email}
			proof := "personal_token_observation"
			if provenance == "session_record" {
				proof = "session_record"
			}
			rowMatched := (callerUserID != "" && uid == callerUserID) ||
				(callerUserID == "" && email != "" && strings.EqualFold(email, callerEmail))
			if rowMatched {
				matched = true
				if owner.proof == "" || (proof == "session_record" && owner.proof != proof) ||
					(proof == owner.proof && (uid < owner.userID || (uid == owner.userID && email < owner.email))) {
					owner.userID, owner.email, owner.proof = uid, email, proof
				}
			}
			owner.verified = append(owner.verified, personalVerifiedIdentity{key, proof})
			if !seen[key] {
				owner.keys = append(owner.keys, key)
				seen[key] = true
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if hidden || !any || !proved || !matched || len(owner.keys) == 0 {
		return nil, nil
	}
	return owner, nil
}

func (s *PgStore) personalTx(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if err = lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

type personalStoredOwner struct {
	identity     personalIdentity
	proof, state string
}

// Stored source identifiers are deletion keys, not independent proof. An existing
// note remains private unless its recorded identity still matches verified source.
func personalOwnerMatches(owner *personalOwner, stored personalStoredOwner) bool {
	if stored.proof == "" {
		return stored.state == "" // only an absent row has no proof
	}
	if stored.state != "valid" || (stored.proof != "session_record" && stored.proof != "personal_token_observation") {
		return false
	}
	key := stored.identity
	if key.userID == "" && key.email == "" {
		return false
	}
	for _, verified := range owner.verified {
		if verified.proof == stored.proof && verified.identity.userID == key.userID &&
			strings.EqualFold(verified.identity.email, key.email) {
			return true
		}
	}
	return false
}

func personalStateRow(ctx context.Context, tx pgx.Tx, userID int64, sessionID string) (*PersonalSessionState, string, personalStoredOwner, error) {
	state := &PersonalSessionState{SessionID: sessionID, Classification: "unsorted"}
	var text string
	var stored personalStoredOwner
	var updatedAt time.Time
	err := tx.QueryRow(ctx, `SELECT classification,note,revision,updated_at,expires_at,owner_state,
		source_user_id,source_profile_email,owner_proof
		FROM session_personal_state WHERE dashboard_user_id=$1 AND session_id=$2`, userID, sessionID).
		Scan(&state.Classification, &text, &state.Revision, &updatedAt, &state.ExpiresAt,
			&stored.state, &stored.identity.userID, &stored.identity.email, &stored.proof)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, "", stored, nil
	}
	if err != nil {
		return nil, "", stored, err
	}
	if text != "" {
		state.Note = &PersonalNote{Text: text, UpdatedAt: updatedAt}
	}
	return state, text, stored, nil
}

func (s *PgStore) GetPersonalSession(ctx context.Context, dashboardUserID int64, sessionID, email, userID string) (*PersonalSessionState, error) {
	tx, err := s.personalTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	owner, err := personalSessionOwner(ctx, tx, sessionID, email, userID)
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return nil, ErrPersonalNotFound
	}
	state, _, stored, err := personalStateRow(ctx, tx, dashboardUserID, sessionID)
	if err != nil {
		return nil, err
	}
	if !personalOwnerMatches(owner, stored) {
		return nil, ErrPersonalNotFound
	}
	if state.Revision > 0 && !state.ExpiresAt.After(time.Now()) {
		return nil, ErrPersonalNotFound
	}
	if state.Revision == 0 {
		state.ExpiresAt = time.Now().Add(personalLifetime)
	}
	return state, tx.Commit(ctx)
}

// GetPersonalSessionOverview returns an existing rollup only while the caller's
// note and its original verified source identity remain accessible.
func (s *PgStore) GetPersonalSessionOverview(ctx context.Context, dashboardUserID int64, sessionID, email, userID string) (*SessionOverview, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return nil, err
	}
	owner, err := personalSessionOwner(ctx, tx, sessionID, email, userID)
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return nil, ErrPersonalNotFound
	}
	state, text, stored, err := personalStateRow(ctx, tx, dashboardUserID, sessionID)
	if err != nil {
		return nil, err
	}
	if state.Revision == 0 || text == "" || !state.ExpiresAt.After(time.Now()) || !personalOwnerMatches(owner, stored) {
		return nil, ErrPersonalNotFound
	}
	scopeType, scopeValue := "profile", email
	if userID != "" {
		scopeType, scopeValue = "user", userID
	}
	if scopeValue == "" {
		return nil, ErrPersonalNotFound
	}
	// An observation can prove a personal-token note, but the overview's raw
	// session ownership must also be established by session_records.
	var rawOwned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM session_records r WHERE r.session_id=$1
		AND (($2<>'' AND r.user_id=$2) OR ($2='' AND $3<>'' AND r.profile_email=$3)))`,
		sessionID, userID, email).Scan(&rawOwned); err != nil {
		return nil, err
	}
	if !rawOwned {
		return nil, ErrPersonalNotFound
	}
	o := &SessionOverview{}
	err = tx.QueryRow(ctx, `SELECT r.session_id, r.profile_email, r.user_id, r.login_email,
		r.account_count, r.model, r.agent, r.start_time, r.end_time, r.input_tokens,
		r.output_tokens, r.cost_usd, r.event_count, r.has_sync, r.project_hash,
		COALESCE(p.project_name, ''), r.entrypoint, r.has_enriched,
		r.cctrace_version, r.claude_version, r.login_email_inferred
		FROM session_overview_rollups r
		LEFT JOIN projects p ON p.project_hash=r.project_hash
			AND p.agent=COALESCE(NULLIF(r.agent, ''), 'claude')
		WHERE r.scope_type=$1 AND r.scope_value=$2 AND r.session_id=$3
			AND (r.has_api_request OR r.has_sync)`, scopeType, scopeValue, sessionID).
		Scan(&o.SessionID, &o.ProfileEmail, &o.UserID, &o.LoginEmail,
			&o.AccountCount, &o.Model, &o.Agent, &o.StartTime, &o.EndTime,
			&o.InputTokens, &o.OutputTokens, &o.CostUSD, &o.EventCount, &o.HasSync,
			&o.ProjectHash, &o.ProjectName, &o.Entrypoint, &o.HasEnriched,
			&o.CctraceVersion, &o.ClaudeVersion, &o.LoginEmailInferred)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPersonalNotFound
	}
	if err != nil {
		return nil, err
	}
	return o, tx.Commit(ctx)
}

func (s *PgStore) PutPersonalSession(ctx context.Context, dashboardUserID int64, sessionID, email, userID string, class, note *string, expected int64) (*PersonalSessionState, error) {
	if expected < 0 || (class == nil && note == nil) || (class != nil && !validPersonalClass(*class)) || (note != nil && !validPersonalText(*note)) {
		return nil, ErrPersonalInvalid
	}
	tx, err := s.personalTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	owner, err := personalSessionOwner(ctx, tx, sessionID, email, userID)
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return nil, ErrPersonalNotFound
	}
	// Serialize concurrent creations for this dashboard user and session.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 918))`, fmt.Sprintf("%d/%s", dashboardUserID, sessionID)); err != nil {
		return nil, err
	}
	state, text, stored, err := personalStateRow(ctx, tx, dashboardUserID, sessionID)
	if err != nil {
		return nil, err
	}
	if !personalOwnerMatches(owner, stored) || (state.Revision > 0 && !state.ExpiresAt.After(time.Now())) {
		return nil, ErrPersonalNotFound
	}
	if state.Revision != expected {
		return state, ErrPersonalConflict
	}
	if class != nil {
		state.Classification = *class
	}
	if note != nil {
		text = *note
	}
	if state.Revision == 0 {
		state.ExpiresAt = time.Now().Add(personalLifetime)
		days, policyErr := s.policyIntervalDays(ctx, "policy_retention", "drop_after", "session_records")
		if policyErr != nil {
			return nil, policyErr
		}
		if days != nil && *days > 0 {
			var latest time.Time
			if err = tx.QueryRow(ctx, `SELECT max(at) FROM (
				SELECT max(ts) at FROM session_records WHERE session_id=$1 AND $2='session_record'
						AND user_id=$3 AND lower(profile_email)=lower($4)
				UNION ALL SELECT max(observed_at) FROM session_snapshots WHERE session_id=$1 AND $2='personal_token_observation'
						AND identity_provenance='personal_token' AND user_id=$3 AND lower(profile_email)=lower($4)
				UNION ALL SELECT max(heartbeat_at) FROM session_live_states WHERE session_id=$1 AND $2='personal_token_observation'
						AND identity_provenance='personal_token' AND user_id=$3 AND lower(profile_email)=lower($4)
				UNION ALL SELECT max(observed_at) FROM session_observation_events WHERE session_id=$1 AND $2='personal_token_observation'
						AND identity_provenance='personal_token' AND user_id=$3 AND lower(profile_email)=lower($4)
			) source WHERE at IS NOT NULL`, sessionID, owner.proof, owner.userID, owner.email).Scan(&latest); err != nil {
				return nil, err
			}
			until := latest.Add(time.Duration(*days) * 24 * time.Hour)
			if !until.After(time.Now()) {
				return nil, ErrPersonalNotFound
			}
			if until.Before(state.ExpiresAt) {
				state.ExpiresAt = until
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO session_personal_state
			(dashboard_user_id,session_id,source_user_id,source_profile_email,owner_proof,classification,note,expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, dashboardUserID, sessionID, owner.userID, owner.email, owner.proof, state.Classification, text, state.ExpiresAt)
	} else {
		_, err = tx.Exec(ctx, `UPDATE session_personal_state SET classification=$3,note=$4,revision=revision+1,updated_at=now()
			WHERE dashboard_user_id=$1 AND session_id=$2`, dashboardUserID, sessionID, state.Classification, text)
	}
	if err != nil {
		return nil, err
	}
	// Keep every verified source-identity combination as a deletion key. Shared
	// upload claims can veto ownership but cannot supply immutable deletion keys.
	for _, key := range owner.keys {
		_, err = tx.Exec(ctx, `INSERT INTO session_personal_identity_keys
			(dashboard_user_id,session_id,source_user_id,source_profile_email) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
			dashboardUserID, sessionID, key.userID, key.email)
		if err != nil {
			return nil, err
		}
	}
	state.Revision++
	if text != "" {
		state.Note = &PersonalNote{Text: text, UpdatedAt: time.Now()}
	} else {
		state.Note = nil
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return state, nil
}

func (s *PgStore) DeletePersonalSession(ctx context.Context, dashboardUserID int64, sessionID, email, userID string, expected int64) (*PersonalSessionState, error) {
	if expected < 0 {
		return nil, ErrPersonalInvalid
	}
	tx, err := s.personalTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	owner, err := personalSessionOwner(ctx, tx, sessionID, email, userID)
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return nil, ErrPersonalNotFound
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 918))`, fmt.Sprintf("%d/%s", dashboardUserID, sessionID)); err != nil {
		return nil, err
	}
	state, text, stored, err := personalStateRow(ctx, tx, dashboardUserID, sessionID)
	if err != nil {
		return nil, err
	}
	if !personalOwnerMatches(owner, stored) || (state.Revision > 0 && !state.ExpiresAt.After(time.Now())) {
		return nil, ErrPersonalNotFound
	}
	if state.Revision != expected {
		return state, ErrPersonalConflict
	}
	if state.Revision > 0 && (text != "" || state.Classification != "unsorted") {
		_, err = tx.Exec(ctx, `UPDATE session_personal_state SET classification='unsorted',note='',revision=revision+1,updated_at=now()
			WHERE dashboard_user_id=$1 AND session_id=$2`, dashboardUserID, sessionID)
		if err != nil {
			return nil, err
		}
		state.Revision++
		state.Classification = "unsorted"
		state.Note = nil
	}
	if state.Revision == 0 {
		state.ExpiresAt = time.Now().Add(personalLifetime)
	}
	return state, tx.Commit(ctx)
}

func (s *PgStore) GetPersonalGlobalNote(ctx context.Context, userID int64) (*PersonalGlobalNote, error) {
	result := &PersonalGlobalNote{ExpiresAt: time.Now().Add(personalLifetime)}
	err := s.pool.QueryRow(ctx, `SELECT note,revision,updated_at,updated_at+interval '90 days'
		FROM dashboard_personal_notes WHERE dashboard_user_id=$1 AND updated_at+interval '90 days'>now()`, userID).
		Scan(&result.Text, &result.Revision, &result.UpdatedAt, &result.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	return result, err
}

func (s *PgStore) PutPersonalGlobalNote(ctx context.Context, userID int64, text string, expected int64, deleteNote bool) (*PersonalGlobalNote, error) {
	if expected < 0 || !validPersonalText(text) {
		return nil, ErrPersonalInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,919))`, fmt.Sprintf("%d", userID)); err != nil {
		return nil, err
	}
	// An expired row must not make the next revision-0 write inherit an old
	// revision through the ON CONFLICT path.
	if _, err = tx.Exec(ctx, `DELETE FROM dashboard_personal_notes WHERE dashboard_user_id=$1
		AND updated_at+interval '90 days'<=now()`, userID); err != nil {
		return nil, err
	}
	result := &PersonalGlobalNote{}
	err = tx.QueryRow(ctx, `SELECT note,revision,updated_at FROM dashboard_personal_notes
		WHERE dashboard_user_id=$1 AND updated_at+interval '90 days'>now()`, userID).Scan(&result.Text, &result.Revision, &result.UpdatedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if expected != result.Revision {
		return result, ErrPersonalConflict
	}
	if deleteNote && result.Text == "" {
		result.ExpiresAt = time.Now().Add(personalLifetime)
		return result, tx.Commit(ctx)
	}
	if result.Revision == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO dashboard_personal_notes (dashboard_user_id,note)
			VALUES($1,$2) RETURNING note,revision,updated_at`, userID, text).Scan(&result.Text, &result.Revision, &result.UpdatedAt)
	} else {
		err = tx.QueryRow(ctx, `UPDATE dashboard_personal_notes SET note=$2,revision=revision+1,updated_at=now()
			WHERE dashboard_user_id=$1 RETURNING note,revision,updated_at`, userID, text).Scan(&result.Text, &result.Revision, &result.UpdatedAt)
	}
	if err != nil {
		return nil, err
	}
	result.ExpiresAt = result.UpdatedAt.Add(personalLifetime)
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

type personalNoteCursor struct {
	UpdatedAt time.Time `json:"updated_at"`
	SessionID string    `json:"session_id"`
}

func encodePersonalNoteCursor(at time.Time, sessionID string) string {
	data, _ := json.Marshal(personalNoteCursor{UpdatedAt: at, SessionID: sessionID})
	return base64.RawURLEncoding.EncodeToString(data)
}

func nullablePersonalCursorTime(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}
	return &at
}

func decodePersonalNoteCursor(raw string) (personalNoteCursor, error) {
	var cursor personalNoteCursor
	if raw == "" {
		return cursor, nil
	}
	if len(raw) > 4096 {
		return cursor, ErrPersonalInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.UpdatedAt.IsZero() || cursor.SessionID == "" {
		return cursor, ErrPersonalInvalid
	}
	return cursor, nil
}

// The source union checks every row for conflicts, even rows whose provenance
// cannot establish ownership. Only a verified row matching the active caller
// mode can prove ownership; stored keys must match that row exactly.
var personalVisibleSourceSQL = personalVisibleSourceQuery("")

func personalOverviewScopeSQL(f SessionOverviewFilter, firstArg int) (string, []interface{}) {
	args := []interface{}{f.DashboardUserID, f.PersonalUserID, f.PersonalEmail}
	if f.DashboardUserID == 0 || (f.PersonalUserID == "" && f.PersonalEmail == "") {
		return "", args
	}
	// A scalar array is evaluated once per source branch. An IN semijoin can
	// instead visit every hypertable chunk once per candidate (2,739 loops on
	// deployed data); preserve the same complete source set without that fanout.
	query := personalVisibleSourceQueryWithGrouping(" AND session_id = ANY(ARRAY(SELECT session_id FROM personal_candidate))", true)
	query = strings.TrimPrefix(query, "WITH ")
	query = strings.NewReplacer("$1", fmt.Sprintf("$%d", firstArg),
		"$2", fmt.Sprintf("$%d", firstArg+1),
		"$3", fmt.Sprintf("$%d", firstArg+2)).Replace(query)
	return query, args
}

func personalOverviewClassificationSQL(f SessionOverviewFilter, firstArg int) (string, []interface{}) {
	personalSQL, args := personalOverviewScopeSQL(f, firstArg)
	if personalSQL == "" {
		return "", args
	}
	// The overview requires a matching raw record even when a verified observation
	// proves ownership. Check that necessary condition before reading every source,
	// but retain all identities/exclusion keys of each surviving session below.
	// owned is already restricted by personal_candidate through source_basis;
	// joining the candidate CTE again can compare every candidate with every owner.
	return `, personal_candidate AS MATERIALIZED (
		SELECT c.session_id FROM candidate c WHERE EXISTS (
			SELECT 1 FROM session_records raw WHERE raw.session_id=c.session_id
				AND (($` + fmt.Sprint(firstArg+1) + `<>'' AND raw.user_id=$` + fmt.Sprint(firstArg+1) + `)
					OR ($` + fmt.Sprint(firstArg+1) + `='' AND $` + fmt.Sprint(firstArg+2) + `<>'' AND raw.profile_email=$` + fmt.Sprint(firstArg+2) + `))
			OFFSET 0)
	), ` + personalSQL + `, personal_classified AS (
		SELECT owned.session_id, owned.classification FROM owned
	)`, args
}

// SessionOverviewClassificationCounts counts the owner's categories over the
// same pre-page overview population used by the list and count routes.
func (s *PgStore) SessionOverviewClassificationCounts(ctx context.Context, f SessionOverviewFilter) (map[string]int, error) {
	counts := map[string]int{"unsorted": 0, "consulting": 0, "working": 0, "later": 0, "done": 0}
	if f.DashboardUserID == 0 || (f.PersonalUserID == "" && f.PersonalEmail == "") {
		return counts, nil
	}
	var query string
	var args []interface{}
	if f.Since == nil && f.Until == nil {
		ready, err := s.sessionOverviewRollupsReady(ctx)
		if err != nil {
			return nil, err
		}
		if ready {
			query, args = sessionOverviewRollupPersonalQuery(f, true)
		}
	}
	if query == "" {
		query, args = personalPeriodBucketsQuery(f)
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var class string
		var count int
		if err = rows.Scan(&class, &count); err != nil {
			break
		}
		counts[class] = count
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return counts, err
}

func personalVisibleSourceQuery(sourceFilter string) string {
	return personalVisibleSourceQueryWithGrouping(sourceFilter, false)
}

func personalVisibleSourceQueryWithGrouping(sourceFilter string, groupBasis bool) string {
	return `WITH source_basis AS MATERIALIZED (
	` + personalVisibleSourceBasisBranch("session_records", "r", `'session_record'::text`, sourceFilter, groupBasis) + `
	UNION ALL ` + personalVisibleSourceBasisBranch("session_snapshots", "o", "o.identity_provenance", sourceFilter, groupBasis) + `
	UNION ALL ` + personalVisibleSourceBasisBranch("session_live_states", "o", "o.identity_provenance", sourceFilter, groupBasis) + `
	UNION ALL ` + personalVisibleSourceBasisBranch("session_observation_events", "o", "o.identity_provenance", sourceFilter, groupBasis) + `
), sources AS (
	SELECT source_rows.session_id,source_rows.user_id,source_rows.profile_email,source_rows.provenance,source_rows.allowed
	FROM (
		SELECT DISTINCT src.source_family,src.session_id,src.user_id,src.profile_email,src.provenance,
			(` + excludedAccountPredicateSQL("src") + `) AND NOT EXISTS
			(SELECT 1 FROM blocked_projects bp WHERE bp.project_hash=src.project_hash) allowed
		FROM source_basis src
	) source_rows
), valid_sources AS (
	SELECT s.session_id FROM sources s
	GROUP BY s.session_id
	HAVING bool_and(s.allowed)
		AND count(DISTINCT NULLIF(s.user_id,''))<=1
		AND count(DISTINCT NULLIF(s.profile_email,''))<=1
		AND bool_or(s.provenance IN ('session_record','personal_token')
			AND (($2<>'' AND s.user_id=$2) OR ($2='' AND $3<>'' AND lower(s.profile_email)=lower($3))))
), owned AS (
	SELECT v.session_id,COALESCE(p.classification,'unsorted') classification,p.note,p.updated_at,p.revision
	FROM valid_sources v
	LEFT JOIN session_personal_state p ON p.session_id=v.session_id AND p.dashboard_user_id=$1
	WHERE NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id=v.session_id)
		AND NOT EXISTS (SELECT 1 FROM excluded_sessions x WHERE x.session_id=v.session_id)
		AND (p.session_id IS NULL OR (p.owner_state='valid' AND p.expires_at>now()
			AND p.owner_proof IN ('session_record','personal_token_observation')
			AND (p.source_user_id<>'' OR p.source_profile_email<>'')
			AND EXISTS (SELECT 1 FROM sources s WHERE s.session_id=v.session_id
				AND s.provenance=CASE p.owner_proof WHEN 'session_record' THEN 'session_record' ELSE 'personal_token' END
				AND s.user_id=p.source_user_id AND lower(s.profile_email)=lower(p.source_profile_email))))
)
`
}

// Table names, aliases, and provenance expressions passed here are trusted
// constants at the call site. sourceFilter is applied only to the raw table
// projection, before any account or project exclusion predicate is evaluated.
func personalVisibleSourceBasisBranch(table, alias, provenanceExpr, sourceFilter string, groupBasis bool) string {
	distinct, group := "DISTINCT ", ""
	if groupBasis {
		// Broad counts measured a disk-spilling incremental sort of 1.65M rows
		// for only 774 distinct tuples. GROUP BY permits hash aggregation of the
		// same complete keys without requiring their raw rows in sorted order.
		distinct, group = "", " GROUP BY 2,3,4,5,6,7,8,9"
	}
	return `SELECT ` + distinct + `'` + table + `'::text AS source_family,` + alias + `.session_id,` + alias + `.user_id,` + alias + `.profile_email,
		` + alias + `.login_email,` + alias + `.billing_provider,` + alias + `.account_id,` + alias + `.project_hash,
		` + provenanceExpr + ` AS provenance
	FROM ` + table + ` ` + alias + ` WHERE ` + alias + `.session_id<>''` + sourceFilter + group
}

// PersonalSessionOverviewValues reads only the requested page's private values in
// one SQL query. The same ownership predicate backs the personal notes and counts.
func (s *PgStore) PersonalSessionOverviewValues(ctx context.Context, dashboardUserID int64, email, userID string, sessionIDs []string) (map[string]PersonalOverviewValues, error) {
	values := make(map[string]PersonalOverviewValues)
	if len(sessionIDs) == 0 || dashboardUserID == 0 || (userID == "" && email == "") {
		return values, nil
	}
	// Most pages have no private overrides. Narrow to stored values before
	// taking source/deletion barriers or re-reading large session sources.
	// Ownership is still revalidated under those barriers for every returned value.
	rows, err := s.pool.Query(ctx, `SELECT session_id FROM session_personal_state
		WHERE dashboard_user_id=$1 AND revision>0 AND session_id=ANY($2)`, dashboardUserID, sessionIDs)
	if err != nil {
		return nil, err
	}
	sessionIDs, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	if len(sessionIDs) == 0 {
		return values, nil
	}
	tx, err := s.personalTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err = lockSessionDeletionReaders(ctx, tx, sessionIDs); err != nil {
		return nil, err
	}
	values, err = personalSessionOverviewValuesTx(ctx, tx, dashboardUserID, email, userID, sessionIDs)
	if err != nil {
		return nil, err
	}
	return values, tx.Commit(ctx)
}

func personalSessionOverviewValuesTx(ctx context.Context, tx pgx.Tx, dashboardUserID int64, email, userID string, sessionIDs []string) (map[string]PersonalOverviewValues, error) {
	values := make(map[string]PersonalOverviewValues)
	if len(sessionIDs) == 0 || dashboardUserID == 0 || (userID == "" && email == "") {
		return values, nil
	}
	rows, err := tx.Query(ctx, personalVisibleSourceQuery(" AND session_id=ANY($4)")+`SELECT owned.session_id,owned.classification,
		CASE WHEN owned.note<>'' THEN left(split_part(owned.note,E'\n',1),120) END
		FROM owned WHERE owned.revision>0
			AND EXISTS (SELECT 1 FROM sources raw WHERE raw.session_id=owned.session_id
				AND raw.provenance='session_record'
				AND (($2<>'' AND raw.user_id=$2) OR ($2='' AND $3<>'' AND raw.profile_email=$3)))`, dashboardUserID, userID, email, sessionIDs)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sessionID, classification string
		var firstLine *string
		if err = rows.Scan(&sessionID, &classification, &firstLine); err != nil {
			break
		}
		values[sessionID] = PersonalOverviewValues{Classification: classification, FirstLine: firstLine}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	return values, nil
}

// PersonalSessionNotes and PersonalSessionCounts recheck current source ownership.
// Stored identity keys never grant access to a note.
func (s *PgStore) PersonalSessionNotes(ctx context.Context, userID int64, email, sourceUserID string, limit int, rawCursor string) (*PersonalNotePage, error) {
	if sourceUserID == "" && email == "" {
		return nil, ErrPersonalNotFound
	}
	if limit < 1 || limit > 100 {
		return nil, ErrPersonalInvalid
	}
	cursor, err := decodePersonalNoteCursor(rawCursor)
	if err != nil {
		return nil, err
	}
	tx, err := s.personalTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rows, err := tx.Query(ctx, personalVisibleSourceSQL+`SELECT owned.session_id,
		left(split_part(p.note,E'\n',1),120), owned.updated_at, COALESCE(f.title,'')
		FROM owned JOIN session_personal_state p ON p.dashboard_user_id=$1 AND p.session_id=owned.session_id
		LEFT JOIN session_analysis_facts f ON f.session_id=owned.session_id AND f.title_state='ok'
		WHERE p.note<>'' AND ($4::timestamptz IS NULL OR owned.updated_at<$4
			OR (owned.updated_at=$4 AND owned.session_id>$5))
		ORDER BY owned.updated_at DESC,owned.session_id LIMIT $6`, userID, sourceUserID, email,
		nullablePersonalCursorTime(cursor.UpdatedAt), cursor.SessionID, limit+1)
	if err != nil {
		return nil, err
	}
	page := &PersonalNotePage{Items: make([]PersonalSessionNoteSummary, 0, limit)}
	for rows.Next() {
		var id, note, title string
		var at time.Time
		if err = rows.Scan(&id, &note, &at, &title); err != nil {
			break
		}
		if len(page.Items) == limit {
			page.Truncated = true
			break
		}
		short := id
		if len(short) > 8 {
			short = short[:8]
		}
		page.Items = append(page.Items, PersonalSessionNoteSummary{SessionID: id, ShortID: short,
			Title: title, FirstLine: note, UpdatedAt: at})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if page.Truncated {
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodePersonalNoteCursor(last.UpdatedAt, last.SessionID)
	}
	return page, tx.Commit(ctx)
}

func (s *PgStore) PersonalSessionCounts(ctx context.Context, userID int64, email, sourceUserID string, filter PersonalCountFilter) (*PersonalClassificationCounts, error) {
	if sourceUserID == "" && email == "" {
		return nil, ErrPersonalNotFound
	}
	if filter.Classification != "" && !validPersonalClass(filter.Classification) {
		return nil, ErrPersonalInvalid
	}
	if filter.Source != "" && filter.Source != "interactive" && filter.Source != "headless" {
		return nil, ErrPersonalInvalid
	}
	if filter.Agent != "" && filter.Agent != "claude" && filter.Agent != "codex" && filter.Agent != "other" {
		return nil, ErrPersonalInvalid
	}
	if filter.Since != nil && filter.Until != nil && !filter.Since.Before(*filter.Until) {
		return nil, ErrPersonalInvalid
	}
	tx, err := s.personalTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	result := &PersonalClassificationCounts{Scope: "owned", Counts: map[string]int64{"unsorted": 0, "consulting": 0, "working": 0, "later": 0, "done": 0}}
	var conditions []string
	args := []any{userID, sourceUserID, email, filter.Since, filter.Until}
	add := func(clause, value string) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(clause, len(args)))
	}
	if filter.Classification != "" {
		add("owned.classification=$%d", filter.Classification)
	}
	if filter.Agent != "" {
		if filter.Agent == "other" {
			conditions = append(conditions, "meta.agent NOT IN ('claude','codex','weekly')")
		} else {
			add("meta.agent=$%d", filter.Agent)
		}
	}
	if len(filter.ProjectHashes) > 0 {
		args = append(args, filter.ProjectHashes)
		conditions = append(conditions, fmt.Sprintf("meta.project_hash=ANY($%d)", len(args)))
	}
	if filter.FoldLineage {
		conditions = append(conditions, "NOT meta.has_fork")
	}
	if filter.Source != "" {
		predicate := `CASE WHEN meta.agent='codex' THEN meta.entrypoint IN ('','cli')
			ELSE (meta.entrypoint<>'sdk-cli' AND (NOT meta.has_enriched OR meta.has_genuine)) END`
		conditions = append(conditions, "meta.has_record")
		if filter.Source == "headless" {
			conditions = append(conditions, "NOT ("+predicate+")")
		} else {
			conditions = append(conditions, predicate)
		}
	}
	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}
	query := personalVisibleSourceSQL + `, metadata AS (
		SELECT session_id,COALESCE(max(agent) FILTER (WHERE agent<>''),'claude') agent,
			COALESCE(max(project_hash) FILTER (WHERE project_hash<>''),'') project_hash,
			COALESCE(max(entrypoint) FILTER (WHERE entrypoint<>''),'') entrypoint,
			bool_or(source_file<>'') has_enriched,
			bool_or(prompt_source IN ('typed','paste','queued','suggestion_accepted')) has_genuine,
			bool_or(forked_from_session<>'') has_fork,
			bool_or(has_record) has_record
		FROM (
			SELECT session_id,COALESCE(agent,'claude') agent,COALESCE(project_hash,'') project_hash,
				COALESCE(entrypoint,'') entrypoint,COALESCE(source_file,'') source_file,
				COALESCE(prompt_source,'') prompt_source,COALESCE(forked_from_session,'') forked_from_session,
				true has_record,ts at FROM session_records
			UNION ALL SELECT session_id,agent,project_hash,''::text,''::text,''::text,''::text,
				false,observed_at FROM session_snapshots
			UNION ALL SELECT session_id,agent,project_hash,''::text,''::text,''::text,''::text,
				false,heartbeat_at FROM session_live_states
			UNION ALL SELECT session_id,agent,project_hash,''::text,''::text,''::text,''::text,
				false,observed_at FROM session_observation_events
		) rows WHERE ($4::timestamptz IS NULL OR at >= $4)
			AND ($5::timestamptz IS NULL OR at < $5)
		GROUP BY session_id
	)
	SELECT owned.classification,count(*) FROM owned JOIN metadata meta USING(session_id) ` + where + `
	GROUP BY owned.classification`
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var class string
		var count int64
		if err = rows.Scan(&class, &count); err != nil {
			break
		}
		result.Counts[class] = count
		result.Total += count
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}

// PrunePersonalState also removes rows whose last source row disappeared after
// a raw-data or observation retention pass. Temporary visibility exclusions and
// invalidated ownership do not authorize early deletion.
func (s *PgStore) PrunePersonalState(ctx context.Context) (int64, error) {
	tx, err := s.personalTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rows, err := tx.Query(ctx, `SELECT dashboard_user_id,session_id,source_user_id,source_profile_email,owner_proof,owner_state
		FROM session_personal_state`)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id      int64
		session string
		stored  personalStoredOwner
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.session, &c.stored.identity.userID, &c.stored.identity.email,
			&c.stored.proof, &c.stored.state); err != nil {
			break
		}
		candidates = append(candidates, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, c.session)
	}
	if err = lockSessionDeletionTargets(ctx, tx, ids); err != nil {
		return 0, err
	}
	var count int64
	for _, c := range candidates {
		// Exclusion and mixed identity hide values but are reversible. A merge
		// invalidates the old owner, yet the destination's verified source can
		// still exist. Shared-key claims or a different new owner cannot preserve
		// a valid private row.
		var sourceExists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM session_records WHERE session_id=$1 AND
				($2<>'valid' OR ($3='session_record' AND user_id=$4 AND lower(profile_email)=lower($5)))
			UNION ALL SELECT 1 FROM session_snapshots WHERE session_id=$1 AND identity_provenance='personal_token' AND
				($2<>'valid' OR ($3='personal_token_observation' AND user_id=$4 AND lower(profile_email)=lower($5)))
			UNION ALL SELECT 1 FROM session_live_states WHERE session_id=$1 AND identity_provenance='personal_token' AND
				($2<>'valid' OR ($3='personal_token_observation' AND user_id=$4 AND lower(profile_email)=lower($5)))
			UNION ALL SELECT 1 FROM session_observation_events WHERE session_id=$1 AND identity_provenance='personal_token' AND
				($2<>'valid' OR ($3='personal_token_observation' AND user_id=$4 AND lower(profile_email)=lower($5)))
		)`, c.session, c.stored.state, c.stored.proof, c.stored.identity.userID, c.stored.identity.email).Scan(&sourceExists); err != nil {
			return 0, err
		}
		if sourceExists {
			continue
		}
		tag, deleteErr := tx.Exec(ctx, `DELETE FROM session_personal_state WHERE dashboard_user_id=$1 AND session_id=$2`, c.id, c.session)
		if deleteErr != nil {
			return 0, deleteErr
		}
		count += tag.RowsAffected()
	}
	tag, err := tx.Exec(ctx, `DELETE FROM session_personal_state WHERE expires_at<=now()`)
	if err != nil {
		return 0, err
	}
	count += tag.RowsAffected()
	if _, err = tx.Exec(ctx, `DELETE FROM dashboard_personal_notes WHERE updated_at+interval '90 days'<=now()`); err != nil {
		return 0, err
	}
	if err = pruneSessionGroupDismissals(ctx, tx); err != nil {
		return 0, err
	}
	return count, tx.Commit(ctx)
}
