package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/processio"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

func TestMultiHomeRuntimeSendsBothAllowedHomesAndNoDeniedHistory(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var sent []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, r.URL.Path+":"+string(body))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"inserted":1}`))
	}))
	defer server.Close()
	p := profile.NewDefault()
	p.ID = "default"
	p.User = profile.UserInfo{ID: "fixture", Email: "fixture@example.test"}
	p.Server.SyncEndpoint = server.URL
	p.Server.Endpoint = server.URL
	p.Server.AuthToken = "synthetic"
	p.HomesVersion = 1
	homes := []string{}
	for _, name := range []string{"first-allowed", "second-allowed", "denied-secret", "undecided-secret"} {
		home, _ := profile.NormalizeCodexHome(t.TempDir())
		homes = append(homes, home)
		file := filepath.Join(home, "projects", "fixture", name+".jsonl")
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		line := `{"type":"user","timestamp":"2026-10-12T09:00:00Z","sessionId":"` + name + `","uuid":"` + name + `","message":{"role":"user","content":"` + name + `"}}` + "\n"
		if err := os.WriteFile(file, []byte(line), 0600); err != nil {
			t.Fatal(err)
		}
		transmission := "allow"
		if name == "denied-secret" {
			transmission = "exclude"
		}
		if name == "undecided-secret" {
			transmission = "undecided"
		}
		p.Homes = append(p.Homes, profile.HomeMembership{Provider: "claude", Home: home, Transmission: transmission})
	}
	p.ClaudeConfigDir = homes[0]
	r := profile.Registry{Root: filepath.Join(t.TempDir(), "registry")}
	path, _ := r.Path("default")
	if err := profile.SaveTo(p, path); err != nil {
		t.Fatal(err)
	}
	entry, err := r.Load("default")
	if err != nil {
		t.Fatal(err)
	}
	for _, home := range homes[:2] {
		state, err := syncer.LoadState(filepath.Join(entry.RuntimeDir, claudeStateFile(entry.Profile, home)))
		if err != nil {
			t.Fatal(err)
		}
		if err = state.Save(); err != nil {
			t.Fatal(err)
		}
	}
	job := &profileJob{entry: entry, endpoint: server.URL, claudeDir: homes[0], claudeDirs: homes[:2]}
	allocation := localdaemon.Allocation{Allowed: []string{"claude:" + homes[0], "claude:" + homes[1]}}
	if err = job.prepare(context.Background(), allocation); err != nil {
		t.Fatal(err)
	}
	for _, pass := range job.passes {
		if _, err = pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	job.flushObservations(context.Background())
	mu.Lock()
	all := strings.Join(sent, "\n")
	mu.Unlock()
	for _, name := range []string{"first-allowed", "second-allowed"} {
		if !strings.Contains(all, name) {
			t.Fatalf("missing %s: %s", name, all)
		}
	}
	for _, name := range []string{"denied-secret", "undecided-secret"} {
		if strings.Contains(all, name) {
			t.Fatalf("unapproved home transmitted %s", name)
		}
	}
	if claudeStateFile(entry.Profile, homes[0]) == claudeStateFile(entry.Profile, homes[1]) {
		t.Fatal("member reordering aliases independent cursors")
	}
}

func TestMultiHomeProcessIOUploadsAllowedSourceAndHoldsUnattributedOrDenied(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	sent := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request syncer.SessionObservationsRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		for _, item := range request.LiveStates {
			sent = append(sent, item.SessionID)
		}
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(syncer.SessionObservationsResponse{Accepted: len(request.LiveStates), Stored: len(request.LiveStates)})
	}))
	defer server.Close()
	allowed, _ := profile.NormalizeCodexHome(t.TempDir())
	denied, _ := profile.NormalizeCodexHome(t.TempDir())
	r := profile.Registry{Root: filepath.Join(t.TempDir(), "registry")}
	p := profile.NewDefault()
	p.ID = "default"
	p.User.ID = "user-1"
	p.User.Email = "owner@example.test"
	p.Server.Endpoint = server.URL
	p.HomesVersion = 1
	p.Homes = []profile.HomeMembership{{Provider: "claude", Home: allowed, Transmission: "allow"}, {Provider: "claude", Home: denied, Transmission: "exclude"}}
	path, _ := r.Path("default")
	if err := profile.SaveTo(p, path); err != nil {
		t.Fatal(err)
	}
	entry, err := r.Load("default")
	if err != nil {
		t.Fatal(err)
	}
	queue := processio.Queue{Path: filepath.Join(entry.RuntimeDir, processIOQueueFile)}
	for _, id := range []string{"allowed-io", "denied-io", "legacy-io", "stale-io"} {
		report := queuedProcessIOForTest(id, "acct-synthetic")
		report.Agent = "claude"
		report.BillingProvider = "anthropic"
		report.SourceHome = allowed
		report.SourceRevision = entry.Revision
		if id == "denied-io" {
			report.SourceHome = denied
		}
		if id == "legacy-io" {
			report.SourceHome = ""
			report.SourceRevision = ""
		}
		if id == "stale-io" {
			report.SourceRevision = "stale"
		}
		if err := queue.Upsert(report); err != nil {
			t.Fatal(err)
		}
	}
	job := &profileJob{entry: entry, client: syncer.NewClient(server.URL, "synthetic", "test")}
	job.flushObservations(context.Background())
	mu.Lock()
	delivered := append([]string{}, sent...)
	mu.Unlock()
	if len(delivered) != 1 || delivered[0] != "allowed-io" {
		t.Fatalf("source consent bypass or allowed report lost: %v", delivered)
	}
	pending, err := queue.List()
	if err != nil || len(pending) != 3 {
		t.Fatalf("held reports lost: %d %v", len(pending), err)
	}
	for _, report := range pending {
		if report.SessionID == "allowed-io" {
			t.Fatal("delivered report remained pending")
		}
	}
}

// The actual one-shot runner uses HOME and global sync hooks, so it is serial.
func TestExplicitStandaloneOneShotProcessIOCannotBypassSourceConsent(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	var recorder observationRecorder
	server := recorder.serve(t)
	allowed, _ := profile.NormalizeCodexHome(t.TempDir())
	denied, _ := profile.NormalizeCodexHome(t.TempDir())
	if err := os.MkdirAll(filepath.Join(allowed, "projects", "fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	p := profile.NewDefault()
	p.User.ID = "user-1"
	p.User.Email = "owner@example.test"
	p.Server.Endpoint = server.URL
	p.Server.SyncEndpoint = server.URL
	p.ClaudeConfigDir = allowed
	p.HomesVersion = 1
	p.HomeCollection = &profile.HomeCollection{Claude: true}
	p.Homes = []profile.HomeMembership{{Provider: "claude", Home: allowed, Transmission: "allow"}, {Provider: "claude", Home: denied, Transmission: "exclude"}}
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	queue := processIOQueue("")
	for _, id := range []string{"standalone-allowed", "standalone-denied", "standalone-unattributed", "standalone-stale"} {
		report := queuedProcessIOForTest(id, "acct-synthetic")
		report.Agent = "claude"
		report.BillingProvider = "anthropic"
		report.SourceHome = allowed
		report.SourceRevision = p.DefinitionRevision()
		if id == "standalone-denied" {
			report.SourceHome = denied
		}
		if id == "standalone-unattributed" {
			report.SourceHome = ""
			report.SourceRevision = ""
		}
		if id == "standalone-stale" {
			report.SourceRevision = "stale"
		}
		if err := queue.Upsert(report); err != nil {
			t.Fatal(err)
		}
	}
	if err := runSync(false, allowed, false, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatal(err)
	}
	sent := strings.Join(recorder.sent(), "\n")
	if !strings.Contains(sent, "standalone-allowed") {
		t.Fatal("allowed explicit report was not delivered")
	}
	for _, id := range []string{"standalone-denied", "standalone-unattributed", "standalone-stale"} {
		if strings.Contains(sent, id) {
			t.Fatalf("standalone queue bypassed source consent: %s", id)
		}
	}
	pending, err := queue.List()
	if err != nil || len(pending) != 3 {
		t.Fatalf("held standalone reports lost: %d %v", len(pending), err)
	}
}
