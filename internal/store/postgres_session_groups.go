package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	sessionGroupingVersion     = 1
	sessionGroupCandidateLimit = 500
)

// SessionGroupItem identifies one observed end, not the final line in a log.
type SessionGroupItem struct {
	SessionID string    `json:"session_id"`
	EndedAt   time.Time `json:"ended_at"`
	Dismissed bool      `json:"dismissed"`
}

type RunningSessionGroup struct {
	SessionID string `json:"session_id"`
	Category  string `json:"category"`
	Bucket    string `json:"bucket"`
}

type EndedSessionGroup struct {
	Key     string             `json:"key"`
	EndedAt time.Time          `json:"ended_at"`
	Total   int                `json:"total"`
	Items   []SessionGroupItem `json:"items"`
}

type SessionGroupMemberPage struct {
	GroupKey        string             `json:"group_key"`
	GroupingVersion int                `json:"grouping_version"`
	Total           int                `json:"total"`
	Items           []SessionGroupItem `json:"items"`
	NextCursor      string             `json:"next_cursor"`
}

type sessionGroupCursor struct {
	GroupKey  string `json:"g"`
	SessionID string `json:"s"`
}

type SessionGroupCounts struct {
	Running     int            `json:"running"`
	Ended       int            `json:"ended"`
	RunningBy   map[string]int `json:"running_by"`
	EndedGroups int            `json:"ended_groups"`
}

type SessionGroups struct {
	Scope           string                `json:"scope"`
	GroupingVersion int                   `json:"grouping_version"`
	Truncated       bool                  `json:"truncated"`
	OverviewTotal   int                   `json:"overview_total"`
	Running         []RunningSessionGroup `json:"running"`
	Ended           []EndedSessionGroup   `json:"ended"`
	Counts          SessionGroupCounts    `json:"counts"`
}

type SessionGroupDismissal struct {
	SessionID string    `json:"session_id"`
	EndedAt   time.Time `json:"ended_at"`
}

type groupCandidate struct {
	id, projectHash, category, bucket string
	endedAt                           time.Time
	owner                             *personalOwner
}

