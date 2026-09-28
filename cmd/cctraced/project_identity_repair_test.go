package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"cctrace/internal/store"
)

// recordingProjectIdentityRepairRunner returns a runner that records it ran.
func recordingProjectIdentityRepairRunner(called *bool) projectIdentityRepairRunner {
	return func(context.Context, string, string, io.Writer) error {
		*called = true
		return nil
	}
}

func TestHandleProjectIdentityRepairCommandRequiresExplicitMode(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantMatch bool
		wantError string
	}{
		{name: "unrelated", args: []string{"--version"}},
		{name: "missing mode", args: []string{"project-identity-repair"}, wantMatch: true, wantError: "--dry-run|--apply"},
		{name: "wrong mode", args: []string{"project-identity-repair", "--force"}, wantMatch: true, wantError: "--dry-run|--apply"},
		{name: "both modes", args: []string{"project-identity-repair", "--dry-run", "--apply"}, wantMatch: true, wantError: "--dry-run|--apply"},
		{name: "extra argument", args: []string{"project-identity-repair", "--apply", "extra"}, wantMatch: true, wantError: "--dry-run|--apply"},
		{name: "subcommand not first", args: []string{"--bad", "project-identity-repair", "--dry-run"}, wantMatch: true, wantError: "first argument"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dryRunCalled, applyCalled bool
			handled, err := handleProjectIdentityRepairCommand(context.Background(), tt.args, func(string) string { return "postgres://explicit" }, &bytes.Buffer{},
				recordingProjectIdentityRepairRunner(&dryRunCalled), recordingProjectIdentityRepairRunner(&applyCalled))
			if handled != tt.wantMatch {
				t.Fatalf("handled = %v, want %v", handled, tt.wantMatch)
			}
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantError)
			}
			if dryRunCalled || applyCalled {
				t.Fatal("runner called for invalid or unrelated arguments")
			}
		})
	}
}

func TestHandleProjectIdentityRepairCommandRoutesEachModeToItsRunner(t *testing.T) {
	tests := []struct {
		mode                  string
		wantDryRun, wantApply bool
	}{
		{mode: "--dry-run", wantDryRun: true},
		{mode: "--apply", wantApply: true},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			var dryRunCalled, applyCalled bool
			handled, err := handleProjectIdentityRepairCommand(context.Background(), []string{"project-identity-repair", tt.mode}, func(string) string { return "postgres://explicit" }, &bytes.Buffer{},
				recordingProjectIdentityRepairRunner(&dryRunCalled), recordingProjectIdentityRepairRunner(&applyCalled))
			if !handled || err != nil {
				t.Fatalf("handled=%v error=%v", handled, err)
			}
			if dryRunCalled != tt.wantDryRun || applyCalled != tt.wantApply {
				t.Fatalf("dry-run called %v, apply called %v; want %v, %v", dryRunCalled, applyCalled, tt.wantDryRun, tt.wantApply)
			}
		})
	}
}

func TestHandleProjectIdentityRepairCommandRequiresExplicitDatabaseTarget(t *testing.T) {
	for _, mode := range []string{"--dry-run", "--apply"} {
		var dryRunCalled, applyCalled bool
		handled, err := handleProjectIdentityRepairCommand(context.Background(), []string{"project-identity-repair", mode}, func(string) string { return "" }, &bytes.Buffer{},
			recordingProjectIdentityRepairRunner(&dryRunCalled), recordingProjectIdentityRepairRunner(&applyCalled))
		if !handled || err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
			t.Fatalf("%s: handled=%v error=%v, want explicit DATABASE_URL error", mode, handled, err)
		}
		if dryRunCalled || applyCalled {
			t.Fatalf("%s: runner called without DATABASE_URL", mode)
		}
	}
}

