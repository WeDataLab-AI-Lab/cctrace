package store

import (
	"context"
	"testing"
	"time"
)

// The SQL is where this fix lives, so it has to run against a real database.
// A unit test would only prove the string was edited.
func TestPluginUsageCountsOnlyPluginCommands(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()

	rows := []*SessionRecord{
		// What a classifying client sends.
		{Ts: now, SessionID: "s1", RecordType: "user", ProfileEmail: "a@ex.com", UserID: "u1",
			Agent: "claude", CommandName: "ralph", CommandSource: "plugin", CommandKind: "skill", UUID: "u-1"},
		{Ts: now, SessionID: "s1", RecordType: "user", ProfileEmail: "a@ex.com", UserID: "u1",
			Agent: "claude", CommandName: "clear", CommandSource: "builtin", CommandKind: "command", UUID: "u-2"},
		{Ts: now, SessionID: "s1", RecordType: "user", ProfileEmail: "a@ex.com", UserID: "u1",
			Agent: "claude", CommandName: "compact", CommandSource: "ambiguous", CommandKind: "unknown", UUID: "u-3"},
		// An older client that never classified: its rows must keep counting, or
		// history would empty out the day this ships.
		{Ts: now, SessionID: "s1", RecordType: "user", ProfileEmail: "a@ex.com", UserID: "u1",
			Agent: "claude", CommandName: "legacy-cmd", UUID: "u-4"},
	}
	if err := s.InsertSessionRecords(ctx, rows); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := s.PluginUsage(ctx, now.Add(-time.Hour), now.Add(time.Hour), "", "", "", "")
	if err != nil {
		t.Fatalf("PluginUsage: %v", err)
	}
	counted := map[string]bool{}
	for _, r := range got {
		counted[r.CommandName] = true
	}
	if !counted["ralph"] {
		t.Error("a plugin command was not counted")
	}
	if !counted["legacy-cmd"] {
		t.Error("an unclassified row stopped counting; history would empty out")
	}
	for _, name := range []string{"clear", "compact"} {
		if counted[name] {
			t.Errorf("%q was counted as plugin usage", name)
		}
	}
}

// Claude was excluded from skill usage outright because its command names could not
// be told apart from builtins. It joins now, but only where the client resolved the
// name to a skill.
func TestSkillUsageAdmitsClaudeOnlyWhenClassifiedAsSkill(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()

	rows := []*SessionRecord{
		{Ts: now, SessionID: "s2", RecordType: "user", ProfileEmail: "b@ex.com", UserID: "u2",
			Agent: "claude", CommandName: "ralph", CommandSource: "plugin", CommandKind: "skill", UUID: "s-1"},
		{Ts: now, SessionID: "s2", RecordType: "user", ProfileEmail: "b@ex.com", UserID: "u2",
			Agent: "claude", CommandName: "commit", CommandSource: "plugin", CommandKind: "command", UUID: "s-2"},
		{Ts: now, SessionID: "s2", RecordType: "user", ProfileEmail: "b@ex.com", UserID: "u2",
			Agent: "claude", CommandName: "clear", CommandSource: "builtin", CommandKind: "command", UUID: "s-3"},
	}
	if err := s.InsertSessionRecords(ctx, rows); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := s.SkillUsage(ctx, now.Add(-time.Hour), now.Add(time.Hour), "", "", "", "")
	if err != nil {
		t.Fatalf("SkillUsage: %v", err)
	}
	counted := map[string]bool{}
	for _, r := range got {
		counted[r.SkillName] = true
	}
	if !counted["ralph"] {
		t.Error("a Claude skill is still missing from skill usage")
	}
	for _, name := range []string{"commit", "clear"} {
		if counted[name] {
			t.Errorf("%q was counted as a skill", name)
		}
	}
}

// The same record turns up under more than one session_id when a session is forked
// or re-recorded -- 33,322 uuids in our own data. Each stored copy used to add
// another invocation to the count (#57).
func TestSkillUsageCountsOneRecordOnceAcrossSessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()

	rows := []*SessionRecord{
		{Ts: now, SessionID: "orig", RecordType: "user", ProfileEmail: "c@ex.com", UserID: "u9",
			Agent: "claude", CommandName: "ralph", CommandSource: "plugin", CommandKind: "skill", UUID: "same-uuid"},
		// The fork re-recorded the very same message under a new session id.
		{Ts: now, SessionID: "forked", RecordType: "user", ProfileEmail: "c@ex.com", UserID: "u9",
			Agent: "claude", CommandName: "ralph", CommandSource: "plugin", CommandKind: "skill", UUID: "same-uuid"},
	}
	if err := s.InsertSessionRecords(ctx, rows); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := s.SkillUsage(ctx, now.Add(-time.Hour), now.Add(time.Hour), "", "", "", "")
	if err != nil {
		t.Fatalf("SkillUsage: %v", err)
	}
	var total int64
	for _, r := range got {
		if r.SkillName == "ralph" {
			total += r.TotalCount
		}
	}
	if total != 1 {
		t.Errorf("ralph counted %d times, want 1 -- the fork's copy was counted again", total)
	}
}
