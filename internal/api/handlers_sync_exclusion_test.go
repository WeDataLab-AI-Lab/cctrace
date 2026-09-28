package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cctrace/internal/ingestblock"
	"cctrace/internal/store"
)

// accountRecord is one synced record stamped with a billing account and, when
// given, a login address.
func accountRecord(sessionID, provider, accountID, loginEmail string) map[string]any {
	r := map[string]any{
		"ts":               time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"session_id":       sessionID,
		"record_type":      "user",
		"profile_email":    "p@example.com",
		"uuid":             sessionID + "-u",
		"billing_provider": provider,
		"account_id":       accountID,
		"raw":              json.RawMessage(`{"text":"hi"}`),
	}
	if loginEmail != "" {
		r["login_email"] = loginEmail
	}
	return r
}

// #715: an excluded account's sessions are refused at ingest, not only hidden on
// the dashboard -- its conversation text never reaches storage. Codex records name
// their account by billing id; a record that carries a login address is refused
// by that too.
func TestHandleSync_refusesExcludedAccounts(t *testing.T) {
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
	bl.AddExcludedAccount("openai", "acct-personal")
	bl.AddExcludedEmail("excluded@example.test")
	srv := newTestServer(m, nil).WithIngestBlocklist(bl)

	body, _ := json.Marshal(map[string]any{
		"profile_email": "p@example.com",
		"user_id":       "u-1",
		"project_hash":  "ph-ok",
		"records": []map[string]any{
			accountRecord("personal-codex", "openai", "acct-personal", ""),
			accountRecord("excluded-login", "anthropic", "acct-other", "Excluded@example.test"),
			accountRecord("team-codex", "openai", "acct-team", ""),
			accountRecord("same-id-other-provider", "anthropic", "acct-personal", ""),
		},
	})
	out := postSync(t, srv, string(body))

	if out["skipped"] != 2 {
		t.Errorf("skipped = %d, want 2", out["skipped"])
	}
	want := map[string]bool{"team-codex": true, "same-id-other-provider": true}
	if len(inserted) != len(want) {
		t.Fatalf("inserted %v, want only %v", inserted, want)
	}
	for _, sid := range inserted {
		if !want[sid] {
			t.Errorf("stored %q, which belongs to an excluded account", sid)
		}
	}
}

// A request that carries nothing but an excluded account's records must not
// leave its project behind either: the name and git remote of a personal
// repository would appear in the project selector with no sessions under it.
func TestHandleSync_excludedOnlyRequestDoesNotCreateTheProject(t *testing.T) {
	upserted := false
	m := &mockStore{
		upsertProjectWithMetadataFn: func(context.Context, string, string, string, store.ProjectIdentityMetadata, time.Time) error {
			upserted = true
			return nil
		},
	}
	bl := ingestblock.New()
	bl.AddExcludedAccount("openai", "acct-personal")
	srv := newTestServer(m, nil).WithIngestBlocklist(bl)

	body, _ := json.Marshal(map[string]any{
		"profile_email": "p@example.com",
		"user_id":       "u-1",
		"project_hash":  "ph-personal",
		"project_name":  "side-project",
		"records":       []map[string]any{accountRecord("personal-codex", "openai", "acct-personal", "")},
	})
	if out := postSync(t, srv, string(body)); out["skipped"] != 1 {
		t.Fatalf("skipped = %d, want 1", out["skipped"])
	}
	if upserted {
		t.Error("the project of an excluded account's request was recorded")
	}
}
