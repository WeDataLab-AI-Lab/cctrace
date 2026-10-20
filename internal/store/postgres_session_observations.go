package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// SessionObservationEventRetention is how long session_observation_events keeps
// a row, measured on observed_at. Of the four observation tables it is the only
// one that grows with time rather than with sessions -- every state change
// appends -- so it is the one with a bound of its own. A shorter session_records
// retention, when one is set, prunes it sooner; the snapshot and live-state
// tables, one row per session or process, follow that retention alone
// (PruneSessionObservations).
const SessionObservationEventRetention = 30 * 24 * time.Hour

var (
	ErrSessionIOConflict          = errors.New("conflicting process I/O report at the same timestamp")
	ErrSessionIOEpochMismatch     = errors.New("process I/O epoch changed without a new process start")
	ErrSessionIOCounterRegression = errors.New("process I/O counters decreased within one epoch")
)

// SessionSnapshot is the client's latest view of one session's metadata lines,
// as one JSON document. Keyed by (SessionID, UserID, ProfileEmail).
type SessionSnapshot struct {
	SessionID          string
	UserID             string
	ProfileEmail       string
	LoginEmail         string
	Agent              string
	ProjectHash        string
	BillingProvider    string
	AccountID          string
	CollectVersion     int
	IdentityProvenance string
	Snapshot           json.RawMessage
	ObservedAt         time.Time
}

// SessionLiveState is one running instance of a session as its native state
// source or process-I/O collector described it. Keyed by (SessionID, UserID,
// ProfileEmail, PID, ProcStart).
type SessionLiveState struct {
	SessionID          string
	UserID             string
	ProfileEmail       string
	LoginEmail         string
	Agent              string
	ProjectHash        string
	BillingProvider    string
	AccountID          string
	PID                int64
	ProcStart          string
	IdentityProvenance string
	Status             string // busy | idle | waiting | shell | unknown | gone
	StatusSource       string // status evidence or explicit unknown reason
	WaitingKind        string
	Name               string
	NameSource         string
	StatusChangedAt    *time.Time
	HeartbeatAt        time.Time
	IOEpoch            string
	IOAsOf             *time.Time
	IOSource           string
	IOMode             string
	IOPartial          bool
	IOFailureCode      string
	InputBytes         *int64
	InputLines         *int64
	OutputBytes        *int64
	OutputLines        *int64
	StdoutBytes        *int64
	StdoutLines        *int64
	StderrBytes        *int64
	StderrLines        *int64
}

func sessionLiveStatusSource(source string) string {
	if source == "" {
		return "legacy"
	}
	return source
}

// SessionObservationEvent is one entry of observation history. EventID is the
// client's, and a retry carries the same one. The server writes entries of its
// own too: the state changes it derives from live states (see
// IngestSessionObservations).
type SessionObservationEvent struct {
	SessionID          string
	UserID             string
	ProfileEmail       string
	LoginEmail         string
	Agent              string
	ProjectHash        string
	BillingProvider    string
	AccountID          string
	EventID            string
	IdentityProvenance string
	Kind               string // state_change | gone | session_end
	ObservedAt         time.Time
	Payload            json.RawMessage
}

// CollectorHeartbeat says the collector of one identity was running at
// HeartbeatAt, the time the server received the request -- not anything the
// client's clock said. ClientVersion is the version it reported, empty when it
// did not.
type CollectorHeartbeat struct {
	UserID        string
	ProfileEmail  string
	HeartbeatAt   time.Time
	ClientVersion string
}

// SessionObservationBatch is everything one POST /api/session-observations
// carries. Heartbeat is nil when the request did not include one.
type SessionObservationBatch struct {
	Snapshots  []*SessionSnapshot
	LiveStates []*SessionLiveState
	Events     []*SessionObservationEvent
	Heartbeat  *CollectorHeartbeat
}

func observationProvenance(value string) string {
	if value == "personal_token" || value == "shared_key_claim" {
		return value
	}
	return "unknown"
}

type storedSessionIOMetrics struct {
	epoch       string
	asOf        *time.Time
	source      string
	mode        string
	partial     bool
	failureCode string
	inputBytes  *int64
	inputLines  *int64
	outputBytes *int64
	outputLines *int64
	stdoutBytes *int64
	stdoutLines *int64
	stderrBytes *int64
	stderrLines *int64
}

