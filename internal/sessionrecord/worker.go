package sessionrecord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"cctrace/internal/airuntime"
)

const (
	RecordDisclosureVersion = "2026-10-07"
	MaxRunTokens            = int64(64_000)
	MaxOutputTokens         = int64(12_000)
	RecordWallClock         = 3 * time.Minute
)

var (
	ErrRecordSendNotAllowed = errors.New("session record external send is no longer authorized")
	ErrRecordNoop           = errors.New("session record input is unchanged")
	ErrRecordBudgetWaiting  = errors.New("session record token budget is exhausted")
)

type WorkerSettings struct {
	Enabled             bool
	Runtime             string
	DailyTokenCap       *int64
	BackfillEnabled     bool
	BackfillDays        *int
	BackfillTokenBudget *int64
	BackfillSince       *time.Time
}

type Owner struct {
	DashboardUserID        int64
	UserID                 string
	ProfileEmail           string
	UseUserIDAccessControl bool
}

type Job struct {
	SessionID         string
	OutputHash        []byte
	BasisHash         []byte
	Owner             Owner
	Lane              string
	Claim             int64
	Force             bool
	RangeRequest      bool
	RangeRequestedAt  *time.Time
	ClaimedAt         time.Time
	RuntimeKey        string
	DisclosureVersion string
	Model             string
	ReasoningEffort   string
	UsageDay          string
}

type PersistedRecord struct {
	SessionID         string
	Outcome           string
	Reason            string
	Agent             string
	OwnerUserID       *string
	OwnerProfileEmail *string
	RepositoryKey     string
	AccountKeys       []byte
	PromptVersion     int
	Runtime           string
	Model             string
	FactsHash         []byte
	FactsRulesVersion int
	BasisHash         []byte
	PrefixHash        []byte
	InputHash         []byte
	SourceInputHash   []byte
	CoversUntil       *time.Time
	CoversTurnKey     string
	CoveredRequests   int
	SourceFirstAt     *time.Time
	GeneratedAt       time.Time
	UsageDay          string
	Generations       int
	Folds             int
	Language          string
	Headline          string
	Content           RecordContent
	Kinds             []string
	Components        []string
	Screens           []string
	WorkTotal         int
	WorkDone          int
	WorkOpen          int
	WorkUnsettled     int
	UncoveredRequests int
	SearchText        string
	Calls             int
	InputBytes        int
	TokensReported    bool
	InputTokens       *int64
	CachedInputTokens *int64
	OutputTokens      *int64
	DurationMS        int
}

type Snapshot struct {
	Input               Input
	Agent               string
	RepositoryKey       string
	AccountKeys         []byte
	OwnerUserID         *string
	OwnerProfileEmail   *string
	FactsHash           []byte
	FactsRulesVersion   int
	BasisHash           []byte
	PrefixHash          []byte
	InputHash           []byte
	SourceInputHash     []byte
	CoversUntil         *time.Time
	CoversTurnKey       string
	CoveredRequests     int
	SourceFirstAt       *time.Time
	RetentionState      []byte
	AllRequestsRedacted bool
	Gaps                []string
	Existing            *PersistedRecord
}

type RuntimeConfig struct {
	Runtime         airuntime.Runtime
	Key             string
	RuntimeKey      string
	Model           string
	ReasoningEffort string
}

type RuntimeResolver interface {
	ResolveSessionRecordRuntime(ctx context.Context, configuredKey string) (RuntimeConfig, error)
}

type TransmissionReservation struct {
	ID             int64
	Lane           string
	ReservedTokens int64
	Day            string
	Runtime        string
	Model          string
	ReleaseBody    func()
}

type CallUsage struct {
	Reported          bool
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	InputBytes        int
	DurationMS        int
	Outcome           string
	DroppedItems      int
	UncoveredRequests int
	NewTerms          int
}

type WorkerRepository interface {
	AcquireSessionRecordWorker(context.Context) (func(), bool, error)
	SessionRecordSettings(context.Context) (WorkerSettings, error)
	ConsentingSessionRecordOwners(context.Context, string, string, bool) ([]Owner, error)
	ClaimNextSessionRecordJob(context.Context, Owner, string, WorkerSettings, time.Time) (Job, bool, error)
	LoadSessionRecordSnapshot(context.Context, Job) (Snapshot, error)
	BeginSessionRecordTransmission(context.Context, Job, Snapshot, string, string, int64, string) (TransmissionReservation, error)
	FinishSessionRecordTransmission(context.Context, TransmissionReservation, CallUsage) error
	PublishSessionRecord(context.Context, Job, Snapshot, PersistedRecord) error
	UpdateSessionRecordNoop(context.Context, Job, Snapshot) error
	PublishRedactedSessionRecord(context.Context, Job, Snapshot) error
	FailSessionRecord(context.Context, Job, string, bool) error
	ReleaseSessionRecordClaim(context.Context, Job) error
	RecoverSessionRecordClaims(context.Context) error
}

