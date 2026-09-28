package gjcsyncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/gjclog"
	"cctrace/internal/store"
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

// writeGjcFixture writes a main session file at
// agent/sessions/v2-<slug>/<ts>_<uuid>.jsonl under gjcDir.
func writeGjcFixture(t *testing.T, gjcDir, slug, sessionUUID string, lines []string) string {
	t.Helper()
	dir := filepath.Join(gjcDir, "agent", "sessions", "v2-"+slug)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	name := "2026-08-14T10-00-00-000Z_" + sessionUUID + ".jsonl"
	path := filepath.Join(dir, name)
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

// writeGjcSubagentFixture writes a subagent transcript at
// agent/sessions/v2-<slug>/<ts>_<parentUUID>/<subagentId>.jsonl under gjcDir.
func writeGjcSubagentFixture(t *testing.T, gjcDir, slug, parentUUID, subagentID string, lines []string) string {
	t.Helper()
	dir := filepath.Join(gjcDir, "agent", "sessions", "v2-"+slug, "2026-08-14T10-00-00-000Z_"+parentUUID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, subagentID+".jsonl")
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

func headerLine(sessionID, cwd string) string {
	return fmt.Sprintf(`{"type":"session","id":%q,"cwd":%q,"title":"synthetic"}`, sessionID, cwd)
}

func userLine(id, parentID, ts, text string) string {
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":%q,"timestamp":%q,"message":{"role":"user","content":[{"type":"text","text":%q}]}}`, id, parentID, ts, text)
}

// userLineWithAttribution is userLine plus an explicit attribution field, for
// exercising the attribution -> prompt_source mapping through a full sync pass.
func userLineWithAttribution(id, parentID, ts, text, attribution string) string {
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":%q,"timestamp":%q,"message":{"role":"user","attribution":%q,"content":[{"type":"text","text":%q}]}}`, id, parentID, ts, attribution, text)
}

func assistantLine(id, parentID, ts, provider, model, text string) string {
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":%q,"timestamp":%q,"message":{"role":"assistant","provider":%q,"model":%q,"usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"totalTokens":15},"content":[{"type":"text","text":%q}]}}`,
		id, parentID, ts, provider, model, text)
}

// assistantWithToolCallsLine builds an assistant message whose content carries
// two toolCall blocks alongside the text block, exercising the sibling
// tool_call records that share MessageID and Timestamp with the assistant
// record and with each other.
func assistantWithToolCallsLine(id, parentID, ts, provider, model string) string {
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":%q,"timestamp":%q,"message":{"role":"assistant","provider":%q,"model":%q,"content":[{"type":"text","text":"working on it"},{"type":"toolCall","id":"tc-1","name":"Bash","arguments":{"command":"ls"}},{"type":"toolCall","id":"tc-2","name":"Read","arguments":{"path":"/tmp/x"}}]}}`,
		id, parentID, ts, provider, model)
}

