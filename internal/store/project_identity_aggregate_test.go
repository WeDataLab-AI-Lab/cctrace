package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestWeeklyInsights_GroupsByRepositoryIdentityAndRetainsMemberHashes(t *testing.T) {
	s := acquireTestStore(t)
	seedIssue382AggregateFixture(t, s)
	ctx := context.Background()
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)

	insights, err := s.WeeklyInsights(ctx, since, until, "qa@example.test", "", "UTC")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}

	// The subpath checkout joins the root. It used to be its own line (#257), and
	// on screen that split claude-plugins from claude-plugins/staging/ into two
	// projects with separate token totals -- a repository is the unit people think
	// in, so the prefix left the grouping key.
	root := findWeeklyProject(t, insights.Projects, []string{"h-main", "h-subpath", "h-worktree"})
	if root.SessionCount != 3 || root.TotalTokens != 350 {
		t.Fatalf("root aggregate = %+v, want sessions=3 tokens=350", root)
	}
	// Member hashes still travel with the row: merging the identity must not lose
	// which checkouts it was built from (#382).
	if len(root.ProjectHashes) != 3 {
		t.Fatalf("root member hashes = %v, want all three checkouts", root.ProjectHashes)
	}
	if len(insights.Projects) != 2 {
		t.Fatalf("projects = %+v, want the repository and the other identity only", insights.Projects)
	}
}

func TestOrganizationInsights_GroupsByRepositoryIdentityAfterPrivacyFilter(t *testing.T) {
	s := acquireTestStore(t)
	seedIssue382AggregateFixture(t, s)
	ctx := context.Background()
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)

	insights, err := s.OrganizationInsights(ctx, since, until, 2)
	if err != nil {
		t.Fatalf("OrganizationInsights: %v", err)
	}
	if !insights.Available || insights.ActiveUsers != 2 {
		t.Fatalf("availability = available:%v active:%d, want true,2", insights.Available, insights.ActiveUsers)
	}
	if len(insights.Projects) != 1 {
		t.Fatalf("projects = %+v, want only the two-contributor root identity", insights.Projects)
	}
	root := insights.Projects[0]
	// Same key as the weekly report: the subpath checkout is part of the
	// repository, not a project of its own.
	if root.ContributorCount != 2 || root.SessionCount != 3 || root.TotalTokens != 350 {
		t.Fatalf("root aggregate = %+v, want contributors=2 sessions=3 tokens=350", root)
	}
	if len(root.ProjectHashes) != 3 {
		t.Fatalf("root member hashes = %v, want all three checkouts", root.ProjectHashes)
	}
}

func TestWeeklyInsights_DoesNotMergeLowConfidenceOrConflictingIdentities(t *testing.T) {
	s := acquireTestStore(t)
	seedIssue382AggregateFixture(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 5, 0, 0, 0, time.UTC)
	intPtr := func(v int) *int { return &v }
	rows := []struct {
		hash, id, repoName string
	}{
		{"h-blank-a", "", ""},
		{"h-blank-b", "", ""},
		{"h-local-a", "local:issue-382:deadbeef", "local-a"},
		{"h-local-b", "local:issue-382:deadbeef", "local-b"},
		{"h-conflict-a", "example.com/org/a", "a"},
		{"h-conflict-b", "example.com/org/b", "b"},
	}
	for i, row := range rows {
		if err := s.UpsertProject(ctx, "claude", row.hash, row.hash, "", row.id, row.repoName, "", now); err != nil {
			t.Fatalf("UpsertProject(%s): %v", row.hash, err)
		}
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
			Ts: now.Add(time.Duration(i) * time.Minute), SessionID: "s-" + row.hash, UUID: "u-" + row.hash,
			RecordType: "assistant", UserID: "userA", ProfileEmail: "qa@example.test", ProjectHash: row.hash,
			InputTokens: intPtr(1), OutputTokens: intPtr(1), Raw: json.RawMessage(`{"message":{"role":"assistant"}}`),
		}}); err != nil {
			t.Fatalf("InsertSessionRecords(%s): %v", row.hash, err)
		}
	}

	insights, err := s.WeeklyInsights(ctx, now.Add(-time.Minute), now.Add(time.Hour), "qa@example.test", "", "UTC")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	for _, row := range insights.Projects {
		if len(row.ProjectHashes) > 1 {
			t.Fatalf("low-confidence/conflicting grouping merged members: %+v", row)
		}
	}
	for _, want := range []string{"h-blank-a", "h-blank-b", "h-local-a", "h-local-b", "h-conflict-a", "h-conflict-b"} {
		findWeeklyProject(t, insights.Projects, []string{want})
	}
}

