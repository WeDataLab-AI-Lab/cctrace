package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// sessionRecordsMessageIDColumnDDL adds session_records.message_id: the API
// message a Claude assistant line belongs to, read out of raw once rather than on
// every read. Claude Code writes one response as several JSONL lines -- 1.95 to
// 2.51 per response on production, by month -- and each line carries the
// response's usage, so a sum over lines counts a response two or three times.
// P1t keys requests on this id (p1t-design §2.2).
//
// Nullable on purpose. On a Claude assistant row NULL is "not filled yet" and the
// empty string is "filled, the line has no id", and the two must not look alike:
// the backfill fills only NULL. InsertSessionRecords fills new rows
// (sessionRecordMessageIDSQL) and BackfillSessionRecordMessageIDs the rows written
// before it. Every other row stays NULL.
//
// The ALTER runs only when the column is missing, for the reason the
// usage_rollup_rebuild_requests block in dependentViews gives: this runs on every
// boot, and ADD COLUMN takes ACCESS EXCLUSIVE even when IF NOT EXISTS leaves it
// nothing to add -- here on a hypertable, one lock per chunk, each queued behind
// every ingest transaction holding that chunk. The IF NOT EXISTS inside the
// branch is for two boots at once: both can find the column missing, and the
// second, having waited out the first's ALTER, must not then fail on 42701. No
// default, so adding it rewrites nothing.
//
// No index. The backfill reads its windows through idx_srec_agent, and no query
// filters on the column.
const sessionRecordsMessageIDColumnDDL = `DO $$
	BEGIN
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'session_records'
			  AND column_name = 'message_id') THEN
			ALTER TABLE session_records ADD COLUMN IF NOT EXISTS message_id TEXT;
		END IF;
	END $$`

// sessionRecordMessageIDBackfill names the one-time fill of session_records.message_id
// for the rows written before InsertSessionRecords filled it. The marker commits
// with the last batch, so it never claims a fill that is still under way.
const sessionRecordMessageIDBackfill = "session_records_message_id_v1"

// messageIDBackfillBatchRows is the most rows one transaction of the walk locks
// and fills, and so the most it holds until it commits. The 5-second rule
// (p1t-design §3) is that a batch holds its rows for no more than about that,
// because the other writers of those rows wait for it. A time window cannot
// keep it: a window takes as long as the lines in it, and lines bunch. The
// P1t-8 rehearsals on the production copy took 15.7 s for the slowest one-day
// window (7,349 rows) and, with 6-hour windows, 30.4 s for the slowest of 801
// (8,709 rows) -- more lines in a quarter of the span.
//
// A cap on the rows keeps the rule for the work that follows the rows, and not
// through a stall. The same rehearsal with this cap walked the 801 windows in
// 1,702 batches at about half a second a batch, and 31 of them (1.8%) took over
// 5 seconds, the slowest 19.3 s for 1,000 rows. Those were not the large ones --
// 408 rows took 8.3 s: the walk's WAL kept checkpoints running back to back, one
// of them syncing for 26.7 s, while the boot's rebuilds ran beside it. A smaller
// cap would hold fewer rows through such a stall and not shorten it. A batch that
// takes longer than messageIDBackfillSlowBatch is logged when it commits, and
// the walk's summary line reports the slowest.
const messageIDBackfillBatchRows = 1000

// messageIDBackfillSlowBatch is the hold the 5-second rule allows a batch.
const messageIDBackfillSlowBatch = 5 * time.Second

// messageIDBackfillWindow is the span of ts the walk takes its batches from, one
// window at a time. A batch is the first messageIDBackfillBatchRows rows of the
// window the cursor is on, so the window bounds what one lock statement reads
// and sorts -- one window's rows, not the range's -- and it orders the walk:
// windows go back from the range end, so the most recent days, which the
// dashboards read the most, are filled first.
//
// It does not bound the hold; the row cap bounds the rows held. That was the
// window's job while a window was a batch, and the 5-second rule halved it
// twice (from a day) for it, but the time a window takes follows its lines,
// which bunch: one 6-hour window held 8,709 and took 30.4 s. Production holds
// at most 22,688 Claude assistant lines in a day, so a window is a few thousand
// rows; those 8,709 take nine batches.
const messageIDBackfillWindow = 6 * time.Hour

