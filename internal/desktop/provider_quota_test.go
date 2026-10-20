package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/codexappserver"
	"cctrace/internal/usage"
)

type quotaReaderStub struct {
	claude func(context.Context, string) (*usage.Response, error)
	codex  func(context.Context, string) (*codexappserver.Snapshot, error)

	claudeHomes []string
	codexHomes  []string
}

func (r *quotaReaderStub) ReadClaudeQuota(ctx context.Context, home string) (*usage.Response, error) {
	r.claudeHomes = append(r.claudeHomes, home)
	if r.claude == nil {
		return nil, nil
	}
	return r.claude(ctx, home)
}

func (r *quotaReaderStub) ReadCodexQuota(ctx context.Context, home string) (*codexappserver.Snapshot, error) {
	r.codexHomes = append(r.codexHomes, home)
	if r.codex == nil {
		return nil, nil
	}
	return r.codex(ctx, home)
}

func TestProviderQuotaModeUsesOnlyExplicitSelectedHomeAndPreservesZero(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	claudeHome := makeQuotaHome(t, "Claude Explicit")
	codexHome := makeQuotaHome(t, "Codex Explicit")
	writeCodexAuth(t, codexHome, `{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-account-a"}}`)
	reader := &quotaReaderStub{
		claude: func(_ context.Context, home string) (*usage.Response, error) {
			if home != claudeHome {
				t.Fatalf("Claude home = %q, want explicit %q", home, claudeHome)
			}
			return &usage.Response{
				FetchedAt:        now,
				TokenFingerprint: "private-fingerprint",
				FiveHour:         &usage.UsageWindow{Utilization: 0, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)},
				SevenDay:         &usage.UsageWindow{Utilization: 25, ResetsAt: now.Add(24 * time.Hour).Format(time.RFC3339)},
			}, nil
		},
		codex: func(_ context.Context, home string) (*codexappserver.Snapshot, error) {
			if home != codexHome {
				t.Fatalf("Codex home = %q, want explicit %q", home, codexHome)
			}
			fiveHourReset := now.Add(time.Hour)
			weeklyReset := now.Add(24 * time.Hour)
			return &codexappserver.Snapshot{
				FetchedAt:  now,
				LoginEmail: "private-account@example.invalid",
				Readings: []codexappserver.Reading{
					{LimitID: "codex", WindowMinutes: 300, UsedPercent: 0, ResetsAt: &fiveHourReset},
					{LimitID: "codex", WindowMinutes: 10080, UsedPercent: 40, ResetsAt: &weeklyReset},
				},
			}, nil
		},
	}
	svc := newProviderTestService(t, now, reader, claudeHome, codexHome)

	claude := callProviderQuota(t, svc, context.Background(), "claude")
	if claude.State != "fresh" || claude.Error != nil || len(claude.Windows) != 2 || claude.Windows[0].Name != "five_hour" || claude.Windows[0].Utilization != 0 {
		t.Fatalf("Claude quota = %+v, want fresh explicit zero and two windows", claude)
	}
	if len(reader.claudeHomes) != 1 || reader.claudeHomes[0] != claudeHome || len(reader.codexHomes) != 0 {
		t.Fatalf("reader calls after Claude request: Claude=%v Codex=%v", reader.claudeHomes, reader.codexHomes)
	}
	encodedClaude, err := json.Marshal(claude)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedClaude), "private-fingerprint") {
		t.Fatalf("Claude token fingerprint escaped into quota result: %s", encodedClaude)
	}

	codex := callProviderQuota(t, svc, context.Background(), "codex")
	if codex.State != "fresh" || codex.Error != nil || len(codex.Windows) != 2 || codex.Windows[0].Name != "five_hour" || codex.Windows[0].Utilization != 0 {
		t.Fatalf("Codex quota = %+v, want fresh explicit zero and two windows", codex)
	}
	if len(reader.codexHomes) != 1 || reader.codexHomes[0] != codexHome {
		t.Fatalf("Codex reader calls = %v, want only %q", reader.codexHomes, codexHome)
	}
	encoded, err := json.Marshal(codex)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-account@example.invalid") {
		t.Fatalf("provider account metadata escaped into quota result: %s", encoded)
	}
}

