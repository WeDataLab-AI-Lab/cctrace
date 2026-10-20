package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	profilepkg "cctrace/internal/profile"
)

type responseView struct {
	Version int             `json:"version"`
	Kind    string          `json:"kind"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *ErrorObject    `json:"error"`
}

func newTestService(t *testing.T, appSupport, fixtureRoot string) *Service {
	t.Helper()
	svc, err := New(Config{
		AppSupportRoot: appSupport,
		FixtureRoot:    fixtureRoot,
		Now:            func() time.Time { return time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

func makeRequest(t *testing.T, id, op string, payload any) Request {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return Request{Version: ProtocolVersion, Kind: "request", ID: id, Op: op, Payload: raw}
}

func call(t *testing.T, svc *Service, id, op string, payload any) responseView {
	t.Helper()
	response := svc.Handle(context.Background(), makeRequest(t, id, op, payload))
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var view responseView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if view.Version != ProtocolVersion || view.Kind != "response" || view.ID != id {
		t.Fatalf("response envelope = %+v", view)
	}
	return view
}

func fixtureScenario(name string) string {
	path, err := filepath.Abs(filepath.Join("..", "..", "tests", "desktop", "fixtures", "v1", "ipc", "quota", name))
	if err != nil {
		return ""
	}
	return path
}

func TestGoldenV1Contract(t *testing.T) {
	svc := newTestService(t, t.TempDir(), "")
	requests, err := os.ReadFile(filepath.Join("..", "..", "tests", "desktop", "fixtures", "v1", "ipc", "golden", "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(filepath.Join("..", "..", "tests", "desktop", "fixtures", "v1", "ipc", "golden", "responses.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var actual bytes.Buffer
	if err := Serve(context.Background(), svc, bytes.NewReader(requests), &actual); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if !bytes.Equal(actual.Bytes(), expected) {
		t.Fatalf("golden response mismatch\n--- got ---\n%s\n--- want ---\n%s", actual.Bytes(), expected)
	}
}

func TestProfileMappingCRUDPreservesMappedHomes(t *testing.T) {
	base := t.TempDir()
	claudeHome := filepath.Join(base, "Claude Home 工作")
	codexHome := filepath.Join(base, "Codex Home 资料")
	if err := os.MkdirAll(claudeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(claudeHome, "read-only sentinel.json")
	secret := []byte("fixture-only-provider-marker\n")
	if err := os.WriteFile(sentinel, secret, 0o400); err != nil {
		t.Fatal(err)
	}
	codexSentinel := filepath.Join(codexHome, "session marker.jsonl")
	if err := os.WriteFile(codexSentinel, []byte("fixture-session-marker\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	claudeBefore := fileDigest(t, sentinel)
	codexBefore := fileDigest(t, codexSentinel)

	claudeAlias := filepath.Join(base, "Claude Alias")
	if err := os.Symlink(claudeHome, claudeAlias); err != nil {
		t.Fatal(err)
	}
	resolvedClaudeHome, err := filepath.EvalSymlinks(claudeHome)
	if err != nil {
		t.Fatal(err)
	}
	resolvedCodexHome, err := filepath.EvalSymlinks(codexHome)
	if err != nil {
		t.Fatal(err)
	}
	appSupport := filepath.Join(base, "cctrace app support")
	svc := newTestService(t, appSupport, "")
	added := call(t, svc, "add-1", "profiles.add", map[string]string{
		"name":        "work",
		"claude_home": claudeAlias,
		"codex_home":  codexHome,
	})
	if added.Error != nil {
		t.Fatalf("profiles.add error: %+v", added.Error)
	}
	var addResult struct {
		Profile struct {
			Name       string `json:"name"`
			ClaudeHome string `json:"claude_home"`
			CodexHome  string `json:"codex_home"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(added.Result, &addResult); err != nil {
		t.Fatal(err)
	}
	if addResult.Profile.Name != "work" || addResult.Profile.ClaudeHome != resolvedClaudeHome || addResult.Profile.CodexHome != resolvedCodexHome {
		t.Fatalf("added mapping = %+v", addResult.Profile)
	}

	profilePath := filepath.Join(appSupport, "profiles", "work", "profile.json")
	stored, err := profilepkg.LoadFrom(profilePath)
	if err != nil {
		t.Fatalf("load isolated core profile: %v", err)
	}
	if stored.ClaudeConfigDir != resolvedClaudeHome || len(stored.Options.CodexDirs) != 1 || stored.Options.CodexDirs[0] != resolvedCodexHome {
		t.Fatalf("stored mapping = claude %q codex %v", stored.ClaudeConfigDir, stored.Options.CodexDirs)
	}
	if stored.Options.SyncEnabled || stored.Options.CodexSyncEnabled {
		t.Fatalf("P0 mapping unexpectedly enables sync: %+v", stored.Options)
	}
	ownedBytes, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	stored.Server.AuthToken = "fixture-only-secret-token"
	if err := profilepkg.SaveTo(stored, profilePath); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Dir(profilePath)); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("profile directory mode = %v, err=%v; want 0700", info, err)
		}
		if info, err := os.Stat(profilePath); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("profile file mode = %v, err=%v; want 0600", info, err)
		}
	}

	listed := call(t, svc, "list-1", "profiles.list", map[string]any{})
	if listed.Error != nil {
		t.Fatalf("profiles.list error: %+v", listed.Error)
	}
	if bytes.Contains(listed.Result, []byte("fixture-only-secret-token")) {
		t.Fatal("profiles.list exposed an internal profile credential")
	}
	var listResult struct {
		Profiles []struct {
			Name       string `json:"name"`
			ClaudeHome string `json:"claude_home"`
			CodexHome  string `json:"codex_home"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(listed.Result, &listResult); err != nil {
		t.Fatal(err)
	}
	if len(listResult.Profiles) != 1 || listResult.Profiles[0].Name != "work" {
		t.Fatalf("profiles.list = %+v", listResult.Profiles)
	}

	sharedBytes, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	rejected := call(t, svc, "remove-shared", "profiles.remove", map[string]string{"name": "work"})
	if rejected.Error == nil || rejected.Error.Code != "unsafe_storage" {
		t.Fatal("shared credential profile removal must require manual repair")
	}
	afterRejected, err := os.ReadFile(profilePath)
	if err != nil || !bytes.Equal(sharedBytes, afterRejected) {
		t.Fatal("shared credential profile changed during rejected removal")
	}
	if err := os.WriteFile(profilePath, ownedBytes, 0600); err != nil {
		t.Fatal(err)
	}
	removed := call(t, svc, "remove-1", "profiles.remove", map[string]string{"name": "work"})
	if removed.Error != nil {
		t.Fatalf("profiles.remove error: %+v", removed.Error)
	}
	if fileDigest(t, sentinel) != claudeBefore || fileDigest(t, codexSentinel) != codexBefore {
		t.Fatal("removing a mapping changed a mapped-home sentinel")
	}
	if info, err := os.Stat(sentinel); err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("Claude sentinel changed or became writable: info=%v err=%v", info, err)
	}
	if info, err := os.Stat(codexSentinel); err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("Codex sentinel changed or became writable: info=%v err=%v", info, err)
	}
}

func TestProfileAddEmptyHomesDoesNotUseEnvironmentFallback(t *testing.T) {
	svc := newTestService(t, t.TempDir(), "")
	added := call(t, svc, "add-empty", "profiles.add", map[string]string{
		"name":        "unmapped",
		"claude_home": "",
		"codex_home":  "",
	})
	if added.Error != nil {
		t.Fatalf("explicitly empty homes should be accepted: %+v", added.Error)
	}
	var result struct {
		Profile struct {
			ClaudeHome string `json:"claude_home"`
			CodexHome  string `json:"codex_home"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(added.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Profile.ClaudeHome != "" || result.Profile.CodexHome != "" {
		t.Fatalf("empty mappings fell back to a home: %+v", result.Profile)
	}
}

func TestProfilePathsFailClosedAndStorageRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	appSupport := filepath.Join(base, "app support")
	svc := newTestService(t, appSupport, "")
	externalHome := filepath.Join(base, "external home")
	if err := os.MkdirAll(externalHome, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{"relative/home", filepath.Join(base, "missing home")} {
		response := call(t, svc, "bad-home", "profiles.add", map[string]string{
			"name":        "badhome",
			"claude_home": home,
			"codex_home":  "",
		})
		if response.Error == nil || response.Error.Code != "invalid_home" {
			t.Fatalf("home %q error = %+v", home, response.Error)
		}
		if strings.Contains(response.Error.Message, home) {
			t.Fatalf("home path leaked in error: %+v", response.Error)
		}
	}
	if err := os.MkdirAll(filepath.Join(appSupport, "profiles"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalHome, filepath.Join(appSupport, "profiles", "escape")); err != nil {
		t.Fatal(err)
	}
	response := call(t, svc, "escape", "profiles.add", map[string]string{
		"name":        "escape",
		"claude_home": "",
		"codex_home":  "",
	})
	if response.Error == nil || response.Error.Code != "unsafe_storage" {
		t.Fatalf("symlinked profile directory error = %+v", response.Error)
	}
	if _, err := os.Stat(filepath.Join(externalHome, "profile.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("profile write escaped into target: %v", err)
	}
}

func TestProfileStorageRootMustNotBeSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory symlink creation may require elevated privileges on Windows")
	}
	base := t.TempDir()
	actual := filepath.Join(base, "actual app support")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "app support link")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{AppSupportRoot: link}); err == nil {
		t.Fatal("New accepted a symlinked app-support root")
	}
}

