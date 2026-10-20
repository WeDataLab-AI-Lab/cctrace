package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
)

var sessionAnalysisCategories = []string{"waiting", "running", "ended", "stale_live", "open_question", "idle", "collector_delayed", "recent", "inactive"}

type sessionOverviewAnalysisQueryOptions struct {
	unpaged          bool
	judgementBuckets bool
}

func sessionGitNumberSQL(snapshot, field string) string {
	value := snapshot + `->'git'->'` + field + `'`
	return `CASE WHEN jsonb_typeof(` + value + `)='number' THEN (` + snapshot + `->'git'->>'` + field + `')::numeric END`
}

func sessionGitSnapshotReasonSQL(snapshot string) string {
	edited := sessionGitNumberSQL(snapshot, "edited_files")
	uncommitted := sessionGitNumberSQL(snapshot, "uncommitted_edited_files")
	repositories := sessionGitNumberSQL(snapshot, "repositories")
	unresolved := sessionGitNumberSQL(snapshot, "unresolved")
	truncated := snapshot + `->'truncated'`
	partial := snapshot + `->'git'->'partial'`
	partialText := snapshot + `->'git'->>'partial'`
	return `CASE
		WHEN ` + snapshot + ` IS NULL OR jsonb_typeof(` + snapshot + `->'git')='null' THEN 'no_git_snapshot'
		WHEN jsonb_typeof(` + snapshot + `->'git') IS DISTINCT FROM 'object' THEN 'invalid_git_snapshot'
		WHEN ` + truncated + ` IS NOT NULL AND jsonb_typeof(` + truncated + `) NOT IN ('array','null') THEN 'invalid_git_snapshot'
		WHEN jsonb_typeof(` + truncated + `)='array' AND ` + truncated + ` ? 'git' THEN 'truncated_snapshot'
		WHEN jsonb_typeof(` + partial + `) NOT IN ('boolean','null') THEN 'invalid_git_snapshot'
		WHEN ` + partialText + `='true' THEN 'partial_snapshot'
		WHEN ` + edited + ` IS NULL OR ` + uncommitted + ` IS NULL OR ` + repositories + ` IS NULL OR ` + unresolved + ` IS NULL
			OR ` + edited + `<>trunc(` + edited + `) OR ` + uncommitted + `<>trunc(` + uncommitted + `)
			OR ` + repositories + `<>trunc(` + repositories + `) OR ` + unresolved + `<>trunc(` + unresolved + `) THEN 'invalid_git_snapshot'
		WHEN ` + edited + `<0 OR ` + uncommitted + `<0 OR ` + repositories + `<0 OR ` + unresolved + `<0 THEN 'invalid_git_snapshot'
		WHEN ` + unresolved + `>0 THEN 'unresolved_git_paths'
		ELSE NULL END`
}

func sessionOverviewAnalysisBucketSQL(alias string) string {
	turnState := `CASE WHEN ` + alias + `.analysis_state NOT IN ('ready','stale') OR ` + alias + `.detail->'signals'->'turn'->>'state' IS DISTINCT FROM 'observed' THEN 'undeterminable'
		WHEN ` + alias + `.last_turn_status='ended' THEN 'pass' ELSE 'fail' END`
	questionState := `CASE WHEN ` + alias + `.analysis_state NOT IN ('ready','stale') OR ` + alias + `.detail->'signals'->'question'->>'state' IS DISTINCT FROM 'observed'
		OR jsonb_typeof(` + alias + `.detail->'signals'->'question'->'value') IS DISTINCT FROM 'number' THEN 'undeterminable'
		WHEN (` + alias + `.detail->'signals'->'question'->>'value')::numeric=0 THEN 'pass' ELSE 'fail' END`
	backgroundState := `CASE WHEN ` + alias + `.analysis_state NOT IN ('ready','stale') OR ` + alias + `.detail->'signals'->'background'->>'state' IS DISTINCT FROM 'observed'
		OR jsonb_typeof(` + alias + `.detail->'signals'->'background'->'value') IS DISTINCT FROM 'number' THEN 'undeterminable'
		WHEN (` + alias + `.detail->'signals'->'background'->>'value')::numeric=0 THEN 'pass' ELSE 'fail' END`
	committedState := `CASE WHEN ` + alias + `.analysis_state NOT IN ('ready','stale') OR ` + alias + `.git_reason IS NOT NULL THEN 'undeterminable'
		WHEN ` + alias + `.git_count=0 THEN 'pass' WHEN ` + alias + `.git_count>0 THEN 'fail' ELSE 'undeterminable' END`
	return `CASE WHEN ` + turnState + `='fail' THEN 'ended_midway'
		WHEN ` + questionState + `='fail' OR ` + backgroundState + `='fail' OR ` + committedState + `='fail' THEN 'leftover'
		WHEN ` + turnState + `='pass' AND ` + questionState + `='pass' AND ` + backgroundState + `='pass'
			AND ` + committedState + `='pass' AND FALSE THEN 'done'
		ELSE 'unknown' END`
}

