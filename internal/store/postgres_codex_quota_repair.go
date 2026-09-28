package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// codexLoginEmailRepair identifies the one-time repair of #524 across all of
// retained history. The periodic passes in postgres_codex_quota_attribution.go
// run against a sliding window and would never reach records older than it; this
// runs once, unbounded, and then never again.
const codexLoginEmailRepair = "session_records_codex_login_email_v1"

// codexLoginEmailRepairBatchSize bounds the source rows one committed transaction
// changes. A batch carries an UPDATE and its cursor and nothing else: unlike the
// login_email history repair, the derived tables are rebuilt once at the end
// rather than per batch. See RepairCodexLoginEmailFromQuotaOnce for why.
const codexLoginEmailRepairBatchSize = 500

// codexRepairStateGuard gives the shared batch runner's repair-name parameter a
// referent in statements that have no staged table to join it to. It is not
// ceremony: the row it checks is the one this transaction took FOR UPDATE a
// moment earlier, so the batch is tied to its own progress row and cannot outlive
// a concurrent completion that deleted it.
const codexRepairStateGuard = `
			  AND EXISTS (SELECT 1 FROM codex_login_email_repair_state st WHERE st.name = $1)`

type codexLoginEmailRepairResult struct {
	RevertUpdated    int64
	AccountUpdated   int64
	AttributeUpdated int64
	Complete         bool
}

// RepairCodexLoginEmailFromQuotaOnce repairs retained history once, in three
// phases, without holding one transaction over the session_records hypertable.
//
// The phase order is revert -> account -> attribute and each half of it is
// load-bearing. Reverting first means the attribute phase decides what a record's
// address is from the quota mapping alone, rather than from whatever survived
// #524 next to it. Filling accounts before attributing means the 106,131 rows that
// only gain an account_id in this run are attributed in the same run instead of
// waiting for the next periodic pass.
//
// It returns the rows changed by each phase. The completion marker is inserted
// only once no phase has a candidate left at or below the high-water mark taken
// when the repair started.
//
// The derived tables are rebuilt exactly once, in the batch that records the
// marker, and not per batch the way the login_email history repair does it. That
// makes the middle of the repair an observable state with a specific meaning: a
// batch commits its source rows, but session_overview_rollups, plugin_invocation_facts
// and task_segment_facts keep describing history as it was, and all three change
// at once when the repair completes. The dashboard therefore reads pre-repair
// numbers throughout the run rather than a moving mixture.
//
// The reason is measured, not theoretical. On dev (1.03M session_records) the
// per-batch version took 1h57m, and pg_stat_activity sampling put nearly all of it
// in the session_union CTE that rebuilds session_overview_rollups -- 1.4-2.0s per
// batch across 2,070 batches, against an UPDATE that is a rounding error next to
// it. Prod holds 1.24M rows, so the same shape projects to roughly 2.5 hours. One
// full rebuild replaces all of them. The history repair keeps its per-batch
// refresh because it touches orders of magnitude fewer rows, and so do the
// periodic passes in postgres_codex_quota_attribution.go.
//
// Deferring the rebuild is safe only because the completion branch commits the
// rebuild, the marker and the progress-row cleanup in one transaction: an
// interruption before that commit leaves no marker, and the resumed run -- which
// finds no source rows left to change -- still owes and performs the rebuild.
func (s *PgStore) RepairCodexLoginEmailFromQuotaOnce(ctx context.Context) (int64, int64, int64, error) {
	for {
		// The lock is acquired per committed batch rather than for the whole repair,
		// the way the login_email history repair does it: a session deletion queued
		// between batches then takes effect against the next one.
		result, err := s.repairCodexLoginEmailFromQuotaBatch(ctx, codexLoginEmailRepairBatchSize)
		if err != nil {
			return 0, 0, 0, err
		}
		if result.Complete {
			return result.RevertUpdated, result.AccountUpdated, result.AttributeUpdated, nil
		}
	}
}

