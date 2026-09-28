package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// unified_events recomputed codex cost on every read: a per-row LATERAL LIKE join
// against codex_model_rates plus a whole extra scan of session_records for a
// day-level model fallback. One 267k-row arm cost as much as the entire 2.45M-row
// view -- 2.6s, inherited by every chart and by the session view's 9 seconds (#245).
//
// The fix follows the precedent already in this repo: claude_imputed_cost is a
// table, not a view arm, and reads 167k rows in 7ms. These tests pin the behaviour
// the materialised codex table has to keep.

const testCodexProfile = "codex-imputed-profile"

func codexUsageRecord(sessionID, model string, ts time.Time, in, out, cacheRead int) *SessionRecord {
	return &SessionRecord{
		Ts:              ts,
		SessionID:       sessionID,
		RecordType:      "usage",
		ProfileEmail:    testCodexProfile,
		UserID:          "uid-codex-imputed",
		Model:           model,
		InputTokens:     ptrInt(in),
		OutputTokens:    ptrInt(out),
		CacheReadTokens: ptrInt(cacheRead),
		Agent:           "codex",
		BillingProvider: "openai",
	}
}

func codexImputedTotals(t *testing.T, s *PgStore) (rows int, cost float64) {
	t.Helper()
	err := s.pool.QueryRow(context.Background(),
		`SELECT count(*), COALESCE(sum(cost_usd),0) FROM codex_imputed_cost`).Scan(&rows, &cost)
	if err != nil {
		t.Fatalf("read codex_imputed_cost: %v", err)
	}
	return rows, cost
}

// The rate table is matched by prefix, longest first. gpt-5.1-codex-max and
// gpt-5.1-codex-mini share a prefix, and mini is the cheaper of the two -- pick the
// wrong one and every max row is priced at a fifth of its real cost, silently.
func TestCodexImputedPicksLongestRatePrefix(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("codex-rate-max", "gpt-5.1-codex-max", now, 1000, 100, 0),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}

	_, got := codexImputedTotals(t, s)
	// gpt-5.1-codex-max: input 1.25, output 10 per 1M
	want := (1000*1.25 + 100*10.0) / 1000000.0
	if got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("cost = %.10f, want %.10f -- a shorter prefix (codex-mini) probably won the match", got, want)
	}
}

// A session that straddles the watermark must be recomputed whole. The dedup rule
// compares a row against the PREVIOUS row of the same session (LAG), so refreshing
// only the new rows would judge the first new row against nothing and keep a
// duplicate that the full path drops.
func TestCodexImputedIncrementalRecomputesWholeSession(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Now().UTC().Add(-time.Hour)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("codex-straddle", "gpt-5.5", base, 1000, 100, 0),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}
	rowsBefore, _ := codexImputedTotals(t, s)

	// Same session gains a later row; the incremental path must see it.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("codex-straddle", "gpt-5.5", base.Add(time.Minute), 2000, 200, 0),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if _, err := s.RefreshCodexImputedCostIncremental(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCostIncremental: %v", err)
	}

	rowsAfter, costAfter := codexImputedTotals(t, s)
	if rowsAfter != rowsBefore+1 {
		t.Fatalf("rows = %d, want %d -- the session was not recomputed as a whole", rowsAfter, rowsBefore+1)
	}
	want := (1000*5.0+100*30.0)/1000000.0 + (2000*5.0+200*30.0)/1000000.0
	if costAfter < want-1e-9 || costAfter > want+1e-9 {
		t.Fatalf("cost = %.10f, want %.10f", costAfter, want)
	}
}

