package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
)

var ErrSessionTimelineChanged = errors.New("session timeline source changed")

// Persisted witness formats need their own one-time rebuild even when the
// computed facts' rules do not change. The build marker uses this version too.
const sessionTimelineSourceWitnessVersion = 3

// SessionTimelineTracking says which source mutations Revision and PolicyEpoch
// account for. Tracked includes same-transaction writer bumps and the witness of
// raw rows, snapshots, events and copy markers.
type SessionTimelineTracking string

const (
	SessionTimelineTracked   SessionTimelineTracking = "tracked"
	SessionTimelineUntracked SessionTimelineTracking = "untracked"
)

// SessionTimelineState is the shared source and policy witness used by the
// timeline mapping and P4 facts, not an authorization decision.
type SessionTimelineState struct {
	SessionID                   string
	Tracking                    SessionTimelineTracking
	Revision, PolicyEpoch, Rows int64
	Witness                     []byte
}

type SessionTimelineMember struct {
	Timestamp                      time.Time
	RecordType, ProfileEmail, UUID string
	RecordID                       int64
	TurnKey                        string
}

// SessionTimelineKey is one raw row: its unique tuple and its id, which alone
// is not unique.
type SessionTimelineKey struct {
	Timestamp                      time.Time
	RecordType, ProfileEmail, UUID string
	RecordID                       int64
}

func (k SessionTimelineKey) tuple() sessionTimelineTuple {
	return sessionTimelineTuple{k.Timestamp.UnixMicro(), k.RecordType, k.ProfileEmail, k.UUID}
}

// SessionTimelineRow is a computed row: the turn it belongs to with its roles,
// or the reason it belongs to none.
type SessionTimelineRow struct {
	SessionTimelineKey
	TurnKey  string
	Roles    []string
	Unmapped string
}

// SessionTimelinePublish is one computation's timeline, as replaceSessionTimelineTx
// stores it. LoadedKeys are the rows the loader read, given apart from the
// computation: Rows must name exactly them, so a row the computation dropped is
// an error, not a raw row it never read. The latter alone are stored as
// not_loaded. State is "observed", "undeterminable" (Reason says why) or
// "truncated", which stores no rows. The public helper alone writes "prepared".
type SessionTimelinePublish struct {
	State, Reason                string
	LoadedKeys                   []SessionTimelineKey
	Rows                         []SessionTimelineRow
	TimelineHash, FactOutputHash []byte
	SourceGeneration             int64
	RulesVersion, MappingVersion int
}

// sessionTimelineNotLoaded is the internal reason of a raw row the loader did not
// read: one it may not show, of an excluded account or another agent. Readers
// must not tell those apart, and callers cannot claim it.
const sessionTimelineNotLoaded = "not_loaded"

