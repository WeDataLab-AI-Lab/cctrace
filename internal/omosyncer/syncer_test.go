package omosyncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cctrace/internal/syncer"
)

// newTestServer creates an httptest.Server that captures sync requests.
func newTestServer(t *testing.T) (*httptest.Server, *[]map[string]interface{}) {
	t.Helper()
	var captured []map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		captured = append(captured, body)
		json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func newTestState(t *testing.T) *syncer.State {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := syncer.LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// writeOmoFixture writes an omo session JSONL fixture under
// <omoDir>/sessions/--<slug>--/<filename> and returns its path. Sessions are
// only discovered from directly inside a "--*--" directory, mirroring omo's
// real on-disk layout.
func writeOmoFixture(t *testing.T, omoDir, slug, filename string, lines []string) string {
	t.Helper()
	dir := filepath.Join(omoDir, "sessions", "--"+slug+"--")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filename)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
	f.Close()
	return path
}

func sessionHeader(cwd, title string) string {
	return fmt.Sprintf(`{"type":"session","cwd":%q,"title":%q}`, cwd, title)
}

func userMessage(id, parentID, ts, text string) string {
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":%q,"timestamp":%q,"message":{"role":"user","content":%q}}`, id, parentID, ts, text)
}

func assistantMessage(id, parentID, ts, model, provider, text string, input, output, cacheRead, cacheWrite int) string {
	msg := map[string]interface{}{
		"role":     "assistant",
		"model":    model,
		"provider": provider,
		"content":  text,
		"usage": map[string]interface{}{
			"input":       input,
			"output":      output,
			"cacheRead":   cacheRead,
			"cacheWrite":  cacheWrite,
			"totalTokens": input + output,
			"cost":        map[string]interface{}{"total": 0.5},
		},
	}
	line := map[string]interface{}{
		"type":      "message",
		"id":        id,
		"parentId":  parentID,
		"timestamp": ts,
		"message":   msg,
	}
	b, err := json.Marshal(line)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestOmoSyncer_SyncOnce_NoFiles(t *testing.T) {
	srv, _ := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	omoDir := t.TempDir()
	os.MkdirAll(filepath.Join(omoDir, "sessions"), 0o755)

	s := New([]string{omoDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 records, got %d", n)
	}
}

func TestOmoSyncer_SyncOnce_FirstRunClosesWindowOnZeroFilePass(t *testing.T) {
	srv, _ := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	omoDir := t.TempDir()
	os.MkdirAll(filepath.Join(omoDir, "sessions"), 0o755)

	s := New([]string{omoDir}, "user@example.com", "uid-001", state, client, nil)
	if !state.IsNew() {
		t.Fatal("state should start new")
	}
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if state.IsNew() {
		t.Fatal("first-run window should have closed after a zero-file pass")
	}

	// A session that appears after the window closed must be collected from
	// its start, not skipped as pre-existing.
	path := writeOmoFixture(t, omoDir, "Users-alice-proj", "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000101.jsonl", []string{
		sessionHeader("/Users/alice/proj", "example session"),
		userMessage("m1", "", "2026-01-01T00:00:01.000Z", "hello"),
	})
	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 record from the post-window session, got %d", n)
	}
	if state.GetOffset(path) == 0 {
		t.Fatal("expected non-zero offset after sync")
	}
}

func TestOmoSyncer_SyncOnce_PreExistingFileSkippedAtFirstSync(t *testing.T) {
	srv, _ := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	omoDir := t.TempDir()
	path := writeOmoFixture(t, omoDir, "Users-alice-proj", "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000102.jsonl", []string{
		sessionHeader("/Users/alice/proj", "pre-existing session"),
		userMessage("m1", "", "2026-01-01T00:00:01.000Z", "already here before first sync"),
	})

	s := New([]string{omoDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 records for a pre-existing file at first sync, got %d", n)
	}
	fs := state.Files[path]
	if fs == nil {
		t.Fatal("expected file state to be recorded")
	}
	if fs.SkippedAtFirstSync == 0 {
		t.Fatal("expected SkippedAtFirstSync to be recorded")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fs.Offset != fi.Size() {
		t.Fatalf("offset = %d, want file skipped to EOF (%d)", fs.Offset, fi.Size())
	}
}

func TestOmoSyncer_SyncOnce_IncrementalOffsetResumeAcrossTwoPasses(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	omoDir := t.TempDir()
	dir := filepath.Join(omoDir, "sessions", "--Users-alice-proj--")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000103.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, sessionHeader("/Users/alice/proj", "growing session"))
	fmt.Fprintln(f, userMessage("m1", "", "2026-01-01T00:00:01.000Z", "first turn"))
	f.Close()

	// State already existed before this file appeared (not a first-run skip).
	state.SetOffset(filepath.Join(omoDir, "sentinel"), 0)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	s := New([]string{omoDir}, "user@example.com", "uid-001", state, client, nil)
	n1, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce (pass 1): %v", err)
	}
	if n1 != 1 {
		t.Fatalf("pass 1: expected 1 record, got %d", n1)
	}
	offsetAfterPass1 := state.GetOffset(path)
	if offsetAfterPass1 == 0 {
		t.Fatal("expected non-zero offset after pass 1")
	}

	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, userMessage("m2", "m1", "2026-01-01T00:00:02.000Z", "second turn"))
	f.Close()

	n2, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce (pass 2): %v", err)
	}
	if n2 != 1 {
		t.Fatalf("pass 2: expected 1 new record, got %d", n2)
	}
	if state.GetOffset(path) <= offsetAfterPass1 {
		t.Fatalf("offset did not advance: pass1=%d pass2=%d", offsetAfterPass1, state.GetOffset(path))
	}

	if len(*captured) != 2 {
		t.Fatalf("expected 2 sync requests (one per pass), got %d", len(*captured))
	}
}