func TestGjcSyncer_SyncOnce_NoFiles(t *testing.T) {
	srv, _ := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	gjcDir := t.TempDir()
	os.MkdirAll(filepath.Join(gjcDir, "agent", "sessions"), 0755)

	gs := New([]string{gjcDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := gs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 records, got %d", n)
	}
}

// TestGjcSyncer_SyncOnce_IncrementalOffsetResume verifies a second pass only
// picks up lines appended after the first pass, resuming from the persisted
// offset rather than rescanning the whole file.
func TestGjcSyncer_SyncOnce_IncrementalOffsetResume(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	// Mark state as not-new before the fixture exists, so the file is a "new
	// session" (synced from the start) rather than "pre-existing at first
	// sync" (skipped to EOF). The first-run skip path has its own tests below.
	state.Save()
	client := syncer.NewClient(srv.URL, "", "")

	gjcDir := t.TempDir()
	sessionUUID := "019f0000-0000-7000-8000-000000000001"
	path := writeGjcFixture(t, gjcDir, "abcde", sessionUUID, []string{
		headerLine(sessionUUID, "/Users/alice/project"),
		userLine("m1", "", "2026-08-14T10:00:01.000Z", "hello"),
		assistantLine("m2", "m1", "2026-08-14T10:00:02.000Z", "anthropic", "claude-x", "hi there"),
	})

	gs := New([]string{gjcDir}, "user@example.com", "uid-001", state, client, nil)
	n1, err := gs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce (pass 1): %v", err)
	}
	if n1 != 2 {
		t.Fatalf("pass 1: expected 2 records, got %d", n1)
	}
	*captured = nil

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, userLine("m3", "m2", "2026-08-14T10:00:03.000Z", "one more"))
	f.Close()

	n2, err := gs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce (pass 2): %v", err)
	}
	if n2 != 1 {
		t.Fatalf("pass 2: expected 1 new record (offset resume), got %d", n2)
	}
	if len(*captured) != 1 {
		t.Fatalf("expected exactly one sync request on pass 2, got %d", len(*captured))
	}
}

// TestGjcSyncer_FirstRunWindowClosesWithNoFiles reproduces the codex-side
// defect: a first pass finding zero files must still close the first-run
// window, or the first session created afterwards is skipped whole as if it
// were pre-existing history. gjc has no lifecycle hook, so enabling the
// integration is always itself the first run.
func TestGjcSyncer_FirstRunWindowClosesWithNoFiles(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	gjcDir := t.TempDir()
	os.MkdirAll(filepath.Join(gjcDir, "agent", "sessions"), 0755)

	gs := New([]string{gjcDir}, "user@example.com", "uid-001", state, client, nil)
	if _, err := gs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (empty pass): %v", err)
	}
	if state.IsNew() {
		t.Fatal("first-run window did not close on a zero-file pass")
	}

	sessionUUID := "019f0000-0000-7000-8000-000000000002"
	writeGjcFixture(t, gjcDir, "fghij", sessionUUID, []string{
		headerLine(sessionUUID, "/Users/alice/project"),
		userLine("m1", "", "2026-08-14T10:00:01.000Z", "hello after enable"),
	})

	n, err := gs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce (second pass): %v", err)
	}
	if n == 0 || len(*captured) == 0 {
		t.Fatal("a session created after the first run was skipped as pre-existing history")
	}
}

