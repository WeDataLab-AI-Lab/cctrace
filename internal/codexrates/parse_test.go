package codexrates

import (
	"math"
	"os"
	"testing"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func wantRate(t *testing.T, got map[string]Rate, model string, in, out, cache float64) {
	t.Helper()
	r, ok := got[model]
	if !ok {
		t.Fatalf("model %q missing from parsed table (%d rows)", model, len(got))
	}
	if math.Abs(r.Input-in) > 1e-9 || math.Abs(r.Output-out) > 1e-9 || math.Abs(r.CacheRead-cache) > 1e-9 {
		t.Errorf("%s = %+v, want {Input:%v Output:%v CacheRead:%v}", model, r, in, out, cache)
	}
}

// The markdown table carries long-context columns for the same models. Reading
// them would double every rate on the models that have both.
func TestParseMarkdown_IgnoresLongContextColumns(t *testing.T) {
	got, err := parseMarkdown(readFixture(t, "pricing.md"))
	if err != nil {
		t.Fatalf("parseMarkdown: %v", err)
	}
	wantRate(t, got, "gpt-6-astra", 10, 50, 1)
	wantRate(t, got, "gpt-5.4", 2.5, 15, 0.25)
	wantRate(t, got, "gpt-5.6-luna", 0.2, 1.2, 0.02)
}

// Batch/Flex/Fast sections repeat every model at a different price. Only the
// Standard section is the one Codex sessions are billed at.
func TestParseMarkdown_StandardSectionOnly(t *testing.T) {
	got, err := parseMarkdown(readFixture(t, "pricing.md"))
	if err != nil {
		t.Fatalf("parseMarkdown: %v", err)
	}
	// Batch price for gpt-6-astra is 5.00/25.00 -- standard is 10.00/50.00.
	wantRate(t, got, "gpt-6-astra", 10, 50, 1)
	if _, ok := got["gpt-5.3-codex"]; ok {
		t.Errorf("grouped-pricing section leaked into the standard table")
	}
}

// Columns are located by header name, not by index, so an added or moved
// column must not shift the rates.
func TestParseMarkdown_ColumnsFoundByHeaderName(t *testing.T) {
	md := `### Standard pricing data

| Model | Short context output | Long context input | Short context cached input | Short context input |
| --- | --- | --- | --- | --- |
| gpt-5.4 | $15.00 | $5.00 | $0.25 | $2.50 |
`
	got, err := parseMarkdown(md)
	if err != nil {
		t.Fatalf("parseMarkdown: %v", err)
	}
	wantRate(t, got, "gpt-5.4", 2.5, 15, 0.25)
}

func TestParseMarkdown_MissingCells(t *testing.T) {
	md := `### Standard pricing data

| Model | Short context input | Short context cached input | Short context cache writes | Short context output |
| --- | --- | --- | --- | --- |
| gpt-5.5-pro | $30.00 | - | - | $180.00 |
| no-output | $1.00 | $0.10 | - | — |
| no-input |  | $0.10 | - | $2.00 |
`
	got, err := parseMarkdown(md)
	if err != nil {
		t.Fatalf("parseMarkdown: %v", err)
	}
	// A missing cached-input rate is 0, not a reason to drop the model.
	wantRate(t, got, "gpt-5.5-pro", 30, 180, 0)
	// A missing input or output rate would price the model at 0 -- skip the row.
	for _, m := range []string{"no-output", "no-input"} {
		if _, ok := got[m]; ok {
			t.Errorf("%s should have been skipped, got %+v", m, got[m])
		}
	}
}

func TestParseMarkdown_ModelNameNormalisation(t *testing.T) {
	md := `### Standard pricing data

| Model | Short context input | Short context cached input | Short context output |
| --- | --- | --- | --- |
| [GPT-5.4](https://developers.openai.com/models/gpt-5.4) (<272K context length) | $2.50 | $0.25 | $15.00 |
| gpt-5.4-mini (limited availability) | $0.75 | $0.075 | $4.50 |
`
	got, err := parseMarkdown(md)
	if err != nil {
		t.Fatalf("parseMarkdown: %v", err)
	}
	wantRate(t, got, "gpt-5.4", 2.5, 15, 0.25)
	wantRate(t, got, "gpt-5.4-mini", 0.75, 4.5, 0.075)
}

func TestParseAstro(t *testing.T) {
	got, err := parseAstro(readFixture(t, "pricing.html"))
	if err != nil {
		t.Fatalf("parseAstro: %v", err)
	}
	wantRate(t, got, "gpt-6-astra", 10, 50, 1)
	wantRate(t, got, "gpt-5.6-sol", 4, 20, 0.4)
	// 4-column row (no cache-writes column): output must still come from the last cell.
	wantRate(t, got, "gpt-5.2", 1.75, 14, 0.175)
	// gpt-5.5-pro has a "-" cached-input cell.
	wantRate(t, got, "gpt-5.5-pro", 30, 180, 0)
	if len(got) < 30 {
		t.Errorf("parsed only %d rows from the astro props", len(got))
	}
}

func TestParseHTMLTable(t *testing.T) {
	got, err := parseHTMLTable(readFixture(t, "pricing.html"))
	if err != nil {
		t.Fatalf("parseHTMLTable: %v", err)
	}
	// The rendered table is collapsed to the newest models; short-context columns
	// come first, and the long-context ones must be ignored.
	wantRate(t, got, "gpt-6-astra", 10, 50, 1)
	wantRate(t, got, "gpt-5.6-terra", 2, 12, 0.2)
}
