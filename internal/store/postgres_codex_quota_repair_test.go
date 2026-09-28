package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// codex_login_email_repair_state is deliberately not in truncateTables: it is
// progress, not data, and a test that leaves it behind would hand the next one a
// repair that believes it is halfway through. The marker lives in
// schema_backfills, which is not truncated either (acquireTestStore's own
// backfill marker must survive), so both are cleared by name here.
func clearCodexLoginEmailRepairMarker(t *testing.T, s *PgStore) {
	t.Helper()
	ctx := context.Background()
	clear := func() {
		if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, codexLoginEmailRepair); err != nil {
			t.Fatalf("clear marker: %v", err)
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM codex_login_email_repair_state WHERE name = $1`, codexLoginEmailRepair); err != nil {
			t.Fatalf("clear repair state: %v", err)
		}
	}
	clear()
	t.Cleanup(clear)
}

func codexRepairMarked(t *testing.T, s *PgStore) bool {
	t.Helper()
	var done bool
	if err := s.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, codexLoginEmailRepair).Scan(&done); err != nil {
		t.Fatalf("read marker: %v", err)
	}
	return done
}

// codexRepairPhase answers "" before the first batch has created the progress
// row, which is a state the caller has to be able to observe rather than crash on:
// the repair is designed to be startable from nothing.
func codexRepairPhase(t *testing.T, s *PgStore) string {
	t.Helper()
	var phase string
	err := s.pool.QueryRow(context.Background(),
		`SELECT phase FROM codex_login_email_repair_state WHERE name = $1`, codexLoginEmailRepair).Scan(&phase)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("read phase: %v", err)
	}
	return phase
}

// The revert phase removes the fiction and nothing else. A claude row attributed
// by the same inference pass is a legitimate guess about an Anthropic account, and
// an observed codex row is a measurement; neither is #524.
//
// Mutation: drop the agent guard from codexRevertMatch and every inferred Claude
// attribution in retained history is blanked with it.
func TestRepairCodexLoginEmail_revertsOnlyTheOtelGuessOnCodexRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearCodexLoginEmailRepairMarker(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s,
		codexRec{uuid: "codex-inferred", session: "sess-a", email: "user-a@example.com", source: "inferred"},
		codexRec{uuid: "codex-otel", session: "sess-b", email: "observed@example.test", source: "otel"},
		codexRec{uuid: "claude-inferred", session: "sess-c", email: "user-a@example.com", source: "inferred",
			agent: "claude", provider: "anthropic"},
	)
	if before := rollupLoginScopeRows(t, s, "user-a@example.com"); before != 2 {
		t.Fatalf("%d login-scope rollup rows for user-a@ before the repair, want 2", before)
	}

	reverted, accounts, attributed, err := s.RepairCodexLoginEmailFromQuotaOnce(ctx)
	if err != nil {
		t.Fatalf("RepairCodexLoginEmailFromQuotaOnce: %v", err)
	}
	if reverted != 1 || accounts != 0 || attributed != 0 {
		t.Fatalf("repair = (%d, %d, %d), want only the one codex guess reverted", reverted, accounts, attributed)
	}
	if email, src := loginEmailSourceOf(t, s, "codex-inferred"); email != "" || src != "" {
		t.Fatalf("codex-inferred = (%q, %q), want it cleared", email, src)
	}
	if email, src := loginEmailSourceOf(t, s, "codex-otel"); email != "observed@example.test" || src != "otel" {
		t.Fatalf("codex-otel = (%q, %q), want the observation untouched", email, src)
	}
	if email, src := loginEmailSourceOf(t, s, "claude-inferred"); email != "user-a@example.com" || src != "inferred" {
		t.Fatalf("claude-inferred = (%q, %q), want the Claude guess untouched", email, src)
	}
	// The roll-up is what the session list reads. Leaving it behind would move the
	// fiction one table along rather than remove it.
	if after := rollupLoginScopeRows(t, s, "user-a@example.com"); after != 1 {
		t.Fatalf("%d login-scope rollup rows for user-a@ after the repair, want 1 (the Claude session)", after)
	}
}

// The end-to-end shape of the repair, one committed batch at a time: phases run in
// order, each batch commits its own slice, and the marker appears only once every
// phase is drained. Resuming is what a batch boundary is for -- the production run
// blanks 1.2M rows and will not survive on one transaction.
//
// The phase order is the assertion that matters most. Reverting after attributing
// would blank what was just repaired, and attributing before the account fill
// would leave the rows that gain an account_id in this run waiting for the next
// periodic pass.
//
// Mutation: start the state row at 'attribute', or advance account -> revert, and
// the final rows come back blank instead of attributed.
func TestRepairCodexLoginEmail_runsPhasesInOrderAndResumes(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearCodexLoginEmailRepairMarker(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s,
		codexRec{uuid: "c1", session: "sess-a", email: "user-a@example.com", source: "inferred"},
		codexRec{uuid: "c2", session: "sess-a", email: "user-a@example.com", source: "inferred"},
	)
	seedCodexSamples(t, s,
		codexSample(0, "acct-a", "wedataopenai@example.com", "sess-a", AttributionObserved))

	var phases []string
	var result codexLoginEmailRepairResult
	var sawDrainedAttributePhase bool
	for i := 0; ; i++ {
		if i > 20 {
			t.Fatal("repair did not finish in 20 batches")
		}
		if phase := codexRepairPhase(t, s); phase != "" {
			phases = append(phases, phase)
		}
		if codexRepairMarked(t, s) {
			t.Fatalf("marker was inserted while batch %d still had work to do", i)
		}
		var err error
		result, err = s.repairCodexLoginEmailFromQuotaBatch(ctx, 1)
		if err != nil {
			t.Fatalf("batch %d: %v", i, err)
		}
		if i == 0 {
			// One row reverted, the other still carrying the guess: the proof that a
			// batch commits its own slice rather than the whole phase.
			if result.RevertUpdated != 1 {
				t.Fatalf("first batch reverted %d rows, want 1", result.RevertUpdated)
			}
			if email, _ := loginEmailSourceOf(t, s, "c2"); email != "user-a@example.com" {
				t.Fatalf("c2 = %q after the first batch, want the guess still there", email)
			}
		}
		if result.AttributeUpdated == 2 && !result.Complete {
			// Every source row now carries the repaired address, but the derived rows
			// still describe history as it was: batches touch session_records only, and
			// the rebuild happens once, in the batch that records the marker.
			sawDrainedAttributePhase = true
			if email, _ := loginEmailSourceOf(t, s, "c2"); email != "wedataopenai@example.com" {
				t.Fatalf("c2 = %q with the attribute phase drained, want the repaired address", email)
			}
			if n := rollupLoginScopeRows(t, s, "wedataopenai@example.com"); n != 0 {
				t.Fatalf("%d repaired login-scope rollup rows mid-repair, want 0 until completion", n)
			}
			if n := rollupLoginScopeRows(t, s, "user-a@example.com"); n != 1 {
				t.Fatalf("%d pre-repair login-scope rollup rows mid-repair, want the old value to stand", n)
			}
		}
		if result.Complete {
			break
		}
	}

	if !sawDrainedAttributePhase {
		t.Fatal("the attribute phase never drained before completion; the mid-repair assertions did not run")
	}
	if !codexRepairMarked(t, s) {
		t.Fatal("repair completed without recording its marker")
	}
	// Mutation: drop the final rebuildAllLoginEmailDerivedRows and the rollups keep
	// serving the address #524 invented, for every session the repair touched.
	if n := rollupLoginScopeRows(t, s, "wedataopenai@example.com"); n != 1 {
		t.Fatalf("%d repaired login-scope rollup rows after completion, want 1", n)
	}
	if n := rollupLoginScopeRows(t, s, "user-a@example.com"); n != 0 {
		t.Fatalf("%d pre-repair login-scope rollup rows after completion, want 0", n)
	}
	if result.RevertUpdated != 2 || result.AccountUpdated != 2 || result.AttributeUpdated != 2 {
		t.Fatalf("repair totals = %+v, want 2 rows through each phase", result)
	}
	seen := map[string]int{}
	order := []string{}
	for _, p := range phases {
		if seen[p] == 0 {
			order = append(order, p)
		}
		seen[p]++
	}
	want := []string{"revert", "account", "attribute"}
	if len(order) != len(want) {
		t.Fatalf("phases visited %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("phases visited %v, want %v", order, want)
		}
	}
	for _, uuid := range []string{"c1", "c2"} {
		if email, src := loginEmailSourceOf(t, s, uuid); email != "wedataopenai@example.com" || src != "quota" {
			t.Fatalf("%s = (%q, %q), want the quota mapping applied in the same run", uuid, email, src)
		}
		if acct, src := codexAccountOf(t, s, uuid); acct != "acct-a" || src != "quota" {
			t.Fatalf("%s account = (%q, %q), want it filled from the session join", uuid, acct, src)
		}
	}
}

// Deferring the derived rebuild to the last batch is only safe because that batch
// commits the rebuild, the marker and the progress cleanup together. Interrupting
// the repair after its last UPDATE but before that batch leaves no marker, so the
// resumed run -- which has no source rows left to change -- still owes the rebuild
// and must perform it.
//
// Mutation: move the rebuild anywhere but the completion branch, or commit the
// marker separately from it, and the resumed run marks the repair done while the
// dashboard keeps serving pre-repair rollups forever.
func TestRepairCodexLoginEmail_rebuildsDerivedRowsAfterAnInterruptedRun(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearCodexLoginEmailRepairMarker(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s,
		codexRec{uuid: "c1", session: "sess-a", email: "user-a@example.com", source: "inferred"},
		codexRec{uuid: "c2", session: "sess-a", email: "user-a@example.com", source: "inferred"},
	)
	seedCodexSamples(t, s,
		codexSample(0, "acct-a", "wedataopenai@example.com", "sess-a", AttributionObserved))

	// Stop at the commit boundary after the last source row is repaired: every phase
	// is drained, nothing is marked, and the derived rows are still the old ones.
	for i := 0; ; i++ {
		if i > 20 {
			t.Fatal("attribute phase did not drain in 20 batches")
		}
		result, err := s.repairCodexLoginEmailFromQuotaBatch(ctx, 1)
		if err != nil {
			t.Fatalf("batch %d: %v", i, err)
		}
		if result.Complete {
			t.Fatal("repair completed before the interruption point could be observed")
		}
		if result.AttributeUpdated == 2 {
			break
		}
	}
	if codexRepairMarked(t, s) {
		t.Fatal("marker exists at the interruption point, so the resumed run would skip the rebuild")
	}
	if n := rollupLoginScopeRows(t, s, "wedataopenai@example.com"); n != 0 {
		t.Fatalf("%d repaired login-scope rollup rows at the interruption point, want 0", n)
	}

	if _, _, _, err := s.RepairCodexLoginEmailFromQuotaOnce(ctx); err != nil {
		t.Fatalf("resume repair: %v", err)
	}
	if !codexRepairMarked(t, s) {
		t.Fatal("resumed repair did not record its marker")
	}
	if n := rollupLoginScopeRows(t, s, "wedataopenai@example.com"); n != 1 {
		t.Fatalf("%d repaired login-scope rollup rows after the resumed run, want 1", n)
	}
	if n := rollupLoginScopeRows(t, s, "user-a@example.com"); n != 0 {
		t.Fatalf("%d pre-repair login-scope rollup rows after the resumed run, want 0", n)
	}
}

// The marker is what makes this a one-time repair. A second call must not scan
// history again, and must not revert what the periodic passes have written since.
//
// Mutation: skip the marker insert and every boot re-runs the whole repair; skip
// the state-row delete and the next run resumes from a completed cursor.
func TestRepairCodexLoginEmail_marksCompletionAndStopsThere(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearCodexLoginEmailRepairMarker(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{uuid: "c1", session: "sess-a", email: "user-a@example.com", source: "inferred"})
	seedCodexSamples(t, s, codexSample(0, "acct-a", "wedataopenai@example.com", "sess-a", AttributionObserved))

	if _, _, _, err := s.RepairCodexLoginEmailFromQuotaOnce(ctx); err != nil {
		t.Fatalf("first repair: %v", err)
	}
	var states int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM codex_login_email_repair_state WHERE name = $1`, codexLoginEmailRepair).Scan(&states); err != nil {
		t.Fatalf("count repair state: %v", err)
	}
	if states != 0 {
		t.Fatalf("%d progress rows survive completion, want 0", states)
	}

	// A row the periodic pass would legitimately attribute after the repair. The
	// second call must be a no-op, not a second revert.
	seedCodexRecords(t, s, codexRec{uuid: "c2", session: "sess-b", account: "acct-a",
		email: "wedataopenai@example.com", source: "quota", at: codexQuotaBase.Add(time.Hour)})

	reverted, accounts, attributed, err := s.RepairCodexLoginEmailFromQuotaOnce(ctx)
	if err != nil {
		t.Fatalf("second repair: %v", err)
	}
	if reverted != 0 || accounts != 0 || attributed != 0 {
		t.Fatalf("second repair = (%d, %d, %d), want a no-op", reverted, accounts, attributed)
	}
	if email, src := loginEmailSourceOf(t, s, "c2"); email != "wedataopenai@example.com" || src != "quota" {
		t.Fatalf("c2 = (%q, %q), want the later attribution untouched", email, src)
	}
}

