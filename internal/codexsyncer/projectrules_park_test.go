package codexsyncer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/gitctx"
	"cctrace/internal/syncer"
)

// ruleRepo creates a scannable repository root holding one Codex rule file, so
// the scan produces a snapshot and execution reaches the send.
func ruleRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# rules\n"), 0644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}
	return root
}

func countingServer(t *testing.T, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// The Codex syncer carries its own copy of this logic, so it needs its own
// guard: the two files have drifted before.
func TestSendProjectRulesParksAfterSendFailure(t *testing.T) {
	srv, hits := countingServer(t, http.StatusInternalServerError)
	s := New(nil, "user@example.invalid", "u1", &syncer.State{}, syncer.NewClient(srv.URL, "token", ""), nil)
	meta := gitctx.Context{RepositoryRoot: ruleRepo(t), RepositoryID: "github.com/org/repo"}

	if err := s.sendProjectRules(context.Background(), "proj", "name", meta); err == nil {
		t.Fatal("expected the send failure to be reported")
	}
	// Same repository, same pass: this is what a second session file does.
	if err := s.sendProjectRules(context.Background(), "proj", "name", meta); err != nil {
		t.Fatalf("second attempt should be suppressed, got %v", err)
	}

	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests issued, want 1 (parked after the first failure)", got)
	}
}

// useFakeClock pins the syncer's clock so parking windows can be crossed
// without sleeping.
func useFakeClock(t *testing.T) func(time.Duration) {
	t.Helper()
	now := time.Date(2026, 8, 13, 14, 0, 0, 0, time.UTC)
	prev := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = prev })
	return func(d time.Duration) { now = now.Add(d) }
}

// A 429 states how long to stay away; parking it on the fixed 30s TTL would
// retry inside the server's window and keep the limit tripped.
func TestSendProjectRulesHonoursRetryAfter(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	advance := useFakeClock(t)
	s := New(nil, "user@example.invalid", "u1", &syncer.State{}, syncer.NewClient(srv.URL, "token", ""), nil)
	meta := gitctx.Context{RepositoryRoot: ruleRepo(t), RepositoryID: "github.com/org/repo"}

	_ = s.sendProjectRules(context.Background(), "proj", "name", meta)

	// Past the ordinary repository TTL but well inside the server's window.
	advance(5 * time.Minute)
	_ = s.sendProjectRules(context.Background(), "proj", "name", meta)
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests inside Retry-After, want 1", got)
	}

	advance(time.Hour)
	_ = s.sendProjectRules(context.Background(), "proj", "name", meta)
	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests after Retry-After elapsed, want 2", got)
	}
}

// A failure carrying no retry deadline keeps the ordinary repository TTL.
func TestSendProjectRulesUsesRuleScanTTLWithoutRetryAfter(t *testing.T) {
	srv, hits := countingServer(t, http.StatusInternalServerError)
	advance := useFakeClock(t)
	s := New(nil, "user@example.invalid", "u1", &syncer.State{}, syncer.NewClient(srv.URL, "token", ""), nil)
	meta := gitctx.Context{RepositoryRoot: ruleRepo(t), RepositoryID: "github.com/org/repo"}

	_ = s.sendProjectRules(context.Background(), "proj", "name", meta)
	advance(ruleScanTTL / 2)
	_ = s.sendProjectRules(context.Background(), "proj", "name", meta)
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests inside the TTL, want 1", got)
	}

	advance(ruleScanTTL)
	_ = s.sendProjectRules(context.Background(), "proj", "name", meta)
	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests after the TTL, want 2", got)
	}
}

// TestSendProjectRulesParksForbiddenLongerThanScanTTL mirrors the Claude path's
// test of the same name.
//
// Access denied is durable: the server decides it from session history, and that
// history does not appear because the client asked again 30 seconds later. This
// path used ruleScanTTL while internal/syncer used the forbidden TTL, so the fix
// for #458 reached only half the clients -- production took 23k-41k refused rule
// posts a day for at least ten days (#619). The two paths now share one constant,
// and this test is what says so out loud.
func TestSendProjectRulesParksForbiddenLongerThanScanTTL(t *testing.T) {
	srv, hits := countingServer(t, http.StatusForbidden)
	advance := useFakeClock(t)
	s := New(nil, "user@example.invalid", "u1", &syncer.State{}, syncer.NewClient(srv.URL, "token", ""), nil)
	meta := gitctx.Context{RepositoryRoot: ruleRepo(t), RepositoryID: "github.com/org/repo"}

	_ = s.sendProjectRules(context.Background(), "proj", "name", meta)
	advance(ruleScanTTL * 2)
	_ = s.sendProjectRules(context.Background(), "proj", "name", meta)
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests after twice the scan TTL, want 1 -- forbidden must park longer", got)
	}

	advance(syncer.RuleForbiddenTTL)
	_ = s.sendProjectRules(context.Background(), "proj", "name", meta)
	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests after the forbidden TTL, want 2 -- the park must expire", got)
	}
}

// TestForbiddenBackoffIsSharedWithTheClaudePath keeps the two implementations
// from drifting again. The value itself is not the point -- naming the same
// constant is, because that is what failed last time.
func TestForbiddenBackoffIsSharedWithTheClaudePath(t *testing.T) {
	if syncer.RuleForbiddenTTL <= ruleScanTTL {
		t.Fatalf("forbidden backoff %s is not longer than the scan TTL %s", syncer.RuleForbiddenTTL, ruleScanTTL)
	}
}

// TestForbiddenIsRecordedInStateAndClearedOnSuccess covers the half the park does
// not: the park stops the retrying and dies with the process, so nothing would
// survive to tell a person that a repository's rules are never collected.
//
// The refusal is durable -- the server decides it from session history -- so the
// silence is durable too. `cctrace status` reads this.
func TestForbiddenIsRecordedInStateAndClearedOnSuccess(t *testing.T) {
	srv, _ := countingServer(t, http.StatusForbidden)
	advance := useFakeClock(t)
	st := &syncer.State{}
	s := New(nil, "user@example.invalid", "u1", st, syncer.NewClient(srv.URL, "token", ""), nil)
	meta := gitctx.Context{RepositoryRoot: ruleRepo(t), RepositoryID: "github.com/org/repo"}

	_ = s.sendProjectRules(context.Background(), "proj", "name", meta)
	if len(st.RulesDenied) == 0 {
		t.Fatal("the refusal left no record, so nothing outlives the process to report it")
	}

	// Accepted now. The record has to go, or status keeps naming a repository
	// that is being collected.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"inserted_rules":1,"inserted_versions":1}`))
	}))
	defer ok.Close()
	s2 := New(nil, "user@example.invalid", "u1", st, syncer.NewClient(ok.URL, "token", ""), nil)
	advance(syncer.RuleForbiddenTTL)
	if err := s2.sendProjectRules(context.Background(), "proj", "name", meta); err != nil {
		t.Fatalf("accepted send failed: %v", err)
	}
	if len(st.RulesDenied) != 0 {
		t.Errorf("the refusal record survived acceptance: %v", st.RulesDenied)
	}
}
