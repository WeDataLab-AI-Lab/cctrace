package main

import (
	"context"
	"errors"
	"log"
	"time"

	"cctrace/internal/store"
)

func runSessionTraceWorker(ctx context.Context, pg *store.PgStore) {
	runTraceWorker(ctx, pg.RunTraceChunk, pg.TraceWake(), time.After, log.Printf)
}
func runTraceWorker(ctx context.Context, run func(context.Context) (bool, error), wake <-chan struct{}, after func(time.Duration) <-chan time.Time, report func(string, ...any)) {
	for ctx.Err() == nil {
		worked, err := run(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil && !errors.Is(err, store.ErrTraceBusy) {
			report("[session-trace] bounded chunk: %v", err)
		}
		if worked && err == nil {
			continue
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-after(250 * time.Millisecond):
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		}

	}
}
