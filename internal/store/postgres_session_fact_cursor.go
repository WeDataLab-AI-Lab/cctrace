package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const sessionFactBatchRows = 1024

// scanSessionFactCursor keeps one snapshot and bounds each FETCH statement.
// A cursor preserves duplicate source keys across batches; keyset pagination
// would silently drop them. The callback borrows only the current row.
func scanSessionFactCursor(ctx context.Context, tx pgx.Tx, query string, args []any, batch int, visit func(pgx.Row) error) error {
	if batch < 1 {
		return fmt.Errorf("session fact batch must be positive")
	}
	const cursor = "session_fact_input"
	if _, err := tx.Exec(ctx, "DECLARE "+cursor+" NO SCROLL CURSOR FOR "+query, args...); err != nil {
		return fmt.Errorf("declare session fact cursor: %w", err)
	}
	defer func() { _, _ = tx.Exec(ctx, "CLOSE "+cursor) }()
	fetch := fmt.Sprintf("FETCH FORWARD %d FROM %s", batch, cursor)
	for {
		// FETCH has the same SQL across source phases but a different row
		// description. A cached description would bind Claude's formats to a
		// head/event query and abort the snapshot transaction.
		rows, err := tx.Query(ctx, fetch, pgx.QueryExecModeDescribeExec)
		if err != nil {
			return fmt.Errorf("fetch session fact batch: %w", err)
		}
		n := 0
		for rows.Next() {
			if err := visit(rows); err != nil {
				rows.Close()
				return err
			}
			n++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fmt.Errorf("read session fact batch: %w", err)
		}
		if n < batch {
			return nil
		}
	}
}
