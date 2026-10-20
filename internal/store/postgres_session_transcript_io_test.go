package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSessionAnalysisTranscriptIOIsOwnerScopedAndTextOnly(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "analysis-transcript-owner"
	base := time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)
	inputText := "SYNTHETIC-TRANSCRIPT-INPUT\r\n끝"
	outputText := "SYNTHETIC-TRANSCRIPT-OUTPUT"
	encode := func(value any) json.RawMessage {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	records := []*SessionRecord{
		{Ts: base, SessionID: id, RecordType: "user", UserID: "owner", ProfileEmail: "owner@example.com", Agent: "claude", UUID: "tr-user",
			PromptSource: "typed", Raw: encode(map[string]interface{}{"type": "user", "message": map[string]interface{}{"role": "user", "content": inputText}})},
		{Ts: base.Add(time.Second), SessionID: id, RecordType: "assistant", UserID: "owner", ProfileEmail: "owner@example.com", Agent: "claude", UUID: "tr-assistant",
			Raw: encode(map[string]interface{}{"type": "assistant", "message": map[string]interface{}{"role": "assistant", "content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "SYNTHETIC-PRIVATE-REASONING"},
				map[string]interface{}{"type": "text", "text": outputText},
				map[string]interface{}{"type": "tool_use", "name": "shell"},
			}}})},
		{Ts: base.Add(2 * time.Second), SessionID: id, RecordType: "user", UserID: "owner", ProfileEmail: "owner@example.com", Agent: "claude", UUID: "tr-tool-result",
			Raw: encode(map[string]interface{}{"type": "user", "message": map[string]interface{}{"role": "user", "content": []interface{}{map[string]interface{}{"type": "tool_result", "content": "SYNTHETIC-TOOL-OUTPUT"}}}})},
		{Ts: base.Add(3 * time.Second), SessionID: id, RecordType: "user", UserID: "owner", ProfileEmail: "owner@example.com", Agent: "claude", UUID: "tr-sdk", PromptSource: "sdk",
			Raw: encode(map[string]interface{}{"type": "user", "message": map[string]interface{}{"role": "user", "content": "SYNTHETIC-AUTOMATED-PROMPT"}})},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := publishSessionP4ForTest(ctx, s, id, load, 1)
	if err != nil || outcome != "written" {
		t.Fatalf("publish=%s, %v", outcome, err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_live_states
		(session_id,user_id,profile_email,pid,proc_start,status,waiting_kind,heartbeat_at,identity_provenance,
		 io_epoch,io_as_of,io_source,io_mode,io_partial,input_bytes,input_lines,output_bytes,output_lines)
		VALUES($1,'owner','owner@example.com',91,'process-start','busy','',now(),'personal_token',
		 'process-epoch',now(),'process_stream','pty',false,999,7,888,8)`, id)
	if err != nil {
		t.Fatal(err)
	}
	access := SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner", OwnerProfileEmail: "owner@example.com"}
	owner, err := s.ReadSessionAnalysis(ctx, id, access)
	if err != nil {
		t.Fatal(err)
	}
	live, ok := owner["live"].(map[string]interface{})
	if !ok {
		t.Fatalf("owner live response = %v", owner["live"])
	}
	transcript, ok := live["transcript_io"].(map[string]interface{})
	if !ok {
		t.Fatalf("owner transcript_io = %v", live["transcript_io"])
	}
	instances, ok := live["instances"].([]interface{})
	if !ok || len(instances) != 1 {
		t.Fatalf("owner process instances = %v", live["instances"])
	}
	process := instances[0].(map[string]interface{})["process"].(map[string]interface{})
	if process["io_source"] != "process_stream" || process["io_scope"] != "instance" || process["io_mode"] != "pty" || process["pid"] != int64(91) {
		t.Fatalf("process stream was conflated with transcript: %v", process)
	}
	if transcript["io_source"] != "transcript" || transcript["io_scope"] != "session" || transcript["io_freshness"] != "fresh" || transcript["io_partial"] != false {
		t.Fatalf("transcript scope/source/freshness = %v", transcript)
	}
	assertCounter := func(key string, want int64) {
		t.Helper()
		got, ok := transcript[key].(*int64)
		if !ok || got == nil || *got != want {
			t.Errorf("transcript %s=%v, want %d", key, transcript[key], want)
		}
	}
	assertCounter("input_bytes", int64(len(inputText)))
	assertCounter("input_lines", 2)
	assertCounter("output_bytes", int64(len(outputText)))
	assertCounter("output_lines", 1)
	if transcript["pid"] != nil || transcript["io_mode"] != nil {
		t.Errorf("session transcript was attributed to a process: %v", transcript)
	}
	encoded, err := json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{inputText, outputText, "SYNTHETIC-PRIVATE-REASONING", "SYNTHETIC-TOOL-OUTPUT", "SYNTHETIC-AUTOMATED-PROMPT"} {
		if strings.Contains(string(encoded), raw) {
			t.Errorf("transcript response contains source text %q", raw)
		}
	}
	admin, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{OwnerUserID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := admin["live"]; ok {
		t.Fatalf("nonowner received transcript or process live details: %v", admin["live"])
	}
}

func TestSessionAnalysisTranscriptIOReusesFactsWhileNewActivityIsPending(t *testing.T) {
	// Shared store truncation prevents running this integration test in parallel.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "analysis-transcript-lazy"
	base := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	record := func(uuid, role, text string, at time.Time) *SessionRecord {
		t.Helper()
		raw, err := json.Marshal(map[string]interface{}{"type": role, "message": map[string]interface{}{"role": role, "content": text}})
		if err != nil {
			t.Fatal(err)
		}
		return &SessionRecord{Ts: at, SessionID: id, RecordType: role, UserID: "owner", ProfileEmail: "owner@example.com",
			Agent: "claude", UUID: uuid, PromptSource: "typed", Raw: raw}
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		record("input", "user", "input", base),
		record("output", "assistant", "old output", base.Add(time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if outcome, err := publishSessionP4ForTest(ctx, s, id, load, 1); err != nil || outcome != "written" {
		t.Fatalf("publish=%s, err=%v", outcome, err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{record("new-output", "assistant", "new output", base.Add(2*time.Second))}); err != nil {
		t.Fatal(err)
	}
	access := SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner", OwnerProfileEmail: "owner@example.com"}
	pending, err := s.ReadSessionAnalysis(ctx, id, access)
	if err != nil {
		t.Fatal(err)
	}
	transcript := pending["live"].(map[string]interface{})["transcript_io"].(map[string]interface{})
	bytes, ok := transcript["output_bytes"].(*int64)
	if !ok || bytes == nil || *bytes != int64(len("old output")) {
		t.Fatalf("detail reread new transcript text instead of the published measurement: %v", transcript)
	}
	if transcript["io_freshness"] != "stale" || pending["analysis_pending"] != true {
		t.Fatalf("cached transcript did not expose pending activity: freshness=%v pending=%v", transcript["io_freshness"], pending["analysis_pending"])
	}
	var generation int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id=$1`, id).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if run, err := s.processSessionFact(ctx, id, generation); err != nil || run.Outcome != "written" {
		t.Fatalf("ordinary dirty computation=%+v, err=%v", run, err)
	}
	ready, err := s.ReadSessionAnalysis(ctx, id, access)
	if err != nil {
		t.Fatal(err)
	}
	transcript = ready["live"].(map[string]interface{})["transcript_io"].(map[string]interface{})
	bytes, ok = transcript["output_bytes"].(*int64)
	if !ok || bytes == nil || *bytes != int64(len("old output")+len("new output")) || transcript["io_freshness"] != "fresh" || ready["analysis_pending"] != false {
		t.Fatalf("worker did not publish the refreshed measurement: transcript=%v pending=%v", transcript, ready["analysis_pending"])
	}
	var cache []byte
	if err := s.pool.QueryRow(ctx, `SELECT detail->'transcript_io' FROM session_analysis_facts WHERE session_id=$1`, id).Scan(&cache); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"old output", "new output"} {
		if strings.Contains(string(cache), text) {
			t.Fatalf("cached measurement retained source text %q", text)
		}
	}
}

