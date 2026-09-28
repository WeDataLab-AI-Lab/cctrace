package codexsyncer

import (
	"testing"
	"time"

	"cctrace/internal/codexappserver"
	"cctrace/internal/store"
)

func snapshot(at time.Time, readings ...codexappserver.Reading) *codexappserver.Snapshot {
	return &codexappserver.Snapshot{FetchedAt: at, Readings: readings}
}

func window(limitID, limitName string, minutes int, pct float64) codexappserver.Reading {
	return codexappserver.Reading{
		LimitID:       limitID,
		LimitName:     limitName,
		PlanType:      "pro",
		UsedPercent:   pct,
		WindowMinutes: minutes,
	}
}

// The five-hour window and the weekly one live under different limit ids, and
// two of those ids report a window of the same length. Keyed on length alone
// the two 10080s collide on (provider, account, window_key, sampled_at) and the
// insert's ON CONFLICT DO NOTHING drops one of them without a word.
func TestBuildAppServerQuotaSamples_limitIdSeparatesEqualLengths(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	rows := BuildAppServerQuotaSamples("acct-1", "me@example.test", snapshot(now,
		window("codex", "", 10080, 27),
		window("codex_bengalfox", "GPT-5.3-Codex-Spark", 300, 4),
		window("codex_bengalfox", "GPT-5.3-Codex-Spark", 10080, 1),
	))

	if len(rows) != 3 {
		t.Fatalf("%d rows, want 3", len(rows))
	}
	keys := map[string]*store.QuotaSample{}
	for _, r := range rows {
		if prev, dup := keys[r.WindowKey]; dup {
			t.Fatalf("window_key %q used twice (%v and %v)", r.WindowKey, prev, r)
		}
		keys[r.WindowKey] = r
	}
	for _, want := range []string{"10080", "codex_bengalfox:300", "codex_bengalfox:10080"} {
		if keys[want] == nil {
			t.Errorf("missing window_key %q; got %v", want, mapKeys(keys))
		}
	}
}

// The canonical bucket keeps the bare-length key the session-log path has
// always written. Those two paths read one meter, so a new scheme here would
// fork the account's weekly series into a before and an after with no join
// between them — and the rows already stored would stop being extended.
func TestBuildAppServerQuotaSamples_canonicalBucketKeepsBareLengthKey(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	rows := BuildAppServerQuotaSamples("acct-1", "", snapshot(now, window("codex", "", 10080, 27)))

	if len(rows) != 1 || rows[0].WindowKey != "10080" {
		t.Fatalf("rows = %+v, want one row keyed 10080", rows)
	}
}

// A reading with no limit id at all is the shape the older session payloads
// have, and it is the canonical bucket by construction.
func TestBuildAppServerQuotaSamples_missingLimitIdKeepsBareLengthKey(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	rows := BuildAppServerQuotaSamples("acct-1", "", snapshot(now, window("", "", 300, 4)))

	if len(rows) != 1 || rows[0].WindowKey != "300" {
		t.Fatalf("rows = %+v, want one row keyed 300", rows)
	}
}

// A live read describes the account standing at that instant, so it is
// measured. The session-log path infers across gaps because it reads files
// written before the observation log existed; nothing about this path does.
func TestBuildAppServerQuotaSamples_liveReadingIsObserved(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	snap := snapshot(now, window("codex", "", 10080, 27))
	snap.LoginEmail = "login@example.test"
	rows := BuildAppServerQuotaSamples("acct-1", "reporter@example.test", snap)

	r := rows[0]
	if r.Attribution != store.AttributionObserved {
		t.Errorf("attribution = %q, want observed", r.Attribution)
	}
	if r.BillingProvider != billingProviderCodex {
		t.Errorf("provider = %q", r.BillingProvider)
	}
	if r.AccountID != "acct-1" || r.ProfileEmail != "reporter@example.test" {
		t.Errorf("identity = %q/%q", r.AccountID, r.ProfileEmail)
	}
	if r.LoginEmail != "login@example.test" {
		t.Errorf("login_email = %q, want the app-server account email", r.LoginEmail)
	}
	if !r.SampledAt.Equal(now) {
		t.Errorf("sampled_at = %v, want %v", r.SampledAt, now)
	}
	if r.WindowMinutes == nil || *r.WindowMinutes != 10080 {
		t.Errorf("window_minutes = %v", r.WindowMinutes)
	}
	if r.UsedPct != 27 {
		t.Errorf("used_pct = %v", r.UsedPct)
	}
}

// codex_bengalfox means nothing on screen; the name the server gives it does.
func TestBuildAppServerQuotaSamples_carriesLimitNameAsScopeLabel(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	rows := BuildAppServerQuotaSamples("acct-1", "",
		snapshot(now, window("codex_bengalfox", "GPT-5.3-Codex-Spark", 300, 4)))

	if rows[0].ScopeLabel != "GPT-5.3-Codex-Spark" {
		t.Errorf("scope_label = %q", rows[0].ScopeLabel)
	}
}

// An unattributable reading is dropped rather than credited to whichever
// account happens to be current, which would put it on another account's line.
func TestBuildAppServerQuotaSamples_dropsUnattributableReading(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	if rows := BuildAppServerQuotaSamples("", "", snapshot(now, window("codex", "", 10080, 27))); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// A snapshot with no fetch instant has no place on a time axis.
func TestBuildAppServerQuotaSamples_dropsSnapshotWithoutInstant(t *testing.T) {
	if rows := BuildAppServerQuotaSamples("acct-1", "", snapshot(time.Time{}, window("codex", "", 10080, 27))); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// Two windows of the same length under the same limit id have not been
// observed, but the schema permits them. Letting both take one key would hand
// the collision to the database, which resolves it by discarding a row.
func TestBuildAppServerQuotaSamples_disambiguatesDuplicateKeysWithinABucket(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	rows := BuildAppServerQuotaSamples("acct-1", "", snapshot(now,
		window("codex_bengalfox", "", 300, 4),
		window("codex_bengalfox", "", 300, 9),
	))

	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	if rows[0].WindowKey == rows[1].WindowKey {
		t.Fatalf("both rows keyed %q", rows[0].WindowKey)
	}
}

func mapKeys(m map[string]*store.QuotaSample) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
