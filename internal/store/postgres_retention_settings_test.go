package store

import (
	"context"
	"testing"
)

// TestRetentionSettingsDurability is the regression test for the integrated
// data-loss bug: a UI "permanent" (0-day) choice for otel must SURVIVE a reboot,
// even though Migrate re-adds the hardcoded 90-day policy. The boot resolution
// (EffectiveRetentionConfig -> ReconcileRetention) must re-assert the stored 0.
func TestRetentionSettingsDurability(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	p := func(n int) *int { return &n }
	truncateTables(t, s)
	forceRetention(t, s, "otel_events", p(90))
	forceRetention(t, s, "otel_metrics", p(90))
	t.Cleanup(func() {
		truncateTables(t, s)
		forceRetention(t, s, "otel_events", p(90))
		forceRetention(t, s, "otel_metrics", p(90))
		forceRetention(t, s, "session_records", nil)
	})

	// Admin sets otel retention to permanent via the UI path: persist + reconcile.
	if err := s.UpsertRetentionSetting(ctx, "otel", 0, "admin@example.com"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	cfg, err := s.EffectiveRetentionConfig(ctx, RetentionConfig{}) // env unset
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if err := s.ReconcileRetention(ctx, cfg); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, tbl := range []string{"otel_events", "otel_metrics"} {
		if d := retentionDays(t, s, tbl); d != nil {
			t.Fatalf("%s policy not removed after permanent set: %v", tbl, d)
		}
	}

	// Simulate a reboot: Migrate re-adds the default 90-day policy...
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("replay migrate: %v", err)
	}
	// ...then the boot resolution re-asserts the stored "permanent" choice.
	cfg2, err := s.EffectiveRetentionConfig(ctx, RetentionConfig{})
	if err != nil {
		t.Fatalf("effective #2: %v", err)
	}
	if cfg2.OtelDays == nil || *cfg2.OtelDays != 0 {
		t.Fatalf("effective otel = %v, want 0 from persisted setting", cfg2.OtelDays)
	}
	if err := s.ReconcileRetention(ctx, cfg2); err != nil {
		t.Fatalf("reconcile #2: %v", err)
	}
	for _, tbl := range []string{"otel_events", "otel_metrics"} {
		if d := retentionDays(t, s, tbl); d != nil {
			t.Errorf("reboot REVERTED the UI permanent choice: %s policy = %v, want removed", tbl, d)
		}
	}
}

func TestEffectiveRetentionConfig(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	p := func(n int) *int { return &n }
	truncateTables(t, s)
	t.Cleanup(func() { truncateTables(t, s) })

	// No env, no settings -> nil (plain deploy never changes anything).
	cfg, err := s.EffectiveRetentionConfig(ctx, RetentionConfig{})
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if cfg.OtelDays != nil || cfg.SessionDays != nil {
		t.Errorf("empty -> %+v, want nil/nil", cfg)
	}

	// A persisted setting fills its axis.
	if err := s.UpsertRetentionSetting(ctx, "session", 45, "a"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	cfg, _ = s.EffectiveRetentionConfig(ctx, RetentionConfig{})
	if cfg.SessionDays == nil || *cfg.SessionDays != 45 {
		t.Errorf("session persisted -> %v, want 45", cfg.SessionDays)
	}
	if cfg.OtelDays != nil {
		t.Errorf("otel with no setting -> %v, want nil", cfg.OtelDays)
	}

	// An env override wins over the persisted setting.
	cfg, _ = s.EffectiveRetentionConfig(ctx, RetentionConfig{SessionDays: p(7)})
	if cfg.SessionDays == nil || *cfg.SessionDays != 7 {
		t.Errorf("env override -> %v, want 7", cfg.SessionDays)
	}

	// Upsert is last-write-wins.
	if err := s.UpsertRetentionSetting(ctx, "session", 30, "b"); err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	settings, _ := s.ListRetentionSettings(ctx)
	if len(settings) != 1 || settings[0].Days != 30 || settings[0].UpdatedBy != "b" {
		t.Errorf("settings = %+v, want one session=30 by b", settings)
	}
}
