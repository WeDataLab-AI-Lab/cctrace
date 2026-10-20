package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// GetAISettings returns the admin's AI report settings for runtime, or nil
// when no admin has saved any for it.
func (s *PgStore) GetAISettings(ctx context.Context, runtime string) (*AISettings, error) {
	a := &AISettings{}
	err := s.pool.QueryRow(ctx, `
		SELECT runtime, model, reasoning_effort, base_url, updated_by, updated_at
		FROM ai_runtime_settings WHERE runtime = $1`, runtime).Scan(&a.Runtime, &a.Model, &a.ReasoningEffort, &a.BaseURL, &a.UpdatedBy, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ai settings: %w", err)
	}
	return a, nil
}

// SetAISettings replaces runtime's settings row. Empty values clear an override.
func (s *PgStore) SetAISettings(ctx context.Context, runtime, model, reasoningEffort, baseURL, actor string) error {
	return s.withSessionRecordPolicyMutation(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
		INSERT INTO ai_runtime_settings (runtime, model, reasoning_effort, base_url, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (runtime) DO UPDATE
		SET model = EXCLUDED.model,
			reasoning_effort = EXCLUDED.reasoning_effort,
			base_url = EXCLUDED.base_url,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`, runtime, model, reasoningEffort, baseURL, actor)
		if err != nil {
			return fmt.Errorf("set ai settings: %w", err)
		}
		return nil
	})
}

// GetAIRuntimeChoice returns the admin's runtime choice, or nil when none is
// saved.
func (s *PgStore) GetAIRuntimeChoice(ctx context.Context) (*AIRuntimeChoice, error) {
	c := &AIRuntimeChoice{}
	err := s.pool.QueryRow(ctx, `
		SELECT runtime, updated_by, updated_at FROM ai_settings
		WHERE id = 1 AND runtime <> ''`).Scan(&c.Runtime, &c.UpdatedBy, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ai runtime choice: %w", err)
	}
	return c, nil
}

// SetAIRuntimeChoice saves the admin's runtime; "" clears the choice.
func (s *PgStore) SetAIRuntimeChoice(ctx context.Context, runtime, actor string) error {
	return s.withSessionRecordPolicyMutation(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
		INSERT INTO ai_settings (id, runtime, updated_by, updated_at)
		VALUES (1, $1, $2, now())
		ON CONFLICT (id) DO UPDATE
		SET runtime = EXCLUDED.runtime,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`, runtime, actor)
		if err != nil {
			return fmt.Errorf("set ai runtime choice: %w", err)
		}
		return nil
	})
}

// GetAIEnabledChoice returns the admin's AI report switch, or nil when no admin
// has set it.
func (s *PgStore) GetAIEnabledChoice(ctx context.Context) (*AIEnabledChoice, error) {
	c := &AIEnabledChoice{}
	err := s.pool.QueryRow(ctx, `
		SELECT enabled, updated_by, updated_at FROM ai_settings
		WHERE id = 1 AND enabled IS NOT NULL`).Scan(&c.Enabled, &c.UpdatedBy, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ai enabled choice: %w", err)
	}
	return c, nil
}

// SetAIEnabledChoice saves the admin's switch; nil clears it. The runtime
// choice on the same row is kept.
func (s *PgStore) SetAIEnabledChoice(ctx context.Context, enabled *bool, actor string) error {
	return s.withSessionRecordPolicyMutation(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
		INSERT INTO ai_settings (id, enabled, updated_by, updated_at)
		VALUES (1, $1, $2, now())
		ON CONFLICT (id) DO UPDATE
		SET enabled = EXCLUDED.enabled,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`, enabled, actor)
		if err != nil {
			return fmt.Errorf("set ai enabled choice: %w", err)
		}
		return nil
	})
}

// GetAIRunPolicy returns the admin's run policy, or nil when no admin has set
// any part of it.
func (s *PgStore) GetAIRunPolicy(ctx context.Context) (*AIRunPolicy, error) {
	p := &AIRunPolicy{}
	err := s.pool.QueryRow(ctx, `
		SELECT consent_required, max_concurrent, updated_by, updated_at FROM ai_settings
		WHERE id = 1 AND (consent_required IS NOT NULL OR max_concurrent IS NOT NULL)`).
		Scan(&p.ConsentRequired, &p.MaxConcurrent, &p.UpdatedBy, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ai run policy: %w", err)
	}
	return p, nil
}

// SetAIRunPolicy saves the admin's run policy; a nil field clears that one.
// The other settings on the same row are kept.
func (s *PgStore) SetAIRunPolicy(ctx context.Context, consentRequired *bool, maxConcurrent *int, actor string) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO ai_settings (id, consent_required, max_concurrent, updated_by, updated_at)
		VALUES (1, $1, $2, $3, now())
		ON CONFLICT (id) DO UPDATE
		SET consent_required = EXCLUDED.consent_required,
			max_concurrent = EXCLUDED.max_concurrent,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`, consentRequired, maxConcurrent, actor); err != nil {
		return fmt.Errorf("set ai run policy: %w", err)
	}
	return nil
}

// GetAIProviderCredential returns provider's registered key, sealed, or nil.
func (s *PgStore) GetAIProviderCredential(ctx context.Context, provider string) (*AIProviderCredential, error) {
	c := &AIProviderCredential{}
	err := s.pool.QueryRow(ctx, `
		SELECT provider, ciphertext, nonce, key_hint, key_source, updated_by, updated_at
		FROM ai_provider_credentials WHERE provider = $1`, provider).Scan(&c.Provider, &c.Ciphertext, &c.Nonce, &c.KeyHint, &c.KeySource, &c.UpdatedBy, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ai provider credential: %w", err)
	}
	return c, nil
}

// SetAIProviderCredential replaces provider's sealed key.
func (s *PgStore) SetAIProviderCredential(ctx context.Context, c AIProviderCredential) error {
	return s.withSessionRecordPolicyMutation(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
		INSERT INTO ai_provider_credentials (provider, ciphertext, nonce, key_hint, key_source, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (provider) DO UPDATE
		SET ciphertext = EXCLUDED.ciphertext,
			nonce = EXCLUDED.nonce,
			key_hint = EXCLUDED.key_hint,
			key_source = EXCLUDED.key_source,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`, c.Provider, c.Ciphertext, c.Nonce, c.KeyHint, c.KeySource, c.UpdatedBy)
		if err != nil {
			return fmt.Errorf("set ai provider credential: %w", err)
		}
		return nil
	})
}

// DeleteAIProviderCredential removes provider's key; none is not an error.
func (s *PgStore) DeleteAIProviderCredential(ctx context.Context, provider string) error {
	return s.withSessionRecordPolicyMutation(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM ai_provider_credentials WHERE provider = $1`, provider); err != nil {
			return fmt.Errorf("delete ai provider credential: %w", err)
		}
		return nil
	})
}

func (s *PgStore) withSessionRecordPolicyMutation(ctx context.Context, mutate func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return err
	}
	if err := mutate(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
