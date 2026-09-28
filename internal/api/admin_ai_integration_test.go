package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
	"cctrace/internal/auth"
	"cctrace/internal/store"
)

// TestAIReportIntegrationFlow tests the complete flow:
// key registration → runtime selection → enable → consent → start report → completion
func TestAIReportIntegrationFlow(t *testing.T) {
	const apiKey = "sk-test-key-123"

	// OpenAI runtime for the flow
	openai := apiKeyRuntime(aireport.RuntimeOpenAI)
	openai.Steps = []airuntime.FakeStep{
		{CallTool: "query_segments", Args: json.RawMessage(`{"limit":5}`)},
		{CallTool: "read_segment", Args: json.RawMessage(`{"segment_id":"10"}`)},
	}
	openai.FinalText = `{"summary":"통합 테스트 요약","items":[{"segment_id":"10","title":"t","reason":"r"}]}`
	openai.Usage = airuntime.Usage{Reported: true, InputTokens: 100, OutputTokens: 20}

	// Setup
	st := aireport.NewMemStore()
	kr := aireport.NewKeyring(st, testSecret, nil)
	cfg := aireport.Config{Keyring: kr}
	admin := &auth.DashboardUser{ID: 9, Role: "admin", Email: "admin@example.com"}
	user := &auth.DashboardUser{ID: 1, Email: "user@example.com", Role: "user", CctraceUserID: "caller"}

	a := newAIRegistryTest(t, admin, st, cfg, openai)

	t.Run("admin registers key", func(t *testing.T) {
		rec := a.do("PUT", "/api/admin/ai/providers/openai/key", `{"api_key":"`+apiKey+`"}`, true)
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if _, ok := decodeBody(t, rec)["credential"]; !ok {
			t.Fatal("credential not in response")
		}
	})

	t.Run("admin selects runtime", func(t *testing.T) {
		rec := a.do("PUT", "/api/admin/ai/runtime", `{"runtime":"openai-api"}`, true)
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("admin enables AI", func(t *testing.T) {
		rec := a.do("PUT", "/api/admin/ai/enabled", `{"enabled":true}`, true)
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
	})

	// Add segment data
	wk, _ := aireport.ParseISOWeek("2026-W37", "Asia/Seoul")
	st.AddSegment("caller", store.AISegment{
		ID:      10,
		StartTs: wk.Since.Add(time.Hour),
	})

	t.Run("user consents and starts", func(t *testing.T) {
		// Switch to user
		userTest := a.as(user)

		// Consent
		if err := st.UpsertAIConsent(context.Background(), user.ID, "openai-api:api_key", aireport.DisclosureVersion); err != nil {
			t.Fatal(err)
		}

		// Start report
		rec := userTest.do("POST", "/api/ai-reports", `{"week":"2026-W37","tz":"Asia/Seoul"}`, true)
		if rec.Code != 202 {
			t.Fatalf("start: status %d: %s", rec.Code, rec.Body.String())
		}

		body := decodeBody(t, rec)
		runData, ok := body["run"].(map[string]any)
		if !ok {
			t.Fatalf("no run in response: %v", body)
		}
		runIDVal, ok := runData["id"]
		if !ok || runIDVal == nil {
			t.Fatalf("no id in run: %v", runData)
		}
		runID := int64(runIDVal.(float64))

		// Wait for completion
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			run, _ := st.GetAIReportRun(context.Background(), runID)
			if run != nil && run.Status == store.AIRunCompleted {
				if run.Runtime != "openai-api" || run.Model == "" {
					t.Fatalf("run metadata: %+v", run)
				}
				break
			}
			time.Sleep(10 * time.Millisecond)
		}

		// Verify report saved
		rep, _ := st.GetAIReport(context.Background(), user.ID, wk.ID)
		if rep == nil {
			t.Fatal("report not saved")
		}
	})
}

// TestAIReportKeyReplacement verifies key changes apply to next run
func TestAIReportKeyReplacement(t *testing.T) {
	const key1 = "sk-key-1"
	const key2 = "sk-key-2"

	st := aireport.NewMemStore()
	kr := aireport.NewKeyring(st, testSecret, nil)
	if _, err := kr.SetKey(context.Background(), aireport.ProviderOpenAI, key1, "a"); err != nil {
		t.Fatal(err)
	}

	// Change key
	if _, err := kr.SetKey(context.Background(), aireport.ProviderOpenAI, key2, "a"); err != nil {
		t.Fatal(err)
	}

	// Verify new key is stored
	k, _ := kr.APIKey(aireport.ProviderOpenAI)(context.Background())
	if k != key2 {
		t.Fatalf("key = %q, want %q", k, key2)
	}
}
