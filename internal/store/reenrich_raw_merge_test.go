package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func storedRaw(t *testing.T, s *PgStore, sessionID, uuid string) map[string]interface{} {
	t.Helper()
	var raw []byte
	if err := s.pool.QueryRow(context.Background(),
		`SELECT raw FROM session_records WHERE session_id = $1 AND uuid = $2`, sessionID, uuid).Scan(&raw); err != nil {
		t.Fatalf("read raw %s/%s: %v", sessionID, uuid, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("raw %s/%s is not an object: %s", sessionID, uuid, raw)
	}
	return m
}

// sessionNumbers is everything the dashboard derives for one session from its
// rows: the overview rollup in every scope, the task segments, and the imputed
// cost. A raw merge adds keys none of them read, so none of them may move.
func sessionNumbers(t *testing.T, s *PgStore, sessionID string) (rollups, segments, imputed string) {
	t.Helper()
	err := s.pool.QueryRow(context.Background(), `SELECT
		(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.scope_type, r.scope_value), '[]'::jsonb)::text
			FROM session_overview_rollups r WHERE r.session_id = $1),
		(SELECT COALESCE(jsonb_agg(to_jsonb(f) ORDER BY f.start_ts, f.boundary_record_id), '[]'::jsonb)::text
			FROM task_segment_facts f WHERE f.session_id = $1),
		(SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY c.srec_id), '[]'::jsonb)::text
			FROM claude_imputed_cost c WHERE c.session_id = $1)`, sessionID).Scan(&rollups, &segments, &imputed)
	if err != nil {
		t.Fatalf("session numbers: %v", err)
	}
	return rollups, segments, imputed
}

// Re-enrichment is the only route by which rows stored before a client kept
// these fields can get them: the insert path is ON CONFLICT DO NOTHING. The
// merge adds collected keys the stored raw lacks and nothing else -- a key the
// row already has keeps its stored value, a redaction marker included, and
// message, or any other key the client sends, is never merged. That does not by
// itself keep a relaxed policy from un-redacting a row: a field the row lacks is
// added under the policy in force when re-enrichment runs, which is why the
// client sends content fields only when asked (see TestReenrichDoesNotUnredact
// in tests). A NUL escape in the fresh raw must not fail the batch, and the
// session's derived numbers must not move.
func TestReenrich_mergesNewRawKeysAndLeavesTheSessionsNumbersAlone(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const sessionID = "raw-merge-s1"
	ts := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	// A rate for the model, so the session (which has no OTEL) gets an imputed cost.
	for i := 0; i < 20; i++ {
		if _, err := s.pool.Exec(ctx, `INSERT INTO otel_events (ts,event_name,session_id,model,cost_usd,input_tokens,agent,billing_provider)
			VALUES ($1,'api_request',$2,'claude-test-model',1,1000,'claude','anthropic')`, ts, fmt.Sprintf("raw-merge-rate-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	in, out := 1000, 100
	row := func(offset time.Duration, recordType, uuid, raw string) *SessionRecord {
		return &SessionRecord{
			Ts: ts.Add(offset), SessionID: sessionID, RecordType: recordType,
			ProfileEmail: "p@example.com", UserID: "u1", Agent: "claude", BillingProvider: "anthropic",
			UUID: uuid, Raw: json.RawMessage(raw),
		}
	}
	prompt := row(0, "user", "u1", `{"type":"user","uuid":"u1","message":{"role":"user","content":"[redacted by cctrace client]"}}`)
	prompt.PromptSource = "typed"
	answer := row(time.Second, "assistant", "a1", `{"type":"assistant","uuid":"a1","message":{"role":"assistant","model":"claude-test-model","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":1000,"output_tokens":100}}}`)
	answer.Model, answer.InputTokens, answer.OutputTokens = "claude-test-model", &in, &out
	next := row(2*time.Second, "user", "u2", `{"type":"user","uuid":"u2","message":{"role":"user","content":"next"}}`)
	next.PromptSource = "typed"
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{prompt, answer, next}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshModelRateBuckets(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshClaudeImputedCost(ctx); err != nil {
		t.Fatal(err)
	}
	rollupsBefore, segmentsBefore, imputedBefore := sessionNumbers(t, s, sessionID)
	if rollupsBefore == "[]" || segmentsBefore == "[]" || imputedBefore == "[]" {
		t.Fatalf("fixture derives nothing to compare: rollups=%s segments=%s imputed=%s", rollupsBefore, segmentsBefore, imputedBefore)
	}

	freshPrompt := *prompt
	freshPrompt.Raw = json.RawMessage(`{"type":"user","uuid":"u1","message":{"role":"user","content":"the prompt in clear"},"promptId":"p-1","permissionMode":"plan","origin":{"kind":"human"},"cctraceCollect":1,"slug":"not collected"}`)
	freshAnswer := *answer
	freshAnswer.Raw = json.RawMessage(`{"type":"assistant","uuid":"a1","message":{"role":"assistant","model":"other-model","content":[]},"requestId":"req_1","error":"bad \u0000 byte","cctraceCollect":1}`)
	freshNext := *next
	freshNext.Raw = nil // a client that sends no raw changes nothing
	reenrich := func() {
		t.Helper()
		batch := []*SessionRecord{&freshPrompt, &freshAnswer, &freshNext}
		updated, err := s.ReenrichSessionRecords(ctx, batch)
		if err != nil {
			t.Fatalf("ReenrichSessionRecords: %v", err)
		}
		if updated != len(batch) {
			t.Fatalf("updated %d rows, want %d", updated, len(batch))
		}
	}
	reenrich()

	got := storedRaw(t, s, sessionID, "u1")
	if msg, _ := got["message"].(map[string]interface{}); msg["content"] != "[redacted by cctrace client]" {
		t.Errorf("stored message was overwritten: %v", got["message"])
	}
	if got["promptId"] != "p-1" || got["permissionMode"] != "plan" || got["cctraceCollect"] != float64(1) {
		t.Errorf("new keys not merged into u1: %v", got)
	}
	if _, ok := got["slug"]; ok {
		t.Errorf("a key outside sessionlog.CollectedKeys was merged: %v", got)
	}
	if origin, _ := got["origin"].(map[string]interface{}); origin["kind"] != "human" {
		t.Errorf("origin not merged: %v", got["origin"])
	}
	got = storedRaw(t, s, sessionID, "a1")
	if msg, _ := got["message"].(map[string]interface{}); msg["model"] != "claude-test-model" {
		t.Errorf("stored message was overwritten: %v", got["message"])
	}
	if got["requestId"] != "req_1" || got["error"] != "bad � byte" || got["cctraceCollect"] != float64(1) {
		t.Errorf("new keys not merged into a1 (NUL sanitised): requestId=%v error=%q cctraceCollect=%v", got["requestId"], got["error"], got["cctraceCollect"])
	}
	if got := storedRaw(t, s, sessionID, "u2"); len(got) != 3 || got["uuid"] != "u2" {
		t.Errorf("record sent without raw changed the stored raw: %v", got)
	}

	if err := s.RefreshClaudeImputedCost(ctx); err != nil {
		t.Fatal(err)
	}
	rollupsAfter, segmentsAfter, imputedAfter := sessionNumbers(t, s, sessionID)
	if rollupsAfter != rollupsBefore {
		t.Errorf("rollups moved\n before %s\n after  %s", rollupsBefore, rollupsAfter)
	}
	if segmentsAfter != segmentsBefore {
		t.Errorf("task segments moved\n before %s\n after  %s", segmentsBefore, segmentsAfter)
	}
	if imputedAfter != imputedBefore {
		t.Errorf("imputed cost moved\n before %s\n after  %s", imputedBefore, imputedAfter)
	}

	// Once merged there is nothing left to add: a second pass leaves raw as it is.
	// (Whether it also leaves the stored value unwritten is
	// TestReenrich_rawWithNothingToAddIsNotRewritten.)
	before := storedRaw(t, s, sessionID, "u1")
	reenrich()
	if after := storedRaw(t, s, sessionID, "u1"); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("second pass changed raw\n before %v\n after  %v", before, after)
	}
}

// reenrichRows runs one re-enrichment batch and returns how many rows it updated.
func reenrichRows(t *testing.T, s *PgStore, records ...*SessionRecord) int {
	t.Helper()
	updated, err := s.ReenrichSessionRecords(context.Background(), records)
	if err != nil {
		t.Fatalf("ReenrichSessionRecords: %v", err)
	}
	return updated
}

// rawMergeRow builds a Claude record in one session, a second apart per offset.
func rawMergeRow(sessionID string, offset int, recordType, uuid, raw string) *SessionRecord {
	return &SessionRecord{
		Ts:        time.Date(2026, 10, 1, 9, 0, offset, 0, time.UTC),
		SessionID: sessionID, RecordType: recordType, ProfileEmail: "p@example.com",
		Agent: "claude", UUID: uuid, Raw: json.RawMessage(raw),
	}
}

func insertRows(t *testing.T, s *PgStore, records ...*SessionRecord) {
	t.Helper()
	if err := s.InsertSessionRecords(context.Background(), records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
}

func member(t *testing.T, raw map[string]interface{}, key string) map[string]interface{} {
	t.Helper()
	m, ok := raw[key].(map[string]interface{})
	if !ok {
		t.Fatalf("raw has no %s object: %v", key, raw)
	}
	return m
}

// A default re-enrichment sends the signal fields only, so it creates
// toolUseResult and attachment with their signal members. A later run asked for
// content (--include-content) sends the same objects with the content members
// too, and those have to land under the objects the first run created -- a
// top-level merge alone would see the key taken and add nothing.
func TestReenrich_contentRunAddsUnderObjectsADefaultRunCreated(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	const sid = "raw-merge-two-runs"
	// As a client before CollectVersion stored them: none of the fields.
	insertRows(t, s,
		rawMergeRow(sid, 0, "user", "answer", `{"type":"user","uuid":"answer","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"answered"}]}}`),
		rawMergeRow(sid, 1, "attachment", "queued", `{"type":"attachment","uuid":"queued","message":null}`),
		rawMergeRow(sid, 2, "attachment", "plan", `{"type":"attachment","uuid":"plan","message":null}`),
	)

	reenrichRows(t, s,
		rawMergeRow(sid, 0, "user", "answer", `{"type":"user","uuid":"answer","toolUseResult":{"status":"completed"},"cctraceCollect":1}`),
		rawMergeRow(sid, 1, "attachment", "queued", `{"type":"attachment","uuid":"queued","attachment":{"type":"queued_command","commandMode":"prompt"},"cctraceCollect":1}`),
		rawMergeRow(sid, 2, "attachment", "plan", `{"type":"attachment","uuid":"plan","attachment":{"type":"plan_file_reference"},"cctraceCollect":1}`),
	)
	if got := member(t, storedRaw(t, s, sid, "answer"), "toolUseResult"); fmt.Sprint(got) != fmt.Sprint(map[string]interface{}{"status": "completed"}) {
		t.Fatalf("default run: toolUseResult = %v", got)
	}
	if got := member(t, storedRaw(t, s, sid, "queued"), "attachment"); got["prompt"] != nil || got["commandMode"] != "prompt" {
		t.Fatalf("default run: attachment = %v", got)
	}

	reenrichRows(t, s,
		rawMergeRow(sid, 0, "user", "answer", `{"type":"user","uuid":"answer","toolUseResult":{"status":"completed","answers":{"Q":"A"}},"cctraceCollect":1}`),
		rawMergeRow(sid, 1, "attachment", "queued", `{"type":"attachment","uuid":"queued","attachment":{"type":"queued_command","commandMode":"prompt","prompt":"typed while busy"},"cctraceCollect":1}`),
		rawMergeRow(sid, 2, "attachment", "plan", `{"type":"attachment","uuid":"plan","attachment":{"type":"plan_file_reference","planFilePath":"/p.md","planContent":"# Plan"},"cctraceCollect":1}`),
	)
	if got := member(t, storedRaw(t, s, sid, "answer"), "toolUseResult"); got["status"] != "completed" || fmt.Sprint(got["answers"]) != "map[Q:A]" {
		t.Errorf("content run: toolUseResult = %v, want the answers added beside status", got)
	}
	if got := member(t, storedRaw(t, s, sid, "queued"), "attachment"); got["prompt"] != "typed while busy" || got["commandMode"] != "prompt" {
		t.Errorf("content run: attachment = %v, want the prompt added", got)
	}
	if got := member(t, storedRaw(t, s, sid, "plan"), "attachment"); got["planContent"] != "# Plan" || got["planFilePath"] != "/p.md" || got["type"] != "plan_file_reference" {
		t.Errorf("content run: attachment = %v, want the plan added", got)
	}
}

// What a row already holds stays as stored at both levels the merge reaches: a
// redaction marker at the top of the line, or inside toolUseResult or an
// attachment, is never replaced by the text a later run sends in clear. Members
// the row did not have are still added beside them.
func TestReenrich_rawMergeNeverReplacesAStoredValue(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	const sid = "raw-merge-markers"
	const marker = "[redacted by cctrace client]"
	// As a client collecting these fields stored them with redaction on.
	insertRows(t, s,
		rawMergeRow(sid, 0, "system", "recap", `{"type":"system","uuid":"recap","subtype":"away_summary","content":"`+marker+`","cctraceCollect":1}`),
		rawMergeRow(sid, 1, "attachment", "queued", `{"type":"attachment","uuid":"queued","attachment":{"type":"queued_command","prompt":"`+marker+`"},"cctraceCollect":1}`),
		rawMergeRow(sid, 2, "user", "answer", `{"type":"user","uuid":"answer","message":{"role":"user","content":"`+marker+`"},"toolUseResult":{"answers":"`+marker+`"},"cctraceCollect":1}`),
	)
	// Redaction since turned off, and content asked for.
	reenrichRows(t, s,
		rawMergeRow(sid, 0, "system", "recap", `{"type":"system","uuid":"recap","subtype":"away_summary","content":"recap in clear","cctraceCollect":1}`),
		rawMergeRow(sid, 1, "attachment", "queued", `{"type":"attachment","uuid":"queued","attachment":{"type":"queued_command","prompt":"prompt in clear","commandMode":"prompt"},"cctraceCollect":1}`),
		rawMergeRow(sid, 2, "user", "answer", `{"type":"user","uuid":"answer","message":{"role":"user","content":"prompt in clear"},"toolUseResult":{"answers":{"Q":"answer in clear"},"status":"completed"},"cctraceCollect":1}`),
	)
	for _, uuid := range []string{"recap", "queued", "answer"} {
		if raw := fmt.Sprint(storedRaw(t, s, sid, uuid)); strings.Contains(raw, "in clear") || !strings.Contains(raw, marker) {
			t.Errorf("%s: a stored marker was replaced: %s", uuid, raw)
		}
	}
	if got := member(t, storedRaw(t, s, sid, "queued"), "attachment"); got["commandMode"] != "prompt" {
		t.Errorf("attachment = %v, want commandMode added beside the marker", got)
	}
	if got := member(t, storedRaw(t, s, sid, "answer"), "toolUseResult"); got["status"] != "completed" {
		t.Errorf("toolUseResult = %v, want status added beside the marker", got)
	}
}

// The merge takes only the keys a collecting client adds, and only into Claude
// rows. A row redacted before these fields existed, re-enriched after the policy
// was relaxed, gets its signals and not its message back in clear; a row stored
// without a message does not gain one, nor a top-level content, which only a
// system line's recap is collected as; a Codex row gets nothing, whatever names
// its fresh raw happens to share with a Claude line. A row whose agent was never
// written reads as Claude, as everywhere else.
func TestReenrich_rawMergeTakesOnlyCollectedKeysAndOnlyOnClaudeRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const sid = "raw-merge-scope"
	const marker = "[redacted by cctrace client]"
	codexRow := func(offset int, uuid string) *SessionRecord {
		r := rawMergeRow(sid, offset, "user", uuid, `{"type":"response_item","payload":{"type":"message","role":"user"}}`)
		r.Agent, r.BillingProvider = "codex", "openai"
		return r
	}
	insertRows(t, s,
		rawMergeRow(sid, 0, "user", "redacted", `{"type":"user","uuid":"redacted","message":{"role":"user","content":"`+marker+`"}}`),
		rawMergeRow(sid, 1, "user", "bare", `{"type":"user","uuid":"bare"}`),
		codexRow(2, "codex"),
		rawMergeRow(sid, 3, "user", "blank-agent", `{"type":"user","uuid":"blank-agent"}`),
		codexRow(4, "codex-stored"),
	)
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET agent = '' WHERE session_id = $1 AND uuid = 'blank-agent'`, sid); err != nil {
		t.Fatal(err)
	}

	// One Codex record the batch says is Codex, and one it does not: the stored
	// row's agent decides on its own.
	freshCodex := rawMergeRow(sid, 2, "user", "codex", `{"type":"response_item","payload":{"type":"message","role":"user"},"promptId":"p-codex","cctraceCollect":1}`)
	freshCodex.Agent = "codex"
	if updated := reenrichRows(t, s,
		rawMergeRow(sid, 0, "user", "redacted", `{"type":"user","uuid":"redacted","message":{"role":"user","content":"in clear"},"promptId":"p-1","cctraceCollect":1}`),
		rawMergeRow(sid, 1, "user", "bare", `{"type":"user","uuid":"bare","message":{"role":"user","content":"in clear"},"content":"in clear","promptId":"p-2","isMeta":true,"cctraceCollect":1}`),
		freshCodex,
		rawMergeRow(sid, 3, "user", "blank-agent", `{"type":"user","uuid":"blank-agent","promptId":"p-4","cctraceCollect":1}`),
		rawMergeRow(sid, 4, "user", "codex-stored", `{"promptId":"p-5","cctraceCollect":1}`),
	); updated != 5 {
		t.Fatalf("updated %d rows, want 5", updated)
	}

	got := storedRaw(t, s, sid, "redacted")
	if msg := member(t, got, "message"); msg["content"] != marker || got["promptId"] != "p-1" {
		t.Errorf("redacted row: %v, want the marker kept and promptId added", got)
	}
	got = storedRaw(t, s, sid, "bare")
	if got["promptId"] != "p-2" {
		t.Errorf("bare row: %v, want promptId added", got)
	}
	for _, key := range []string{"message", "isMeta", "content"} {
		if _, ok := got[key]; ok {
			t.Errorf("bare row: %s merged into %v", key, got)
		}
	}
	for _, uuid := range []string{"codex", "codex-stored"} {
		if got := storedRaw(t, s, sid, uuid); len(got) != 2 || got["promptId"] != nil {
			t.Errorf("%s row: %v, want it as stored", uuid, got)
		}
	}
	if got := storedRaw(t, s, sid, "blank-agent"); got["promptId"] != "p-4" {
		t.Errorf("row with a blank agent: %v, want it merged as a Claude row", got)
	}
}

// toastValueIDs lists the out-of-line values stored for the hypertable chunk
// holding one row. A value written again gets a new id even when it is equal,
// so an unchanged list means the stored value was kept, not rewritten.
func toastValueIDs(t *testing.T, s *PgStore, sessionID, uuid string) string {
	t.Helper()
	ctx := context.Background()
	var toast string
	if err := s.pool.QueryRow(ctx, `SELECT c.reltoastrelid::regclass::text
		FROM session_records r JOIN pg_class c ON c.oid = r.tableoid
		WHERE r.session_id = $1 AND r.uuid = $2`, sessionID, uuid).Scan(&toast); err != nil {
		t.Fatalf("toast relation of %s/%s: %v", sessionID, uuid, err)
	}
	var ids string
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(array_agg(DISTINCT chunk_id ORDER BY chunk_id), '{}')::text FROM `+toast).Scan(&ids); err != nil {
		t.Fatalf("read %s: %v", toast, err)
	}
	return ids
}

// A pass with nothing to add assigns raw to itself, which keeps the stored value
// as it is; assigning an equal value it built would write it out again, for every
// row on every pass after the first. Content equality cannot tell the two apart,
// so this watches the stored value itself: raw large enough to live out of line,
// and the ids of the out-of-line values before and after.
func TestReenrich_rawWithNothingToAddIsNotRewritten(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	const sid = "raw-merge-toast"
	noise := make([]byte, 16<<10)
	if _, err := rand.Read(noise); err != nil {
		t.Fatal(err)
	}
	big := hex.EncodeToString(noise) // 32 KiB that compression cannot fold back inline
	insertRows(t, s, rawMergeRow(sid, 0, "user", "big", `{"type":"user","uuid":"big","message":{"role":"user","content":"`+big+`"}}`))
	fresh := rawMergeRow(sid, 0, "user", "big", `{"type":"user","uuid":"big","message":{"role":"user","content":"`+big+`"},"promptId":"p-1","cctraceCollect":1}`)

	if reenrichRows(t, s, fresh) != 1 {
		t.Fatal("first pass matched no row")
	}
	if got := storedRaw(t, s, sid, "big"); got["promptId"] != "p-1" {
		t.Fatalf("first pass did not merge: promptId = %v", got["promptId"])
	}
	before := toastValueIDs(t, s, sid, "big")
	if before == "{}" {
		t.Fatal("fixture raw is stored inline, so this test cannot see a rewrite")
	}
	if reenrichRows(t, s, fresh) != 1 {
		t.Fatal("second pass matched no row")
	}
	if after := toastValueIDs(t, s, sid, "big"); after != before {
		t.Errorf("a pass with nothing to add rewrote raw: out-of-line values %s -> %s", before, after)
	}
}

// A partly re-collected session can hold a legacy row with an empty uuid and its
// enriched twin in the same slot. The update goes to the twin alone -- filling uuid on the
// legacy row would collide with it -- so the raw merge lands on the twin and the
// legacy row keeps its raw. A legacy row with no twin is the row the line
// belongs to: it gets its uuid and the merge both.
func TestReenrich_rawMergeWithLegacyUUIDRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	const twinSession, loneSession = "raw-merge-twin", "raw-merge-lone"
	insertRows(t, s,
		rawMergeRow(twinSession, 0, "user", "", `{"type":"user","message":{"role":"user","content":"legacy"}}`),
		rawMergeRow(twinSession, 0, "user", "u-twin", `{"type":"user","uuid":"u-twin","message":{"role":"user","content":"enriched"}}`),
		rawMergeRow(loneSession, 0, "user", "", `{"type":"user","message":{"role":"user","content":"legacy"}}`),
	)
	if updated := reenrichRows(t, s,
		rawMergeRow(twinSession, 0, "user", "u-twin", `{"type":"user","uuid":"u-twin","promptId":"p-twin","cctraceCollect":1}`),
		rawMergeRow(loneSession, 0, "user", "u-lone", `{"type":"user","uuid":"u-lone","promptId":"p-lone","cctraceCollect":1}`),
	); updated != 2 {
		t.Fatalf("updated %d rows, want the twin and the lone legacy row", updated)
	}
	if got := storedRaw(t, s, twinSession, "u-twin"); got["promptId"] != "p-twin" {
		t.Errorf("twin: %v, want promptId merged", got)
	}
	if got := storedRaw(t, s, twinSession, ""); len(got) != 2 || got["promptId"] != nil {
		t.Errorf("legacy row beside its twin: %v, want it as stored", got)
	}
	if got := storedRaw(t, s, loneSession, "u-lone"); got["promptId"] != "p-lone" {
		t.Errorf("lone legacy row: %v, want its uuid filled and promptId merged", got)
	}
}

// A lone surrogate escape is valid JSON and what Go reads as U+FFFD, but jsonb
// refuses it -- and one refused value failed the whole re-enrichment batch. The
// merge object is rewritten by Go first. A fresh raw that is not an object at all
// merges nothing, and the column updates beside it still apply.
func TestReenrich_rawMergeSurvivesWhatJSONBRefuses(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const sid = "raw-merge-normalise"
	insertRows(t, s,
		rawMergeRow(sid, 0, "assistant", "cut", `{"type":"assistant","uuid":"cut","message":{"role":"assistant"}}`),
		rawMergeRow(sid, 1, "assistant", "odd", `{"type":"assistant","uuid":"odd","message":{"role":"assistant"}}`),
	)
	cut := rawMergeRow(sid, 0, "assistant", "cut", `{"type":"assistant","uuid":"cut","message":{"role":"assistant","content":"cut \ud83d"},"error":"cut \ud83d","requestId":"req_1","cctraceCollect":1}`)
	cut.SourceFile = "cut.jsonl"
	odd := rawMergeRow(sid, 1, "assistant", "odd", `"not an object"`)
	odd.SourceFile = "odd.jsonl"
	if updated := reenrichRows(t, s, cut, odd); updated != 2 {
		t.Fatalf("updated %d rows, want 2", updated)
	}
	if got := storedRaw(t, s, sid, "cut"); got["error"] != "cut �" || got["requestId"] != "req_1" {
		t.Errorf("cut: error=%q requestId=%v, want the surrogate as U+FFFD and the merge applied", got["error"], got["requestId"])
	}
	if got := storedRaw(t, s, sid, "odd"); len(got) != 3 {
		t.Errorf("odd: %v, want it as stored", got)
	}
	for uuid, want := range map[string]string{"cut": "cut.jsonl", "odd": "odd.jsonl"} {
		var got string
		if err := s.pool.QueryRow(ctx, `SELECT source_file FROM session_records WHERE session_id = $1 AND uuid = $2`, sid, uuid).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: source_file = %q, want %q", uuid, got, want)
		}
	}
}

// jsonb || joins two objects; on anything else it builds an array, which would
// turn a stored line into something no reader recognises as one. The merge only
// ever joins object into object.
func TestReenrich_rawMergeOnlyJoinsObjects(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	rec := func(uuid, raw string) *SessionRecord {
		return &SessionRecord{Ts: ts, SessionID: "raw-merge-shape", RecordType: "user", ProfileEmail: "p@example.com",
			Agent: "claude", UUID: uuid, Raw: json.RawMessage(raw)}
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		rec("object", `{"type":"user","uuid":"object"}`),
		rec("array", `["stored","array"]`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReenrichSessionRecords(ctx, []*SessionRecord{
		rec("object", `["fresh","array"]`),
		rec("array", `{"type":"user","uuid":"array","promptId":"p"}`),
	}); err != nil {
		t.Fatalf("ReenrichSessionRecords: %v", err)
	}
	for uuid, want := range map[string]string{
		"object": `{"type": "user", "uuid": "object"}`,
		"array":  `["stored", "array"]`,
	} {
		var got string
		if err := s.pool.QueryRow(ctx, `SELECT raw::text FROM session_records WHERE session_id = 'raw-merge-shape' AND uuid = $1`, uuid).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: raw = %s, want %s", uuid, got, want)
		}
	}
}

// A top-level content is collected only as Claude Code's recap: a system line
// whose subtype is away_summary. On any other line the name holds conversation,
// so the merge puts content on a system row only when the object it merges says
// it is a recap -- never because a system row happens to lack one.
func TestReenrich_rawMergeTakesContentOnlyAsARecap(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	const sid = "raw-merge-recap-gate"
	insertRows(t, s,
		rawMergeRow(sid, 0, "system", "recap", `{"type":"system","uuid":"recap"}`),
		rawMergeRow(sid, 1, "system", "duration", `{"type":"system","uuid":"duration"}`),
		rawMergeRow(sid, 2, "system", "unnamed", `{"type":"system","uuid":"unnamed"}`),
		rawMergeRow(sid, 3, "attachment", "attached", `{"type":"attachment","uuid":"attached"}`),
	)
	if updated := reenrichRows(t, s,
		rawMergeRow(sid, 0, "system", "recap", `{"subtype":"away_summary","content":"the recap","cctraceCollect":1}`),
		rawMergeRow(sid, 1, "system", "duration", `{"subtype":"turn_duration","content":"not a recap","durationMs":5,"cctraceCollect":1}`),
		rawMergeRow(sid, 2, "system", "unnamed", `{"content":"no subtype","cctraceCollect":1}`),
		rawMergeRow(sid, 3, "attachment", "attached", `{"subtype":"away_summary","content":"on an attachment","cctraceCollect":1}`),
	); updated != 4 {
		t.Fatalf("updated %d rows, want 4", updated)
	}
	if got := storedRaw(t, s, sid, "recap"); got["content"] != "the recap" {
		t.Errorf("recap: %v, want its content merged", got)
	}
	for _, uuid := range []string{"duration", "unnamed", "attached"} {
		got := storedRaw(t, s, sid, uuid)
		if _, ok := got["content"]; ok {
			t.Errorf("%s: content merged into %v", uuid, got)
		}
		if got["cctraceCollect"] != float64(1) {
			t.Errorf("%s: %v, want the rest of the object merged", uuid, got)
		}
	}
}

// What reaches jsonb is the collected part of the fresh raw, each value written
// out by Go: anything else is dropped unread, a record from another agent merges
// nothing, and a value jsonb would refuse as the client wrote it -- a lone
// surrogate, the NUL escape -- arrives in a form it takes, numbers keeping every
// digit.
func TestReenrichMergeObjectIsTheCollectedKeysOfAClaudeRecord(t *testing.T) {
	const fresh = `{"type":"user","uuid":"u","message":{"role":"user","content":"cut \ud83d"},"slug":"x",` +
		`"promptId":"p \u0000 1","durationMs":123456789012345678901234567890,"error":"cut \ud83d","attachment":{"type":"queued_command","prompt":"q"}}`
	for _, tc := range []struct {
		name  string
		agent string
		raw   string
		want  string
	}{
		{"claude", "claude", fresh,
			`{"attachment":{"prompt":"q","type":"queued_command"},"durationMs":123456789012345678901234567890,"error":"cut ` + "\uFFFD" + `","promptId":"p \uFFFD 1"}`},
		{"agent never written", "", fresh,
			`{"attachment":{"prompt":"q","type":"queued_command"},"durationMs":123456789012345678901234567890,"error":"cut ` + "\uFFFD" + `","promptId":"p \uFFFD 1"}`},
		{"codex", "codex", fresh, `{}`},
		{"not an object", "claude", `["promptId"]`, `{}`},
		{"no raw", "claude", ``, `{}`},
		{"nothing collected", "claude", `{"type":"user","message":{"content":"x"}}`, `{}`},
	} {
		if got := string(reenrichMergeObject(tc.agent, json.RawMessage(tc.raw))); got != tc.want {
			t.Errorf("%s:\n got  %s\n want %s", tc.name, got, tc.want)
		}
	}
}

func TestReenrichV3ContentMerge(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	const sid = "inspection-v3-recovery"
	insertRows(t, s,
		rawMergeRow(sid, 0, "system", "system", `{"type":"system","cctraceCollect":2}`),
		rawMergeRow(sid, 1, "attachment", "rules", `{"type":"attachment","attachment":{"type":"instructions"},"cctraceCollect":2}`),
	)
	_, err := s.ReenrichSessionRecords(context.Background(), []*SessionRecord{
		rawMergeRow(sid, 0, "system", "system", `{"content":"recorded system message","cctraceCollect":3}`),
		rawMergeRow(sid, 1, "attachment", "rules", `{"attachment":{"type":"instructions","files":[{"path":"AGENTS.md","content":"actual injected"}]},"cctraceCollect":3}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var content, rule string
	if err = s.pool.QueryRow(context.Background(), `SELECT raw->>'content' FROM session_records WHERE session_id=$1 AND uuid='system'`, sid).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if err = s.pool.QueryRow(context.Background(), `SELECT raw->'attachment'->'files'->0->>'content' FROM session_records WHERE session_id=$1 AND uuid='rules'`, sid).Scan(&rule); err != nil {
		t.Fatal(err)
	}
	if content != "recorded system message" || rule != "actual injected" {
		t.Fatalf("restored system=%q rule=%q", content, rule)
	}
}
