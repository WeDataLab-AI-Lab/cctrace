package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/profile"
)

// HOME is process-global, so these tests cannot run in parallel.
func localTestProfile(t *testing.T) *profile.Profile {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	p := profile.NewDefault()
	p.Options.SyncEnabled = false
	p.ClaudeConfigDir = t.TempDir()
	p.Options.CodexDirs = []string{t.TempDir()}
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLocalEnableStatusDisable(t *testing.T) {
	localTestProfile(t)
	var out bytes.Buffer
	if err := runLocalEnable(&out, "", "http://localhost:3000"); err != nil {
		t.Fatal(err)
	}
	var bootstrap struct{ Endpoint, Token string }
	if err := json.Unmarshal(out.Bytes(), &bootstrap); err != nil {
		t.Fatal(err)
	}
	policy, err := readLocalPolicy("")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(syncProfileDir(""), "local-api-policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), bootstrap.Token) || bootstrap.Token == "" {
		t.Fatal("plaintext token persisted or missing bootstrap")
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 6 {
		t.Fatalf("policy fields: %s", data)
	}
	fi, _ := os.Stat(filepath.Join(syncProfileDir(""), "local-api-policy.json"))
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v", fi.Mode())
	}
	p, _ := loadProfile("")
	if !p.Options.LocalAnalysisEnabled || p.Options.SyncEnabled {
		t.Fatal("local opt-in changed sync consent")
	}
	out.Reset()
	if err := runLocalStatus(&out, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), bootstrap.Token) || strings.Contains(out.String(), policy.TokenHash) {
		t.Fatal("status leaked secret")
	}
	if err := runLocalDisable(""); err != nil {
		t.Fatal(err)
	}
	disabled, _ := readLocalPolicy("")
	if disabled.Enabled || disabled.TokenHash != "" || disabled.Generation != policy.Generation+1 {
		t.Fatalf("disable=%+v", disabled)
	}
	out.Reset()
	if err := runLocalEnable(&out, "", "https://localhost:4000"); err != nil {
		t.Fatal(err)
	}
	enabled, _ := readLocalPolicy("")
	if enabled.Generation != disabled.Generation+1 || enabled.ExactOrigin != "https://localhost:4000" {
		t.Fatal("generation/origin not advanced")
	}
}

func TestLocalEnableRejectsInvalidOriginAndMissingProfile(t *testing.T) {
	localTestProfile(t)
	// A default-port origin is not an invalid origin: the allowed origin is the web
	// page's, and an HTTPS dashboard on :443 is a normal one. That case is covered
	// by TestLocalEnableAcceptsDefaultPortOrigin.
	for _, origin := range []string{"", "null", "http://localhost:3000/", "http://user@localhost:3000", "http://localhost:3000?x=1", "http://localhost:3000#x", "http://*.example:3000"} {
		if err := runLocalEnable(&bytes.Buffer{}, "", origin); err == nil {
			t.Fatalf("accepted %q", origin)
		}
	}
	if err := runLocalEnable(&bytes.Buffer{}, "missing", "http://localhost:3000"); err == nil {
		t.Fatal("missing profile accepted")
	}
}

func TestLocalEnableAcceptsDefaultPortOrigin(t *testing.T) {
	localTestProfile(t)
	for _, origin := range []string{"https://dashboard.example.com", "http://localhost"} {
		var out bytes.Buffer
		if err := runLocalEnable(&out, "", origin); err != nil {
			t.Fatalf("rejected default-port origin %q: %v", origin, err)
		}
		var bootstrap struct{ Endpoint, Token string }
		if err := json.Unmarshal(out.Bytes(), &bootstrap); err != nil {
			t.Fatal(err)
		}
		if bootstrap.Endpoint == "" || bootstrap.Token == "" {
			t.Fatalf("%q: bootstrap = %q", origin, out.String())
		}
	}
}

func TestLocalWatchLifecycleAndRoots(t *testing.T) {
	p := localTestProfile(t)
	if a := startLocalAnalysis(context.Background(), "", p, p.ClaudeConfigDir); a != nil {
		t.Fatal("disabled listener created")
	}
	var out bytes.Buffer
	if err := runLocalEnable(&out, "", "http://localhost:3000"); err != nil {
		t.Fatal(err)
	}
	p, _ = loadProfile("")
	var bootstrap struct{ Endpoint, Token string }
	_ = json.Unmarshal(out.Bytes(), &bootstrap)
	explicitRoot := t.TempDir()
	projects := filepath.Join(explicitRoot, "projects", "test")
	if err := os.MkdirAll(projects, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projects, "session.jsonl"), []byte("{\"type\":\"user\",\"sessionId\":\"explicit-root\",\"message\":{\"content\":\"hello\"}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		a := startLocalAnalysis(ctx, "", p, explicitRoot)
		if a == nil {
			cancel()
			t.Fatal("listener not created")
		}
		req, _ := http.NewRequest("GET", bootstrap.Endpoint+"/v1/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+bootstrap.Token)
		client := &http.Client{Timeout: time.Second}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			a.Close()
			t.Fatal(err)
		}
		var listing struct {
			Sessions []struct {
				NativeSessionID string `json:"native_session_id"`
			}
		}
		err = json.NewDecoder(resp.Body).Decode(&listing)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || len(listing.Sessions) != 1 || listing.Sessions[0].NativeSessionID != "explicit-root" {
			cancel()
			a.Close()
			t.Fatalf("explicit root response=%+v err=%v status=%d", listing, err, resp.StatusCode)
		}
		cancel()
		a.Close()
		address := strings.TrimPrefix(bootstrap.Endpoint, "http://")
		l, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatalf("handoff listener not released: %v", err)
		}
		l.Close()
	}
}

func TestLocalDisableRevokesRunningListener(t *testing.T) {
	localTestProfile(t)
	if err := runLocalEnable(&bytes.Buffer{}, "", "http://localhost:3000"); err != nil {
		t.Fatal(err)
	}
	p, _ := loadProfile("")
	api := startLocalAnalysis(context.Background(), "", p, p.ClaudeConfigDir)
	if api == nil {
		t.Fatal("listener not started")
	}
	defer api.Close()
	if err := runLocalDisable(""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-api.done:
	case <-time.After(time.Second):
		t.Fatal("disabled policy still serving")
	}
	var status localAnalysisStatus
	ok, err := readJSONFile(filepath.Join(syncProfileDir(""), "local-api-status.json"), &status)
	if err != nil || !ok || status.Running || status.EndedAt == nil {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestLocalAnalysisConfigKey(t *testing.T) {
	t.Parallel()
	p := &profile.Profile{}
	if err := setProfileField(p, "options.local_analysis_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if !p.Options.LocalAnalysisEnabled || p.Options.SyncEnabled {
		t.Fatal("independent local option not set")
	}
	found := false
	for _, s := range flattenProfile(p) {
		if s.key == "options.local_analysis_enabled" {
			found = true
		}
	}
	if !found {
		t.Fatal("local option not listed")
	}
}
