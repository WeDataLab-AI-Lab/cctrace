package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
)

const (
	sessionFactCopyMemberLimit = 1000
	sessionFactCopyRowLimit    = 500000
)

type sessionFactCopyGeneration struct {
	SessionID         string
	Generation        int64
	RemovalGeneration int64
}

type sessionFactCopyGroup struct {
	Members     []string
	Generations []sessionFactCopyGeneration
	Existing    []sessionfacts.CopyMarker
	Desired     []sessionfacts.CopyMarker
	Skipped     bool
}

var errSessionFactCopyGroupChanged = errors.New("session fact copy group changed")

func verifySessionFactCopyGroup(ctx context.Context, tx pgx.Tx, group *sessionFactCopyGroup, sessionID string) error {
	if group == nil || group.Skipped {
		return nil
	}
	var tombstoned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deleted_sessions WHERE session_id=ANY($1::text[]))`, group.Members).Scan(&tombstoned); err != nil {
		return err
	}
	if tombstoned {
		return errSessionFactCopyGroupChanged
	}
	current, err := loadSessionFactCopyGenerations(ctx, tx, group.Members)
	if err != nil {
		return err
	}
	// A singleton has no peer generation to protect; its caller owns the queue
	// generation fence. With actual peers, every member's generation is part of
	// the copy witness, including the current session's.
	withoutSession := func(generations []sessionFactCopyGeneration) []sessionFactCopyGeneration {
		filtered := make([]sessionFactCopyGeneration, 0, len(generations))
		for _, generation := range generations {
			if generation.SessionID != sessionID {
				filtered = append(filtered, generation)
			}
		}
		return filtered
	}
	expectedGenerations, currentGenerations := group.Generations, current
	if len(group.Members) == 1 && group.Members[0] == sessionID {
		expectedGenerations, currentGenerations = withoutSession(expectedGenerations), withoutSession(currentGenerations)
	}
	if !sameSessionFactCopyGenerations(expectedGenerations, currentGenerations) {
		return errSessionFactCopyGroupChanged
	}
	markers, err := loadSessionFactCopyMarkers(ctx, tx, group.Members)
	if err != nil {
		return err
	}
	if !sameSessionFactCopyMarkers(group.Existing, markers) {
		return errSessionFactCopyGroupChanged
	}
	return nil
}

func sameSessionFactCopyGenerations(a, b []sessionFactCopyGeneration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameSessionFactCopyMarkers(a, b []sessionfacts.CopyMarker) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].SRecID != b[i].SRecID || a[i].SessionID != b[i].SessionID ||
			a[i].OriginalSessionID != b[i].OriginalSessionID || a[i].RecordType != b[i].RecordType || !a[i].TS.Equal(b[i].TS) {
			return false
		}
	}
	return true
}

// applySessionFactCopyMarkers writes the classifier's marker diff in the
// caller's publish transaction. It returns the copied-line sessions whose
// derived facts need refresh when the shared predicate is enabled.
func applySessionFactCopyMarkers(ctx context.Context, tx pgx.Tx, group *sessionFactCopyGroup) ([]string, error) {
	if group == nil || group.Skipped || sameSessionFactCopyMarkers(group.Existing, group.Desired) {
		return nil, nil
	}
	existing := make(map[int64]sessionfacts.CopyMarker, len(group.Existing))
	desired := make(map[int64]sessionfacts.CopyMarker, len(group.Desired))
	for _, marker := range group.Existing {
		existing[marker.SRecID] = marker
	}
	for _, marker := range group.Desired {
		desired[marker.SRecID] = marker
	}
	var remove []int64
	changed := make(map[string]struct{})
	for id, old := range existing {
		current, ok := desired[id]
		if !ok || !sameSessionFactCopyMarkers([]sessionfacts.CopyMarker{old}, []sessionfacts.CopyMarker{current}) {
			remove = append(remove, id)
			changed[old.SessionID] = struct{}{}
			if ok {
				changed[current.SessionID] = struct{}{}
			}
		}
	}
	var add []sessionfacts.CopyMarker
	for id, current := range desired {
		old, ok := existing[id]
		if !ok || !sameSessionFactCopyMarkers([]sessionfacts.CopyMarker{old}, []sessionfacts.CopyMarker{current}) {
			add = append(add, current)
			changed[current.SessionID] = struct{}{}
		}
	}
	if len(remove) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM session_record_copies WHERE srec_id=ANY($1::bigint[])`, remove); err != nil {
			return nil, fmt.Errorf("delete changed copy markers: %w", err)
		}
	}
	if len(add) > 0 {
		sort.Slice(add, func(i, j int) bool { return add[i].SRecID < add[j].SRecID })
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"session_record_copies"},
			[]string{"srec_id", "session_id", "original_session_id", "record_type", "ts"},
			pgx.CopyFromSlice(len(add), func(i int) ([]any, error) {
				marker := add[i]
				return []any{marker.SRecID, marker.SessionID, marker.OriginalSessionID, marker.RecordType, marker.TS}, nil
			})); err != nil {
			return nil, fmt.Errorf("insert changed copy markers: %w", err)
		}
	}
	changedIDs := make([]string, 0, len(changed))
	for id := range changed {
		if id != "" {
			changedIDs = append(changedIDs, id)
		}
	}
	return uniqueSortedSessionIDs(changedIDs), nil
}

