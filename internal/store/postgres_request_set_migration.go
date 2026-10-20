package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// usageHourlyRollupArchive marks step 1 of the P1t transition: the copy of
// usage_hourly_rollups taken before any later step recounts it.
const usageHourlyRollupArchive = "p1t_archive_v1"

// requestSetMigrationDone marks the end of the P1t transition (p1t-design §4.1,
// step 9). Until it exists the stored aggregates may still hold totals counted
// line by line while the live paths count requests, and the dashboard says so.
// Step 9, the last in RequestSetMigrationSteps, writes it and nothing else.
const requestSetMigrationDone = "request_set_v1"

// RequestSetMigrationStep is one step of the P1t transition (p1t-design §4.1).
//
// Run returns at once when Marker exists, and writes Marker in the transaction
// that finishes its work, so calling it again is always safe: a process that
// restarts mid-transition resumes at the first step without its marker, and a
// batch step at its cursor. The runner never writes a marker.
//
// progressed says whether the call committed any of the step's transactions,
// the marker's included. It is false only for a call that returned because the
// marker was already there, or that failed before committing anything. The
// runner logs a completion only for a call that progressed, so a boot after
// the transition is silent; and after a failure, progress is what tells a step
// getting through its batches with a collision now and then from one that
// fails on the same batch every time.
type RequestSetMigrationStep struct {
	Marker string
	Run    func(ctx context.Context) (progressed bool, err error)
}

// RequestSetMigrationSteps is the P1t transition in order. The runner in
// cmd/cctraced runs it and RequestSetMigrationStatus reports on it, so the two
// cannot disagree about which steps there are or in what order.
func (s *PgStore) RequestSetMigrationSteps() []RequestSetMigrationStep {
	return []RequestSetMigrationStep{
		{Marker: usageHourlyRollupArchive, Run: s.archiveUsageHourlyRollupsStep},
		{Marker: sessionRecordMessageIDBackfill, Run: s.BackfillSessionRecordMessageIDs},
		{Marker: claudeImputedRequestsBackfill, Run: s.rewriteClaudeImputedCostStep},
		{Marker: sessionOverviewTokensRecount, Run: s.recountSessionOverviewTokensStep},
		{Marker: usageHourlyRollupsRecount, Run: s.recountUsageHourlyRollupsStep},
		{Marker: coverageMinutesRecount, Run: s.recountCoverageMinutesStep},
		{Marker: pluginInvocationFactsRecount, Run: s.recountPluginInvocationFactsStep},
		{Marker: taskSegmentTokensRecount, Run: s.recountTaskSegmentTokensStep},
		{Marker: requestSetMigrationDone, Run: s.completeRequestSetMigrationStep},
	}
}

// archiveUsageHourlyRollupsStep is step 1: the rollups built, then archived.
//
// Its own marker comes first, as every step's does: once the archive is taken,
// the step builds nothing either. Were the build first, a rollup marker gone
// after the transition and a boot build that keeps failing would have this step
// retry a full build every five minutes for good, holding the rollup writers'
// lock against the 60-second tail and the 30-minute refresh.
//
// An archive of rollups that were never built would record an empty or partial
// "before". The boot repair ahead of the transition builds them, and then the
// build here returns at once; when that repair failed, it is retried here, and
// its failure is this step's. Progress is the archive's: the build is the
// rollup backfill's own work, under its own marker.
func (s *PgStore) archiveUsageHourlyRollupsStep(ctx context.Context) (bool, error) {
	done, err := backfillMarked(ctx, s.pool, usageHourlyRollupArchive)
	if err != nil {
		return false, fmt.Errorf("read usage hourly rollup archive marker: %w", err)
	}
	if done {
		return false, nil
	}
	if err := s.BackfillUsageHourlyRollups(ctx); err != nil {
		return false, fmt.Errorf("build usage hourly rollups before the archive: %w", err)
	}
	return s.ArchiveUsageHourlyRollups(ctx)
}

