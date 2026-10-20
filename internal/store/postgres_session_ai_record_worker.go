package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"cctrace/internal/sessionrecord"
)

func (s *PgStore) SessionRecordSettings(ctx context.Context) (sessionrecord.WorkerSettings, error) {
	settings, err := s.GetSessionAIRecordSettings(ctx)
	if err != nil {
		return sessionrecord.WorkerSettings{}, err
	}
	return sessionrecord.WorkerSettings{Enabled: settings.Enabled, Runtime: settings.Runtime,
		DailyTokenCap: settings.DailyTokenCap, BackfillEnabled: settings.BackfillEnabled,
		BackfillDays: settings.BackfillDays, BackfillTokenBudget: settings.BackfillTokenBudget,
		BackfillSince: settings.BackfillSince}, nil
}

func (s *PgStore) ConsentingSessionRecordOwners(ctx context.Context, runtimeKey, disclosure string, useUserID bool) ([]sessionrecord.Owner, error) {
	rows, err := s.pool.Query(ctx, `SELECT u.id,u.cctrace_user_id,u.email
		FROM session_ai_record_consents c JOIN dashboard_users u ON u.id=c.dashboard_user_id
		WHERE u.is_active AND c.runtime_key=$1 AND c.disclosure_version=$2
		AND (($3::boolean AND NULLIF(u.cctrace_user_id,'') IS NOT NULL)
			OR (NOT $3::boolean AND NULLIF(u.email,'') IS NOT NULL))
		AND NOT EXISTS (SELECT 1 FROM privacy_settings p
			WHERE p.scope_type='user' AND ((u.cctrace_user_id<>'' AND p.user_id=u.cctrace_user_id)
				OR lower(p.profile_email)=lower(u.email)))
		ORDER BY u.id`, runtimeKey, disclosure, useUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := make([]sessionrecord.Owner, 0)
	for rows.Next() {
		var owner sessionrecord.Owner
		if err := rows.Scan(&owner.DashboardUserID, &owner.UserID, &owner.ProfileEmail); err != nil {
			return nil, err
		}
		owner.UseUserIDAccessControl = useUserID
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}

type recordCandidate struct {
	SessionID        string
	OutputHash       []byte
	Force            bool
	RangeRequest     bool
	RangeRequestedAt *time.Time
}

func (s *PgStore) ClaimNextSessionRecordJob(ctx context.Context, owner sessionrecord.Owner, lane string, settings sessionrecord.WorkerSettings, now time.Time) (sessionrecord.Job, bool, error) {
	useUserID := owner.UseUserIDAccessControl
	userID, email := owner.UserID, owner.ProfileEmail
	args := []any{userID, email, useUserID, now, sessionrecord.PromptVersion, owner.DashboardUserID}
	var candidate recordCandidate
	var err error
	switch lane {
	case "manual":
		candidate, err = s.pickManualSessionRecord(ctx, args)
		if err != nil {
			return sessionrecord.Job{}, false, err
		}
	case "live", "backfill":
		candidate, err = s.pickAutomaticSessionRecord(ctx, lane, args, settings)
		if err != nil {
			return sessionrecord.Job{}, false, err
		}
	default:
		return sessionrecord.Job{}, false, fmt.Errorf("unknown session record lane %q", lane)
	}
	if candidate.SessionID == "" {
		return sessionrecord.Job{}, false, nil
	}
	return s.claimSessionRecordCandidate(ctx, owner, lane, candidate)
}

func (s *PgStore) pickManualSessionRecord(ctx context.Context, args []any) (recordCandidate, error) {
	const explicit = `SELECT f.session_id,f.output_hash,j.force,
		CASE WHEN j.force THEN false ELSE EXISTS(SELECT 1 FROM session_ai_record_range_requests rr
			WHERE rr.dashboard_user_id=j.requested_by AND rr.requested_at=j.requested_at AND rr.finished_at IS NULL) END,
		(SELECT rr.requested_at FROM session_ai_record_range_requests rr
			WHERE rr.dashboard_user_id=j.requested_by AND rr.requested_at=j.requested_at AND rr.finished_at IS NULL LIMIT 1)
		FROM session_analysis_facts f JOIN session_ai_record_jobs j ON j.session_id=f.session_id
		WHERE ` + sessionRecordOwnerScopeSQL + `
		AND j.requested_by=$6 AND j.requested_at IS NOT NULL AND j.claim IS NULL
		AND $5::int>0
		AND (j.retry_at IS NULL OR j.retry_at <= $4 OR f.computed_at > j.updated_at)
		AND ($3::boolean OR (j.force OR NOT EXISTS (SELECT 1 FROM session_ai_records r
			WHERE r.session_id=f.session_id AND r.generated_at>=$4-interval '10 minutes')))
		AND ` + sessionRecordCandidatePredicateSQL + `
		ORDER BY j.requested_at,f.last_activity_at DESC LIMIT 1`
	candidate, found, err := scanRecordCandidate(ctx, s.pool.QueryRow(ctx, explicit, args...))
	if err != nil || found {
		return candidate, err
	}
	const ranged = `SELECT f.session_id,f.output_hash,false,true,rr.requested_at
		FROM session_ai_record_range_requests rr JOIN session_analysis_facts f ON TRUE
		LEFT JOIN session_ai_records r ON r.session_id=f.session_id
		LEFT JOIN session_ai_record_jobs j ON j.session_id=f.session_id
		WHERE rr.dashboard_user_id=$6 AND rr.finished_at IS NULL AND rr.served<50
		AND f.last_activity_at >= rr.range_from AND f.last_activity_at <= rr.range_to
		AND (r.session_id IS NULL OR r.facts_hash<>f.output_hash OR r.prompt_version<>$5)
		AND (j.session_id IS NULL OR (j.claim IS NULL AND (j.retry_at IS NULL OR j.retry_at<=$4 OR f.computed_at>j.updated_at)))
		AND ` + sessionRecordOwnerScopeSQL + `
		AND ` + sessionRecordCandidatePredicateSQL + `
		ORDER BY f.last_activity_at DESC LIMIT 1`
	candidate, found, err = scanRecordCandidate(ctx, s.pool.QueryRow(ctx, ranged, args...))
	if err != nil {
		return recordCandidate{}, err
	}
	if !found {
		_, err = s.pool.Exec(ctx, `UPDATE session_ai_record_range_requests SET finished_at=$2
			WHERE dashboard_user_id=$1 AND finished_at IS NULL AND served<50`, ownerID(args), args[3])
	}
	return candidate, err
}

func ownerID(args []any) int64 {
	if len(args) < 6 {
		return 0
	}
	id, _ := args[5].(int64)
	return id
}

const sessionRecordOwnerScopeSQL = `(($3::boolean AND NULLIF($1::text,'') IS NOT NULL AND f.owner_user_id=$1)
	OR (NOT $3::boolean AND NULLIF($2::text,'') IS NOT NULL AND lower(f.owner_profile_email)=lower($2)))`

const sessionRecordCandidatePredicateSQL = `
	f.agent IN ('claude','codex') AND f.coverage='full' AND f.turn_boundaries='observed'
	AND COALESCE(f.requests,0)>0 AND NOT EXISTS(SELECT 1 FROM excluded_sessions x WHERE x.session_id=f.session_id)
	AND NOT EXISTS (SELECT 1 FROM jsonb_to_recordset(f.account_keys) AS k(login_email text,billing_provider text,account_id text)
		WHERE NOT (` + `NOT EXISTS (SELECT 1 FROM excluded_accounts x WHERE lower(x.login_email)=lower(k.login_email))
		AND NOT EXISTS (SELECT 1 FROM excluded_billing_accounts b WHERE b.billing_provider=k.billing_provider AND b.account_id=k.account_id)
		AND NOT EXISTS (SELECT 1 FROM excluded_billing_links l WHERE l.billing_provider=k.billing_provider AND l.account_id=k.account_id))` + `)
	AND NOT EXISTS (SELECT 1 FROM privacy_settings p WHERE (p.user_id=$1 OR lower(p.profile_email)=lower($2))
		AND (p.scope_type='user' OR (p.scope_type='session' AND p.scope_value=f.session_id)
			OR (p.scope_type='project' AND p.scope_value=f.project_hash)))
	AND NOT EXISTS (SELECT 1 FROM visible_session_records sr WHERE sr.session_id=f.session_id
		AND (($3::boolean AND sr.user_id IS DISTINCT FROM $1) OR (NOT $3::boolean AND lower(sr.profile_email)<>lower($2))))
	AND NOT EXISTS (SELECT 1 FROM visible_events e WHERE e.session_id=f.session_id
		AND (($3::boolean AND e.user_id IS DISTINCT FROM $1) OR (NOT $3::boolean AND lower(e.profile_email)<>lower($2))))
	AND NOT EXISTS (SELECT 1 FROM session_snapshots n WHERE n.session_id=f.session_id
		AND (($3::boolean AND n.user_id IS DISTINCT FROM $1) OR (NOT $3::boolean AND lower(n.profile_email)<>lower($2))))`

func (s *PgStore) pickAutomaticSessionRecord(ctx context.Context, lane string, args []any, settings sessionrecord.WorkerSettings) (recordCandidate, error) {
	var query string
	queryArgs := args[:5]
	if lane == "live" {
		query = `SELECT f.session_id,f.output_hash,false,false,NULL::timestamptz
			FROM session_analysis_facts f LEFT JOIN session_ai_records r ON r.session_id=f.session_id
			LEFT JOIN session_ai_record_jobs j ON j.session_id=f.session_id
		WHERE ` + sessionRecordOwnerScopeSQL + ` AND f.last_activity_at >= $4::timestamptz-interval '72 hours'
			AND (r.session_id IS NULL OR r.facts_hash<>f.output_hash OR r.prompt_version<>$5)
			AND (r.session_id IS NULL OR r.generated_at<=$4::timestamptz-interval '60 minutes')
			AND (j.session_id IS NULL OR (j.claim IS NULL AND j.requested_at IS NULL AND
				(j.retry_at IS NULL OR j.retry_at<=$4 OR f.computed_at>j.updated_at)))
			AND f.last_activity_at <= $4::timestamptz-CASE
				WHEN EXISTS(SELECT 1 FROM session_observation_events e WHERE e.session_id=f.session_id
					AND e.kind IN ('session_end','gone') AND e.observed_at>=f.last_activity_at) THEN interval '3 minutes'
				WHEN f.last_turn_status='open' THEN interval '6 hours' ELSE interval '30 minutes' END
			AND ` + sessionRecordCandidatePredicateSQL + `
			ORDER BY f.last_activity_at DESC LIMIT 1`
	} else {
		days := 30
		if settings.BackfillDays != nil {
			days = *settings.BackfillDays
		}
		if days < 0 {
			days = 30
		}
		window := time.Time{}
		if days > 0 {
			window = args[3].(time.Time).AddDate(0, 0, -days)
		}
		query = `SELECT f.session_id,f.output_hash,false,false,NULL::timestamptz
			FROM session_analysis_facts f LEFT JOIN session_ai_records r ON r.session_id=f.session_id
			LEFT JOIN session_ai_record_jobs j ON j.session_id=f.session_id
			WHERE ` + sessionRecordOwnerScopeSQL + ` AND f.last_activity_at >= $6
			AND (r.session_id IS NULL OR r.facts_hash<>f.output_hash OR r.prompt_version<>$5)
			AND (r.session_id IS NULL OR r.generated_at<=$4::timestamptz-interval '60 minutes')
			AND (j.session_id IS NULL OR (j.claim IS NULL AND j.requested_at IS NULL AND
				(j.retry_at IS NULL OR j.retry_at<=$4 OR f.computed_at>j.updated_at)))
			AND f.last_activity_at <= $4::timestamptz-CASE
				WHEN EXISTS(SELECT 1 FROM session_observation_events e WHERE e.session_id=f.session_id
					AND e.kind IN ('session_end','gone') AND e.observed_at>=f.last_activity_at) THEN interval '3 minutes'
				WHEN f.last_turn_status='open' THEN interval '6 hours' ELSE interval '30 minutes' END
			AND ` + sessionRecordCandidatePredicateSQL + `
			ORDER BY f.last_activity_at DESC LIMIT 1`
		queryArgs = append(queryArgs, window)
	}
	candidate, found, err := scanRecordCandidate(ctx, s.pool.QueryRow(ctx, query, queryArgs...))
	if err != nil || !found {
		return candidate, err
	}
	return candidate, nil
}

func scanRecordCandidate(ctx context.Context, row pgx.Row) (recordCandidate, bool, error) {
	var c recordCandidate
	err := row.Scan(&c.SessionID, &c.OutputHash, &c.Force, &c.RangeRequest, &c.RangeRequestedAt)
	if err == pgx.ErrNoRows {
		return recordCandidate{}, false, nil
	}
	if err != nil {
		return recordCandidate{}, false, err
	}
	return c, true, nil
}

func (s *PgStore) claimSessionRecordCandidate(ctx context.Context, owner sessionrecord.Owner, lane string, candidate recordCandidate) (sessionrecord.Job, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return sessionrecord.Job{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('session_overview_retention'))`); err != nil {
		return sessionrecord.Job{}, false, err
	}
	if err = lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return sessionrecord.Job{}, false, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('session_record_claim'),hashtext($1))`, candidate.SessionID); err != nil {
		return sessionrecord.Job{}, false, err
	}
	eligible := `EXISTS(SELECT 1 FROM session_analysis_facts f
		WHERE f.session_id=$1 AND f.output_hash=$6
		AND (($9::boolean AND NULLIF($7::text,'') IS NOT NULL AND f.owner_user_id=$7)
			OR (NOT $9::boolean AND NULLIF($8::text,'') IS NOT NULL AND lower(f.owner_profile_email)=lower($8)))
		AND f.agent IN ('claude','codex') AND f.coverage='full' AND f.turn_boundaries='observed' AND COALESCE(f.requests,0)>0
		AND ` + sessionAnalysisVisibleSQL + `
		AND NOT EXISTS(SELECT 1 FROM privacy_settings p WHERE (p.user_id=$7 OR lower(p.profile_email)=lower($8))
			AND (p.scope_type='user' OR (p.scope_type='session' AND p.scope_value=f.session_id)
				OR (p.scope_type='project' AND p.scope_value=f.project_hash)))
		AND NOT EXISTS(SELECT 1 FROM visible_session_records sr WHERE sr.session_id=f.session_id
			AND (($9::boolean AND sr.user_id IS DISTINCT FROM $7) OR (NOT $9::boolean AND lower(sr.profile_email)<>lower($8))))
		AND NOT EXISTS(SELECT 1 FROM visible_events e WHERE e.session_id=f.session_id
			AND (($9::boolean AND e.user_id IS DISTINCT FROM $7) OR (NOT $9::boolean AND lower(e.profile_email)<>lower($8))))
		AND NOT EXISTS(SELECT 1 FROM session_snapshots n WHERE n.session_id=f.session_id
			AND (($9::boolean AND n.user_id IS DISTINCT FROM $7) OR (NOT $9::boolean AND lower(n.profile_email)<>lower($8)))))`
	var claim int64
	var requestedAt *time.Time
	var requestedBy *int64
	var force bool
	err = tx.QueryRow(ctx, `INSERT INTO session_ai_record_jobs
		(session_id,lane,requested_at,requested_by,force,claim,claimed_at,updated_at)
		SELECT $1,$2,CASE WHEN $4::boolean THEN now() WHEN $5::boolean THEN $10::timestamptz ELSE NULL END,
			CASE WHEN ($4::boolean OR $5::boolean) THEN $3::bigint ELSE NULL END,$4,
			nextval('session_ai_record_claim'),now(),now()
		WHERE `+eligible+`
		ON CONFLICT(session_id) DO UPDATE SET lane=EXCLUDED.lane,
			requested_at=COALESCE(session_ai_record_jobs.requested_at,EXCLUDED.requested_at),
			requested_by=COALESCE(session_ai_record_jobs.requested_by,EXCLUDED.requested_by),
			force=session_ai_record_jobs.force OR EXCLUDED.force,claim=EXCLUDED.claim,
			claimed_at=now(),updated_at=now()
		WHERE session_ai_record_jobs.claim IS NULL
		RETURNING claim,requested_at,requested_by,force`,
		candidate.SessionID, lane, owner.DashboardUserID, candidate.Force, candidate.RangeRequest, candidate.OutputHash,
		owner.UserID, owner.ProfileEmail, owner.UseUserIDAccessControl, candidate.RangeRequestedAt).
		Scan(&claim, &requestedAt, &requestedBy, &force)
	if err != nil {
		if err == pgx.ErrNoRows {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return sessionrecord.Job{}, false, commitErr
			}
			return sessionrecord.Job{}, false, nil
		}
		return sessionrecord.Job{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sessionrecord.Job{}, false, err
	}
	return sessionrecord.Job{SessionID: candidate.SessionID, OutputHash: candidate.OutputHash, Owner: owner, Lane: lane,
		Claim: claim, Force: force, RangeRequest: candidate.RangeRequest, RangeRequestedAt: candidate.RangeRequestedAt,
		ClaimedAt: time.Now().UTC()}, true, nil
}

func (s *PgStore) BeginSessionRecordTransmission(ctx context.Context, job sessionrecord.Job, snapshot sessionrecord.Snapshot, runtimeKey, disclosure string, reserve int64, day string) (sessionrecord.TransmissionReservation, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return sessionrecord.TransmissionReservation{}, err
	}
	var retentionLocked, sourceLocked, sessionLocked bool
	var once sync.Once
	release := func() {
		once.Do(func() {
			unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if sessionLocked {
				_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtext('session_record_send'),hashtext($1))`, job.SessionID)
			}
			if sourceLocked {
				_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtext('session_overview_source'),0)`)
			}
			if retentionLocked {
				_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock_shared(hashtext('session_overview_retention'))`)
			}
			conn.Release()
		})
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock_shared(hashtext('session_overview_retention'))`); err != nil {
		release()
		return sessionrecord.TransmissionReservation{}, err
	}
	retentionLocked = true
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('session_overview_source'),0)`); err != nil {
		release()
		return sessionrecord.TransmissionReservation{}, err
	}
	sourceLocked = true
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('session_record_send'),hashtext($1))`, job.SessionID); err != nil {
		release()
		return sessionrecord.TransmissionReservation{}, err
	}
	sessionLocked = true
	readTx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		release()
		return sessionrecord.TransmissionReservation{}, err
	}
	current, err := s.readSessionRecordSnapshotTx(ctx, readTx, job)
	if err == nil && (!sameRecordHash(current.SourceInputHash, snapshot.SourceInputHash) ||
		!sameRecordHash(current.BasisHash, snapshot.BasisHash) || !sameRecordHash(current.FactsHash, snapshot.FactsHash) ||
		!sameRecordHash(current.RetentionState, snapshot.RetentionState)) {
		err = sessionrecord.ErrRecordSendNotAllowed
	}
	if err != nil {
		_ = readTx.Rollback(ctx)
		release()
		return sessionrecord.TransmissionReservation{}, err
	}
	if err := readTx.Commit(ctx); err != nil {
		release()
		return sessionrecord.TransmissionReservation{}, err
	}
	var enabled bool
	var recordRuntime, weeklyRuntime string
	var dailyCap, backfillBudget pgtype.Int8
	var backfillEnabled bool
	var backfillSince pgtype.Timestamptz
	if err := conn.QueryRow(ctx, `SELECT record_enabled,record_runtime,runtime,record_daily_token_cap,
		record_backfill_enabled,record_backfill_token_budget,record_backfill_since FROM ai_settings WHERE id=1`).
		Scan(&enabled, &recordRuntime, &weeklyRuntime, &dailyCap, &backfillEnabled, &backfillBudget, &backfillSince); err != nil {
		release()
		return sessionrecord.TransmissionReservation{}, err
	}
	providerRuntime := strings.SplitN(runtimeKey, ":", 2)[0]
	if !enabled || (recordRuntime != "" && recordRuntime != providerRuntime) ||
		(recordRuntime == "" && weeklyRuntime != "" && weeklyRuntime != providerRuntime) {
		release()
		return sessionrecord.TransmissionReservation{}, sessionrecord.ErrRecordSendNotAllowed
	}
	var savedRecordModel, savedRecordEffort, weeklyModel string
	if err := conn.QueryRow(ctx, `SELECT record_model,record_reasoning_effort,model FROM ai_runtime_settings WHERE runtime=$1`, providerRuntime).
		Scan(&savedRecordModel, &savedRecordEffort, &weeklyModel); err != nil && err != pgx.ErrNoRows {
		release()
		return sessionrecord.TransmissionReservation{}, err
	}
	if (savedRecordModel != "" && savedRecordModel != job.Model) ||
		(savedRecordModel == "" && weeklyModel != "" && weeklyModel != job.Model) ||
		(savedRecordEffort != "" && savedRecordEffort != job.ReasoningEffort) {
		release()
		return sessionrecord.TransmissionReservation{}, sessionrecord.ErrRecordSendNotAllowed
	}
	limit := int64(1_000_000)
	if dailyCap.Valid {
		limit = dailyCap.Int64
	}
	if limit <= 0 || reserve <= 0 {
		release()
		return sessionrecord.TransmissionReservation{}, sessionrecord.ErrRecordBudgetWaiting
	}
	reservationID, err := reserveSessionRecordTokens(ctx, conn, day, job.Lane, reserve, limit, backfillEnabled, backfillBudget, backfillSince)
	if err != nil {
		release()
		return sessionrecord.TransmissionReservation{}, err
	}
	return sessionrecord.TransmissionReservation{ID: reservationID, Lane: job.Lane, ReservedTokens: reserve, Day: day,
		Runtime: providerRuntime, Model: job.Model, ReleaseBody: release}, nil
}

