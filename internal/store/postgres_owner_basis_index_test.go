package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const sessionRecordOwnerBasisIndex = "idx_srec_owner_basis"

func TestSessionRecordOwnerBasisCoveringIndexMigration(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DROP INDEX IF EXISTS `+sessionRecordOwnerBasisIndex); err != nil {
		t.Fatal(err)
	}
	owner := personalFixture(t, s, "owner@example.test", "uid-owner")
	base := time.Now().UTC()
	record := &SessionRecord{Ts: base, SessionID: "owner-basis-session", UUID: "owner-basis-record",
		RecordType: "user", UserID: "uid-owner", ProfileEmail: "owner@example.test", LoginEmail: "login@example.test",
		BillingProvider: "anthropic", AccountID: "acct-owner", ProjectHash: "project-owner", Raw: json.RawMessage(`{"text":"synthetic"}`)}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{record}); err != nil {
		t.Fatalf("insert normal session record before index migration: %v", err)
	}
	if !ownerBasisVisible(ctx, t, s, owner, record.SessionID) {
		t.Fatal("synthetic current owner was not visible before index migration")
	}
	if hasOwnerBasisIndexOnlyScan(t, ctx, s, record.SessionID) {
		t.Fatal("test precondition failed: owner-basis covering index exists before migration")
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate adding owner-basis index: %v", err)
	}
	projection := "session_id,user_id,profile_email,login_email,billing_provider,account_id,project_hash"
	if plan := ownerBasisExplainJSON(t, ctx, s, record.SessionID, projection); !containsOwnerBasisIndexOnlyScan(decodeOwnerBasisPlan(t, plan)) {
		t.Fatalf("owner-basis projection cannot use its covering index-only scan after Migrate: %s", plan)
	}
	if !ownerBasisVisible(ctx, t, s, owner, record.SessionID) {
		t.Fatal("covering index migration changed the existing owner result")
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("idempotent second Migrate: %v", err)
	}
	if !hasOwnerBasisIndexOnlyScan(t, ctx, s, record.SessionID) {
		t.Fatal("second Migrate removed or invalidated the owner-basis covering index")
	}
	if hasOwnerBasisIndexOnlyScanForProjection(t, ctx, s, record.SessionID, "session_id,raw") {
		t.Fatal("transcript-bearing raw column unexpectedly became index-only")
	}

	// Exercise the regular store insert path after the index exists.
	longEmail := strings.Repeat("a", 64) + "@" + strings.Repeat("d", 63) + "." + strings.Repeat("e", 63) + "." + strings.Repeat("f", 50) + ".test"
	longID := strings.Repeat("i", 256)
	longProject := strings.Repeat("p", 256)
	longAccount := strings.Repeat("a", 256)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: base.Add(time.Second), SessionID: longID,
		UUID: "owner-basis-after-migrate-record", RecordType: "user", UserID: longID,
		ProfileEmail: longEmail, LoginEmail: longEmail, BillingProvider: "anthropic",
		AccountID: longAccount, ProjectHash: longProject, Raw: json.RawMessage(`{"text":"synthetic"}`)}}); err != nil {
		t.Fatalf("normal session record insert after index migration: %v", err)
	}
	var userIDLen, profileEmailLen, loginEmailLen, accountIDLen, projectHashLen int
	if err := s.pool.QueryRow(ctx, `SELECT length(user_id),length(profile_email),length(login_email),length(account_id),length(project_hash)
		FROM session_records WHERE uuid='owner-basis-after-migrate-record'`).Scan(
		&userIDLen, &profileEmailLen, &loginEmailLen, &accountIDLen, &projectHashLen); err != nil {
		t.Fatal(err)
	}
	if userIDLen != len(longID) || profileEmailLen != len(longEmail) || loginEmailLen != len(longEmail) ||
		accountIDLen != len(longAccount) || projectHashLen != len(longProject) {
		t.Fatalf("normal insert metadata was truncated: user=%d profile=%d login=%d account=%d project=%d",
			userIDLen, profileEmailLen, loginEmailLen, accountIDLen, projectHashLen)
	}
}

func ownerBasisVisible(ctx context.Context, t *testing.T, s *PgStore, dashboardUserID int64, sessionID string) bool {
	t.Helper()
	query := personalVisibleSourceQuery(" AND session_id IN (SELECT session_id FROM candidate)")
	query = `WITH candidate(session_id) AS (VALUES ($4::text)), ` + strings.TrimPrefix(query, "WITH ") + `
		SELECT EXISTS(SELECT 1 FROM owned WHERE session_id=$4)`
	var visible bool
	if err := s.pool.QueryRow(ctx, query, dashboardUserID, "uid-owner", "owner@example.test", sessionID).Scan(&visible); err != nil {
		t.Fatalf("query synthetic owner proof: %v", err)
	}
	return visible
}

func hasOwnerBasisIndexOnlyScan(t *testing.T, ctx context.Context, s *PgStore, sessionID string) bool {
	t.Helper()
	return hasOwnerBasisIndexOnlyScanForProjection(t, ctx, s, sessionID,
		"session_id,user_id,profile_email,login_email,billing_provider,account_id,project_hash")
}

func hasOwnerBasisIndexOnlyScanForProjection(t *testing.T, ctx context.Context, s *PgStore, sessionID, projection string) bool {
	t.Helper()
	return containsOwnerBasisIndexOnlyScan(decodeOwnerBasisPlan(t,
		ownerBasisExplainJSON(t, ctx, s, sessionID, projection)))
}

func ownerBasisExplainJSON(t *testing.T, ctx context.Context, s *PgStore, sessionID, projection string) string {
	t.Helper()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	query := `EXPLAIN (FORMAT JSON) SELECT ` + projection + ` FROM session_records WHERE session_id=$1 AND session_id<>''`
	if err := tx.QueryRow(ctx, query, sessionID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func decodeOwnerBasisPlan(t *testing.T, raw string) any {
	t.Helper()
	var plan any
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatalf("decode EXPLAIN JSON: %v", err)
	}
	return plan
}

func containsOwnerBasisIndexOnlyScan(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		if node["Node Type"] == "Index Only Scan" {
			if indexName, ok := node["Index Name"].(string); ok &&
				(indexName == sessionRecordOwnerBasisIndex || strings.HasSuffix(indexName, "_"+sessionRecordOwnerBasisIndex)) {
				return true
			}
		}
		for _, child := range node {
			if containsOwnerBasisIndexOnlyScan(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if containsOwnerBasisIndexOnlyScan(child) {
				return true
			}
		}
	}
	return false
}
