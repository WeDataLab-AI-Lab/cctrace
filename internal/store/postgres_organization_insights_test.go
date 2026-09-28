package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestOrganizationInsightsSuppressesDimensionsBelowDistinctUserThreshold(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	insert := func(user string) {
		t.Helper()
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
			Ts: now, SessionID: "session-" + user, UUID: "prompt-" + user, RecordType: "user", UserID: user, ProfileEmail: user + "@example.com", ProjectHash: "shared-project",
			Raw: []byte(`{"message":{"role":"user","content":"implement the endpoint"}}`),
		}}); err != nil {
			t.Fatalf("insert session record %s: %v", user, err)
		}
		if err := s.InsertEvents(ctx, []*OtelEvent{
			{Ts: now, EventName: "api_request", UserID: user, ProfileEmail: user + "@example.com", Model: "sonnet", InputTokens: ptrInt(10), OutputTokens: ptrInt(2), CostUSD: ptrFloat(0.1)},
			{Ts: now, EventName: "tool_result", UserID: user, ProfileEmail: user + "@example.com", ToolName: "Bash", ToolSuccess: ptrBool(user != "user-1")},
		}); err != nil {
			t.Fatalf("insert %s: %v", user, err)
		}
	}
	for i := 1; i <= 4; i++ {
		insert(fmt.Sprintf("user-%d", i))
	}
	insights, err := s.OrganizationInsights(ctx, now.Add(-time.Minute), now.Add(time.Minute), 5)
	if err != nil {
		t.Fatalf("OrganizationInsights: %v", err)
	}
	if insights.Available || insights.ActiveUsers != 0 || len(insights.Models) != 0 || len(insights.Tools) != 0 || len(insights.Hours) != 0 || len(insights.Projects) != 0 || len(insights.Bottlenecks) != 0 {
		t.Fatalf("small cohort leaked: %+v", insights)
	}

	insert("user-5")
	insights, err = s.OrganizationInsights(ctx, now.Add(-time.Minute), now.Add(time.Minute), 5)
	if err != nil {
		t.Fatalf("OrganizationInsights: %v", err)
	}
	if !insights.Available || insights.ActiveUsers != 5 || len(insights.Models) != 1 || len(insights.Tools) != 1 || len(insights.Hours) != 1 || len(insights.Projects) != 1 || len(insights.Bottlenecks) != 1 {
		t.Fatalf("insights = %+v", insights)
	}
	if insights.Models[0].Model != "sonnet" || insights.Models[0].ContributorCount != 5 {
		t.Fatalf("model = %+v", insights.Models[0])
	}
	if insights.Tools[0].ToolName != "Bash" || insights.Tools[0].ContributorCount != 5 || insights.Tools[0].UseCount != 5 {
		t.Fatalf("tool = %+v", insights.Tools[0])
	}
	if insights.Hours[0].Hour != now.Hour() || insights.Hours[0].ContributorCount != 5 || insights.Hours[0].SessionCount != 5 {
		t.Fatalf("hour = %+v", insights.Hours[0])
	}
	if insights.Projects[0].ProjectHash != "shared-project" || insights.Projects[0].ContributorCount != 5 || insights.Projects[0].SessionCount != 5 {
		t.Fatalf("project = %+v", insights.Projects[0])
	}
	if insights.Bottlenecks[0].ToolName != "Bash" || insights.Bottlenecks[0].ContributorCount != 5 || insights.Bottlenecks[0].FailCount != 1 {
		t.Fatalf("bottleneck = %+v", insights.Bottlenecks[0])
	}
}

