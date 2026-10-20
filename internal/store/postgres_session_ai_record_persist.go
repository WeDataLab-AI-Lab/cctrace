package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionrecord"
)

func (s *PgStore) AcquireSessionRecordWorker(ctx context.Context) (func(), bool, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext('session_record_worker'))`).Scan(&acquired); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !acquired {
		conn.Release()
		return func() {}, false, nil
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtext('session_record_worker'))`)
			conn.Release()
		})
	}
	return release, true, nil
}

func (s *PgStore) RecoverSessionRecordClaims(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE session_ai_record_jobs SET claim=NULL,claimed_at=NULL,updated_at=now() WHERE claim IS NOT NULL`)
	return err
}

func (s *PgStore) ReleaseSessionRecordClaim(ctx context.Context, job sessionrecord.Job) error {
	_, err := s.pool.Exec(ctx, `UPDATE session_ai_record_jobs SET claim=NULL,claimed_at=NULL,updated_at=now()
		WHERE session_id=$1 AND claim=$2`, job.SessionID, job.Claim)
	return err
}

func (s *PgStore) FailSessionRecord(ctx context.Context, job sessionrecord.Job, code string, contentFailure bool) error {
	basis := job.BasisHash
	if len(basis) == 0 {
		basis = job.OutputHash
	}
	_, err := s.pool.Exec(ctx, `WITH current AS (
		SELECT CASE WHEN attempts_basis IS DISTINCT FROM $3::bytea THEN 1 ELSE attempts+1 END AS next_attempt
		FROM session_ai_record_jobs WHERE session_id=$1 AND claim=$2
	) UPDATE session_ai_record_jobs j SET
		attempts=CASE WHEN $5::boolean THEN current.next_attempt ELSE j.attempts END,
		attempts_basis=CASE WHEN $5::boolean THEN $3 ELSE j.attempts_basis END,
		retry_at=CASE WHEN NOT $5::boolean THEN NULL
			WHEN current.next_attempt>=3 THEN 'infinity'::timestamptz
			WHEN current.next_attempt=1 THEN now()+interval '1 hour'
			WHEN current.next_attempt=2 THEN now()+interval '6 hours'
			ELSE now()+interval '24 hours' END,
		last_error=CASE WHEN $5::boolean THEN $4 ELSE '' END,
		claim=NULL,claimed_at=NULL,updated_at=now()
	FROM current WHERE j.session_id=$1 AND j.claim=$2`, job.SessionID, job.Claim, basis, code, contentFailure)
	return err
}

func beginSessionRecordWrite(ctx context.Context, s *PgStore) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('session_overview_retention'))`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func (s *PgStore) UpdateSessionRecordNoop(ctx context.Context, job sessionrecord.Job, snapshot sessionrecord.Snapshot) error {
	tx, err := beginSessionRecordWrite(ctx, s)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	current, err := s.readSessionRecordSnapshotTx(ctx, tx, job)
	if err != nil {
		return err
	}
	if !sameRecordHash(current.FactsHash, snapshot.FactsHash) || !sameRecordHash(current.SourceInputHash, snapshot.SourceInputHash) ||
		!sameRecordHash(current.BasisHash, snapshot.BasisHash) || !sameRecordHash(current.RetentionState, snapshot.RetentionState) {
		return sessionrecord.ErrRecordSendNotAllowed
	}
	requestedAt, requestedBy, force, claimedAt, err := deleteSessionRecordClaim(ctx, tx, job)
	if err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE session_ai_records r SET facts_hash=$2,facts_rules_version=$3,basis_hash=$4,prefix_hash=$5,
		input_hash=$6,source_input_hash=$7,covers_until=$8,covers_turn_key=$9,covered_requests=$10,source_first_at=$11
		WHERE r.session_id=$1 AND r.source_input_hash=$7 AND r.prompt_version=$12`, job.SessionID, current.FactsHash,
		current.FactsRulesVersion, current.BasisHash, current.PrefixHash, current.InputHash, current.SourceInputHash,
		current.CoversUntil, current.CoversTurnKey, current.CoveredRequests, current.SourceFirstAt, sessionrecord.PromptVersion)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return sessionrecord.ErrRecordSendNotAllowed
	}
	if err := rescheduleClaimedRequest(ctx, tx, job, requestedAt, requestedBy, force, claimedAt); err != nil {
		return err
	}
	if err := addSessionRecordCount(ctx, tx, job.UsageDay, "", "", job.Lane, "noop"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) PublishRedactedSessionRecord(ctx context.Context, job sessionrecord.Job, snapshot sessionrecord.Snapshot) error {
	if !snapshot.AllRequestsRedacted {
		return fmt.Errorf("redacted publish without all-request redaction")
	}
	tx, err := beginSessionRecordWrite(ctx, s)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	current, err := s.readSessionRecordSnapshotTx(ctx, tx, job)
	if err != nil {
		return err
	}
	if !current.AllRequestsRedacted || !sameRecordHash(current.FactsHash, snapshot.FactsHash) ||
		!sameRecordHash(current.SourceInputHash, snapshot.SourceInputHash) || !sameRecordHash(current.RetentionState, snapshot.RetentionState) {
		return sessionrecord.ErrRecordSendNotAllowed
	}
	requestedAt, requestedBy, force, claimedAt, err := deleteSessionRecordClaim(ctx, tx, job)
	if err != nil {
		return err
	}
	if err := insertRedactedSessionRecord(ctx, tx, job, current); err != nil {
		return err
	}
	if err := rescheduleClaimedRequest(ctx, tx, job, requestedAt, requestedBy, force, claimedAt); err != nil {
		return err
	}
	if err := addSessionRecordCount(ctx, tx, job.UsageDay, "", "", job.Lane, "redacted"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) PublishSessionRecord(ctx context.Context, job sessionrecord.Job, snapshot sessionrecord.Snapshot, record sessionrecord.PersistedRecord) error {
	tx, err := beginSessionRecordWrite(ctx, s)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	current, err := s.readSessionRecordSnapshotTx(ctx, tx, job)
	if err != nil {
		return err
	}
	if current.AllRequestsRedacted || !sameRecordHash(current.FactsHash, snapshot.FactsHash) ||
		!sameRecordHash(current.SourceInputHash, snapshot.SourceInputHash) || !sameRecordHash(current.BasisHash, snapshot.BasisHash) ||
		!sameRecordHash(current.RetentionState, snapshot.RetentionState) {
		return sessionrecord.ErrRecordSendNotAllowed
	}
	requestedAt, requestedBy, force, claimedAt, err := deleteSessionRecordClaim(ctx, tx, job)
	if err != nil {
		return err
	}
	content, err := json.Marshal(record.Content)
	if err != nil {
		return err
	}
	accountKeys := json.RawMessage(record.AccountKeys)
	if len(accountKeys) == 0 {
		accountKeys = json.RawMessage(`[]`)
	}
	_, err = tx.Exec(ctx, `INSERT INTO session_ai_records(session_id,outcome,reason,agent,owner_user_id,owner_profile_email,repository_key,account_keys,
		prompt_version,runtime,model,facts_hash,facts_rules_version,basis_hash,prefix_hash,input_hash,source_input_hash,covers_until,covers_turn_key,
		covered_requests,source_first_at,generated_at,generations,folds,language,headline,content,kinds,components,screens,work_total,work_done,work_open,
		work_unsettled,uncovered_requests,search_text,calls,input_bytes,tokens_reported,input_tokens,cached_input_tokens,output_tokens,duration_ms)
		VALUES($1,'ok','',$2,$3,$4,$5,$6::jsonb,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25::jsonb,
		$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41)
		ON CONFLICT(session_id) DO UPDATE SET outcome='ok',reason='',agent=EXCLUDED.agent,owner_user_id=EXCLUDED.owner_user_id,
		owner_profile_email=EXCLUDED.owner_profile_email,repository_key=EXCLUDED.repository_key,account_keys=EXCLUDED.account_keys,
		prompt_version=EXCLUDED.prompt_version,runtime=EXCLUDED.runtime,model=EXCLUDED.model,facts_hash=EXCLUDED.facts_hash,
		facts_rules_version=EXCLUDED.facts_rules_version,basis_hash=EXCLUDED.basis_hash,prefix_hash=EXCLUDED.prefix_hash,input_hash=EXCLUDED.input_hash,
		source_input_hash=EXCLUDED.source_input_hash,covers_until=EXCLUDED.covers_until,covers_turn_key=EXCLUDED.covers_turn_key,
		covered_requests=EXCLUDED.covered_requests,source_first_at=EXCLUDED.source_first_at,generated_at=EXCLUDED.generated_at,
		generations=session_ai_records.generations+1,folds=EXCLUDED.folds,language=EXCLUDED.language,headline=EXCLUDED.headline,content=EXCLUDED.content,
		kinds=EXCLUDED.kinds,components=EXCLUDED.components,screens=EXCLUDED.screens,work_total=EXCLUDED.work_total,work_done=EXCLUDED.work_done,
		work_open=EXCLUDED.work_open,work_unsettled=EXCLUDED.work_unsettled,uncovered_requests=EXCLUDED.uncovered_requests,search_text=EXCLUDED.search_text,
		calls=EXCLUDED.calls,input_bytes=EXCLUDED.input_bytes,tokens_reported=EXCLUDED.tokens_reported,input_tokens=EXCLUDED.input_tokens,
		cached_input_tokens=EXCLUDED.cached_input_tokens,output_tokens=EXCLUDED.output_tokens,duration_ms=EXCLUDED.duration_ms`,
		record.SessionID, record.Agent, record.OwnerUserID, record.OwnerProfileEmail, record.RepositoryKey, accountKeys, record.PromptVersion, record.Runtime, record.Model,
		record.FactsHash, record.FactsRulesVersion, record.BasisHash, record.PrefixHash, record.InputHash, record.SourceInputHash, record.CoversUntil,
		record.CoversTurnKey, record.CoveredRequests, record.SourceFirstAt, record.GeneratedAt, record.Generations, record.Folds, record.Language, record.Headline,
		json.RawMessage(content), record.Kinds, record.Components, record.Screens, record.WorkTotal, record.WorkDone, record.WorkOpen, record.WorkUnsettled,
		record.UncoveredRequests, record.SearchText, record.Calls, record.InputBytes, record.TokensReported, record.InputTokens, record.CachedInputTokens,
		record.OutputTokens, record.DurationMS)
	if err != nil {
		return fmt.Errorf("publish session record: %w", err)
	}
	if err := rescheduleClaimedRequest(ctx, tx, job, requestedAt, requestedBy, force, claimedAt); err != nil {
		return err
	}
	if job.RangeRequest && job.RangeRequestedAt != nil {
		if _, err := tx.Exec(ctx, `UPDATE session_ai_record_range_requests SET served=served+1,
			finished_at=CASE WHEN served+1>=50 THEN now() ELSE finished_at END
			WHERE dashboard_user_id=$1 AND requested_at=$2 AND finished_at IS NULL`, job.Owner.DashboardUserID, *job.RangeRequestedAt); err != nil {
			return err
		}
	}
	if err := addSessionRecordCount(ctx, tx, record.UsageDay, record.Runtime, record.Model, job.Lane, "ok"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func deleteSessionRecordClaim(ctx context.Context, tx pgx.Tx, job sessionrecord.Job) (*time.Time, *int64, bool, time.Time, error) {
	var requestedAt *time.Time
	var requestedBy *int64
	var force bool
	var claimedAt time.Time
	err := tx.QueryRow(ctx, `DELETE FROM session_ai_record_jobs WHERE session_id=$1 AND claim=$2
		RETURNING requested_at,requested_by,force,claimed_at`, job.SessionID, job.Claim).Scan(&requestedAt, &requestedBy, &force, &claimedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil, false, time.Time{}, sessionrecord.ErrRecordSendNotAllowed
		}
		return nil, nil, false, time.Time{}, err
	}
	return requestedAt, requestedBy, force, claimedAt, nil
}

func rescheduleClaimedRequest(ctx context.Context, tx pgx.Tx, job sessionrecord.Job, requestedAt *time.Time, requestedBy *int64, force bool, claimedAt time.Time) error {
	if requestedAt == nil || requestedBy == nil || !requestedAt.After(claimedAt) {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO session_ai_record_jobs(session_id,lane,requested_at,requested_by,force,updated_at)
		VALUES($1,'manual',$2,$3,$4,now()) ON CONFLICT(session_id) DO UPDATE SET requested_at=EXCLUDED.requested_at,
		requested_by=EXCLUDED.requested_by,force=EXCLUDED.force,updated_at=now()`, job.SessionID, requestedAt, requestedBy, force)
	return err
}

