package store

import (
	"strings"
	"testing"
	"time"
)

// Nullable optional predicates force PostgreSQL's generic plan to accommodate
// both an unbounded scan and a selective bounded scan. These assertions pin the
// intended alternative: two stable statement shapes, with the bounded parameter
// used only for active-key discovery.
func TestLoginEmailBackfillSQLShapesAreGenericPlanSafe(t *testing.T) {
	since := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		build         func(time.Time) (string, []any)
		activeKeyCTE  string
		completeJoin  string
		timelineAlias string
	}{
		{
			name: "session backfill", build: backfillEdgesFor,
			activeKeyCTE: "WITH active_sessions AS", completeJoin: "JOIN active_sessions a ON a.session_id = o.session_id",
			timelineAlias: "o.ts >= $1",
		},
		{
			name: "user inference", build: inferEdgesFor,
			activeKeyCTE: "WITH active_users AS", completeJoin: "JOIN active_users a ON a.user_id = o.user_id",
			timelineAlias: "o.ts >= $1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			full, fullArgs := tt.build(time.Time{})
			bounded, boundedArgs := tt.build(since)

			if full == bounded {
				t.Fatal("full and bounded passes produced the same SQL shape")
			}
			if len(fullArgs) != 0 || strings.Contains(full, "$1") {
				t.Fatalf("full-history shape has parameter state: args=%v query=%q", fullArgs, full)
			}
			if strings.Contains(strings.ToUpper(full), "IS NULL OR") {
				t.Fatalf("full-history shape contains a NULL-OR predicate: %q", full)
			}
			if len(boundedArgs) != 1 || boundedArgs[0] != since {
				t.Fatalf("bounded args = %v, want [%v]", boundedArgs, since)
			}
			if strings.Count(bounded, "$1") != 1 {
				t.Fatalf("bounded shape uses $1 %d times, want once: %q", strings.Count(bounded, "$1"), bounded)
			}
			if strings.Contains(strings.ToUpper(bounded), "IS NULL OR") {
				t.Fatalf("bounded shape contains a NULL-OR predicate: %q", bounded)
			}
			if !strings.Contains(bounded, tt.activeKeyCTE) || !strings.Contains(bounded, tt.completeJoin) {
				t.Fatalf("bounded shape does not discover active keys then join their timelines: %q", bounded)
			}
			if strings.Contains(bounded, tt.timelineAlias) {
				t.Fatalf("bounded shape applies since to retained timeline rows: %q", bounded)
			}
		})
	}
}

// The inference predicate exists twice: once as inferMatch, once copied out
// literally inside the one-time history repair. #524 was a missing agent guard,
// and a fix applied to one copy leaves the other reproducing the defect on any
// database that has not run the repair yet. Nothing at runtime compares the two,
// so this assertion is the only thing that keeps them agreeing.
//
// The exact COALESCE form is pinned, not just the word 'codex': session_records
// still holds rows with the literal empty agent, and a bare `agent <> 'codex'`
// would answer the same for those while `agent = 'claude'` would silently drop
// them. Every other reader folds the empty string in this way.
//
// Mutation: guard one copy and not the other, or write the guard without the
// COALESCE, and this fails.
func TestInferenceExcludesCodexInEveryCopyOfThePredicate(t *testing.T) {
	const guard = `COALESCE(NULLIF(sr.agent, ''), 'claude') <> 'codex'`
	copies := map[string]string{
		"inferMatch":                     inferMatch,
		"history repair inference batch": loginEmailHistoryInferenceSQL,
	}
	for name, sql := range copies {
		if !strings.Contains(sql, guard) {
			t.Errorf("%s does not exclude codex records: want %q in %q", name, guard, sql)
		}
	}
}

