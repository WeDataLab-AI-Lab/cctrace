package store

import (
	"reflect"
	"testing"
	"time"

	"cctrace/internal/gitctx"
)

func TestPlanProjectIdentityRepairClassifiesEvidenceWithoutGuessing(t *testing.T) {
	tests := []struct {
		name             string
		rows             []ProjectIdentityRepairRow
		wantCategory     ProjectIdentityRepairCategory
		wantReason       ProjectIdentityRepairReason
		wantRepositoryID string
		wantProposal     bool
	}{
		{
			name: "remote URL",
			rows: []ProjectIdentityRepairRow{{
				Agent: "claude", ProjectHash: "hash-url",
				GitRemoteURL: "https://git.example.test/team/alpha.git", RepoSubpath: "service/",
			}},
			wantCategory:     ProjectIdentityRepairRecoverable,
			wantReason:       ProjectIdentityRepairUniqueRemote,
			wantRepositoryID: "git.example.test/team/alpha",
			wantProposal:     true,
		},
		{
			name: "repository ID",
			rows: []ProjectIdentityRepairRow{{
				Agent: "claude", ProjectHash: "hash-id",
				RepositoryID: "git.example.test/team/alpha", RepositoryName: "alpha",
			}},
			wantCategory: ProjectIdentityRepairAlreadyCanonical,
			wantReason:   ProjectIdentityRepairAlreadyCanonicalReason,
		},
		{
			name: "equivalent URL and ID with a missing name",
			rows: []ProjectIdentityRepairRow{{
				Agent: "claude", ProjectHash: "hash-same",
				GitRemoteURL: "ssh://git@git.example.test/team/alpha.git",
				RepositoryID: "git.example.test/team/alpha",
			}},
			wantCategory:     ProjectIdentityRepairRecoverable,
			wantReason:       ProjectIdentityRepairUniqueRemote,
			wantRepositoryID: "git.example.test/team/alpha",
			wantProposal:     true,
		},
		{
			name: "stored ID in URL form",
			rows: []ProjectIdentityRepairRow{{
				Agent: "claude", ProjectHash: "hash-url-form",
				RepositoryID: "https://git.example.test/team/alpha.git", RepositoryName: "alpha",
			}},
			wantCategory:     ProjectIdentityRepairRecoverable,
			wantReason:       ProjectIdentityRepairUniqueRemote,
			wantRepositoryID: "git.example.test/team/alpha",
			wantProposal:     true,
		},
		{
			name: "worktree name stored beside a resolved ID",
			rows: []ProjectIdentityRepairRow{{
				Agent: "claude", ProjectHash: "hash-name-drift",
				RepositoryID: "git.example.test/team/alpha", RepositoryName: "alpha-worktree",
			}},
			wantCategory:     ProjectIdentityRepairRecoverable,
			wantReason:       ProjectIdentityRepairUniqueRemote,
			wantRepositoryID: "git.example.test/team/alpha",
			wantProposal:     true,
		},
		{
			name: "conflicting remote evidence",
			rows: []ProjectIdentityRepairRow{{
				Agent: "claude", ProjectHash: "hash-conflict",
				GitRemoteURL: "https://git.example.test/team/alpha.git",
				RepositoryID: "git.example.test/team/beta",
			}},
			wantCategory: ProjectIdentityRepairConflict,
			wantReason:   ProjectIdentityRepairConflictingRemote,
		},
		{
			name: "local only",
			rows: []ProjectIdentityRepairRow{{
				Agent: "claude", ProjectHash: "hash-local",
				RepositoryID: "local:alpha:0123456789abcdef",
			}},
			wantCategory: ProjectIdentityRepairLocal,
			wantReason:   ProjectIdentityRepairLocalOnly,
		},
		{
			name: "blank",
			rows: []ProjectIdentityRepairRow{{
				Agent: "claude", ProjectHash: "hash-blank", RepoSubpath: "",
			}},
			wantCategory: ProjectIdentityRepairBlank,
			wantReason:   ProjectIdentityRepairMissingRemote,
		},
		{
			name: "malformed remote URL",
			rows: []ProjectIdentityRepairRow{{
				Agent: "claude", ProjectHash: "hash-malformed", GitRemoteURL: "not-a-remote",
			}},
			wantCategory: ProjectIdentityRepairBlank,
			wantReason:   ProjectIdentityRepairInvalidRemote,
		},
		{
			name: "opaque non-local repository ID",
			rows: []ProjectIdentityRepairRow{{
				Agent: "claude", ProjectHash: "hash-opaque-id", RepositoryID: "private-repo", RepoSubpath: "service/",
			}},
			wantCategory:     ProjectIdentityRepairRecoverable,
			wantReason:       ProjectIdentityRepairUniqueRemote,
			wantRepositoryID: "private-repo",
			wantProposal:     true,
		},
		{
			name: "credential-bearing repository ID without a repository path",
			rows: []ProjectIdentityRepairRow{{
				Agent: "claude", ProjectHash: "hash-credential-id", RepositoryID: "https://token:phase3-secret@git.example.test",
			}},
			wantCategory: ProjectIdentityRepairBlank,
			wantReason:   ProjectIdentityRepairInvalidRepositoryID,
		},
		{
			name: "malformed credential-bearing remote URL",
			rows: []ProjectIdentityRepairRow{{
				Agent: "claude", ProjectHash: "hash-malformed-credential", GitRemoteURL: "https://user:bad%zz@git.example.test/team/repo.git", RepoSubpath: "service/",
			}},
			wantCategory:     ProjectIdentityRepairRecoverable,
			wantReason:       ProjectIdentityRepairUniqueRemote,
			wantRepositoryID: "git.example.test/team/repo",
			wantProposal:     true,
		},
		{
			name: "scheme-less credential-bearing repository ID",
			rows: []ProjectIdentityRepairRow{{
				Agent: "claude", ProjectHash: "hash-schemeless-credential-id", RepositoryID: "user:phase3-secret@git.example.test/team/repo",
			}},
			wantCategory: ProjectIdentityRepairBlank,
			wantReason:   ProjectIdentityRepairInvalidRepositoryID,
		},
		{
			name: "scheme-less credential-bearing remote URL",
			rows: []ProjectIdentityRepairRow{{
				Agent: "claude", ProjectHash: "hash-schemeless-credential-url", GitRemoteURL: "user:phase3-secret@git.example.test/team/repo",
			}},
			wantCategory: ProjectIdentityRepairBlank,
			wantReason:   ProjectIdentityRepairInvalidRemote,
		},
		{
			name: "valid scp remote URL",
			rows: []ProjectIdentityRepairRow{{
				Agent: "claude", ProjectHash: "hash-scp", GitRemoteURL: "git@git.example.test:team/repo.git", RepoSubpath: "service/",
			}},
			wantCategory:     ProjectIdentityRepairRecoverable,
			wantReason:       ProjectIdentityRepairUniqueRemote,
			wantRepositoryID: "git.example.test/team/repo",
			wantProposal:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanProjectIdentityRepair(tt.rows)
			if len(plan.Results) != 1 {
				t.Fatalf("result count = %d, want 1", len(plan.Results))
			}
			result := plan.Results[0]
			if result.Category != tt.wantCategory || result.Reason != tt.wantReason {
				t.Fatalf("classification = (%q, %q), want (%q, %q)", result.Category, result.Reason, tt.wantCategory, tt.wantReason)
			}
			if got := result.Proposal != nil; got != tt.wantProposal {
				t.Fatalf("proposal present = %v, want %v: %#v", got, tt.wantProposal, result.Proposal)
			}
			if tt.wantProposal {
				if result.Proposal.RepositoryID != tt.wantRepositoryID {
					t.Fatalf("repository ID = %q, want %q", result.Proposal.RepositoryID, tt.wantRepositoryID)
				}
				if want := gitctx.RepositoryNameFromID(tt.wantRepositoryID); result.Proposal.RepositoryName != want {
					t.Fatalf("repository name = %q, want %q derived from the ID", result.Proposal.RepositoryName, want)
				}
			} else if result.Proposal != nil {
				t.Fatalf("non-recoverable result has proposal: %#v", result.Proposal)
			}
		})
	}
}

