package store

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

type quotaOwnerStoreContract interface {
	ApplyQuotaAccountBinding(context.Context, int64, string, string) (*QuotaAccountBinding, error)
	ListQuotaAccountBindings(context.Context, int64) ([]*QuotaAccountBinding, error)
	ListAdminQuotaAccountBindings(context.Context, string) ([]*QuotaAccountBinding, error)
	ApproveQuotaAccountBinding(context.Context, int64, int64, string, string) (*QuotaAccountBinding, error)
	RevokeQuotaAccountBinding(context.Context, int64, int64) (*QuotaAccountBinding, error)
	InsertQuotaOwnerSamples(context.Context, int64, []*QuotaOwnerSample) (int, error)
	ListQuotaUsage(context.Context, int64, time.Time, time.Time, time.Time) ([]*QuotaUsageAccount, error)
	CompareQuotaUsageWindows(context.Context, int64, string, string, int, int, time.Time) (*QuotaWindowComparison, error)
}

func quotaOwnerService(t *testing.T, s *PgStore) quotaOwnerStoreContract {
	t.Helper()
	service, ok := any(s).(quotaOwnerStoreContract)
	if !ok {
		t.Fatal("PgStore does not implement the quota owner store contract")
	}
	return service
}

func TestPgStoreImplementsQuotaOwnerContract(t *testing.T) {
	s := &PgStore{}
	if _, ok := any(s).(quotaOwnerStoreContract); !ok {
		t.Fatal("PgStore does not implement the quota owner store contract")
	}
}