func TestSessionAnalysisTranscriptIORebuildsLegacyCacheWithoutReadDemand(t *testing.T) {
	// Shared store truncation prevents running this integration test in parallel.
	s := lifecycleStore(t)
	ctx := context.Background()
	const id = "analysis-transcript-legacy"
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot,identity_provenance)
		VALUES($1,'owner','owner@example.com','claude',now(),'{}'::jsonb,'personal_token')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	// Publish the pre-cache input so both its detail and hash match a legacy fact.
	load.Input.TranscriptIO = nil
	if outcome, err := publishSessionP4ForTest(ctx, s, id, load, 1); err != nil || outcome != "written" {
		t.Fatalf("publish=%s, err=%v", outcome, err)
	}
	access := SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner", OwnerProfileEmail: "owner@example.com"}
	result, err := s.ReadSessionAnalysis(ctx, id, access)
	if err != nil || result["analysis_pending"] != false {
		t.Fatalf("legacy GET created work: %v, %v", result, err)
	}
	if n := countWhere(t, s, "session_fact_dirty", "session_id=$1", id); n != 0 {
		t.Fatal("legacy GET enqueued a session")
	}
	// The automatic versioned build, rather than a GET, recovers old caches.
	if err := s.enqueueSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	var generation int64
	for i := range 2 {
		result, err := s.ReadSessionAnalysis(ctx, id, access)
		if err != nil {
			t.Fatal(err)
		}
		if result["analysis_pending"] != true {
			t.Fatalf("legacy detail did not read existing build work: pending=%v", result["analysis_pending"])
		}
		transcript := result["live"].(map[string]interface{})["transcript_io"].(map[string]interface{})
		if transcript["io_freshness"] != "unknown" {
			t.Fatalf("legacy cache reported a current transcript measurement: %v", transcript)
		}
		var current int64
		if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id=$1`, id).Scan(&current); err != nil {
			t.Fatal(err)
		}
		if i > 0 && current != generation {
			t.Fatalf("repeat legacy read changed generation: %d -> %d", generation, current)
		}
		generation = current
	}
	if run, err := s.processSessionFact(ctx, id, generation); err != nil || run.Outcome != "written" {
		t.Fatalf("legacy measurement computation=%+v, err=%v", run, err)
	}
	result, err = s.ReadSessionAnalysis(ctx, id, access)
	if err != nil || result["analysis_pending"] != false {
		t.Fatalf("refreshed cache queued again: pending=%v err=%v", result["analysis_pending"], err)
	}
}