func TestProviderQuotaDefaultsAndFixturesNeverCallProviderReader(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	reader := &quotaReaderStub{
		claude: func(context.Context, string) (*usage.Response, error) {
			t.Fatal("default mode invoked Claude provider reader")
			return nil, nil
		},
		codex: func(context.Context, string) (*codexappserver.Snapshot, error) {
			t.Fatal("default mode invoked Codex provider reader")
			return nil, nil
		},
	}
	root := t.TempDir()
	defaultService, err := New(Config{AppSupportRoot: filepath.Join(root, "default"), Now: func() time.Time { return now }, ProviderReader: reader})
	if err != nil {
		t.Fatal(err)
	}
	defaultResult := callProviderQuota(t, defaultService, context.Background(), "claude")
	if defaultResult.State != "unavailable" || defaultResult.Error == nil || defaultResult.Error.Code != "fixture_unavailable" {
		t.Fatalf("default-mode quota = %+v, want unchanged fixture-unavailable result", defaultResult)
	}

	fixtureRoot, err := filepath.Abs(filepath.Join("..", "..", "tests", "desktop", "fixtures", "v1", "ipc", "quota", "fresh"))
	if err != nil {
		t.Fatal(err)
	}
	fixtureService, err := New(Config{AppSupportRoot: filepath.Join(root, "fixture"), FixtureRoot: fixtureRoot, Now: func() time.Time { return now }, ProviderReader: reader})
	if err != nil {
		t.Fatal(err)
	}
	fixtureResult := callProviderQuota(t, fixtureService, context.Background(), "claude")
	if fixtureResult.State != "fresh" || len(fixtureResult.Windows) != 1 || fixtureResult.Windows[0].Utilization != 0 {
		t.Fatalf("fixture result = %+v, want existing explicit-zero fixture", fixtureResult)
	}
	if len(reader.claudeHomes) != 0 || len(reader.codexHomes) != 0 {
		t.Fatalf("default/fixture mode called readers: Claude=%v Codex=%v", reader.claudeHomes, reader.codexHomes)
	}
}

func TestProviderQuotaAcceptsEmptyProviderHomesAndValidatesConfiguredHomes(t *testing.T) {
	root := t.TempDir()
	if _, err := New(Config{AppSupportRoot: filepath.Join(root, "empty app"), ProviderMode: true}); err != nil {
		t.Fatalf("provider mode rejected an unconfigured personal home: %v", err)
	}
	if _, err := New(Config{AppSupportRoot: filepath.Join(root, "app"), ProviderMode: true, ClaudeHome: "relative/home"}); err == nil {
		t.Fatal("provider mode accepted a relative home")
	}
	fixtureRoot := filepath.Join(root, "fixtures")
	if err := os.Mkdir(fixtureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{
		AppSupportRoot: filepath.Join(root, "app"),
		FixtureRoot:    fixtureRoot,
		ProviderMode:   true,
		ClaudeHome:     makeQuotaHome(t, "Claude"),
	}); err == nil {
		t.Fatal("provider mode accepted simultaneous fixture mode")
	}
}

func TestProviderQuotaRetainsOnlyBoundedLastSuccessAfterTransientFailure(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	currentTime := now
	claudeHome := makeQuotaHome(t, "Claude")
	reads := 0
	reader := &quotaReaderStub{claude: func(context.Context, string) (*usage.Response, error) {
		reads++
		if reads == 1 {
			fetched := now.Add(-14 * time.Minute)
			return &usage.Response{FetchedAt: fetched, TokenFingerprint: "claude-account-a", FiveHour: &usage.UsageWindow{Utilization: 18, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)}}, nil
		}
		return &usage.Response{TokenFingerprint: "claude-account-a"}, errors.New("transport failed for /private/provider secret-token")
	}}
	svc := newProviderTestServiceWithClock(t, func() time.Time { return currentTime }, reader, claudeHome, "")
	if got := callProviderQuota(t, svc, context.Background(), "claude"); got.State != "fresh" {
		t.Fatalf("first quota state = %q, want fresh", got.State)
	}
	stale := callProviderQuota(t, svc, context.Background(), "claude")
	if stale.State != "stale" || stale.Error == nil || stale.Error.Code != "provider_unavailable" || len(stale.Windows) != 1 || stale.Windows[0].Utilization != 18 || stale.LastSuccessAt == nil {
		t.Fatalf("transient failure result = %+v, want bounded stale snapshot", stale)
	}
	encoded, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"/private/provider", "secret-token", "transport failed"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("provider error leaked %q: %s", secret, encoded)
		}
	}

	currentTime = currentTime.Add(2 * time.Minute)
	expired := callProviderQuota(t, svc, context.Background(), "claude")
	if expired.State != "unavailable" || len(expired.Windows) != 0 || expired.LastSuccessAt != nil {
		t.Fatalf("expired last-success result = %+v, want unavailable without old data", expired)
	}
}

