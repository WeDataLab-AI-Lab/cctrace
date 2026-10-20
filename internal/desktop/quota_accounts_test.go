package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/profile"
)

func accountTestService(t *testing.T, root string) *Service {
	t.Helper()
	s, e := New(Config{AppSupportRoot: root})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func accountListFrom(t *testing.T, s *Service) map[string]any {
	t.Helper()
	r := call(t, s, "list", "quota.accounts.list", map[string]any{})
	if r.Error != nil {
		t.Fatalf("list: %+v", r.Error)
	}
	b, e := json.Marshal(r.Result)
	if e != nil {
		t.Fatal(e)
	}
	var got map[string]any
	if e = json.Unmarshal(b, &got); e != nil {
		t.Fatal(e)
	}
	return got
}
func TestQuotaAccountsPersonalPersistenceAndIsolation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := accountTestService(t, root)
	home := t.TempDir()
	r := call(t, s, "add", "quota.account.add", map[string]any{"provider": "claude", "label": "Personal", "home": home})
	if r.Error != nil {
		t.Fatalf("add: %+v", r.Error)
	}
	got := accountListFrom(t, s)
	rows := got["accounts"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows: %v", rows)
	}
	row := rows[0].(map[string]any)
	if row["enabled"] != false || row["personal"] != true || row["identity_state"] != "unverified" {
		t.Fatalf("unsafe default: %v", row)
	}
	ref, gen := row["account_ref"], row["generation"]
	r = call(t, s, "read", "quota.account.read", map[string]any{"account_ref": ref, "generation": gen})
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	b, _ := json.Marshal(r.Result)
	if !strings.Contains(string(b), "provider_disabled") {
		t.Fatalf("disabled read: %s", b)
	}
	r = call(t, s, "set", "quota.account.set", map[string]any{"account_ref": ref, "generation": gen, "enabled": true})
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	r = call(t, s, "old", "quota.account.set", map[string]any{"account_ref": ref, "generation": gen, "enabled": false})
	if r.Error == nil {
		t.Fatal("old ownership accepted")
	}
	reopened := accountTestService(t, root)
	again := accountListFrom(t, reopened)["accounts"].([]any)[0].(map[string]any)
	if again["id"] != row["id"] || again["enabled"] != true || again["account_ref"] == ref {
		t.Fatal("persistence/ownership broken")
	}
	if strings.Contains(string(b), home) {
		t.Fatal("home leaked")
	}
	_ = filepath.Separator
}
func TestQuotaAccountsCapabilityIsExplicit(t *testing.T) {
	t.Parallel()
	s := accountTestService(t, t.TempDir())
	input := `{"version":1,"kind":"request","id":"a","op":"quota.accounts.list","payload":{}}` + "\n" + `{"version":1,"kind":"request","id":"h","op":"hello","payload":{"quota_accounts":true}}` + "\n" + `{"version":1,"kind":"request","id":"b","op":"quota.accounts.list","payload":{}}` + "\n"
	var out bytes.Buffer
	if e := Serve(context.Background(), s, strings.NewReader(input), &out); e != nil {
		t.Fatal(e)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "capability_not_negotiated") || !strings.Contains(lines[1], `"quota_accounts":true`) || !strings.Contains(lines[2], `"accounts":[]`) {
		t.Fatalf("negotiation: %s", out.String())
	}
}

func TestQuotaAccountsDiscoveryIsSourceScopedAndPauseRetainsRows(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registry := filepath.Join(root, "registry")
	if e := os.MkdirAll(registry, 0700); e != nil {
		t.Fatal(e)
	}
	home := t.TempDir()
	dir := filepath.Join(registry, "profiles", "one")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	p := profile.NewDefault()
	p.ClaudeConfigDir = home
	p.CodexHome = t.TempDir()
	p.Options.SyncEnabled = true
	p.Options.CodexSyncEnabled = false
	p.Options.LocalAnalysisEnabled = true
	path := filepath.Join(dir, "profile.json")
	if e := profile.SaveTo(p, path); e != nil {
		t.Fatal(e)
	}
	s, e := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: registry})
	if e != nil {
		t.Fatal(e)
	}
	rows := accountListFrom(t, s)["accounts"].([]any)
	if len(rows) != 1 {
		t.Fatalf("source eligibility: %v", rows)
	}
	a := rows[0].(map[string]any)
	if a["provider"] != "claude" || a["enabled"] != false {
		t.Fatalf("unsafe auto default: %v", a)
	}
	if len(a["profiles"].([]any)) != 1 {
		t.Fatal("missing link")
	}
	id := a["id"]
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	s.daemonRoot = registry
	s.daemonLockProbe = func(string) daemonLockState { return daemonLockHeld }
	writeRawDaemonState(t, filepath.Join(registry, "daemon-runtime.json"), `{"pid":123,"instance_id":"pause-fixture","started_at":"`+now.Add(-time.Hour).Format(time.RFC3339Nano)+`","version":"dev"}`)
	writeRawDaemonState(t, filepath.Join(registry, "daemon-status.json"), `{"pid":123,"instance_id":"pause-fixture","observed_at":"`+now.Format(time.RFC3339Nano)+`","complete":true,"jobs":[{"id":"named:one","name":"one","enabled":true,"state":"stopped","generation":1}]}`)
	paused := accountListFrom(t, s)["accounts"].([]any)[0].(map[string]any)
	links := paused["profiles"].([]any)
	link := links[0].(map[string]any)
	if paused["id"] != id || paused["retention_pending"] != false || link["collecting"] != false {
		t.Fatalf("pause removed discovery or claimed collecting: %v", paused)
	}
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	rows = accountListFrom(t, s)["accounts"].([]any)
	if len(rows) != 1 {
		t.Fatal("orphan removed without choice")
	}
	a = rows[0].(map[string]any)
	if a["id"] != id || a["retention_pending"] != true || a["enabled"] != false {
		t.Fatalf("orphan: %v", a)
	}
	r := call(t, s, "keep", "quota.account.retention", map[string]any{"account_ref": a["account_ref"], "generation": a["generation"], "keep": true})
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	a = accountListFrom(t, s)["accounts"].([]any)[0].(map[string]any)
	if a["personal"] != true || a["retention_pending"] != false {
		t.Fatal("retention not resolved")
	}
}