// validateSessionIOUpdate enforces the process epoch's monotone counter stream.
// A fresh live-state status can arrive after a newer I/O sample, so stale I/O
// samples are ignored independently from the status heartbeat.
func validateSessionIOUpdate(ctx context.Context, tx pgx.Tx, incoming *SessionLiveState) (bool, error) {
	if incoming.IOEpoch == "" {
		return false, nil
	}
	if incoming.IOAsOf == nil {
		return false, errors.New("process I/O report has no as_of timestamp")
	}
	var old storedSessionIOMetrics
	err := tx.QueryRow(ctx, `SELECT io_epoch,io_as_of,io_source,io_mode,io_partial,io_failure_code,
		input_bytes,input_lines,output_bytes,output_lines,stdout_bytes,stdout_lines,stderr_bytes,stderr_lines
		FROM session_live_states WHERE session_id=$1 AND user_id=$2 AND lower(profile_email)=lower($3)
		AND pid=$4 AND proc_start=$5 FOR UPDATE`,
		incoming.SessionID, incoming.UserID, incoming.ProfileEmail, incoming.PID, incoming.ProcStart,
	).Scan(&old.epoch, &old.asOf, &old.source, &old.mode, &old.partial, &old.failureCode,
		&old.inputBytes, &old.inputLines, &old.outputBytes, &old.outputLines,
		&old.stdoutBytes, &old.stdoutLines, &old.stderrBytes, &old.stderrLines)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if old.epoch == "" {
		return true, nil
	}
	if old.epoch != incoming.IOEpoch {
		return false, ErrSessionIOEpochMismatch
	}
	if old.source != incoming.IOSource || old.mode != incoming.IOMode {
		return false, ErrSessionIOConflict
	}
	if old.asOf == nil {
		return true, nil
	}
	if incoming.IOAsOf.Before(*old.asOf) {
		return false, nil
	}
	if incoming.IOAsOf.Equal(*old.asOf) {
		if !sameSessionIOMetrics(old, incoming) {
			return false, ErrSessionIOConflict
		}
		return true, nil
	}
	for _, pair := range [][2]*int64{
		{incoming.InputBytes, old.inputBytes}, {incoming.InputLines, old.inputLines},
		{incoming.OutputBytes, old.outputBytes}, {incoming.OutputLines, old.outputLines},
		{incoming.StdoutBytes, old.stdoutBytes}, {incoming.StdoutLines, old.stdoutLines},
		{incoming.StderrBytes, old.stderrBytes}, {incoming.StderrLines, old.stderrLines},
	} {
		if pair[1] != nil && (pair[0] == nil || *pair[0] < *pair[1]) {
			return false, ErrSessionIOCounterRegression
		}
	}
	return true, nil
}

func sameSessionIOMetrics(old storedSessionIOMetrics, incoming *SessionLiveState) bool {
	return old.partial == incoming.IOPartial && old.failureCode == incoming.IOFailureCode &&
		sameOptionalCounter(old.inputBytes, incoming.InputBytes) && sameOptionalCounter(old.inputLines, incoming.InputLines) &&
		sameOptionalCounter(old.outputBytes, incoming.OutputBytes) && sameOptionalCounter(old.outputLines, incoming.OutputLines) &&
		sameOptionalCounter(old.stdoutBytes, incoming.StdoutBytes) && sameOptionalCounter(old.stdoutLines, incoming.StdoutLines) &&
		sameOptionalCounter(old.stderrBytes, incoming.StderrBytes) && sameOptionalCounter(old.stderrLines, incoming.StderrLines)
}