// The high-water mark is taken when the repair starts. Records that arrive while it
// runs belong to the periodic passes, and extending a phase that has already moved
// on would mean the repair never terminates on a busy server.
//
// Mutation: drop the sr.id <= $3 bound and this row is dragged into a phase the
// repair has already left behind.
func TestRepairCodexLoginEmail_ignoresRecordsArrivingMidRun(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearCodexLoginEmailRepairMarker(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{uuid: "c1", session: "sess-a", email: "user-a@example.com", source: "inferred"})

	// One batch: enough to freeze the high-water mark and revert the only row.
	if _, err := s.repairCodexLoginEmailFromQuotaBatch(ctx, 1); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	seedCodexRecords(t, s, codexRec{uuid: "late", session: "sess-b", email: "user-a@example.com",
		source: "inferred", at: codexQuotaBase.Add(time.Hour)})

	if _, _, _, err := s.RepairCodexLoginEmailFromQuotaOnce(ctx); err != nil {
		t.Fatalf("drain repair: %v", err)
	}
	if email, src := loginEmailSourceOf(t, s, "late"); email != "user-a@example.com" || src != "inferred" {
		t.Fatalf("late = (%q, %q), want it left for the periodic passes", email, src)
	}
}

// A record whose session is queued for deletion still gets its fiction removed:
// the revert phase has no deleted_sessions anti-join, on purpose. The account and
// attribute phases do have one, because writing a new attribution onto a row on
// its way out is work with no reader.
//
// Mutation: add the anti-join to codexRevertMatch and #524's wrong address
// survives in exactly the rows nobody will look at again.
func TestRepairCodexLoginEmail_revertsInsideDeletedSessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearCodexLoginEmailRepairMarker(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{uuid: "c1", session: "sess-a", account: "acct-a",
		email: "user-a@example.com", source: "inferred"})
	seedCodexSamples(t, s, codexSample(0, "acct-a", "wedataopenai@example.com", "", AttributionObserved))
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO deleted_sessions (session_id) VALUES ($1) ON CONFLICT DO NOTHING`, "sess-a"); err != nil {
		t.Fatalf("queue deletion: %v", err)
	}

	reverted, _, attributed, err := s.RepairCodexLoginEmailFromQuotaOnce(ctx)
	if err != nil {
		t.Fatalf("RepairCodexLoginEmailFromQuotaOnce: %v", err)
	}
	if reverted != 1 {
		t.Fatalf("reverted %d rows, want 1", reverted)
	}
	if attributed != 0 {
		t.Fatalf("attributed %d rows in a deleted session, want 0", attributed)
	}
	if email, src := loginEmailSourceOf(t, s, "c1"); email != "" || src != "" {
		t.Fatalf("c1 = (%q, %q), want it cleared", email, src)
	}
}

// The repair reuses the batch runner the login_email history repair introduced,
// which passes the repair name as $1. A statement that does not reference it fails
// to plan at all, so this keeps the batch statements honest about their own
// parameter list rather than discovering it on a production boot.
//
// The account batch binds $1 through codex_login_email_repair_candidates.repair_name
// rather than through codexRepairStateGuard, so it is covered here for the same
// reason and by the same probe.
//
// Mutation: remove codexRepairStateGuard from the revert or attribute statement,
// or the repair_name predicate from the account one, and this fails with
// "could not determine data type of parameter $1".
func TestCodexRepairBatchStatementsBindTheirRepairName(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearCodexLoginEmailRepairMarker(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{uuid: "c1", session: "sess-a"})
	if _, err := s.pool.Exec(ctx, `INSERT INTO codex_login_email_repair_state (name, phase, max_record_id)
		VALUES ($1, 'revert', 0) ON CONFLICT (name) DO NOTHING`, codexLoginEmailRepair); err != nil {
		t.Fatalf("seed repair state: %v", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only probe
	for name, apply := range map[string]func(context.Context, pgx.Tx, int64, int64, int) (int64, int64, []string, error){
		"revert":    applyCodexRevertBatch,
		"account":   applyCodexAccountFillBatch,
		"attribute": applyCodexQuotaAttributeBatch,
	} {
		if _, _, _, err := apply(ctx, tx, 0, 0, 1); err != nil {
			t.Fatalf("%s batch statement: %v", name, err)
		}
	}
}

// codexImputedLoginEmails reports the addresses the imputed-cost table currently
// carries for one session. That copy, not session_records, is what unified_events
// reads for a codex row, so it is where a stale attribution actually survives.
func codexImputedLoginEmails(t *testing.T, s *PgStore, sessionID string) []string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(),
		`SELECT login_email FROM codex_imputed_cost WHERE session_id = $1 ORDER BY srec_id`, sessionID)
	if err != nil {
		t.Fatalf("query codex_imputed_cost: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			t.Fatalf("scan codex_imputed_cost: %v", err)
		}
		out = append(out, email)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read codex_imputed_cost: %v", err)
	}
	return out
}

// The completion batch rebuilds codex_imputed_cost before it rebuilds the derived
// rows, and the order is the whole point. session_overview_rollups' login scope
// comes from unified_events, one of whose arms is codex_imputed_cost, and that
// table holds its own copy of login_email. Rebuilding the rollups first reads the
// pre-repair copy and writes the address #524 invented straight back into the
// scope the dashboard filters on -- with every source record already correct.
//
// This is measured, not hypothetical: on dev the repair finished with 0 codex
// session_records carrying user-a@ and 48 sessions still scoped to it.
//
// Mutation: drop the refreshCodexImputedCostTx call from the completion branch and
// the user-a@ login scope survives the repair.
func TestRepairCodexLoginEmail_rebuildsImputedCostBeforeTheDerivedRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearCodexLoginEmailRepairMarker(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{uuid: "c1", session: "sess-a", recordType: "usage",
		email: "user-a@example.com", source: "inferred"})
	seedCodexSamples(t, s,
		codexSample(0, "acct-a", "wedataopenai@example.com", "sess-a", AttributionObserved))

	// The stale copy the repair has to correct: what a periodic refresh materialised
	// while the source record still carried #524's guess.
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}
	if got := codexImputedLoginEmails(t, s, "sess-a"); len(got) != 1 || got[0] != "user-a@example.com" {
		t.Fatalf("codex_imputed_cost login emails = %v, want the one pre-repair guess", got)
	}
	if n := rollupLoginScopeRows(t, s, "user-a@example.com"); n != 1 {
		t.Fatalf("%d login-scope rollup rows for user-a@ before the repair, want 1", n)
	}

	if _, _, _, err := s.RepairCodexLoginEmailFromQuotaOnce(ctx); err != nil {
		t.Fatalf("RepairCodexLoginEmailFromQuotaOnce: %v", err)
	}

	if email, src := loginEmailSourceOf(t, s, "c1"); email != "wedataopenai@example.com" || src != "quota" {
		t.Fatalf("c1 = (%q, %q), want the quota mapping applied", email, src)
	}
	if got := codexImputedLoginEmails(t, s, "sess-a"); len(got) != 1 || got[0] != "wedataopenai@example.com" {
		t.Fatalf("codex_imputed_cost login emails = %v, want the repaired address", got)
	}
	if n := rollupLoginScopeRows(t, s, "user-a@example.com"); n != 0 {
		t.Fatalf("%d login-scope rollup rows for user-a@ after the repair, want 0", n)
	}
	if n := rollupLoginScopeRows(t, s, "wedataopenai@example.com"); n != 1 {
		t.Fatalf("%d login-scope rollup rows for the repaired address, want 1", n)
	}
}

// codexRepairCandidates counts the account phase's staged candidate rows. It is
// the observable that separates "the phase derives its candidates once" from "it
// derives them every batch": the table is empty before the phase, holds the whole
// set while it runs, and is empty again after it drains.
func codexRepairCandidates(t *testing.T, s *PgStore) int64 {
	t.Helper()
	var n int64
	if err := s.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM codex_login_email_repair_candidates WHERE repair_name = $1`,
		codexLoginEmailRepair).Scan(&n); err != nil {
		t.Fatalf("count repair candidates: %v", err)
	}
	return n
}