// The same invariant the weekly report pins (see
// TestPgStore_WeeklyInsightsTypedTurnCountCoversTasks): the denominator cannot be
// smaller than the counts it divides.
//
// Both surfaces render Tasks as a percentage of TypedTurnCount, and both used to
// read that denominator from task_segment_facts -- a narrower population written
// by a periodic reconciler. Fixing one and not the other would leave the two
// screens disagreeing about the same week, so the invariant is fixed on both.
func TestOrganizationInsightsTypedTurnCountCoversTasks(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)

	// Five users so the cohort clears the privacy threshold, each with prompts
	// spread far enough apart that the segment builder would split them -- and
	// nothing here runs it, which is the point.
	for i := 1; i <= 5; i++ {
		user := fmt.Sprintf("user-%d", i)
		records := []*SessionRecord{}
		for j, text := range []string{"implement the endpoint", "write documentation", "add a test"} {
			records = append(records, &SessionRecord{
				Ts:        now.Add(time.Duration(j) * time.Hour),
				SessionID: "s-" + user,
				UUID:      fmt.Sprintf("p-%s-%d", user, j),
				// Nudged apart so no two rows collide on the storage key.
				RecordType: "user", UserID: user, ProfileEmail: user + "@example.com",
				ProjectHash: "shared-project",
				Raw:         []byte(`{"message":{"role":"user","content":"` + text + `"}}`),
			})
		}
		if err := s.InsertSessionRecords(ctx, records); err != nil {
			t.Fatalf("insert %s: %v", user, err)
		}
	}

	// Reconciler lag, made explicit. Segment facts are written on ingest here, so
	// without this the fixture never exercises the case the invariant exists for:
	// a prompt that Tasks counts and the derived table has not caught up to.
	if _, err := s.pool.Exec(ctx, `DELETE FROM task_segment_facts`); err != nil {
		t.Fatalf("clear derived facts: %v", err)
	}

	insights, err := s.OrganizationInsights(ctx, now.Add(-time.Minute), now.Add(4*time.Hour), 5)
	if err != nil {
		t.Fatalf("OrganizationInsights: %v", err)
	}

	var classified int64
	for _, task := range insights.Tasks {
		classified += task.PromptCount
	}
	if classified == 0 {
		t.Fatalf("no classified prompts in %+v -- the fixture stopped exercising the invariant", insights.Tasks)
	}
	if insights.TypedTurnCount < classified {
		t.Fatalf("typed_turn_count = %d, classified = %d: a denominator smaller than what it divides renders over 100%%",
			insights.TypedTurnCount, classified)
	}
}

// Codex sends no tool_result event: its calls arrive as the codex.tool.call delta
// metric (#698). They count toward tools and bottlenecks under the same
// distinct-user threshold Claude's tools are held to.
func TestOrganizationInsightsToolsIncludeCodexToolCallMetric(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	two, one := int64(2), int64(1)
	for i := 1; i <= 5; i++ {
		user := fmt.Sprintf("user-%d", i)
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
			Ts: now, SessionID: "session-" + user, UUID: "prompt-" + user, RecordType: "user", UserID: user, ProfileEmail: user + "@example.com",
			Agent: "codex", Raw: []byte(`{"message":{"role":"user","content":"implement the endpoint"}}`),
		}}); err != nil {
			t.Fatalf("insert session record %s: %v", user, err)
		}
		metric := func(tool, success string, v *int64) *OtelMetric {
			return &OtelMetric{Ts: now, MetricName: "codex.tool.call", UserID: user, ProfileEmail: user + "@example.com",
				Agent: "codex", ValueInt: v, Dimensions: map[string]interface{}{"tool": tool, "success": success}}
		}
		metrics := []*OtelMetric{metric("shell", "true", &two)}
		if i == 1 {
			metrics = append(metrics, metric("shell", "false", &one))
		}
		if i <= 4 { // four users: below the threshold
			metrics = append(metrics, metric("apply_patch", "false", &one))
		}
		if err := s.InsertMetrics(ctx, metrics); err != nil {
			t.Fatalf("insert metrics %s: %v", user, err)
		}
	}

	insights, err := s.OrganizationInsights(ctx, now.Add(-time.Minute), now.Add(time.Minute), 5)
	if err != nil {
		t.Fatalf("OrganizationInsights: %v", err)
	}
	wantTool := OrganizationToolInsight{ToolName: "shell", ContributorCount: 5, UseCount: 11, SuccessCount: 10, FailCount: 1}
	if len(insights.Tools) != 1 || insights.Tools[0] != wantTool {
		t.Fatalf("tools = %+v, want [%+v]", insights.Tools, wantTool)
	}
	wantBottleneck := OrganizationBottleneckInsight{ToolName: "shell", ContributorCount: 5, UseCount: 11, FailCount: 1}
	if len(insights.Bottlenecks) != 1 || insights.Bottlenecks[0] != wantBottleneck {
		t.Fatalf("bottlenecks = %+v, want [%+v]", insights.Bottlenecks, wantBottleneck)
	}
}

