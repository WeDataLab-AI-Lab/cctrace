package codexsyncer

import (
	"strings"
	"testing"
	"time"

	"cctrace/internal/codexappserver"
	"cctrace/internal/codexlog"
	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

func stateObserving(home, account string, at time.Time) *syncer.State {
	s := &syncer.State{}
	s.ObserveCodexAccount(at, home, account)
	return s
}

func reading(ts time.Time, plan string, windows ...int) codexlog.RateLimitSample {
	s := codexlog.RateLimitSample{Timestamp: ts, PlanType: plan}
	for _, m := range windows {
		s.Windows = append(s.Windows, codexlog.RateLimitWindow{UsedPercent: float64(m % 100), WindowMinutes: m})
	}
	return s
}

// The window's length is its identity, so window_key is that number. Codex does
// not fix which length lands in primary versus secondary, and filing by
// position would put weekly percentages on the five-hour line.
func TestBuildQuotaSamples_keysWindowsByLength(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	st := stateObserving("/home/user/.codex", "acct-1", now)

	rows := BuildQuotaSamples(st, "/home/user/.codex", "me@example.test", "",
		[]codexlog.RateLimitSample{reading(now, "pro", 300, 10080)})

	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	keys := map[string]bool{}
	for _, r := range rows {
		keys[r.WindowKey] = true
		if r.BillingProvider != billingProviderCodex {
			t.Errorf("provider = %q, want %q", r.BillingProvider, billingProviderCodex)
		}
		if r.Plan != "pro" {
			t.Errorf("plan = %q, want pro", r.Plan)
		}
	}
	if !keys["300"] || !keys["10080"] {
		t.Errorf("window keys = %v, want 300 and 10080", keys)
	}
	for _, r := range rows {
		if r.LoginEmail != "" {
			t.Errorf("login_email = %q, want empty when the session payload does not know it", r.LoginEmail)
		}
	}
}

// A reading taken while the account was standing is measured, not inferred.
func TestBuildQuotaSamples_liveReadingIsObserved(t *testing.T) {
	obsAt := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	st := stateObserving("/home/user/.codex", "acct-1", obsAt)

	rows := BuildQuotaSamples(st, "/home/user/.codex", "", "", []codexlog.RateLimitSample{
		reading(obsAt.Add(time.Minute), "pro", 300),
	})
	if len(rows) != 1 || rows[0].Attribution != store.AttributionObserved {
		t.Fatalf("rows = %+v, want one observed row", rows)
	}
}

// A backfilled reading predates the observation log entirely. It still gets an
// account — otherwise the backfill inserts nothing at all — but it is marked so
// the chart can draw it differently.
func TestBuildQuotaSamples_backfilledReadingIsInferred(t *testing.T) {
	obsAt := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	st := stateObserving("/home/user/.codex", "acct-1", obsAt)

	rows := BuildQuotaSamples(st, "/home/user/.codex", "", "", []codexlog.RateLimitSample{
		reading(obsAt.Add(-30*24*time.Hour), "pro", 300),
	})
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1", len(rows))
	}
	if rows[0].AccountID != "acct-1" {
		t.Errorf("account = %q, want acct-1", rows[0].AccountID)
	}
	if rows[0].Attribution != store.AttributionInferred {
		t.Errorf("attribution = %q, want inferred", rows[0].Attribution)
	}
}

// A home nobody ever observed has no account to credit, and picking one would
// put the reading on somebody else's line.
func TestBuildQuotaSamples_dropsUnattributableReadings(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	st := stateObserving("/home/user/.codex", "acct-1", now)

	rows := BuildQuotaSamples(st, "/opt/tools/my-cctrace", "", "", []codexlog.RateLimitSample{
		reading(now, "pro", 300),
	})
	if len(rows) != 0 {
		t.Fatalf("%d rows for an unobserved home, want 0", len(rows))
	}
}

// A backfilled reading records which session log it came out of, so an inferred
// attribution can be checked against the file that produced it. The account on
// those rows is inferred rather than measured -- the JSONL's rate_limits records
// carry no account identifier -- and without this link there was nothing to
// audit a credit against.
func TestBuildQuotaSamples_recordsTheSourceSession(t *testing.T) {
	at := time.Date(2026, 8, 20, 5, 0, 0, 0, time.UTC)
	st := stateObserving("/home/user/.codex", "acct-1", at)

	const sessionID = "00000000-0000-4000-8000-000000000001"
	rows := BuildQuotaSamples(st, "/home/user/.codex", "", "/home/user/.codex/sessions/rollout-2026-08-20T05-00-00-"+sessionID+".jsonl",
		[]codexlog.RateLimitSample{{
			Timestamp: at,
			Windows:   []codexlog.RateLimitWindow{{WindowMinutes: 10080, UsedPercent: 42}},
		}})

	if len(rows) != 1 {
		t.Fatalf("built %d rows, want 1", len(rows))
	}
	if rows[0].SourceSessionID != sessionID {
		t.Errorf("source_session_id = %q, want the session uuid %q", rows[0].SourceSessionID, sessionID)
	}
}

// The uuid, never the path. A path embeds a home directory, so storing it would
// carry a person's account name into every row and every export of the table.
func TestBuildQuotaSamples_sourceCarriesNoPath(t *testing.T) {
	at := time.Date(2026, 8, 20, 5, 0, 0, 0, time.UTC)
	st := stateObserving("/home/user/.codex", "acct-1", at)

	rows := BuildQuotaSamples(st, "/home/user/.codex", "", "/home/user/.codex/sessions/rollout-2026-08-20T05-00-00-00000000-0000-4000-8000-000000000001.jsonl",
		[]codexlog.RateLimitSample{{
			Timestamp: at,
			Windows:   []codexlog.RateLimitWindow{{WindowMinutes: 10080, UsedPercent: 42}},
		}})

	if strings.Contains(rows[0].SourceSessionID, "/") || strings.Contains(rows[0].SourceSessionID, "user") {
		t.Errorf("source_session_id = %q, want no path material", rows[0].SourceSessionID)
	}
}

// A live read has no session log behind it and was never inferred, so the field
// is empty rather than filled with something that only looks like a source.
func TestBuildAppServerQuotaSamples_hasNoSource(t *testing.T) {
	rows := BuildAppServerQuotaSamples("acct-1", "", &codexappserver.Snapshot{
		FetchedAt: time.Date(2026, 8, 20, 5, 0, 0, 0, time.UTC),
		Readings: []codexappserver.Reading{
			{WindowMinutes: 300, UsedPercent: 12, LimitID: "codex"},
		},
	})

	if len(rows) != 1 {
		t.Fatalf("built %d rows, want 1", len(rows))
	}
	if rows[0].SourceSessionID != "" {
		t.Errorf("source_session_id = %q, want empty for a live read", rows[0].SourceSessionID)
	}
}