// codexRepairAccountProgress reports the account phase's staging flag and cursor.
func codexRepairAccountProgress(t *testing.T, s *PgStore) (bool, int64) {
	t.Helper()
	var staged bool
	var cursor int64
	if err := s.pool.QueryRow(context.Background(),
		`SELECT account_staged, account_cursor FROM codex_login_email_repair_state WHERE name = $1`,
		codexLoginEmailRepair).Scan(&staged, &cursor); err != nil {
		t.Fatalf("read account progress: %v", err)
	}
	return staged, cursor
}

// The account phase stages its candidates once and consumes them across committed
// batches, so an interruption in the middle of the phase must resume against the
// set that is already there rather than derive it again.
//
// Re-deriving would be invisible in the row values -- the fill is idempotent and
// the second derivation would name the same ids -- so the test removes one staged
// candidate at the interruption point and checks that the resumed run leaves that
// record alone. A phase that re-staged would put the row back and fill it; a phase
// that re-staged without ON CONFLICT would fail on the primary key instead. The
// cursor surviving the interruption is pinned alongside it: each staged id is
// counted exactly once across the two runs.
//
// Mutation: stage on every batch instead of on !account_staged and the removed
// candidate comes back, which is the cheap version of the defect #527 is about --
// the 840ms derivation moving back inside the per-batch loop.
func TestRepairCodexLoginEmail_resumesTheAccountPhaseAgainstTheStagedCandidates(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearCodexLoginEmailRepairMarker(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s,
		codexRec{uuid: "c1", session: "sess-a", email: "user-a@example.com", source: "inferred"},
		codexRec{uuid: "c2", session: "sess-a", email: "user-a@example.com", source: "inferred"},
		codexRec{uuid: "c3", session: "sess-a", email: "user-a@example.com", source: "inferred"},
	)
	seedCodexSamples(t, s,
		codexSample(0, "acct-a", "wedataopenai@example.com", "sess-a", AttributionObserved))

	if n := codexRepairCandidates(t, s); n != 0 {
		t.Fatalf("%d staged candidates before the repair started, want 0", n)
	}

	// Stop one batch into the account phase: the set is staged whole, one id of it
	// is consumed, and the rest is still waiting.
	var filled int64
	for i := 0; ; i++ {
		if i > 20 {
			t.Fatal("the account phase did not produce a batch in 20 tries")
		}
		result, err := s.repairCodexLoginEmailFromQuotaBatch(ctx, 1)
		if err != nil {
			t.Fatalf("batch %d: %v", i, err)
		}
		if result.Complete {
			t.Fatal("repair completed before the account phase could be interrupted")
		}
		if result.AccountUpdated > 0 {
			filled = result.AccountUpdated
			break
		}
	}
	if filled != 1 {
		t.Fatalf("the first account batch filled %d rows, want 1", filled)
	}
	if n := codexRepairCandidates(t, s); n != 3 {
		t.Fatalf("%d staged candidates one batch into the account phase, want all 3", n)
	}
	staged, cursor := codexRepairAccountProgress(t, s)
	if !staged {
		t.Fatal("account_staged is false after the staging pass committed")
	}
	if cursor == 0 {
		t.Fatal("account_cursor did not advance over the first staged candidate")
	}

	// The marker for "this phase reads its staged set and nothing else". c3 still
	// satisfies the live predicate, so a phase that re-derives its candidates will
	// fill it anyway and this row is what tells the two apart.
	if _, err := s.pool.Exec(ctx, `DELETE FROM codex_login_email_repair_candidates
		WHERE repair_name = $1 AND id = (SELECT id FROM session_records WHERE uuid = $2)`,
		codexLoginEmailRepair, "c3"); err != nil {
		t.Fatalf("drop one staged candidate: %v", err)
	}

	// The interruption: a fresh drain that reads its progress back out of the
	// database, exactly as a restarted server does.
	_, accounts, _, err := s.RepairCodexLoginEmailFromQuotaOnce(ctx)
	if err != nil {
		t.Fatalf("resume repair: %v", err)
	}
	if accounts != 2 {
		t.Fatalf("account phase filled %d rows across the interruption, want the 2 staged ids, each once", accounts)
	}
	for _, uuid := range []string{"c1", "c2"} {
		if acct, src := codexAccountOf(t, s, uuid); acct != "acct-a" || src != "quota" {
			t.Fatalf("%s account = (%q, %q), want it filled from the staged candidate", uuid, acct, src)
		}
	}
	if acct, src := codexAccountOf(t, s, "c3"); acct != "" || src != "" {
		t.Fatalf("c3 account = (%q, %q), want it untouched: the phase re-derived a set it had already staged", acct, src)
	}
}

