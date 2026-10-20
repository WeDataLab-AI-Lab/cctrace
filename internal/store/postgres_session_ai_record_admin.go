package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"cctrace/internal/sessionrecord"
)

// SessionAIRecordAdminMetrics contains aggregate operating data only. It never
// returns session ids, record text, owners, or search matches.
func (s *PgStore) SessionAIRecordAdminMetrics(ctx context.Context, runtimeKey, disclosure string, now time.Time, timezone string) (map[string]interface{}, error) {
	settings, err := s.GetSessionAIRecordSettings(ctx)
	if err != nil {
		return nil, err
	}
	loc := time.UTC
	if timezone != "" {
		if loaded, e := time.LoadLocation(timezone); e == nil {
			loc = loaded
		}
	}
	day := now.In(loc).Format("2006-01-02")
	capTokens := int64(1_000_000)
	if settings.DailyTokenCap != nil {
		capTokens = *settings.DailyTokenCap
	}
	var users, current int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE u.is_active),count(*) FILTER(WHERE u.is_active AND c.dashboard_user_id IS NOT NULL)
		FROM dashboard_users u LEFT JOIN session_ai_record_consents c ON c.dashboard_user_id=u.id AND c.runtime_key=$1 AND c.disclosure_version=$2`, runtimeKey, disclosure).Scan(&users, &current); err != nil {
		return nil, err
	}
	var used, input, cached, output, estimated, unreported int64
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(input_tokens+output_tokens),0),COALESCE(sum(input_tokens),0),
		COALESCE(sum(cached_input_tokens),0),COALESCE(sum(output_tokens),0),COALESCE(sum(estimated_cap_tokens),0),COALESCE(sum(unreported_calls),0)
		FROM session_ai_record_usage WHERE day=$1::date`, day).Scan(&used, &input, &cached, &output, &estimated, &unreported); err != nil {
		return nil, err
	}
	var reserved int64
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(reserved_tokens),0) FROM session_ai_record_budget_reservations WHERE day=$1::date`, day).Scan(&reserved); err != nil {
		return nil, err
	}
	byLane := map[string]interface{}{"live": map[string]interface{}{"sessions": int64(0), "calls": int64(0), "input_tokens": int64(0), "cached_input_tokens": int64(0), "output_tokens": int64(0)},
		"backfill": map[string]interface{}{"sessions": int64(0), "calls": int64(0), "input_tokens": int64(0), "cached_input_tokens": int64(0), "output_tokens": int64(0)},
		"manual":   map[string]interface{}{"sessions": int64(0), "calls": int64(0), "input_tokens": int64(0), "cached_input_tokens": int64(0), "output_tokens": int64(0)}}
	rows, err := s.pool.Query(ctx, `SELECT lane,COALESCE(sum(sessions),0),COALESCE(sum(calls),0),COALESCE(sum(input_tokens),0),
		COALESCE(sum(cached_input_tokens),0),COALESCE(sum(output_tokens),0) FROM session_ai_record_usage WHERE day=$1::date GROUP BY lane`, day)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var lane string
		var sessions, calls, in, cache, out int64
		if err := rows.Scan(&lane, &sessions, &calls, &in, &cache, &out); err != nil {
			rows.Close()
			return nil, err
		}
		byLane[lane] = map[string]interface{}{"sessions": sessions, "calls": calls, "input_tokens": in, "cached_input_tokens": cache, "output_tokens": out}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	last7 := []map[string]interface{}{}
	rows, err = s.pool.Query(ctx, `SELECT day::text,COALESCE(sum(sessions),0),COALESCE(sum(calls),0),
		COALESCE(sum(input_tokens+output_tokens),0),COALESCE(sum(sessions) FILTER(WHERE outcome='noop'),0),
		COALESCE(sum(sessions) FILTER(WHERE outcome LIKE 'rejected:%'),0),COALESCE(sum(sessions) FILTER(WHERE outcome LIKE 'error:%'),0)
		FROM session_ai_record_usage WHERE day >= $1::date-6 AND day <= $1::date GROUP BY day ORDER BY day`, day)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var date string
		var sessions, calls, tokens, noop, rejected, errors int64
		if err := rows.Scan(&date, &sessions, &calls, &tokens, &noop, &rejected, &errors); err != nil {
			rows.Close()
			return nil, err
		}
		last7 = append(last7, map[string]interface{}{"day": date, "sessions": sessions, "calls": calls, "tokens": tokens, "noop": noop, "rejected": rejected, "errors": errors})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	var failed int
	byCode := map[string]int{}
	rows, err = s.pool.Query(ctx, `SELECT last_error,count(*) FROM session_ai_record_jobs WHERE attempts>0 GROUP BY last_error ORDER BY last_error`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var code string
		var n int
		if err := rows.Scan(&code, &n); err != nil {
			rows.Close()
			return nil, err
		}
		failed += n
		byCode[code] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	var requested int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_ai_record_jobs WHERE requested_at IS NOT NULL`).Scan(&requested); err != nil {
		return nil, err
	}
	var due, waitingQuiet, backfillRemaining int
	backfillDays := 30
	if settings.BackfillDays != nil {
		backfillDays = *settings.BackfillDays
	}
	if backfillDays < 0 {
		backfillDays = 30
	}
	var since pgtype.Timestamptz
	if settings.BackfillSince != nil {
		since = pgtype.Timestamptz{Time: *settings.BackfillSince, Valid: true}
	}
	query := `WITH eligible AS (
		SELECT f.session_id,f.last_activity_at,j.claim,j.requested_at,
			CASE WHEN EXISTS(SELECT 1 FROM session_observation_events e WHERE e.session_id=f.session_id AND e.kind IN ('session_end','gone') AND e.observed_at>=f.last_activity_at) THEN interval '3 minutes'
				WHEN f.last_turn_status='open' THEN interval '6 hours' ELSE interval '30 minutes' END AS quiet
		FROM session_analysis_facts f LEFT JOIN session_ai_records r ON r.session_id=f.session_id LEFT JOIN session_ai_record_jobs j ON j.session_id=f.session_id
		WHERE f.agent IN ('claude','codex') AND f.coverage='full' AND f.turn_boundaries='observed' AND COALESCE(f.requests,0)>0
		AND (r.session_id IS NULL OR r.facts_hash<>f.output_hash OR r.prompt_version<>$3)
		AND (r.session_id IS NULL OR r.generated_at<=$4::timestamptz-interval '60 minutes')
		AND (j.session_id IS NULL OR (j.claim IS NULL AND j.requested_at IS NULL))
		AND EXISTS(SELECT 1 FROM session_ai_record_consents c JOIN dashboard_users u ON u.id=c.dashboard_user_id AND u.is_active
			WHERE c.runtime_key=$1 AND c.disclosure_version=$2 AND
				((f.owner_user_id IS NOT NULL AND u.cctrace_user_id=f.owner_user_id) OR (f.owner_profile_email IS NOT NULL AND lower(u.email)=lower(f.owner_profile_email))))
		AND NOT EXISTS(SELECT 1 FROM excluded_sessions x WHERE x.session_id=f.session_id)
		AND NOT EXISTS(SELECT 1 FROM jsonb_to_recordset(f.account_keys) AS k(login_email text,billing_provider text,account_id text) WHERE NOT (` + excludedAccountPredicateSQL("k") + `))
		AND NOT EXISTS(SELECT 1 FROM privacy_settings p WHERE (p.user_id=f.owner_user_id OR lower(p.profile_email)=lower(f.owner_profile_email))
			AND (p.scope_type='user' OR (p.scope_type='session' AND p.scope_value=f.session_id) OR (p.scope_type='project' AND p.scope_value=f.project_hash)))
		AND NOT EXISTS(SELECT 1 FROM visible_session_records sr WHERE sr.session_id=f.session_id AND
			(sr.user_id IS DISTINCT FROM f.owner_user_id OR lower(sr.profile_email)<>lower(f.owner_profile_email)))
		AND NOT EXISTS(SELECT 1 FROM visible_events e WHERE e.session_id=f.session_id AND e.event_name='api_request' AND
			(e.user_id IS DISTINCT FROM f.owner_user_id OR lower(e.profile_email)<>lower(f.owner_profile_email)))
		AND NOT EXISTS(SELECT 1 FROM session_snapshots n WHERE n.session_id=f.session_id AND
			(n.user_id IS DISTINCT FROM f.owner_user_id OR lower(n.profile_email)<>lower(f.owner_profile_email)))
	)
	SELECT count(*) FILTER(WHERE last_activity_at >= $4::timestamptz-interval '72 hours' AND last_activity_at <= $4::timestamptz-quiet),
		count(*) FILTER(WHERE last_activity_at >= $4::timestamptz-interval '72 hours' AND last_activity_at > $4::timestamptz-quiet),
		count(*) FILTER(WHERE last_activity_at < $4::timestamptz-interval '72 hours'
			AND $5::boolean AND ($6::int=0 OR last_activity_at >= $4::timestamptz-make_interval(days=>$6))
			AND ($7::timestamptz IS NULL OR last_activity_at >= $7::timestamptz)
			AND last_activity_at <= $4::timestamptz-quiet)
	FROM eligible`
	if err := s.pool.QueryRow(ctx, query, runtimeKey, disclosure, sessionrecord.PromptVersion, now, settings.BackfillEnabled, backfillDays, since).Scan(&due, &waitingQuiet, &backfillRemaining); err != nil {
		return nil, fmt.Errorf("session record admin backlog: %w", err)
	}
	var dropped, uncovered, newTerms, redacted int64
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(dropped_items),0),COALESCE(sum(uncovered_requests),0),COALESCE(sum(new_terms),0),
		COALESCE(sum(sessions) FILTER(WHERE outcome='redacted'),0) FROM session_ai_record_usage WHERE day >= $1::date-6 AND day <= $1::date`, day).
		Scan(&dropped, &uncovered, &newTerms, &redacted); err != nil {
		return nil, err
	}
	var backfillUsed int64
	var backfillSince interface{}
	if settings.BackfillSince != nil {
		backfillSince = *settings.BackfillSince
	}
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(input_tokens+output_tokens+estimated_cap_tokens),0) FROM session_ai_record_usage
		WHERE lane='backfill' AND ($1::timestamptz IS NULL OR day >= $1::timestamptz::date)`, backfillSince).Scan(&backfillUsed); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"flag": false, "enabled": settings.Enabled, "prompt_version": sessionrecord.PromptVersion,
		"runtime": strings.SplitN(runtimeKey, ":", 2)[0], "runtime_key": runtimeKey,
		"consents": map[string]int{"users": users, "current": current},
		"today": map[string]interface{}{"day": day, "tz": loc.String(), "cap_tokens": capTokens, "used_tokens": used, "reserved_tokens": reserved,
			"estimated_cap_tokens": estimated, "by_lane": byLane, "unreported_calls": unreported, "input_tokens": input, "cached_input_tokens": cached, "output_tokens": output},
		"last_7_days": last7, "backlog": map[string]interface{}{"due": due, "waiting_quiet": waitingQuiet, "backfill_remaining": backfillRemaining, "requested": requested},
		"backfill":   map[string]interface{}{"enabled": settings.BackfillEnabled, "days": backfillDays, "token_budget": settings.BackfillTokenBudget, "used_tokens": backfillUsed, "since": settings.BackfillSince},
		"failures":   map[string]interface{}{"sessions": failed, "by_code": byCode},
		"quality_7d": map[string]int64{"dropped_items": dropped, "uncovered_requests": uncovered, "new_terms": newTerms, "redacted": redacted},
	}, nil
}
