package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/usage"
)

func personalAccount(t *testing.T, s *Service, provider string) map[string]any {
	t.Helper()
	home := t.TempDir()
	r := call(t, s, "add", "quota.account.add", map[string]any{"provider": provider, "label": "Test account", "home": home})
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	rows := accountListFrom(t, s)["accounts"].([]any)
	var found map[string]any
	for _, v := range rows {
		a := v.(map[string]any)
		if a["provider"] == provider && a["enabled"] == false {
			found = a
		}
	}
	r = call(t, s, "set", "quota.account.set", map[string]any{"account_ref": found["account_ref"], "generation": found["generation"], "enabled": true})
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	for _, v := range accountListFrom(t, s)["accounts"].([]any) {
		a := v.(map[string]any)
		if a["id"] == found["id"] {
			return a
		}
	}
	t.Fatal("missing")
	return nil
}
func TestQuotaAccountsVerifiedMergePreservesAliasAndPreferences(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	read := func(context.Context, string, string) (accountSample, error) {
		return accountSample{Quota: quotaResult{Provider: "claude", State: "fresh", LastSuccessAt: &stamp, Windows: []quotaWindow{{Name: "five_hour", Utilization: 0, ResetsAt: now.Add(time.Hour).Format(time.RFC3339Nano)}}}, Proof: "claude\x00acct\x00org\x00scope", Billing: "acct"}, nil
	}
	s, e := New(Config{AppSupportRoot: root, Now: func() time.Time { return now }, accountReader: read})
	if e != nil {
		t.Fatal(e)
	}
	a := personalAccount(t, s, "claude")
	b := personalAccount(t, s, "claude")
	for _, row := range []map[string]any{a, b} {
		r := call(t, s, "read", "quota.account.read", map[string]any{"account_ref": row["account_ref"], "generation": row["generation"]})
		if r.Error != nil && r.Error.Code != "account_mismatch" {
			t.Fatal(r.Error)
		}
	}
	rows := accountListFrom(t, s)["accounts"].([]any)
	if len(rows) != 1 {
		t.Fatalf("same proof not merged: %v", rows)
	}
	one := rows[0].(map[string]any)
	if one["enabled"] != true || one["personal"] != true {
		t.Fatal("choice lost")
	}
	reopened := accountTestService(t, root)
	rows = accountListFrom(t, reopened)["accounts"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != one["id"] {
		t.Fatal("stable group lost")
	}
	reopened.accountsMu.Lock()
	if len(reopened.accounts.Accounts[0].Aliases) != 1 {
		t.Error("second personal home lost")
	}
	reopened.accountsMu.Unlock()
}
func TestQuotaAccountsCorruptStoreDoesNotBreakLegacyDaemon(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if e := os.WriteFile(filepath.Join(root, accountStoreFile), []byte(`{"version":1,"accounts":[{"id":"/private/SECRET"}]}`), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := New(Config{AppSupportRoot: root, DaemonRoot: t.TempDir()})
	if e != nil {
		t.Fatal("new store broke startup")
	}
	for _, op := range []string{"hello", "status", "daemon.status.read"} {
		r := call(t, s, "legacy", op, map[string]any{})
		if r.Error != nil {
			t.Fatal(r.Error)
		}
	}
	r := call(t, s, "accounts", "quota.accounts.list", map[string]any{})
	if r.Error == nil || r.Error.Code != "storage_error" {
		t.Fatal("unsafe store accepted")
	}
}
func TestQuotaAccountsRetryFloorAndCancellationAreAccountScoped(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	s, e := New(Config{AppSupportRoot: t.TempDir(), Now: func() time.Time { return now }, accountReader: func(_ context.Context, p, h string) (accountSample, error) {
		calls.Add(1)
		if p == "claude" {
			return accountSample{}, &usage.RateLimitError{RetryAfter: time.Minute}
		}
		return accountSample{Quota: quotaResult{Provider: p, State: "fresh", LastSuccessAt: &stamp, Windows: []quotaWindow{{Name: "five_hour", Utilization: 0, ResetsAt: now.Add(time.Hour).Format(time.RFC3339Nano)}}}}, nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	a := personalAccount(t, s, "claude")
	b := personalAccount(t, s, "codex")
	for _, row := range []map[string]any{a, a, b} {
		r := call(t, s, "read", "quota.account.read", map[string]any{"account_ref": row["account_ref"], "generation": row["generation"]})
		if r.Error != nil {
			t.Fatal(r.Error)
		}
		encoded, _ := json.Marshal(r.Result)
		if row["provider"] == "claude" && !strings.Contains(string(encoded), "rate_limited") {
			t.Fatal("retry state lost")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("retry floor bypass: %d", calls.Load())
	}
}

func TestQuotaAccountCancellationLeavesHealthyPeerAndLateGenerationUnpublished(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	entered := make(chan struct{})
	release := make(chan struct{})
	s, e := New(Config{AppSupportRoot: t.TempDir(), Now: func() time.Time { return now }, accountReader: func(ctx context.Context, p, h string) (accountSample, error) {
		if p == "claude" {
			close(entered)
			<-release
		}
		return accountSample{Quota: quotaResult{Provider: p, State: "fresh", LastSuccessAt: &stamp, Windows: []quotaWindow{{Name: "five_hour", Utilization: 12, ResetsAt: now.Add(time.Hour).Format(time.RFC3339Nano)}}}}, nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	a := personalAccount(t, s, "claude")
	b := personalAccount(t, s, "codex")
	payload := map[string]any{"account_ref": a["account_ref"], "generation": a["generation"]}
	raw, _ := json.Marshal(payload)
	done := make(chan *ErrorObject, 1)
	go func() { _, err := s.readQuotaAccount(context.Background(), raw); done <- err }()
	<-entered
	peer := call(t, s, "peer", "quota.account.read", map[string]any{"account_ref": b["account_ref"], "generation": b["generation"]})
	if peer.Error != nil {
		t.Fatal(peer.Error)
	}
	r := call(t, s, "cancel", "quota.account.cancel", payload)
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	r = call(t, s, "off", "quota.account.set", map[string]any{"account_ref": a["account_ref"], "generation": a["generation"], "enabled": false})
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("late cancelled result published")
	}
}

func TestQuotaAccountCredentialRotationKeepsLogicalIdentityButRotatesOwnership(t *testing.T) {
	t.Parallel()
	var epoch atomic.Int32
	epoch.Store(1)
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	s, e := New(Config{AppSupportRoot: t.TempDir(), Now: func() time.Time { return now }, accountReader: func(context.Context, string, string) (accountSample, error) {
		return accountSample{Quota: quotaResult{Provider: "claude", State: "fresh", LastSuccessAt: &stamp, Windows: []quotaWindow{{Name: "five_hour", Utilization: 0, ResetsAt: now.Add(time.Hour).Format(time.RFC3339Nano)}}}, Proof: "stable-billing-scope", Billing: "acct", Epoch: fmt.Sprint(epoch.Load())}, nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	row := personalAccount(t, s, "claude")
	payload := map[string]any{"account_ref": row["account_ref"], "generation": row["generation"]}
	r := call(t, s, "one", "quota.account.read", payload)
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	epoch.Store(2)
	r = call(t, s, "two", "quota.account.read", payload)
	if r.Error == nil || r.Error.Code != "account_mismatch" {
		t.Fatalf("old credential epoch published: %+v", r)
	}
	fresh := accountListFrom(t, s)["accounts"].([]any)[0].(map[string]any)
	if fresh["id"] != row["id"] || fresh["account_ref"] == row["account_ref"] {
		t.Fatal("rotation destroyed logical account or kept old ref")
	}
}

func TestQuotaAccountsTwoSameProviderAccountsHaveIndependentMeasuredWindows(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	samples := map[string]accountSample{}
	s, e := New(Config{AppSupportRoot: t.TempDir(), Now: func() time.Time { return now }, accountReader: func(_ context.Context, _ string, home string) (accountSample, error) { return samples[home], nil }})
	if e != nil {
		t.Fatal(e)
	}
	a := personalAccount(t, s, "claude")
	b := personalAccount(t, s, "claude")
	for index, row := range []map[string]any{a, b} {
		s.accountsMu.Lock()
		i := s.accountIndexLocked(row["account_ref"].(string), uint64(row["generation"].(float64)))
		home := s.accounts.Accounts[i].Home
		s.accountsMu.Unlock()
		value := 31.0
		proof := "alice-org-scope"
		if index == 1 {
			value = 77
			proof = "bob-org-scope"
		}
		samples[home] = accountSample{Quota: quotaResult{Provider: "claude", State: "fresh", LastSuccessAt: &stamp, Windows: []quotaWindow{{Name: "five_hour", Utilization: value, ResetsAt: now.Add(time.Hour).Format(time.RFC3339Nano)}}}, Proof: proof, Billing: fmt.Sprintf("account%d", index)}
	}
	for index, row := range []map[string]any{a, b} {
		r := call(t, s, "read", "quota.account.read", map[string]any{"account_ref": row["account_ref"], "generation": row["generation"]})
		if r.Error != nil {
			t.Fatal(r.Error)
		}
		encoded, _ := json.Marshal(r.Result)
		var reading struct {
			State   string        `json:"state"`
			Windows []quotaWindow `json:"windows"`
		}
		json.Unmarshal(encoded, &reading)
		want := 31.0
		if index == 1 {
			want = 77
		}
		if reading.State != "fresh" || len(reading.Windows) != 1 || reading.Windows[0].Utilization != want {
			t.Fatalf("cross-account reading: %s", encoded)
		}
	}
	if len(accountListFrom(t, s)["accounts"].([]any)) != 2 {
		t.Fatal("different proven accounts merged")
	}
}