// Whatever the incremental path builds must equal what a full rebuild builds. If
// the two drift, the table's contents depend on the order refreshes happened to
// run in, which is not something anyone can debug from a dashboard.
func TestCodexImputedIncrementalMatchesFullRebuild(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Now().UTC().Add(-2 * time.Hour)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("codex-a", "gpt-5.5", base, 500, 50, 10),
		codexUsageRecord("codex-b", "gpt-5-codex", base.Add(time.Minute), 700, 70, 0),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("codex-c", "gpt-5.4", base.Add(2*time.Minute), 900, 90, 5),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if _, err := s.RefreshCodexImputedCostIncremental(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCostIncremental: %v", err)
	}
	incRows, incCost := codexImputedTotals(t, s)

	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost (rebuild): %v", err)
	}
	fullRows, fullCost := codexImputedTotals(t, s)

	if incRows != fullRows {
		t.Fatalf("rows: incremental %d, full rebuild %d", incRows, fullRows)
	}
	if incCost < fullCost-1e-9 || incCost > fullCost+1e-9 {
		t.Fatalf("cost: incremental %.10f, full rebuild %.10f", incCost, fullCost)
	}
}

// The refresh runs every 10 seconds. That interval is only defensible if a tick
// with nothing new does nothing at all -- no delete, no insert, no write.
func TestCodexImputedIncrementalIsNoopWhenNothingNew(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("codex-idle", "gpt-5.5", time.Now().UTC().Add(-time.Hour), 100, 10, 0),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}
	rowsBefore, costBefore := codexImputedTotals(t, s)

	n, err := s.RefreshCodexImputedCostIncremental(ctx)
	if err != nil {
		t.Fatalf("RefreshCodexImputedCostIncremental: %v", err)
	}
	if n != 0 {
		t.Fatalf("refreshed %d row(s) with nothing new, want 0", n)
	}
	rowsAfter, costAfter := codexImputedTotals(t, s)
	if rowsAfter != rowsBefore || costAfter != costBefore {
		t.Fatalf("idle tick changed the table: %d/%.10f -> %d/%.10f", rowsBefore, costBefore, rowsAfter, costAfter)
	}
}

