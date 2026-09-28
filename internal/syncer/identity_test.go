package syncer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_Send_TransmitsIdentityAuthority(t *testing.T) {
	var got SyncPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sync" {
			t.Fatalf("request = %s %s, want POST /api/sync", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "", "")
	if _, err := client.Send(context.Background(), "claude", "profile@example.com", "user-1", "h-transport", "transport", ProjectIdentity{
		GitRemoteURL:       "https://example.com/org/transport.git",
		RepositoryID:       "example.com/org/transport",
		RepositoryIDSource: "resolved",
		RepositoryName:     "transport",
		RepoSubpath:        "",
		RepoSubpathPresent: true,
		CommitSHA:          "deadbeef",
		Branch:             "main",
	}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.RepositoryIDSource != "resolved" || !got.RepoSubpathPresent {
		t.Fatalf("identity = source:%q present:%v, want resolved,true", got.RepositoryIDSource, got.RepoSubpathPresent)
	}
}

func TestSyncPayload_ResolvedRootCarriesSubpathPresence(t *testing.T) {
	payload := SyncPayload{
		RepositoryID:       "example.com/org/issue-382",
		RepositoryIDSource: "resolved",
		RepoSubpath:        "",
		RepoSubpathPresent: true,
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("decode wire: %v", err)
	}

	var present bool
	if err := json.Unmarshal(wire["repo_subpath_present"], &present); err != nil {
		t.Fatalf("decode repo_subpath_present: %v", err)
	}
	if !present {
		t.Fatal("repo_subpath_present = false, want true")
	}
	if got := string(wire["repository_id_source"]); got != `"resolved"` {
		t.Fatalf("repository_id_source = %s, want resolved", got)
	}
}

func TestSyncPayload_LegacyAbsentMetadataRemainsUnknown(t *testing.T) {
	var payload SyncPayload
	if err := json.Unmarshal([]byte(`{"repository_id":"example.com/org/issue-382","repo_subpath":""}`), &payload); err != nil {
		t.Fatalf("unmarshal legacy payload: %v", err)
	}
	if payload.RepositoryIDSource != "" {
		t.Fatalf("RepositoryIDSource = %q, want absent/unknown", payload.RepositoryIDSource)
	}
	if payload.RepoSubpathPresent {
		t.Fatal("RepoSubpathPresent = true for legacy payload without presence marker")
	}
}
