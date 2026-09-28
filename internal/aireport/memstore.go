package aireport

import (
	"context"
	"sort"
	"sync"
	"time"

	"cctrace/internal/store"
)

// MemStore is an in-memory Store for service and handler tests in other
// packages, the way airuntime.FakeRuntime stands in for a runtime. It keeps the
// contracts the tests lean on -- one running run per user, NULL-able usage,
// segment scoping by owner and [Since, Until) -- and nothing more.
//
// Segments are keyed by owner: the scope's UserID, or "email:"+ProfileEmail when
// UserID is empty.
type MemStore struct {
	mu sync.Mutex

	Segments      map[string][]store.AISegment
	Conversations map[int64][]*store.SessionRecord
	Runs          map[int64]*store.AIReportRun
	Reports       map[string]*store.AIReport
	ToolCalls     map[int64][]store.AIToolCall
	Consents      map[string]*store.AIConsent
	// Settings holds ai_runtime_settings rows by runtime; a missing key means
	// none saved.
	Settings map[string]*store.AISettings
	// Choice is the admin's runtime choice; nil means none saved.
	Choice *store.AIRuntimeChoice
	// Enabled is the admin's AI report switch; nil means none saved.
	Enabled *store.AIEnabledChoice
	// Credentials holds sealed provider keys; CredentialErr fails their writes.
	Credentials   map[string]*store.AIProviderCredential
	CredentialErr error
	// CompleteCalls counts CompleteAIReportRun calls.
	CompleteCalls int
	// AutoSchedule is the admin's default for automatic runs; nil means none saved.
	AutoSchedule *store.AIAutoSchedule
	// UserSchedules holds each user's override by dashboard user id.
	UserSchedules map[int64]*store.AIUserSchedule
	// Candidates is what the scheduler sees on a tick. Tests set it; it is not
	// derived from UserSchedules, so a test can hand the scheduler a user who
	// has saved nothing.
	Candidates []store.AIScheduleCandidate

	lastFilter store.AISegmentFilter
	nextID     int64
}

var _ Store = (*MemStore)(nil)

func NewMemStore() *MemStore {
	return &MemStore{
		Segments:      map[string][]store.AISegment{},
		Conversations: map[int64][]*store.SessionRecord{},
		Runs:          map[int64]*store.AIReportRun{},
		Reports:       map[string]*store.AIReport{},
		ToolCalls:     map[int64][]store.AIToolCall{},
		Consents:      map[string]*store.AIConsent{},
		UserSchedules: map[int64]*store.AIUserSchedule{},
	}
}

func (m *MemStore) GetAIAutoSchedule(context.Context) (*store.AIAutoSchedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.AutoSchedule, nil
}

// SetAIAutoSchedule replaces the whole default, as the single-row UPSERT does:
// the request carries the state the row should have, and a nil field clears
// that one. Callers that mean to change a single control send the other three
// as they stand -- a request holding only the changed field would wipe the
// rest.
func (m *MemStore) SetAIAutoSchedule(_ context.Context, enabled *bool, weekday, hour, minute *int, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if enabled == nil && weekday == nil && hour == nil && minute == nil {
		m.AutoSchedule = nil
		return nil
	}
	m.AutoSchedule = &store.AIAutoSchedule{
		Enabled: enabled, Weekday: weekday, Hour: hour, Minute: minute,
		UpdatedBy: actor, UpdatedAt: time.Now(),
	}
	return nil
}

func (m *MemStore) GetAIUserSchedule(_ context.Context, userID int64) (*store.AIUserSchedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.UserSchedules[userID], nil
}

func (m *MemStore) UpsertAIUserSchedule(_ context.Context, u store.AIUserSchedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := u
	cp.UpdatedAt = time.Now()
	m.UserSchedules[u.DashboardUserID] = &cp
	return nil
}

// LatestAIReportTZ answers from the stored reports, as the query does: the most
// recently generated one for that user, or "" when they have none.
func (m *MemStore) LatestAIReportTZ(_ context.Context, userID int64) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tz, newest := "", time.Time{}
	for _, r := range m.Reports {
		if r.DashboardUserID != userID || r.TZ == "" {
			continue
		}
		if tz == "" || r.GeneratedAt.After(newest) {
			tz, newest = r.TZ, r.GeneratedAt
		}
	}
	return tz, nil
}