func TestPlanProjectIdentityRepairKeepsExactScopeKeys(t *testing.T) {
	plan := PlanProjectIdentityRepair([]ProjectIdentityRepairRow{
		{Agent: "claude", ProjectHash: "same-hash", RepositoryID: "Repo-A"},
		{Agent: " claude", ProjectHash: "same-hash", RepositoryID: "repo-b"},
	})

	if len(plan.Results) != 2 || plan.Counts.Recoverable != 2 {
		t.Fatalf("exact scope keys were merged: %+v", plan)
	}
	if plan.Results[0].Agent != " claude" || plan.Results[1].Agent != "claude" {
		t.Fatalf("scope keys were normalized: %+v", plan.Results)
	}
}

// The repository is the unit on every screen (#382), so an unknown subpath no
// longer stands between a scope and its one remote identity.
func TestPlanProjectIdentityRepairRecoversUnknownSubpathWithoutHistory(t *testing.T) {
	plan := PlanProjectIdentityRepair([]ProjectIdentityRepairRow{{
		Agent:        "claude",
		ProjectHash:  "unknown-subpath",
		GitRemoteURL: "https://git.example.test/team/alpha.git",
	}})

	if len(plan.Results) != 1 {
		t.Fatalf("result count = %d, want 1", len(plan.Results))
	}
	result := plan.Results[0]
	if result.Category != ProjectIdentityRepairRecoverable || result.Reason != ProjectIdentityRepairUniqueRemote {
		t.Fatalf("classification = (%q, %q), want recoverable", result.Category, result.Reason)
	}
	want := &ProjectIdentityRepairProposal{RepositoryID: "git.example.test/team/alpha", RepositoryName: "alpha"}
	if !reflect.DeepEqual(result.Proposal, want) {
		t.Fatalf("proposal = %#v, want %#v", result.Proposal, want)
	}
}