func TestProviderQuota_SnapshotExpiry(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	claudeHome := makeQuotaHome(t, "Claude")
	reader := &quotaReaderStub{claude: func(context.Context, string) (*usage.Response, error) {
		fetched := now.Add(-16 * time.Minute)
		return &usage.Response{FetchedAt: fetched, TokenFingerprint: "claude-account-a", FiveHour: &usage.UsageWindow{Utilization: 20, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)}}, nil
	}}
	svc := newProviderTestService(t, now, reader, claudeHome, "")
	got := callProviderQuota(t, svc, context.Background(), "claude")
	if got.State != "unavailable" || got.LastSuccessAt != nil || len(got.Windows) != 0 || got.Error == nil {
		t.Fatalf("expired successful snapshot = %+v, want unavailable without old data", got)
	}
}

func TestProviderQuotaAccountSwitchUsesNewIdentityEvenWithOlderSample(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	claudeHome := makeQuotaHome(t, "Claude")
	reads := 0
	reader := &quotaReaderStub{claude: func(context.Context, string) (*usage.Response, error) {
		reads++
		if reads == 1 {
			return &usage.Response{FetchedAt: now, TokenFingerprint: "claude-account-a", FiveHour: &usage.UsageWindow{Utilization: 71, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)}}, nil
		}
		return &usage.Response{FetchedAt: now.Add(-5 * time.Minute), TokenFingerprint: "claude-account-b", FiveHour: &usage.UsageWindow{Utilization: 19, ResetsAt: now.Add(2 * time.Hour).Format(time.RFC3339)}}, nil
	}}
	svc := newProviderTestService(t, now, reader, claudeHome, "")
	_ = callProviderQuota(t, svc, context.Background(), "claude")
	got := callProviderQuota(t, svc, context.Background(), "claude")
	if got.State != "fresh" || got.LastSuccessAt == nil || *got.LastSuccessAt != now.Add(-5*time.Minute).Format(time.RFC3339Nano) || len(got.Windows) != 1 || got.Windows[0].Utilization != 19 {
		t.Fatalf("account B response = %+v, want B data despite older sample time", got)
	}
}

func TestProviderQuotaFailureWithoutIdentityProofDoesNotReusePriorAccount(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	claudeHome := makeQuotaHome(t, "Claude")
	reads := 0
	reader := &quotaReaderStub{claude: func(context.Context, string) (*usage.Response, error) {
		reads++
		if reads == 1 {
			return &usage.Response{FetchedAt: now, TokenFingerprint: "claude-account-a", FiveHour: &usage.UsageWindow{Utilization: 63, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)}}, nil
		}
		return nil, &usage.RateLimitError{RetryAfter: time.Minute}
	}}
	svc := newProviderTestService(t, now, reader, claudeHome, "")
	_ = callProviderQuota(t, svc, context.Background(), "claude")
	got := callProviderQuota(t, svc, context.Background(), "claude")
	if got.State != "unavailable" || got.LastSuccessAt != nil || len(got.Windows) != 0 || got.Error == nil || got.Error.Code != "rate_limited" {
		t.Fatalf("unbound transient failure = %+v, want unavailable without prior account data", got)
	}
}

