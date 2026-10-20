package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type SessionAIRecordSettings struct {
	Enabled             bool       `json:"enabled"`
	Runtime             string     `json:"runtime"`
	DailyTokenCap       *int64     `json:"daily_token_cap"`
	BackfillEnabled     bool       `json:"backfill_enabled"`
	BackfillDays        *int       `json:"backfill_days"`
	BackfillTokenBudget *int64     `json:"backfill_token_budget"`
	BackfillSince       *time.Time `json:"backfill_since"`
}

type SessionAIRecordRuntimeSettings struct {
	Runtime         string `json:"runtime"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
}

func (s *PgStore) GetSessionAIRecordSettings(ctx context.Context) (*SessionAIRecordSettings, error) {
	settings := &SessionAIRecordSettings{}
	var cap pgtype.Int8
	var days pgtype.Int4
	var budget pgtype.Int8
	var since pgtype.Timestamptz
	err := s.pool.QueryRow(ctx, `SELECT record_enabled,record_runtime,record_daily_token_cap,
		record_backfill_enabled,record_backfill_days,record_backfill_token_budget,record_backfill_since
		FROM ai_settings WHERE id=1`).Scan(&settings.Enabled, &settings.Runtime, &cap, &settings.BackfillEnabled, &days, &budget, &since)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session record settings: %w", err)
	}
	if cap.Valid {
		settings.DailyTokenCap = &cap.Int64
	}
	if days.Valid {
		v := int(days.Int32)
		settings.BackfillDays = &v
	}
	if budget.Valid {
		settings.BackfillTokenBudget = &budget.Int64
	}
	if since.Valid {
		settings.BackfillSince = &since.Time
	}
	return settings, nil
}

func (s *PgStore) SetSessionAIRecordSettings(ctx context.Context, settings SessionAIRecordSettings, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return err
	}
	var oldEnabled bool
	var oldBudget pgtype.Int8
	var oldSince pgtype.Timestamptz
	err = tx.QueryRow(ctx, `SELECT record_backfill_enabled,record_backfill_token_budget,record_backfill_since
		FROM ai_settings WHERE id=1 FOR UPDATE`).Scan(&oldEnabled, &oldBudget, &oldSince)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read prior session record settings: %w", err)
	}
	since := oldSince
	budgetChanged := oldBudget.Valid != (settings.BackfillTokenBudget != nil)
	if oldBudget.Valid && settings.BackfillTokenBudget != nil && oldBudget.Int64 != *settings.BackfillTokenBudget {
		budgetChanged = true
	}
	if oldEnabled != settings.BackfillEnabled || budgetChanged || !oldSince.Valid && settings.BackfillEnabled {
		since = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	var sinceValue any
	if since.Valid {
		sinceValue = since.Time
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ai_settings
		(id,record_enabled,record_runtime,record_daily_token_cap,record_backfill_enabled,record_backfill_days,
		record_backfill_token_budget,record_backfill_since,updated_by,updated_at)
		VALUES (1,$1,$2,$3,$4,$5,$6,$7,$8,now())
		ON CONFLICT (id) DO UPDATE SET record_enabled=EXCLUDED.record_enabled,record_runtime=EXCLUDED.record_runtime,
		record_daily_token_cap=EXCLUDED.record_daily_token_cap,record_backfill_enabled=EXCLUDED.record_backfill_enabled,
		record_backfill_days=EXCLUDED.record_backfill_days,record_backfill_token_budget=EXCLUDED.record_backfill_token_budget,
		record_backfill_since=EXCLUDED.record_backfill_since,updated_by=EXCLUDED.updated_by,updated_at=now()`,
		settings.Enabled, settings.Runtime, settings.DailyTokenCap, settings.BackfillEnabled, settings.BackfillDays,
		settings.BackfillTokenBudget, sinceValue, actor); err != nil {
		return fmt.Errorf("write session record settings: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *PgStore) GetAIRecordRuntimeSettings(ctx context.Context, runtime string) (*SessionAIRecordRuntimeSettings, error) {
	settings := &SessionAIRecordRuntimeSettings{Runtime: runtime}
	err := s.pool.QueryRow(ctx, `SELECT record_model,record_reasoning_effort FROM ai_runtime_settings WHERE runtime=$1`, runtime).
		Scan(&settings.Model, &settings.ReasoningEffort)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session record runtime settings: %w", err)
	}
	return settings, nil
}

func (s *PgStore) SetAIRecordRuntimeSettings(ctx context.Context, runtime, model, effort, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ai_runtime_settings(runtime,record_model,record_reasoning_effort,updated_by,updated_at)
		VALUES($1,$2,$3,$4,now()) ON CONFLICT(runtime) DO UPDATE SET
		record_model=EXCLUDED.record_model,record_reasoning_effort=EXCLUDED.record_reasoning_effort,
		updated_by=EXCLUDED.updated_by,updated_at=now()`, runtime, model, effort, actor); err != nil {
		return fmt.Errorf("write session record runtime settings: %w", err)
	}
	return tx.Commit(ctx)
}
