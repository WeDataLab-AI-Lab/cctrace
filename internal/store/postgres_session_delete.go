package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrEmptySessionID rejects a delete keyed on the empty session id.
//
// This is not defensive boilerplate. session_records legitimately holds rows with
// session_id set to the empty string (metadata lines that belong to no session),
// and on prod
// otel_metrics had 3.06M of its 3.67M rows under that same empty id -- data
// belonging to every user of the deployment. `DELETE ... WHERE session_id = $1`
// with $1 empty is therefore not a no-op, it is a near-total wipe that returns a
// success code. The guard exists at four layers (this check, the CHECK constraint
// on deleted_sessions, the non-empty guard in every statement below, and the
// handler) because one layer is one typo away from gone.
var ErrEmptySessionID = errors.New("session id is required")

// ErrEmptyProjectHash guards the project-keyed delete for the same reason
// ErrEmptySessionID guards the session-keyed one: an empty hash is not an empty
// filter. session_records holds rows under it, and a statement that took one would
// sweep every project at once.
var ErrEmptyProjectHash = errors.New("project hash is required")

// DeleteSessionResult reports what a delete actually removed, per table, so the
// caller can show it rather than claim it.
type DeleteSessionResult struct {
	// MarkedSessions counts what this call took out of view, the clicked session
	// included. Row counts are absent on purpose: the rows are still there when this
	// returns, and reporting a number for work that has not happened is the one lie
	// a delete confirmation must not tell.
	MarkedSessions int64  `json:"marked_sessions"`
	ProjectHash    string `json:"project_hash"`
	ProjectName    string `json:"project_name"`
	ProjectBlocked bool   `json:"project_blocked"`
	// PurgedSessions counts the project's OTHER sessions taken alongside this one.
	// Zero when the purge was not asked for.
	PurgedSessions int64 `json:"purged_sessions"`
}

// BlockedProject is one entry in the "never collect this again" list.
type BlockedProject struct {
	ProjectHash string `json:"project_hash"`
	ProjectName string `json:"project_name"`
	// Subpaths lists the directories inside the repository that sessions started in,
	// comma separated, empty when they all started at the root. Shown because a block
	// covering more than one of them is the one case where "project" and "directory
	// I was in" are not the same thing, and hiding that makes the entry look wrong.
	Subpaths  string `json:"subpaths"`
	CreatedBy string `json:"created_by"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"created_at"`
}

// DeletionPolicy is the single-row deletion_policy table.
type DeletionPolicy struct {
	AllowOwnerDelete bool   `json:"allow_owner_delete"`
	UpdatedBy        string `json:"updated_by"`
	UpdatedAt        string `json:"updated_at"`
}

// IngestBlocklist is the set ingest consults. It is loaded whole and cached by the
// caller: the ingest path handles one record at a time and cannot afford a query
// per record, and both sets are small (a tombstone per deleted session, a row per
// blocked project) next to the tables they protect.
type IngestBlocklist struct {
	DeletedSessions map[string]bool
	BlockedProjects map[string]bool
	// ExcludedAccounts and ExcludedEmails are what an excluded account can be
	// recognised by at ingest (#715): billing ids -- excluded directly or reached
	// through an excluded address -- and the addresses, lowercased.
	ExcludedAccounts []BillingAccountRef
	ExcludedEmails   []string
}