func TestHandleProjectIdentityRepairCommandPassesVersionAndDatabaseWithoutServerBoot(t *testing.T) {
	var gotDSN, gotVersion string
	var envKeys []string
	out := &bytes.Buffer{}
	var applyCalled bool
	handled, err := handleProjectIdentityRepairCommand(context.Background(), []string{"project-identity-repair", "--dry-run"}, func(key string) string {
		envKeys = append(envKeys, key)
		if key == "DATABASE_URL" {
			return "postgres://explicit-target"
		}
		return ""
	}, out, func(runCtx context.Context, dsn, binaryVersion string, gotOut io.Writer) error {
		gotDSN, gotVersion = dsn, binaryVersion
		if _, ok := runCtx.Deadline(); !ok {
			t.Fatal("runner context has no operator timeout")
		}
		if gotOut != out {
			t.Fatal("output writer changed")
		}
		return nil
	}, recordingProjectIdentityRepairRunner(&applyCalled))
	if err != nil || !handled {
		t.Fatalf("handled=%v error=%v", handled, err)
	}
	if gotDSN != "postgres://explicit-target" || gotVersion != version {
		t.Fatalf("runner args = (%q, %q), want explicit target and version %q", gotDSN, gotVersion, version)
	}
	if len(envKeys) != 1 || envKeys[0] != "DATABASE_URL" {
		t.Fatalf("operator read environment keys %v, want DATABASE_URL only", envKeys)
	}
}

func TestRunProjectIdentityRepairRedactsDatabaseURLFromConnectionError(t *testing.T) {
	const marker = "private-fixture-value"
	for name, run := range map[string]projectIdentityRepairRunner{
		"dry-run": runProjectIdentityRepairDryRun,
		"apply":   runProjectIdentityRepairApply,
	} {
		err := run(context.Background(), "postgres://fixture:"+marker+"@%", "v-test", &bytes.Buffer{})
		if err == nil {
			t.Fatalf("%s: invalid database URL succeeded", name)
		}
		if strings.Contains(err.Error(), marker) {
			t.Fatalf("%s: connection error exposed DATABASE_URL: %v", name, err)
		}
	}
}

func TestUsageTextDocumentsProjectIdentityRepairModes(t *testing.T) {
	help := usageText()
	for _, required := range []string{"project-identity-repair --dry-run", "complete projects identity snapshot", "without writes", "project-identity-repair --apply", "one transaction"} {
		if !strings.Contains(help, required) {
			t.Fatalf("help missing %q: %s", required, help)
		}
	}
}

func TestWriteProjectIdentityRepairReportIsStableAndSanitized(t *testing.T) {
	report := &store.ProjectIdentityRepairReport{
		SchemaVersion:      1,
		Mode:               "dry-run",
		ReadOnlyVerified:   true,
		SourceRelation:     "projects",
		SourceColumns:      []string{"agent", "project_hash"},
		SnapshotBefore:     store.ProjectIdentityRepairSnapshot{RowCount: 1, Checksum: "checksum"},
		SnapshotAfter:      store.ProjectIdentityRepairSnapshot{RowCount: 1, Checksum: "checksum"},
		SnapshotConsistent: true,
		WriteQueries:       0,
		Counts:             store.ProjectIdentityRepairCounts{Recoverable: 1},
		DatabaseName:       "testdb",
		DatabaseVersion:    "16",
		Results: []store.ProjectIdentityRepairReportResult{{
			Agent: "claude", ScopeFingerprint: "sha256:fingerprint",
			Category: store.ProjectIdentityRepairRecoverable,
			Reason:   store.ProjectIdentityRepairUniqueRemote,
			Proposal: &store.ProjectIdentityRepairProposal{RepositoryID: "git.example.test/team/alpha", RepositoryName: "alpha"},
		}},
	}

	encode := func() string {
		var out bytes.Buffer
		if err := writeProjectIdentityRepairReport(&out, "v-test", report); err != nil {
			t.Fatalf("encode report: %v", err)
		}
		return out.String()
	}
	first, second := encode(), encode()
	if first != second {
		t.Fatalf("JSON output is not stable\nfirst:  %s\nsecond: %s", first, second)
	}
	for _, forbidden := range []string{"raw-project-hash", "https://git.example.test/team/alpha.git", "profile_email", "user_id"} {
		if strings.Contains(first, forbidden) {
			t.Fatalf("report contains sensitive value %q: %s", forbidden, first)
		}
	}
	for _, required := range []string{"\"binary_version\": \"v-test\"", "\"read_only_verified\": true", "\"scope_fingerprint\": \"sha256:fingerprint\"", "\"repository_id\": \"git.example.test/team/alpha\""} {
		if !strings.Contains(first, required) {
			t.Fatalf("report missing %q: %s", required, first)
		}
	}
}
