package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMigration_ClientPlatform_preservesExistingRowsAsEmptyStrings(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	ctx := context.Background()
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE client_versions (
		profile_email TEXT NOT NULL PRIMARY KEY,
		user_id TEXT NOT NULL DEFAULT '',
		client_version TEXT NOT NULL DEFAULT '',
		last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		t.Fatalf("create legacy client_versions: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO client_versions (profile_email, client_version) VALUES ('legacy@example.com', 'v0.7.30')`); err != nil {
		t.Fatalf("insert legacy client version: %v", err)
	}
	var platformMigration string
	for _, migration := range migrations {
		if strings.Contains(migration, "ALTER TABLE client_versions") && strings.Contains(migration, "client_os") {
			platformMigration = migration
			break
		}
	}
	if platformMigration == "" {
		t.Fatal("client platform migration not found")
	}

	// When
	if _, err := tx.Exec(ctx, platformMigration); err != nil {
		t.Fatalf("apply client platform migration: %v", err)
	}
	if _, err := tx.Exec(ctx, platformMigration); err != nil {
		t.Fatalf("reapply client platform migration: %v", err)
	}

	// Then
	var clientOS, clientArch string
	if err := tx.QueryRow(ctx, `SELECT client_os, client_arch FROM client_versions WHERE profile_email = 'legacy@example.com'`).Scan(&clientOS, &clientArch); err != nil {
		t.Fatalf("query migrated client version: %v", err)
	}
	if clientOS != "" || clientArch != "" {
		t.Fatalf("migrated legacy platform = %q/%q, want empty values", clientOS, clientArch)
	}
}

func TestPgStore_UpsertClientVersion_updatesClientPlatform(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: now, SessionID: "platform-session", RecordType: "user", ProfileEmail: "alice@example.com", UUID: "platform-1",
	}}); err != nil {
		t.Fatalf("seed active account: %v", err)
	}
	if err := s.UpsertClientVersion(ctx, ClientVersionUpdate{
		UserID: "alice", ProfileEmail: "alice@example.com", ClientVersion: "v0.7.30", ClientOS: "linux", ClientArch: "amd64",
	}); err != nil {
		t.Fatalf("seed client version: %v", err)
	}

	// When
	if err := s.UpsertClientVersion(ctx, ClientVersionUpdate{
		UserID: "alice", ProfileEmail: "alice@example.com", ClientVersion: "v0.8.0", ClientOS: "darwin", ClientArch: "arm64",
	}); err != nil {
		t.Fatalf("update client version: %v", err)
	}

	// Then
	records, err := s.ListClientVersions(ctx)
	if err != nil {
		t.Fatalf("list client versions: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("client version records = %d, want 1", len(records))
	}
	got := records[0]
	if got.ClientVersion != "v0.8.0" || got.ClientOS != "darwin" || got.ClientArch != "arm64" {
		t.Fatalf("updated client version = %+v, want v0.8.0 darwin/arm64", got)
	}
}

func TestPgStore_UpsertClientVersion_storesEmptyClientPlatformForLegacyClient(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: time.Now().UTC(), SessionID: "legacy-session", RecordType: "user", ProfileEmail: "legacy@example.com", UUID: "legacy-1",
	}}); err != nil {
		t.Fatalf("seed active account: %v", err)
	}

	// When
	if err := s.UpsertClientVersion(ctx, ClientVersionUpdate{
		UserID: "legacy", ProfileEmail: "legacy@example.com", ClientVersion: "v0.7.30",
	}); err != nil {
		t.Fatalf("upsert legacy client version: %v", err)
	}

	// Then
	records, err := s.ListClientVersions(ctx)
	if err != nil {
		t.Fatalf("list client versions: %v", err)
	}
	if len(records) != 1 || records[0].ClientOS != "" || records[0].ClientArch != "" {
		t.Fatalf("legacy client platform records = %+v, want empty values", records)
	}
}