// SessionOwner returns who a session belongs to, for the ownership check. Reading
// session_records rather than otel_events is deliberate: 67% of sessions never
// emit OTEL at all, and a session with no events would otherwise look ownerless
// and fall through to "not yours".
//
// /api/sync stores the identity the client sends, so a row under someone else's
// session_id can be planted. A session whose rows name more than one non-empty
// user_id or profile_email therefore has no owner; empty values are rows synced
// before that field existed and do not count as a second owner. Measured on the
// 2026-09-15 prod snapshot: 0 of 8,937 sessions carry two identities.
func (s *PgStore) SessionOwner(ctx context.Context, sessionID string) (profileEmail string, userID string, err error) {
	if sessionID == "" {
		return "", "", ErrEmptySessionID
	}
	var rows, emails, userIDs int64
	err = s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(DISTINCT NULLIF(profile_email,'')),
		       count(DISTINCT NULLIF(user_id,'')),
		       COALESCE(max(NULLIF(profile_email,'')),''),
		       COALESCE(max(NULLIF(user_id,'')),'')
		FROM session_records
		WHERE session_id = $1 AND session_id <> ''`, sessionID).Scan(&rows, &emails, &userIDs, &profileEmail, &userID)
	if err != nil {
		return "", "", fmt.Errorf("session owner: %w", err)
	}
	if rows == 0 || emails > 1 || userIDs > 1 {
		return "", "", nil
	}
	return profileEmail, userID, nil
}

// DeleteSession removes one session from every table that stores it and records a
// tombstone so re-syncing cannot bring it back.
//
// The project identity is read BEFORE the delete: it lives on the session_records
// rows this is about to remove, so afterwards there is nothing left to ask.
//
// Everything runs in one transaction. A partial delete is the worst outcome
// available here -- rows gone from one table, still present in another, and a
// tombstone that may or may not have been written -- so either all of it lands or
// none of it does.
// DeleteSession also accepts purgeProject, which removes the rest of the project's
// stored sessions in the same transaction.
//
// Purging and blocking stay separate flags because they answer different
// questions -- "erase what you already have" and "stop accepting more" -- and a
// caller can want either without the other. They run in one transaction so the
// half-done state, where the clicked session is gone and its siblings are not,
// cannot be what the user is left looking at.
// DeleteSession marks a session gone and returns. It does not remove any rows.
//
// Removing them takes 15 to 30 seconds: otel_events and otel_metrics are
// compressed hypertables, and a DELETE has to decompress every batch holding a
// matching row -- a measured purge of 1,418 rows decompressed 1,095,980 tuples and
// the statement alone ran 14.5s. Doing that while a person waits on a modal is
// what the person experiences as the delete.
//
// So the transaction writes two small rows instead. The tombstone in
// deleted_sessions is what ingest consults, and the copy in excluded_sessions is
// what the read path already anti-joins -- visible_session_records has filtered on
// that table since #298, so the session leaves every list the moment this commits,
// at no added query cost.
//
// The rows are reclaimed by SweepDeletedSessions afterwards. Until it runs, the
// session is invisible but its events still count toward cost and usage totals:
// those read visible_events, and adding the same anti-join there measured
// 72ms -> 161ms on a seven-day rollup with a THREE-row tombstone table. The cost
// is in evaluating the anti-join per chunk, not in the rows, so keeping the table
// small would not have helped. Seconds of stale totals is the better trade.
func (s *PgStore) DeleteSession(ctx context.Context, sessionID, actor, reason string, blockProject, purgeProject bool) (*DeleteSessionResult, error) {
	if sessionID == "" {
		return nil, ErrEmptySessionID
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("delete session: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	res := &DeleteSessionResult{}
	if err := beginStrictDeletion(ctx, tx); err != nil {
		return nil, err
	}

	err = tx.QueryRow(ctx, `
		SELECT COALESCE(sr.project_hash,''), COALESCE(p.project_name,'')
		FROM session_records sr
		LEFT JOIN projects p ON p.project_hash = sr.project_hash
		WHERE sr.session_id = $1 AND sr.session_id <> '' AND sr.project_hash <> ''
		LIMIT 1`, sessionID).Scan(&res.ProjectHash, &res.ProjectName)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("delete session: resolve project: %w", err)
	}

	ids := []string{sessionID}
	if purgeProject && res.ProjectHash != "" {
		siblings, err := projectSessionIDsTx(ctx, tx, res.ProjectHash, sessionID)
		if err != nil {
			return nil, err
		}
		ids = append(ids, siblings...)
		res.PurgedSessions = int64(len(siblings))
	}

	if err := tombstoneTx(ctx, tx, ids, res.ProjectHash, actor, reason); err != nil {
		return nil, err
	}

	if blockProject && res.ProjectHash != "" {
		if err := blockProjectTx(ctx, tx, res.ProjectHash, res.ProjectName, actor, reason); err != nil {
			return nil, err
		}
		res.ProjectBlocked = true
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("delete session: commit: %w", err)
	}
	res.MarkedSessions = int64(len(ids))
	return res, nil
}

// DeleteProject removes every session in the given projects, optionally refusing
// them from then on.
//
// A slice, not one hash: the picker shows identities (#304), and one row can stand
// for several project hashes -- the same repository opened from different
// worktrees. Splitting that into one request per hash would let half of them
// succeed, which is the failure the session dialog was built to avoid.
//
// DeleteSession already does this when asked to purge, but it needs a session to
// start from. The picker has no session in hand -- and a project whose sessions are
// all gone has none to offer -- so the project is the key here.
//
// The name is read from projects rather than session_records: a project reachable
// from the picker has a row there, and after a purge session_records may hold only
// metadata rows for it, which carry no session to name.
func (s *PgStore) DeleteProject(ctx context.Context, projectHashes []string, actor, reason string, blockProject bool) (*DeleteSessionResult, error) {
	hashes := make([]string, 0, len(projectHashes))
	for _, h := range projectHashes {
		if h != "" {
			hashes = append(hashes, h)
		}
	}
	if len(hashes) == 0 {
		return nil, ErrEmptyProjectHash
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("delete project: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	res := &DeleteSessionResult{ProjectHash: hashes[0]}
	if err := beginStrictDeletion(ctx, tx); err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(max(repository_name), ''), NULLIF(max(project_name), ''), '')
		FROM projects WHERE project_hash = ANY($1)`, hashes).Scan(&res.ProjectName)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("delete project: resolve name: %w", err)
	}

	for _, hash := range hashes {
		// Empty exclude: there is no clicked session to hold back.
		ids, err := projectSessionIDsTx(ctx, tx, hash, "")
		if err != nil {
			return nil, err
		}
		// A project with nothing left to tombstone still gets blocked when asked. That
		// is the case the picker hits most: the sessions are already gone and what the
		// user wants is for the project to stop coming back.
		if len(ids) > 0 {
			if err := tombstoneTx(ctx, tx, ids, hash, actor, reason); err != nil {
				return nil, err
			}
			res.MarkedSessions += int64(len(ids))
		}
		if blockProject {
			if err := blockProjectTx(ctx, tx, hash, res.ProjectName, actor, reason); err != nil {
				return nil, err
			}
			res.ProjectBlocked = true
		}
	}
	res.PurgedSessions = res.MarkedSessions

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("delete project: commit: %w", err)
	}
	return res, nil
}

