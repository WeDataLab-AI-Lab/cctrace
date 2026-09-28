package syncer

import (
	"encoding/json"
	"testing"

	"cctrace/internal/sessionlog"
)

// How a skill was invoked is not a separate signal to collect: it is which field
// carried the name. A <command-name> line exists because somebody typed a slash
// command; an attributionSkill is written when a skill runs that nobody typed.
//
// Reading the Skill tool_use as a third source counted the same invocation twice --
// every Skill call in a real session also carried the attribution (#57).
func TestInvokeTypeComesFromWhichFieldNamedTheCommand(t *testing.T) {
	t.Run("typed slash command is explicit", func(t *testing.T) {
		var r sessionlog.Record
		line := `{"type":"user","uuid":"u1","timestamp":"2026-08-20T00:00:00Z",` +
			`"message":{"content":"<command-name>/clear</command-name>"}}`
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		got := toStoreRecord(&r, "p", "u", "h")
		if got.CommandName != "clear" || got.CommandInvoke != "explicit" {
			t.Errorf("got %q/%q, want clear/explicit", got.CommandName, got.CommandInvoke)
		}
	})

	t.Run("attributed skill is implicit", func(t *testing.T) {
		var r sessionlog.Record
		if err := json.Unmarshal([]byte(`{"type":"assistant","uuid":"u2","timestamp":"2026-08-20T00:00:00Z"}`), &r); err != nil {
			t.Fatal(err)
		}
		got := toStoreRecord(&r, "p", "u", "h", "some-plugin:some-skill")
		if got.CommandName != "some-plugin:some-skill" || got.CommandInvoke != "implicit" {
			t.Errorf("got %q/%q, want some-plugin:some-skill/implicit", got.CommandName, got.CommandInvoke)
		}
	})

	t.Run("no command means no invoke type", func(t *testing.T) {
		var r sessionlog.Record
		if err := json.Unmarshal([]byte(`{"type":"assistant","uuid":"u3","timestamp":"2026-08-20T00:00:00Z"}`), &r); err != nil {
			t.Fatal(err)
		}
		got := toStoreRecord(&r, "p", "u", "h")
		if got.CommandInvoke != "" {
			t.Errorf("CommandInvoke = %q, want empty", got.CommandInvoke)
		}
	})
}

// One Skill tool call must not become a second row: the attributed record already
// represents it.
func TestSkillToolCallDoesNotAddARow(t *testing.T) {
	var r sessionlog.Record
	line := `{"type":"assistant","uuid":"u4","timestamp":"2026-08-20T00:00:00Z","attributionSkill":"p:s",` +
		`"message":{"content":[{"type":"tool_use","name":"Skill","input":{"skill":"p:s"}}]}}`
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		t.Fatal(err)
	}
	active := ""
	name := attributionSkillInvocationName(&r, &active)
	got := toStoreRecord(&r, "p", "u", "h", name)
	if got == nil {
		t.Fatal("record was skipped")
	}
	if got.RecordType == "skill-use" {
		t.Error("a synthetic skill-use row was produced; the attributed record already counts it")
	}
	if got.CommandInvoke != "implicit" {
		t.Errorf("CommandInvoke = %q, want implicit", got.CommandInvoke)
	}
}