// TestGjcSyncer_SyncOnce_SkipsPreExistingFileAtFirstSync verifies a
// pre-existing file is skipped to EOF on first run, logs the skip, and
// persists SkippedAtFirstSync so cctrace status can surface it.
func TestGjcSyncer_SyncOnce_SkipsPreExistingFileAtFirstSync(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	gjcDir := t.TempDir()
	sessionUUID := "019f0000-0000-7000-8000-000000000003"
	path := writeGjcFixture(t, gjcDir, "klmno", sessionUUID, []string{
		headerLine(sessionUUID, "/Users/alice/project"),
		userLine("m1", "", "2026-08-14T10:00:01.000Z", "already here before install"),
	})

	var logBuf strings.Builder
	restore := redirectLog(&logBuf)
	defer restore()

	gs := New([]string{gjcDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := gs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 records for pre-existing file, got %d", n)
	}
	if len(*captured) != 0 {
		t.Fatalf("expected no HTTP calls for pre-existing file, got %d", len(*captured))
	}
	if !strings.Contains(logBuf.String(), "skipped (pre-existing") {
		t.Fatalf("expected a skip log line, got:\n%s", logBuf.String())
	}

	fs := state.Files[path]
	if fs == nil {
		t.Fatal("expected a file state entry after skip")
	}
	if fs.SkippedAtFirstSync == 0 {
		t.Error("expected SkippedAtFirstSync to be recorded")
	}
}

// TestGjcSyncer_SyncOnce_SubagentSessionIDFromMetadata verifies a subagent
// transcript file (whose filename is an arbitrary subagentId, not a UUID)
// recovers its session id from the header on the first pass and keeps
// tagging its records with that session id on a later, resumed pass where
// the header line is no longer re-read.
func TestGjcSyncer_SyncOnce_SubagentSessionIDFromMetadata(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	gjcDir := t.TempDir()
	parentUUID := "019f0000-0000-7000-8000-000000000004"
	subagentSessionID := "019f0000-0000-7000-8000-0000000000aa"
	path := writeGjcSubagentFixture(t, gjcDir, "pqrst", parentUUID, "sub-task-1", []string{
		headerLine(subagentSessionID, "/Users/alice/project"),
		userLine("m1", "", "2026-08-14T10:00:01.000Z", "subagent turn 1"),
	})

	gs := New([]string{gjcDir}, "user@example.com", "uid-001", state, client, nil)
	if _, err := gs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 1): %v", err)
	}
	if _, err := gs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (empty pass to close first-run window): %v", err)
	}
	*captured = nil

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, userLine("m2", "m1", "2026-08-14T10:00:02.000Z", "subagent turn 2"))
	f.Close()

	n, err := gs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce (pass 2, resumed): %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 new record, got %d", n)
	}
	if len(*captured) != 1 {
		t.Fatalf("expected 1 sync request, got %d", len(*captured))
	}
	records, ok := (*captured)[0]["records"].([]interface{})
	if !ok || len(records) != 1 {
		t.Fatalf("expected 1 record in payload, got %#v", (*captured)[0]["records"])
	}
	rec := records[0].(map[string]interface{})
	if rec["session_id"] != subagentSessionID {
		t.Errorf("session_id = %v, want %s (recovered from header via persisted metadata)", rec["session_id"], subagentSessionID)
	}
}

// TestToStoreRecord_BillingProviderMapping covers the three gjc providers
// that can appear within a single session, plus an unrecognized value that
// must pass through unchanged.
func TestToStoreRecord_BillingProviderMapping(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{"anthropic", "anthropic"},
		{"openai-codex", "openai"},
		{"amazon-bedrock", "other"},
		{"some-future-provider", "other"},
		{"", "other"},
	}
	for _, c := range cases {
		r := &gjclog.Record{
			SessionID:  "019f0000-0000-7000-8000-000000000005",
			RecordType: "assistant",
			Provider:   c.provider,
			Raw:        json.RawMessage(`{}`),
		}
		sr := toStoreRecord(r, "user@example.com", "uid-001", "session.jsonl")
		if sr == nil {
			t.Fatalf("toStoreRecord returned nil for provider %q", c.provider)
		}
		if sr.BillingProvider != c.want {
			t.Errorf("provider %q: BillingProvider = %q, want %q", c.provider, sr.BillingProvider, c.want)
		}
		if sr.Agent != "gjc" {
			t.Errorf("provider %q: Agent = %q, want gjc", c.provider, sr.Agent)
		}
	}
}

