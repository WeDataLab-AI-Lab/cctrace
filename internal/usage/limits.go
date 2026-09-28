package usage

// The usage API describes rate limits two ways at once. The older shape is a
// fixed set of top-level windows (five_hour, seven_day, seven_day_sonnet,
// seven_day_opus); the newer one is a typed limits[] array.
//
// The fixed shape is decaying in place. seven_day_sonnet and seven_day_opus now
// come back null, and five_hour lost its resets_at — so half of what the poller
// stores is already blank. Codename fields (nimbus_quill, tangelo,
// amber_ladder, omelette_promotional) keep appearing and disappearing beside
// them, which is what makes reading by field name structurally unable to hold.
//
// limits[] is read first and the fixed fields remain as the floor. It is a
// priority, not a replacement: limits[] cannot be guaranteed on every account,
// plan, and server version, so the worst case has to equal today's behaviour.
// There is no path here that gets worse.

// Limit kinds observed in the response.
const (
	KindSession      = "session"       // the 5-hour window
	KindWeeklyAll    = "weekly_all"    // the 7-day window, all models
	KindWeeklyScoped = "weekly_scoped" // the 7-day window for one model
)

// Limit is one entry of limits[].
type Limit struct {
	Kind     string  `json:"kind"`
	Group    string  `json:"group"`
	Percent  float64 `json:"percent"`
	Severity string  `json:"severity"` // normal | critical — set by the server
	ResetsAt string  `json:"resets_at"`
	// IsActive is a pointer so "absent" stays distinct from "false". The server
	// not saying is not the server saying no.
	IsActive *bool  `json:"is_active"`
	Scope    *Scope `json:"scope"`
}

// Scope narrows a limit to one model.
type Scope struct {
	Model *struct {
		DisplayName string `json:"display_name"`
	} `json:"model"`
}

// Spend is the money block. It sits beside extra_usage.credits_ever_enabled,
// which suggests it reports overage credit spend rather than the subscription
// fee — so it is carried through but is not the denominator of the weighted
// average. Confirming that needs an account with credits enabled.
type Spend struct {
	Used *struct {
		AmountMinor int64  `json:"amount_minor"`
		Currency    string `json:"currency"`
		Exponent    int    `json:"exponent"`
	} `json:"used"`
	Percent  *float64 `json:"percent"`
	Severity string   `json:"severity"`
}

// Window returns the live window for kind, or nil when nothing reported it.
//
// nil rather than a zero window on purpose: zero charts as "this window is
// empty", and the truth is "we did not measure it". Those are different claims
// and the chart has to be able to tell them apart.
func (r *Response) Window(kind string) *UsageWindow {
	if r == nil {
		return nil
	}
	for i := range r.Limits {
		l := &r.Limits[i]
		if l.Kind != kind {
			continue
		}
		// is_active is NOT read as "this window is in force". The live response
		// on the development machine returned weekly_all at 28% consumed with
		// is_active=false, and weekly_scoped likewise — a window that is being
		// spent is plainly in force, so the flag means something narrower than
		// it appears (which of the windows is currently the binding one).
		//
		// Skipping on it dropped weekly_scoped entirely: weekly_all survived
		// only because the legacy seven_day field caught it on the way down, and
		// weekly_scoped has no legacy field to fall back to. The flag is carried
		// through to the sample instead, so a consumer can act on it knowingly.
		_ = l.IsActive
		w := &UsageWindow{
			Utilization: l.Percent,
			ResetsAt:    l.ResetsAt,
			Severity:    l.Severity,
			IsActive:    l.IsActive,
		}
		if l.Scope != nil && l.Scope.Model != nil {
			w.ScopeLabel = l.Scope.Model.DisplayName
		}
		return w
	}
	return r.legacyWindow(kind)
}

// legacyWindow maps a kind onto the fixed top-level fields.
//
// seven_day_sonnet is deliberately not mapped. The API returns null for it, and
// the closest live entry is kind=weekly_scoped — whose scope.model.display_name
// in the observed response was "Fable". Feeding that to a field named after
// Sonnet would make the name a lie, so the scoped window is exposed under its
// own kind with ScopeLabel attached and the Sonnet column is left empty.
func (r *Response) legacyWindow(kind string) *UsageWindow {
	switch kind {
	case KindSession:
		return r.FiveHour
	case KindWeeklyAll:
		return r.SevenDay
	}
	return nil
}