func sameOptionalCounter(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// SessionObservationResult reports what a batch changed. Stored counts the
// snapshot, live-state and event rows that were inserted or updated; the rest
// were older than what the table already held, an event already recorded, or a
// session with a tombstone. The heartbeat is not counted: it is the collector's
// own row, and GREATEST rewrites it on every request whether or not it moved, so
// counting it would only add one to every answer. Nor are the history rows the
// server derives from live states: the client did not send them, and counting
// them would let Stored exceed the items the request carried.
type SessionObservationResult struct {
	Stored int
}

// IngestSessionObservations writes one batch in one transaction. On any error the
// result is empty: the transaction rolled back, so nothing it had counted is
// stored.
//
// Every write is newer-wins or idempotent, because the client retries, and a
// retry can arrive after a newer report has already landed:
//   - a snapshot or live state replaces the stored row only when its own time is
//     not older -- an older retry must not undo a newer report. An equal time
//     wins: clients send RFC3339Nano and the columns keep microseconds, so an
//     equal time is in practice the same report sent again, and the later
//     arrival taking the row leaves it as it was;
//   - an event is inserted once, and its retry finds the same event_id;
//   - the heartbeat only ever moves forward.
//
// Times are compared and stored as the client sent them. A clock running ahead
// would win every comparison above until real time caught up with it, so the
// API skips an item dated more than five minutes past its own clock
// (maxSessionObservationClockAhead in internal/api): that is as long as a fast
// clock can hold a row. Cutting a future time to now() instead would date every
// report from such a clock by its arrival, and an older retry landing after a
// newer report would take the row.
//
// A live state that is new, or that replaces a row of another status -- or, while
// waiting, of another waiting kind -- also writes the history row that change is
// (insertLiveStateChange). An unchanged state writes none -- a heartbeat is not
// history -- and neither does a state older than the stored one, which changed
// nothing.
//
// Rows are written one statement at a time, as InsertSessionRecords does. A batch
// can name the same key twice, and a multi-row ON CONFLICT DO UPDATE refuses to
// touch one row twice; one statement each makes the second simply compare
// against the first.
//
// Session rows take the same locks as every other session writer: the shared
// source lock, which DeleteUserData and MergeUsers take exclusively, and the
// per-session deletion locks, after which a tombstoned session is dropped. The
// API already refuses deleted sessions from its cache; this is the check that
// holds through commit.
func (s *PgStore) IngestSessionObservations(ctx context.Context, batch SessionObservationBatch) (SessionObservationResult, error) {
	if len(batch.Snapshots) == 0 && len(batch.LiveStates) == 0 && len(batch.Events) == 0 && batch.Heartbeat == nil {
		return SessionObservationResult{}, nil
	}
	for attempt := 0; ; attempt++ {
		stored, err := s.ingestSessionObservations(ctx, batch)
		if err == nil {
			return SessionObservationResult{Stored: stored}, nil
		}
		var pgErr *pgconn.PgError
		if attempt >= 2 || ctx.Err() != nil || !errors.As(err, &pgErr) || (pgErr.Code != "40P01" && pgErr.Code != "40001") {
			return SessionObservationResult{}, err
		}
		// ingestSessionObservations has rolled back the entire transaction. Never
		// retry a statement in the aborted transaction or report partial Stored.
	}
}

func (s *PgStore) ingestSessionObservations(ctx context.Context, batch SessionObservationBatch) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return 0, err
	}

	var ids []string
	for _, sn := range batch.Snapshots {
		if sn != nil {
			ids = append(ids, sn.SessionID)
		}
	}
	for _, ls := range batch.LiveStates {
		if ls != nil {
			ids = append(ids, ls.SessionID)
		}
	}
	for _, ev := range batch.Events {
		if ev != nil {
			ids = append(ids, ev.SessionID)
		}
	}
	snapshots, claimPeers, snapshotStored, err := prepareSnapshotClaims(ctx, tx, batch.Snapshots, ids)
	if err != nil {
		return 0, err
	}
	live, err := liveSessionIDs(ctx, tx, ids)
	if err != nil {
		return 0, err
	}

	stored := snapshotStored
	// The sessions whose snapshot this batch wrote, for the session analysis
	// queue. A write is enough, the same snapshot sent again included; one the
	// newer stored snapshot refused changed nothing.
	var snapshotSessions []string
	for _, sn := range snapshots {
		if sn == nil || !live[sn.SessionID] {
			continue
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO session_snapshots
				(session_id, user_id, profile_email, login_email, agent, project_hash,
				 billing_provider, account_id, collect_version, snapshot, observed_at, identity_provenance)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (session_id, user_id, profile_email) DO UPDATE SET
				login_email      = EXCLUDED.login_email,
				agent            = EXCLUDED.agent,
				project_hash     = EXCLUDED.project_hash,
				billing_provider = EXCLUDED.billing_provider,
				account_id       = EXCLUDED.account_id,
				collect_version  = EXCLUDED.collect_version,
				identity_provenance = EXCLUDED.identity_provenance,
				snapshot         = EXCLUDED.snapshot,
				observed_at      = EXCLUDED.observed_at,
				received_at      = now()
			WHERE EXCLUDED.observed_at >= session_snapshots.observed_at`,
			sn.SessionID, sn.UserID, sn.ProfileEmail, sn.LoginEmail, sn.Agent, sn.ProjectHash,
			sn.BillingProvider, sn.AccountID, sn.CollectVersion, sanitizeJSONNUL(sn.Snapshot), sn.ObservedAt, observationProvenance(sn.IdentityProvenance))
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() > 0 {
			snapshotSessions = append(snapshotSessions, sn.SessionID)
			if err := replaceSnapshotClaims(ctx, tx, snapshotIdentity(sn)); err != nil {
				return 0, err
			}
		}
	}

	for _, ls := range batch.LiveStates {
		if ls == nil || !live[ls.SessionID] {
			continue
		}
		ioFresh, err := validateSessionIOUpdate(ctx, tx, ls)
		if err != nil {
			return 0, err
		}
		// prev is the row as it stood before this statement: every part of one
		// statement reads the same snapshot, RETURNING's subqueries included.
		// Nothing else writes the row in between -- liveSessionIDs holds this
		// session's lock to commit. No row back means the WHERE refused an older
		// state.
		var prevStatus *string
		var prevWaitingKind string
		var prevChangedAt, changedAt *time.Time
		var heartbeatAt time.Time
		err = tx.QueryRow(ctx, `
			WITH prev AS (
				SELECT status, waiting_kind, status_changed_at FROM session_live_states
				WHERE session_id = $1 AND user_id = $2 AND profile_email = $3
				  AND pid = $9 AND proc_start = $10)
			INSERT INTO session_live_states
				(session_id, user_id, profile_email, login_email, agent, project_hash,
				 billing_provider, account_id, pid, proc_start, status, waiting_kind, name,
				 name_source, status_changed_at, heartbeat_at, identity_provenance,
				 io_epoch,io_as_of,io_source,io_mode,io_partial,io_failure_code,
				 input_bytes,input_lines,output_bytes,output_lines,stdout_bytes,stdout_lines,stderr_bytes,stderr_lines,status_source)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,
				$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$33)
			ON CONFLICT (session_id, user_id, profile_email, pid, proc_start) DO UPDATE SET
				login_email       = EXCLUDED.login_email,
				agent             = EXCLUDED.agent,
				project_hash      = EXCLUDED.project_hash,
				billing_provider  = EXCLUDED.billing_provider,
				account_id        = EXCLUDED.account_id,
				status            = CASE
					WHEN EXCLUDED.status='gone' THEN 'gone'
					WHEN EXCLUDED.io_epoch<>'' AND EXCLUDED.status_source='process_io_unknown' THEN session_live_states.status
					WHEN EXCLUDED.io_epoch<>'' AND EXCLUDED.status_source='claude_process_file' AND
						(session_live_states.status_source IN ('legacy','process_io_unknown') OR
						(session_live_states.status_source='claude_process_file' AND
						 EXCLUDED.status_changed_at>COALESCE(session_live_states.status_changed_at,'-infinity'::timestamptz))) THEN EXCLUDED.status
					WHEN EXCLUDED.io_epoch<>'' THEN session_live_states.status
					WHEN EXCLUDED.heartbeat_at >= session_live_states.heartbeat_at THEN EXCLUDED.status ELSE session_live_states.status END,
				status_source      = CASE
					WHEN EXCLUDED.status='gone' THEN EXCLUDED.status_source
					WHEN EXCLUDED.io_epoch<>'' AND EXCLUDED.status_source='process_io_unknown' THEN session_live_states.status_source
					WHEN EXCLUDED.io_epoch<>'' AND EXCLUDED.status_source='claude_process_file' AND
						(session_live_states.status_source IN ('legacy','process_io_unknown') OR
						(session_live_states.status_source='claude_process_file' AND
						 EXCLUDED.status_changed_at>COALESCE(session_live_states.status_changed_at,'-infinity'::timestamptz))) THEN EXCLUDED.status_source
					WHEN EXCLUDED.io_epoch<>'' THEN session_live_states.status_source
					WHEN EXCLUDED.heartbeat_at >= session_live_states.heartbeat_at THEN EXCLUDED.status_source ELSE session_live_states.status_source END,
				identity_provenance = EXCLUDED.identity_provenance,
				waiting_kind      = CASE
					WHEN EXCLUDED.status='gone' THEN ''
					WHEN EXCLUDED.io_epoch<>'' AND EXCLUDED.status_source='process_io_unknown' THEN session_live_states.waiting_kind
					WHEN EXCLUDED.io_epoch<>'' AND EXCLUDED.status_source='claude_process_file' AND
						(session_live_states.status_source IN ('legacy','process_io_unknown') OR
						(session_live_states.status_source='claude_process_file' AND
						 EXCLUDED.status_changed_at>COALESCE(session_live_states.status_changed_at,'-infinity'::timestamptz))) THEN EXCLUDED.waiting_kind
					WHEN EXCLUDED.io_epoch<>'' THEN session_live_states.waiting_kind
					WHEN EXCLUDED.heartbeat_at >= session_live_states.heartbeat_at THEN EXCLUDED.waiting_kind ELSE session_live_states.waiting_kind END,
				name              = CASE WHEN EXCLUDED.io_epoch<>'' THEN session_live_states.name WHEN EXCLUDED.heartbeat_at >= session_live_states.heartbeat_at THEN EXCLUDED.name ELSE session_live_states.name END,
				name_source       = CASE WHEN EXCLUDED.io_epoch<>'' THEN session_live_states.name_source WHEN EXCLUDED.heartbeat_at >= session_live_states.heartbeat_at THEN EXCLUDED.name_source ELSE session_live_states.name_source END,
				status_changed_at = CASE
					WHEN EXCLUDED.status='gone' THEN EXCLUDED.status_changed_at
					WHEN EXCLUDED.io_epoch<>'' AND EXCLUDED.status_source='process_io_unknown' THEN session_live_states.status_changed_at
					WHEN EXCLUDED.io_epoch<>'' AND EXCLUDED.status_source='claude_process_file' AND
						(session_live_states.status_source IN ('legacy','process_io_unknown') OR
						(session_live_states.status_source='claude_process_file' AND
						 EXCLUDED.status_changed_at>COALESCE(session_live_states.status_changed_at,'-infinity'::timestamptz))) THEN EXCLUDED.status_changed_at
					WHEN EXCLUDED.io_epoch<>'' THEN session_live_states.status_changed_at
					WHEN EXCLUDED.heartbeat_at >= session_live_states.heartbeat_at THEN EXCLUDED.status_changed_at ELSE session_live_states.status_changed_at END,
				heartbeat_at      = GREATEST(EXCLUDED.heartbeat_at,session_live_states.heartbeat_at),
				io_epoch          = CASE WHEN $32 THEN EXCLUDED.io_epoch ELSE session_live_states.io_epoch END,
				io_as_of          = CASE WHEN $32 THEN EXCLUDED.io_as_of ELSE session_live_states.io_as_of END,
				io_source         = CASE WHEN $32 THEN EXCLUDED.io_source ELSE session_live_states.io_source END,
				io_mode           = CASE WHEN $32 THEN EXCLUDED.io_mode ELSE session_live_states.io_mode END,
				io_partial        = CASE WHEN $32 THEN EXCLUDED.io_partial ELSE session_live_states.io_partial END,
				io_failure_code   = CASE WHEN $32 THEN EXCLUDED.io_failure_code ELSE session_live_states.io_failure_code END,
				input_bytes       = CASE WHEN $32 THEN EXCLUDED.input_bytes ELSE session_live_states.input_bytes END,
				input_lines       = CASE WHEN $32 THEN EXCLUDED.input_lines ELSE session_live_states.input_lines END,
				output_bytes      = CASE WHEN $32 THEN EXCLUDED.output_bytes ELSE session_live_states.output_bytes END,
				output_lines      = CASE WHEN $32 THEN EXCLUDED.output_lines ELSE session_live_states.output_lines END,
				stdout_bytes      = CASE WHEN $32 THEN EXCLUDED.stdout_bytes ELSE session_live_states.stdout_bytes END,
				stdout_lines      = CASE WHEN $32 THEN EXCLUDED.stdout_lines ELSE session_live_states.stdout_lines END,
				stderr_bytes      = CASE WHEN $32 THEN EXCLUDED.stderr_bytes ELSE session_live_states.stderr_bytes END,
				stderr_lines      = CASE WHEN $32 THEN EXCLUDED.stderr_lines ELSE session_live_states.stderr_lines END,
				received_at       = now()
			WHERE EXCLUDED.heartbeat_at >= session_live_states.heartbeat_at OR
				($32 AND EXCLUDED.io_as_of >= COALESCE(session_live_states.io_as_of,'-infinity'::timestamptz))
			RETURNING (SELECT status FROM prev), COALESCE((SELECT waiting_kind FROM prev), ''),
				(SELECT status_changed_at FROM prev), status_changed_at, heartbeat_at`,
			ls.SessionID, ls.UserID, ls.ProfileEmail, ls.LoginEmail, ls.Agent, ls.ProjectHash,
			ls.BillingProvider, ls.AccountID, ls.PID, ls.ProcStart, ls.Status, ls.WaitingKind,
			ls.Name, ls.NameSource, ls.StatusChangedAt, ls.HeartbeatAt, observationProvenance(ls.IdentityProvenance),
			ls.IOEpoch, ls.IOAsOf, ls.IOSource, ls.IOMode, ls.IOPartial, ls.IOFailureCode,
			ls.InputBytes, ls.InputLines, ls.OutputBytes, ls.OutputLines, ls.StdoutBytes, ls.StdoutLines,
			ls.StderrBytes, ls.StderrLines, ioFresh, sessionLiveStatusSource(ls.StatusSource),
		).Scan(&prevStatus, &prevWaitingKind, &prevChangedAt, &changedAt, &heartbeatAt)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, err
		}
		stored++
		// Not a change: the same status and, while waiting, the same kind of wait.
		// A wait that changes kind -- a permission prompt, then a dialog -- is one.
		sameStatus := prevStatus != nil && *prevStatus == ls.Status
		if sameStatus && (ls.Status != "waiting" || prevWaitingKind == ls.WaitingKind) {
			continue
		}
		// Dated by status_changed_at, the time the state file gives, else by the
		// heartbeat. A change of waiting kind alone takes status_changed_at only
		// when it moved: unmoved, it is when the wait began, not when its kind
		// changed, and the heartbeat is the first report of the new kind.
		at := heartbeatAt
		if changedAt != nil && (!sameStatus || prevChangedAt == nil || changedAt.After(*prevChangedAt)) {
			at = *changedAt
		}
		if err := insertLiveStateChange(ctx, tx, ls, prevStatus, at); err != nil {
			return 0, err
		}
	}

	for _, ev := range batch.Events {
		if ev == nil || !live[ev.SessionID] {
			continue
		}
		payload := []byte(ev.Payload)
		if payload == nil {
			payload = []byte(`{}`)
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO session_observation_events
				(session_id, user_id, profile_email, login_email, agent, project_hash,
				 billing_provider, account_id, event_id, kind, observed_at, payload, identity_provenance)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			ON CONFLICT (session_id, user_id, profile_email, event_id) DO NOTHING`,
			ev.SessionID, ev.UserID, ev.ProfileEmail, ev.LoginEmail, ev.Agent, ev.ProjectHash,
			ev.BillingProvider, ev.AccountID, ev.EventID, ev.Kind, ev.ObservedAt, sanitizeJSONNUL(payload), observationProvenance(ev.IdentityProvenance))
		if err != nil {
			return 0, err
		}
		stored += int(tag.RowsAffected())
	}

	if hb := batch.Heartbeat; hb != nil {
		// heartbeat_at is the API's time of receipt (CollectorHeartbeat), not a
		// client's clock. client_version keeps its last non-empty value, as
		// client_versions does: it comes from a request header, and a missing
		// header means "unknown", not "no version".
		if _, err := tx.Exec(ctx, `
			INSERT INTO collector_heartbeats (user_id, profile_email, heartbeat_at, client_version)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (user_id, profile_email) DO UPDATE SET
				heartbeat_at   = GREATEST(collector_heartbeats.heartbeat_at, EXCLUDED.heartbeat_at),
				client_version = COALESCE(NULLIF(EXCLUDED.client_version, ''), collector_heartbeats.client_version)`,
			hb.UserID, hb.ProfileEmail, hb.HeartbeatAt, hb.ClientVersion); err != nil {
			return 0, err
		}
	}

	if err := markSessionFactsDirtyInOrder(ctx, tx,
		dirtyGroup{ids: snapshotSessions, reason: dirtySnapshot},
		dirtyGroup{ids: claimPeers, reason: dirtyDeletionPeer}); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return stored, nil
}