func TestProviderQuotaCodexAuthChangeRejectsCachedOldAccountSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	currentTime := now
	codexHome := makeQuotaHome(t, "Codex")
	writeCodexAuth(t, codexHome, `{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-account-a"}}`)
	reader := &quotaReaderStub{codex: func(context.Context, string) (*codexappserver.Snapshot, error) {
		return codexSnapshot(currentTime, "account-a@example.invalid", 12), nil
	}}
	svc := newProviderTestServiceWithClock(t, func() time.Time { return currentTime }, reader, "", codexHome)
	first := callProviderQuota(t, svc, context.Background(), "codex")
	if first.State != "fresh" || first.Windows[0].Utilization != 12 {
		t.Fatalf("initial Codex quota = %+v, want account A reading", first)
	}

	currentTime = currentTime.Add(time.Minute)
	reader.codex = func(context.Context, string) (*codexappserver.Snapshot, error) {
		return nil, errors.New("transport failed for /private/codex secret-token")
	}
	transient := callProviderQuota(t, svc, context.Background(), "codex")
	if transient.State != "stale" || transient.Error == nil || transient.Error.Code != "provider_unavailable" || transient.Windows[0].Utilization != 12 {
		t.Fatalf("same-auth transient result = %+v, want bounded stale account A reading", transient)
	}

	currentTime = currentTime.Add(time.Minute)
	writeCodexAuth(t, codexHome, `{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-account-b"}}`)
	reader.codex = func(context.Context, string) (*codexappserver.Snapshot, error) {
		return nil, errors.New("transport failed after credential replacement")
	}
	failedAfterSwitch := callProviderQuota(t, svc, context.Background(), "codex")
	if failedAfterSwitch.State != "unavailable" || failedAfterSwitch.LastSuccessAt != nil || len(failedAfterSwitch.Windows) != 0 {
		t.Fatalf("Codex failure after credential change reused old snapshot = %+v", failedAfterSwitch)
	}

	cachedOld := codexSnapshot(now, "account-a@example.invalid", 12)
	reader.codex = func(context.Context, string) (*codexappserver.Snapshot, error) { return cachedOld, nil }
	changed := callProviderQuota(t, svc, context.Background(), "codex")
	if changed.State != "unavailable" || changed.LastSuccessAt != nil || len(changed.Windows) != 0 || changed.Error == nil || changed.Error.Code != "account_changed" {
		t.Fatalf("Codex account change reused cached snapshot = %+v, want unavailable", changed)
	}

	currentTime = currentTime.Add(5 * time.Minute)
	reader.codex = func(context.Context, string) (*codexappserver.Snapshot, error) {
		return codexSnapshot(currentTime, "account-b@example.invalid", 28), nil
	}
	refreshed := callProviderQuota(t, svc, context.Background(), "codex")
	if refreshed.State != "fresh" || refreshed.Windows[0].Utilization != 28 {
		t.Fatalf("Codex refreshed account = %+v, want account B reading", refreshed)
	}
}

func TestProviderQuotaCodexAuthFingerprintRejectsFIFOBeforeOpen(t *testing.T) {
	const envName = "CCTRACE_TEST_CODEX_AUTH_FIFO"
	if fifoPath := os.Getenv(envName); fifoPath != "" {
		if _, err := codexAuthFingerprint(filepath.Dir(fifoPath)); err == nil {
			t.Fatal("Codex auth fingerprint accepted a FIFO")
		}
		return
	}

	home := makeQuotaHome(t, "Codex FIFO")
	fifoPath := filepath.Join(home, "auth.json")
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo is unavailable")
	}
	mkfifoCtx, cancelMkfifo := context.WithTimeout(context.Background(), time.Second)
	defer cancelMkfifo()
	mkfifoCommand := exec.CommandContext(mkfifoCtx, mkfifo, "-m", "600", fifoPath)
	mkfifoCommand.WaitDelay = 200 * time.Millisecond
	if err := mkfifoCommand.Run(); err != nil {
		t.Fatalf("create synthetic auth FIFO: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProviderQuotaCodexAuthFingerprintRejectsFIFOBeforeOpen$")
	cmd.Env = append(os.Environ(), envName+"="+fifoPath)
	cmd.WaitDelay = 200 * time.Millisecond
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatal("auth FIFO blocked before the credential file type check")
		}
		t.Fatalf("bounded FIFO witness process failed: %v", err)
	}
}

func TestProviderQuotaRateLimitRetainsSanitizedLastSuccess(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	claudeHome := makeQuotaHome(t, "Claude")
	reads := 0
	reader := &quotaReaderStub{claude: func(context.Context, string) (*usage.Response, error) {
		reads++
		if reads == 1 {
			return &usage.Response{FetchedAt: now, TokenFingerprint: "claude-account-a", FiveHour: &usage.UsageWindow{Utilization: 35, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)}}, nil
		}
		return &usage.Response{TokenFingerprint: "claude-account-a"}, &usage.RateLimitError{RetryAfter: time.Minute}
	}}
	svc := newProviderTestService(t, now, reader, claudeHome, "")
	_ = callProviderQuota(t, svc, context.Background(), "claude")
	got := callProviderQuota(t, svc, context.Background(), "claude")
	if got.State != "stale" || got.Error == nil || got.Error.Code != "rate_limited" || len(got.Windows) != 1 || got.Windows[0].Utilization != 35 {
		t.Fatalf("rate-limited result = %+v, want retained stale snapshot", got)
	}
}