func TestPlanProjectIdentityRepairTreatsOpaqueIDCaseDifferencesAsConflict(t *testing.T) {
	plan := PlanProjectIdentityRepair([]ProjectIdentityRepairRow{
		{Agent: "claude", ProjectHash: "same-hash", RepositoryID: "Repo-A"},
		{Agent: "claude", ProjectHash: "same-hash", RepositoryID: "repo-a"},
	})

	if len(plan.Results) != 1 || plan.Results[0].Category != ProjectIdentityRepairConflict {
		t.Fatalf("case-distinct opaque IDs did not conflict: %+v", plan)
	}
}

func TestPlanProjectIdentityRepairUsesExactAgentAndHashScope(t *testing.T) {
	plan := PlanProjectIdentityRepair([]ProjectIdentityRepairRow{
		{Agent: "claude", ProjectHash: "shared", RepositoryID: "git.example.test/team/alpha"},
		{Agent: "codex", ProjectHash: "shared", RepositoryID: "git.example.test/team/beta"},
		{Agent: "Claude", ProjectHash: "shared", RepositoryID: "git.example.test/team/gamma"},
	})

	if len(plan.Results) != 3 {
		t.Fatalf("result count = %d, want exact agent+hash scopes", len(plan.Results))
	}
	want := []string{"Claude/shared", "claude/shared", "codex/shared"}
	got := make([]string, 0, len(plan.Results))
	for _, result := range plan.Results {
		got = append(got, result.Agent+"/"+result.ProjectHash)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered scopes = %v, want %v", got, want)
	}
}

func TestProjectIdentityRepairPlanCountsEveryScope(t *testing.T) {
	plan := PlanProjectIdentityRepair([]ProjectIdentityRepairRow{
		{Agent: "claude", ProjectHash: "recoverable", RepositoryID: "git.example.test/team/alpha"},
		{Agent: "claude", ProjectHash: "conflict", RepositoryID: "git.example.test/team/alpha", GitRemoteURL: "https://git.example.test/team/beta.git"},
		{Agent: "claude", ProjectHash: "local", RepositoryID: "local:alpha:0123456789abcdef"},
		{Agent: "claude", ProjectHash: "blank"},
	})

	want := ProjectIdentityRepairCounts{
		Recoverable: 1, ConflictingRemote: 1, LocalOnly: 1, BlankIdentity: 1,
		Conflict: 1, Local: 1, Blank: 1,
	}
	if plan.Counts != want {
		t.Fatalf("counts = %+v, want %+v", plan.Counts, want)
	}
	if plan.WriteQueries != 0 {
		t.Fatalf("write queries = %d, want 0", plan.WriteQueries)
	}
}