func TestWeeklyInsights_JoinsProjectMetadataWithinAgentScope(t *testing.T) {
	s := acquireTestStore(t)
	seedIssue382AggregateFixture(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)
	for _, project := range []struct {
		agent, id, name string
	}{
		{"claude", "example.com/org/issue-382", "issue-382"},
		{"codex", "example.com/org/other", "other"},
	} {
		if err := s.UpsertProject(ctx, project.agent, "h-agent-shared", project.name, "", project.id, project.name, "", now); err != nil {
			t.Fatalf("UpsertProject(%s): %v", project.agent, err)
		}
	}
	for _, agent := range []string{"claude", "codex"} {
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{
			{Ts: now, SessionID: "s-agent-" + agent, UUID: "u-agent-" + agent, RecordType: "assistant", Agent: agent, UserID: "userA", ProfileEmail: "qa@example.test", ProjectHash: "h-agent-shared", InputTokens: ptrInt(2), OutputTokens: ptrInt(3), Raw: json.RawMessage(`{"message":{"role":"assistant"}}`)},
		}); err != nil {
			t.Fatalf("InsertSessionRecords(%s): %v", agent, err)
		}
	}

	insights, err := s.WeeklyInsights(ctx, now.Add(-time.Minute), now.Add(time.Hour), "qa@example.test", "", "UTC")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	seen := map[string]bool{}
	for _, row := range insights.Projects {
		if len(row.ProjectHashes) == 1 && row.ProjectHashes[0] == "h-agent-shared" {
			seen[row.ProjectName] = true
			if row.SessionCount != 1 || row.TotalTokens != 5 {
				t.Fatalf("agent-scoped row = %+v, want one session and five tokens", row)
			}
		}
	}
	if !seen["issue-382"] || !seen["other"] {
		t.Fatalf("agent metadata crossed or rows missing: %+v", seen)
	}
}