func TestQuotaOwnerBindingLifecycleAndIsolation(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	service := quotaOwnerService(t, s)
	ownerID := insertQuotaDashboardUser(t, s, "owner@example.test", "user")
	otherID := insertQuotaDashboardUser(t, s, "other@example.test", "user")
	adminID := insertQuotaDashboardUser(t, s, "admin@example.test", "admin")

	binding, err := service.ApplyQuotaAccountBinding(ctx, ownerID, "anthropic", "acct-owner")
	if err != nil {
		t.Fatalf("apply quota binding: %v", err)
	}
	if binding.Status != "pending" || binding.VerifiedAt != nil {
		t.Fatalf("new binding = %+v, want pending and unverified", binding)
	}
	repeated, err := service.ApplyQuotaAccountBinding(ctx, ownerID, "anthropic", "acct-owner")
	if err != nil || repeated.ID != binding.ID {
		t.Fatalf("repeated apply = %+v, err=%v; want same pending row", repeated, err)
	}

	window := 300
	now := time.Now().UTC().Truncate(time.Second)
	reset := now.Add(4 * time.Hour)
	sample := &QuotaOwnerSample{BillingProvider: "anthropic", AccountID: "acct-owner", WindowKey: "session", SampledAt: now, UsedPct: 42, ResetsAt: &reset, WindowMinutes: &window, Plan: "max", Attribution: AttributionObserved}
	if _, err := service.InsertQuotaOwnerSamples(ctx, ownerID, []*QuotaOwnerSample{sample}); !errors.Is(err, ErrQuotaBindingUnverified) {
		t.Fatalf("pending binding sample error = %v, want ErrQuotaBindingUnverified", err)
	}

	approved, err := service.ApproveQuotaAccountBinding(ctx, binding.ID, adminID, "provider_console", "ticket-42")
	if err != nil {
		t.Fatalf("approve quota binding: %v", err)
	}
	if approved.Status != "approved" || approved.VerifiedAt == nil || approved.ApprovedBy == nil || *approved.ApprovedBy != adminID {
		t.Fatalf("approved binding = %+v", approved)
	}
	if n, err := service.InsertQuotaOwnerSamples(ctx, ownerID, []*QuotaOwnerSample{sample}); !errors.Is(err, ErrQuotaBindingUnverified) || n != 0 {
		t.Fatalf("pre-approval sample was accepted after approval: inserted=%d err=%v", n, err)
	}
	sample.SampledAt = approved.VerifiedAt.Add(time.Second)
	now = sample.SampledAt
	if n, err := service.InsertQuotaOwnerSamples(ctx, ownerID, []*QuotaOwnerSample{sample}); err != nil || n != 1 {
		t.Fatalf("insert approved owner sample = %d, err=%v; want 1 row", n, err)
	}
	preApprovalAt := approved.VerifiedAt.Add(-time.Minute)
	if _, err := s.pool.Exec(ctx, `INSERT INTO quota_owner_samples
		(dashboard_user_id,billing_provider,account_id,window_key,sampled_at,used_pct,resets_at,window_minutes,attribution,source,binding_verified_at)
		VALUES($1,'anthropic','acct-owner','session',$2,99,$3,300,'observed','client_reported',$4)`,
		ownerID, preApprovalAt, reset, *approved.VerifiedAt); err != nil {
		t.Fatalf("seed pre-approval row with the current epoch: %v", err)
	}
	ownerUsage, err := service.ListQuotaUsage(ctx, ownerID, now.Add(-24*time.Hour), now.Add(time.Minute), now)
	if err != nil || len(ownerUsage) != 1 {
		t.Fatalf("owner usage = %+v, err=%v; want one account", ownerUsage, err)
	}
	if got := ownerUsage[0].Windows.FiveHour.UsedPct; got == nil || *got != 42 || ownerUsage[0].Windows.FiveHour.State != "measured" {
		t.Fatalf("five-hour usage = %+v, want measured 42%%", ownerUsage[0].Windows.FiveHour)
	}
	if ownerUsage[0].Windows.FiveHour.Source != "client_reported" {
		t.Fatalf("source = %q, want client_reported", ownerUsage[0].Windows.FiveHour.Source)
	}
	comparison, err := service.CompareQuotaUsageWindows(ctx, ownerID, "anthropic", "acct-owner", 300, 36, now)
	if err != nil || comparison.Current == nil || len(comparison.Current.Points) != 1 || comparison.HistoryCount != 0 {
		t.Fatalf("current-window comparison = %+v, err=%v", comparison, err)
	}
	if comparison.Current.Points[0].UsedPct != 42 {
		t.Fatalf("comparison exposed pre-approval row: current=%+v", comparison.Current.Points)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_billing_accounts (billing_provider,account_id,reason,created_by) VALUES ('anthropic','acct-owner','fixture','admin')`); err != nil {
		t.Fatalf("exclude quota account: %v", err)
	}
	if n, err := service.InsertQuotaOwnerSamples(ctx, ownerID, []*QuotaOwnerSample{sample}); !errors.Is(err, ErrQuotaAccountExcluded) || n != 0 {
		t.Fatalf("excluded account accepted private sample: inserted=%d err=%v", n, err)
	}
	ownerUsage, err = service.ListQuotaUsage(ctx, ownerID, now.Add(-24*time.Hour), now.Add(time.Minute), now)
	if err != nil || len(ownerUsage) != 1 || ownerUsage[0].Windows.FiveHour.Reason != "excluded" || ownerUsage[0].Windows.FiveHour.UsedPct != nil {
		t.Fatalf("excluded account still visible as measured: usage=%+v err=%v", ownerUsage, err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM excluded_billing_accounts WHERE billing_provider='anthropic' AND account_id='acct-owner'`); err != nil {
		t.Fatalf("remove quota exclusion: %v", err)
	}

	otherBinding, err := service.ApplyQuotaAccountBinding(ctx, otherID, "anthropic", "acct-owner")
	if err != nil {
		t.Fatalf("other user apply: %v", err)
	}
	otherUsage, err := service.ListQuotaUsage(ctx, otherID, now.Add(-24*time.Hour), now.Add(time.Minute), now)
	if err != nil || len(otherUsage) != 1 {
		t.Fatalf("other user usage = %+v, err=%v; want pending account only", otherUsage, err)
	}
	if otherUsage[0].Windows.FiveHour.UsedPct != nil || otherUsage[0].Windows.FiveHour.Attribution != "unknown" {
		t.Fatalf("other user's pending binding inherited sample data: %+v", otherUsage[0].Windows.FiveHour)
	}
	adminQueue, err := service.ListAdminQuotaAccountBindings(ctx, "pending")
	if err != nil || len(adminQueue) != 1 || adminQueue[0].ID != otherBinding.ID {
		t.Fatalf("admin pending queue = %+v, err=%v", adminQueue, err)
	}

	revoked, err := service.RevokeQuotaAccountBinding(ctx, binding.ID, adminID)
	if err != nil || revoked.Status != "revoked" {
		t.Fatalf("revoke = %+v, err=%v", revoked, err)
	}
	if n, err := service.InsertQuotaOwnerSamples(ctx, ownerID, []*QuotaOwnerSample{sample}); !errors.Is(err, ErrQuotaBindingUnverified) || n != 0 {
		t.Fatalf("revoked binding accepted sample: inserted=%d err=%v", n, err)
	}

	reapproved, err := service.ApproveQuotaAccountBinding(ctx, binding.ID, adminID, "provider_console", "ticket-43")
	if err != nil || reapproved.VerifiedAt == nil || !reapproved.VerifiedAt.After(*approved.VerifiedAt) {
		t.Fatalf("re-approval did not advance verification epoch: before=%v after=%v err=%v", approved.VerifiedAt, reapproved.VerifiedAt, err)
	}
	ownerUsage, err = service.ListQuotaUsage(ctx, ownerID, now.Add(-24*time.Hour), now.Add(time.Minute), now)
	if err != nil || len(ownerUsage) != 1 || ownerUsage[0].Windows.FiveHour.UsedPct != nil {
		t.Fatalf("old epoch sample reappeared after re-approval: usage=%+v err=%v", ownerUsage, err)
	}
	comparison, err = service.CompareQuotaUsageWindows(ctx, ownerID, "anthropic", "acct-owner", 300, 36, now)
	if err != nil || comparison.Current != nil || comparison.HistoryCount != 0 {
		t.Fatalf("old epoch comparison reappeared after re-approval: comparison=%+v err=%v", comparison, err)
	}
}