func TestProviderQuotaUnconfiguredSelectedProviderDoesNotFallback(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	claudeHome := makeQuotaHome(t, "Claude")
	reader := &quotaReaderStub{codex: func(context.Context, string) (*codexappserver.Snapshot, error) {
		t.Fatal("unconfigured Codex provider fell back to reader")
		return nil, nil
	}}
	svc := newProviderTestService(t, now, reader, claudeHome, "")
	got := callProviderQuota(t, svc, context.Background(), "codex")
	if got.State != "unavailable" || got.LastSuccessAt != nil || len(got.Windows) != 0 || got.Error == nil || got.Error.Code != "provider_not_configured" {
		t.Fatalf("unconfigured provider result = %+v", got)
	}
	if len(reader.codexHomes) != 0 {
		t.Fatalf("unconfigured Codex reader calls = %v", reader.codexHomes)
	}
}

func TestProviderQuotaAuthenticationAndIdentityFailuresClearLastSuccess(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	claudeHome := makeQuotaHome(t, "Claude")
	reads := 0
	reader := &quotaReaderStub{claude: func(context.Context, string) (*usage.Response, error) {
		reads++
		if reads == 1 {
			return &usage.Response{FetchedAt: now, FiveHour: &usage.UsageWindow{Utilization: 54, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)}}, nil
		}
		if reads == 2 {
			return nil, errors.New("no OAuth token in /private/home access_token=secret")
		}
		if reads == 3 {
			return &usage.Response{FetchedAt: now, FiveHour: &usage.UsageWindow{Utilization: 33, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)}}, nil
		}
		return nil, usage.ErrAccountSwitch
	}}
	svc := newProviderTestService(t, now, reader, claudeHome, "")
	_ = callProviderQuota(t, svc, context.Background(), "claude")
	authFailure := callProviderQuota(t, svc, context.Background(), "claude")
	if authFailure.State != "unavailable" || authFailure.LastSuccessAt != nil || len(authFailure.Windows) != 0 || authFailure.Error == nil || authFailure.Error.Code != "authentication_required" {
		t.Fatalf("auth failure retained prior account data: %+v", authFailure)
	}
	encoded, err := json.Marshal(authFailure)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"/private/home", "access_token", "secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("authentication error leaked %q: %s", secret, encoded)
		}
	}

	_ = callProviderQuota(t, svc, context.Background(), "claude")
	identityFailure := callProviderQuota(t, svc, context.Background(), "claude")
	if identityFailure.State != "unavailable" || identityFailure.LastSuccessAt != nil || len(identityFailure.Windows) != 0 || identityFailure.Error == nil || identityFailure.Error.Code != "account_changed" {
		t.Fatalf("identity failure retained prior account data: %+v", identityFailure)
	}
}

func TestProviderQuotaFormatFailureIsSanitizedAndDoesNotInventZero(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	claudeHome := makeQuotaHome(t, "Claude")
	reader := &quotaReaderStub{claude: func(context.Context, string) (*usage.Response, error) {
		return nil, nil
	}}
	svc := newProviderTestService(t, now, reader, claudeHome, "")
	got := callProviderQuota(t, svc, context.Background(), "claude")
	if got.State != "unavailable" || got.LastSuccessAt != nil || len(got.Windows) != 0 || got.Error == nil || got.Error.Code != "invalid_provider_response" {
		t.Fatalf("malformed provider response = %+v", got)
	}
	if got.Error.Message == "" || strings.Contains(strings.ToLower(got.Error.Message), "secret") {
		t.Fatalf("unsafe format error message: %+v", got.Error)
	}
}

func TestProviderQuotaClassifiesFetchContextFormatErrors(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	claudeHome := makeQuotaHome(t, "Claude")
	reader := &quotaReaderStub{claude: func(context.Context, string) (*usage.Response, error) {
		return nil, errors.New("invalid usage response at /private/provider token=synthetic-secret")
	}}
	svc := newProviderTestService(t, now, reader, claudeHome, "")
	got := callProviderQuota(t, svc, context.Background(), "claude")
	if got.State != "unavailable" || got.Error == nil || got.Error.Code != "invalid_provider_response" {
		t.Fatalf("FetchContext format error = %+v, want sanitized invalid_provider_response", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"/private/provider", "synthetic-secret", "invalid usage response"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("format error leaked %q: %s", secret, encoded)
		}
	}
}