func TestProjectIdentityRepairProposalIgnoresDifferingSubpaths(t *testing.T) {
	plan := PlanProjectIdentityRepair([]ProjectIdentityRepairRow{
		{Agent: "claude", ProjectHash: "same", RepositoryID: "git.example.test/team/alpha", RepoSubpath: ""},
		{Agent: "claude", ProjectHash: "same", GitRemoteURL: "https://git.example.test/team/alpha.git", RepoSubpath: "web/"},
	})
	if len(plan.Results) != 1 || plan.Results[0].Category != ProjectIdentityRepairRecoverable {
		t.Fatalf("plan = %#v, want one recoverable result", plan)
	}
	want := &ProjectIdentityRepairProposal{RepositoryID: "git.example.test/team/alpha", RepositoryName: "alpha"}
	if !reflect.DeepEqual(plan.Results[0].Proposal, want) {
		t.Fatalf("proposal = %#v, want %#v", plan.Results[0].Proposal, want)
	}
}

func TestPlanProjectIdentityRepairHistoryAcceptsBlankAndSubpathMix(t *testing.T) {
	plan := PlanProjectIdentityRepairWithHistory(
		[]ProjectIdentityRepairRow{{
			Agent: "claude", ProjectHash: "same", RepositoryID: "git.example.test/team/alpha", RepositoryName: "alpha",
		}},
		[]ProjectIdentityRepairHistoryRow{
			{Agent: "claude", ProjectHash: "same", RepositoryID: "git.example.test/team/alpha", RepoSubpath: "", RecordCount: 3},
			{Agent: "claude", ProjectHash: "same", RepositoryID: "git.example.test/team/alpha", RepoSubpath: "web/", RecordCount: 4},
		},
	)
	if len(plan.Results) != 1 {
		t.Fatalf("history scopes = %#v, want one exact scope", plan.Results)
	}
	result := plan.Results[0]
	if result.Category != ProjectIdentityRepairAlreadyCanonical || result.Reason != ProjectIdentityRepairAlreadyCanonicalReason || result.Proposal != nil {
		t.Fatalf("history result = %#v, want no-op: the subpath mix is one repository", result)
	}
}

func TestPlanProjectIdentityRepairHistoryRecoversScopeWithoutCurrentRow(t *testing.T) {
	plan := PlanProjectIdentityRepairWithHistory(nil, []ProjectIdentityRepairHistoryRow{
		{Agent: " claude ", ProjectHash: " same ", RepositoryID: "git.example.test/team/alpha", RepoSubpath: "web/", RecordCount: 7, FirstSeen: time.Unix(1, 0), LastSeen: time.Unix(2, 0)},
	})
	if len(plan.Results) != 1 || plan.Results[0].Category != ProjectIdentityRepairRecoverable {
		t.Fatalf("history classification = %#v, want recoverable", plan)
	}
	want := &ProjectIdentityRepairProposal{RepositoryID: "git.example.test/team/alpha", RepositoryName: "alpha"}
	if !reflect.DeepEqual(plan.Results[0].Proposal, want) {
		t.Fatalf("history proposal = %#v, want %#v", plan.Results[0].Proposal, want)
	}
}