// unified_events must serve the same numbers from the table that it used to compute
// inline. This is the property the whole change is judged on: the dashboard's cost
// must not move by a cent.
func TestUnifiedEventsCodexArmReadsTheTable(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("codex-view", "gpt-5.5", now, 1000, 100, 200),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}

	var rows int
	var cost float64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*), COALESCE(sum(cost_usd),0) FROM unified_events WHERE event_name='codex_usage'`,
	).Scan(&rows, &cost); err != nil {
		t.Fatalf("read unified_events: %v", err)
	}
	if rows != 1 {
		t.Fatalf("unified_events codex rows = %d, want 1", rows)
	}
	// input excludes cache_read; cache_read priced at its own rate
	want := ((1000-200)*5.0 + 100*30.0 + 200*0.5) / 1000000.0
	if cost < want-1e-9 || cost > want+1e-9 {
		t.Fatalf("cost = %.10f, want %.10f", cost, want)
	}
}

// unified_events UNIONs codex_imputed_cost's token columns with otel_events'. If the
// two ever disagree on width, the view's column type changes -- and CREATE OR REPLACE
// VIEW refuses to change a column's type, so the migration fails on every database
// that already has the old view. A fresh test database never sees this: the view is
// created once, from scratch, already agreeing with itself.
//
// That is exactly how it got through here. The table shipped as BIGINT, every test
// passed, and the failure appeared only on a dev server with an existing schema:
//
//	cannot change data type of view column "input_tokens" from integer to bigint
//
// So this checks the property the fresh-schema tests structurally cannot.
func TestCodexImputedTokenColumnsMatchOtelEvents(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	for _, col := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_create_tokens"} {
		var otelType, codexType string
		if err := s.pool.QueryRow(ctx, `
			SELECT
				(SELECT data_type FROM information_schema.columns
				  WHERE table_name = 'otel_events' AND column_name = $1),
				(SELECT data_type FROM information_schema.columns
				  WHERE table_name = 'codex_imputed_cost' AND column_name = $1)`,
			col).Scan(&otelType, &codexType); err != nil {
			t.Fatalf("read column types for %s: %v", col, err)
		}
		if otelType != codexType {
			t.Errorf("%s: otel_events is %s, codex_imputed_cost is %s -- "+
				"CREATE OR REPLACE VIEW will fail on any database that already has unified_events",
				col, otelType, codexType)
		}
	}
}

// The incremental cursor used to be max(ts) of the materialised table. Two things
// break that, and both were found in live data before a line of it shipped:
//
//   - session_records permits timestamp ties (its uniqueness key includes uuid), so
//     a new row sharing the current maximum timestamp is never > it.
//   - rows do not arrive in timestamp order at all. A client syncing a machine for
//     the first time uploads months of history: high ids, old timestamps. Prod had
//     two such rows sitting behind the maximum at the time this was written.
//
// Either way the row waits for the half-hourly rebuild, which is the opposite of
// what a 10-second refresh is for.
func TestCodexImputedIncrementalCatchesSameTimestampRow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	ts := time.Now().UTC().Add(-time.Hour)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("codex-tie-a", "gpt-5.5", ts, 1000, 100, 0),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}
	rowsBefore, _ := codexImputedTotals(t, s)

	// Same timestamp to the microsecond, different session: a `ts >` cursor cannot
	// see this row, ever.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("codex-tie-b", "gpt-5.5", ts, 2000, 200, 0),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if _, err := s.RefreshCodexImputedCostIncremental(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCostIncremental: %v", err)
	}

	rowsAfter, _ := codexImputedTotals(t, s)
	if rowsAfter != rowsBefore+1 {
		t.Fatalf("rows = %d, want %d -- a row sharing the newest timestamp was skipped", rowsAfter, rowsBefore+1)
	}
}

// Backfill: a row that arrives now but happened long ago. Ingestion order and event
// order are different things, and only the first one is monotonic.
func TestCodexImputedIncrementalCatchesOutOfOrderRow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("codex-recent", "gpt-5.5", now.Add(-time.Minute), 1000, 100, 0),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}
	rowsBefore, _ := codexImputedTotals(t, s)

	// Ingested after everything above, timestamped a week before it.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("codex-history", "gpt-5.5", now.Add(-7*24*time.Hour), 3000, 300, 0),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if _, err := s.RefreshCodexImputedCostIncremental(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCostIncremental: %v", err)
	}

	rowsAfter, _ := codexImputedTotals(t, s)
	if rowsAfter != rowsBefore+1 {
		t.Fatalf("rows = %d, want %d -- a backfilled row (new id, old timestamp) was skipped", rowsAfter, rowsBefore+1)
	}
}

// The cursor records how far the source has been read, not what the output contains.
// Those differ: rows dropped as duplicate token snapshots are never materialised, so
// a cursor derived from the output table would stop just behind one of them and
// rebuild the same session on every tick, forever.
func TestCodexImputedCursorAdvancesPastDroppedRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Now().UTC().Add(-time.Hour)
	first := codexUsageRecord("codex-dropped", "gpt-5.5", base, 1000, 100, 0)
	// A repeated total_token_usage snapshot: the dedup rule drops the second one.
	snapshot := []byte(`{"payload":{"info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":1,"output_tokens":2}}}}`)
	first.Raw = snapshot
	second := codexUsageRecord("codex-dropped", "gpt-5.5", base.Add(time.Second), 1000, 100, 0)
	second.Raw = snapshot

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{first, second}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}

	// Nothing new has arrived since. The tick must be idle even though the newest
	// source row is deliberately absent from the table.
	n, err := s.RefreshCodexImputedCostIncremental(ctx)
	if err != nil {
		t.Fatalf("RefreshCodexImputedCostIncremental: %v", err)
	}
	if n != 0 {
		t.Fatalf("refreshed %d row(s) with nothing new -- the cursor is stuck behind a dropped row", n)
	}
}

// Each source row produces at most one materialised row, so srec_id is unique by
// construction. Saying so in the schema turns the one failure mode nobody would
// notice -- a session counted twice, cost silently doubled -- into an error.
//
// It is worth stating because the refreshes can overlap: the 30-minute rebuild and
// the 10-second incremental are separate goroutines. Read committed lets a rebuild's
// DELETE miss rows another transaction inserted after that statement began, and then
// its INSERT re-adds them. Serialising the two is the actual fix; this is the seat
// belt for the day someone adds a third writer.
func TestCodexImputedRejectsDuplicateSourceRow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("codex-dup-guard", "gpt-5.5", time.Now().UTC().Add(-time.Hour), 100, 10, 0),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}

	var srecID int64
	if err := s.pool.QueryRow(ctx, `SELECT srec_id FROM codex_imputed_cost LIMIT 1`).Scan(&srecID); err != nil {
		t.Fatalf("read srec_id: %v", err)
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO codex_imputed_cost (srec_id, session_id, ts, cost_usd) VALUES ($1, 'x', now(), 1.0)`, srecID)
	if err == nil {
		t.Fatal("a second row for the same source id was accepted -- a doubled session would go unnoticed")
	}
}