// sessionOverviewAnalysisQuery keeps the existing overview population intact and
// joins one fact row before applying analysis filters, ordering, and pagination.
func sessionOverviewAnalysisQuery(base string, args []interface{}, f SessionOverviewFilter, count, categories bool, options ...sessionOverviewAnalysisQueryOptions) (string, []interface{}) {
	queryOptions := sessionOverviewAnalysisQueryOptions{}
	if len(options) > 0 {
		queryOptions = options[0]
	}
	allRows := queryOptions.unpaged
	first := len(args)
	if len(args) > 0 {
		if _, period := args[0].(pgx.QueryExecMode); period {
			// The period path passes an execution mode before SQL parameters.
			first--
		}
	}
	add := func(value interface{}) string {
		args = append(args, value)
		first++
		return fmt.Sprintf("$%d", first)
	}
	owner := f.AnalysisOwner && f.DashboardUserID != 0 && (f.PersonalUserID != "" || f.PersonalEmail != "")
	analysisFilters := f.AnalysisPlan != "" || f.AnalysisQuery != "" || f.AnalysisCategory != "" ||
		f.IncludeSessionRecords && (f.AnalysisBucket != "" || f.AnalysisKind != "" ||
			f.AnalysisComponent != "" || f.AnalysisScreen != "")
	countBaseOnly := owner && count && !categories && !allRows && !queryOptions.judgementBuckets && !analysisFilters
	pageBaseFirst := owner && !count && !categories && !allRows && !queryOptions.judgementBuckets &&
		!analysisFilters && f.AnalysisSort != "requests"
	if countBaseOnly {
		return `SELECT count(*) FROM (` + base + `) overview_population`, args
	}
	queryPrefix := `WITH overview AS (` + base + `)`
	if pageBaseFirst {
		limit, offset := sessionOverviewAnalysisPage(f)
		limitArg, offsetArg := add(limit), add(offset)
		order := strings.ReplaceAll(sessionOverviewAnalysisOrder(f.AnalysisSort), "j.", "base_row.")
		queryPrefix = `WITH base_page AS MATERIALIZED (
			SELECT base_row.* FROM (` + base + `) base_row
			ORDER BY ` + order + ` LIMIT ` + limitArg + ` OFFSET ` + offsetArg + `
		), overview AS MATERIALIZED (SELECT * FROM base_page)`
	}
	currentIdentitySQL := ""
	factScope := "TRUE"
	var uid, email string
	if owner {
		uid, email = add(f.PersonalUserID), add(f.PersonalEmail)
	}
	if owner {
		currentIdentitySQL = sessionOverviewCurrentIdentitySQL(uid, email)
		currentScope := `COALESCE(ci.has_owner,false) AND NOT COALESCE(ci.has_foreign,false)`
		factScope = fmt.Sprintf(`cardinality(f.profile_emails)<=1 AND cardinality(f.user_ids)<=1 AND
			((%s<>'' AND cardinality(f.user_ids)=1 AND f.owner_user_id=%s)
			OR (%s='' AND %s<>'' AND cardinality(f.profile_emails)=1 AND f.owner_profile_email=%s)) AND %s`, uid, uid, uid, email, email, currentScope)
	} else {
		if f.UserID != "" {
			p := add(f.UserID)
			factScope += " AND " + p + "=ANY(f.user_ids)"
		} else if f.ProfileEmail != "" {
			p := add(f.ProfileEmail)
			factScope += " AND " + p + "=ANY(f.profile_emails)"
		} else if f.LoginEmail != "" {
			p := add(f.LoginEmail)
			factScope += " AND " + p + "=ANY(f.login_emails)"
		}
	}
	state := fmt.Sprintf(`CASE WHEN o.agent NOT IN ('claude','codex') THEN 'unsupported'
		WHEN f.session_id IS NULL AND `+sessionAnalysisNoneSQL("o.session_id")+` THEN 'none'
		WHEN f.session_id IS NULL THEN 'building'
		WHEN f.coverage='truncated' THEN 'truncated'
		WHEN f.rules_version<>%d THEN 'stale' ELSE 'ready' END`, sessionfacts.RulesVersion)
	if owner {
		mixedVisible := strings.ReplaceAll(sessionAnalysisVisibleSQL, "f.", "mixed.")
		state = fmt.Sprintf(`CASE WHEN o.agent NOT IN ('claude','codex') THEN 'unsupported'
			WHEN f.session_id IS NULL AND COALESCE(ci.has_owner,false) AND
				(COALESCE(ci.has_foreign,false) OR EXISTS (SELECT 1 FROM session_analysis_facts mixed
				WHERE mixed.session_id=o.session_id AND %s
				AND (cardinality(mixed.user_ids)>1 OR cardinality(mixed.profile_emails)>1))) THEN 'withheld'
			WHEN f.session_id IS NULL AND `+sessionAnalysisNoneSQL("o.session_id")+` THEN 'none'
		WHEN f.session_id IS NULL THEN 'building'
			WHEN f.coverage='truncated' THEN 'truncated'
			WHEN f.rules_version<>%d THEN 'stale' ELSE 'ready' END`, mixedVisible, sessionfacts.RulesVersion)
	}
	currentJoin, lastActivity, activityName := "", "f.last_activity_at", "last_activity_at"
	gitSelect := "NULL::text AS git_reason,NULL::numeric AS git_count"
	if owner {
		currentJoin = ` LEFT JOIN current_identity ci ON ci.session_id=o.session_id`
		if categories || f.AnalysisCategory != "" {
			lastActivity = "GREATEST(f.last_activity_at,ra.last_record_at)"
			currentIdentitySQL += `, record_activity AS (
				SELECT r.session_id,max(r.ts) AS last_record_at FROM session_records r
				JOIN overview o ON o.session_id=r.session_id GROUP BY r.session_id
			)`
			currentJoin += ` LEFT JOIN record_activity ra ON ra.session_id=o.session_id`
		} else {
			activityName = "posted_activity_at"
		}
	}
	needGitSnapshot := owner && f.IncludeSessionRecords && (f.AnalysisBucket != "" || queryOptions.judgementBuckets)
	if needGitSnapshot {
		snapshotVisible := strings.ReplaceAll(sessionFactSnapshotVisibleSQL, "$1", "o.session_id")
		gitReason := sessionGitSnapshotReasonSQL("ss.snapshot")
		currentIdentitySQL += `, git_snapshots AS (
			SELECT DISTINCT ON (ss.session_id) ss.session_id,` + gitReason + ` AS git_reason,
				CASE WHEN ` + gitReason + ` IS NULL THEN ` + sessionGitNumberSQL("ss.snapshot", "uncommitted_edited_files") + ` END AS git_count
			FROM session_snapshots ss JOIN overview o ON o.session_id=ss.session_id
			WHERE ` + snapshotVisible + ` AND ((` + uid + `<>'' AND ss.user_id=` + uid + `)
				OR (` + uid + `='' AND ` + email + `<>'' AND lower(ss.profile_email)=lower(` + email + `)))
			ORDER BY ss.session_id,ss.observed_at DESC
		)`
		currentJoin += ` LEFT JOIN git_snapshots gs ON gs.session_id=o.session_id`
		gitSelect = `CASE WHEN gs.session_id IS NULL THEN 'no_git_snapshot' ELSE gs.git_reason END AS git_reason,gs.git_count`
	}
	query := queryPrefix + currentIdentitySQL + `, joined AS (
		SELECT o.*, f.coverage, f.title, f.title_source, f.title_state, f.requests AS fact_requests,
			f.inputs AS fact_inputs, f.last_context_tokens, f.last_context_window,
			f.commits_state, f.commit_count,
			f.last_context_source, f.large_window_seen, f.has_plan_file, f.used_plan_mode,
			f.last_turn_status, f.last_turn_key, f.open_question_turn_key, f.open_questions,
			` + lastActivity + ` AS ` + activityName + `, f.detail, ` + state + ` AS analysis_state,` + gitSelect + `
		FROM overview o` + currentJoin + ` LEFT JOIN session_analysis_facts f ON f.session_id=o.session_id
			AND ` + factScope + ` AND ` + sessionAnalysisVisibleSQL + `
	)`
	ownerSQL := ""
	if owner && (categories || f.AnalysisCategory != "" || !count) {
		ownerSQL = sessionOverviewAnalysisActivitySQL(uid, email)
		if categories || f.AnalysisCategory != "" {
			query += ownerSQL
		}
	}
	where := []string{}
	if f.AnalysisPlan != "" {
		field := "has_plan_file"
		if f.AnalysisPlan == "mode" {
			field = "used_plan_mode"
		}
		where = append(where, "j."+field)
	}
	if f.AnalysisQuery != "" {
		p := add("%" + escapeAnalysisLike(f.AnalysisQuery) + "%")
		where = append(where, `j.title ILIKE `+p+` ESCAPE '\' AND j.title_state='ok'`)
	}
	if f.AnalysisCategory != "" {
		p := add(f.AnalysisCategory)
		where = append(where, "activity.category="+p)
	}
	if f.IncludeSessionRecords {
		if f.AnalysisBucket != "" {
			p := add(f.AnalysisBucket)
			where = append(where, sessionOverviewAnalysisBucketSQL("j")+"="+p)
		}
		if f.AnalysisKind != "" || f.AnalysisComponent != "" || f.AnalysisScreen != "" {
			p5owner := fmt.Sprintf(`((%s<>'' AND p5f.owner_user_id=%s) OR (%s='' AND %s<>'' AND lower(p5f.owner_profile_email)=lower(%s)))`, uid, uid, uid, email, email)
			p5record := `EXISTS(SELECT 1 FROM session_ai_records p5r JOIN session_analysis_facts p5f USING(session_id)
				WHERE p5r.session_id=j.session_id AND p5r.outcome='ok' AND ` + p5owner + ` AND ` + recordVisibilityPredicate("p5r", "p5f")
			if f.AnalysisKind != "" {
				p := add(f.AnalysisKind)
				where = append(where, p5record+` AND `+p+`=ANY(p5r.kinds))`)
			}
			if f.AnalysisComponent != "" {
				p := add(f.AnalysisComponent)
				where = append(where, p5record+` AND `+p+`=ANY(p5r.components))`)
			}
			if f.AnalysisScreen != "" {
				p := add(f.AnalysisScreen)
				where = append(where, p5record+` AND `+p+`=ANY(p5r.screens))`)
			}
		}
	}
	from := " FROM joined j"
	if owner && (categories || f.AnalysisCategory != "") {
		from += " LEFT JOIN activity ON activity.session_id=j.session_id"
	}
	if len(where) > 0 {
		from += " WHERE " + strings.Join(where, " AND ")
	}
	if categories {
		if len(where) > 0 {
			from += ` AND activity.category IS NOT NULL`
		} else {
			from += ` WHERE activity.category IS NOT NULL`
		}
		return query + ` SELECT activity.category,count(*)` + from + ` GROUP BY activity.category`, args
	}
	if count {
		return query + ` SELECT count(*)` + from, args
	}
	if queryOptions.judgementBuckets {
		bucket := sessionOverviewAnalysisBucketSQL("j")
		return query + ` SELECT ` + bucket + ` AS bucket,count(*)` + from + ` GROUP BY ` + bucket, args
	}
	if allRows {
		return query + ` SELECT j.session_id,j.project_hash,j.analysis_state` + from + ` ORDER BY ` + sessionOverviewAnalysisOrder(f.AnalysisSort), args
	}
	if owner && f.AnalysisCategory == "" && !allRows {
		// The ordinary unfiltered page already narrowed overview before identity
		// evaluation. Filtered and requests-sorted pages must preserve the broader
		// candidate set until facts and current identity have been checked.
		if pageBaseFirst {
			query += `, page AS (
				SELECT j.*,GREATEST(j.posted_activity_at,ra.last_record_at) AS last_activity_at
				FROM joined j LEFT JOIN LATERAL (SELECT max(r.ts) AS last_record_at
					FROM session_records r WHERE r.session_id=j.session_id) ra ON true
			)`
		} else {
			pageWhere := ""
			if len(where) > 0 {
				pageWhere = " WHERE " + strings.Join(where, " AND ")
			}
			order := sessionOverviewAnalysisOrder(f.AnalysisSort)
			limit, offset := sessionOverviewAnalysisPage(f)
			query += `, selected AS (SELECT * FROM joined j` + pageWhere + ` ORDER BY ` + order +
				` LIMIT ` + add(limit) + ` OFFSET ` + add(offset) + `), page AS (
				SELECT s.*,GREATEST(s.posted_activity_at,ra.last_record_at) AS last_activity_at
				FROM selected s LEFT JOIN LATERAL (SELECT max(r.ts) AS last_record_at
					FROM session_records r WHERE r.session_id=s.session_id) ra ON true
			)`
		}
		ownerSQL = strings.ReplaceAll(ownerSQL, "JOIN joined j", "JOIN page j")
		ownerSQL = strings.ReplaceAll(ownerSQL, "FROM joined j", "FROM page j")
		query += ownerSQL
		from = " FROM page j LEFT JOIN activity ON activity.session_id=j.session_id"
	}
	categoryExpr, signalsExpr := "NULL::text", "NULL::jsonb"
	if owner {
		categoryExpr = "activity.category"
		signalsExpr = `jsonb_strip_nulls(jsonb_build_object(
			'turn', CASE WHEN j.detail->'signals'->'turn'->>'state' IS NULL OR j.detail->'signals'->'turn'->>'state'='not_applicable' THEN NULL ELSE
				CASE WHEN j.detail->'signals'->'turn'->>'state'='observed' THEN to_jsonb(j.last_turn_status) ELSE 'null'::jsonb END END,
			'question', CASE WHEN j.detail->'signals'->'question'->>'state' IS NULL OR j.detail->'signals'->'question'->>'state'='not_applicable' THEN NULL ELSE
				CASE WHEN j.detail->'signals'->'question'->>'state'='observed' THEN j.detail->'signals'->'question'->'value' ELSE 'null'::jsonb END END,
			'background', CASE WHEN j.detail->'signals'->'background'->>'state' IS NULL OR j.detail->'signals'->'background'->>'state'='not_applicable' THEN NULL ELSE
				CASE WHEN j.detail->'signals'->'background'->>'state'='observed' THEN j.detail->'signals'->'background'->'value' ELSE 'null'::jsonb END END,
			'todo', CASE WHEN j.detail->'signals'->'todo'->>'state' IS NULL OR j.detail->'signals'->'todo'->>'state'='not_applicable' THEN NULL ELSE
				CASE WHEN j.detail->'signals'->'todo'->>'state'='observed' THEN j.detail->'signals'->'todo'->'value' ELSE 'null'::jsonb END END))`
	}
	// Do not manufacture an uncommitted count; its signal has no source yet.
	analysis := `jsonb_build_object('state',j.analysis_state,'title',jsonb_build_object('text',j.title,'source',j.title_source,'state',j.title_state),
		'requests',j.fact_requests,'inputs',j.fact_inputs,
		'commits',CASE WHEN j.commits_state='observed' THEN to_jsonb(j.commit_count) ELSE 'null'::jsonb END,
		'context',jsonb_build_object('percent',CASE WHEN j.last_context_window>0 AND j.last_context_tokens IS NOT NULL THEN
			j.last_context_tokens::double precision * 100 / j.last_context_window END,
			'tokens',j.last_context_tokens,'source',j.last_context_source,'large_window_seen',j.large_window_seen),
		'plan_flags',jsonb_build_object('has_plan_file',j.has_plan_file,'used_plan_mode',j.used_plan_mode))`
	analysis = `CASE WHEN j.analysis_state='withheld' THEN jsonb_build_object('state','withheld','reason','mixed_identity')
		WHEN j.analysis_state IN ('building','none','unsupported') THEN jsonb_build_object('state',j.analysis_state)
		ELSE ` + analysis + ` END`
	if owner {
		analysis = `CASE WHEN j.analysis_state IN ('ready','stale') AND activity.category IS NOT NULL
			THEN ` + analysis + ` || jsonb_build_object('category',` + categoryExpr + `,'signals',` + signalsExpr + `)
			ELSE ` + analysis + ` END`
	}
	order := sessionOverviewAnalysisOrder(f.AnalysisSort)
	limit, offset := sessionOverviewAnalysisPage(f)
	projection := ` SELECT j.session_id,j.profile_email,j.user_id,j.login_email,j.account_count,j.model,j.agent,
		j.start_time,j.end_time,j.input_tokens,j.output_tokens,j.cost_usd,j.event_count,j.has_sync,j.project_hash,
		j.project_name,j.entrypoint,j.has_enriched,j.cctrace_version,j.claude_version,j.login_email_inferred,` + analysis
	if allRows {
		return query + projection + from + ` ORDER BY ` + order, args
	}
	if owner && f.AnalysisCategory == "" {
		return query + projection + from + ` ORDER BY ` + order, args
	}
	return query + projection + from + ` ORDER BY ` + order + ` LIMIT ` + add(limit) + ` OFFSET ` + add(offset), args
}

