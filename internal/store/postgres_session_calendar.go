package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
)

const calendarInputLimit = 2000

var ErrSessionCalendarRange = errors.New("invalid session calendar range")

type SessionCalendarFilter struct {
	From, To time.Time
	SessionOverviewFilter
	Access SessionAnalysisAccess
}

type SessionCalendar struct {
	From      string                   `json:"from"`
	To        string                   `json:"to"`
	OwnerView bool                     `json:"owner_view"`
	Sessions  []SessionCalendarSession `json:"sessions"`
}

type SessionCalendarSession struct {
	SessionID string                 `json:"session_id"`
	Title     map[string]interface{} `json:"title"`
	Colors    map[string]interface{} `json:"colors"`
	Requests  *int                   `json:"requests"`
	Cost      map[string]interface{} `json:"cost"`
	Facts     string                 `json:"facts"`
	Range     map[string]string      `json:"range"`
	Segments  []CalendarSegment      `json:"segments"`
	Inputs    CalendarInputs         `json:"inputs"`
	Overview  *SessionOverview       `json:"overview,omitempty"`
}

type CalendarSegment struct {
	Start       string `json:"start"`
	End         string `json:"end"`
	Records     int    `json:"records"`
	HumanInputs *int   `json:"human_inputs"`
}

type CalendarInput struct {
	TS      string                 `json:"ts"`
	TurnKey string                 `json:"turn_key"`
	Preview map[string]interface{} `json:"preview"`
	at      time.Time
}

type CalendarInputs struct {
	State     string          `json:"state"`
	Items     []CalendarInput `json:"items"`
	Truncated bool            `json:"truncated"`
}

func calendarTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func calendarScope(f *SessionCalendarFilter) (bool, error) {
	if f.Access.User {
		// Never trust a caller-supplied rollup scope for a restricted reader.
		f.UserID, f.ProfileEmail = f.Access.UserID, f.Access.ProfileEmail
		f.LoginEmail = ""
	} else if f.UserID == "" && f.ProfileEmail == "" {
		f.UserID, f.ProfileEmail = f.Access.OwnerUserID, f.Access.OwnerProfileEmail
	}
	if f.UserID != "" && f.ProfileEmail != "" {
		return false, ErrSessionCalendarRange
	}
	owner := f.Access.User || (f.UserID != "" && f.UserID == f.Access.OwnerUserID) ||
		(f.UserID == "" && f.ProfileEmail != "" && f.ProfileEmail == f.Access.OwnerProfileEmail)
	return owner, nil
}