// projectSessionIDsTx lists a project's other sessions.
func projectSessionIDsTx(ctx context.Context, tx pgx.Tx, projectHash, exclude string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT session_id FROM session_records
		WHERE project_hash = $1 AND session_id <> '' AND session_id <> $2`, projectHash, exclude)
	if err != nil {
		return nil, fmt.Errorf("delete session: list project sessions: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("delete session: scan project session: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// tombstoneTx records the decision in both tables it has to reach.
//
// deleted_sessions is the durable record ingest reads. excluded_sessions is the
// one the read path already anti-joins; writing there is what makes the session
// disappear now rather than after the sweep.
//
// ON CONFLICT DO UPDATE, not DO NOTHING: a session can be deleted, slip back in
// through a race with an in-flight sync, and be deleted again. The second delete
// should refresh who did it and why.
//
// swept_at goes back to NULL with them. That is the whole point of the second
// delete: rows returned, so the tombstone has work again. Leaving the mark set
// would hide the new rows from the 60-second backstop, which finds its work by
// reading that column -- they would sit until the daily verification noticed,
// invisible but not deleted, which is the state this mechanism exists to prevent.
func tombstoneTx(ctx context.Context, tx pgx.Tx, ids []string, projectHash, actor, reason string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := lockSessionDeletionTargets(ctx, tx, ids); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO deleted_sessions (session_id, project_hash, deleted_by, reason)
		SELECT unnest($1::text[]), $2, $3, $4
		ON CONFLICT (session_id) DO UPDATE
		SET project_hash = EXCLUDED.project_hash,
			deleted_by   = EXCLUDED.deleted_by,
			reason       = EXCLUDED.reason,
			deleted_at   = now(),
			swept_at     = NULL,
			verified_at  = NULL`, ids, projectHash, actor, reason); err != nil {
		return fmt.Errorf("delete session: tombstone: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO excluded_sessions (session_id)
		SELECT unnest($1::text[]) ON CONFLICT DO NOTHING`, ids); err != nil {
		return fmt.Errorf("delete session: hide: %w", err)
	}
	// The overview rollup is derived and write-maintained, so hiding the source rows
	// does not by itself change what it holds. Re-derive these sessions inside the same
	// transaction: both source arms now exclude them, so the refresh deletes the rows
	// and finds nothing to put back.
	if err := refreshSessionOverviewRollups(ctx, tx, ids); err != nil {
		return fmt.Errorf("delete session: refresh overview rollup: %w", err)
	}
	return nil
}

func blockProjectTx(ctx context.Context, tx pgx.Tx, projectHash, projectName, actor, reason string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO blocked_projects (project_hash, project_name, created_by, reason)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (project_hash) DO UPDATE
		SET project_name = CASE WHEN EXCLUDED.project_name <> '' THEN EXCLUDED.project_name
			ELSE blocked_projects.project_name END,
			created_by = EXCLUDED.created_by,
			reason     = EXCLUDED.reason,
			created_at = now()`,
		projectHash, projectName, actor, reason); err != nil {
		return fmt.Errorf("block project: %w", err)
	}
	return nil
}