type Worker struct {
	repo                WorkerRepository
	runtimes            RuntimeResolver
	gate                *TransmissionGate
	featureEnabled      bool
	userIDAccessControl bool
	DisclosureVersion   string
	PromptVersion       int
	PollInterval        time.Duration
	BackfillSpacing     time.Duration
	DefaultTZ           string
	Now                 func() time.Time
	Logger              *slog.Logger
	mu                  sync.Mutex
	ownerCursor         int
	contentFailures     int
	temporaryFailures   int
	failureLogs         int
	status              WorkerStatus
}

type WorkerStatus struct {
	State           string     `json:"state"`
	PausedReason    string     `json:"paused_reason"`
	PausedUntil     *time.Time `json:"paused_until"`
	LastGeneratedAt *time.Time `json:"last_generated_at"`
}

func NewWorker(repo WorkerRepository, runtimes RuntimeResolver, featureEnabled bool, gate *TransmissionGate) *Worker {
	if gate == nil {
		gate = &TransmissionGate{}
	}
	return &Worker{repo: repo, runtimes: runtimes, gate: gate, featureEnabled: featureEnabled,
		userIDAccessControl: true,
		DisclosureVersion:   RecordDisclosureVersion, PromptVersion: PromptVersion,
		PollInterval: 30 * time.Second, BackfillSpacing: 2 * time.Second, Now: time.Now, Logger: slog.Default(),
		status: WorkerStatus{State: "off"}}
}

func (w *Worker) WithUserIDAccessControl(enabled bool) *Worker {
	w.userIDAccessControl = enabled
	return w
}

func (w *Worker) WithDefaultTimezone(name string) *Worker {
	if strings.TrimSpace(name) != "" {
		if _, err := time.LoadLocation(name); err == nil {
			w.DefaultTZ = name
		}
	}
	return w
}

func (w *Worker) Status() WorkerStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

func (w *Worker) setStatus(state, reason string) {
	w.mu.Lock()
	lastGenerated := w.status.LastGeneratedAt
	w.status = WorkerStatus{State: state, PausedReason: reason, LastGeneratedAt: lastGenerated}
	w.mu.Unlock()
}

func (w *Worker) pause(state, reason string, until *time.Time) {
	w.mu.Lock()
	lastGenerated := w.status.LastGeneratedAt
	w.status = WorkerStatus{State: state, PausedReason: reason, PausedUntil: until, LastGeneratedAt: lastGenerated}
	w.mu.Unlock()
}

func (w *Worker) paused(now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status.State != "paused" || w.status.PausedUntil == nil {
		return false
	}
	if now.Before(*w.status.PausedUntil) {
		return true
	}
	w.contentFailures = 0
	w.status.PausedUntil = nil
	w.status.PausedReason = ""
	w.status.State = "idle"
	return false
}

