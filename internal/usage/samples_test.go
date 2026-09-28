package usage

import (
	"testing"
	"time"

	"cctrace/internal/store"
)

func byWindow(samples []*store.QuotaSample) map[string]*store.QuotaSample {
	m := map[string]*store.QuotaSample{}
	for _, s := range samples {
		m[s.WindowKey] = s
	}
	return m
}

func TestSamples_oneRowPerReportedWindow(t *testing.T) {
	r := decodeResponse(t, limitsBody)
	r.FetchedAt = time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	r.Plan = "max"

	got := r.Samples("acct-1", "someone@example.test", "profile@example.test")
	if len(got) != 3 {
		t.Fatalf("%d samples, want 3 (session, weekly_all, weekly_scoped)", len(got))
	}

	m := byWindow(got)
	if m[KindSession] == nil || m[KindSession].UsedPct != 42 {
		t.Errorf("session = %+v", m[KindSession])
	}
	if m[KindSession].WindowMinutes == nil || *m[KindSession].WindowMinutes != 300 {
		t.Errorf("session window minutes = %v, want 300", m[KindSession].WindowMinutes)
	}
	if m[KindWeeklyAll].WindowMinutes == nil || *m[KindWeeklyAll].WindowMinutes != 10080 {
		t.Errorf("weekly window minutes = %v, want 10080", m[KindWeeklyAll].WindowMinutes)
	}
	if m[KindWeeklyScoped].ScopeLabel != "Fable" {
		t.Errorf("scope label = %q, want Fable", m[KindWeeklyScoped].ScopeLabel)
	}
	if m[KindWeeklyScoped].Severity != "critical" {
		t.Errorf("severity = %q, want critical (the server's own call)", m[KindWeeklyScoped].Severity)
	}
}

// sampled_at is the moment the reading came off the wire, unrounded. Bucketing
// would throw away exactly the resolution that several reporters buy, and the
// identical value on a re-offered cached response is what lets the key drop the
// duplicate.
func TestSamples_useTheFetchInstantUnrounded(t *testing.T) {
	r := decodeResponse(t, limitsBody)
	r.FetchedAt = time.Date(2026, 8, 24, 9, 3, 47, 123456789, time.UTC)

	got := r.Samples("acct-1", "", "")
	for _, s := range got {
		if !s.SampledAt.Equal(r.FetchedAt) {
			t.Fatalf("sampled_at = %v, want the exact fetch instant %v", s.SampledAt, r.FetchedAt)
		}
	}
}

// Without a fetch instant there is no point on the time axis to place the
// reading at, and inventing one with time.Now() would attribute it to whenever
// the code happened to run.
func TestSamples_noFetchInstantYieldsNothing(t *testing.T) {
	r := decodeResponse(t, limitsBody)
	if got := r.Samples("acct-1", "", ""); got != nil {
		t.Fatalf("got %d samples without a FetchedAt, want none", len(got))
	}
}

// limits[] gains and loses entries between server versions. A window this build
// has no constant for still burned down, so dropping it would quietly narrow
// what the chart can see.
func TestSamples_keepsUnknownWindowKinds(t *testing.T) {
	r := decodeResponse(t, `{"limits":[
	  {"kind":"session","percent":10},
	  {"kind":"weekly_cowork","percent":55,"resets_at":"2026-08-25T00:00:00Z"}
	]}`)
	r.FetchedAt = time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	m := byWindow(r.Samples("acct-1", "", ""))
	got := m["weekly_cowork"]
	if got == nil {
		t.Fatal("weekly_cowork was dropped")
	}
	if got.UsedPct != 55 {
		t.Errorf("used_pct = %v, want 55", got.UsedPct)
	}
	// Its length is unknown rather than guessed at, so the chart can exclude it
	// from a window-scoped query instead of being told a wrong number.
	if got.WindowMinutes != nil {
		t.Errorf("window minutes = %v, want nil for an unrecognised window", *got.WindowMinutes)
	}
}

// The legacy fields stay as the floor here too: a response with no limits[]
// must still produce history.
func TestSamples_fallBackToLegacyFields(t *testing.T) {
	r := decodeResponse(t, `{
	  "five_hour": {"utilization": 42, "resets_at": "2026-08-24T14:00:00Z"},
	  "seven_day": {"utilization": 61}
	}`)
	r.FetchedAt = time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	m := byWindow(r.Samples("acct-1", "", ""))
	if len(m) != 2 {
		t.Fatalf("%d windows without limits[], want 2", len(m))
	}
	if m[KindSession].ResetsAt == nil {
		t.Error("session resets_at was not parsed")
	}
	if m[KindWeeklyAll].ResetsAt != nil {
		t.Error("weekly resets_at invented a value the response did not carry")
	}
}

// A window reported by both limits[] and the legacy fields is one window.
func TestSamples_doesNotDuplicateAWindow(t *testing.T) {
	r := decodeResponse(t, `{
	  "five_hour": {"utilization": 1},
	  "limits": [{"kind":"session","percent":42}]
	}`)
	r.FetchedAt = time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	got := r.Samples("acct-1", "", "")
	if len(got) != 1 {
		t.Fatalf("%d samples, want 1", len(got))
	}
	if got[0].UsedPct != 42 {
		t.Errorf("used_pct = %v, want 42 from limits[]", got[0].UsedPct)
	}
}

// Live polling reads the account at the instant it reports it, so nothing is
// being inferred across a gap.
func TestSamples_areMarkedObserved(t *testing.T) {
	r := decodeResponse(t, limitsBody)
	r.FetchedAt = time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	for _, s := range r.Samples("acct-1", "", "") {
		if s.Attribution != store.AttributionObserved {
			t.Fatalf("attribution = %q, want observed", s.Attribution)
		}
	}
}

// A Max 5x and a Max 20x account both report subscriptionType "max", and they
// are different subscriptions at different prices. The finer identifier is what
// the price lookup is keyed on, so it has to win.
func TestPlanOf_prefersTheRateLimitTier(t *testing.T) {
	got := planOf(Credentials{SubscriptionType: "max", RateLimitTier: "default_claude_max_20x"})
	if got != "default_claude_max_20x" {
		t.Errorf("plan = %q, want the tier", got)
	}
}

// Anything that reports no tier keeps the previous behaviour rather than
// landing with an empty plan and becoming unpriceable.
func TestPlanOf_fallsBackToSubscriptionType(t *testing.T) {
	if got := planOf(Credentials{SubscriptionType: "max"}); got != "max" {
		t.Errorf("plan = %q, want max", got)
	}
	if got := planOf(Credentials{}); got != "" {
		t.Errorf("plan = %q, want empty", got)
	}
}
