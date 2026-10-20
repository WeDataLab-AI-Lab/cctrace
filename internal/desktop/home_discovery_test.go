package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/profile"
)

func TestHomeDiscoveryRegistrationIsSelectedAndCollectionOff(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, ".codex-2")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("# synthetic marker"), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	s, err := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: filepath.Join(root, "registry"), homeDiscoveryRoot: home})
	if err != nil {
		t.Fatal(err)
	}
	response := call(t, s, "discover", "profiles.discover", map[string]any{})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	data, _ := json.Marshal(response.Result)
	var found homeDiscoveryResult
	if err := json.Unmarshal(data, &found); err != nil || len(found.Candidates) != 1 {
		t.Fatalf("discovery %s %v", data, err)
	}
	ref := found.Candidates[0].CandidateRef
	for _, payload := range []map[string]any{{"candidate_ref": ref}, {"candidate_ref": ref, "collection_enabled": true}, {"candidate_ref": ref, "collection_enabled": false, "home": dir}} {
		if r := call(t, s, "bad", "profiles.discover.register", payload); r.Error == nil {
			t.Fatal("unsafe registration accepted")
		}
	}
	if r := call(t, s, "register", "profiles.discover.register", map[string]any{"candidate_ref": ref, "collection_enabled": false}); r.Error != nil {
		t.Fatal(r.Error)
	}
	registered, err := (profile.Registry{Root: filepath.Join(root, "registry")}).Load("codex-2")
	if err != nil || !registered.Profile.RegistrationOnly || registered.CollectionState != "registered" || registered.Profile.Options.CodexSyncEnabled {
		t.Fatalf("registration %+v %v", registered, err)
	}
	if r := call(t, s, "stale", "profiles.discover.register", map[string]any{"candidate_ref": ref, "collection_enabled": false}); r.Error == nil {
		t.Fatal("stale candidate reference accepted")
	}
}

func TestHomeDiscoveryRequiresNegotiation(t *testing.T) {
	t.Parallel()
	s, err := New(Config{AppSupportRoot: t.TempDir(), ProfileRoot: filepath.Join(t.TempDir(), "registry"), homeDiscoveryRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	input := `{"version":1,"kind":"request","id":"before","op":"profiles.discover","payload":{}}` + "\n" + `{"version":1,"kind":"request","id":"hello","op":"hello","payload":{"profile_discovery":true}}` + "\n" + `{"version":1,"kind":"request","id":"after","op":"profiles.discover","payload":{}}` + "\n"
	var output bytes.Buffer
	if err := Serve(context.Background(), s, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "capability_not_negotiated") || !strings.Contains(lines[1], `"profile_discovery":true`) || !strings.Contains(lines[2], `"candidates":[]`) {
		t.Fatal(output.String())
	}
}

func TestHomeDiscoveryBatchKeepsOtherSelectedCandidateReference(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	for _, name := range []string{".codex-2", ".codex-3"} {
		dir := filepath.Join(home, name)
		_ = os.Mkdir(dir, 0700)
		_ = os.WriteFile(filepath.Join(dir, "config.toml"), []byte("# synthetic marker"), 0600)
	}
	root := t.TempDir()
	s, err := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: filepath.Join(root, "registry"), homeDiscoveryRoot: home})
	if err != nil {
		t.Fatal(err)
	}
	response := call(t, s, "list", "profiles.discover", map[string]any{})
	data, _ := json.Marshal(response.Result)
	var found homeDiscoveryResult
	_ = json.Unmarshal(data, &found)
	if response.Error != nil || len(found.Candidates) != 2 {
		t.Fatalf("missing batch %+v", response)
	}
	for _, candidate := range found.Candidates {
		response := call(t, s, "register", "profiles.discover.register", map[string]any{"candidate_ref": candidate.CandidateRef, "collection_enabled": false})
		if response.Error != nil {
			t.Fatalf("selected batch reference invalidated: %+v", response.Error)
		}
	}
}