// The staged set is scratch space for one phase, not a record of the repair. It is
// deleted when the phase drains rather than when the repair completes, so it is
// already gone while the attribute phase -- the longer one -- is still running.
//
// Deleting it at completion instead would work too, because the progress row's
// foreign key cascades; asserting the earlier point is what makes the phase-end
// DELETE load-bearing rather than decorative.
//
// Mutation: drop the DELETE from the account -> attribute transition and the rows
// survive into the attribute phase, where nothing reads them and their only effect
// is to hold storage for the length of the repair.
func TestRepairCodexLoginEmail_dropsStagedCandidatesWhenTheAccountPhaseDrains(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearCodexLoginEmailRepairMarker(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s,
		codexRec{uuid: "c1", session: "sess-a", email: "user-a@example.com", source: "inferred"},
		codexRec{uuid: "c2", session: "sess-a", email: "user-a@example.com", source: "inferred"},
	)
	seedCodexSamples(t, s,
		codexSample(0, "acct-a", "wedataopenai@example.com", "sess-a", AttributionObserved))

	var sawStagedSet, sawEmptiedSet bool
	for i := 0; ; i++ {
		if i > 20 {
			t.Fatal("repair did not finish in 20 batches")
		}
		phase := codexRepairPhase(t, s)
		result, err := s.repairCodexLoginEmailFromQuotaBatch(ctx, 1)
		if err != nil {
			t.Fatalf("batch %d: %v", i, err)
		}
		if phase == "account" && codexRepairCandidates(t, s) == 2 {
			sawStagedSet = true
		}
		if codexRepairPhase(t, s) == "attribute" {
			if n := codexRepairCandidates(t, s); n != 0 {
				t.Fatalf("%d staged candidates survive into the attribute phase, want 0", n)
			}
			sawEmptiedSet = true
		}
		if result.Complete {
			break
		}
	}
	if !sawStagedSet {
		t.Fatal("the account phase never held its staged set; the assertions did not run")
	}
	if !sawEmptiedSet {
		t.Fatal("the attribute phase was never observed; the emptied-set assertion did not run")
	}
	if n := codexRepairCandidates(t, s); n != 0 {
		t.Fatalf("%d staged candidates survive completion, want 0", n)
	}
}