func TestDeleteUserDataRemovesPrivateQuotaBindingsAndSamples(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	service := quotaOwnerService(t, s)
	ownerID := insertQuotaDashboardUser(t, s, "delete@example.test", "user")
	adminID := insertQuotaDashboardUser(t, s, "delete-admin@example.test", "admin")
	binding, err := service.ApplyQuotaAccountBinding(ctx, ownerID, "openai", "acct-delete")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := service.ApproveQuotaAccountBinding(ctx, binding.ID, adminID, "provider_console", "DELETE-1")
	if err != nil {
		t.Fatal(err)
	}
	if approved.VerifiedAt == nil {
		t.Fatal("approved binding has no verification epoch")
	}
	// Derive the fixture time from the approval epoch: the DB and test host
	// clocks can differ, and deletion requires a genuinely post-approval sample.
	window := 10080
	sample := &QuotaOwnerSample{BillingProvider: "openai", AccountID: "acct-delete", WindowKey: "10080", SampledAt: approved.VerifiedAt.Add(time.Second), UsedPct: 7, WindowMinutes: &window, Attribution: AttributionObserved}
	if n, err := service.InsertQuotaOwnerSamples(ctx, ownerID, []*QuotaOwnerSample{sample}); err != nil || n != 1 {
		t.Fatalf("seed approved quota sample: inserted=%d err=%v", n, err)
	}
	if err := s.DeleteUserData(ctx, "delete@example.test", ""); err != nil {
		t.Fatalf("DeleteUserData: %v", err)
	}
	var bindings, samples int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM quota_account_bindings WHERE dashboard_user_id=$1`, ownerID).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM quota_owner_samples WHERE dashboard_user_id=$1`, ownerID).Scan(&samples); err != nil {
		t.Fatal(err)
	}
	if bindings != 0 || samples != 0 {
		t.Fatalf("DeleteUserData left private quota data: bindings=%d samples=%d", bindings, samples)
	}
}

func TestDeletingDashboardUserCascadesPrivateQuotaData(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	service := quotaOwnerService(t, s)
	ownerID := insertQuotaDashboardUser(t, s, "cascade@example.test", "user")
	adminID := insertQuotaDashboardUser(t, s, "cascade-admin@example.test", "admin")
	binding, err := service.ApplyQuotaAccountBinding(ctx, ownerID, "anthropic", "acct-cascade")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := service.ApproveQuotaAccountBinding(ctx, binding.ID, adminID, "provider_console", "CASCADE-1")
	if err != nil {
		t.Fatal(err)
	}
	if approved.VerifiedAt == nil {
		t.Fatal("approved binding has no verification epoch")
	}
	// Match the approved DB epoch instead of depending on host/DB clock order.
	window := 300
	if n, err := service.InsertQuotaOwnerSamples(ctx, ownerID, []*QuotaOwnerSample{{
		BillingProvider: "anthropic", AccountID: "acct-cascade", WindowKey: "session",
		SampledAt: approved.VerifiedAt.Add(time.Second), UsedPct: 12, WindowMinutes: &window, Attribution: AttributionObserved,
	}}); err != nil || n != 1 {
		t.Fatalf("seed approved quota sample: inserted=%d err=%v", n, err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM dashboard_users WHERE id=$1`, ownerID); err != nil {
		t.Fatalf("delete dashboard user: %v", err)
	}
	for _, item := range []struct{ table, ownerColumn string }{
		{"quota_account_bindings", "dashboard_user_id"},
		{"quota_owner_samples", "dashboard_user_id"},
		{"quota_account_binding_events", "owner_dashboard_user_id"},
	} {
		var count int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM `+item.table+` WHERE `+item.ownerColumn+`=$1`, ownerID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("dashboard user deletion left %d rows in %s", count, item.table)
		}
	}
}

