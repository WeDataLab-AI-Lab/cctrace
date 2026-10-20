package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PostgreSQL's built-in sha256(bytea) needs no extension. Hash the database's
// JSONB representation, not the client's whitespace or Go's JSON encoding.
const claimSourceWitnessSQL = `sha256(convert_to(s.snapshot::text,'UTF8'))`
const claimCoverageMatchesSQL = `c.observed_at=s.observed_at AND c.source_witness=` + claimSourceWitnessSQL
const claimBackfillBudget = 10 * time.Second
const claimFinalizeBudget = 5 * time.Second

// BackfillSessionCommitClaims processes at most limit identities, then attempts
// finalization only after no work remains. The fact worker schedules it after
// the request-set and overview prerequisites are ready.
// Coverage, including zero-claim snapshots, is the durable resume state.
func (s *PgStore) BackfillSessionCommitClaims(ctx context.Context, limit int) (bool, error) {
	if limit < 1 || limit > 1000 {
		return false, fmt.Errorf("claim backfill limit must be between 1 and 1000")
	}
	ctx, cancel := context.WithTimeout(ctx, claimBackfillBudget)
	defer cancel()
	worked, done, err := s.backfillSessionCommitClaimsBatch(ctx, limit)
	if err != nil || done || worked {
		return done, err
	}
	return s.finalizeSessionCommitClaims(ctx)
}

func (s *PgStore) backfillSessionCommitClaimsBatch(ctx context.Context, limit int) (bool, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback(ctx)
	if err = lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return false, false, err
	}
	done, err := sessionCommitClaimsComplete(ctx, tx)
	if err != nil || done {
		return false, done, err
	}
	rows, err := tx.Query(ctx, `SELECT s.session_id,s.user_id,s.profile_email FROM session_snapshots s
 LEFT JOIN session_commit_claim_coverage c USING(session_id,user_id,profile_email)
 WHERE (NOT EXISTS(SELECT 1 FROM deleted_sessions d WHERE d.session_id=s.session_id)
 AND NOT COALESCE((`+claimCoverageMatchesSQL+`),false))
 OR (EXISTS(SELECT 1 FROM deleted_sessions d WHERE d.session_id=s.session_id)
 AND EXISTS(SELECT 1 FROM session_commit_claims k WHERE k.session_id=s.session_id
 AND k.user_id=s.user_id AND k.profile_email=s.profile_email))
 ORDER BY s.session_id COLLATE "C",s.user_id COLLATE "C",s.profile_email COLLATE "C" LIMIT $1`, limit)
	if err != nil {
		return false, false, err
	}
	identities, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (snapshotClaimIdentity, error) {
		var id snapshotClaimIdentity
		err := r.Scan(&id.SessionID, &id.UserID, &id.ProfileEmail)
		return id, err
	})
	if err != nil {
		return false, false, err
	}
	if len(identities) == 0 {
		return false, false, nil
	}
	var sessions []string
	for _, id := range identities {
		sessions = append(sessions, id.SessionID)
	}
	// This helper re-reads current snapshots after the original session locks,
	// includes old indexed keys, and acquires identity and claim fences in order.
	peers, err := prepareSessionClaimMutation(ctx, tx, sessions)
	if err != nil {
		return false, false, err
	}
	for _, id := range identities {
		if err = replaceSnapshotClaims(ctx, tx, id); err != nil {
			return false, false, err
		}
		if err = recordSnapshotClaimCoverage(ctx, tx, id); err != nil {
			return false, false, err
		}
	}
	if err = markSessionFactsDirtyInOrder(ctx, tx, dirtyGroup{ids: sessions, reason: dirtySnapshot}, dirtyGroup{ids: peers, reason: dirtyDeletionPeer}); err != nil {
		return false, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func recordSnapshotClaimCoverage(ctx context.Context, tx pgx.Tx, id snapshotClaimIdentity) error {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT snapshot FROM session_snapshots s
		WHERE session_id=$1 AND user_id=$2 AND profile_email=$3
		AND NOT EXISTS(SELECT 1 FROM deleted_sessions d WHERE d.session_id=s.session_id)`, id.SessionID, id.UserID, id.ProfileEmail).Scan(&raw)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	actual, err := readSnapshotClaimKeys(ctx, tx, `session_id=$1 AND user_id=$2 AND profile_email=$3`, id.SessionID, id.UserID, id.ProfileEmail)
	if err != nil {
		return err
	}
	if !sameSnapshotClaimKeys(snapshotClaimKeys(raw), actual) {
		return fmt.Errorf("snapshot claim parity failed before coverage")
	}
	// A concurrent delete may have committed between discovery and session lock.
	if _, err := tx.Exec(ctx, `DELETE FROM session_commit_claim_coverage WHERE session_id=$1 AND user_id=$2 AND profile_email=$3`, id.SessionID, id.UserID, id.ProfileEmail); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO session_commit_claim_coverage(session_id,user_id,profile_email,observed_at,source_witness)
 SELECT s.session_id,s.user_id,s.profile_email,s.observed_at,`+claimSourceWitnessSQL+` FROM session_snapshots s
 WHERE s.session_id=$1 AND s.user_id=$2 AND s.profile_email=$3
 AND NOT EXISTS(SELECT 1 FROM deleted_sessions d WHERE d.session_id=s.session_id)
 ON CONFLICT(session_id,user_id,profile_email) DO UPDATE SET observed_at=EXCLUDED.observed_at,source_witness=EXCLUDED.source_witness`, id.SessionID, id.UserID, id.ProfileEmail)
	return err
}