// messageIDBackfillLockTimeout bounds every lock wait a batch can still have: the
// shared source lock an identity-wide admin change holds exclusively, the cursor
// row, and the relation locks of the chunks it reads and writes. A row is never
// waited on -- the batch's rows are taken NOWAIT -- so this is not what keeps the
// batch out of other writers' way. The value is the per-lock wait the boot
// migrations use (defaultMigrateLockTimeout), sized there against ingest that
// routinely holds a lock for a second or two.
const messageIDBackfillLockTimeout = 3 * time.Second

// messageIDWindowSQL is the rows of one window still to fill: $1 is the window's
// exclusive lower end, $2 its inclusive upper end, $3 the range start that cuts
// the last window off. The row lock picks a batch among the rows it matches, and
// the UPDATE reads it again beside the batch's ids, so the rows written are
// exactly the rows locked and the UPDATE keeps the ts bounds chunk exclusion
// needs.
const messageIDWindowSQL = `agent = 'claude' AND record_type = 'assistant' AND message_id IS NULL
			AND ts > $1 AND ts <= $2 AND ts >= $3`

// messageIDWalk counts what a walk has committed, for the line logged when it
// completes: the batches, the windows they walked to the end -- a batch that
// finds fewer rows than the cap ends its window -- and the rows filled. The
// slowest batch is the figure P1t-8 holds against the 5-second rule
// (messageIDBackfillBatchRows); it runs from the row lock to the commit, the
// time ingest re-sending those lines can be kept waiting.
type messageIDWalk struct {
	mu          sync.Mutex
	windows     int
	batches     int
	filled      int64
	slowest     time.Duration
	slowestRows int64
}

// BackfillSessionRecordMessageIDs fills message_id on the Claude assistant rows
// written before InsertSessionRecords filled it, one batch per transaction --
// at most messageIDBackfillBatchRows rows of one fixed window, newest first --
// until the completion marker commits. It is step 2 of the P1t transition, and
// it moves no aggregate: message_id holds what the request key would otherwise
// read out of raw (p1t-design §2.2), so every read sees the same answer before,
// during and after.
//
// Every commit is a place to resume. An error rolls back only the batch it hit
// and leaves the cursor where it was, so calling again retries that batch: the
// batches of the same window committed before it stay, and the cursor leaves the
// window only with a batch that found fewer rows than the cap. A row another
// writer holds, a lock wait past messageIDBackfillLockTimeout or a deadlock is
// such an error, and isContendedLockError recognises it: the caller decides how
// long to back off. Once the marker exists a call does nothing.
//
// It also reports whether it committed anything -- a batch, or the marker of
// an empty range -- with an error or without; false without one is a walk that
// was already done when the call began. The transition runner logs the step's
// completion only for a call that committed, and after an error reads it as
// progress: a walk of eight hundred windows and over a thousand batches that
// meets a held row now and then is getting through, and must not back off as
// though it were stuck on one. The marker is read once up front for this; past
// it, every batch call that returns without an error has committed (or, should
// another process finish the walk meanwhile, found it done).
//
// Deleting the marker and the cursor row together -- the P1t rollback runbook
// does both -- starts a new walk over a newly recorded range. That is how rows an
// older binary inserted with message_id NULL during a rollback are filled after
// the redeploy (p1t-design §10, N3). Deleting the marker alone does not: the next
// call finds the old cursor spent and marks the walk done again without filling
// anything.
func (s *PgStore) BackfillSessionRecordMessageIDs(ctx context.Context) (bool, error) {
	done, err := backfillMarked(ctx, s.pool, sessionRecordMessageIDBackfill)
	if err != nil {
		return false, fmt.Errorf("read message_id backfill marker: %w", err)
	}
	if done {
		return false, nil
	}
	progressed := false
	for {
		finished, err := s.sessionRecordMessageIDStep(ctx, &s.messageIDWalk)
		if err != nil {
			return progressed, err
		}
		progressed = true
		if finished {
			return true, nil
		}
	}
}

