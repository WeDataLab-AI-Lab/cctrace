package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
	"cctrace/internal/store"
)

type recordAdminReadbackStore struct {
	sessionRecordAdminStore
	store.Store
	settings        *store.SessionAIRecordSettings
	runtimeSettings *store.SessionAIRecordRuntimeSettings
}

func (m *recordAdminReadbackStore) GetSessionAIRecordSettings(context.Context) (*store.SessionAIRecordSettings, error) {
	return m.settings, nil
}

func (m *recordAdminReadbackStore) GetAIRecordRuntimeSettings(context.Context, string) (*store.SessionAIRecordRuntimeSettings, error) {
	return m.runtimeSettings, nil
}

func (*recordAdminReadbackStore) SessionAIRecordAdminMetrics(context.Context, string, string, time.Time, string) (map[string]interface{}, error) {
	return map[string]interface{}{"today": map[string]interface{}{"cap_tokens": int64(1_000_000)}}, nil
}

func TestSessionRecordAdminViewIncludesPersistedSettingsForSafeRoundTrip(t *testing.T) {
	settings := &store.SessionAIRecordSettings{
		Enabled: true, Runtime: "", DailyTokenCap: nil, BackfillEnabled: true, BackfillDays: nil, BackfillTokenBudget: nil,
	}
	runtimeSettings := &store.SessionAIRecordRuntimeSettings{Runtime: "openai-api", Model: "record-model", ReasoningEffort: "high"}
	repo := &recordAdminReadbackStore{settings: settings, runtimeSettings: runtimeSettings}
	runtime := &airuntime.FakeRuntime{
		InfoValue:   airuntime.Info{Key: "openai-api", AuthMode: airuntime.AuthModeAPIKey},
		StatusValue: airuntime.Status{Configured: true, Available: true},
	}
	svc := aireport.NewService(aireport.NewMemStore(), []airuntime.Runtime{runtime}, aireport.Config{EnvRuntime: "openai-api"})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		svc.Shutdown(ctx)
	})
	srv := &Server{store: repo, aiReports: svc, sessionRecordsEnabled: true}

	view, err := srv.sessionRecordAdminView(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	gotSettings, ok := view["settings"].(*store.SessionAIRecordSettings)
	if !ok || gotSettings.Runtime != "" || gotSettings.DailyTokenCap != nil || gotSettings.BackfillDays != nil || gotSettings.BackfillTokenBudget != nil {
		t.Fatalf("persisted settings not preserved: %#v", view["settings"])
	}
	gotRuntime, ok := view["runtime_settings"].(*store.SessionAIRecordRuntimeSettings)
	if !ok || gotRuntime.Runtime != "openai-api" || gotRuntime.Model != "record-model" || gotRuntime.ReasoningEffort != "high" {
		t.Fatalf("persisted runtime settings not preserved: %#v", view["runtime_settings"])
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	jsonSettings := body["settings"].(map[string]interface{})
	if jsonSettings["runtime"] != "" || jsonSettings["daily_token_cap"] != nil || jsonSettings["backfill_days"] != nil {
		t.Fatalf("JSON settings lost empty/default values: %v", jsonSettings)
	}
	if _, ok := body["runtime_key"]; !ok || body["runtime_settings"].(map[string]interface{})["reasoning_effort"] != "high" {
		t.Fatalf("runtime settings response = %s", encoded)
	}
}
