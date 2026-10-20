package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cctrace/internal/sessionfacts"
)

// seedTimelineClaude stores a typed question and its reply as a collecting
// client does, and queues the session.
func seedTimelineClaude(t *testing.T, s *PgStore, id string) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,uuid,parent_uuid,prompt_source,raw)
			VALUES ('2026-09-01 09:00:01+00',$1,'user','claude','owner','profile','u1','','typed',
			'{"cctraceCollect":1,"promptId":"p1","origin":{"kind":"human"},"message":{"content":"hello"}}'::jsonb)`,
		`INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,uuid,parent_uuid,model,raw)
			VALUES ('2026-09-01 09:00:02+00',$1,'assistant','claude','owner','profile','a1','u1','claude-opus-5',
			'{"cctraceCollect":1,"message":{"content":[{"type":"text","text":"hi"}]}}'::jsonb)`,
		`INSERT INTO session_fact_dirty(session_id) VALUES ($1) ON CONFLICT DO NOTHING`,
	} {
		if _, err := s.pool.Exec(ctx, q, id); err != nil {
			t.Fatal(err)
		}
	}
}

// requeueTimeline queues the session again, as a later change would, and
// returns the generation.
func requeueTimeline(t *testing.T, s *PgStore, id string) int64 {
	t.Helper()
	var generation int64
	if err := s.pool.QueryRow(context.Background(), `INSERT INTO session_fact_dirty(session_id) VALUES ($1)
		ON CONFLICT (session_id) DO UPDATE SET generation = nextval('session_fact_generation')
		RETURNING generation`, id).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	return generation
}

func dirtyGeneration(t *testing.T, s *PgStore, id string) int64 {
	t.Helper()
	var generation int64
	if err := s.pool.QueryRow(context.Background(), `SELECT generation FROM session_fact_dirty WHERE session_id=$1`, id).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	return generation
}

// The worker computes once and publishes facts with the P4 output hash and the
// timeline with its own extended hash, in one transaction. Requeued without a
// source change, the facts are unchanged and the rows are not written again:
// only the provenance's generation moves.
func TestSessionTimelinePublishWrittenThenUnchanged(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "publish-timeline")
	ctx := context.Background()
	seedTimelineClaude(t, s, "publish-timeline")
	generation := dirtyGeneration(t, s, "publish-timeline")
	// The same source as the run reads: what both hashes should be.
	load, err := s.LoadSessionFactsInput(ctx, "publish-timeline", "claude")
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.processSessionFact(ctx, "publish-timeline", generation)
	if err != nil || run.Outcome != "written" {
		t.Fatalf("first run %+v, %v", run, err)
	}
	dump := timelineDump(t, s, "publish-timeline")
	rows := dump[:strings.Index(dump, "observed")]
	if !strings.Contains(rows, " u1 {opener,input} -") || !strings.Contains(rows, " u1 {assistant_final} -") {
		t.Fatalf("stored rows\n%s", dump)
	}
	var factHash, storedFact, timelineHash []byte
	var storedGeneration int64
	if err := s.pool.QueryRow(ctx, `SELECT f.output_hash, p.fact_output_hash, p.timeline_hash, p.source_generation
		FROM session_analysis_facts f JOIN session_timeline_provenance p USING (session_id) WHERE session_id=$1`, "publish-timeline").
		Scan(&factHash, &storedFact, &timelineHash, &storedGeneration); err != nil {
		t.Fatal(err)
	}
	extended := sessionfacts.ComputeWithTimelineAndCommits(load.Input)
	if !bytes.Equal(factHash, extended.Hash) || !bytes.Equal(storedFact, factHash) {
		t.Fatalf("facts carry %x, provenance %x, want the P4 fact hash %x", factHash, storedFact, extended.Hash)
	}
	if bytes.Equal(timelineHash, factHash) || !bytes.Equal(timelineHash, extended.TimelineHash) {
		t.Fatalf("timeline hash %x is not the extended hash", timelineHash)
	}
	if storedGeneration != generation || !timelineValid(t, s, "publish-timeline") {
		t.Fatalf("provenance generation %d (want %d) or not valid", storedGeneration, generation)
	}

	var xmin []string
	readXmin := func() []string {
		t.Helper()
		got, err := s.pool.Query(ctx, `SELECT xmin::text FROM session_timeline_members WHERE session_id='publish-timeline' ORDER BY source_uuid`)
		if err != nil {
			t.Fatal(err)
		}
		defer got.Close()
		var out []string
		for got.Next() {
			var x string
			if err := got.Scan(&x); err != nil {
				t.Fatal(err)
			}
			out = append(out, x)
		}
		return out
	}
	xmin = readXmin()
	next := requeueTimeline(t, s, "publish-timeline")
	run, err = s.processSessionFact(ctx, "publish-timeline", next)
	if err != nil || run.Outcome != "unchanged" {
		t.Fatalf("second run %+v, %v", run, err)
	}
	if got := readXmin(); len(got) != len(xmin) || got[0] != xmin[0] || got[1] != xmin[1] {
		t.Fatalf("unchanged run rewrote the rows: %v then %v", xmin, got)
	}
	if err := s.pool.QueryRow(ctx, `SELECT source_generation FROM session_timeline_provenance WHERE session_id='publish-timeline'`).Scan(&storedGeneration); err != nil || storedGeneration != next {
		t.Fatalf("generation %d, want %d (%v)", storedGeneration, next, err)
	}
	if got := timelineDump(t, s, "publish-timeline"); got[:len(rows)] != rows {
		t.Fatalf("unchanged run changed the rows:\n%s", got)
	}
}

// A source change between the load and the publish rolls back the facts and
// the timeline together and keeps the queue row for a fresh computation.
func TestSessionTimelinePublishRollsBackOnWitnessChange(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "moved-timeline")
	ctx := context.Background()
	seedTimelineClaude(t, s, "moved-timeline")
	generation := dirtyGeneration(t, s, "moved-timeline")
	load, err := s.LoadSessionFactsInput(ctx, "moved-timeline", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if load.TimelineWitness == nil || load.TimelineWitness.Rows != 2 {
		t.Fatalf("load witness %+v", load.TimelineWitness)
	}
	out, timeline := computeSessionFactTimeline(load.Input)
	// A writer that does not queue the session, as direct SQL or a chunk drop.
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,uuid,raw)
		VALUES ('2026-09-01 09:00:03+00','moved-timeline','system','claude','owner','profile','s1','{}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	outcome, err := s.publishSessionFactWithTimeline(ctx, "moved-timeline", load.Generation, load, out, timeline, 1)
	if err != nil || outcome != "timeline_source" || !sessionFactAborted(outcome) {
		t.Fatalf("publish over a moved source %q, %v", outcome, err)
	}
	var facts, members, provenance int
	if err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_analysis_facts WHERE session_id=$1),
		(SELECT count(*) FROM session_timeline_members WHERE session_id=$1),
		(SELECT count(*) FROM session_timeline_provenance WHERE session_id=$1)`, "moved-timeline").Scan(&facts, &members, &provenance); err != nil {
		t.Fatal(err)
	}
	if facts != 0 || members != 0 || provenance != 0 {
		t.Fatalf("rolled-back publish left facts=%d members=%d provenance=%d", facts, members, provenance)
	}
	if got := dirtyGeneration(t, s, "moved-timeline"); got != generation {
		t.Fatalf("queue generation %d, want %d kept", got, generation)
	}
	// The next computation reads the moved source and publishes it.
	run, err := s.processSessionFact(ctx, "moved-timeline", generation)
	if err != nil || run.Outcome != "written" {
		t.Fatalf("fresh run %+v, %v", run, err)
	}
	if !timelineValid(t, s, "moved-timeline") {
		t.Fatal("fresh publish is not valid")
	}
}

