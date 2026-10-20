package sessionrecord

import (
	"context"
	"fmt"
	"testing"
	"time"

	"cctrace/internal/airuntime"
)

type workerRepoFake struct {
	settings      WorkerSettings
	owners        []Owner
	job           Job
	snapshot      Snapshot
	claims        int
	settingsReads int
	ownerReads    int
	published     *PersistedRecord
	failed        string
}

func (f *workerRepoFake) AcquireSessionRecordWorker(context.Context) (func(), bool, error) {
	return func() {}, true, nil
}

func (f *workerRepoFake) SessionRecordSettings(context.Context) (WorkerSettings, error) {
	f.settingsReads++
	return f.settings, nil
}
func (f *workerRepoFake) ConsentingSessionRecordOwners(context.Context, string, string, bool) ([]Owner, error) {
	f.ownerReads++
	return f.owners, nil
}
func (f *workerRepoFake) ClaimNextSessionRecordJob(_ context.Context, _ Owner, lane string, _ WorkerSettings, _ time.Time) (Job, bool, error) {
	if lane != f.job.Lane || f.claims > 0 {
		return Job{}, false, nil
	}
	f.claims++
	return f.job, true, nil
}
func (f *workerRepoFake) LoadSessionRecordSnapshot(context.Context, Job) (Snapshot, error) {
	return f.snapshot, nil
}
func (f *workerRepoFake) BeginSessionRecordTransmission(context.Context, Job, Snapshot, string, string, int64, string) (TransmissionReservation, error) {
	return TransmissionReservation{}, nil
}
func (f *workerRepoFake) FinishSessionRecordTransmission(context.Context, TransmissionReservation, CallUsage) error {
	return nil
}
func (f *workerRepoFake) PublishSessionRecord(_ context.Context, _ Job, _ Snapshot, record PersistedRecord) error {
	f.published = &record
	return nil
}
func (f *workerRepoFake) UpdateSessionRecordNoop(context.Context, Job, Snapshot) error { return nil }
func (f *workerRepoFake) PublishRedactedSessionRecord(context.Context, Job, Snapshot) error {
	return nil
}
func (f *workerRepoFake) FailSessionRecord(_ context.Context, _ Job, code string, _ bool) error {
	f.failed = code
	return nil
}
func (f *workerRepoFake) ReleaseSessionRecordClaim(context.Context, Job) error { return nil }
func (f *workerRepoFake) RecoverSessionRecordClaims(context.Context) error     { return nil }

type runtimeResolverFake struct {
	config RuntimeConfig
	err    error
	reads  int
}

func (f *runtimeResolverFake) ResolveSessionRecordRuntime(context.Context, string) (RuntimeConfig, error) {
	f.reads++
	return f.config, f.err
}

func TestWorkerUsesFakeRuntimeAndPersistsValidatedSyntheticRecord(t *testing.T) {
	model := `{"headline":"API 변경을 구현했다","bullets":[{"text":"` + "`API`" + ` 요청을 추가했다","turns":["t1"]}],"review":[],"kinds":["implementation"],"components":["API"],"screens":[],"new_component":null,"new_screen":null,"work":[{"turn":"t1","kind":"request","text":"API 검색 경로를 추가했다","done":true,"decision":null,"note":null,"children":[]}],"skipped":[],"work_updates":[]}`
	runtime := &airuntime.FakeRuntime{InfoValue: airuntime.Info{Key: "claude-api", AuthMode: airuntime.AuthModeAPIKey},
		StatusValue: airuntime.Status{Configured: true, Available: true}, FinalText: model,
		Usage: airuntime.Usage{Reported: true, InputTokens: 1200, OutputTokens: 80}, ResultModel: "synthetic-model"}
	job := Job{SessionID: "synthetic-session", Lane: "manual", Claim: 1, Owner: Owner{DashboardUserID: 7, UserID: "synthetic-user"}}
	snapshot := Snapshot{Agent: "claude", RepositoryKey: "repo", AccountKeys: []byte("[]"),
		FactsHash: []byte("facts"), BasisHash: []byte("basis"), PrefixHash: []byte("prefix"), SourceInputHash: []byte("source"),
		Input: Input{Language: "en", Vocabulary: Vocabulary{Components: []VocabularyTerm{{Term: "API", Sessions: 1}}},
			Turns: []Turn{{Name: "t1", Key: "synthetic-turn-key", Request: true, Status: "ended", Inputs: []Text{{Kind: "typed", Text: "Add the API endpoint"}}, Final: "Added the API endpoint."}}}}
	repo := &workerRepoFake{settings: WorkerSettings{Enabled: true}, owners: []Owner{job.Owner}, job: job, snapshot: snapshot}
	runtimes := &runtimeResolverFake{config: RuntimeConfig{Runtime: runtime, Key: "claude-api", RuntimeKey: "claude-api:api_key", Model: "synthetic-model"}}
	w := NewWorker(repo, runtimes, true, nil)
	w.Now = func() time.Time { return time.Date(2026, 10, 9, 4, 0, 0, 0, time.UTC) }
	worked, lane, err := w.RunOne(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !worked || lane != "manual" || repo.published == nil {
		t.Fatalf("worked=%v lane=%q record=%v", worked, lane, repo.published)
	}
	if repo.published.WorkTotal != 1 || repo.published.WorkDone != 1 || repo.published.Runtime != "claude-api" {
		t.Fatalf("stored record summary = %+v", repo.published)
	}
	requests := runtime.Requests()
	if len(requests) != 1 || len(requests[0].Tools) != 0 || requests[0].MaxOutputTokens != MaxOutputTokens || string(requests[0].OutputSchema) != string(OutputSchema) {
		t.Fatalf("runtime requests do not match the no-tool structured contract: %+v", requests)
	}
}

func TestWorkerDisabledDoesNotResolveRuntimeOrReadConsent(t *testing.T) {
	repo := &workerRepoFake{settings: WorkerSettings{Enabled: false}}
	runtimes := &runtimeResolverFake{}
	w := NewWorker(repo, runtimes, true, nil)
	worked, _, err := w.RunOne(context.Background())
	if err != nil || worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	if runtimes.reads != 0 || repo.ownerReads != 0 {
		t.Fatalf("disabled worker read runtime=%d consent owners=%d", runtimes.reads, repo.ownerReads)
	}
}

func TestWorkerTemporaryBackoffDoublesAndCapsAtOneHour(t *testing.T) {
	w := NewWorker(&workerRepoFake{}, &runtimeResolverFake{}, true, nil)
	for _, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second} {
		if got := w.nextTemporaryBackoff(); got != want {
			t.Fatalf("temporary backoff=%s want=%s", got, want)
		}
	}
	for i := 0; i < 12; i++ {
		_ = w.nextTemporaryBackoff()
	}
	if got := w.nextTemporaryBackoff(); got != time.Hour {
		t.Fatalf("capped temporary backoff=%s", got)
	}
	w.clearTemporaryFailures()
	if got := w.nextTemporaryBackoff(); got != time.Second {
		t.Fatalf("reset temporary backoff=%s", got)
	}
}

func TestSessionRecordValidationFailuresUseStableLedgerCode(t *testing.T) {
	for _, err := range []error{fmt.Errorf("record schema: invalid field"), fmt.Errorf("record content failure: missing request evidence")} {
		if got := sessionRecordErrorCode(err); got != "schema_violation" {
			t.Fatalf("validation error code=%q", got)
		}
	}
}
