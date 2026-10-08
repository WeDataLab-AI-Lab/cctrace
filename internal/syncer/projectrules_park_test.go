package syncer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// ruleRepo creates a scannable repository root holding one rule file, so the
// scan produces a snapshot and execution reaches the send.
func ruleRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("# rules\n"), 0644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}
	return root
}

// countingServer answers every request with status and reports how many arrived.
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

func newRuleSyncer(t *testing.T, endpoint string) *Syncer {
	t.Helper()
	return New(t.TempDir(), "user@example.invalid", "u1", &State{}, NewClient(endpoint, "token", ""), nil)
}

// A send failure has to park the repository like every other failure does.
//
// It was the one path that did not: an unreachable server left rulesSent
// untouched, so the whole tree walk and POST ran again on the next pass — and
// once for every session file the repository owns. A local log carried 192,236
// copies of one such failure, 64% of its bytes.
func TestSendProjectRulesParksAfterSendFailure(t *testing.T) {
	srv, hits := countingServer(t, http.StatusInternalServerError)
	s := newRuleSyncer(t, srv.URL)
	meta := &projectMeta{repositoryRoot: ruleRepo(t), repositoryID: "github.com/org/repo"}

	if err := s.sendProjectRules(context.Background(), "proj", meta, ""); err == nil {
		t.Fatal("expected the send failure to be reported")
	}
	// Same repository, same pass: this is what a second session file does.
	if err := s.sendProjectRules(context.Background(), "proj", meta, ""); err != nil {
		t.Fatalf("second attempt should be suppressed, got %v", err)
	}

	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests issued, want 1 (parked after the first failure)", got)
	}
}

// Parking must not be limited to the send: a 403 and a successful send already
// parked, and this pins all three so the paths cannot drift apart again.
func TestSendProjectRulesParksAfterForbidden(t *testing.T) {
	srv, hits := countingServer(t, http.StatusForbidden)
	s := newRuleSyncer(t, srv.URL)
	meta := &projectMeta{repositoryRoot: ruleRepo(t), repositoryID: "github.com/org/repo"}

	for i := 0; i < 3; i++ {
		if err := s.sendProjectRules(context.Background(), "proj", meta, ""); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests issued, want 1", got)
	}
}

func TestSendProjectRulesParksAfterSuccess(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"inserted_rules":1,"inserted_versions":1}`))
	}))
	defer srv.Close()
	s := newRuleSyncer(t, srv.URL)
	meta := &projectMeta{repositoryRoot: ruleRepo(t), repositoryID: "github.com/org/repo"}

	for i := 0; i < 3; i++ {
		if err := s.sendProjectRules(context.Background(), "proj", meta, ""); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests issued, want 1", got)
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

// A 429 states how long to stay away. Parking every send failure on the fixed
// 30s repository TTL discarded that: a Retry-After of an hour was retried after
// 30 seconds, which is what the header exists to prevent.
func TestSendProjectRulesHonoursRetryAfter(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	advance := useFakeClock(t)
	s := newRuleSyncer(t, srv.URL)
	meta := &projectMeta{repositoryRoot: ruleRepo(t), repositoryID: "github.com/org/repo"}

	if err := s.sendProjectRules(context.Background(), "proj", meta, ""); err == nil {
		t.Fatal("expected the 429 to be reported")
	}

	// Past the ordinary repository TTL but well inside the server's window.
	advance(5 * time.Minute)
	_ = s.sendProjectRules(context.Background(), "proj", meta, "")
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests inside Retry-After, want 1", got)
	}

	advance(time.Hour)
	_ = s.sendProjectRules(context.Background(), "proj", meta, "")
	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests after Retry-After elapsed, want 2", got)
	}
}

// An absurd Retry-After must not park a repository indefinitely.
func TestSendProjectRulesClampsRetryAfter(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "99999999999999")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	advance := useFakeClock(t)
	s := newRuleSyncer(t, srv.URL)
	meta := &projectMeta{repositoryRoot: ruleRepo(t), repositoryID: "github.com/org/repo"}

	_ = s.sendProjectRules(context.Background(), "proj", meta, "")
	advance(maxRuleRetryAfter + time.Minute)
	_ = s.sendProjectRules(context.Background(), "proj", meta, "")

	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests after the clamp elapsed, want 2", got)
	}
}

// A failure carrying no retry deadline keeps the ordinary repository TTL, so
// honouring Retry-After does not silently lengthen every other failure.
func TestSendProjectRulesUsesRuleScanTTLWithoutRetryAfter(t *testing.T) {
	srv, hits := countingServer(t, http.StatusInternalServerError)
	advance := useFakeClock(t)
	s := newRuleSyncer(t, srv.URL)
	meta := &projectMeta{repositoryRoot: ruleRepo(t), repositoryID: "github.com/org/repo"}

	_ = s.sendProjectRules(context.Background(), "proj", meta, "")
	advance(ruleScanTTL / 2)
	_ = s.sendProjectRules(context.Background(), "proj", meta, "")
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests inside the TTL, want 1", got)
	}

	advance(ruleScanTTL)
	_ = s.sendProjectRules(context.Background(), "proj", meta, "")
	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests after the TTL, want 2", got)
	}
}

// Parking is per repository: one failing repository must not suppress another.
func TestSendProjectRulesParksPerRepository(t *testing.T) {
	srv, hits := countingServer(t, http.StatusInternalServerError)
	s := newRuleSyncer(t, srv.URL)

	first := &projectMeta{repositoryRoot: ruleRepo(t), repositoryID: "github.com/org/own"}
	second := &projectMeta{repositoryRoot: ruleRepo(t), repositoryID: "github.com/org/victim"}

	_ = s.sendProjectRules(context.Background(), "proj", first, "")
	_ = s.sendProjectRules(context.Background(), "proj", second, "")
	_ = s.sendProjectRules(context.Background(), "proj", first, "")

	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests issued, want 2 (one per repository)", got)
	}
}

// Access denied is durable: a repository the token cannot read stays unreadable.
// Retrying it on the ordinary 30s TTL costs one request and one log line every
// 30 seconds for as long as the daemon runs, and the log is where update
// failures have to be found afterwards. A client on v0.7.27 filled its whole
// sync.log with this message -- 200 lines covered 30 minutes, and the update
// error that the diagnosis needed had already scrolled away (#458).
func TestSendProjectRulesParksForbiddenLongerThanScanTTL(t *testing.T) {
	srv, hits := countingServer(t, http.StatusForbidden)
	advance := useFakeClock(t)
	s := newRuleSyncer(t, srv.URL)
	meta := &projectMeta{repositoryRoot: ruleRepo(t), repositoryID: "github.com/org/repo"}

	_ = s.sendProjectRules(context.Background(), "proj", meta, "")
	advance(ruleScanTTL * 2)
	_ = s.sendProjectRules(context.Background(), "proj", meta, "")
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests after twice the scan TTL, want 1 -- forbidden must park longer", got)
	}

	advance(RuleForbiddenTTL)
	_ = s.sendProjectRules(context.Background(), "proj", meta, "")
	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests after the forbidden TTL, want 2 -- the park must expire", got)
	}
}