func TestProfileRejectsAppSupportOverlappingMappedHome(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "agent home")
	appSupport := filepath.Join(home, "cctrace app support")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, appSupport, "")
	response := call(t, svc, "overlap", "profiles.add", map[string]string{
		"name":        "overlap",
		"claude_home": home,
		"codex_home":  "",
	})
	if response.Error == nil || response.Error.Code != "invalid_home" {
		t.Fatalf("overlapping storage/home error = %+v", response.Error)
	}
}

func TestQuotaReadDistinguishesFreshStaleUnauthorizedAndRateLimited(t *testing.T) {
	tests := []struct {
		fixture       string
		state         string
		errorCode     string
		wantWindows   int
		wantPercent   float64
		wantTimestamp bool
	}{
		{fixture: "fresh", state: "fresh", wantWindows: 1, wantPercent: 0, wantTimestamp: true},
		{fixture: "stale", state: "stale", wantWindows: 1, wantPercent: 42, wantTimestamp: true},
		{fixture: "401", state: "unavailable", errorCode: "authentication_required", wantWindows: 0},
		{fixture: "429", state: "stale", errorCode: "rate_limited", wantWindows: 1, wantPercent: 50, wantTimestamp: true},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			svc := newTestService(t, t.TempDir(), fixtureScenario(tc.fixture))
			response := call(t, svc, "quota-"+tc.fixture, "quota.read", map[string]string{"provider": "claude"})
			if response.Error != nil {
				t.Fatalf("quota.read protocol error: %+v", response.Error)
			}
			var got struct {
				Provider      string  `json:"provider"`
				State         string  `json:"state"`
				LastSuccessAt *string `json:"last_success_at"`
				Windows       []struct {
					Name        string  `json:"name"`
					Utilization float64 `json:"utilization"`
					ResetsAt    string  `json:"resets_at"`
				} `json:"windows"`
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Result, &got); err != nil {
				t.Fatal(err)
			}
			if got.Provider != "claude" || got.State != tc.state || len(got.Windows) != tc.wantWindows {
				t.Fatalf("quota result = %+v", got)
			}
			if (got.LastSuccessAt != nil) != tc.wantTimestamp {
				t.Fatalf("last_success_at = %v, want present=%v", got.LastSuccessAt, tc.wantTimestamp)
			}
			if tc.wantWindows > 0 && got.Windows[0].Utilization != tc.wantPercent {
				t.Fatalf("utilization = %v, want %v", got.Windows[0].Utilization, tc.wantPercent)
			}
			if tc.errorCode == "" && got.Error != nil {
				t.Fatalf("unexpected quota error: %+v", got.Error)
			}
			if tc.errorCode != "" && (got.Error == nil || got.Error.Code != tc.errorCode || got.Error.Message == "") {
				t.Fatalf("quota error = %+v, want code %q and safe message", got.Error, tc.errorCode)
			}
		})
	}
}

