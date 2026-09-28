package store

import (
	"context"
	"strings"
	"time"
)

// UnpricedModel is an (agent, model) whose token usage was costed at $0 or not
// at all, because no rate matched it (#441).
//
// A model with no rate costs $0 on purpose -- inventing a price for a local model
// is worse than the gap -- but that same $0 is how #286 hid a hundred million
// tokens of a routing alias. This list is what separates the two for an admin:
// every entry is either a missing rate or a model to mark flat-rate.
//
// Model is the raw id as stored, never a display name, so it can be copied into
// a rate table as is.
type UnpricedModel struct {
	Agent           string    `json:"agent"`
	Model           string    `json:"model"`
	Rows            int64     `json:"rows"`
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	CacheReadTokens int64     `json:"cache_read_tokens"`
	FirstTs         time.Time `json:"first_ts"`
	LastTs          time.Time `json:"last_ts"`
}

// FlatRateModel is a model an admin declared "$0 is the right cost".
type FlatRateModel struct {
	Agent     string    `json:"agent"`
	Model     string    `json:"model"`
	Reason    string    `json:"reason"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// NormalizeFlatRateKey folds the agent and trims the model without folding it:
// the model is the raw id the unpriced list shows, and it must match that id.
func NormalizeFlatRateKey(agent, model string) (string, string) {
	return strings.ToLower(strings.TrimSpace(agent)), strings.TrimSpace(model)
}

// ListUnpricedModels reads unified_events rather than visible_events: a missing
// rate is a property of the model, and an excluded account's usage of it is the
// same evidence. Only aggregates leave this function -- no session or account.
func (s *PgStore) ListUnpricedModels(ctx context.Context) ([]UnpricedModel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(e.agent,''), COALESCE(e.model,''), count(*),
			COALESCE(sum(e.input_tokens),0)::bigint,
			COALESCE(sum(e.output_tokens),0)::bigint,
			COALESCE(sum(e.cache_read_tokens),0)::bigint,
			min(e.ts), max(e.ts)
		FROM unified_events e
		WHERE (e.cost_usd IS NULL OR e.cost_usd = 0)
			AND (COALESCE(e.input_tokens,0) > 0 OR COALESCE(e.output_tokens,0) > 0)
			AND NOT EXISTS (
				-- f.agent is always lowercase (NormalizeFlatRateKey folds it before a
				-- mark is stored). Every producer of e.agent already writes lowercase
				-- too, so this fold changes nothing in practice; it's defense against
				-- the day one doesn't.
				SELECT 1 FROM flat_rate_models f
				WHERE f.agent = lower(COALESCE(e.agent,'')) AND f.model = COALESCE(e.model,'')
			)
		GROUP BY 1, 2
		-- An ORDER BY ordinal is only a column reference when it is a lone integer
		-- literal; "4 + 5" is the expression 9, not a reference to the 9th select-list
		-- column, so this used to sort by (agent, model) only and never by volume.
		ORDER BY (COALESCE(sum(e.input_tokens),0) + COALESCE(sum(e.output_tokens),0)) DESC, 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UnpricedModel{}
	for rows.Next() {
		var m UnpricedModel
		if err := rows.Scan(&m.Agent, &m.Model, &m.Rows, &m.InputTokens, &m.OutputTokens,
			&m.CacheReadTokens, &m.FirstTs, &m.LastTs); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *PgStore) ListFlatRateModels(ctx context.Context) ([]FlatRateModel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT agent, model, reason, created_by, created_at
		FROM flat_rate_models ORDER BY agent, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FlatRateModel{}
	for rows.Next() {
		var m FlatRateModel
		if err := rows.Scan(&m.Agent, &m.Model, &m.Reason, &m.CreatedBy, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkFlatRateModel is idempotent; marking again replaces the reason and actor.
func (s *PgStore) MarkFlatRateModel(ctx context.Context, agent, model, reason, actor string) error {
	agent, model = NormalizeFlatRateKey(agent, model)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO flat_rate_models (agent, model, reason, created_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (agent, model) DO UPDATE SET
			reason = EXCLUDED.reason, created_by = EXCLUDED.created_by, created_at = now()`,
		agent, model, reason, actor)
	return err
}

func (s *PgStore) UnmarkFlatRateModel(ctx context.Context, agent, model string) error {
	agent, model = NormalizeFlatRateKey(agent, model)
	_, err := s.pool.Exec(ctx, `DELETE FROM flat_rate_models WHERE agent = $1 AND model = $2`, agent, model)
	return err
}