func (s *PgStore) ReadSessionTimelineState(ctx context.Context, id string) (SessionTimelineState, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return SessionTimelineState{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='5s'`); err != nil {
		return SessionTimelineState{}, err
	}
	state, err := readSessionTimelineState(ctx, tx, id)
	if err != nil {
		return SessionTimelineState{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return SessionTimelineState{}, err
	}
	return state, nil
}

// readSessionTimelineState hashes every key of the session's raw rows in one
// statement. Any error returns the zero state: a partial witness is never issued.
func readSessionTimelineState(ctx context.Context, tx pgx.Tx, id string) (SessionTimelineState, error) {
	state, _, err := scanSessionTimeline(ctx, tx, id, false)
	return state, err
}

// readSessionTimelineKeys is readSessionTimelineState with the keys it hashed,
// from the same scan. Any error returns neither.
func readSessionTimelineKeys(ctx context.Context, tx pgx.Tx, id string) (SessionTimelineState, []SessionTimelineKey, error) {
	return scanSessionTimeline(ctx, tx, id, true)
}

func scanSessionTimeline(ctx context.Context, tx pgx.Tx, id string, keep bool) (SessionTimelineState, []SessionTimelineKey, error) {
	if id == "" {
		return SessionTimelineState{}, nil, errors.New("empty timeline session")
	}
	state := SessionTimelineState{SessionID: id, Tracking: SessionTimelineTracked}
	err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT revision FROM session_timeline_revisions WHERE session_id=$1),0),epoch FROM session_timeline_policy_epoch WHERE only_row`, id).Scan(&state.Revision, &state.PolicyEpoch)
	if err != nil {
		return SessionTimelineState{}, nil, err
	}
	rows, err := tx.Query(ctx, `SELECT sr.ts,sr.uuid,sr.id,sr.record_type,sr.profile_email,sr.xmin::text,
		NOT (EXISTS(SELECT 1 FROM excluded_sessions x WHERE x.session_id=sr.session_id)
			OR NOT (`+excludedAccountPredicateSQL("sr")+`))
		FROM session_records sr WHERE sr.session_id=$1
		ORDER BY sr.ts,sr.uuid COLLATE "C",sr.id,sr.record_type COLLATE "C",sr.profile_email COLLATE "C"`, id)
	if err != nil {
		return SessionTimelineState{}, nil, err
	}
	defer rows.Close()
	h := sha256.New()
	var b [8]byte
	put := func(v string) { binary.BigEndian.PutUint64(b[:], uint64(len(v))); h.Write(b[:]); h.Write([]byte(v)) }
	put(fmt.Sprintf("session-analysis-source-v%d", sessionTimelineSourceWitnessVersion))
	put(id)
	var keys []SessionTimelineKey
	for rows.Next() {
		var k SessionTimelineKey
		var xmin string
		var visible bool
		if err = rows.Scan(&k.Timestamp, &k.UUID, &k.RecordID, &k.RecordType, &k.ProfileEmail, &xmin, &visible); err != nil {
			return SessionTimelineState{}, nil, err
		}
		binary.BigEndian.PutUint64(b[:], uint64(k.Timestamp.UnixMicro()))
		h.Write(b[:])
		put(k.UUID)
		binary.BigEndian.PutUint64(b[:], uint64(k.RecordID))
		h.Write(b[:])
		put(k.RecordType)
		put(k.ProfileEmail)
		put(xmin)
		if visible {
			put("visible")
		} else {
			put("excluded")
		}
		state.Rows++
		if keep {
			keys = append(keys, k)
		}
	}
	if err = rows.Err(); err != nil {
		return SessionTimelineState{}, nil, err
	}
	binary.BigEndian.PutUint64(b[:], uint64(state.Rows))
	h.Write(b[:])
	if err := appendSessionTimelineSourceWitness(ctx, tx, id, put); err != nil {
		return SessionTimelineState{}, nil, err
	}
	state.Witness = h.Sum(nil)
	return state, keys, nil
}

