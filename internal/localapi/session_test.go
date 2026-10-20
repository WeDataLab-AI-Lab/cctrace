package localapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func fixtureServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	claude := t.TempDir()
	dir := filepath.Join(claude, "projects", "-sample")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "session.jsonl")
	lines := `{"type":"user","timestamp":"2026-01-01T00:00:00Z","uuid":"u1","sessionId":"native-id","message":{"role":"user","content":"hello"}}` + "\n" + `{"type":"assistant","timestamp":"2026-01-01T00:00:02Z","uuid":"a1","parentUuid":"u1","sessionId":"native-id","message":{"id":"m1","role":"assistant","model":"test-model","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":2}}}` + "\n"
	if err := os.WriteFile(file, []byte(lines), 0600); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	s, err := New(Options{Address: "127.0.0.1:12345", Token: token, ClaudeDirs: []string{claude}, AllowedOrigins: []string{"http://127.0.0.1:60895"}})
	if err != nil {
		t.Fatal(err)
	}
	return s, token, file
}
func request(t *testing.T, s *Server, token, path string) (int, []byte) {
	t.Helper()
	r := httptest.NewRequest("GET", "http://127.0.0.1:12345"+path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}
func TestListRecordsAnalysisAndRevision(t *testing.T) {
	s, token, file := fixtureServer(t)
	code, body := request(t, s, token, "/v1/sessions?limit=1")
	if code != 200 {
		t.Fatalf("list %d %s", code, body)
	}
	var listed ListResponse
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 {
		t.Fatalf("sessions=%d", len(listed.Sessions))
	}
	id := listed.Sessions[0].LocalID
	for _, endpoint := range []string{"records", "analysis"} {
		code, body = request(t, s, token, "/v1/sessions/"+id+"/"+endpoint)
		if code != 200 {
			t.Fatalf("%s %d %s", endpoint, code, body)
		}
		if endpoint == "records" {
			var v RecordsResponse
			if err := json.Unmarshal(body, &v); err != nil || len(v.Records) != 2 || v.Records[0].View.Text != "hello" {
				t.Fatalf("records: %s (%v)", body, err)
			}
		} else {
			var v AnalysisResponse
			if err := json.Unmarshal(body, &v); err != nil || v.Summary.InputTokens == nil || *v.Summary.InputTokens != 10 || v.Summary.TurnCount == nil || *v.Summary.TurnCount != 1 {
				t.Fatalf("analysis: %s (%v)", body, err)
			}
		}
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"type":"user","timestamp":"2026-01-01T00:00:03Z","uuid":"partial"}`)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	code, body = request(t, s, token, "/v1/sessions/"+id+"/records")
	if code != 200 {
		t.Fatalf("partial %d %s", code, body)
	}
	var v RecordsResponse
	_ = json.Unmarshal(body, &v)
	if len(v.Records) != 2 {
		t.Fatalf("partial line returned: %d", len(v.Records))
	}
	if err = os.WriteFile(file, []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, body = request(t, s, token, "/v1/sessions?limit=1")
	if code != 200 {
		t.Fatalf("rewrite %d %s", code, body)
	}
	var changed ListResponse
	_ = json.Unmarshal(body, &changed)
	if changed.Revision == listed.Revision {
		t.Fatal("rewrite did not change revision")
	}
}
