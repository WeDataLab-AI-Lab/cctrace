package sessionlog

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNativeRequestProtocolPreservesUUIDLessRows(t *testing.T) {
	t.Parallel()
	lines := []string{
		`{"type":"api-request","sessionId":"s","id":"request-1","timestamp":"2026-10-18T00:00:00Z","version":"2.2","querySource":"repl_main","shapeHash":"shape-1","params":{"model":"synthetic","max_tokens":4096,"betas":["synthetic"],"context_management":{"edits":[{"type":"clear_tool_uses","keep":2}]},"thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":"high"}},"base":"request-0","keep":2,"tail":["message-1"],"cacheControl":[{"path":["messages",0,"content",0],"value":{"type":"ephemeral","ttl":"1h","scope":"global"}}],"requestDigest":"digest-1","cut":true,"cutRequestDigest":"cut-1","withheld":["messages"],"unknown":"omit"}`,
		`{"type":"api-request-shape","sessionId":"s","shapeHash":"shape-1","shape":{"system":[{"type":"text","text":"NATIVE-SYSTEM"}],"tools":[{"name":"Read","description":"NATIVE-DESC","input_schema":{"type":"object","properties":{"NATIVE-PROP":{"type":"string"}}}}],"unknown":"omit"}}`,
		`{"type":"api-request-blob","sessionId":"s","hash":"message-1","message":{"role":"user","content":[{"type":"text","text":"NATIVE-PROMPT"}]}}`,
	}
	rows := scanLines(t, lines)
	for i, r := range rows {
		var got, want map[string]any
		raw, _ := json.Marshal(r)
		_ = json.Unmarshal(raw, &got)
		_ = json.Unmarshal([]byte(lines[i]), &want)
		for k, v := range want {
			if k == "unknown" {
				continue
			}
			if k == "shape" {
				delete(v.(map[string]any), "unknown")
			}
			if !reflect.DeepEqual(got[k], v) {
				t.Errorf("%s %s: got %v, want %v", r.Type, k, got[k], v)
			}
		}
		if got["cctraceCollect"] != float64(4) {
			t.Errorf("%s collection mark = %v, want 4", r.Type, got["cctraceCollect"])
		}
		if strings.Contains(string(raw), "omit") {
			t.Errorf("unknown protocol payload collected: %s", raw)
		}
		collected := r.CollectedRaw()
		if i == 2 && !strings.Contains(string(collected), "NATIVE-PROMPT") {
			t.Errorf("blob message missing from explicit content reenrich: %s", collected)
		}
		r.WithholdContent()
		if strings.Contains(string(r.CollectedRaw()), "NATIVE-") {
			t.Errorf("default reenrich leaks native content: %s", r.CollectedRaw())
		}
		if i > 0 && len(r.CctraceWithheld) == 0 {
			t.Errorf("no withheld marker on %s", r.Type)
		}
	}
}