// The overview count's predicate defines the complete filtered population.
// Stage its IDs in PostgreSQL so no Go slice scales with the population size.
func (s *PgStore) stageGroupOverviews(ctx context.Context, tx pgx.Tx, f SessionOverviewFilter) error {
	var ready bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name=$1)`, sessionOverviewRollupBackfill).Scan(&ready); err != nil {
		return err
	}
	base := f
	base.Classification = ""
	// The default analysis view enriches and classifies the base population, but
	// those derived fields do not change which sessions belong to a group query.
	// Keep analysis work for filters that actually narrow membership.
	analysisPopulationFilter := base.AnalysisPlan != "" || base.AnalysisQuery != "" || base.AnalysisCategory != "" ||
		base.IncludeSessionRecords && (base.AnalysisBucket != "" || base.AnalysisKind != "" ||
			base.AnalysisComponent != "" || base.AnalysisScreen != "")
	if base.IncludeAnalysis && !analysisPopulationFilter {
		base.IncludeAnalysis = false
		base.AnalysisOwner = false
		base.IncludeSessionRecords = false
	}
	// Account selection narrows the overview population, never the personal
	// ownership proof applied below. Restricted API callers ignore this input.
	if base.LoginEmail != "" {
		base.UserID, base.ProfileEmail = "", ""
	}
	var query string
	var args []interface{}
	var err error
	if base.IncludeAnalysis {
		query, args, err = s.sessionOverviewAnalysisPopulation(ctx, tx, base)
		if err != nil {
			return err
		}
		if query == "" {
			query = `SELECT ''::text AS session_id,''::text AS project_hash WHERE false`
		}
		query, args = sessionOverviewAnalysisQuery(query, args, base, false, false, sessionOverviewAnalysisQueryOptions{unpaged: true})
		query = `SELECT session_id,project_hash FROM (` + query + `) analysis_population`
	} else if ready && f.Since == nil && f.Until == nil {
		query, args = sessionOverviewRollupCountQuery(base)
		query = strings.Replace(query, "SELECT count(*)", "SELECT r.session_id,r.project_hash", 1)
	} else {
		query, args = sessionOverviewPeriodCountQuery(base, false)
		query = strings.Replace(query, "SELECT count(*) FROM grouped", "SELECT session_id,project_hash FROM grouped", 1)
	}
	_, err = tx.Exec(ctx, `CREATE TEMP TABLE group_overviews ON COMMIT DROP AS `+query, args...)
	return err
}

// loadSessionActivity reads verified, owner-scoped observations for both the
// group list and individual session analysis.
func loadSessionActivity(ctx context.Context, tx pgx.Tx, id string, owner *personalOwner) (sessionActivityInput, error) {
	var activity *time.Time
	if err := tx.QueryRow(ctx, `SELECT GREATEST((SELECT last_activity_at FROM session_analysis_facts WHERE session_id=$1),
		(SELECT max(ts) FROM session_records WHERE session_id=$1))`, id).Scan(&activity); err != nil {
		return sessionActivityInput{}, err
	}
	in := sessionActivityInput{lastActivityAt: activity}
	// A raw record may prove ownership, but a shared-key observation can only
	// veto it. Positive live/end evidence must carry its own verified token.
	liveRows, err := tx.Query(ctx, `SELECT status,waiting_kind,status_changed_at,heartbeat_at FROM session_live_states
		WHERE session_id=$1 AND (($2<>'' AND user_id=$2) OR ($2='' AND lower(profile_email)=lower($3)))
		AND identity_provenance='personal_token'`, id, owner.userID, owner.email)
	if err != nil {
		return sessionActivityInput{}, err
	}
	allGone := true
	for liveRows.Next() {
		var row activityLive
		if err = liveRows.Scan(&row.status, &row.waitingKind, &row.statusChangedAt, &row.heartbeatAt); err != nil {
			break
		}
		in.anyLiveRow = true
		if row.status != "gone" {
			allGone = false
		}
		in.live = append(in.live, row)
	}
	if err == nil {
		err = liveRows.Err()
	}
	liveRows.Close()
	if err != nil {
		return sessionActivityInput{}, err
	}
	var endAt, goneAt *time.Time
	if err = tx.QueryRow(ctx, `SELECT max(observed_at) FILTER (WHERE kind='session_end'),
		max(observed_at) FILTER (WHERE kind='gone') FROM session_observation_events
		WHERE session_id=$1 AND (($2<>'' AND user_id=$2) OR ($2='' AND lower(profile_email)=lower($3)))
		AND identity_provenance='personal_token' AND kind IN ('session_end','gone')`,
		id, owner.userID, owner.email).Scan(&endAt, &goneAt); err != nil {
		return sessionActivityInput{}, err
	}
	in.endAt = endAt
	if allGone && in.anyLiveRow {
		in.allGoneAt = goneAt
	}
	var turnStatus, turnKey, questionTurn string
	var questions, background, todo *int
	if err = tx.QueryRow(ctx, `SELECT last_turn_status,last_turn_key,open_question_turn_key,open_questions,
		CASE WHEN detail->'signals'->'background'->>'state'='observed'
			THEN (detail->'signals'->'background'->>'value')::int END,
		CASE WHEN detail->'signals'->'todo'->>'state'='observed'
			THEN (detail->'signals'->'todo'->>'value')::int END
		FROM session_analysis_facts WHERE session_id=$1`, id).
		Scan(&turnStatus, &turnKey, &questionTurn, &questions, &background, &todo); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return sessionActivityInput{}, err
	}
	in.lastTurnStatus, in.sameQuestionTurn = turnStatus, turnKey != "" && turnKey == questionTurn
	if questions != nil {
		in.openQuestions = *questions
	}
	if background != nil {
		in.background = *background
	}
	if todo != nil {
		in.todo = *todo
	}
	if err = tx.QueryRow(ctx, `SELECT max(heartbeat_at) FROM collector_heartbeats
		WHERE user_id=$1 AND lower(profile_email)=lower($2)`, owner.userID, owner.email).
		Scan(&in.collectorHeartbeat); err != nil {
		return sessionActivityInput{}, err
	}
	return in, nil
}

func observedGroupCandidate(c *groupCandidate, in sessionActivityInput, now time.Time) *groupCandidate {
	decision := evaluateSessionActivity(now, in)
	if decision.category == "ended" {
		if decision.endedAt != nil && !decision.endedAt.After(now.Add(time.Minute)) {
			c.endedAt = *decision.endedAt
		}
		return c
	}
	if decision.selected != nil && (decision.category == "waiting" || decision.category == "running" || decision.category == "idle") {
		c.category = decision.category
	}
	if c.category != "" {
		c.bucket = "unknown"
		if in.lastTurnStatus == "interrupted" {
			c.bucket = "interrupted"
		} else if c.category == "waiting" {
			c.bucket = "waiting"
		} else if c.category == "running" {
			c.bucket = "processing"
		} else if in.openQuestions > 0 || in.background > 0 || in.todo > 0 {
			c.bucket = "leftover"
		}
		return c
	}
	return c
}

func sessionGroupClusters(candidates []*groupCandidate) [][]*groupCandidate {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].endedAt.Equal(candidates[j].endedAt) {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].endedAt.After(candidates[j].endedAt)
	})
	var groups [][]*groupCandidate
	latestByProject := make(map[string]int)
	for _, c := range candidates {
		if c.endedAt.IsZero() {
			continue
		}
		index, found := latestByProject[c.projectHash]
		if !found || groups[index][0].endedAt.Sub(c.endedAt) > time.Minute {
			index = len(groups)
			groups = append(groups, []*groupCandidate{c})
			latestByProject[c.projectHash] = index
		} else {
			groups[index] = append(groups[index], c)
		}
	}
	return groups
}

func groupDismissed(ctx context.Context, tx pgx.Tx, id int64, c *groupCandidate) (bool, error) {
	var stored personalStoredOwner
	err := tx.QueryRow(ctx, `SELECT source_user_id,source_profile_email,owner_proof,owner_state
		FROM session_group_dismissals WHERE dashboard_user_id=$1 AND session_id=$2
		AND grouping_version=$3 AND ended_at=$4`, id, c.id, sessionGroupingVersion, c.endedAt).
		Scan(&stored.identity.userID, &stored.identity.email, &stored.proof, &stored.state)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return personalOwnerMatches(c.owner, stored), nil
}

func groupBatchOwners(ctx context.Context, tx pgx.Tx, f SessionOverviewFilter, ids []string) (map[string]*personalOwner, error) {
	rows, err := tx.Query(ctx, personalVisibleSourceQuery(" AND session_id=ANY($4)")+`
		SELECT DISTINCT s.session_id,s.user_id,s.profile_email,s.provenance
		FROM sources s JOIN valid_sources v USING(session_id)
		WHERE s.provenance IN ('session_record','personal_token') AND (s.user_id<>'' OR s.profile_email<>'')
		AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id=s.session_id)
		AND NOT EXISTS (SELECT 1 FROM excluded_sessions x WHERE x.session_id=s.session_id)
		AND ($5='' OR (EXISTS (SELECT 1 FROM owned WHERE owned.session_id=s.session_id AND classification=$5)
			AND EXISTS (SELECT 1 FROM sources raw WHERE raw.session_id=s.session_id AND raw.provenance='session_record'
				AND (($2<>'' AND raw.user_id=$2) OR ($2='' AND $3<>'' AND raw.profile_email=$3)))))
		ORDER BY s.session_id,s.provenance DESC,s.user_id,s.profile_email`, f.DashboardUserID, f.PersonalUserID, f.PersonalEmail, ids, f.Classification)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := make(map[string]*personalOwner)
	for rows.Next() {
		var id, uid, email, provenance string
		if err := rows.Scan(&id, &uid, &email, &provenance); err != nil {
			return nil, err
		}
		o := owners[id]
		if o == nil {
			o = &personalOwner{}
			owners[id] = o
		}
		proof := "personal_token_observation"
		if provenance == "session_record" {
			proof = "session_record"
		}
		key := personalIdentity{uid, email}
		o.keys = append(o.keys, key)
		o.verified = append(o.verified, personalVerifiedIdentity{key, proof})
		if o.proof == "" && ((f.PersonalUserID != "" && uid == f.PersonalUserID) || (f.PersonalUserID == "" && strings.EqualFold(email, f.PersonalEmail))) {
			o.userID, o.email, o.proof = uid, email, proof
		}
	}
	return owners, rows.Err()
}

func groupBatchActivity(ctx context.Context, tx pgx.Tx, f SessionOverviewFilter, ids []string) (map[string]sessionActivityInput, error) {
	inputs := make(map[string]sessionActivityInput, len(ids))
	rows, err := tx.Query(ctx, `SELECT v.session_id,
		GREATEST(a.last_activity_at,(SELECT max(ts) FROM session_records r WHERE r.session_id=v.session_id)),
		COALESCE(a.last_turn_status,''),COALESCE(a.last_turn_key,''),COALESCE(a.open_question_turn_key,''),COALESCE(a.open_questions,0),
		CASE WHEN a.detail->'signals'->'background'->>'state'='observed' THEN (a.detail->'signals'->'background'->>'value')::int ELSE 0 END,
		CASE WHEN a.detail->'signals'->'todo'->>'state'='observed' THEN (a.detail->'signals'->'todo'->>'value')::int ELSE 0 END
		FROM unnest($1::text[]) v(session_id) LEFT JOIN session_analysis_facts a USING(session_id)`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, turn, question string
		var in sessionActivityInput
		if err = rows.Scan(&id, &in.lastActivityAt, &in.lastTurnStatus, &turn, &question, &in.openQuestions, &in.background, &in.todo); err != nil {
			break
		}
		in.sameQuestionTurn = turn != "" && turn == question
		inputs[id] = in
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `SELECT session_id,status,waiting_kind,status_changed_at,heartbeat_at FROM session_live_states
		WHERE session_id=ANY($1) AND identity_provenance='personal_token'
		AND (($2<>'' AND user_id=$2) OR ($2='' AND lower(profile_email)=lower($3)))`, ids, f.PersonalUserID, f.PersonalEmail)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var row activityLive
		if err = rows.Scan(&id, &row.status, &row.waitingKind, &row.statusChangedAt, &row.heartbeatAt); err != nil {
			break
		}
		in := inputs[id]
		in.anyLiveRow = true
		in.live = append(in.live, row)
		inputs[id] = in
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `SELECT session_id,max(observed_at) FILTER(WHERE kind='session_end'),max(observed_at) FILTER(WHERE kind='gone')
		FROM session_observation_events WHERE session_id=ANY($1) AND identity_provenance='personal_token'
		AND (($2<>'' AND user_id=$2) OR ($2='' AND lower(profile_email)=lower($3))) AND kind IN ('session_end','gone') GROUP BY session_id`, ids, f.PersonalUserID, f.PersonalEmail)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var end, gone *time.Time
		if err = rows.Scan(&id, &end, &gone); err != nil {
			break
		}
		in := inputs[id]
		in.endAt = end
		allGone := in.anyLiveRow
		for _, r := range in.live {
			if r.status != "gone" {
				allGone = false
			}
		}
		if allGone {
			in.allGoneAt = gone
		}
		inputs[id] = in
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return inputs, err
}

// Keep at most one batch in memory; PostgreSQL sorts and stores the full set.
func (s *PgStore) stageSessionGroups(ctx context.Context, tx pgx.Tx, f SessionOverviewFilter) (int, error) {
	if err := s.stageGroupOverviews(ctx, tx, f); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `CREATE INDEX ON group_overviews(session_id);
		CREATE TEMP TABLE group_candidates(session_id text PRIMARY KEY,project_hash text NOT NULL,category text NOT NULL,bucket text NOT NULL,ended_at timestamptz,group_key text) ON COMMIT DROP`); err != nil {
		return 0, err
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return 0, err
	}
	total := 0
	after := ""
	for {
		rows, err := tx.Query(ctx, `SELECT session_id,project_hash FROM group_overviews WHERE session_id>$1 ORDER BY session_id LIMIT $2`, after, sessionGroupCandidateLimit)
		if err != nil {
			return 0, err
		}
		page, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (groupCandidate, error) {
			var c groupCandidate
			err := row.Scan(&c.id, &c.projectHash)
			return c, err
		})
		if err != nil {
			return 0, err
		}
		if len(page) == 0 {
			break
		}
		ids := make([]string, len(page))
		for i, c := range page {
			ids[i] = c.id
		}
		after = ids[len(ids)-1]
		owners, err := groupBatchOwners(ctx, tx, f, ids)
		if err != nil {
			return 0, err
		}
		if f.Classification == "" {
			total += len(page)
		} else {
			total += len(owners)
		}
		inputs, err := groupBatchActivity(ctx, tx, f, ids)
		if err != nil {
			return 0, err
		}
		values := make([][]any, 0, len(page))
		for i := range page {
			c := &page[i]
			c.owner = owners[c.id]
			if c.owner == nil {
				continue
			}
			observedGroupCandidate(c, inputs[c.id], now)
			if c.category == "" && c.endedAt.IsZero() {
				continue
			}
			var end *time.Time
			if !c.endedAt.IsZero() {
				end = &c.endedAt
			}
			values = append(values, []any{c.id, c.projectHash, c.category, c.bucket, end})
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"group_candidates"}, []string{"session_id", "project_hash", "category", "bucket", "ended_at"}, pgx.CopyFromRows(values)); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `DECLARE group_ends NO SCROLL CURSOR FOR SELECT session_id,project_hash,ended_at FROM group_candidates WHERE ended_at IS NOT NULL ORDER BY project_hash,ended_at DESC,session_id`); err != nil {
		return 0, err
	}
	var project, key string
	var head time.Time
	for {
		rows, err := tx.Query(ctx, `FETCH FORWARD 500 FROM group_ends`)
		if err != nil {
			return 0, err
		}
		page, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (groupCandidate, error) {
			var c groupCandidate
			err := row.Scan(&c.id, &c.projectHash, &c.endedAt)
			return c, err
		})
		if err != nil {
			return 0, err
		}
		if len(page) == 0 {
			break
		}
		ids, keys := make([]string, 0, len(page)), make([]string, 0, len(page))
		for _, c := range page {
			if key == "" || c.projectHash != project || head.Sub(c.endedAt) > time.Minute {
				project, key, head = c.projectHash, c.id, c.endedAt
			}
			ids = append(ids, c.id)
			keys = append(keys, key)
		}
		if _, err := tx.Exec(ctx, `UPDATE group_candidates c SET group_key=v.key FROM unnest($1::text[],$2::text[]) v(id,key) WHERE c.session_id=v.id`, ids, keys); err != nil {
			return 0, err
		}
	}
	_, err := tx.Exec(ctx, `CLOSE group_ends; CREATE TEMP TABLE group_clusters ON COMMIT DROP AS SELECT group_key,max(ended_at) ended_at,count(*) members,
		'ended-v1-' || md5(string_agg(jsonb_build_array(project_hash,session_id,extract(epoch FROM ended_at))::text,E'\\n' ORDER BY session_id COLLATE "C")) public_key
		FROM group_candidates WHERE ended_at IS NOT NULL GROUP BY group_key HAVING count(*)>=2`)
	return total, err
}

func (s *PgStore) GetSessionGroups(ctx context.Context, f SessionOverviewFilter) (*SessionGroups, error) {
	if f.DashboardUserID <= 0 || (f.PersonalEmail == "" && f.PersonalUserID == "") || (f.Classification != "" && !validPersonalClass(f.Classification)) {
		return nil, ErrPersonalInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	total, err := s.stageSessionGroups(ctx, tx, f)
	if err != nil {
		return nil, err
	}
	result := &SessionGroups{Scope: "filtered_total", GroupingVersion: sessionGroupingVersion,
		OverviewTotal: total,
		Running:       []RunningSessionGroup{}, Ended: []EndedSessionGroup{},
		Counts: SessionGroupCounts{RunningBy: map[string]int{"interrupted": 0, "waiting": 0, "processing": 0, "done": 0, "leftover": 0, "unknown": 0}}}
	if err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE category<>''),count(*) FILTER(WHERE ended_at IS NOT NULL),(SELECT count(*) FROM group_clusters) FROM group_candidates`).Scan(&result.Counts.Running, &result.Counts.Ended, &result.Counts.EndedGroups); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT bucket,count(*) FROM group_candidates WHERE category<>'' GROUP BY bucket`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var bucket string
		var n int
		if err = rows.Scan(&bucket, &n); err != nil {
			break
		}
		result.Counts.RunningBy[bucket] = n
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `SELECT session_id,category,bucket FROM group_candidates WHERE category<>'' ORDER BY session_id LIMIT $1`, sessionGroupCandidateLimit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r RunningSessionGroup
		if err = rows.Scan(&r.SessionID, &r.Category, &r.Bucket); err != nil {
			break
		}
		result.Running = append(result.Running, r)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `SELECT c.session_id,c.ended_at,g.public_key,g.ended_at,g.members FROM group_candidates c JOIN group_clusters g USING(group_key)
		ORDER BY g.ended_at DESC,g.group_key,c.ended_at DESC,c.session_id LIMIT $1`, sessionGroupCandidateLimit-len(result.Running))
	if err != nil {
		return nil, err
	}
	type endedRow struct {
		id, key   string
		end, head time.Time
		total     int
	}
	page, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (endedRow, error) {
		var r endedRow
		err := row.Scan(&r.id, &r.end, &r.key, &r.head, &r.total)
		return r, err
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(page))
	for i, r := range page {
		ids[i] = r.id
	}
	owners, err := groupBatchOwners(ctx, tx, f, ids)
	if err != nil {
		return nil, err
	}
	last := ""
	for _, r := range page {
		c := &groupCandidate{id: r.id, endedAt: r.end, owner: owners[r.id]}
		dismissed, err := groupDismissed(ctx, tx, f.DashboardUserID, c)
		if err != nil {
			return nil, err
		}
		if r.key != last {
			result.Ended = append(result.Ended, EndedSessionGroup{Key: r.key, EndedAt: r.head, Total: r.total, Items: []SessionGroupItem{}})
			last = r.key
		}
		i := len(result.Ended) - 1
		result.Ended[i].Items = append(result.Ended[i].Items, SessionGroupItem{SessionID: r.id, EndedAt: r.end, Dismissed: dismissed})
	}
	var groupedMembers int
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(members),0) FROM group_clusters`).Scan(&groupedMembers); err != nil {
		return nil, err
	}
	result.Truncated = result.Counts.Running+groupedMembers > len(result.Running)+len(page)
	return result, ctx.Err()
}