// RequestSetMigrationStatus is where the P1t transition stands, as GET
// /api/maintenance/status reports it (p1t-design §4.2).
type RequestSetMigrationStatus struct {
	// TokenRulePending holds until the transition's completion marker commits.
	// The live paths count requests from the deploy on and the stored aggregates
	// from the step that recounts them, so until then screens may disagree.
	TokenRulePending bool `json:"token_rule_pending"`
	// Step is the marker of the first step in RequestSetMigrationSteps not yet
	// done, "" once all of them are. A transition stuck on a failing step shows
	// which one.
	Step string `json:"step"`
}

// RequestSetMigrationStatus reads where the transition stands, in one query.
func (s *PgStore) RequestSetMigrationStatus(ctx context.Context) (RequestSetMigrationStatus, error) {
	steps := s.RequestSetMigrationSteps()
	markers := make([]string, len(steps))
	for i, step := range steps {
		markers[i] = step.Marker
	}
	var status RequestSetMigrationStatus
	if err := s.pool.QueryRow(ctx, `SELECT
			NOT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1),
			COALESCE((SELECT step.name FROM unnest($2::text[]) WITH ORDINALITY AS step(name, n)
				WHERE NOT EXISTS (SELECT 1 FROM schema_backfills b WHERE b.name = step.name)
				ORDER BY step.n LIMIT 1), '')`,
		requestSetMigrationDone, markers).Scan(&status.TokenRulePending, &status.Step); err != nil {
		return RequestSetMigrationStatus{}, fmt.Errorf("read request set migration status: %w", err)
	}
	return status, nil
}

