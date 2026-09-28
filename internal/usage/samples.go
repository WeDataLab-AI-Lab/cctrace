package usage

import (
	"time"

	"cctrace/internal/store"
)

// BillingProviderAnthropic is the canonical provider name for Claude
// subscription burn. It matches the vocabulary the ingest paths already
// normalise to, so the chart's left axis and the existing right axis agree on
// what an "anthropic" row means.
const BillingProviderAnthropic = "anthropic"

// windowMinutes maps a limit kind onto the length of its window. Codex reports
// this number directly; Anthropic names the window instead, so the two are
// reconciled here and the history table can hold one row shape for both.
var windowMinutes = map[string]int{
	KindSession:      300,
	KindWeeklyAll:    10080,
	KindWeeklyScoped: 10080,
}

// Samples turns one usage response into history rows.
//
// Every reported window becomes a row, including kinds this build has no
// constant for: limits[] gains and loses entries between server versions, and a
// window nobody anticipated is still a window that burned down. Dropping it
// because the name is unfamiliar would silently narrow what the chart can see.
//
// accountID and loginEmail come from the caller because the response carries no
// identity of its own — who it describes is implicit in which token asked. A
// row with no accountID is still returned; the store drops it, so the decision
// to discard an unattributable reading is made in one place rather than two.
func (r *Response) Samples(accountID, loginEmail, profileEmail string) []*store.QuotaSample {
	if r == nil {
		return nil
	}
	sampledAt := r.FetchedAt
	if sampledAt.IsZero() {
		return nil
	}

	seen := map[string]bool{}
	var out []*store.QuotaSample

	add := func(key string, w *UsageWindow) {
		if w == nil || seen[key] {
			return
		}
		seen[key] = true
		s := &store.QuotaSample{
			BillingProvider: BillingProviderAnthropic,
			AccountID:       accountID,
			WindowKey:       key,
			SampledAt:       sampledAt,
			UsedPct:         w.Utilization,
			Severity:        w.Severity,
			ScopeLabel:      w.ScopeLabel,
			Plan:            r.Plan,
			LoginEmail:      loginEmail,
			ProfileEmail:    profileEmail,
			Attribution:     store.AttributionObserved,
		}
		if m, ok := windowMinutes[key]; ok {
			s.WindowMinutes = &m
		}
		if t, err := time.Parse(time.RFC3339Nano, w.ResetsAt); err == nil && !t.IsZero() {
			s.ResetsAt = &t
		}
		out = append(out, s)
	}

	for i := range r.Limits {
		l := &r.Limits[i]
		if l.Kind == "" {
			continue
		}
		add(l.Kind, r.Window(l.Kind))
	}
	// The fixed fields are the floor, for the same reason Window() reads them:
	// limits[] cannot be assumed present on every account, plan and server
	// version. add() ignores a key limits[] already supplied.
	add(KindSession, r.Window(KindSession))
	add(KindWeeklyAll, r.Window(KindWeeklyAll))

	return out
}
