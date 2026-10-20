package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The index is maintained for incoming snapshots, not backfilled at migration.
// Readers must treat an absent completion marker as an unknown claim set.
const sessionCommitClaimsBackfill = "session_commit_claims_v1"

func sessionCommitClaimsComplete(ctx context.Context, tx pgx.Tx) (bool, error) {
	var complete bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name=$1)`, sessionCommitClaimsBackfill).Scan(&complete)
	return complete, err
}

type snapshotClaimIdentity struct {
	SessionID, UserID, ProfileEmail string
}

type snapshotClaimKey struct {
	Repository, Hash string
}

func snapshotIdentity(s *SessionSnapshot) snapshotClaimIdentity {
	return snapshotClaimIdentity{s.SessionID, s.UserID, s.ProfileEmail}
}

// Only indexing evidence is decoded here. Subjects and commands never enter the
// index, and an invalid item cannot become an affirmative ownership assertion.
func snapshotClaimKeys(raw []byte) []snapshotClaimKey {
	var doc struct {
		Commits struct {
			Trees []struct {
				Repository string `json:"repository"`
			} `json:"trees"`
			Items []struct {
				Hash  string `json:"hash"`
				Tree  *int   `json:"tree"`
				Proof string `json:"proof"`
			} `json:"items"`
		} `json:"commits"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	var keys []snapshotClaimKey
	for _, item := range doc.Commits.Items {
		if item.Proof != "output_hash" || item.Tree == nil || *item.Tree < 0 || *item.Tree >= len(doc.Commits.Trees) || (len(item.Hash) != 40 && len(item.Hash) != 64) {
			continue
		}
		if strings.IndexFunc(item.Hash, func(r rune) bool { return r < '0' || r > '9' && r < 'a' || r > 'f' }) >= 0 {
			continue
		}
		repository := doc.Commits.Trees[*item.Tree].Repository
		if repository != "" && !strings.ContainsRune(repository, 0) {
			keys = append(keys, snapshotClaimKey{repository, item.Hash})
		}
	}
	return keys
}

func claimIdentityLockKey(id snapshotClaimIdentity) string {
	b, _ := json.Marshal([]string{id.SessionID, id.UserID, id.ProfileEmail})
	return string(b)
}

// Lock each actual advisory id in one total order, including hash collisions.
// Identity and claim locks use different namespaces. The complete original
// session batch is locked first; peer session locks are never acquired.
func lockSnapshotClaimKeys(ctx context.Context, tx pgx.Tx, namespace int32, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT hashtext(k) AS lock_id FROM unnest($1::text[]) k ORDER BY lock_id`, keys)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int32])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int,$2::int)`, namespace, id); err != nil {
			return err
		}
	}
	return nil
}

func lockClaimIdentities(ctx context.Context, tx pgx.Tx, identities []snapshotClaimIdentity) error {
	keys := make([]string, 0, len(identities))
	for _, id := range identities {
		keys = append(keys, claimIdentityLockKey(id))
	}
	return lockSnapshotClaimKeys(ctx, tx, 393, keys)
}

func lockClaimPeers(ctx context.Context, tx pgx.Tx, keys []snapshotClaimKey) ([]string, error) {
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Repository != keys[j].Repository {
			return keys[i].Repository < keys[j].Repository
		}
		return keys[i].Hash < keys[j].Hash
	})
	var lockKeys, repositories, hashes []string
	for i, key := range keys {
		if i > 0 && keys[i-1] == key {
			continue
		}
		b, _ := json.Marshal([]string{key.Repository, key.Hash})
		lockKeys = append(lockKeys, string(b))
		repositories, hashes = append(repositories, key.Repository), append(hashes, key.Hash)
	}
	if err := lockSnapshotClaimKeys(ctx, tx, 394, lockKeys); err != nil {
		return nil, err
	}
	peers, err := querySessionIDs(ctx, tx, `SELECT DISTINCT c.session_id
		FROM session_commit_claims c JOIN unnest($1::text[],$2::text[]) k(repository,commit_hash)
		ON c.repository=k.repository AND c.commit_hash=k.commit_hash
		WHERE NOT EXISTS(SELECT 1 FROM deleted_sessions d WHERE d.session_id=c.session_id)`, repositories, hashes)
	if err != nil {
		return nil, err
	}
	return uniqueSortedSessionIDs(peers), nil
}