func TestProviderQuotaClassifiesFetchContextMissingAuthErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		message string
	}{
		{name: "credential file unavailable", message: "selected credential file unavailable"},
		{name: "selected home has no OAuth token", message: "no selected-home OAuth token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
			claudeHome := makeQuotaHome(t, "Claude")
			reader := &quotaReaderStub{claude: func(context.Context, string) (*usage.Response, error) {
				return nil, errors.New(test.message)
			}}
			svc := newProviderTestService(t, now, reader, claudeHome, "")
			got := callProviderQuota(t, svc, context.Background(), "claude")
			if got.State != "unavailable" || got.LastSuccessAt != nil || len(got.Windows) != 0 || got.Error == nil || got.Error.Code != "authentication_required" {
				t.Fatalf("FetchContext missing-auth error = %+v, want unavailable authentication_required", got)
			}
			if got.Error.Message != safeProviderErrorMessage("authentication_required") {
				t.Fatalf("FetchContext missing-auth message = %q, want sanitized text", got.Error.Message)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), test.message) {
				t.Fatalf("FetchContext error leaked raw text: %s", encoded)
			}
		})
	}
}

func TestProviderQuotaReadPassesDeadlineAndHonorsCancellationWithoutChildWork(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	claudeHome := makeQuotaHome(t, "Claude")
	started := make(chan struct{})
	reader := &quotaReaderStub{claude: func(ctx context.Context, _ string) (*usage.Response, error) {
		deadline, ok := ctx.Deadline()
		if !ok || deadline.After(time.Now().Add(30*time.Second)) {
			t.Errorf("provider reader deadline missing or unbounded: deadline=%v present=%v", deadline, ok)
		}
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	svc := newProviderTestService(t, now, reader, claudeHome, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan quotaResult, 1)
	go func() { done <- callProviderQuota(t, svc, ctx, "claude") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("provider reader did not start")
	}
	cancel()
	select {
	case got := <-done:
		if got.State != "unavailable" || got.Error == nil {
			t.Fatalf("cancelled read = %+v, want unavailable", got)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled provider reader did not return")
	}
	if len(reader.claudeHomes) != 1 {
		t.Fatalf("reader call count=%d, want one joined synchronous read", len(reader.claudeHomes))
	}
}

func makeQuotaHome(t *testing.T, name string) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func writeCodexAuth(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func codexSnapshot(fetchedAt time.Time, loginEmail string, usedPercent float64) *codexappserver.Snapshot {
	reset := fetchedAt.Add(time.Hour)
	return &codexappserver.Snapshot{
		FetchedAt:  fetchedAt,
		LoginEmail: loginEmail,
		Readings: []codexappserver.Reading{
			{LimitID: "codex", WindowMinutes: 300, UsedPercent: usedPercent, ResetsAt: &reset},
		},
	}
}

func newProviderTestService(t *testing.T, now time.Time, reader ProviderQuotaReader, claudeHome, codexHome string) *Service {
	t.Helper()
	return newProviderTestServiceWithClock(t, func() time.Time { return now }, reader, claudeHome, codexHome)
}

func newProviderTestServiceWithClock(t *testing.T, now func() time.Time, reader ProviderQuotaReader, claudeHome, codexHome string) *Service {
	t.Helper()
	service, err := New(Config{
		AppSupportRoot: filepath.Join(t.TempDir(), "app support"),
		ProviderMode:   true,
		ClaudeHome:     claudeHome,
		CodexHome:      codexHome,
		ProviderReader: reader,
		Now:            now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func callProviderQuota(t *testing.T, service *Service, ctx context.Context, provider string) quotaResult {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"provider": provider})
	if err != nil {
		t.Fatal(err)
	}
	response := service.Handle(ctx, Request{Version: ProtocolVersion, Kind: "request", ID: "provider-quota", Op: "quota.read", Payload: payload})
	if response.Error != nil {
		t.Fatalf("quota.read error = %+v", response.Error)
	}
	encoded, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result quotaResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