func appendSessionTimelineSourceWitness(ctx context.Context, tx pgx.Tx, id string, put func(string)) error {
	appendRows := func(label, query string) error {
		put(label)
		rows, err := tx.Query(ctx, query, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		var count int64
		for rows.Next() {
			values, err := rows.Values()
			if err != nil {
				return err
			}
			for _, value := range values {
				if value == nil {
					put("<null>")
				} else {
					put(fmt.Sprint(value))
				}
			}
			count++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		put(fmt.Sprintf("rows:%d", count))
		return nil
	}
	if err := appendRows("snapshot-v1", `SELECT user_id,profile_email,login_email,billing_provider,account_id,agent,observed_at,
		sha256(convert_to(snapshot::text,'UTF8')),xmin::text,
		(NOT EXISTS(SELECT 1 FROM excluded_sessions x WHERE x.session_id=ss.session_id) AND `+excludedAccountPredicateSQL("ss")+`)
		FROM session_snapshots ss WHERE session_id=$1 ORDER BY user_id COLLATE "C",profile_email COLLATE "C"`); err != nil {
		return fmt.Errorf("session source snapshot witness: %w", err)
	}
	if err := appendRows("copy-marker-v1", `SELECT srec_id,original_session_id,record_type,ts,xmin::text
		FROM session_record_copies WHERE session_id=$1
		AND EXISTS(SELECT 1 FROM schema_backfills WHERE name='session_fact_copies_v1') ORDER BY srec_id`); err != nil {
		return fmt.Errorf("session source copy witness: %w", err)
	}
	// Transparent decompression cannot expose xmin. Hash every logical column
	// instead: compression must not change the witness, while same-key content
	// and identity rewrites (including non-API events) must still invalidate it.
	// IDs are not unique, so equal IDs need a content-stable tie order as well.
	if err := appendRows("otel-source-v2", `SELECT e.id,e.ts,e.event_name,
		sha256(convert_to(to_jsonb(e)::text,'UTF8')) AS content_hash
		FROM otel_events e WHERE e.session_id=$1 ORDER BY e.id,e.ts,e.event_name COLLATE "C",content_hash`); err != nil {
		return fmt.Errorf("session source event witness: %w", err)
	}
	if err := appendRows("cost-source-a-v1", `SELECT srec_id,ts,cost_usd::text,input_tokens,output_tokens,cache_read_tokens,cache_create_tokens,xmin::text
		FROM claude_imputed_cost WHERE session_id=$1 ORDER BY srec_id,ts,xmin::text`); err != nil {
		return fmt.Errorf("session source claude cost witness: %w", err)
	}
	if err := appendRows("cost-source-b-v1", `SELECT srec_id,ts,cost_usd::text,input_tokens,output_tokens,cache_read_tokens,cache_create_tokens,xmin::text
		FROM codex_imputed_cost WHERE session_id=$1 ORDER BY srec_id,ts,xmin::text`); err != nil {
		return fmt.Errorf("session source codex cost witness: %w", err)
	}
	if err := appendRows("visible-event-v1", `SELECT ts,prompt_id,COALESCE(cost_usd::text,''),user_id,profile_email,login_email,billing_provider
		FROM visible_events WHERE session_id=$1 AND event_name='api_request'
		AND NOT EXISTS(SELECT 1 FROM excluded_sessions x WHERE x.session_id=$1)
		ORDER BY ts,prompt_id COLLATE "C",user_id COLLATE "C",profile_email COLLATE "C",login_email COLLATE "C",billing_provider COLLATE "C",COALESCE(cost_usd::text,'')`); err != nil {
		return fmt.Errorf("session source visible-event witness: %w", err)
	}
	var copies, claims, excluded, deleted bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name='session_fact_copies_v1'),
		EXISTS(SELECT 1 FROM schema_backfills WHERE name='session_commit_claims_v1'),
		EXISTS(SELECT 1 FROM excluded_sessions WHERE session_id=$1),
		EXISTS(SELECT 1 FROM deleted_sessions WHERE session_id=$1)`, id).Scan(&copies, &claims, &excluded, &deleted); err != nil {
		return fmt.Errorf("session source gates: %w", err)
	}
	put("source-gates-v1")
	for _, value := range []bool{copies, claims, excluded, deleted} {
		if value {
			put("true")
		} else {
			put("false")
		}
	}
	return nil
}

func sameSessionTimelineState(a, b SessionTimelineState) bool {
	return a.SessionID == b.SessionID && a.Tracking == b.Tracking && a.Revision == b.Revision && a.PolicyEpoch == b.PolicyEpoch && a.Rows == b.Rows && bytes.Equal(a.Witness, b.Witness)
}

// sessionTimelineMappingValid reports whether the stored timeline is a
// published result -- observed, undeterminable or truncated, with the
// provenance its state requires -- of the current source at this mapping
// version. A "prepared" mapping, from the public helper or a row older than the
// published states, never is. It compares source provenance, never output_hash:
// a chunk drop changes the witness even though no writer recorded it.
func sessionTimelineMappingValid(ctx context.Context, tx pgx.Tx, current SessionTimelineState, version int) (bool, error) {
	stored := SessionTimelineState{SessionID: current.SessionID}
	var storedVersion, rules int
	var state, reason string
	var timelineHash, factHash []byte
	var generation *int64
	var rulesVersion *int
	err := tx.QueryRow(ctx, `SELECT tracking,source_revision,policy_epoch,source_rows,source_witness,mapping_version,
		timeline_state,timeline_reason,timeline_hash,fact_output_hash,source_generation,rules_version
		FROM session_timeline_provenance WHERE session_id=$1`, current.SessionID).Scan(&stored.Tracking, &stored.Revision, &stored.PolicyEpoch, &stored.Rows, &stored.Witness, &storedVersion,
		&state, &reason, &timelineHash, &factHash, &generation, &rulesVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if rulesVersion != nil {
		rules = *rulesVersion
	}
	published := len(factHash) == sha256.Size && generation != nil && *generation > 0 && rules > 0
	switch state {
	case "observed":
		published = published && len(timelineHash) == sha256.Size && reason == ""
	case "undeterminable":
		published = published && len(timelineHash) == sha256.Size && reason != ""
	case "truncated":
		published = published && timelineHash == nil
	default:
		published = false
	}
	return published && storedVersion == version && sameSessionTimelineState(stored, current), nil
}

type sessionTimelineTuple struct {
	ts                             int64
	recordType, profileEmail, uuid string
}

func validSessionTimelineMembers(members []SessionTimelineMember) error {
	seen := make(map[sessionTimelineTuple]struct{}, len(members))
	for _, m := range members {
		if m.TurnKey == "" {
			return errors.New("empty timeline turn key")
		}
		// TIMESTAMPTZ keeps microseconds. A finer instant would be stored as a
		// different tuple than the one the caller named.
		if !m.Timestamp.Truncate(time.Microsecond).Equal(m.Timestamp) {
			return errors.New("timeline source timestamp finer than a microsecond")
		}
		k := sessionTimelineTuple{m.Timestamp.UnixMicro(), m.RecordType, m.ProfileEmail, m.UUID}
		if _, ok := seen[k]; ok {
			return errors.New("duplicate timeline source tuple")
		}
		seen[k] = struct{}{}
	}
	return nil
}

// ReplaceSessionTimelineMapping atomically replaces one session's explicit
// source membership as a "prepared" mapping: rows it does not name are stored
// not_loaded and nothing marks it usable. It is intentionally not connected to
// the fact publisher, which calls replaceSessionTimelineTx in its own
// transaction.
//
// It takes the publisher's retention -> source locks and then the per-session
// deletion-target lock, the order every source writer uses, and blocks no other
// session. That fences the cooperative session_records writers, retention and
// strict deletion for this session. The shared witness also covers raw record,
// snapshot, copy-marker and event changes, including writes that missed a queue
// bump.
//
// Read committed is deliberate: a repeatable-read snapshot would be taken by the
// first lock statement, before the wait, and miss a writer that committed while
// this transaction waited for it.
func (s *PgStore) ReplaceSessionTimelineMapping(ctx context.Context, expected SessionTimelineState, version int, members []SessionTimelineMember) error {
	if err := validSessionTimelineMembers(members); err != nil {
		return err
	}
	p := SessionTimelinePublish{State: "prepared", MappingVersion: version}
	for _, m := range members {
		k := SessionTimelineKey{Timestamp: m.Timestamp, RecordType: m.RecordType, ProfileEmail: m.ProfileEmail, UUID: m.UUID, RecordID: m.RecordID}
		p.LoadedKeys = append(p.LoadedKeys, k)
		p.Rows = append(p.Rows, SessionTimelineRow{SessionTimelineKey: k, TurnKey: m.TurnKey})
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockSessionFactPublish(ctx, tx); err != nil {
		return err
	}
	if err = lockSessionDeletionTargets(ctx, tx, []string{expected.SessionID}); err != nil {
		return err
	}
	var deleted bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deleted_sessions WHERE session_id=$1)`, expected.SessionID).Scan(&deleted); err != nil {
		return err
	}
	if deleted {
		return ErrSessionTimelineChanged
	}
	if err = replaceSessionTimelineTx(ctx, tx, expected, p); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// validSessionTimelinePublish checks what needs no database: the state's
// columns, and each row's shape.
func validSessionTimelinePublish(p SessionTimelinePublish) error {
	hashed := len(p.FactOutputHash) == sha256.Size && p.SourceGeneration > 0 && p.RulesVersion > 0
	switch {
	case p.MappingVersion <= 0:
		return errors.New("invalid timeline mapping version")
	case p.State == "prepared":
		if p.Reason != "" || p.TimelineHash != nil || p.FactOutputHash != nil || p.SourceGeneration != 0 || p.RulesVersion != 0 {
			return errors.New("a prepared timeline carries no publish provenance")
		}
	case p.State == "truncated":
		if !hashed || p.TimelineHash != nil || len(p.LoadedKeys) != 0 || len(p.Rows) != 0 {
			return errors.New("invalid truncated timeline")
		}
	case p.State == "observed" || p.State == "undeterminable":
		if !hashed || len(p.TimelineHash) != sha256.Size || (p.State == "undeterminable") != (p.Reason != "") {
			return errors.New("invalid timeline provenance")
		}
	default:
		return fmt.Errorf("invalid timeline state %q", p.State)
	}
	for _, r := range p.Rows {
		if !r.Timestamp.Truncate(time.Microsecond).Equal(r.Timestamp) {
			return errors.New("timeline source timestamp finer than a microsecond")
		}
		mapped := r.TurnKey != "" && r.Unmapped == ""
		unmapped := r.TurnKey == "" && r.Unmapped != "" && len(r.Roles) == 0 && r.Unmapped != sessionTimelineNotLoaded
		if !mapped && !unmapped {
			return errors.New("timeline row is neither placed nor unplaced")
		}
		for _, role := range r.Roles {
			if role == "" {
				return errors.New("empty timeline role")
			}
		}
	}
	return nil
}

// replaceSessionTimelineTx stores one session's whole timeline -- a row for every
// raw row -- and its provenance in tx, replacing what was there, whether or not
// the facts beside it changed.
//
// The caller holds lockSessionFactPublish and lockSessionDeletionTargets for the
// session in a read committed transaction and has checked it has no tombstone.
// Any error leaves tx to be rolled back whole, facts included: nothing here is
// partial.
func replaceSessionTimelineTx(ctx context.Context, tx pgx.Tx, expected SessionTimelineState, p SessionTimelinePublish) error {
	if expected.SessionID == "" || expected.Tracking != SessionTimelineTracked || len(expected.Witness) != sha256.Size {
		return errors.New("invalid timeline provenance")
	}
	if err := validSessionTimelinePublish(p); err != nil {
		return err
	}
	current, keys, err := readSessionTimelineKeys(ctx, tx, expected.SessionID)
	if err != nil {
		return err
	}
	if !sameSessionTimelineState(expected, current) {
		return ErrSessionTimelineChanged
	}
	return storeSessionTimelineTx(ctx, tx, current, keys, p)
}

// storeSessionTimelineTx is replaceSessionTimelineTx after its source read:
// current and keys are the session's raw rows as read in tx under the same
// locks, already compared with what the computation loaded.
func storeSessionTimelineTx(ctx context.Context, tx pgx.Tx, current SessionTimelineState, keys []SessionTimelineKey, p SessionTimelinePublish) error {
	if err := validSessionTimelinePublish(p); err != nil {
		return err
	}
	source := make(map[sessionTimelineTuple]int64, len(keys))
	for _, k := range keys {
		source[k.tuple()] = k.RecordID
	}
	// A key must be a raw row's, tuple and id both.
	exists := func(k SessionTimelineKey) bool {
		id, ok := source[k.tuple()]
		return ok && id == k.RecordID
	}
	loaded := make(map[sessionTimelineTuple]bool, len(p.LoadedKeys))
	for _, k := range p.LoadedKeys {
		if loaded[k.tuple()] {
			return errors.New("duplicate loaded timeline tuple")
		}
		if !exists(k) {
			return ErrSessionTimelineChanged
		}
		loaded[k.tuple()] = true
	}
	named := make(map[sessionTimelineTuple]bool, len(p.Rows))
	for _, r := range p.Rows {
		switch {
		case named[r.tuple()]:
			return errors.New("duplicate timeline source tuple")
		case !exists(r.SessionTimelineKey):
			return ErrSessionTimelineChanged
		case !loaded[r.tuple()]:
			return errors.New("timeline row the loader did not read")
		}
		named[r.tuple()] = true
	}
	if len(named) != len(loaded) {
		return errors.New("timeline rows do not cover the loaded rows")
	}
	// The same publish of the same source is stored already: only the
	// generation moves, and the rows are not written again.
	if same, err := sameStoredSessionTimeline(ctx, tx, current, p); err != nil || same {
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE session_timeline_provenance SET source_generation=$2 WHERE session_id=$1`, current.SessionID, p.SourceGeneration)
		}
		return err
	}
	rows := p.Rows
	if p.State != "truncated" {
		rows = append([]SessionTimelineRow(nil), p.Rows...)
		for _, k := range keys {
			if !loaded[k.tuple()] {
				rows = append(rows, SessionTimelineRow{SessionTimelineKey: k, Unmapped: sessionTimelineNotLoaded})
			}
		}
		if int64(len(rows)) != current.Rows {
			return errors.New("timeline rows do not cover the source")
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_timeline_members WHERE session_id=$1`, current.SessionID); err != nil {
		return err
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"session_timeline_members"}, []string{"session_id", "source_ts", "source_record_type", "source_profile_email", "source_uuid", "source_record_id", "turn_key", "roles", "unmapped_reason"}, pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
		r := rows[i]
		var turn, reason *string
		if r.TurnKey != "" {
			turn = &r.TurnKey
		} else {
			reason = &r.Unmapped
		}
		roles := r.Roles
		if roles == nil {
			roles = []string{}
		}
		return []any{current.SessionID, r.Timestamp, r.RecordType, r.ProfileEmail, r.UUID, r.RecordID, turn, roles, reason}, nil
	}))
	if err != nil {
		return fmt.Errorf("copy timeline members: %w", err)
	}
	// The whole tuple and its id must name an existing row; an id alone is not
	// unique across the hypertable.
	var missing bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_timeline_members m WHERE m.session_id=$1 AND NOT EXISTS(SELECT 1 FROM session_records r WHERE r.session_id=m.session_id AND r.ts=m.source_ts AND r.record_type=m.source_record_type AND r.profile_email=m.source_profile_email AND r.uuid=m.source_uuid AND r.id=m.source_record_id))`, current.SessionID).Scan(&missing)
	if err != nil {
		return err
	}
	if missing {
		return ErrSessionTimelineChanged
	}
	var generation *int64
	var rules *int
	if p.State != "prepared" {
		generation, rules = &p.SourceGeneration, &p.RulesVersion
	}
	_, err = tx.Exec(ctx, `INSERT INTO session_timeline_provenance(session_id,tracking,source_revision,policy_epoch,source_rows,source_witness,mapping_version,
		timeline_state,timeline_reason,timeline_hash,fact_output_hash,source_generation,rules_version)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT(session_id) DO UPDATE SET tracking=excluded.tracking,source_revision=excluded.source_revision,policy_epoch=excluded.policy_epoch,
		source_rows=excluded.source_rows,source_witness=excluded.source_witness,mapping_version=excluded.mapping_version,
		timeline_state=excluded.timeline_state,timeline_reason=excluded.timeline_reason,timeline_hash=excluded.timeline_hash,
		fact_output_hash=excluded.fact_output_hash,source_generation=excluded.source_generation,rules_version=excluded.rules_version`,
		current.SessionID, string(current.Tracking), current.Revision, current.PolicyEpoch, current.Rows, current.Witness, p.MappingVersion,
		p.State, p.Reason, p.TimelineHash, p.FactOutputHash, generation, rules)
	return err
}

// sameStoredSessionTimeline reports whether what is stored is this publish of
// this source already: the same tracking, witness, state and reason, both
// hashes, rules and mapping version. The timeline hash covers every row, the
// loaded ones and so the unloaded rest too, so only the generation can differ.
// A prepared mapping, or a truncated publish, is never the same.
func sameStoredSessionTimeline(ctx context.Context, tx pgx.Tx, current SessionTimelineState, p SessionTimelinePublish) (bool, error) {
	if p.State != "observed" && p.State != "undeterminable" {
		return false, nil
	}
	stored := SessionTimelineState{SessionID: current.SessionID}
	var state, reason string
	var timelineHash, factHash []byte
	var rules *int
	var version int
	err := tx.QueryRow(ctx, `SELECT tracking,source_revision,policy_epoch,source_rows,source_witness,mapping_version,
		timeline_state,timeline_reason,timeline_hash,fact_output_hash,rules_version
		FROM session_timeline_provenance WHERE session_id=$1`, current.SessionID).Scan(&stored.Tracking, &stored.Revision, &stored.PolicyEpoch, &stored.Rows, &stored.Witness, &version,
		&state, &reason, &timelineHash, &factHash, &rules)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return sameSessionTimelineState(stored, current) && version == p.MappingVersion && state == p.State && reason == p.Reason &&
		bytes.Equal(timelineHash, p.TimelineHash) && bytes.Equal(factHash, p.FactOutputHash) && rules != nil && *rules == p.RulesVersion, nil
}

// sessionTimelineMappingVersion is the shape of the rows the publisher stores.
const sessionTimelineMappingVersion = 1

// clearSessionTimelineTx removes a session's mapping and provenance. Absence
// means "not computed": a truncated or fallback publish, a computation with no
// timeline, or rows that arrived without their storage key. Readers must not
// read it as zero rows or as complete.
func clearSessionTimelineTx(ctx context.Context, tx pgx.Tx, id string) error {
	for _, table := range []string{"session_timeline_members", "session_timeline_provenance"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE session_id=$1`, id); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	return nil
}