// TestGjcSyncer_SyncOnce_SiblingToolCallRecordsSurviveConflict verifies that
// an assistant record and the tool_call records for its toolCall content
// blocks - which share MessageID and Timestamp because they come from one
// source line - are not collapsed into fewer records than were parsed.
func TestGjcSyncer_SyncOnce_SiblingToolCallRecordsSurviveConflict(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	// Not-new state: see TestGjcSyncer_SyncOnce_IncrementalOffsetResume.
	state.Save()
	client := syncer.NewClient(srv.URL, "", "")

	gjcDir := t.TempDir()
	sessionUUID := "019f0000-0000-7000-8000-000000000006"
	writeGjcFixture(t, gjcDir, "uvwxy", sessionUUID, []string{
		headerLine(sessionUUID, "/Users/alice/project"),
		userLine("m1", "", "2026-08-14T10:00:01.000Z", "please run these tools"),
		assistantWithToolCallsLine("m2", "m1", "2026-08-14T10:00:02.000Z", "anthropic", "claude-x"),
	})

	gs := New([]string{gjcDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := gs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	// user + assistant + 2 tool_call siblings = 4 records.
	if n != 4 {
		t.Fatalf("expected 4 records (assistant + 2 sibling tool_calls survive), got %d", n)
	}
	if len(*captured) != 1 {
		t.Fatalf("expected 1 sync request, got %d", len(*captured))
	}
	records, ok := (*captured)[0]["records"].([]interface{})
	if !ok || len(records) != 4 {
		t.Fatalf("expected 4 records in payload, got %#v", (*captured)[0]["records"])
	}
	toolCallCount := 0
	toolCallTimestamps := map[string]int{}
	for _, raw := range records {
		rec := raw.(map[string]interface{})
		if rec["record_type"] == "tool_call" {
			toolCallCount++
			if ts, ok := rec["ts"].(string); ok {
				toolCallTimestamps[ts]++
			}
		}
	}
	if toolCallCount != 2 {
		t.Errorf("expected 2 tool_call records, got %d", toolCallCount)
	}
	// The two tool_call siblings share both SessionID and Timestamp (and,
	// unlike the assistant record, RecordType too), so a naive dedup keyed on
	// (session, ts, record_type, profile) collides them. The conflict-avoidance
	// nudge must have separated their timestamps by a microsecond so both
	// distinct records made it into the payload instead of one clobbering the
	// other.
	for ts, count := range toolCallTimestamps {
		if count > 1 {
			t.Errorf("expected the 2 tool_call siblings to have distinct timestamps after conflict avoidance, but %d share %s", count, ts)
		}
	}
}

// TestGjcSyncer_SyncOnce_AttributionWiredToPromptSource verifies the
// attribution -> prompt_source mapping is applied through a full sync pass,
// not just in the toStoreRecord helper: a "user" attribution record produces
// prompt_source "typed" in the payload actually sent to the server, while an
// "agent" attribution record's prompt_source stays empty.
func TestGjcSyncer_SyncOnce_AttributionWiredToPromptSource(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	state.Save()
	client := syncer.NewClient(srv.URL, "", "")

	gjcDir := t.TempDir()
	sessionUUID := "019f0000-0000-7000-8000-000000000009"
	writeGjcFixture(t, gjcDir, "attrib", sessionUUID, []string{
		headerLine(sessionUUID, "/Users/alice/project"),
		userLineWithAttribution("m1", "", "2026-08-14T10:00:01.000Z", "human typed this", "user"),
		userLineWithAttribution("m2", "m1", "2026-08-14T10:00:02.000Z", "gjc generated this", "agent"),
	})

	gs := New([]string{gjcDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := gs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 records, got %d", n)
	}
	records, ok := (*captured)[0]["records"].([]interface{})
	if !ok || len(records) != 2 {
		t.Fatalf("expected 2 records in payload, got %#v", (*captured)[0]["records"])
	}
	byTs := map[string]map[string]interface{}{}
	for _, raw := range records {
		rec := raw.(map[string]interface{})
		byTs[rec["ts"].(string)] = rec
	}
	m1 := byTs["2026-08-14T10:00:01Z"]
	m2 := byTs["2026-08-14T10:00:02Z"]
	if m1 == nil || m2 == nil {
		t.Fatalf("expected records at both timestamps, got %#v", byTs)
	}
	if got := m1["prompt_source"]; got != "typed" {
		t.Errorf("m1 (attribution=user): prompt_source = %v, want typed", got)
	}
	if got, ok := m2["prompt_source"]; ok && got != "" && got != nil {
		t.Errorf("m2 (attribution=agent): prompt_source = %v, want empty", got)
	}
}

// TestToStoreRecord_PromptSourceFromAttribution verifies gjc's attribution
// field maps onto the canonical prompt_source vocabulary: only "user" (human
// typed) produces the genuine-turn marker "typed"; "agent" (gjc's own
// generated turns) and any unrecognized or empty value must leave
// prompt_source empty rather than optimistically claiming human authorship.
func TestToStoreRecord_PromptSourceFromAttribution(t *testing.T) {
	cases := []struct {
		attribution string
		want        string
	}{
		{"user", "typed"},
		{"agent", ""},
		{"some-future-value", ""},
		{"", ""},
	}
	for _, c := range cases {
		r := &gjclog.Record{
			SessionID:   "019f0000-0000-7000-8000-000000000007",
			RecordType:  "user",
			Attribution: c.attribution,
			Raw:         json.RawMessage(`{}`),
		}
		sr := toStoreRecord(r, "user@example.com", "uid-001", "session.jsonl")
		if sr == nil {
			t.Fatalf("toStoreRecord returned nil for attribution %q", c.attribution)
		}
		if sr.PromptSource != c.want {
			t.Errorf("attribution %q: PromptSource = %q, want %q", c.attribution, sr.PromptSource, c.want)
		}
	}
}

// Tool identity is carried only by the records that represent one tool
// invocation or its result; other record types never pick it up.
func TestToStoreRecord_ToolIdentity(t *testing.T) {
	cases := []struct {
		recordType       string
		wantName, wantID string
	}{
		{"tool_call", "bash", "tc1"},
		{"tool_result", "bash", "tc1"},
		{"assistant", "", ""},
	}
	for _, c := range cases {
		r := &gjclog.Record{RecordType: c.recordType, ToolName: "bash", ToolCallID: "tc1", Raw: json.RawMessage(`{}`)}
		sr := toStoreRecord(r, "user@example.com", "uid-001", "session.jsonl")
		if sr.ToolName != c.wantName || sr.ToolCallID != c.wantID {
			t.Errorf("%s: ToolName = %q, ToolCallID = %q; want %q, %q", c.recordType, sr.ToolName, sr.ToolCallID, c.wantName, c.wantID)
		}
	}
}

// TestToStoreRecord_PromptSourceIgnoredOnNonUserRecords verifies that even if
// an attribution value happens to be set on an assistant or tool_result
// record, it is not translated into prompt_source: attribution only carries
// meaning on user turns.
func TestToStoreRecord_PromptSourceIgnoredOnNonUserRecords(t *testing.T) {
	for _, recordType := range []string{"assistant", "tool_result"} {
		r := &gjclog.Record{
			SessionID:   "019f0000-0000-7000-8000-000000000008",
			RecordType:  recordType,
			Attribution: "user",
			Raw:         json.RawMessage(`{}`),
		}
		sr := toStoreRecord(r, "user@example.com", "uid-001", "session.jsonl")
		if sr == nil {
			t.Fatalf("toStoreRecord returned nil for record type %q", recordType)
		}
		if sr.PromptSource != "" {
			t.Errorf("record type %q: PromptSource = %q, want empty (non-user records must be unaffected)", recordType, sr.PromptSource)
		}
	}
}

// A narrow unit test of the nudge itself, independent of the sync pass, to pin
// the key shape gjc's sibling tool_call records depend on.
func TestAvoidSessionRecordConflict(t *testing.T) {
	nudger := syncer.NewConflictNudger(nil)
	base := "2026-08-14T10:00:02Z"
	ts, _ := parseRFC3339(base)
	r1 := &store.SessionRecord{SessionID: "s1", RecordType: "tool_call", ProfileEmail: "user@example.com", Ts: ts}
	r2 := &store.SessionRecord{SessionID: "s1", RecordType: "tool_call", ProfileEmail: "user@example.com", Ts: ts}
	nudger.Apply(r1)
	nudger.Apply(r2)
	if r1.Ts.Equal(r2.Ts) {
		t.Fatal("expected the second colliding record's timestamp to be nudged apart from the first")
	}
}
