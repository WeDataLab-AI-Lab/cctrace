package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"cctrace/internal/codexrates"
	"cctrace/internal/store"
)

type rateSyncHarness struct {
	s                                        *codexRatesSync
	now                                      time.Time
	keys                                     []store.UnpricedModelKey
	listErr, fetchErr, upsertErr, repriceErr error
	invalid                                  bool
	fetches, upserts, reprices               int
	changed                                  int
}

func newRateSyncHarness(t *testing.T) *rateSyncHarness {
	t.Helper()
	h := &rateSyncHarness{now: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), changed: 1}
	h.s = &codexRatesSync{
		now:     func() time.Time { return h.now },
		missing: func(context.Context) ([]store.UnpricedModelKey, error) { return h.keys, h.listErr },
		fetch: func(context.Context) (map[string]codexrates.Rate, string, error) {
			h.fetches++
			if h.invalid {
				return map[string]codexrates.Rate{}, "test", nil
			}
			return map[string]codexrates.Rate{
				"gpt-3.5-turbo": {Input: .5, Output: 1.5},
				"gpt-4-0613":    {Input: 30, Output: 60},
				"babbage-002":   {Input: .4, Output: .4},
				"gpt-5.6-sol":   {Input: 4, Output: 20, CacheRead: .4},
				"gpt-5.6-luna":  {Input: .2, Output: 1.2, CacheRead: .02},
				"gpt-6-astra":   {Input: 10, Output: 50, CacheRead: 1},
				"gpt-6-sol":     {Input: 2, Output: 10, CacheRead: .2},
				"gpt-6.1-sol":   {Input: 2, Output: 10, CacheRead: .1},
			}, "test", h.fetchErr
		},
		changelog: func(context.Context) ([]codexrates.ChangeEntry, error) {
			return []codexrates.ChangeEntry{{Date: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC), Models: []string{"gpt-6.1-sol"}}}, nil
		},
		upsert: func(_ context.Context, rates map[string]codexrates.Rate, dates map[string]time.Time, _ string, _ time.Time) (int, error) {
			h.upserts++
			if rates["gpt-6.1-sol"].CacheRead != .1 || !dates["gpt-6.1-sol"].Equal(time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)) {
				t.Fatal("published model rate/date changed")
			}
			return h.changed, h.upsertErr
		},
		reprice: func(context.Context) error { h.reprices++; return h.repriceErr },
	}
	return h
}

func (h *rateSyncHarness) boot() { h.s.step(context.Background(), true) }
func (h *rateSyncHarness) tick(after time.Duration) {
	// Explicit time advancement, independent of how often production calls now().
	h.now = time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC).Add(after)
	h.s.step(context.Background(), false)
}

func assertFetches(t *testing.T, h *rateSyncHarness, want int) {
	t.Helper()
	if h.fetches != want {
		t.Fatalf("external sync attempts = %d, want %d", h.fetches, want)
	}
}

func TestCodexRatesSync_TC01PermanentAliasStopsAfterThreeFastAttempts(t *testing.T) {
	h := newRateSyncHarness(t)
	h.keys = []store.UnpricedModelKey{{Agent: "codex", Model: "gpt-reserve"}}
	h.boot()
	for i := 1; i <= 10; i++ {
		h.tick(time.Duration(i) * 5 * time.Minute)
	}
	assertFetches(t, h, 4) // boot is independent of the three accelerated attempts.
}

func TestCodexRatesSync_TC02NewUsageOrTemporaryAbsenceDoesNotResetBudget(t *testing.T) {
	h := newRateSyncHarness(t)
	key := store.UnpricedModelKey{Agent: "codex", Model: "gpt-reserve"}
	h.keys = []store.UnpricedModelKey{key}
	h.boot()
	for i := 1; i <= 3; i++ {
		h.tick(time.Duration(i) * 5 * time.Minute)
	}
	h.keys = nil
	h.tick(20 * time.Minute)
	h.keys = []store.UnpricedModelKey{key, key} // new rows still identify the same raw model.
	h.tick(25 * time.Minute)
	assertFetches(t, h, 4)
}

