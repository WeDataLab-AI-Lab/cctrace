package store

import (
	"strings"
	"testing"
)

func TestBuildProjectRuleWhereIgnoresAllStatusAndSearchesTextFields(t *testing.T) {
	_, where, args := buildProjectRuleWhere(ProjectRuleFilter{
		Agent:  "claude",
		Status: "all",
		Query:  "rules",
	})

	if !strings.Contains(where, "pr.agent = $1") {
		t.Fatalf("where missing agent condition: %q", where)
	}
	if strings.Contains(where, "current_status") {
		t.Fatalf("status=all must not filter current_status: %q", where)
	}
	if !strings.Contains(where, "pr.rule_path ILIKE $2") ||
		!strings.Contains(where, "pr.title ILIKE $2") ||
		!strings.Contains(where, "pr.repository_name ILIKE $2") {
		t.Fatalf("query condition should search path/title/repository_name with one placeholder: %q", where)
	}
	if len(args) != 2 || args[0] != "claude" || args[1] != "%rules%" {
		t.Fatalf("args = %#v, want [claude %%rules%%]", args)
	}
}

func TestBuildProjectRuleWhereUserIDTakesPrecedenceOverProfileEmail(t *testing.T) {
	cte, where, args := buildProjectRuleWhere(ProjectRuleFilter{
		UserID:       "otel-user-1",
		ProfileEmail: "login@example.com",
	})

	if !strings.Contains(cte, "user_id = $1") {
		t.Fatalf("cte missing user_id visibility condition: %q", cte)
	}
	if strings.Contains(cte, "profile_email") {
		t.Fatalf("profile_email condition must not be used when user_id is set: %q", cte)
	}
	if !strings.Contains(where, "o.agent = pr.agent") {
		t.Fatalf("where missing owned-CTE semijoin: %q", where)
	}
	if len(args) != 1 || args[0] != "otel-user-1" {
		t.Fatalf("args = %#v, want [otel-user-1]", args)
	}
}

// The whole rewrite hinges on positional placeholders: the owned CTE is emitted
// first textually but its arg is appended last. Guard the case where other filters
// precede the access filter, so the CTE must reference $3 (not $1) and args[2]==UserID.
func TestBuildProjectRuleWhereMultiFilterPlaceholderNumbering(t *testing.T) {
	cte, where, args := buildProjectRuleWhere(ProjectRuleFilter{
		Agent:  "claude",
		Query:  "x",
		UserID: "u1",
	})

	if !strings.Contains(cte, "user_id = $3") {
		t.Fatalf("cte should reference $3 for user_id (agent=$1, query=$2): %q", cte)
	}
	if !strings.Contains(where, "pr.agent = $1") {
		t.Fatalf("where missing agent = $1: %q", where)
	}
	if !strings.Contains(where, "$2") {
		t.Fatalf("where missing query ILIKE $2: %q", where)
	}
	if len(args) != 3 || args[0] != "claude" || args[1] != "%x%" || args[2] != "u1" {
		t.Fatalf("args = %#v, want [claude %%x%% u1]", args)
	}
}

// The ProfileEmail-only branch (UserID empty) must scope the owned CTE on
// profile_email, bound to $1.
func TestBuildProjectRuleWhereProfileEmailBranch(t *testing.T) {
	cte, _, args := buildProjectRuleWhere(ProjectRuleFilter{
		ProfileEmail: "x@y",
	})

	if !strings.Contains(cte, "profile_email = $1") {
		t.Fatalf("cte missing profile_email = $1: %q", cte)
	}
	if strings.Contains(cte, "user_id =") {
		t.Fatalf("cte must not scope on user_id when only ProfileEmail is set: %q", cte)
	}
	if len(args) != 1 || args[0] != "x@y" {
		t.Fatalf("args = %#v, want [x@y]", args)
	}
}