// insertLiveStateChange writes the history row for a live state that changed:
// kind gone when the new status is gone, state_change otherwise. from is the
// status it replaced, nil for a process seen for the first time, and at is the
// time the caller dated the change by.
//
// The event_id hashes the change itself -- session, process, the status it left,
// the status and waiting kind it entered, and at -- so a resend of the same change
// finds the row already there, and two changes that end in the same state at the
// same time, from different states, are still two rows.
//
// A process gone is dated like any change, so the client sends the time it found
// the process gone as both heartbeat_at and status_changed_at; that is its side
// of this contract. The state file a crashed process leaves behind still holds
// the time of its last status change, and gone dated by that would say the
// process ended when it last changed status.
func insertLiveStateChange(ctx context.Context, tx pgx.Tx, ls *SessionLiveState, from *string, at time.Time) error {
	kind := "state_change"
	if ls.Status == "gone" {
		kind = "gone"
	}
	key, err := json.Marshal([]any{ls.SessionID, ls.PID, ls.ProcStart, from, ls.Status, ls.WaitingKind, at.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(key)
	payload, err := json.Marshal(map[string]any{"from": from, "to": ls.Status, "waiting_kind": ls.WaitingKind})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO session_observation_events
			(session_id, user_id, profile_email, login_email, agent, project_hash,
			 billing_provider, account_id, event_id, kind, observed_at, payload, identity_provenance)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (session_id, user_id, profile_email, event_id) DO NOTHING`,
		ls.SessionID, ls.UserID, ls.ProfileEmail, ls.LoginEmail, ls.Agent, ls.ProjectHash,
		ls.BillingProvider, ls.AccountID, "state-"+hex.EncodeToString(sum[:]), kind, at, payload, observationProvenance(ls.IdentityProvenance)); err != nil {
		return fmt.Errorf("record live state change: %w", err)
	}
	return nil
}

// TouchCollectorStored records that a sync from this identity committed records
// at the given time. handleSync calls it only after the insert returned, which is
// the difference from client_versions.last_seen_at: that one is written before
// the insert and so also moves for a sync that then fails.
//
// It takes the shared source lock, as every writer of an identity's rows does:
// DeleteUserData and MergeUsers hold it exclusively, so a touch cannot write this
// row back between their delete or move and their commit.
func (s *PgStore) TouchCollectorStored(ctx context.Context, userID, profileEmail string, at time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO collector_heartbeats (user_id, profile_email, last_stored_at)
		VALUES ($1,$2,$3)
		ON CONFLICT (user_id, profile_email) DO UPDATE SET
			last_stored_at = GREATEST(collector_heartbeats.last_stored_at, EXCLUDED.last_stored_at)`,
		userID, profileEmail, at); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PruneSessionObservations deletes what the observation tables keep past their
// retention and reports how many rows went:
//   - session_observation_events older than eventsOlderThan, always;
//   - every observation table's rows older than the session_records retention,
//     while one is in force. They hold session titles and names, and when each
//     session was busy, waiting or gone -- what the records say, in other words
//     -- so they last no longer than the records they describe. History is then
//     kept for the shorter of the two windows. Without a session retention the
//     snapshots and live states stay: each is the latest value for its session
//     or process, and a session that went quiet must not lose it.
//
// The window is read from the session_records policy itself, as the rollup
// reconciliation reads it, so a retention changed from the admin page applies
// here from the next run, not from the next restart. Neither latest-value table
// has an index on its time: they hold one row per session or process, and a
// scan of that is what one run costs.
//
// A snapshot is an input of session analysis -- the title, among others -- so
// deleting one also deletes its facts and queues recomputation (p1b-design §6.2).
// The copy markers stay: no record went. All observation deletions and the
// personal state of sessions whose last source disappeared commit together.
// The retention lock precedes the exclusive source lock; personal writes take
// the source lock shared and cannot race the source removal.
func (s *PgStore) PruneSessionObservations(ctx context.Context, eventsOlderThan time.Time) (int64, error) {
	days, err := s.policyIntervalDays(ctx, "policy_retention", "drop_after", "session_records")
	if err != nil {
		return 0, fmt.Errorf("read session retention: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('session_overview_retention'))`); err != nil {
		return 0, fmt.Errorf("lock session overview retention: %w", err)
	}
	// A personal write and source retention must not commit between the last
	// source deletion and the matching personal-row deletion.
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return 0, err
	}
	// The sweeper holds the per-session lock before deleting these rows. Claim
	// candidate sessions before any DELETE takes row locks, in the same order.
	retentionDays := 0
	if days != nil && *days > 0 {
		retentionDays = *days
	}
	candidateRows, err := tx.Query(ctx, `SELECT session_id FROM session_observation_events WHERE observed_at < $1
		UNION SELECT session_id FROM session_observation_events
			WHERE $2::int > 0 AND observed_at < now() - make_interval(days => $2)
		UNION SELECT session_id FROM session_live_states
			WHERE $2::int > 0 AND heartbeat_at < now() - make_interval(days => $2)
		UNION SELECT session_id FROM session_snapshots
			WHERE $2::int > 0 AND observed_at < now() - make_interval(days => $2)`, eventsOlderThan, retentionDays)
	if err != nil {
		return 0, err
	}
	candidates, err := pgx.CollectRows(candidateRows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	claimPeers, err := prepareSessionClaimMutation(ctx, tx, candidates)
	if err != nil {
		return 0, err
	}
	var affected []string
	pruned := int64(0)
	deleteRows := func(query string, args ...any) ([]string, error) {
		rows, queryErr := tx.Query(ctx, query, args...)
		if queryErr != nil {
			return nil, queryErr
		}
		return pgx.CollectRows(rows, pgx.RowTo[string])
	}
	eventSessions, err := deleteRows(`DELETE FROM session_observation_events WHERE observed_at < $1 RETURNING session_id`, eventsOlderThan)
	if err != nil {
		return 0, err
	}
	affected = append(affected, eventSessions...)
	pruned += int64(len(eventSessions))
	var snapshotSessions []string
	if days != nil && *days > 0 {
		for _, query := range []string{
			// Finds nothing unless the session retention is the shorter window.
			`DELETE FROM session_observation_events WHERE observed_at < now() - make_interval(days => $1) RETURNING session_id`,
			`DELETE FROM session_live_states WHERE heartbeat_at < now() - make_interval(days => $1) RETURNING session_id`,
		} {
			ids, deleteErr := deleteRows(query, *days)
			if deleteErr != nil {
				return 0, deleteErr
			}
			affected = append(affected, ids...)
			pruned += int64(len(ids))
		}
		snapshotSessions, err = deleteRows(`DELETE FROM session_snapshots WHERE observed_at < now() - make_interval(days => $1) RETURNING session_id`, *days)
		if err != nil {
			return 0, err
		}
		affected = append(affected, snapshotSessions...)
		pruned += int64(len(snapshotSessions))
		if err := deleteSessionFacts(ctx, tx, snapshotSessions); err != nil {
			return 0, err
		}
	}
	if len(affected) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM session_personal_state p WHERE p.session_id=ANY($1::text[])
			AND NOT EXISTS (SELECT 1 FROM session_records r WHERE r.session_id=p.session_id AND
					(p.owner_state<>'valid' OR (p.owner_proof='session_record' AND r.user_id=p.source_user_id
						AND lower(r.profile_email)=lower(p.source_profile_email)))
				UNION ALL SELECT 1 FROM session_snapshots o WHERE o.session_id=p.session_id AND o.identity_provenance='personal_token' AND
					(p.owner_state<>'valid' OR (p.owner_proof='personal_token_observation' AND o.user_id=p.source_user_id
						AND lower(o.profile_email)=lower(p.source_profile_email)))
				UNION ALL SELECT 1 FROM session_live_states o WHERE o.session_id=p.session_id AND o.identity_provenance='personal_token' AND
					(p.owner_state<>'valid' OR (p.owner_proof='personal_token_observation' AND o.user_id=p.source_user_id
						AND lower(o.profile_email)=lower(p.source_profile_email)))
				UNION ALL SELECT 1 FROM session_observation_events o WHERE o.session_id=p.session_id AND o.identity_provenance='personal_token' AND
					(p.owner_state<>'valid' OR (p.owner_proof='personal_token_observation' AND o.user_id=p.source_user_id
						AND lower(o.profile_email)=lower(p.source_profile_email))))`, affected); err != nil {
			return 0, err
		}
	}
	if err := pruneSessionGroupDismissals(ctx, tx); err != nil {
		return 0, err
	}
	if len(snapshotSessions) == 0 {
		claimPeers = nil
	}
	if err := rebuildSessionClaims(ctx, tx, snapshotSessions); err != nil {
		return 0, err
	}
	if err := markSessionFactsDirtyInOrder(ctx, tx,
		dirtyGroup{ids: snapshotSessions, reason: dirtySnapshotPrune},
		dirtyGroup{ids: claimPeers, reason: dirtyDeletionPeer}); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return pruned, nil
}
