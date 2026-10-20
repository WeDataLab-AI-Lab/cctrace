package main

import (
	"context"
	"log"
	"os"
	"time"

	"cctrace/internal/store"
)

func sessionAnalysisEnabled() bool {
	return os.Getenv("CCTRACE_SESSION_ANALYSIS") == "on"
}

func runSessionFactWorker(ctx context.Context, pg *store.PgStore) {
	pg.RunSessionFactWorker(ctx, time.After, func(run store.SessionFactRun, err error) {
		if err == nil {
			log.Printf("[session-facts] session=%q outcome=%s compute_ms=%d", run.SessionID, run.Outcome, run.ComputeMS)
		} else {
			log.Printf("[session-facts] session=%q outcome=%s attempts=%d: %v", run.SessionID, run.Outcome, run.Attempts, err)
		}
	})
}