func sessionOverviewAnalysisOrder(sort string) string {
	switch sort {
	case "start":
		return "j.start_time DESC,j.session_id DESC"
	case "cost":
		return "j.cost_usd DESC,j.session_id DESC"
	case "requests":
		return "j.fact_requests DESC NULLS LAST,j.session_id DESC"
	default:
		return "j.end_time DESC,j.session_id DESC"
	}
}

func sessionOverviewAnalysisPage(f SessionOverviewFilter) (int, int) {
	limit, offset := f.Limit, f.Offset
	if limit <= 0 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// sessionOverviewCurrentIdentitySQL checks the same three visible source families
// as analysisForeignIdentity, including empty and foreign identities.
func sessionOverviewCurrentIdentitySQL(uid, email string) string {
	return `, current_sources AS (
		SELECT r.session_id,r.user_id,r.profile_email FROM visible_session_records r
		JOIN overview o ON o.session_id=r.session_id
		UNION ALL
		SELECT e.session_id,e.user_id,e.profile_email FROM visible_events e
		JOIN overview o ON o.session_id=e.session_id WHERE e.event_name='api_request'
		UNION ALL
		SELECT ss.session_id,ss.user_id,ss.profile_email FROM session_snapshots ss
		JOIN overview o ON o.session_id=ss.session_id
		WHERE ` + strings.ReplaceAll(sessionFactSnapshotVisibleSQL, "$1", "o.session_id") + `
	), current_identity AS (
		SELECT session_id,
			bool_or(CASE WHEN ` + uid + `<>'' THEN user_id=` + uid + ` ELSE profile_email=` + email + ` END) AS has_owner,
			bool_or(CASE WHEN ` + uid + `<>'' THEN user_id IS DISTINCT FROM ` + uid + `
				ELSE profile_email IS DISTINCT FROM ` + email + ` END) AS has_foreign
		FROM current_sources GROUP BY session_id
	)`
}

func escapeAnalysisLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}