// Materialising the candidate set moves the moment codexAccountFillMatch is
// evaluated from "this batch" to "the start of the phase", and a session queued
// for deletion in between would otherwise be attributed anyway -- writing an
// account onto rows on their way out, which is the work the anti-join exists to
// refuse. The UPDATE re-checks it for that reason.
//
// The revert phase is deliberately not held to this: it has no anti-join at all,
// so a deletion queued mid-run does not save the fiction in those rows.
//
// Mutation: drop the deleted_sessions anti-join from codexAccountFillBatchSQL's
// UPDATE and the staged candidate is filled despite the queued deletion.
func TestRepairCodexLoginEmail_skipsCandidatesDeletedAfterStaging(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearCodexLoginEmailRepairMarker(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s,
		codexRec{uuid: "c1", session: "sess-a", email: "user-a@example.com", source: "inferred"},
		codexRec{uuid: "c2", session: "sess-a", email: "user-a@example.com", source: "inferred"},
	)
	seedCodexSamples(t, s,
		codexSample(0, "acct-a", "wedataopenai@example.com", "sess-a", AttributionObserved))

	// Run until the account phase has staged both rows and consumed one of them.
	for i := 0; ; i++ {
		if i > 20 {
			t.Fatal("the account phase did not produce a batch in 20 tries")
		}
		result, err := s.repairCodexLoginEmailFromQuotaBatch(ctx, 1)
		if err != nil {
			t.Fatalf("batch %d: %v", i, err)
		}
		if result.Complete {
			t.Fatal("repair completed before the deletion could be queued mid-phase")
		}
		if result.AccountUpdated > 0 {
			break
		}
	}
	if n := codexRepairCandidates(t, s); n != 2 {
		t.Fatalf("%d staged candidates mid-phase, want both", n)
	}

	if _, err := s.pool.Exec(ctx,
		`INSERT INTO deleted_sessions (session_id) VALUES ($1) ON CONFLICT DO NOTHING`, "sess-a"); err != nil {
		t.Fatalf("queue deletion: %v", err)
	}

	if _, _, _, err := s.RepairCodexLoginEmailFromQuotaOnce(ctx); err != nil {
		t.Fatalf("drain repair: %v", err)
	}
	if acct, src := codexAccountOf(t, s, "c2"); acct != "" || src != "" {
		t.Fatalf("c2 account = (%q, %q), want the queued deletion to be seen at the batch, not only at staging", acct, src)
	}
}