// The Codex quota passes have the same two-shape rule as the OTEL passes, for the
// same reason: one statement carrying an optional nullable bound gets one generic
// plan that has to serve both an unbounded scan of the hypertable and a highly
// selective recent slice.
//
// They differ in one way, and it is deliberate. The OTEL passes must discover
// active keys and then read those keys' complete timelines, because truncating a
// timeline changes what it says. The quota mapping is a per-account constant, so
// `since` bounds the records directly and there is no CTE to keep whole.
//
// Mutation: fold the bound into one shape with `($1 IS NULL OR sr.ts >= $1)`, or
// let it reach a second statement, and this fails.
func TestCodexQuotaSQLShapesAreGenericPlanSafe(t *testing.T) {
	since := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	builders := map[string]func(time.Time) (string, []any){
		"account fill preview":      codexAccountFillPreviewSQL,
		"account fill apply":        codexAccountFillApplySQL,
		"quota attribution preview": codexQuotaAttributionPreviewSQL,
		"quota attribution apply":   codexQuotaAttributionApplySQL,
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			full, fullArgs := build(time.Time{})
			bounded, boundedArgs := build(since)

			if full == bounded {
				t.Fatal("full and bounded passes produced the same SQL shape")
			}
			if len(fullArgs) != 0 || strings.Contains(full, "$1") {
				t.Fatalf("full-history shape has parameter state: args=%v query=%q", fullArgs, full)
			}
			if len(boundedArgs) != 1 || boundedArgs[0] != since {
				t.Fatalf("bounded args = %v, want [%v]", boundedArgs, since)
			}
			if strings.Count(bounded, "$1") != 1 {
				t.Fatalf("bounded shape uses $1 %d times, want once: %q", strings.Count(bounded, "$1"), bounded)
			}
			for _, q := range []string{full, bounded} {
				if strings.Contains(strings.ToUpper(q), "IS NULL OR") {
					t.Fatalf("shape contains a NULL-OR predicate: %q", q)
				}
			}
		})
	}

	revert := codexInferredRevertPreviewSQL()
	if strings.Contains(revert, "$1") {
		t.Fatalf("the revert preview is unscoped by design and must take no parameter: %q", revert)
	}
}

// A preview an operator approves is worthless unless the statement that follows
// runs the same predicate. Nothing at runtime compares them; concatenating one
// constant into both is the mechanism, and this is what pins it.
//
// The one-time repair is held to the same rule. loginEmailHistoryInferenceSQL is
// the cautionary case -- a repair carrying its own literal copy of a predicate is
// how a fixed rule gets reproduced unfixed on the next fresh database.
//
// Mutation: inline any of these predicates into one of its two call sites and
// edit it there.
func TestPreviewAndApplyShareCodexPredicates(t *testing.T) {
	since := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	fillPreview, _ := codexAccountFillPreviewSQL(since)
	fillApply, _ := codexAccountFillApplySQL(since)
	attrPreview, _ := codexQuotaAttributionPreviewSQL(since)
	attrApply, _ := codexQuotaAttributionApplySQL(since)

	cases := []struct {
		name      string
		predicate string
		queries   map[string]string
	}{
		{
			// The repair's copy lives in its staging statement rather than its batch
			// statement (#527): the batch reads ids the staging pass already decided
			// on, so staging is where membership is settled and where the shared rule
			// has to appear.
			name: "account fill", predicate: codexAccountFillMatch,
			queries: map[string]string{
				"preview":        fillPreview,
				"apply":          fillApply,
				"repair staging": codexAccountCandidateStageSQL,
			},
		},
		{
			name: "session account mapping", predicate: codexQuotaSessionAccountCTE,
			queries: map[string]string{
				"preview":        fillPreview,
				"apply":          fillApply,
				"repair staging": codexAccountCandidateStageSQL,
			},
		},
		{
			name: "quota attribution", predicate: codexQuotaMatch,
			queries: map[string]string{
				"preview":      attrPreview,
				"apply":        attrApply,
				"repair batch": codexQuotaAttributeBatchSQL,
			},
		},
		{
			name: "account email mapping", predicate: codexQuotaAccountEmailCTE,
			queries: map[string]string{
				"preview":        attrPreview,
				"apply":          attrApply,
				"repair batch":   codexQuotaAttributeBatchSQL,
				"revert preview": codexInferredRevertPreviewSQL(),
			},
		},
		{
			name: "provenance", predicate: codexQuotaSourceExpr,
			queries: map[string]string{
				"preview":      attrPreview,
				"apply":        attrApply,
				"repair batch": codexQuotaAttributeBatchSQL,
			},
		},
		{
			name: "revert", predicate: codexRevertMatch,
			queries: map[string]string{
				"revert preview": codexInferredRevertPreviewSQL(),
				"repair batch":   codexRevertBatchSQL,
			},
		},
	}
	for _, tc := range cases {
		for where, q := range tc.queries {
			if !strings.Contains(q, tc.predicate) {
				t.Errorf("%s %s does not carry the shared %s predicate", tc.name, where, tc.name)
			}
		}
	}

	// Every one of them resolves the agent column the way every other reader does.
	// session_records still holds rows with the literal empty agent.
	const guard = `COALESCE(NULLIF(sr.agent, ''), 'claude') = 'codex'`
	for name, predicate := range map[string]string{
		"codexRevertMatch":      codexRevertMatch,
		"codexAccountFillMatch": codexAccountFillMatch,
		"codexQuotaMatch":       codexQuotaMatch,
	} {
		if !strings.Contains(predicate, guard) {
			t.Errorf("%s does not select codex rows as %q", name, guard)
		}
	}
}
