package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// seedRuleOwnership inserts a session_record so the rule ingest ownership gate
// recognizes the identity as having history for the repository.
func seedRuleOwnership(t *testing.T, s *PgStore, agent, userID, profileEmail, repositoryID, projectHash string) {
	t.Helper()
	if err := s.InsertSessionRecords(context.Background(), []*SessionRecord{{
		Ts:           time.Now().UTC().Truncate(time.Millisecond),
		SessionID:    "seed-" + agent + "-" + projectHash,
		RecordType:   "user",
		Agent:        agent,
		UserID:       userID,
		ProfileEmail: profileEmail,
		RepositoryID: repositoryID,
		ProjectHash:  projectHash,
	}}); err != nil {
		t.Fatalf("seed session record: %v", err)
	}
}

func TestMigration_ProjectRulesTables(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	checks := []struct {
		table  string
		column string
	}{
		{"project_rules", "agent"},
		{"project_rules", "repository_key"},
		{"project_rules", "rule_path"},
		{"project_rules", "current_status"},
		{"project_rule_versions", "rule_id"},
		{"project_rule_versions", "content_hash"},
		{"project_rule_versions", "change_reason"},
		{"project_rule_versions", "commit_sha"},
		{"project_rule_versions", "branch"},
		{"project_rule_versions", "applies_to"},
		{"project_rule_comments", "rule_id"},
		{"project_rule_comments", "version_id"},
		{"project_rule_comments", "comment_type"},
	}

	for _, c := range checks {
		var exists bool
		err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = $1 AND column_name = $2
			)`, c.table, c.column,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("query %s.%s: %v", c.table, c.column, err)
		}
		if !exists {
			t.Errorf("column %s.%s does not exist", c.table, c.column)
		}
	}
}

func TestProjectRules_UniqueIdentity(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	insertRule := `INSERT INTO project_rules
		(agent, project_hash, repository_id, repository_key, repository_name, rule_path, rule_kind)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	if _, err := s.pool.Exec(ctx, insertRule,
		"codex", "proj-a", "github.com/org/repo", "github.com/org/repo", "repo", "AGENTS.md", "agents"); err != nil {
		t.Fatalf("insert first rule: %v", err)
	}
	if _, err := s.pool.Exec(ctx, insertRule,
		"claude", "proj-a", "github.com/org/repo", "github.com/org/repo", "repo", "AGENTS.md", "agents"); err != nil {
		t.Fatalf("same path for different agent should be allowed: %v", err)
	}
	if _, err := s.pool.Exec(ctx, insertRule,
		"codex", "proj-b", "github.com/org/repo", "github.com/org/repo", "repo", "AGENTS.md", "agents"); err == nil {
		t.Fatal("expected duplicate identity error for same agent, repository_key, rule_path")
	}
}

func TestProjectRuleVersions_DeduplicateByHash(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	var ruleID int64
	err := s.pool.QueryRow(ctx, `INSERT INTO project_rules
		(agent, project_hash, repository_key, rule_path, rule_kind)
		VALUES ('codex', 'proj-a', 'github.com/org/repo', 'AGENTS.md', 'agents')
		RETURNING id`).Scan(&ruleID)
	if err != nil {
		t.Fatalf("insert rule: %v", err)
	}

	insertVersion := `INSERT INTO project_rule_versions
		(rule_id, version_number, content_hash, content, size_bytes)
		VALUES ($1, $2, $3, $4, $5)`
	if _, err := s.pool.Exec(ctx, insertVersion, ruleID, 1, "sha256-a", "# AGENTS", 8); err != nil {
		t.Fatalf("insert version: %v", err)
	}
	if _, err := s.pool.Exec(ctx, insertVersion, ruleID, 2, "sha256-a", "# AGENTS", 8); err == nil {
		t.Fatal("expected duplicate content_hash error for same rule")
	}
}

