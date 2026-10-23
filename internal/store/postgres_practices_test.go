package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func practiceEvent(ts time.Time, userID, eventName string, attrs map[string]interface{}) *OtelEvent {
	email := ""
	if userID != "" {
		email = userID + "@example.com"
	}
	return &OtelEvent{
		Ts: ts, EventName: eventName, SessionID: "session-of-" + userID, UserID: userID,
		ProfileEmail: email, LoginEmail: email, Agent: "claude", BillingProvider: "anthropic", Attrs: attrs,
	}
}

// practiceDecisions is one person's Claude tool decisions, one event per source.
func practiceDecisions(ts time.Time, userID string, sources ...string) []*OtelEvent {
	out := make([]*OtelEvent, 0, len(sources))
	for i, source := range sources {
		e := practiceEvent(ts.Add(time.Duration(i)*time.Second), userID, "tool_decision", map[string]interface{}{"source": source})
		e.ToolName, e.ToolDecision = "Bash", "accept"
		out = append(out, e)
	}
	return out
}

func repeatSource(source string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = source
	}
	return out
}

func practiceRow(t *testing.T, got *Practices, kind, name string) PracticeRow {
	t.Helper()
	for _, row := range got.Rows {
		if row.Kind == kind && row.Name == name {
			return row
		}
	}
	t.Fatalf("no %s row %q in %+v", kind, name, got.Rows)
	return PracticeRow{}
}

// Each group's value is the median of per-person rates. Pooling decisions, or
// joining every membership event to every decision, would let one heavy user
// set the number; person-one's five plugin_loaded events are there to catch that.
func TestPgStore_PracticesComparesPerPersonMedians(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	since := recentDay(3)
	until := since.Add(24 * time.Hour)
	at := since.Add(time.Hour)

	var events []*OtelEvent
	// person-one: 10 decisions -> manual 100, rejected 100
	events = append(events, practiceDecisions(at, "person-one", append([]string{"user_temporary", "user_reject"}, repeatSource("config", 8)...)...)...)
	// person-two: 5 decisions -> manual 200 (hook is not a person), rejected 0
	events = append(events, practiceDecisions(at, "person-two", "user_permanent", "hook", "config", "config", "config")...)
	// person-three: 10 decisions -> manual 900, rejected 100
	events = append(events, practiceDecisions(at, "person-three", append(repeatSource("user_temporary", 9), "user_abort")...)...)
	// person-four: 4 decisions -> manual 0, rejected 250
	events = append(events, practiceDecisions(at, "person-four", "user_reject", "config", "config", "config")...)
	// person-five: 2 decisions -> manual 500, rejected 0
	events = append(events, practiceDecisions(at, "person-five", "user_temporary", "config")...)
	// person-six: 1 decision -> manual 1000, rejected 0
	events = append(events, practiceDecisions(at, "person-six", "user_temporary")...)
	// person-seven makes no decisions: a practice user, never a metric contributor.

	for i := 0; i < 5; i++ {
		events = append(events, practiceEvent(at.Add(time.Duration(i)*time.Minute), "person-one", "plugin_loaded", map[string]interface{}{"plugin.name": "alpha"}))
	}
	for _, p := range []string{"person-two", "person-three"} {
		events = append(events, practiceEvent(at, p, "plugin_loaded", map[string]interface{}{"plugin.name": "alpha"}))
	}
	// gamma has three users but only two contributors.
	for _, p := range []string{"person-one", "person-two", "person-seven"} {
		events = append(events, practiceEvent(at, p, "plugin_loaded", map[string]interface{}{"plugin.name": "gamma"}))
	}
	for _, p := range []string{"person-one", "person-two"} {
		events = append(events, practiceEvent(at, p, "skill_activated", map[string]interface{}{"skill.name": "beta"}))
	}
	events = append(events,
		practiceEvent(at, "person-four", "hook_registered", map[string]interface{}{
			"hook_event": "PreToolUse", "hook_source": "pluginHook", "hook_type": "command", "plugin.name": "alpha",
		}),
		practiceEvent(at, "person-five", "hook_registered", map[string]interface{}{
			"hook_event": "Stop", "hook_source": "userSettings", "hook_type": "command",
		}),
	)
	if err := s.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := s.Practices(ctx, since, until, "person-three")
	if err != nil {
		t.Fatalf("Practices: %v", err)
	}
	if !got.WindowStart.Equal(since) || !got.WindowEnd.Equal(until) {
		t.Fatalf("window = %v..%v, want %v..%v", got.WindowStart, got.WindowEnd, since, until)
	}

	var order []string
	for _, row := range got.Rows {
		order = append(order, row.Kind+":"+row.Name)
	}
	want := "hook:PreToolUse/pluginHook/command/alpha,hook:Stop/userSettings/command,plugin:alpha,plugin:gamma,skill:beta"
	if strings.Join(order, ",") != want {
		t.Fatalf("rows = %v, want %s", order, want)
	}

	alpha := practiceRow(t, got, "plugin", "alpha")
	if alpha.UsersWith != 3 || alpha.UsersWithout == nil || *alpha.UsersWithout != 4 {
		t.Fatalf("alpha users = %d with, %v without; want 3 and 4 (membership counted once per person)", alpha.UsersWith, alpha.UsersWithout)
	}
	if alpha.Metrics == nil {
		t.Fatalf("alpha has three contributors on each side but no metrics: %+v", alpha)
	}
	if m := alpha.Metrics.ManualApprovalsPer1000; m.With != 200 || m.Without != 500 {
		t.Fatalf("alpha manual approvals = %+v, want with 200 (per-person median), without 500", m)
	}
	if m := alpha.Metrics.HumanRejectionsPer1000; m.With != 100 || m.Without != 0 {
		t.Fatalf("alpha human rejections = %+v, want with 100, without 0", m)
	}

	for _, name := range []string{"gamma", "beta"} {
		kind := "plugin"
		if name == "beta" {
			kind = "skill"
		}
		row := practiceRow(t, got, kind, name)
		if row.Metrics != nil || row.UsersWithout != nil {
			t.Fatalf("%s has fewer than three contributors with the practice but shows %+v", name, row)
		}
	}
	if gamma := practiceRow(t, got, "plugin", "gamma"); gamma.UsersWith != 3 {
		t.Fatalf("gamma users_with = %d, want 3 (a practice user need not be a contributor)", gamma.UsersWith)
	}

	if got.Self.ManualApprovalsPer1000 == nil || *got.Self.ManualApprovalsPer1000 != 900 ||
		got.Self.HumanRejectionsPer1000 == nil || *got.Self.HumanRejectionsPer1000 != 100 {
		t.Fatalf("self = %+v, want person-three's own 900 and 100", got.Self)
	}
	if len(got.Caveats) == 0 {
		t.Fatal("caveats missing")
	}

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"person-", "@example.com", "session-of-"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("response contains %q: %s", forbidden, body)
		}
	}

	noDecisions, err := s.Practices(ctx, since, until, "person-seven")
	if err != nil {
		t.Fatalf("Practices(person-seven): %v", err)
	}
	if noDecisions.Self.ManualApprovalsPer1000 != nil || noDecisions.Self.HumanRejectionsPer1000 != nil {
		t.Fatalf("self for a caller without decisions = %+v, want no rates", noDecisions.Self)
	}
}

