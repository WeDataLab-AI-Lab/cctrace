package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The deletion contract of session analysis (p1b-design §6.1). A transaction that
// deletes source rows of a set of sessions D does, in that same transaction and
// whatever the feature flag says:
//
//  1. collect the sessions on the other side of every copy marker that names one
//     of D, before anything deletes the markers that say so;
//  2. delete those markers, so a copy whose original is gone is no longer left
//     out of its session's counts (F6);
//  3. while the copy predicate is on, refresh the derived rows of the sessions
//     that held copies of D's lines. This step does nothing yet: the markers are
//     written by the copy judgement that P1b-7 brings, so until then the table is
//     empty, no session counts anything as a copy, and there is no derived row
//     that a deleted marker could have changed. It belongs between steps 2 and 4
//     of every path that takes step 1 -- the tombstone, DeleteUserData, the
//     orphan cleanup (in its own plugin refresh, §6.2) and the retention
//     reconciliation;
//  4. delete D's facts, so nothing computed from the deleted rows stays readable;
//  5. queue D for a removal recompute and the neighbours of step 1 as peers,
//     rescanned whole (markSessionFactsDirtyInOrder).
//
// Which steps a path takes is p1b-design §6.2's table: a tombstone leaves step 4
// to the sweep and is never queued itself, and MergeUsers and the snapshot prune
// move or delete rows that no marker names, so they take steps 4 and 5 alone.

// takeCopyMarkers is steps 1 and 2 of the deletion contract for the sessions d:
// it returns the sessions on the other side of the copy markers that name one of
// d -- the copies of d's lines and the originals of d's copies, d itself left
// out -- and deletes those markers.
func takeCopyMarkers(ctx context.Context, tx pgx.Tx, d []string) ([]string, error) {
	if len(d) == 0 {
		return nil, nil
	}
	neighbours, err := querySessionIDs(ctx, tx, `
		SELECT DISTINCT n FROM (
			SELECT c.session_id AS n FROM session_record_copies c WHERE c.original_session_id = ANY($1::text[])
			UNION
			SELECT c.original_session_id FROM session_record_copies c WHERE c.session_id = ANY($1::text[])) x
		WHERE n <> ALL($1::text[])`, d)
	if err != nil {
		return nil, fmt.Errorf("collect copy neighbours: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_record_copies
		WHERE session_id = ANY($1::text[]) OR original_session_id = ANY($1::text[])`, d); err != nil {
		return nil, fmt.Errorf("delete copy markers: %w", err)
	}
	return neighbours, nil
}

// deleteSessionFacts is step 4 of the deletion contract: the facts of the
// sessions d, including unpublished timeline state and its source identifiers.
func deleteSessionFacts(ctx context.Context, tx pgx.Tx, d []string) error {
	if err := deleteSessionComputedFacts(ctx, tx, d); err != nil {
		return err
	}
	return deleteSessionAIRecords(ctx, tx, d)
}

// deleteSessionComputedFacts replaces deterministic facts during recomputation.
// Saved LLM content and active claims remain: new activity makes a saved record
// stale, while the claim's source/hash checks fence a concurrent generation.
func deleteSessionComputedFacts(ctx context.Context, tx pgx.Tx, d []string) error {
	if len(d) == 0 {
		return nil
	}
	for _, table := range []string{"session_commit_facts", "session_task_facts", "session_analysis_facts", "session_turn_facts", "session_activity_segments", "session_timeline_members", "session_timeline_provenance"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE session_id = ANY($1::text[])`, d); err != nil {
			return fmt.Errorf("delete %s: %w", table, err)
		}
	}
	// session_timeline_revisions stays. Resetting a counter to absent here would
	// let a stale expected state match again (ABA). Only the strict sweep removes
	// it, after the permanent tombstone that ReplaceSessionTimelineMapping refuses
	// under the same per-session lock.
	return nil
}

// deleteSessionAIRecords is part of the source deletion transaction. Jobs are
// removed first because their claim is the compare-and-swap fence for publish.
func deleteSessionAIRecords(ctx context.Context, tx pgx.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	for _, table := range []string{"session_ai_record_jobs", "session_ai_records"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE session_id = ANY($1::text[])`, ids); err != nil {
			return fmt.Errorf("delete %s: %w", table, err)
		}
	}
	return nil
}
