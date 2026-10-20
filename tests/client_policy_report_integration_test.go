package tests

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

// The pieces are tested apart -- the client against a recording handler, the
// handler against a recording store, the store against a database. This runs
// them joined: the request the client builds has to decode in the real handler
// and land as the row an operator will query. (That the route sits behind the
// CLI authentication is the handler test's to show; this server, like the other
// sync integration tests', is built without that middleware.)
func TestClientPolicyReport_reachesTheDatabase(t *testing.T) {
	// Given
	s := acquireSyncStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, "TRUNCATE client_policy_reports"); err != nil {
		t.Fatalf("truncate client_policy_reports: %v", err)
	}
	ts, token := newSyncServer(t, s)
	defer ts.Close()
	report := store.ClientPolicyReport{
		ProfileEmail: "alice@example.com", UserID: "alice",
		InstallationID: uuid.NewString(), Profile: "work",
		SyncEndpoint: "http://cctrace.company.example:4318", OTELEndpoint: "http://cctrace.company.example:4317", Protocol: "grpc",
		AppliedSequence: 2, CheckResult: store.ClientPolicyCheckDeferred, CheckSequence: 3,
		CheckReason: `shared Codex settings with profile "home"; policy deferred`,
	}

	// When: the same client reports twice, as a daemon does every five minutes.
	client := syncer.NewClient(ts.URL, token, "v0.7.62")
	for i := 0; i < 2; i++ {
		if err := client.SendPolicyReport(ctx, report); err != nil {
			t.Fatalf("report %d: %v", i+1, err)
		}
	}

	// Then: one row, as sent.
	var rows int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM client_policy_reports`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want 1", rows)
	}
	var got store.ClientPolicyReport
	var applied, attempted int64
	if err := s.Pool().QueryRow(ctx, `SELECT profile_email, user_id, installation_id, profile,
			sync_endpoint, otel_endpoint, protocol, applied_sequence, check_result, check_sequence, check_reason, client_version
		FROM client_policy_reports`).Scan(&got.ProfileEmail, &got.UserID, &got.InstallationID, &got.Profile,
		&got.SyncEndpoint, &got.OTELEndpoint, &got.Protocol, &applied, &got.CheckResult, &attempted, &got.CheckReason, &got.ClientVersion); err != nil {
		t.Fatalf("read row: %v", err)
	}
	got.AppliedSequence, got.CheckSequence = uint64(applied), uint64(attempted)
	want := report
	want.ClientVersion = "v0.7.62"
	if got != want {
		t.Fatalf("stored row = %+v, want %+v", got, want)
	}
}