// BlockProject adds a project to the never-collect list on its own, without a
// delete attached.
func (s *PgStore) BlockProject(ctx context.Context, projectHash, projectName, actor, reason string) error {
	if projectHash == "" {
		return fmt.Errorf("project hash is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("block project: begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := blockProjectTx(ctx, tx, projectHash, projectName, actor, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UnblockProject lets collection resume. It does not restore anything -- what was
// refused while the block stood was never stored.
func (s *PgStore) UnblockProject(ctx context.Context, projectHash string) error {
	if projectHash == "" {
		return fmt.Errorf("project hash is required")
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM blocked_projects WHERE project_hash = $1`, projectHash); err != nil {
		return fmt.Errorf("unblock project: %w", err)
	}
	return nil
}

// ListBlockedProjects returns one row per blocked directory.
//
// The grouping is not cosmetic. projects is keyed by (agent, project_hash), so a
// directory worked in by both Claude and Codex has two rows there, and a plain join
// drew a single block twice. Worse, project_name comes from the directory a session
// started in, so the same repository appeared under two names -- "ods" from a session
// at the root and "server" from one in server/ -- which read as two separate blocks.
//
// The label prefers repository_name because that is the level a block actually
// covers. project_name answers "where was I standing", which is useful in a session
// list and wrong here.
func (s *PgStore) ListBlockedProjects(ctx context.Context) ([]BlockedProject, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT b.project_hash,
			COALESCE(
				NULLIF(max(p.repository_name), ''),
				NULLIF(max(p.project_name), ''),
				NULLIF(b.project_name, ''),
				b.project_hash
			) AS name,
			COALESCE(NULLIF(string_agg(DISTINCT NULLIF(p.repo_subpath, ''), ', '), ''), '') AS subpaths,
			b.created_by, b.reason,
			to_char(b.created_at, 'YYYY-MM-DD"T"HH24:MI:SSOF')
		FROM blocked_projects b
		LEFT JOIN projects p ON p.project_hash = b.project_hash
		GROUP BY b.project_hash, b.project_name, b.created_by, b.reason, b.created_at
		ORDER BY b.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list blocked projects: %w", err)
	}
	defer rows.Close()
	out := make([]BlockedProject, 0)
	for rows.Next() {
		var b BlockedProject
		if err := rows.Scan(&b.ProjectHash, &b.ProjectName, &b.Subpaths,
			&b.CreatedBy, &b.Reason, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("list blocked projects: scan: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// LoadIngestBlocklist reads both sets for the ingest cache.
func (s *PgStore) LoadIngestBlocklist(ctx context.Context) (*IngestBlocklist, error) {
	bl := &IngestBlocklist{
		DeletedSessions: map[string]bool{},
		BlockedProjects: map[string]bool{},
	}
	rows, err := s.pool.Query(ctx, `SELECT session_id FROM deleted_sessions`)
	if err != nil {
		return nil, fmt.Errorf("load blocklist: sessions: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("load blocklist: sessions scan: %w", err)
		}
		bl.DeletedSessions[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load blocklist: sessions: %w", err)
	}

	rows, err = s.pool.Query(ctx, `SELECT project_hash FROM blocked_projects`)
	if err != nil {
		return nil, fmt.Errorf("load blocklist: projects: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("load blocklist: projects scan: %w", err)
		}
		bl.BlockedProjects[h] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load blocklist: projects: %w", err)
	}
	rows.Close()

	// The three exclusion keys of excludedAccountPredicateSQL, read into Go:
	// ingest judges each incoming row against this cached set, not with a SQL
	// predicate, so the shared helper does not apply here.
	rows, err = s.pool.Query(ctx, `
		SELECT billing_provider, account_id FROM excluded_billing_accounts
		UNION
		SELECT billing_provider, account_id FROM excluded_billing_links`)
	if err != nil {
		return nil, fmt.Errorf("load blocklist: excluded accounts: %w", err)
	}
	for rows.Next() {
		var ref BillingAccountRef
		if err := rows.Scan(&ref.BillingProvider, &ref.AccountID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("load blocklist: excluded accounts scan: %w", err)
		}
		bl.ExcludedAccounts = append(bl.ExcludedAccounts, ref)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load blocklist: excluded accounts: %w", err)
	}

	rows, err = s.pool.Query(ctx, `SELECT lower(login_email) FROM excluded_accounts`)
	if err != nil {
		return nil, fmt.Errorf("load blocklist: excluded emails: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, fmt.Errorf("load blocklist: excluded emails scan: %w", err)
		}
		bl.ExcludedEmails = append(bl.ExcludedEmails, e)
	}
	return bl, rows.Err()
}

// PurgeBlockedProjects removes rows for blocked projects that reached storage
// anyway -- a sync already in flight when the block landed, or a record that
// arrived without the project hash the envelope would have carried.
//
// It resolves sessions through session_records because otel_events and
// otel_metrics have no project_hash of their own; they are only reachable by
// session id. The non-empty guard is repeated here for the same reason it
// exists everywhere else in this file.
// SweepDeletedSessions reclaims the rows behind the tombstones, in batches.
//
// This is the 15-to-30-second half of a delete, moved off the request. It is
// idempotent and safe to run at any time: the tombstones already decide what is
// gone, so a sweep that dies partway leaves nothing visible and simply has more to
// do next time.
//
// Batched by session rather than run as one statement because a project-wide purge
// is unbounded -- one transaction over every session of a large project holds locks
// on compressed chunks for as long as it takes, and a failure anywhere throws away
// all of it. limit caps the sessions per call so a large backlog drains over
// several ticks instead of one long stall.
func (s *PgStore) SweepDeletedSessions(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		limit = 25
	}
	rows, err := s.pool.Query(ctx, `
		SELECT session_id FROM deleted_sessions
		WHERE swept_at IS NULL
		ORDER BY deleted_at
		LIMIT $1`, limit)
	if err != nil {
		return 0, fmt.Errorf("sweep deleted sessions: list: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("sweep deleted sessions: scan: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("sweep deleted sessions: list: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}

	var total int64
	for _, id := range ids {
		n, err := s.sweepOne(ctx, id)
		total += n
		if err != nil {
			// Report what was reclaimed and stop. The rest keeps its tombstone, so the
			// next tick picks it up; nothing became visible again.
			return total, err
		}
	}
	return total, nil
}

// VerifyDeletedSessions re-checks the flag against the data and clears it wherever
// a tombstone still has rows, so the ordinary sweep picks those up on its next tick.
//
// The flag alone would be cheaper, and almost always right. It is not enough here
// because this is what makes a privacy delete real: a flag can be wrong -- a bug, a
// write that never landed, an instance that died between the deletes and the mark,
// rows that arrived on another instance during the ~30s its ingest blocklist had
// not yet learned the tombstone. Without a pass that asks the data, "deleted" is an
// assertion rather than something checked.
//
// Each query is bounded and tombstone-driven. One invocation drains every batch
// that existed at its database cutoff; verified_at is the durable cursor that keeps
// a full final batch from wrapping around to the beginning. Tombstones swept after
// the cutoff belong to the next invocation rather than extending this one forever.
//
// Returns the number of tombstones put back in the queue; 0 is the expected result.
func (s *PgStore) VerifyDeletedSessions(ctx context.Context) (int64, error) {
	const verifyLimit = 100
	var cutoff time.Time
	if err := s.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&cutoff); err != nil {
		return 0, fmt.Errorf("verify deleted sessions: cutoff: %w", err)
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		examined, requeued, err := s.verifyDeletedSessionsBatch(ctx, cutoff, verifyLimit)
		total += requeued
		if err != nil {
			return total, err
		}
		if examined < verifyLimit {
			return total, nil
		}
	}
}

func (s *PgStore) verifyDeletedSessionsBatch(ctx context.Context, cutoff time.Time, limit int) (int, int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT session_id FROM deleted_sessions
		WHERE swept_at IS NOT NULL AND swept_at <= $1
		  AND (verified_at IS NULL OR verified_at < $1)
		ORDER BY verified_at ASC NULLS FIRST, swept_at, session_id LIMIT $2`, cutoff, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("verify deleted sessions: list: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("verify deleted sessions: scan: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("verify deleted sessions: list: %w", err)
	}
	if len(ids) == 0 {
		return 0, 0, nil
	}
	var sources []string
	for _, target := range sessionDeletionTargets {
		sources = append(sources, fmt.Sprintf(`SELECT %s AS session_id FROM %s WHERE %s = ANY($1::text[])`,
			target.key, target.table, target.key))
	}
	checked, err := s.pool.Query(ctx, `WITH surviving AS (`+strings.Join(sources, " UNION ALL ")+`)
		UPDATE deleted_sessions d
		SET swept_at = CASE WHEN EXISTS (SELECT 1 FROM surviving s WHERE s.session_id = d.session_id)
		                    THEN NULL ELSE d.swept_at END,
		    verified_at = CASE WHEN EXISTS (SELECT 1 FROM surviving s WHERE s.session_id = d.session_id)
		                       THEN NULL ELSE $2::timestamptz END
		WHERE d.session_id = ANY($1::text[]) AND d.swept_at IS NOT NULL
		RETURNING swept_at IS NULL`, ids, cutoff)
	if err != nil {
		return len(ids), 0, fmt.Errorf("verify deleted sessions: %w", err)
	}
	defer checked.Close()
	var requeued int64
	for checked.Next() {
		var wasRequeued bool
		if err := checked.Scan(&wasRequeued); err != nil {
			return len(ids), requeued, fmt.Errorf("verify deleted sessions: scan result: %w", err)
		}
		if wasRequeued {
			requeued++
		}
	}
	if err := checked.Err(); err != nil {
		return len(ids), requeued, fmt.Errorf("verify deleted sessions: %w", err)
	}
	return len(ids), requeued, nil
}

func (s *PgStore) sweepOne(ctx context.Context, sessionID string) (int64, error) {
	if sessionID == "" {
		return 0, ErrEmptySessionID
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("sweep %s: begin: %w", sessionID, err)
	}
	defer tx.Rollback(ctx)
	if err := lockSessionDeletionTargets(ctx, tx, []string{sessionID}); err != nil {
		return 0, fmt.Errorf("sweep %s: claim: %w", sessionID, err)
	}
	var unswept bool
	if err := tx.QueryRow(ctx, `SELECT swept_at IS NULL FROM deleted_sessions
		WHERE session_id = $1 FOR UPDATE`, sessionID).Scan(&unswept); errors.Is(err, pgx.ErrNoRows) {
		return 0, tx.Commit(ctx)
	} else if err != nil {
		return 0, fmt.Errorf("sweep %s: claim: %w", sessionID, err)
	} else if !unswept {
		return 0, tx.Commit(ctx)
	}

	// otel_events and otel_metrics are compressed hypertables, and a DELETE must
	// decompress every batch holding a matching row. TimescaleDB caps that at 100,000
	// tuples per transaction, and the cap counts tuples DECOMPRESSED, not deleted --
	// so the data's layout decides, not the size of the delete. A real purge of 1,418
	// rows decompressed 1,095,980 and failed with SQLSTATE 53400.
	if _, err := tx.Exec(ctx, `SET LOCAL timescaledb.max_tuples_decompressed_per_dml_transaction = 0`); err != nil {
		return 0, fmt.Errorf("sweep %s: lift decompression cap: %w", sessionID, err)
	}

	var total int64
	for _, target := range sessionDeletionTargets {
		tag, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s = $1 AND $1 <> ''`,
			target.table, target.key), sessionID)
		if err != nil {
			return total, fmt.Errorf("sweep %s: %s: %w", sessionID, target.table, err)
		}
		total += tag.RowsAffected()
	}
	for _, target := range sessionDeletionTargets {
		var remains bool
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE %s = $1)`,
			target.table, target.key), sessionID).Scan(&remains); err != nil {
			return total, fmt.Errorf("sweep %s: verify %s: %w", sessionID, target.table, err)
		}
		if remains {
			return total, fmt.Errorf("sweep %s: verify %s: rows remain", sessionID, target.table)
		}
	}
	// Marked in the same transaction as the deletes. Split across two, a crash
	// between them either loses the mark (harmless -- the next pass repeats an
	// idempotent sweep) or keeps it without the rows being gone, which is the one
	// outcome this mechanism must not produce.
	if _, err := tx.Exec(ctx,
		`UPDATE deleted_sessions SET swept_at = now(), verified_at = NULL WHERE session_id = $1`, sessionID); err != nil {
		return total, fmt.Errorf("sweep %s: mark swept: %w", sessionID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return total, fmt.Errorf("sweep %s: commit: %w", sessionID, err)
	}
	return total, nil
}

// PurgeBlockedProjects tombstones anything a blocked project let through, so the
// sweep above reclaims it. It does not delete rows itself: keeping one path
// responsible for removal keeps the decompression handling in one place.
func (s *PgStore) PurgeBlockedProjects(ctx context.Context) (int64, error) {
	// Avoid canceling an idle login-history repair when there is no deletion work.
	// This is only a gate; candidates are rediscovered under the exclusive source
	// lock below so a writer cannot change the deletion set mid-transaction.
	var pending bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM session_records sr JOIN blocked_projects b ON b.project_hash = sr.project_hash
		WHERE sr.session_id <> ''
		  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id))`).Scan(&pending); err != nil {
		return 0, fmt.Errorf("purge blocked projects: check: %w", err)
	}
	if !pending {
		return 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("purge blocked projects: begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := beginStrictDeletion(ctx, tx); err != nil {
		return 0, err
	}
	ids, err := querySessionIDs(ctx, tx, `SELECT DISTINCT sr.session_id
		FROM session_records sr JOIN blocked_projects b ON b.project_hash = sr.project_hash
		WHERE sr.session_id <> ''
		  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id)`)
	if err != nil {
		return 0, fmt.Errorf("purge blocked projects: list: %w", err)
	}
	if err := tombstoneTx(ctx, tx, ids, "", "sweep", "blocked project"); err != nil {
		return 0, fmt.Errorf("purge blocked projects: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("purge blocked projects: commit: %w", err)
	}
	return int64(len(ids)), nil
}

// ProjectSessionCount reports how many stored sessions a project has, excluding
// one. The dialog needs the number BEFORE anything is deleted, to ask "and the
// other N?" -- asking without the count would make the user guess how much they
// are about to remove.
func (s *PgStore) ProjectSessionCount(ctx context.Context, projectHash, excludeSessionID string) (int, error) {
	if projectHash == "" {
		return 0, nil
	}
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(DISTINCT session_id) FROM session_records
		WHERE project_hash = $1 AND session_id <> '' AND session_id <> $2`,
		projectHash, excludeSessionID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("project session count: %w", err)
	}
	return n, nil
}

func (s *PgStore) GetDeletionPolicy(ctx context.Context) (*DeletionPolicy, error) {
	p := &DeletionPolicy{}
	err := s.pool.QueryRow(ctx, `
		SELECT allow_owner_delete, updated_by, to_char(updated_at, 'YYYY-MM-DD"T"HH24:MI:SSOF')
		FROM deletion_policy WHERE id = 1`).Scan(&p.AllowOwnerDelete, &p.UpdatedBy, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// The migration seeds this row, so its absence means a database that predates
		// it. Answer with the seeded default rather than an error: a missing row must
		// not read as "owners may not delete".
		return &DeletionPolicy{AllowOwnerDelete: true}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get deletion policy: %w", err)
	}
	return p, nil
}

func (s *PgStore) SetDeletionPolicy(ctx context.Context, allowOwnerDelete bool, actor string) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO deletion_policy (id, allow_owner_delete, updated_by, updated_at)
		VALUES (1, $1, $2, now())
		ON CONFLICT (id) DO UPDATE
		SET allow_owner_delete = EXCLUDED.allow_owner_delete,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`, allowOwnerDelete, actor); err != nil {
		return fmt.Errorf("set deletion policy: %w", err)
	}
	return nil
}