func sameSnapshotClaimKeys(expected, actual []snapshotClaimKey) bool {
	a, b := map[snapshotClaimKey]bool{}, map[snapshotClaimKey]bool{}
	for _, k := range expected {
		a[k] = true
	}
	for _, k := range actual {
		b[k] = true
	}
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// Final coverage and both directions of claim parity are checked while every
// cooperating snapshot writer is excluded. No marker is written on mismatch or
// cancellation. A mismatch invalidates only its coverage for the next batch.
func (s *PgStore) finalizeSessionCommitClaims(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, claimFinalizeBudget)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err = lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return false, err
	}
	done, err := sessionCommitClaimsComplete(ctx, tx)
	if err != nil || done {
		return done, err
	}
	rows, err := tx.Query(ctx, `SELECT s.session_id,s.user_id,s.profile_email,s.snapshot,
 COALESCE((`+claimCoverageMatchesSQL+`),false),
 EXISTS(SELECT 1 FROM deleted_sessions d WHERE d.session_id=s.session_id),
 COALESCE((SELECT jsonb_agg(jsonb_build_object('Repository',k.repository,'Hash',k.commit_hash))
 FROM session_commit_claims k WHERE k.session_id=s.session_id AND k.user_id=s.user_id AND k.profile_email=s.profile_email),'[]'::jsonb)
 FROM session_snapshots s LEFT JOIN session_commit_claim_coverage c USING(session_id,user_id,profile_email)`)
	if err != nil {
		return false, err
	}
	var invalid []snapshotClaimIdentity
	for rows.Next() {
		if err = ctx.Err(); err != nil {
			rows.Close()
			return false, err
		}
		var id snapshotClaimIdentity
		var raw, claims []byte
		var covered, deleted bool
		if err = rows.Scan(&id.SessionID, &id.UserID, &id.ProfileEmail, &raw, &covered, &deleted, &claims); err != nil {
			rows.Close()
			return false, err
		}
		var actual []snapshotClaimKey
		if err = json.Unmarshal(claims, &actual); err != nil {
			rows.Close()
			return false, err
		}
		if deleted {
			if len(actual) > 0 {
				invalid = append(invalid, id)
			}
			continue
		}
		if !covered || !sameSnapshotClaimKeys(snapshotClaimKeys(raw), actual) {
			invalid = append(invalid, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if len(invalid) > 0 {
		for _, id := range invalid {
			if _, err = tx.Exec(ctx, `DELETE FROM session_commit_claim_coverage WHERE session_id=$1 AND user_id=$2 AND profile_email=$3`, id.SessionID, id.UserID, id.ProfileEmail); err != nil {
				return false, err
			}
		}
		return false, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES($1) ON CONFLICT DO NOTHING`, sessionCommitClaimsBackfill); err != nil {
		return false, err
	}
	if err = requeueSessionFactPolicyChange(ctx, tx); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
