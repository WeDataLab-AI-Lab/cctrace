package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestPgStore_WeeklyInsightsReturnsOnlyCallerAggregates(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)
	if err := s.UpsertProject(ctx, "claude", "weekly-project", "weekly-report", "", "", "", "", time.Time{}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "mine", UUID: "prompt", RecordType: "user", UserID: "caller", ProfileEmail: "caller@example.com", ProjectHash: "weekly-project", InputTokens: ptrInt(100), OutputTokens: ptrInt(20), Raw: json.RawMessage(`{"message":{"role":"user","content":"implement the endpoint"}}`)},
		{Ts: now.Add(time.Minute), SessionID: "other", UUID: "other-prompt", RecordType: "user", UserID: "other", ProfileEmail: "other@example.com", ProjectHash: "other-project", InputTokens: ptrInt(999), Raw: json.RawMessage(`{"message":{"role":"user","content":"write documentation"}}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "tool_result", UserID: "caller", ProfileEmail: "caller@example.com", ToolName: "Bash", ToolSuccess: ptrBool(false)},
		{Ts: now, EventName: "tool_result", UserID: "other", ProfileEmail: "other@example.com", ToolName: "Bash", ToolSuccess: ptrBool(true)},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	insights, err := s.WeeklyInsights(ctx, now.Add(-time.Minute), now.Add(time.Hour), "", "caller", "")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	if len(insights.Hours) != 1 || insights.Hours[0].Hour != 14 || insights.Hours[0].SessionCount != 1 {
		t.Fatalf("hours = %+v", insights.Hours)
	}
	if len(insights.Projects) != 1 || insights.Projects[0].ProjectName != "weekly-report" || insights.Projects[0].TotalTokens != 120 {
		t.Fatalf("projects = %+v", insights.Projects)
	}
	if len(insights.Tasks) != 1 || insights.Tasks[0].TaskType != "implementation" || insights.Tasks[0].PromptCount != 1 {
		t.Fatalf("tasks = %+v", insights.Tasks)
	}
	if len(insights.Tools) != 1 || insights.Tools[0].UseCount != 1 || insights.Tools[0].FailCount != 1 {
		t.Fatalf("tools = %+v", insights.Tools)
	}
}

// A UTC-fixed bucket reads three hours off for Asia/Seoul-adjacent zones and nine
// hours off for KST -- exactly the discrepancy the Weekly report dashboard showed
// ("06:00" on screen, 15:00 for the person who typed the prompts). Asia/Tokyo
// pins the offset without a DST calendar (UTC+9 year-round).
func TestPgStore_WeeklyInsightsHoursRespectsTimezone(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC) // 23:00 in Asia/Tokyo (UTC+9)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "s1", UUID: "prompt", RecordType: "user", UserID: "caller", ProfileEmail: "caller@example.com", Raw: json.RawMessage(`{"message":{"role":"user","content":"hi"}}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	insights, err := s.WeeklyInsights(ctx, now.Add(-time.Minute), now.Add(time.Hour), "", "caller", "Asia/Tokyo")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	if len(insights.Hours) != 1 || insights.Hours[0].Hour != 23 {
		t.Fatalf("hours = %+v, want a single 23:00 bucket (Asia/Tokyo)", insights.Hours)
	}
}

// TypedTurnCount is documented as "every typed prompt in the window, classified
// or not -- the denominator Tasks' counts are a subset of". It was read by
// summing task_segment_facts.typed_turn_count, and that sum is not the same
// population: a prompt outside any reconciled segment is counted by Tasks and
// not by the denominator, and the window was applied to the segment's start_ts
// rather than to the turn's own ts.
//
// The dashboard divides by it. On dev with production data the card read
// "Testing 243 prompts (486%)" and "Request of no listed type 1,020 prompts
// (2040%)", and #430's suggestion text repeated it as "486% of what you asked
// for was testing". Segment facts are written by the reconciler, so any lag --
// which is the whole subject of #435 -- turns the shares into nonsense.
//
// This pins the invariant rather than the number: whatever the denominator is
// read from, it cannot be smaller than the counts it divides.
func TestPgStore_WeeklyInsightsTypedTurnCountCoversTasks(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)

	// Three typed prompts, far enough apart that the segment builder would put
	// them in separate runs -- but nothing here runs it, which is the point: the
	// reconciler is periodic and the report must not depend on having caught up.
	records := []*SessionRecord{}
	for i, text := range []string{"implement the endpoint", "write documentation", "add a test"} {
		records = append(records, &SessionRecord{
			Ts: now.Add(time.Duration(i) * time.Hour), SessionID: "s1",
			UUID: "p" + string(rune('a'+i)), RecordType: "user",
			UserID: "caller", ProfileEmail: "caller@example.com",
			Raw: json.RawMessage(`{"message":{"role":"user","content":"` + text + `"}}`),
		})
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	// The state the report has to survive: prompts are in, their segment facts are
	// not. InsertSessionRecords builds them synchronously, so leaving that alone
	// would test a store that has already caught up -- which is exactly the case
	// that never failed. Emptying the table models a bulk load or a reconciler
	// that has not run yet; both happen.
	if _, err := s.pool.Exec(ctx, "TRUNCATE task_segment_facts"); err != nil {
		t.Fatalf("truncate facts: %v", err)
	}

	insights, err := s.WeeklyInsights(ctx, now.Add(-time.Minute), now.Add(24*time.Hour), "", "caller", "")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	var classified int64
	for _, task := range insights.Tasks {
		classified += task.PromptCount
	}
	if classified == 0 {
		t.Fatalf("fixture classified nothing; tasks = %+v", insights.Tasks)
	}
	if insights.TypedTurnCount < classified {
		t.Fatalf("TypedTurnCount = %d but Tasks sum to %d: the denominator is smaller than "+
			"what it divides, so every share exceeds 100%%", insights.TypedTurnCount, classified)
	}
}

// A conversation cannot happen inside one second. When a whole session's records
// carry effectively the same timestamp, the work is real but its times are not --
// every hour bucket it lands in is an artefact of when the file was written
// (#686). prod held 42 such segments at one instant, one of them with 3,274 turns.
func TestWeeklyInsights_CountsSessionsWithNoUsableTimeline(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)

	// One imported session: twenty-five records inside a few hundred microseconds,
	// the shape a bulk conversion leaves behind.
	collapsed := []*SessionRecord{}
	for i := 0; i < 25; i++ {
		collapsed = append(collapsed, &SessionRecord{
			Ts: now.Add(time.Duration(i) * time.Microsecond), SessionID: "imported",
			UUID: fmt.Sprintf("imported-%d", i), RecordType: "user", UserID: "u1",
			ProjectHash: "p1", Raw: []byte(`{"message":{"role":"user","content":"work"}}`),
		})
	}
	// And one ordinary session spread over real minutes.
	ordinary := []*SessionRecord{}
	for i := 0; i < 25; i++ {
		ordinary = append(ordinary, &SessionRecord{
			Ts: now.Add(time.Duration(i) * time.Minute), SessionID: "lived",
			UUID: fmt.Sprintf("lived-%d", i), RecordType: "user", UserID: "u1",
			ProjectHash: "p2", Raw: []byte(`{"message":{"role":"user","content":"work"}}`),
		})
	}
	if err := s.InsertSessionRecords(ctx, append(collapsed, ordinary...)); err != nil {
		t.Fatalf("insert: %v", err)
	}

	insights, err := s.WeeklyInsights(ctx, now.Add(-time.Hour), now.Add(2*time.Hour), "", "u1", "UTC")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	if insights.CollapsedTimelineSessions != 1 {
		t.Fatalf("collapsed = %d, want 1 -- the imported session, not the lived one",
			insights.CollapsedTimelineSessions)
	}
}