func TestCodexRatesSync_TC03NewKeyHasIndependentBudget(t *testing.T) {
	for _, key := range []store.UnpricedModelKey{
		{Agent: "codex", Model: "gpt-6.1-sol"},
		{Agent: "codex", Model: "GPT-RESERVE"},
		{Agent: "another-agent", Model: "gpt-reserve"},
	} {
		t.Run(key.Agent+"/"+key.Model, func(t *testing.T) {
			h := newRateSyncHarness(t)
			h.keys = []store.UnpricedModelKey{{Agent: "codex", Model: "gpt-reserve"}}
			h.boot()
			for i := 1; i <= 4; i++ {
				h.tick(time.Duration(i) * 5 * time.Minute)
			}
			h.keys = append(h.keys, key)
			h.tick(25 * time.Minute)
			assertFetches(t, h, 5)
		})
	}
}

func TestCodexRatesSync_TC04CoalescesAndDeduplicatesKeys(t *testing.T) {
	for _, many := range []bool{false, true} {
		t.Run(map[bool]string{false: "duplicate", true: "many"}[many], func(t *testing.T) {
			h := newRateSyncHarness(t)
			key := store.UnpricedModelKey{Agent: "codex", Model: "gpt-reserve"}
			h.keys = []store.UnpricedModelKey{key, key}
			if many {
				h.keys = append(h.keys, store.UnpricedModelKey{Agent: "codex", Model: "codex-auto-review"})
			}
			h.boot()
			for i := 1; i <= 5; i++ {
				h.tick(time.Duration(i) * 5 * time.Minute)
			}
			assertFetches(t, h, 4)
		})
	}
}

func TestCodexRatesSync_TC05DailyDeadlineIndependentOfFastAttempts(t *testing.T) {
	h := newRateSyncHarness(t)
	h.keys = []store.UnpricedModelKey{{Agent: "codex", Model: "old-alias"}}
	h.boot()
	for i := 1; i <= 3; i++ {
		h.tick(time.Duration(i) * 5 * time.Minute)
	}
	h.keys = append(h.keys, store.UnpricedModelKey{Agent: "codex", Model: "new-alias"})
	h.tick(24*time.Hour - 5*time.Minute)
	assertFetches(t, h, 5)
	h.tick(24 * time.Hour)
	assertFetches(t, h, 6) // due daily and an eligible fast key still cause one fetch.
	h.tick(48 * time.Hour)
	assertFetches(t, h, 7)
}

func TestCodexRatesSync_TC06RestartExplicitlyResetsProcessBudget(t *testing.T) {
	for process := 0; process < 2; process++ {
		h := newRateSyncHarness(t) // intentionally no persistence between processes.
		h.keys = []store.UnpricedModelKey{{Agent: "codex", Model: "gpt-reserve"}}
		h.boot()
		for i := 1; i <= 5; i++ {
			h.tick(time.Duration(i) * 5 * time.Minute)
		}
		assertFetches(t, h, 4)
	}
}

