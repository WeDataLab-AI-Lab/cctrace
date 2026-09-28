// Package aireport generates weekly AI reports: it binds the report tools to one
// caller and week, runs them through an airuntime.Runtime, validates the answer,
// and records runs, tool calls and reports.
package aireport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"cctrace/internal/airuntime"
	"cctrace/internal/store"
)

// DefaultRuntimeKey names the runtime when none is configured.
const DefaultRuntimeKey = "codex-app-server"

// DisclosureVersion is the current consent text version. Changing the text
// below means bumping it, which asks every user to consent again.
const DisclosureVersion = "2026-09-15"

// Consent text, served by the API so the screen never hard-codes it.
var (
	ConsentSends = []string{
		"선택된 작업 구간의 내 입력과 에이전트 응답 텍스트",
		"도구 이름과 실패 여부",
		"작업 구간 메타데이터 (프로젝트 이름·시각·입력 기록·도구 호출·실패 수)",
	}
	ConsentNotSends = []string{
		"도구 입력과 출력",
		"관리자가 제외한 계정·세션",
		"다른 사용자의 기록",
	}
	ProviderRetention = "확인되지 않음"
	// ProviderLabels names, per runtime, the provider the data is sent to.
	ProviderLabels = map[string]string{
		DefaultRuntimeKey: "OpenAI (Codex)",
		RuntimeOpenAI:     "OpenAI",
		RuntimeClaude:     "Anthropic",
		RuntimeNVIDIA:     "NVIDIA",
		RuntimeLiteLLM:    "LiteLLM",
	}
	// ProviderRetentions holds what is verified about a runtime's retention;
	// a runtime missing here shows ProviderRetention. openairuntime sends
	// store:false on every Responses request; nothing else is verified.
	ProviderRetentions = map[string]string{
		RuntimeOpenAI: "요청마다 store:false 지정 (Responses API 응답 저장 안 함 요청). 그 밖의 공급자 보관 정책은 확인되지 않음",
	}
)

// RetentionFor is the retention line shown for runtime.
func RetentionFor(runtime string) string {
	if r := ProviderRetentions[runtime]; r != "" {
		return r
	}
	return ProviderRetention
}

var (
	ErrAlreadyRunning  = errors.New("a report run is already running")
	ErrConsentRequired = errors.New("data transfer consent required")
	ErrConsentMismatch = errors.New("consent does not match the current runtime or disclosure")
	ErrNoRecords       = errors.New("no task segments in the week")
	ErrNotFound        = errors.New("report run not found")
	ErrNotRunning      = errors.New("report run is not running")
)

// AlreadyRunningError carries the caller's running run. RunID is 0 when it
// could not be identified.
type AlreadyRunningError struct{ RunID int64 }

func (e *AlreadyRunningError) Error() string {
	return fmt.Sprintf("%v (run %d)", ErrAlreadyRunning, e.RunID)
}

func (e *AlreadyRunningError) Is(target error) bool { return target == ErrAlreadyRunning }

// Scope is the caller: DashboardUserID owns runs and reports; ProfileEmail and
// UserID are resolveUserAccessParams' answer and bound the segments.
type Scope struct {
	DashboardUserID int64
	ProfileEmail    string
	UserID          string
}