func TestWeeklyInsights_ShortRealSessionIsNotCollapsed(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)

	// Five records in half a second: quick, but nowhere near the record floor. The
	// thresholds are conservative on purpose -- a looser cut starts calling short
	// real sessions broken.
	records := []*SessionRecord{}
	for i := 0; i < 5; i++ {
		records = append(records, &SessionRecord{
			Ts: now.Add(time.Duration(i*100) * time.Millisecond), SessionID: "quick",
			UUID: fmt.Sprintf("quick-%d", i), RecordType: "user", UserID: "u1",
			ProjectHash: "p1", Raw: []byte(`{"message":{"role":"user","content":"ok"}}`),
		})
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("insert: %v", err)
	}

	insights, err := s.WeeklyInsights(ctx, now.Add(-time.Hour), now.Add(time.Hour), "", "u1", "UTC")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	if insights.CollapsedTimelineSessions != 0 {
		t.Fatalf("collapsed = %d, want 0", insights.CollapsedTimelineSessions)
	}
}

func TestPgStore_WeeklyInsightsScopeCounts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	since := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	until := since.Add(24 * time.Hour)
	records := []*SessionRecord{}
	for i, f := range []struct {
		session, agent, user, email string
		ts                          time.Time
	}{
		{"covered", "claude", "caller", "caller@example.com", since},
		{"covered", "claude", "caller", "caller@example.com", since.Add(time.Minute)},
		{"uncovered", "codex", "caller", "caller@example.com", since},
		{"old-segment", "claude", "caller", "caller@example.com", since},
		{"email-only", "codex", "legacy", "caller@example.com", since},
		{"other", "codex", "other", "other@example.com", since},
		{"before", "claude", "caller", "caller@example.com", since.Add(-time.Second)},
		{"until", "claude", "caller", "caller@example.com", until},
	} {
		records = append(records, &SessionRecord{Ts: f.ts, SessionID: f.session, Agent: f.agent, UserID: f.user, ProfileEmail: f.email, UUID: fmt.Sprintf("scope-%d", i), RecordType: "user", Raw: json.RawMessage(`{"message":{"role":"user","content":"work"}}`)})
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	// Model reconciler lag explicitly, including coverage from outside the window.
	if _, err := s.pool.Exec(ctx, `TRUNCATE task_segment_facts`); err != nil {
		t.Fatal(err)
	}
	// Boundaries point at real records: a segment counts only while its boundary
	// record is still visible.
	if _, err := s.pool.Exec(ctx, `INSERT INTO task_segment_facts (boundary_record_id, session_id, start_ts, user_id, profile_email)
SELECT sr.id, f.session_id, f.start_ts, f.user_id, f.profile_email
FROM (VALUES
 ('scope-0','covered',$1::timestamptz,'caller','caller@example.com'),
 ('scope-1','covered',$1::timestamptz + interval '1 hour','caller','caller@example.com'),
 ('scope-3','old-segment',$1::timestamptz - interval '1 second','caller','caller@example.com'),
 ('scope-4','email-only',$1::timestamptz,'legacy','caller@example.com'),
 ('scope-5','other',$1::timestamptz,'other','other@example.com'),
 ('scope-7','until',$2::timestamptz,'caller','caller@example.com'),
 ('scope-2','uncovered',$1::timestamptz,'other','other@example.com')
) AS f(uuid, session_id, start_ts, user_id, profile_email)
JOIN session_records sr ON sr.uuid = f.uuid`, since, until); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, email, user                  string
		claude, codex, segments, uncovered int64
	}{
		{"user", "", "caller", 2, 1, 2, 1},
		{"email", "caller@example.com", "", 2, 2, 3, 1},
		{"both", "caller@example.com", "caller", 2, 2, 3, 1},
		{"empty", "", "", 0, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.WeeklyInsights(ctx, since, until, tc.email, tc.user, "")
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int64{}
			for _, a := range got.AgentSessions {
				counts[a.Agent] = a.SessionCount
			}
			wantAgents := 2
			if tc.claude == 0 {
				wantAgents = 0
			}
			if counts["claude"] != tc.claude || counts["codex"] != tc.codex || len(counts) != wantAgents {
				t.Errorf("agent sessions = %+v", got.AgentSessions)
			}
			if got.SegmentCount != tc.segments {
				t.Errorf("segments = %d, want %d", got.SegmentCount, tc.segments)
			}
			if got.UncoveredSessionCount != tc.uncovered {
				t.Errorf("uncovered = %d, want %d", got.UncoveredSessionCount, tc.uncovered)
			}
		})
	}
}

