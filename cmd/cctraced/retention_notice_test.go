package main

import (
	"strings"
	"testing"

	"cctrace/internal/store"
)

// session_records holds conversation content and, unlike the otel hypertables
// (90d from migrations), gets NO retention policy by default. That asymmetry is
// invisible: nothing in the boot path says the most sensitive table is the one
// kept forever. These tests pin the notice that says it.
func TestSessionRetentionNotice(t *testing.T) {
	days := 90
	zero := 0

	t.Run("unset axis warns that conversation content is kept forever", func(t *testing.T) {
		got := sessionRetentionNotice(store.RetentionConfig{})
		if got == "" {
			t.Fatal("want a notice when the session axis is unmanaged, got none")
		}
		// The operator must be able to act on it, so the notice has to name the
		// table, the knob, and the consequence.
		for _, want := range []string{"session_records", "SESSION_RETENTION_DAYS"} {
			if !strings.Contains(got, want) {
				t.Errorf("notice must mention %q; got %q", want, got)
			}
		}
	})

	t.Run("explicit permanent (0) is a choice, not a surprise", func(t *testing.T) {
		if got := sessionRetentionNotice(store.RetentionConfig{SessionDays: &zero}); got != "" {
			t.Errorf("an explicit 0 was chosen deliberately; want no notice, got %q", got)
		}
	})

	t.Run("configured axis is silent", func(t *testing.T) {
		if got := sessionRetentionNotice(store.RetentionConfig{SessionDays: &days}); got != "" {
			t.Errorf("want no notice when the axis is managed, got %q", got)
		}
	})

	t.Run("otel setting alone does not silence the session notice", func(t *testing.T) {
		if got := sessionRetentionNotice(store.RetentionConfig{OtelDays: &days}); got == "" {
			t.Fatal("otel retention says nothing about session_records; want a notice")
		}
	})
}
