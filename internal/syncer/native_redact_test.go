package syncer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"strings"
	"testing"

	"cctrace/internal/sessionlog"
	"cctrace/internal/store"
)

func TestNativeRequestSensitivePathsRespectEitherPolicy(t *testing.T) {
	t.Parallel()
	for _, policy := range []RedactPolicy{{UserPrompts: true}, {ToolDetails: true}} {
		for _, raw := range []string{
			`{"type":"api-request-blob","message":{"role":"system","content":[{"type":"text","text":"PRIVATE-SYSTEM-BLOB"}],"output_config":{"PRIVATE-SETTING":"value"}}}`,
			`{"message":{"role":"system","content":[{"type":"text","text":"PRIVATE-SYSTEM-BLOB"}],"output_config":{"PRIVATE-SETTING":"value"}}}`,
			`{"shape":{"system":[{"type":"text","text":"PRIVATE-SYSTEM"}],"tools":[{"name":"Read","input_schema":{"properties":{"PRIVATE-PROPERTY":{"type":"string"}}}}]},"shapeHash":"shape-1"}`,
			`{"wireToolInputs":{"tool-1":{"PRIVATE-PROPERTY":"PRIVATE-INPUT"}},"wireIngestContext":{"tool-1":{"cwd":"PRIVATE-CWD"}},"requestRef":"request-1"}`,
			`{"subtype":"stop_hook_summary","hookInfos":[{"command":"PRIVATE-COMMAND","promptText":"PRIVATE-PROMPT","durationMs":1}],"hookErrors":["PRIVATE-ERROR"],"hookAdditionalContext":["PRIVATE-CONTEXT"],"stopReason":"PRIVATE-REASON","preventedContinuation":true}`,
			`{"apiError":"PRIVATE-ERROR","apiErrorParams":{"message":"PRIVATE-DETAILS"},"apiErrorStatus":429}`,
		} {
			row := &store.SessionRecord{Raw: json.RawMessage(raw)}
			Redact([]*store.SessionRecord{row}, policy)
			if strings.Contains(string(row.Raw), "PRIVATE-") {
				t.Errorf("policy %+v leaked: %s", policy, row.Raw)
			}
		}
	}
}

func TestNativeReferenceHashesSurviveRedaction(t *testing.T) {
	t.Parallel()
	const source = `{"type":"assistant","uuid":"reply","sessionId":"native","message":{"id":"message-1","role":"assistant","content":[{"type":"text","text":"<PRIVATE-TEXT>한글","cache_control":{"type":"ephemeral"}},{"type":"tool_use","id":"tool-1","name":"Read","input":{"z":1,"a":"PRIVATE-PATH"}}],"usage":{"input_tokens":9},"diagnostics":{"note":"diagnostic"}}}`
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(source+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rows, _, err := sessionlog.ScanFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	row := toStoreRecord(rows[0], "", "", "project")
	raw := decodeObject(t, string(row.Raw))
	refs, ok := raw["cctraceReferences"].(map[string]any)
	if !ok {
		t.Fatalf("pre-redaction references missing: %s", row.Raw)
	}
	messages := refs["messages"].([]any)
	message := messages[0].(map[string]any)
	// Hashes use role/content only; cache_control, usage and diagnostic objects
	// cannot affect native blob identity. This expected digest is independently
	// computed from the canonical synthetic fixture below.
	expected := sha256.Sum256([]byte(`{"content":[{"text":"<PRIVATE-TEXT>한글","type":"text"},{"id":"tool-1","input":{"a":"PRIVATE-PATH","z":1},"name":"Read","type":"tool_use"}],"role":"assistant"}`))
	if message["hash"] != hex.EncodeToString(expected[:]) {
		t.Errorf("message hash=%v want=%x", message["hash"], expected)
	}
	blocks := message["blocks"].([]any)
	if len(blocks) != 2 || blocks[1].(map[string]any)["index"] != float64(1) {
		t.Fatalf("original block indices lost: %v", blocks)
	}
	before, _ := json.Marshal(refs)
	Redact([]*store.SessionRecord{row}, RedactPolicy{UserPrompts: true, ToolDetails: true})
	after := decodeObject(t, string(row.Raw))["cctraceReferences"]
	afterJSON, _ := json.Marshal(after)
	if string(before) != string(afterJSON) {
		t.Errorf("redaction changed reference index: before=%s after=%s", before, afterJSON)
	}
	if strings.Contains(string(row.Raw), "PRIVATE-") {
		t.Errorf("redaction leaked source body: %s", row.Raw)
	}
	// Repeated conversion and caller-supplied metadata cannot change the index.
	row2 := toStoreRecord(rows[0], "", "", "project")
	repeated := decodeObject(t, string(row2.Raw))["cctraceReferences"]
	repeatedJSON, _ := json.Marshal(repeated)
	if string(before) != string(repeatedJSON) {
		t.Errorf("reference metadata not stable: %s", repeatedJSON)
	}
}