func timelineCounts(t *testing.T, s *PgStore, id string) (members, provenance int) {
	t.Helper()
	if err := s.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM session_timeline_members WHERE session_id=$1),
		(SELECT count(*) FROM session_timeline_provenance WHERE session_id=$1)`, id).Scan(&members, &provenance); err != nil {
		t.Fatal(err)
	}
	return members, provenance
}

// Incomplete computations retain the old mapping and queue. Empty and excluded
// sessions still remove their facts and mapping in the same transaction.
func TestSessionTimelinePublishClearsWithoutATimeline(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	publish := func(id string) {
		t.Helper()
		timelineCleanup(t, s, id)
		seedTimelineClaude(t, s, id)
		if run, err := s.processSessionFact(ctx, id, dirtyGeneration(t, s, id)); err != nil || run.Outcome != "written" {
			t.Fatalf("%s: first run %+v, %v", id, run, err)
		}
		if m, p := timelineCounts(t, s, id); m != 2 || p != 1 {
			t.Fatalf("%s: mapping not published: %d %d", id, m, p)
		}
	}
	cleared := func(id, outcome string, run SessionFactRun, err error) {
		t.Helper()
		if err != nil || run.Outcome != outcome {
			t.Fatalf("%s: run %+v, %v, want %s", id, run, err, outcome)
		}
		if m, p := timelineCounts(t, s, id); m != 0 || p != 0 {
			t.Fatalf("%s: mapping kept: %d %d", id, m, p)
		}
	}

	publish("truncated-timeline")
	run, err := s.processSessionFactWithSnapshotCompute(ctx, "truncated-timeline", requeueTimeline(t, s, "truncated-timeline"),
		func(ctx context.Context, id, agent string) (SessionFactLoad, sessionfacts.TimelineCommitOutput, error) {
			load, out, err := s.computeSessionFactSnapshot(ctx, id, agent)
			out.Session.Coverage = "truncated"
			return load, out, err
		})
	if err == nil || run.Outcome != "" {
		t.Fatalf("truncated run published %+v, %v", run, err)
	}
	if m, p := timelineCounts(t, s, "truncated-timeline"); m != 2 || p != 1 {
		t.Fatalf("incomplete compute cleared mapping: %d %d", m, p)
	}
	var coverage string
	if err := s.pool.QueryRow(ctx, `SELECT coverage FROM session_analysis_facts WHERE session_id='truncated-timeline'`).Scan(&coverage); err != nil || coverage != "full" {
		t.Fatalf("existing coverage %q, %v", coverage, err)
	}

	publish("excluded-timeline")
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_sessions(session_id) VALUES ('excluded-timeline')`); err != nil {
		t.Fatal(err)
	}
	run, err = s.processSessionFact(ctx, "excluded-timeline", requeueTimeline(t, s, "excluded-timeline"))
	cleared("excluded-timeline", "copy_only", run, err)

	publish("empty-timeline")
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_records WHERE session_id='empty-timeline'`); err != nil {
		t.Fatal(err)
	}
	run, err = s.processSessionFact(ctx, "empty-timeline", requeueTimeline(t, s, "empty-timeline"))
	cleared("empty-timeline", "copy_only", run, err)
}

// A Codex session's usage rows are placed by their root turn id once the
// loader reads the uuid; a row stored without one leaves the session
// uncollected, its mapping removed, its facts still full.
func TestSessionTimelinePublishCodex(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "codex-timeline", "codex-old-timeline")
	ctx := context.Background()
	seed := func(id, usageUUID string) {
		t.Helper()
		for _, q := range []string{
			`INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,uuid,raw)
				VALUES ('2026-09-01 09:00:01+00',$1,'user','codex','owner','profile','content-user',
				'{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"do it"}]}}'::jsonb)`,
			`INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,uuid,raw)
				VALUES ('2026-09-01 09:00:02+00',$1,'usage','codex','owner','profile','` + usageUUID + `',
				'{"type":"token_usage_record","payload":{"turn_id":"t1","root_turn_id":"t1"}}'::jsonb)`,
			`INSERT INTO session_fact_dirty(session_id) VALUES ($1) ON CONFLICT DO NOTHING`,
		} {
			if _, err := s.pool.Exec(ctx, q, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed("codex-timeline", "content-usage")
	if run, err := s.processSessionFact(ctx, "codex-timeline", dirtyGeneration(t, s, "codex-timeline")); err != nil || run.Outcome != "written" {
		t.Fatalf("codex run %+v, %v", run, err)
	}
	dump := timelineDump(t, s, "codex-timeline")
	if !strings.Contains(dump, "content-usage/") || !strings.Contains(dump, " t1 {} -") ||
		!strings.Contains(dump, " - {} codex_same_second_lifecycle_boundary") || !timelineValid(t, s, "codex-timeline") {
		t.Fatalf("codex timeline\n%s", dump)
	}

	seed("codex-old-timeline", "")
	if run, err := s.processSessionFact(ctx, "codex-old-timeline", dirtyGeneration(t, s, "codex-old-timeline")); err != nil || run.Outcome != "written" {
		t.Fatalf("codex old run %+v, %v", run, err)
	}
	if m, p := timelineCounts(t, s, "codex-old-timeline"); m != 0 || p != 0 {
		t.Fatalf("uncollected codex mapping stored: %d %d", m, p)
	}
	var coverage string
	if err := s.pool.QueryRow(ctx, `SELECT coverage FROM session_analysis_facts WHERE session_id='codex-old-timeline'`).Scan(&coverage); err != nil || coverage != "full" {
		t.Fatalf("uncollected codex coverage %q, %v", coverage, err)
	}
}

// A run for a generation the queue has moved past publishes nothing and leaves
// the stored timeline as it was.
func TestSessionTimelinePublishStaleLeavesTimeline(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "stale-timeline")
	ctx := context.Background()
	seedTimelineClaude(t, s, "stale-timeline")
	old := dirtyGeneration(t, s, "stale-timeline")
	if run, err := s.processSessionFact(ctx, "stale-timeline", old); err != nil || run.Outcome != "written" {
		t.Fatalf("first run %+v, %v", run, err)
	}
	want := timelineDump(t, s, "stale-timeline")
	requeueTimeline(t, s, "stale-timeline")
	if run, err := s.processSessionFact(ctx, "stale-timeline", old); err != nil || run.Outcome != "stale" {
		t.Fatalf("stale run %+v, %v", run, err)
	}
	if got := timelineDump(t, s, "stale-timeline"); got != want {
		t.Fatalf("stale run changed the timeline:\n%s", got)
	}
}

// The publisher takes the session lock before its checks: a writer holding it
// commits first, and the publish then sees the moved source instead of
// storing a timeline of the old one.
func TestSessionTimelinePublishWaitsForSessionWriter(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "waiting-timeline")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	seedTimelineClaude(t, s, "waiting-timeline")
	load, err := s.LoadSessionFactsInput(ctx, "waiting-timeline", "claude")
	if err != nil {
		t.Fatal(err)
	}
	out, timeline := computeSessionFactTimeline(load.Input)
	writer, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(context.Background())
	if err = lockSessionOverviewSourceMutation(ctx, writer, false); err != nil {
		t.Fatal(err)
	}
	if err = lockSessionDeletionTargets(ctx, writer, []string{"waiting-timeline"}); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,uuid,raw)
		VALUES ('2026-09-01 09:00:03+00','waiting-timeline','system','claude','owner','profile','s1','{}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		outcome string
		err     error
	}
	done := make(chan result, 1)
	go func() {
		outcome, err := s.publishSessionFactWithTimeline(ctx, "waiting-timeline", load.Generation, load, out, timeline, 1)
		done <- result{outcome, err}
	}()
	for {
		var waiting int
		if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("publish did not wait for the session writer: %q %v", r.outcome, r.err)
		case <-ctx.Done():
			t.Fatal("publish never waited")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.err != nil || r.outcome != "timeline_source" {
		t.Fatalf("publish after the writer: %q %v", r.outcome, r.err)
	}
	if m, p := timelineCounts(t, s, "waiting-timeline"); m != 0 || p != 0 {
		t.Fatalf("a timeline of the old source was stored: %d %d", m, p)
	}
}

// Cancellation after a complete read/compute still preserves the last atomic
// publish. No fallback may replace its full coverage or clear its timeline.
func TestSessionTimelinePublishCancellationAfterComputeKeepsTimeline(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "late-timeline")
	ctx := context.Background()
	seedTimelineClaude(t, s, "late-timeline")
	if run, err := s.processSessionFact(ctx, "late-timeline", dirtyGeneration(t, s, "late-timeline")); err != nil || run.Outcome != "written" {
		t.Fatalf("first run %+v, %v", run, err)
	}
	computed := false
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	generation := requeueTimeline(t, s, "late-timeline")
	run, err := s.processSessionFactWithSnapshotCompute(workerCtx, "late-timeline", generation,
		func(ctx context.Context, id, agent string) (SessionFactLoad, sessionfacts.TimelineCommitOutput, error) {
			load, out, err := s.computeSessionFactSnapshot(ctx, id, agent)
			computed = err == nil
			cancel()
			return load, out, err
		})
	if !errors.Is(err, context.Canceled) || run.Outcome != "" || !computed {
		t.Fatalf("late run %+v, %v, computed=%v", run, err, computed)
	}
	var coverage string
	if err := s.pool.QueryRow(ctx, `SELECT coverage FROM session_analysis_facts WHERE session_id='late-timeline'`).Scan(&coverage); err != nil || coverage != "full" {
		t.Fatalf("coverage %q, %v", coverage, err)
	}
	if m, p := timelineCounts(t, s, "late-timeline"); m != 2 || p != 1 {
		t.Fatalf("cancelled compute cleared the old timeline: %d %d", m, p)
	}
	if queued := dirtyGeneration(t, s, "late-timeline"); queued != generation {
		t.Fatalf("cancelled compute lost queue generation: %d want %d", queued, generation)
	}
}

// A moved source stops the publish before it touches the facts already
// stored: they and the timeline stay as the last publish left them.
func TestSessionTimelinePublishMovedSourceKeepsStoredFacts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "kept-timeline")
	ctx := context.Background()
	seedTimelineClaude(t, s, "kept-timeline")
	if run, err := s.processSessionFact(ctx, "kept-timeline", dirtyGeneration(t, s, "kept-timeline")); err != nil || run.Outcome != "written" {
		t.Fatalf("first run %+v, %v", run, err)
	}
	facts := func() string {
		t.Helper()
		var f string
		if err := s.pool.QueryRow(ctx, `SELECT concat_ws(' ', encode(output_hash,'hex'), recompute_count, source_generation, xmin::text)
			FROM session_analysis_facts WHERE session_id='kept-timeline'`).Scan(&f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	wantFacts, wantTimeline := facts(), timelineDump(t, s, "kept-timeline")
	generation := requeueTimeline(t, s, "kept-timeline")
	// A different input, so the facts would be rewritten if the publish went on.
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET raw = jsonb_set(raw, '{message,content}', '"hello again"') WHERE session_id='kept-timeline' AND uuid='u1'`); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, "kept-timeline", "claude")
	if err != nil {
		t.Fatal(err)
	}
	out, timeline := computeSessionFactTimeline(load.Input)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,uuid,raw)
		VALUES ('2026-09-01 09:00:03+00','kept-timeline','system','claude','owner','profile','s1','{}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	outcome, err := s.publishSessionFactWithTimeline(ctx, "kept-timeline", load.Generation, load, out, timeline, 1)
	if err != nil || outcome != "timeline_source" {
		t.Fatalf("publish over a moved source %q, %v", outcome, err)
	}
	if got := facts(); got != wantFacts {
		t.Fatalf("stored facts changed: %s then %s", wantFacts, got)
	}
	if got := timelineDump(t, s, "kept-timeline"); got != wantTimeline {
		t.Fatalf("stored timeline changed:\n%s", got)
	}
	if got := dirtyGeneration(t, s, "kept-timeline"); got != generation {
		t.Fatalf("queue generation %d, want %d kept", got, generation)
	}
}