func insertRedactedSessionRecord(ctx context.Context, tx pgx.Tx, job sessionrecord.Job, snapshot sessionrecord.Snapshot) error {
	accountKeys := json.RawMessage(snapshot.AccountKeys)
	if len(accountKeys) == 0 {
		accountKeys = json.RawMessage(`[]`)
	}
	_, err := tx.Exec(ctx, `INSERT INTO session_ai_records(session_id,outcome,reason,agent,owner_user_id,owner_profile_email,repository_key,account_keys,
		prompt_version,runtime,model,facts_hash,facts_rules_version,basis_hash,prefix_hash,source_input_hash,covers_until,covers_turn_key,
		covered_requests,source_first_at,generated_at,generations,folds,language,headline,content,kinds,components,screens,work_total,work_done,work_open,
		work_unsettled,uncovered_requests,search_text,calls,input_bytes,tokens_reported,duration_ms)
		VALUES($1,'redacted','prompt_policy',$2,$3,$4,$5,$6::jsonb,$7,'','',$8,$9,$10,$11,$12,$13,$14,$15,$16,now(),1,0,'',NULL,NULL,'{}','{}','{}',0,0,0,0,0,'',0,0,false,0)
		ON CONFLICT(session_id) DO UPDATE SET outcome='redacted',reason='prompt_policy',agent=EXCLUDED.agent,owner_user_id=EXCLUDED.owner_user_id,
		owner_profile_email=EXCLUDED.owner_profile_email,repository_key=EXCLUDED.repository_key,account_keys=EXCLUDED.account_keys,
		prompt_version=EXCLUDED.prompt_version,runtime='',model='',facts_hash=EXCLUDED.facts_hash,facts_rules_version=EXCLUDED.facts_rules_version,
		basis_hash=EXCLUDED.basis_hash,prefix_hash=EXCLUDED.prefix_hash,input_hash=NULL,source_input_hash=EXCLUDED.source_input_hash,
		covers_until=EXCLUDED.covers_until,covers_turn_key=EXCLUDED.covers_turn_key,covered_requests=EXCLUDED.covered_requests,
		source_first_at=EXCLUDED.source_first_at,generated_at=now(),generations=session_ai_records.generations+1,folds=0,language='',headline=NULL,content=NULL,
		kinds='{}',components='{}',screens='{}',work_total=0,work_done=0,work_open=0,work_unsettled=0,uncovered_requests=0,search_text='',calls=0,input_bytes=0,
		tokens_reported=false,input_tokens=NULL,cached_input_tokens=NULL,output_tokens=NULL,duration_ms=0`,
		job.SessionID, snapshot.Agent, snapshot.OwnerUserID, snapshot.OwnerProfileEmail, snapshot.RepositoryKey, accountKeys, sessionrecord.PromptVersion,
		snapshot.FactsHash, snapshot.FactsRulesVersion, snapshot.BasisHash, snapshot.PrefixHash, snapshot.SourceInputHash, snapshot.CoversUntil,
		snapshot.CoversTurnKey, snapshot.CoveredRequests, snapshot.SourceFirstAt)
	return err
}

func addSessionRecordCount(ctx context.Context, tx pgx.Tx, day, runtime, model, lane, outcome string) error {
	if day == "" {
		day = time.Now().UTC().Format("2006-01-02")
	}
	_, err := tx.Exec(ctx, `INSERT INTO session_ai_record_usage(day,runtime,model,lane,outcome,sessions)
		VALUES($1::date,$2,$3,$4,$5,1) ON CONFLICT(day,runtime,model,lane,outcome)
		DO UPDATE SET sessions=session_ai_record_usage.sessions+1`, day, runtime, model, lane, outcome)
	return err
}
