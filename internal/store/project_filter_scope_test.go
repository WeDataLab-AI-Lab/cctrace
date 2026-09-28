package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The picker and the session list have to see one population.
//
// ListProjects took no filter at all while the list filtered on source and agent, so
// a project whose every session the list dropped stayed selectable: choosing it left
// the list empty. On dev, 34 of 572 listed projects vanished under the page's default
// source filter alone.
func TestListProjects_scopedBySourceAndAgent(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, hash := range []string{"proj-interactive", "proj-headless", "proj-codex"} {
		if err := s.UpsertProject(ctx, "claude", hash, hash, "", "", "", "", now); err != nil {
			t.Fatalf("upsert %s: %v", hash, err)
		}
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "sess-interactive", RecordType: "user", Agent: "claude", ProjectHash: "proj-interactive", Entrypoint: "cli", SourceFile: "a.jsonl", PromptSource: "typed"},
		// Enriched with no genuine turn anywhere: headless by the list's own rule.
		{Ts: now, SessionID: "sess-headless", RecordType: "user", Agent: "claude", ProjectHash: "proj-headless", Entrypoint: "sdk-cli", SourceFile: "b.jsonl", PromptSource: "sdk"},
		{Ts: now, SessionID: "sess-codex", RecordType: "user", Agent: "codex", ProjectHash: "proj-codex", Entrypoint: "cli"},
	}); err != nil {
		t.Fatalf("seed session records: %v", err)
	}

	for _, tc := range []struct {
		name string
		f    ProjectFilter
		want []string
	}{
		{"no filter lists everything, as before", ProjectFilter{}, []string{"proj-interactive", "proj-headless", "proj-codex"}},
		{"interactive drops the headless-only project", ProjectFilter{Source: "interactive"}, []string{"proj-interactive", "proj-codex"}},
		{"headless keeps only it", ProjectFilter{Source: "headless"}, []string{"proj-headless"}},
		{"agent scopes to that harness", ProjectFilter{Agent: "codex"}, []string{"proj-codex"}},
		{"source and agent narrow together", ProjectFilter{Source: "interactive", Agent: "claude"}, []string{"proj-interactive"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertProjectHashes(t, s, tc.f, tc.want)
		})
	}
}

// A filter may only narrow a restricted user's scope, never widen it.
func TestListProjects_userScopeSurvivesFilters(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, hash := range []string{"proj-mine", "proj-theirs"} {
		if err := s.UpsertProject(ctx, "claude", hash, hash, "", "", "", "", now); err != nil {
			t.Fatalf("upsert %s: %v", hash, err)
		}
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "sess-mine", RecordType: "user", Agent: "claude", ProjectHash: "proj-mine", Entrypoint: "cli", UserID: "u1", ProfileEmail: "me@example.com"},
		{Ts: now, SessionID: "sess-theirs", RecordType: "user", Agent: "claude", ProjectHash: "proj-theirs", Entrypoint: "cli", UserID: "u2", ProfileEmail: "other@example.com"},
	}); err != nil {
		t.Fatalf("seed session records: %v", err)
	}

	assertProjectHashes(t, s, ProjectFilter{UserID: "u1"}, []string{"proj-mine"})
	assertProjectHashes(t, s, ProjectFilter{UserID: "u1", Source: "interactive"}, []string{"proj-mine"})
	assertProjectHashes(t, s, ProjectFilter{UserID: "u1", Agent: "claude"}, []string{"proj-mine"})
	assertProjectHashes(t, s, ProjectFilter{ProfileEmail: "me@example.com", Source: "interactive"}, []string{"proj-mine"})
}

// Classification is folded per session, not decided per record.
//
// Every other fixture here seeds one-record sessions, and with one record the row-level
// answer and the folded answer always coincide -- which is why they could not tell the
// GROUP BY apart from the DISTINCT it replaced. These two sessions disagree with
// themselves record by record, so only the fold gets them right:
//
//   - proj-sdk-mixed has a cli record and an sdk-cli record. Read row by row the cli one
//     looks interactive; folded, the session's entrypoint is sdk-cli and it is headless.
//   - proj-late-genuine is enriched throughout with one sdk turn and one typed turn. Read
//     row by row the sdk one looks headless; folded, the genuine turn makes the whole
//     session interactive.
func TestListProjects_classifiesPerSessionNotPerRecord(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, hash := range []string{"proj-sdk-mixed", "proj-late-genuine"} {
		if err := s.UpsertProject(ctx, "claude", hash, hash, "", "", "", "", now); err != nil {
			t.Fatalf("upsert %s: %v", hash, err)
		}
	}
	if err := s.InsertSessionRecords(ctx, mixedRecordSessions(now)); err != nil {
		t.Fatalf("seed session records: %v", err)
	}

	assertProjectHashes(t, s, ProjectFilter{Source: "interactive"}, []string{"proj-late-genuine"})
	assertProjectHashes(t, s, ProjectFilter{Source: "headless"}, []string{"proj-sdk-mixed"})
}

