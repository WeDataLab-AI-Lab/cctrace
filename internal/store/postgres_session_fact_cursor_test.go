package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Each test uses its own read-only transaction and no shared fixture tables.
func TestSessionFactCursorPreservesBatchBoundaryDuplicates(t *testing.T) {
	t.Parallel()
	s := acquireTestStore(t)
	ctx := context.Background()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for _, batch := range []int{1, 2, 1024} {
		var got []int
		err = scanSessionFactCursor(ctx, tx, `SELECT n FROM (VALUES ($1::int),($1::int),($1::int+1)) v(n) ORDER BY n`, []any{7}, batch, func(row pgx.Row) error {
			var n int
			if err := row.Scan(&n); err != nil {
				return err
			}
			got = append(got, n)
			return nil
		})
		if err != nil || !reflect.DeepEqual(got, []int{7, 7, 8}) {
			t.Fatalf("batch=%d rows=%v error=%v", batch, got, err)
		}
	}
	// The same FETCH text must not reuse the preceding cursor's result format.
	var text string
	var flag bool
	err = scanSessionFactCursor(ctx, tx, `SELECT $1::text,true`, []any{"next projection"}, 1024, func(row pgx.Row) error {
		return row.Scan(&text, &flag)
	})
	if err != nil || text != "next projection" || !flag {
		t.Fatalf("changed projection text=%q flag=%v error=%v", text, flag, err)
	}
}
