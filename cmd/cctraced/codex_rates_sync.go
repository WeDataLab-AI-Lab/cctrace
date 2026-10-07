package main

import (
	"context"
	"log"
	"time"

	"cctrace/internal/codexrates"
	"cctrace/internal/store"
)

type codexRateTable struct {
	rates     map[string]codexrates.Rate
	effective map[string]time.Time
	source    string
	observed  time.Time
	reprice   bool
}

type codexRatesSync struct {
	now       func() time.Time
	missing   func(context.Context) ([]store.UnpricedModelKey, error)
	fetch     func(context.Context) (map[string]codexrates.Rate, string, error)
	changelog func(context.Context) ([]codexrates.ChangeEntry, error)
	upsert    func(context.Context, map[string]codexrates.Rate, map[string]time.Time, string, time.Time) (int, error)
	reprice   func(context.Context) error
	lastDaily time.Time
	pending   *codexRateTable
	// At most three accelerated sync attempts per (agent, raw model) during
	// this process lifetime. Keep exhausted keys even if usage disappears:
	// new usage must not reset them. Restart deliberately resets this map;
	// boot/daily confirmations are independent and do not consume this budget.
	fastAttempts map[store.UnpricedModelKey]int
}

func (s *codexRatesSync) run(ctx context.Context, ticks <-chan time.Time) {
	s.step(ctx, true)
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			s.step(ctx, false)
		}
	}
}

func (s *codexRatesSync) step(ctx context.Context, boot bool) {
	if ctx.Err() != nil {
		return
	}
	observed := s.now().UTC()
	daily := boot || observed.Sub(s.lastDaily) >= 24*time.Hour
	if s.pending != nil {
		if !s.apply(ctx) {
			return
		}
		// A DB outage may cross the daily deadline. Retain that deadline until
		// the accepted table is applied; then confirm the source once, not twice.
		if !daily {
			return
		}
	}
	if !daily {
		keys, err := s.missing(ctx)
		if err != nil {
			log.Printf("[codex-rates] check unpriced models: %v", err)
			return
		}
		eligible := map[store.UnpricedModelKey]bool{}
		for _, key := range keys {
			if s.fastAttempts[key] < 3 {
				eligible[key] = true
			}
		}
		if len(eligible) == 0 {
			return
		}
		if s.fastAttempts == nil {
			s.fastAttempts = map[store.UnpricedModelKey]int{}
		}
		// Charge only when making an external sync attempt, including HTTP,
		// parse and Accept failures; coalesce all eligible keys into one fetch.
		for key := range eligible {
			s.fastAttempts[key]++
		}
	}
	if daily {
		s.lastDaily = observed
	}
	rates, source, err := s.fetch(ctx)
	if err != nil {
		log.Printf("[codex-rates] fetch: %v", err)
		return
	}
	if err := codexrates.Accept(rates); err != nil {
		log.Printf("[codex-rates] rejected %d rows from %s: %v", len(rates), source, err)
		return
	}
	// Quote observation is after fetch/validation, not the tick time: a slow
	// response can cross UTC midnight into a newly published release day.
	observed = s.now().UTC()
	effective := map[string]time.Time{}
	entries, err := s.changelog(ctx)
	if err != nil {
		log.Printf("[codex-rates] changelog unavailable, dating any change from today: %v", err)
	}
	undated := 0
	for model := range rates {
		// This is the accepted table's observation day when no official date
		// matches, not an inferred release date. Freeze it even without a
		// changelog so a cached DB retry across midnight cannot move it.
		d, ok := codexrates.EffectiveDate(entries, model, observed)
		effective[model] = d
		if !ok {
			undated++
		}
	}
	log.Printf("[codex-rates] changelog: %d entries, %d of %d models undated", len(entries), undated, len(rates))
	s.pending = &codexRateTable{rates: rates, effective: effective, source: source, observed: observed}
	s.apply(ctx)
}

// Keep the accepted table only until the upsert succeeds. A failed reprice is
// left to the periodic full rebuild in main.go; retrying it here every tick
// would only repeat that rebuild and block the daily fetch.
func (s *codexRatesSync) apply(ctx context.Context) bool {
	table := s.pending
	changed, err := s.upsert(ctx, table.rates, table.effective, table.source, table.observed)
	if err != nil {
		// Commit may have succeeded before its acknowledgement failed. The
		// retry can report zero changed rates; history still needs a rebuild.
		table.reprice = true
		log.Printf("[codex-rates] upsert: %v", err)
		return false
	}
	table.reprice = table.reprice || changed > 0
	if changed > 0 {
		log.Printf("[codex-rates] %d of %d rates changed (source %s); repricing codex cost", changed, len(table.rates), table.source)
	}
	if table.reprice {
		if err := s.reprice(ctx); err != nil {
			log.Printf("[codex-rates] reprice: %v; deferring to periodic full rebuild", err)
		}
	}
	s.pending = nil
	return true
}