func TestHomeDiscoveryExclusionReturnsCurrentResult(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	for _, name := range []string{".codex-2", ".codex-3"} {
		dir := filepath.Join(home, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("# marker"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	s, err := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: filepath.Join(root, "registry"), homeDiscoveryRoot: home})
	if err != nil {
		t.Fatal(err)
	}
	response := call(t, s, "discover", "profiles.discover", map[string]any{})
	data, _ := json.Marshal(response.Result)
	var found homeDiscoveryResult
	_ = json.Unmarshal(data, &found)
	if response.Error != nil || len(found.Candidates) != 2 {
		t.Fatalf("discovery %+v", response)
	}
	ref := found.Candidates[0].CandidateRef
	for _, payload := range []map[string]any{
		{"candidate_ref": ref}, {"candidate_ref": ref, "excluded": "true"}, {"candidate_ref": ref, "excluded": true, "home": home}, {"candidate_ref": "missing", "excluded": true},
	} {
		if response := call(t, s, "bad", "profiles.discover.exclude", payload); response.Error == nil {
			t.Fatalf("bad exclusion accepted %+v", payload)
		}
	}
	response = call(t, s, "exclude", "profiles.discover.exclude", map[string]any{"candidate_ref": ref, "excluded": true})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	data, _ = json.Marshal(response.Result)
	var current struct {
		ExclusionsSupported bool `json:"exclusions_supported"`
		Candidates          []struct {
			CandidateRef string `json:"candidate_ref"`
			Excluded     bool   `json:"excluded"`
		} `json:"candidates"`
	}
	_ = json.Unmarshal(data, &current)
	if !current.ExclusionsSupported || len(current.Candidates) != 2 || !current.Candidates[0].Excluded || current.Candidates[0].CandidateRef != ref || current.Candidates[1].CandidateRef != found.Candidates[1].CandidateRef {
		t.Fatalf("result %s", data)
	}
	if response := call(t, s, "register", "profiles.discover.register", map[string]any{"candidate_ref": ref, "collection_enabled": false}); response.Error == nil {
		t.Fatal("excluded registration accepted")
	}
	response = call(t, s, "restore", "profiles.discover.exclude", map[string]any{"candidate_ref": ref, "excluded": false})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	entries, err := (profile.Registry{Root: filepath.Join(root, "registry")}).List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("restore registered %+v %v", entries, err)
	}
	if response := call(t, s, "register", "profiles.discover.register", map[string]any{"candidate_ref": ref, "collection_enabled": false}); response.Error != nil {
		t.Fatal(response.Error)
	}
	if response := call(t, s, "stale", "profiles.discover.exclude", map[string]any{"candidate_ref": ref, "excluded": true}); response.Error == nil {
		t.Fatal("stale ref accepted")
	}
}

func TestHomeDiscoveryExclusionRequiresNegotiation(t *testing.T) {
	t.Parallel()
	s, err := New(Config{AppSupportRoot: t.TempDir(), ProfileRoot: filepath.Join(t.TempDir(), "registry"), homeDiscoveryRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	input := `{"version":1,"kind":"request","id":"before","op":"profiles.discover.exclude","payload":{"candidate_ref":"missing","excluded":true}}` + "\n"
	var output bytes.Buffer
	if err := Serve(context.Background(), s, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "capability_not_negotiated") {
		t.Fatal(output.String())
	}
}

func TestHomeDiscoveryExclusionRejectsReplacedDirectory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, ".codex-2")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("# marker"), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	s, err := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: filepath.Join(root, "registry"), homeDiscoveryRoot: home})
	if err != nil {
		t.Fatal(err)
	}
	response := call(t, s, "discover", "profiles.discover", map[string]any{})
	data, _ := json.Marshal(response.Result)
	var found homeDiscoveryResult
	_ = json.Unmarshal(data, &found)
	if response.Error != nil || len(found.Candidates) != 1 {
		t.Fatalf("discovery %+v", response)
	}
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("# replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	response = call(t, s, "exclude", "profiles.discover.exclude", map[string]any{"candidate_ref": found.Candidates[0].CandidateRef, "excluded": true})
	if response.Error == nil || response.Error.Code != "candidate_changed" {
		t.Fatalf("replacement accepted %+v", response)
	}
}