func loadSessionFactCopyGroup(ctx context.Context, tx pgx.Tx, sessionID string, scanFromID *int64) (*sessionFactCopyGroup, error) {
	keys, err := loadSessionFactNewKeys(ctx, tx, sessionID, scanFromID)
	if err != nil {
		return nil, err
	}
	neighbors, err := matchingSessionFactCopyNeighbors(ctx, tx, sessionID, keys)
	if err != nil {
		return nil, err
	}
	seeds := append([]string{sessionID}, neighbors...)
	seeds = uniqueSortedSessionIDs(seeds)
	if len(seeds) > sessionFactCopyMemberLimit {
		return &sessionFactCopyGroup{Members: seeds[:sessionFactCopyMemberLimit+1], Skipped: true}, nil
	}
	members, err := expandSessionFactCopyGroup(ctx, tx, seeds)
	if err != nil {
		return nil, err
	}
	group := &sessionFactCopyGroup{Members: members}
	if len(members) > sessionFactCopyMemberLimit {
		group.Skipped = true
		return group, nil
	}
	rows, err := loadSessionFactCopyRecords(ctx, tx, members)
	if err != nil {
		return nil, err
	}
	if len(rows) > sessionFactCopyRowLimit {
		group.Skipped = true
		return group, nil
	}
	continued, err := loadSessionFactContinuedIn(ctx, tx, members)
	if err != nil {
		return nil, err
	}
	group.Generations, err = loadSessionFactCopyGenerations(ctx, tx, members)
	if err != nil {
		return nil, err
	}
	group.Existing, err = loadSessionFactCopyMarkers(ctx, tx, members)
	if err != nil {
		return nil, err
	}
	group.Desired = sessionfacts.ClassifyCopies(rows, continued, sessionFactCopyRowLimit).Markers
	return group, nil
}