func (s *PgStore) beginSessionOverviewAnalysis(ctx context.Context) (context.Context, context.CancelFunc, pgx.Tx, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout = '5s'`); err != nil {
		_ = tx.Rollback(ctx)
		cancel()
		return nil, nil, nil, err
	}
	return ctx, cancel, tx, nil
}

func (s *PgStore) listSessionOverviewAnalysis(ctx context.Context, f SessionOverviewFilter) ([]*SessionOverview, error) {
	if f.Classification != "" && !validPersonalClass(f.Classification) {
		return nil, ErrPersonalInvalid
	}
	ctx, cancel, tx, err := s.beginSessionOverviewAnalysis(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer tx.Rollback(ctx) //nolint:errcheck
	base, args, err := s.sessionOverviewAnalysisPopulation(ctx, tx, f)
	if err != nil {
		return nil, err
	}
	if base == "" {
		return []*SessionOverview{}, tx.Commit(ctx)
	}
	query, args := sessionOverviewAnalysisQuery(base, args, f, false, false)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []*SessionOverview{}
	for rows.Next() {
		o := &SessionOverview{}
		var analysis []byte
		if err := rows.Scan(&o.SessionID, &o.ProfileEmail, &o.UserID, &o.LoginEmail, &o.AccountCount,
			&o.Model, &o.Agent, &o.StartTime, &o.EndTime, &o.InputTokens, &o.OutputTokens,
			&o.CostUSD, &o.EventCount, &o.HasSync, &o.ProjectHash, &o.ProjectName, &o.Entrypoint,
			&o.HasEnriched, &o.CctraceVersion, &o.ClaudeVersion, &o.LoginEmailInferred, &analysis); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(analysis, &o.Analysis); err != nil {
			return nil, err
		}
		result = append(result, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	ids := make([]string, 0, len(result))
	for _, row := range result {
		ids = append(ids, row.SessionID)
	}
	values, err := personalSessionOverviewValuesTx(ctx, tx, f.DashboardUserID, f.PersonalEmail, f.PersonalUserID, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range result {
		if personal, ok := values[row.SessionID]; ok {
			classification := personal.Classification
			row.Classification = &classification
			row.FirstLine = personal.FirstLine
		}
	}
	if f.IncludeSessionRecords {
		if err := addSessionRecordOverview(ctx, tx, f, result); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *PgStore) countSessionOverviewAnalysis(ctx context.Context, f SessionOverviewFilter) (int, error) {
	if f.Classification != "" && !validPersonalClass(f.Classification) {
		return 0, ErrPersonalInvalid
	}
	ctx, cancel, tx, err := s.beginSessionOverviewAnalysis(ctx)
	if err != nil {
		return 0, err
	}
	defer cancel()
	defer tx.Rollback(ctx) //nolint:errcheck
	base, args, err := s.sessionOverviewAnalysisPopulation(ctx, tx, f)
	if err != nil {
		return 0, err
	}
	if base == "" {
		return 0, tx.Commit(ctx)
	}
	query, args := sessionOverviewAnalysisQuery(base, args, f, true, false)
	var n int
	if err = tx.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}

// SessionOverviewAnalysisCountWithClassification reads the HTTP count response
// against one ready marker and one repeatable-read source frame.
func (s *PgStore) SessionOverviewAnalysisCountWithClassification(ctx context.Context, f SessionOverviewFilter, includeUnattributed bool) (int, map[string]int, int, error) {
	if f.Classification != "" && !validPersonalClass(f.Classification) {
		return 0, nil, 0, ErrPersonalInvalid
	}
	ctx, cancel, tx, err := s.beginSessionOverviewAnalysis(ctx)
	if err != nil {
		return 0, nil, 0, err
	}
	defer cancel()
	defer tx.Rollback(ctx) //nolint:errcheck
	base, args, err := s.sessionOverviewAnalysisPopulation(ctx, tx, f)
	if err != nil {
		return 0, nil, 0, err
	}
	count := 0
	if base != "" {
		query, queryArgs := sessionOverviewAnalysisQuery(base, args, f, true, false)
		if err := tx.QueryRow(ctx, query, queryArgs...).Scan(&count); err != nil {
			return 0, nil, 0, err
		}
	}
	var classes map[string]int
	if f.DashboardUserID != 0 && (f.PersonalEmail != "" || f.PersonalUserID != "") {
		classes = map[string]int{"unsorted": 0, "consulting": 0, "working": 0, "later": 0, "done": 0}
		bucketFilter := f
		bucketFilter.Classification = ""
		var query string
		var queryArgs []interface{}
		rollup := false
		if f.Since == nil && f.Until == nil {
			rollup, err = sessionOverviewAnalysisRollupsReady(ctx, tx)
			if err != nil {
				return 0, nil, 0, err
			}
		}
		if rollup {
			query, queryArgs = sessionOverviewRollupPersonalQuery(bucketFilter, true)
		} else {
			query, queryArgs = personalPeriodBucketsQuery(bucketFilter)
		}
		rows, err := tx.Query(ctx, query, queryArgs...)
		if err != nil {
			return 0, nil, 0, err
		}
		for rows.Next() {
			var name string
			var n int
			if err := rows.Scan(&name, &n); err != nil {
				rows.Close()
				return 0, nil, 0, err
			}
			classes[name] = n
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, nil, 0, err
		}
		rows.Close()
	}
	unattributed := 0
	if includeUnattributed && f.LoginEmail != "" {
		unattributedFilter := SessionOverviewFilter{
			ProfileEmail: f.ProfileEmail, LoginEmail: f.LoginEmail, UserID: f.UserID,
			Since: f.Since, Until: f.Until, FoldLineage: f.FoldLineage, Source: f.Source,
			ProjectHashes: f.ProjectHashes, Agent: f.Agent, OnlyUnattributed: true,
		}
		rollup := false
		if f.Since == nil && f.Until == nil {
			rollup, err = sessionOverviewAnalysisRollupsReady(ctx, tx)
			if err != nil {
				return 0, nil, 0, err
			}
		}
		var query string
		var queryArgs []interface{}
		if rollup {
			query, queryArgs = sessionOverviewRollupCountQuery(unattributedFilter)
		} else {
			query, queryArgs = sessionOverviewPeriodCountQuery(unattributedFilter, false)
		}
		if query != "" {
			if err := tx.QueryRow(ctx, query, queryArgs...).Scan(&unattributed); err != nil {
				return 0, nil, 0, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, nil, 0, err
	}
	return count, classes, unattributed, nil
}

// SessionOverviewJudgementBucketCounts applies the same analysis population and
// filters as the list, then classifies every matching owner row before pagination.
func (s *PgStore) SessionOverviewJudgementBucketCounts(ctx context.Context, f SessionOverviewFilter) (map[string]int, error) {
	counts := map[string]int{"done": 0, "leftover": 0, "ended_midway": 0, "unknown": 0}
	if !f.AnalysisOwner || !f.IncludeSessionRecords || f.DashboardUserID == 0 || f.PersonalUserID == "" && f.PersonalEmail == "" {
		return counts, nil
	}
	if f.Classification != "" && !validPersonalClass(f.Classification) {
		return nil, ErrPersonalInvalid
	}
	ctx, cancel, tx, err := s.beginSessionOverviewAnalysis(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer tx.Rollback(ctx) //nolint:errcheck
	base, args, err := s.sessionOverviewAnalysisPopulation(ctx, tx, f)
	if err != nil {
		return nil, err
	}
	if base == "" {
		return counts, tx.Commit(ctx)
	}
	query, args := sessionOverviewAnalysisQuery(base, args, f, false, false,
		sessionOverviewAnalysisQueryOptions{judgementBuckets: true})
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var bucket string
		var count int
		if err := rows.Scan(&bucket, &count); err != nil {
			rows.Close()
			return nil, err
		}
		if _, ok := counts[bucket]; !ok {
			rows.Close()
			return nil, fmt.Errorf("unexpected judgement bucket %q", bucket)
		}
		counts[bucket] = count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return counts, tx.Commit(ctx)
}

func (s *PgStore) SessionOverviewCategoryCounts(ctx context.Context, f SessionOverviewFilter) (map[string]int, error) {
	if f.Classification != "" && !validPersonalClass(f.Classification) {
		return nil, ErrPersonalInvalid
	}
	counts := make(map[string]int, len(sessionAnalysisCategories))
	for _, name := range sessionAnalysisCategories {
		counts[name] = 0
	}
	if !f.AnalysisOwner || f.DashboardUserID == 0 || f.PersonalUserID == "" && f.PersonalEmail == "" {
		return counts, nil
	}
	ctx, cancel, tx, err := s.beginSessionOverviewAnalysis(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer tx.Rollback(ctx) //nolint:errcheck
	base, args, err := s.sessionOverviewAnalysisPopulation(ctx, tx, f)
	if err != nil {
		return nil, err
	}
	if base == "" {
		return counts, tx.Commit(ctx)
	}
	query, args := sessionOverviewAnalysisQuery(base, args, f, false, true)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return nil, err
		}
		counts[key] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return counts, tx.Commit(ctx)
}

func sessionOverviewAnalysisRollupsReady(ctx context.Context, tx pgx.Tx) (bool, error) {
	var ready bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name=$1)`, sessionOverviewRollupBackfill).Scan(&ready)
	return ready, err
}

