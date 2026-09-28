package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestPgStore_PluginUsage_excludesBuiltinsWhenUnclassified pins the server-side
// subtraction that makes the Plugins & Skills page usable before any classifying
// client exists.
//
// Every row in the field carries command_source = ” today, so the "unclassified"
// arm of that query is not a legacy tail -- it is all of the data. Left unfiltered
// it counted /clear and /model as plugin usage, which is the 5.5x over-count #57
// set out to remove.
func TestPgStore_PluginUsage_excludesBuiltinsWhenUnclassified(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	since, until := ts.Add(-time.Hour), ts.Add(time.Hour)

	raw := json.RawMessage(`{"text":"x"}`)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		// Unclassified, as every row in the field is today.
		{Ts: ts, SessionID: "s1", RecordType: "user", ProfileEmail: "p@example.com",
			Agent: "claude", CommandName: "clear", UUID: "u1", Raw: raw},
		{Ts: ts, SessionID: "s1", RecordType: "user", ProfileEmail: "p@example.com",
			Agent: "claude", CommandName: "model", UUID: "u2", Raw: raw},
		{Ts: ts, SessionID: "s1", RecordType: "user", ProfileEmail: "p@example.com",
			Agent: "claude", CommandName: "deploy", UUID: "u3", Raw: raw},
		{Ts: ts, SessionID: "s1", RecordType: "user", ProfileEmail: "p@example.com",
			Agent: "claude", CommandName: "security-review", UUID: "u5", Raw: raw},
		// /worktree is a Codex builtin and is not one of Claude's. Keying the
		// subtraction by agent is what keeps one agent's builtin from excusing a plugin
		// of the same name under the other. (An earlier draft used /rename for this and
		// was wrong -- both agents ship it.)
		{Ts: ts, SessionID: "s3", RecordType: "user", ProfileEmail: "p@example.com",
			Agent: "codex", CommandName: "worktree", UUID: "u6", Raw: raw},
		{Ts: ts, SessionID: "s4", RecordType: "user", ProfileEmail: "p@example.com",
			Agent: "claude", CommandName: "worktree", UUID: "u7", Raw: raw},
		// Classified by a new client: trusted as-is. A project command that happens to
		// share a builtin's name survives here, which is the whole reason the
		// subtraction is limited to unclassified rows.
		{Ts: ts, SessionID: "s2", RecordType: "user", ProfileEmail: "p@example.com",
			Agent: "claude", CommandName: "clear", CommandSource: "plugin", CommandKind: "command",
			UUID: "u4", Raw: raw},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	got, err := s.PluginUsage(ctx, since, until, "", "", "", "")
	if err != nil {
		t.Fatalf("PluginUsage: %v", err)
	}
	calls := map[string]int64{}
	for _, r := range got {
		calls[r.CommandName] += r.InvocationCount
	}

	if calls["model"] != 0 {
		t.Errorf("model counted %d times; a builtin with no classification is not plugin usage", calls["model"])
	}
	// Shipped either way -- the docs mark /security-review a command and /debug a
	// skill, and neither is a plugin.
	if calls["security-review"] != 0 {
		t.Errorf("security-review counted %d times as plugin usage; it ships with the binary", calls["security-review"])
	}
	if calls["deploy"] == 0 {
		t.Error("deploy vanished; only the names the binary ships may be subtracted, " +
			"and the server cannot tell a project command from a plugin one by name")
	}
	// 'clear' appears once from the unclassified row (dropped) and once from the
	// plugin-classified row (kept), so exactly one survives.
	if calls["worktree"] != 1 {
		t.Errorf("worktree counted %d times, want 1 — Codex's builtin must drop while the "+
			"Claude row of the same name survives", calls["worktree"])
	}
	if calls["clear"] != 1 {
		t.Errorf("clear counted %d times, want 1 — the classified row must survive the "+
			"name subtraction that removes the unclassified one", calls["clear"])
	}
}
