package store

import (
	"context"
	"testing"
	"time"
)

// TestAccountSwitchStats distinguishes the two things the audit log conflated:
// a user who switched accounts *within* a session (the case that breaks
// per-session attribution) and a user who simply used two accounts in separate
// sessions.
func TestAccountSwitchStats(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		// u1: one session holding two accounts -> a real mid-session switch.
		{Ts: ts, EventName: "api_request", UserID: "u1", SessionID: "s1", LoginEmail: "one@example.com"},
		{Ts: ts.Add(time.Hour), EventName: "api_request", UserID: "u1", SessionID: "s1", LoginEmail: "two@example.com"},
		// u2: two accounts but never inside the same session.
		{Ts: ts, EventName: "api_request", UserID: "u2", SessionID: "s2", LoginEmail: "one@example.com"},
		{Ts: ts, EventName: "api_request", UserID: "u2", SessionID: "s3", LoginEmail: "two@example.com"},
		// u3: single account throughout -> must not be reported at all.
		{Ts: ts, EventName: "api_request", UserID: "u3", SessionID: "s4", LoginEmail: "one@example.com"},
		{Ts: ts, EventName: "api_request", UserID: "u3", SessionID: "s5", LoginEmail: "one@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := s.AccountSwitchStats(ctx, time.Time{})
	if err != nil {
		t.Fatalf("AccountSwitchStats: %v", err)
	}

	byUser := map[string]*AccountSwitchStat{}
	for _, st := range got {
		byUser[st.UserID] = st
	}

	if _, ok := byUser["u3"]; ok {
		t.Error("u3 uses one account throughout and must not be reported")
	}

	u1 := byUser["u1"]
	if u1 == nil {
		t.Fatal("u1 missing: a mid-session switch is the case this exists to surface")
	}
	if u1.MultiAcctSess != 1 {
		t.Errorf("u1 multi-account sessions = %d, want 1", u1.MultiAcctSess)
	}
	if len(u1.LoginEmails) != 2 {
		t.Errorf("u1 login emails = %v, want 2", u1.LoginEmails)
	}
	if u1.LastSeenSwitch == nil {
		t.Error("u1 last_seen_switch is nil, want the switching session's last ts")
	}

	u2 := byUser["u2"]
	if u2 == nil {
		t.Fatal("u2 missing: two accounts across sessions is still worth reporting")
	}
	if u2.MultiAcctSess != 0 {
		t.Errorf("u2 multi-account sessions = %d, want 0 (never switched inside a session)", u2.MultiAcctSess)
	}
	if u2.TotalSessions != 2 {
		t.Errorf("u2 total sessions = %d, want 2", u2.TotalSessions)
	}
	if u2.LastSeenSwitch != nil {
		t.Errorf("u2 last_seen_switch = %v, want nil (no mid-session switch)", *u2.LastSeenSwitch)
	}
}

// Excluded accounts are presentation-only elsewhere, and this report reads
// visible_events for the same reason: an admin who hid an account should not
// keep seeing it here.
func TestAccountSwitchStatsRespectsExclusion(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", UserID: "u1", SessionID: "s1", LoginEmail: "one@example.com"},
		{Ts: ts.Add(time.Hour), EventName: "api_request", UserID: "u1", SessionID: "s1", LoginEmail: "two@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO excluded_accounts (login_email) VALUES ($1)`, "two@example.com"); err != nil {
		t.Fatalf("exclude: %v", err)
	}

	got, err := s.AccountSwitchStats(ctx, time.Time{})
	if err != nil {
		t.Fatalf("AccountSwitchStats: %v", err)
	}
	for _, st := range got {
		for _, e := range st.LoginEmails {
			if e == "two@example.com" {
				t.Fatalf("excluded account still reported for %s", st.UserID)
			}
		}
	}
}
