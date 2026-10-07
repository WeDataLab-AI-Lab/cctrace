package store

import "context"

// UnpricedModelKey retains the raw model id: aliases and versions have their
// own retry budget, as do identical model ids belonging to another agent.
type UnpricedModelKey struct {
	Agent string
	Model string
}

// ListUnpricedCodexModelKeys shares the admin's row predicate but reads only
// materialized Codex usage and returns keys, not unified_events aggregates.
func (s *PgStore) ListUnpricedCodexModelKeys(ctx context.Context) ([]UnpricedModelKey, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT c.agent, c.model FROM codex_imputed_cost c
		WHERE c.agent = 'codex' AND c.model <> '' AND `+unpricedModelPredicate("c")+`
		ORDER BY c.agent, c.model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []UnpricedModelKey{}
	for rows.Next() {
		var key UnpricedModelKey
		if err := rows.Scan(&key.Agent, &key.Model); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}
