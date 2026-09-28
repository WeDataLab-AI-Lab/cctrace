package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// codexDeveloperRecordTypeRepair names the one-time relabel of Codex developer
// messages that clients before the codexlog fix stored as record_type='user'.
const codexDeveloperRecordTypeRepair = "codex_developer_record_type_v1"

// codexDeveloperFactsRepair names the one-time recompute of facts for the sessions
// that relabel touched.
const codexDeveloperFactsRepair = "codex_developer_facts_v1"

// codexRecordType relabels a Codex developer message an old client sent as 'user'.
// The unique key includes record_type, so without this a re-sync from such a client
// would put a 'user' copy back beside the row the repair migration relabeled.
func codexRecordType(agent, recordType string, raw []byte) string {
	if agent != "codex" || recordType != "user" {
		return recordType
	}
	var root struct {
		Role    string `json:"role"`
		Payload struct {
			Role string `json:"role"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &root) == nil && (root.Payload.Role == "developer" || root.Role == "developer") {
		return "developer"
	}
	return recordType
}

// codexDeveloperFactsBatchSize bounds one transaction. A batch holds its own
// sessions' fact locks until commit, so ingest for those sessions waits at most one
// batch, and ingest for every other session does not wait at all.
const codexDeveloperFactsBatchSize = 100

// BackfillCodexDeveloperFacts recomputes plugin and task segment facts for the Codex
// sessions holding developer rows, in session_id batches. Each batch advances the
// cursor in its own transaction, so a restart resumes after the last committed one.
func (s *PgStore) BackfillCodexDeveloperFacts(ctx context.Context) error {
	for {
		finished, err := s.codexDeveloperFactsStep(ctx)
		if err != nil || finished {
			return err
		}
	}
}

func (s *PgStore) codexDeveloperFactsStep(ctx context.Context) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`,
		codexDeveloperFactsRepair).Scan(&done); err != nil {
		return false, err
	}
	if done {
		return true, tx.Commit(ctx)
	}
	n, err := refreshCodexDeveloperFactsBatch(ctx, tx, codexDeveloperFactsBatchSize)
	if err != nil {
		return false, err
	}
	if n == 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`,
			codexDeveloperFactsRepair); err != nil {
			return false, err
		}
	}
	return n == 0, tx.Commit(ctx)
}

// refreshCodexDeveloperFactsBatch recomputes the next limit sessions after the cursor
// and advances it. It takes only the shared maintenance locks and per-session locks
// the ingest path takes, in the same order, never the exclusive rebuild locks.
func refreshCodexDeveloperFactsBatch(ctx context.Context, tx pgx.Tx, limit int) (int, error) {
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return 0, err
	}
	var after string
	if err := tx.QueryRow(ctx, `SELECT last_session_id FROM codex_developer_facts_repair_cursor
		FOR UPDATE`).Scan(&after); err != nil {
		return 0, fmt.Errorf("read codex developer facts cursor: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT session_id FROM session_records
		WHERE agent = 'codex' AND record_type = 'developer' AND session_id > $1
		ORDER BY session_id LIMIT $2`, after, limit)
	if err != nil {
		return 0, fmt.Errorf("list codex developer sessions: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("list codex developer sessions: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if err := refreshPluginInvocationFacts(ctx, tx, ids); err != nil {
		return 0, err
	}
	if err := refreshTaskSegmentFacts(ctx, tx, ids); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE codex_developer_facts_repair_cursor SET last_session_id = $1`,
		ids[len(ids)-1]); err != nil {
		return 0, fmt.Errorf("advance codex developer facts cursor: %w", err)
	}
	return len(ids), nil
}
