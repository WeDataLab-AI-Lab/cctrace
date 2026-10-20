package localapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestListCursorDetectsAppendAndSameSizeRewrite(t *testing.T) {
	s, token, file := fixtureServer(t)
	other := filepath.Join(filepath.Dir(file), "second.jsonl")
	if err := os.WriteFile(other, []byte(`{"type":"user","timestamp":"2026-01-01T00:00:00Z","uuid":"u2","message":{"role":"user","content":"second"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, body := request(t, s, token, "/v1/sessions?limit=1")
	if code != 200 {
		t.Fatalf("list %d %s", code, body)
	}
	var result ListResponse
	_ = json.Unmarshal(body, &result)
	if result.NextCursor == nil {
		t.Fatal("missing cursor")
	}
	code, body = request(t, s, token, "/v1/sessions?limit=1&cursor="+*result.NextCursor)
	if code != 200 {
		t.Fatalf("valid cursor %d %s", code, body)
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"type":"user"}` + "\n")
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	code, body = request(t, s, token, "/v1/sessions?limit=1&cursor="+*result.NextCursor)
	if code != 409 {
		t.Fatalf("stale cursor %d %s", code, body)
	}
	before, err := os.ReadFile(other)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	after := append([]byte(nil), before...)
	for i := range after {
		if after[i] == 's' {
			after[i] = 'x'
			break
		}
	}
	if err := os.WriteFile(other, after, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	code, body = request(t, s, token, "/v1/sessions")
	if code != 200 {
		t.Fatalf("rewritten list %d %s", code, body)
	}
	var changed ListResponse
	_ = json.Unmarshal(body, &changed)
	if changed.Revision == result.Revision {
		t.Fatal("same-size rewrite left revision unchanged")
	}
}