// RunOne claims and processes at most one session. Tests and the ticker share
// this exact path; tests inject airuntime.FakeRuntime or a local HTTP provider.
func (w *Worker) RunOne(ctx context.Context) (bool, string, error) {
	if w.paused(w.now()) {
		return false, "", nil
	}
	if !w.featureEnabled {
		w.setStatus("off", "feature_flag")
		return false, "", nil
	}
	settings, err := w.repo.SessionRecordSettings(ctx)
	if err != nil {
		w.setStatus("paused", "settings_error")
		return false, "", err
	}
	if !settings.Enabled {
		w.setStatus("off", "admin_disabled")
		return false, "", nil
	}
	runtimeCfg, err := w.runtimes.ResolveSessionRecordRuntime(ctx, settings.Runtime)
	if err != nil || runtimeCfg.Runtime == nil {
		if err == nil {
			err = airuntime.ErrNotConfigured
		}
		w.setStatus("paused", runtimeErrorCode(err))
		return false, "", err
	}
	if runtimeCfg.Key == "codex-app-server" || runtimeCfg.Runtime.Info().Key == "codex-app-server" {
		w.setStatus("paused", "runtime_unsupported")
		return false, "", airuntime.ErrNotConfigured
	}
	if runtimeCfg.RuntimeKey == "" {
		runtimeCfg.RuntimeKey = RuntimeConsentKey(runtimeCfg.Runtime.Info())
	}
	w.setStatus("running", "")
	owners, err := w.repo.ConsentingSessionRecordOwners(ctx, runtimeCfg.RuntimeKey, w.DisclosureVersion, w.userIDAccessControl)
	if err != nil {
		w.setStatus("paused", "consent_lookup_failed")
		return false, "", err
	}
	if len(owners) == 0 {
		w.setStatus("idle", "")
		return false, "", nil
	}
	start := w.nextOwner(len(owners))
	lanes := []string{"manual", "live"}
	if settings.BackfillEnabled {
		lanes = append(lanes, "backfill")
	}
	for _, lane := range lanes {
		for offset := range owners {
			owner := owners[(start+offset)%len(owners)]
			job, ok, pickErr := w.repo.ClaimNextSessionRecordJob(ctx, owner, lane, settings, w.now())
			if pickErr != nil {
				w.setStatus("paused", "candidate_lookup_failed")
				return false, "", pickErr
			}
			if !ok {
				continue
			}
			job.RuntimeKey = runtimeCfg.RuntimeKey
			job.DisclosureVersion = w.DisclosureVersion
			job.Model, job.ReasoningEffort = runtimeCfg.Model, runtimeCfg.ReasoningEffort
			w.advanceOwner((start + offset + 1) % len(owners))
			if err := w.generate(ctx, runtimeCfg, job); err != nil {
				if isContentFailure(err) {
					w.noteContentFailure()
				}
				return true, lane, err
			}
			w.clearContentFailures()
			return true, lane, nil
		}
	}
	w.setStatus("idle", "")
	return false, "", nil
}

func (w *Worker) Run(ctx context.Context) error {
	release, acquired, err := w.repo.AcquireSessionRecordWorker(ctx)
	if err != nil {
		return err
	}
	if !acquired {
		w.setStatus("idle", "worker_already_running")
		return nil
	}
	defer release()
	if err := w.repo.RecoverSessionRecordClaims(ctx); err != nil {
		return fmt.Errorf("recover session record claims: %w", err)
	}
	for ctx.Err() == nil {
		worked, lane, err := w.RunOne(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, airuntime.ErrCanceled) {
				continue
			}
			w.logFailure(err)
			if status := w.Status(); status.PausedUntil != nil && w.now().Before(*status.PausedUntil) {
				waitContext(ctx, status.PausedUntil.Sub(w.now()))
				continue
			}
			if isTemporaryFailure(err) {
				waitContext(ctx, w.nextTemporaryBackoff())
				continue
			}
			if isConfigurationFailure(err) {
				waitContext(ctx, w.PollInterval)
				continue
			}
			waitContext(ctx, w.PollInterval)
			continue
		}
		w.clearTemporaryFailures()
		w.clearFailureLogs()
		if worked && lane == "backfill" {
			waitContext(ctx, w.BackfillSpacing)
		} else if !worked {
			waitContext(ctx, w.PollInterval)
		}
	}
	return ctx.Err()
}