func TestOmoSyncer_SyncOnce_ClaudeSDKOAuthRecordForwardedWithUsageStripped(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	omoDir := t.TempDir()
	// Pre-existing state entry so this is treated as an ordinary incremental
	// sync rather than a first-run skip.
	sentinel := filepath.Join(omoDir, "sentinel")
	state.SetOffset(sentinel, 0)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	writeOmoFixture(t, omoDir, "Users-alice-proj", "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000104.jsonl", []string{
		sessionHeader("/Users/alice/proj", "claude driven session"),
		assistantMessage("m1", "", "2026-01-01T00:00:01.000Z", "claude-sonnet-5", "claude-sdk-oauth", "hi there", 100, 200, 10, 5),
	})

	s := New([]string{omoDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected the record to still be sent, got n=%d", n)
	}
	if len(*captured) != 1 {
		t.Fatalf("expected 1 sync request, got %d", len(*captured))
	}
	records, ok := (*captured)[0]["records"].([]interface{})
	if !ok || len(records) != 1 {
		t.Fatalf("expected 1 record in request, got %+v", (*captured)[0]["records"])
	}
	rec := records[0].(map[string]interface{})
	if rec["billing_provider"] != "anthropic" {
		t.Fatalf("billing_provider = %v, want anthropic", rec["billing_provider"])
	}
	for _, field := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_create_tokens"} {
		if v, ok := rec[field]; ok && v != nil {
			t.Fatalf("expected %s to be stripped for a claude-sdk-oauth record, got %v", field, v)
		}
	}
}

