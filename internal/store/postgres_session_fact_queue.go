package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// dirtyReason names a row of dirtyReasons, the session analysis queue's reason
// table.
type dirtyReason int

const (
	dirtySync dirtyReason = iota
	dirtyReenrich
	dirtyOtel
	dirtySnapshot
	dirtyExclusion
	dirtyPublishPeer
	dirtyIdentity
	dirtyImputed
	dirtyTaskType
	dirtyProjectHash
	dirtyDeletionPeer
	dirtyDeletionUser
	dirtyMerge
	dirtyOrphan
	dirtySnapshotPrune
	dirtyRetention
	dirtyBuild
	dirtyCopyGate
	dirtyClockRestart
	dirtyDemand
)

// dirtyParams is what one kind of bump writes into session_fact_dirty.
type dirtyParams struct {
	reason   int32         // the bit ORed into reasons
	debounce time.Duration // not_before moves this far past the bump ...
	maxDelay time.Duration // ... but no further than this past first_dirty_at
	priority int16         // 0 live, 1 bulk; a row keeps the lower
	removal  bool          // the bump's transaction deleted source rows of the session
	restart  bool          // the bump restarts the row's clock, as a removal does, with nothing removed
	rescan   bool          // scan_from_id 0: every row of the session is new to the copy scan
}

// dirtyReasons is the reason table of p1b-design §5.1, the one place a bump's
// schedule is decided: markSessionFactsDirty takes a row of it, never loose
// values. scan_from_id is 0 where the row says rescan, the least id of the rows
// the caller inserted when it passes them (sync), and NULL otherwise.
//
// The peer bit has two rows. A session whose copy markers a publish changed waits
// out the debounce like any edit; the neighbour of a deletion is due at once and
// scanned whole, since the lines it shared are gone.
//
// A removal restarts the row's clock (markSessionFactsRestartSQL says why). The
// clock restart is that restart without the removal, for the stale rows a
// retention run finds: no source row went, so no generation is recorded to hold
// a publish back, and it is a bulk reason so that a row keeps the priority it
// had -- the queue keeps the lower of the two, and at live priority a history
// the 30-minute rewrites queued as bulk would sort ahead of live bumps.
var dirtyReasons = [...]dirtyParams{
	dirtySync:          {reason: 1, debounce: 10 * time.Second, maxDelay: 2 * time.Minute},
	dirtyReenrich:      {reason: 2, debounce: time.Minute, maxDelay: 10 * time.Minute, priority: 1, rescan: true},
	dirtyOtel:          {reason: 4, debounce: 10 * time.Second, maxDelay: 2 * time.Minute},
	dirtySnapshot:      {reason: 8, debounce: 10 * time.Second, maxDelay: 2 * time.Minute},
	dirtyExclusion:     {reason: 16, debounce: 10 * time.Second, maxDelay: 2 * time.Minute},
	dirtyPublishPeer:   {reason: 256, debounce: 10 * time.Second, maxDelay: 2 * time.Minute},
	dirtyIdentity:      {reason: 32, debounce: 30 * time.Second, maxDelay: 10 * time.Minute, priority: 1},
	dirtyImputed:       {reason: 64, debounce: 30 * time.Second, maxDelay: 10 * time.Minute, priority: 1},
	dirtyTaskType:      {reason: 2048, debounce: 30 * time.Second, maxDelay: 10 * time.Minute, priority: 1},
	dirtyProjectHash:   {reason: 16384, debounce: 30 * time.Second, maxDelay: 10 * time.Minute, priority: 1},
	dirtyDeletionPeer:  {reason: 256, rescan: true},
	dirtyDeletionUser:  {reason: 8192, removal: true},
	dirtyMerge:         {reason: 4096, removal: true},
	dirtyOrphan:        {reason: 65536, removal: true},
	dirtySnapshotPrune: {reason: 32768, removal: true},
	dirtyRetention:     {reason: 128, removal: true},
	dirtyBuild:         {reason: 512, priority: 1, rescan: true},
	dirtyCopyGate:      {reason: 1024, priority: 1},
	dirtyClockRestart:  {reason: 131072, priority: 1, restart: true},
	dirtyDemand:        {reason: 262144}, // historical GET demand rows still drain normally
}

