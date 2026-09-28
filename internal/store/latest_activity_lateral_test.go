package store

import (
	"strings"
	"testing"
)

// Each arm asks "what is the newest ts here", and a plain JOIN answers it by
// walking the table newest-first until a row belonging to the project turns up.
// That walk crosses every other project's rows on the way. On prod one project
// whose newest otel_events row was 24 days old spent 35.6s of a 36.0s query
// there -- the scan had to pass 24 days of other people's events first.
//
// Driving from the (small) project scope and taking one indexed lookup per
// session inverts that: 1,168 lookups on (session_id, ts DESC) instead of one
// unbounded backwards scan.
func TestLatestActivityDrivesFromProjectScopePerSession(t *testing.T) {
	q, _ := buildLatestActivityQuery(EventFilter{ProjectHash: "ph"})

	if !strings.Contains(q, "CROSS JOIN LATERAL") {
		t.Fatal("project-filtered arms must drive from project_scope, not scan each table newest-first")
	}
	if strings.Contains(q, "JOIN project_scope project_filter\n") && !strings.Contains(q, "CROSS JOIN LATERAL") {
		t.Fatal("plain join still present")
	}
	// The per-session lookup only pays off when the correlation is on the
	// indexed column; a lateral that forgets it degrades to the same full scan.
	if !strings.Contains(q, "project_filter.session_id") {
		t.Fatal("lateral must correlate on session_id, which is the indexed column")
	}
	// The outer query selects `ts` from the union of arms, so an aggregate arm
	// has to name its column. Without the alias Postgres calls it "max" and the
	// whole query fails with `column "ts" does not exist` -- which the string
	// assertions above happily passed.
	if !strings.Contains(q, "max(newest.ts) AS ts") {
		t.Fatal("aggregate arm must alias its column as ts for the outer select")
	}
}

// An aggregate arm returns one NULL row when its table holds nothing for the
// project, where the unfiltered form returned no row. Two things break: NULLs
// sort first under DESC, and a project with no activity anywhere returns a NULL
// row into a *time.Time scan.
func TestLatestActivityDropsNullArms(t *testing.T) {
	q, _ := buildLatestActivityQuery(EventFilter{ProjectHash: "ph"})
	if !strings.Contains(q, "WHERE ts IS NOT NULL") {
		t.Fatal("an empty aggregate arm must not become a NULL row: it outranks the real answer under DESC, and a project with no activity would return a row where the caller expects none")
	}
}

// Without a project filter nothing changes: the arms already stop at the first
// row and there is no scope to drive from.
func TestLatestActivityWithoutProjectFilterHasNoLateral(t *testing.T) {
	q, _ := buildLatestActivityQuery(EventFilter{})
	if strings.Contains(q, "LATERAL") {
		t.Fatal("unfiltered query gained a lateral it does not need")
	}
	if strings.Contains(q, "project_scope") {
		t.Fatal("unfiltered query built a project scope CTE")
	}
}

// An explicitly empty project list still matches nothing.
func TestLatestActivityEmptyProjectListStaysFalse(t *testing.T) {
	q, _ := buildLatestActivityQuery(EventFilter{ProjectHashesPresent: true})
	if !strings.Contains(q, "IS NULL AND FALSE") {
		t.Fatal("empty project list must select nothing")
	}
}