// sessionRecordMessageIDStep fills one batch in one transaction, counts it in
// walk, and reports whether the walk is complete. Package-private so the tests
// can stop between commits, each with a walk of its own; production drains it
// through BackfillSessionRecordMessageIDs with the store's.
//
// The range is recorded by the first call -- [min(ts), max(ts)] of the Claude
// assistant rows -- and only read after that. Rows that arrive later are not the
// walk's to fill: this binary's InsertSessionRecords writes message_id with the
// row, so chasing the newest ts would only re-read rows that are already filled.
// ts > 'epoch' is sessionStartExpr's rule for a row that never knew when it
// happened: one such row would stretch the range back to 1970, tens of thousands
// of empty windows. It keeps NULL, and the request key reads its id out of raw
// instead.
//
// A window is (end - messageIDBackfillWindow, end], cut off below at the range
// start, and end walks back from the range end, so the most recent days, which
// the dashboards read the most, are filled first. A batch is the first
// messageIDBackfillBatchRows rows of the window the cursor is on, newest first --
// ts DESC, id DESC, a total order, so the rows of a batch are the same whatever
// plan reads them. message_id IS NULL keeps a batch off every row that is
// already filled -- by the insert path, by an earlier batch, or by a walk a
// rollback interrupted -- so none of them is rewritten, and it is what makes a
// window run out: a batch fills every row it takes, a line with no id with the
// empty string, so none is taken twice.
//
// Only a batch that finds fewer rows than the cap, having taken the window's
// last, moves the cursor to the next window; one that finds the cap leaves it,
// and the next call takes the next rows of the same window. A window of n rows
// takes n / messageIDBackfillBatchRows + 1 calls (whole-number division): rows
// that are an exact multiple of the cap cost one more call that finds nothing,
// as an empty window costs one.
//
// The batch's rows are locked NOWAIT before they are written. This is a
// background job sharing rows with writers somebody is waiting on -- sync
// reenrich, the login_email passes, the orphan cleanup -- and it yields to them
// on purpose: a row one of them holds fails the batch at once with 55P03, the
// batch rolls back with the cursor where it was, and the caller backs off and
// retries it. Waiting instead would hold a batch of rows that writer may need
// next, and the deadlock detector breaking that cycle could pick the user's own
// command as the victim. Only the rows of the batch count: a row held further
// down the window's order does not fail it, and the batches of the window
// committed before stay. Once the lock is taken, the UPDATE waits on no row: it
// is limited to the ids the lock took, rows this transaction holds, so a row
// that matches messageIDWindowSQL by then without having been locked -- one that
// arrived, or one past the cap -- is not its business.
func (s *PgStore) sessionRecordMessageIDStep(ctx context.Context, walk *messageIDWalk) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL lock_timeout = %d",
		messageIDBackfillLockTimeout.Milliseconds())); err != nil {
		return false, fmt.Errorf("set message_id backfill lock_timeout: %w", err)
	}
	done, err := backfillMarked(ctx, tx, sessionRecordMessageIDBackfill)
	if err != nil {
		return false, fmt.Errorf("read message_id backfill marker: %w", err)
	}
	if done {
		return true, tx.Commit(ctx)
	}
	// Shared, as every other session_records writer takes it. An identity-wide
	// admin change (DeleteUserData, MergeUsers, a strict session delete) takes it
	// exclusively before touching a row, so either it waits for this batch or this
	// batch waits for it, and neither holds rows the other is waiting on.
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return false, err
	}

	var start, end time.Time
	recorded, empty := false, false
	err = tx.QueryRow(ctx, `SELECT range_start, next_window_end
		FROM session_records_message_id_cursor FOR UPDATE`).Scan(&start, &end)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// The first call records the range. The inserted row is this transaction's
		// until it commits, which locks it as FOR UPDATE would.
		// No ON CONFLICT DO NOTHING: a second first call running alongside would get
		// no row back, read that as an empty range and mark the walk done while this
		// one is still walking. It fails on the primary key instead and finds the
		// row when it is retried.
		err = tx.QueryRow(ctx, `INSERT INTO session_records_message_id_cursor
				(range_start, range_end, next_window_end)
			SELECT lo, hi, hi FROM (
				SELECT min(ts) AS lo, max(ts) AS hi FROM session_records
				WHERE agent = 'claude' AND record_type = 'assistant' AND ts > 'epoch'
			) observed
			WHERE lo IS NOT NULL
			RETURNING range_start, next_window_end`).Scan(&start, &end)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// No Claude assistant line with a real ts: nothing was written before the
			// column, and everything after it is the insert path's.
			empty = true
		case err != nil:
			return false, fmt.Errorf("record message_id backfill range: %w", err)
		default:
			recorded = true
		}
	case err != nil:
		return false, fmt.Errorf("read message_id backfill cursor: %w", err)
	}

	finished := empty
	var filled int64
	var exhausted bool
	var locked time.Time
	if !empty {
		lower := end.Add(-messageIDBackfillWindow)
		bounds := fmt.Sprintf("(%s, %s]", lower.Format(time.RFC3339), end.Format(time.RFC3339))
		locked = time.Now()
		rows, err := tx.Query(ctx, `SELECT id FROM session_records WHERE `+messageIDWindowSQL+`
			ORDER BY ts DESC, id DESC LIMIT $4
			FOR NO KEY UPDATE NOWAIT`, lower, end, start, messageIDBackfillBatchRows)
		if err != nil {
			return false, fmt.Errorf("lock message_id batch of window %s: %w", bounds, err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return false, fmt.Errorf("lock message_id batch of window %s: %w", bounds, err)
		}
		tag, err := tx.Exec(ctx, `UPDATE session_records
			SET message_id = `+fmt.Sprintf(sessionRecordMessageIDSQL, "record_type", "agent", "raw")+`
			WHERE `+messageIDWindowSQL+` AND id = ANY($4::bigint[])`, lower, end, start, ids)
		if err != nil {
			return false, fmt.Errorf("fill message_id batch of window %s: %w", bounds, err)
		}
		filled = tag.RowsAffected()
		// Every locked row is a NULL row of the window, and the expression gives
		// such a row a value, so the UPDATE writes all of them. Were it to leave
		// one, a batch that found the cap would commit, keep the cursor, and pick
		// the same rows on the next call, for ever and without an error; a batch
		// that found fewer would move the cursor past the row and leave it NULL
		// for good. More rows than were locked is neither hazard and is let
		// through: it takes two rows of the window sharing an id, and both are
		// then filled.
		if filled < int64(len(ids)) {
			return false, fmt.Errorf("fill message_id batch of window %s: locked %d row(s), filled %d",
				bounds, len(ids), filled)
		}
		// Fewer rows than the cap means the window has none left, and the cursor
		// leaves it with this batch. A batch that found the cap leaves the window
		// as it is; the next call takes what is left of it, if anything.
		exhausted = len(ids) < messageIDBackfillBatchRows
		if exhausted {
			if _, err := tx.Exec(ctx, `UPDATE session_records_message_id_cursor
				SET next_window_end = $1`, lower); err != nil {
				return false, fmt.Errorf("advance message_id backfill cursor: %w", err)
			}
			finished = lower.Before(start)
		}
	}
	if finished {
		if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`,
			sessionRecordMessageIDBackfill); err != nil {
			return false, fmt.Errorf("record message_id backfill marker: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}

	var took time.Duration
	walk.mu.Lock()
	if !empty {
		took = time.Since(locked)
		walk.batches++
		if exhausted {
			walk.windows++
		}
		walk.filled += filled
		if took > walk.slowest {
			walk.slowest, walk.slowestRows = took, filled
		}
	}
	windows, batches, total, slowest, slowestRows := walk.windows, walk.batches, walk.filled, walk.slowest, walk.slowestRows
	walk.mu.Unlock()
	if took > messageIDBackfillSlowBatch {
		log.Printf("[store] message_id backfill: a batch of %d row(s) held its rows for %s, over the %s rule",
			filled, took.Round(time.Millisecond), messageIDBackfillSlowBatch)
	}
	if recorded {
		log.Printf("[store] message_id backfill: range recorded, %s to %s",
			start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339))
	}
	if finished {
		if windows == 0 {
			log.Printf("[store] message_id backfill: done, no Claude assistant row to fill")
		} else {
			log.Printf("[store] message_id backfill: done; this process walked %d window(s) in %d batch(es) and filled %d row(s), slowest batch %s for %d row(s)",
				windows, batches, total, slowest.Round(time.Millisecond), slowestRows)
		}
	}
	return finished, nil
}