func TestNativeAssistantAndStopSummaryAllowlist(t *testing.T) {
	t.Parallel()
	assistant := `{"type":"assistant","uuid":"a","sessionId":"s","requestRef":"request-1","apiBlockIndex":3,"wireToolInputs":{"tool-1":{"path":"WIRE-PATH","arbitrary":"WIRE-INPUT"}},"wireIngestContext":{"tool-1":{"cwd":"WIRE-CWD"}},"effort":"high","apiErrorStatus":429,"apiError":"API-ERROR","apiErrorParams":{"message":"API-PARAMS"},"apiErrorIsTransient":true,"message":{"role":"assistant","id":"message-1","content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"path":"file"}}],"diagnostics":{"arbitrary":"keep"},"input_transformations":[{"type":"synthetic"}],"usage":{"input_tokens":1,"server_tool_use":{"web_search_requests":3}}}}`
	stop := `{"type":"system","uuid":"stop","sessionId":"s","subtype":"stop_hook_summary","hookCount":1,"hookInfos":[{"command":"HOOK-COMMAND","durationMs":3,"promptText":"HOOK-PROMPT","unknown":"omit"}],"hookErrors":["HOOK-ERROR"],"hookAdditionalContext":["HOOK-CONTEXT"],"preventedContinuation":true,"stopReason":"HOOK-REASON","hasOutput":true,"level":"info","toolUseID":"tool-1"}`
	rows := scanLines(t, []string{assistant, stop})
	for i, r := range rows {
		got := collected(t, r)
		var want map[string]any
		line := assistant
		if i == 1 {
			line = stop
		}
		_ = json.Unmarshal([]byte(line), &want)
		for _, k := range recordKeys {
			delete(want, k)
		}
		if i == 1 {
			delete(want["hookInfos"].([]any)[0].(map[string]any), "unknown")
		}
		want["cctraceCollect"] = float64(4)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s metadata\ngot %v\nwant %v", r.Type, got, want)
		}
		if i == 0 {
			var original map[string]json.RawMessage
			_ = json.Unmarshal([]byte(assistant), &original)
			if !reflect.DeepEqual(decodeJSON(t, string(r.Message)), decodeJSON(t, string(original["message"]))) {
				t.Error("message diagnostic/usage fields changed")
			}
		}
		r.WithholdContent()
		if strings.Contains(string(r.CollectedRaw()), "WIRE-") || strings.Contains(string(r.CollectedRaw()), "HOOK-") || strings.Contains(string(r.CollectedRaw()), "API-") {
			t.Errorf("native metadata content leaked: %s", r.CollectedRaw())
		}
	}
}

func TestNativeCollectionMalformedFieldsAndErrorTruncationAreExplicit(t *testing.T) {
	t.Parallel()
	errorText := strings.Repeat("한", maxErrorBytes)
	encodedError, _ := json.Marshal(errorText)
	rows := scanLines(t, []string{
		`{"type":"api-request","sessionId":"s","id":"request-1","params":[],"tail":{},"cacheControl":false,"shapeHash":123,"keep":"zero"}`,
		`{"type":"assistant","uuid":"a","sessionId":"s","requestRef":{},"apiBlockIndex":true,"wireToolInputs":[],"wireIngestContext":"bad","apiError":` + string(encodedError) + `,"apiErrorParams":{"text":` + string(encodedError) + `},"cctraceReferences":{"version":99,"messages":[{"hash":"untrusted"}]}}`,
	})
	for _, r := range rows {
		if len(r.CctraceDropped) == 0 {
			t.Fatalf("malformed field absence not marked: %s", r.CollectedRaw())
		}
	}
	if got := rows[1].CctraceTruncated; !reflect.DeepEqual([]string(got), []string{"apiError", "apiErrorParams"}) {
		t.Errorf("error collection truncations not marked: %v", got)
	}
	if !json.Valid(rows[1].CollectedRaw()) {
		t.Errorf("truncation broke JSON: %s", rows[1].CollectedRaw())
	}
	if strings.Contains(string(rows[1].CollectedRaw()), "untrusted") {
		t.Error("source JSONL supplied derived reference metadata")
	}
	rows[1].WithholdContent()
	if strings.Contains(string(rows[1].CollectedRaw()), "한") {
		t.Errorf("truncated error content escaped default reenrich: %s", rows[1].CollectedRaw())
	}
}

func TestNativeSystemBlobPreservesSourceOutputConfig(t *testing.T) {
	t.Parallel()
	const message = `{"role":"system","content":[{"type":"text","text":"NATIVE-SYSTEM-BLOB"}],"output_config":{"effort":"high"},"unrelated":"omit"}`
	row := scanLines(t, []string{`{"type":"api-request-blob","sessionId":"s","hash":"system-1","message":` + message + `}`})[0]
	got := decodeJSON(t, string(row.Message))
	want := decodeJSON(t, message)
	delete(want, "unrelated")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("system blob source fields changed: got %v want %v", got, want)
	}
	row.WithholdContent()
	var raw map[string]any
	if err := json.Unmarshal(row.CollectedRaw(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["message"] != nil || string(row.CctraceWithheld[0]) != "message" {
		t.Errorf("default reenrich exposed system blob payload: %s", row.CollectedRaw())
	}
}
