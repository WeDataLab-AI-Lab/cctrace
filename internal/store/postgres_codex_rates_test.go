package store

import (
	"context"
	"testing"
	"time"

	"cctrace/internal/codexrates"
)

func codexRate(t *testing.T, s *PgStore, prefix string) (in, out, cache float64, source string, fetchedAt *time.Time) {
	t.Helper()
	err := s.pool.QueryRow(context.Background(),
		`SELECT input_rate, output_rate, cache_read_rate, source, fetched_at
		FROM codex_model_rates WHERE model_prefix = $1`, prefix,
	).Scan(&in, &out, &cache, &source, &fetchedAt)
	if err != nil {
		t.Fatalf("read rate %s: %v", prefix, err)
	}
	return
}

// The published table drops models when they are retired. Deleting the rows it
// no longer lists would reprice every historical Codex row on those models to
// $0, so the upsert never deletes.
func TestUpsertCodexModelRates_KeepsRowsAbsentFromTheFetch(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	const fake = "zz-codexrates-test"
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DELETE FROM codex_model_rates WHERE model_prefix = $1`, fake)
	})

	var before int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM codex_model_rates`).Scan(&before); err != nil {
		t.Fatalf("count: %v", err)
	}

	n, err := s.UpsertCodexModelRates(ctx, map[string]codexrates.Rate{
		fake: {Input: 1.5, Output: 9, CacheRead: 0.15},
	}, nil, "openai-md")
	if err != nil {
		t.Fatalf("UpsertCodexModelRates: %v", err)
	}
	if n != 1 {
		t.Errorf("changed rows = %d, want 1", n)
	}

	var after int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM codex_model_rates`).Scan(&after); err != nil {
		t.Fatalf("count: %v", err)
	}
	if after != before+1 {
		t.Errorf("row count went %d -> %d; the upsert removed rows the fetch did not list", before, after)
	}
	// The seeded rows are still exactly as migrations left them.
	in, out, cache, source, _ := codexRate(t, s, "gpt-5-codex")
	if in != 1.25 || out != 10 || cache != 0.125 || source != "seed" {
		t.Errorf("seeded gpt-5-codex = %v/%v/%v from %q, want 1.25/10/0.125 from \"seed\"", in, out, cache, source)
	}
}

// fetched_at means "last confirmed", not "last changed". A row whose price the
// sync confirms unchanged every day must still show a fresh timestamp,
// otherwise max(fetched_at) cannot answer "is the sync running at all" -- and
// rows that have always matched the seed would look untouched forever.
func TestUpsertCodexModelRates_ConfirmsUnchangedRows(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	const fake = "zz-codexrates-test-3"
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DELETE FROM codex_model_rates WHERE model_prefix = $1`, fake)
	})

	rates := map[string]codexrates.Rate{fake: {Input: 2, Output: 12, CacheRead: 0.2}}
	if _, err := s.UpsertCodexModelRates(ctx, rates, nil, "openai-html"); err != nil {
		t.Fatalf("UpsertCodexModelRates: %v", err)
	}
	_, _, _, _, first := codexRate(t, s, fake)
	if first == nil {
		t.Fatal("fetched_at is NULL after the first sync")
	}

	changed, err := s.UpsertCodexModelRates(ctx, rates, nil, "openai-md")
	if err != nil {
		t.Fatalf("UpsertCodexModelRates (repeat): %v", err)
	}
	if changed != 0 {
		t.Errorf("changed rows = %d on an unchanged table, want 0", changed)
	}
	_, _, _, source, second := codexRate(t, s, fake)
	if second == nil || !second.After(*first) {
		t.Errorf("fetched_at %v did not advance past %v on a confirming sync", second, first)
	}
	if source != "openai-md" {
		t.Errorf("source = %q, want openai-md -- the confirming source must be recorded", source)
	}
}

func TestUpsertCodexModelRates_RecordsProvenanceAndOnlyCountsChanges(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	const fake = "zz-codexrates-test-2"
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DELETE FROM codex_model_rates WHERE model_prefix = $1`, fake)
	})

	rates := map[string]codexrates.Rate{fake: {Input: 2, Output: 12, CacheRead: 0.2}}
	if _, err := s.UpsertCodexModelRates(ctx, rates, nil, "openai-astro"); err != nil {
		t.Fatalf("UpsertCodexModelRates: %v", err)
	}
	in, out, cache, source, fetchedAt := codexRate(t, s, fake)
	if in != 2 || out != 12 || cache != 0.2 {
		t.Errorf("rate = %v/%v/%v, want 2/12/0.2", in, out, cache)
	}
	if source != "openai-astro" || fetchedAt == nil {
		t.Errorf("source = %q fetched_at = %v, want \"openai-astro\" and a timestamp", source, fetchedAt)
	}

	// A daily sync that finds no price change must report nothing changed, so the
	// caller does not rebuild codex_imputed_cost every day for nothing.
	n, err := s.UpsertCodexModelRates(ctx, rates, nil, "openai-astro")
	if err != nil {
		t.Fatalf("UpsertCodexModelRates (repeat): %v", err)
	}
	if n != 0 {
		t.Errorf("changed rows = %d on an unchanged table, want 0", n)
	}

	rates[fake] = codexrates.Rate{Input: 3, Output: 12, CacheRead: 0.2}
	if n, err = s.UpsertCodexModelRates(ctx, rates, nil, "openai-md"); err != nil {
		t.Fatalf("UpsertCodexModelRates (changed): %v", err)
	}
	if n != 1 {
		t.Errorf("changed rows = %d after a price change, want 1", n)
	}
}

func TestUpsertCodexModelRates_Empty(t *testing.T) {
	s := acquireTestStore(t)
	n, err := s.UpsertCodexModelRates(context.Background(), nil, nil, "openai-md")
	if err != nil || n != 0 {
		t.Fatalf("UpsertCodexModelRates(nil) = %d, %v; want 0, nil", n, err)
	}
}
