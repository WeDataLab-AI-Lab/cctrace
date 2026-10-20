package localapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteDTOFixtures serializes synthetic handler output; no personal transcript is used.
func TestWriteDTOFixtures(t *testing.T) {
	// Fixture regeneration writes shared testdata, so this test must not run in parallel.
	s, token, _ := fixtureServer(t)
	code, body := request(t, s, token, "/v1/sessions")
	if code != 200 {
		t.Fatal(code)
	}
	var list ListResponse
	if err := json.Unmarshal(body, &list); err != nil || len(list.Sessions) != 1 {
		t.Fatalf("list: %v", err)
	}
	// Keep raw IDs for handler requests; genericize only serialized synthetic samples.
	fixtureIDs := strings.NewReplacer(
		`"`+list.Sessions[0].LocalID+`"`, `"11111111111111111111111111111111"`,
		`"`+list.Sessions[0].ProfileFingerprint+`"`, `"22222222222222222222222222222222"`,
		`"`+list.Revision+`"`, `"33333333333333333333333333333333"`,
		`"`+list.Sessions[0].Revision+`"`, `"44444444444444444444444444444444"`,
	)
	samples := []struct {
		name, path string
		value      any
	}{{"list.json", "/v1/sessions", &ListResponse{}}, {"records.json", "/v1/sessions/" + list.Sessions[0].LocalID + "/records", &RecordsResponse{}}, {"analysis.json", "/v1/sessions/" + list.Sessions[0].LocalID + "/analysis", &AnalysisResponse{}}}
	for _, item := range samples {
		code, body = request(t, s, token, item.path)
		if code != 200 {
			t.Fatalf("%s: %d", item.name, code)
		}
		if err := json.Unmarshal(body, item.value); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("UPDATE_LOCALAPI_FIXTURES") != "1" {
			continue
		}
		var v any
		_ = json.Unmarshal([]byte(fixtureIDs.Replace(string(body))), &v)
		pretty, _ := json.MarshalIndent(v, "", "  ")
		pretty = append(pretty, '\n')
		if err := os.WriteFile(filepath.Join("testdata", item.name), pretty, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDTOFixtureIdentifiers(t *testing.T) {
	t.Parallel()
	var list ListResponse
	var records RecordsResponse
	var analysis AnalysisResponse
	for _, item := range []struct {
		name  string
		value any
	}{{"list.json", &list}, {"records.json", &records}, {"analysis.json", &analysis}} {
		body, err := os.ReadFile(filepath.Join("testdata", item.name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, item.value); err != nil {
			t.Fatal(err)
		}
	}
	if len(list.Sessions) != 1 {
		t.Fatalf("sessions=%d", len(list.Sessions))
	}
	session := list.Sessions[0]
	if session.LocalID != strings.Repeat("1", 32) || session.CanonicalID != session.LocalID || analysis.LocalID != session.LocalID {
		t.Fatal("fixture local and canonical IDs must share the synthetic placeholder")
	}
	if session.ProfileFingerprint != strings.Repeat("2", 32) || list.Revision != strings.Repeat("3", 32) {
		t.Fatal("fixture profile and list revision must use distinct synthetic placeholders")
	}
	if session.Revision != strings.Repeat("4", 32) || records.Revision != session.Revision || analysis.Revision != session.Revision {
		t.Fatal("fixture session revisions must share the synthetic placeholder")
	}
}
