package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Codex never emits prompt_source, so the Claude-shaped interactive test (which requires
// a genuine typed turn) put every enriched Codex session in the headless bucket while
// un-enriched ones slipped into interactive through the legacy escape hatch. The
// classifier now reads Codex's entrypoint, which the syncer derives from originator.
func TestPgStore_SessionOverview_CodexSourceByEntrypoint(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	const profile = "codex-source-filter-profile"
	rec := func(session, entrypoint string) *SessionRecord {
		return &SessionRecord{
			Ts:              ts,
			SessionID:       session,
			RecordType:      "assistant",
			ProfileEmail:    profile,
			UserID:          "u1",
			Agent:           "codex",
			BillingProvider: "openai",
			SourceFile:      "rollout-" + session + ".jsonl",
			Entrypoint:      entrypoint,
			Raw:             json.RawMessage(`{}`),
		}
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		rec("codex-tui-sess", "cli"),
		rec("codex-exec-sess", "codex_exec"),
		rec("codex-delegated-sess", "claude-code"),
		rec("codex-legacy-sess", ""),
	}); err != nil {
		t.Fatal(err)
	}

	ids := func(source string) map[string]bool {
		got, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{
			ProfileEmail: profile, Source: source, Limit: 50,
		})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, o := range got {
			out[o.SessionID] = true
		}
		return out
	}

	// cli is the human TUI; an empty entrypoint is a pre-originator rollout whose
	// launcher is unknown, so it keeps the legacy benefit of the doubt until reenrich.
	interactive := ids("interactive")
	for _, want := range []string{"codex-tui-sess", "codex-legacy-sess"} {
		if !interactive[want] {
			t.Errorf("interactive missing %s", want)
		}
	}
	for _, unwanted := range []string{"codex-exec-sess", "codex-delegated-sess"} {
		if interactive[unwanted] {
			t.Errorf("interactive should not contain %s", unwanted)
		}
	}

	headless := ids("headless")
	for _, want := range []string{"codex-exec-sess", "codex-delegated-sess"} {
		if !headless[want] {
			t.Errorf("headless missing %s", want)
		}
	}
	if headless["codex-tui-sess"] {
		t.Error("headless should not contain codex-tui-sess")
	}
}