func (w *Worker) generate(ctx context.Context, runtimeCfg RuntimeConfig, job Job) error {
	job.UsageDay = w.recordDay(w.now())
	defer func() { _ = w.repo.ReleaseSessionRecordClaim(context.WithoutCancel(ctx), job) }()
	snapshot, err := w.repo.LoadSessionRecordSnapshot(ctx, job)
	if err != nil {
		return err
	}
	job.BasisHash = snapshot.BasisHash
	if snapshot.AllRequestsRedacted {
		if err := w.repo.PublishRedactedSessionRecord(ctx, job, snapshot); err != nil {
			return err
		}
		w.markGenerated()
		return nil
	}
	baseInput := snapshot.Input
	chunks, err := ChunkInput(baseInput)
	if err != nil {
		_ = w.repo.FailSessionRecord(context.WithoutCancel(ctx), job, "input_too_large", true)
		return err
	}
	inputHash, err := InputFingerprint(baseInput)
	if err != nil {
		return err
	}
	if snapshot.Existing != nil && !job.Force && snapshot.Existing.PromptVersion == w.PromptVersion &&
		bytesEqual(snapshot.Existing.SourceInputHash, snapshot.SourceInputHash) &&
		bytesEqual(snapshot.Existing.InputHash, inputHash) {
		if err := w.repo.UpdateSessionRecordNoop(ctx, job, snapshot); err != nil {
			return err
		}
		return nil
	}
	folding := false
	if snapshot.Existing != nil && !job.Force && len(baseInput.Turns) > 0 && totalInputBytes(chunks) > 32*1024 {
		if bytesEqual(snapshot.Existing.PrefixHash, snapshot.PrefixHash) {
			baseInput.Turns = suffixTurns(baseInput.Turns, snapshot.Existing.CoversTurnKey)
			if len(baseInput.Turns) > 0 && snapshot.Existing.CoversTurnKey != "" {
				last := baseInput.Turns[0]
				if last.Key == snapshot.Existing.CoversTurnKey && last.Status == "ended" {
					baseInput.Turns = baseInput.Turns[1:]
				}
			}
			if len(baseInput.Turns) == 0 {
				return w.repo.UpdateSessionRecordNoop(ctx, job, snapshot)
			}
			chunks, err = ChunkInput(baseInput)
			if err != nil {
				return err
			}
			folding = true
		}
	}
	draft := draftFromExisting(snapshot.Existing)
	if !folding {
		draft = RecordDraft{}
	}
	var metrics GenerationMetrics
	for chunkIndex, chunk := range chunks {
		if chunkIndex > 0 || folding {
			chunk.Prior = priorFromDraft(draft)
		}
		built, buildErr := BuildInput(chunk)
		if buildErr != nil {
			return buildErr
		}
		priorIDs := workIDs(draft.Content.Work)
		out, callMetrics, callErr := w.runChunk(ctx, runtimeCfg, job, snapshot, built, priorIDs)
		metrics.add(callMetrics)
		if callErr != nil {
			code := sessionRecordErrorCode(callErr)
			contentFailure := isContentFailure(callErr)
			if errors.Is(callErr, ErrRecordBudgetWaiting) {
				_ = w.repo.ReleaseSessionRecordClaim(context.WithoutCancel(ctx), job)
				until := w.nextRecordDay(w.now())
				w.pause("paused", "daily_cap", &until)
				return nil
			}
			if errors.Is(callErr, ErrRecordSendNotAllowed) || errors.Is(callErr, context.Canceled) {
				_ = w.repo.ReleaseSessionRecordClaim(context.WithoutCancel(ctx), job)
				return nil
			}
			if contentFailure && runtimeCfg.Key != "litellm-api" {
				retryOut, retryMetrics, retryErr := w.runChunk(ctx, runtimeCfg, job, snapshot, built, priorIDs)
				metrics.add(retryMetrics)
				if retryErr == nil {
					out = retryOut
					callErr = nil
				} else {
					callErr = retryErr
					code = sessionRecordErrorCode(retryErr)
					contentFailure = isContentFailure(retryErr)
				}
			}
			if callErr != nil {
				_ = w.repo.FailSessionRecord(context.WithoutCancel(ctx), job, code, contentFailure)
				return callErr
			}
		}
		draft = mergeDraft(draft, out)
	}
	if len(chunks) == 0 {
		return fmt.Errorf("record has no input turns")
	}
	generatedAt := w.now()
	result := persistedRecord(job, snapshot, runtimeCfg, draft, metrics, inputHash, len(chunks), folding, w.recordDay(generatedAt), generatedAt)
	if err := w.repo.PublishSessionRecord(ctx, job, snapshot, result); err != nil {
		return err
	}
	w.markGenerated()
	return nil
}

type GenerationMetrics struct {
	Model             string
	Calls             int
	InputBytes        int
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	TokensReported    bool
	DurationMS        int
	DroppedItems      int
	UncoveredRequests int
	NewTerms          int
}

func (m *GenerationMetrics) add(other GenerationMetrics) {
	if m.Calls == 0 {
		m.TokensReported = other.TokensReported
	} else {
		m.TokensReported = m.TokensReported && other.TokensReported
	}
	if other.Model != "" {
		m.Model = other.Model
	}
	m.Calls += other.Calls
	m.InputBytes += other.InputBytes
	m.DurationMS += other.DurationMS
	m.DroppedItems += other.DroppedItems
	m.UncoveredRequests += other.UncoveredRequests
	m.NewTerms += other.NewTerms
	if other.TokensReported {
		m.InputTokens += other.InputTokens
		m.CachedInputTokens += other.CachedInputTokens
		m.OutputTokens += other.OutputTokens
	}
}

