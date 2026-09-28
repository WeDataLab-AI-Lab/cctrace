package store

import (
	"context"
	"testing"
	"time"
)

func TestProjectIdentityAuthority_LocalFallbackCannotDowngradeRemote(t *testing.T) {
	metadata := ProjectIdentityMetadata{
		RepositoryID:       "local:issue-382:deadbeef",
		RepositoryName:     "worktree",
		RepositoryIDSource: RepositoryIDSourceFallback,
	}

	if projectIdentityRemoteAuthority(metadata) {
		t.Fatal("local fallback was treated as remote-authoritative")
	}
	clear, fill := projectIdentitySubpathFlags(metadata)
	if clear || fill {
		t.Fatalf("local fallback subpath flags = clear:%v fill:%v, want both false", clear, fill)
	}
}

func TestProjectIdentityAuthority_ResolvedEmptyRootMayClearStaleSubpath(t *testing.T) {
	clear, fill := projectIdentitySubpathFlags(ProjectIdentityMetadata{
		RepositoryID:       "example.com/org/issue-382",
		RepositoryIDSource: RepositoryIDSourceResolved,
		RepoSubpath:        "",
		RepoSubpathPresent: true,
	})
	if !clear || !fill {
		t.Fatalf("resolved root flags = clear:%v fill:%v, want true,true", clear, fill)
	}
}

func TestProjectIdentityAuthority_LegacyAbsentSubpathCannotClear(t *testing.T) {
	clear, fill := projectIdentitySubpathFlags(ProjectIdentityMetadata{
		RepositoryID: "example.com/org/issue-382",
		RepoSubpath:  "",
	})
	if clear || !fill {
		t.Fatalf("legacy root flags = clear:%v fill:%v, want false,true", clear, fill)
	}
}

func TestProjectIdentityAuthority_ExplicitUnknownCannotClear(t *testing.T) {
	clear, fill := projectIdentitySubpathFlags(ProjectIdentityMetadata{
		RepositoryID:       "example.com/org/issue-382",
		RepositoryIDSource: RepositoryIDSourceUnknown,
		RepoSubpath:        "",
		RepoSubpathPresent: true,
	})
	if clear || fill {
		t.Fatalf("explicit unknown flags = clear:%v fill:%v, want both false", clear, fill)
	}
}

func TestSessionReenrich_UsesSameAuthorityDecision(t *testing.T) {
	remote, clear, fill := sessionRecordIdentityFlags(&SessionRecord{
		RepositoryID:       "local:issue-382:deadbeef",
		RepositoryIDSource: RepositoryIDSourceFallback,
		RepoSubpath:        "stale/package/",
		RepoSubpathPresent: true,
	})
	if remote || clear || fill {
		t.Fatalf("fallback reenrich flags = remote:%v clear:%v fill:%v, want all false", remote, clear, fill)
	}

	remote, clear, fill = sessionRecordIdentityFlags(&SessionRecord{
		RepositoryID:       "example.com/org/issue-382",
		RepositoryIDSource: RepositoryIDSourceResolved,
		RepoSubpathPresent: true,
	})
	if !remote || !clear || !fill {
		t.Fatalf("resolved root reenrich flags = remote:%v clear:%v fill:%v, want all true", remote, clear, fill)
	}
}

func TestUpsertProject_AuthoritativeRootClearsStaleSubpath(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	if err := s.UpsertProject(ctx, "claude", "h-main", "issue-382", "", "example.com/org/issue-382", "issue-382", "stale/package/", time.Now()); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := s.UpsertProjectWithMetadata(ctx, "claude", "h-main", "issue-382", ProjectIdentityMetadata{
		GitRemoteURL:       "https://example.com/org/issue-382.git",
		RepositoryID:       "example.com/org/issue-382",
		RepositoryName:     "issue-382",
		RepoSubpath:        "",
		RepositoryIDSource: "resolved",
		RepoSubpathPresent: true,
	}, time.Now()); err != nil {
		t.Fatalf("authoritative root upsert: %v", err)
	}

	var got string
	if err := s.pool.QueryRow(ctx, `SELECT repo_subpath FROM projects WHERE agent = 'claude' AND project_hash = 'h-main'`).Scan(&got); err != nil {
		t.Fatalf("read project: %v", err)
	}
	if got != "" {
		t.Fatalf("repo_subpath = %q, want stale value cleared", got)
	}
}

func TestUpsertProject_LocalFallbackCannotDowngradeRemote(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	if err := s.UpsertProject(ctx, "claude", "h-worktree", "issue-382", "https://example.com/org/issue-382.git", "example.com/org/issue-382", "issue-382", "", time.Now()); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := s.UpsertProjectWithMetadata(ctx, "claude", "h-worktree", "worktree", ProjectIdentityMetadata{
		RepositoryID:       "local:issue-382:deadbeef",
		RepositoryName:     "worktree",
		RepoSubpath:        "",
		RepositoryIDSource: "fallback",
	}, time.Now()); err != nil {
		t.Fatalf("fallback upsert: %v", err)
	}

	var id, name, remote string
	if err := s.pool.QueryRow(ctx, `SELECT repository_id, repository_name, git_remote_url FROM projects WHERE agent = 'claude' AND project_hash = 'h-worktree'`).Scan(&id, &name, &remote); err != nil {
		t.Fatalf("read project: %v", err)
	}
	if id != "example.com/org/issue-382" || name != "issue-382" || remote != "https://example.com/org/issue-382.git" {
		t.Fatalf("fallback downgraded remote metadata: id=%q name=%q remote=%q", id, name, remote)
	}
}
