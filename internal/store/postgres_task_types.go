package store

import (
	"context"
	"fmt"
)

// taskTypeBackfillCandidateSQL selects unclassified human prompts. idx_srec_unclassified
// (migrations.go) is the only index that can serve an empty task_type -- without it
// this falls back to a sequential scan across every chunk, which only gets slower as
// the table grows. See that index's comment for the measurement.
//
// A tool_result row is record_type='user' too but carries no typed text -- see
// insights.IsTypedTurn. Excluding it from candidacy (not just from the update) keeps
// it out of every future call's LIMIT budget; leaving it selectable would have the
// backfill spend its budget re-selecting rows it can never progress.
//
// A plain-string content check alone is not enough: measured on prod, 8,375 of
// 370,858 array-shaped Claude message.content values carry a real text/input_text
// block (the rest are tool_result) -- the array branch below catches those.
// jsonb_array_elements errors on a non-array input, hence the array-type guard.
const taskTypeBackfillCandidateSQL = `SELECT id, raw FROM session_records
	WHERE record_type = 'user' AND task_type = ''
	  AND (
	    COALESCE(NULLIF(agent, ''), 'claude') = 'codex'
	    OR jsonb_typeof(raw->'message'->'content') = 'string'
	    OR (jsonb_typeof(raw->'message'->'content') = 'array' AND EXISTS (
	          SELECT 1 FROM jsonb_array_elements(raw->'message'->'content') e
	          WHERE e->>'type' IN ('text', 'input_text')
	        ))
	  )
	ORDER BY ts ASC
	LIMIT $1
	FOR UPDATE SKIP LOCKED`

// BackfillTaskTypes labels at most limit previously stored human prompts. It is
// deliberately bounded so an administrator can resume it without a startup scan.
func (s *PgStore) BackfillTaskTypes(ctx context.Context, limit int) (int, error) {
	if s.classifier == nil {
		return 0, fmt.Errorf("task classifier not configured")
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, taskTypeBackfillCandidateSQL, limit)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id  int64
		raw []byte
	}
	candidates := make([]candidate, 0, limit)
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, candidate{id: id, raw: raw})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	if len(candidates) == 0 {
		return 0, tx.Commit(ctx)
	}
	ids := make([]int64, len(candidates))
	taskTypes := make([]string, len(candidates))
	for i, candidate := range candidates {
		ids[i] = candidate.id
		taskTypes[i] = s.classifier.Classify(s.classifier.PromptFromRaw(candidate.raw))
	}
	// One round trip for the whole batch instead of one per row: unnest zips the two
	// parallel arrays back into (id, task_type) pairs server-side. Classification
	// itself stays in Go (the injected classifier decides the label), only the write
	// is batched.
	if _, err := tx.Exec(ctx, `UPDATE session_records sr
		SET task_type = v.task_type, classifier_version = $1
		FROM (SELECT unnest($2::bigint[]) AS id, unnest($3::text[]) AS task_type) v
		WHERE sr.id = v.id AND sr.task_type = ''`,
		s.classifier.Version(), ids, taskTypes); err != nil {
		return 0, err
	}
	return len(candidates), tx.Commit(ctx)
}

// reclassifySweepBatch bounds one pass. The sweep walks the primary key, so a
// batch is a bounded index range rather than a scan of everything.
const reclassifySweepBatch = 2000

// ReclassifyStaleTaskTypes relabels rows whose stored verdict came from an older
// rule set, a bounded batch at a time.
//
// Bumping insights.ClassifierVersion is how a rule change says the stored answers
// are stale. BackfillTaskTypes cannot serve that: it selects rows with no verdict
// at all, and widening it to compare versions would cost it the one index that can
// answer an empty task_type. So the sweep walks ids instead, which is an index
// range whatever the predicate.
//
// The cursor remembers which version it was walking for. A later bump resets it
// without anybody having to remember, and a completed sweep costs one index lookup
// per call until then.
func (s *PgStore) ReclassifyStaleTaskTypes(ctx context.Context) (int, error) {
	if s.classifier == nil {
		return 0, fmt.Errorf("task classifier not configured")
	}
	version := s.classifier.Version()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	var cursor int64
	var cursorVersion string
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(max(last_record_id), 0),
		        COALESCE(max(version), '')
		   FROM task_type_reclassify_cursor`).Scan(&cursor, &cursorVersion); err != nil {
		return 0, err
	}
	if cursorVersion != version {
		// A new rule set: start the walk over.
		cursor = 0
	}

	rows, err := tx.Query(ctx, `
		SELECT id, raw, classifier_version FROM session_records
		WHERE id > $1 AND record_type = 'user'
		ORDER BY id LIMIT $2`, cursor, reclassifySweepBatch)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id  int64
		raw []byte
	}
	var stale []candidate
	var head int64
	for rows.Next() {
		var id int64
		var raw []byte
		var stored string
		if err := rows.Scan(&id, &raw, &stored); err != nil {
			rows.Close()
			return 0, err
		}
		head = id
		// '' is "never classified" -- BackfillTaskTypes owns those, and taking them
		// here would race it for the same rows under a different lock.
		if stored != "" && stored != version {
			stale = append(stale, candidate{id: id, raw: raw})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if head == 0 {
		// The walk is finished. Leave the cursor where it is so the next call is
		// one index lookup rather than a rescan.
		return 0, tx.Commit(ctx)
	}

	relabelled := 0
	for _, c := range stale {
		prompt := s.classifier.PromptFromRaw(c.raw)
		if prompt == "" {
			continue
		}
		if _, err := tx.Exec(ctx,
			`UPDATE session_records SET task_type = $1, classifier_version = $2 WHERE id = $3`,
			s.classifier.Classify(prompt), version, c.id); err != nil {
			return 0, err
		}
		relabelled++
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO task_type_reclassify_cursor (only_row, last_record_id, version) VALUES (TRUE, $1, $2)
		 ON CONFLICT (only_row) DO UPDATE SET last_record_id = EXCLUDED.last_record_id, version = EXCLUDED.version`,
		head, version); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return relabelled, nil
}
