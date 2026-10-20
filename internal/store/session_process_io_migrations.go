package store

// sessionProcessIOMigrations extends the existing per-process live-state row.
// It deliberately stores only counters and process measurement metadata.
var sessionProcessIOMigrations = []string{
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS status_source TEXT NOT NULL DEFAULT 'legacy'`,
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS io_epoch TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS io_as_of TIMESTAMPTZ`,
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS io_source TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS io_mode TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS io_partial BOOLEAN NOT NULL DEFAULT FALSE`,
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS io_failure_code TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS input_bytes BIGINT`,
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS input_lines BIGINT`,
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS output_bytes BIGINT`,
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS output_lines BIGINT`,
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS stdout_bytes BIGINT`,
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS stdout_lines BIGINT`,
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS stderr_bytes BIGINT`,
	`ALTER TABLE session_live_states ADD COLUMN IF NOT EXISTS stderr_lines BIGINT`,
}