func reserveSessionRecordTokens(ctx context.Context, conn *pgxpool.Conn, day, lane string, reserve, limit int64, backfillEnabled bool, backfillBudget pgtype.Int8, backfillSince pgtype.Timestamptz) (int64, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('session_record_budget'),hashtext($1))`, day); err != nil {
		return 0, err
	}
	var used, reserved int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(input_tokens+output_tokens+estimated_cap_tokens),0)
		FROM session_ai_record_usage WHERE day=$1::date`, day).Scan(&used); err != nil {
		return 0, err
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(reserved_tokens),0) FROM session_ai_record_budget_reservations WHERE day=$1::date`, day).Scan(&reserved); err != nil {
		return 0, err
	}
	if used+reserved+reserve > limit {
		return 0, sessionrecord.ErrRecordBudgetWaiting
	}
	if lane == "backfill" {
		backfillLimit := limit * 80 / 100
		var since any
		if backfillSince.Valid {
			since = backfillSince.Time
		}
		var backfillUsed, backfillReserved int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(input_tokens+output_tokens+estimated_cap_tokens),0)
			FROM session_ai_record_usage WHERE lane='backfill' AND ($1::timestamptz IS NULL OR day >= $1::timestamptz::date)`, since).Scan(&backfillUsed); err != nil {
			return 0, err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(reserved_tokens),0) FROM session_ai_record_budget_reservations
			WHERE lane='backfill' AND ($1::timestamptz IS NULL OR day >= $1::timestamptz::date)`, since).Scan(&backfillReserved); err != nil {
			return 0, err
		}
		if !backfillEnabled || backfillUsed+backfillReserved+reserve > backfillLimit ||
			(backfillBudget.Valid && backfillUsed+backfillReserved+reserve > backfillBudget.Int64) {
			return 0, sessionrecord.ErrRecordBudgetWaiting
		}
	}
	var reservationID int64
	if err := tx.QueryRow(ctx, `INSERT INTO session_ai_record_budget_reservations(day,lane,reserved_tokens)
		VALUES($1::date,$2,$3) RETURNING reservation_id`, day, lane, reserve).Scan(&reservationID); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return reservationID, nil
}

func (s *PgStore) FinishSessionRecordTransmission(ctx context.Context, reservation sessionrecord.TransmissionReservation, usage sessionrecord.CallUsage) error {
	if reservation.ID == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('session_record_budget'),hashtext($1))`, reservation.Day); err != nil {
		return err
	}
	var lane string
	var reserved int64
	err = tx.QueryRow(ctx, `DELETE FROM session_ai_record_budget_reservations WHERE reservation_id=$1
		RETURNING lane,reserved_tokens`, reservation.ID).Scan(&lane, &reserved)
	if err == pgx.ErrNoRows {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	var inputTokens, cachedTokens, outputTokens, unreported, estimated int64
	if usage.Reported {
		inputTokens, cachedTokens, outputTokens = usage.InputTokens, usage.CachedInputTokens, usage.OutputTokens
	} else {
		unreported, estimated = 1, reserved
	}
	_, err = tx.Exec(ctx, `INSERT INTO session_ai_record_usage
		(day,runtime,model,lane,outcome,sessions,calls,unreported_calls,estimated_cap_tokens,input_bytes,input_tokens,cached_input_tokens,output_tokens,duration_ms,dropped_items,uncovered_requests,new_terms)
		VALUES($1::date,$2,$3,$4,$5,0,1,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT(day,runtime,model,lane,outcome) DO UPDATE SET
		calls=session_ai_record_usage.calls+1,unreported_calls=session_ai_record_usage.unreported_calls+EXCLUDED.unreported_calls,
		estimated_cap_tokens=session_ai_record_usage.estimated_cap_tokens+EXCLUDED.estimated_cap_tokens,
		input_bytes=session_ai_record_usage.input_bytes+EXCLUDED.input_bytes,input_tokens=session_ai_record_usage.input_tokens+EXCLUDED.input_tokens,
		cached_input_tokens=session_ai_record_usage.cached_input_tokens+EXCLUDED.cached_input_tokens,output_tokens=session_ai_record_usage.output_tokens+EXCLUDED.output_tokens,
		duration_ms=session_ai_record_usage.duration_ms+EXCLUDED.duration_ms,dropped_items=session_ai_record_usage.dropped_items+EXCLUDED.dropped_items,
		uncovered_requests=session_ai_record_usage.uncovered_requests+EXCLUDED.uncovered_requests,new_terms=session_ai_record_usage.new_terms+EXCLUDED.new_terms`,
		reservation.Day, reservation.Runtime, reservation.Model, lane, usage.Outcome, unreported, estimated, usage.InputBytes, inputTokens, cachedTokens, outputTokens,
		usage.DurationMS, usage.DroppedItems, usage.UncoveredRequests, usage.NewTerms)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