// Excluded accounts and events with no user_id belong to no one in the
// comparison, and the window is [since, until).
func TestPgStore_PracticesCountsOnlyVisibleAttributedEventsInWindow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	since := recentDay(3)
	until := since.Add(24 * time.Hour)
	at := since.Add(time.Hour)
	alpha := map[string]interface{}{"plugin.name": "alpha"}

	events := []*OtelEvent{
		practiceEvent(since, "person-one", "plugin_loaded", alpha),
		practiceEvent(since.Add(-time.Second), "person-one", "plugin_loaded", map[string]interface{}{"plugin.name": "early"}),
		practiceEvent(until, "person-one", "plugin_loaded", map[string]interface{}{"plugin.name": "late"}),
		practiceEvent(at, "person-hidden", "plugin_loaded", alpha),
		practiceEvent(at, "", "plugin_loaded", alpha),
		practiceEvent(at, "", "plugin_loaded", map[string]interface{}{"plugin.name": "orphan"}),
	}
	events = append(events, practiceDecisions(since, "person-one", "user_temporary", "config")...)
	events = append(events, practiceDecisions(since.Add(-time.Minute), "person-one", "user_reject")...)
	events = append(events, practiceDecisions(until, "person-one", "user_reject")...)
	events = append(events, practiceDecisions(at, "person-hidden", "user_temporary")...)
	events = append(events, practiceDecisions(at, "", "user_temporary")...)
	if err := s.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if _, err := s.ExcludeAccount(ctx, "person-hidden@example.com", "test", "test"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}

	got, err := s.Practices(ctx, since, until, "person-one")
	if err != nil {
		t.Fatalf("Practices: %v", err)
	}
	if len(got.Rows) != 1 {
		t.Fatalf("rows = %+v, want only alpha", got.Rows)
	}
	row := got.Rows[0]
	if row.Kind != "plugin" || row.Name != "alpha" || row.UsersWith != 1 {
		t.Fatalf("row = %+v, want plugin alpha used by person-one only", row)
	}
	if got.Self.ManualApprovalsPer1000 == nil || *got.Self.ManualApprovalsPer1000 != 500 ||
		got.Self.HumanRejectionsPer1000 == nil || *got.Self.HumanRejectionsPer1000 != 0 {
		t.Fatalf("self = %+v, want 500 and 0 from the two decisions inside the window", got.Self)
	}

	hidden, err := s.Practices(ctx, since, until, "person-hidden")
	if err != nil {
		t.Fatalf("Practices(person-hidden): %v", err)
	}
	if hidden.Self.ManualApprovalsPer1000 != nil {
		t.Fatalf("an excluded account still has rates: %+v", hidden.Self)
	}
}