func (w *Worker) runChunk(ctx context.Context, runtimeCfg RuntimeConfig, job Job, snapshot Snapshot, built BuiltInput, priorIDs map[string]bool) (ValidatedOutput, GenerationMetrics, error) {
	started := w.now()
	day := w.recordDay(started)
	metrics := GenerationMetrics{Calls: 1, InputBytes: len(built.JSON), TokensReported: true}
	callCtx, cancel := context.WithTimeout(ctx, RecordWallClock)
	defer cancel()
	callCtx = airuntime.WithSensitiveInput(callCtx)
	type sendCall struct {
		reservation TransmissionReservation
		lease       *TransmissionLease
	}
	type ticketHolder struct {
		mu               sync.Mutex
		calls            []sendCall
		authorizationErr error
	}
	var holder ticketHolder
	authorizedCtx := WithRequestAuthorizer(callCtx, func(requestCtx context.Context, request *http.Request) (func(), error) {
		var reservation TransmissionReservation
		lease, err := w.gate.Acquire(requestCtx, cancel, func(ctx context.Context) (func(), error) {
			var err error
			reservation, err = w.repo.BeginSessionRecordTransmission(ctx, job, snapshot, runtimeCfg.RuntimeKey, w.DisclosureVersion, MaxRunTokens, day)
			if err != nil {
				return nil, err
			}
			return reservation.ReleaseBody, nil
		})
		if err != nil {
			holder.mu.Lock()
			holder.authorizationErr = err
			holder.mu.Unlock()
			return nil, err
		}
		holder.mu.Lock()
		holder.calls = append(holder.calls, sendCall{reservation: reservation, lease: lease})
		holder.mu.Unlock()
		return lease.ReleaseBody, nil
	})
	var usage airuntime.Usage
	result, err := runtimeCfg.Runtime.Run(authorizedCtx, airuntime.RunRequest{
		Model: runtimeCfg.Model, ReasoningEffort: runtimeCfg.ReasoningEffort,
		Instructions: Instructions(), Prompt: string(built.JSON), Tools: nil, OutputSchema: OutputSchema,
		WallClock: RecordWallClock, MaxTotalTokens: MaxRunTokens, MaxOutputTokens: MaxOutputTokens,
	}, func(event airuntime.Event) {
		if event.Kind == airuntime.EventUsage && event.Usage != nil {
			usage = *event.Usage
		}
	})
	metrics.DurationMS = int(w.now().Sub(started).Milliseconds())
	if result != nil && result.Usage.Reported {
		usage = result.Usage
	}
	metrics.TokensReported = usage.Reported
	metrics.InputTokens, metrics.CachedInputTokens, metrics.OutputTokens = usage.InputTokens, usage.CachedInputTokens, usage.OutputTokens
	holder.mu.Lock()
	calls := append([]sendCall(nil), holder.calls...)
	// HTTP runtimes may classify a transport error as provider_unavailable.
	// The local authorization decision still controls budget and policy waits.
	if holder.authorizationErr != nil {
		err = holder.authorizationErr
	}
	holder.mu.Unlock()
	if len(calls) > 0 {
		metrics.Calls = len(calls)
	}
	settle := func(outcome string) error {
		var settleErr error
		for i := range calls {
			if calls[i].lease != nil {
				calls[i].lease.Finish()
			}
			callUsage := CallUsage{Reported: i == len(calls)-1 && usage.Reported, InputTokens: usage.InputTokens, CachedInputTokens: usage.CachedInputTokens,
				OutputTokens: usage.OutputTokens, InputBytes: metrics.InputBytes, DurationMS: metrics.DurationMS, Outcome: outcome}
			if i != len(calls)-1 {
				callUsage.Reported = false
				callUsage.InputTokens, callUsage.CachedInputTokens, callUsage.OutputTokens = 0, 0, 0
			}
			if err := w.repo.FinishSessionRecordTransmission(context.WithoutCancel(ctx), calls[i].reservation, callUsage); err != nil && settleErr == nil {
				settleErr = err
			}
		}
		return settleErr
	}
	if err != nil {
		if settleErr := settle("error:" + runtimeErrorCode(err)); settleErr != nil {
			err = fmt.Errorf("%w (usage settlement: %v)", err, settleErr)
		}
		return ValidatedOutput{}, metrics, err
	}
	if result == nil {
		err = fmt.Errorf("runtime returned no result")
		if settleErr := settle("rejected:protocol_error"); settleErr != nil {
			err = fmt.Errorf("%w (usage settlement: %v)", err, settleErr)
		}
		return ValidatedOutput{}, metrics, err
	}
	evidenceText, evidenceErr := EvidenceText(built.JSON)
	if evidenceErr != nil {
		if settleErr := settle("rejected:protocol_error"); settleErr != nil {
			evidenceErr = fmt.Errorf("%w (usage settlement: %v)", evidenceErr, settleErr)
		}
		return ValidatedOutput{}, metrics, evidenceErr
	}
	options := ValidationOptions{TurnKeys: built.TurnKeys, RequestTurns: built.RequestTurns,
		TurnStatuses: turnStatusesForInput(built, snapshot.Input), LastTurn: lastTurnName(built.TurnNames),
		InputJSON: evidenceText, PriorWorkIDs: priorIDs,
		Vocabulary: TagVocabulary{Components: vocabularyNames(snapshot.Input.Vocabulary.Components), Screens: vocabularyNames(snapshot.Input.Vocabulary.Screens),
			ComponentsClosed: snapshot.Input.Vocabulary.ComponentsClosed, ScreensClosed: snapshot.Input.Vocabulary.ScreensClosed}}
	validated, validationErr := ValidateOutput(result.FinalText, options)
	if validationErr != nil {
		if settleErr := settle("rejected:schema_violation"); settleErr != nil {
			validationErr = fmt.Errorf("%w (usage settlement: %v)", validationErr, settleErr)
		}
		return ValidatedOutput{}, metrics, validationErr
	}
	if settleErr := settle("ok"); settleErr != nil {
		return ValidatedOutput{}, metrics, settleErr
	}
	metrics.DroppedItems, metrics.UncoveredRequests, metrics.NewTerms = validated.DroppedItems, validated.UncoveredRequests, validated.NewTerms
	metrics.Model = result.Model
	return validated, metrics, nil
}