// Store is the subset of *store.PgStore the service uses. Getters return
// (nil, nil) when the row does not exist.
type Store interface {
	CreateAIReportRun(ctx context.Context, r *store.AIReportRun) (int64, error)
	AppendAIToolCall(ctx context.Context, runID int64, c store.AIToolCall) error
	FinishAIToolCall(ctx context.Context, runID int64, seq int, status string, rows, bytes, durMs int) error
	UpdateAIRunUsage(ctx context.Context, runID int64, u store.AIUsage) error
	CompleteAIReportRun(ctx context.Context, runID int64, r store.AIReport, u store.AIUsage, dropped int) error
	FailAIReportRun(ctx context.Context, runID int64, status, code, msg string) error
	FailRunningAIReportRuns(ctx context.Context, code string) (int, error)
	GetAIReportRun(ctx context.Context, runID int64) (*store.AIReportRun, error)
	LatestAIReportRun(ctx context.Context, userID int64, week string) (*store.AIReportRun, error)
	RunningAIReportRun(ctx context.Context, userID int64) (*store.AIReportRun, error)
	SetAIRunModel(ctx context.Context, runID int64, model string) error
	GetAIReport(ctx context.Context, userID int64, week string) (*store.AIReport, error)
	ListAIToolCalls(ctx context.Context, runID int64, afterSeq int) ([]store.AIToolCall, error)
	GetAIConsent(ctx context.Context, userID int64, runtimeKey string) (*store.AIConsent, error)
	UpsertAIConsent(ctx context.Context, userID int64, runtimeKey, version string) error
	GetAIAutoSchedule(ctx context.Context) (*store.AIAutoSchedule, error)
	SetAIAutoSchedule(ctx context.Context, enabled *bool, weekday, hour, minute *int, actor string) error
	GetAIUserSchedule(ctx context.Context, userID int64) (*store.AIUserSchedule, error)
	UpsertAIUserSchedule(ctx context.Context, u store.AIUserSchedule) error
	LatestAIReportTZ(ctx context.Context, userID int64) (string, error)
	ListAIScheduleCandidates(ctx context.Context) ([]store.AIScheduleCandidate, error)
	ScheduledAIRunExists(ctx context.Context, userID int64, week string) (bool, error)
	AIUsageSince(ctx context.Context, since time.Time) (*store.AIUsageSummary, error)
	AIWeekSegments(ctx context.Context, sc store.AISegmentScope, f store.AISegmentFilter) ([]store.AISegment, error)
	AISegmentsByIDs(ctx context.Context, sc store.AISegmentScope, ids []int64) (map[int64]store.AISegment, error)
	AISegmentConversation(ctx context.Context, sc store.AISegmentScope, id int64, offset, limit int) ([]*store.SessionRecord, error)
	AIWeekAggregate(ctx context.Context, sc store.AISegmentScope) (*store.AIWeekAggregate, error)
	GetAISettings(ctx context.Context, runtime string) (*store.AISettings, error)
	SetAISettings(ctx context.Context, runtime, model, reasoningEffort, baseURL, actor string) error
	GetAIRuntimeChoice(ctx context.Context) (*store.AIRuntimeChoice, error)
	SetAIRuntimeChoice(ctx context.Context, runtime, actor string) error
	GetAIEnabledChoice(ctx context.Context) (*store.AIEnabledChoice, error)
	SetAIEnabledChoice(ctx context.Context, enabled *bool, actor string) error
	CredentialStore
}

// Config holds the runtime choice defaults and run budgets. Zero values take
// the defaults in NewService.
type Config struct {
	// EnvRuntime is CCTRACE_AI_RUNTIME_DEFAULT, which an admin's choice overrides.
	EnvRuntime string
	// EnvEnabled is CCTRACE_AI_ENABLED_DEFAULT, nil when unset; see Enablement.
	EnvEnabled *bool
	// Env is each runtime's model and effort from the environment, which an
	// admin setting overrides.
	Env map[string]RuntimeEnv
	// Missing says why a known runtime was not built, for the admin screen.
	Missing map[string]string
	// Keyring holds the API runtimes' keys. Nil gets one with no environment
	// keys and no secret, so nothing can be registered.
	Keyring           *Keyring
	MaxConcurrent     int
	ToolTimeout       time.Duration
	WallClock         time.Duration
	MaxToolCalls      int
	MaxTotalTokens    int64
	DisclosureVersion string
	// DefaultTZ is CCTRACE_AI_DEFAULT_TZ: the IANA zone an automatic run is read
	// in for a user who saved none and has no report to take one from. Empty is
	// UTC. The caller loads it first; see aiEnvFromEnv.
	DefaultTZ string
}

// Service runs reports. Runs outlive the request that started them: each run's
// context derives from the service root, not the HTTP request.
type Service struct {
	st Store
	// runtimes are the runtimes built at boot; byKey indexes them.
	runtimes []airuntime.Runtime
	byKey    map[string]airuntime.Runtime
	keyring  *Keyring
	cfg      Config
	sem      chan struct{}
	broker   *broker

	rootCtx     context.Context
	rootCancel  context.CancelFunc
	streamsDone chan struct{}
	streamsOnce sync.Once
	wg          sync.WaitGroup

	mu     sync.Mutex
	active map[int64]*activeRun
	byUser map[int64]int64
	closed bool
	// changing counts account changes executing; loginID is the last device
	// login started through Accounts. See Accounts.
	changing int
	loginID  string
	// switching counts runtime switches being saved; runtimeGen moves when one
	// starts and ends. See SelectRuntime.
	switching  int
	runtimeGen uint64

	// toolHook, when set, runs before each tool handler. Tests only.
	toolHook func(name string)
}