func TestMergeUsersDeletesSourceQuotaOwnershipWithoutTransferringIt(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	service := quotaOwnerService(t, s)
	sourceID := insertQuotaDashboardUser(t, s, "source@example.test", "user")
	targetID := insertQuotaDashboardUser(t, s, "target@example.test", "user")
	adminID := insertQuotaDashboardUser(t, s, "merge-admin@example.test", "admin")
	source, err := service.ApplyQuotaAccountBinding(ctx, sourceID, "anthropic", "acct-source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveQuotaAccountBinding(ctx, source.ID, adminID, "provider_console", "MERGE-1"); err != nil {
		t.Fatal(err)
	}
	target, err := service.ApplyQuotaAccountBinding(ctx, targetID, "anthropic", "acct-target")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveQuotaAccountBinding(ctx, target.ID, adminID, "provider_console", "MERGE-2"); err != nil {
		t.Fatal(err)
	}
	if err := s.MergeUsers(ctx, "source@example.test", "", "target@example.test"); err != nil {
		t.Fatalf("MergeUsers: %v", err)
	}
	var sourceBindings, targetBindings int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM quota_account_bindings WHERE dashboard_user_id=$1`, sourceID).Scan(&sourceBindings); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM quota_account_bindings WHERE dashboard_user_id=$1`, targetID).Scan(&targetBindings); err != nil {
		t.Fatal(err)
	}
	if sourceBindings != 0 || targetBindings != 1 {
		t.Fatalf("MergeUsers transferred or retained source binding: source=%d target=%d", sourceBindings, targetBindings)
	}
}

func TestQuotaWindowComparisonQuantilesUseDistinctPastWindows(t *testing.T) {
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	window := 300
	values := []float64{10, 20, 30, 40}
	samples := make([]*QuotaOwnerSample, 0, len(values)+1)
	for i, value := range values {
		resets := now.Add(-time.Duration(i+1) * 5 * time.Hour)
		start := resets.Add(-5 * time.Hour)
		sampled := start.Add(150 * time.Minute)
		samples = append(samples, &QuotaOwnerSample{
			BillingProvider: "anthropic", AccountID: "acct-q", WindowKey: "session",
			SampledAt: sampled, UsedPct: value, ResetsAt: &resets, WindowMinutes: &window, Attribution: AttributionObserved,
		})
	}
	currentReset := now.Add(4 * time.Hour)
	samples = append(samples, &QuotaOwnerSample{
		BillingProvider: "anthropic", AccountID: "acct-q", WindowKey: "session",
		SampledAt: now.Add(-time.Minute), UsedPct: 55, ResetsAt: &currentReset, WindowMinutes: &window, Attribution: AttributionObserved,
	})

	comparison := buildQuotaWindowComparisonFromSamples("anthropic", "acct-q", window, 36, now, samples)
	if comparison.Current == nil || comparison.HistoryCount != 4 || comparison.Source != "client_reported" {
		t.Fatalf("comparison = %+v", comparison)
	}
	var median *QuotaQuantilePoint
	for i := range comparison.Quantiles {
		if comparison.Quantiles[i].ElapsedFraction == 0.5 {
			median = &comparison.Quantiles[i]
			break
		}
	}
	if median == nil || median.N != 4 || median.P25 == nil || median.P50 == nil || median.P75 == nil {
		t.Fatalf("median quantile point = %+v, want n=4 and full band", median)
	}
	if math.Abs(*median.P25-17.5) > 1e-9 || math.Abs(*median.P50-25) > 1e-9 || math.Abs(*median.P75-32.5) > 1e-9 {
		t.Fatalf("quantiles = %v/%v/%v, want 17.5/25/32.5", *median.P25, *median.P50, *median.P75)
	}
}

