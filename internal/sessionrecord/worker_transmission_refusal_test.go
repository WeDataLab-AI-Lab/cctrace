package sessionrecord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/chatruntime"
)

type refusingWorkerRepo struct {
	workerRepoFake
	refusal    error
	sendChecks int
}

func (r *refusingWorkerRepo) BeginSessionRecordTransmission(context.Context, Job, Snapshot, string, string, int64, string) (TransmissionReservation, error) {
	r.sendChecks++
	return TransmissionReservation{}, r.refusal
}

func TestWorkerPreservesTransmissionRefusalAcrossHTTPRuntime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refusal error
		paused  bool
	}{
		{name: "budget", refusal: ErrRecordBudgetWaiting, paused: true},
		{name: "policy", refusal: ErrRecordSendNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var providerCalls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				providerCalls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer provider.Close()
			runtime := chatruntime.New(chatruntime.Config{
				RuntimeKey: "litellm-api", ProviderName: "litellm", DefaultModel: "synthetic-model",
				APIKey:     func(context.Context) (string, error) { return "synthetic-local-key", nil },
				BaseURL:    func(context.Context) (string, error) { return provider.URL, nil },
				HTTPClient: &http.Client{Transport: GatedRoundTripper{Base: http.DefaultTransport}},
			})
			job := Job{SessionID: "synthetic-budget-session", Lane: "manual", Claim: 1,
				Owner: Owner{DashboardUserID: 7, UserID: "synthetic-owner"}}
			repo := &refusingWorkerRepo{refusal: tc.refusal, workerRepoFake: workerRepoFake{
				settings: WorkerSettings{Enabled: true}, owners: []Owner{job.Owner}, job: job,
				snapshot: Snapshot{Agent: "claude", FactsHash: []byte("facts"), BasisHash: []byte("basis"),
					SourceInputHash: []byte("source"), Input: Input{Language: "ko", Turns: []Turn{
						{Key: "synthetic-turn", Request: true, Status: "ended", Inputs: []Text{{Kind: "typed", Text: "합성 예산 검증"}}},
					}}},
			}}
			resolver := &runtimeResolverFake{config: RuntimeConfig{Runtime: runtime, Key: "litellm-api",
				RuntimeKey: "litellm-api:api_key", Model: "synthetic-model"}}
			worker := NewWorker(repo, resolver, true, nil)
			worker.Now = func() time.Time { return time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC) }
			worked, lane, err := worker.RunOne(context.Background())
			if err != nil {
				t.Fatalf("local transmission refusal became a provider failure: %v", err)
			}
			if !worked || lane != "manual" || repo.sendChecks != 1 || providerCalls.Load() != 0 || repo.published != nil || repo.failed != "" {
				t.Fatalf("refused request crossed processing boundary: worked=%v lane=%s checks=%d calls=%d published=%v failed=%s", worked, lane, repo.sendChecks, providerCalls.Load(), repo.published != nil, repo.failed)
			}
			if tc.paused {
				status := worker.Status()
				if status.State != "paused" || status.PausedReason != "daily_cap" || status.PausedUntil == nil || !status.PausedUntil.After(worker.now()) {
					t.Fatalf("budget refusal did not pause until the next record day: %+v", status)
				}
				if again, _, err := worker.RunOne(context.Background()); err != nil || again || repo.sendChecks != 1 {
					t.Fatalf("budget pause retried a blocked call: worked=%v checks=%d error=%v", again, repo.sendChecks, err)
				}
			}
		})
	}
}