// Codex reports through metrics, which carry the same identity columns events
// do. Someone whose only telemetry in the window is a metric still used an agent
// that window, and counts toward the cohort once, like everyone else.
func TestOrganizationInsightsActiveUsersCountMetricOnlyIdentities(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := 1; i <= 4; i++ {
		user := fmt.Sprintf("user-%d", i)
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: now, SessionID: "session-" + user, UUID: "prompt-" + user,
			RecordType: "user", UserID: user, ProfileEmail: user + "@example.com"}}); err != nil {
			t.Fatalf("insert session record %s: %v", user, err)
		}
	}
	one := int64(1)
	if err := s.InsertMetrics(ctx, []*OtelMetric{
		{Ts: now, MetricName: "codex.tool.call", UserID: "user-5", ProfileEmail: "user-5@example.com", Agent: "codex", ValueInt: &one},
		{Ts: now, MetricName: "codex.tool.call", UserID: "user-1", ProfileEmail: "user-1@example.com", Agent: "codex", ValueInt: &one},
	}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	insights, err := s.OrganizationInsights(ctx, now.Add(-time.Minute), now.Add(time.Minute), 5)
	if err != nil {
		t.Fatalf("OrganizationInsights: %v", err)
	}
	if !insights.Available || insights.ActiveUsers != 5 {
		t.Fatalf("available=%v active=%d, want the metric-only identity counted once for 5", insights.Available, insights.ActiveUsers)
	}
}

// A tool call's contributor is its first non-empty identity -- user_id, then
// profile_email, then login_email -- on both arms, and a call with none is not
// counted at all. The window is [since, until).
func TestOrganizationInsightsToolsIdentityFallbackAndWindow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	since, until := now.Add(-time.Minute), now.Add(time.Minute)
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "tool_result", ProfileEmail: "p1@example.com", ToolName: "Read", ToolSuccess: ptrBool(true)},
		{Ts: now, EventName: "tool_result", UserID: "u2", ToolName: "Read", ToolSuccess: ptrBool(false)},
		{Ts: since, EventName: "tool_result", UserID: "u2", ToolName: "Read", ToolSuccess: ptrBool(true)},
		{Ts: until, EventName: "tool_result", UserID: "u2", ToolName: "Read", ToolSuccess: ptrBool(false)},
		{Ts: now, EventName: "tool_result", ToolName: "Read", ToolSuccess: ptrBool(false)},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	two, nine := int64(2), int64(9)
	codex := func(ts time.Time, login string, v *int64) *OtelMetric {
		return &OtelMetric{Ts: ts, MetricName: "codex.tool.call", LoginEmail: login, Agent: "codex", ValueInt: v,
			Dimensions: map[string]interface{}{"tool": "Read", "success": "true"}}
	}
	if err := s.InsertMetrics(ctx, []*OtelMetric{
		codex(now, "l3@example.com", &two),
		codex(until, "l3@example.com", &nine),
		codex(now, "", &nine),
	}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	insights, err := s.OrganizationInsights(ctx, since, until, 3)
	if err != nil {
		t.Fatalf("OrganizationInsights: %v", err)
	}
	wantTool := OrganizationToolInsight{ToolName: "Read", ContributorCount: 3, UseCount: 5, SuccessCount: 4, FailCount: 1}
	if len(insights.Tools) != 1 || insights.Tools[0] != wantTool {
		t.Fatalf("tools = %+v, want [%+v]", insights.Tools, wantTool)
	}
	wantBottleneck := OrganizationBottleneckInsight{ToolName: "Read", ContributorCount: 3, UseCount: 5, FailCount: 1}
	if len(insights.Bottlenecks) != 1 || insights.Bottlenecks[0] != wantBottleneck {
		t.Fatalf("bottlenecks = %+v, want [%+v]", insights.Bottlenecks, wantBottleneck)
	}
}
