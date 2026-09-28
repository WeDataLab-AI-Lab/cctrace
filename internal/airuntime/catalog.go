package airuntime

import (
	"context"
	"errors"
)

// ErrNotLoggedIn means the runtime is configured but holds no credentials, so
// nothing that needs the provider can be asked.
var ErrNotLoggedIn = errors.New("ai runtime not logged in")

// ModelCatalog is implemented by a runtime that can list the models a run may
// ask for. Errors wrap ErrNotConfigured, ErrNotLoggedIn or ErrUnavailable.
type ModelCatalog interface {
	Models(ctx context.Context) ([]Model, error)
}

// Model is one selectable model. ID is what RunRequest.Model takes.
type Model struct {
	ID                        string
	DisplayName               string
	Description               string
	IsDefault                 bool
	DefaultReasoningEffort    string
	SupportedReasoningEfforts []ReasoningEffortOption
}

type ReasoningEffortOption struct {
	ReasoningEffort string
	Description     string
}

// SupportsEffort reports whether effort is one of the model's listed efforts.
func (m Model) SupportsEffort(effort string) bool {
	for _, o := range m.SupportedReasoningEfforts {
		if o.ReasoningEffort == effort {
			return true
		}
	}
	return false
}