// markSessionFactsDirtySQL queues sessions for the worker in one statement
// (p1b-design §5.1).
//
// The generation of a row already queued is drawn inside the conflict update,
// once the row lock is held, as the usage rebuild queue draws its own
// (requestUsageRollupRebuild); a new row takes the column default. A session's
// generations so rise in the order its bumps commit. Drawn before the lock, a
// bump that committed later could write a smaller generation than a removal that
// committed first, and the publish would roll back for good (F2).
//
// Every queue write locks its rows in the same order (F3), byte order rather than
// the database collation: a transaction that queues sessions under different
// reasons does it in several statements (markSessionFactsDirtyInOrder), split by
// a Go sort, and the two orders have to agree. A session named twice is queued
// once with its least scan_from_id; min and LEAST skip NULL, so scan_from_id
// never passes the least new row not yet scanned. A tombstoned session is never
// queued: the sweep is about to delete its rows, and the caller's source lock
// keeps the tombstone check true through commit. attempts and retry_at are left
// alone here: a bump does not cancel a failure's backoff.
const markSessionFactsDirtySQL = `
	INSERT INTO session_fact_dirty AS d (session_id, scan_from_id, first_dirty_at, not_before, priority, reasons)
	SELECT u.s, u.f, now(), now() + make_interval(secs => $3::float8), $4::smallint, $5::int
	FROM (SELECT s, min(f) AS f FROM unnest($1::text[], $2::bigint[]) AS t(s, f) GROUP BY s) u
	WHERE u.s <> '' AND NOT EXISTS (SELECT 1 FROM deleted_sessions x WHERE x.session_id = u.s)
	ORDER BY u.s COLLATE "C"
	ON CONFLICT (session_id) DO UPDATE SET
		generation   = nextval('session_fact_generation'),
		scan_from_id = LEAST(d.scan_from_id, EXCLUDED.scan_from_id),
		not_before   = LEAST(now() + make_interval(secs => $3::float8), d.first_dirty_at + make_interval(secs => $6::float8)),
		priority     = LEAST(d.priority, EXCLUDED.priority),
		reasons      = d.reasons | EXCLUDED.reasons`

// markSessionFactsRestartSQL restarts the clock of the rows a bump just wrote,
// for a removal and for the clock restart (p1b-design §5.1). The rows are the
// ones the queue write above just locked, tombstones skipped alike. $2 says the
// bump is a removal, which also records the generation it wrote: a computation
// that read the session before that is not to be published.
//
// first_dirty_at is when the session first changed after its last computation
// -- in practice a time its owner was at work in it -- and a session whose source
// rows were just deleted must not keep that time in the queue, where with the
// worker off a row stays until someone empties the table. not_before is what
// §5.1 gives from the new first_dirty_at with the zero delays of every reason
// that restarts. A retry_at already past goes too: it holds nothing back -- the
// worker picks a row whose retry_at is null or past (§5.3) -- and is only a
// trace of when the worker last worked on the session. One still ahead is a
// live backoff and stays, so a bump still cancels no backoff.
//
// The cost is delay alone: the row sorts behind the rows of its priority queued
// before it, and a later bump of another reason can push it out by that reason's
// debounce. The max delay restarts from the removal as well, so a session bumped
// steadily is computed up to its max delay after the removal rather than after
// its first change, and until then its facts are gone and the view shows it
// building; the privacy reason wins.
//
// The retention reconciliation bumps stale rows for this restart alone
// (dirtyClockRestart): their source rows may all still be there, so no removal
// is recorded, and their facts stay.
const markSessionFactsRestartSQL = `
	UPDATE session_fact_dirty SET
		removal_generation = CASE WHEN $2::boolean THEN generation ELSE removal_generation END,
		first_dirty_at     = now(),
		not_before         = now(),
		retry_at           = CASE WHEN retry_at > now() THEN retry_at END
	WHERE session_id = ANY($1::text[])
	  AND NOT EXISTS (SELECT 1 FROM deleted_sessions x WHERE x.session_id = session_fact_dirty.session_id)`

// markSessionFactsDirty queues sessions whose facts are owed a recompute. It is
// the last write of the calling transaction that bears on session analysis, and
// that transaction took the source lock (lockSessionOverviewSourceMutation)
// first, before any row (p1b-design §5.4). scanFrom[i], when scanFrom is given,
// is the least session_records.id the caller inserted into ids[i], nil when it
// inserted none; only the sync bump passes it.
func markSessionFactsDirty(ctx context.Context, tx pgx.Tx, ids []string, scanFrom []*int64, reason dirtyReason) error {
	if len(ids) == 0 {
		return nil
	}
	p := dirtyReasons[reason]
	if reason != dirtyBuild && !p.restart {
		if err := bumpSessionTimelineSourceRevisions(ctx, tx, ids); err != nil {
			return fmt.Errorf("bump session analysis source revisions: %w", err)
		}
	}
	if reason == dirtyExclusion || reason == dirtyCopyGate {
		if err := bumpSessionTimelinePolicyEpoch(ctx, tx); err != nil {
			return fmt.Errorf("bump session analysis policy epoch: %w", err)
		}
	}
	return queueSessionFactsDirty(ctx, tx, ids, scanFrom, reason)
}