// prepareSnapshotClaims resolves the batch's last valid winner while holding the
// exact snapshot identities. Rejected older snapshots do not affect peers or the
// dirty queue. Original batch session locks precede identity and claim locks.
func prepareSnapshotClaims(ctx context.Context, tx pgx.Tx, snapshots []*SessionSnapshot, sessions []string) ([]*SessionSnapshot, []string, int, error) {
	liveSessions, err := liveSessionIDs(ctx, tx, sessions)
	if err != nil {
		return nil, nil, 0, err
	}
	var identities []snapshotClaimIdentity
	for _, s := range snapshots {
		if s != nil && liveSessions[s.SessionID] {
			if !json.Valid(sanitizeJSONNUL(s.Snapshot)) {
				return nil, nil, 0, fmt.Errorf("invalid session snapshot JSON")
			}
			identities = append(identities, snapshotIdentity(s))
		}
	}
	if err := lockClaimIdentities(ctx, tx, identities); err != nil {
		return nil, nil, 0, err
	}
	times := map[snapshotClaimIdentity]time.Time{}
	old := map[snapshotClaimIdentity][]byte{}
	for _, id := range identities {
		if _, ok := times[id]; ok {
			continue
		}
		var at time.Time
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT observed_at,snapshot FROM session_snapshots
			WHERE session_id=$1 AND user_id=$2 AND profile_email=$3`, id.SessionID, id.UserID, id.ProfileEmail).Scan(&at, &raw)
		if err != nil && err != pgx.ErrNoRows {
			return nil, nil, 0, err
		}
		times[id], old[id] = at, raw
	}
	winners := map[snapshotClaimIdentity]*SessionSnapshot{}
	accepted := map[snapshotClaimIdentity]int{}
	for _, s := range snapshots {
		if s == nil || !liveSessions[s.SessionID] {
			continue
		}
		id := snapshotIdentity(s)
		at := s.ObservedAt.Truncate(time.Microsecond)
		if !at.Before(times[id]) {
			times[id], winners[id] = at, s
			accepted[id]++
		}
	}
	var keys []snapshotClaimKey
	var result []*SessionSnapshot
	for id, s := range winners {
		indexed, err := readSnapshotClaimKeys(ctx, tx, `session_id=$1 AND user_id=$2 AND profile_email=$3`, id.SessionID, id.UserID, id.ProfileEmail)
		if err != nil {
			return nil, nil, 0, err
		}
		keys = append(keys, indexed...)
		keys = append(keys, snapshotClaimKeys(old[id])...)
		keys = append(keys, snapshotClaimKeys(sanitizeJSONNUL(s.Snapshot))...)
		result = append(result, s)
	}
	peers, err := lockClaimPeers(ctx, tx, keys)
	if err != nil {
		return nil, nil, 0, err
	}
	// READ COMMITTED re-read after waiting for claim locks. A source mutation
	// outside the cooperating writer set must not introduce an unlocked key.
	for id := range winners {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT snapshot FROM session_snapshots WHERE session_id=$1 AND user_id=$2 AND profile_email=$3`, id.SessionID, id.UserID, id.ProfileEmail).Scan(&raw)
		if err != nil && err != pgx.ErrNoRows {
			return nil, nil, 0, err
		}
		if !bytes.Equal(raw, old[id]) {
			return nil, nil, 0, &pgconn.PgError{Code: "40001", Message: "snapshot claim source changed while acquiring fences"}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return claimIdentityLockKey(snapshotIdentity(result[i])) < claimIdentityLockKey(snapshotIdentity(result[j]))
	})
	stored := 0
	var live []*SessionSnapshot
	for _, s := range result {
		var deleted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deleted_sessions WHERE session_id=$1)`, s.SessionID).Scan(&deleted); err != nil {
			return nil, nil, 0, err
		}
		if !deleted {
			live = append(live, s)
			stored += accepted[snapshotIdentity(s)]
		}
	}
	return live, peers, stored, nil
}

func replaceSnapshotClaims(ctx context.Context, tx pgx.Tx, id snapshotClaimIdentity) error {
	if _, err := tx.Exec(ctx, `DELETE FROM session_commit_claims WHERE session_id=$1 AND user_id=$2 AND profile_email=$3`, id.SessionID, id.UserID, id.ProfileEmail); err != nil {
		return err
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT snapshot FROM session_snapshots s WHERE session_id=$1 AND user_id=$2 AND profile_email=$3
		AND NOT EXISTS(SELECT 1 FROM deleted_sessions d WHERE d.session_id=s.session_id)`, id.SessionID, id.UserID, id.ProfileEmail).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	for _, key := range snapshotClaimKeys(raw) {
		if _, err := tx.Exec(ctx, `INSERT INTO session_commit_claims(session_id,user_id,profile_email,repository,commit_hash)
			VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, id.SessionID, id.UserID, id.ProfileEmail, key.Repository, key.Hash); err != nil {
			return fmt.Errorf("replace snapshot claims: %w", err)
		}
	}
	return nil
}

// Admin paths hold the source lock exclusively before discovering identities.
// Sweep holds the shared source lock and original session lock before discovery.
func prepareSessionClaimMutation(ctx context.Context, tx pgx.Tx, sessions []string, mergeProfiles ...string) ([]string, error) {
	if err := lockSessionDeletionTargets(ctx, tx, sessions); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT session_id,user_id,profile_email,snapshot FROM session_snapshots WHERE session_id=ANY($1::text[])`, sessions)
	if err != nil {
		return nil, err
	}
	var identities []snapshotClaimIdentity
	var keys []snapshotClaimKey
	for rows.Next() {
		var id snapshotClaimIdentity
		var raw []byte
		if err := rows.Scan(&id.SessionID, &id.UserID, &id.ProfileEmail, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		identities = append(identities, id)
		for _, profile := range mergeProfiles {
			identities = append(identities, snapshotClaimIdentity{id.SessionID, id.UserID, profile})
		}
		keys = append(keys, snapshotClaimKeys(raw)...)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := lockClaimIdentities(ctx, tx, identities); err != nil {
		return nil, err
	}
	indexed, err := readSnapshotClaimKeys(ctx, tx, `session_id=ANY($1::text[])`, sessions)
	if err != nil {
		return nil, err
	}
	return lockClaimPeers(ctx, tx, append(keys, indexed...))
}

func readSnapshotClaimKeys(ctx context.Context, tx pgx.Tx, predicate string, args ...any) ([]snapshotClaimKey, error) {
	rows, err := tx.Query(ctx, `SELECT repository,commit_hash FROM session_commit_claims WHERE `+predicate, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (snapshotClaimKey, error) {
		var key snapshotClaimKey
		err := row.Scan(&key.Repository, &key.Hash)
		return key, err
	})
}

func rebuildSessionClaims(ctx context.Context, tx pgx.Tx, sessions []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM session_commit_claims WHERE session_id=ANY($1::text[])`, sessions); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT session_id,user_id,profile_email FROM session_snapshots WHERE session_id=ANY($1::text[])`, sessions)
	if err != nil {
		return err
	}
	identities, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (snapshotClaimIdentity, error) {
		var id snapshotClaimIdentity
		err := row.Scan(&id.SessionID, &id.UserID, &id.ProfileEmail)
		return id, err
	})
	if err != nil {
		return err
	}
	for _, id := range identities {
		if err := replaceSnapshotClaims(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}
