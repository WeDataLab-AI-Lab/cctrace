package store

import (
	"context"
	"time"
)

// LatestEventTime backs /health. It reads unified_events, not visible_events:
// this is an ingest-liveness signal, so an excluded account's traffic still counts
// as "data is arriving" — and the filtered view loses the min/max index shortcut,
// turning a 4-buffer index probe into a full scan of both tables.
func (s *PgStore) LatestEventTime(ctx context.Context) (*time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx, `SELECT MAX(ts) FROM unified_events`).Scan(&t)
	if err != nil {
		return nil, err
	}
	return t, nil
}