// The split stored row #382 describes: the projects row fell back to a local id
// while the sessions under the same hash carried the remote identity.
func TestPlanProjectIdentityRepairHistoryRecoversLocalRowWithRemoteHistory(t *testing.T) {
	plan := PlanProjectIdentityRepairWithHistory(
		[]ProjectIdentityRepairRow{{
			Agent: "claude", ProjectHash: "blank-current", RepositoryID: "local:project:0123456789abcdef", RepositoryName: "project",
		}},
		[]ProjectIdentityRepairHistoryRow{{
			Agent: "claude", ProjectHash: "blank-current", RepositoryID: "git.example.test/team/alpha", RepoSubpath: "", RecordCount: 2,
		}},
	)
	if len(plan.Results) != 1 {
		t.Fatalf("results = %#v, want one scope", plan.Results)
	}
	result := plan.Results[0]
	want := &ProjectIdentityRepairProposal{RepositoryID: "git.example.test/team/alpha", RepositoryName: "alpha"}
	if result.Category != ProjectIdentityRepairRecoverable || !reflect.DeepEqual(result.Proposal, want) {
		t.Fatalf("local-row result = %#v, want recoverable to %#v", result, want)
	}
}

func TestPlanProjectIdentityRepairHistoryMarksCanonicalScopeAsNoOp(t *testing.T) {
	path := "service/"
	plan := PlanProjectIdentityRepairWithHistory(
		[]ProjectIdentityRepairRow{{
			Agent: "claude", ProjectHash: "same", RepositoryID: "git.example.test/team/alpha", RepositoryName: "alpha", RepoSubpath: path,
		}},
		[]ProjectIdentityRepairHistoryRow{{
			Agent: "claude", ProjectHash: "same", RepositoryID: "git.example.test/team/alpha", RepoSubpath: path, RecordCount: 1,
		}},
	)
	if len(plan.Results) != 1 {
		t.Fatalf("results = %#v, want one scope", plan.Results)
	}
	result := plan.Results[0]
	if result.Category != ProjectIdentityRepairAlreadyCanonical || result.Reason != ProjectIdentityRepairAlreadyCanonicalReason || result.Proposal != nil {
		t.Fatalf("canonical result = %#v, want no-op without proposal", result)
	}
}

// The proposal never writes repo_subpath, so a rooted value in it is not evidence
// against the repository identity.
func TestPlanProjectIdentityRepairIgnoresRootedSubpath(t *testing.T) {
	plan := PlanProjectIdentityRepair([]ProjectIdentityRepairRow{{
		Agent: "claude", ProjectHash: "rooted-path", RepositoryID: "git.example.test/team/alpha", RepositoryName: "alpha", RepoSubpath: `\private\project`,
	}})
	if len(plan.Results) != 1 || plan.Results[0].Category != ProjectIdentityRepairAlreadyCanonical || plan.Results[0].Proposal != nil {
		t.Fatalf("rooted subpath result = %#v, want no-op", plan.Results[0])
	}
}

func TestPlanProjectIdentityRepairHistorySeparatesCrossAgentHashConflict(t *testing.T) {
	plan := PlanProjectIdentityRepairWithHistory(
		[]ProjectIdentityRepairRow{
			{Agent: "claude", ProjectHash: "same", RepositoryID: "git.example.test/team/alpha"},
			{Agent: "codex", ProjectHash: "same", RepositoryID: "git.example.test/team/beta"},
		}, []ProjectIdentityRepairHistoryRow{
			{Agent: "claude", ProjectHash: "same", RepositoryID: "git.example.test/team/alpha"},
			{Agent: "codex", ProjectHash: "same", RepositoryID: "git.example.test/team/beta"},
		},
	)
	if len(plan.Results) != 2 {
		t.Fatalf("scopes = %#v, want two agent scopes", plan.Results)
	}
	for _, result := range plan.Results {
		if result.Category != ProjectIdentityRepairHistoricalIdentityConflict || result.Reason != ProjectIdentityRepairCrossAgentConflict || result.Proposal != nil {
			t.Fatalf("cross-agent result = %#v, want historical conflict without proposal", result)
		}
	}
}