// Segments are counted from task_segment_facts, which the exclusion views do not
// cover. The drill-down in TaskSegmentsByType already asks whether the boundary
// record is still visible; the scope line has to ask the same, or an excluded
// session's segments stay in "작업 구간 N" after its sessions are gone.
func TestWeeklyInsights_SegmentCountSkipsExcludedSessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "kept", UUID: "kept-1", RecordType: "user", UserID: "u1", Raw: json.RawMessage(`{"message":{"role":"user","content":"work"}}`)},
		{Ts: now, SessionID: "hidden", UUID: "hidden-1", RecordType: "user", UserID: "u1", Raw: json.RawMessage(`{"message":{"role":"user","content":"work"}}`)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_sessions (session_id) VALUES ('hidden')`); err != nil {
		t.Fatalf("exclude: %v", err)
	}

	got, err := s.WeeklyInsights(ctx, now.Add(-time.Hour), now.Add(time.Hour), "", "u1", "")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	if got.SegmentCount != 1 {
		t.Fatalf("segments = %d, want 1 -- the excluded session's segment must not count", got.SegmentCount)
	}
}

// Segment facts are built only from typed user turns. A session with none --
// assistant records only, or tool results posted as user records -- can never be
// covered, so counting it as uncovered reports reconciler lag that is not there.
func TestWeeklyInsights_UncoveredCountsOnlySessionsWithTypedTurns(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "assistant-only", UUID: "a-1", RecordType: "assistant", UserID: "u1", Raw: json.RawMessage(`{"message":{"role":"assistant","content":"done"}}`)},
		{Ts: now, SessionID: "tool-result-only", UUID: "t-1", RecordType: "user", UserID: "u1", Raw: json.RawMessage(`{"message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`)},
		{Ts: now, SessionID: "lagging", UUID: "l-1", RecordType: "user", UserID: "u1", Raw: json.RawMessage(`{"message":{"role":"user","content":"work"}}`)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `TRUNCATE task_segment_facts`); err != nil {
		t.Fatalf("truncate facts: %v", err)
	}

	got, err := s.WeeklyInsights(ctx, now.Add(-time.Hour), now.Add(time.Hour), "", "u1", "")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	if got.UncoveredSessionCount != 1 {
		t.Fatalf("uncovered = %d, want 1 -- only the session with a typed turn and no fact", got.UncoveredSessionCount)
	}
}

// The scope line tells the reader that excluded accounts and sessions were not
// used. Said to someone none of whose records were excluded, it is a standing
// disclaimer, and standing disclaimers teach the reader to skip the caveats that
// do apply -- the same reason an agent caveat is dropped when that agent has no
// sessions this week. Counting the caller's own hidden records is what lets the
// screen decide, so it counts records, not exclusion rows: an exclusion the
// admin configured that never touched this caller's week is not this caller's
// caveat.
func TestWeeklyInsights_CountsOnlyTheCallersHiddenRecords(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "kept", UUID: "kept-1", RecordType: "user", UserID: "u1", Raw: json.RawMessage(`{"message":{"role":"user","content":"work"}}`)},
		{Ts: now, SessionID: "hidden", UUID: "hidden-1", RecordType: "user", UserID: "u1", Raw: json.RawMessage(`{"message":{"role":"user","content":"work"}}`)},
		{Ts: now, SessionID: "hidden", UUID: "hidden-2", RecordType: "assistant", UserID: "u1", Raw: json.RawMessage(`{"message":{"role":"assistant","content":"ok"}}`)},
		{Ts: now, SessionID: "theirs", UUID: "theirs-1", RecordType: "user", UserID: "u2", Raw: json.RawMessage(`{"message":{"role":"user","content":"work"}}`)},
		// u2's other record is NOT excluded. The count is read as a subtraction, so
		// an out-of-scope row that is hidden anyway cannot catch the visible half
		// losing the caller scope -- only a visible one can.
		{Ts: now, SessionID: "theirs-kept", UUID: "theirs-2", RecordType: "user", UserID: "u2", Raw: json.RawMessage(`{"message":{"role":"user","content":"work"}}`)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_sessions (session_id) VALUES ('hidden'), ('theirs')`); err != nil {
		t.Fatalf("exclude: %v", err)
	}

	got, err := s.WeeklyInsights(ctx, now.Add(-time.Hour), now.Add(time.Hour), "", "u1", "")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	if got.ExcludedRecordCount != 2 {
		t.Fatalf("excluded = %d, want 2 -- u1's two hidden records, not u2's and not the kept one", got.ExcludedRecordCount)
	}
}

// Nothing excluded must read as zero, not as "unknown": the screen prints the
// sentence only above zero, so a wrong non-zero would restore the boilerplate.
func TestWeeklyInsights_ExcludedRecordCountIsZeroWhenNothingIsExcluded(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "kept", UUID: "kept-1", RecordType: "user", UserID: "u1", Raw: json.RawMessage(`{"message":{"role":"user","content":"work"}}`)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := s.WeeklyInsights(ctx, now.Add(-time.Hour), now.Add(time.Hour), "", "u1", "")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	if got.ExcludedRecordCount != 0 {
		t.Fatalf("excluded = %d, want 0", got.ExcludedRecordCount)
	}
}

// Rows written before the agent column existed carry agent = ”. They are Claude
// sessions, and the projects query already reads them that way.
func TestWeeklyInsights_AgentSessionsTreatsEmptyAgentAsClaude(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "tagged", UUID: "c-1", Agent: "claude", RecordType: "user", UserID: "u1", Raw: json.RawMessage(`{"message":{"role":"user","content":"work"}}`)},
		{Ts: now, SessionID: "legacy", UUID: "c-2", Agent: "claude", RecordType: "user", UserID: "u1", Raw: json.RawMessage(`{"message":{"role":"user","content":"work"}}`)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// InsertSessionRecords fills an empty agent, so the legacy shape is written directly.
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET agent = '' WHERE session_id = 'legacy'`); err != nil {
		t.Fatalf("blank agent: %v", err)
	}

	got, err := s.WeeklyInsights(ctx, now.Add(-time.Hour), now.Add(time.Hour), "", "u1", "")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	if len(got.AgentSessions) != 1 || got.AgentSessions[0].Agent != "claude" || got.AgentSessions[0].SessionCount != 2 {
		t.Fatalf("agent sessions = %+v, want one claude row with 2 sessions", got.AgentSessions)
	}
}

