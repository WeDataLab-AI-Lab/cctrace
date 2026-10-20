package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// factInputTables are the tables p1b-design §5.2 names as inputs of the session
// analysis facts: a write to any of them owes the written sessions a recompute.
var factInputTables = []string{
	"session_records", "otel_events", "session_snapshots", "claude_imputed_cost", "codex_imputed_cost",
	"excluded_sessions", "excluded_accounts", "excluded_billing_accounts", "excluded_billing_links",
	"deleted_sessions",
}

// factWriterList is the writer list of p1b-design §5.2, by function name: the
// entry points and the shared refreshes that queue what they write, the delete
// paths of §6.2 that run the deletion contract, and the writers that owe no
// queue write, each with the reason. TestFactWritersMarkDirty and
// TestSessionFactDeletionContract are what show the listed ones do it.
var factWriterList = map[string]string{
	// §5.2 1-4: ingest.
	"InsertSessionRecords":      "entry point 1, sync",
	"ReenrichSessionRecords":    "entry point 2, reenrich",
	"InsertEventsTx":            "entry point 3, otel",
	"ingestSessionObservations": "entry point 4, snapshot",
	"IngestSessionObservations": "entry point 4, through ingestSessionObservations",
	// §5.2 5: the shared login_email refresh and each of its callers.
	"refreshLoginEmailDerivedRows":      "entry point 5, identity",
	"BackfillSessionRecordLoginEmail":   "entry point 5, through refreshLoginEmailDerivedRows",
	"InferSessionRecordLoginEmail":      "entry point 5, through refreshLoginEmailDerivedRows",
	"runLoginEmailHistoryBatch":         "entry point 5, through refreshLoginEmailDerivedRows",
	"applyCodexQuotaUpdate":             "entry point 5, through refreshLoginEmailDerivedRows",
	"FillCodexAccountFromQuota":         "entry point 5, through applyCodexQuotaUpdate",
	"AttributeCodexLoginEmailFromQuota": "entry point 5, through applyCodexQuotaUpdate",
	"rebuildAllLoginEmailDerivedRows":   "entry point 6, identity, every session with facts",
	"runCodexLoginEmailRepairBatch":     "entry point 6, through rebuildAllLoginEmailDerivedRows at completion",
	// §5.2 7-8: exclusions.
	"recomputeExcludedSessionsTx":        "entry point 7, exclusion",
	"applyExclusionChange":               "entry point 7, through recomputeExcludedSessionsTx",
	"ExcludeAccount":                     "entry point 7, through applyExclusionChange",
	"RemoveExcludedAccount":              "entry point 7, through applyExclusionChange",
	"ExcludeBillingAccount":              "entry point 7, through applyExclusionChange",
	"RemoveExcludedBillingAccount":       "entry point 7, through applyExclusionChange",
	"SelfExcludeBillingAccount":          "entry point 7, through applyExclusionChange",
	"RemoveSelfExcludedBillingAccount":   "entry point 7, through applyExclusionChange",
	"RefreshExcludedBillingLinks":        "entry point 7, through applyExclusionChange",
	"RecomputeExcludedSessions":          "entry point 7, through applyExclusionChange, every session with facts",
	"RefreshExcludedSessionsIncremental": "entry point 8, exclusion",
	// §5.2 9-10: imputed cost.
	"RefreshClaudeImputedCost":           "entry point 9, imputed, from refreshClaudeImputedCost",
	"rewriteClaudeImputedCostStep":       "entry point 9 in P1t step 3, imputed, after the index and the marker",
	"RefreshCodexImputedCost":            "entry point 10, imputed",
	"RefreshCodexImputedCostIncremental": "entry point 10, imputed",
	// §5.2 11-13: one-off repairs.
	"BackfillLoginEmailSource":     "entry point 11, identity",
	"BackfillTaskTypes":            "entry point 12, task_type",
	"ReclassifyStaleTaskTypes":     "entry point 12, task_type",
	"moveHash":                     "entry point 13, project_hash",
	"BackfillCanonicalProjectHash": "entry point 13, through moveHash",
	// §7.5 reset orchestration calls the source-locked Claude/Codex imputed writers,
	// which mark affected sessions dirty, then rebuilds overview rollups and clears
	// P1b facts plus that queue while holding the worker lock.
	"ResetSessionFacts": "reset recomputation: imputed writers mark dirty; source-locked rollup rebuild; final P1b clear is serialized against the worker",
	// §6.2: the delete paths.
	"DeleteSession":               "delete path: tombstone, steps 1-2 in tombstoneTx, 5 here",
	"DeleteProject":               "delete path: tombstone, steps 1-2 in tombstoneTx, 5 once after every hash",
	"PurgeBlockedProjects":        "delete path: tombstone, steps 1-2 in tombstoneTx, 5 here",
	"DeleteUserData":              "delete path: steps 1-5",
	"MergeUsers":                  "delete path: steps 4 and 5",
	"CleanupOrphanSessionRecords": "delete path: steps 1-5",
	"ReconcileSessionOverviewRollupsForRetention": "delete path: steps 1-5 and the dangling markers",
	"PruneSessionObservations":                    "delete path: steps 4 and 5",
	// The exceptions.
	"Migrate":                           "exception: the migrations' one-time writes run before the build, which queues every session",
	"sessionRecordMessageIDStep":        "exception: the P1t message_id backfill leaves every request key as it was, and the worker starts after request_set_v1",
	"sweepOne":                          "exception: the sweep deletes a tombstoned session's rows; the tombstone already queued its neighbours, and the session is never queued",
	"verifyDeletedSessionsBatch":        "exception: the sweep's tombstone bookkeeping; whether a session is tombstoned does not change",
	"recountSessionOverviewTokensBatch": "exception: P1t step 4 rewrites the overview rollups alone; no fact input changes",
	"BackfillSessionOverviewRollups":    "exception: the first build of the overview rollups writes nothing else",
}