type RecordDraft struct {
	Headline      string
	Content       RecordContent
	Kinds         []string
	Components    []string
	Screens       []string
	NewComponents []string
	NewScreens    []string
}

func draftFromExisting(existing *PersistedRecord) RecordDraft {
	if existing == nil {
		return RecordDraft{}
	}
	return RecordDraft{Headline: existing.Headline, Content: existing.Content,
		Kinds: existing.Kinds, Components: existing.Components, Screens: existing.Screens}
}

func mergeDraft(previous RecordDraft, current ValidatedOutput) RecordDraft {
	content := previous.Content
	content.Bullets, content.Review = current.Bullets, current.Review
	if content.Work == nil {
		content.Work = []WorkItem{}
	}
	workIndex := map[string]int{}
	for i, work := range content.Work {
		workIndex[work.ID] = i
	}
	for _, update := range current.WorkUpdates {
		if i, ok := workIndex[update.ID]; ok {
			content.Work[i].Done, content.Work[i].Note = cloneBool(update.Done), boundedOptionalText(update.Note, 120)
		}
	}
	content.Work = append(content.Work, current.Work...)
	content.Skipped = append(content.Skipped, current.Skipped...)
	content.NewTerms.Component = appendUniqueTerms(content.NewTerms.Component, current.NewComponents)
	content.NewTerms.Screen = appendUniqueTerms(content.NewTerms.Screen, current.NewScreens)
	return RecordDraft{Headline: current.Headline, Content: content, Kinds: current.Kinds, Components: current.Components,
		Screens: current.Screens, NewComponents: current.NewComponents, NewScreens: current.NewScreens}
}

func persistedRecord(job Job, snapshot Snapshot, runtimeCfg RuntimeConfig, draft RecordDraft, metrics GenerationMetrics, inputHash []byte, chunks int, folded bool, usageDay string, now time.Time) PersistedRecord {
	content := draft.Content
	content.Gaps = append([]string(nil), snapshot.Gaps...)
	if content.Bullets == nil {
		content.Bullets = []Bullet{}
	}
	if content.Review == nil {
		content.Review = []ReviewItem{}
	}
	if content.Work == nil {
		content.Work = []WorkItem{}
	}
	if content.Skipped == nil {
		content.Skipped = []SkippedTurn{}
	}
	if content.NewTerms.Component == nil {
		content.NewTerms.Component = []string{}
	}
	if content.NewTerms.Screen == nil {
		content.NewTerms.Screen = []string{}
	}
	if content.Gaps == nil {
		content.Gaps = []string{}
	}
	record := PersistedRecord{SessionID: job.SessionID, Outcome: "ok", Agent: snapshot.Agent, OwnerUserID: snapshot.OwnerUserID,
		OwnerProfileEmail: snapshot.OwnerProfileEmail, RepositoryKey: snapshot.RepositoryKey, AccountKeys: snapshot.AccountKeys,
		PromptVersion: PromptVersion, Runtime: runtimeCfg.Key, Model: firstNonEmpty(metrics.Model, runtimeCfg.Model), FactsHash: snapshot.FactsHash,
		FactsRulesVersion: snapshot.FactsRulesVersion, BasisHash: snapshot.BasisHash, PrefixHash: snapshot.PrefixHash,
		InputHash: inputHash, SourceInputHash: snapshot.SourceInputHash, CoversUntil: snapshot.CoversUntil,
		CoversTurnKey: snapshot.CoversTurnKey, CoveredRequests: snapshot.CoveredRequests, SourceFirstAt: snapshot.SourceFirstAt,
		GeneratedAt: now, UsageDay: usageDay, Language: snapshot.Input.Language, Headline: draft.Headline, Content: content,
		Kinds: draft.Kinds, Components: draft.Components, Screens: draft.Screens, UncoveredRequests: metrics.UncoveredRequests,
		SearchText: searchText(draft.Headline, content), Calls: metrics.Calls, InputBytes: metrics.InputBytes,
		TokensReported: metrics.TokensReported, DurationMS: metrics.DurationMS}
	for _, work := range content.Work {
		record.WorkTotal++
		switch {
		case work.Done == nil:
			record.WorkUnsettled++
		case *work.Done:
			record.WorkDone++
		default:
			record.WorkOpen++
		}
	}
	if metrics.TokensReported {
		in, cached, out := metrics.InputTokens, metrics.CachedInputTokens, metrics.OutputTokens
		record.InputTokens, record.CachedInputTokens, record.OutputTokens = &in, &cached, &out
	}
	if snapshot.Existing != nil {
		record.Generations = snapshot.Existing.Generations + 1
		record.Folds = snapshot.Existing.Folds
	} else {
		record.Generations = 1
	}
	if folded && snapshot.Existing != nil {
		record.Folds += chunks
	} else if chunks > 1 {
		record.Folds = chunks - 1
	}
	return record
}