func (s *PgStore) processSessionFactCopyOnly(ctx context.Context, sessionID string, picked int64) (string, error) {
	readTx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", err
	}
	defer readTx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := readTx.Exec(ctx, sessionFactStatementLimit); err != nil {
		return "", err
	}
	var generation, removal int64
	var scanFromID *int64
	if err := readTx.QueryRow(ctx, `SELECT generation,removal_generation,scan_from_id
		FROM session_fact_dirty WHERE session_id=$1`, sessionID).Scan(&generation, &removal, &scanFromID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "stale", nil
		}
		return "", err
	}
	if generation != picked || removal > generation {
		return "stale", nil
	}
	var loadedGate bool
	if err := readTx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name='session_fact_copies_v1')`).Scan(&loadedGate); err != nil {
		return "", err
	}
	group, err := loadSessionFactCopyGroup(ctx, readTx, sessionID, scanFromID)
	if err != nil {
		return "", err
	}
	if err := readTx.Commit(ctx); err != nil {
		return "", err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionFactPublish(ctx, tx); err != nil {
		return "", err
	}
	lockIDs := []string{sessionID}
	if !group.Skipped {
		lockIDs = append(lockIDs, group.Members...)
	}
	if err := lockSessionDeletionTargets(ctx, tx, lockIDs); err != nil {
		return "", err
	}
	var tombstoned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deleted_sessions WHERE session_id=$1)`, sessionID).Scan(&tombstoned); err != nil {
		return "", err
	}
	if tombstoned {
		if _, err := tx.Exec(ctx, `DELETE FROM session_fact_dirty WHERE session_id=$1`, sessionID); err != nil {
			return "", err
		}
		return "tombstone", tx.Commit(ctx)
	}
	var currentGeneration, currentRemoval int64
	if err := tx.QueryRow(ctx, `SELECT generation,removal_generation FROM session_fact_dirty WHERE session_id=$1`, sessionID).Scan(&currentGeneration, &currentRemoval); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "stale", nil
		}
		return "", err
	}
	if currentGeneration != picked || currentRemoval > picked {
		return "stale", nil
	}
	var currentGate bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name='session_fact_copies_v1')`).Scan(&currentGate); err != nil {
		return "", err
	}
	if currentGate != loadedGate {
		return "copy_gate", nil
	}
	if err := verifySessionFactCopyGroup(ctx, tx, group, sessionID); errors.Is(err, errSessionFactCopyGroupChanged) {
		return "copy_source", nil
	} else if err != nil {
		return "", err
	}
	var hasSupportedSource bool
	if err := tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM visible_session_records WHERE session_id=$1 AND COALESCE(NULLIF(agent,''),'claude') IN ('claude','codex'))
		OR EXISTS(SELECT 1 FROM visible_events WHERE session_id=$1 AND event_name='api_request' AND agent IN ('claude','codex')
			AND NOT EXISTS(SELECT 1 FROM excluded_sessions x WHERE x.session_id=$1))
		OR EXISTS(SELECT 1 FROM session_snapshots ss WHERE `+sessionFactSnapshotVisibleSQL+` AND ss.agent IN ('claude','codex'))`, sessionID).Scan(&hasSupportedSource); err != nil {
		return "", err
	}
	if hasSupportedSource {
		return "source", nil
	}
	changed, err := applySessionFactCopyMarkers(ctx, tx, group)
	if err != nil {
		return "", err
	}
	if currentGate && len(changed) > 0 {
		if err := refreshSessionFactCopyDerived(ctx, tx, changed); err != nil {
			return "", err
		}
	}
	if err := deleteSessionFacts(ctx, tx, []string{sessionID}); err != nil {
		return "", err
	}
	if currentGate && len(changed) > 0 {
		if err := markSessionFactsDirty(ctx, tx, changed, nil, dirtyPublishPeer); err != nil {
			return "", err
		}
	}
	deleted, err := tx.Exec(ctx, `DELETE FROM session_fact_dirty WHERE session_id=$1 AND generation=$2`, sessionID, picked)
	if err != nil {
		return "", err
	}
	outcome := "copy_only"
	if containsSessionID(changed, sessionID) && currentGate {
		outcome = "copy_requeued"
	} else if deleted.RowsAffected() == 0 {
		outcome = "requeued"
	}
	return outcome, tx.Commit(ctx)
}

type sessionFactCopyKey struct {
	Agent, UUID, ResponseID string
	TS                      time.Time
}

