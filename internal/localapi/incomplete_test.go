package localapi

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"cctrace/internal/jsonlscan"
)

func TestAnalysisDoesNotTreatUnreadableLinesAsFull(t *testing.T) {
	// The line-size hook is global, so these cases must not run in parallel.
	old := jsonlscan.MaxLineBytes
	defer func() { jsonlscan.MaxLineBytes = old }()
	for _, tc := range []struct {
		name, line, reason string
		limit              int
	}{
		{"malformed", "{broken}\n", "invalid_jsonl", old},
		{"oversized", `{"type":"ignored","padding":"` + strings.Repeat("x", 2048) + "\"}\n", "oversized_jsonl", 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, token, file := fixtureServer(t)
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, append(data, []byte(tc.line)...), 0600); err != nil {
				t.Fatal(err)
			}
			jsonlscan.MaxLineBytes = tc.limit
			code, body := request(t, s, token, sessionRoute(t, s, token)+"/analysis")
			var result AnalysisResponse
			if code != 200 || json.Unmarshal(body, &result) != nil {
				t.Fatalf("analysis status %d", code)
			}
			if result.Summary.TurnBoundaries != "undeterminable" || result.Summary.TurnBoundariesReason != tc.reason || result.Summary.InputTokens != nil || len(result.Turns) != 0 || result.Timeline.State != "undeterminable" {
				t.Fatal("incomplete source was presented as a complete analysis")
			}
		})
	}
}
