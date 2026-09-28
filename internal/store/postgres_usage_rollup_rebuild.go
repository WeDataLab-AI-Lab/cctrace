package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// usageRebuildDB is what the rebuild queue is written through: the pool, or the
// transaction of the exclusion change that owes the rebuild.
type usageRebuildDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// earliestUsageOf is the earliest usage row of the given addresses and billing
// accounts: where a rebuild of usage_hourly_rollups has to start for a change in
// their visibility to reach every bucket. The timer only ever refreshes a recent
// window, so without that rebuild an exclusion left every older bucket at its
// pre-exclusion total: a personal account's cost from ten days earlier stayed on
// the trend chart (#715). nil when they have no usage.
//
// Nothing indexes unified_events by billing account, so this is a full scan:
// only the worker calls it, outside any lock.
func earliestUsageOf(ctx context.Context, db usageRebuildDB, emails []string, accounts []BillingAccountRef) (*time.Time, error) {
	if len(emails) == 0 && len(accounts) == 0 {
		return nil, nil
	}
	providers := make([]string, 0, len(accounts))
	ids := make([]string, 0, len(accounts))
	for _, a := range accounts {
		providers = append(providers, a.BillingProvider)
		ids = append(ids, a.AccountID)
	}
	var earliest *time.Time
	if err := db.QueryRow(ctx, `
		SELECT min(ts) FROM unified_events
		WHERE lower(login_email) = ANY($1)
		   OR (billing_provider, account_id) IN (SELECT * FROM unnest($2::text[], $3::text[]))`,
		emails, providers, ids).Scan(&earliest); err != nil {
		return nil, err
	}
	return earliest, nil
}

// requestUsageRollupRebuild records that the usage of these addresses and billing
// accounts has to be rebuilt. Requests merge into the one row as a union; each
// takes a new generation, so a run that started before it does not clear it
// (see RunPendingUsageRollupRebuild).
func requestUsageRollupRebuild(ctx context.Context, db usageRebuildDB, emails []string, accounts []BillingAccountRef) error {
	if len(emails) == 0 && len(accounts) == 0 {
		return nil
	}
	lowered := make([]string, 0, len(emails))
	for _, e := range emails {
		lowered = append(lowered, normalizeLoginEmail(e))
	}
	if accounts == nil {
		accounts = []BillingAccountRef{}
	}
	encoded, err := json.Marshal(accounts)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		INSERT INTO usage_rollup_rebuild_requests AS r (id, emails, accounts)
		VALUES (1, ARRAY(SELECT DISTINCT e FROM unnest($1::text[]) e), (SELECT COALESCE(jsonb_agg(DISTINCT a), '[]') FROM jsonb_array_elements($2::jsonb) a))
		ON CONFLICT (id) DO UPDATE SET
			emails = ARRAY(SELECT DISTINCT e FROM unnest(r.emails || EXCLUDED.emails) e),
			accounts = (SELECT COALESCE(jsonb_agg(DISTINCT a), '[]') FROM jsonb_array_elements(r.accounts || EXCLUDED.accounts) a),
			generation = nextval('usage_rollup_rebuild_generation')`,
		lowered, string(encoded))
	return err
}

// UsageRollupRebuildPending reports whether an exclusion change is still waiting
// for its usage rebuild, so the screens that make those changes can say the
// charts have not caught up yet.
func (s *PgStore) UsageRollupRebuildPending(ctx context.Context) (bool, error) {
	var pending bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM usage_rollup_rebuild_requests)`).Scan(&pending)
	return pending, err
}

// RunPendingUsageRollupRebuild runs the queued usage rebuild, if there is one,
// and reports whether it ran. The server's worker calls it with its own
// lifetime context, so a rebuild no longer dies with the request that asked for
// it, and a request left by a restart is picked up by the next run.
//
// Where to start is found first, outside any lock: it is a full scan. Then the
// writers' lock is only tried. Every instance runs this worker, and one already
// rebuilding holds that lock for ~100s on production data; waiting for it would
// only repeat the same rebuild after it. The request is read again under the
// lock, and a run that finds it gone -- another instance finished it -- stops.
//
// The request is cleared whole, and only if its generation is still the one
// read before the rebuild. A request that arrived meanwhile may name an identity
// again, and its change may have committed after the rebuild read the rows; its
// identities cannot be told apart from the ones this run covered, so the whole
// row stays for the next run. Rebuilding an identity twice costs time; clearing
// one too early leaves wrong totals for good.
func (s *PgStore) RunPendingUsageRollupRebuild(ctx context.Context) (bool, error) {
	var emails []string
	var rawAccounts []byte
	var generation int64
	err := s.pool.QueryRow(ctx,
		`SELECT emails, accounts, generation FROM usage_rollup_rebuild_requests WHERE id = 1`).Scan(&emails, &rawAccounts, &generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var accounts []BillingAccountRef
	if err := json.Unmarshal(rawAccounts, &accounts); err != nil {
		return false, fmt.Errorf("decode usage rebuild request: %w", err)
	}
	from, err := earliestUsageOf(ctx, s.pool, emails, accounts)
	// A request names no one only when a build before release queued it by start
	// time; which start is lost with that column, so rebuild everything.
	full := len(emails) == 0 && len(accounts) == 0
	if err != nil {
		if ctx.Err() != nil {
			return false, err
		}
		// Without a start, only a full rebuild is sure to reach every bucket.
		log.Printf("[usage-rollup] earliest row of queued identities: %v; rebuilding everything", err)
		from, full = nil, true
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	var locked bool
	if err := tx.QueryRow(ctx,
		`SELECT pg_try_advisory_xact_lock(hashtext('usage_hourly_rollups_writers'))`).Scan(&locked); err != nil {
		return false, fmt.Errorf("lock usage hourly rollups: %w", err)
	}
	if !locked {
		return false, nil
	}
	var queued bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM usage_rollup_rebuild_requests)`).Scan(&queued); err != nil {
		return false, err
	}
	if !queued {
		return false, nil
	}
	// No usage at all under these identities leaves nothing to rebuild.
	if from != nil || full {
		if err := refreshUsageHourlyRollups(ctx, tx, from); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM usage_rollup_rebuild_requests WHERE id = 1 AND generation = $1`, generation); err != nil {
		return false, fmt.Errorf("clear usage rebuild request: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