// The invariant the picker exists to satisfy: whatever scope it is asked for, the
// projects it offers are exactly the projects the session list has sessions for. Stated
// against ListSessionOverviews rather than against a hand-written expectation, because a
// hand-written one drifts the day either side's classification is edited.
func TestListProjects_agreesWithSessionOverviews(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()
	// The rollup table answers the overview when its backfill marker is present, and
	// then the comparison would be against a cache instead of against the query this
	// change edited. Dropping the marker pins both sides to their direct SQL.
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, sessionOverviewRollupBackfill); err != nil {
		t.Fatalf("clear rollup marker: %v", err)
	}
	// The store is shared across the whole package and built once (sync.Once), and
	// truncateTables does not touch schema_backfills: leaving the marker deleted would
	// put every later test in this package on the live path for good, which silently
	// turns the rollup equivalence tests into tautologies. Put it back the way the
	// rollup tests do.
	t.Cleanup(func() {
		if err := s.BackfillSessionOverviewRollups(context.Background()); err != nil {
			t.Fatalf("restore rollup marker: %v", err)
		}
	})

	for _, hash := range []string{"proj-sdk-mixed", "proj-late-genuine", "proj-codex", "proj-forked"} {
		if err := s.UpsertProject(ctx, "claude", hash, hash, "", "", "", "", now); err != nil {
			t.Fatalf("upsert %s: %v", hash, err)
		}
	}
	records := append(mixedRecordSessions(now), []*SessionRecord{
		{Ts: now, SessionID: "sess-codex", RecordType: "user", Agent: "codex", ProjectHash: "proj-codex", Entrypoint: "cli", UUID: "c1", LoginEmail: "me@example.com"},
		// A branch child: assembled folds it into its root, raw lists it.
		{Ts: now, SessionID: "sess-forked", RecordType: "user", Agent: "claude", ProjectHash: "proj-forked", Entrypoint: "cli", UUID: "f1", ForkedFromSession: "sess-codex"},
	}...)
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("seed session records: %v", err)
	}

	for _, f := range []ProjectFilter{
		{},
		{Source: "interactive"},
		{Source: "headless"},
		{Agent: "codex"},
		{Agent: "claude"},
		{Source: "interactive", Agent: "claude"},
		{FoldLineage: true},
		{LoginEmail: "me@example.com"},
	} {
		t.Run(fmt.Sprintf("%+v", f), func(t *testing.T) {
			overviews, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{
				LoginEmail:  f.LoginEmail,
				Source:      f.Source,
				Agent:       f.Agent,
				FoldLineage: f.FoldLineage,
				Limit:       1000,
			})
			if err != nil {
				t.Fatalf("list overviews: %v", err)
			}
			seen := map[string]bool{}
			var want []string
			for _, o := range overviews {
				if o.ProjectHash == "" || seen[o.ProjectHash] {
					continue
				}
				seen[o.ProjectHash] = true
				want = append(want, o.ProjectHash)
			}
			if len(want) == 0 {
				t.Fatal("fixture proves nothing: no session survived this filter")
			}
			assertProjectHashes(t, s, f, want)
		})
	}
}

func mixedRecordSessions(now time.Time) []*SessionRecord {
	return []*SessionRecord{
		{Ts: now, SessionID: "sess-sdk-mixed", RecordType: "user", Agent: "claude", ProjectHash: "proj-sdk-mixed", Entrypoint: "cli", SourceFile: "m.jsonl", PromptSource: "typed", UUID: "m1"},
		{Ts: now, SessionID: "sess-sdk-mixed", RecordType: "user", Agent: "claude", ProjectHash: "proj-sdk-mixed", Entrypoint: "sdk-cli", SourceFile: "m.jsonl", PromptSource: "sdk", UUID: "m2"},
		{Ts: now, SessionID: "sess-late-genuine", RecordType: "user", Agent: "claude", ProjectHash: "proj-late-genuine", Entrypoint: "cli", SourceFile: "g.jsonl", PromptSource: "sdk", UUID: "g1"},
		{Ts: now, SessionID: "sess-late-genuine", RecordType: "user", Agent: "claude", ProjectHash: "proj-late-genuine", Entrypoint: "cli", SourceFile: "g.jsonl", PromptSource: "typed", UUID: "g2"},
	}
}

func assertProjectHashes(t *testing.T, s *PgStore, f ProjectFilter, want []string) {
	t.Helper()
	projects, err := s.ListProjects(context.Background(), f)
	if err != nil {
		t.Fatalf("list projects %+v: %v", f, err)
	}
	got := map[string]bool{}
	for _, p := range projects {
		got[p.ProjectHash] = true
	}
	for _, h := range want {
		if !got[h] {
			t.Errorf("%+v: %s missing", f, h)
		}
		delete(got, h)
	}
	for h := range got {
		t.Errorf("%+v: %s must not be listed", f, h)
	}
}
