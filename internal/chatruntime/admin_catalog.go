package chatruntime

import (
	"context"
	"fmt"

	"cctrace/internal/airuntime"
)

var _ airuntime.ModelCatalog = (*Runtime)(nil)

// Models connects the proxy's model list to administrator selection. The
// models endpoint advertises IDs only, so reasoning support stays unspecified.
func (r *Runtime) Models(ctx context.Context) ([]airuntime.Model, error) {
	key, err := r.apiKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: could not read the provider credential", airuntime.ErrUnavailable)
	}
	if key == "" {
		return nil, airuntime.ErrNotConfigured
	}
	if _, err := r.baseURL(ctx); err != nil {
		return nil, err
	}
	ids, err := r.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: could not read the provider model list", airuntime.ErrUnavailable)
	}
	models := make([]airuntime.Model, 0, len(ids))
	for _, id := range ids {
		models = append(models, airuntime.Model{ID: id, DisplayName: id, IsDefault: id == r.cfg.DefaultModel})
	}
	return models, nil
}