type activeRun struct {
	runtime  string
	cancel   context.CancelFunc
	canceled bool
	// cancelCode is recorded on a canceled run: "canceled" for the user's
	// stop, runtime_disabled when an admin turned reports off.
	cancelCode string
	// finishing is set once the outcome is decided and the final write begins.
	finishing bool
}

// NewService builds a service over the runtimes built at boot. Nil entries are
// skipped; none at all means no runtime is configured. Which one runs is only
// ever an explicit choice; see Selection.
func NewService(st Store, runtimes []airuntime.Runtime, cfg Config) *Service {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2
	}
	if cfg.ToolTimeout <= 0 {
		cfg.ToolTimeout = 15 * time.Second
	}
	if cfg.WallClock <= 0 {
		cfg.WallClock = 8 * time.Minute
	}
	if cfg.MaxToolCalls <= 0 {
		cfg.MaxToolCalls = 30
	}
	if cfg.MaxTotalTokens <= 0 {
		cfg.MaxTotalTokens = 600_000
	}
	if cfg.DisclosureVersion == "" {
		cfg.DisclosureVersion = DisclosureVersion
	}
	if cfg.Keyring == nil {
		cfg.Keyring = NewKeyring(st, "", nil)
	}
	var built []airuntime.Runtime
	byKey := map[string]airuntime.Runtime{}
	for _, rt := range runtimes {
		if rt == nil {
			continue
		}
		if key := rt.Info().Key; byKey[key] == nil {
			built, byKey[key] = append(built, rt), rt
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		st: st, runtimes: built, byKey: byKey, keyring: cfg.Keyring, cfg: cfg,
		sem:         make(chan struct{}, cfg.MaxConcurrent),
		broker:      newBroker(),
		rootCtx:     ctx,
		rootCancel:  cancel,
		streamsDone: make(chan struct{}),
		active:      map[int64]*activeRun{},
		byUser:      map[int64]int64{},
	}
}

func runtimeKey(info airuntime.Info) string {
	mode := info.AuthMode
	if mode == "" {
		mode = airuntime.AuthModeNone
	}
	return info.Key + ":" + mode
}

// RuntimeStatus reports the active runtime's identity and readiness. With no
// runtime chosen or built, or when the choice cannot be read, it returns the
// default key and a not-configured status.
func (s *Service) RuntimeStatus(ctx context.Context) (airuntime.Info, airuntime.Status) {
	rt, _, err := s.activeRuntime(ctx)
	if rt == nil || err != nil {
		return airuntime.Info{Key: DefaultRuntimeKey, AuthMode: airuntime.AuthModeNone}, airuntime.Status{}
	}
	return rt.Info(), s.status(ctx, rt)
}

// Start validates preconditions, records a running run on the runtime active
// now and starts it in the background. Errors: ErrRuntimeDisabled, airuntime.ErrNotConfigured,
// airuntime.ErrUnavailable, ErrConsentRequired, ErrNoRecords,
// ErrAccountChanging, ErrRuntimeChanging, *AlreadyRunningError.
// Start begins a run the user asked for.
func (s *Service) Start(ctx context.Context, sc Scope, wk Week) (*store.AIReportRun, error) {
	return s.start(ctx, sc, wk, store.AIRunStartedByManual)
}

