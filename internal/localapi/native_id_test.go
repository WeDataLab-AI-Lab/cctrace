package localapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestListUnobservedNativeIDIsNullAndStillSelectable(t *testing.T) {
	s, token, file := fixtureServer(t)
	files := map[string]string{
		"unknown.jsonl":                 `{"type":"user","timestamp":"2026-01-01T00:00:00Z","uuid":"unknown","message":{"role":"user","content":"example"}}` + "\n",
		"partial.jsonl":                 `{"type":"user","sessionId":"unfinished"}`,
		"subagents/agent-unknown.jsonl": `{"type":"user","timestamp":"2026-01-01T00:00:00Z","uuid":"agent","message":{"role":"user","content":"example"}}` + "\n",
	}
	for name, data := range files {
		path := filepath.Join(filepath.Dir(file), name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	code, body := request(t, s, token, "/v1/sessions")
	var list struct {
		Sessions []struct {
			LocalID         string          `json:"local_id"`
			CanonicalID     string          `json:"canonical_id"`
			SessionID       string          `json:"session_id"`
			NativeSessionID json.RawMessage `json:"native_session_id"`
		} `json:"sessions"`
	}
	if code != 200 || json.Unmarshal(body, &list) != nil || len(list.Sessions) != 4 {
		t.Fatalf("mixed listing status %d", code)
	}
	unknown := 0
	for _, row := range list.Sessions {
		if row.LocalID == "" || row.CanonicalID != row.LocalID || row.SessionID == "" {
			t.Error("unobserved native identity removed the local selection key")
		}
		if string(row.NativeSessionID) == `"native-id"` {
			continue
		}
		unknown++
		if string(row.NativeSessionID) != "null" {
			t.Error("unobserved native identity is not JSON null")
		}
		code, _ = request(t, s, token, "/v1/sessions/"+row.LocalID+"/records")
		if code != 200 {
			t.Errorf("opaque selection status %d", code)
		}
	}
	if unknown != 3 {
		t.Errorf("unknown native identities %d, want 3", unknown)
	}
}
