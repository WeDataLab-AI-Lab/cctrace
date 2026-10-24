// demo-seed fills an empty cctrace server with a fictitious five-person team so
// the dashboard can be recorded for the public static demo
// (docs/planning/plan-static-demo-2026-10-24.md).
//
// It drives the production ingestion paths: transcripts written into each
// person's home are uploaded by the real `cctrace sync` client, Claude Code
// events and Codex metrics go over OTLP, and plan-limit readings go to
// /api/quota-samples. Run it once, against an empty server: OTLP rows carry no
// idempotency key, so a second run doubles every cost.
//
// Data older than the server's periodic windows (48h for rollups) only reaches
// the hourly charts through the one-time boot repairs, so the last step clears
// their markers; restart cctraced afterwards and wait for the repairs to finish.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var bootRepairs = []string{
	"usage_hourly_rollups_v2", "usage_hourly_rollups_weekly_v1", "coverage_measured_minutes_v1",
	"session_records_login_email_history_v1", "session_records_codex_login_email_v1",
	"session_overview_rollups_v1", "task_segment_facts_v1", "codex_segment_tool_counts_v1",
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "record":
			runRecord(os.Args[2:])
			return
		case "anchor":
			fmt.Println(workdayAnchor(time.Now()).Format(time.RFC3339))
			return
		case "settle":
			if err := settle(context.Background(), os.Getenv("DEMO_DATABASE_URL")); err != nil {
				log.Fatal(err)
			}
			return
		case "rollups":
			if err := rearmRollups(context.Background(), os.Getenv("DEMO_DATABASE_URL")); err != nil {
				log.Fatal(err)
			}
			return
		}
	}
	server := flag.String("server", "http://cctraced:8080", "dashboard and sync base URL")
	otlpHTTP := flag.String("otlp", "http://cctraced:4318", "OTLP/HTTP base URL")
	otlpGRPC := flag.String("otlp-grpc", "http://cctraced:4317", "OTLP endpoint written into each profile")
	client := flag.String("cctrace", "/opt/demo/cctrace", "cctrace client binary")
	root := flag.String("root", "/home", "where the personas' home directories are created")
	days := flag.Int("days", 28, "days of history before the anchor")
	seed := flag.Uint64("seed", 1, "random seed; the same seed and anchor give the same data")
	anchorFlag := flag.String("anchor", "", "end of the data, RFC3339 (default: now)")
	stats := flag.Bool("stats", false, "print the plan and exit without contacting the server")
	flag.Parse()

	anchor := time.Now().Truncate(time.Minute)
	if *anchorFlag != "" {
		t, err := time.Parse(time.RFC3339, *anchorFlag)
		if err != nil {
			log.Fatalf("-anchor: %v", err)
		}
		anchor = t
	}
	pl := buildPlan(*seed, anchor, *days)
	printStats(pl)
	if *stats {
		return
	}

	ctx := context.Background()
	adminEmail, adminPassword := os.Getenv("DEMO_ADMIN_EMAIL"), os.Getenv("DEMO_ADMIN_PASSWORD")
	dbURL := os.Getenv("DEMO_DATABASE_URL")
	if adminEmail == "" || adminPassword == "" || dbURL == "" {
		log.Fatal("DEMO_ADMIN_EMAIL, DEMO_ADMIN_PASSWORD and DEMO_DATABASE_URL are required")
	}
	db, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close(ctx)
	if err := seedCodexRates(ctx, db); err != nil {
		log.Fatalf("codex rates: %v", err)
	}

	tokens, err := provision(*server, adminEmail, adminPassword)
	if err != nil {
		log.Fatalf("provision: %v", err)
	}
	log.Printf("provisioned %d accounts", len(tokens))
	sentEvents := 0

	for i, p := range team {
		acct := personaAccounts(*seed, i)
		token := tokens[p.UserID]
		home, bytes, err := prepareHome(*root, p, acct, token, *server, *otlpGRPC, pl)
		if err != nil {
			log.Fatalf("%s home: %v", p.UserID, err)
		}
		log.Printf("%s: wrote %.1f MB of transcripts", p.UserID, float64(bytes)/1e6)

		events := claudeEvents(pl, p, acct, home)
		if err := sendClaudeEvents(*otlpHTTP, token, p, events); err != nil {
			log.Fatalf("%s otel events: %v", p.UserID, err)
		}
		if err := sendCodexMetrics(*otlpHTTP, token, acct.CodexAccount, codexMetrics(pl, p)); err != nil {
			log.Fatalf("%s codex metrics: %v", p.UserID, err)
		}
		sentEvents += len(events)
		log.Printf("%s: sent %d OTEL events", p.UserID, len(events))

		out, err := runSync(*client, home, *server)
		if err != nil {
			log.Fatalf("%s sync: %v\n%s", p.UserID, err, tail(out, 40))
		}
		log.Printf("%s: sync done\n%s", p.UserID, tail(out, 6))

		samples := quotaSamples(pl, p, i, acct)
		n, err := postQuotaSamples(*server, token, samples)
		if err != nil {
			log.Fatalf("%s quota: %v", p.UserID, err)
		}
		log.Printf("%s: %d quota samples (%d inserted)", p.UserID, len(samples), n)
	}

	// The receiver queues OTEL and a worker drains it about 100 rows a second; the
	// boot repairs must not run before the last row lands, or older rows miss the
	// hourly rollups for good.
	if err := waitForRows(ctx, db, "otel_events", sentEvents); err != nil {
		log.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM schema_backfills WHERE name = ANY($1)`, bootRepairs); err != nil {
		log.Fatalf("clear boot repair markers: %v", err)
	}
	log.Printf("done; restart cctraced so the boot repairs rebuild history")
}

// seedCodexRates writes the current OpenAI prices the server would have fetched
// itself. Its pricing fetch rejects the page today ("anchor babbage-002 missing"),
// which would leave every GPT-6 session at $0.
func seedCodexRates(ctx context.Context, db *pgx.Conn) error {
	for model, pr := range codexPrices {
		if _, err := db.Exec(ctx, `INSERT INTO codex_model_rates (model_prefix, input_rate, output_rate, cache_read_rate, effective_from, source)
			VALUES ($1, $2, $3, $4, '-infinity', 'demo-seed')
			ON CONFLICT (model_prefix, effective_from) DO UPDATE
			SET input_rate = EXCLUDED.input_rate, output_rate = EXCLUDED.output_rate,
			    cache_read_rate = EXCLUDED.cache_read_rate, source = EXCLUDED.source`,
			model, pr.in, pr.out, pr.cached); err != nil {
			return err
		}
	}
	return nil
}

// workdayAnchor is the demo's "now": 16:40 in Seoul on the latest weekday before
// t. The team is mid-afternoon then, so the opening view -- the last three hours
// -- shows people at work rather than a quiet night.
func workdayAnchor(t time.Time) time.Time {
	k := t.In(kst)
	a := time.Date(k.Year(), k.Month(), k.Day(), 16, 40, 0, 0, kst)
	for a.After(t) || a.Weekday() == time.Saturday || a.Weekday() == time.Sunday {
		a = a.AddDate(0, 0, -1)
	}
	return a
}

// rollupRepairs are the boot repairs that fold history into the hourly
// aggregates. The first boot after seeding runs them before it attributes Codex
// usage to its login (session_records_codex_login_email_v1), so the aggregates
// hold that usage under no account; cleared and run on a second boot, they read
// the attributed rows. A server that ingests as it goes never meets this: the
// periodic refresh catches the attribution within its 48-hour window.
var rollupRepairs = []string{"usage_hourly_rollups_v2", "usage_hourly_rollups_weekly_v1", "coverage_measured_minutes_v1"}

func rearmRollups(ctx context.Context, dbURL string) error {
	db, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer db.Close(ctx)
	_, err = db.Exec(ctx, `DELETE FROM schema_backfills WHERE name = ANY($1)`, rollupRepairs)
	return err
}

// settle waits, after cctraced restarts, until every boot repair the seeder
// cleared has run again.
func settle(ctx context.Context, dbURL string) error {
	db, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer db.Close(ctx)
	deadline := time.Now().Add(30 * time.Minute)
	for {
		var done int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM schema_backfills WHERE name = ANY($1)`, bootRepairs).Scan(&done); err != nil {
			return err
		}
		if done == len(bootRepairs) {
			log.Printf("boot repairs done")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d of %d boot repairs done after 30 minutes", done, len(bootRepairs))
		}
		log.Printf("%d of %d boot repairs done, waiting", done, len(bootRepairs))
		time.Sleep(10 * time.Second)
	}
}