var (
	// factWriteSQL is a write naming a fact input table.
	factWriteSQL = regexp.MustCompile(`(?i)\b(INSERT\s+INTO|UPDATE|DELETE\s+FROM|TRUNCATE(\s+TABLE)?)\s+(` +
		strings.Join(factInputTables, "|") + `)\b`)
	// tableFormattedWriteSQL is a write whose table is formatted or concatenated
	// in; the table names the function holds say which.
	tableFormattedWriteSQL = regexp.MustCompile(`(?i)\b(INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+(%|$)`)
)

// storeFunc is what the alarm knows of a function of this package: the string
// literals it holds or reaches through package-level constants and variables,
// and the functions it names.
type storeFunc struct {
	texts []string
	names map[string]bool
}

// The writer list of p1b-design §5.2 is the contract; this is its alarm. It reads
// the store package's source and finds (a) the functions that hold or build SQL
// writing a fact input table, and (b) the functions that call
// refreshSessionOverviewRollups or rebuildAllSessionOverviewRollups. Each has to
// be on the list, or reached only from functions that are.
//
// It is not a proof: SQL built in one function and run in another, a table name
// passed in from a caller, escape both halves, and moveHash is one. The list
// itself is what the container tests check.
//
// Mutation: take a writer off the list, or add a function that writes
// session_records without queueing, and it names it.
func TestFactWriterListComplete(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		files = append(files, f)
	}
	literals := func(n ast.Node) []string {
		var out []string
		ast.Inspect(n, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					out = append(out, s)
				}
			}
			return true
		})
		return out
	}

	pkgTexts := map[string][]string{}
	funcs := map[string]*storeFunc{}
	for _, f := range files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range vs.Names {
							for _, v := range vs.Values {
								pkgTexts[name.Name] = append(pkgTexts[name.Name], literals(v)...)
							}
						}
					}
				}
			case *ast.FuncDecl:
				// Methods and functions share one namespace here: two of the same name
				// are one entry, which errs toward naming more writers, not fewer.
				fn := funcs[d.Name.Name]
				if fn == nil {
					fn = &storeFunc{names: map[string]bool{}}
					funcs[d.Name.Name] = fn
				}
				if d.Body == nil {
					continue
				}
				fn.texts = append(fn.texts, literals(d.Body)...)
				ast.Inspect(d.Body, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok {
						fn.names[id.Name] = true
					}
					return true
				})
			}
		}
	}

	isTable := map[string]bool{}
	for _, table := range factInputTables {
		isTable[table] = true
	}
	writes := func(name string, fn *storeFunc) bool {
		texts := append([]string(nil), fn.texts...)
		for ref := range fn.names {
			if ref != name {
				texts = append(texts, pkgTexts[ref]...)
			}
		}
		namesTable, formatted := false, false
		for _, text := range texts {
			if factWriteSQL.MatchString(text) {
				return true
			}
			namesTable = namesTable || isTable[text]
			formatted = formatted || tableFormattedWriteSQL.MatchString(text)
		}
		return namesTable && (formatted || fn.names["CopyFrom"])
	}
	callers := map[string][]string{}
	for name, fn := range funcs {
		for ref := range fn.names {
			if _, ok := funcs[ref]; ok && ref != name {
				callers[ref] = append(callers[ref], name)
			}
		}
	}

	var flagged []string
	why := map[string]string{}
	for name, fn := range funcs {
		switch {
		case writes(name, fn):
			why[name] = "writes a fact input table"
		case (fn.names["refreshSessionOverviewRollups"] || fn.names["rebuildAllSessionOverviewRollups"]) &&
			name != "refreshSessionOverviewRollups" && name != "rebuildAllSessionOverviewRollups":
			why[name] = "refreshes the overview rollups"
		default:
			continue
		}
		flagged = append(flagged, name)
	}
	sort.Strings(flagged)

	// The alarm has to see the writers it is about, or it reports nothing for
	// the wrong reason.
	for _, known := range []string{"InsertSessionRecords", "InsertEventsTx", "ingestSessionObservations",
		"DeleteUserData", "sweepOne", "RefreshExcludedSessionsIncremental", "BackfillTaskTypes"} {
		if _, ok := why[known]; !ok {
			t.Errorf("the alarm does not see %s, a known writer", known)
		}
	}

	covered := map[string]bool{}
	visiting := map[string]bool{}
	var isCovered func(name string) bool
	isCovered = func(name string) bool {
		if _, ok := factWriterList[name]; ok {
			return true
		}
		if done, ok := covered[name]; ok {
			return done
		}
		if visiting[name] || len(callers[name]) == 0 {
			return false
		}
		visiting[name] = true
		ok := true
		for _, caller := range callers[name] {
			ok = ok && isCovered(caller)
		}
		visiting[name] = false
		covered[name] = ok
		return ok
	}
	for _, name := range flagged {
		if !isCovered(name) {
			t.Errorf("목록에 없는 writer: %s %s, and neither it nor every caller is on p1b-design §5.2's list"+
				" (factWriterList); queue what it writes and list it, or list it as an exception with the reason",
				name, why[name])
		}
	}
	for name := range factWriterList {
		if _, ok := funcs[name]; !ok {
			t.Errorf("factWriterList names %s, which is no function of the package", name)
		}
	}
}