func loadSessionFactNewKeys(ctx context.Context, tx pgx.Tx, sessionID string, scanFromID *int64) ([]sessionFactCopyKey, error) {
	if scanFromID == nil {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT 'claude', uuid, '', ts
		FROM session_records WHERE session_id=$1 AND id >= $2
		AND COALESCE(NULLIF(agent,''),'claude')='claude'
		AND record_type IN ('user','assistant','system','attachment')
		AND uuid<>'' AND uuid NOT LIKE 'content-%' AND ts>'epoch'
		UNION ALL
		SELECT 'codex', '', raw->'payload'->>'response_id', ts
		FROM session_records WHERE session_id=$1 AND id >= $2
		AND agent='codex' AND record_type='usage'
		AND COALESCE(raw->'payload'->>'response_id','')<>'' AND ts>'epoch'`, sessionID, *scanFromID)
	if err != nil {
		return nil, fmt.Errorf("read new copy keys: %w", err)
	}
	defer rows.Close()
	var keys []sessionFactCopyKey
	for rows.Next() {
		var key sessionFactCopyKey
		if err := rows.Scan(&key.Agent, &key.UUID, &key.ResponseID, &key.TS); err != nil {
			return nil, fmt.Errorf("scan new copy key: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read new copy keys: %w", err)
	}
	return keys, nil
}

func matchingSessionFactCopyNeighbors(ctx context.Context, tx pgx.Tx, sessionID string, keys []sessionFactCopyKey) ([]string, error) {
	var claudeUUIDs, codexIDs []string
	var claudeTS, codexTS []time.Time
	for _, key := range keys {
		switch key.Agent {
		case "claude":
			claudeUUIDs, claudeTS = append(claudeUUIDs, key.UUID), append(claudeTS, key.TS)
		case "codex":
			codexIDs, codexTS = append(codexIDs, key.ResponseID), append(codexTS, key.TS)
		}
	}
	set := make(map[string]struct{})
	if len(claudeUUIDs) > 0 {
		rows, err := tx.Query(ctx, `SELECT DISTINCT other.session_id
			FROM unnest($1::text[],$2::timestamptz[]) k(uuid,ts)
			JOIN session_records other ON other.uuid=k.uuid AND other.ts=k.ts
			WHERE other.session_id<>$3 AND other.ts BETWEEN (SELECT min(x) FROM unnest($2::timestamptz[]) x)
				AND (SELECT max(x) FROM unnest($2::timestamptz[]) x)
				AND COALESCE(NULLIF(other.agent,''),'claude')='claude'
				AND other.record_type IN ('user','assistant','system','attachment')
				AND other.uuid<>'' AND other.uuid NOT LIKE 'content-%'
				AND NOT EXISTS(SELECT 1 FROM deleted_sessions d WHERE d.session_id=other.session_id) LIMIT 1001`, claudeUUIDs, claudeTS, sessionID)
		if err != nil {
			return nil, fmt.Errorf("find Claude copy neighbors: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			set[id] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("find Claude copy neighbors: %w", err)
		}
		rows.Close()
	}
	if len(codexIDs) > 0 {
		rows, err := tx.Query(ctx, `SELECT DISTINCT other.session_id
			FROM unnest($1::text[],$2::timestamptz[]) k(response_id,ts)
			JOIN session_records other ON other.agent='codex' AND other.record_type='usage'
				AND other.ts=k.ts AND other.raw->'payload'->>'response_id'=k.response_id
			WHERE other.session_id<>$3 AND other.ts BETWEEN (SELECT min(x) FROM unnest($2::timestamptz[]) x)
				AND (SELECT max(x) FROM unnest($2::timestamptz[]) x)
				AND NOT EXISTS(SELECT 1 FROM deleted_sessions d WHERE d.session_id=other.session_id) LIMIT 1001`, codexIDs, codexTS, sessionID)
		if err != nil {
			return nil, fmt.Errorf("find Codex copy neighbors: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			set[id] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("find Codex copy neighbors: %w", err)
		}
		rows.Close()
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func expandSessionFactCopyGroup(ctx context.Context, tx pgx.Tx, seeds []string) ([]string, error) {
	visited := make(map[string]struct{}, len(seeds))
	frontier := make([]string, 0, len(seeds))
	for _, id := range seeds {
		if id == "" {
			continue
		}
		visited[id] = struct{}{}
		frontier = append(frontier, id)
	}
	if len(visited) > sessionFactCopyMemberLimit {
		return sortedSessionFactCopyIDs(visited, sessionFactCopyMemberLimit+1), nil
	}
	for len(frontier) > 0 {
		rows, err := tx.Query(ctx, `SELECT other_id FROM (
			SELECT original_session_id AS other_id FROM session_record_copies WHERE session_id=ANY($1::text[])
			UNION SELECT session_id AS other_id FROM session_record_copies WHERE original_session_id=ANY($1::text[])
		) n WHERE other_id<>'' AND NOT EXISTS(SELECT 1 FROM deleted_sessions d WHERE d.session_id=n.other_id)
		LIMIT $2`, frontier, sessionFactCopyMemberLimit+1)
		if err != nil {
			return nil, fmt.Errorf("expand copy group: %w", err)
		}
		neighbors, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, fmt.Errorf("expand copy group: %w", err)
		}
		frontier = frontier[:0]
		for _, id := range neighbors {
			if _, seen := visited[id]; seen {
				continue
			}
			visited[id] = struct{}{}
			frontier = append(frontier, id)
			if len(visited) > sessionFactCopyMemberLimit {
				return sortedSessionFactCopyIDs(visited, sessionFactCopyMemberLimit+1), nil
			}
		}
	}
	return sortedSessionFactCopyIDs(visited, len(visited)), nil
}

func sortedSessionFactCopyIDs(ids map[string]struct{}, limit int) []string {
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

func loadSessionFactCopyRecords(ctx context.Context, tx pgx.Tx, members []string) ([]sessionfacts.CopyRecord, error) {
	rows, err := tx.Query(ctx, `SELECT id,session_id,COALESCE(NULLIF(agent,''),'claude'),record_type,uuid,
		COALESCE(raw->'payload'->>'response_id',''),
		COALESCE(NULLIF(message_id,''),NULLIF(raw->'message'->>'id',''),''),
		forked_from_session,ts
		FROM session_records
		WHERE session_id=ANY($1::text[]) AND (
			(COALESCE(NULLIF(agent,''),'claude')='claude' AND (
				(record_type IN ('user','assistant','system','attachment') AND uuid<>'' AND uuid NOT LIKE 'content-%' AND ts>'epoch')
				OR COALESCE(NULLIF(message_id,''),NULLIF(raw->'message'->>'id',''))<>''))
			OR (agent='codex' AND record_type='usage' AND COALESCE(raw->'payload'->>'response_id','')<>'' AND ts>'epoch')
		)
		ORDER BY session_id COLLATE "C",id LIMIT $2`, members, sessionFactCopyRowLimit+1)
	if err != nil {
		return nil, fmt.Errorf("read copy group records: %w", err)
	}
	defer rows.Close()
	var records []sessionfacts.CopyRecord
	for rows.Next() {
		var record sessionfacts.CopyRecord
		if err := rows.Scan(&record.ID, &record.SessionID, &record.Agent, &record.RecordType,
			&record.UUID, &record.ResponseID, &record.RequestKey, &record.ForkedFromSession, &record.TS); err != nil {
			return nil, fmt.Errorf("scan copy group record: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read copy group records: %w", err)
	}
	return records, nil
}

func loadSessionFactContinuedIn(ctx context.Context, tx pgx.Tx, members []string) (map[string][]string, error) {
	rows, err := tx.Query(ctx, `SELECT ss.session_id, jsonb_array_elements_text(
		CASE WHEN jsonb_typeof(ss.snapshot->'continued_in')='array' THEN ss.snapshot->'continued_in' ELSE '[]'::jsonb END)
		FROM session_snapshots ss WHERE ss.session_id=ANY($1::text[])`, members)
	if err != nil {
		return nil, fmt.Errorf("read copy continuation markers: %w", err)
	}
	defer rows.Close()
	continued := make(map[string][]string)
	for rows.Next() {
		var sessionID, other string
		if err := rows.Scan(&sessionID, &other); err != nil {
			return nil, fmt.Errorf("scan copy continuation marker: %w", err)
		}
		continued[sessionID] = append(continued[sessionID], other)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read copy continuation markers: %w", err)
	}
	return continued, nil
}

func loadSessionFactCopyGenerations(ctx context.Context, tx pgx.Tx, members []string) ([]sessionFactCopyGeneration, error) {
	rows, err := tx.Query(ctx, `SELECT u.session_id,COALESCE(d.generation,0),COALESCE(d.removal_generation,0)
		FROM unnest($1::text[]) u(session_id) LEFT JOIN session_fact_dirty d USING(session_id)
		ORDER BY u.session_id COLLATE "C"`, members)
	if err != nil {
		return nil, fmt.Errorf("read copy group generations: %w", err)
	}
	defer rows.Close()
	var generations []sessionFactCopyGeneration
	for rows.Next() {
		var item sessionFactCopyGeneration
		if err := rows.Scan(&item.SessionID, &item.Generation, &item.RemovalGeneration); err != nil {
			return nil, fmt.Errorf("scan copy group generation: %w", err)
		}
		generations = append(generations, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read copy group generations: %w", err)
	}
	return generations, nil
}

func loadSessionFactCopyMarkers(ctx context.Context, tx pgx.Tx, members []string) ([]sessionfacts.CopyMarker, error) {
	rows, err := tx.Query(ctx, `SELECT srec_id,session_id,original_session_id,record_type,ts
		FROM session_record_copies WHERE session_id=ANY($1::text[])
		ORDER BY session_id COLLATE "C",srec_id`, members)
	if err != nil {
		return nil, fmt.Errorf("read existing copy markers: %w", err)
	}
	defer rows.Close()
	var markers []sessionfacts.CopyMarker
	for rows.Next() {
		var marker sessionfacts.CopyMarker
		if err := rows.Scan(&marker.SRecID, &marker.SessionID, &marker.OriginalSessionID, &marker.RecordType, &marker.TS); err != nil {
			return nil, fmt.Errorf("scan existing copy marker: %w", err)
		}
		markers = append(markers, marker)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read existing copy markers: %w", err)
	}
	return markers, nil
}
