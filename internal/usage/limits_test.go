package usage

import "testing"

// The usage API moved from fixed top-level windows to a typed limits[] array.
// Codename fields (nimbus_quill, tangelo, amber_ladder) keep appearing and
// disappearing beside them, so reading by field name cannot hold: the poller
// still asks for five_hour / seven_day / seven_day_sonnet and two of those are
// now null or gone.
const limitsBody = `{
  "five_hour":        {"utilization": 42},
  "seven_day":        {"utilization": 61, "resets_at": "2026-08-20T09:00:00Z"},
  "seven_day_sonnet": null,
  "seven_day_opus":   null,
  "limits": [
    {"kind":"session",       "percent": 42, "severity":"normal",   "resets_at":"2026-08-13T18:00:00Z", "is_active": true},
    {"kind":"weekly_all",    "percent": 61, "severity":"normal",   "resets_at":"2026-08-20T09:00:00Z", "is_active": true},
    {"kind":"weekly_scoped", "percent": 88, "severity":"critical", "resets_at":"2026-08-20T09:00:00Z", "is_active": true,
     "scope": {"model": {"display_name":"Fable"}}}
  ]
}`

// limits[] carries what the legacy fields lost: five_hour has no resets_at in
// the current response, but kind=session does.
func TestWindowFromLimits(t *testing.T) {
	r := decodeResponse(t, limitsBody)

	w := r.Window(KindSession)
	if w == nil {
		t.Fatal("Window(session) = nil")
	}
	if w.Utilization != 42 {
		t.Errorf("session utilization = %v, want 42", w.Utilization)
	}
	if w.ResetsAt != "2026-08-13T18:00:00Z" {
		t.Errorf("session resets_at = %q, want the value only limits[] carries", w.ResetsAt)
	}
	if w.Severity != "normal" {
		t.Errorf("session severity = %q, want normal", w.Severity)
	}
}

// severity comes from the server, so the chart does not have to invent a
// threshold of its own.
func TestWindowCarriesSeverityAndScope(t *testing.T) {
	r := decodeResponse(t, limitsBody)

	w := r.Window(KindWeeklyScoped)
	if w == nil {
		t.Fatal("Window(weekly_scoped) = nil")
	}
	if w.Severity != "critical" {
		t.Errorf("severity = %q, want critical", w.Severity)
	}
	// Deliberately not stored as seven_day_sonnet_pct: the scoped model in the
	// observed response is "Fable", so that column name would be a lie.
	if w.ScopeLabel != "Fable" {
		t.Errorf("scope label = %q, want Fable", w.ScopeLabel)
	}
}

// limits[] is not guaranteed on every account, plan, or server version, so the
// legacy fields stay as the floor. The worst case must equal today's behaviour.
func TestWindowFallsBackToLegacyFields(t *testing.T) {
	r := decodeResponse(t, `{
	  "five_hour": {"utilization": 42, "resets_at": "2026-08-13T18:00:00Z"},
	  "seven_day": {"utilization": 61, "resets_at": "2026-08-20T09:00:00Z"}
	}`)

	w := r.Window(KindSession)
	if w == nil {
		t.Fatal("Window(session) = nil without limits[]")
	}
	if w.Utilization != 42 || w.ResetsAt != "2026-08-13T18:00:00Z" {
		t.Errorf("legacy fallback = %+v, want the five_hour values", w)
	}
	if w := r.Window(KindWeeklyAll); w == nil || w.Utilization != 61 {
		t.Errorf("Window(weekly_all) = %+v, want the seven_day values", w)
	}
}

// When both are present limits[] wins: it is the typed source, and the legacy
// fields are the ones going stale.
func TestWindowPrefersLimitsOverLegacy(t *testing.T) {
	r := decodeResponse(t, `{
	  "five_hour": {"utilization": 1, "resets_at": "2000-01-01T00:00:00Z"},
	  "limits": [{"kind":"session","percent":42,"resets_at":"2026-08-13T18:00:00Z"}]
	}`)

	w := r.Window(KindSession)
	if w == nil {
		t.Fatal("Window(session) = nil")
	}
	if w.Utilization != 42 {
		t.Errorf("utilization = %v, want 42 from limits[]", w.Utilization)
	}
}

// A kind nobody reported is absent, not zero. Zero would be charted as "the
// window is empty" when the truth is "we did not measure it".
func TestWindowUnknownKindIsNil(t *testing.T) {
	r := decodeResponse(t, limitsBody)
	if w := r.Window("weekly_opus"); w != nil {
		t.Errorf("Window(weekly_opus) = %+v, want nil", w)
	}
}

// is_active is not read as "this window is in force". The live response on the
// development machine returned weekly_all at 28% consumed with is_active=false,
// and a window that is being spent is plainly in force — so the flag means
// something narrower than it appears.
//
// Skipping on it dropped weekly_scoped entirely. weekly_all survived only
// because the legacy seven_day field caught it on the way down, which is what
// hid the bug: the one window with no legacy fallback was the one that vanished.
func TestWindowKeepsInactiveLimits(t *testing.T) {
	r := decodeResponse(t, `{
	  "limits": [{"kind":"weekly_scoped","percent":28,"is_active":false,
	              "scope":{"model":{"display_name":"Fable"}}}]
	}`)

	w := r.Window(KindWeeklyScoped)
	if w == nil {
		t.Fatal("Window(weekly_scoped) = nil; an inactive-flagged window with no legacy field disappears")
	}
	if w.Utilization != 28 {
		t.Errorf("utilization = %v, want 28", w.Utilization)
	}
	if w.IsActive == nil || *w.IsActive {
		t.Error("is_active was not carried through for the caller to judge")
	}
}

// is_active absent means the server did not say; that is not the same as
// saying no, so the entry counts.
func TestWindowKeepsLimitsWithoutIsActive(t *testing.T) {
	r := decodeResponse(t, `{
	  "limits": [{"kind":"session","percent":42}]
	}`)
	if w := r.Window(KindSession); w == nil {
		t.Error("Window(session) = nil, want the entry kept when is_active is absent")
	}
}