func (s *PgStore) sessionOverviewAnalysisPopulation(ctx context.Context, tx pgx.Tx, f SessionOverviewFilter) (string, []interface{}, error) {
	if f.Since == nil && f.Until == nil {
		ready, err := sessionOverviewAnalysisRollupsReady(ctx, tx)
		if err != nil {
			return "", nil, err
		}
		if ready {
			base, args := sessionOverviewRollupListQuery(f)
			return base, args, nil
		}
	}
	base, args, ok := sessionOverviewPeriodListQuery(f)
	if !ok {
		return "", nil, nil
	}
	return base, args, nil
}

// The batch category query is filled from one owner-scoped candidate set. It
// never performs one SQL round trip per session.
func sessionOverviewAnalysisActivitySQL(uid, email string) string {
	return `, live_ranked AS (
		SELECT l.session_id,l.status,l.waiting_kind,l.heartbeat_at,l.status_changed_at
		FROM session_live_states l JOIN joined j ON j.session_id=l.session_id
		WHERE l.identity_provenance='personal_token' AND ((` + uid + `<>'' AND l.user_id=` + uid + `)
			OR (` + uid + `='' AND lower(l.profile_email)=lower(` + email + `)))
	), live_summary AS (
		SELECT l.session_id,true AS anyrow,
			bool_and(l.status='gone') AS allgone,
			count(*) FILTER (WHERE l.status<>'gone' AND l.heartbeat_at>=now()-interval '3 minutes'
				AND l.heartbeat_at<=now()+interval '1 minute') AS fresh_count,
			count(*) FILTER (WHERE l.status<>'gone' AND l.heartbeat_at>=now()-interval '3 minutes'
				AND l.heartbeat_at<=now()+interval '1 minute' AND l.status_changed_at<now()-interval '6 hours'
				AND j.last_activity_at<now()-interval '6 hours' AND (l.status IN ('busy','waiting') OR l.waiting_kind<>'')) AS stale_count
		FROM live_ranked l JOIN joined j USING(session_id) GROUP BY l.session_id
	), alive AS (
		SELECT DISTINCT ON (l.session_id) l.session_id,l.status,l.waiting_kind
		FROM live_ranked l JOIN joined j USING(session_id)
		WHERE l.status<>'gone' AND l.heartbeat_at>=now()-interval '3 minutes'
			AND l.heartbeat_at<=now()+interval '1 minute'
			AND NOT COALESCE((l.status IN ('busy','waiting') OR l.waiting_kind<>'')
				AND l.status_changed_at<now()-interval '6 hours' AND j.last_activity_at<now()-interval '6 hours',false)
		ORDER BY l.session_id,l.heartbeat_at DESC
	), endings AS (
		SELECT e.session_id,max(e.observed_at) FILTER (WHERE e.kind='session_end') AS end_at,
			max(e.observed_at) FILTER (WHERE e.kind='gone') AS gone_at
		FROM session_observation_events e JOIN joined j ON j.session_id=e.session_id
		WHERE e.identity_provenance='personal_token' AND e.kind IN ('session_end','gone')
			AND ((` + uid + `<>'' AND e.user_id=` + uid + `) OR (` + uid + `='' AND lower(e.profile_email)=lower(` + email + `)))
		GROUP BY e.session_id
	), heartbeat AS (
		SELECT max(h.heartbeat_at) AS at FROM collector_heartbeats h
		WHERE h.user_id=` + uid + ` AND lower(h.profile_email)=lower(` + email + `)
	), activity AS (
		SELECT j.session_id,CASE
			WHEN a.status='waiting' OR a.waiting_kind<>'' THEN 'waiting'
			WHEN a.status='busy' THEN 'running'
			WHEN ls.anyrow IS NULL AND j.last_turn_status='open'
				AND j.last_activity_at>=now()-interval '5 minutes' THEN 'running'
			WHEN a.session_id IS NULL AND GREATEST(e.end_at,CASE WHEN ls.allgone THEN e.gone_at END)>j.last_activity_at THEN 'ended'
			WHEN ls.fresh_count>0 AND ls.fresh_count=ls.stale_count THEN 'stale_live'
			WHEN j.open_questions>0 AND j.last_turn_key<>'' AND j.open_question_turn_key=j.last_turn_key THEN 'open_question'
			WHEN a.status IN ('idle','shell') THEN 'idle'
			WHEN j.last_activity_at>=now()-interval '24 hours' AND hb.at<now()-interval '3 minutes' THEN 'collector_delayed'
			WHEN j.last_activity_at>=now()-interval '24 hours' THEN 'recent'
			ELSE 'inactive' END AS category
		FROM joined j LEFT JOIN live_summary ls ON ls.session_id=j.session_id
		LEFT JOIN alive a ON a.session_id=j.session_id
		LEFT JOIN endings e ON e.session_id=j.session_id CROSS JOIN heartbeat hb
		WHERE j.analysis_state IN ('ready','stale')
	)`
}
