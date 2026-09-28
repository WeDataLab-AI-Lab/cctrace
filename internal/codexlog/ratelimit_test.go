package codexlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeJSONL(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout-2026-08-24T09-00-00-0199aaaa-bbbb-cccc-dddd-eeeeffff0000.jsonl")
	// The fixtures are written across several source lines for readability, but
	// JSONL is one record per line — so the newlines are folded back out.
	body := ""
	for _, l := range lines {
		body += strings.Join(strings.Fields(l), " ") + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func scanSamples(t *testing.T, path string) []RateLimitSample {
	t.Helper()
	_, samples, _, _, err := ScanFileWithSamplesContext(t.Context(), path, 0, "s1", Metadata{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return samples
}

// Codex reports the length of each window as a number rather than naming it,
// and the position in the payload does not fix which length appears there. On
// the machine this was written against, primary was the 7-day window on some
// sessions and the 5-hour window on others. Keying off primary/secondary would
// therefore file weekly readings as five-hour ones.
func TestRateLimits_windowIsIdentifiedByLengthNotPosition(t *testing.T) {
	path := writeJSONL(t, `{"timestamp":"2026-08-24T09:00:00.000Z","type":"event_msg","payload":{
	  "type":"token_count","info":null,
	  "rate_limits":{
	    "primary":{"used_percent":22.0,"window_minutes":10080,"resets_at":1786850006},
	    "secondary":null,"plan_type":"pro"}}}`)

	samples := scanSamples(t, path)
	if len(samples) != 1 {
		t.Fatalf("%d samples, want 1", len(samples))
	}
	if len(samples[0].Windows) != 1 {
		t.Fatalf("%d windows, want 1 (secondary was null)", len(samples[0].Windows))
	}
	w := samples[0].Windows[0]
	if w.WindowMinutes != 10080 {
		t.Errorf("window minutes = %d, want 10080 even though it arrived as primary", w.WindowMinutes)
	}
	if w.UsedPercent != 22 {
		t.Errorf("used_percent = %v, want 22", w.UsedPercent)
	}
}

// info and rate_limits are siblings, and info is null on some readings. Parsing
// the limits inside the info branch would drop those silently.
func TestRateLimits_arriveWithNullInfo(t *testing.T) {
	path := writeJSONL(t, `{"timestamp":"2026-08-24T09:00:00.000Z","type":"event_msg","payload":{
	  "type":"token_count","info":null,
	  "rate_limits":{
	    "primary":{"used_percent":0.0,"window_minutes":300,"resets_at":1765459134},
	    "secondary":{"used_percent":0.0,"window_minutes":10080,"resets_at":1766045934},
	    "credits":{"has_credits":false,"unlimited":false,"balance":null},"plan_type":null}}}`)

	samples := scanSamples(t, path)
	if len(samples) != 1 {
		t.Fatalf("%d samples with a null info, want 1", len(samples))
	}
	if len(samples[0].Windows) != 2 {
		t.Fatalf("%d windows, want 2", len(samples[0].Windows))
	}
}

// resets_at is unix epoch seconds here, unlike the ISO8601 Anthropic sends.
func TestRateLimits_resetsAtIsUnixEpoch(t *testing.T) {
	path := writeJSONL(t, `{"timestamp":"2026-08-24T09:00:00.000Z","type":"event_msg","payload":{
	  "type":"token_count",
	  "rate_limits":{"primary":{"used_percent":10,"window_minutes":300,"resets_at":1786850006}}}}`)

	w := scanSamples(t, path)[0].Windows[0]
	if w.ResetsAt == nil {
		t.Fatal("resets_at was not parsed")
	}
	if got, want := w.ResetsAt.UTC(), time.Unix(1786850006, 0).UTC(); !got.Equal(want) {
		t.Errorf("resets_at = %v, want %v", got, want)
	}
}

// The plan arrives with the reading, so the price lookup needs no separate
// call and no JWT decoding.
func TestRateLimits_carryThePlan(t *testing.T) {
	path := writeJSONL(t, `{"timestamp":"2026-08-24T09:00:00.000Z","type":"event_msg","payload":{
	  "type":"token_count",
	  "rate_limits":{"primary":{"used_percent":10,"window_minutes":300},"plan_type":"prolite"}}}`)

	if got := scanSamples(t, path)[0].PlanType; got != "prolite" {
		t.Errorf("plan = %q, want prolite", got)
	}
}

// The sample's own timestamp is the point on the time axis; it is not the
// moment the scan happened to run.
func TestRateLimits_useTheRecordTimestamp(t *testing.T) {
	path := writeJSONL(t, `{"timestamp":"2026-08-24T09:03:47.123Z","type":"event_msg","payload":{
	  "type":"token_count",
	  "rate_limits":{"primary":{"used_percent":10,"window_minutes":300}}}}`)

	want := time.Date(2026, 8, 24, 9, 3, 47, 123000000, time.UTC)
	if got := scanSamples(t, path)[0].Timestamp; !got.Equal(want) {
		t.Errorf("timestamp = %v, want %v", got, want)
	}
}

// A window with no length cannot be filed against the 5h or 7d series, and
// guessing one would put a reading on the wrong line.
func TestRateLimits_skipWindowsWithoutALength(t *testing.T) {
	path := writeJSONL(t, `{"timestamp":"2026-08-24T09:00:00.000Z","type":"event_msg","payload":{
	  "type":"token_count",
	  "rate_limits":{"primary":{"used_percent":10},"secondary":{"used_percent":5,"window_minutes":300}}}}`)

	windows := scanSamples(t, path)[0].Windows
	if len(windows) != 1 || windows[0].WindowMinutes != 300 {
		t.Fatalf("windows = %+v, want only the one with a length", windows)
	}
}

// The token usage path must keep working exactly as before: rate limits are an
// addition to that scan, not a replacement for it.
func TestRateLimits_doNotDisturbTokenUsageRecords(t *testing.T) {
	path := writeJSONL(t,
		`{"timestamp":"2026-08-24T09:00:00.000Z","type":"turn_context","payload":{"cwd":"/tmp/p","model":"gpt-5"}}`,
		`{"timestamp":"2026-08-24T09:00:01.000Z","type":"event_msg","payload":{
		  "type":"token_count",
		  "info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":20}},
		  "rate_limits":{"primary":{"used_percent":10,"window_minutes":300}}}}`)

	records, samples, _, _, err := ScanFileWithSamplesContext(t.Context(), path, 0, "s1", Metadata{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	var usage int
	for _, r := range records {
		if r.RecordType == "usage" {
			usage++
		}
	}
	if usage != 1 {
		t.Errorf("%d usage records, want 1", usage)
	}
	if len(samples) != 1 {
		t.Errorf("%d rate-limit samples, want 1", len(samples))
	}
}

// A rate-limit reading is account-scoped, not session-scoped. It must never
// become a session record: toStoreRecord passes any non-empty record type
// straight through, so a reading that arrived as a Record would land in
// session_records with no filter to stop it.
func TestRateLimits_neverBecomeRecords(t *testing.T) {
	path := writeJSONL(t, `{"timestamp":"2026-08-24T09:00:00.000Z","type":"event_msg","payload":{
	  "type":"token_count","info":null,
	  "rate_limits":{"primary":{"used_percent":10,"window_minutes":300}}}}`)

	records, samples, _, _, err := ScanFileWithSamplesContext(t.Context(), path, 0, "s1", Metadata{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("%d records produced by a rate-limit-only reading, want 0: %+v", len(records), records[0])
	}
	if len(samples) != 1 {
		t.Fatalf("%d samples, want 1", len(samples))
	}
}

// The existing entry points keep their signatures, so nothing that only wants
// records has to change or pay for the samples.
func TestScanFileWithMetadata_stillIgnoresRateLimits(t *testing.T) {
	path := writeJSONL(t, `{"timestamp":"2026-08-24T09:00:00.000Z","type":"event_msg","payload":{
	  "type":"token_count","info":null,
	  "rate_limits":{"primary":{"used_percent":10,"window_minutes":300}}}}`)

	records, _, _, err := ScanFileWithMetadata(path, 0, "s1", Metadata{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("%d records, want 0", len(records))
	}
}

// Newer session payloads name the bucket the reading came off. Older ones do
// not, and both shapes are on disk right now. The id is carried because the
// window's length stopped being enough to identify it: the account meters
// several buckets and two of them report a window of the same length.
func TestScanRateLimits_carriesLimitID(t *testing.T) {
	line := `{"timestamp":"2026-08-20T15:55:00.000Z","type":"event_msg","payload":{"type":"token_count",` +
		`"rate_limits":{"limit_id":"codex","limit_name":"GPT-5.3-Codex-Spark","plan_type":"pro",` +
		`"primary":{"used_percent":5,"window_minutes":10080,"resets_at":1787803085},"secondary":null}}}`
	got := scanSamples(t, writeJSONL(t, line))

	if len(got) != 1 {
		t.Fatalf("%d samples, want 1", len(got))
	}
	if got[0].LimitID != "codex" {
		t.Errorf("limit_id = %q, want codex", got[0].LimitID)
	}
	if got[0].LimitName != "GPT-5.3-Codex-Spark" {
		t.Errorf("limit_name = %q", got[0].LimitName)
	}
}

// A payload written before the field existed still parses, with no id.
func TestScanRateLimits_missingLimitIDIsEmpty(t *testing.T) {
	line := `{"timestamp":"2025-12-30T15:59:14.000Z","type":"event_msg","payload":{"type":"token_count",` +
		`"rate_limits":{"primary":{"used_percent":0,"window_minutes":300,"resets_at":1767095957},` +
		`"secondary":{"used_percent":0,"window_minutes":10080,"resets_at":1767682757}}}}`
	got := scanSamples(t, writeJSONL(t, line))

	if len(got) != 1 {
		t.Fatalf("%d samples, want 1", len(got))
	}
	if got[0].LimitID != "" {
		t.Errorf("limit_id = %q, want empty", got[0].LimitID)
	}
}
