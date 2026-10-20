package store

import (
	"context"
	"testing"
	"time"
)

// Other fixtures truncate these shared tables, so this test is not parallel.
func TestPersonalOverviewWithoutStoredValuesAvoidsSourceBarrier(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	writer, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx) //nolint:errcheck
	if err := lockSessionOverviewSourceMutation(ctx, writer, true); err != nil {
		t.Fatal(err)
	}
	read, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	values, err := s.PersonalSessionOverviewValues(read, 42, "owner@example.com", "owner", []string{"no-private-values"})
	if err != nil || len(values) != 0 {
		t.Fatalf("a page without stored private values waited for source work: values=%v error=%v", values, err)
	}
}