func TestPlanProjectIdentityRepairCrossAgentConflictIgnoresUnrelatedHistory(t *testing.T) {
	plan := PlanProjectIdentityRepairWithHistory(
		[]ProjectIdentityRepairRow{
			{Agent: "claude", ProjectHash: "same", RepositoryID: "git.example.test/team/alpha"},
			{Agent: "codex", ProjectHash: "same", RepositoryID: "git.example.test/team/beta"},
		},
		[]ProjectIdentityRepairHistoryRow{{
			Agent: "claude", ProjectHash: "other", RepositoryID: "git.example.test/team/elsewhere", RepoSubpath: "service/",
		}},
	)
	seen := 0
	for _, result := range plan.Results {
		if result.ProjectHash != "same" {
			continue
		}
		seen++
		if result.Category != ProjectIdentityRepairRecoverable || result.Proposal == nil {
			t.Fatalf("unrelated history changed %s result: %#v", result.Agent, result)
		}
	}
	if seen != 2 {
		t.Fatalf("same-hash results = %d, want two: %#v", seen, plan.Results)
	}
}

func TestPlanProjectIdentityRepairKeepsRawAgentAndHashHistoryScopesSeparate(t *testing.T) {
	identity := "git.example.test/team/alpha"
	plan := PlanProjectIdentityRepairWithHistory(
		[]ProjectIdentityRepairRow{
			{Agent: " claude", ProjectHash: "same", RepositoryID: identity, RepositoryName: "alpha", RepoSubpath: "service-a/"},
			{Agent: "claude", ProjectHash: "same", RepositoryID: identity, RepositoryName: "alpha", RepoSubpath: "service-b/"},
		},
		[]ProjectIdentityRepairHistoryRow{
			{Agent: " claude", ProjectHash: "same", RepositoryID: identity, RepoSubpath: "service-a/"},
			{Agent: "claude", ProjectHash: "same", RepositoryID: identity, RepoSubpath: "service-b/"},
		},
	)
	if len(plan.Results) != 2 {
		t.Fatalf("results = %#v, want two exact agent scopes", plan.Results)
	}
	for _, result := range plan.Results {
		if result.Category != ProjectIdentityRepairAlreadyCanonical || result.Proposal != nil {
			t.Fatalf("raw scope %q was merged with another history scope: %#v", result.Agent, result)
		}
	}
}

func TestPlanProjectIdentityRepairHistoryRejectsMultipleRemoteIdentities(t *testing.T) {
	plan := PlanProjectIdentityRepairWithHistory(
		[]ProjectIdentityRepairRow{{
			Agent: "claude", ProjectHash: "same", RepositoryID: "git.example.test/team/alpha",
		}},
		[]ProjectIdentityRepairHistoryRow{
			{Agent: "claude", ProjectHash: "same", RepositoryID: "git.example.test/team/alpha", RepoSubpath: "service/"},
			{Agent: "claude", ProjectHash: "same", RepositoryID: "git.example.test/team/beta", RepoSubpath: "service/"},
		},
	)
	if len(plan.Results) != 1 {
		t.Fatalf("results = %#v, want one scope", plan.Results)
	}
	result := plan.Results[0]
	if result.Category != ProjectIdentityRepairHistoricalIdentityConflict || result.Reason != ProjectIdentityRepairHistoricalConflict || result.Proposal != nil {
		t.Fatalf("historical remote conflict = %#v, want unresolved without proposal", result)
	}
}

func TestPlanProjectIdentityRepairHistoryDoesNotConflictSameRemoteAcrossAgents(t *testing.T) {
	identity := "git.example.test/team/alpha"
	plan := PlanProjectIdentityRepairWithHistory(
		[]ProjectIdentityRepairRow{
			{Agent: "claude", ProjectHash: "same", RepositoryID: identity, RepositoryName: "alpha", RepoSubpath: "service/"},
			{Agent: "codex", ProjectHash: "same", RepositoryID: identity, RepositoryName: "alpha", RepoSubpath: "service/"},
		},
		[]ProjectIdentityRepairHistoryRow{
			{Agent: "claude", ProjectHash: "same", RepositoryID: identity, RepoSubpath: "service/"},
			{Agent: "codex", ProjectHash: "same", RepositoryID: identity, RepoSubpath: "service/"},
		},
	)
	if len(plan.Results) != 2 {
		t.Fatalf("results = %#v, want two agent scopes", plan.Results)
	}
	for _, result := range plan.Results {
		if result.Category != ProjectIdentityRepairAlreadyCanonical || result.Proposal != nil {
			t.Fatalf("same-remote cross-agent result = %#v, want independent no-op", result)
		}
	}
}