func TestCodexRatesSync_TC07NoKeysKeepsDailyAndCancellationStops(t *testing.T) {
	h := newRateSyncHarness(t)
	h.boot()
	h.tick(5 * time.Minute)
	h.tick(24 * time.Hour)
	assertFetches(t, h, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	closed := make(chan time.Time)
	close(closed)
	h.s.run(ctx, closed)
	assertFetches(t, h, 2)
	fresh := newRateSyncHarness(t)
	fresh.s.run(context.Background(), closed)
	assertFetches(t, fresh, 1) // an ordinarily closed tick channel exits after boot.
}

func TestCodexRatesSync_TC08CandidateDBFailureDoesNotSpendBudget(t *testing.T) {
	h := newRateSyncHarness(t)
	h.keys = []store.UnpricedModelKey{{Agent: "codex", Model: "gpt-reserve"}}
	h.boot()
	h.listErr = errors.New("DB unavailable")
	h.tick(5 * time.Minute)
	assertFetches(t, h, 1)
	h.listErr = nil
	for i := 2; i <= 5; i++ {
		h.tick(time.Duration(i) * 5 * time.Minute)
	}
	assertFetches(t, h, 4)
	h.listErr = errors.New("DB unavailable")
	h.tick(24 * time.Hour)
	assertFetches(t, h, 5) // unconditional daily confirmation is not the failed candidate query.
}

func TestCodexRatesSync_TC09ExternalFailuresSpendFastBudget(t *testing.T) {
	for _, failure := range []string{"HTTP", "parse", "Accept"} {
		t.Run(failure, func(t *testing.T) {
			h := newRateSyncHarness(t)
			h.keys = []store.UnpricedModelKey{{Agent: "codex", Model: "gpt-reserve"}}
			h.boot()
			if failure == "Accept" {
				h.invalid = true
			} else {
				h.fetchErr = errors.New(failure)
			}
			for i := 1; i <= 5; i++ {
				h.tick(time.Duration(i) * 5 * time.Minute)
			}
			assertFetches(t, h, 4)
			if h.upserts != 1 {
				t.Fatalf("invalid fetch applied %d tables", h.upserts)
			}
		})
	}
}

func TestCodexRatesSync_TC10UpsertFailureRetriesAcceptedTableWithoutFetch(t *testing.T) {
	h := newRateSyncHarness(t)
	h.upsertErr = errors.New("DB unavailable")
	h.boot()
	h.tick(5 * time.Minute)
	h.upsertErr = nil
	h.tick(10 * time.Minute)
	assertFetches(t, h, 1)
	if h.upserts != 3 || h.reprices != 1 {
		t.Fatalf("upsert/reprice = %d/%d, want 3/1", h.upserts, h.reprices)
	}
}

func TestCodexRatesSync_TC11RepriceFailureIsNotRetriedByRatesSync(t *testing.T) {
	h := newRateSyncHarness(t)
	h.repriceErr = errors.New("DB unavailable")
	h.boot()
	h.tick(5 * time.Minute)
	h.repriceErr = nil
	h.tick(10 * time.Minute)
	assertFetches(t, h, 1)
	if h.upserts != 1 || h.reprices != 1 {
		t.Fatalf("upsert/reprice = %d/%d, want 1/1", h.upserts, h.reprices)
	}
}

func TestCodexRatesSync_RepriceFailureDefersToPeriodicRebuildAndKeepsDaily(t *testing.T) {
	h := newRateSyncHarness(t)
	h.repriceErr = errors.New("DB unavailable")
	h.boot()
	h.tick(5 * time.Minute)
	h.tick(10 * time.Minute)
	h.tick(24 * time.Hour)
	assertFetches(t, h, 2)
	if h.upserts != 2 || h.reprices != 2 {
		t.Fatalf("upsert/reprice = %d/%d, want 2/2", h.upserts, h.reprices)
	}
}

func TestCodexRatesSync_TC12PendingDBKeepsDailyDeadlineAndFastBudget(t *testing.T) {
	h := newRateSyncHarness(t)
	h.keys = []store.UnpricedModelKey{{Agent: "codex", Model: "gpt-reserve"}}
	h.boot()
	h.tick(5 * time.Minute)
	h.tick(10 * time.Minute)
	h.upsertErr = errors.New("DB unavailable")
	h.tick(15 * time.Minute) // third fast attempt fetched a valid table, DB now blocked.
	h.keys = nil             // no unpriced keys left: only the overdue daily deadline, held behind the pending DB application, can trigger a fetch.
	h.tick(24 * time.Hour)
	assertFetches(t, h, 4) // do not refetch a table whose DB application is still pending.
	h.upsertErr = nil
	h.tick(24*time.Hour + 5*time.Minute)
	assertFetches(t, h, 5) // overdue daily confirmation is retained, not reset by the failed DB retry.
	h.keys = []store.UnpricedModelKey{{Agent: "codex", Model: "gpt-reserve"}}
	h.tick(24*time.Hour + 10*time.Minute)
	assertFetches(t, h, 5) // recovery did not reset this key's exhausted fast budget.
}

func TestCodexRatesSync_TC22AcceptedFallbackDateSurvivesMidnightDBRetry(t *testing.T) {
	h := newRateSyncHarness(t)
	h.now = h.now.Add(23*time.Hour + 59*time.Minute)
	h.s.changelog = func(context.Context) ([]codexrates.ChangeEntry, error) {
		return nil, errors.New("changelog unavailable")
	}
	var dates []time.Time
	h.upsertErr = errors.New("DB unavailable")
	h.s.upsert = func(_ context.Context, _ map[string]codexrates.Rate, effective map[string]time.Time, _ string, observed time.Time) (int, error) {
		if observed.Format("2006-01-02") != "2026-09-30" {
			t.Errorf("accepted observation changed to %v", observed)
		}
		dates = append(dates, effective["gpt-6.1-sol"])
		return 1, h.upsertErr
	}
	h.boot()
	h.upsertErr = nil
	h.tick(24*time.Hour + 5*time.Minute)
	assertFetches(t, h, 1)
	if len(dates) != 2 {
		t.Fatalf("upsert dates = %v, want two attempts", dates)
	}
	for _, date := range dates {
		if date.Format("2006-01-02") != "2026-09-30" {
			t.Errorf("fallback date = %v, want original observation day 2026-09-30 (not inferred release)", date)
		}
	}
}

func TestCodexRatesSync_TC24AmbiguousUpsertOutcomeStillRepricesOnRetry(t *testing.T) {
	h := newRateSyncHarness(t)
	calls := 0
	h.s.upsert = func(context.Context, map[string]codexrates.Rate, map[string]time.Time, string, time.Time) (int, error) {
		calls++
		if calls == 1 {
			// A durable commit can lose its acknowledgement. The retry sees the
			// stored rates unchanged, not proof that history was ever repriced.
			return 0, errors.New("commit acknowledgement lost")
		}
		return 0, nil
	}
	h.boot()
	if h.reprices != 0 {
		t.Fatal("repriced before upsert recovered")
	}
	h.tick(5 * time.Minute)
	assertFetches(t, h, 1)
	if calls != 2 || h.reprices != 1 {
		t.Fatalf("upsert/reprice = %d/%d, want 2/1", calls, h.reprices)
	}
}

func TestCodexRatesSync_TC25ObservationAfterAcceptedFetchCrossesReleaseMidnight(t *testing.T) {
	h := newRateSyncHarness(t)
	h.now = time.Date(2026, 9, 28, 23, 59, 0, 0, time.UTC)
	fetch := h.s.fetch
	h.s.fetch = func(ctx context.Context) (map[string]codexrates.Rate, string, error) {
		rates, source, err := fetch(ctx)
		h.now = time.Date(2026, 9, 29, 0, 1, 0, 0, time.UTC) // source response arrived after midnight.
		return rates, source, err
	}
	h.s.upsert = func(_ context.Context, _ map[string]codexrates.Rate, dates map[string]time.Time, _ string, observed time.Time) (int, error) {
		if observed.Format("2006-01-02") != "2026-09-29" || dates["gpt-6.1-sol"].Format("2006-01-02") != "2026-09-29" {
			t.Errorf("accepted observation/release = %v/%v, want published release day 2026-09-29", observed, dates["gpt-6.1-sol"])
		}
		return 1, nil
	}
	h.boot()
	assertFetches(t, h, 1)
}

func TestCodexRatesSync_TC26UnchangedConfirmationDoesNotReprice(t *testing.T) {
	h := newRateSyncHarness(t)
	h.changed = 0
	h.boot()
	h.tick(5 * time.Minute)
	assertFetches(t, h, 1)
	if h.upserts != 1 || h.reprices != 0 {
		t.Fatalf("upsert/reprice = %d/%d, want 1/0", h.upserts, h.reprices)
	}
}