// ArchiveUsageHourlyRollups is the archive of step 1 of the P1t transition. It
// copies usage_hourly_rollups, summed down to (bucket, agent, billing_provider,
// model), into usage_hourly_rollups_archive_p1t before any later step recounts
// it: the "before" of the P1t-8 report (p1t-design §5.1). It reports whether it
// committed: false when the marker was already there.
//
// The rows and the marker commit in one transaction, so a failure leaves
// neither and the next call archives again. Once the marker exists a call does
// nothing. The archive is taken whole, once: should the marker be deleted by
// hand, the next call finds the table holding the first archive, adds nothing
// to it -- not even a key that has appeared since -- and only writes the marker
// again. The first archive is the real "before", which is also why the P1t
// rollback runbook keeps the marker (p1t-design §10). ON CONFLICT DO NOTHING is
// for two first calls at once, which both find the table empty.
//
// No lock against the rollup writers. Each rewrites its buckets in one
// transaction, and the INSERT ... SELECT reads one snapshot, so the copy holds
// every refresh whole or not at all.
func (s *PgStore) ArchiveUsageHourlyRollups(ctx context.Context) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	done, err := backfillMarked(ctx, tx, usageHourlyRollupArchive)
	if err != nil {
		return false, fmt.Errorf("read usage hourly rollup archive marker: %w", err)
	}
	if done {
		return false, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO usage_hourly_rollups_archive_p1t (
			bucket, agent, billing_provider, model,
			cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, event_count,
			archived_at)
		SELECT bucket, agent, billing_provider, model,
			sum(cost_usd), sum(input_tokens)::bigint, sum(output_tokens)::bigint,
			sum(cache_read_tokens)::bigint, sum(cache_create_tokens)::bigint, sum(event_count)::bigint,
			now()
		FROM usage_hourly_rollups
		WHERE NOT EXISTS (SELECT 1 FROM usage_hourly_rollups_archive_p1t)
		GROUP BY bucket, agent, billing_provider, model
		ON CONFLICT DO NOTHING`); err != nil {
		return false, fmt.Errorf("archive usage hourly rollups: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`,
		usageHourlyRollupArchive); err != nil {
		return false, fmt.Errorf("record usage hourly rollup archive marker: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// sessionOverviewTokensRecount marks step 4 of the P1t transition: the overview
// rows of every Claude and Codex session recounted by request (p1t-design §6.4).
const sessionOverviewTokensRecount = "session_overview_tokens_v2"

// sessionOverviewTokensBatchSize is the sessions one transaction of step 4
// recounts.
const sessionOverviewTokensBatchSize = 100

// recountSessionOverviewTokensStep is step 4 of the P1t transition (p1t-design
// §6.4): every session session_overview_rollups holds for Claude or Codex goes
// through refreshSessionOverviewRollups again, in session_id order, a batch per
// transaction. A session's four scopes are rebuilt together, so a scope whose
// tokens came from the session records while another's came from OTEL -- OTEL
// under one profile, JSONL under another -- is recounted as well. Codex is in the
// walk because its session-record rows now read codex_imputed_cost.
//
// A cursor row taken FOR UPDATE holds the last session recounted and advances
// in the batch's transaction, and the marker commits with the last batch, so a
// failure loses only the batch it hit and the next call goes on from there. It
// reports progress as RequestSetMigrationStep documents: true once any batch,
// or the marker alone, has committed in this call.
//
// Without the overview's build marker (sessionOverviewRollupBackfill) there is
// nothing to recount: the boot repair that builds the overview,
// BackfillSessionOverviewRollups, builds it with this binary's rules, and the
// step writes its marker alone. It does not call that build itself: cmd/cctraced
// runs it only once project_hash canonicalization has succeeded.
func (s *PgStore) recountSessionOverviewTokensStep(ctx context.Context) (bool, error) {
	done, err := backfillMarked(ctx, s.pool, sessionOverviewTokensRecount)
	if err != nil {
		return false, fmt.Errorf("read session overview token recount marker: %w", err)
	}
	if done {
		return false, nil
	}
	progressed := false
	for {
		finished, err := s.recountSessionOverviewTokensBatch(ctx)
		if err != nil {
			return progressed, err
		}
		progressed = true
		if finished {
			return true, nil
		}
	}
}

// recountSessionOverviewTokensBatch recounts the next batch in one transaction
// and reports whether the walk is complete.
func (s *PgStore) recountSessionOverviewTokensBatch(ctx context.Context) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	done, err := backfillMarked(ctx, tx, sessionOverviewTokensRecount)
	if err != nil {
		return false, fmt.Errorf("read session overview token recount marker: %w", err)
	}
	if done {
		return true, tx.Commit(ctx)
	}
	// The locks every other caller of refreshSessionOverviewRollups takes, in
	// their order: the shared source lock here; inside the refresh, the shared
	// maintenance lock, then the sessions' own in session_id order.
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return false, err
	}
	built, err := backfillMarked(ctx, tx, sessionOverviewRollupBackfill)
	if err != nil {
		return false, fmt.Errorf("read session overview rollup marker: %w", err)
	}
	finished := !built
	if built {
		var after string
		err := tx.QueryRow(ctx, `SELECT last_session_id FROM session_overview_tokens_cursor FOR UPDATE`).Scan(&after)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// The first batch creates the row, which this transaction holds until it
			// commits. No ON CONFLICT DO NOTHING, as for the message_id cursor: a
			// second first batch alongside fails on the primary key and finds the
			// row when it is retried.
			if _, err := tx.Exec(ctx, `INSERT INTO session_overview_tokens_cursor (last_session_id) VALUES ('')`); err != nil {
				return false, fmt.Errorf("create session overview token recount cursor: %w", err)
			}
		case err != nil:
			return false, fmt.Errorf("read session overview token recount cursor: %w", err)
		}
		ids, err := querySessionIDs(ctx, tx, `SELECT session_id FROM session_overview_rollups
			WHERE scope_type = 'all' AND agent IN ('claude', 'codex') AND session_id > $1
			ORDER BY session_id LIMIT $2`, after, sessionOverviewTokensBatchSize)
		if err != nil {
			return false, fmt.Errorf("read session overview token recount batch: %w", err)
		}
		if err := refreshSessionOverviewRollups(ctx, tx, ids); err != nil {
			return false, err
		}
		if len(ids) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE session_overview_tokens_cursor SET last_session_id = $1`,
				ids[len(ids)-1]); err != nil {
				return false, fmt.Errorf("advance session overview token recount cursor: %w", err)
			}
		}
		finished = len(ids) < sessionOverviewTokensBatchSize
	}
	if finished {
		if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`,
			sessionOverviewTokensRecount); err != nil {
			return false, fmt.Errorf("record session overview token recount marker: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return finished, nil
}

// usageHourlyRollupsRecount marks step 5 of the P1t transition: every hour of
// usage_hourly_rollups recounted from visible_events (p1t-design §5.2).
const usageHourlyRollupsRecount = "usage_hourly_rollups_requests_v1"

// recountUsageHourlyRollupsStep is step 5 of the P1t transition (p1t-design
// §5.2): usage_hourly_rollups rebuilt whole from visible_events --
// refreshUsageHourlyRollups with no start -- and the marker, in one transaction.
// Every key is recounted, Claude's included: an hour whose OTEL has expired now
// holds the request-level estimate of step 3, an hour that still has its OTEL
// keeps the measured values, and what every hour held before is in the archive
// of step 1. A failure leaves the rollups as they were and no marker, and the
// next call recounts them all again. It reports whether it committed: false when
// the marker was already there.
//
// It holds the rollup writers' lock for as long as a full rebuild runs: the
// exclusion rebuild, which starts at an identity's earliest row, took ~100s on
// production data, and P1t-8 times this step. All that time the 60-second tail
// refresh and the 30-minute window refresh wait on the lock, and so does the
// coverage refresh each of their ticks runs next; the queued exclusion rebuild
// only tries the lock, so it skips and tries again at its next 5-second poll.
// Readers are not held: they read the rows as they were until the step commits.
// The marker is read again under the lock, so of two first calls at once the
// second finds the first one's and does nothing.
//
// Without the rollups' build marker (usageHourlyRollupBackfill) there is nothing
// to recount: the boot repair that builds them, BackfillUsageHourlyRollups,
// builds them with this binary's rules, and the step writes its marker alone.
func (s *PgStore) recountUsageHourlyRollupsStep(ctx context.Context) (bool, error) {
	done, err := backfillMarked(ctx, s.pool, usageHourlyRollupsRecount)
	if err != nil {
		return false, fmt.Errorf("read usage hourly rollup recount marker: %w", err)
	}
	if done {
		return false, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockUsageHourlyRollupWriters(ctx, tx); err != nil {
		return false, err
	}
	done, err = backfillMarked(ctx, tx, usageHourlyRollupsRecount)
	if err != nil {
		return false, fmt.Errorf("read usage hourly rollup recount marker: %w", err)
	}
	if done {
		return false, tx.Commit(ctx)
	}
	built, err := backfillMarked(ctx, tx, usageHourlyRollupBackfill)
	if err != nil {
		return false, fmt.Errorf("read usage hourly rollup marker: %w", err)
	}
	if built {
		if err := refreshUsageHourlyRollups(ctx, tx, nil); err != nil {
			return false, fmt.Errorf("recount usage hourly rollups by request: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`,
		usageHourlyRollupsRecount); err != nil {
		return false, fmt.Errorf("record usage hourly rollup recount marker: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// coverageMinutesRecount marks step 6 of the P1t transition: every minute of
// coverage_measured_minutes recounted from visible_events (p1t-design §6.5).
const coverageMinutesRecount = "coverage_minutes_requests_v1"

// recountCoverageMinutesStep is step 6 of the P1t transition (p1t-design §6.5):
// coverage_measured_minutes rebuilt whole from visible_events --
// refreshCoverageMeasuredMinutes with no start -- and the marker, in one
// transaction, as step 5 does for the hourly rollups. Exclusions apply where
// they always have, at the refresh and at read time, so the minutes an account
// excluded now had stored before its exclusion leave the table here. A failure
// leaves the table as it was and no marker. It reports whether it committed:
// false when the marker was already there.
//
// It holds the coverage writers' lock for as long as the recount runs, tens of
// seconds to minutes by the design's estimate (p1t-design §4.2); P1t-8 times it.
// All that time the 60-second tail refresh and the 30-minute window refresh of
// coverage wait on the lock, and the goroutine of the 60-second one with it, so
// the hourly tail refresh of the next tick comes late too. Readers are not held.
// The marker is read again under the lock, so of two first calls at once the
// second finds the first one's and does nothing.
//
// Without the table's build marker (coverageMinutesBackfill) there is nothing to
// recount: the boot repair that builds it, BackfillCoverageMeasuredMinutes,
// builds it with this binary's rules, and until then the coverage reader
// aggregates raw events. The step writes its marker alone.
func (s *PgStore) recountCoverageMinutesStep(ctx context.Context) (bool, error) {
	done, err := backfillMarked(ctx, s.pool, coverageMinutesRecount)
	if err != nil {
		return false, fmt.Errorf("read coverage minutes recount marker: %w", err)
	}
	if done {
		return false, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockCoverageMeasuredMinutesWriters(ctx, tx); err != nil {
		return false, err
	}
	done, err = backfillMarked(ctx, tx, coverageMinutesRecount)
	if err != nil {
		return false, fmt.Errorf("read coverage minutes recount marker: %w", err)
	}
	if done {
		return false, tx.Commit(ctx)
	}
	built, err := backfillMarked(ctx, tx, coverageMinutesBackfill)
	if err != nil {
		return false, fmt.Errorf("read coverage minutes marker: %w", err)
	}
	if built {
		if err := refreshCoverageMeasuredMinutes(ctx, tx, nil); err != nil {
			return false, fmt.Errorf("recount coverage measured minutes by request: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`,
		coverageMinutesRecount); err != nil {
		return false, fmt.Errorf("record coverage minutes recount marker: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// pluginInvocationFactsRecount marks step 7 of the P1t transition: every plugin
// invocation fact rebuilt by request (p1t-design §6.3).
const pluginInvocationFactsRecount = "plugin_invocation_facts_requests_v1"

// recountPluginInvocationFactsStep is step 7 of the P1t transition: every plugin
// invocation fact rebuilt -- rebuildAllPluginInvocationFacts, now counting
// requests -- and the marker, in one transaction. A failure leaves the facts as
// they were and no marker, and the next call rebuilds them all again. It reports
// whether it committed: false when the marker was already there.
//
// It takes the locks BackfillPluginInvocationFacts takes, in its order: the
// shared source lock, then the facts' exclusive one, which every scoped refresh
// waits on for as long as the rebuild runs. The marker is read again under that
// lock, so of two first calls at once the second finds the first one's and does
// nothing.
//
// Without the facts' build marker (pluginInvocationFactsBackfill) there is
// nothing to rebuild: the boot repair that builds the facts,
// BackfillPluginInvocationFacts, builds them with this binary's rules, and until
// then PluginUsage reads the raw query, which counts requests too. The step
// writes its marker alone.
func (s *PgStore) recountPluginInvocationFactsStep(ctx context.Context) (bool, error) {
	done, err := backfillMarked(ctx, s.pool, pluginInvocationFactsRecount)
	if err != nil {
		return false, fmt.Errorf("read plugin invocation fact recount marker: %w", err)
	}
	if done {
		return false, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return false, err
	}
	if err := lockPluginInvocationFacts(ctx, tx, true); err != nil {
		return false, err
	}
	done, err = backfillMarked(ctx, tx, pluginInvocationFactsRecount)
	if err != nil {
		return false, fmt.Errorf("read plugin invocation fact recount marker: %w", err)
	}
	if done {
		return false, tx.Commit(ctx)
	}
	built, err := backfillMarked(ctx, tx, pluginInvocationFactsBackfill)
	if err != nil {
		return false, fmt.Errorf("read plugin invocation fact marker: %w", err)
	}
	if built {
		if err := rebuildAllPluginInvocationFacts(ctx, tx); err != nil {
			return false, fmt.Errorf("rebuild plugin invocation facts by request: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`,
		pluginInvocationFactsRecount); err != nil {
		return false, fmt.Errorf("record plugin invocation fact recount marker: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// taskSegmentTokensRecount marks step 8 of the P1t transition: the token columns
// of every stored task segment recounted by request (p1t-design §6.2). Until it
// exists TaskSegment.TokensReady is false.
const taskSegmentTokensRecount = "task_segment_tokens_v2"

// taskSegmentTokensBatchSize is the sessions one transaction of step 8 updates.
const taskSegmentTokensBatchSize = 100

// recountTaskSegmentTokensStep is step 8 of the P1t transition: the sessions of
// task_segment_facts, in session_id order, a batch per transaction, have the
// token columns of their stored segments recounted by request
// (taskSegmentTokensUpdateSQL). Only those two columns: the segments keep their
// keys, tool counts and activity types, which a session whose OTEL has expired
// could not rebuild.
//
// A cursor row taken FOR UPDATE, task_segment_tokens_cursor, holds the last
// session updated and advances in the batch's transaction, and the marker
// commits with the last batch, as in step 4. It reports progress as
// RequestSetMigrationStep documents.
//
// Without the facts' build marker (taskSegmentFactsBackfill) there is nothing to
// recount: the boot repair that builds the facts, BackfillTaskSegmentFacts,
// builds them with this binary's rules, and the step writes its marker alone.
func (s *PgStore) recountTaskSegmentTokensStep(ctx context.Context) (bool, error) {
	done, err := backfillMarked(ctx, s.pool, taskSegmentTokensRecount)
	if err != nil {
		return false, fmt.Errorf("read task segment token recount marker: %w", err)
	}
	if done {
		return false, nil
	}
	progressed := false
	for {
		finished, err := s.recountTaskSegmentTokensBatch(ctx)
		if err != nil {
			return progressed, err
		}
		progressed = true
		if finished {
			return true, nil
		}
	}
}

// recountTaskSegmentTokensBatch updates the next batch in one transaction and
// reports whether the walk is complete.
func (s *PgStore) recountTaskSegmentTokensBatch(ctx context.Context) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	done, err := backfillMarked(ctx, tx, taskSegmentTokensRecount)
	if err != nil {
		return false, fmt.Errorf("read task segment token recount marker: %w", err)
	}
	if done {
		return true, tx.Commit(ctx)
	}
	// The locks refreshTaskSegmentFacts and its callers take, in their order: the
	// shared source lock, the facts' shared lock, then the sessions' own in
	// session_id order. taskSegmentFactsBackfill is not bumped, so the keys are
	// theirs. CleanupOrphanSessionRecords does otherwise: it deletes orphaned
	// segment rows before its refresh takes the session locks.
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return false, err
	}
	if err := lockTaskSegmentFacts(ctx, tx, false); err != nil {
		return false, err
	}
	built, err := backfillMarked(ctx, tx, taskSegmentFactsBackfill)
	if err != nil {
		return false, fmt.Errorf("read task segment fact marker: %w", err)
	}
	finished := !built
	if built {
		var after string
		err := tx.QueryRow(ctx, `SELECT last_session_id FROM task_segment_tokens_cursor FOR UPDATE`).Scan(&after)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// As for step 4's cursor: the first batch creates the row, and a second
			// first batch alongside fails on the primary key and finds it on retry.
			if _, err := tx.Exec(ctx, `INSERT INTO task_segment_tokens_cursor (last_session_id) VALUES ('')`); err != nil {
				return false, fmt.Errorf("create task segment token recount cursor: %w", err)
			}
		case err != nil:
			return false, fmt.Errorf("read task segment token recount cursor: %w", err)
		}
		ids, err := querySessionIDs(ctx, tx, `SELECT DISTINCT session_id FROM task_segment_facts
			WHERE session_id > $1 ORDER BY session_id LIMIT $2`, after, taskSegmentTokensBatchSize)
		if err != nil {
			return false, fmt.Errorf("read task segment token recount batch: %w", err)
		}
		if len(ids) > 0 {
			if err := lockTaskSegmentSessions(ctx, tx, ids); err != nil {
				return false, err
			}
			if _, err := tx.Exec(ctx, taskSegmentTokensUpdateSQL, ids); err != nil {
				return false, fmt.Errorf("recount task segment tokens: %w", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE task_segment_tokens_cursor SET last_session_id = $1`,
				ids[len(ids)-1]); err != nil {
				return false, fmt.Errorf("advance task segment token recount cursor: %w", err)
			}
		}
		finished = len(ids) < taskSegmentTokensBatchSize
	}
	if finished {
		if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`,
			taskSegmentTokensRecount); err != nil {
			return false, fmt.Errorf("record task segment token recount marker: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return finished, nil
}

// completeRequestSetMigrationStep is step 9 of the P1t transition, the last: the
// completion marker alone, in one statement. The runner reaches it only once
// steps 1 to 8 have their markers, every stored aggregate recounted by request,
// and from its commit RequestSetMigrationStatus no longer reports the token rule
// pending. It reports whether it wrote the marker: false when it was already
// there.
func (s *PgStore) completeRequestSetMigrationStep(ctx context.Context) (bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`,
		requestSetMigrationDone)
	if err != nil {
		return false, fmt.Errorf("record request set migration marker: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