func TestProjectRulesIngest_ListAndDetail(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedRuleOwnership(t, s, "codex", "uid-dev", "dev@example.com", "github.com/org/repo", "proj-a")

	req := &ProjectRuleIngestRequest{
		ProfileEmail:   "dev@example.com",
		UserID:         "uid-dev",
		Agent:          "codex",
		ProjectHash:    "proj-a",
		ProjectName:    "repo",
		RepositoryID:   "github.com/org/repo",
		RepositoryName: "repo",
		CommitSHA:      "abc123",
		Branch:         "main",
		Rules: []*ProjectRuleSnapshot{{
			RulePath:  "AGENTS.md",
			RuleKind:  "agents",
			RuleScope: "repository",
			Title:     "Repo Rules",
			Status:    "active",
			Content:   "# Rules\n",
			SizeBytes: 8,
			AppliesTo: []string{"."},
		}},
	}

	resp, err := s.IngestProjectRules(ctx, req)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if resp.InsertedRules != 1 || resp.InsertedVersions != 1 {
		t.Fatalf("first ingest response = %+v, want inserted rule/version", resp)
	}

	resp, err = s.IngestProjectRules(ctx, req)
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if resp.InsertedVersions != 0 || resp.UnchangedRules != 1 {
		t.Fatalf("second ingest response = %+v, want unchanged without new version", resp)
	}

	req.CommitSHA = "def456"
	req.Rules[0].Content = "# Rules\n\nMore\n"
	req.Rules[0].SizeBytes = len(req.Rules[0].Content)
	resp, err = s.IngestProjectRules(ctx, req)
	if err != nil {
		t.Fatalf("third ingest: %v", err)
	}
	if resp.InsertedVersions != 1 {
		t.Fatalf("third ingest response = %+v, want one new version", resp)
	}

	list, err := s.ListProjectRules(ctx, ProjectRuleFilter{Agent: "codex", RepositoryKey: "github.com/org/repo"})
	if err != nil {
		t.Fatalf("ListProjectRules: %v", err)
	}
	if list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("list = %+v, want one item", list)
	}
	item := list.Items[0]
	if item.RulePath != "AGENTS.md" || item.VersionCount != 2 || item.CurrentStatus != "active" {
		t.Fatalf("list item = %+v, want AGENTS.md with two versions", item)
	}

	detail, err := s.GetProjectRuleDetail(ctx, item.ID, item.CurrentVersionID)
	if err != nil {
		t.Fatalf("GetProjectRuleDetail: %v", err)
	}
	if detail.Rule.ID != item.ID || len(detail.Versions) != 2 {
		t.Fatalf("detail = %+v, want two versions", detail)
	}
	if detail.Versions[0].VersionNumber != 2 || detail.Versions[0].CommitSHA != "def456" {
		t.Fatalf("latest version = %+v, want version 2 at def456", detail.Versions[0])
	}
	if detail.Versions[0].Content == "" || detail.Versions[1].Content != "" {
		t.Fatalf("version contents = [%q, %q], want only selected current content", detail.Versions[0].Content, detail.Versions[1].Content)
	}

	historical, err := s.GetProjectRuleDetail(ctx, item.ID, detail.Versions[1].ID)
	if err != nil {
		t.Fatalf("GetProjectRuleDetail historical: %v", err)
	}
	if historical.Versions[0].Content != "" || historical.Versions[1].Content == "" {
		t.Fatalf("historical contents = [%q, %q], want only selected historical content", historical.Versions[0].Content, historical.Versions[1].Content)
	}
}