// The rebuild reads its ceiling, writes, and moves the cursor to that same ceiling.
// It used to re-read max(id) in a separate statement after the INSERT, and under read
// committed that is a different snapshot: a row committed by ordinary sync traffic in
// between is never inserted, yet the cursor advances past it, so the incremental pass
// skips it too. It reappears only at the next rebuild -- up to half an hour of a
// finished session's cost missing from every chart.
func TestCodexImputedRebuildCursorMatchesWhatItWrote(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("codex-ceiling", "gpt-5.5", time.Now().UTC().Add(-time.Hour), 100, 10, 0),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}

	var cursor, maxMaterialised int64
	if err := s.pool.QueryRow(ctx, `SELECT last_srec_id FROM codex_imputed_cursor`).Scan(&cursor); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(max(srec_id),0) FROM codex_imputed_cost`).Scan(&maxMaterialised); err != nil {
		t.Fatalf("read max srec_id: %v", err)
	}
	if cursor != maxMaterialised {
		t.Fatalf("cursor = %d but the newest row written was %d -- the gap is a source row "+
			"the rebuild skipped and the cursor claims to have handled", cursor, maxMaterialised)
	}
}

// Both refreshes run on their own goroutine and can overlap: a rebuild takes seconds
// over a real table while the incremental fires every ten. This drives them into each
// other on purpose and checks the table afterwards.
func TestCodexImputedConcurrentRefreshesDoNotDuplicate(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 20; i++ {
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{
			codexUsageRecord(fmt.Sprintf("codex-race-%d", i), "gpt-5.5", base.Add(time.Duration(i)*time.Second), 100, 10, 0),
		}); err != nil {
			t.Fatalf("InsertSessionRecords: %v", err)
		}
	}

	for round := 0; round < 12; round++ {
		// A new session arrives while both passes are in flight -- the case where the
		// incremental has nothing to lock and the rebuild has already deleted.
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{
			codexUsageRecord(fmt.Sprintf("codex-race-new-%d", round), "gpt-5.5",
				base.Add(time.Duration(100+round)*time.Second), 200, 20, 0),
		}); err != nil {
			t.Fatalf("InsertSessionRecords: %v", err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = s.RefreshCodexImputedCost(ctx) }()
		go func() { defer wg.Done(); _, _ = s.RefreshCodexImputedCostIncremental(ctx) }()
		wg.Wait()

		var dupes int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM (
			SELECT srec_id FROM codex_imputed_cost GROUP BY srec_id HAVING count(*) > 1) d`).Scan(&dupes); err != nil {
			t.Fatalf("check duplicates: %v", err)
		}
		if dupes > 0 {
			t.Fatalf("round %d: %d source row(s) materialised more than once -- their cost is counted twice", round, dupes)
		}
	}

	// And the end state must equal a clean rebuild: overlapping passes may not leave
	// the table short either.
	var racedRows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM codex_imputed_cost`).Scan(&racedRows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}
	var cleanRows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM codex_imputed_cost`).Scan(&cleanRows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if racedRows != cleanRows {
		t.Fatalf("after concurrent passes the table held %d rows, a clean rebuild gives %d", racedRows, cleanRows)
	}
}