func TestQuotaWindowComparisonHidesBandBelowFourWindows(t *testing.T) {
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	window := 300
	samples := make([]*QuotaOwnerSample, 0, 3)
	for i, value := range []float64{10, 20, 30} {
		resets := now.Add(-time.Duration(i+1) * 5 * time.Hour)
		start := resets.Add(-5 * time.Hour)
		samples = append(samples, &QuotaOwnerSample{
			BillingProvider: "openai", AccountID: "acct-q", WindowKey: "300",
			SampledAt: start.Add(150 * time.Minute), UsedPct: value, ResetsAt: &resets, WindowMinutes: &window, Attribution: AttributionObserved,
		})
	}
	comparison := buildQuotaWindowComparisonFromSamples("openai", "acct-q", window, 36, now, samples)
	var point *QuotaQuantilePoint
	for i := range comparison.Quantiles {
		if comparison.Quantiles[i].ElapsedFraction == 0.5 {
			point = &comparison.Quantiles[i]
			break
		}
	}
	if point == nil || point.N != 3 || point.P50 == nil || point.P25 != nil || point.P75 != nil {
		t.Fatalf("n<4 quantile point = %+v, want median and no band", point)
	}
}

func TestCurrentQuotaWindowDistinguishesStaleResetAndUnmeasured(t *testing.T) {
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	window := 300
	staleReset := now.Add(time.Hour)
	stale := &QuotaOwnerSample{BillingProvider: "anthropic", AccountID: "acct-state", WindowKey: "session", SampledAt: now.Add(-time.Hour), UsedPct: 20, ResetsAt: &staleReset, WindowMinutes: &window, Attribution: AttributionObserved}
	if got := quotaUsageWindow(currentQuotaOwnerSample("anthropic", 300, []*QuotaOwnerSample{stale}, now), now); got.State != "stale" || got.UsedPct == nil || *got.UsedPct != 20 {
		t.Fatalf("stale window = %+v", got)
	}
	expiredReset := now.Add(-time.Minute)
	expired := &QuotaOwnerSample{BillingProvider: "anthropic", AccountID: "acct-state", WindowKey: "session", SampledAt: now.Add(-6 * time.Hour), UsedPct: 20, ResetsAt: &expiredReset, WindowMinutes: &window, Attribution: AttributionObserved}
	selected := currentQuotaOwnerSample("anthropic", 300, []*QuotaOwnerSample{expired}, now)
	if got := quotaUsageWindow(selected, now); got.State != "unmeasured" || got.Reason != "reset_not_yet_observed" || got.UsedPct != nil {
		t.Fatalf("expired reset = %+v, want unmeasured until new reset sample", got)
	}
	if got := quotaUsageWindow(currentQuotaOwnerSample("anthropic", 300, nil, now), now); got.State != "unmeasured" || got.Reason != "no_current_window_sample" || got.UsedPct != nil {
		t.Fatalf("unmeasured window = %+v", got)
	}
}

func insertQuotaDashboardUser(t *testing.T, s *PgStore, email, role string) int64 {
	t.Helper()
	var id int64
	if err := s.pool.QueryRow(context.Background(), `INSERT INTO dashboard_users (email,password_hash,role,name) VALUES ($1,'test-hash',$2,$1) RETURNING id`, email, role).Scan(&id); err != nil {
		t.Fatalf("insert dashboard user: %v", err)
	}
	return id
}

func TestQuotaOwnerSchemaCreatesPrivateTablesWithoutMigratingSharedSamples(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	var bindings, samples, events bool
	if err := s.pool.QueryRow(ctx, `SELECT
		to_regclass('public.quota_account_bindings') IS NOT NULL,
		to_regclass('public.quota_owner_samples') IS NOT NULL,
		to_regclass('public.quota_account_binding_events') IS NOT NULL`).Scan(&bindings, &samples, &events); err != nil {
		t.Fatalf("check quota owner schema: %v", err)
	}
	if !bindings || !samples || !events {
		t.Fatalf("quota owner tables present bindings=%v samples=%v events=%v", bindings, samples, events)
	}

	var forbiddenColumns int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='public' AND table_name='quota_owner_samples'
		AND column_name IN ('login_email','profile_email','source_session_id')`).Scan(&forbiddenColumns); err != nil {
		t.Fatalf("check private sample columns: %v", err)
	}
	if forbiddenColumns != 0 {
		t.Fatalf("quota_owner_samples contains %d identity/path columns", forbiddenColumns)
	}

	window := 300
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{{BillingProvider: "anthropic", AccountID: "legacy-acct", WindowKey: "session", SampledAt: time.Now().UTC(), UsedPct: 42, WindowMinutes: &window, LoginEmail: "legacy@example.test"}}); err != nil {
		t.Fatalf("insert shared legacy sample: %v", err)
	}
	var ownerSamples int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM quota_owner_samples`).Scan(&ownerSamples); err != nil {
		t.Fatal(err)
	}
	if ownerSamples != 0 {
		t.Fatalf("shared sample was promoted into private quota history: owner samples=%d", ownerSamples)
	}
}