func waitForRows(ctx context.Context, db *pgx.Conn, table string, want int) error {
	deadline := time.Now().Add(time.Hour)
	for {
		var n int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			return err
		}
		if n >= want {
			log.Printf("%s: %d rows stored", table, n)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: %d of %d rows stored after an hour", table, n, want)
		}
		log.Printf("%s: %d of %d rows stored, waiting", table, n, want)
		time.Sleep(15 * time.Second)
	}
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func printStats(pl *plan) {
	type agg struct {
		sessions, turns, requests, records int
		cost                               float64
		bytes                              int
	}
	per := map[string]*agg{}
	for _, s := range pl.Sessions {
		a := per[s.P.UserID+"/"+s.Agent]
		if a == nil {
			a = &agg{}
			per[s.P.UserID+"/"+s.Agent] = a
		}
		a.sessions++
		a.turns += len(s.Turns)
		for _, tr := range s.Turns {
			a.requests += len(tr.Requests)
			for _, req := range tr.Requests {
				a.cost += req.Cost
				a.records += 1 + len(req.Actions)
				if req.Say != "" && len(req.Actions) > 0 {
					a.records++
				}
			}
		}
		for _, bg := range s.Background {
			a.cost += bg.Cost
		}
		if s.Agent == "claude" {
			a.bytes += len(claudeTranscript("/home/"+s.P.Home, s))
		} else {
			a.bytes += len(codexRollout("/home/"+s.P.Home, s))
		}
	}
	keys := make([]string, 0, len(per))
	for k := range per {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var total agg
	fmt.Printf("anchor %s, %d days\n", pl.Anchor.In(kst).Format(time.RFC3339), pl.Days)
	fmt.Printf("%-26s %8s %6s %8s %8s %10s %9s\n", "persona/agent", "sessions", "turns", "requests", "records", "cost $", "jsonl MB")
	for _, k := range keys {
		a := per[k]
		fmt.Printf("%-26s %8d %6d %8d %8d %10.2f %9.1f\n", k, a.sessions, a.turns, a.requests, a.records, a.cost, float64(a.bytes)/1e6)
		total.sessions += a.sessions
		total.turns += a.turns
		total.requests += a.requests
		total.records += a.records
		total.cost += a.cost
		total.bytes += a.bytes
	}
	fmt.Printf("%-26s %8d %6d %8d %8d %10.2f %9.1f\n", "total", total.sessions, total.turns, total.requests, total.records, total.cost, float64(total.bytes)/1e6)
}