// codexRawRecord builds a usage record carrying the raw line shape the guard reads.
// kind is "token_usage_record" or "token_count".
func codexRawRecord(sessionID, model string, ts time.Time, in, out int, kind string) *SessionRecord {
	r := codexUsageRecord(sessionID, model, ts, in, out, 0)
	if kind == "token_usage_record" {
		r.Raw = []byte(`{"type":"token_usage_record","payload":{"usage":{"input_tokens":1}}}`)
	} else {
		r.Raw = []byte(`{"type":"event_msg","payload":{"type":"token_count","info":{}}}`)
	}
	r.UUID = fmt.Sprintf("%s-%s-%d", sessionID, kind, ts.UnixNano())
	return r
}

// A token_count event that follows a token_usage_record is that record's mirror.
// Counting both doubles the session; on a resumed session it turned eleven minutes
// into 1.49 billion input tokens and $24,572, because thread_token_usage restarts at
// the resume while total_token_usage keeps counting the whole file (#685).
//
// The client stopped emitting these in v0.7.51. The server guards anyway: one
// account was observed writing from v0.7.41 and v0.7.50 at the same time for thirty
// hours (#623), so "every client is current" is not something a deploy can promise.
func TestCodexImputed_DropsEventsMirroringATokenUsageRecord(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexRawRecord("mirrored", "gpt-5.6-sol", base, 1000, 100, "token_usage_record"),
		// The mirror, one second later. Same turn, counted twice before the guard.
		codexRawRecord("mirrored", "gpt-5.6-sol", base.Add(time.Second), 1000, 100, "token_count"),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}

	rows, _ := codexImputedTotals(t, s)
	if rows != 1 {
		t.Fatalf("rows = %d, want 1 -- the event mirrors the record", rows)
	}
	var in int64
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(input_tokens),0) FROM codex_imputed_cost`).Scan(&in); err != nil {
		t.Fatalf("read tokens: %v", err)
	}
	if in != 1000 {
		t.Fatalf("input_tokens = %d, want 1000 (counted once)", in)
	}
}

func TestCodexImputed_KeepsEventsBeforeTheFirstRecord(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	// The client's own guard turns on when it reads the first record line, so events
	// written before that were counted legitimately. Dropping them would lose real
	// usage from every file that opened with token_count events.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexRawRecord("early", "gpt-5.6-sol", base, 500, 50, "token_count"),
		codexRawRecord("early", "gpt-5.6-sol", base.Add(time.Minute), 700, 70, "token_usage_record"),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}

	rows, _ := codexImputedTotals(t, s)
	if rows != 2 {
		t.Fatalf("rows = %d, want 2 -- the early event predates the first record", rows)
	}
}

func TestCodexImputed_GuardIsPerSession(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	// A session that never writes records is the ordinary case -- 5,458 of them on a
	// production copy. Its events are the only source it has.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexRawRecord("with-record", "gpt-5.6-sol", base, 100, 10, "token_usage_record"),
		codexRawRecord("with-record", "gpt-5.6-sol", base.Add(time.Second), 100, 10, "token_count"),
		codexRawRecord("events-only", "gpt-5.6-sol", base, 300, 30, "token_count"),
		codexRawRecord("events-only", "gpt-5.6-sol", base.Add(time.Minute), 400, 40, "token_count"),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}

	var withRecord, eventsOnly int
	if err := s.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE session_id='with-record'),
		count(*) FILTER (WHERE session_id='events-only') FROM codex_imputed_cost`).
		Scan(&withRecord, &eventsOnly); err != nil {
		t.Fatalf("read: %v", err)
	}
	if withRecord != 1 {
		t.Fatalf("with-record rows = %d, want 1", withRecord)
	}
	if eventsOnly != 2 {
		t.Fatalf("events-only rows = %d, want 2 -- nothing mirrors them", eventsOnly)
	}
}