// start begins a run and records who set it going. The scheduler passes
// AIRunStartedBySchedule so a later tick can tell its own run from one the user
// started by hand; every refusal below happens before the row is created, so a
// user who has not consented leaves no failed run behind each week.
func (s *Service) start(ctx context.Context, sc Scope, wk Week, startedBy string) (*store.AIReportRun, error) {
	// Taken before the runtime is read, and compared where the run becomes
	// active, so a switch in between fails this run instead of letting it run
	// on the runtime the admin just left.
	s.mu.Lock()
	gen := s.runtimeGen
	s.mu.Unlock()
	if err := s.requireEnabled(ctx); err != nil {
		return nil, err
	}
	rt, _, err := s.activeRuntime(ctx)
	if err != nil {
		return nil, err
	}
	if rt == nil {
		return nil, airuntime.ErrNotConfigured
	}
	info := rt.Info()
	s.mu.Lock()
	closed := s.closed
	busyID, busy := s.byUser[sc.DashboardUserID]
	changing, switching := s.accountChanging(info.Key), s.switching > 0
	s.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("%w: server shutting down", airuntime.ErrUnavailable)
	}
	if changing {
		return nil, ErrAccountChanging
	}
	if switching {
		return nil, ErrRuntimeChanging
	}
	if busy {
		return nil, &AlreadyRunningError{RunID: busyID}
	}

	if status := s.status(ctx, rt); !status.Configured {
		return nil, airuntime.ErrNotConfigured
	} else if !status.Available {
		return nil, fmt.Errorf("%w: %s", airuntime.ErrUnavailable, status.Reason)
	}
	consent, err := s.st.GetAIConsent(ctx, sc.DashboardUserID, runtimeKey(info))
	if err != nil {
		return nil, err
	}
	if consent == nil || consent.DisclosureVersion != s.cfg.DisclosureVersion {
		return nil, ErrConsentRequired
	}
	agg, err := s.st.AIWeekAggregate(ctx, segmentScope(sc, wk))
	if err != nil {
		return nil, err
	}
	if agg == nil || agg.SegmentCount == 0 {
		return nil, ErrNoRecords
	}
	settings, err := s.SettingsFor(ctx, info.Key)
	if err != nil {
		return nil, err
	}

	run := &store.AIReportRun{
		DashboardUserID: sc.DashboardUserID, Week: wk.ID, TZ: wk.TZ, Since: wk.Since, Until: wk.Until,
		ScopeProfileEmail: sc.ProfileEmail, ScopeUserID: sc.UserID, Status: store.AIRunRunning,
		StartedBy: startedBy,
		Runtime:   info.Key, Model: settings.Model, AuthMode: info.AuthMode, StartedAt: time.Now(),
	}
	id, err := s.st.CreateAIReportRun(ctx, run)
	if errors.Is(err, store.ErrAIRunAlreadyRunning) {
		return nil, s.alreadyRunning(ctx, sc, wk)
	}
	if err != nil {
		return nil, err
	}
	run.ID = id

	runCtx, cancel := context.WithCancel(s.rootCtx)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		_ = s.st.FailAIReportRun(context.Background(), id, store.AIRunFailed, "server_shutdown", "server shutting down")
		return nil, fmt.Errorf("%w: server shutting down", airuntime.ErrUnavailable)
	}
	// Checked again where the run becomes active: a change that began during
	// the checks above would otherwise run under it.
	if s.accountChanging(info.Key) {
		s.mu.Unlock()
		cancel()
		_ = s.st.FailAIReportRun(context.Background(), id, store.AIRunFailed, "runtime_account_changing", "ai runtime account is changing")
		return nil, ErrAccountChanging
	}
	if s.switching > 0 || s.runtimeGen != gen {
		s.mu.Unlock()
		cancel()
		_ = s.st.FailAIReportRun(context.Background(), id, store.AIRunFailed, "runtime_changing", "ai runtime is being switched")
		return nil, ErrRuntimeChanging
	}
	s.active[id] = &activeRun{runtime: info.Key, cancel: cancel}
	s.byUser[sc.DashboardUserID] = id
	s.wg.Add(1)
	s.mu.Unlock()

	s.broker.open(id)
	req := s.request(sc, wk, agg)
	req.Model, req.ReasoningEffort = settings.Model, settings.ReasoningEffort
	go s.execute(runCtx, rt, *run, req, sc, wk)
	return run, nil
}

// requireEnabled answers ErrRuntimeDisabled while AI reports are off.
func (s *Service) requireEnabled(ctx context.Context) error {
	e, err := s.Enablement(ctx)
	if err != nil {
		return err
	}
	if !e.Enabled {
		return ErrRuntimeDisabled
	}
	return nil
}

// alreadyRunning names the running run after the database refused a second one.
func (s *Service) alreadyRunning(ctx context.Context, sc Scope, wk Week) error {
	s.mu.Lock()
	id, ok := s.byUser[sc.DashboardUserID]
	s.mu.Unlock()
	if !ok {
		// One running run per user in any week, so look beyond this week.
		if running, err := s.st.RunningAIReportRun(ctx, sc.DashboardUserID); err == nil && running != nil {
			id = running.ID
		}
	}
	return &AlreadyRunningError{RunID: id}
}