func TestProjectRulesCommentAndChangeReason(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedRuleOwnership(t, s, "claude", "uid-dev", "dev@example.com", "local:repo", "proj-a")

	resp, err := s.IngestProjectRules(ctx, &ProjectRuleIngestRequest{
		ProfileEmail:  "dev@example.com",
		UserID:        "uid-dev",
		Agent:         "claude",
		ProjectHash:   "proj-a",
		RepositoryKey: "local:repo",
		Rules: []*ProjectRuleSnapshot{{
			RulePath: "CLAUDE.md",
			RuleKind: "claude",
			Status:   "active",
			Content:  "# Claude\n",
		}},
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if resp.InsertedVersions != 1 {
		t.Fatalf("ingest response = %+v, want one version", resp)
	}

	list, err := s.ListProjectRules(ctx, ProjectRuleFilter{Agent: "claude", RepositoryKey: "local:repo"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ruleID := list.Items[0].ID
	detail, err := s.GetProjectRuleDetail(ctx, ruleID, 0)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	versionID := detail.Versions[0].ID

	comment, err := s.CreateProjectRuleComment(ctx, ruleID, &CreateProjectRuleCommentRequest{
		VersionID: versionID,
		Body:      "needs cleanup",
	}, "dev@example.com", "uid-dev")
	if err != nil {
		t.Fatalf("CreateProjectRuleComment: %v", err)
	}
	if comment.RuleID != ruleID || comment.VersionID != versionID || comment.Body != "needs cleanup" {
		t.Fatalf("comment = %+v, want attached comment", comment)
	}

	version, err := s.UpdateProjectRuleChangeReason(ctx, ruleID, versionID, "documented repeated mistake", "dev@example.com", "uid-dev")
	if err != nil {
		t.Fatalf("UpdateProjectRuleChangeReason: %v", err)
	}
	if version.ChangeReason != "documented repeated mistake" {
		t.Fatalf("change reason = %q", version.ChangeReason)
	}

	detail, err = s.GetProjectRuleDetail(ctx, ruleID, 0)
	if err != nil {
		t.Fatalf("detail after update: %v", err)
	}
	if len(detail.Comments) != 2 {
		t.Fatalf("comments = %d, want comment + change_reason", len(detail.Comments))
	}
}

func TestProjectRulesIngest_OwnershipGate(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// uid-attacker has session history for their own repo only.
	seedRuleOwnership(t, s, "codex", "uid-attacker", "attacker@example.com", "github.com/org/own", "proj-own")

	req := &ProjectRuleIngestRequest{
		ProfileEmail: "attacker@example.com",
		UserID:       "uid-attacker",
		Agent:        "codex",
		RepositoryID: "github.com/org/victim",
		Rules: []*ProjectRuleSnapshot{{
			RulePath: "AGENTS.md", RuleKind: "agents", Status: "active", Content: "# pwned\n",
		}},
	}
	if _, err := s.IngestProjectRules(ctx, req); !errors.Is(err, ErrProjectRuleAccessDenied) {
		t.Fatalf("ingest for unowned repo: err = %v, want ErrProjectRuleAccessDenied", err)
	}

	// Ingest with no identity is also denied.
	req.UserID, req.ProfileEmail = "", ""
	req.RepositoryID = "github.com/org/own"
	if _, err := s.IngestProjectRules(ctx, req); !errors.Is(err, ErrProjectRuleAccessDenied) {
		t.Fatalf("ingest without identity: err = %v, want ErrProjectRuleAccessDenied", err)
	}
}

func TestProjectRuleVisibleTo(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedRuleOwnership(t, s, "codex", "uid-dev", "dev@example.com", "github.com/org/repo", "proj-a")

	if _, err := s.IngestProjectRules(ctx, &ProjectRuleIngestRequest{
		ProfileEmail: "dev@example.com", UserID: "uid-dev", Agent: "codex",
		ProjectHash: "proj-a", RepositoryID: "github.com/org/repo",
		Rules: []*ProjectRuleSnapshot{{RulePath: "AGENTS.md", RuleKind: "agents", Status: "active", Content: "# Rules\n"}},
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	list, err := s.ListProjectRules(ctx, ProjectRuleFilter{Agent: "codex", RepositoryKey: "github.com/org/repo"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ruleID := list.Items[0].ID

	ok, err := s.ProjectRuleVisibleTo(ctx, ruleID, "uid-dev", "")
	if err != nil || !ok {
		t.Fatalf("owner visibility = %v (err %v), want true", ok, err)
	}
	ok, err = s.ProjectRuleVisibleTo(ctx, ruleID, "uid-other", "other@example.com")
	if err != nil || ok {
		t.Fatalf("stranger visibility = %v (err %v), want false", ok, err)
	}
	ok, err = s.ProjectRuleVisibleTo(ctx, ruleID, "", "")
	if err != nil || ok {
		t.Fatalf("empty identity visibility = %v (err %v), want false", ok, err)
	}
}