func seedIssue382AggregateFixture(t *testing.T, s *PgStore) {
	t.Helper()
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	remote := "https://example.com/org/issue-382.git"
	projects := []struct {
		hash, name, id, repoName, subpath string
	}{
		{"h-main", "issue-382", "example.com/org/issue-382", "issue-382", ""},
		{"h-worktree", "repo-worktree", "example.com/org/issue-382", "issue-382", ""},
		{"h-subpath", "store", "example.com/org/issue-382", "issue-382", "internal/store/"},
		{"h-other", "other", "example.com/org/other", "other", ""},
	}
	for _, p := range projects {
		if err := s.UpsertProject(ctx, "claude", p.hash, p.name, remote, p.id, p.repoName, p.subpath, now); err != nil {
			t.Fatalf("UpsertProject(%s): %v", p.hash, err)
		}
	}
	intPtr := func(v int) *int { return &v }
	records := []*SessionRecord{
		{Ts: now, SessionID: "s-main", UUID: "u-main", RecordType: "assistant", UserID: "userA", ProfileEmail: "qa@example.test", ProjectHash: "h-main", InputTokens: intPtr(40), OutputTokens: intPtr(60), Raw: json.RawMessage(`{"message":{"role":"assistant"}}`)},
		{Ts: now.Add(24 * time.Hour), SessionID: "s-worktree", UUID: "u-worktree", RecordType: "assistant", UserID: "userB", ProfileEmail: "qa@example.test", ProjectHash: "h-worktree", InputTokens: intPtr(80), OutputTokens: intPtr(120), Raw: json.RawMessage(`{"message":{"role":"assistant"}}`)},
		{Ts: now.Add(48 * time.Hour), SessionID: "s-subpath", UUID: "u-subpath", RecordType: "assistant", UserID: "userA", ProfileEmail: "qa@example.test", ProjectHash: "h-subpath", InputTokens: intPtr(20), OutputTokens: intPtr(30), Raw: json.RawMessage(`{"message":{"role":"assistant"}}`)},
		{Ts: now.Add(72 * time.Hour), SessionID: "s-other", UUID: "u-other", RecordType: "assistant", UserID: "userA", ProfileEmail: "qa@example.test", ProjectHash: "h-other", InputTokens: intPtr(10), OutputTokens: intPtr(15), Raw: json.RawMessage(`{"message":{"role":"assistant"}}`)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
}

func findWeeklyProject(t *testing.T, rows []WeeklyInsightProject, hashes []string) WeeklyInsightProject {
	t.Helper()
	for _, row := range rows {
		if len(row.ProjectHashes) != len(hashes) {
			continue
		}
		matched := true
		for i := range hashes {
			if row.ProjectHashes[i] != hashes[i] {
				matched = false
				break
			}
		}
		if matched {
			return row
		}
	}
	t.Fatalf("project hashes %v not found in %+v", hashes, rows)
	return WeeklyInsightProject{}
}

// A session started inside a repository folder carries a non-empty
// `git rev-parse --show-prefix`, and that prefix used to sit in the grouping key
// (#257). The effect on screen was that claude-plugins and claude-plugins/staging/
// appeared as two projects with separate token totals. A repository is the unit
// people think in.
func TestWeeklyInsights_GroupsSubdirectorySessionsUnderTheirRepository(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)

	for _, p := range []struct{ hash, name, subpath string }{
		{"h-root", "plugins", ""},
		{"h-staging", "staging", "staging/"},
		{"h-pkg", "humanize", "plugins/humanize/"},
	} {
		if err := s.UpsertProject(ctx, "claude", p.hash, p.name, "",
			"example.com/org/plugins", "plugins", p.subpath, now); err != nil {
			t.Fatalf("UpsertProject(%s): %v", p.hash, err)
		}
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
			Ts: now, SessionID: "s-" + p.hash, UUID: "u-" + p.hash, RecordType: "assistant",
			UserID: "u1", ProjectHash: p.hash, InputTokens: ptrInt(10), OutputTokens: ptrInt(5),
			Raw: json.RawMessage(`{"message":{"role":"assistant"}}`),
		}}); err != nil {
			t.Fatalf("InsertSessionRecords(%s): %v", p.hash, err)
		}
	}

	insights, err := s.WeeklyInsights(ctx, now.Add(-time.Hour), now.Add(time.Hour), "", "u1", "UTC")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	if len(insights.Projects) != 1 {
		t.Fatalf("projects = %d, want 1 (%+v)", len(insights.Projects), insights.Projects)
	}
	row := insights.Projects[0]
	if len(row.ProjectHashes) != 3 {
		t.Fatalf("member hashes = %v, want all three checkouts", row.ProjectHashes)
	}
	// And the totals follow: three sessions of 15 tokens each under one line.
	if row.SessionCount != 3 || row.TotalTokens != 45 {
		t.Fatalf("row = %+v, want 3 sessions and 45 tokens", row)
	}
}

// prod carried 17 repositories with two names each, because the name is stored
// beside the id instead of derived from it. One of the two was the worktree's own
// directory, so a repository displayed under its worktree codename once MAX()
// over the group picked whichever string sorted last (#691).
func TestWeeklyInsights_NamesARepositoryFromItsIDNotItsWorktreeDirectory(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)

	// Two worktrees of one repository. The second carries the wrong stored name --
	// the shape the production rows are in.
	for _, p := range []struct{ hash, storedName string }{
		{"h-main", "engine"},
		// The worktree's own directory name, which is the shape the production
		// rows are in.
		{"h-worktree", "seaslug"},
	} {
		if err := s.UpsertProject(ctx, "claude", p.hash, p.storedName, "",
			"example.com/org/engine", p.storedName, "", now); err != nil {
			t.Fatalf("UpsertProject(%s): %v", p.hash, err)
		}
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
			Ts: now, SessionID: "s-" + p.hash, UUID: "u-" + p.hash, RecordType: "assistant",
			UserID: "u1", ProjectHash: p.hash, InputTokens: ptrInt(1), OutputTokens: ptrInt(1),
			Raw: json.RawMessage(`{"message":{"role":"assistant"}}`),
		}}); err != nil {
			t.Fatalf("InsertSessionRecords(%s): %v", p.hash, err)
		}
	}

	insights, err := s.WeeklyInsights(ctx, now.Add(-time.Hour), now.Add(time.Hour), "", "u1", "UTC")
	if err != nil {
		t.Fatalf("WeeklyInsights: %v", err)
	}
	if len(insights.Projects) != 1 {
		t.Fatalf("projects = %d, want the two worktrees merged", len(insights.Projects))
	}
	if got := insights.Projects[0].ProjectName; got != "engine" {
		t.Fatalf("project_name = %q, want engine -- \"seaslug\" is a worktree directory", got)
	}
}