func (s *Service) execute(ctx context.Context, rt airuntime.Runtime, run store.AIReportRun, req airuntime.RunRequest, sc Scope, wk Week) {
	defer s.wg.Done()

	var res *airuntime.Result
	var err error
	var lastUsage atomic.Pointer[airuntime.Usage]
	select {
	case s.sem <- struct{}{}:
		res, err = runRecovered(ctx, rt, req, s.sink(run.ID, &lastUsage))
		<-s.sem
	case <-ctx.Done():
		err = airuntime.ErrCanceled
	}

	dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	status, code, msg := "", "", ""
	var report ValidatedReport
	dropped := 0
	switch {
	case ctx.Err() != nil:
		// Canceled by the user or by shutdown; a partial answer is discarded either way.
	case err != nil:
		status, code, msg = store.AIRunFailed, errorCode(err), truncateBytes(err.Error(), 500)
	case res == nil:
		status, code, msg = store.AIRunFailed, "protocol_error", "runtime returned no result"
	default:
		report, dropped, err = ValidateOutput(dbCtx, s.st, sc, wk, res.FinalText)
		switch {
		case errors.Is(err, ErrEmptyReport):
			status, code, msg = store.AIRunFailed, "empty_report", truncateBytes(err.Error(), 500)
		case errors.Is(err, ErrInvalidOutput):
			status, code, msg = store.AIRunFailed, "invalid_output", truncateBytes(err.Error(), 500)
			// The stored message says the answer was rejected but not what it was,
			// which leaves "summary must be 1..2000 characters" unreadable: empty,
			// truncated mid-sentence, or the wrong shape all say the same thing.
			// The answer is the operator's only way to tell those apart.
			log.Printf("[aireport] run %d invalid output, answer was: %s", run.ID, truncateBytes(res.FinalText, 800))
		case err != nil:
			status, code, msg = store.AIRunFailed, "store_error", "report validation failed"
			log.Printf("[aireport] run %d validation: %v", run.ID, err)
		}
	}

	// From here the outcome is fixed: Cancel answers ErrNotRunning instead of
	// canceling a run whose report is being written. The run leaves active only
	// after its final write, so there is no window where the database still
	// says running and nothing in memory says finishing.
	s.mu.Lock()
	if a := s.active[run.ID]; a != nil {
		if ctx.Err() != nil {
			if a.canceled {
				status, code, msg = store.AIRunCanceled, a.cancelCode, ""
			} else {
				status, code, msg = store.AIRunFailed, "server_shutdown", "server shut down during the run"
			}
		}
		a.finishing = true
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.active, run.ID)
		if s.byUser[run.DashboardUserID] == run.ID {
			delete(s.byUser, run.DashboardUserID)
		}
		s.mu.Unlock()
		s.broker.close(run.ID)
	}()

	if status != "" && code != "canceled" {
		// The stored message is for operators; the log keeps the full cause.
		log.Printf("[aireport] run %d %s: %s", run.ID, code, msg)
	}
	if status == "" && res.Model != "" && res.Model != run.Model {
		if err := s.st.SetAIRunModel(dbCtx, run.ID, res.Model); err != nil {
			log.Printf("[aireport] run %d model: %v", run.ID, err)
		}
	}
	if status == "" {
		usage := res.Usage
		if !usage.Reported {
			if streamed := lastUsage.Load(); streamed != nil {
				usage = *streamed
			}
		}
		err = retryFinalWrite(func(ctx context.Context) error {
			return s.st.CompleteAIReportRun(ctx, run.ID, store.AIReport{
				DashboardUserID: run.DashboardUserID, Week: wk.ID, RunID: run.ID, TZ: wk.TZ, Since: wk.Since, Until: wk.Until,
				Summary: report.Summary, Items: report.Items, GeneratedAt: time.Now(),
			}, storeUsage(usage), dropped)
		})
		if err != nil {
			log.Printf("[aireport] run %d complete: %v", run.ID, err)
			status, code, msg = store.AIRunFailed, "store_error", "saving the report failed"
		}
	}
	if status != "" {
		if err := retryFinalWrite(func(ctx context.Context) error {
			return s.st.FailAIReportRun(ctx, run.ID, status, code, msg)
		}); err != nil {
			// The row stays running; Cancel can still close it and boot recovery will.
			log.Printf("[aireport] run %d fail(%s): %v", run.ID, code, err)
		}
	}
}