// repairCodexLoginEmailFromQuotaBatch is intentionally package-private: the
// integration tests use it to stop at deterministic commit boundaries and prove
// that a resumed repair picks up its cursors rather than restarting. Production
// drains every batch through the public method above.
func (s *PgStore) repairCodexLoginEmailFromQuotaBatch(ctx context.Context, batchSize int) (codexLoginEmailRepairResult, error) {
	if batchSize <= 0 {
		return codexLoginEmailRepairResult{}, fmt.Errorf("codex login_email repair batch size must be positive")
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return codexLoginEmailRepairResult{}, err
	}
	defer conn.Release()
	if err := lockCodexLoginEmailRepair(ctx, conn); err != nil {
		return codexLoginEmailRepairResult{}, err
	}
	defer unlockCodexLoginEmailRepair(conn)
	if err := ensureCodexLoginEmailRepairState(ctx, conn); err != nil {
		return codexLoginEmailRepairResult{}, err
	}
	return runCodexLoginEmailRepairBatch(ctx, conn, batchSize)
}

func lockCodexLoginEmailRepair(ctx context.Context, conn *pgxpool.Conn) error {
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext($1))`, codexLoginEmailRepair); err != nil {
		return fmt.Errorf("lock codex login_email repair: %w", err)
	}
	return nil
}

func unlockCodexLoginEmailRepair(conn *pgxpool.Conn) {
	// A fresh context: cancelling the caller must not hand a pooled connection back
	// while it still holds a session advisory lock.
	_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, codexLoginEmailRepair)
}

// ensureCodexLoginEmailRepairState records the high-water mark the whole repair
// runs against.
//
// Unlike the login_email history repair there is nothing to stage: the evidence
// here is a per-account constant read live from quota_samples, not a timeline that
// retention could truncate between batches, so a single row is the entire setup.
// Freezing MAX(id) still matters -- records arriving while the repair runs are
// attributed by the periodic passes and must not extend a pass that has already
// moved on to the next phase.
func ensureCodexLoginEmailRepairState(ctx context.Context, conn *pgxpool.Conn) error {
	var done bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, codexLoginEmailRepair).Scan(&done); err != nil {
		return fmt.Errorf("read codex login_email repair marker: %w", err)
	}
	if done {
		return nil
	}
	if _, err := conn.Exec(ctx, `INSERT INTO codex_login_email_repair_state (name, phase, max_record_id)
		SELECT $1, 'revert', COALESCE(MAX(id), 0) FROM session_records
		ON CONFLICT (name) DO NOTHING`, codexLoginEmailRepair); err != nil {
		return fmt.Errorf("create codex login_email repair progress: %w", err)
	}
	return nil
}

func runCodexLoginEmailRepairBatch(ctx context.Context, conn *pgxpool.Conn, batchSize int) (codexLoginEmailRepairResult, error) {
	var done bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, codexLoginEmailRepair).Scan(&done); err != nil {
		return codexLoginEmailRepairResult{}, err
	}
	if done {
		return codexLoginEmailRepairResult{Complete: true}, nil
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return codexLoginEmailRepairResult{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return codexLoginEmailRepairResult{}, err
	}

	var phase string
	var accountStaged bool
	var maxID, revertCursor, accountCursor, attributeCursor int64
	var revertTotal, accountTotal, attributeTotal int64
	if err := tx.QueryRow(ctx, `SELECT phase, max_record_id, account_staged,
			revert_cursor, account_cursor, attribute_cursor,
			revert_updated, account_updated, attribute_updated
		FROM codex_login_email_repair_state WHERE name = $1 FOR UPDATE`, codexLoginEmailRepair).
		Scan(&phase, &maxID, &accountStaged, &revertCursor, &accountCursor, &attributeCursor,
			&revertTotal, &accountTotal, &attributeTotal); err != nil {
		return codexLoginEmailRepairResult{}, fmt.Errorf("lock codex login_email repair progress: %w", err)
	}

	// The touched session ids the batch statements report are discarded: the derived
	// rows for every session are rebuilt in one pass at completion, so accumulating
	// the list would only be a second, slower way to name the same rows.
	var cursor, updated int64
	switch phase {
	case "revert":
		cursor, updated, _, err = applyCodexRevertBatch(ctx, tx, maxID, revertCursor, batchSize)
	case "account":
		// Staging is the first thing the phase does and it commits with this batch,
		// so an interruption either leaves the flag false and no rows -- the resumed
		// run stages again -- or the flag true and the whole set present. There is no
		// half-staged state for a cursor to run off the end of.
		if !accountStaged {
			err = stageCodexAccountCandidates(ctx, tx, maxID)
		}
		if err == nil {
			cursor, updated, _, err = applyCodexAccountFillBatch(ctx, tx, maxID, accountCursor, batchSize)
		}
	case "attribute":
		cursor, updated, _, err = applyCodexQuotaAttributeBatch(ctx, tx, maxID, attributeCursor, batchSize)
	default:
		err = fmt.Errorf("unknown codex login_email repair phase %q", phase)
	}
	if err != nil {
		return codexLoginEmailRepairResult{}, err
	}

	result := codexLoginEmailRepairResult{
		RevertUpdated: revertTotal, AccountUpdated: accountTotal, AttributeUpdated: attributeTotal,
	}
	switch {
	case cursor != 0:
		var total int64
		switch phase {
		case "revert":
			revertTotal += updated
			total, result.RevertUpdated = revertTotal, revertTotal
		case "account":
			accountTotal += updated
			total, result.AccountUpdated = accountTotal, accountTotal
		default:
			attributeTotal += updated
			total, result.AttributeUpdated = attributeTotal, attributeTotal
		}
		// The phase name is the column prefix. It comes from the CHECK constraint on
		// the state row, never from a caller, so the interpolation cannot carry
		// anything the schema does not already permit.
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE codex_login_email_repair_state
			SET %s_cursor = $2, %s_updated = $3, updated_at = now() WHERE name = $1`, phase, phase),
			codexLoginEmailRepair, cursor, total); err != nil {
			return codexLoginEmailRepairResult{}, fmt.Errorf("advance codex login_email repair progress: %w", err)
		}
	case phase == "revert" || phase == "account":
		next := "account"
		if phase == "account" {
			next = "attribute"
			// The staged set has been drained, so it is now only a copy of decisions
			// already written. Dropping it here rather than at completion keeps the
			// table empty for the whole attribute phase, which is the longer one, and
			// makes "rows exist" mean "the account phase is still running".
			if _, err := tx.Exec(ctx, `DELETE FROM codex_login_email_repair_candidates WHERE repair_name = $1`,
				codexLoginEmailRepair); err != nil {
				return codexLoginEmailRepairResult{}, fmt.Errorf("clean codex account fill candidates: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE codex_login_email_repair_state
			SET phase = $2, updated_at = now() WHERE name = $1`, codexLoginEmailRepair, next); err != nil {
			return codexLoginEmailRepairResult{}, fmt.Errorf("advance codex login_email repair phase: %w", err)
		}
	default:
		// No candidate remains in any phase at or below the frozen high-water mark.
		// The full derived rebuild, the marker and the progress cleanup commit
		// together: a failure here leaves the progress row intact and therefore
		// resumable, never a marker claiming a repair whose derived rows still
		// describe pre-repair history.
		//
		// The explicit full rebuild takes all three exclusive maintenance locks
		// itself, exactly as the boot-path backfills do.
		// The shared source-mutation lock this batch already holds is the same one
		// BackfillPluginInvocationFacts and BackfillTaskSegmentFacts take around their
		// own full rebuilds.
		//
		// codex_imputed_cost goes first, and the order is load-bearing. The rollups'
		// login scope is computed from unified_events, one arm of which is
		// codex_imputed_cost, and that table holds its own copy of login_email that
		// only this rebuild refreshes. Rebuilding the derived rows against the stale
		// copy writes the address #524 invented straight back into the scope the
		// dashboard filters on, with every source record already correct: on dev that
		// left 0 codex session_records carrying user-a@ and 48 sessions still scoped to
		// it. The session list it returns is discarded because the rebuild below
		// covers every session anyway.
		//
		// claude_imputed_cost needs no equivalent: its INSERT selects agent = 'claude'
		// rows only, and all three repair phases match agent 'codex', so no row this
		// repair can change is ever in it.
		if _, err := refreshCodexImputedCostTx(ctx, tx); err != nil {
			return codexLoginEmailRepairResult{}, err
		}
		if err := rebuildAllLoginEmailDerivedRows(ctx, tx); err != nil {
			return codexLoginEmailRepairResult{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1)`, codexLoginEmailRepair); err != nil {
			return codexLoginEmailRepairResult{}, fmt.Errorf("record codex login_email repair marker: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM codex_login_email_repair_state WHERE name = $1`, codexLoginEmailRepair); err != nil {
			return codexLoginEmailRepairResult{}, fmt.Errorf("clean codex login_email repair progress: %w", err)
		}
		result.Complete = true
	}
	if err := tx.Commit(ctx); err != nil {
		return codexLoginEmailRepairResult{}, err
	}
	return result, nil
}

// The three batch statements below are the periodic passes' predicates against a
// cursor-bounded slice of session_records instead of a `since` bound. The
// predicates themselves are the same constants, so a rule changed in one place
// changes in both -- the one-time repair reproducing a stale copy of a predicate
// is exactly how #524 would have survived its own fix (see
// loginEmailHistoryInferenceSQL).

const codexRevertBatchSQL = `WITH candidates AS MATERIALIZED (
			SELECT sr.id, sr.session_id
			FROM session_records sr
			WHERE sr.id > $2 AND sr.id <= $3` + codexRepairStateGuard + codexRevertMatch + `
			ORDER BY sr.id
			LIMIT $4
		), updated AS (
			UPDATE session_records sr
			SET login_email = '', login_email_source = ''
			FROM candidates c
			WHERE sr.id = c.id AND sr.login_email_source = 'inferred'
			RETURNING sr.id, sr.session_id
		)
		SELECT COALESCE((SELECT MAX(id) FROM candidates), 0), COUNT(*),
		       COALESCE(array_agg(DISTINCT session_id), ARRAY[]::text[])
		FROM updated`

func applyCodexRevertBatch(ctx context.Context, tx pgx.Tx, maxID, cursor int64, batchSize int) (int64, int64, []string, error) {
	return applyLoginEmailHistoryBatch(ctx, tx, codexLoginEmailRepair, codexRevertBatchSQL, maxID, cursor, batchSize)
}

// codexAccountCandidateStageSQL materialises the account phase's candidate set
// once, and it is the phase's former batch statement with the cursor, the ORDER BY
// and the LIMIT taken off.
//
// Everything that decides membership is unchanged and still shared:
// codexQuotaSessionAccountCTE is the same session-to-account mapping the periodic
// pass and the preview join, and codexAccountFillMatch is the same predicate. The
// row set staged here is therefore the row set PreviewCodexAccountFill counts, and
// the preview-equals-apply contract holds across the materialisation rather than
// in spite of it. The high-water mark stays too: a record that arrives after the
// repair started belongs to the periodic passes, not to a phase already in flight.
//
// Why the join had to leave the batch at all is measured. Driven from the mapping
// -- which is the only way Postgres will drive it, at any cursor position -- one
// batch fans 1,554 sessions across all 35 session_records chunks, builds the entire
// 412,717-row join and keeps 500 rows of it. On dev that is 835ms whether the
// cursor sits at the start of the id range or 3% from its end; on the
// production-shaped run it was 14s a batch, flat, for 104,668 rows in 50 minutes.
// The same join run once, unbounded, measures 840ms. See migrations.go.
//
// ON CONFLICT DO NOTHING is not there to make a partial stage resumable -- the
// INSERT and the flag commit together, so no partial stage exists -- but so that a
// retried transaction cannot fail on rows its own earlier attempt wrote.
const codexAccountCandidateStageSQL = codexQuotaSessionAccountCTE + `
		INSERT INTO codex_login_email_repair_candidates (repair_name, id, session_id, account_id, any_inferred)
		SELECT $1, sr.id, sr.session_id, a.account_id, a.any_inferred
		FROM session_records sr
		JOIN session_account a ON a.session_id = sr.session_id
		WHERE sr.id <= $2` + codexAccountFillMatch + `
		ON CONFLICT (repair_name, id) DO NOTHING`

func stageCodexAccountCandidates(ctx context.Context, tx pgx.Tx, maxID int64) error {
	if _, err := tx.Exec(ctx, codexAccountCandidateStageSQL, codexLoginEmailRepair, maxID); err != nil {
		return fmt.Errorf("stage codex account fill candidates: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE codex_login_email_repair_state
		SET account_staged = true, updated_at = now() WHERE name = $1`, codexLoginEmailRepair); err != nil {
		return fmt.Errorf("record codex account fill staging: %w", err)
	}
	return nil
}

// codexAccountFillBatchSQL consumes the staged candidates by primary key. It needs
// no codexRepairStateGuard: repair_name is the leading column of that key, and the
// foreign key cascades, so the rows this statement can see exist only while the
// progress row it belongs to does -- a stronger tie than the EXISTS the other two
// phases need, and one that gives $1 its referent for free.
//
// Both re-checks in the UPDATE are guards against the world moving between staging
// and consumption, which is the one thing materialisation gives up. The
// empty-account_id test is the same re-check the live version carried, now also
// covering a periodic pass that filled the row in between. The deleted_sessions anti-join is the half that
// would otherwise be lost: codexAccountFillMatch refuses a session queued for
// deletion, and with the predicate evaluated once at staging a deletion queued
// afterwards would no longer be seen. Neither re-check touches the cursor, which
// advances over staged ids regardless of what the UPDATE ends up writing.
const codexAccountFillBatchSQL = `WITH candidates AS MATERIALIZED (
			SELECT c.id, c.session_id, c.account_id, c.any_inferred
			FROM codex_login_email_repair_candidates c
			WHERE c.repair_name = $1 AND c.id > $2 AND c.id <= $3
			ORDER BY c.id
			LIMIT $4
		), updated AS (
			UPDATE session_records sr
			SET account_id = c.account_id,
			    account_id_source = CASE WHEN c.any_inferred THEN 'quota-inferred' ELSE 'quota' END
			FROM candidates c
			WHERE sr.id = c.id AND sr.account_id = ''
			  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id)
			RETURNING sr.id, sr.session_id
		)
		SELECT COALESCE((SELECT MAX(id) FROM candidates), 0), COUNT(*),
		       COALESCE(array_agg(DISTINCT session_id), ARRAY[]::text[])
		FROM updated`

func applyCodexAccountFillBatch(ctx context.Context, tx pgx.Tx, maxID, cursor int64, batchSize int) (int64, int64, []string, error) {
	return applyLoginEmailHistoryBatch(ctx, tx, codexLoginEmailRepair, codexAccountFillBatchSQL, maxID, cursor, batchSize)
}

const codexQuotaAttributeBatchSQL = codexQuotaAccountEmailCTE + `, candidates AS MATERIALIZED (
			SELECT sr.id, sr.session_id, m.login_email,
			       ` + codexQuotaSourceExpr + ` AS login_email_source
			FROM session_records sr
			JOIN account_email m ON m.account_id = sr.account_id
			WHERE sr.id > $2 AND sr.id <= $3` + codexRepairStateGuard + codexQuotaMatch + `
			ORDER BY sr.id
			LIMIT $4
		), updated AS (
			UPDATE session_records sr
			SET login_email = c.login_email, login_email_source = c.login_email_source
			FROM candidates c
			WHERE sr.id = c.id AND sr.login_email_source <> 'otel'
			RETURNING sr.id, sr.session_id
		)
		SELECT COALESCE((SELECT MAX(id) FROM candidates), 0), COUNT(*),
		       COALESCE(array_agg(DISTINCT session_id), ARRAY[]::text[])
		FROM updated`

func applyCodexQuotaAttributeBatch(ctx context.Context, tx pgx.Tx, maxID, cursor int64, batchSize int) (int64, int64, []string, error) {
	return applyLoginEmailHistoryBatch(ctx, tx, codexLoginEmailRepair, codexQuotaAttributeBatchSQL, maxID, cursor, batchSize)
}