func queueSessionFactsDirty(ctx context.Context, tx pgx.Tx, ids []string, scanFrom []*int64, reason dirtyReason) error {
	if len(ids) == 0 {
		return nil
	}
	p := dirtyReasons[reason]
	from := make([]*int64, len(ids))
	if p.rescan {
		all := int64(0)
		for i := range from {
			from[i] = &all
		}
	} else {
		copy(from, scanFrom)
	}
	if _, err := tx.Exec(ctx, markSessionFactsDirtySQL,
		ids, from, p.debounce.Seconds(), p.priority, p.reason, p.maxDelay.Seconds()); err != nil {
		return fmt.Errorf("queue session facts: %w", err)
	}
	if p.removal || p.restart {
		if _, err := tx.Exec(ctx, markSessionFactsRestartSQL, ids, p.removal); err != nil {
			return fmt.Errorf("restart session facts queue clock: %w", err)
		}
	}
	return nil
}

// Source revisions and policy epochs are written before the dirty queue, which
// remains the last session-analysis write of the transaction.
func bumpSessionTimelineSourceRevisions(ctx context.Context, tx pgx.Tx, ids []string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO session_timeline_revisions(session_id,revision)
		SELECT s,1 FROM (SELECT DISTINCT unnest($1::text[]) AS s) u
		WHERE s<>'' AND NOT EXISTS(SELECT 1 FROM deleted_sessions d WHERE d.session_id=s)
		ORDER BY s COLLATE "C"
		ON CONFLICT(session_id) DO UPDATE SET revision=session_timeline_revisions.revision+1`, ids); err != nil {
		return err
	}
	return nil
}

func bumpSessionTimelinePolicyEpoch(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `INSERT INTO session_timeline_policy_epoch(only_row,epoch) VALUES(TRUE,1)
		ON CONFLICT(only_row) DO UPDATE SET epoch=session_timeline_policy_epoch.epoch+1`); err != nil {
		return err
	}
	return nil
}

// dirtyGroup is the sessions one reason queues.
type dirtyGroup struct {
	ids    []string
	reason dirtyReason
}

// markSessionFactsDirtyInOrder queues groups of sessions under their own
// reasons, in one session_id order across all of them. Group by group, a
// transaction holding a high session of one group would wait for a low session
// of the next while a writer that queues both, in order, holds the low one and
// waits for the high (F3). So the groups are merged, sorted, and written run by
// run; a session in two groups is written once for each, the second time under a
// lock it already holds.
func markSessionFactsDirtyInOrder(ctx context.Context, tx pgx.Tx, groups ...dirtyGroup) error {
	type member struct {
		id    string
		group int
	}
	var all []member
	for g, group := range groups {
		for _, id := range group.ids {
			all = append(all, member{id: id, group: g})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].id != all[j].id {
			return all[i].id < all[j].id
		}
		return all[i].group < all[j].group
	})
	policyChanged := false
	sourceSet := make(map[string]bool)
	for _, item := range all {
		reason := groups[item.group].reason
		params := dirtyReasons[reason]
		if item.id != "" && reason != dirtyBuild && !params.restart {
			sourceSet[item.id] = true
		}
		policyChanged = policyChanged || reason == dirtyExclusion || reason == dirtyCopyGate
	}
	sourceIDs := make([]string, 0, len(sourceSet))
	for id := range sourceSet {
		sourceIDs = append(sourceIDs, id)
	}
	sort.Strings(sourceIDs)
	if len(sourceIDs) > 0 {
		if err := bumpSessionTimelineSourceRevisions(ctx, tx, sourceIDs); err != nil {
			return fmt.Errorf("bump session analysis source revisions: %w", err)
		}
	}
	if policyChanged {
		if err := bumpSessionTimelinePolicyEpoch(ctx, tx); err != nil {
			return fmt.Errorf("bump session analysis policy epoch: %w", err)
		}
	}
	for start := 0; start < len(all); {
		end := start + 1
		for end < len(all) && all[end].group == all[start].group {
			end++
		}
		ids := make([]string, 0, end-start)
		for _, m := range all[start:end] {
			ids = append(ids, m.id)
		}
		reason := groups[all[start].group].reason
		if err := queueSessionFactsDirty(ctx, tx, ids, nil, reason); err != nil {
			return err
		}
		start = end
	}
	return nil
}