// runRecovered turns a runtime panic into a failed run instead of a crashed
// server with a leaked concurrency slot.
func runRecovered(ctx context.Context, rt airuntime.Runtime, req airuntime.RunRequest, sink func(airuntime.Event)) (res *airuntime.Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[aireport] runtime panicked: %v", r)
			res, err = nil, fmt.Errorf("%w: runtime panicked", airuntime.ErrProtocol)
		}
	}()
	return rt.Run(ctx, req, sink)
}

// retryFinalWrite gives the run's terminal write three tries, each with its own
// deadline, because a run left running blocks the user's next run until restart.
func retryFinalWrite(write func(context.Context) error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = write(ctx)
		cancel()
		if err == nil {
			return nil
		}
	}
	return err
}

func (s *Service) request(sc Scope, wk Week, agg *store.AIWeekAggregate) airuntime.RunRequest {
	tools := BuildTools(s.st, sc, wk)
	if hook := s.toolHook; hook != nil {
		for i := range tools {
			name, handler := tools[i].Name, tools[i].Handler
			tools[i].Handler = func(ctx context.Context, args json.RawMessage) (airuntime.ToolOutput, error) {
				hook(name)
				return handler(ctx, args)
			}
		}
	}
	return airuntime.RunRequest{
		Instructions:   Instructions,
		Prompt:         BuildPrompt(wk, agg),
		Tools:          tools,
		OutputSchema:   OutputSchema,
		ToolTimeout:    s.cfg.ToolTimeout,
		WallClock:      s.cfg.WallClock,
		MaxToolCalls:   s.cfg.MaxToolCalls,
		MaxTotalTokens: s.cfg.MaxTotalTokens,
	}
}

