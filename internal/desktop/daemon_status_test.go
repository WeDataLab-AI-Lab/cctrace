package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/codexappserver"
	"cctrace/internal/profile"
	"cctrace/internal/usage"
)

func TestDaemonStatusReadIsOptInAndSeparateFromLegacyHello(t *testing.T) {
	svc := newDaemonStatusService(t, t.TempDir())
	legacy := call(t, svc, "legacy", "hello", map[string]any{})
	if legacy.Error != nil {
		t.Fatalf("legacy hello failed: %+v", legacy.Error)
	}
	var legacyResult struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(legacy.Result, &legacyResult); err != nil {
		t.Fatal(err)
	}
	if _, ok := legacyResult.Capabilities["daemon_status"]; ok {
		t.Fatal("legacy hello unexpectedly advertised daemon_status")
	}

	optIn := call(t, svc, "opt-in", "hello", map[string]any{"daemon_status": true})
	if optIn.Error != nil {
		t.Fatalf("daemon_status hello failed: %+v", optIn.Error)
	}
	var optInResult struct {
		Capabilities struct {
			DaemonStatus bool `json:"daemon_status"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(optIn.Result, &optInResult); err != nil {
		t.Fatal(err)
	}
	if !optInResult.Capabilities.DaemonStatus {
		t.Fatal("daemon_status opt-in capability was not advertised")
	}
	quotaOptIn := call(t, svc, "quota-opt-in", "hello", map[string]any{"quota_selection": true})
	wantQuotaHello := `{"protocol_version":1,"self_update":false,"capabilities":{"fixture_only":true,"progress":false,"cancellation":false,"quota_selection":true}}`
	if quotaOptIn.Error != nil || string(quotaOptIn.Result) != wantQuotaHello {
		t.Fatalf("legacy quota hello bytes changed: %s error=%+v", quotaOptIn.Result, quotaOptIn.Error)
	}

	read := svc.Handle(context.Background(), makeRequest(t, "read", "daemon.status.read", map[string]any{}))
	if read.Error != nil {
		t.Fatalf("daemon.status.read failed: %+v", read.Error)
	}
}

func TestDaemonHelloPreservesNegotiatedQuotaSelection(t *testing.T) {
	svc := newDaemonStatusService(t, t.TempDir())
	input := strings.NewReader(
		`{"version":1,"kind":"request","id":"quota","op":"hello","payload":{"quota_selection":true}}` + "\n" +
			`{"version":1,"kind":"request","id":"daemon","op":"hello","payload":{"daemon_status":true}}` + "\n" +
			`{"version":1,"kind":"request","id":"read","op":"quota.selection.read","payload":{}}` + "\n")
	var output bytes.Buffer
	if err := Serve(context.Background(), svc, input, &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	for range 3 {
		var response Response
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.Error != nil {
			t.Fatalf("daemon hello cleared quota negotiation: %+v", response.Error)
		}
	}
}

func TestDaemonStatusReadEnumeratesCLIProfilesAndKeepsCollectionUnknown(t *testing.T) {
	root := t.TempDir()
	writeDaemonProfile(t, root, true, `"auth_token":"private-auth-sentinel"`)
	writeDaemonStates(t, root, daemonStateFile{Files: map[string]daemonFileState{
		"/private/account/session.jsonl": {LastSynced: time.Date(2026, 10, 12, 11, 30, 0, 0, time.UTC), SkippedAtFirstSync: 12},
	}})
	writeRawDaemonState(t, filepath.Join(root, "sync-state.json"), `{"files":{"/private/account/session.jsonl":{"last_synced":"2026-10-12T11:30:00Z","skipped_at_first_sync":12,"cwd":"/private/cwd","account_id":"private-account-sentinel"}},"failure_text":"private-error-sentinel"}`)
	writeDaemonProfile(t, filepath.Join(root, "profiles", "alpha"), true, "")
	writeEmptyDaemonStates(t, filepath.Join(root, "profiles", "alpha"))
	writeDaemonProfile(t, filepath.Join(root, "profiles", "default"), false, "")
	writeEmptyDaemonStates(t, filepath.Join(root, "profiles", "default"))
	writeDaemonProfile(t, filepath.Join(root, "profiles", "beta"), true, "")
	writeEmptyDaemonStates(t, filepath.Join(root, "profiles", "beta"))
	for _, name := range []string{"claude-4", "devtest", "local-dev"} {
		writeDaemonProfile(t, filepath.Join(root, "profiles", name), true, "")
		writeEmptyDaemonStates(t, filepath.Join(root, "profiles", name))
	}

	svc := newDaemonStatusService(t, root)
	svc.daemonLockProbe = func(string) daemonLockState { return daemonLockFree }
	response := call(t, svc, "read", "daemon.status.read", map[string]any{})
	if response.Error != nil {
		t.Fatalf("daemon status read error: %+v", response.Error)
	}
	var got daemonStatusResult
	if err := json.Unmarshal(response.Result, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Complete || len(got.Profiles) != 7 {
		t.Fatalf("complete=%v profiles=%d", got.Complete, len(got.Profiles))
	}
	wantIDs := []string{"default", "named:alpha", "named:beta", "named:claude-4", "named:default", "named:devtest", "named:local-dev"}
	for i, want := range wantIDs {
		if got.Profiles[i].ID != want {
			t.Fatalf("profile[%d].id=%q want=%q", i, got.Profiles[i].ID, want)
		}
	}
	if got.Profiles[0].Daemon.State != "stopped" || got.Profiles[2].Collection.State != "unknown" {
		t.Fatalf("daemon/collection states=%+v %+v", got.Profiles[0].Daemon, got.Profiles[2].Collection)
	}
	if got.Profiles[0].Collection.Counters.KnownFiles == nil || *got.Profiles[0].Collection.Counters.KnownFiles != 1 ||
		got.Profiles[0].Collection.Counters.FirstSyncSkippedBytes == nil || *got.Profiles[0].Collection.Counters.FirstSyncSkippedBytes != 12 {
		t.Fatalf("persisted counters were not projected: %+v", got.Profiles[0].Collection.Counters)
	}
	if got.Profiles[0].Collection.LastStateAdvanceAt == nil || *got.Profiles[0].Collection.LastStateAdvanceAt != "2026-10-12T11:30:00Z" {
		t.Fatalf("last state advance = %v", got.Profiles[0].Collection.LastStateAdvanceAt)
	}
	encoded, _ := json.Marshal(got)
	if bytes.Contains(encoded, []byte("private-auth-sentinel")) || bytes.Contains(encoded, []byte("/private/account")) {
		t.Fatalf("private profile/state fields leaked: %s", encoded)
	}
	if bytes.Contains(encoded, []byte("private-cwd")) || bytes.Contains(encoded, []byte("private-account-sentinel")) || bytes.Contains(encoded, []byte("private-error-sentinel")) {
		t.Fatalf("private state detail leaked: %s", encoded)
	}
}

func TestDaemonStatusReadSeparatesFailureStallBlockedDisabledAndPartialErrors(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	writeDaemonProfile(t, root, true, "")
	writeRawDaemonState(t, filepath.Join(root, "sync-state.json"), `{"files":{},"transport_failure":{"class":"server","first_failed_at":"2026-10-12T11:30:00Z","last_failed_at":"2026-10-12T11:59:00Z","files_failed":3,"last_error":"private-error-sentinel"}}`)
	for _, name := range []string{"codex-sync-state.json", "gjc-sync-state.json", "omo-sync-state.json"} {
		writeRawDaemonState(t, filepath.Join(root, name), `{"files":{}}`)
	}
	writeDaemonProfile(t, filepath.Join(root, "profiles", "blocked"), true, "")
	writeDaemonStates(t, filepath.Join(root, "profiles", "blocked"), daemonStateFile{Files: map[string]daemonFileState{"/session": {BlockedByBodyLimit: true}}})
	for _, name := range []string{"codex-sync-state.json", "gjc-sync-state.json", "omo-sync-state.json"} {
		writeRawDaemonState(t, filepath.Join(root, "profiles", "blocked", name), `{"files":{}}`)
	}
	writeDaemonProfile(t, filepath.Join(root, "profiles", "disabled"), false, "")
	writeEmptyDaemonStates(t, filepath.Join(root, "profiles", "disabled"))
	writeDaemonProfile(t, filepath.Join(root, "profiles", "failing"), true, "")
	writeRawDaemonState(t, filepath.Join(root, "profiles", "failing", "sync-state.json"), `{"files":{},"transport_failure":{"class":"transport","first_failed_at":"2026-10-12T11:50:00Z","last_failed_at":"2026-10-12T11:59:00Z","files_failed":1}}`)
	writeEmptyDaemonStatesExcept(t, filepath.Join(root, "profiles", "failing"), "sync-state.json")
	writeDaemonProfile(t, filepath.Join(root, "profiles", "partial"), true, "")
	writeRawDaemonState(t, filepath.Join(root, "profiles", "partial", "sync-state.json"), `{"files":{"x":{"last_synced":"2027-01-01T00:00:00Z"}}}`)
	writeEmptyDaemonStatesExcept(t, filepath.Join(root, "profiles", "partial"), "sync-state.json")

	svc := newDaemonStatusService(t, root)
	svc.now = func() time.Time { return now }
	svc.daemonLockProbe = func(string) daemonLockState { return daemonLockFree }
	response := call(t, svc, "read", "daemon.status.read", map[string]any{})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	var got daemonStatusResult
	if err := json.Unmarshal(response.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Complete {
		t.Fatal("future timestamp did not mark the aggregate incomplete")
	}
	byID := make(map[string]daemonProfileStatus, len(got.Profiles))
	for _, p := range got.Profiles {
		byID[p.ID] = p
	}
	if byID["default"].Collection.State != "stalled" || byID["default"].Collection.Failure == nil || byID["default"].Collection.Failure.Class != "server" {
		t.Fatalf("failure projection = %+v", byID["default"].Collection)
	}
	if byID["named:blocked"].Collection.State != "blocked" {
		t.Fatalf("blocked state = %+v", byID["named:blocked"].Collection)
	}
	if byID["named:disabled"].Daemon.State != "disabled" || byID["named:disabled"].Collection.State != "disabled" {
		t.Fatalf("disabled state = %+v", byID["named:disabled"])
	}
	if byID["named:failing"].Collection.State != "failing" || byID["named:failing"].Collection.Failure == nil || byID["named:failing"].Collection.Failure.Class != "transport" {
		t.Fatalf("failing state = %+v", byID["named:failing"].Collection)
	}
	if byID["named:partial"].Collection.State != "unknown" || byID["named:partial"].ErrorCode == nil {
		t.Fatalf("future timestamp was not preserved as an error: %+v", byID["named:partial"])
	}
	encoded, _ := json.Marshal(got)
	if bytes.Contains(encoded, []byte("private-error-sentinel")) {
		t.Fatalf("raw sync error leaked: %s", encoded)
	}
}

func TestDaemonStatusPartialBodyLimitRemainsBlockedWithUnknownTotals(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		stateFile   string
		failure     string
		wantFailure bool
	}{
		{name: "primary provider state", stateFile: "sync-state.json"},
		{name: "Codex provider state", stateFile: "codex-sync-state.json"},
		{name: "GJC provider state", stateFile: "gjc-sync-state.json"},
		{name: "OMO provider state", stateFile: "omo-sync-state.json"},
		{
			name:        "blocked with transport failure",
			stateFile:   "sync-state.json",
			failure:     `,"transport_failure":{"class":"transport","first_failed_at":"2026-10-12T11:30:00Z","last_failed_at":"2026-10-12T11:59:00Z","files_failed":1}`,
			wantFailure: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeDaemonProfile(t, root, true, "")
			writeRawDaemonState(t, filepath.Join(root, tc.stateFile),
				`{"files":{"/session":{"blocked_by_body_limit":true}}`+tc.failure+`}`)
			svc, err := New(Config{
				AppSupportRoot: t.TempDir(),
				DaemonRoot:     root,
				Now:            func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			svc.daemonLockProbe = func(string) daemonLockState { return daemonLockFree }

			result := readDaemonStatusResult(t, svc)
			profile := result.Profiles[0]
			if profile.Collection.State != "blocked" {
				t.Fatalf("partial body-limit state=%q, want blocked: %+v", profile.Collection.State, profile.Collection)
			}
			if profile.Collection.Counters.KnownFiles != nil ||
				profile.Collection.Counters.FirstSyncSkippedBytes != nil ||
				profile.Collection.Counters.BodyLimitBlockedFiles != nil {
				t.Fatalf("incomplete state totals were reported as exact counters: %+v", profile.Collection.Counters)
			}
			if (profile.Collection.Failure != nil) != tc.wantFailure {
				t.Fatalf("retained failure=%+v, want present=%v", profile.Collection.Failure, tc.wantFailure)
			}
		})
	}
}

func TestDaemonStatusFixtureBlockedAllowsUnknownCounterUnlessContradicted(t *testing.T) {
	truth := true
	zero := int64(0)
	result := daemonStatusResult{
		ObservedAt: "2026-10-12T12:00:00Z",
		Profiles: []daemonProfileStatus{{
			ID:          "default",
			Name:        "default",
			SyncEnabled: &truth,
			Daemon:      daemonOperationStatus{State: "stopped", Evidence: "lock_free"},
			Collection:  daemonCollectionStatus{State: "blocked", Counters: daemonCollectionCounters{}},
		}},
	}
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	if !validDaemonStatusFixture(result, now) {
		t.Fatal("blocked fixture with unknown totals was rejected")
	}
	result.Profiles[0].Collection.Counters.KnownFiles = &zero
	if validDaemonStatusFixture(result, now) {
		t.Fatal("blocked fixture contradicted by zero known files was accepted")
	}
	result.Profiles[0].Collection.Counters.KnownFiles = nil
	result.Profiles[0].Collection.Counters.BodyLimitBlockedFiles = &zero
	if validDaemonStatusFixture(result, now) {
		t.Fatal("blocked fixture contradicted by zero blocked files was accepted")
	}
	one := int64(1)
	two := int64(2)
	result.Profiles[0].Collection.Counters.KnownFiles = &one
	result.Profiles[0].Collection.Counters.BodyLimitBlockedFiles = &two
	if validDaemonStatusFixture(result, now) {
		t.Fatal("blocked fixture with more blocked than known files was accepted")
	}
}

func TestDaemonStatusReadLockEvidenceOverridesConfigurationAndStaleMarkers(t *testing.T) {
	root := t.TempDir()
	writeDaemonProfile(t, root, false, "")
	writeEmptyDaemonStates(t, root)
	staleDir := filepath.Join(root, "profiles", "stale")
	writeDaemonProfile(t, staleDir, true, "")
	writeEmptyDaemonStates(t, staleDir)
	if err := os.WriteFile(filepath.Join(staleDir, "sync.pid"), []byte("99999999"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, "sync-runtime.json"), []byte(`{"pid":99999999,"instance_id":"foreign","started_at":"2020-01-01T00:00:00Z","version":"v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sync.pid"), []byte("99999999"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sync-runtime.json"), []byte(`{"pid":99999999,"instance_id":"stale","started_at":"2020-01-01T00:00:00Z","version":"v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := newDaemonStatusService(t, root)
	svc.daemonLockProbe = func(lockPath string) daemonLockState {
		if strings.Contains(lockPath, string(filepath.Join("profiles", "stale"))) {
			return daemonLockFree
		}
		return daemonLockHeld
	}
	read := call(t, svc, "read", "daemon.status.read", map[string]any{})
	var got daemonStatusResult
	if err := json.Unmarshal(read.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Profiles[0].Daemon.State != "running" || got.Profiles[0].Daemon.Evidence != "lock_held" {
		t.Fatalf("held lock hidden by disabled config: %+v", got.Profiles[0].Daemon)
	}
	if got.Profiles[1].ID != "named:stale" || got.Profiles[1].Daemon.State != "stopped" {
		t.Fatalf("foreign PID/runtime promoted running without a held lock: %+v", got.Profiles[1].Daemon)
	}
	svc.daemonLockProbe = func(string) daemonLockState { return daemonLockFree }
	read = call(t, svc, "read", "daemon.status.read", map[string]any{})
	if err := json.Unmarshal(read.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Profiles[0].Daemon.State != "disabled" {
		t.Fatalf("free lock with disabled config = %+v", got.Profiles[0].Daemon)
	}
	svc.daemonLockProbe = func(string) daemonLockState { return daemonLockMissing }
	read = call(t, svc, "read", "daemon.status.read", map[string]any{})
	if err := json.Unmarshal(read.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Profiles[0].Daemon.State != "unknown" {
		t.Fatalf("runtime/PID traces promoted missing lock state: %+v", got.Profiles[0].Daemon)
	}
}

func TestDaemonStatusReadRejectsUnsafeCorruptDuplicateAndOversizedInputs(t *testing.T) {
	root := t.TempDir()
	writeDaemonProfile(t, root, true, "")
	writeEmptyDaemonStates(t, root)
	svc := newDaemonStatusService(t, root)
	svc.daemonLockProbe = func(string) daemonLockState { return daemonLockFree }

	profilePath := filepath.Join(root, "profile.json")
	if err := os.WriteFile(profilePath, []byte(`{"options":{"sync_enabled":true,"sync_enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readDaemonStatusResult(t, svc)
	if got.Profiles[0].SyncEnabled != nil || got.Profiles[0].ErrorCode == nil || *got.Profiles[0].ErrorCode != "invalid_profile" {
		t.Fatalf("duplicate profile key accepted: %+v", got.Profiles[0])
	}
	writeDaemonProfile(t, root, true, "")
	writeRawDaemonState(t, filepath.Join(root, "sync-state.json"), `{"files":{},"files":{}}`)
	got = readDaemonStatusResult(t, svc)
	if got.Profiles[0].ErrorCode == nil || got.Profiles[0].Collection.Counters.KnownFiles != nil || got.Complete {
		t.Fatalf("duplicate state keys did not become unknown: %+v", got.Profiles[0])
	}
	writeEmptyDaemonStates(t, root)
	if err := os.Remove(filepath.Join(root, "omo-sync-state.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "sync-state.json"), filepath.Join(root, "omo-sync-state.json")); err != nil {
		t.Fatal(err)
	}
	got = readDaemonStatusResult(t, svc)
	if got.Profiles[0].ErrorCode == nil || *got.Profiles[0].ErrorCode != "unsafe_path" || got.Complete {
		t.Fatalf("state symlink not rejected: %+v", got.Profiles[0])
	}
	_ = os.Remove(filepath.Join(root, "omo-sync-state.json"))
	if err := os.WriteFile(filepath.Join(root, "omo-sync-state.json"), bytes.Repeat([]byte(" "), maxDaemonInputFile+1), 0o600); err != nil {
		t.Fatal(err)
	}
	got = readDaemonStatusResult(t, svc)
	if got.Profiles[0].ErrorCode == nil || !strings.Contains(*got.Profiles[0].ErrorCode, "input_too_large") || got.Complete {
		t.Fatalf("oversized state was accepted: %+v", got.Profiles[0])
	}
}

func TestDaemonStatusReadHonorsTotalInputCap(t *testing.T) {
	root := t.TempDir()
	writeDaemonProfile(t, root, true, "")
	state := []byte(`{"files":{}}`)
	state = append(state, bytes.Repeat([]byte(" "), maxDaemonInputFile-len(state))...)
	for _, name := range []string{"sync-state.json", "codex-sync-state.json", "gjc-sync-state.json", "omo-sync-state.json"} {
		if err := os.WriteFile(filepath.Join(root, name), state, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	svc := newDaemonStatusService(t, root)
	got := readDaemonStatusResult(t, svc)
	if got.Complete || got.Profiles[0].ErrorCode == nil || *got.Profiles[0].ErrorCode != "input_limit_exceeded" {
		t.Fatalf("total input cap not represented: complete=%v profile=%+v", got.Complete, got.Profiles[0])
	}
}

func TestDaemonStatus_ProfileCapAndOrder(t *testing.T) {
	root := t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	for i := 0; i < maxDaemonProfiles+1; i++ {
		name := fmt.Sprintf("profile%03d", i)
		if err := os.MkdirAll(filepath.Join(profilesDir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	svc := newDaemonStatusService(t, root)
	svc.daemonLockProbe = func(string) daemonLockState { return daemonLockMissing }
	result := readDaemonStatusResult(t, svc)
	if result.Complete || len(result.Profiles) != maxDaemonProfiles || result.Profiles[0].ID != "default" || result.Profiles[1].ID != "named:profile000" || result.Profiles[len(result.Profiles)-1].ID != "named:profile126" {
		t.Fatalf("profile cap/order complete=%v count=%d first=%q last=%q", result.Complete, len(result.Profiles), result.Profiles[1].ID, result.Profiles[len(result.Profiles)-1].ID)
	}
}

func TestDaemonStatusFileRaceIsUnknown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"files":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, failure := readDaemonFileWithHook(path, maxDaemonInputFile, func(*os.File) {
		if err := os.Rename(path, path+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"files":{"replacement":{}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if failure == nil || failure.code != "snapshot_race" {
		t.Fatalf("file replacement failure=%v want snapshot_race", failure)
	}
}

func TestDaemonStatusFixtureIsTypedAndReadOnly(t *testing.T) {
	fixtureRoot := t.TempDir()
	fixtureData := []byte(`{"observed_at":"2026-10-12T12:00:00Z","complete":true,"profiles":[{"id":"default","name":"default","sync_enabled":true,"daemon":{"state":"unknown","evidence":"insufficient","version":null,"started_at":null},"collection":{"state":"unknown","last_state_advance_at":null,"failure":null,"counters":{"known_files":null,"first_sync_skipped_bytes":null,"body_limit_blocked_files":null}},"error_code":null}]}`)
	fixturePath := filepath.Join(fixtureRoot, "daemon-status.json")
	if err := os.WriteFile(fixturePath, fixtureData, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(fixtureRoot)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{AppSupportRoot: t.TempDir(), FixtureRoot: fixtureRoot, Now: func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	response := call(t, svc, "read", "daemon.status.read", map[string]any{})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	after, err := os.ReadDir(fixtureRoot)
	if err != nil {
		t.Fatal(err)
	}
	dataAfter, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) || !bytes.Equal(fixtureData, dataAfter) || before[0].Name() != after[0].Name() {
		t.Fatal("daemon fixture filesystem changed during read")
	}
	if err := os.Remove(fixturePath); err != nil {
		t.Fatal(err)
	}
	response = call(t, svc, "missing", "daemon.status.read", map[string]any{})
	if response.Error == nil || response.Error.Code != "feature_unavailable" {
		t.Fatalf("missing fixture error=%+v", response.Error)
	}
	if err := os.WriteFile(fixturePath, []byte(`{"observed_at":"2026-10-12T12:00:00Z","observed_at":"2026-10-12T12:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	response = call(t, svc, "corrupt", "daemon.status.read", map[string]any{})
	var unknown daemonStatusResult
	if response.Error != nil || json.Unmarshal(response.Result, &unknown) != nil || unknown.Complete || len(unknown.Profiles) != 0 {
		t.Fatalf("corrupt fixture was not unknown/incomplete: response=%+v result=%+v", response.Error, unknown)
	}
	if err := os.WriteFile(fixturePath, bytes.Repeat([]byte(" "), maxDaemonInputFile+1), 0o600); err != nil {
		t.Fatal(err)
	}
	response = call(t, svc, "oversized", "daemon.status.read", map[string]any{})
	if response.Error != nil || json.Unmarshal(response.Result, &unknown) != nil || unknown.Complete || len(unknown.Profiles) != 0 {
		t.Fatalf("oversized fixture was not unknown/incomplete: response=%+v result=%+v", response.Error, unknown)
	}
}

func TestDaemonStatusFixtureRejectsTimestampsAfterObservation(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	observedAt := now.Add(-time.Hour).Format(time.RFC3339Nano)
	futureOfObservation := now.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	beforeObservation := now.Add(-90 * time.Minute).Format(time.RFC3339Nano)
	futureObservedAt := now.Add(time.Second).Format(time.RFC3339Nano)

	stamp := func(value string) *string { return &value }
	baseFixture := func() daemonStatusResult {
		return daemonStatusResult{
			ObservedAt: observedAt,
			Complete:   true,
			Profiles: []daemonProfileStatus{{
				ID:   "default",
				Name: "default",
				Daemon: daemonOperationStatus{
					State: "unknown", Evidence: "insufficient",
				},
				Collection: daemonCollectionStatus{
					State: "unknown", Counters: daemonCollectionCounters{},
				},
			}},
		}
	}
	setFailure := func(result *daemonStatusResult, first, last string) {
		result.Profiles[0].Collection.State = "failing"
		result.Profiles[0].Collection.Failure = &daemonFailureStatus{
			Class: "transport", FirstFailedAt: first, LastFailedAt: last, FilesFailed: 1,
		}
	}
	cases := []struct {
		name   string
		mutate func(*daemonStatusResult)
	}{
		{
			name: "observed_at_after_now",
			mutate: func(result *daemonStatusResult) {
				result.ObservedAt = futureObservedAt
			},
		},
		{
			name: "started_at_after_observation",
			mutate: func(result *daemonStatusResult) {
				result.Profiles[0].Daemon.StartedAt = stamp(futureOfObservation)
			},
		},
		{
			name: "last_state_advance_at_after_observation",
			mutate: func(result *daemonStatusResult) {
				result.Profiles[0].Collection.LastStateAdvanceAt = stamp(futureOfObservation)
			},
		},
		{
			name: "first_failed_at_after_observation",
			mutate: func(result *daemonStatusResult) {
				setFailure(result, futureOfObservation, futureOfObservation)
			},
		},
		{
			name: "last_failed_at_after_observation",
			mutate: func(result *daemonStatusResult) {
				setFailure(result, beforeObservation, futureOfObservation)
			},
		},
	}

	fixtureRoot := t.TempDir()
	svc, err := New(Config{
		AppSupportRoot: t.TempDir(),
		FixtureRoot:    fixtureRoot,
		Now:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(fixtureRoot, "daemon-status.json")
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := baseFixture()
			test.mutate(&fixture)
			data, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixturePath, data, 0o600); err != nil {
				t.Fatal(err)
			}

			response := call(t, svc, test.name, "daemon.status.read", map[string]any{})
			if response.Error != nil {
				t.Fatalf("invalid fixture should use the established unknown result: %+v", response.Error)
			}
			var got daemonStatusResult
			if err := json.Unmarshal(response.Result, &got); err != nil {
				t.Fatal(err)
			}
			if got.Complete || len(got.Profiles) != 0 || got.ObservedAt != now.Format(time.RFC3339Nano) {
				t.Fatalf("invalid fixture was accepted or exposed: %+v", got)
			}
		})
	}
}

func TestDaemonStatusReadRequiresEmptyPayloadAndDoesNotUseActualHomeInFixtureMode(t *testing.T) {
	svc, err := New(Config{AppSupportRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	response := call(t, svc, "read", "daemon.status.read", map[string]any{"root": "/private"})
	if response.Error == nil || response.Error.Code != "invalid_payload" {
		t.Fatalf("nonempty payload error=%+v", response.Error)
	}
	response = call(t, svc, "read", "daemon.status.read", map[string]any{})
	if response.Error == nil || response.Error.Code != "feature_unavailable" {
		t.Fatalf("fixture mode without fixture fell back to home: %+v", response.Error)
	}
}

func TestDaemonStatusReadNeverCallsQuotaProvider(t *testing.T) {
	root := t.TempDir()
	writeDaemonProfile(t, root, true, "")
	writeEmptyDaemonStates(t, root)
	reader := &countedDaemonStatusQuotaReader{}
	svc, err := New(Config{AppSupportRoot: t.TempDir(), DaemonRoot: root, ProviderMode: true, ProviderReader: reader})
	if err != nil {
		t.Fatal(err)
	}
	svc.daemonLockProbe = func(string) daemonLockState { return daemonLockMissing }
	if response := call(t, svc, "read", "daemon.status.read", map[string]any{}); response.Error != nil {
		t.Fatal(response.Error)
	}
	if reader.calls != 0 {
		t.Fatalf("daemon status called provider quota reader %d times", reader.calls)
	}
}

type countedDaemonStatusQuotaReader struct{ calls int }

func (r *countedDaemonStatusQuotaReader) ReadClaudeQuota(context.Context, string) (*usage.Response, error) {
	r.calls++
	return nil, errors.New("unexpected quota read")
}

func (r *countedDaemonStatusQuotaReader) ReadCodexQuota(context.Context, string) (*codexappserver.Snapshot, error) {
	r.calls++
	return nil, errors.New("unexpected quota read")
}

func TestDaemonStatusNewDoesNotCreateExplicitDaemonRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "daemon-root")
	if _, err := New(Config{AppSupportRoot: t.TempDir(), DaemonRoot: missing}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("New created daemon root: %v", err)
	}
}

func TestDaemonStatusNewRejectsSupportRootInsideReadOnlyDaemonRoot(t *testing.T) {
	base := t.TempDir()
	appSupport := filepath.Join(base, "desktop-support")
	if _, err := New(Config{AppSupportRoot: appSupport, DaemonRoot: base}); err == nil {
		t.Fatal("overlapping app-support and daemon roots were accepted")
	}
	if _, err := os.Lstat(appSupport); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed New wrote into read-only daemon root: %v", err)
	}
}

func TestDaemonStatusProviderModeDefaultsToCLIProfileRootWithoutReadingIt(t *testing.T) {
	svc, err := New(Config{AppSupportRoot: t.TempDir(), ProviderMode: true})
	if err != nil {
		t.Fatal(err)
	}
	if svc.daemonRoot != profile.DefaultDir() {
		t.Fatalf("provider daemon root=%q want profile default %q", svc.daemonRoot, profile.DefaultDir())
	}
}

func TestDaemonStatusVersionMatchesDesktopSafeVersionBoundary(t *testing.T) {
	for _, value := range []string{"v0.7.60", "dev", "A" + strings.Repeat("x", 63)} {
		if !safeDaemonVersion(value) {
			t.Errorf("safe version %q was rejected", value)
		}
	}
	for _, value := range []string{"", "-v1", "_v1", "A" + strings.Repeat("x", 64), "private/path"} {
		if safeDaemonVersion(value) {
			t.Errorf("unsafe version %q was accepted", value)
		}
	}
}

func TestDaemonStatusReadRejectsSymlinkDaemonRootAndLock(t *testing.T) {
	actualRoot := t.TempDir()
	writeDaemonProfile(t, actualRoot, true, "")
	writeEmptyDaemonStates(t, actualRoot)
	rootAlias := filepath.Join(t.TempDir(), "daemon-root-link")
	if err := os.Symlink(actualRoot, rootAlias); err != nil {
		t.Fatal(err)
	}
	svc := newDaemonStatusService(t, rootAlias)
	result := readDaemonStatusResult(t, svc)
	if result.Profiles[0].SyncEnabled != nil || result.Profiles[0].Daemon.State != "unknown" || result.Profiles[0].ErrorCode == nil {
		t.Fatalf("symlink daemon root was read: %+v", result.Profiles[0])
	}

	lockTarget := filepath.Join(t.TempDir(), "foreign-lock-target")
	if err := os.WriteFile(lockTarget, []byte("do-not-touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(actualRoot, "sync.lock")
	if err := os.Symlink(lockTarget, lockPath); err != nil {
		t.Fatal(err)
	}
	svc = newDaemonStatusService(t, actualRoot)
	result = readDaemonStatusResult(t, svc)
	if result.Profiles[0].Daemon.State != "unknown" || result.Profiles[0].ErrorCode == nil {
		t.Fatalf("symlink lock was followed: %+v", result.Profiles[0])
	}
	data, err := os.ReadFile(lockTarget)
	if err != nil || string(data) != "do-not-touch" {
		t.Fatalf("symlink target changed: data=%q err=%v", data, err)
	}
}

func newDaemonStatusService(t *testing.T, root string) *Service {
	t.Helper()
	svc, err := New(Config{AppSupportRoot: t.TempDir(), DaemonRoot: root, Now: func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func writeDaemonProfile(t *testing.T, dir string, enabled bool, extra string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"options":{"sync_enabled":` + map[bool]string{true: "true", false: "false"}[enabled] + `}`
	if extra != "" {
		data += `,"server":{` + extra + `}`
	}
	data += `}`
	if err := os.WriteFile(filepath.Join(dir, "profile.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeDaemonStates(t *testing.T, dir string, claude daemonStateFile) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(claude)
	if err != nil {
		t.Fatal(err)
	}
	writeRawDaemonState(t, filepath.Join(dir, "sync-state.json"), string(data))
	for _, name := range []string{"codex-sync-state.json", "gjc-sync-state.json", "omo-sync-state.json"} {
		writeRawDaemonState(t, filepath.Join(dir, name), `{"files":{}}`)
	}
}

func writeEmptyDaemonStates(t *testing.T, dir string) {
	t.Helper()
	writeDaemonStates(t, dir, daemonStateFile{Files: map[string]daemonFileState{}})
}

func writeEmptyDaemonStatesExcept(t *testing.T, dir, except string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sync-state.json", "codex-sync-state.json", "gjc-sync-state.json", "omo-sync-state.json"} {
		if name != except {
			writeRawDaemonState(t, filepath.Join(dir, name), `{"files":{}}`)
		}
	}
}

func writeRawDaemonState(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readDaemonStatusResult(t *testing.T, svc *Service) daemonStatusResult {
	t.Helper()
	response := call(t, svc, "read", "daemon.status.read", map[string]any{})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	var result daemonStatusResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