// ReadSessionCalendar reads segment candidates and range-only facts in one bounded
// snapshot. A range-only candidate never manufactures an activity segment.
func (s *PgStore) ReadSessionCalendar(parent context.Context, f SessionCalendarFilter) (SessionCalendar, error) {
	result := SessionCalendar{From: calendarTime(f.From), To: calendarTime(f.To), Sessions: []SessionCalendarSession{}}
	if f.From.IsZero() || !f.From.Before(f.To) || f.To.Sub(f.From) > 14*24*time.Hour {
		return result, ErrSessionCalendarRange
	}
	if f.Classification != "" && !validPersonalClass(f.Classification) {
		return result, ErrPersonalInvalid
	}
	owner, err := calendarScope(&f)
	if err != nil {
		return result, err
	}
	result.OwnerView = owner
	if f.UserID == "__no_access__" || f.UserID == "" && (f.ProfileEmail == "" || f.ProfileEmail == "__no_access__") {
		return result, nil
	}
	ctx, cancel, tx, err := s.beginSessionOverviewAnalysis(parent)
	if err != nil {
		return result, err
	}
	defer cancel()
	defer tx.Rollback(ctx) //nolint:errcheck

	scope, value := sessionOverviewRollupScope(f.SessionOverviewFilter, false)
	args := []interface{}{f.From, f.To, scope, value, f.UserID, f.ProfileEmail}
	n := len(args)
	conditions := appendRollupFilters(f.SessionOverviewFilter, false, &n, &args)
	// Named classifications require a saved personal row. Narrow candidates before
	// scanning activity and raw ownership sources; retain the full proof below.
	// Unsorted also includes sessions with no saved row and cannot use this shortcut.
	if f.Classification != "" && f.Classification != "unsorted" {
		var classifiedIDs []string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(array_agg(session_id),'{}'::text[])
			FROM session_personal_state WHERE dashboard_user_id=$1 AND classification=$2
			AND owner_state='valid' AND expires_at>now()`, f.DashboardUserID, f.Classification).Scan(&classifiedIDs); err != nil {
			return result, err
		}
		if len(classifiedIDs) == 0 {
			return result, tx.Commit(ctx)
		}
		args = append(args, classifiedIDs)
		conditions = append(conditions, fmt.Sprintf("r.session_id=ANY($%d::text[])", len(args)))
	}
	conditions = append(conditions,
		"NOT EXISTS (SELECT 1 FROM excluded_sessions x WHERE x.session_id=r.session_id)",
		"NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id=r.session_id)",
		"NOT EXISTS (SELECT 1 FROM blocked_projects bp WHERE bp.project_hash=r.project_hash)",
		`NOT EXISTS (SELECT 1 FROM session_analysis_facts af WHERE af.session_id=r.session_id
			AND EXISTS (SELECT 1 FROM jsonb_to_recordset(af.account_keys)
			AS a(login_email text,billing_provider text,account_id text) WHERE NOT (`+excludedAccountPredicateSQL("a")+`)))`)
	if f.LoginEmail != "" {
		args = append(args, f.LoginEmail)
		conditions = append(conditions, fmt.Sprintf(`EXISTS (SELECT 1 FROM session_overview_rollups account
			WHERE account.session_id=r.session_id AND account.scope_type='login' AND account.scope_value=$%d)`, len(args)))
	}
	factScope := `cardinality(f.user_ids)<=1 AND cardinality(f.profile_emails)<=1 AND
		(($5<>'' AND cardinality(f.user_ids)=1 AND f.owner_user_id=$5)
		OR ($5='' AND cardinality(f.profile_emails)=1 AND f.owner_profile_email=$6))`
	ownerFactScope := factScope
	identityScope := `ci.has_owner AND NOT ci.has_foreign`
	segmentScope := `(($5<>'' AND s.owner_user_id=$5) OR ($5='' AND s.owner_profile_email=$6))`
	if !f.Access.User {
		// An administrator selects a contributor, not an exclusive owner. Facts and
		// activity remain whole-session values; personal signals still require ownership.
		factScope = `(($5<>'' AND $5=ANY(f.user_ids)) OR ($5='' AND $6=ANY(f.profile_emails)))`
		segmentScope = `EXISTS (SELECT 1 FROM session_analysis_facts f WHERE f.session_id=s.session_id AND ` + factScope + `)`
		identityScope = `ci.has_owner`
	}
	// A removal or newly enabled copy judgement invalidates cached content until
	// the worker republishes it. The marker, queue and source fences share this TX.
	fresh := `NOT EXISTS (SELECT 1 FROM session_fact_dirty d WHERE d.session_id=f.session_id
		AND (d.removal_generation>f.source_generation OR (d.generation>f.source_generation AND (d.reasons & 1024)<>0)))
		AND NOT EXISTS (SELECT 1 FROM schema_backfills b WHERE b.name='session_fact_copies_v1' AND b.done_at>f.computed_at)`
	// The ownership proof reads the current visible sources, including a large
	// session's metadata. Evaluate it once before joining per-session facts:
	// inlining the aggregate lets a low candidate estimate repeatedly rescan the
	// same population through nested joins (2,000 sessions plus 200,000 records).
	identitySQL := strings.Replace(sessionOverviewCurrentIdentitySQL("$5", "$6"),
		"current_identity AS (", "current_identity AS MATERIALIZED (", 1)
	query := `WITH scoped AS (
		SELECT r.*,p.project_name FROM session_overview_rollups r
		LEFT JOIN projects p ON p.project_hash=r.project_hash AND p.agent=COALESCE(NULLIF(r.agent,''),'claude')
		WHERE r.scope_type=$3 AND r.scope_value=$4 AND ` + strings.Join(conditions, " AND ") + `
	), overview AS (
		SELECT r.session_id FROM scoped r WHERE
		EXISTS (SELECT 1 FROM session_activity_segments s WHERE s.session_id=r.session_id
			AND ` + segmentScope + ` AND tstzrange(s.seg_start,s.seg_end,'[]') && tstzrange($1,$2,'[)'))
		OR EXISTS (SELECT 1 FROM session_analysis_facts f WHERE f.session_id=r.session_id
			AND ` + factScope + ` AND ` + sessionAnalysisVisibleSQL + ` AND ` + fresh + `
			AND (f.coverage='truncated' OR (f.agent='codex' AND NOT EXISTS
				(SELECT 1 FROM session_activity_segments any_segment WHERE any_segment.session_id=f.session_id)))
			AND tstzrange(COALESCE(f.first_activity_at,r.start_time),COALESCE(f.last_activity_at,r.end_time),'[]') && tstzrange($1,$2,'[)'))
	)` + identitySQL + `, candidate AS MATERIALIZED (
		SELECT r.*,f.coverage,f.title,f.title_source,f.title_state,f.requests,f.cost_usd AS fact_cost,
			f.cost_source,f.rules_version,f.turn_boundaries,f.session_id AS fact_id,
			COALESCE((` + ownerFactScope + `) AND NOT ci.has_foreign,false) AS personal_owner,
			COALESCE(f.first_activity_at,r.start_time) AS range_start,COALESCE(f.last_activity_at,r.end_time) AS range_end
		FROM scoped r JOIN overview o USING(session_id)
		JOIN current_identity ci USING(session_id)
		LEFT JOIN session_analysis_facts f ON f.session_id=r.session_id
			AND ` + factScope + ` AND ` + sessionAnalysisVisibleSQL + ` AND ` + fresh + `
		WHERE ` + identityScope + `
	)`
	personalJoin := ""
	if f.Classification != "" {
		personalSQL, personalArgs := personalOverviewClassificationSQL(f.SessionOverviewFilter, len(args)+1)
		if personalSQL == "" {
			return result, tx.Commit(ctx)
		}
		query += personalSQL
		args = append(args, personalArgs...)
		args = append(args, f.Classification)
		personalJoin = fmt.Sprintf(" JOIN personal_classified pc ON pc.session_id=c.session_id AND pc.classification=$%d", len(args))
	}
	query += ` SELECT c.session_id,c.coverage,c.title,c.title_source,c.title_state,c.requests,c.fact_cost,c.cost_source,
		c.rules_version,c.turn_boundaries,c.range_start,c.range_end,c.personal_owner,
		to_jsonb(r)||jsonb_build_object('project_name',COALESCE(r.project_name,'')),
		COALESCE((SELECT jsonb_agg(jsonb_build_object('start',s.seg_start,'end',s.seg_end,'records',s.records,
			'human_inputs',CASE WHEN c.turn_boundaries='observed' THEN s.human_inputs END) ORDER BY s.seg_start)
			FROM session_activity_segments s WHERE s.session_id=c.session_id AND ` + segmentScope + `
			AND c.fact_id IS NOT NULL AND c.coverage='full'
			AND tstzrange(s.seg_start,s.seg_end,'[]') && tstzrange($1,$2,'[)')), '[]'::jsonb)
		FROM candidate c JOIN scoped r USING(session_id)` + personalJoin + ` ORDER BY c.session_id`
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return result, err
	}
	inputIDs := []string{}
	for rows.Next() {
		var id string
		var coverage, title, titleSource, titleState, costSource, turnBoundaries *string
		var requests, version *int
		var cost *float64
		var start, end time.Time
		var personalOwner bool
		var overviewJSON, segmentJSON []byte
		if err = rows.Scan(&id, &coverage, &title, &titleSource, &titleState, &requests, &cost, &costSource,
			&version, &turnBoundaries, &start, &end, &personalOwner, &overviewJSON, &segmentJSON); err != nil {
			break
		}
		overview := &SessionOverview{}
		segments := []CalendarSegment{}
		if err = json.Unmarshal(overviewJSON, overview); err != nil {
			break
		}
		if err = json.Unmarshal(segmentJSON, &segments); err != nil {
			break
		}
		for i := range segments {
			segmentStart, parseErr := time.Parse(time.RFC3339Nano, segments[i].Start)
			if parseErr != nil {
				rows.Close()
				return result, parseErr
			}
			segmentEnd, parseErr := time.Parse(time.RFC3339Nano, segments[i].End)
			if parseErr != nil {
				rows.Close()
				return result, parseErr
			}
			segments[i].Start, segments[i].End = calendarTime(segmentStart), calendarTime(segmentEnd)
		}
		state := "building"
		if overview.Agent == "gjc" || overview.Agent == "omo" {
			state = "unsupported"
		} else if coverage != nil {
			state = "ready"
			if *coverage == "truncated" {
				state = "truncated"
			} else if version != nil && *version != sessionfacts.RulesVersion {
				state = "stale"
			}
		}
		var project interface{}
		if overview.ProjectHash != "" {
			project = map[string]interface{}{"hash": overview.ProjectHash, "name": analysisString(overview.ProjectName)}
		}
		colors := map[string]interface{}{"project": project, "agent": overview.Agent, "model": analysisString(overview.Model),
			"profile": nil, "components": nil, "screens": nil, "kinds": nil}
		if owner && personalOwner {
			colors["status"] = nil
		}
		titleValue := map[string]interface{}{"text": title, "source": titleSource, "state": "none"}
		if titleState != nil {
			titleValue["state"] = *titleState
		}
		costValue := map[string]interface{}{"usd": cost, "source": "none"}
		if costSource != nil {
			costValue["source"] = *costSource
		}
		inputState := "undeterminable"
		if (state == "ready" || state == "stale") && turnBoundaries != nil && *turnBoundaries == "observed" {
			inputState = "observed"
			inputIDs = append(inputIDs, id)
		}
		if state != "ready" && state != "stale" {
			segments = []CalendarSegment{}
		}
		result.Sessions = append(result.Sessions, SessionCalendarSession{SessionID: id, Title: titleValue, Colors: colors,
			Requests: requests, Cost: costValue, Facts: state, Range: map[string]string{"start": calendarTime(start), "end": calendarTime(end)},
			Segments: segments, Inputs: CalendarInputs{State: inputState, Items: []CalendarInput{}}, Overview: overview})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return result, err
	}
	if f.IncludeSessionRecords && result.OwnerView {
		if err := addCalendarSessionRecords(ctx, tx, &result, f); err != nil {
			return result, err
		}
	}
	if err := calendarLoadInputs(ctx, tx, &result, inputIDs, f.From, f.To); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func calendarLoadInputs(ctx context.Context, tx pgx.Tx, result *SessionCalendar, ids []string, from, to time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT session_id,turn_key,started_at,preview,preview_state,detail FROM session_turn_facts
		WHERE session_id=ANY($1) ORDER BY session_id,ordinal`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	inputs := make(map[string][]CalendarInput)
	for rows.Next() {
		var id, key, state string
		var started time.Time
		var preview *string
		var body []byte
		if err = rows.Scan(&id, &key, &started, &preview, &state, &body); err != nil {
			return err
		}
		var detail sessionfacts.TurnDetail
		if err = json.Unmarshal(body, &detail); err != nil {
			return err
		}
		for _, input := range detail.Inputs {
			if !input.TS.Before(from) && input.TS.Before(to) {
				var text interface{}
				inputState := "none"
				if input.TS.Equal(started) {
					inputState = state
					if state == "ok" {
						text = preview
					}
				}
				inputs[id] = append(inputs[id], CalendarInput{TS: calendarTime(input.TS), TurnKey: key, at: input.TS,
					Preview: map[string]interface{}{"text": text, "state": inputState}})
			}
		}
		for _, ts := range detail.ExtraInputTS {
			if !ts.Before(from) && ts.Before(to) {
				inputs[id] = append(inputs[id], CalendarInput{TS: calendarTime(ts), TurnKey: key, at: ts,
					Preview: map[string]interface{}{"text": nil, "state": "none"}})
			}
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for i := range result.Sessions {
		item := &result.Sessions[i]
		values := inputs[item.SessionID]
		sort.SliceStable(values, func(i, j int) bool { return values[i].at.Before(values[j].at) })
		item.Inputs.Truncated = len(values) > calendarInputLimit
		if item.Inputs.Truncated {
			values = values[:calendarInputLimit]
		}
		if len(values) > 0 {
			item.Inputs.Items = values
		}
	}
	return nil
}