func TestQuotaReadWithoutFixtureIsUnavailableNotZero(t *testing.T) {
	svc := newTestService(t, t.TempDir(), "")
	response := call(t, svc, "quota-no-fixture", "quota.read", map[string]string{"provider": "codex"})
	if response.Error != nil {
		t.Fatalf("quota.read protocol error: %+v", response.Error)
	}
	var got struct {
		Provider string `json:"provider"`
		State    string `json:"state"`
		Windows  []any  `json:"windows"`
		Error    *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Provider != "codex" || got.State != "unavailable" || len(got.Windows) != 0 || got.Error == nil {
		t.Fatalf("missing fixture did not fail closed: %+v", got)
	}
}

func TestQuotaFutureTimestampFailsClosed(t *testing.T) {
	base := t.TempDir()
	fixtureRoot := filepath.Join(base, "fixtures")
	if err := os.MkdirAll(fixtureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"claude":{"state":"fresh","last_success_at":"2026-10-04T12:31:00Z","windows":[{"name":"five_hour","utilization":20,"resets_at":"2026-10-04T13:00:00Z"}],"error":null},"codex":{"state":"unavailable","last_success_at":null,"windows":[],"error":{"code":"authentication_required","message":"provider authorization is required"}}}`
	if err := os.WriteFile(filepath.Join(fixtureRoot, "quota.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, filepath.Join(base, "app support"), fixtureRoot)
	response := call(t, svc, "quota-future", "quota.read", map[string]string{"provider": "claude"})
	if response.Error != nil {
		t.Fatalf("future timestamp should be a fail-closed read model: %+v", response.Error)
	}
	var got struct {
		State         string  `json:"state"`
		LastSuccessAt *string `json:"last_success_at"`
		Windows       []any   `json:"windows"`
		Error         *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "unavailable" || got.LastSuccessAt != nil || len(got.Windows) != 0 || got.Error == nil || got.Error.Code != "invalid_fixture" {
		t.Fatalf("future timestamp result = %+v", got)
	}
}

func TestQuotaExplicitStaleCannotBecomeFresh(t *testing.T) {
	base := t.TempDir()
	fixtureRoot := filepath.Join(base, "fixtures")
	if err := os.MkdirAll(fixtureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"claude":{"state":"stale","last_success_at":"2026-10-04T12:20:00Z","windows":[{"name":"five_hour","utilization":20,"resets_at":"2026-10-04T13:00:00Z"}],"error":null},"codex":{"state":"unavailable","last_success_at":null,"windows":[],"error":{"code":"authentication_required","message":"provider authorization is required"}}}`
	if err := os.WriteFile(filepath.Join(fixtureRoot, "quota.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, filepath.Join(base, "app support"), fixtureRoot)
	response := call(t, svc, "quota-explicit-stale", "quota.read", map[string]string{"provider": "claude"})
	if response.Error != nil {
		t.Fatalf("quota.read protocol error: %+v", response.Error)
	}
	var got struct {
		State   string `json:"state"`
		Windows []any  `json:"windows"`
	}
	if err := json.Unmarshal(response.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "stale" || len(got.Windows) != 1 {
		t.Fatalf("recent explicit stale was promoted or discarded: %+v", got)
	}
}

func TestQuotaUnavailableWithSnapshotRemainsStale(t *testing.T) {
	base := t.TempDir()
	fixtureRoot := filepath.Join(base, "fixtures")
	if err := os.MkdirAll(fixtureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"claude":{"state":"unavailable","last_success_at":"2026-10-04T12:20:00Z","windows":[{"name":"five_hour","utilization":23,"resets_at":"2026-10-04T13:00:00Z"}],"error":null},"codex":{"state":"unavailable","last_success_at":null,"windows":[],"error":{"code":"authentication_required","message":"provider authorization is required"}}}`
	if err := os.WriteFile(filepath.Join(fixtureRoot, "quota.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, filepath.Join(base, "app support"), fixtureRoot)
	response := call(t, svc, "quota-unavailable-snapshot", "quota.read", map[string]string{"provider": "claude"})
	if response.Error != nil {
		t.Fatalf("quota.read protocol error: %+v", response.Error)
	}
	var got struct {
		State         string  `json:"state"`
		LastSuccessAt *string `json:"last_success_at"`
		Windows       []any   `json:"windows"`
		Error         *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "stale" || got.LastSuccessAt == nil || len(got.Windows) != 1 || got.Error == nil {
		t.Fatalf("previous snapshot was not retained as stale: %+v", got)
	}
}

func TestQuotaProviderStateIsIndependent(t *testing.T) {
	base := t.TempDir()
	fixtureRoot := filepath.Join(base, "fixtures")
	if err := os.MkdirAll(fixtureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"claude":{"state":"fresh","last_success_at":"2026-10-04T12:20:00Z","windows":[{"name":"five_hour","utilization":0,"resets_at":"2026-10-04T13:00:00Z"}],"error":null},"codex":{"state":"invalid-state","last_success_at":null,"windows":[],"error":null}}`
	if err := os.WriteFile(filepath.Join(fixtureRoot, "quota.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, filepath.Join(base, "app support"), fixtureRoot)
	claude := call(t, svc, "quota-claude", "quota.read", map[string]string{"provider": "claude"})
	codex := call(t, svc, "quota-codex", "quota.read", map[string]string{"provider": "codex"})
	var claudeResult struct {
		State string `json:"state"`
	}
	var codexResult struct {
		State string `json:"state"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(claude.Result, &claudeResult); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(codex.Result, &codexResult); err != nil {
		t.Fatal(err)
	}
	if claudeResult.State != "fresh" || codexResult.State != "unavailable" || codexResult.Error == nil || codexResult.Error.Code != "invalid_fixture" {
		t.Fatalf("provider states were coupled: claude=%+v codex=%+v", claudeResult, codexResult)
	}
}

func TestConcurrentProfileAddHasSingleWinner(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "agent home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, filepath.Join(base, "app support"), "")
	const workers = 24
	request := makeRequest(t, "add", "profiles.add", map[string]string{
		"name":        "shared",
		"claude_home": home,
		"codex_home":  "",
	})
	var wg sync.WaitGroup
	var mu sync.Mutex
	succeeded := 0
	conflicts := 0
	unexpected := []string{}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := svc.Handle(context.Background(), request)
			mu.Lock()
			defer mu.Unlock()
			if response.Error == nil {
				succeeded++
			} else if response.Error.Code == "profile_exists" {
				conflicts++
			} else {
				unexpected = append(unexpected, response.Error.Code)
			}
		}()
	}
	wg.Wait()
	if succeeded != 1 || conflicts != workers-1 || len(unexpected) != 0 {
		t.Fatalf("add outcomes: succeeded=%d conflicts=%d unexpected=%v", succeeded, conflicts, unexpected)
	}
}

func TestServeMalformedOversizedUnknownAndCorrelatedRequests(t *testing.T) {
	svc := newTestService(t, t.TempDir(), "")
	var input strings.Builder
	input.WriteString("not-json\n")
	input.WriteString(`{"version":2,"kind":"request","id":"v2","op":"hello","payload":{}}` + "\n")
	input.WriteString(`{"version":1,"kind":"request","id":"unknown-1","op":"unknown.op","payload":{}}` + "\n")
	input.WriteString(strings.Repeat("x", maxRequestBytes+1) + "\n")
	input.WriteString(`{"version":1,"kind":"request","id":"after-large","op":"hello","payload":{}}` + "\n")
	input.WriteString(`{"version":1,"kind":"request","id":"secret-id","op":"not-real","secret":"fixture-only-private-marker","payload":{}}` + "\n")
	var output bytes.Buffer
	if err := Serve(context.Background(), svc, strings.NewReader(input.String()), &output); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if strings.Contains(output.String(), "fixture-only-private-marker") {
		t.Fatal("secret request material leaked into protocol error")
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("response lines=%d, want 6: %q", len(lines), output.String())
	}
	want := []struct {
		id   string
		code string
	}{
		{id: "", code: "invalid_json"},
		{id: "v2", code: "unsupported_version"},
		{id: "unknown-1", code: "unknown_operation"},
		{id: "", code: "request_too_large"},
		{id: "after-large"},
		{id: "secret-id", code: "invalid_request"},
	}
	for i, line := range lines {
		var got responseView
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("response %d invalid JSON: %v", i, err)
		}
		if got.ID != want[i].id {
			t.Errorf("response %d id=%q, want %q", i, got.ID, want[i].id)
		}
		if want[i].code == "" {
			if got.Error != nil {
				t.Errorf("response %d error=%+v, want success", i, got.Error)
			}
		} else if got.Error == nil || got.Error.Code != want[i].code {
			t.Errorf("response %d error=%+v, want %q", i, got.Error, want[i].code)
		}
	}
}

func TestServeCleanEOFAndContextCancellation(t *testing.T) {
	svc := newTestService(t, t.TempDir(), "")
	var output bytes.Buffer
	if err := Serve(context.Background(), svc, strings.NewReader(""), &output); err != nil {
		t.Fatalf("empty EOF: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("empty EOF wrote %q", output.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Serve(ctx, svc, strings.NewReader(""), &output); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Serve error = %v, want context.Canceled", err)
	}
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