// sink records progress before publishing it, so a subscriber that falls back
// to the database never sees less than the stream did. Writes use their own
// context: a canceled run still logs the calls it made.
func (s *Service) sink(runID int64, lastUsage *atomic.Pointer[airuntime.Usage]) func(airuntime.Event) {
	return func(ev airuntime.Event) {
		if ev.Kind == airuntime.EventUsage && ev.Usage != nil && ev.Usage.Reported {
			u := *ev.Usage
			lastUsage.Store(&u)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		switch ev.Kind {
		case airuntime.EventToolCall:
			ev.Args = SummarizeArgs(ev.Tool, ev.Args)
			args, _ := json.Marshal(ev.Args)
			err = s.st.AppendAIToolCall(ctx, runID, store.AIToolCall{
				RunID: runID, Seq: ev.Seq, Tool: ev.Tool, Args: args, Status: store.AIToolCallRunning, StartedAt: time.Now(),
			})
		case airuntime.EventToolResult:
			ev.Args = SummarizeArgs(ev.Tool, ev.Args)
			// The event carries no byte count; 0 is written until the runtime contract has one.
			err = s.st.FinishAIToolCall(ctx, runID, ev.Seq, ev.Status, ev.Rows, 0, ev.DurationMs)
		case airuntime.EventUsage:
			if ev.Usage == nil {
				return
			}
			err = s.st.UpdateAIRunUsage(ctx, runID, storeUsage(*ev.Usage))
		default:
			return
		}
		if err != nil {
			log.Printf("[aireport] run %d %s seq=%d: %v", runID, ev.Kind, ev.Seq, err)
		}
		s.broker.publish(runID, ev)
	}
}

func storeUsage(u airuntime.Usage) store.AIUsage {
	if !u.Reported {
		return store.AIUsage{}
	}
	return store.AIUsage{Reported: true, InputTokens: u.InputTokens, CachedInputTokens: u.CachedInputTokens, OutputTokens: u.OutputTokens}
}

func errorCode(err error) string {
	var re *airuntime.RunError
	if errors.As(err, &re) && re.Code != "" {
		return re.Code
	}
	switch {
	case errors.Is(err, airuntime.ErrTimeLimit):
		return "time_limit"
	case errors.Is(err, airuntime.ErrBudget):
		return "budget"
	case errors.Is(err, airuntime.ErrUnexpectedTool):
		return "unexpected_tool"
	case errors.Is(err, airuntime.ErrProtocol):
		return "protocol_error"
	case errors.Is(err, airuntime.ErrUnavailable):
		return "runtime_unavailable"
	case errors.Is(err, airuntime.ErrNotConfigured):
		return "runtime_unconfigured"
	case errors.Is(err, airuntime.ErrCanceled):
		return "canceled"
	}
	return "runtime_error"
}

// GetRun returns the caller's run, or ErrNotFound -- also for another user's run,
// administrators included.
func (s *Service) GetRun(ctx context.Context, sc Scope, runID int64) (*store.AIReportRun, error) {
	run, err := s.st.GetAIReportRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run == nil || run.DashboardUserID != sc.DashboardUserID {
		return nil, ErrNotFound
	}
	return run, nil
}

// Cancel stops the caller's running run. The run ends as canceled and the
// previous report stays.
func (s *Service) Cancel(ctx context.Context, sc Scope, runID int64) error {
	run, err := s.GetRun(ctx, sc, runID)
	if err != nil {
		return err
	}
	if run.Status != store.AIRunRunning {
		return ErrNotRunning
	}
	s.mu.Lock()
	a := s.active[runID]
	if a != nil && a.finishing {
		s.mu.Unlock()
		return ErrNotRunning
	}
	if a != nil {
		a.canceled, a.cancelCode = true, "canceled"
		a.cancel()
	}
	s.mu.Unlock()
	if a == nil {
		// Running in the database with nothing executing it: its final write
		// failed. Close it here so the user is not locked out until a restart.
		return s.st.FailAIReportRun(ctx, runID, store.AIRunCanceled, "canceled", "")
	}
	return nil
}

// cancelActive stops every run not yet finishing; each ends canceled with code.
func (s *Service) cancelActive(code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.active {
		if !a.finishing && !a.canceled {
			a.canceled, a.cancelCode = true, code
			a.cancel()
		}
	}
}

// Subscribe streams a run's progress after afterSeq. The channel closes when the
// run ends or the subscriber falls behind; either way the database has the rest.
func (s *Service) Subscribe(runID int64, afterSeq int) (<-chan airuntime.Event, func()) {
	return s.broker.subscribe(runID, afterSeq)
}

// ToolCalls lists a run's logged tool calls after afterSeq. Callers check
// ownership with GetRun first.
func (s *Service) ToolCalls(ctx context.Context, runID int64, afterSeq int) ([]store.AIToolCall, error) {
	return s.st.ListAIToolCalls(ctx, runID, afterSeq)
}

// WeekView is what the report screen needs for one week, all owned by the caller.
type WeekView struct {
	SegmentCount    int64
	Run             *store.AIReportRun
	RunToolCalls    []store.AIToolCall
	Report          *store.AIReport
	ReportRun       *store.AIReportRun
	ReportToolCalls []store.AIToolCall
	// Segments holds the report items' current metadata. An item missing here is
	// no longer visible and must not be shown.
	Segments map[int64]store.AISegment
}

func (s *Service) WeekView(ctx context.Context, sc Scope, wk Week) (*WeekView, error) {
	scope := segmentScope(sc, wk)
	v := &WeekView{Segments: map[int64]store.AISegment{}}
	agg, err := s.st.AIWeekAggregate(ctx, scope)
	if err != nil {
		return nil, err
	}
	if agg != nil {
		v.SegmentCount = agg.SegmentCount
	}
	if v.Run, err = s.st.LatestAIReportRun(ctx, sc.DashboardUserID, wk.ID); err != nil {
		return nil, err
	}
	if v.Run != nil {
		if v.RunToolCalls, err = s.st.ListAIToolCalls(ctx, v.Run.ID, 0); err != nil {
			return nil, err
		}
	}
	if v.Report, err = s.st.GetAIReport(ctx, sc.DashboardUserID, wk.ID); err != nil || v.Report == nil {
		return v, err
	}
	if v.Run != nil && v.Run.ID == v.Report.RunID {
		v.ReportRun, v.ReportToolCalls = v.Run, v.RunToolCalls
	} else {
		if v.ReportRun, err = s.st.GetAIReportRun(ctx, v.Report.RunID); err != nil {
			return nil, err
		}
		if v.ReportToolCalls, err = s.st.ListAIToolCalls(ctx, v.Report.RunID, 0); err != nil {
			return nil, err
		}
	}
	var ids []int64
	for _, it := range v.Report.Items {
		var id int64
		if _, scanErr := fmt.Sscan(it.SegmentID, &id); scanErr == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 {
		// The report key has no time zone: read items in the window the report
		// was generated for, or a viewer in another zone loses its edge items.
		itemScope := scope
		if !v.Report.Since.IsZero() && !v.Report.Until.IsZero() {
			itemScope.Since, itemScope.Until = v.Report.Since, v.Report.Until
		}
		if v.Segments, err = s.st.AISegmentsByIDs(ctx, itemScope, ids); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// ConsentState is the caller's consent for the current runtime and disclosure.
type ConsentState struct {
	// Runtime is the active runtime; RuntimeKey adds its auth mode.
	Runtime           string
	RuntimeKey        string
	DisclosureVersion string
	Granted           bool
	GrantedAt         *time.Time
}

// currentRuntimeKey is the consent key of the active runtime; rt is nil when
// none is built.
func (s *Service) currentRuntimeKey(ctx context.Context) (string, airuntime.Runtime, error) {
	rt, _, err := s.activeRuntime(ctx)
	if err != nil {
		return "", nil, err
	}
	if rt == nil {
		return runtimeKey(airuntime.Info{Key: DefaultRuntimeKey}), nil, nil
	}
	return runtimeKey(rt.Info()), rt, nil
}

func (s *Service) Consent(ctx context.Context, sc Scope) (ConsentState, error) {
	key, rt, err := s.currentRuntimeKey(ctx)
	if err != nil {
		return ConsentState{}, err
	}
	cs := ConsentState{Runtime: DefaultRuntimeKey, RuntimeKey: key, DisclosureVersion: s.cfg.DisclosureVersion}
	if rt != nil {
		cs.Runtime = rt.Info().Key
	}
	c, err := s.st.GetAIConsent(ctx, sc.DashboardUserID, cs.RuntimeKey)
	if err != nil {
		return cs, err
	}
	if c != nil && c.DisclosureVersion == cs.DisclosureVersion {
		at := c.ConsentedAt
		cs.Granted, cs.GrantedAt = true, &at
	}
	return cs, nil
}

// GrantConsent records consent only for the current runtime key and disclosure
// version, so a screen showing stale text cannot consent to new terms.
func (s *Service) GrantConsent(ctx context.Context, sc Scope, key, version string) error {
	if err := s.requireEnabled(ctx); err != nil {
		return err
	}
	current, rt, err := s.currentRuntimeKey(ctx)
	if err != nil {
		return err
	}
	if rt == nil {
		return airuntime.ErrNotConfigured
	}
	if key != current || version != s.cfg.DisclosureVersion {
		return ErrConsentMismatch
	}
	return s.st.UpsertAIConsent(ctx, sc.DashboardUserID, key, version)
}

// UsageSince summarizes runs started at or after since, for the admin screen.
func (s *Service) UsageSince(ctx context.Context, since time.Time) (*store.AIUsageSummary, error) {
	return s.st.AIUsageSince(ctx, since)
}

// RecoverOnBoot fails runs a previous process left running.
func (s *Service) RecoverOnBoot(ctx context.Context) error {
	n, err := s.st.FailRunningAIReportRuns(ctx, "server_restarted")
	if err != nil {
		return err
	}
	if n > 0 {
		log.Printf("[aireport] marked %d interrupted report run(s) failed", n)
	}
	return nil
}

// StreamsDone closes when the server starts shutting down, so progress streams
// can end instead of holding http.Server.Shutdown open.
func (s *Service) StreamsDone() <-chan struct{} { return s.streamsDone }

// CloseStreams ends progress streams without stopping runs.
func (s *Service) CloseStreams() { s.streamsOnce.Do(func() { close(s.streamsDone) }) }

// Shutdown refuses new runs, cancels running ones (they end as
// failed/server_shutdown) and waits for them until ctx ends.
func (s *Service) Shutdown(ctx context.Context) {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.CloseStreams()
	s.rootCancel()
	done := make(chan struct{})
	go func() {
		// A pending device login holds a runtime process of its own. Its
		// teardown waits on that process, so it shares the deadline below.
		if m, err := s.Accounts(); err == nil {
			m.CloseLogins()
		}
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		log.Printf("[aireport] shutdown: runs still finishing: %v", ctx.Err())
	}
}