// Codex sends no tool_result event; its calls and outcomes arrive as the
// codex.tool.call delta metric, with no session_id (#698).
func TestWeeklyInsights_ToolsIncludeCodexToolCallMetric(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "tool_result", UserID: "caller", ProfileEmail: "caller@example.com", ToolName: "Bash", ToolSuccess: ptrBool(true)},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	three, one, nine := int64(3), int64(1), int64(9)
	codex := func(ts time.Time, user, account, success string, v *int64) *OtelMetric {
		return &OtelMetric{Ts: ts, MetricName: "codex.tool.call", UserID: user, ProfileEmail: user + "@example.com",
			Agent: "codex", BillingProvider: "openai", AccountID: account, ValueInt: v,
			Dimensions: map[string]interface{}{"tool": "shell", "success": success}}
	}
	if err := s.InsertMetrics(ctx, []*OtelMetric{
		codex(now, "caller", "", "true", &three),
		codex(now.Add(time.Second), "caller", "", "false", &one),
		codex(now, "other", "", "false", &nine),                    // someone else
		codex(now.Add(-2*time.Hour), "caller", "", "false", &nine), // outside the week
		codex(now, "caller", "acct-hidden", "false", &nine),        // excluded billing account
		{Ts: now, MetricName: "codex.turn", UserID: "caller", ValueInt: &nine, Dimensions: map[string]interface{}{"tool": "shell"}},
	}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_billing_accounts (billing_provider, account_id) VALUES ('openai', 'acct-hidden')`); err != nil {
		t.Fatalf("exclude: %v", err)
	}

	insights, err := s.WeeklyInsights(ctx, now.Add(-time.Minute), now.Add(time.Hour), "", "caller", "")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	want := []WeeklyInsightTool{{ToolName: "shell", UseCount: 4, FailCount: 1}, {ToolName: "Bash", UseCount: 1, FailCount: 0}}
	if fmt.Sprint(insights.Tools) != fmt.Sprint(want) {
		t.Fatalf("tools = %+v, want %+v", insights.Tools, want)
	}
}

// A caller known only by profile_email gets the tool calls of both arms made
// under that address, whatever user_id they carry, within [since, until).
func TestWeeklyInsights_ToolsScopeByProfileEmailAndWindow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)
	since, until := now.Add(-time.Minute), now.Add(time.Hour)
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "tool_result", UserID: "some-id", ProfileEmail: "caller@example.com", ToolName: "Read", ToolSuccess: ptrBool(true)},
		{Ts: since, EventName: "tool_result", ProfileEmail: "caller@example.com", ToolName: "Read", ToolSuccess: ptrBool(true)},
		{Ts: until, EventName: "tool_result", ProfileEmail: "caller@example.com", ToolName: "Read", ToolSuccess: ptrBool(false)},
		{Ts: now, EventName: "tool_result", ProfileEmail: "other@example.com", ToolName: "Read", ToolSuccess: ptrBool(false)},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	two, nine := int64(2), int64(9)
	codex := func(ts time.Time, profile string, v *int64) *OtelMetric {
		return &OtelMetric{Ts: ts, MetricName: "codex.tool.call", ProfileEmail: profile, Agent: "codex", ValueInt: v,
			Dimensions: map[string]interface{}{"tool": "shell", "success": "false"}}
	}
	if err := s.InsertMetrics(ctx, []*OtelMetric{
		codex(now, "caller@example.com", &two),
		codex(until, "caller@example.com", &nine),
		codex(now, "other@example.com", &nine),
	}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	insights, err := s.WeeklyInsights(ctx, since, until, "caller@example.com", "", "")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	want := []WeeklyInsightTool{{ToolName: "shell", UseCount: 2, FailCount: 2}, {ToolName: "Read", UseCount: 2, FailCount: 0}}
	if fmt.Sprint(insights.Tools) != fmt.Sprint(want) {
		t.Fatalf("tools = %+v, want %+v", insights.Tools, want)
	}
}

// The count is "how many of this caller's rows in the window the views hid",
// which is one number per row, not one per exclusion rule. Both exclusion axes
// can name the same row -- an account excluded by login_email whose sessions are
// also swept into excluded_sessions is the ordinary shape, not an edge case (see
// the #298 note on visible_session_records) -- so anything that adds the two
// exclusion populations together overcounts. The row hidden on both axes here is
// what separates the two readings: 4 rows hidden, 5 exclusion hits.
//
// An excluded_sessions entry naming a session with no records in the window
// ('ghost') must add nothing: that table is a session-id set kept by a periodic
// sweep and outlives the records it names, so counting its entries rather than
// the rows they hide would print a caveat for a week that has none.
//
// The count is read as a subtraction of two counts, so the seed also has to pin
// the subtrahend's window and caller scope. That needs rows that are VISIBLE and
// out of scope ('theirs-visible', another caller) or out of the window
// ('old-visible'): an out-of-scope row that is also excluded proves nothing,
// because it is absent from the visible half either way. With these two, losing
// the scope or the window on the visible subquery moves the answer from 4 to 3.
func TestWeeklyInsights_ExcludedRecordCountCountsRowsNotExclusions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)
	user := json.RawMessage(`{"message":{"role":"user","content":"work"}}`)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		// Visible: neither axis names it.
		{Ts: now, SessionID: "kept", UUID: "kept-1", RecordType: "user", UserID: "u1", Raw: user},
		// Hidden by excluded_sessions alone.
		{Ts: now, SessionID: "sess-hidden", UUID: "sess-1", RecordType: "user", UserID: "u1", Raw: user},
		{Ts: now, SessionID: "sess-hidden", UUID: "sess-2", RecordType: "assistant", UserID: "u1", Raw: user},
		// Hidden by excluded_accounts alone.
		{Ts: now, SessionID: "acct-hidden", UUID: "acct-1", RecordType: "user", UserID: "u1", LoginEmail: "hidden@example.com", Raw: user},
		// Hidden by both axes at once -- one hidden row, two exclusion hits.
		{Ts: now, SessionID: "both-hidden", UUID: "both-1", RecordType: "user", UserID: "u1", LoginEmail: "hidden@example.com", Raw: user},
		// Hidden, but outside the window.
		{Ts: now.Add(-48 * time.Hour), SessionID: "sess-hidden", UUID: "old-1", RecordType: "user", UserID: "u1", Raw: user},
		// Hidden, but another caller's.
		{Ts: now, SessionID: "sess-hidden", UUID: "theirs-1", RecordType: "user", UserID: "u2", Raw: user},
		// Visible and out of scope: only a row the visible half would wrongly count
		// can catch the visible half losing the caller scope.
		{Ts: now, SessionID: "theirs-visible", UUID: "theirs-2", RecordType: "user", UserID: "u2", Raw: user},
		// Visible and out of the window, for the same reason on the other axis.
		{Ts: now.Add(-48 * time.Hour), SessionID: "old-visible", UUID: "old-2", RecordType: "user", UserID: "u1", Raw: user},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO excluded_sessions (session_id) VALUES ('sess-hidden'), ('both-hidden'), ('ghost')`); err != nil {
		t.Fatalf("exclude sessions: %v", err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO excluded_accounts (login_email) VALUES ('hidden@example.com')`); err != nil {
		t.Fatalf("exclude account: %v", err)
	}

	got, err := s.WeeklyInsights(ctx, now.Add(-time.Hour), now.Add(time.Hour), "", "u1", "")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	if got.ExcludedRecordCount != 4 {
		t.Fatalf("excluded = %d, want 4 -- two session-hidden rows, one account-hidden, one hidden by both counted once; not the kept row, the out-of-window rows, u2's rows, or the record-less 'ghost' exclusion", got.ExcludedRecordCount)
	}
	// A separate query over the same seed, asserted here only to prove the seed is
	// not degenerate: 'kept' really is visible and in scope, so the 4 above is a
	// count of hidden rows rather than the whole week reading as hidden.
	if len(got.AgentSessions) != 1 || got.AgentSessions[0].SessionCount != 1 {
		t.Fatalf("agent sessions = %+v, want the single kept session", got.AgentSessions)
	}
}
