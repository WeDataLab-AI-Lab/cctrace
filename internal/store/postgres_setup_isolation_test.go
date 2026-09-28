package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCreateInitialDashboardUserIsolation(t *testing.T) {
	s := acquireTestStore(t)
	for _, isolation := range []string{"read committed", "repeatable read"} {
		t.Run(isolation, func(t *testing.T) {
			truncateTables(t, s)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cfg := s.pool.Config()
			cfg.ConnConfig.RuntimeParams["default_transaction_isolation"] = isolation
			p, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			probe := &PgStore{pool: p}
			blocker, err := s.pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Release()
			if _, err = blocker.Exec(ctx, `SELECT pg_advisory_lock(4846816632899917136)`); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = blocker.Exec(context.Background(), `SELECT pg_advisory_unlock(4846816632899917136)`) }()
			results := make(chan error, 2)
			for _, email := range []string{"probe1@example.com", "probe2@example.com"} {
				go func() {
					results <- probe.CreateInitialDashboardUser(ctx, &DashboardUser{Email: email, PasswordHash: "hash", Role: "admin"})
				}()
			}
			var waiters int
			for {
				err = blocker.QueryRow(ctx, `SELECT count(DISTINCT pid) FROM pg_locks WHERE locktype='advisory' AND NOT granted`).Scan(&waiters)
				if err != nil {
					t.Fatal(err)
				}
				if waiters == 2 {
					break
				}
				if ctx.Err() != nil {
					t.Fatal(ctx.Err())
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Logf("isolation=%s independent waiting connections=%d", isolation, waiters)
			if _, err = blocker.Exec(ctx, `SELECT pg_advisory_unlock(4846816632899917136)`); err != nil {
				t.Fatal(err)
			}
			successes, conflicts := 0, 0
			for range 2 {
				err = <-results
				if err == nil {
					successes++
				} else if errors.Is(err, ErrSetupAlreadyCompleted) {
					conflicts++
				} else {
					t.Errorf("unexpected error: %v", err)
				}
			}
			count, err := s.CountDashboardUsers(ctx)
			t.Logf("successes=%d conflicts=%d users=%d err=%v", successes, conflicts, count, err)
			if successes != 1 || conflicts != 1 || count != 1 || err != nil {
				t.Fail()
			}
		})
	}
}
