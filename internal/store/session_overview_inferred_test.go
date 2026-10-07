package store

import (
	"context"
	"testing"
	"time"
)

// overviewOf finds one session in a list, so a test can say what it means rather
// than index into whatever order the query returned.
func overviewOf(t *testing.T, rows []*SessionOverview, sessionID string) *SessionOverview {
	t.Helper()
	for _, r := range rows {
		if r.SessionID == sessionID {
			return r
		}
	}
	t.Fatalf("session %s missing from %d overview rows", sessionID, len(rows))
	return nil
}

// The session detail reads a session, not a record, so the record-level
// login_email_source has to be aggregated before it can be shown. Any inferred
// row makes the session's account inferred: the header names a single
// representative account, and calling that account observed while some of the
// rows behind it were guessed would overstate what is known.
//
// Both read paths are asserted because they are two separate SQL statements over
// the same idea -- the rollup table (no date filter) and the live union (date
// filtered) -- and a fix applied to one of them silently leaves the other lying.
//
// Mutation: drop bool_or(login_email_source = 'inferred') from either statement
// and that path reports false for a session the inference pass just filled.
func TestInferSurfacesInferredOnSessionOverview(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(43).Add(9*time.Hour + 46*time.Minute + 23*time.Second)

	seedInference(t, s,
		inferSeed{
			user:   "u-guess",
			events: []inferEvent{{base, "bot@example.com"}, {base.Add(15 * time.Minute), "bot@example.com"}},
			recs:   []inferRec{{uuid: "g1", at: base.Add(5 * time.Minute)}},
		},
		// The control: already attributed, so the inference pass cannot touch it and
		// its session must stay unflagged. Without it the assertion below would pass
		// on a statement that reports true for everything.
		inferSeed{
			user: "u-known",
			recs: []inferRec{{uuid: "k1", at: base.Add(5 * time.Minute), email: "human@example.com"}},
		},
	)

	if _, err := s.InferSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}

	// Rollup path: no Since/Until, so ListSessionOverviews reads
	// session_overview_rollups, which the inference pass refreshed.
	rolled, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListSessionOverviews (rollup): %v", err)
	}
	if got := overviewOf(t, rolled, "u-guess-quiet"); !got.LoginEmailInferred {
		t.Errorf("rollup path: inferred session reported LoginEmailInferred=false")
	}
	if got := overviewOf(t, rolled, "u-known-quiet"); got.LoginEmailInferred {
		t.Errorf("rollup path: observed session reported LoginEmailInferred=true")
	}

	// Union path: a Since bound skips the rollup table entirely.
	since := base.Add(-time.Hour)
	live, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{Since: &since, Limit: 100})
	if err != nil {
		t.Fatalf("ListSessionOverviews (union): %v", err)
	}
	if got := overviewOf(t, live, "u-guess-quiet"); !got.LoginEmailInferred {
		t.Errorf("union path: inferred session reported LoginEmailInferred=false")
	}
	if got := overviewOf(t, live, "u-known-quiet"); got.LoginEmailInferred {
		t.Errorf("union path: observed session reported LoginEmailInferred=true")
	}
}

// stampLoginEmailSource writes a provenance value directly and rebuilds the derived
// rows for that session, which is what every pass that writes the column does. The
// quota attribution passes are not part of this file's subject; the point here is
// only that both read paths agree on which provenance values count as a guess.
func stampLoginEmailSource(t *testing.T, s *PgStore, sessionID, email, source string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx,
		`UPDATE session_records SET login_email = $2, login_email_source = $3 WHERE session_id = $1`,
		sessionID, email, source); err != nil {
		t.Fatalf("stamp %s: %v", sessionID, err)
	}
	if err := refreshLoginEmailDerivedRows(ctx, tx, []string{sessionID}); err != nil {
		t.Fatalf("refresh derived rows for %s: %v", sessionID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// 'quota-inferred' is a guess for the same reason 'inferred' is: at least one piece
// of the evidence chain behind it was itself inferred rather than observed. It has
// to read as a guess wherever a guess is marked, or the badge quietly stops meaning
// "this account was not observed" the moment the quota attribution pass runs.
//
// Both statements are asserted for the reason the test above gives -- they are two
// separate pieces of SQL over one idea, and widening one leaves the other lying.
//
// Mutation: widen either bool_or to IN ('inferred','quota-inferred') but not the
// other, and that path reports false for a quota-inferred session.
func TestQuotaInferredCountsAsInferredOnSessionOverview(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)

	seedInference(t, s,
		inferSeed{user: "u-quota", recs: []inferRec{{uuid: "qi1", at: base, email: "seed@example.com"}}},
		// The control: attributed from an observation, so it must stay unflagged.
		inferSeed{user: "u-observed", recs: []inferRec{{uuid: "ob1", at: base, email: "seed@example.com"}}},
	)
	stampLoginEmailSource(t, s, "u-quota-quiet", "wedataopenai@example.com", "quota-inferred")
	stampLoginEmailSource(t, s, "u-observed-quiet", "wedataopenai@example.com", "quota")

	rolled, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListSessionOverviews (rollup): %v", err)
	}
	if got := overviewOf(t, rolled, "u-quota-quiet"); !got.LoginEmailInferred {
		t.Errorf("rollup path: quota-inferred session reported LoginEmailInferred=false")
	}
	if got := overviewOf(t, rolled, "u-observed-quiet"); got.LoginEmailInferred {
		t.Errorf("rollup path: quota session reported LoginEmailInferred=true")
	}

	since := base.Add(-time.Hour)
	live, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{Since: &since, Limit: 100})
	if err != nil {
		t.Fatalf("ListSessionOverviews (union): %v", err)
	}
	if got := overviewOf(t, live, "u-quota-quiet"); !got.LoginEmailInferred {
		t.Errorf("union path: quota-inferred session reported LoginEmailInferred=false")
	}
	if got := overviewOf(t, live, "u-observed-quiet"); got.LoginEmailInferred {
		t.Errorf("union path: quota session reported LoginEmailInferred=true")
	}
}