func TestOmoSyncer_SyncOnce_OpenAICodexRecordForwardedWithUsageIntact(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	omoDir := t.TempDir()
	sentinel := filepath.Join(omoDir, "sentinel")
	state.SetOffset(sentinel, 0)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	writeOmoFixture(t, omoDir, "Users-alice-proj", "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000105.jsonl", []string{
		sessionHeader("/Users/alice/proj", "codex driven session"),
		assistantMessage("m1", "", "2026-01-01T00:00:01.000Z", "gpt-5", "openai-codex", "hi there", 100, 200, 10, 5),
	})

	s := New([]string{omoDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 record, got n=%d", n)
	}
	records := (*captured)[0]["records"].([]interface{})
	rec := records[0].(map[string]interface{})
	if rec["billing_provider"] != "openai" {
		t.Fatalf("billing_provider = %v, want openai", rec["billing_provider"])
	}
	if got := rec["input_tokens"]; got != float64(100) {
		t.Fatalf("input_tokens = %v, want 100", got)
	}
	if got := rec["output_tokens"]; got != float64(200) {
		t.Fatalf("output_tokens = %v, want 200", got)
	}
	if got := rec["cache_read_tokens"]; got != float64(10) {
		t.Fatalf("cache_read_tokens = %v, want 10", got)
	}
	if got := rec["cache_create_tokens"]; got != float64(5) {
		t.Fatalf("cache_create_tokens = %v, want 5", got)
	}
}

func TestOmoSyncer_SyncOnce_UnknownProviderMapsBillingToOtherButKeepsUsage(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	omoDir := t.TempDir()
	sentinel := filepath.Join(omoDir, "sentinel")
	state.SetOffset(sentinel, 0)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	writeOmoFixture(t, omoDir, "Users-alice-proj", "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000106.jsonl", []string{
		sessionHeader("/Users/alice/proj", "unknown provider session"),
		assistantMessage("m1", "", "2026-01-01T00:00:01.000Z", "some-model", "", "hi there", 7, 8, 1, 2),
	})

	s := New([]string{omoDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 record, got n=%d", n)
	}
	records := (*captured)[0]["records"].([]interface{})
	rec := records[0].(map[string]interface{})
	if rec["billing_provider"] != "other" {
		t.Fatalf("billing_provider = %v, want other for an empty source provider", rec["billing_provider"])
	}
	if got := rec["input_tokens"]; got != float64(7) {
		t.Fatalf("input_tokens = %v, want 7 (empty provider must not be treated as double-counted)", got)
	}
}

func TestUsageIsDoubleCounted(t *testing.T) {
	cases := []struct {
		provider string
		want     bool
	}{
		{"claude-sdk-oauth", true},
		{"CLAUDE-SDK-OAUTH", true},
		{"  claude-sdk-oauth  ", true},
		{"openai-codex", false},
		{"", false},
		{"some-other-provider", false},
	}
	for _, tc := range cases {
		if got := usageIsDoubleCounted(tc.provider); got != tc.want {
			t.Errorf("usageIsDoubleCounted(%q) = %v, want %v", tc.provider, got, tc.want)
		}
	}
}

func TestBillingProviderFor(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{"claude-sdk-oauth", "anthropic"},
		{"CLAUDE-SDK-OAUTH", "anthropic"},
		{"anthropic", "anthropic"},
		{"openai-codex", "openai"},
		{"OPENAI-CODEX", "openai"},
		// omo's own "openai" is API-key metered usage, not Codex subscription
		// usage -- it must NOT collide with the canonical "openai" value.
		{"openai", "other"},
		{"OpenAI", "other"},
		{"agent-memory", "other"},
		{"some-unrecognized-provider", "other"},
		{"", "other"},
	}
	for _, tc := range cases {
		if got := billingProviderFor(tc.provider); got != tc.want {
			t.Errorf("billingProviderFor(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}

// TestBillingProviderFor_DoesNotCollapseIntoUsageDoubleCountPredicate pins the
// interaction the correction called out explicitly: billingProviderFor and
// usageIsDoubleCounted both key off the source provider string, but they are
// not the same decision. "claude-sdk-oauth" and a bare "anthropic" map to the
// same billing_provider ("anthropic"), yet only claude-sdk-oauth's usage is
// double-counted and must be stripped -- a bare "anthropic" record keeps its
// usage.
func TestBillingProviderFor_DoesNotCollapseIntoUsageDoubleCountPredicate(t *testing.T) {
	if billingProviderFor("claude-sdk-oauth") != billingProviderFor("anthropic") {
		t.Fatal("claude-sdk-oauth and anthropic should map to the same billing_provider")
	}
	if !usageIsDoubleCounted("claude-sdk-oauth") {
		t.Fatal("claude-sdk-oauth usage should be double-counted")
	}
	if usageIsDoubleCounted("anthropic") {
		t.Fatal("a bare anthropic provider should NOT be treated as double-counted, even though it shares a billing_provider with claude-sdk-oauth")
	}
}

// TestEntrypointForPath verifies the session-root -> entrypoint mapping used
// as omo's only available human-turn signal (see the doc comment on
// entrypointForPath in syncer.go): a file under the plain sessions root is
// human-facing and gets "cli"; a file under agent/sessions is automated and
// gets "omo_agent", a truthful name for omo's own task runner as launcher --
// not "sdk-cli", which is reserved elsewhere for a Claude Code launcher.
func TestEntrypointForPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{
			name: "plain sessions root",
			path: "/home/user/.omo/sessions/--Users-alice-proj--/2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000201.jsonl",
			want: "cli",
		},
		{
			name: "agent sessions root",
			path: "/home/user/.omo/agent/sessions/--Users-alice-proj--/2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000202.jsonl",
			want: "omo_agent",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := entrypointForPath(c.path); got != c.want {
				t.Errorf("entrypointForPath(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

// TestOmoSyncer_SyncOnce_EntrypointWiredFromSessionRoot verifies the
// session-root -> entrypoint mapping is applied through a full sync pass, not
// just in the entrypointForPath helper: a file under the plain sessions root
// produces records with entrypoint "cli", while a file under agent/sessions
// produces records with entrypoint "omo_agent".
func TestOmoSyncer_SyncOnce_EntrypointWiredFromSessionRoot(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	omoDir := t.TempDir()
	sentinel := filepath.Join(omoDir, "sentinel")
	state.SetOffset(sentinel, 0)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	writeOmoFixture(t, omoDir, "Users-alice-proj", "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000203.jsonl", []string{
		sessionHeader("/Users/alice/proj", "human session"),
		userMessage("m1", "", "2026-01-01T00:00:01.000Z", "hello"),
	})

	agentDir := filepath.Join(omoDir, "agent", "sessions", "--Users-alice-proj--")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(agentDir, "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000204.jsonl")
	f, err := os.Create(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, sessionHeader("/Users/alice/proj", "omo-spawned session"))
	fmt.Fprintln(f, userMessage("m2", "", "2026-01-01T00:00:02.000Z", "spawned turn"))
	f.Close()

	s := New([]string{omoDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 records, got %d", n)
	}

	entrypoints := map[string]int{}
	for _, req := range *captured {
		records, ok := req["records"].([]interface{})
		if !ok {
			continue
		}
		for _, raw := range records {
			rec := raw.(map[string]interface{})
			ep, _ := rec["entrypoint"].(string)
			entrypoints[ep]++
		}
	}
	if entrypoints["cli"] != 1 {
		t.Errorf("expected 1 record with entrypoint=cli, got %d (all: %#v)", entrypoints["cli"], entrypoints)
	}
	if entrypoints["omo_agent"] != 1 {
		t.Errorf("expected 1 record with entrypoint=omo_agent, got %d (all: %#v)", entrypoints["omo_agent"], entrypoints)
	}
}

func TestOmoSyncer_SyncOnce_StopsOnCancelledContext(t *testing.T) {
	srv, _ := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	omoDir := t.TempDir()
	sentinel := filepath.Join(omoDir, "sentinel")
	state.SetOffset(sentinel, 0)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	writeOmoFixture(t, omoDir, "Users-alice-proj", "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000107.jsonl", []string{
		sessionHeader("/Users/alice/proj", "cancelled pass session"),
		userMessage("m1", "", "2026-01-01T00:00:01.000Z", "hello"),
	})

	s := New([]string{omoDir}, "user@example.com", "uid-001", state, client, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.SyncOnce(ctx); err == nil || err != context.Canceled {
		t.Fatalf("SyncOnce err = %v, want context.Canceled", err)
	}
}

func TestOmoSyncer_SyncOnce_StopsWhenSendDeadlineExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
	}))
	defer srv.Close()
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	omoDir := t.TempDir()
	sentinel := filepath.Join(omoDir, "sentinel")
	state.SetOffset(sentinel, 0)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	writeOmoFixture(t, omoDir, "Users-alice-proj", "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000108.jsonl", []string{
		sessionHeader("/Users/alice/proj", "slow send session"),
		userMessage("m1", "", "2026-01-01T00:00:01.000Z", "hello"),
	})

	s := New([]string{omoDir}, "user@example.com", "uid-001", state, client, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	if _, err := s.SyncOnce(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SyncOnce err = %v, want context deadline", err)
	}
}