func (m *MemStore) ListAIScheduleCandidates(context.Context) ([]store.AIScheduleCandidate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.AIScheduleCandidate, len(m.Candidates))
	copy(out, m.Candidates)
	return out, nil
}

// ScheduledAIRunExists counts a run in any state, as the query does: a failed
// automatic run has been spent and must not fire again the same week.
func (m *MemStore) ScheduledAIRunExists(_ context.Context, userID int64, week string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.Runs {
		if r.DashboardUserID == userID && r.Week == week && r.StartedBy == store.AIRunStartedBySchedule {
			return true, nil
		}
	}
	return false, nil
}

func memOwner(sc store.AISegmentScope) string {
	if sc.UserID != "" {
		return sc.UserID
	}
	return "email:" + sc.ProfileEmail
}

func memKey(userID int64, s string) string { return itoa64(userID) + "/" + s }

// AddSegment stores seg under owner.
func (m *MemStore) AddSegment(owner string, seg store.AISegment) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Segments[owner] = append(m.Segments[owner], seg)
}

// LastFilter returns the filter of the most recent AIWeekSegments call.
func (m *MemStore) LastFilter() store.AISegmentFilter {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastFilter
}

func (m *MemStore) CreateAIReportRun(_ context.Context, r *store.AIReportRun) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Status == store.AIRunRunning {
		for _, existing := range m.Runs {
			if existing.DashboardUserID == r.DashboardUserID && existing.Status == store.AIRunRunning {
				return 0, store.ErrAIRunAlreadyRunning
			}
		}
	}
	m.nextID++
	cp := *r
	cp.ID = m.nextID
	if cp.StartedAt.IsZero() {
		cp.StartedAt = time.Now()
	}
	// The column has this default in Postgres, so the fake answers the same. The
	// scheduler decides whether to fire by reading this marker back.
	if cp.StartedBy == "" {
		cp.StartedBy = store.AIRunStartedByManual
	}
	m.Runs[cp.ID] = &cp
	return cp.ID, nil
}

func (m *MemStore) AppendAIToolCall(_ context.Context, runID int64, c store.AIToolCall) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.RunID = runID
	if c.StartedAt.IsZero() {
		c.StartedAt = time.Now()
	}
	m.ToolCalls[runID] = append(m.ToolCalls[runID], c)
	return nil
}

func (m *MemStore) FinishAIToolCall(_ context.Context, runID int64, seq int, status string, rows, bytes, durMs int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	calls := m.ToolCalls[runID]
	for i := range calls {
		if calls[i].Seq == seq {
			calls[i].Status = status
			calls[i].ResultRows, calls[i].ResultBytes, calls[i].DurationMs = &rows, &bytes, &durMs
		}
	}
	return nil
}

func (m *MemStore) UpdateAIRunUsage(_ context.Context, runID int64, u store.AIUsage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if run := m.Runs[runID]; run != nil {
		run.Usage = u
	}
	return nil
}

func (m *MemStore) CompleteAIReportRun(_ context.Context, runID int64, r store.AIReport, u store.AIUsage, dropped int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CompleteCalls++
	if run := m.Runs[runID]; run != nil {
		now := time.Now()
		run.Status, run.Usage, run.ItemsDropped, run.FinishedAt = store.AIRunCompleted, u, dropped, &now
	}
	r.RunID = runID
	if r.GeneratedAt.IsZero() {
		r.GeneratedAt = time.Now()
	}
	m.Reports[memKey(r.DashboardUserID, r.Week)] = &r
	return nil
}

func (m *MemStore) FailAIReportRun(_ context.Context, runID int64, status, code, msg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Like the database, a terminal run is never rewritten.
	if run := m.Runs[runID]; run != nil && run.Status == store.AIRunRunning {
		now := time.Now()
		run.Status, run.ErrorCode, run.ErrorMessage, run.FinishedAt = status, code, msg, &now
	}
	return nil
}

