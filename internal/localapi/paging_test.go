package localapi

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func sessionRoute(t *testing.T, s *Server, token string) string {
	t.Helper()
	code, body := request(t, s, token, "/v1/sessions")
	var list ListResponse
	if code != 200 || json.Unmarshal(body, &list) != nil || len(list.Sessions) != 1 {
		t.Fatalf("listing failed: status %d", code)
	}
	return "/v1/sessions/" + list.Sessions[0].LocalID
}

func TestRecordPagesAndStaleCursor(t *testing.T) {
	s, token, file := fixtureServer(t)
	route := sessionRoute(t, s, token)
	code, body := request(t, s, token, route+"/records?limit=1")
	var first RecordsResponse
	if code != 200 || json.Unmarshal(body, &first) != nil || len(first.Records) != 1 || first.NextCursor == nil {
		t.Fatalf("first page: status %d", code)
	}
	code, body = request(t, s, token, route+"/records?limit=1&cursor="+*first.NextCursor)
	var second RecordsResponse
	if code != 200 || json.Unmarshal(body, &second) != nil || len(second.Records) != 1 || second.NextCursor != nil || second.Records[0].UUID == first.Records[0].UUID {
		t.Fatalf("second page: status %d", code)
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("{\"type\":\"user\",\"uuid\":\"u2\"}\n")
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	code, _ = request(t, s, token, route+"/records?cursor="+*first.NextCursor)
	if code != 409 {
		t.Fatalf("stale cursor status %d, want 409", code)
	}
	for _, query := range []string{"limit=0", "limit=501", "cursor=bad"} {
		code, _ = request(t, s, token, route+"/records?"+query)
		if code != 400 {
			t.Errorf("%s status %d, want 400", query, code)
		}
	}
}

func TestLargeSessionReadsPagesAndAnalysis(t *testing.T) {
	s, token, file := fixtureServer(t)
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// Large valid metadata lines must not become retained conversation bodies.
	padding := `{"type":"ignored","padding":"` + strings.Repeat("x", 1<<20) + "\"}\n"
	for range 150 {
		if _, err = f.WriteString(padding); err != nil {
			t.Fatal(err)
		}
	}
	_, err = f.WriteString("{\"type\":\"user\",\"timestamp\":\"2026-01-01T00:00:03Z\",\"uuid\":\"u2\",\"parentUuid\":\"a1\",\"message\":{\"role\":\"user\",\"content\":\"next\"}}\n")
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	route := sessionRoute(t, s, token)
	code, body := request(t, s, token, route+"/records?limit=1")
	var page RecordsResponse
	if code != 200 || json.Unmarshal(body, &page) != nil || len(page.Records) != 1 {
		t.Fatalf("large first page status %d", code)
	}
	code, body = request(t, s, token, route+"/analysis")
	var analysis AnalysisResponse
	if code != 200 || json.Unmarshal(body, &analysis) != nil || analysis.Summary.InputTokens == nil || *analysis.Summary.InputTokens != 10 {
		t.Fatalf("large analysis status %d", code)
	}
}

func TestAnalysisUsesObservedTitleAndRequestHeads(t *testing.T) {
	s, token, file := fixtureServer(t)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("{\"type\":\"custom-title\",\"customTitle\":\"example title\"}\n")...)
	data = append(data, []byte("{\"type\":\"assistant\",\"timestamp\":\"2026-01-01T00:00:02Z\",\"uuid\":\"a2\",\"parentUuid\":\"a1\",\"message\":{\"id\":\"m1\",\"role\":\"assistant\",\"model\":\"test-model\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n")...)
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	code, body := request(t, s, token, sessionRoute(t, s, token)+"/analysis")
	var a AnalysisResponse
	if code != 200 || json.Unmarshal(body, &a) != nil {
		t.Fatalf("analysis status %d", code)
	}
	if a.Summary.Title == nil || *a.Summary.Title != "example title" || a.Summary.TitleState != "ok" {
		t.Error("observed title missing or marked withheld")
	}
	if a.Summary.InputTokens == nil || *a.Summary.InputTokens != 10 || a.Summary.OutputTokens == nil || *a.Summary.OutputTokens != 5 {
		t.Error("request head token maximum was not preserved")
	}
}

func TestIncompleteLineIsNotARecordPage(t *testing.T) {
	s, token, file := fixtureServer(t)
	data, _ := os.ReadFile(file)
	data = append(data, []byte(`{"type":"user","uuid":"partial"}`)...)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	route := sessionRoute(t, s, token)
	code, body := request(t, s, token, route+"/records?limit=2")
	var page RecordsResponse
	if code != 200 || json.Unmarshal(body, &page) != nil || len(page.Records) != 2 || page.NextCursor != nil {
		t.Fatalf("incomplete last line produced page: status %d", code)
	}
	if bytes.Contains(body, []byte(`"uuid":"partial"`)) {
		t.Fatal("incomplete line returned")
	}
}