// GetSessionGroupMembers pages the current group, not the bounded overview DTO.
// Its fingerprint excludes preferences so acknowledged batches remain retryable.
func (s *PgStore) GetSessionGroupMembers(ctx context.Context, f SessionOverviewFilter, key, cursor string, limit int) (*SessionGroupMemberPage, error) {
	if f.DashboardUserID <= 0 || (f.PersonalEmail == "" && f.PersonalUserID == "") || (f.Classification != "" && !validPersonalClass(f.Classification)) || key == "" || len(key) > 128 || limit < 1 || limit > 100 {
		return nil, ErrPersonalInvalid
	}
	after := ""
	if cursor != "" {
		if len(cursor) > 2048 {
			return nil, ErrPersonalInvalid
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, ErrPersonalInvalid
		}
		var c sessionGroupCursor
		if json.Unmarshal(raw, &c) != nil || c.GroupKey != key || c.SessionID == "" {
			return nil, ErrPersonalInvalid
		}
		after = c.SessionID
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if _, err = s.stageSessionGroups(ctx, tx, f); err != nil {
		return nil, err
	}
	result := &SessionGroupMemberPage{GroupKey: key, GroupingVersion: sessionGroupingVersion, Items: []SessionGroupItem{}}
	if err = tx.QueryRow(ctx, `SELECT members FROM group_clusters WHERE public_key=$1`, key).Scan(&result.Total); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPersonalNotFound
		}
		return nil, err
	}
	if after != "" {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM group_candidates c JOIN group_clusters g USING(group_key) WHERE g.public_key=$1 AND c.session_id=$2)`, key, after).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrPersonalInvalid
		}
	}
	rows, err := tx.Query(ctx, `SELECT c.session_id,c.ended_at FROM group_candidates c JOIN group_clusters g USING(group_key)
		WHERE g.public_key=$1 AND c.session_id>$2 ORDER BY c.session_id LIMIT $3`, key, after, limit+1)
	if err != nil {
		return nil, err
	}
	page, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SessionGroupItem, error) {
		var item SessionGroupItem
		err := row.Scan(&item.SessionID, &item.EndedAt)
		return item, err
	})
	if err != nil {
		return nil, err
	}
	if len(page) > limit {
		page = page[:limit]
		raw, _ := json.Marshal(sessionGroupCursor{key, page[len(page)-1].SessionID})
		result.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	ids := make([]string, len(page))
	for i, item := range page {
		ids[i] = item.SessionID
	}
	owners, err := groupBatchOwners(ctx, tx, f, ids)
	if err != nil {
		return nil, err
	}
	for i := range page {
		page[i].Dismissed, err = groupDismissed(ctx, tx, f.DashboardUserID, &groupCandidate{id: page[i].SessionID, endedAt: page[i].EndedAt, owner: owners[page[i].SessionID]})
		if err != nil {
			return nil, err
		}
	}
	result.Items = page
	return result, ctx.Err()
}

// UpdateSessionGroupDismissals checks the entire filtered population and all
// submitted epochs before changing anything. An invalid item rolls back the batch.
func (s *PgStore) UpdateSessionGroupDismissals(ctx context.Context, f SessionOverviewFilter, items []SessionGroupDismissal, dismiss bool, groupKeys ...string) error {
	key := ""
	if len(groupKeys) > 1 {
		return ErrPersonalInvalid
	}
	if len(groupKeys) == 1 {
		key = groupKeys[0]
	}
	if len(key) > 128 {
		return ErrPersonalInvalid
	}
	if f.DashboardUserID <= 0 || (f.PersonalEmail == "" && f.PersonalUserID == "") || len(items) == 0 || len(items) > 100 || (f.Classification != "" && !validPersonalClass(f.Classification)) {
		return ErrPersonalInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Serialize the rare preference mutation with source writers before reading.
	// READ COMMITTED sees a deletion that committed while this lock was waiting;
	// the exclusive source lock then keeps every group's eligibility stable.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err = lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return err
	}
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.SessionID
	}
	if err = lockSessionDeletionTargets(ctx, tx, ids); err != nil {
		return err
	}
	if _, err = s.stageSessionGroups(ctx, tx, f); err != nil {
		return err
	}
	owners, err := groupBatchOwners(ctx, tx, f, ids)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT c.session_id,c.ended_at FROM group_candidates c JOIN group_clusters g USING(group_key) WHERE c.session_id=ANY($1) AND ($2='' OR g.public_key=$2)`, ids, key)
	if err != nil {
		return err
	}
	members := make(map[string]*groupCandidate, len(items))
	for rows.Next() {
		c := &groupCandidate{}
		if err = rows.Scan(&c.id, &c.endedAt); err != nil {
			break
		}
		c.owner = owners[c.id]
		members[c.id] = c
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, item := range items {
		c, ok := members[item.SessionID]
		if !ok || item.SessionID == "" || item.EndedAt.IsZero() || !c.endedAt.Equal(item.EndedAt) || seen[item.SessionID] {
			return ErrPersonalNotFound
		}
		seen[item.SessionID] = true
		if !dismiss {
			var stored personalStoredOwner
			err = tx.QueryRow(ctx, `SELECT source_user_id,source_profile_email,owner_proof,owner_state
				FROM session_group_dismissals WHERE dashboard_user_id=$1 AND session_id=$2
				AND grouping_version=$3 AND ended_at=$4`, f.DashboardUserID, c.id, sessionGroupingVersion, c.endedAt).
				Scan(&stored.identity.userID, &stored.identity.email, &stored.proof, &stored.state)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrPersonalNotFound
				}
				return err
			}
			if !personalOwnerMatches(c.owner, stored) {
				return ErrPersonalNotFound
			}
		} else {
			var stored personalStoredOwner
			err = tx.QueryRow(ctx, `SELECT source_user_id,source_profile_email,owner_proof,owner_state
				FROM session_group_dismissals WHERE dashboard_user_id=$1 AND session_id=$2
				AND grouping_version=$3 AND ended_at=$4`, f.DashboardUserID, c.id, sessionGroupingVersion, c.endedAt).
				Scan(&stored.identity.userID, &stored.identity.email, &stored.proof, &stored.state)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil && !personalOwnerMatches(c.owner, stored) {
				return ErrPersonalNotFound
			}
		}
	}
	for _, item := range items {
		c := members[item.SessionID]
		if !dismiss {
			if _, err = tx.Exec(ctx, `DELETE FROM session_group_dismissals WHERE dashboard_user_id=$1
				AND session_id=$2 AND grouping_version=$3 AND ended_at=$4`, f.DashboardUserID, c.id, sessionGroupingVersion, c.endedAt); err != nil {
				return err
			}
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO session_group_dismissals
			(dashboard_user_id,session_id,source_user_id,source_profile_email,owner_proof,grouping_version,ended_at)
			VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, f.DashboardUserID, c.id,
			c.owner.userID, c.owner.email, c.owner.proof, sessionGroupingVersion, c.endedAt); err != nil {
			return err
		}
		for _, key := range c.owner.keys {
			if _, err = tx.Exec(ctx, `INSERT INTO session_group_dismissal_identity_keys
				(dashboard_user_id,session_id,grouping_version,ended_at,source_user_id,source_profile_email)
				VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, f.DashboardUserID,
				c.id, sessionGroupingVersion, c.endedAt, key.userID, key.email); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// Keep deletion keys for invalidated rows while source exists, but never retain
// a preference after its end observation has expired or its proof disappeared.
func pruneSessionGroupDismissals(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `DELETE FROM session_group_dismissals d WHERE
		NOT EXISTS (SELECT 1 FROM session_observation_events e
			WHERE e.session_id=d.session_id AND e.kind IN ('session_end','gone') AND e.observed_at=d.ended_at
			AND e.identity_provenance='personal_token' AND EXISTS (
				SELECT 1 FROM session_group_dismissal_identity_keys k WHERE k.dashboard_user_id=d.dashboard_user_id
				AND k.session_id=d.session_id AND k.grouping_version=d.grouping_version AND k.ended_at=d.ended_at
				AND e.user_id=k.source_user_id AND lower(e.profile_email)=lower(k.source_profile_email)))
		OR NOT EXISTS (SELECT 1 FROM session_records r WHERE r.session_id=d.session_id
			UNION ALL SELECT 1 FROM session_snapshots o WHERE o.session_id=d.session_id AND o.identity_provenance='personal_token'
			UNION ALL SELECT 1 FROM session_live_states o WHERE o.session_id=d.session_id AND o.identity_provenance='personal_token'
			UNION ALL SELECT 1 FROM session_observation_events o WHERE o.session_id=d.session_id AND o.identity_provenance='personal_token')
		OR (d.owner_state='valid' AND NOT EXISTS (
			SELECT 1 FROM session_records r WHERE r.session_id=d.session_id AND d.owner_proof='session_record'
				AND r.user_id=d.source_user_id AND lower(r.profile_email)=lower(d.source_profile_email)
			UNION ALL SELECT 1 FROM session_snapshots o WHERE o.session_id=d.session_id AND d.owner_proof='personal_token_observation'
				AND o.identity_provenance='personal_token' AND o.user_id=d.source_user_id AND lower(o.profile_email)=lower(d.source_profile_email)
			UNION ALL SELECT 1 FROM session_live_states o WHERE o.session_id=d.session_id AND d.owner_proof='personal_token_observation'
				AND o.identity_provenance='personal_token' AND o.user_id=d.source_user_id AND lower(o.profile_email)=lower(d.source_profile_email)
			UNION ALL SELECT 1 FROM session_observation_events o WHERE o.session_id=d.session_id AND d.owner_proof='personal_token_observation'
				AND o.identity_provenance='personal_token' AND o.user_id=d.source_user_id AND lower(o.profile_email)=lower(d.source_profile_email)))`)
	return err
}
