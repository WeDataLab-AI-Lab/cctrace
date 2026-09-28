package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cctrace/internal/store"
)

func TestHandleSync_ForwardsResolvedRootPresenceToProjectAndRecord(t *testing.T) {
	var gotProject store.ProjectIdentityMetadata
	var gotRecord *store.SessionRecord
	m := &mockStore{
		upsertProjectWithMetadataFn: func(_ context.Context, _, _, _ string, metadata store.ProjectIdentityMetadata, _ time.Time) error {
			gotProject = metadata
			return nil
		},
		insertSessionRecordsFn: func(_ context.Context, records []*store.SessionRecord) error {
			if len(records) != 1 {
				t.Fatalf("records = %d, want 1", len(records))
			}
			gotRecord = records[0]
			return nil
		},
	}
	srv := newTestServer(m, nil)
	body, err := json.Marshal(map[string]any{
		"profile_email":        "qa@example.test",
		"user_id":              "qa-user",
		"agent":                "claude",
		"project_hash":         "h-main",
		"git_remote_url":       "https://example.com/org/issue-382.git",
		"repository_id":        "example.com/org/issue-382",
		"repository_name":      "issue-382",
		"repo_subpath":         "",
		"repo_subpath_present": true,
		"repository_id_source": "resolved",
		"records": []map[string]any{{
			"session_id":  "s-main",
			"record_type": "user",
			"raw":         json.RawMessage(`{"text":"hello"}`),
		}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	postSync(t, srv, string(body))

	if gotProject.RepositoryIDSource != store.RepositoryIDSourceResolved || !gotProject.RepoSubpathPresent {
		t.Fatalf("project authority = source:%q present:%v, want resolved,true", gotProject.RepositoryIDSource, gotProject.RepoSubpathPresent)
	}
	if gotProject.RepoSubpath != "" || gotProject.RepositoryID != "example.com/org/issue-382" {
		t.Fatalf("project identity = %#v, want resolved root", gotProject)
	}
	if gotRecord == nil || gotRecord.RepositoryIDSource != store.RepositoryIDSourceResolved || !gotRecord.RepoSubpathPresent {
		t.Fatalf("record authority = %#v, want resolved,true", gotRecord)
	}
}

func TestHandleSync_ReenrichForwardsFallbackAuthorityToSessionRecords(t *testing.T) {
	var gotProject store.ProjectIdentityMetadata
	var gotRecord *store.SessionRecord
	m := &mockStore{
		upsertProjectWithMetadataFn: func(_ context.Context, _, _, _ string, metadata store.ProjectIdentityMetadata, _ time.Time) error {
			gotProject = metadata
			return nil
		},
		reenrichSessionRecordsFn: func(_ context.Context, records []*store.SessionRecord) (int, error) {
			if len(records) != 1 {
				t.Fatalf("records = %d, want 1", len(records))
			}
			gotRecord = records[0]
			return 1, nil
		},
	}
	srv := newTestServer(m, nil)
	body, err := json.Marshal(map[string]any{
		"profile_email":        "qa@example.test",
		"user_id":              "qa-user",
		"agent":                "claude",
		"project_hash":         "h-worktree",
		"project_name":         "worktree",
		"repository_id":        "local:issue-382:deadbeef",
		"repository_name":      "worktree",
		"repository_id_source": "fallback",
		"reenrich":             true,
		"records": []map[string]any{{
			"session_id":  "s-worktree",
			"record_type": "assistant",
			"raw":         json.RawMessage(`{"text":"hello"}`),
		}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	postSync(t, srv, string(body))

	if gotProject.RepositoryIDSource != store.RepositoryIDSourceFallback {
		t.Fatalf("project source = %q, want fallback", gotProject.RepositoryIDSource)
	}
	if gotRecord == nil || gotRecord.RepositoryIDSource != store.RepositoryIDSourceFallback {
		t.Fatalf("record = %#v, want fallback source", gotRecord)
	}
}
