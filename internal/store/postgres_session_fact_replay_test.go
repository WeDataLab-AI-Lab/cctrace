package store

import (
	"context"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cctrace/internal/sessionfacts"
)

// Explicit measurement only: build this package's test binary, pipe a DSN on
// stdin, and run -test.run=^$ -test.bench=^BenchmarkSessionFactReadOnlyReplay$
// -test.benchtime=1x. The connection enforces read-only transactions; this path
// never migrates, enqueues, runs a worker, or publishes. No payload is logged.
// CCTRACE_REPLAY_MODE=full selects the unbounded reference for an RSS comparison.
func BenchmarkSessionFactReadOnlyReplay(b *testing.B) {
	if os.Getenv("CCTRACE_REPLAY_DSN_STDIN") != "1" || b.N != 1 {
		b.Fatal("explicit stdin DSN and -benchtime=1x required")
	}
	dsn, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		b.Fatal("read DSN")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(strings.TrimSpace(string(dsn)))
	clear(dsn)
	if err != nil {
		b.Fatal("parse replay database")
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		b.Fatal("connect read-only replay database")
	}
	s := &PgStore{pool: pool}
	defer s.Close() //nolint:errcheck
	var readOnly string
	if err := s.pool.QueryRow(ctx, `SHOW default_transaction_read_only`).Scan(&readOnly); err != nil || readOnly != "on" {
		b.Fatal("read-only replay connection required")
	}
	id, agent := os.Getenv("CCTRACE_REPLAY_SESSION"), os.Getenv("CCTRACE_REPLAY_AGENT")
	if id == "" || (agent != "claude" && agent != "codex") {
		b.Fatal("CCTRACE_REPLAY_SESSION and CCTRACE_REPLAY_AGENT required")
	}
	runtime.GC()
	b.ResetTimer()
	started := time.Now()
	var load SessionFactLoad
	var out sessionfacts.TimelineCommitOutput
	if os.Getenv("CCTRACE_REPLAY_MODE") == "full" {
		load, out, err = replayFullSessionFact(ctx, s, id, agent)
	} else {
		load, out, err = s.computeSessionFactReadSnapshot(ctx, id, agent, false)
	}
	b.StopTimer()
	if err != nil {
		b.Fatal(err)
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	b.ReportMetric(float64(len(load.LoadedKeys)), "rows")
	b.ReportMetric(float64(mem.TotalAlloc), "allocated-bytes")
	b.Logf("mode=%s elapsed=%s coverage=%s rules=%d rows=%d turns=%d timeline=%d hash=%x timeline_hash=%x heap=%d sys=%d",
		os.Getenv("CCTRACE_REPLAY_MODE"), time.Since(started), out.Session.Coverage, out.Session.RulesVersion,
		len(load.LoadedKeys), len(out.Turns), len(out.Timeline.Rows), out.Hash, out.TimelineHash, mem.HeapAlloc, mem.Sys)
}

func replayFullSessionFact(ctx context.Context, s *PgStore, id, agent string) (load SessionFactLoad, out sessionfacts.TimelineCommitOutput, err error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return load, out, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err = tx.Exec(ctx, sessionFactStatementLimit); err != nil {
		return load, out, err
	}
	load.Input.Agent = agent
	if err = loadSessionFactSnapshotMetadata(ctx, tx, id, &load, func() {}, false); err != nil {
		return load, out, err
	}
	calls := map[sessionFactToolCallKey]bool{}
	err = scanSessionFactSource(ctx, tx, id, agent, func(_ int64, row sessionfacts.ClaudeRow) error {
		load.Input.Rows = append(load.Input.Rows, row)
		load.LoadedKeys = append(load.LoadedKeys, SessionTimelineKey{Timestamp: row.TS, UUID: row.UUID, RecordID: row.ID, RecordType: row.RecordType, ProfileEmail: row.ProfileEmail})
		addSessionFactClaudeToolCalls(row, calls)
		return nil
	}, func(_ int64, row sessionfacts.CodexRow) error {
		load.Input.CodexRows = append(load.Input.CodexRows, row)
		load.LoadedKeys = append(load.LoadedKeys, SessionTimelineKey{Timestamp: row.TS, UUID: row.UUID, RecordID: row.ID, RecordType: row.RecordType, ProfileEmail: row.ProfileEmail})
		addSessionFactCodexToolCalls(row, calls)
		return nil
	})
	if err != nil {
		return load, out, err
	}
	if err = scanSessionFactSelectedResults(ctx, tx, id, agent, calls, func(r sessionfacts.CommitResult) error {
		load.Input.CommitResults = append(load.Input.CommitResults, r)
		return nil
	}); err != nil {
		return load, out, err
	}
	if err = scanSessionFactHeadEvidence(ctx, tx, id, agent, func(h sessionfacts.Head) error {
		load.Input.Heads = append(load.Input.Heads, h)
		return nil
	}); err != nil {
		return load, out, err
	}
	if err = scanSessionFactEventEvidence(ctx, tx, id, func(e sessionfacts.OTELRow) error {
		load.Input.OTEL = append(load.Input.OTEL, e)
		return nil
	}); err != nil {
		return load, out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return load, out, err
	}
	return load, sessionfacts.ComputeWithTimelineAndCommits(load.Input), nil
}