func searchText(headline string, content RecordContent) string {
	parts := []string{headline}
	for _, b := range content.Bullets {
		parts = append(parts, b.Text)
	}
	for _, r := range content.Review {
		parts = append(parts, r.Text)
	}
	for _, w := range content.Work {
		parts = append(parts, w.Text)
		if w.Decision != nil {
			parts = append(parts, *w.Decision)
		}
		if w.Note != nil {
			parts = append(parts, *w.Note)
		}
		for _, child := range w.Children {
			parts = append(parts, child.Text)
		}
	}
	text := strings.Join(parts, "\n")
	return clipEdges(text, 16*1024, 16*1024, 0)
}

func InputFingerprint(input Input) ([]byte, error) {
	chunks, err := ChunkInput(input)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	for _, chunk := range chunks {
		chunk.Vocabulary = Vocabulary{}
		built, err := BuildInput(chunk)
		if err != nil {
			return nil, err
		}
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(built.JSON)))
		_, _ = h.Write(size[:])
		_, _ = h.Write(built.JSON)
	}
	return h.Sum(nil), nil
}

func (w *Worker) nextOwner(n int) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if n == 0 {
		return 0
	}
	return w.ownerCursor % n
}
func (w *Worker) advanceOwner(n int) { w.mu.Lock(); w.ownerCursor = n; w.mu.Unlock() }
func (w *Worker) markGenerated() {
	now := w.now()
	w.mu.Lock()
	w.status = WorkerStatus{State: "running", LastGeneratedAt: &now}
	w.mu.Unlock()
}

func (w *Worker) noteContentFailure() {
	w.mu.Lock()
	w.contentFailures++
	count := w.contentFailures
	w.mu.Unlock()
	if count >= 5 {
		until := w.now().Add(time.Hour)
		w.pause("paused", "content_failures", &until)
	}
}

func (w *Worker) clearContentFailures() {
	w.mu.Lock()
	w.contentFailures = 0
	w.mu.Unlock()
}

func (w *Worker) nextTemporaryBackoff() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.temporaryFailures++
	shift := w.temporaryFailures - 1
	if shift > 12 {
		shift = 12
	}
	delay := time.Second * time.Duration(1<<shift)
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}

func (w *Worker) clearTemporaryFailures() {
	w.mu.Lock()
	w.temporaryFailures = 0
	w.mu.Unlock()
}

func (w *Worker) clearFailureLogs() {
	w.mu.Lock()
	w.failureLogs = 0
	w.mu.Unlock()
}

func (w *Worker) nextRecordDay(now time.Time) time.Time {
	location := time.UTC
	if w.DefaultTZ != "" {
		if loaded, err := time.LoadLocation(w.DefaultTZ); err == nil {
			location = loaded
		}
	}
	local := now.In(location)
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, location)
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w *Worker) recordDay(t time.Time) string {
	if w.DefaultTZ != "" {
		if loc, err := time.LoadLocation(w.DefaultTZ); err == nil {
			return t.In(loc).Format("2006-01-02")
		}
	}
	return t.UTC().Format("2006-01-02")
}