func TestQuotaAccountsMalformedPeerDoesNotHideHealthyDiscovery(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registry := filepath.Join(root, "registry")
	os.MkdirAll(registry, 0700)
	p := profile.NewDefault()
	p.ClaudeConfigDir = t.TempDir()
	p.Options.SyncEnabled = true
	p.Options.LocalAnalysisEnabled = true
	if e := profile.SaveTo(p, filepath.Join(registry, "profile.json")); e != nil {
		t.Fatal(e)
	}
	bad := filepath.Join(registry, "profiles", "bad")
	os.MkdirAll(bad, 0700)
	os.WriteFile(filepath.Join(bad, "profile.json"), []byte(`{"options":`), 0600)
	s, e := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: registry})
	if e != nil {
		t.Fatal(e)
	}
	snapshot := accountListFrom(t, s)
	if snapshot["complete"] != false || len(snapshot["accounts"].([]any)) != 1 || len(snapshot["errors"].([]any)) != 1 {
		t.Fatalf("broken peer poisoned list: %v", snapshot)
	}
	problem := snapshot["errors"].([]any)[0].(map[string]any)
	if problem["profile_id"] != "named:bad" || problem["code"] != "invalid_profile" {
		t.Fatal("unsafe issue shape")
	}
}

func TestProfileDiscoveryCapabilityRequiresRegistry(t *testing.T) {
	t.Parallel()
	s := accountTestService(t, t.TempDir())
	r := call(t, s, "hello", "hello", map[string]any{"profile_discovery": true})
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	b, _ := json.Marshal(r.Result)
	if strings.Contains(string(b), `"profile_discovery":true`) {
		t.Fatal("unavailable registry discovery advertised")
	}
}

func TestQuotaAccountsMalformedLinkedProfileRetainsUnknownLinkAndBlocksExclusion(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registry := filepath.Join(root, "registry")
	home := t.TempDir()
	paths := []string{filepath.Join(registry, "profile.json"), filepath.Join(registry, "profiles", "peer", "profile.json")}
	for _, path := range paths {
		p := profile.NewDefault()
		p.ClaudeConfigDir = home
		p.Options.SyncEnabled = true
		p.Options.LocalAnalysisEnabled = true
		if err := profile.SaveTo(p, path); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: registry, accountIdentityReader: func(context.Context, string, string) (string, error) { return "synthetic-account", nil }})
	if err != nil {
		t.Fatal(err)
	}
	before := accountListFrom(t, s)["accounts"].([]any)[0].(map[string]any)
	if len(before["profiles"].([]any)) != 2 {
		t.Fatal("shared home links missing")
	}
	if err := os.WriteFile(paths[1], []byte(`{"options":`), 0600); err != nil {
		t.Fatal(err)
	}
	partial := accountListFrom(t, s)
	row := partial["accounts"].([]any)[0].(map[string]any)
	links := row["profiles"].([]any)
	if partial["complete"] != false || len(links) != 2 {
		t.Fatalf("incomplete discovery lost known link: %v", partial)
	}
	unknown := false
	for _, link := range links {
		p := link.(map[string]any)
		if p["id"] == "named:peer" {
			unknown = p["collecting"] == nil && p["excluded"] == nil
		}
	}
	if !unknown {
		t.Fatal("broken linked profile claimed current collection/exclusion")
	}
	response := call(t, s, "exclude", "quota.account.exclusion", map[string]any{"account_ref": row["account_ref"], "generation": row["generation"], "excluded": true})
	if response.Error == nil {
		t.Fatal("incomplete linked profile accepted all-links exclusion")
	}
	current, err := (profile.Registry{Root: registry}).Load("default")
	if err != nil || len(current.Profile.Options.ExcludeAccounts) != 0 {
		t.Fatalf("incomplete census mutated healthy profile: %+v %v", current, err)
	}
	reopened, err := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: registry})
	if err != nil {
		t.Fatal(err)
	}
	again := accountListFrom(t, reopened)["accounts"].([]any)[0].(map[string]any)
	if len(again["profiles"].([]any)) != 2 {
		t.Fatal("restart lost the unknown link")
	}
}

func TestQuotaAccountDiscoveryAllowsEmptyNamedProfilesDirectory(t *testing.T) {
	t.Parallel()
	s := accountTestService(t, t.TempDir())
	account := personalAccount(t, s, "claude")
	registry := t.TempDir()
	if err := os.Mkdir(filepath.Join(registry, "profiles"), 0700); err != nil {
		t.Fatal(err)
	}
	s.registry = &profile.Registry{Root: registry}
	response := call(t, s, "empty-named-profiles", "quota.accounts.list", map[string]any{})
	if response.Error != nil {
		t.Fatalf("empty named profile directory rejected: %+v", response.Error)
	}
	var snapshot quotaAccountsList
	if err := json.Unmarshal(response.Result, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Complete || len(snapshot.Errors) != 0 || len(snapshot.Accounts) != 1 || snapshot.Accounts[0].ID != account["id"] || snapshot.Accounts[0].Error != nil {
		t.Fatalf("personal account lost with an empty named profile directory: %+v", snapshot)
	}
}