// Each of a truncated load, a missing witness, a missing timeline and an
// uncollected one stores no timeline on its own.
func TestStoresSessionTimelineNeedsAFullComputation(t *testing.T) {
	witness := SessionTimelineState{SessionID: "x"}
	full := SessionFactLoad{TimelineWitness: &witness}
	observed := &sessionFactTimeline{Facts: sessionfacts.TimelineFacts{State: "observed"}, HasTimeline: true}
	truncated := full
	truncated.Truncated = &sessionfacts.Output{}
	for name, c := range map[string]struct {
		load     SessionFactLoad
		timeline *sessionFactTimeline
		want     bool
	}{
		"full":           {full, observed, true},
		"undeterminable": {full, &sessionFactTimeline{Facts: sessionfacts.TimelineFacts{State: "undeterminable"}, HasTimeline: true}, true},
		"truncated":      {truncated, observed, false},
		"no witness":     {SessionFactLoad{}, observed, false},
		"no timeline":    {full, nil, false},
		"not collected":  {full, &sessionFactTimeline{Facts: sessionfacts.TimelineFacts{State: "not_collected"}, HasTimeline: true}, false},
	} {
		if got := storesSessionTimeline(c.load, c.timeline); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// A change that keeps every raw key -- here a rewritten raw -- moves the queue,
// not the witness. The r2 path publishes and requeues as before; the timeline
// path publishes neither facts nor mapping and keeps the queue row. A change
// that does not queue the session is not caught this way.
func TestSessionTimelinePublishNeedsTheLoadedGeneration(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	stale := func(id string) (SessionFactLoad, int64) {
		t.Helper()
		timelineCleanup(t, s, id)
		seedTimelineClaude(t, s, id)
		load, err := s.LoadSessionFactsInput(ctx, id, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `UPDATE session_records SET raw = jsonb_set(raw, '{message,content}', '"rewritten"') WHERE session_id=$1 AND uuid='u1'`, id); err != nil {
			t.Fatal(err)
		}
		next := requeueTimeline(t, s, id)
		if next == load.Generation {
			t.Fatal("the queue did not move")
		}
		return load, next
	}
	count := func(id string) (facts, members int) {
		t.Helper()
		if err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_analysis_facts WHERE session_id=$1),
			(SELECT count(*) FROM session_timeline_members WHERE session_id=$1)`, id).Scan(&facts, &members); err != nil {
			t.Fatal(err)
		}
		return facts, members
	}

	load, next := stale("generation-timeline")
	out, timeline := computeSessionFactTimeline(load.Input)
	outcome, err := s.publishSessionFactWithTimeline(ctx, "generation-timeline", load.Generation, load, out, timeline, 1)
	if err != nil || outcome != "timeline_source" {
		t.Fatalf("timeline publish of an older generation %q, %v", outcome, err)
	}
	if f, m := count("generation-timeline"); f != 0 || m != 0 {
		t.Fatalf("an older generation was published: facts=%d members=%d", f, m)
	}
	if got := dirtyGeneration(t, s, "generation-timeline"); got != next {
		t.Fatalf("queue generation %d, want %d", got, next)
	}

	load, next = stale("generation-legacy")
	outcome, err = s.publishSessionFact(ctx, "generation-legacy", load.Generation, load, sessionfacts.Compute(load.Input), 1)
	if err != nil || outcome != "requeued" {
		t.Fatalf("r2 publish of an older generation %q, %v", outcome, err)
	}
	if f, m := count("generation-legacy"); f != 1 || m != 0 {
		t.Fatalf("r2 path: facts=%d members=%d, want the facts and no mapping", f, m)
	}
	if got := dirtyGeneration(t, s, "generation-legacy"); got != next {
		t.Fatalf("requeued generation %d, want %d", got, next)
	}
}

// A requeue that commits after the publisher's generation check, while the
// publisher waits on the facts row, leaves the conditional queue delete with
// nothing to remove. The timeline path then rolls back the facts and the
// mapping and keeps the new queue row; the r2 path still publishes and
// requeues.
func TestSessionTimelinePublishRequeuedAfterTheCheck(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type state struct{ facts, timeline string }
	read := func(id string) state {
		t.Helper()
		var f string
		if err := s.pool.QueryRow(ctx, `SELECT concat_ws(' ', encode(output_hash,'hex'), recompute_count, source_generation, xmin::text)
			FROM session_analysis_facts WHERE session_id=$1`, id).Scan(&f); err != nil {
			t.Fatal(err)
		}
		return state{f, timelineDump(t, s, id)}
	}
	// race publishes the session's requeued generation while a requeue commits
	// between the generation check and the facts update, and returns the
	// outcome with the state before and after.
	race := func(id string, publish func(SessionFactLoad) (string, error)) (outcome string, before, after state) {
		t.Helper()
		timelineCleanup(t, s, id)
		seedTimelineClaude(t, s, id)
		if run, err := s.processSessionFact(ctx, id, dirtyGeneration(t, s, id)); err != nil || run.Outcome != "written" {
			t.Fatalf("%s: first run %+v, %v", id, run, err)
		}
		before = read(id)
		requeueTimeline(t, s, id)
		load, err := s.LoadSessionFactsInput(ctx, id, "claude")
		if err != nil {
			t.Fatal(err)
		}
		blocker, err := s.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(context.Background())
		if _, err = blocker.Exec(ctx, `SELECT 1 FROM session_analysis_facts WHERE session_id=$1 FOR UPDATE`, id); err != nil {
			t.Fatal(err)
		}
		type result struct {
			outcome string
			err     error
		}
		done := make(chan result, 1)
		go func() {
			outcome, err := publish(load)
			done <- result{outcome, err}
		}()
		for {
			var waiting int
			if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
				WHERE wait_event_type='Lock' AND query LIKE 'UPDATE session_analysis_facts%'`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting > 0 {
				break
			}
			select {
			case r := <-done:
				t.Fatalf("%s: publish did not wait on the facts row: %q %v", id, r.outcome, r.err)
			case <-ctx.Done():
				t.Fatalf("%s: publish never waited", id)
			case <-time.After(10 * time.Millisecond):
			}
		}
		next := requeueTimeline(t, s, id)
		if err = blocker.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		r := <-done
		if r.err != nil {
			t.Fatalf("%s: publish %v", id, r.err)
		}
		if got := dirtyGeneration(t, s, id); got != next {
			t.Fatalf("%s: queue generation %d, want %d kept", id, got, next)
		}
		return r.outcome, before, read(id)
	}

	outcome, before, after := race("late-requeue-timeline", func(load SessionFactLoad) (string, error) {
		out, timeline := computeSessionFactTimeline(load.Input)
		return s.publishSessionFactWithTimeline(ctx, "late-requeue-timeline", load.Generation, load, out, timeline, 1)
	})
	if outcome != "timeline_source" {
		t.Fatalf("timeline publish after a late requeue %q", outcome)
	}
	if after != before {
		t.Fatalf("the late-requeued publish changed\n%+v\nto\n%+v", before, after)
	}

	outcome, before, after = race("late-requeue-legacy", func(load SessionFactLoad) (string, error) {
		out := sessionfacts.ComputeWithTimelineAndCommits(load.Input).Output
		return s.publishSessionFact(ctx, "late-requeue-legacy", load.Generation, load, out, 1)
	})
	if outcome != "requeued" {
		t.Fatalf("r2 publish after a late requeue %q", outcome)
	}
	if after.facts == before.facts || !strings.HasSuffix(after.timeline, "no provenance") {
		t.Fatalf("r2 path did not publish: %+v", after)
	}
}
