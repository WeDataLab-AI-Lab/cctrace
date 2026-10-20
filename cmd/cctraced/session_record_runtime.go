package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
	"cctrace/internal/sessionrecord"
	"cctrace/internal/store"
)

type sessionRecordRuntimeResolver struct {
	ai    *aireport.Service
	store *store.PgStore
}

func (r sessionRecordRuntimeResolver) ResolveSessionRecordRuntime(ctx context.Context, selected string) (sessionrecord.RuntimeConfig, error) {
	rt, weekly, status, err := r.ai.RuntimeForSessionRecords(ctx, selected)
	if err != nil {
		return sessionrecord.RuntimeConfig{}, err
	}
	if rt == nil || !status.Configured || !status.Available {
		return sessionrecord.RuntimeConfig{}, airuntime.ErrNotConfigured
	}
	info := rt.Info()
	settings, err := r.store.GetAIRecordRuntimeSettings(ctx, info.Key)
	if err != nil {
		return sessionrecord.RuntimeConfig{}, err
	}
	model := settings.Model
	if model == "" {
		model = weekly.Model
	}
	if model == "" && info.Key != aireport.RuntimeLiteLLM {
		model = info.Model
	}
	effort := settings.ReasoningEffort
	models, err := r.ai.Models(ctx, info.Key)
	if err != nil {
		return sessionrecord.RuntimeConfig{}, err
	}
	if model != "" {
		var selectedModel *airuntime.Model
		for i := range models {
			if models[i].ID == model {
				selectedModel = &models[i]
				break
			}
		}
		if selectedModel == nil {
			return sessionrecord.RuntimeConfig{}, fmt.Errorf("%w: selected session-record model is unavailable", airuntime.ErrNotConfigured)
		}
		if effort == "" {
			effort = lowestReasoningEffort(selectedModel.SupportedReasoningEfforts)
		}
		if effort != "" && !selectedModel.SupportsEffort(effort) {
			return sessionrecord.RuntimeConfig{}, fmt.Errorf("%w: selected session-record effort is unavailable", airuntime.ErrNotConfigured)
		}
	}
	return sessionrecord.RuntimeConfig{Runtime: rt, Key: info.Key, RuntimeKey: sessionrecord.RuntimeConsentKey(info), Model: model, ReasoningEffort: effort}, nil
}

func lowestReasoningEffort(options []airuntime.ReasoningEffortOption) string {
	if len(options) == 0 {
		return ""
	}
	rank := map[string]int{"minimal": 0, "low": 1, "medium": 2, "high": 3, "xhigh": 4}
	selected := options[0].ReasoningEffort
	for _, option := range options[1:] {
		if rank[option.ReasoningEffort] < rank[selected] {
			selected = option.ReasoningEffort
		}
	}
	return selected
}

func sessionRecordsFeatureEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("CCTRACE_SESSION_RECORDS")), "on")
}
