package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/ingestblock"
	"cctrace/internal/projecthash"
	"cctrace/internal/store"
)

// syncBody builds the envelope the client posts.
func syncBody(projectHash string, sessionIDs ...string) string {
	recs := make([]map[string]any, 0, len(sessionIDs))
	for i, sid := range sessionIDs {
		recs = append(recs, map[string]any{
			"ts":            time.Date(2026, 8, 21, 10, i, 0, 0, time.UTC).Format(time.RFC3339),
			"session_id":    sid,
			"record_type":   "user",
			"profile_email": "p@example.com",
			"uuid":          sid + "-u",
			"raw":           json.RawMessage(`{"text":"hi"}`),
		})
	}
	b, _ := json.Marshal(map[string]any{
		"profile_email": "p@example.com",
		"user_id":       "u-1",
		"project_hash":  projectHash,
		"records":       recs,
	})
	return string(b)
}

func postSync(t *testing.T, srv *Server, body string) map[string]int {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/sync", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleSync(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d, want 200 (body %s) — a refusal must not be an error, "+
			"or `sync --watch` retries a decision that will never change", rec.Code, rec.Body.String())
	}
	var out map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestHandleSync_dropsDeletedSessionsAndBlockedProjects(t *testing.T) {
	var inserted []string
	m := &mockStore{
		insertSessionRecordsFn: func(ctx context.Context, records []*store.SessionRecord) error {
			for _, r := range records {
				inserted = append(inserted, r.SessionID)
			}
			return nil
		},
	}
	bl := ingestblock.New()
	bl.AddSession("deleted-one")
	srv := newTestServer(m, nil).WithIngestBlocklist(bl)

	out := postSync(t, srv, syncBody("ph-ok", "deleted-one", "fresh"))

	if out["skipped"] != 1 {
		t.Errorf("skipped = %d, want 1 — the count is how the client learns records were "+
			"refused rather than lost", out["skipped"])
	}
	if len(inserted) != 1 || inserted[0] != "fresh" {
		t.Errorf("inserted = %v; want only the session that was never deleted", inserted)
	}
}

func TestHandleSync_blockedProjectSkipsUpsertProject(t *testing.T) {
	upserted := false
	m := &mockStore{
		upsertProjectFn: func(ctx context.Context, agent, projectHash, projectName, gitRemoteURL, repositoryID, repositoryName string) error {
			upserted = true
			return nil
		},
		insertSessionRecordsFn: func(ctx context.Context, records []*store.SessionRecord) error {
			if len(records) > 0 {
				t.Errorf("inserted %d records for a blocked project", len(records))
			}
			return nil
		},
	}
	// The blocklist holds hashes in their REPAIRED form, because that is the only
	// form that ever reaches storage: ingest repairs the envelope hash before
	// UpsertProject and each record's hash before insert, so blocked_projects --
	// which is populated from session_records -- can only ever hold repaired values.
	// Adding the raw string here would silently match nothing.
	bl := ingestblock.New()
	bl.AddProject(projecthash.Repair("ph-blocked"))
	srv := newTestServer(m, nil).WithIngestBlocklist(bl)

	out := postSync(t, srv, syncBody("ph-blocked", "s1", "s2"))

	if out["skipped"] != 2 {
		t.Errorf("skipped = %d, want 2", out["skipped"])
	}
	if upserted {
		t.Error("UpsertProject ran for a blocked project; recreating the projects row " +
			"puts it back in the selector with no sessions behind it")
	}
}

func TestHandleSync_withoutBlocklistAcceptsEverything(t *testing.T) {
	// The cache is nil in any server constructed without it. That has to mean "no
	// rules" rather than "block everything", or a misconfiguration would silently
	// stop all collection.
	count := 0
	m := &mockStore{
		insertSessionRecordsFn: func(ctx context.Context, records []*store.SessionRecord) error {
			count = len(records)
			return nil
		},
	}
	srv := newTestServer(m, nil)
	out := postSync(t, srv, syncBody("ph-ok", "s1", "s2"))

	if count != 2 || out["skipped"] != 0 {
		t.Errorf("inserted %d, skipped %d; want 2 and 0", count, out["skipped"])
	}
}