func (m *MemStore) FailRunningAIReportRuns(_ context.Context, code string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, run := range m.Runs {
		if run.Status == store.AIRunRunning {
			now := time.Now()
			run.Status, run.ErrorCode, run.FinishedAt = store.AIRunFailed, code, &now
			n++
		}
	}
	return n, nil
}

func (m *MemStore) GetAIReportRun(_ context.Context, runID int64) (*store.AIReportRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if run := m.Runs[runID]; run != nil {
		cp := *run
		return &cp, nil
	}
	return nil, nil
}

func (m *MemStore) LatestAIReportRun(_ context.Context, userID int64, week string) (*store.AIReportRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *store.AIReportRun
	for _, run := range m.Runs {
		if run.DashboardUserID != userID || run.Week != week {
			continue
		}
		if latest == nil || run.StartedAt.After(latest.StartedAt) || (run.StartedAt.Equal(latest.StartedAt) && run.ID > latest.ID) {
			latest = run
		}
	}
	if latest == nil {
		return nil, nil
	}
	cp := *latest
	return &cp, nil
}

func (m *MemStore) RunningAIReportRun(_ context.Context, userID int64) (*store.AIReportRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, run := range m.Runs {
		if run.DashboardUserID == userID && run.Status == store.AIRunRunning {
			cp := *run
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *MemStore) SetAIRunModel(_ context.Context, runID int64, model string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if run := m.Runs[runID]; run != nil {
		run.Model = model
	}
	return nil
}

func (m *MemStore) GetAISettings(_ context.Context, runtime string) (*store.AISettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Settings[runtime] == nil {
		return nil, nil
	}
	cp := *m.Settings[runtime]
	return &cp, nil
}

func (m *MemStore) SetAISettings(_ context.Context, runtime, model, reasoningEffort, baseURL, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Settings == nil {
		m.Settings = map[string]*store.AISettings{}
	}
	m.Settings[runtime] = &store.AISettings{Runtime: runtime, Model: model, ReasoningEffort: reasoningEffort, BaseURL: baseURL, UpdatedBy: actor, UpdatedAt: time.Now()}
	return nil
}

func (m *MemStore) GetAIRuntimeChoice(context.Context) (*store.AIRuntimeChoice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Choice == nil || m.Choice.Runtime == "" {
		return nil, nil
	}
	cp := *m.Choice
	return &cp, nil
}

func (m *MemStore) SetAIRuntimeChoice(_ context.Context, runtime, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Choice = &store.AIRuntimeChoice{Runtime: runtime, UpdatedBy: actor, UpdatedAt: time.Now()}
	return nil
}

func (m *MemStore) GetAIEnabledChoice(context.Context) (*store.AIEnabledChoice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Enabled == nil {
		return nil, nil
	}
	cp := *m.Enabled
	return &cp, nil
}

func (m *MemStore) SetAIEnabledChoice(_ context.Context, enabled *bool, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if enabled == nil {
		m.Enabled = nil
		return nil
	}
	m.Enabled = &store.AIEnabledChoice{Enabled: *enabled, UpdatedBy: actor, UpdatedAt: time.Now()}
	return nil
}

func (m *MemStore) GetAIProviderCredential(_ context.Context, provider string) (*store.AIProviderCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.Credentials[provider]; c != nil {
		cp := *c
		return &cp, nil
	}
	return nil, nil
}

func (m *MemStore) SetAIProviderCredential(_ context.Context, c store.AIProviderCredential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CredentialErr != nil {
		return m.CredentialErr
	}
	if m.Credentials == nil {
		m.Credentials = map[string]*store.AIProviderCredential{}
	}
	c.UpdatedAt = time.Now()
	m.Credentials[c.Provider] = &c
	return nil
}

func (m *MemStore) DeleteAIProviderCredential(_ context.Context, provider string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CredentialErr != nil {
		return m.CredentialErr
	}
	delete(m.Credentials, provider)
	return nil
}

func (m *MemStore) GetAIReport(_ context.Context, userID int64, week string) (*store.AIReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.Reports[memKey(userID, week)]; r != nil {
		cp := *r
		return &cp, nil
	}
	return nil, nil
}

func (m *MemStore) ListAIToolCalls(_ context.Context, runID int64, afterSeq int) ([]store.AIToolCall, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.AIToolCall
	for _, c := range m.ToolCalls[runID] {
		if c.Seq > afterSeq {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func (m *MemStore) GetAIConsent(_ context.Context, userID int64, runtimeKey string) (*store.AIConsent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.Consents[memKey(userID, runtimeKey)]; c != nil {
		cp := *c
		return &cp, nil
	}
	return nil, nil
}

func (m *MemStore) UpsertAIConsent(_ context.Context, userID int64, runtimeKey, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Consents[memKey(userID, runtimeKey)] = &store.AIConsent{
		DashboardUserID: userID, RuntimeKey: runtimeKey, DisclosureVersion: version, ConsentedAt: time.Now(),
	}
	return nil
}

func (m *MemStore) AIUsageSince(_ context.Context, since time.Time) (*store.AIUsageSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := &store.AIUsageSummary{}
	for _, run := range m.Runs {
		if run.StartedAt.Before(since) {
			continue
		}
		out.Runs++
		if run.Status == store.AIRunFailed {
			out.Failed++
		}
		if run.Usage.Reported {
			out.InputTokens += run.Usage.InputTokens
			out.OutputTokens += run.Usage.OutputTokens
		}
	}
	return out, nil
}

func (m *MemStore) scoped(sc store.AISegmentScope) []store.AISegment {
	var out []store.AISegment
	for _, seg := range m.Segments[memOwner(sc)] {
		if !seg.StartTs.Before(sc.Since) && seg.StartTs.Before(sc.Until) {
			out = append(out, seg)
		}
	}
	return out
}

func (m *MemStore) AIWeekSegments(_ context.Context, sc store.AISegmentScope, f store.AISegmentFilter) ([]store.AISegment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastFilter = f
	var out []store.AISegment
	for _, seg := range m.scoped(sc) {
		if (f.HadCompact != nil && seg.HadCompact != *f.HadCompact) ||
			seg.ToolFailCount < int64(f.MinToolFail) || seg.TypedTurnCount < int64(f.MinTypedTurns) ||
			(f.Agent != "" && seg.Agent != f.Agent) {
			continue
		}
		out = append(out, seg)
	}
	sort.SliceStable(out, func(i, j int) bool {
		switch f.OrderBy {
		case store.AISegmentOrderToolFailCount:
			return out[i].ToolFailCount > out[j].ToolFailCount
		case store.AISegmentOrderTypedTurnCount:
			return out[i].TypedTurnCount > out[j].TypedTurnCount
		default:
			return out[i].StartTs.Before(out[j].StartTs)
		}
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (m *MemStore) AISegmentsByIDs(_ context.Context, sc store.AISegmentScope, ids []int64) (map[int64]store.AISegment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int64]store.AISegment{}
	for _, seg := range m.scoped(sc) {
		for _, id := range ids {
			if seg.ID == id {
				out[id] = seg
			}
		}
	}
	return out, nil
}

func (m *MemStore) AISegmentConversation(_ context.Context, sc store.AISegmentScope, id int64, offset, limit int) ([]*store.SessionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	visible := false
	for _, seg := range m.scoped(sc) {
		visible = visible || seg.ID == id
	}
	recs := m.Conversations[id]
	if !visible || offset >= len(recs) {
		return nil, nil
	}
	recs = recs[offset:]
	if limit > 0 && len(recs) > limit {
		recs = recs[:limit]
	}
	return recs, nil
}

func (m *MemStore) AIWeekAggregate(_ context.Context, sc store.AISegmentScope) (*store.AIWeekAggregate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := &store.AIWeekAggregate{}
	sessions := map[string]bool{}
	for _, seg := range m.scoped(sc) {
		out.SegmentCount++
		sessions[seg.SessionID] = true
		out.TypedTurnCount += seg.TypedTurnCount
		out.ToolCallCount += seg.ToolCallCount
		if seg.ToolOutcomeEvidence {
			out.ToolOutcomeObservedCount += seg.ToolCallCount
		}
		out.ToolFailCount += seg.ToolFailCount
		out.CommandCount += seg.CommandCount
		if seg.HadCompact {
			out.CompactedSegmentCount++
		}
	}
	out.SessionCount = int64(len(sessions))
	return out, nil
}
