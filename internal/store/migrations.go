package store

// migrations contains DDL statements executed in order.
// Safe to run repeatedly (IF NOT EXISTS / OR REPLACE).
//
// The DO blocks below tolerate their own failure on purpose -- a TimescaleDB
// feature that is already configured, or absent on plain Postgres, must not stop
// a boot. They re-raise the contention codes first, though, because those are not
// about the statement at all: they mean someone else held what it needed and
// Migrate should retry (PgStore.contendedLockCodes is the same list). Letting
// `WHEN others` catch one would turn a blocked hypertable or compression setting
// into one that is silently never created, which is worse than the wait these
// timeouts exist to end.
//
// The carve-out is a property of each handler, not of the block: PL/pgSQL runs
// the innermost one. A nested `BEGIN ... EXCEPTION WHEN others`, or a PERFORM
// into a PL/pgSQL function that has its own, reopens the hole. Every block here
// is flat and every function it calls is a C function with no handler of its own
// -- verified, not assumed -- and TestExceptionBlocksDoNotSwallowLockTimeouts
// counts handlers so a second one cannot be added without the carve-out.
var migrations = []string{
	// Enable TimescaleDB extension (no-op on plain PG)
	`CREATE EXTENSION IF NOT EXISTS timescaledb`,

	// OTEL events table
	`CREATE TABLE IF NOT EXISTS otel_events (
		id BIGSERIAL,
		ts TIMESTAMPTZ NOT NULL DEFAULT now(),
		event_name TEXT NOT NULL,
		session_id TEXT NOT NULL DEFAULT '',
		prompt_id TEXT NOT NULL DEFAULT '',
		user_id TEXT NOT NULL DEFAULT '',
		profile_email TEXT NOT NULL DEFAULT '',
		user_team TEXT NOT NULL DEFAULT '',
		org_id TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		cost_usd DOUBLE PRECISION,
		input_tokens INT,
		output_tokens INT,
		cache_read_tokens INT,
		cache_create_tokens INT,
		duration_ms INT,
		tool_name TEXT NOT NULL DEFAULT '',
		tool_decision TEXT NOT NULL DEFAULT '',
		tool_success BOOLEAN,
		speed TEXT NOT NULL DEFAULT '',
		service_version TEXT NOT NULL DEFAULT '',
		attrs JSONB DEFAULT '{}'::jsonb
	)`,

	`ALTER TABLE otel_events ADD COLUMN IF NOT EXISTS source_envelope JSONB`,

	// Convert to hypertable (TimescaleDB). Errors silently if already a hypertable.
	`DO $$ BEGIN
		PERFORM create_hypertable('otel_events', 'ts', if_not_exists => TRUE);
	EXCEPTION
	WHEN lock_not_available OR deadlock_detected OR object_in_use THEN
		RAISE;
	WHEN others THEN
		RAISE NOTICE 'otel_events hypertable skipped: %', SQLERRM;
	END $$`,

	// OTEL metrics table
	`CREATE TABLE IF NOT EXISTS otel_metrics (
		id BIGSERIAL,
		ts TIMESTAMPTZ NOT NULL DEFAULT now(),
		metric_name TEXT NOT NULL,
		session_id TEXT NOT NULL DEFAULT '',
		profile_email TEXT NOT NULL DEFAULT '',
		user_team TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		value_double DOUBLE PRECISION,
		value_int BIGINT,
		agent TEXT NOT NULL DEFAULT '',
		billing_provider TEXT NOT NULL DEFAULT '',
		dimensions JSONB DEFAULT '{}'::jsonb
	)`,

	// Convert to hypertable
	`DO $$ BEGIN
		PERFORM create_hypertable('otel_metrics', 'ts', if_not_exists => TRUE);
	EXCEPTION
	WHEN lock_not_available OR deadlock_detected OR object_in_use THEN
		RAISE;
	WHEN others THEN
		RAISE NOTICE 'otel_metrics hypertable skipped: %', SQLERRM;
	END $$`,

	// Indexes for common query patterns
	`CREATE INDEX IF NOT EXISTS idx_events_session ON otel_events (session_id, ts DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_events_user ON otel_events (profile_email, ts DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_events_team ON otel_events (user_team, ts DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_events_name ON otel_events (event_name, ts DESC)`,
	// otel_events got its session index in this same block; otel_metrics did not,
	// and nothing added one later. The sweep behind session deletion asks all three
	// tables "does this session still have rows", so the one without an index made
	// that question a seq scan over every chunk -- 331s per sweep on dev against a
	// 60s tick. See #392.
	`CREATE INDEX IF NOT EXISTS idx_metrics_session ON otel_metrics (session_id, ts DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_metrics_name ON otel_metrics (metric_name, ts DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_metrics_user ON otel_metrics (profile_email, ts DESC)`,

	// Retention policy: drop chunks older than 90 days
	`DO $$ BEGIN
		PERFORM add_retention_policy('otel_events', INTERVAL '90 days', if_not_exists => TRUE);
	EXCEPTION
	WHEN lock_not_available OR deadlock_detected OR object_in_use THEN
		RAISE;
	WHEN others THEN
		RAISE NOTICE 'otel_events retention policy skipped: %', SQLERRM;
	END $$`,

	`DO $$ BEGIN
		PERFORM add_retention_policy('otel_metrics', INTERVAL '90 days', if_not_exists => TRUE);
	EXCEPTION
	WHEN lock_not_available OR deadlock_detected OR object_in_use THEN
		RAISE;
	WHEN others THEN
		RAISE NOTICE 'otel_metrics retention policy skipped: %', SQLERRM;
	END $$`,

	// Session records table (Claude Code JSONL session logs)
	`CREATE TABLE IF NOT EXISTS session_records (
		id BIGSERIAL,
		ts TIMESTAMPTZ NOT NULL DEFAULT now(),
		session_id TEXT NOT NULL DEFAULT '',
		project_hash TEXT NOT NULL DEFAULT '',
		record_type TEXT NOT NULL DEFAULT '',
		profile_email TEXT NOT NULL DEFAULT '',
		user_id TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		input_tokens INT,
		output_tokens INT,
		cache_read_tokens INT,
		cache_create_tokens INT,
		raw JSONB NOT NULL DEFAULT '{}'::jsonb
	)`,

	`DO $$ BEGIN
		PERFORM create_hypertable('session_records', 'ts', if_not_exists => TRUE);
	EXCEPTION
	WHEN lock_not_available OR deadlock_detected OR object_in_use THEN
		RAISE;
	WHEN others THEN
		RAISE NOTICE 'session_records hypertable skipped: %', SQLERRM;
	END $$`,

	`CREATE INDEX IF NOT EXISTS idx_srec_session ON session_records (session_id, ts DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_srec_user ON session_records (profile_email, ts DESC)`,

	// Server-side, deterministic task labels. The source prompt stays only in raw.
	`ALTER TABLE session_records ADD COLUMN IF NOT EXISTS task_type TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS idx_srec_task_type ON session_records (task_type, ts DESC) WHERE task_type <> ''`,

	// Mirror of idx_srec_task_type for the other side of the partition: the
	// backfill's candidate query (postgres_task_types.go) filters on
	// task_type = '', which idx_srec_task_type's WHERE task_type <> '' cannot
	// serve. Without this, that query has no usable index at all and falls back
	// to a sequential scan across every chunk -- measured on prod-mirrored dev
	// data (3.7M+ session_records), 8+ seconds per 1000-row candidate batch, and
	// climbing as the table grows. A row drops out of this partial index the
	// moment it is classified, so the index self-shrinks as the backfill runs.
	`CREATE INDEX IF NOT EXISTS idx_srec_unclassified ON session_records (ts ASC)
		WHERE record_type = 'user' AND task_type = ''`,

	// Add user_id column to session_records if it doesn't exist (migration for existing tables)
	`DO $$ BEGIN
		ALTER TABLE session_records ADD COLUMN user_id TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'session_records.user_id already exists, skipping';
	END $$`,

	// Add login_email column to otel_events (Anthropic login account, separate from org user_email)
	`DO $$ BEGIN
		ALTER TABLE otel_events ADD COLUMN login_email TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'otel_events.login_email already exists, skipping';
	END $$`,

	// Rename user_email -> profile_email (separating org profile email from login_email)
	`DO $$ BEGIN
    ALTER TABLE otel_events RENAME COLUMN user_email TO profile_email;
EXCEPTION WHEN undefined_column THEN
    RAISE NOTICE 'otel_events.user_email not found, skipping rename';
END $$`,
	`DO $$ BEGIN
    ALTER TABLE otel_metrics RENAME COLUMN user_email TO profile_email;
EXCEPTION WHEN undefined_column THEN
    RAISE NOTICE 'otel_metrics.user_email not found, skipping rename';
END $$`,
	`DO $$ BEGIN
    ALTER TABLE session_records RENAME COLUMN user_email TO profile_email;
EXCEPTION WHEN undefined_column THEN
    RAISE NOTICE 'session_records.user_email not found, skipping rename';
END $$`,

	// Nothing to do for the three profile_email indexes after the rename above,
	// and the DROP/CREATE pair that used to sit here has been removed.
	//
	// An index references its columns by attnum, so the rename carries the index
	// with it: the definition is already (profile_email, ts DESC), and lines 89,
	// 99 and 147 created it. Dropping first was not free either -- Migrate keeps
	// no applied history, so every statement runs on every boot, and the pair
	// rebuilt 73 chunk indexes and 556MB on prod every time. That rebuild was
	// part of the 150s migration that failed a dev-sync (#703, #707).
	//
	// The rename block above only ever runs on a database still on user_email,
	// and no such database can reach it: CREATE INDEX IF NOT EXISTS resolves its
	// columns before the name check skips, so line 89 raises undefined_column
	// first. See TestCreateIndexIfNotExistsResolvesColumnsBeforeSkipping.

	// Compression: enable + 30-day policy on the otel hypertables. Placed AFTER
	// the user_email -> profile_email rename above, because compress_segmentby
	// references profile_email. add_compression_policy errors (and is swallowed)
	// unless compression is enabled first, so the ALTER must precede the policy —
	// historically the ALTER was missing entirely, leaving compression silently
	// off. The ALTER is guarded to run only when not already enabled (idempotent
	// across boot replays; changing segmentby later would error on existing
	// compressed chunks). Compression is lossless and coexists with retention
	// (compressed chunks are still dropped by the 90-day retention policy).
	`DO $$ BEGIN
		IF NOT EXISTS (
			SELECT 1 FROM timescaledb_information.hypertables
			WHERE hypertable_name = 'otel_events' AND compression_enabled
		) THEN
			ALTER TABLE otel_events SET (
				timescaledb.compress,
				timescaledb.compress_segmentby = 'profile_email',
				timescaledb.compress_orderby = 'ts DESC'
			);
		END IF;
	EXCEPTION
	WHEN lock_not_available OR deadlock_detected OR object_in_use THEN
		RAISE;
	WHEN others THEN
		RAISE NOTICE 'otel_events compression enable skipped: %', SQLERRM;
	END $$`,

	`DO $$ BEGIN
		IF NOT EXISTS (
			SELECT 1 FROM timescaledb_information.hypertables
			WHERE hypertable_name = 'otel_metrics' AND compression_enabled
		) THEN
			ALTER TABLE otel_metrics SET (
				timescaledb.compress,
				timescaledb.compress_segmentby = 'metric_name',
				timescaledb.compress_orderby = 'ts DESC'
			);
		END IF;
	EXCEPTION
	WHEN lock_not_available OR deadlock_detected OR object_in_use THEN
		RAISE;
	WHEN others THEN
		RAISE NOTICE 'otel_metrics compression enable skipped: %', SQLERRM;
	END $$`,

	`DO $$ BEGIN
		PERFORM add_compression_policy('otel_events', INTERVAL '30 days', if_not_exists => TRUE);
	EXCEPTION
	WHEN lock_not_available OR deadlock_detected OR object_in_use THEN
		RAISE;
	WHEN others THEN
		RAISE NOTICE 'otel_events compression policy skipped: %', SQLERRM;
	END $$`,

	`DO $$ BEGIN
		PERFORM add_compression_policy('otel_metrics', INTERVAL '30 days', if_not_exists => TRUE);
	EXCEPTION
	WHEN lock_not_available OR deadlock_detected OR object_in_use THEN
		RAISE;
	WHEN others THEN
		RAISE NOTICE 'otel_metrics compression policy skipped: %', SQLERRM;
	END $$`,

	// user_aliases: persists merge rules so future OTEL events are auto-resolved
	`CREATE TABLE IF NOT EXISTS user_aliases (
		from_profile_email TEXT PRIMARY KEY,
		to_profile_email   TEXT NOT NULL,
		created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,

	// (The 4-col unique index that used to live here was superseded by
	// uniq_session_record_v2 below, which adds uuid. It must NOT be recreated here:
	// this list replays on every boot, so recreating the 4-col index after v2 exists
	// conflicts with enriched rows that legitimately share (session_id, ts,
	// record_type, profile_email) but differ by uuid — e.g. a re-synced session.)

	// Projects table: stores project metadata derived from CWD
	`CREATE TABLE IF NOT EXISTS projects (
    project_hash TEXT PRIMARY KEY,
    project_name TEXT NOT NULL DEFAULT '',
    git_remote_url TEXT NOT NULL DEFAULT '',
    repository_id TEXT NOT NULL DEFAULT '',
    repository_name TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`,

	// Dashboard users table (authentication + RBAC)
	`CREATE TABLE IF NOT EXISTS dashboard_users (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin', 'user')),
    name          TEXT NOT NULL DEFAULT '',
    is_active     BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
)`,
	`CREATE INDEX IF NOT EXISTS idx_dashboard_users_email ON dashboard_users (email)`,

	// Privacy settings table
	`CREATE TABLE IF NOT EXISTS privacy_settings (
    id             BIGSERIAL PRIMARY KEY,
    profile_email  TEXT NOT NULL,
    scope_type     TEXT NOT NULL CHECK (scope_type IN ('session', 'project', 'user')),
    scope_value    TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (profile_email, scope_type, scope_value)
)`,
	`CREATE INDEX IF NOT EXISTS idx_privacy_email ON privacy_settings (profile_email)`,
	`CREATE INDEX IF NOT EXISTS idx_privacy_scope ON privacy_settings (scope_type, scope_value)`,

	// Add cctrace_user_id to dashboard_users for identity linking
	`DO $$ BEGIN
		ALTER TABLE dashboard_users ADD COLUMN cctrace_user_id TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'dashboard_users.cctrace_user_id already exists, skipping';
	END $$`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_dashboard_users_cctrace_uid ON dashboard_users (cctrace_user_id) WHERE cctrace_user_id != ''`,

	// Add user_id and login_email to otel_metrics for identity consistency with otel_events
	`DO $$ BEGIN
		ALTER TABLE otel_metrics ADD COLUMN user_id TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'otel_metrics.user_id already exists, skipping';
	END $$`,
	`CREATE INDEX IF NOT EXISTS idx_metrics_user_id ON otel_metrics (user_id, ts DESC)`,
	`DO $$ BEGIN
		ALTER TABLE otel_metrics ADD COLUMN login_email TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'otel_metrics.login_email already exists, skipping';
	END $$`,
	`DO $$ BEGIN
		ALTER TABLE otel_metrics ADD COLUMN agent TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'otel_metrics.agent already exists, skipping';
	END $$`,
	`DO $$ BEGIN
		ALTER TABLE otel_metrics ADD COLUMN billing_provider TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'otel_metrics.billing_provider already exists, skipping';
	END $$`,
	// The Codex billing account the exporter header named (#715). Codex metrics
	// carry no session and their login_email is the dashboard address, so without
	// it no billing-account exclusion can reach them. Empty for Claude and for
	// clients older than the header.
	`ALTER TABLE otel_metrics ADD COLUMN IF NOT EXISTS account_id TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS idx_metrics_agent ON otel_metrics (agent, ts DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_metrics_codex_skill ON otel_metrics ((dimensions->>'skill'), ts DESC) WHERE metric_name = 'codex.skill.injected'`,

	// Add login_email to session_records (Anthropic login account, mirrors otel_events.login_email)
	`DO $$ BEGIN
		ALTER TABLE session_records ADD COLUMN login_email TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'session_records.login_email already exists, skipping';
	END $$`,

	// Add user_name to otel_events (extracted from user.name OTEL resource attribute)
	`DO $$ BEGIN
		ALTER TABLE otel_events ADD COLUMN user_name TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'otel_events.user_name already exists, skipping';
	END $$`,

	// Add user_id to privacy_settings for identity-based privacy rules (links to otel user_id)
	`DO $$ BEGIN
		ALTER TABLE privacy_settings ADD COLUMN user_id TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'privacy_settings.user_id already exists, skipping';
	END $$`,

	// Add must_change_password to dashboard_users for temp password flow
	`DO $$ BEGIN
		ALTER TABLE dashboard_users ADD COLUMN must_change_password BOOLEAN NOT NULL DEFAULT false;
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'dashboard_users.must_change_password already exists, skipping';
	END $$`,

	// Add api_token to dashboard_users for per-user API token authentication
	`DO $$ BEGIN
		ALTER TABLE dashboard_users ADD COLUMN api_token TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'dashboard_users.api_token already exists, skipping';
	END $$`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_dashboard_users_api_token ON dashboard_users (api_token) WHERE api_token != ''`,

	// Multiple named API tokens per dashboard user. The primary row mirrors the
	// legacy dashboard_users.api_token used by cctrace init.
	`CREATE TABLE IF NOT EXISTS dashboard_api_tokens (
    id                BIGSERIAL PRIMARY KEY,
    dashboard_user_id BIGINT NOT NULL REFERENCES dashboard_users(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    token             TEXT NOT NULL UNIQUE,
    created_via       TEXT NOT NULL CHECK (created_via IN ('web', 'api')),
    is_primary        BOOLEAN NOT NULL DEFAULT false,
    is_active         BOOLEAN NOT NULL DEFAULT true,
    expires_at        TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at        TIMESTAMPTZ
)`,
	`ALTER TABLE dashboard_api_tokens ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT true`,
	`ALTER TABLE dashboard_api_tokens ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ`,
	`CREATE INDEX IF NOT EXISTS idx_dashboard_api_tokens_user ON dashboard_api_tokens (dashboard_user_id, created_at DESC)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_dashboard_api_tokens_primary ON dashboard_api_tokens (dashboard_user_id) WHERE is_primary`,
	// cli_read marks read tokens issued by `cctrace auth read`, so the read API can
	// refuse them once their owner is an administrator (ErrCLIReadTokenAdmin).
	`ALTER TABLE dashboard_api_tokens DROP CONSTRAINT IF EXISTS dashboard_api_tokens_created_via_check`,
	`ALTER TABLE dashboard_api_tokens ADD CONSTRAINT dashboard_api_tokens_created_via_check CHECK (created_via IN ('web', 'api', 'cli_read'))`,
	`INSERT INTO dashboard_api_tokens (dashboard_user_id, name, token, created_via, is_primary, created_at)
 SELECT id, 'CLI token', api_token, 'api', true, updated_at
 FROM dashboard_users
 WHERE api_token != ''
 ON CONFLICT (token) DO NOTHING`,

	// Add team to dashboard_users for organizational grouping
	`DO $$ BEGIN
		ALTER TABLE dashboard_users ADD COLUMN team TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'dashboard_users.team already exists, skipping';
	END $$`,

	// quota_snapshots: one row per profile_email, latest Anthropic OAuth rate-limit utilization
	`CREATE TABLE IF NOT EXISTS quota_snapshots (
		profile_email              TEXT             NOT NULL PRIMARY KEY,
		user_id                    TEXT             NOT NULL DEFAULT '',
		five_hour_pct              DOUBLE PRECISION NOT NULL DEFAULT 0,
		five_hour_resets_at        TIMESTAMPTZ,
		seven_day_pct              DOUBLE PRECISION NOT NULL DEFAULT 0,
		seven_day_resets_at        TIMESTAMPTZ,
		seven_day_sonnet_pct       DOUBLE PRECISION NOT NULL DEFAULT 0,
		seven_day_sonnet_resets_at TIMESTAMPTZ,
		updated_at                 TIMESTAMPTZ      NOT NULL DEFAULT now()
	)`,

	// quota_samples: rate-limit history, one row per (billing account, window, reading).
	//
	// quota_snapshots above cannot serve this: it is a single row per profile
	// overwritten every five minutes, so no time axis survives, and it hardcodes
	// a column per window at a time when the API has outgrown that shape.
	//
	// Windows are rows, not columns. Anthropic's limits[] is a typed array whose
	// membership changes (codename windows appear and vanish; seven_day_sonnet
	// went null), and Codex reports numeric window_minutes buckets. One row
	// shape holds both.
	//
	// The key is the billing account, never the reporting profile. Several
	// profiles report the same account and that densifies the series rather than
	// duplicating it — they all read one meter, each at its own moment. Keyed on
	// profile_email the same account would split into N lines and be counted N
	// times in the price-weighted average.
	//
	// sampled_at is the client's own fetch time, deliberately unrounded: bucketing
	// would discard exactly the resolution that multiple reporters buy.
	`CREATE TABLE IF NOT EXISTS quota_samples (
		billing_provider TEXT             NOT NULL,
		account_id       TEXT             NOT NULL,
		window_key       TEXT             NOT NULL,
		sampled_at       TIMESTAMPTZ      NOT NULL,
		used_pct         DOUBLE PRECISION NOT NULL DEFAULT 0,
		resets_at        TIMESTAMPTZ,
		window_minutes   INT,
		severity         TEXT             NOT NULL DEFAULT '',
		is_active        BOOLEAN,
		scope_label      TEXT             NOT NULL DEFAULT '',
		plan             TEXT             NOT NULL DEFAULT '',
		login_email      TEXT             NOT NULL DEFAULT '',
		profile_email    TEXT             NOT NULL DEFAULT '',
		attribution      TEXT             NOT NULL DEFAULT 'observed',
		PRIMARY KEY (billing_provider, account_id, window_key, sampled_at)
	)`,

	// source_session_id names the Codex session log a backfilled reading was read
	// out of, so an attribution can be checked against the file that produced it.
	//
	// It is needed because account_id on those rows is inferred, not measured: the
	// JSONL's rate_limits records carry no account identifier, so the backfill
	// credited each reading from the observation log of which account was logged
	// in at the time. 109,449 of 109,780 rows on the development database are
	// inferred that way, and with no link back to the source there was no way to
	// audit or correct one.
	//
	// The session UUID rather than the file path: the path embeds a home
	// directory, and this column would carry a person's account name into every
	// row and every export of it. The UUID identifies the same file to anyone who
	// has it, and matches session_records.session_id, which is what makes it
	// checkable.
	//
	// Empty for readings that have no such file -- the Claude poller and the Codex
	// app-server both read live -- because those were never inferred and have
	// nothing to check against.
	`ALTER TABLE quota_samples ADD COLUMN IF NOT EXISTS source_session_id TEXT NOT NULL DEFAULT ''`,

	// The chart reads a time range across all accounts, which the primary key
	// (provider first) cannot serve.
	`CREATE INDEX IF NOT EXISTS idx_quota_samples_sampled_at
		ON quota_samples (sampled_at DESC)`,
	// Personal quota readings have a separate trust boundary from the shared
	// history above. A dashboard owner must have an independently verified
	// provider-account binding before any row can be written or read as theirs.
	// The verification epoch fences rows across revocation and re-approval.
	`CREATE TABLE IF NOT EXISTS quota_account_bindings (
		id BIGSERIAL PRIMARY KEY,
		dashboard_user_id BIGINT NOT NULL REFERENCES dashboard_users(id) ON DELETE CASCADE,
		billing_provider TEXT NOT NULL CHECK (billing_provider IN ('anthropic','openai')),
		account_id TEXT NOT NULL CHECK (account_id <> '' AND length(account_id) <= 256),
		status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','revoked')),
		verified_at TIMESTAMPTZ,
		approved_by BIGINT REFERENCES dashboard_users(id) ON DELETE SET NULL,
		verification_method TEXT NOT NULL DEFAULT '',
		verification_reference TEXT NOT NULL DEFAULT '',
		requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		UNIQUE (dashboard_user_id, billing_provider, account_id),
		CHECK (length(verification_method) <= 64),
		CHECK (length(verification_reference) <= 128),
		CHECK (status <> 'approved' OR
			(verified_at IS NOT NULL AND verification_method <> '' AND verification_reference <> ''))
	)`,
	`CREATE INDEX IF NOT EXISTS idx_quota_account_bindings_status
		ON quota_account_bindings (status, requested_at DESC)`,
	// The event table stores only the non-secret verification method/reference,
	// actor, and time. It never receives screenshots, credentials, or tokens.
	`CREATE TABLE IF NOT EXISTS quota_account_binding_events (
		id BIGSERIAL PRIMARY KEY,
		binding_id BIGINT NOT NULL REFERENCES quota_account_bindings(id) ON DELETE CASCADE,
		owner_dashboard_user_id BIGINT NOT NULL REFERENCES dashboard_users(id) ON DELETE CASCADE,
		actor_dashboard_user_id BIGINT REFERENCES dashboard_users(id) ON DELETE SET NULL,
		action TEXT NOT NULL CHECK (action IN ('requested','approved','revoked')),
		verification_method TEXT NOT NULL DEFAULT '',
		verification_reference TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		CHECK (length(verification_method) <= 64),
		CHECK (length(verification_reference) <= 128),
		CHECK (action <> 'approved' OR
			(verification_method <> '' AND verification_reference <> ''))
	)`,
	`CREATE INDEX IF NOT EXISTS idx_quota_account_binding_events_owner
		ON quota_account_binding_events (owner_dashboard_user_id, created_at DESC)`,
	// Unlike quota_samples, these rows never contain a login/profile email or a
	// session id. The exact binding epoch is required for visibility, so samples
	// from before a revoke cannot return after a later re-approval.
	`CREATE TABLE IF NOT EXISTS quota_owner_samples (
		dashboard_user_id BIGINT NOT NULL,
		billing_provider TEXT NOT NULL,
		account_id TEXT NOT NULL,
		window_key TEXT NOT NULL,
		sampled_at TIMESTAMPTZ NOT NULL,
		used_pct DOUBLE PRECISION NOT NULL CHECK (used_pct >= 0 AND used_pct <= 100),
		resets_at TIMESTAMPTZ,
		window_minutes INT,
		plan TEXT NOT NULL DEFAULT '',
		scope_label TEXT NOT NULL DEFAULT '',
		severity TEXT NOT NULL DEFAULT '',
		is_active BOOLEAN,
		attribution TEXT NOT NULL CHECK (attribution IN ('observed','inferred')),
		source TEXT NOT NULL DEFAULT 'client_reported' CHECK (source = 'client_reported'),
		binding_verified_at TIMESTAMPTZ NOT NULL,
		CHECK (account_id <> '' AND length(account_id) <= 256),
		CHECK (window_key <> '' AND length(window_key) <= 128),
		CHECK (window_minutes IS NULL OR window_minutes > 0),
		PRIMARY KEY (dashboard_user_id, billing_provider, account_id, window_key, sampled_at),
		FOREIGN KEY (dashboard_user_id, billing_provider, account_id)
			REFERENCES quota_account_bindings (dashboard_user_id, billing_provider, account_id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_quota_owner_samples_window
		ON quota_owner_samples (dashboard_user_id, billing_provider, account_id, window_minutes, sampled_at DESC)`,

	// client_versions: one row per profile_email, latest cctrace CLI version reported on sync
	`CREATE TABLE IF NOT EXISTS client_versions (
		profile_email   TEXT        NOT NULL PRIMARY KEY,
		user_id         TEXT        NOT NULL DEFAULT '',
		client_version  TEXT        NOT NULL DEFAULT '',
		last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	// Client platform metadata is forward-only. Existing rows retain empty values
	// until that profile's next sync, and clients without these headers remain valid.
	`ALTER TABLE client_versions
		ADD COLUMN IF NOT EXISTS client_os TEXT NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS client_arch TEXT NOT NULL DEFAULT ''`,
	// What the client says about its own self-update (#750). update_reported is
	// the field that keeps "too old to say" apart from "said nothing is wrong":
	// it defaults to false and only a client that carries the report sets it, so
	// existing rows read as unknown rather than healthy. The timestamps are
	// nullable because "never failed" has no time.
	`ALTER TABLE client_versions
		ADD COLUMN IF NOT EXISTS update_reported BOOLEAN NOT NULL DEFAULT false,
		ADD COLUMN IF NOT EXISTS update_target_version TEXT NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS update_fail_count INTEGER NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS update_first_failed_at TIMESTAMPTZ,
		ADD COLUMN IF NOT EXISTS update_last_failed_at TIMESTAMPTZ,
		ADD COLUMN IF NOT EXISTS update_fail_reason TEXT NOT NULL DEFAULT ''`,

	// client_policy_reports: what each signed-policy target last said about itself
	// (#848). Keyed by the target, (installation id, profile), because that pair is
	// what a policy names -- client_versions is keyed by account, and one account
	// is several targets. profile is '' for the default profile.
	`CREATE TABLE IF NOT EXISTS client_policy_reports (
		installation_id  TEXT        NOT NULL,
		profile          TEXT        NOT NULL DEFAULT '',
		profile_email    TEXT        NOT NULL DEFAULT '',
		user_id          TEXT        NOT NULL DEFAULT '',
		sync_endpoint    TEXT        NOT NULL DEFAULT '',
		otel_endpoint    TEXT        NOT NULL DEFAULT '',
		protocol         TEXT        NOT NULL DEFAULT '',
		applied_sequence BIGINT      NOT NULL DEFAULT 0,
		check_result     TEXT        NOT NULL DEFAULT '',
		check_sequence   BIGINT      NOT NULL DEFAULT 0,
		check_reason     TEXT        NOT NULL DEFAULT '',
		client_version   TEXT        NOT NULL DEFAULT '',
		first_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
		last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (installation_id, profile)
	)`,

	// retention_settings: durable source of truth for admin-chosen retention per
	// axis (otel | session). A row means "the operator explicitly set this"; the
	// boot reconcile applies it (when the axis is not env-pinned) so a UI edit
	// survives restarts and is re-asserted after Migrate re-adds a default policy.
	// days: 0 = permanent (remove policy), >0 = drop after N days.
	`CREATE TABLE IF NOT EXISTS retention_settings (
		axis        TEXT        NOT NULL PRIMARY KEY CHECK (axis IN ('otel','session')),
		days        INT         NOT NULL CHECK (days >= 0),
		updated_by  TEXT        NOT NULL DEFAULT '',
		updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,

	// One-time data repairs record themselves here so they do not re-scan a growing
	// table on every boot -- the mistake session_records.command_name above documents.
	`CREATE TABLE IF NOT EXISTS schema_backfills (
		name    TEXT        NOT NULL PRIMARY KEY,
		done_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,

	// Add command_name to session_records for plugin usage analytics. The one-time
	// backfill lives INSIDE the ADD COLUMN branch so it runs only when the column is
	// first created: on existing DBs the column already exists (duplicate_column skips
	// the whole block) and new inserts populate command_name directly, so there is no
	// need to re-scan. Keeping it as a standalone UPDATE re-scanned all of session_records
	// with a JSONB LIKE on every boot — cheap when small, minutes once the table grows.
	`DO $$ BEGIN
	ALTER TABLE session_records ADD COLUMN command_name TEXT NOT NULL DEFAULT '';
	UPDATE session_records
	SET command_name = COALESCE(ltrim(
	  substring(raw->'message'->>'content' FROM '<command-name>([^<]+)</command-name>'),
	  '/'
	), '')
	WHERE record_type = 'user'
	  AND raw->'message'->>'content' LIKE '%<command-name>%';
EXCEPTION WHEN duplicate_column THEN
	RAISE NOTICE 'session_records.command_name already exists, skipping';
END $$`,

	// What a slash command actually was, decided on the machine that ran it (#57).
	// The name alone cannot say: "compact" is both a Claude Code builtin and a
	// command one plugin ships, and the log records only the name. Empty means an
	// older client that never looked, which is distinct from 'unknown' -- it looked
	// and the machine could not tell.
	`DO $$ BEGIN
	ALTER TABLE session_records ADD COLUMN command_source TEXT NOT NULL DEFAULT '';
EXCEPTION WHEN duplicate_column THEN
	RAISE NOTICE 'session_records.command_source already exists, skipping';
END $$`,
	`DO $$ BEGIN
	ALTER TABLE session_records ADD COLUMN command_kind TEXT NOT NULL DEFAULT '';
EXCEPTION WHEN duplicate_column THEN
	RAISE NOTICE 'session_records.command_kind already exists, skipping';
END $$`,
	`DO $$ BEGIN
	ALTER TABLE session_records ADD COLUMN command_invoke TEXT NOT NULL DEFAULT '';
EXCEPTION WHEN duplicate_column THEN
	RAISE NOTICE 'session_records.command_invoke already exists, skipping';
END $$`,
	`CREATE INDEX IF NOT EXISTS idx_srec_command_source ON session_records (command_source, ts DESC) WHERE command_name <> ''`,

	`CREATE INDEX IF NOT EXISTS idx_session_records_command_name ON session_records (command_name) WHERE command_name != ''`,

	// --- Codex / multi-agent support (Phase 2) ---
	// source, event_dedup_key, telemetry_completeness, source_rank 제외:
	// Claude도 테이블 분리(otel_events/session_records)로 dedup/source를 해결하므로 불필요.

	`DO $$ BEGIN
		ALTER TABLE otel_events ADD COLUMN agent TEXT NOT NULL DEFAULT 'claude';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'otel_events.agent already exists, skipping';
	END $$`,

	`DO $$ BEGIN
		ALTER TABLE otel_events ADD COLUMN billing_provider TEXT NOT NULL DEFAULT 'anthropic';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'otel_events.billing_provider already exists, skipping';
	END $$`,

	`DO $$ BEGIN
		ALTER TABLE session_records ADD COLUMN agent TEXT NOT NULL DEFAULT 'claude';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'session_records.agent already exists, skipping';
	END $$`,

	`DO $$ BEGIN
		ALTER TABLE session_records ADD COLUMN billing_provider TEXT NOT NULL DEFAULT 'anthropic';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'session_records.billing_provider already exists, skipping';
	END $$`,

	// Single ALTER TABLE acquires AccessExclusiveLock once instead of 5 times.
	// Idempotent via ADD COLUMN IF NOT EXISTS (PG 9.6+).
	`ALTER TABLE session_records
		ADD COLUMN IF NOT EXISTS repository_id TEXT NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS repository_name TEXT NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS repo_subpath TEXT NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS commit_sha TEXT NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS branch TEXT NOT NULL DEFAULT ''`,

	`DO $$ BEGIN
		ALTER TABLE session_records ADD COLUMN repository_id TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'session_records.repository_id already exists, skipping';
	END $$`,

	`DO $$ BEGIN
		ALTER TABLE session_records ADD COLUMN repository_name TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'session_records.repository_name already exists, skipping';
	END $$`,

	`DO $$ BEGIN
		ALTER TABLE session_records ADD COLUMN repo_subpath TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'session_records.repo_subpath already exists, skipping';
	END $$`,

	`DO $$ BEGIN
		ALTER TABLE session_records ADD COLUMN commit_sha TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'session_records.commit_sha already exists, skipping';
	END $$`,

	`DO $$ BEGIN
		ALTER TABLE session_records ADD COLUMN branch TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'session_records.branch already exists, skipping';
	END $$`,

	// Lineage/source fields for individual-vs-assembled session views (forward-only;
	// populated for records synced after this migration). Single ALTER for one lock.
	`ALTER TABLE session_records
		ADD COLUMN IF NOT EXISTS uuid TEXT NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS parent_uuid TEXT NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS is_sidechain BOOLEAN NOT NULL DEFAULT false,
		ADD COLUMN IF NOT EXISTS agent_id TEXT NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS forked_from_session TEXT NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS forked_from_uuid TEXT NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS source_file TEXT NOT NULL DEFAULT ''`,

	// Subagent spawn anchor: the main-thread Task/Agent tool_use id (from the
	// subagent's .meta.json) so the aside chip can attach to its call site.
	`ALTER TABLE session_records ADD COLUMN IF NOT EXISTS tool_use_id TEXT NOT NULL DEFAULT ''`,

	// Meta fields for /export-parity classification: compaction continuity,
	// command scaffolding, genuine-turn origin, headless. Forward-only.
	`ALTER TABLE session_records
		ADD COLUMN IF NOT EXISTS is_compact_summary BOOLEAN NOT NULL DEFAULT false,
		ADD COLUMN IF NOT EXISTS is_meta BOOLEAN NOT NULL DEFAULT false,
		ADD COLUMN IF NOT EXISTS prompt_source TEXT NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS entrypoint TEXT NOT NULL DEFAULT ''`,

	// Resolve branch lineage (forked_from_session -> uuid) without full scans.
	`CREATE INDEX IF NOT EXISTS idx_srec_uuid ON session_records (uuid)`,

	// cctrace collector version that produced this record (from the X-Cctrace-Version
	// sync header). Empty value = collected by a client older than the release that
	// introduced this column (see CctraceVersionSince), not "unknown".
	`ALTER TABLE session_records ADD COLUMN IF NOT EXISTS cctrace_version TEXT NOT NULL DEFAULT ''`,
	// Exact Codex lifecycle context reconstructed from local JSONL order. Empty IDs
	// with metadata_known=true mean the row was outside an active root turn.
	`ALTER TABLE session_records
		ADD COLUMN IF NOT EXISTS codex_turn_id TEXT NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS codex_root_turn_id TEXT NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS codex_turn_metadata_known BOOLEAN NOT NULL DEFAULT false`,

	// Provider billing account this record is attributed to (Codex:
	// chatgpt_account_id from the local auth.json). Empty = the account active
	// at that time was never observed, not "the account in use today".
	`DO $$ BEGIN
		ALTER TABLE session_records ADD COLUMN account_id TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'session_records.account_id already exists, skipping';
	END $$`,

	// Widen the dedup key to include uuid. Subagent files (/btw, Task) share the
	// parent session_id, so records could collide on (session_id, ts, record_type,
	// profile_email) and be dropped. uuid is per-record unique, so the new key is a
	// superset of the old one — existing rows stay unique, no dups to resolve.
	// The old index MUST be dropped: ON CONFLICT only arbitrates one index, and a
	// violation of the remaining 4-col unique index would raise an error, not skip.
	`CREATE UNIQUE INDEX IF NOT EXISTS uniq_session_record_v2
ON session_records (session_id, ts, record_type, profile_email, uuid)`,
	`DROP INDEX IF EXISTS uniq_session_record`,

	`DO $$ BEGIN
		ALTER TABLE projects ADD COLUMN agent TEXT NOT NULL DEFAULT 'claude';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'projects.agent already exists, skipping';
	END $$`,

	`DO $$ BEGIN
		ALTER TABLE projects ADD COLUMN repository_id TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'projects.repository_id already exists, skipping';
	END $$`,

	`DO $$ BEGIN
		ALTER TABLE projects ADD COLUMN repository_name TEXT NOT NULL DEFAULT '';
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'projects.repository_name already exists, skipping';
	END $$`,

	`ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_pkey`,
	`ALTER TABLE projects ADD PRIMARY KEY (agent, project_hash)`,

	`CREATE INDEX IF NOT EXISTS idx_events_agent ON otel_events (agent, ts DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_srec_agent ON session_records (agent, ts DESC)`,
	// Project-filtered latest activity resolves the selected session membership
	// once, then joins that small scope to each physical event source. Keeping the
	// projected columns in this index avoids a full scan of every hypertable chunk.
	`CREATE INDEX IF NOT EXISTS idx_srec_project_scope ON session_records (project_hash, agent, session_id)`,
	// session_records is a TimescaleDB hypertable; CONCURRENTLY is rejected with
	// "hypertables do not support concurrent index creation" (SQLSTATE 0A000).
	// Plain CREATE INDEX on a hypertable builds per-chunk and only holds a brief
	// AccessExclusiveLock per chunk rather than across the whole table, so the
	// write-blocking concern that motivates CONCURRENTLY does not apply here.
	`CREATE INDEX IF NOT EXISTS idx_srec_repository ON session_records (repository_id, ts DESC) WHERE repository_id != ''`,
	`CREATE INDEX IF NOT EXISTS idx_srec_user_id ON session_records (user_id, ts DESC) WHERE user_id != ''`,
	`CREATE INDEX IF NOT EXISTS idx_projects_repository ON projects (repository_id) WHERE repository_id != ''`,

	// claude_imputed_cost: precomputed cost for OFFLINE Claude sessions (JSONL tokens x
	// the weekly OTEL-derived scale, sessions with no OTEL). Refreshed periodically by
	// cctraced (RefreshClaudeImputedCost),
	// NOT computed per query — the anti-join scan is too expensive for the hot cost path.
	// unified_events reads this table directly.
	`CREATE TABLE IF NOT EXISTS claude_imputed_cost (
		srec_id BIGINT NOT NULL DEFAULT 0,
		session_id TEXT NOT NULL DEFAULT '',
		ts TIMESTAMPTZ NOT NULL,
		user_id TEXT NOT NULL DEFAULT '',
		profile_email TEXT NOT NULL DEFAULT '',
		login_email TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens INTEGER NOT NULL DEFAULT 0,
		cache_create_tokens INTEGER NOT NULL DEFAULT 0,
		agent TEXT NOT NULL DEFAULT '',
		billing_provider TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_claude_imputed_ts ON claude_imputed_cost (ts DESC)`,

	// codex_imputed_cost: precomputed cost for Codex usage records. Same reason as
	// claude_imputed_cost above, arrived at the same way -- unified_events used to
	// derive this per query, and that one 267k-row arm cost as much as the whole
	// 2.45M-row view (2.6s), which every chart and the session view paid (#245).
	//
	// Refreshed by cctraced: RefreshCodexImputedCostIncremental on a short tick for
	// freshness, RefreshCodexImputedCost as a periodic rebuild. unified_events reads
	// this table directly.
	`CREATE TABLE IF NOT EXISTS codex_imputed_cost (
		srec_id BIGINT NOT NULL DEFAULT 0,
		session_id TEXT NOT NULL DEFAULT '',
		ts TIMESTAMPTZ NOT NULL,
		user_id TEXT NOT NULL DEFAULT '',
		profile_email TEXT NOT NULL DEFAULT '',
		login_email TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
		-- INTEGER, not BIGINT, and not by accident: unified_events UNIONs these
		-- columns with otel_events, whose token columns are INT. A wider type here
		-- widens the view's column, and CREATE OR REPLACE VIEW refuses to change a
		-- column's type -- the migration fails outright on any existing database.
		-- Per-row counts are nowhere near the limit (observed max 331,622).
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens INTEGER NOT NULL DEFAULT 0,
		cache_create_tokens INTEGER NOT NULL DEFAULT 0,
		agent TEXT NOT NULL DEFAULT '',
		billing_provider TEXT NOT NULL DEFAULT ''
	)`,
	// Account identity travels with the imputed row so unified_events can expose
	// it without re-joining session_records -- that join would double-count this
	// arm, whose rows are already the session_records copies.
	`ALTER TABLE codex_imputed_cost ADD COLUMN IF NOT EXISTS account_id TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS idx_codex_imputed_ts ON codex_imputed_cost (ts DESC)`,
	// One source row yields at most one materialised row, so this is true by
	// construction -- but saying it in the schema is what turns the failure nobody
	// would notice into an error. A session materialised twice reads as a session
	// that cost twice as much, and nothing else in the system would object.
	`CREATE UNIQUE INDEX IF NOT EXISTS uniq_codex_imputed_srec ON codex_imputed_cost (srec_id)`,
	// The incremental refresh deletes and reinserts one session at a time, which the
	// claude table never does (it only ever rebuilds wholesale), so it needs this.
	`CREATE INDEX IF NOT EXISTS idx_codex_imputed_session ON codex_imputed_cost (session_id, profile_email)`,

	// How far the codex refresh has read its source, kept separately from what it
	// wrote. The two are not the same: duplicate token snapshots are dropped and
	// never materialised, so a cursor derived from codex_imputed_cost would park
	// behind one of them and rebuild that session on every tick forever.
	//
	// Ingestion order, not event order. Rows do not arrive newest-first -- a client
	// syncing a machine for the first time uploads months of history, giving high
	// ids to old timestamps -- and session_records permits timestamp ties outright
	// (its uniqueness key includes uuid). A `ts >` cursor misses both.
	`CREATE TABLE IF NOT EXISTS codex_imputed_cursor (
		only_row BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (only_row),
		last_srec_id BIGINT NOT NULL DEFAULT 0
	)`,
	`INSERT INTO codex_imputed_cursor (only_row, last_srec_id) VALUES (TRUE, 0)
		ON CONFLICT (only_row) DO NOTHING`,

	// Lets the refresh ask "anything past the cursor?" without walking every chunk
	// of session_records. Partial, so it only carries codex usage rows -- a few
	// hundred a day, against 4.6M rows in the table as a whole.
	`CREATE INDEX IF NOT EXISTS idx_srec_codex_usage_id ON session_records (id)
		WHERE agent = 'codex' AND record_type = 'usage'`,

	// Freeze the day-model fallback into the 26 legacy rows that need it, so the view
	// can stop computing it. Those rows carry an empty model from clients that predate
	// model capture; unified_events used to fall back to that DAY's most common codex
	// model, which cost a whole extra scan of session_records (514ms) on every read --
	// for 0.01% of rows, a set that cannot grow because no current client writes them.
	//
	// Writing the same value into the row costs nothing and changes no number: it is
	// the query-time computation made permanent. Idempotent -- after the first run
	// there is nothing left with an empty model to update.
	`UPDATE session_records sr SET model = d.top_model
	FROM (
		SELECT date_trunc('day', ts) AS day,
			mode() WITHIN GROUP (ORDER BY model) AS top_model
		FROM session_records
		WHERE agent = 'codex' AND record_type = 'usage' AND COALESCE(model,'') <> ''
		GROUP BY 1
	) d
	WHERE sr.agent = 'codex' AND sr.record_type = 'usage'
		AND COALESCE(sr.model,'') = ''
		AND date_trunc('day', sr.ts) = d.day
		AND d.top_model IS NOT NULL`,

	// codex_model_rates: Codex/OpenAI per-model $/MTok pricing. Codex JSONL carries no cost
	// (unlike Claude which comes via OTEL), so cost is imputed from tokens x these rates.
	// Matched by LONGEST model_prefix (so gpt-5.4-mini beats gpt-5.4). Models with no matching
	// prefix get cost 0 (unknown/local models like ollama are not OpenAI-priced) — keep this
	// table current when OpenAI ships new models. Rates are $/1M tokens.
	`CREATE TABLE IF NOT EXISTS codex_model_rates (
		model_prefix TEXT PRIMARY KEY,
		input_rate DOUBLE PRECISION NOT NULL DEFAULT 0,
		output_rate DOUBLE PRECISION NOT NULL DEFAULT 0,
		cache_read_rate DOUBLE PRECISION NOT NULL DEFAULT 0
	)`,
	// The rate table is temporal: one row per (model, effective_from), not one row
	// per model. OpenAI cuts prices -- the gpt-5.6 family twice in two months --
	// and a single-row table forces a choice between two wrong answers, billing
	// all history at the old price or all of it at the new one. On production
	// spend those two brackets sat ~$2.0k above and ~$2.5k below the truth.
	//
	// effective_from = '-infinity' means "this is the earliest price we know of,
	// and we do not know when it started". Every seeded row is such a row, which
	// is why the seed must keep the OLD prices: it is the baseline the dated rows
	// below supersede, not a statement about today.
	//
	// These three run before the seed rather than at the end of the list because
	// the seed's ON CONFLICT target names effective_from, so the column and the
	// composite key have to exist by then -- on an already-deployed database as
	// much as on a fresh one. All three are idempotent.
	`ALTER TABLE codex_model_rates ADD COLUMN IF NOT EXISTS effective_from DATE NOT NULL DEFAULT '-infinity'`,
	// Postgres has no "ALTER PRIMARY KEY", and both the old and the new constraint
	// carry the same auto-generated name, so the swap is guarded on the key's
	// actual columns rather than on its name.
	`DO $$
	DECLARE cols text;
	BEGIN
		SELECT string_agg(a.attname, ',' ORDER BY k.ord) INTO cols
		FROM pg_constraint c
		JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord) ON TRUE
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		WHERE c.conrelid = 'codex_model_rates'::regclass AND c.contype = 'p';
		IF cols IS DISTINCT FROM 'model_prefix,effective_from' THEN
			ALTER TABLE codex_model_rates DROP CONSTRAINT IF EXISTS codex_model_rates_pkey;
			ALTER TABLE codex_model_rates
				ADD CONSTRAINT codex_model_rates_pkey PRIMARY KEY (model_prefix, effective_from);
		END IF;
	END $$`,

	`INSERT INTO codex_model_rates (model_prefix, input_rate, output_rate, cache_read_rate) VALUES
		('gpt-5.6-sol', 5.00, 30.00, 0.50),
		('gpt-5.6-terra', 2.50, 15.00, 0.25),
		('gpt-5.6-luna', 1.00, 6.00, 0.10),
		('gpt-5.5', 5.00, 30.00, 0.50),
		('gpt-5.4-mini', 0.75, 4.50, 0.075),
		('gpt-5.4', 2.50, 15.00, 0.25),
		('gpt-5.3-codex', 1.75, 14.00, 0.175),
		('gpt-5.2-codex', 1.75, 14.00, 0.175),
		('gpt-5.2', 1.75, 14.00, 0.175),
		('gpt-5.1-codex-mini', 0.25, 2.00, 0.025),
		('gpt-5.1-codex-max', 1.25, 10.00, 0.125),
		('codex-mini-latest', 1.50, 6.00, 0.375),
		('gpt-5-codex', 1.25, 10.00, 0.125)
	ON CONFLICT (model_prefix, effective_from) DO UPDATE SET
		input_rate = EXCLUDED.input_rate,
		output_rate = EXCLUDED.output_rate,
		cache_read_rate = EXCLUDED.cache_read_rate`,

	// unified_events is created near the end of this list, after ai_report_runs.

	// project_rules: repository-scoped instruction files discovered by cctrace sync.
	// repository_key is the stable grouping key: repository_id when available, otherwise project_hash.
	`CREATE TABLE IF NOT EXISTS project_rules (
		id BIGSERIAL PRIMARY KEY,
		agent TEXT NOT NULL DEFAULT '',
		project_hash TEXT NOT NULL DEFAULT '',
		project_name TEXT NOT NULL DEFAULT '',
		repository_id TEXT NOT NULL DEFAULT '',
		repository_key TEXT NOT NULL DEFAULT '',
		repository_name TEXT NOT NULL DEFAULT '',
		rule_path TEXT NOT NULL,
		rule_kind TEXT NOT NULL DEFAULT '',
		rule_scope TEXT NOT NULL DEFAULT 'repository',
		title TEXT NOT NULL DEFAULT '',
		current_version_id BIGINT,
		current_content_hash TEXT NOT NULL DEFAULT '',
		current_status TEXT NOT NULL DEFAULT 'active'
			CHECK (current_status IN ('active', 'missing', 'deleted', 'unreadable', 'archived')),
		discovered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		UNIQUE (agent, repository_key, rule_path)
	)`,

	`CREATE INDEX IF NOT EXISTS idx_project_rules_repo
		ON project_rules (agent, repository_key, current_status)`,
	`CREATE INDEX IF NOT EXISTS idx_project_rules_project
		ON project_rules (agent, project_hash) WHERE project_hash != ''`,
	`CREATE INDEX IF NOT EXISTS idx_project_rules_repository_id
		ON project_rules (repository_id) WHERE repository_id != ''`,

	// project_rule_versions: immutable snapshots. New row only when content_hash changes.
	`CREATE TABLE IF NOT EXISTS project_rule_versions (
		id BIGSERIAL PRIMARY KEY,
		rule_id BIGINT NOT NULL REFERENCES project_rules(id) ON DELETE CASCADE,
		version_number INT NOT NULL,
		content_hash TEXT NOT NULL,
		content TEXT NOT NULL DEFAULT '',
		size_bytes INT NOT NULL DEFAULT 0,
		change_reason TEXT NOT NULL DEFAULT '',
		commit_sha TEXT NOT NULL DEFAULT '',
		branch TEXT NOT NULL DEFAULT '',
		applies_to JSONB NOT NULL DEFAULT '[]'::jsonb,
		frontmatter JSONB NOT NULL DEFAULT '{}'::jsonb,
		raw_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
		created_by_profile_email TEXT NOT NULL DEFAULT '',
		created_by_user_id TEXT NOT NULL DEFAULT '',
		discovered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		UNIQUE (rule_id, version_number),
		UNIQUE (rule_id, content_hash)
	)`,

	`CREATE INDEX IF NOT EXISTS idx_project_rule_versions_rule
		ON project_rule_versions (rule_id, version_number DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_project_rule_versions_hash
		ON project_rule_versions (content_hash)`,

	// project_rule_comments: human notes and version change reasons.
	`CREATE TABLE IF NOT EXISTS project_rule_comments (
		id BIGSERIAL PRIMARY KEY,
		rule_id BIGINT NOT NULL REFERENCES project_rules(id) ON DELETE CASCADE,
		version_id BIGINT REFERENCES project_rule_versions(id) ON DELETE SET NULL,
		comment_type TEXT NOT NULL DEFAULT 'comment'
			CHECK (comment_type IN ('comment', 'change_reason')),
		author_profile_email TEXT NOT NULL DEFAULT '',
		author_user_id TEXT NOT NULL DEFAULT '',
		body TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,

	`CREATE INDEX IF NOT EXISTS idx_project_rule_comments_rule
		ON project_rule_comments (rule_id, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_project_rule_comments_version
		ON project_rule_comments (version_id, created_at DESC) WHERE version_id IS NOT NULL`,

	// Scrub credentials (scheme://user:token@host) leaked into previously stored
	// git remote URLs. Idempotent: only rows still containing userinfo match.
	`UPDATE projects
	SET git_remote_url = regexp_replace(git_remote_url, '://[^/@]*@', '://')
	WHERE git_remote_url ~ '://[^/@]*@'`,

	// Accounts hidden from the dashboard. Exclusion is presentation-only: rows stay
	// in the tables, so removing an entry here restores them at once — within the
	// retention window, which keeps expiring regardless of exclusion.
	// The CHECK matters: entries are also insertable by hand, and an empty
	// login_email would hide every row that predates login_email tagging.
	`CREATE TABLE IF NOT EXISTS excluded_accounts (
		login_email TEXT PRIMARY KEY CHECK (login_email <> '' AND login_email LIKE '%@%'),
		reason TEXT NOT NULL DEFAULT '',
		created_by TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`ALTER TABLE excluded_accounts ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT ''`,

	// excluded_billing_accounts excludes an account by the id it is billed under,
	// for accounts that have no login email to exclude by.
	//
	// Codex is the case that forced it: session_records carries login_email for 0
	// of its 1,002,433 rows, and otel_events carries no Codex rows at all, so an
	// excluded_accounts entry -- keyed on login_email, and CHECKed to look like an
	// address -- can never match one. quota_samples, by contrast, has account_id on
	// every row because it is part of the primary key, which is what makes this the
	// usable key there.
	//
	// A separate table rather than a kind/value column on excluded_accounts: that
	// table's primary key and CHECK are re-asserted on every boot, so relaxing them
	// would make an older binary fail to start against the newer schema, and the
	// three visible_* views read login_email from it directly. Additive costs
	// nothing and stays revertible.
	`CREATE TABLE IF NOT EXISTS excluded_billing_accounts (
		billing_provider TEXT NOT NULL CHECK (billing_provider <> ''),
		account_id TEXT NOT NULL CHECK (account_id <> ''),
		reason TEXT NOT NULL DEFAULT '',
		created_by TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (billing_provider, account_id)
	)`,
	// registered_by_self marks an entry the account's owner made from Settings
	// rather than an admin (#716). It hides exactly as an admin entry does; the flag
	// only decides who may take it back -- the owner their own, never an admin's.
	`ALTER TABLE excluded_billing_accounts ADD COLUMN IF NOT EXISTS registered_by_self BOOLEAN NOT NULL DEFAULT false`,

	// excluded_billing_links is the billing accounts an excluded address reaches.
	//
	// An address and a billing account are the same person when a quota reading
	// carried both, and quota_samples is the only table that ever does. Excluding
	// the address alone left every Codex row of that account visible, because those
	// rows carry the billing id and never the address (#715): an admin excluded a
	// personal account and its sessions, conversation text and cost stayed on the
	// dashboard for days.
	//
	// Materialized rather than joined in the views: quota_samples is hundreds of
	// thousands of rows, and a per-row join from every visible_* read is the shape
	// #298 measured at 61% of a dashboard query. This table holds a handful of rows.
	// It is derived -- rewritten by recomputeExcludedSessionsTx on every exclusion
	// change and by RefreshExcludedBillingLinks when a new reading links an address
	// that is already excluded -- so it carries no reason or actor of its own.
	`CREATE TABLE IF NOT EXISTS excluded_billing_links (
		billing_provider TEXT NOT NULL,
		account_id TEXT NOT NULL,
		login_email TEXT NOT NULL,
		PRIMARY KEY (billing_provider, account_id, login_email)
	)`,

	// excluded_codex_metric_profiles is the last resort for Codex metrics that name
	// no billing account -- everything sent before the exporter header existed
	// (#715) -- in minutes excluded_codex_metric_minutes has no decision for. Such
	// a row is hidden when every Codex account its profile was seen on
	// in quota_samples is excluded. A profile with any visible account, or none
	// seen, stays visible: the row could be from that account, and hiding it would
	// take a non-excluded account's usage with it.
	//
	// Derived like excluded_billing_links, rewritten by recomputeExcludedSessionsTx
	// on every exclusion change, and small -- one row per fully excluded person.
	`CREATE TABLE IF NOT EXISTS excluded_codex_metric_profiles (
		profile_email TEXT PRIMARY KEY
	)`,
	// excluded_codex_metric_minutes judges the same account-less rows minute by
	// minute, from the Codex session records of the profile around that minute;
	// see syncExcludedCodexMetricMinutesTx. hidden=false is a decision too: it
	// keeps the per-profile rule above from overriding a minute known to be on a
	// visible account. Only profiles with an excluded account get rows.
	`CREATE TABLE IF NOT EXISTS excluded_codex_metric_minutes (
		profile_email TEXT NOT NULL,
		minute TIMESTAMPTZ NOT NULL,
		hidden BOOLEAN NOT NULL,
		PRIMARY KEY (profile_email, minute)
	)`,

	// The CHECK has to be (re)applied separately: CREATE TABLE IF NOT EXISTS skips it
	// on databases that already ran an earlier build of this table.
	`ALTER TABLE excluded_accounts DROP CONSTRAINT IF EXISTS excluded_accounts_login_email_check`,
	`ALTER TABLE excluded_accounts ADD CONSTRAINT excluded_accounts_login_email_check
		CHECK (login_email <> '' AND login_email LIKE '%@%')`,

	`DO $$ BEGIN
		ALTER TABLE projects ADD COLUMN last_session_at TIMESTAMPTZ;
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'projects.last_session_at already exists, skipping';
	END $$`,

	// One-time backfill from session_records. A row-level "WHERE last_session_at IS
	// NULL" guard on the UPDATE would NOT skip the scan: Postgres still has to build
	// the max(ts)-per-project_hash subquery (a seq scan across every hypertable
	// chunk, no index on project_hash) before it can join and filter rows out. Some
	// projects legitimately have no session history and stay NULL forever, so that
	// guard would also never become false. Instead the whole statement is wrapped in
	// an EXISTS check that is true once any row has been backfilled — that check
	// short-circuits on the first match without touching session_records.
	`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM projects WHERE last_session_at IS NOT NULL) THEN
			UPDATE projects p SET last_session_at = sub.max_ts
			FROM (
				SELECT project_hash, max(ts) AS max_ts FROM session_records
				WHERE project_hash != '' GROUP BY project_hash
			) sub
			WHERE p.project_hash = sub.project_hash;
		END IF;
	END $$`,

	// projects.repo_subpath: identifies which cwd inside a repo a session started
	// in, from `git rev-parse --show-prefix` (session_records.repo_subpath, already
	// collected by gitctx). A worktree is its own worktree root, so its prefix is
	// empty like the main checkout's — the two merge under (repository_id,
	// repo_subpath). A monorepo subdirectory sits inside the root, so its prefix is
	// non-empty and it stays a separate identity. See issue #257.
	//
	// The backfill lives INSIDE the ADD COLUMN branch, not as a standalone
	// EXISTS-guarded statement like last_session_at above: '' is both the column
	// default AND a legitimate post-backfill value (an empty prefix is the correct,
	// already-backfilled state for a worktree root), so there is no sentinel that
	// distinguishes "not yet backfilled" from "backfilled to empty on purpose". Only
	// duplicate_column reliably answers that — it fires exactly once, when the
	// column is first created.
	`DO $$ BEGIN
		ALTER TABLE projects ADD COLUMN repo_subpath TEXT NOT NULL DEFAULT '';
		UPDATE projects p SET repo_subpath = sub.repo_subpath
		FROM (
			SELECT DISTINCT ON (project_hash) project_hash, repo_subpath
			FROM session_records
			WHERE repo_subpath != ''
			ORDER BY project_hash, ts DESC
		) sub
		WHERE p.project_hash = sub.project_hash;
	EXCEPTION WHEN duplicate_column THEN
		RAISE NOTICE 'projects.repo_subpath already exists, skipping';
	END $$`,

	// excluded_sessions: materialized session_id set for visible_session_records'
	// second anti-join. See the comment on that view below for why (issue #298) and
	// postgres_excluded_sessions.go for how it stays in sync.
	`CREATE TABLE IF NOT EXISTS excluded_sessions (
		session_id TEXT PRIMARY KEY
	)`,

	// otel_events carries no id index (BIGSERIAL alone creates no index). The
	// incremental refresh below needs one: it watermarks on id rather than ts (see
	// that comment for why), and without an index "id > $1" falls back to scanning
	// every chunk, which is the exact cost this feature exists to avoid.
	`CREATE INDEX IF NOT EXISTS idx_events_id ON otel_events (id)`,

	// Watermark for the incremental refresh in postgres_excluded_sessions.go: the
	// otel_events.id the last refresh has already covered, so the periodic job scans
	// only new rows instead of the whole table.
	//
	// Keyed on id, not ts: otel_events ingest can replay through the disk WAL buffer
	// (internal/buffer/diskspiller.go) after downtime, and a replayed row keeps its
	// original event ts, which can be far behind a ts-based watermark that already
	// advanced past it — that row would then never be picked up, permanently leaking
	// an excluded account's session. id is a BIGSERIAL assigned at insert time, so a
	// replayed row always gets a fresh id greater than the watermark regardless of
	// its ts, and "id > watermark" catches it on the next tick.
	`CREATE TABLE IF NOT EXISTS excluded_sessions_refresh_state (
		id BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
		last_event_id BIGINT NOT NULL DEFAULT 0
	)`,

	// One-time backfill. Guarded on excluded_sessions_refresh_state existing, NOT on
	// excluded_sessions being non-empty: a deployment whose excluded accounts happen
	// to have zero matching otel_events would insert 0 rows into excluded_sessions
	// forever, so an excluded_sessions-based guard never becomes true and the
	// otel_events scan below re-runs on every restart — the same "empty result vs.
	// never ran" trap already hit once on projects.last_session_at above. The
	// refresh_state row is written unconditionally (even on a 0-row match), so its
	// existence alone proves this block already ran once.
	`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM excluded_sessions_refresh_state) THEN
			IF EXISTS (SELECT 1 FROM excluded_accounts) THEN
				INSERT INTO excluded_sessions (session_id)
				SELECT DISTINCT o.session_id FROM otel_events o
				JOIN excluded_accounts x ON lower(x.login_email) = lower(o.login_email)
				WHERE o.session_id <> ''
				ON CONFLICT DO NOTHING;
			END IF;
			INSERT INTO excluded_sessions_refresh_state (id, last_event_id)
			VALUES (true, COALESCE((SELECT max(id) FROM otel_events), 0))
			ON CONFLICT (id) DO UPDATE SET last_event_id = EXCLUDED.last_event_id;
		END IF;
	END $$`,

	// deleted_sessions is a tombstone, not a log. Deleting a session server-side is
	// not enough on its own: the client re-reads the same local JSONL and the next
	// sync puts the rows straight back. Ingest consults this table and drops what it
	// names, so a delete stays deleted without the client having to know anything --
	// which matters because the client is the half we cannot rely on reaching (an
	// install built without main.updatePublicKeyBase64 can never self-update).
	//
	// The CHECK is the load-bearing line. session_records holds rows with
	// session_id = '' (metadata lines that carry no session), and otel_metrics on
	// prod had 3.06M of its 3.67M rows under that same empty id -- every user's
	// data. A delete keyed on '' would take all of it. Reject the value where it is
	// cheapest to reject, and let the DELETE statements repeat the guard.
	`CREATE TABLE IF NOT EXISTS deleted_sessions (
		session_id   TEXT PRIMARY KEY CHECK (session_id <> ''),
		project_hash TEXT NOT NULL DEFAULT '',
		deleted_by   TEXT NOT NULL DEFAULT '',
		reason       TEXT NOT NULL DEFAULT '',
		deleted_at   TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,

	// swept_at records that every row this session produced has been reclaimed --
	// not that three source tables were cleaned. The scope is "everything derived
	// from this session", which is what makes a privacy delete mean something, and
	// the authoritative list of what that covers is sessionDeletionTargets in
	// postgres_session_deletion_guard.go (nine tables as of #400, up from the three
	// this comment used to name).
	//
	// Without the flag the sweep has to ask every one of those tables "does this
	// session still have rows" to find its work, which costs the same whether there
	// is work or not -- and there usually is not, because the delete API sweeps
	// inline and the periodic pass is only a backstop. Reading a flag instead makes
	// the idle case free. See #392.
	//
	// Existing tombstones start NULL and get swept once. That is deliberate:
	// assuming they are already clean would be faster, but this is the mechanism
	// that makes a privacy delete real, so it verifies rather than assumes. Each
	// pass over an already-clean tombstone is one indexed DELETE per target that
	// matches nothing.
	`ALTER TABLE deleted_sessions ADD COLUMN IF NOT EXISTS swept_at TIMESTAMPTZ`,
	// Verification is bounded and tombstone-driven. This cursor makes daily passes
	// rotate through all swept tombstones instead of checking the oldest page forever.
	`ALTER TABLE deleted_sessions ADD COLUMN IF NOT EXISTS verified_at TIMESTAMPTZ`,
	`CREATE INDEX IF NOT EXISTS idx_deleted_sessions_unswept
		ON deleted_sessions (deleted_at) WHERE swept_at IS NULL`,
	`CREATE INDEX IF NOT EXISTS idx_deleted_sessions_unverified
		ON deleted_sessions (verified_at NULLS FIRST, swept_at, session_id) WHERE swept_at IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS idx_claude_imputed_session ON claude_imputed_cost (session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_quota_samples_source_session ON quota_samples (source_session_id)
		WHERE source_session_id <> ''`,

	// CREATE TABLE IF NOT EXISTS skips the CHECK on a database that already ran an
	// earlier build of this table, so (re)apply it separately -- same reason
	// excluded_accounts does it above.
	`ALTER TABLE deleted_sessions DROP CONSTRAINT IF EXISTS deleted_sessions_session_id_check`,
	`ALTER TABLE deleted_sessions ADD CONSTRAINT deleted_sessions_session_id_check
		CHECK (session_id <> '')`,

	// blocked_projects stops FUTURE collection for a project; it does not touch what
	// is already stored. Keeping those two apart is deliberate -- "do not collect
	// this any more" and "erase what you already have" are different decisions, and
	// folding them together would let one checkbox delete history nobody meant to
	// lose.
	`CREATE TABLE IF NOT EXISTS blocked_projects (
		project_hash TEXT PRIMARY KEY CHECK (project_hash <> ''),
		project_name TEXT NOT NULL DEFAULT '',
		created_by   TEXT NOT NULL DEFAULT '',
		reason       TEXT NOT NULL DEFAULT '',
		created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`ALTER TABLE blocked_projects DROP CONSTRAINT IF EXISTS blocked_projects_project_hash_check`,
	`ALTER TABLE blocked_projects ADD CONSTRAINT blocked_projects_project_hash_check
		CHECK (project_hash <> '')`,

	// One row, enforced by the id CHECK -- the same shape retention_settings uses to
	// keep a settings table from growing rows nobody reads. The default is true:
	// people deleting their own sessions is the expected case, and an admin who
	// wants it locked down turns it off explicitly rather than having to discover a
	// switch that was off all along.
	`CREATE TABLE IF NOT EXISTS deletion_policy (
		id                 SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
		allow_owner_delete BOOLEAN NOT NULL DEFAULT true,
		updated_by         TEXT NOT NULL DEFAULT '',
		updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`INSERT INTO deletion_policy (id) VALUES (1) ON CONFLICT (id) DO NOTHING`,
	// Write-maintained session overview aggregate. Scope rows preserve the read API's
	// identity precedence without re-scanning history: all, then user, profile, login.
	// The data backfill runs asynchronously after cctraced has opened its listeners;
	// schema_backfills makes that history scan exactly-once and crash-safe.
	`CREATE TABLE IF NOT EXISTS session_overview_rollups (
		scope_type TEXT NOT NULL CHECK (scope_type IN ('all', 'user', 'profile', 'login')),
		scope_value TEXT NOT NULL DEFAULT '',
		session_id TEXT NOT NULL,
		profile_email TEXT NOT NULL DEFAULT '',
		user_id TEXT NOT NULL DEFAULT '',
		login_email TEXT NOT NULL DEFAULT '',
		account_count INT NOT NULL DEFAULT 0,
		model TEXT NOT NULL DEFAULT '',
		agent TEXT NOT NULL DEFAULT 'claude',
		start_time TIMESTAMPTZ NOT NULL,
		end_time TIMESTAMPTZ NOT NULL,
		input_tokens BIGINT NOT NULL DEFAULT 0,
		output_tokens BIGINT NOT NULL DEFAULT 0,
		cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
		event_count BIGINT NOT NULL DEFAULT 0,
		has_sync BOOLEAN NOT NULL DEFAULT false,
		has_api_request BOOLEAN NOT NULL DEFAULT false,
		has_account BOOLEAN NOT NULL DEFAULT false,
		project_hash TEXT NOT NULL DEFAULT '',
		entrypoint TEXT NOT NULL DEFAULT '',
		has_enriched BOOLEAN NOT NULL DEFAULT false,
		has_genuine BOOLEAN NOT NULL DEFAULT false,
		has_fork BOOLEAN NOT NULL DEFAULT false,
		cctrace_version TEXT NOT NULL DEFAULT '',
		claude_version TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (scope_type, scope_value, session_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_session_overview_rollups_page
		ON session_overview_rollups (scope_type, scope_value, end_time DESC, session_id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_session_overview_rollups_project
		ON session_overview_rollups (scope_type, scope_value, project_hash, end_time DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_session_overview_rollups_agent
		ON session_overview_rollups (scope_type, scope_value, agent, end_time DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_session_overview_rollups_session
		ON session_overview_rollups (session_id)`,
	// Tracks completed asynchronous retention runs already reflected in the rollup.
	// A durable watermark avoids rebuilding every old session on every maintenance tick.
	`CREATE TABLE IF NOT EXISTS session_overview_retention_state (
		table_name TEXT PRIMARY KEY,
		last_run_finished TIMESTAMPTZ,
		oldest_source_ts TIMESTAMPTZ
	)`,
	`ALTER TABLE session_overview_retention_state ADD COLUMN IF NOT EXISTS oldest_source_ts TIMESTAMPTZ`,
	`ALTER TABLE session_overview_retention_state ALTER COLUMN last_run_finished DROP NOT NULL`,

	// One narrow row per plugin command source record. Response-window attribution is
	// maintained by the application whenever a session changes, so plugin reads only
	// aggregate this bounded table instead of repeatedly joining raw transcripts.
	`CREATE TABLE IF NOT EXISTS plugin_invocation_facts (
		source_record_id BIGINT PRIMARY KEY,
		session_id TEXT NOT NULL DEFAULT '',
		command_ts TIMESTAMPTZ NOT NULL,
		command_name TEXT NOT NULL,
		profile_email TEXT NOT NULL DEFAULT '',
		login_email TEXT NOT NULL DEFAULT '',
		user_id TEXT NOT NULL DEFAULT '',
		agent TEXT NOT NULL DEFAULT 'claude',
		project_hash TEXT NOT NULL DEFAULT '',
		repository_id TEXT NOT NULL DEFAULT '',
		repository_name TEXT NOT NULL DEFAULT '',
		repo_subpath TEXT NOT NULL DEFAULT '',
		commit_sha TEXT NOT NULL DEFAULT '',
		branch TEXT NOT NULL DEFAULT '',
		input_tokens BIGINT NOT NULL DEFAULT 0,
		output_tokens BIGINT NOT NULL DEFAULT 0
	)`,

	// command_source rides along so the read path can decide what counts as plugin
	// usage. Without it the only place that decision can be made is the INSERT, and a
	// fact table that has already thrown the column away cannot be re-judged when the
	// rule changes -- which it does: #324 rebuilt the builtin list from the vendors'
	// own docs, and that list moves every release. Same reasoning as the account and
	// session exclusions below the fact join: keep the policy reversible at read time
	// rather than baking it into stored facts.
	`ALTER TABLE plugin_invocation_facts ADD COLUMN IF NOT EXISTS command_source TEXT NOT NULL DEFAULT ''`,
	// The billing account rides along for the same reason: the read path matches it
	// against excluded_billing_accounts and excluded_billing_links (#717). Rows
	// written before this column exist are rebuilt once under the v2 backfill marker.
	`ALTER TABLE plugin_invocation_facts ADD COLUMN IF NOT EXISTS billing_provider TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE plugin_invocation_facts ADD COLUMN IF NOT EXISTS account_id TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS idx_plugin_invocation_facts_time
		ON plugin_invocation_facts (command_ts DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_plugin_invocation_facts_session
		ON plugin_invocation_facts (session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_plugin_invocation_facts_profile
		ON plugin_invocation_facts (profile_email, command_ts DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_plugin_invocation_facts_user
		ON plugin_invocation_facts (user_id, command_ts DESC)`,

	// One row per contiguous stretch of typed turns within a session that shares a
	// project and has no idle gap of 30+ minutes -- narrower than a session (one
	// session can run several tasks back to back) and wider than a prompt (a short
	// continuation like "ok" or "1" belongs to the segment it continues, not a fact
	// of its own). activity_type/labeler_version are filled by the behavior-based
	// labeler, which mirrors internal/activitylabel.Classify in SQL
	// (postgres_task_segment_facts.go:239). The comment here used to say nothing
	// wrote them; that stopped being true when the labeler landed (#666).
	`CREATE TABLE IF NOT EXISTS task_segment_facts (
		boundary_record_id BIGINT PRIMARY KEY,
		session_id TEXT NOT NULL,
		start_ts TIMESTAMPTZ NOT NULL,
		end_ts TIMESTAMPTZ,
		boundary_reason TEXT NOT NULL DEFAULT '',
		profile_email TEXT NOT NULL DEFAULT '',
		login_email TEXT NOT NULL DEFAULT '',
		user_id TEXT NOT NULL DEFAULT '',
		agent TEXT NOT NULL DEFAULT 'claude',
		project_hash TEXT NOT NULL DEFAULT '',
		repository_id TEXT NOT NULL DEFAULT '',
		repository_name TEXT NOT NULL DEFAULT '',
		repo_subpath TEXT NOT NULL DEFAULT '',
		commit_sha TEXT NOT NULL DEFAULT '',
		branch TEXT NOT NULL DEFAULT '',
		turn_count INT NOT NULL DEFAULT 0,
		typed_turn_count INT NOT NULL DEFAULT 0,
		tool_call_count INT NOT NULL DEFAULT 0,
		tool_fail_count INT NOT NULL DEFAULT 0,
		command_count INT NOT NULL DEFAULT 0,
		had_compact BOOLEAN NOT NULL DEFAULT false,
		input_tokens BIGINT NOT NULL DEFAULT 0,
		output_tokens BIGINT NOT NULL DEFAULT 0,
		activity_type TEXT NOT NULL DEFAULT '',
		labeler_version TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_task_segment_facts_session
		ON task_segment_facts (session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_task_segment_facts_time
		ON task_segment_facts (start_ts DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_task_segment_facts_profile
		ON task_segment_facts (profile_email, start_ts DESC)`,
	// Pairs with idx_srec_user_id on session_records -- WeeklyInsights' scope
	// filter is `user_id = $3 OR profile_email = $4`, so both branches need an
	// index or the one that lacks it forces a scan (measured on otel_events,
	// which has profile_email but not user_id: see PR #339's investigation).
	`CREATE INDEX IF NOT EXISTS idx_task_segment_facts_user
		ON task_segment_facts (user_id, start_ts DESC) WHERE user_id <> ''`,

	// '' means unclassified (not yet computed); a non-empty value -- including the
	// literal string "unknown" -- means insights.ClassifyPrompt rendered a verdict.
	// Collapsing "unknown" into '' at write time (the pre-existing behavior) made
	// coverage unmeasurable: an unclassified row and a classified-but-unknown row
	// were indistinguishable in this column alone. classifier_version disambiguates
	// task_type = '' further: '' here means "not yet run", not "run and answered ''".
	`ALTER TABLE session_records ADD COLUMN IF NOT EXISTS classifier_version TEXT NOT NULL DEFAULT ''`,

	// Codex writes injected skill, permission and environment instructions as
	// role=developer messages, and clients before the codexlog fix stored them as
	// record_type='user' -- 15,697 of 57,160 Codex user rows on the 2026-09-15 prod
	// snapshot, each counted as a typed turn. Relabel them, clear the classifier
	// verdict they should never have had. Facts are recomputed afterwards by
	// BackfillCodexDeveloperFacts for the affected sessions only: dropping the fact
	// markers instead ran a full rebuild whose exclusive locks stall every session
	// record write (4m41s for task segments on a 6.0M-row local copy).
	// session_records is not compressed (only otel_* are), so the UPDATE is plain.
	// A 'user' copy whose 'developer' twin already exists (new client, then an old
	// client re-syncing the same line) is deleted first: relabeling it would
	// collide on uniq_session_record_v2. Marker-gated: InsertSessionRecords relabels
	// old-client rows on the way in, so this never has anything new to find.
	`DO $$ BEGIN
		IF EXISTS (SELECT 1 FROM schema_backfills WHERE name = 'codex_developer_record_type_v1') THEN
			RETURN;
		END IF;
		DELETE FROM session_records u
		WHERE u.agent = 'codex' AND u.record_type = 'user'
		  AND (u.raw->'payload'->>'role' = 'developer' OR u.raw->>'role' = 'developer')
		  AND EXISTS (SELECT 1 FROM session_records d
		    WHERE d.session_id = u.session_id AND d.ts = u.ts AND d.record_type = 'developer'
		      AND d.profile_email = u.profile_email AND d.uuid = u.uuid);
		UPDATE session_records SET record_type = 'developer', task_type = '', classifier_version = ''
		WHERE agent = 'codex' AND record_type = 'user'
		  AND (raw->'payload'->>'role' = 'developer' OR raw->>'role' = 'developer');
		INSERT INTO schema_backfills (name) VALUES ('codex_developer_record_type_v1');
	END $$`,
	// How far BackfillCodexDeveloperFacts has recomputed, in session_id order. It
	// advances in the same transaction as each batch's facts, so a restart resumes
	// after the last committed batch instead of starting over.
	`CREATE TABLE IF NOT EXISTS codex_developer_facts_repair_cursor (
		only_row BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (only_row),
		last_session_id TEXT NOT NULL DEFAULT ''
	)`,
	`INSERT INTO codex_developer_facts_repair_cursor (only_row, last_session_id) VALUES (TRUE, '')
		ON CONFLICT DO NOTHING`,
	// How far BackfillCodexSegmentToolCounts has recomputed Codex facts, in
	// session_id order, with the same resume-after-restart rule as above.
	`CREATE TABLE IF NOT EXISTS codex_segment_tool_counts_cursor (
		only_row BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (only_row),
		last_session_id TEXT NOT NULL DEFAULT ''
	)`,
	`INSERT INTO codex_segment_tool_counts_cursor (only_row, last_session_id) VALUES (TRUE, '')
		ON CONFLICT DO NOTHING`,
	// The same cursor for BackfillPiSegmentToolCounts (gjc and omo), kept separate
	// so each pass resumes on its own.
	`CREATE TABLE IF NOT EXISTS pi_segment_tool_counts_cursor (
		only_row BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (only_row),
		last_session_id TEXT NOT NULL DEFAULT ''
	)`,
	`INSERT INTO pi_segment_tool_counts_cursor (only_row, last_session_id) VALUES (TRUE, '')
		ON CONFLICT DO NOTHING`,
	// Both backfill passes page with `DISTINCT session_id WHERE agent = ANY(..)
	// AND session_id > $cursor ORDER BY session_id`. idx_task_segment_facts_session
	// cannot filter on agent, so a sparse agent -- 9 gjc and 64 omo rows out of
	// 19,660 on prod -- made every batch, and the final empty one, read the whole
	// table. (agent, session_id) answers the filter and the order in one range.
	// A plain CREATE INDEX rather than CONCURRENTLY: the table is not a hypertable
	// and holds one row per segment (20 MB on prod), so the write lock it takes
	// lasts well under a second, and the runner's lock_timeout retry already
	// covers a busy moment. CONCURRENTLY would buy nothing at this size and leaves
	// an INVALID index behind if interrupted.
	`CREATE INDEX IF NOT EXISTS idx_task_segment_facts_agent_session
		ON task_segment_facts (agent, session_id)`,

	// Where login_email came from, kept apart from the value itself the way
	// prompt_source, command_source and billing_provider already are. '' = nobody
	// has attributed this row, 'otel' = an OTEL event observed that account on this
	// session, 'inferred' = derived from the user's OTEL timeline because the
	// session itself emitted no telemetry at all (#346), 'quota' = carried over from
	// a quota_samples observation (attribution = 'observed') that mapped this row's
	// account_id to a login_email, 'quota-inferred' = the same mapping but with at
	// least one link in the evidence chain inferred rather than observed. The split
	// is what lets a later pass revisit only what it guessed and never an
	// observation.
	//
	// 'inferred' cannot appear on a codex row, by definition rather than by
	// coincidence: otel_events is a Claude-only source (only the Anthropic exporter
	// writes it), so the timeline that pass reads carries no evidence about OpenAI
	// billing at all. Codex rows are attributed by the quota mapping instead, which
	// is why the two 'quota*' values exist -- see #524, where the missing guard
	// stamped 344,034 codex rows with a Claude account.
	//
	// ADD COLUMN with a constant default is metadata-only from PG11 on, so this
	// statement stays cheap no matter how large the table is. Stamping the rows that
	// were already attributed before the column existed is NOT done here -- see
	// BackfillLoginEmailSource for why that half runs off the boot path.
	`ALTER TABLE session_records ADD COLUMN IF NOT EXISTS login_email_source TEXT NOT NULL DEFAULT ''`,

	// Where account_id came from, and it exists to keep two very different things
	// apart. '' = the client observed it directly (Codex reads it out of auth.json
	// and sends it on sync), 'quota' = the server derived it by joining
	// quota_samples on source_session_id where every sample of that session agreed
	// and was itself observed, 'quota-inferred' = the same join but the samples
	// behind it were inferred.
	//
	// Without the column a client-observed account_id and a server-derived one are
	// indistinguishable, so a session that has one inferred quota sample would drag
	// the observed value down to the weaker provenance. It also keeps the derivation
	// auditable after quota_samples' retention window has dropped the evidence.
	//
	// ADD COLUMN with a constant default is metadata-only from PG11 on, so the
	// existing rows -- all of them client-observed -- keep the correct '' for free.
	`ALTER TABLE session_records ADD COLUMN IF NOT EXISTS account_id_source TEXT NOT NULL DEFAULT ''`,

	// Identity of the single tool invocation or output a record carries (Codex
	// tool_call/tool_output, gjc tool_call, omo tool_result), so the session view
	// can pair a call with its output without re-parsing each provider's raw shape.
	// Arguments and output are deliberately not copied out: they stay in raw, where
	// client-side redaction already covers them. Distinct from tool_use_id, which
	// anchors a subagent file to the main-thread call that spawned it.
	//
	// Forward-only: '' on every row written before this column, and on Claude rows,
	// whose one record can hold several tool_use blocks. Constant default, so
	// metadata-only.
	`ALTER TABLE session_records
		ADD COLUMN IF NOT EXISTS tool_name TEXT NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS tool_call_id TEXT NOT NULL DEFAULT ''`,

	// The session-level roll-up of the column above: true when any record of the
	// session was attributed by inference. The session list and detail read this
	// table, not session_records, so without a column here the provenance stops one
	// table short of the screen.
	//
	// No backfill accompanies it, and none is needed: false is the correct answer
	// for every row written before inference existed, and InferSessionRecordLoginEmail
	// refreshes the rollups of exactly the sessions it fills.
	`ALTER TABLE session_overview_rollups ADD COLUMN IF NOT EXISTS login_email_inferred BOOLEAN NOT NULL DEFAULT false`,

	// Durable staging for the one-time login-email history repair. Building the
	// interval timelines is the only full-history read. Keeping those intervals
	// immutable across committed batches prevents retention or newly arriving OTEL
	// from changing attribution after a restart.
	`CREATE TABLE IF NOT EXISTS login_email_history_repair_state (
		name TEXT PRIMARY KEY,
		phase TEXT NOT NULL CHECK (phase IN ('source', 'inference')),
		max_record_id BIGINT NOT NULL,
		source_cursor BIGINT NOT NULL DEFAULT 0,
		inference_cursor BIGINT NOT NULL DEFAULT 0,
		source_updated BIGINT NOT NULL DEFAULT 0,
		inference_updated BIGINT NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE TABLE IF NOT EXISTS login_email_history_session_intervals (
		repair_name TEXT NOT NULL REFERENCES login_email_history_repair_state(name) ON DELETE CASCADE,
		session_id TEXT NOT NULL,
		interval_no BIGINT NOT NULL,
		login_email TEXT NOT NULL,
		from_ts TIMESTAMPTZ NOT NULL,
		to_ts TIMESTAMPTZ,
		first_interval BOOLEAN NOT NULL,
		PRIMARY KEY (repair_name, session_id, interval_no)
	)`,
	`CREATE TABLE IF NOT EXISTS login_email_history_user_intervals (
		repair_name TEXT NOT NULL REFERENCES login_email_history_repair_state(name) ON DELETE CASCADE,
		user_id TEXT NOT NULL,
		interval_no BIGINT NOT NULL,
		login_email TEXT NOT NULL,
		from_ts TIMESTAMPTZ NOT NULL,
		last_obs_ts TIMESTAMPTZ NOT NULL,
		to_ts TIMESTAMPTZ,
		next_login_email TEXT,
		pick_next BOOLEAN NOT NULL,
		PRIMARY KEY (repair_name, user_id, interval_no)
	)`,

	// Durable progress for the one-time Codex quota attribution repair (#524), the
	// same shape as login_email_history_repair_state above and for the same reason:
	// the repair rewrites over a million session_records rows and must survive a
	// restart without redoing or skipping a batch.
	//
	// The phase order is load-bearing. 'revert' clears the wrong 'inferred' values
	// first, because until it has run the rows 'attribute' should claim still look
	// attributed; 'account' fills account_id from the quota join before 'attribute'
	// reads it, so rows whose account_id the same run supplied are attributed in
	// that run rather than the next one.
	//
	// No interval staging accompanies it. What the history repair had to freeze was
	// a timeline, which retention and newly arriving OTEL can both change under it.
	// The source here is a per-account constant -- one account_id maps to one
	// login_email -- so re-reading it between batches cannot shift an earlier
	// batch's answer.
	`CREATE TABLE IF NOT EXISTS codex_login_email_repair_state (
		name TEXT PRIMARY KEY,
		phase TEXT NOT NULL CHECK (phase IN ('revert', 'account', 'attribute')),
		max_record_id BIGINT NOT NULL,
		revert_cursor BIGINT NOT NULL DEFAULT 0,
		account_cursor BIGINT NOT NULL DEFAULT 0,
		attribute_cursor BIGINT NOT NULL DEFAULT 0,
		revert_updated BIGINT NOT NULL DEFAULT 0,
		account_updated BIGINT NOT NULL DEFAULT 0,
		attribute_updated BIGINT NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,

	// Says the account phase has materialised its candidate set into the table
	// below. It is a separate flag rather than "the candidate table has rows"
	// because a phase with nothing to do stages an empty set, and the two states
	// have to be told apart or every batch of such a run re-runs the staging join.
	//
	// ADD COLUMN with a constant default is metadata-only, and the table holds one
	// row at most, so this costs nothing on a database that already ran #524's
	// repair -- which, having recorded its marker, never reads the column again.
	`ALTER TABLE codex_login_email_repair_state ADD COLUMN IF NOT EXISTS account_staged BOOLEAN NOT NULL DEFAULT false`,

	// The account phase's candidate rows, materialised once per repair run (#527).
	//
	// The phase used to derive its candidates inside every batch, joining
	// session_records to the quota session mapping live. The mapping is what drives
	// that join, so each batch fanned its 1,554 sessions out across all 35
	// session_records chunks -- roughly 54,000 index scans -- built the whole
	// 412,717-row join, and then kept the 500 rows above its cursor. The cursor
	// bounded the result and not the scan, so the cost per batch was flat wherever
	// the cursor stood: 835ms on dev, 14s on the production-shaped run, 104,668 rows
	// in about 50 minutes.
	//
	// Staging turns N of those passes into one. The identical join runs once,
	// unbounded, at a measured 840ms on dev, and every batch after it is a
	// primary-key range read of this table.
	//
	// The rows are deleted when the phase drains. The foreign key is the backstop:
	// completion deletes the progress row, and anything left here goes with it, so
	// no repair can ever consume another repair's staged candidates.
	`CREATE TABLE IF NOT EXISTS codex_login_email_repair_candidates (
		repair_name TEXT NOT NULL REFERENCES codex_login_email_repair_state(name) ON DELETE CASCADE,
		id BIGINT NOT NULL,
		session_id TEXT NOT NULL,
		account_id TEXT NOT NULL,
		any_inferred BOOLEAN NOT NULL,
		PRIMARY KEY (repair_name, id)
	)`,
	// Weekly AI report runs, validated reports, tool logs and consent.
	`CREATE TABLE IF NOT EXISTS ai_report_runs (
  id BIGSERIAL PRIMARY KEY,
  dashboard_user_id BIGINT NOT NULL REFERENCES dashboard_users(id) ON DELETE CASCADE,
  week TEXT NOT NULL, tz TEXT NOT NULL,
  since TIMESTAMPTZ NOT NULL, until TIMESTAMPTZ NOT NULL,
  scope_profile_email TEXT NOT NULL DEFAULT '', scope_user_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('running','completed','failed','canceled')),
  error_code TEXT NOT NULL DEFAULT '', error_message TEXT NOT NULL DEFAULT '',
  runtime TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', auth_mode TEXT NOT NULL DEFAULT '',
  tokens_reported BOOLEAN NOT NULL DEFAULT false,
  input_tokens BIGINT, cached_input_tokens BIGINT, output_tokens BIGINT,
  items_dropped INT NOT NULL DEFAULT 0,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(), finished_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_ai_report_runs_one_running
  ON ai_report_runs (dashboard_user_id) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS idx_ai_report_runs_user_week
  ON ai_report_runs (dashboard_user_id, week, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_ai_report_runs_started ON ai_report_runs (started_at);

CREATE TABLE IF NOT EXISTS ai_report_tool_calls (
  run_id BIGINT NOT NULL REFERENCES ai_report_runs(id) ON DELETE CASCADE,
  seq INT NOT NULL,
  tool TEXT NOT NULL, args JSONB NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'running',          -- running|ok|failed|timeout
  result_rows INT, result_bytes INT, duration_ms INT,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (run_id, seq)
);

CREATE TABLE IF NOT EXISTS ai_reports (
  dashboard_user_id BIGINT NOT NULL REFERENCES dashboard_users(id) ON DELETE CASCADE,
  week TEXT NOT NULL,
  run_id BIGINT NOT NULL REFERENCES ai_report_runs(id),
  tz TEXT NOT NULL, since TIMESTAMPTZ NOT NULL, until TIMESTAMPTZ NOT NULL,
  summary TEXT NOT NULL,
  items JSONB NOT NULL,                             -- [{segment_id,title,reason}] 검증 후. 메타 미저장
  generated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (dashboard_user_id, week)
);

CREATE TABLE IF NOT EXISTS ai_consents (
  dashboard_user_id BIGINT NOT NULL REFERENCES dashboard_users(id) ON DELETE CASCADE,
  runtime_key TEXT NOT NULL,                        -- "codex-app-server:chatgpt" / "codex-app-server:api_key"
  disclosure_version TEXT NOT NULL,
  consented_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (dashboard_user_id, runtime_key)
);`,
	// The admin's model and reasoning effort for AI report runs, one row as in
	// deletion_policy. Not seeded: no row and empty values both mean "no
	// override", so the environment keeps deciding until an admin chooses.
	`CREATE TABLE IF NOT EXISTS ai_settings (
		id               SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
		model            TEXT NOT NULL DEFAULT '',
		reasoning_effort TEXT NOT NULL DEFAULT '',
		updated_by       TEXT NOT NULL DEFAULT '',
		updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	// The row now holds the admin's runtime choice; '' is no choice. Model and
	// effort moved to ai_runtime_settings, one row per runtime, so one runtime's
	// model id never reaches another.
	`ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS runtime TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE IF NOT EXISTS ai_runtime_settings (
		runtime          TEXT PRIMARY KEY,
		model            TEXT NOT NULL DEFAULT '',
		reasoning_effort TEXT NOT NULL DEFAULT '',
		updated_by       TEXT NOT NULL DEFAULT '',
		updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	// Settings saved before the split were Codex's, the only runtime then. Copied
	// once: an existing codex row, cleared or not, is the admin's later word. The
	// old columns stay so a rolled-back binary still reads its setting.
	`INSERT INTO ai_runtime_settings (runtime, model, reasoning_effort, updated_by, updated_at)
	SELECT 'codex-app-server', model, reasoning_effort, updated_by, updated_at
	FROM ai_settings WHERE id = 1 AND (model <> '' OR reasoning_effort <> '')
	ON CONFLICT (runtime) DO NOTHING`,
	// The admin's AI report switch. NULL is no admin word:
	// CCTRACE_AI_ENABLED_DEFAULT, then a set CCTRACE_AI_RUNTIME_DEFAULT, decide,
	// and otherwise reports stay off.
	`ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS enabled BOOLEAN`,
	// API keys registered in Admin › AI, sealed with AES-256-GCM by aireport's
	// keyring. The key itself is never stored; key_hint is its last 4 characters.
	`CREATE TABLE IF NOT EXISTS ai_provider_credentials (
		provider   TEXT PRIMARY KEY CHECK (provider IN ('openai', 'anthropic')),
		ciphertext BYTEA NOT NULL,
		nonce      BYTEA NOT NULL,
		key_hint   TEXT NOT NULL DEFAULT '',
		updated_by TEXT NOT NULL DEFAULT '',
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	// Which secret sealed the key: 'secrets_key' (CCTRACE_SECRETS_KEY) or
	// 'jwt_secret' (JWT_SECRET). '' for keys sealed before this column, whose
	// source is unknown.
	`ALTER TABLE ai_provider_credentials ADD COLUMN IF NOT EXISTS key_source TEXT NOT NULL DEFAULT ''`,

	// Expand provider CHECK constraint to include nvidia and litellm
	`DO $$
	DECLARE
		v_constraint_name TEXT;
	BEGIN
		SELECT constraint_name INTO v_constraint_name
		FROM information_schema.table_constraints
		WHERE table_name = 'ai_provider_credentials'
			AND constraint_type = 'CHECK'
			AND constraint_name LIKE '%provider%'
		LIMIT 1;

		IF v_constraint_name IS NOT NULL THEN
			EXECUTE 'ALTER TABLE ai_provider_credentials DROP CONSTRAINT ' || quote_ident(v_constraint_name);
		END IF;
	END $$`,

	`ALTER TABLE ai_provider_credentials
	ADD CONSTRAINT ai_provider_credentials_provider_check
		CHECK (provider IN ('openai', 'anthropic', 'nvidia', 'litellm'))`,

	// Add base_url for LiteLLM and other proxies: deployment-specific endpoint
	`ALTER TABLE ai_runtime_settings
	ADD COLUMN IF NOT EXISTS base_url TEXT NOT NULL DEFAULT ''`,

	// The weekly arm below prices its rows through this; see weekly_usage.go.
	codexRateCostUSDFunction,
	claudeRateCostUSDFunction,

	// Placed after ai_report_runs: the weekly arm reads that table, and a view cannot
	// be created before the tables it names. dropDependentViews drops it before every
	// pass, so moving it down the list costs an existing database nothing.
	// unified_events: combine otel_events with codex usage records from session_records,
	// and the weekly AI report runs (weeklyUsageArm).
	// Codex JSONL doesn't include cost; compute implied cost via model-specific OpenAI pricing.
	`CREATE OR REPLACE VIEW unified_events AS
	SELECT
		ts, event_name, session_id, prompt_id,
		user_id, profile_email, login_email, user_name, user_team, org_id,
		model, cost_usd, input_tokens, output_tokens,
		cache_read_tokens, cache_create_tokens, duration_ms,
		tool_name, tool_decision, tool_success,
		speed, service_version, attrs, agent, billing_provider,
		-- Account identity travels with the usage row so consumers do not have to
		-- re-join session_records (which would double-count the codex arm, since
		-- those rows are already surfaced here). OTEL carries no account id of its
		-- own; for Claude the identity is login_email above.
		'' AS account_id,
		'o:' || id AS tiebreak
	FROM otel_events
	UNION ALL
	SELECT
		ts, 'codex_usage' AS event_name, session_id, '' AS prompt_id,
		user_id, profile_email, login_email,
		'' AS user_name, '' AS user_team, '' AS org_id,
		model, cost_usd, input_tokens, output_tokens,
		cache_read_tokens, cache_create_tokens,
		NULL::int AS duration_ms,
		'' AS tool_name, '' AS tool_decision, NULL::boolean AS tool_success,
		'' AS speed, '' AS service_version, '{}'::jsonb AS attrs,
		agent, billing_provider,
		COALESCE(account_id,'') AS account_id,
		'c:' || srec_id AS tiebreak
	FROM codex_imputed_cost
	UNION ALL
	SELECT
		ts, 'claude_usage' AS event_name, session_id, '' AS prompt_id,
		user_id, profile_email, login_email,
		'' AS user_name, '' AS user_team, '' AS org_id,
		model, cost_usd, input_tokens, output_tokens,
		cache_read_tokens, cache_create_tokens,
		NULL::int AS duration_ms,
		'' AS tool_name, '' AS tool_decision, NULL::boolean AS tool_success,
		'' AS speed, '' AS service_version, '{}'::jsonb AS attrs,
		agent, billing_provider,
		'' AS account_id,
		'j:' || srec_id AS tiebreak
	FROM claude_imputed_cost
	UNION ALL
	SELECT
		ts, 'gjc_usage' AS event_name, session_id, '' AS prompt_id,
		COALESCE(user_id,'') AS user_id, profile_email, COALESCE(login_email,'') AS login_email,
		'' AS user_name, '' AS user_team, '' AS org_id,
		model,
		-- gjc/omo JSONL carries the tool's own exact cost at raw#>>'{message,usage,cost,total}'
		-- (unlike Codex, which has none and is imputed via codex_model_rates above). But
		-- raw carries this cost even for records whose token usage was deliberately dropped
		-- by the syncer as double-counted (e.g. omo driving the real claude binary via
		-- claude-sdk-oauth, already collected through the ordinary Claude Code path -- see
		-- usageIsDoubleCounted in internal/omosyncer/syncer.go). Re-deriving that decision
		-- here from provider/raw would let this view and the syncer drift out of sync, so
		-- this view does not re-test the provider: it trusts the syncer's own signal, which
		-- is the presence of tokens. Cost follows the tokens, not the raw payload -- if you
		-- are looking at this because "raw has a cost, why aren't we using it", the answer is
		-- that using it here would resurrect a double-count the syncer already prevented.
		CASE WHEN input_tokens IS NOT NULL
			THEN (raw #>> '{message,usage,cost,total}')::double precision
			ELSE NULL
		END AS cost_usd,
		input_tokens, output_tokens,
		cache_read_tokens, cache_create_tokens,
		NULL::int AS duration_ms,
		'' AS tool_name, '' AS tool_decision, NULL::boolean AS tool_success,
		'' AS speed, '' AS service_version, '{}'::jsonb AS attrs,
		agent, billing_provider,
		-- session_records carries the billing id for these rows, so the exclusion
		-- keyed on it reaches them here as it already does in visible_session_records.
		account_id,
		'g:' || id AS tiebreak
	FROM session_records
	WHERE agent = 'gjc' AND record_type = 'assistant'
	UNION ALL
	SELECT
		ts, 'omo_usage' AS event_name, session_id, '' AS prompt_id,
		COALESCE(user_id,'') AS user_id, profile_email, COALESCE(login_email,'') AS login_email,
		'' AS user_name, '' AS user_team, '' AS org_id,
		model,
		-- See the gjc arm above for why this is gated on input_tokens IS NOT NULL --
		-- the same double-count trap applies here, and this is in fact where it was found.
		CASE WHEN input_tokens IS NOT NULL
			THEN (raw #>> '{message,usage,cost,total}')::double precision
			ELSE NULL
		END AS cost_usd,
		input_tokens, output_tokens,
		cache_read_tokens, cache_create_tokens,
		NULL::int AS duration_ms,
		'' AS tool_name, '' AS tool_decision, NULL::boolean AS tool_success,
		'' AS speed, '' AS service_version, '{}'::jsonb AS attrs,
		agent, billing_provider,
		account_id,
		'm:' || id AS tiebreak
	FROM session_records
	WHERE agent = 'omo' AND record_type = 'assistant'
	UNION ALL` + weeklyUsageArm,

	// Session observations: what the collector reports about a session that is not
	// a JSONL line with a stable key -- the latest values of its metadata lines, the
	// process running it, and when that process changed state or ended
	// (POST /api/session-observations).
	//
	// They live here and not in session_records on purpose. A synthetic record there
	// moves every aggregate that counts rows or reads first and last ts -- the
	// rollup's start, end and event_count, a segment's record_count, the Weekly
	// session count -- and the metadata lines have no uuid or ts to key one by.
	// These four tables hold only what was observed; anything derived from them is
	// recomputed on read or by a later stage, never stored beside them.
	//
	// The three session-scoped tables carry the columns a read will need to filter
	// them the way session_records is filtered: user_id, profile_email and
	// login_email for the rollup's scopes, and login_email, billing_provider and
	// account_id for an account exclusion (excludedAccountPredicateSQL). No read
	// applies either yet -- nothing reads these tables -- so whatever reads them
	// first has to. Ingest refuses an excluded account's items, but an exclusion
	// can be made after a row landed, and only a read-time filter reaches that row.
	// A privacy delete or a user merge finds a row by user_id or profile_email, the
	// columns it already uses. The three are on sessionDeletionTargets.
	//
	// Every time a client sends is stored as sent, because newer-wins upserts
	// compare them. The API skips an item dated more than five minutes past its own
	// clock, so a client clock running ahead pins a row for at most that long.
	//
	// session_snapshots: one row per session per identity, overwritten as the
	// client's view of the metadata lines changes. observed_at is the client's
	// clock, and the upsert keeps the newer one, so a retry of an older snapshot
	// cannot undo a newer one.
	`CREATE TABLE IF NOT EXISTS session_snapshots (
		session_id       TEXT        NOT NULL,
		user_id          TEXT        NOT NULL DEFAULT '',
		profile_email    TEXT        NOT NULL DEFAULT '',
		login_email      TEXT        NOT NULL DEFAULT '',
		agent            TEXT        NOT NULL DEFAULT '',
		project_hash     TEXT        NOT NULL DEFAULT '',
		billing_provider TEXT        NOT NULL DEFAULT '',
		account_id       TEXT        NOT NULL DEFAULT '',
		collect_version  INT         NOT NULL DEFAULT 0,
		snapshot         JSONB       NOT NULL,
		observed_at      TIMESTAMPTZ NOT NULL,
		received_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (session_id, user_id, profile_email)
	)`,
	// session_live_states: one row per running instance of a session. A session
	// resumed in two terminals is two processes, so pid and proc_start are part of
	// the key -- pid alone is reused by the OS once a process exits. heartbeat_at is
	// when the client last read the process's state file; status_changed_at is
	// what the file itself says, and the two answer different questions (alive
	// versus since when).
	`CREATE TABLE IF NOT EXISTS session_live_states (
		session_id        TEXT        NOT NULL,
		user_id           TEXT        NOT NULL DEFAULT '',
		profile_email     TEXT        NOT NULL DEFAULT '',
		login_email       TEXT        NOT NULL DEFAULT '',
		agent             TEXT        NOT NULL DEFAULT '',
		project_hash      TEXT        NOT NULL DEFAULT '',
		billing_provider  TEXT        NOT NULL DEFAULT '',
		account_id        TEXT        NOT NULL DEFAULT '',
		pid               BIGINT      NOT NULL,
		proc_start        TEXT        NOT NULL DEFAULT '',
		status            TEXT        NOT NULL,
		waiting_kind      TEXT        NOT NULL DEFAULT '',
		name              TEXT        NOT NULL DEFAULT '',
		name_source       TEXT        NOT NULL DEFAULT '',
		status_changed_at TIMESTAMPTZ,
		heartbeat_at      TIMESTAMPTZ NOT NULL,
		received_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (session_id, user_id, profile_email, pid, proc_start)
	)`,
	// session_observation_events: history, written only when something happened --
	// a state change, a process gone, a SessionEnd hook -- never per heartbeat, so
	// its size follows activity rather than uptime. The server writes the first two
	// itself, when an incoming live state is new or changes status (or, while
	// waiting, its kind of wait), with an event_id derived from that change; a
	// client may send only the third, with an event_id of its own. Either way a
	// resend carries the same id and is a no-op.
	//
	// Unlike the two tables above it grows without bound, so it has a retention:
	// cctraced prunes it by observed_at (SessionObservationEventRetention), or
	// sooner when the session retention is shorter. The two above are pruned only
	// when session retention is set (PruneSessionObservations).
	`CREATE TABLE IF NOT EXISTS session_observation_events (
		session_id       TEXT        NOT NULL,
		user_id          TEXT        NOT NULL DEFAULT '',
		profile_email    TEXT        NOT NULL DEFAULT '',
		login_email      TEXT        NOT NULL DEFAULT '',
		agent            TEXT        NOT NULL DEFAULT '',
		project_hash     TEXT        NOT NULL DEFAULT '',
		billing_provider TEXT        NOT NULL DEFAULT '',
		account_id       TEXT        NOT NULL DEFAULT '',
		event_id         TEXT        NOT NULL,
		kind             TEXT        NOT NULL CHECK (kind IN ('state_change','gone','session_end')),
		observed_at      TIMESTAMPTZ NOT NULL,
		payload          JSONB       NOT NULL DEFAULT '{}',
		received_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (session_id, user_id, profile_email, event_id)
	)`,
	// The prune is a range delete by observed_at, which the primary key cannot serve.
	`CREATE INDEX IF NOT EXISTS idx_session_observation_events_observed_at
		ON session_observation_events (observed_at)`,
	// A project's sessions are looked up by project_hash in each of the three
	// tables (projectSessionTables): ProjectSessionCount, DeleteProject, the
	// sibling purge of DeleteSession, and PurgeBlockedProjects on every maintenance
	// tick. An update writes back the project_hash the row already holds, so the
	// index does not take a heartbeat update off its HOT path.
	`CREATE INDEX IF NOT EXISTS idx_session_snapshots_project_hash
		ON session_snapshots (project_hash)`,
	`CREATE INDEX IF NOT EXISTS idx_session_live_states_project_hash
		ON session_live_states (project_hash)`,
	`CREATE INDEX IF NOT EXISTS idx_session_observation_events_project_hash
		ON session_observation_events (project_hash)`,
	// collector_heartbeats: one row per identity. heartbeat_at says the collector
	// is running; last_stored_at says a sync of its records committed. They are kept
	// apart because client_versions.last_seen_at cannot say the second: it is
	// written before the insert, so a client whose every sync fails still looks
	// current there. heartbeat_at is the server's time of receipt, never the
	// client's clock.
	//
	// No session column, so the session sweep never reaches it; deleting a user's
	// data (DeleteUserData) does, by identity. A request with no profile_email
	// writes no row: every anonymous client would otherwise share the one.
	`CREATE TABLE IF NOT EXISTS collector_heartbeats (
		user_id        TEXT        NOT NULL DEFAULT '',
		profile_email  TEXT        NOT NULL DEFAULT '',
		heartbeat_at   TIMESTAMPTZ,
		last_stored_at TIMESTAMPTZ,
		client_version TEXT        NOT NULL DEFAULT '',
		PRIMARY KEY (user_id, profile_email)
	)`,
	// Provenance is server-set during authenticated observation ingest. Legacy rows
	// remain unknown and cannot alone establish ownership of a personal note.
	`ALTER TABLE session_snapshots ADD COLUMN IF NOT EXISTS identity_provenance TEXT NOT NULL DEFAULT 'unknown'`,
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS identity_provenance TEXT NOT NULL DEFAULT 'unknown'`,
	`ALTER TABLE session_observation_events ADD COLUMN IF NOT EXISTS identity_provenance TEXT NOT NULL DEFAULT 'unknown'`,
	`CREATE TABLE IF NOT EXISTS session_group_dismissals (
		dashboard_user_id BIGINT NOT NULL REFERENCES dashboard_users(id) ON DELETE CASCADE,
		session_id TEXT NOT NULL CHECK (session_id <> ''),
		source_user_id TEXT NOT NULL DEFAULT '',
		source_profile_email TEXT NOT NULL DEFAULT '',
		owner_proof TEXT NOT NULL CHECK (owner_proof IN ('session_record','personal_token_observation')),
		owner_state TEXT NOT NULL DEFAULT 'valid' CHECK (owner_state IN ('valid','invalidated')),
		CHECK (source_user_id <> '' OR source_profile_email <> ''),
		grouping_version INT NOT NULL CHECK (grouping_version > 0),
		ended_at TIMESTAMPTZ NOT NULL,
		dismissed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (dashboard_user_id,session_id,grouping_version,ended_at)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_session_group_dismissals_owner
		ON session_group_dismissals (dashboard_user_id,dismissed_at DESC)`,
	`CREATE TABLE IF NOT EXISTS session_group_dismissal_identity_keys (
		dashboard_user_id BIGINT NOT NULL,
		session_id TEXT NOT NULL,
		grouping_version INT NOT NULL,
		ended_at TIMESTAMPTZ NOT NULL,
		source_user_id TEXT NOT NULL DEFAULT '',
		source_profile_email TEXT NOT NULL DEFAULT '',
		CHECK (source_user_id <> '' OR source_profile_email <> ''),
		PRIMARY KEY (dashboard_user_id,session_id,grouping_version,ended_at,source_user_id,source_profile_email),
		FOREIGN KEY (dashboard_user_id,session_id,grouping_version,ended_at)
			REFERENCES session_group_dismissals (dashboard_user_id,session_id,grouping_version,ended_at)
			ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_group_dismissal_identity_user
		ON session_group_dismissal_identity_keys (source_user_id,session_id) WHERE source_user_id <> ''`,
	`CREATE INDEX IF NOT EXISTS idx_group_dismissal_identity_email
		ON session_group_dismissal_identity_keys (source_profile_email,session_id) WHERE source_profile_email <> ''`,
	`CREATE TABLE IF NOT EXISTS session_personal_state (
		dashboard_user_id BIGINT NOT NULL REFERENCES dashboard_users(id) ON DELETE CASCADE,
		session_id TEXT NOT NULL CHECK (session_id <> ''),
		source_user_id TEXT NOT NULL DEFAULT '',
		source_profile_email TEXT NOT NULL DEFAULT '',
		owner_proof TEXT NOT NULL CHECK (owner_proof IN ('session_record','personal_token_observation')),
		owner_state TEXT NOT NULL DEFAULT 'valid' CHECK (owner_state IN ('valid','invalidated')),
		classification TEXT NOT NULL DEFAULT 'unsorted'
			CHECK (classification IN ('unsorted','consulting','working','later','done')),
		note TEXT NOT NULL DEFAULT '' CHECK (octet_length(note) <= 16384),
		revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		expires_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (dashboard_user_id,session_id),
		CHECK (source_user_id <> '' OR source_profile_email <> '')
	)`,
	`CREATE INDEX IF NOT EXISTS idx_session_personal_note_list
		ON session_personal_state (dashboard_user_id,updated_at DESC,session_id) WHERE note <> ''`,
	`CREATE TABLE IF NOT EXISTS session_personal_identity_keys (
		dashboard_user_id BIGINT NOT NULL,
		session_id TEXT NOT NULL,
		source_user_id TEXT NOT NULL DEFAULT '',
		source_profile_email TEXT NOT NULL DEFAULT '',
		CHECK (source_user_id <> '' OR source_profile_email <> ''),
		PRIMARY KEY (dashboard_user_id,session_id,source_user_id,source_profile_email),
		FOREIGN KEY (dashboard_user_id,session_id)
			REFERENCES session_personal_state (dashboard_user_id,session_id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_personal_identity_user
		ON session_personal_identity_keys (source_user_id,session_id) WHERE source_user_id <> ''`,
	`CREATE INDEX IF NOT EXISTS idx_personal_identity_email
		ON session_personal_identity_keys (source_profile_email,session_id) WHERE source_profile_email <> ''`,
	`CREATE TABLE IF NOT EXISTS dashboard_personal_notes (
		dashboard_user_id BIGINT PRIMARY KEY REFERENCES dashboard_users(id) ON DELETE CASCADE,
		note TEXT NOT NULL CHECK (octet_length(note) <= 16384),
		revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,

	// session_records.message_id; see postgres_message_id_backfill.go. In this
	// group and not at the end of dependentViews, where the newer statements go:
	// visible_session_records is SELECT * over session_records and is recreated at
	// the top of dependentViews, so a column added after it would reach the view
	// only on the next boot (see PgStore.Migrate).
	sessionRecordsMessageIDColumnDDL,
	// How far BackfillSessionRecordMessageIDs has walked. One row, written by its
	// first call: the range of Claude assistant ts it found then, and the upper end
	// of the next window, which walks back from range_end to range_start. There is
	// no row before that first call. Deleting the row together with the
	// session_records_message_id_v1 marker -- the P1t rollback runbook does both --
	// makes the next call record a new range and walk it again.
	//
	// No session column: the row is the deployment's progress, not anyone's
	// session, so a privacy delete has nothing to take from it.
	`CREATE TABLE IF NOT EXISTS session_records_message_id_cursor (
		only_row BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (only_row),
		range_start TIMESTAMPTZ NOT NULL,
		range_end TIMESTAMPTZ NOT NULL,
		next_window_end TIMESTAMPTZ NOT NULL
	)`,
	// How far step 4 of the P1t transition has recounted the overview
	// (recountSessionOverviewTokensStep): the last session_id of the last batch it
	// committed. One row, written by its first batch. Deleting it together with
	// the session_overview_tokens_v2 marker -- the P1t rollback runbook does both
	// -- makes the next call recount from the first session again.
	//
	// The id is a position in the walk, not anyone's data, so a privacy delete
	// does not reach it; deleted_sessions keeps a deleted session's id as well.
	`CREATE TABLE IF NOT EXISTS session_overview_tokens_cursor (
		only_row BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (only_row),
		last_session_id TEXT NOT NULL
	)`,
	// The same for step 8 (recountTaskSegmentTokensStep), which updates the token
	// columns of the stored task segments: the last session_id of its last
	// committed batch, deleted with the task_segment_tokens_v2 marker by the P1t
	// rollback runbook. Like the row above, a position in the walk that a privacy
	// delete does not reach.
	`CREATE TABLE IF NOT EXISTS task_segment_tokens_cursor (
		only_row BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (only_row),
		last_session_id TEXT NOT NULL
	)`,

	// Session analysis (p1b-design §2): one row of facts per session, its turns and
	// activity segments, the worker's queue, and the copy markers. They sit after
	// the observation tables and the P1t statements, beside the tables they are
	// computed from -- the snapshots, and session_records with its message_id
	// request key -- as p1b-design §2.3 places them. Nothing outside this block
	// orders them: no foreign key or view ties them to another statement, here or
	// in dependentViews.
	//
	// A boot that finds them in place takes no lock at all. IF NOT EXISTS skips an
	// existing table or sequence by name without locking it, but CREATE INDEX IF
	// NOT EXISTS takes SHARE on its table before it looks at the name: once these
	// tables are written, a boot would wait behind their writers, and every writer
	// queued after it would wait for the boot. So each index statement runs only
	// when to_regclass finds no index of that name; the IF NOT EXISTS inside stays
	// for two boots at once.
	//
	// All five tables are on sessionDeletionTargets, and session_analysis_facts,
	// which carries a project_hash, is on projectSessionTables.
	//
	// session_analysis_facts: one row per session, computed from all of its visible
	// rows. user_ids, profile_emails and login_emails collect every value the
	// contributing rows carry -- JSONL, OTEL api_request, snapshots -- the empty
	// string included. owner_user_id and owner_profile_email hold a value only when
	// their array is exactly that one non-empty value, and are NULL when the
	// contributing identities are mixed or empty: role=user reads match on them, so
	// a session that is not wholly the caller's is no one's (p1b-design §2.1).
	// account_keys lets a read re-apply an account exclusion made after the row was
	// computed.
	`CREATE TABLE IF NOT EXISTS session_analysis_facts (
		session_id             TEXT        PRIMARY KEY,
		agent                  TEXT        NOT NULL,
		coverage               TEXT        NOT NULL,              -- full | truncated
		user_ids               TEXT[]      NOT NULL DEFAULT '{}',
		profile_emails         TEXT[]      NOT NULL DEFAULT '{}',
		login_emails           TEXT[]      NOT NULL DEFAULT '{}',
		owner_user_id          TEXT,
		owner_profile_email    TEXT,
		login_email_inferred   BOOLEAN     NOT NULL DEFAULT false,
		account_keys           JSONB       NOT NULL DEFAULT '[]',
		project_hash           TEXT        NOT NULL DEFAULT '',
		repository_key         TEXT        NOT NULL DEFAULT '',   -- repository_id, else project_hash
		branch                 TEXT        NOT NULL DEFAULT '',
		model                  TEXT        NOT NULL DEFAULT '',
		transcript             BOOLEAN     NOT NULL,              -- has JSONL rows
		title                  TEXT,
		title_source           TEXT        NOT NULL,              -- custom | ai | agent_name | first_request | none
		title_state            TEXT        NOT NULL,              -- ok | withheld | redacted | none
		first_activity_at      TIMESTAMPTZ,
		last_activity_at       TIMESTAMPTZ,
		first_record_at        TIMESTAMPTZ,                       -- min ts of the session_records read, copies in, 1970 rows out; for retention only
		active_seconds         INT         NOT NULL DEFAULT 0,
		turn_boundaries        TEXT        NOT NULL,              -- observed | undeterminable
		turn_boundaries_reason TEXT        NOT NULL DEFAULT '',
		turns                  INT,                               -- NULL: undeterminable
		requests               INT,
		inputs                 INT,
		abandoned_turns        INT,
		api_requests           INT         NOT NULL DEFAULT 0,
		input_tokens           BIGINT      NOT NULL DEFAULT 0,
		output_tokens          BIGINT      NOT NULL DEFAULT 0,
		cache_read_tokens      BIGINT      NOT NULL DEFAULT 0,
		cache_create_tokens    BIGINT      NOT NULL DEFAULT 0,
		cost_usd               DOUBLE PRECISION,
		cost_source            TEXT        NOT NULL,              -- otel | otel_partial | estimate | estimate_pending | none
		last_turn_key          TEXT        NOT NULL DEFAULT '',
		last_turn_status       TEXT        NOT NULL DEFAULT '',
		last_context_tokens    BIGINT,
		last_context_window    INT,
		last_context_source    TEXT        NOT NULL DEFAULT 'unknown', -- observed | model_default | unknown
		large_window_seen      BOOLEAN     NOT NULL DEFAULT false, -- a hint for the session, not a window
		open_questions         INT,                               -- NULL: undeterminable or not applicable
		open_question_turn_key TEXT        NOT NULL DEFAULT '',
		has_plan_file          BOOLEAN     NOT NULL DEFAULT false,
		used_plan_mode         BOOLEAN     NOT NULL DEFAULT false,
		detail                 JSONB       NOT NULL DEFAULT '{}',
		output_hash            BYTEA       NOT NULL,              -- spares an identical rewrite; invalidates nothing
		compute_ms             INT         NOT NULL DEFAULT 0,
		recompute_count        BIGINT      NOT NULL DEFAULT 0,
		source_generation      BIGINT      NOT NULL,
		rules_version          INT         NOT NULL,
		computed_at            TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	// The owner indexes serve role=user lists, newest activity first; user_ids
	// serves an administrator's pick of a user, mixed sessions included;
	// project_hash serves projectSessionTables; first_record_at serves the
	// retention candidates (p1b-design §6.3).
	`DO $$ BEGIN
		IF to_regclass('idx_session_analysis_owner_user') IS NULL THEN
			CREATE INDEX IF NOT EXISTS idx_session_analysis_owner_user
				ON session_analysis_facts (owner_user_id, last_activity_at DESC) WHERE owner_user_id IS NOT NULL;
		END IF;
	END $$`,
	`DO $$ BEGIN
		IF to_regclass('idx_session_analysis_owner_profile') IS NULL THEN
			CREATE INDEX IF NOT EXISTS idx_session_analysis_owner_profile
				ON session_analysis_facts (owner_profile_email, last_activity_at DESC) WHERE owner_profile_email IS NOT NULL;
		END IF;
	END $$`,
	`DO $$ BEGIN
		IF to_regclass('idx_session_analysis_user_ids') IS NULL THEN
			CREATE INDEX IF NOT EXISTS idx_session_analysis_user_ids
				ON session_analysis_facts USING gin (user_ids);
		END IF;
	END $$`,
	`DO $$ BEGIN
		IF to_regclass('idx_session_analysis_project_hash') IS NULL THEN
			CREATE INDEX IF NOT EXISTS idx_session_analysis_project_hash
				ON session_analysis_facts (project_hash);
		END IF;
	END $$`,
	`DO $$ BEGIN
		IF to_regclass('idx_session_analysis_first_record') IS NULL THEN
			CREATE INDEX IF NOT EXISTS idx_session_analysis_first_record
				ON session_analysis_facts (first_record_at);
		END IF;
	END $$`,
	// session_turn_facts: one row per turn, Claude's keyed by the uuid of the line
	// that opened it and Codex's by its root turn id. login_email is the opening
	// line's, so a /login mid-session shows turn by turn; who may read a turn is
	// decided by its session's row above (p1b-design §2.2).
	`CREATE TABLE IF NOT EXISTS session_turn_facts (
		session_id                TEXT        NOT NULL,
		turn_key                  TEXT        NOT NULL, -- Codex with no turn id: 'cx:' || ts of the opening user row
		agent                     TEXT        NOT NULL,
		ordinal                   INT         NOT NULL, -- start order from 1, abandoned branches included
		request_no                INT,                  -- NULL: not a request
		branch                    TEXT        NOT NULL, -- active | abandoned
		opener_copied             BOOLEAN     NOT NULL DEFAULT false,
		prompt_id                 TEXT        NOT NULL DEFAULT '',
		prompt_id_shared          BOOLEAN     NOT NULL DEFAULT false, -- another turn has the same prompt_id
		opener_uuid               TEXT        NOT NULL DEFAULT '',
		started_at                TIMESTAMPTZ NOT NULL,
		ended_at                  TIMESTAMPTZ NOT NULL,
		input_kind                TEXT        NOT NULL,
		is_request                BOOLEAN     NOT NULL,
		login_email               TEXT        NOT NULL DEFAULT '',
		status                    TEXT        NOT NULL, -- ended | interrupted | error | no_response | open | undeterminable
		status_reason             TEXT        NOT NULL DEFAULT '',
		permission_mode           TEXT        NOT NULL DEFAULT '',
		denied                    BOOLEAN     NOT NULL DEFAULT false,
		image_count               INT         NOT NULL DEFAULT 0,
		preview                   TEXT,                 -- first line of the first input, 160 bytes
		preview_state             TEXT        NOT NULL, -- ok | redacted | withheld | none
		api_requests              INT         NOT NULL DEFAULT 0, -- API calls, not request_no
		input_tokens              BIGINT      NOT NULL DEFAULT 0,
		output_tokens             BIGINT      NOT NULL DEFAULT 0,
		cache_read_tokens         BIGINT      NOT NULL DEFAULT 0,
		cache_create_tokens       BIGINT      NOT NULL DEFAULT 0,
		cost_usd                  DOUBLE PRECISION,
		cost_source               TEXT        NOT NULL,
		cost_reason               TEXT        NOT NULL DEFAULT '', -- why unattributable: pre_p0 | shared_prompt_id
		context_tokens            BIGINT,
		context_window            INT,
		context_source            TEXT        NOT NULL, -- observed | model_default | unknown
		tool_calls                INT         NOT NULL DEFAULT 0,
		subagents                 INT         NOT NULL DEFAULT 0,
		last_assistant_message_id TEXT        NOT NULL DEFAULT '',
		detail                    JSONB       NOT NULL DEFAULT '{}',
		rules_version             INT         NOT NULL,
		PRIMARY KEY (session_id, turn_key)
	)`,
	`DO $$ BEGIN
		IF to_regclass('idx_session_turn_facts_started') IS NULL THEN
			CREATE INDEX IF NOT EXISTS idx_session_turn_facts_started
				ON session_turn_facts (session_id, started_at);
		END IF;
	END $$`,
	// session_activity_segments: a session's activity, cut wherever it went quiet
	// for 15 minutes, for the weekly calendar. The owner columns are copies of the
	// session row's, so the calendar filters by owner on this table alone
	// (p1b-design §2.2).
	`CREATE TABLE IF NOT EXISTS session_activity_segments (
		session_id          TEXT        NOT NULL,
		seg_start           TIMESTAMPTZ NOT NULL,
		seg_end             TIMESTAMPTZ NOT NULL CHECK (seg_end >= seg_start),
		owner_user_id       TEXT,
		owner_profile_email TEXT,
		agent               TEXT        NOT NULL,
		records             INT         NOT NULL,
		human_inputs        INT         NOT NULL,
		rules_version       INT         NOT NULL,
		PRIMARY KEY (session_id, seg_start)
	)`,
	`DO $$ BEGIN
		IF to_regclass('idx_session_segments_owner_user') IS NULL THEN
			CREATE INDEX IF NOT EXISTS idx_session_segments_owner_user
				ON session_activity_segments (owner_user_id, seg_end) WHERE owner_user_id IS NOT NULL;
		END IF;
	END $$`,
	`DO $$ BEGIN
		IF to_regclass('idx_session_segments_owner_profile') IS NULL THEN
			CREATE INDEX IF NOT EXISTS idx_session_segments_owner_profile
				ON session_activity_segments (owner_profile_email, seg_end) WHERE owner_profile_email IS NOT NULL;
		END IF;
	END $$`,
	// '[]', not the default '[)': a segment of one line starts and ends at the same
	// instant, and '[)' makes that range empty, so no overlap query would find it.
	`DO $$ BEGIN
		IF to_regclass('idx_session_segments_range') IS NULL THEN
			CREATE INDEX IF NOT EXISTS idx_session_segments_range
				ON session_activity_segments USING gist (tstzrange(seg_start, seg_end, '[]'));
		END IF;
	END $$`,
	// session_fact_dirty: the worker's queue, one row per session whose facts are
	// owed a recompute. No identity column: a row is a session id and its schedule.
	// generation comes from a sequence, as the rebuild queue's does, so a value is
	// never handed out twice, even after the row is cleared and queued again.
	`CREATE SEQUENCE IF NOT EXISTS session_fact_generation`,
	`CREATE TABLE IF NOT EXISTS session_fact_dirty (
		session_id         TEXT        PRIMARY KEY,
		generation         BIGINT      NOT NULL DEFAULT nextval('session_fact_generation'),
		removal_generation BIGINT      NOT NULL DEFAULT 0, -- generation of the bump that deleted source rows
		scan_from_id       BIGINT,                         -- least new session_records.id; NULL: none new, 0: all
		first_dirty_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
		not_before         TIMESTAMPTZ NOT NULL DEFAULT now(),
		priority           SMALLINT    NOT NULL DEFAULT 0, -- 0 live, 1 bulk
		reasons            INT         NOT NULL DEFAULT 0, -- bit set
		attempts           INT         NOT NULL DEFAULT 0,
		retry_at           TIMESTAMPTZ,                    -- not picked before this; a bump leaves one still ahead alone
		last_error         TEXT        NOT NULL DEFAULT ''
	)`,
	`DO $$ BEGIN
		IF to_regclass('idx_session_fact_dirty_order') IS NULL THEN
			CREATE INDEX IF NOT EXISTS idx_session_fact_dirty_order
				ON session_fact_dirty (priority, first_dirty_at);
		END IF;
	END $$`,
	// session_record_copies: the session_records rows judged copies of a line that
	// another session keeps -- a fork or a resume writes the same lines again under
	// a new session -- keyed by the copy's row id. original_session_id is the
	// session that keeps the line. No identity column: a row is about one record.
	`CREATE TABLE IF NOT EXISTS session_record_copies (
		srec_id             BIGINT      PRIMARY KEY, -- every row of a copied line's request, not the line alone
		session_id          TEXT        NOT NULL,
		original_session_id TEXT        NOT NULL,
		record_type         TEXT        NOT NULL,
		ts                  TIMESTAMPTZ NOT NULL
	)`,
	`DO $$ BEGIN
		IF to_regclass('idx_session_record_copies_session') IS NULL THEN
			CREATE INDEX IF NOT EXISTS idx_session_record_copies_session
				ON session_record_copies (session_id);
		END IF;
	END $$`,
	`DO $$ BEGIN
		IF to_regclass('idx_session_record_copies_original') IS NULL THEN
			CREATE INDEX IF NOT EXISTS idx_session_record_copies_original
				ON session_record_copies (original_session_id);
		END IF;
	END $$`,
}

// dropDependentViews runs before every migration pass; dependentViews runs after.
// See PgStore.Migrate for why the split exists.
var dropDependentViews = []string{
	`DROP VIEW IF EXISTS visible_events`,
	`DROP VIEW IF EXISTS visible_metrics`,
	`DROP VIEW IF EXISTS visible_session_records`,
	// unified_events is itself replaced by the pass below, and CREATE OR REPLACE VIEW
	// only ever appends columns at the end -- inserting one mid-list reads to Postgres
	// as renaming the n-th column and fails 42P16 on any existing database. Dropping it
	// here (after its dependents above, which is why the order matters) lets the pass
	// recreate it with whatever column list the code now declares.
	`DROP VIEW IF EXISTS unified_events`,
}

// dependentViews: what the dashboard reads. Three views because exclusion has to
// hold on both axes — otel_events carries the cost, session_records carries the
// conversation text, and hiding only one leaves the account half-visible.
//
// Maintenance and integrity queries must keep reading the unfiltered sources:
//   - CleanupOrphanSessionRecords deletes session_records with no unified_events
//     row, so a filtered read there would treat an excluded account's sessions as
//     orphans and delete them — reversible exclusion turned into data loss.
//   - SkillUsage's dedupe anti-join drops JSONL rows that already have a metric;
//     filtering it lets the JSONL copies resurface, so exclusion would *raise* the
//     counts it is meant to remove.
//   - /health reports ingest liveness, not dashboard content.
//
// Comparisons are case-folded: login_email is stored exactly as the client sent it
// and nothing lowercases it on ingest, so an exact match would silently miss.
//
// Known gap: a session that never emitted OTEL carries no account identity at all
// (login_email is blank in session_records and in claude_imputed_cost, which by
// definition only holds sessions without OTEL). Such sessions cannot be excluded by
// account — the same attribution gap tracked in #140.
//
// visible_session_records' second condition reads excluded_sessions, a materialized
// table, instead of joining otel_events directly (#298): the direct anti-join scanned
// every otel_events row (millions) for every session_records row, ~224ms of a 563ms
// dashboard query with as few as 2 excluded accounts. excluded_sessions is kept in
// sync by the exclusion recompute (on every exclusion change — see
// applyExclusionChange) and RefreshExcludedSessionsIncremental
// (incremental, on the periodic refresh loop in cmd/cctraced — same cadence as the
// login_email backfill / imputed-cost refresh, since otel_events ingest is a bulk
// CopyFrom with no per-row hook to update this table at write time).
//
// Tradeoff: the unmaterialized view above (visible_events, visible_metrics) is always
// exactly correct. This one is not — a session that starts under a newly-excluded
// account between two ticks of the periodic refresh is visible for up to that
// interval (30 minutes as of this writing). That window closes for good once #140 is
// resolved (session_records.login_email fully populated): the first NOT EXISTS above
// would then cover every row and this second condition, and excluded_sessions with
// it, could be dropped entirely.
var dependentViews = append([]string{
	// The three-key exclusion (excludedAccountPredicateSQL). Of unified_events'
	// arms only codex_imputed_cost projects a billing id; every other arm reports ''
	// there, so only the login_email key can match them. The quota read path already
	// applied the billing keys; the session and cost paths did not, so an excluded
	// personal Codex account kept its sessions and cost on every list while only
	// its burn hid. visible_metrics carries its own rules; see the comment above it.
	`CREATE OR REPLACE VIEW visible_events AS
	SELECT * FROM unified_events e
	WHERE ` + excludedAccountPredicateSQL("e"),

	// visible_metrics judges Codex rows by billing account, never by login_email:
	// a Codex metric's login_email is the cctrace dashboard address filled in at
	// ingest, not an account, and visible_session_records does not hide Codex
	// sessions by it either. Matching it hid every Codex metric of a person whose
	// dashboard address happened to be excluded, visible accounts included (#715).
	// Rows that name no account are judged by their minute
	// (excluded_codex_metric_minutes); a minute with no decision falls back to
	// the per-profile rule (excluded_codex_metric_profiles).
	`CREATE OR REPLACE VIEW visible_metrics AS
	SELECT * FROM otel_metrics m
	WHERE (m.agent = 'codex' OR NOT EXISTS (
		SELECT 1 FROM excluded_accounts x WHERE lower(x.login_email) = lower(m.login_email)
	))
	AND NOT EXISTS (
		SELECT 1 FROM excluded_billing_accounts b
		WHERE b.billing_provider = m.billing_provider AND b.account_id = m.account_id
	)
	AND NOT EXISTS (
		SELECT 1 FROM excluded_billing_links l
		WHERE l.billing_provider = m.billing_provider AND l.account_id = m.account_id
	)
	AND NOT (m.agent = 'codex' AND m.account_id = '' AND COALESCE(
		(SELECT t.hidden FROM excluded_codex_metric_minutes t
			WHERE t.profile_email = lower(m.profile_email) AND t.minute = date_trunc('minute', m.ts)),
		EXISTS (SELECT 1 FROM excluded_codex_metric_profiles p WHERE p.profile_email = lower(m.profile_email))
	))`,

	// session_records.login_email is only filled by the periodic otel backfill, so it
	// is blank for freshly synced rows and stays blank for sessions where the account
	// was switched mid-session. Matching on the session's OTEL events as well closes
	// both holes: otel_events.login_email is written at ingest and is the reliable key.
	`CREATE OR REPLACE VIEW visible_session_records AS
	SELECT * FROM session_records sr
	WHERE ` + excludedAccountPredicateSQL("sr") + `
	AND NOT EXISTS (
		SELECT 1 FROM excluded_sessions es
		WHERE es.session_id = sr.session_id AND sr.session_id <> ''
	)`,

	// WeeklyInsights asks whether each task segment's boundary record is still
	// visible, which is a lookup by session_records.id -- the one column the table
	// had no index on. Without one the planner cannot probe, so it built a hash of
	// every visible row to satisfy 357 of them: 5,030,983 rows appended from a Seq
	// Scan of every chunk, hashed down to 3,853,528, spilling to disk (batches 32
	// to 64). Measured on a copy of prod: 2,618ms for that one query, against 465ms
	// for the next slowest of the five the page runs. With the index, 37.5ms.
	//
	// Deliberately id alone, not (id, ts). The lookup arrives with an id and no
	// time bound -- boundary_record_id carries no window -- so a leading ts would
	// not be usable and a trailing one only widens the entry for nothing.
	`CREATE INDEX IF NOT EXISTS idx_srec_id ON session_records (id)`,

	// Owner classification reads these scalar columns for every raw row in the
	// candidate sessions. The session_id index narrows the lookup, and INCLUDE
	// makes the raw owner basis available without fetching transcript-bearing
	// heap pages; raw is intentionally excluded from the index.
	`CREATE INDEX IF NOT EXISTS idx_srec_owner_basis ON session_records (session_id)
		INCLUDE (user_id, profile_email, login_email, billing_provider, account_id, project_hash)`,

	// The aggregate the dashboards read, kept so that history survives the raw
	// tables. Today the only reason a 2026-04 cost still renders is that
	// session_records duplicates it and has no retention -- otel_events was cut at
	// 90 days. That makes 23 GB of conversation content the load-bearing copy of a
	// number that fits in a few hundred bytes, and it is why SESSION_RETENTION_DAYS
	// (#265) exists but cannot be set: pulling it would take the history with it.
	//
	// Hour buckets, not day. The trend queries bucket with
	// date_trunc(granularity, ts AT TIME ZONE tz) and tz arrives per request from
	// the browser -- Asia/Seoul in practice -- so a day rollup would freeze one
	// timezone's midnight into the stored key and answer every other timezone
	// wrong. Hours re-bucket into any whole-hour offset at read time. Offsets that
	// are not whole hours (+05:30, +05:45) cannot be served exactly; that is a
	// known limit, not an oversight.
	//
	// Every axis the three trend queries filter or group on is a key column, and
	// measured on prod that costs nothing worth trading: the full-fat rollup is
	// 21,397 rows / 4,616 kB for 164 days (221 bytes/row, ~10 MB per year) against
	// 2,708,885 raw rows. There is no cheaper axis set worth the questions it would
	// permanently give up.
	//
	// project_hash earns its place despite living only on session_records: the
	// project filter reaches events by joining through it, so leaving it out means
	// per-project history dies the moment session_records gets a retention policy --
	// the exact event this table exists to survive.
	//
	// cache_read/cache_create are stored although no trend query reads them today.
	// They cost columns, not rows, and session_overview_rollups is already unusable
	// for coverage work precisely because it lacks them.
	`CREATE TABLE IF NOT EXISTS usage_hourly_rollups (
		bucket TIMESTAMPTZ NOT NULL,
		model TEXT NOT NULL DEFAULT '',
		user_id TEXT NOT NULL DEFAULT '',
		profile_email TEXT NOT NULL DEFAULT '',
		login_email TEXT NOT NULL DEFAULT '',
		user_team TEXT NOT NULL DEFAULT '',
		agent TEXT NOT NULL DEFAULT '',
		billing_provider TEXT NOT NULL DEFAULT '',
		project_hash TEXT NOT NULL DEFAULT '',
		cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
		input_tokens BIGINT NOT NULL DEFAULT 0,
		output_tokens BIGINT NOT NULL DEFAULT 0,
		cache_read_tokens BIGINT NOT NULL DEFAULT 0,
		cache_create_tokens BIGINT NOT NULL DEFAULT 0,
		event_count BIGINT NOT NULL DEFAULT 0,
		PRIMARY KEY (bucket, model, user_id, profile_email, login_email, user_team,
			agent, billing_provider, project_hash)
	)`,
	// The refresh deletes and reinserts by bucket range, and every read is a range
	// scan over buckets, so bucket leads. The PK already starts with bucket, but it
	// carries eight trailing text columns; a narrow bucket index is what the range
	// delete actually wants.
	`CREATE INDEX IF NOT EXISTS idx_usage_hourly_rollups_bucket
		ON usage_hourly_rollups (bucket DESC)`,
	// A rebuild of usage_hourly_rollups an exclusion change is owed. It ran inside
	// the exclude request and took ~100s on production data, so a client that gave
	// up cancelled it after the exclusion had committed, and buckets older than the
	// periodic window kept their pre-exclusion cost. The change now records the
	// rebuild here, in its own transaction, and the server's worker runs it.
	//
	// One row naming who is affected -- addresses, and billing accounts as
	// {billing_provider, account_id} objects -- not from when: finding the earliest
	// row is a full scan of unified_events, too slow for the request, so the worker
	// does it. Requests merge as a union. generation comes from a sequence, so it
	// never repeats after the row is cleared and re-created, and the worker clears
	// only the request it actually rebuilt for.
	`CREATE SEQUENCE IF NOT EXISTS usage_rollup_rebuild_generation`,
	`CREATE TABLE IF NOT EXISTS usage_rollup_rebuild_requests (
		id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
		emails TEXT[] NOT NULL DEFAULT '{}',
		accounts JSONB NOT NULL DEFAULT '[]',
		generation BIGINT NOT NULL DEFAULT nextval('usage_rollup_rebuild_generation')
	)`,
	// A build before release queued a start time instead. Its table is brought to
	// this shape, and a row it left names no one, which the worker answers with a
	// full rebuild. Each ALTER runs only when its column is off: this runs on every
	// boot, and an ALTER takes ACCESS EXCLUSIVE even when IF [NOT] EXISTS makes it a
	// no-op -- it would wait out a running rebuild's row lock (~100s on production
	// data) and fail the boot on the lock budget.
	`DO $$
	DECLARE
		has_col CONSTANT TEXT := 'SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ''usage_rollup_rebuild_requests''
			  AND column_name = $1)';
		present BOOLEAN;
	BEGIN
		EXECUTE has_col INTO present USING 'from_ts';
		IF present THEN
			ALTER TABLE usage_rollup_rebuild_requests DROP COLUMN from_ts;
		END IF;
		EXECUTE has_col INTO present USING 'emails';
		IF NOT present THEN
			ALTER TABLE usage_rollup_rebuild_requests ADD COLUMN emails TEXT[] NOT NULL DEFAULT '{}';
		END IF;
		EXECUTE has_col INTO present USING 'accounts';
		IF NOT present THEN
			ALTER TABLE usage_rollup_rebuild_requests ADD COLUMN accounts JSONB NOT NULL DEFAULT '[]';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'usage_rollup_rebuild_requests'
			  AND column_name = 'generation' AND column_default LIKE 'nextval(%') THEN
			ALTER TABLE usage_rollup_rebuild_requests
				ALTER COLUMN generation SET DEFAULT nextval('usage_rollup_rebuild_generation');
		END IF;
	END $$`,

	// The measured side of the coverage estimate, pre-aggregated.
	//
	// The estimate fits a tokens-per-percent constant over a 28-day window, and
	// that window was being aggregated from raw events on every request. Measured
	// on a prod snapshot: 773,384 rows in, 14,366 out, 757ms -- and EXPLAIN put
	// almost all of it in the sort feeding the aggregate, not in the scan (67ms of
	// 953ms). Reading the 14,366 rows back instead takes 8.2ms.
	//
	// Minute grain is not negotiable: segment() attributes measured tokens to the
	// span between two meter readings, and readings land ~5 minutes apart at
	// arbitrary offsets, so any coarser bucket straddles a boundary and lands on
	// the wrong interval. usage_hourly_rollups cannot serve this for the same
	// reason.
	//
	// Unlike a cached fit constant -- which the drift replay refuted, because k
	// jumps 13-21 points when an account crosses the minimum-segment threshold --
	// these are measured facts. They do not move once the events are in.
	`CREATE TABLE IF NOT EXISTS coverage_measured_minutes (
		login_email TEXT NOT NULL,
		minute TIMESTAMPTZ NOT NULL,
		tokens DOUBLE PRECISION NOT NULL DEFAULT 0,
		chart_tokens DOUBLE PRECISION NOT NULL DEFAULT 0,
		cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
		PRIMARY KEY (login_email, minute)
	)`,
	// The refresh deletes and reinserts by minute range, and the read is a range
	// scan bounded by the fit window, so minute leads.
	`CREATE INDEX IF NOT EXISTS idx_coverage_measured_minutes_minute
		ON coverage_measured_minutes (minute DESC)`,

	// Provenance for codex_model_rates. The seed above is no longer the only
	// writer: cctraced reads OpenAI's published price table daily and upserts it
	// (internal/codexrates). These two columns say which source last moved a
	// row's price and when, so a wrong rate can be traced to a parser rather than
	// guessed at. Rows never touched by a sync keep source = 'seed'.
	`ALTER TABLE codex_model_rates ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'seed'`,
	`ALTER TABLE codex_model_rates ADD COLUMN IF NOT EXISTS fetched_at TIMESTAMPTZ`,

	// flat_rate_models: models an admin has declared "$0 is the right cost" --
	// local models, subscription SKUs. A model no rate matches costs $0 either way,
	// and without this mark "price unknown" and "free by design" look the same
	// (#441). Marked models leave the admin's unpriced-model list and nothing else:
	// no cost is computed from this table. model is the raw id, case kept, so it
	// matches the id the list shows.
	`CREATE TABLE IF NOT EXISTS flat_rate_models (
		agent TEXT NOT NULL CHECK (agent <> ''),
		model TEXT NOT NULL CHECK (model <> ''),
		reason TEXT NOT NULL DEFAULT '',
		created_by TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (agent, model)
	)`,

	// The 90-day rolling view model_rate_buckets replaces. Dropped in the same
	// release that stops reading it, so a later join cannot silently pick up a
	// scale that is still recomputing all of history every 30 minutes.
	`DROP VIEW IF EXISTS model_rates`,

	// claude_weighted_tokens: the one expression that both the rate fit and the
	// cost application must use. A scale is $ per weighted token, so if the two
	// sides ever weight a row differently the number means nothing -- it is the
	// invariant, not a helper, which is why it is a function and not copy-pasted
	// SQL in two files.
	//
	// Anthropic's published prices hold a fixed shape across every model:
	// output = 5x input, 5m cache write = 1.25x input, cache read = 0.1x input --
	// except Fable 5.1 and Mythos 5.1, where a documented footnote puts cache
	// reads at 0.025x. Measured on production: billing Fable 5.1 cache reads at
	// 0.1x over-predicts its spend by 1.5x, at 0.025x the residual drops to the
	// same ~1.13 every other model shows.
	//
	// 1h cache writes really cost 2x, not 1.25x, and nothing here distinguishes
	// them -- the back-solved scale absorbs whatever mix a model actually ran
	// (measured: $9.08/MTok against a 5m list price of $6.25, so ~75% 1h). That
	// absorption is why the scale is fit rather than read off a price list.
	`CREATE OR REPLACE FUNCTION claude_weighted_tokens(
		model TEXT, input BIGINT, output BIGINT, cache_read BIGINT, cache_write BIGINT
	) RETURNS DOUBLE PRECISION
	LANGUAGE SQL IMMUTABLE PARALLEL SAFE AS $$
		SELECT COALESCE(input,0)::float8
		     + 5.0 * COALESCE(output,0)
		     + CASE WHEN model LIKE 'claude-fable-5-1%' OR model LIKE 'claude-mythos-5-1%'
		            THEN 0.025 ELSE 0.1 END * COALESCE(cache_read,0)
		     + 1.25 * COALESCE(cache_write,0)
	$$`,

	// model_rate_buckets: the OTEL-derived scale, cut into weeks and frozen once
	// written. Replaces a 90-day rolling view whose every property was a bug:
	// the scale it handed to a March row was fit on June traffic, a model that
	// stopped appearing lost its rate entirely (the JOIN dropped those rows out
	// of the cost table), and the whole history was recomputed every 30 minutes
	// so past spend moved.
	//
	// Weeks, not months, because prices change mid-month: Sonnet 5 dropped from
	// $3/MTok to $2 between 2026-08-21 and 2026-08-24 and monthly buckets would
	// average the two. Weekly buckets pick the change up on their own -- nobody
	// has to notice an announcement, which is the advantage this design has over
	// a published price table.
	`CREATE TABLE IF NOT EXISTS model_rate_buckets (
		model TEXT NOT NULL,
		bucket_start DATE NOT NULL,
		scale DOUBLE PRECISION NOT NULL,
		sample_rows BIGINT NOT NULL,
		computed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (model, bucket_start)
	)`,

	// Which otel_events rows the bucket refresh has already folded in. An
	// ingestion-order cursor, not a timestamp one, for the reason
	// codex_imputed_cursor gives: rows do not arrive newest-first. A dev-sync
	// bulk-loads months of history into a database that already has recent
	// buckets, and a `ts >` cursor would step straight over all of it, leaving
	// those weeks with no bucket forever.
	`CREATE TABLE IF NOT EXISTS model_rate_cursor (
		only_row BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (only_row),
		last_event_id BIGINT NOT NULL DEFAULT 0
	)`,
	`INSERT INTO model_rate_cursor (only_row, last_event_id) VALUES (TRUE, 0)
		ON CONFLICT (only_row) DO NOTHING`,
	`CREATE INDEX IF NOT EXISTS idx_otel_claude_cost_id ON otel_events (id)
		WHERE agent = 'claude' AND cost_usd > 0`,

	// claude_model_rates: Anthropic's published input price, used only when a
	// model has no OTEL observation to fit against at all -- a model too new to
	// have been billed yet, or one this deployment only ever ran offline. Every
	// other price category is a fixed multiple of this one (see
	// claude_weighted_tokens), so one column is the whole price.
	//
	// Longest-prefix match, like codex_model_rates: 'claude-haiku-4-5' has to
	// cover 'claude-haiku-4-5-20251001', and 'claude-opus-4-6' has to beat
	// 'claude-opus-4'. A model matching nothing gets no cost rather than a
	// guessed one (#441).
	`CREATE TABLE IF NOT EXISTS claude_model_rates (
		model_prefix TEXT NOT NULL,
		effective_from DATE NOT NULL DEFAULT '-infinity',
		input_rate DOUBLE PRECISION NOT NULL,
		source TEXT NOT NULL DEFAULT 'seed',
		PRIMARY KEY (model_prefix, effective_from)
	)`,

	// $/MTok base input price, from docs.anthropic.com/en/docs/about-claude/pricing.
	// Verified against billed OTEL cost: opus-4-6 and opus-4-7 reproduce their
	// months at a ratio of exactly 1.0000, opus-5 at 1.0000 on rows with no cache
	// writes. Retired models are kept -- deleting a row reprices that model's
	// history to $0.
	`INSERT INTO claude_model_rates (model_prefix, input_rate) VALUES
		('claude-fable-5', 10.0), ('claude-mythos-5', 10.0),
		('claude-opus-5', 5.0), ('claude-opus-4-8', 5.0), ('claude-opus-4-7', 5.0),
		('claude-opus-4-6', 5.0), ('claude-opus-4-5', 5.0),
		('claude-opus-4-1', 15.0), ('claude-opus-4', 15.0),
		('claude-sonnet-5', 3.0), ('claude-sonnet-4-6', 3.0), ('claude-sonnet-4-5', 3.0),
		('claude-sonnet-4', 3.0),
		('claude-haiku-4-5', 1.0), ('claude-haiku-3-5', 0.80)
	ON CONFLICT (model_prefix, effective_from) DO NOTHING`,

	// Sonnet 5's price cut, as a dated row. Found in our own billing, not in an
	// announcement: against $3/MTok the daily ratio holds at ~1.0 through
	// 2026-08-21 and falls to ~0.70 from 2026-08-24, while against $2/MTok it
	// does the reverse (2026-08-28 lands on exactly 1.0000). The 22nd and 23rd
	// are a weekend with no traffic, so the boundary is only observable to
	// within those three days; dating it to the 22nd costs nothing because no
	// row falls in the gap.
	`INSERT INTO claude_model_rates (model_prefix, effective_from, input_rate, source) VALUES
		('claude-sonnet-5', DATE '2026-08-22', 2.0, 'measured-billing')
	ON CONFLICT (model_prefix, effective_from) DO NOTHING`,

	// Which rate priced each row, so the dashboard can separate spend measured
	// against real billing from spend carried in from another week or read off a
	// price list.
	`ALTER TABLE claude_imputed_cost ADD COLUMN IF NOT EXISTS rate_source TEXT NOT NULL DEFAULT ''`,

	// How far the task-segment reconciler has folded OTEL in. Segment token counts
	// come from session_records and tool counts from otel_events, and only a
	// session_records write recomputes the fact -- so tool events that arrive after
	// the sync leave the fact saying zero. A finished session has no next write, so
	// its last segment stays wrong forever.
	//
	// Ingestion order, not a timestamp: a client that was offline uploads OTEL for
	// sessions that ended days ago, and those rows are old by ts and new by arrival.
	// codex_imputed_cursor makes the same choice for the same reason.
	//
	// Measured on production: 98 segments had tool events their fact did not count.
	// Small, but silent and permanent -- and the same mechanism underlies every
	// late-OTEL session, not only the ones that happened to be observed.
	`CREATE TABLE IF NOT EXISTS task_segment_otel_cursor (
		only_row BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (only_row),
		last_event_id BIGINT NOT NULL DEFAULT 0
	)`,
	`INSERT INTO task_segment_otel_cursor (only_row, last_event_id) VALUES (TRUE, 0)
		ON CONFLICT (only_row) DO NOTHING`,
	// The reconciler asks "any tool events past the cursor?" and must not walk every
	// chunk of otel_events to answer. Partial, so it carries tool rows only.
	`CREATE INDEX IF NOT EXISTS idx_otel_tool_result_id ON otel_events (id)
		WHERE event_name = 'tool_result'`,

	// How far the reclassification sweep has walked session_records, and which rule
	// set it was walking for. Bumping insights.ClassifierVersion says the stored
	// verdicts are stale, but the backfill only ever looked at rows with no verdict
	// at all -- so a bump changed nothing for the rows already labelled, and the
	// chart would mix one week under the new rules with every earlier week under
	// the old (#429).
	//
	// A cursor rather than an OR in the backfill's candidate query: that query is
	// served by idx_srec_unclassified, the only index that can answer an empty
	// task_type, and a classifier_version predicate beside it falls back to a
	// sequential scan over every chunk. Walking the primary key in bounded batches
	// keeps the sweep off that path entirely.
	//
	// version is stored with the cursor so a later bump resets the walk on its own,
	// rather than needing somebody to remember.
	`CREATE TABLE IF NOT EXISTS task_type_reclassify_cursor (
		only_row BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (only_row),
		last_record_id BIGINT NOT NULL DEFAULT 0,
		version TEXT NOT NULL DEFAULT ''
	)`,
	`INSERT INTO task_type_reclassify_cursor (only_row, last_record_id, version) VALUES (TRUE, 0, '')
		ON CONFLICT (only_row) DO NOTHING`,

	// The two price cuts the changelog documents, as dated rows. The seeded
	// -infinity rows above keep the pre-cut prices, so spend on either side of a
	// date is billed at what it actually cost:
	//
	//   Jul 30 2026 -- gpt-5.6-luna 80% cheaper, gpt-5.6-terra 20% cheaper
	//   Aug 21 2026 -- gpt-5.6-sol input 20% / output 33% cheaper
	//
	// Measured on production spend for these three models: billing it all at the
	// old prices overstates by ~$2,037; billing it all at the new ones understates
	// by ~$2,526. These rows are how the total lands between the two.
	//
	// DO NOTHING, not DO UPDATE: once these rows exist the daily sync owns them,
	// and a boot must not drag a later correction back to these values.
	`INSERT INTO codex_model_rates
		(model_prefix, input_rate, output_rate, cache_read_rate, effective_from, source) VALUES
		('gpt-5.6-luna', 0.20, 1.20, 0.02, DATE '2026-07-30', 'changelog-backfill'),
		('gpt-5.6-terra', 2.00, 12.00, 0.20, DATE '2026-07-30', 'changelog-backfill'),
		('gpt-5.6-sol', 4.00, 20.00, 0.40, DATE '2026-08-21', 'changelog-backfill')
	ON CONFLICT (model_prefix, effective_from) DO NOTHING`,
	// The session sweep reaches the repair staging tables too (#396). Their primary
	// keys lead with repair_name, so a per-session DELETE would scan -- cheap while
	// they are empty, which is most of the time, and not while a repair is mid-flight
	// with millions of staged rows.
	`CREATE INDEX IF NOT EXISTS idx_login_email_intervals_session
		ON login_email_history_session_intervals (session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_codex_repair_candidates_session
		ON codex_login_email_repair_candidates (session_id)`,
	// The admin's default for the weekly automatic run, on the single ai_settings
	// row beside the runtime choice and the on/off switch. NULL is no admin word,
	// as it already is for enabled: the built-in default decides until an admin
	// chooses, and each column is written on its own so saving a weekday states
	// no opinion about the switch.
	`ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS auto_enabled BOOLEAN`,
	`ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS auto_weekday INTEGER`,
	`ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS auto_hour INTEGER`,
	`ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS auto_minute INTEGER`,
	// One user's override of that default. A NULL column keeps following the
	// admin's value rather than meaning "off" or "midnight", so a user who moves
	// only the hour still tracks a later change of weekday.
	//
	// tz is the zone the weekday and time are read in. The server keeps no other
	// record of a user's zone -- the screen sends it with every request -- so this
	// row holds the one in force when the user saved.
	`CREATE TABLE IF NOT EXISTS ai_report_schedules (
		dashboard_user_id BIGINT PRIMARY KEY REFERENCES dashboard_users(id) ON DELETE CASCADE,
		enabled           BOOLEAN,
		weekday           INTEGER CHECK (weekday BETWEEN 0 AND 6),
		hour              INTEGER CHECK (hour BETWEEN 0 AND 23),
		minute            INTEGER CHECK (minute BETWEEN 0 AND 59),
		tz                TEXT NOT NULL DEFAULT '',
		updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	// Who started a run. The scheduler decides whether to fire by asking if it
	// already did for that week, so a run the user started by hand must not
	// answer for it -- one manual report would otherwise swallow that week's
	// automatic one. Existing rows are manual, which is what they were.
	// Not named "trigger": that is a reserved word and would need quoting
	// everywhere it appears.
	`ALTER TABLE ai_report_runs ADD COLUMN IF NOT EXISTS started_by TEXT NOT NULL DEFAULT 'manual'`,
	`CREATE INDEX IF NOT EXISTS idx_ai_report_runs_scheduled
		ON ai_report_runs (dashboard_user_id, week) WHERE started_by = 'schedule'`,
	// Once per user per week, enforced by the database rather than by the check
	// the scheduler makes before it starts a run. That check and the insert are
	// seconds apart -- the runtime lookup, the consent read and the segment query
	// all sit between them -- so two instances can both pass it. The unique index
	// closes that window; the caller reads the violation as "already fired".
	//
	// Replaces the plain index above rather than adding a second one on the same
	// columns: keeping both would leave two structures to maintain for one rule.
	`DROP INDEX IF EXISTS idx_ai_report_runs_scheduled`,
	`CREATE UNIQUE INDEX IF NOT EXISTS uq_ai_report_runs_scheduled
		ON ai_report_runs (dashboard_user_id, week) WHERE started_by = 'schedule'`,
	// Snapshot claim index preparation. Existing snapshots are not backfilled here;
	// session_commit_claims_v1 must remain absent until a complete rebuild is verified.
	`CREATE TABLE IF NOT EXISTS session_commit_claims (
		session_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		profile_email TEXT NOT NULL,
		repository TEXT NOT NULL CHECK(repository <> ''),
		commit_hash TEXT NOT NULL CHECK(commit_hash ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
		PRIMARY KEY(session_id,user_id,profile_email,repository,commit_hash),
		FOREIGN KEY(session_id,user_id,profile_email)
			REFERENCES session_snapshots(session_id,user_id,profile_email) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_session_commit_claims_key ON session_commit_claims
		(repository COLLATE "C",commit_hash COLLATE "C",session_id COLLATE "C")`,
	`CREATE TABLE IF NOT EXISTS session_commit_claim_coverage (
		session_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		profile_email TEXT NOT NULL,
		observed_at TIMESTAMPTZ NOT NULL,
		source_witness BYTEA NOT NULL CHECK(octet_length(source_witness)=32),
		PRIMARY KEY(session_id,user_id,profile_email),
		FOREIGN KEY(session_id,user_id,profile_email)
			REFERENCES session_snapshots(session_id,user_id,profile_email) ON DELETE CASCADE
	)`,
	// usage_hourly_rollups as it stood before the P1t transition recounted it
	// (p1t-design §5.1): the "before" of the P1t-8 report, kept for audit. No
	// screen reads it. ArchiveUsageHourlyRollups writes it once.
	//
	// Summed down to (bucket, agent, billing_provider, model). No user, profile,
	// login, team, project or session column, on purpose: a table that names no
	// one has nothing for a user delete or merge to rewrite, and no session for
	// the privacy sweep to reach (TestEverySessionScopedTableIsJudged).
	`CREATE TABLE IF NOT EXISTS usage_hourly_rollups_archive_p1t (
		bucket TIMESTAMPTZ NOT NULL,
		agent TEXT NOT NULL DEFAULT '',
		billing_provider TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
		input_tokens BIGINT NOT NULL DEFAULT 0,
		output_tokens BIGINT NOT NULL DEFAULT 0,
		cache_read_tokens BIGINT NOT NULL DEFAULT 0,
		cache_create_tokens BIGINT NOT NULL DEFAULT 0,
		event_count BIGINT NOT NULL DEFAULT 0,
		archived_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (bucket, agent, billing_provider, model)
	)`,
	// Whether a weekly report waits for the user's own consent. NULL is no admin
	// word, and the default is not to ask: the admin already chose to send
	// records when turning reports on and picking the runtime.
	`ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS consent_required BOOLEAN`,
	// How many report runs go at once; the rest wait in line. NULL leaves it to
	// the server's CCTRACE_AI_MAX_CONCURRENT.
	`ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS max_concurrent INTEGER`,
}, sessionTimelineMigrations...)

// sessionTimelineMigrations contains the timeline provenance schema. It stays
// separate so the older timeline shapes can be exercised in tests.
var sessionTimelineMigrations = []string{
	// One row per raw unique tuple, not per non-unique BIGSERIAL id: either the
	// turn it belongs to with its roles there, or the reason it belongs to none.
	`CREATE TABLE IF NOT EXISTS session_timeline_members (
		session_id TEXT NOT NULL,
		source_ts TIMESTAMPTZ NOT NULL,
		source_record_type TEXT NOT NULL,
		source_profile_email TEXT NOT NULL,
		source_uuid TEXT NOT NULL,
		source_record_id BIGINT NOT NULL,
		turn_key TEXT NOT NULL,
		PRIMARY KEY(session_id,source_ts,source_record_type,source_profile_email,source_uuid)
	)`,
	// Source revision and account/copy policy epoch are bumped in the same
	// transaction as every fact writer; the witness also hashes row xmin, snapshot
	// content and copy markers so a missed dirty bump cannot publish stale facts.
	`CREATE SEQUENCE IF NOT EXISTS session_timeline_generation`,
	`CREATE TABLE IF NOT EXISTS session_timeline_revisions (
		session_id TEXT PRIMARY KEY, revision BIGINT NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS session_timeline_policy_epoch (
		only_row BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK(only_row), epoch BIGINT NOT NULL DEFAULT 0
	)`,
	`INSERT INTO session_timeline_policy_epoch(only_row) VALUES(TRUE) ON CONFLICT DO NOTHING`,
	`CREATE TABLE IF NOT EXISTS session_timeline_provenance (
		session_id TEXT PRIMARY KEY,
		tracking TEXT NOT NULL CHECK(tracking IN ('tracked','untracked')),
		source_revision BIGINT NOT NULL,
		policy_epoch BIGINT NOT NULL,
		source_witness BYTEA NOT NULL CHECK(octet_length(source_witness)=32),
		source_rows BIGINT NOT NULL,
		mapping_version INTEGER NOT NULL CHECK(mapping_version>0)
	)`,
	// The rows above came only from the preparation helper: every one names a
	// turn. Unmapped rows join them here, and each CHECK spells out both sides so
	// a NULL or an empty string cannot pass it.
	`ALTER TABLE session_timeline_members ALTER COLUMN turn_key DROP NOT NULL`,
	`ALTER TABLE session_timeline_members ADD COLUMN IF NOT EXISTS roles TEXT[] NOT NULL DEFAULT '{}'`,
	`ALTER TABLE session_timeline_members ADD COLUMN IF NOT EXISTS unmapped_reason TEXT`,
	`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'session_timeline_members_result'
			AND conrelid = 'session_timeline_members'::regclass) THEN
			ALTER TABLE session_timeline_members ADD CONSTRAINT session_timeline_members_result CHECK (
				((turn_key IS NOT NULL AND turn_key <> '' AND unmapped_reason IS NULL)
					OR (turn_key IS NULL AND unmapped_reason IS NOT NULL AND unmapped_reason <> '' AND cardinality(roles) = 0))
				AND array_position(roles, NULL) IS NULL AND array_position(roles, '') IS NULL);
		END IF;
	END $$`,
	`DROP INDEX IF EXISTS idx_session_timeline_members_turn`,
	`CREATE INDEX IF NOT EXISTS idx_session_timeline_members_mapped ON session_timeline_members
		(session_id,turn_key,source_ts,source_uuid,source_record_id,source_record_type,source_profile_email)
		WHERE turn_key IS NOT NULL`,
	// A provenance row from before these columns is 'prepared': kept, never
	// usable, never promoted, and carrying nothing a publish writes. Every other
	// state carries the hashes and the generation it was published with, as
	// validSessionTimelinePublish requires of it.
	`ALTER TABLE session_timeline_provenance ADD COLUMN IF NOT EXISTS timeline_state TEXT NOT NULL DEFAULT 'prepared'`,
	`ALTER TABLE session_timeline_provenance ADD COLUMN IF NOT EXISTS timeline_reason TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE session_timeline_provenance ADD COLUMN IF NOT EXISTS timeline_hash BYTEA`,
	`ALTER TABLE session_timeline_provenance ADD COLUMN IF NOT EXISTS fact_output_hash BYTEA`,
	`ALTER TABLE session_timeline_provenance ADD COLUMN IF NOT EXISTS source_generation BIGINT`,
	`ALTER TABLE session_timeline_provenance ADD COLUMN IF NOT EXISTS rules_version INTEGER`,
	`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'session_timeline_provenance_result'
			AND conrelid = 'session_timeline_provenance'::regclass) THEN
			ALTER TABLE session_timeline_provenance ADD CONSTRAINT session_timeline_provenance_result CHECK (
				(timeline_state = 'prepared' AND timeline_reason = '' AND timeline_hash IS NULL
					AND fact_output_hash IS NULL AND source_generation IS NULL AND rules_version IS NULL)
				OR (timeline_state IN ('observed', 'undeterminable', 'truncated')
					AND COALESCE(octet_length(fact_output_hash), 0) = 32
					AND COALESCE(source_generation, 0) > 0
					AND COALESCE(rules_version, 0) > 0
					AND CASE timeline_state
						WHEN 'truncated' THEN timeline_hash IS NULL
						WHEN 'observed' THEN COALESCE(octet_length(timeline_hash), 0) = 32 AND timeline_reason = ''
						ELSE COALESCE(octet_length(timeline_hash), 0) = 32 AND timeline_reason <> ''
					END));
		END IF;
	END $$`,
	`DO $$ BEGIN
		IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname='session_timeline_provenance_tracking_check'
			AND conrelid='session_timeline_provenance'::regclass
			AND pg_get_constraintdef(oid) NOT LIKE '%tracked%untracked%') THEN
			ALTER TABLE session_timeline_provenance DROP CONSTRAINT session_timeline_provenance_tracking_check;
		END IF;
		IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='session_timeline_provenance_tracking_check'
			AND conrelid='session_timeline_provenance'::regclass) THEN
			ALTER TABLE session_timeline_provenance ADD CONSTRAINT session_timeline_provenance_tracking_check
				CHECK(tracking IN ('tracked','untracked'));
		END IF;
	END $$`, // P5 session records. The per-session tables are in the deletion inventory;
	// usage, consent, range-request and vocabulary rows are deployment/user state.
	`CREATE TABLE IF NOT EXISTS session_ai_records (
		session_id TEXT PRIMARY KEY,
		outcome TEXT NOT NULL CHECK (outcome IN ('ok','redacted')),
		reason TEXT NOT NULL DEFAULT '',
		agent TEXT NOT NULL,
		owner_user_id TEXT,
		owner_profile_email TEXT,
		repository_key TEXT NOT NULL DEFAULT '',
		account_keys JSONB NOT NULL DEFAULT '[]'::jsonb,
		prompt_version INT NOT NULL,
		runtime TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		facts_hash BYTEA NOT NULL,
		facts_rules_version INT NOT NULL,
		basis_hash BYTEA NOT NULL,
		prefix_hash BYTEA,
		input_hash BYTEA,
		source_input_hash BYTEA,
		covers_until TIMESTAMPTZ,
		covers_turn_key TEXT NOT NULL DEFAULT '',
		covered_requests INT NOT NULL DEFAULT 0,
		source_first_at TIMESTAMPTZ,
		generated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		generations INT NOT NULL DEFAULT 1,
		folds INT NOT NULL DEFAULT 0,
		language TEXT NOT NULL DEFAULT '',
		headline TEXT,
		content JSONB,
		kinds TEXT[] NOT NULL DEFAULT '{}',
		components TEXT[] NOT NULL DEFAULT '{}',
		screens TEXT[] NOT NULL DEFAULT '{}',
		work_total INT NOT NULL DEFAULT 0,
		work_done INT NOT NULL DEFAULT 0,
		work_open INT NOT NULL DEFAULT 0,
		work_unsettled INT NOT NULL DEFAULT 0,
		uncovered_requests INT NOT NULL DEFAULT 0,
		search_text TEXT NOT NULL DEFAULT '',
		calls INT NOT NULL DEFAULT 0,
		input_bytes INT NOT NULL DEFAULT 0,
		tokens_reported BOOLEAN NOT NULL DEFAULT false,
		input_tokens BIGINT,
		cached_input_tokens BIGINT,
		output_tokens BIGINT,
		duration_ms INT NOT NULL DEFAULT 0,
		CHECK ((outcome = 'ok' AND content IS NOT NULL AND headline IS NOT NULL)
			OR (outcome = 'redacted' AND content IS NULL AND headline IS NULL))
	)`,
	`DO $$ BEGIN
		IF to_regclass('idx_session_ai_records_owner_user') IS NULL THEN
			CREATE INDEX idx_session_ai_records_owner_user ON session_ai_records (owner_user_id, repository_key)
				WHERE owner_user_id IS NOT NULL;
		END IF;
		IF to_regclass('idx_session_ai_records_owner_profile') IS NULL THEN
			CREATE INDEX idx_session_ai_records_owner_profile ON session_ai_records (owner_profile_email, repository_key)
				WHERE owner_profile_email IS NOT NULL;
		END IF;
	END $$`,
	`CREATE SEQUENCE IF NOT EXISTS session_ai_record_claim`,
	`CREATE TABLE IF NOT EXISTS session_ai_record_jobs (
		session_id TEXT PRIMARY KEY,
		lane TEXT NOT NULL DEFAULT 'live' CHECK (lane IN ('live','backfill','manual')),
		requested_at TIMESTAMPTZ,
		requested_by BIGINT,
		force BOOLEAN NOT NULL DEFAULT false,
		claim BIGINT,
		claimed_at TIMESTAMPTZ,
		attempts INT NOT NULL DEFAULT 0,
		attempts_basis BYTEA,
		retry_at TIMESTAMPTZ,
		last_error TEXT NOT NULL DEFAULT '',
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE TABLE IF NOT EXISTS session_ai_record_budget_reservations (
		reservation_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		day DATE NOT NULL,
		lane TEXT NOT NULL CHECK (lane IN ('live','backfill','manual')),
		reserved_tokens BIGINT NOT NULL CHECK (reserved_tokens > 0),
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE TABLE IF NOT EXISTS session_ai_record_usage (
		day DATE NOT NULL,
		runtime TEXT NOT NULL,
		model TEXT NOT NULL,
		lane TEXT NOT NULL,
		outcome TEXT NOT NULL,
		sessions BIGINT NOT NULL DEFAULT 0,
		calls BIGINT NOT NULL DEFAULT 0,
		unreported_calls BIGINT NOT NULL DEFAULT 0,
		estimated_cap_tokens BIGINT NOT NULL DEFAULT 0,
		input_bytes BIGINT NOT NULL DEFAULT 0,
		input_tokens BIGINT NOT NULL DEFAULT 0,
		cached_input_tokens BIGINT NOT NULL DEFAULT 0,
		output_tokens BIGINT NOT NULL DEFAULT 0,
		duration_ms BIGINT NOT NULL DEFAULT 0,
		dropped_items BIGINT NOT NULL DEFAULT 0,
		uncovered_requests BIGINT NOT NULL DEFAULT 0,
		new_terms BIGINT NOT NULL DEFAULT 0,
		PRIMARY KEY (day,runtime,model,lane,outcome)
	)`,
	`CREATE TABLE IF NOT EXISTS session_ai_record_consents (
		dashboard_user_id BIGINT NOT NULL REFERENCES dashboard_users(id) ON DELETE CASCADE,
		runtime_key TEXT NOT NULL,
		disclosure_version TEXT NOT NULL,
		consented_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (dashboard_user_id,runtime_key)
	)`,
	`CREATE TABLE IF NOT EXISTS session_ai_record_range_requests (
		dashboard_user_id BIGINT PRIMARY KEY REFERENCES dashboard_users(id) ON DELETE CASCADE,
		range_from TIMESTAMPTZ NOT NULL,
		range_to TIMESTAMPTZ NOT NULL,
		requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		served INT NOT NULL DEFAULT 0,
		finished_at TIMESTAMPTZ,
		CHECK (range_to > range_from)
	)`,
	`CREATE TABLE IF NOT EXISTS session_ai_record_vocab_pins (
		repository_key TEXT NOT NULL,
		axis TEXT NOT NULL CHECK (axis IN ('component','screen')),
		term TEXT NOT NULL,
		position INT NOT NULL DEFAULT 0,
		created_by TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (repository_key,axis,term)
	)`,
	`ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS record_enabled BOOLEAN NOT NULL DEFAULT false`,
	`ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS record_runtime TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS record_daily_token_cap BIGINT`,
	`ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS record_backfill_enabled BOOLEAN NOT NULL DEFAULT false`,
	`ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS record_backfill_days INT`,
	`ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS record_backfill_token_budget BIGINT`,
	`ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS record_backfill_since TIMESTAMPTZ`,
	`ALTER TABLE ai_runtime_settings ADD COLUMN IF NOT EXISTS record_model TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE ai_runtime_settings ADD COLUMN IF NOT EXISTS record_reasoning_effort TEXT NOT NULL DEFAULT ''`,
}

var sessionP4FactMigrations = []string{
	`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema()
			AND table_name='session_analysis_facts' AND column_name='commits_state') THEN
			ALTER TABLE session_analysis_facts ADD COLUMN commits_state TEXT NOT NULL DEFAULT 'not_collected';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema()
			AND table_name='session_analysis_facts' AND column_name='commit_count') THEN
			ALTER TABLE session_analysis_facts ADD COLUMN commit_count INT;
		END IF;
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema()
			AND table_name='session_analysis_facts' AND column_name='commits_truncated') THEN
			ALTER TABLE session_analysis_facts ADD COLUMN commits_truncated BOOLEAN NOT NULL DEFAULT false;
		END IF;
		IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema()
			AND table_name='session_analysis_facts' AND column_name='tasks_state') THEN
			ALTER TABLE session_analysis_facts ADD COLUMN tasks_state TEXT NOT NULL DEFAULT 'not_collected';
		END IF;
	END $$`,
	`CREATE TABLE IF NOT EXISTS session_commit_facts (
		session_id TEXT NOT NULL,
		ordinal INT NOT NULL,
		turn_key TEXT NOT NULL DEFAULT '',
		call_id TEXT NOT NULL DEFAULT '',
		call_srec_id BIGINT,
		hash TEXT NOT NULL DEFAULT '',
		short TEXT NOT NULL DEFAULT '',
		subject TEXT NOT NULL DEFAULT '',
		subject_state TEXT NOT NULL,
		committed_at TIMESTAMPTZ NOT NULL,
		kind TEXT NOT NULL,
		source TEXT NOT NULL,
		repository TEXT NOT NULL DEFAULT '',
		branch TEXT NOT NULL DEFAULT '',
		refs TEXT[] NOT NULL DEFAULT '{}',
		rules_version INT NOT NULL,
		PRIMARY KEY(session_id,ordinal)
	)`,
	`CREATE TABLE IF NOT EXISTS session_task_facts (
		session_id TEXT NOT NULL,
		item_key TEXT NOT NULL,
		kind TEXT NOT NULL,
		display_id TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT '',
		title_state TEXT NOT NULL,
		summary TEXT NOT NULL DEFAULT '',
		url TEXT NOT NULL DEFAULT '',
		added BOOLEAN NOT NULL DEFAULT false,
		completed BOOLEAN NOT NULL DEFAULT false,
		edited BOOLEAN NOT NULL DEFAULT false,
		commit_linked BOOLEAN NOT NULL DEFAULT false,
		first_ts TIMESTAMPTZ NOT NULL,
		touches JSONB NOT NULL DEFAULT '[]',
		rules_version INT NOT NULL,
		PRIMARY KEY(session_id,item_key)
	)`,
}