// sessionTimelineSource is the session's raw key set read in the publisher's
// transaction, under its locks, before any fact is written.
type sessionTimelineSource struct {
	state SessionTimelineState
	keys  []SessionTimelineKey
}

// storesSessionTimeline reports whether a publish stores a timeline at all:
// a full computation's observed or undeterminable timeline with the witness of
// its own snapshot. A fallback load carries no witness, so a full read's older
// one is never taken for it.
func storesSessionTimeline(load SessionFactLoad, timeline *sessionFactTimeline) bool {
	return timeline != nil && timeline.HasTimeline && load.Truncated == nil && load.TimelineWitness != nil &&
		(timeline.Facts.State == "observed" || timeline.Facts.State == "undeterminable")
}

// readSessionTimelineSourceTx is the publisher's check before it deletes or
// inserts facts: nil when no timeline will be stored, ErrSessionTimelineChanged
// when the raw rows moved since the load, so nothing is written and the queue
// row stays.
func readSessionTimelineSourceTx(ctx context.Context, tx pgx.Tx, id string, load SessionFactLoad, timeline *sessionFactTimeline) (*sessionTimelineSource, error) {
	if !storesSessionTimeline(load, timeline) {
		return nil, nil
	}
	current, keys, err := readSessionTimelineKeys(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if !sameSessionTimelineState(*load.TimelineWitness, current) {
		return nil, ErrSessionTimelineChanged
	}
	return &sessionTimelineSource{current, keys}, nil
}

// publishSessionTimelineTx stores the timeline beside the facts the publisher
// is writing in tx, from the source readSessionTimelineSourceTx read; with
// none it removes the session's mapping. The loaded keys come from the input
// itself, not from the computation, so a row the computation dropped is an
// error rather than an unloaded one. output.Hash covers session, turn, activity,
// commit and task facts; the timeline keeps its own extended hash.
func publishSessionTimelineTx(ctx context.Context, tx pgx.Tx, id string, load SessionFactLoad, output sessionfacts.Output, timeline *sessionFactTimeline, source *sessionTimelineSource) error {
	if source == nil {
		return clearSessionTimelineTx(ctx, tx, id)
	}
	loadedKeys := load.LoadedKeys
	if loadedKeys == nil {
		loadedKeys = sessionTimelineLoadedKeys(load.Input)
	}
	p := SessionTimelinePublish{
		State: timeline.Facts.State, Reason: timeline.Facts.Reason,
		LoadedKeys:   loadedKeys,
		TimelineHash: timeline.Hash, FactOutputHash: output.Hash,
		SourceGeneration: load.Generation, RulesVersion: sessionfacts.RulesVersion, MappingVersion: sessionTimelineMappingVersion,
	}
	for _, r := range timeline.Facts.Rows {
		p.Rows = append(p.Rows, SessionTimelineRow{
			SessionTimelineKey: SessionTimelineKey{Timestamp: r.TS, RecordType: r.RecordType, ProfileEmail: r.ProfileEmail, UUID: r.UUID, RecordID: r.ID},
			TurnKey:            r.TurnKey, Roles: r.Roles, Unmapped: r.Unmapped,
		})
	}
	return storeSessionTimelineTx(ctx, tx, source.state, source.keys, p)
}

// sessionTimelineLoadedKeys are the rows the loader read for the computation:
// a Codex session's Codex rows, any other session's Claude rows.
func sessionTimelineLoadedKeys(in sessionfacts.Input) []SessionTimelineKey {
	var keys []SessionTimelineKey
	if in.Agent == "codex" {
		for _, r := range in.CodexRows {
			keys = append(keys, SessionTimelineKey{Timestamp: r.TS, RecordType: r.RecordType, ProfileEmail: r.ProfileEmail, UUID: r.UUID, RecordID: r.ID})
		}
		return keys
	}
	for _, r := range in.Rows {
		keys = append(keys, SessionTimelineKey{Timestamp: r.TS, RecordType: r.RecordType, ProfileEmail: r.ProfileEmail, UUID: r.UUID, RecordID: r.ID})
	}
	return keys
}
