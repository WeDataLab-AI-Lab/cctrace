package syncer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cctrace/internal/sessionlog"
)

// The fields sessionlog keeps on a line reach raw through toStoreRecord, and
// keeping them moves no row. The storage key is (session_id, ts, record_type,
// profile_email, uuid): a line with a uuid is keyed by it, a line without one by
// a digest of the line, and either way a changed key would insert a second row
// for a line already stored and leave the reenrich merge nothing to find.
//
// The expected keys, and the raw of every line without a uuid, are what
// toStoreRecord produced for these exact lines before CollectVersion (8db0110d),
// so this pins "unchanged" against the old code rather than against itself.
func TestToStoreRecordCarriesCollectedFieldsWithoutMovingKeys(t *testing.T) {
	cases := []struct {
		line    string
		uuid    string
		ts      string
		oldRaw  string
		added   string // kept fields; "" means raw must be byte-identical to oldRaw
		recType string
	}{
		{
			line:    `{"type":"user","uuid":"u-1","timestamp":"2026-10-01T00:00:00Z","sessionId":"s-keys","message":{"role":"user","content":"hello"},"promptId":"p-1","permissionMode":"plan","toolUseResult":{"status":"completed","stdout":"x"}}`,
			recType: "user", uuid: "u-1", ts: "2026-10-01T00:00:00Z",
			oldRaw: `{"type":"user","timestamp":"2026-10-01T00:00:00Z","uuid":"u-1","sessionId":"s-keys","cwd":"","version":"","gitBranch":"","message":{"role":"user","content":"hello"}}`,
			added:  `{"promptId":"p-1","permissionMode":"plan","toolUseResult":{"status":"completed"},"cctraceCollect":4}`,
		},
		{
			line:    `{"type":"assistant","uuid":"a-1","timestamp":"2026-10-01T00:00:01Z","sessionId":"s-keys","message":{"role":"assistant","model":"m","usage":{"input_tokens":3}},"requestId":"req_1"}`,
			recType: "assistant", uuid: "a-1", ts: "2026-10-01T00:00:01Z",
			oldRaw: `{"type":"assistant","timestamp":"2026-10-01T00:00:01Z","uuid":"a-1","sessionId":"s-keys","cwd":"","version":"","gitBranch":"","message":{"role":"assistant","model":"m","usage":{"input_tokens":3}}}`,
			added:  `{"requestId":"req_1","cctraceCollect":4}`,
		},
		{
			line:    `{"type":"system","uuid":"y-1","timestamp":"2026-10-01T00:00:02Z","sessionId":"s-keys","subtype":"turn_duration","durationMs":5}`,
			recType: "system", uuid: "y-1", ts: "2026-10-01T00:00:02Z",
			oldRaw: `{"type":"system","timestamp":"2026-10-01T00:00:02Z","uuid":"y-1","sessionId":"s-keys","cwd":"","version":"","gitBranch":"","message":null}`,
			added:  `{"subtype":"turn_duration","durationMs":5,"cctraceCollect":4}`,
		},
		{
			line:    `{"type":"attachment","uuid":"t-1","timestamp":"2026-10-01T00:00:03Z","sessionId":"s-keys","attachment":{"type":"queued_command","prompt":"later","commandMode":"prompt"}}`,
			recType: "attachment", uuid: "t-1", ts: "2026-10-01T00:00:03Z",
			oldRaw: `{"type":"attachment","timestamp":"2026-10-01T00:00:03Z","uuid":"t-1","sessionId":"s-keys","cwd":"","version":"","gitBranch":"","message":null}`,
			added:  `{"attachment":{"type":"queued_command","prompt":"later","commandMode":"prompt"},"cctraceCollect":4}`,
		},
		{
			line:    `{"type":"permission-mode","permissionMode":"plan","sessionId":"s-keys"}`,
			recType: "permission-mode", uuid: "content-8cec391be8b155f49dba12d99b9f4f14", ts: "1970-01-01T00:00:00Z",
			oldRaw: `{"type":"permission-mode","timestamp":"0001-01-01T00:00:00Z","uuid":"","sessionId":"s-keys","cwd":"","version":"","gitBranch":"","message":null}`,
		},
		{
			line:    `{"type":"last-prompt","lastPrompt":"do it","sessionId":"s-keys"}`,
			recType: "last-prompt", uuid: "content-f3a48013ba41175b667274e9d57f8657", ts: "1970-01-01T00:00:00Z",
			oldRaw: `{"type":"last-prompt","timestamp":"0001-01-01T00:00:00Z","uuid":"","sessionId":"s-keys","cwd":"","version":"","gitBranch":"","message":null}`,
		},
		{
			line:    `{"type":"queue-operation","operation":"enqueue","timestamp":"2026-10-01T00:00:04Z","sessionId":"s-keys","content":"queued"}`,
			recType: "queue-operation", uuid: "content-7759f579517e822059ef51648e9ed810", ts: "2026-10-01T00:00:04Z",
			oldRaw: `{"type":"queue-operation","timestamp":"2026-10-01T00:00:04Z","uuid":"","sessionId":"s-keys","cwd":"","version":"","gitBranch":"","message":null}`,
		},
		{
			line:    `{"type":"user","timestamp":"2026-10-01T00:00:05Z","sessionId":"s-keys","message":{"role":"user","content":"no uuid"},"promptId":"p-2"}`,
			recType: "user", uuid: "content-96ab72671c22d895a97aded3e59e2a03", ts: "2026-10-01T00:00:05Z",
			oldRaw: `{"type":"user","timestamp":"2026-10-01T00:00:05Z","uuid":"","sessionId":"s-keys","cwd":"","version":"","gitBranch":"","message":{"role":"user","content":"no uuid"}}`,
		},
	}

	lines := make([]string, len(cases))
	for i, tc := range cases {
		lines[i] = tc.line
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	records, _, err := sessionlog.ScanFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(cases) {
		t.Fatalf("scanned %d records, want %d", len(records), len(cases))
	}

	for i, tc := range cases {
		sr := toStoreRecord(records[i], "p@example.com", "u", "h")
		if sr == nil {
			t.Fatalf("%s: skipped", tc.line)
		}
		wantTs, _ := time.Parse(time.RFC3339, tc.ts)
		if sr.RecordType != tc.recType || sr.UUID != tc.uuid || !sr.Ts.Equal(wantTs) {
			t.Errorf("key moved for %s\n got  (%s, %s, %s)\n want (%s, %s, %s)", tc.line, sr.RecordType, sr.UUID, sr.Ts.UTC().Format(time.RFC3339), tc.recType, tc.uuid, tc.ts)
		}
		// References are independently verified by TestNativeReferenceHashesSurviveRedaction.
		// They add only hashes and block indices; all previous raw keys remain exact.
		gotRaw := decodeObject(t, string(sr.Raw))
		delete(gotRaw, "cctraceReferences")
		if tc.added == "" {
			if !reflect.DeepEqual(gotRaw, decodeObject(t, tc.oldRaw)) {
				t.Errorf("raw of a line without a uuid changed\n got  %s\n want %s", sr.Raw, tc.oldRaw)
			}
			continue
		}
		// Everything raw carried before is still there, unchanged, and the only
		// additions are the kept fields.
		want := decodeObject(t, tc.oldRaw)
		for k, v := range decodeObject(t, tc.added) {
			want[k] = v
		}
		if !reflect.DeepEqual(gotRaw, want) {
			wantJSON, _ := json.Marshal(want)
			t.Errorf("raw for %s\n got  %s\n want %s", tc.uuid, sr.Raw, wantJSON)
		}
	}
}

func decodeObject(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("decode %s: %v", s, err)
	}
	return m
}