func (w *Worker) logFailure(err error) {
	w.mu.Lock()
	w.failureLogs++
	count := w.failureLogs
	w.mu.Unlock()
	if count != 1 && count&(count-1) != 0 {
		return
	}
	if w.Logger != nil {
		w.Logger.Error("session record worker operation failed", "code", runtimeErrorCode(err))
	}
}

func RuntimeConsentKey(info airuntime.Info) string {
	mode := info.AuthMode
	if mode == "" {
		mode = airuntime.AuthModeNone
	}
	return info.Key + ":" + mode
}

func runtimeErrorCode(err error) string {
	if err == nil {
		return "runtime_unconfigured"
	}
	var runErr *airuntime.RunError
	if errors.As(err, &runErr) && runErr.Code != "" {
		return runErr.Code
	}
	if errors.Is(err, airuntime.ErrNotConfigured) || errors.Is(err, airuntime.ErrNotLoggedIn) {
		return "runtime_unconfigured"
	}
	if errors.Is(err, airuntime.ErrUnavailable) {
		return "runtime_unavailable"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, airuntime.ErrCanceled) {
		return "canceled"
	}
	return "runtime_error"
}

func sessionRecordErrorCode(err error) string {
	if err != nil && (strings.HasPrefix(err.Error(), "record schema:") || strings.HasPrefix(err.Error(), "record content failure:")) {
		return "schema_violation"
	}
	return runtimeErrorCode(err)
}

func isContentFailure(err error) bool {
	if err == nil {
		return false
	}
	var runErr *airuntime.RunError
	if errors.As(err, &runErr) {
		switch runErr.Code {
		case "schema_violation", "protocol_error", "output_limit", "refused", "context_window_exceeded", "budget_exceeded":
			return true
		default:
			return false
		}
	}
	return strings.HasPrefix(err.Error(), "record schema:") || strings.HasPrefix(err.Error(), "record content failure:")
}

func isConfigurationFailure(err error) bool {
	return errors.Is(err, airuntime.ErrNotConfigured) || errors.Is(err, airuntime.ErrNotLoggedIn)
}
func isTemporaryFailure(err error) bool {
	return errors.Is(err, airuntime.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded)
}

func waitContext(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func bytesEqual(a, b []byte) bool { return bytes.Equal(a, b) }
func totalInputBytes(inputs []Input) int {
	sum := 0
	for _, in := range inputs {
		if b, e := BuildInput(in); e == nil {
			sum += len(b.JSON)
		}
	}
	return sum
}
func suffixTurns(turns []Turn, key string) []Turn {
	if key == "" {
		return turns
	}
	for i, t := range turns {
		if t.Key == key {
			return append([]Turn(nil), turns[i:]...)
		}
	}
	return turns
}
func lastTurnName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return names[len(names)-1]
}
func turnStatusesForInput(built BuiltInput, input Input) map[string]string {
	byKey := map[string]string{}
	for _, t := range input.Turns {
		byKey[t.Key] = wireStatus(t.Status)
	}
	out := map[string]string{}
	for name, key := range built.TurnKeys {
		out[name] = byKey[key]
	}
	return out
}
func workIDs(work []WorkItem) map[string]bool {
	out := map[string]bool{}
	for _, w := range work {
		if w.Done == nil || !*w.Done {
			out[w.ID] = true
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func vocabularyNames(values []VocabularyTerm) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, v.Term)
	}
	return out
}
func priorFromDraft(d RecordDraft) *Prior {
	p := &Prior{Headline: d.Headline, Bullets: make([]string, 0, len(d.Content.Bullets)), Review: make([]PriorNote, 0, len(d.Content.Review)), Kinds: d.Kinds, Components: d.Components, Screens: d.Screens, OpenWork: []PriorWork{}}
	for _, b := range d.Content.Bullets {
		p.Bullets = append(p.Bullets, b.Text)
	}
	for _, r := range d.Content.Review {
		p.Review = append(p.Review, PriorNote{Kind: r.Kind, Text: r.Text})
	}
	for _, w := range d.Content.Work {
		if w.Done != nil && *w.Done {
			p.DoneCount++
			continue
		}
		p.OpenWork = append(p.OpenWork, PriorWork{ID: w.ID, Kind: w.Kind, Text: w.Text, Decision: w.Decision, Note: w.Note})
	}
	return p
}
func appendUniqueTerms(dst, src []string) []string {
	for _, v := range src {
		if !containsFold(dst, v) {
			dst = append(dst, v)
		}
	}
	return dst
}
