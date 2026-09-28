package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

// --- MockStore ---

type mockStore struct {
	countDashboardUsersFn            func(context.Context) (int, error)
	createInitialDashboardUserFn     func(context.Context, *store.DashboardUser) error
	quotaSamples                     []*store.QuotaSample
	excludedBilling                  []store.ExcludedBillingAccount
	removedBilling                   []string
	unpricedModels                   []store.UnpricedModel
	flatRateModels                   []store.FlatRateModel
	unmarkedFlatRate                 []string
	exclusionChangeErr               error
	usageRebuildPending              bool
	usageRebuildPendingErr           error
	quotaSampleFilter                store.QuotaSampleFilter
	sessionOwnerFn                   func(sessionID string) (string, string, error)
	deleteSessionFn                  func(sessionID, actor, reason string, blockProject, purgeProject bool) (*store.DeleteSessionResult, error)
	deleteProjectFn                  func(projectHashes []string, actor, reason string, blockProject bool) (*store.DeleteSessionResult, error)
	projectSessionCountFn            func(projectHash, excludeSessionID string) (int, error)
	deletionPolicyFn                 func() (*store.DeletionPolicy, error)
	setDeletionPolicyFn              func(allowOwnerDelete bool, actor string) error
	sweepDeletedFn                   func(limit int) (int64, error)
	pingFn                           func(ctx context.Context) error
	listEventsFn                     func(ctx context.Context, f store.EventFilter) ([]*store.OtelEvent, error)
	countEventsFn                    func(ctx context.Context, f store.EventFilter) (int64, error)
	listMetricsFn                    func(ctx context.Context, f store.MetricFilter) ([]*store.OtelMetric, error)
	costByUserFn                     func(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID string) ([]*store.CostSummary, error)
	costByTeamFn                     func(ctx context.Context, since, until time.Time, profileEmail, userID string) ([]*store.CostSummary, error)
	costByModelFn                    func(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID string) ([]*store.ModelStat, error)
	toolUsageFn                      func(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID string) ([]*store.ToolUsageSummary, error)
	toolTimeSeriesFn                 func(ctx context.Context, toolName string, since, until time.Time, profileEmail, loginEmail, userID, granularity string) ([]*store.ToolTimeBucket, error)
	toolFailuresFn                   func(ctx context.Context, toolName string, since, until time.Time, profileEmail, loginEmail, userID string, limit int) ([]*store.ToolFailure, error)
	pluginUsageFn                    func(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID, agent string) ([]*store.PluginUsageSummary, error)
	skillUsageFn                     func(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID string, agent string) ([]*store.SkillUsageSummary, error)
	organizationInsightsFn           func(ctx context.Context, since, until time.Time, minUsers int64) (*store.OrganizationInsights, error)
	backfillTaskTypesFn              func(ctx context.Context, limit int) (int, error)
	sessionListFn                    func(ctx context.Context, userEmail string, limit, offset int) ([]string, error)
	latestEventTimeFn                func(ctx context.Context) (*time.Time, error)
	insertSessionRecordsFn           func(ctx context.Context, records []*store.SessionRecord) error
	reenrichSessionRecordsFn         func(ctx context.Context, records []*store.SessionRecord) (int, error)
	listSessionRecordsFn             func(ctx context.Context, filter store.SessionRecordFilter) ([]*store.SessionRecord, error)
	latestActivityTsFn               func(ctx context.Context, f store.EventFilter) (time.Time, bool, error)
	listSessionOverviewsFn           func(ctx context.Context, f store.SessionOverviewFilter) ([]*store.SessionOverview, error)
	countSessionOverviewsFn          func(ctx context.Context, f store.SessionOverviewFilter) (int, error)
	usageAggregatesFn                func(ctx context.Context, f store.SessionOverviewFilter) (*store.UsageAggregate, error)
	dailyStatsFn                     func(ctx context.Context, f store.EventFilter) ([]*store.DailyStat, error)
	ingestProjectRulesFn             func(ctx context.Context, req *store.ProjectRuleIngestRequest) (*store.ProjectRuleIngestResponse, error)
	listProjectRulesFn               func(ctx context.Context, f store.ProjectRuleFilter) (*store.ProjectRuleListResponse, error)
	getProjectRuleDetailFn           func(ctx context.Context, id, contentVersionID int64) (*store.ProjectRuleDetail, error)
	projectRuleVisibleToFn           func(ctx context.Context, ruleID int64, userID, profileEmail string) (bool, error)
	createProjectRuleCommentFn       func(ctx context.Context, ruleID int64, req *store.CreateProjectRuleCommentRequest, authorProfileEmail, authorUserID string) (*store.ProjectRuleComment, error)
	updateProjectRuleChangeReasonFn  func(ctx context.Context, ruleID, versionID int64, changeReason, authorProfileEmail, authorUserID string) (*store.ProjectRuleVersion, error)
	getDashboardUserByAPITokenFn     func(ctx context.Context, token string) (*store.DashboardUser, error)
	getDashboardUserByOpenAPITokenFn func(ctx context.Context, token string) (*store.DashboardUser, error)
	listProjectsFn                   func(ctx context.Context, f store.ProjectFilter) ([]*store.Project, error)
	upsertProjectFn                  func(ctx context.Context, agent, projectHash, projectName, gitRemoteURL, repositoryID, repositoryName string) error
	upsertProjectWithMetadataFn      func(ctx context.Context, agent, projectHash, projectName string, metadata store.ProjectIdentityMetadata, lastSessionAt time.Time) error
	getDashboardUserByIDFn           func(ctx context.Context, id int64) (*store.DashboardUser, error)
	setDashboardUserAPITokenFn       func(ctx context.Context, id int64, token string) error
	listDashboardUserAPITokensFn     func(ctx context.Context, userID int64) ([]*store.DashboardAPIToken, error)
	createDashboardUserAPITokenFn    func(ctx context.Context, userID int64, name, token, createdVia string, expiresAt *time.Time) (*store.DashboardAPIToken, error)
	rotateDashboardUserAPITokenFn    func(ctx context.Context, userID, tokenID int64, token string) (*store.DashboardAPIToken, error)
	setDashboardUserTokenActiveFn    func(ctx context.Context, userID, tokenID int64, active bool) (*store.DashboardAPIToken, error)
	setDashboardUserExpirationFn     func(ctx context.Context, userID, tokenID int64, expiresAt *time.Time) (*store.DashboardAPIToken, error)
	deleteDashboardUserAPITokenFn    func(ctx context.Context, userID, tokenID int64) (bool, error)
	deleteCLIReadTokenFn             func(ctx context.Context, userID int64, secret string) (bool, error)
	deleteAllDashboardUserTokensFn   func(ctx context.Context, userID int64) error
	retentionPreviewAxisFn           func(ctx context.Context, axis string, days int) ([]*store.RetentionPreview, error)
	reconcileRetentionFn             func(ctx context.Context, cfg store.RetentionConfig) error
	upsertRetentionSettingFn         func(ctx context.Context, axis string, days int, updatedBy string) error
	previewInferLoginEmailFn         func(ctx context.Context, since time.Time) (*store.LoginEmailInferencePreview, error)
	previewCodexInferredRevertFn     func(ctx context.Context) (*store.CodexInferredRevertPreview, error)
	previewCodexAccountFillFn        func(ctx context.Context, since time.Time) (*store.CodexAccountFillPreview, error)
	previewCodexQuotaAttributionFn   func(ctx context.Context, since time.Time) (*store.CodexQuotaAttributionPreview, error)
}

func (m *mockStore) Migrate(ctx context.Context) error                         { return nil }
func (m *mockStore) Close() error                                              { return nil }
func (m *mockStore) InsertEvent(ctx context.Context, e *store.OtelEvent) error { return nil }
func (m *mockStore) InsertEvents(ctx context.Context, events []*store.OtelEvent) error {
	return nil
}
func (m *mockStore) InsertMetric(ctx context.Context, met *store.OtelMetric) error { return nil }
func (m *mockStore) InsertMetrics(ctx context.Context, metrics []*store.OtelMetric) error {
	return nil
}

func (m *mockStore) Ping(ctx context.Context) error {
	if m.pingFn != nil {
		return m.pingFn(ctx)
	}
	return nil
}

func (m *mockStore) BackfillTaskTypes(ctx context.Context, limit int) (int, error) {
	if m.backfillTaskTypesFn != nil {
		return m.backfillTaskTypesFn(ctx, limit)
	}
	return 0, nil
}

func (m *mockStore) ListEvents(ctx context.Context, f store.EventFilter) ([]*store.OtelEvent, error) {
	if m.listEventsFn != nil {
		return m.listEventsFn(ctx, f)
	}
	return nil, nil
}

func (m *mockStore) CountEvents(ctx context.Context, f store.EventFilter) (int64, error) {
	if m.countEventsFn != nil {
		return m.countEventsFn(ctx, f)
	}
	return 0, nil
}

func (m *mockStore) ListMetrics(ctx context.Context, f store.MetricFilter) ([]*store.OtelMetric, error) {
	if m.listMetricsFn != nil {
		return m.listMetricsFn(ctx, f)
	}
	return nil, nil
}

func (m *mockStore) CostByUser(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID string) ([]*store.CostSummary, error) {
	if m.costByUserFn != nil {
		return m.costByUserFn(ctx, since, until, profileEmail, loginEmail, userID)
	}
	return nil, nil
}

func (m *mockStore) CostByTeam(ctx context.Context, since, until time.Time, profileEmail string, userID string) ([]*store.CostSummary, error) {
	if m.costByTeamFn != nil {
		return m.costByTeamFn(ctx, since, until, profileEmail, userID)
	}
	return nil, nil
}

func (m *mockStore) CostByModel(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID string) ([]*store.ModelStat, error) {
	if m.costByModelFn != nil {
		return m.costByModelFn(ctx, since, until, profileEmail, loginEmail, userID)
	}
	return nil, nil
}

func (m *mockStore) ToolUsage(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID string) ([]*store.ToolUsageSummary, error) {
	if m.toolUsageFn != nil {
		return m.toolUsageFn(ctx, since, until, profileEmail, loginEmail, userID)
	}
	return nil, nil
}

func (m *mockStore) ToolTimeSeries(ctx context.Context, toolName string, since, until time.Time, profileEmail, loginEmail, userID, granularity, tz string) ([]*store.ToolTimeBucket, error) {
	if m.toolTimeSeriesFn != nil {
		return m.toolTimeSeriesFn(ctx, toolName, since, until, profileEmail, loginEmail, userID, granularity)
	}
	return nil, nil
}

func (m *mockStore) ToolFailures(ctx context.Context, toolName string, since, until time.Time, profileEmail, loginEmail, userID string, limit int) ([]*store.ToolFailure, error) {
	if m.toolFailuresFn != nil {
		return m.toolFailuresFn(ctx, toolName, since, until, profileEmail, loginEmail, userID, limit)
	}
	return nil, nil
}

func (m *mockStore) SkillUsage(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID string, agent string) ([]*store.SkillUsageSummary, error) {
	if m.skillUsageFn != nil {
		return m.skillUsageFn(ctx, since, until, profileEmail, loginEmail, userID, agent)
	}
	return nil, nil
}

func (m *mockStore) OrganizationInsights(ctx context.Context, since, until time.Time, minUsers int64) (*store.OrganizationInsights, error) {
	if m.organizationInsightsFn != nil {
		return m.organizationInsightsFn(ctx, since, until, minUsers)
	}
	return nil, nil
}

func (m *mockStore) ListLoginAccounts(ctx context.Context, userID string) ([]string, error) {
	return nil, nil
}
func (m *mockStore) ListSessionSummaries(ctx context.Context, profileEmail string, userID string, limit int) ([]*store.SessionSummary, error) {
	return nil, nil
}

func (m *mockStore) SessionList(ctx context.Context, userEmail string, limit, offset int) ([]string, error) {
	if m.sessionListFn != nil {
		return m.sessionListFn(ctx, userEmail, limit, offset)
	}
	return nil, nil
}

func (m *mockStore) LatestEventTime(ctx context.Context) (*time.Time, error) {
	if m.latestEventTimeFn != nil {
		return m.latestEventTimeFn(ctx)
	}
	return nil, nil
}

func (m *mockStore) InsertSessionRecords(ctx context.Context, records []*store.SessionRecord) error {
	if m.insertSessionRecordsFn != nil {
		return m.insertSessionRecordsFn(ctx, records)
	}
	return nil
}

func (m *mockStore) ReenrichSessionRecords(ctx context.Context, records []*store.SessionRecord) (int, error) {
	if m.reenrichSessionRecordsFn != nil {
		return m.reenrichSessionRecordsFn(ctx, records)
	}
	return 0, nil
}

func (m *mockStore) ListSessionRecords(ctx context.Context, filter store.SessionRecordFilter) ([]*store.SessionRecord, error) {
	if m.listSessionRecordsFn != nil {
		return m.listSessionRecordsFn(ctx, filter)
	}
	return nil, nil
}

func (m *mockStore) ListSessionLineage(ctx context.Context, filter store.SessionRecordFilter) ([]*store.SessionRecord, error) {
	if m.listSessionRecordsFn != nil {
		return m.listSessionRecordsFn(ctx, filter)
	}
	return nil, nil
}

func (m *mockStore) DailyStats(ctx context.Context, f store.EventFilter) ([]*store.DailyStat, error) {
	if m.dailyStatsFn != nil {
		return m.dailyStatsFn(ctx, f)
	}
	return nil, nil
}

func (m *mockStore) TimeSeriesStats(ctx context.Context, f store.EventFilter, granularity string) ([]*store.DailyStat, error) {
	return nil, nil
}

func (m *mockStore) DeleteUserData(ctx context.Context, profileEmail string, userID string) error {
	return nil
}

func (m *mockStore) MergeUsers(ctx context.Context, fromProfileEmail string, fromUserID string, toProfileEmail string) error {
	return nil
}

func (m *mockStore) UpsertAlias(ctx context.Context, fromEmail, toEmail string) error { return nil }
func (m *mockStore) LoadAliases(ctx context.Context) (map[string]string, error) {
	return nil, nil
}

func (m *mockStore) TimeSeriesStatsByModel(ctx context.Context, f store.EventFilter, granularity string) ([]*store.ModelDailyStat, error) {
	return nil, nil
}

func (m *mockStore) CoverageGapStats(ctx context.Context, f store.CoverageGapFilter) (*store.CoverageGap, error) {
	return &store.CoverageGap{}, nil
}

func (m *mockStore) LatestActivityTs(ctx context.Context, f store.EventFilter) (time.Time, bool, error) {
	if m.latestActivityTsFn != nil {
		return m.latestActivityTsFn(ctx, f)
	}
	return time.Time{}, false, nil
}

func (m *mockStore) TimeSeriesStatsByUser(ctx context.Context, f store.EventFilter, granularity string) ([]*store.UserDailyStat, error) {
	return nil, nil
}

func (m *mockStore) ListSessionOverviews(ctx context.Context, f store.SessionOverviewFilter) ([]*store.SessionOverview, error) {
	if m.listSessionOverviewsFn != nil {
		return m.listSessionOverviewsFn(ctx, f)
	}
	return nil, nil
}

func (m *mockStore) CountSessionOverviews(ctx context.Context, f store.SessionOverviewFilter) (int, error) {
	if m.countSessionOverviewsFn != nil {
		return m.countSessionOverviewsFn(ctx, f)
	}
	return 0, nil
}

func (m *mockStore) UsageAggregates(ctx context.Context, f store.SessionOverviewFilter) (*store.UsageAggregate, error) {
	if m.usageAggregatesFn != nil {
		return m.usageAggregatesFn(ctx, f)
	}
	return &store.UsageAggregate{}, nil
}

func (m *mockStore) CleanupOrphanSessionRecords(ctx context.Context) (int64, error) {
	return 0, nil
}

func (m *mockStore) ListExcludedAccounts(ctx context.Context) ([]store.ExcludedAccount, error) {
	return nil, nil
}

func (m *mockStore) UsageRollupRebuildPending(ctx context.Context) (bool, error) {
	return m.usageRebuildPending, m.usageRebuildPendingErr
}

func (m *mockStore) ExcludeAccount(ctx context.Context, loginEmail, reason, actor string) (int64, error) {
	return 0, m.exclusionChangeErr
}

func (m *mockStore) RemoveExcludedAccount(ctx context.Context, loginEmail string) error {
	return m.exclusionChangeErr
}

// The billing-id counterpart. excludedBilling records what the handler asked for
// so a test can assert the key reached the store in its normalized form.
func (m *mockStore) ListExcludedBillingAccounts(ctx context.Context) ([]store.ExcludedBillingAccount, error) {
	return m.excludedBilling, nil
}

func (m *mockStore) ExcludeBillingAccount(ctx context.Context, provider, accountID, reason, actor string) (int64, int64, error) {
	m.excludedBilling = append(m.excludedBilling, store.ExcludedBillingAccount{
		BillingProvider: provider, AccountID: accountID, Reason: reason, CreatedBy: actor,
	})
	return int64(len(m.excludedBilling)), 0, m.exclusionChangeErr
}

func (m *mockStore) RemoveExcludedBillingAccount(ctx context.Context, provider, accountID string) error {
	m.removedBilling = append(m.removedBilling, provider+"/"+accountID)
	return m.exclusionChangeErr
}

// Unpriced models (#441). flatRateModels records what the handler asked for so a
// test can assert the key reached the store in its normalized form.
func (m *mockStore) ListUnpricedModels(ctx context.Context) ([]store.UnpricedModel, error) {
	return m.unpricedModels, nil
}

func (m *mockStore) ListFlatRateModels(ctx context.Context) ([]store.FlatRateModel, error) {
	return m.flatRateModels, nil
}

func (m *mockStore) MarkFlatRateModel(ctx context.Context, agent, model, reason, actor string) error {
	m.flatRateModels = append(m.flatRateModels, store.FlatRateModel{Agent: agent, Model: model, Reason: reason, CreatedBy: actor})
	return nil
}

func (m *mockStore) UnmarkFlatRateModel(ctx context.Context, agent, model string) error {
	m.unmarkedFlatRate = append(m.unmarkedFlatRate, agent+"/"+model)
	return nil
}

func (m *mockStore) ListObservedBillingAccounts(ctx context.Context, profileEmail, userID, actor string) ([]store.ObservedBillingAccount, error) {
	return nil, nil
}

func (m *mockStore) BillingAccountUsedByOthers(ctx context.Context, provider, accountID, profileEmail, userID string) (bool, error) {
	return false, nil
}

func (m *mockStore) SelfExcludeBillingAccount(ctx context.Context, provider, accountID, reason, actor string) error {
	return nil
}

func (m *mockStore) RemoveSelfExcludedBillingAccount(ctx context.Context, provider, accountID, actor string) (bool, error) {
	return false, nil
}

// Session deletion. The hooks let a test drive ownership and the delete result
// without a database; the zero values mean "unknown session, nothing deleted",
// which is what a handler test that never sets them should see.
func (m *mockStore) SessionOwner(ctx context.Context, sessionID string) (string, string, error) {
	if m.sessionOwnerFn != nil {
		return m.sessionOwnerFn(sessionID)
	}
	return "", "", nil
}

func (m *mockStore) DeleteSession(ctx context.Context, sessionID, actor, reason string, blockProject, purgeProject bool) (*store.DeleteSessionResult, error) {
	if m.deleteSessionFn != nil {
		return m.deleteSessionFn(sessionID, actor, reason, blockProject, purgeProject)
	}
	return &store.DeleteSessionResult{}, nil
}

func (m *mockStore) DeleteProject(ctx context.Context, projectHashes []string, actor, reason string, blockProject bool) (*store.DeleteSessionResult, error) {
	if m.deleteProjectFn != nil {
		return m.deleteProjectFn(projectHashes, actor, reason, blockProject)
	}
	return &store.DeleteSessionResult{}, nil
}

func (m *mockStore) ProjectSessionCount(ctx context.Context, projectHash, excludeSessionID string) (int, error) {
	if m.projectSessionCountFn != nil {
		return m.projectSessionCountFn(projectHash, excludeSessionID)
	}
	return 0, nil
}

func (m *mockStore) ListBlockedProjects(ctx context.Context) ([]store.BlockedProject, error) {
	return nil, nil
}

func (m *mockStore) BlockProject(ctx context.Context, projectHash, projectName, actor, reason string) error {
	return nil
}

func (m *mockStore) UnblockProject(ctx context.Context, projectHash string) error {
	return nil
}

func (m *mockStore) LoadIngestBlocklist(ctx context.Context) (*store.IngestBlocklist, error) {
	return &store.IngestBlocklist{
		DeletedSessions: map[string]bool{},
		BlockedProjects: map[string]bool{},
	}, nil
}

func (m *mockStore) PurgeBlockedProjects(ctx context.Context) (int64, error) {
	return 0, nil
}

func (m *mockStore) SweepDeletedSessions(ctx context.Context, limit int) (int64, error) {
	if m.sweepDeletedFn != nil {
		return m.sweepDeletedFn(limit)
	}
	return 0, nil
}

func (m *mockStore) GetDeletionPolicy(ctx context.Context) (*store.DeletionPolicy, error) {
	if m.deletionPolicyFn != nil {
		return m.deletionPolicyFn()
	}
	return &store.DeletionPolicy{AllowOwnerDelete: true}, nil
}

func (m *mockStore) SetDeletionPolicy(ctx context.Context, allowOwnerDelete bool, actor string) error {
	if m.setDeletionPolicyFn != nil {
		return m.setDeletionPolicyFn(allowOwnerDelete, actor)
	}
	return nil
}

func (m *mockStore) UpsertProject(ctx context.Context, agent, projectHash, projectName, gitRemoteURL, repositoryID, repositoryName, repoSubpath string, lastSessionAt time.Time) error {
	// The hook takes the fields tests assert on; repoSubpath and lastSessionAt are
	// carried by the signature but no test inspects them through this hook yet.
	if m.upsertProjectFn != nil {
		return m.upsertProjectFn(ctx, agent, projectHash, projectName, gitRemoteURL, repositoryID, repositoryName)
	}
	return nil
}

func (m *mockStore) UpsertProjectWithMetadata(ctx context.Context, agent, projectHash, projectName string, metadata store.ProjectIdentityMetadata, lastSessionAt time.Time) error {
	if m.upsertProjectWithMetadataFn != nil {
		return m.upsertProjectWithMetadataFn(ctx, agent, projectHash, projectName, metadata, lastSessionAt)
	}
	return m.UpsertProject(ctx, agent, projectHash, projectName, metadata.GitRemoteURL, metadata.RepositoryID, metadata.RepositoryName, metadata.RepoSubpath, lastSessionAt)
}
func (m *mockStore) ListProjects(ctx context.Context, f store.ProjectFilter) ([]*store.Project, error) {
	if m.listProjectsFn != nil {
		return m.listProjectsFn(ctx, f)
	}
	return nil, nil
}

func (m *mockStore) IngestProjectRules(ctx context.Context, req *store.ProjectRuleIngestRequest) (*store.ProjectRuleIngestResponse, error) {
	if m.ingestProjectRulesFn != nil {
		return m.ingestProjectRulesFn(ctx, req)
	}
	return &store.ProjectRuleIngestResponse{}, nil
}

func (m *mockStore) ListProjectRules(ctx context.Context, f store.ProjectRuleFilter) (*store.ProjectRuleListResponse, error) {
	if m.listProjectRulesFn != nil {
		return m.listProjectRulesFn(ctx, f)
	}
	return &store.ProjectRuleListResponse{}, nil
}

func (m *mockStore) GetProjectRuleDetail(ctx context.Context, id, contentVersionID int64) (*store.ProjectRuleDetail, error) {
	if m.getProjectRuleDetailFn != nil {
		return m.getProjectRuleDetailFn(ctx, id, contentVersionID)
	}
	return nil, nil
}

func (m *mockStore) ProjectRuleVisibleTo(ctx context.Context, ruleID int64, userID, profileEmail string) (bool, error) {
	if m.projectRuleVisibleToFn != nil {
		return m.projectRuleVisibleToFn(ctx, ruleID, userID, profileEmail)
	}
	return true, nil
}

func (m *mockStore) CreateProjectRuleComment(ctx context.Context, ruleID int64, req *store.CreateProjectRuleCommentRequest, authorProfileEmail, authorUserID string) (*store.ProjectRuleComment, error) {
	if m.createProjectRuleCommentFn != nil {
		return m.createProjectRuleCommentFn(ctx, ruleID, req, authorProfileEmail, authorUserID)
	}
	return &store.ProjectRuleComment{}, nil
}

func (m *mockStore) UpdateProjectRuleChangeReason(ctx context.Context, ruleID, versionID int64, changeReason, authorProfileEmail, authorUserID string) (*store.ProjectRuleVersion, error) {
	if m.updateProjectRuleChangeReasonFn != nil {
		return m.updateProjectRuleChangeReasonFn(ctx, ruleID, versionID, changeReason, authorProfileEmail, authorUserID)
	}
	return &store.ProjectRuleVersion{}, nil
}

func (m *mockStore) ListUsers(ctx context.Context) ([]*store.UserInfo, error) {
	return nil, nil
}
func (m *mockStore) ListOtelUserIDs(ctx context.Context) ([]string, error) { return nil, nil }
func (m *mockStore) AccountSwitchStats(ctx context.Context, since time.Time) ([]*store.AccountSwitchStat, error) {
	return nil, nil
}
func (m *mockStore) PreviewBackfillSessionRecordLoginEmail(ctx context.Context, since time.Time) (*store.BackfillPreview, error) {
	return nil, nil
}
func (m *mockStore) PreviewInferSessionRecordLoginEmail(ctx context.Context, since time.Time) (*store.LoginEmailInferencePreview, error) {
	if m.previewInferLoginEmailFn != nil {
		return m.previewInferLoginEmailFn(ctx, since)
	}
	return nil, nil
}
func (m *mockStore) PreviewCodexInferredRevert(ctx context.Context) (*store.CodexInferredRevertPreview, error) {
	if m.previewCodexInferredRevertFn != nil {
		return m.previewCodexInferredRevertFn(ctx)
	}
	return nil, nil
}
func (m *mockStore) PreviewCodexAccountFill(ctx context.Context, since time.Time) (*store.CodexAccountFillPreview, error) {
	if m.previewCodexAccountFillFn != nil {
		return m.previewCodexAccountFillFn(ctx, since)
	}
	return nil, nil
}
func (m *mockStore) PreviewCodexQuotaAttribution(ctx context.Context, since time.Time) (*store.CodexQuotaAttributionPreview, error) {
	if m.previewCodexQuotaAttributionFn != nil {
		return m.previewCodexQuotaAttributionFn(ctx, since)
	}
	return nil, nil
}
func (m *mockStore) SessionAccountSegments(ctx context.Context, sessionID string) ([]*store.SessionAccountSegment, error) {
	return nil, nil
}
func (m *mockStore) CheckUserID(ctx context.Context, userID string) (bool, []string, error) {
	return false, nil, nil
}
func (m *mockStore) CountDashboardUsers(ctx context.Context) (int, error) {
	if m.countDashboardUsersFn != nil {
		return m.countDashboardUsersFn(ctx)
	}
	return 0, nil
}
func (m *mockStore) CreateInitialDashboardUser(ctx context.Context, u *store.DashboardUser) error {
	if m.createInitialDashboardUserFn != nil {
		return m.createInitialDashboardUserFn(ctx, u)
	}
	return nil
}
func (m *mockStore) CreateDashboardUser(ctx context.Context, u *store.DashboardUser) (*store.DashboardUser, error) {
	return u, nil
}
func (m *mockStore) GetDashboardUserByEmail(ctx context.Context, email string) (*store.DashboardUser, error) {
	return nil, errors.New("not found")
}
func (m *mockStore) GetDashboardUserByID(ctx context.Context, id int64) (*store.DashboardUser, error) {
	if m.getDashboardUserByIDFn != nil {
		return m.getDashboardUserByIDFn(ctx, id)
	}
	return nil, errors.New("not found")
}
func (m *mockStore) GetDashboardUserByCctraceUserID(ctx context.Context, cctraceUserID string) (*store.DashboardUser, error) {
	return nil, errors.New("not found")
}
func (m *mockStore) ListDashboardUsers(ctx context.Context) ([]*store.DashboardUser, error) {
	return nil, nil
}
func (m *mockStore) UpdateDashboardUser(ctx context.Context, id int64, p store.UpdateDashboardUserParams) error {
	return nil
}
func (m *mockStore) UpdateDashboardUserPassword(ctx context.Context, id int64, passwordHash string, mustChange bool) error {
	return nil
}
func (m *mockStore) SetPrivacy(ctx context.Context, userID, profileEmail, scopeType, scopeValue string) error {
	return nil
}
func (m *mockStore) RemovePrivacy(ctx context.Context, userID, profileEmail, scopeType, scopeValue string) error {
	return nil
}
func (m *mockStore) ListPrivacySettings(ctx context.Context, userID, profileEmail string) ([]*store.PrivacySetting, error) {
	return nil, nil
}
func (m *mockStore) IsPrivate(ctx context.Context, profileEmail, projectHash, sessionID string) (bool, error) {
	return false, nil
}
func (m *mockStore) PrivateSessionIDs(ctx context.Context, sessions []*store.SessionOverview) (map[string]bool, error) {
	return nil, nil
}
func (m *mockStore) ResolveUserEmails(ctx context.Context, email string) ([]string, error) {
	return []string{email}, nil
}
func (m *mockStore) GetDashboardUserByApiToken(ctx context.Context, token string) (*store.DashboardUser, error) {
	if m.getDashboardUserByAPITokenFn != nil {
		return m.getDashboardUserByAPITokenFn(ctx, token)
	}
	return nil, errors.New("not found")
}
func (m *mockStore) GetDashboardUserByOpenAPIToken(ctx context.Context, token string) (*store.DashboardUser, error) {
	if m.getDashboardUserByOpenAPITokenFn != nil {
		return m.getDashboardUserByOpenAPITokenFn(ctx, token)
	}
	// Tests written before the read API existed set only the ingestion hook; treat
	// that as a token a person created, which is what they meant.
	if m.getDashboardUserByAPITokenFn != nil {
		return m.getDashboardUserByAPITokenFn(ctx, token)
	}
	return nil, errors.New("not found")
}
func (m *mockStore) SetDashboardUserApiToken(ctx context.Context, id int64, token string) error {
	if m.setDashboardUserAPITokenFn != nil {
		return m.setDashboardUserAPITokenFn(ctx, id, token)
	}
	return nil
}
func (m *mockStore) ListDashboardUserAPITokens(ctx context.Context, userID int64) ([]*store.DashboardAPIToken, error) {
	if m.listDashboardUserAPITokensFn != nil {
		return m.listDashboardUserAPITokensFn(ctx, userID)
	}
	return nil, nil
}
func (m *mockStore) CreateDashboardUserAPIToken(ctx context.Context, userID int64, name, token, createdVia string, expiresAt *time.Time) (*store.DashboardAPIToken, error) {
	if m.createDashboardUserAPITokenFn != nil {
		return m.createDashboardUserAPITokenFn(ctx, userID, name, token, createdVia, expiresAt)
	}
	return &store.DashboardAPIToken{UserID: userID, Name: name, CreatedVia: createdVia, IsActive: true, ExpiresAt: expiresAt}, nil
}
func (m *mockStore) RotateDashboardUserAPIToken(ctx context.Context, userID, tokenID int64, token string) (*store.DashboardAPIToken, error) {
	if m.rotateDashboardUserAPITokenFn != nil {
		return m.rotateDashboardUserAPITokenFn(ctx, userID, tokenID, token)
	}
	return &store.DashboardAPIToken{ID: tokenID, UserID: userID}, nil
}
func (m *mockStore) SetDashboardUserAPITokenActive(ctx context.Context, userID, tokenID int64, active bool) (*store.DashboardAPIToken, error) {
	if m.setDashboardUserTokenActiveFn != nil {
		return m.setDashboardUserTokenActiveFn(ctx, userID, tokenID, active)
	}
	return &store.DashboardAPIToken{ID: tokenID, UserID: userID, IsActive: active}, nil
}
func (m *mockStore) SetDashboardUserAPITokenExpiration(ctx context.Context, userID, tokenID int64, expiresAt *time.Time) (*store.DashboardAPIToken, error) {
	if m.setDashboardUserExpirationFn != nil {
		return m.setDashboardUserExpirationFn(ctx, userID, tokenID, expiresAt)
	}
	return &store.DashboardAPIToken{ID: tokenID, UserID: userID, ExpiresAt: expiresAt}, nil
}
func (m *mockStore) DeleteDashboardUserAPIToken(ctx context.Context, userID, tokenID int64) (bool, error) {
	if m.deleteDashboardUserAPITokenFn != nil {
		return m.deleteDashboardUserAPITokenFn(ctx, userID, tokenID)
	}
	return true, nil
}
func (m *mockStore) DeleteCLIReadToken(ctx context.Context, userID int64, secret string) (bool, error) {
	if m.deleteCLIReadTokenFn != nil {
		return m.deleteCLIReadTokenFn(ctx, userID, secret)
	}
	return false, nil
}
func (m *mockStore) DeleteAllDashboardUserAPITokens(ctx context.Context, userID int64) error {
	if m.deleteAllDashboardUserTokensFn != nil {
		return m.deleteAllDashboardUserTokensFn(ctx, userID)
	}
	return nil
}
func (m *mockStore) UpsertQuotaSnapshot(ctx context.Context, s *store.QuotaSnapshot) error {
	return nil
}
func (m *mockStore) GetQuotaSnapshot(ctx context.Context, profileEmail string) (*store.QuotaSnapshot, error) {
	return nil, nil
}
func (m *mockStore) ListQuotaSnapshots(ctx context.Context) ([]*store.QuotaSnapshot, error) {
	return nil, nil
}
func (m *mockStore) InsertQuotaSamples(ctx context.Context, samples []*store.QuotaSample) (int, error) {
	m.quotaSamples = append(m.quotaSamples, samples...)
	return len(samples), nil
}
func (m *mockStore) ListQuotaSamples(ctx context.Context, f store.QuotaSampleFilter) ([]*store.QuotaSample, error) {
	m.quotaSampleFilter = f
	return m.quotaSamples, nil
}
func (m *mockStore) UpsertClientVersion(ctx context.Context, update store.ClientVersionUpdate) error {
	return nil
}
func (m *mockStore) ListClientVersions(ctx context.Context) ([]*store.ClientVersionRecord, error) {
	return nil, nil
}
func (m *mockStore) ReconcileRetention(ctx context.Context, cfg store.RetentionConfig) error {
	if m.reconcileRetentionFn != nil {
		return m.reconcileRetentionFn(ctx, cfg)
	}
	return nil
}
func (m *mockStore) RetentionInfo(ctx context.Context) (*store.StorageReport, error) {
	return &store.StorageReport{Tables: []*store.TableRetention{
		{Table: "otel_events", RowsApprox: 100, SizeBytes: 4096},
	}}, nil
}
func (m *mockStore) RetentionPreviewAxis(ctx context.Context, axis string, days int) ([]*store.RetentionPreview, error) {
	if m.retentionPreviewAxisFn != nil {
		return m.retentionPreviewAxisFn(ctx, axis, days)
	}
	return nil, nil
}
func (m *mockStore) UpsertRetentionSetting(ctx context.Context, axis string, days int, updatedBy string) error {
	if m.upsertRetentionSettingFn != nil {
		return m.upsertRetentionSettingFn(ctx, axis, days, updatedBy)
	}
	return nil
}
func (m *mockStore) ListRetentionSettings(ctx context.Context) ([]*store.RetentionSetting, error) {
	return nil, nil
}
func (m *mockStore) EffectiveRetentionConfig(ctx context.Context, env store.RetentionConfig) (store.RetentionConfig, error) {
	return env, nil
}
func (m *mockStore) PluginUsage(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID, agent string) ([]*store.PluginUsageSummary, error) {
	if m.pluginUsageFn != nil {
		return m.pluginUsageFn(ctx, since, until, profileEmail, loginEmail, userID, agent)
	}
	return nil, nil
}

// --- Tests ---

func newTestServer(s store.Store, jwtMgr *auth.JWTManager, authMW ...AuthMiddleware) *Server {
	passThrough := func(next http.Handler) http.Handler { return next }
	return newServer(s, jwtMgr, passThrough, authMW...)
}

func TestHealth_OK(t *testing.T) {
	srv := newTestServer(&mockStore{}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected status=ok, got %q", body["status"])
	}
}

func TestVersion_IncludesSessionRecordVersionSince(t *testing.T) {
	srv := newTestServer(&mockStore{}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/version")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["session_record_version_since"] != store.CctraceVersionSince {
		t.Fatalf("session_record_version_since = %q, want %q", body["session_record_version_since"], store.CctraceVersionSince)
	}
}

func TestHealth_Unhealthy(t *testing.T) {
	m := &mockStore{
		pingFn: func(ctx context.Context) error { return errors.New("db down") },
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
}

func TestListEvents(t *testing.T) {
	now := time.Now()
	m := &mockStore{
		listEventsFn: func(ctx context.Context, f store.EventFilter) ([]*store.OtelEvent, error) {
			return []*store.OtelEvent{
				{Ts: now, EventName: "user_prompt", SessionID: "s1"},
				{Ts: now, EventName: "api_request", SessionID: "s1"},
			}, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var events []*store.OtelEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
}

func TestListEvents_WithFilters(t *testing.T) {
	var captured store.EventFilter
	m := &mockStore{
		listEventsFn: func(ctx context.Context, f store.EventFilter) ([]*store.OtelEvent, error) {
			captured = f
			return nil, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	sinceStr := "2025-01-01T00:00:00Z"
	untilStr := "2025-06-01T00:00:00Z"
	url := ts.URL + "/api/events?session_id=abc&profile_email=test@x.com&since=" + sinceStr + "&until=" + untilStr

	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if captured.SessionID != "abc" {
		t.Fatalf("expected session_id=abc, got %q", captured.SessionID)
	}
	if captured.ProfileEmail != "test@x.com" {
		t.Fatalf("expected profile_email=test@x.com, got %q", captured.ProfileEmail)
	}
	if captured.Since == nil {
		t.Fatal("expected Since to be set")
	}
	expectedSince, _ := time.Parse(time.RFC3339, sinceStr)
	if !captured.Since.Equal(expectedSince) {
		t.Fatalf("expected Since=%v, got %v", expectedSince, *captured.Since)
	}
	if captured.Until == nil {
		t.Fatal("expected Until to be set")
	}
	expectedUntil, _ := time.Parse(time.RFC3339, untilStr)
	if !captured.Until.Equal(expectedUntil) {
		t.Fatalf("expected Until=%v, got %v", expectedUntil, *captured.Until)
	}
}

func TestCountEvents(t *testing.T) {
	m := &mockStore{
		countEventsFn: func(ctx context.Context, f store.EventFilter) (int64, error) {
			return 42, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/events/count")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]int64
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["count"] != 42 {
		t.Fatalf("expected count=42, got %d", body["count"])
	}
}

func TestListMetrics(t *testing.T) {
	now := time.Now()
	val := 3.14
	m := &mockStore{
		listMetricsFn: func(ctx context.Context, f store.MetricFilter) ([]*store.OtelMetric, error) {
			return []*store.OtelMetric{
				{Ts: now, MetricName: "token_count", ValueDouble: &val},
			}, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var metrics []*store.OtelMetric
	if err := json.NewDecoder(resp.Body).Decode(&metrics); err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(metrics))
	}
}

// A model= query param must reach the store filter. Dropping it silently
// returns every model's rows while the caller believes it asked for one.
func TestListMetrics_ModelFilter(t *testing.T) {
	var got store.MetricFilter
	m := &mockStore{
		listMetricsFn: func(ctx context.Context, f store.MetricFilter) ([]*store.OtelMetric, error) {
			got = f
			return nil, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/metrics?model=gpt-5-codex")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got.Model != "gpt-5-codex" {
		t.Errorf("filter.Model = %q, want gpt-5-codex", got.Model)
	}
}

func TestCostByUser(t *testing.T) {
	m := &mockStore{
		costByUserFn: func(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID string) ([]*store.CostSummary, error) {
			return []*store.CostSummary{
				{ProfileEmail: "a@b.com", TotalCost: 1.5, RequestCount: 10},
			}, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/cost/by-user")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var summaries []*store.CostSummary
	if err := json.NewDecoder(resp.Body).Decode(&summaries); err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ProfileEmail != "a@b.com" {
		t.Fatalf("unexpected cost summaries: %+v", summaries)
	}
}

func TestCostByTeam(t *testing.T) {
	m := &mockStore{
		costByTeamFn: func(ctx context.Context, since, until time.Time, profileEmail, userID string) ([]*store.CostSummary, error) {
			return []*store.CostSummary{
				{UserTeam: "eng", TotalCost: 100.0, RequestCount: 500},
			}, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/cost/by-team")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var summaries []*store.CostSummary
	if err := json.NewDecoder(resp.Body).Decode(&summaries); err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].UserTeam != "eng" {
		t.Fatalf("unexpected cost summaries: %+v", summaries)
	}
}

func TestToolUsage(t *testing.T) {
	m := &mockStore{
		toolUsageFn: func(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID string) ([]*store.ToolUsageSummary, error) {
			return []*store.ToolUsageSummary{
				{ToolName: "Read", UseCount: 50, SuccessCount: 48, FailCount: 2},
			}, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/tools")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var tools []*store.ToolUsageSummary
	if err := json.NewDecoder(resp.Body).Decode(&tools); err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].ToolName != "Read" {
		t.Fatalf("unexpected tool usage: %+v", tools)
	}
}

func TestSkillUsage(t *testing.T) {
	var capturedAgent string
	m := &mockStore{
		skillUsageFn: func(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID string, agent string) ([]*store.SkillUsageSummary, error) {
			capturedAgent = agent
			return []*store.SkillUsageSummary{
				{SkillName: "imagegen", Agent: "codex", InvokeType: "implicit", SuccessCount: 3, TotalCount: 3},
			}, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/skills")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if capturedAgent != "codex" {
		t.Fatalf("expected default agent codex, got %q", capturedAgent)
	}

	var skills []*store.SkillUsageSummary
	if err := json.NewDecoder(resp.Body).Decode(&skills); err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].SkillName != "imagegen" {
		t.Fatalf("unexpected skill usage: %+v", skills)
	}
}

func TestSessionList(t *testing.T) {
	m := &mockStore{
		listSessionRecordsFn: func(ctx context.Context, filter store.SessionRecordFilter) ([]*store.SessionRecord, error) {
			return []*store.SessionRecord{
				{SessionID: "sess-1", RecordType: "assistant"},
				{SessionID: "sess-2", RecordType: "user"},
				{SessionID: "sess-3", RecordType: "assistant"},
			}, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var records []*store.SessionRecord
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}
}

func TestCORS_OriginPolicy(t *testing.T) {
	for _, method := range []string{http.MethodOptions, http.MethodGet, http.MethodPost} {
		for _, origin := range []string{"", "http://localhost:3000", "http://localhost:3001", "null", "*"} {
			t.Run(method+"/"+origin, func(t *testing.T) {
				srv := newTestServer(&mockStore{}, nil)
				req := httptest.NewRequest(method, "http://localhost:8080/api/health", strings.NewReader(`{}`))
				req.Header.Set("Origin", origin)
				req.Header.Set("Content-Type", "text/plain")
				req.Header.Set("Access-Control-Request-Method", "POST")
				req.Header.Set("Access-Control-Request-Headers", "X-Requested-With")
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, req)
				for _, key := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers"} {
					if got := rec.Header().Get(key); got != "" {
						t.Errorf("default policy: %s = %q", key, got)
					}
				}
			})
		}
	}
}

func TestListEvents_StoreError(t *testing.T) {
	m := &mockStore{
		listEventsFn: func(ctx context.Context, f store.EventFilter) ([]*store.OtelEvent, error) {
			return nil, errors.New("db error")
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestCountEvents_StoreError(t *testing.T) {
	m := &mockStore{
		countEventsFn: func(ctx context.Context, f store.EventFilter) (int64, error) {
			return 0, errors.New("db error")
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/events/count")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestListMetrics_StoreError(t *testing.T) {
	m := &mockStore{
		listMetricsFn: func(ctx context.Context, f store.MetricFilter) ([]*store.OtelMetric, error) {
			return nil, errors.New("db error")
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestCostByUser_StoreError(t *testing.T) {
	m := &mockStore{
		costByUserFn: func(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID string) ([]*store.CostSummary, error) {
			return nil, errors.New("db error")
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/cost/by-user")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestCostByTeam_StoreError(t *testing.T) {
	m := &mockStore{
		costByTeamFn: func(ctx context.Context, since, until time.Time, profileEmail, userID string) ([]*store.CostSummary, error) {
			return nil, errors.New("db error")
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/cost/by-team")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestToolUsage_StoreError(t *testing.T) {
	m := &mockStore{
		toolUsageFn: func(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID string) ([]*store.ToolUsageSummary, error) {
			return nil, errors.New("db error")
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/tools")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestSessionList_StoreError(t *testing.T) {
	m := &mockStore{
		listSessionRecordsFn: func(ctx context.Context, filter store.SessionRecordFilter) ([]*store.SessionRecord, error) {
			return nil, errors.New("db error")
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestHandler_WithAuthMiddleware(t *testing.T) {
	invokedPaths := map[string]bool{}
	authMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			invokedPaths[r.Method+" "+r.URL.Path] = true
			next.ServeHTTP(w, r)
		})
	}

	srv := newTestServer(&mockStore{
		insertSessionRecordsFn: func(ctx context.Context, records []*store.SessionRecord) error {
			return nil
		},
	}, nil, authMW)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/sync", "application/json", strings.NewReader(`{"records":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if !invokedPaths["POST /api/sync"] {
		t.Fatal("expected auth middleware to be invoked on /api/sync")
	}

	resp, err = http.Get(ts.URL + "/api/sync/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !invokedPaths["GET /api/sync/capabilities"] {
		t.Fatal("expected auth middleware to be invoked on /api/sync/capabilities")
	}
}

func TestSyncCapabilities_ReturnsReenrichSupport(t *testing.T) {
	srv := newTestServer(&mockStore{}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sync/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got struct {
		Reenrich bool `json:"reenrich"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.Reenrich {
		t.Fatalf("reenrich capability = false, want true")
	}
}

func TestListEvents_EventNameFilter(t *testing.T) {
	var captured store.EventFilter
	m := &mockStore{
		listEventsFn: func(ctx context.Context, f store.EventFilter) ([]*store.OtelEvent, error) {
			captured = f
			return nil, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/events?event_name=api_request")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if captured.EventName != "api_request" {
		t.Fatalf("expected EventName=api_request, got %q", captured.EventName)
	}
}

func TestHealth_IncludesLatestEventAt(t *testing.T) {
	fixedTime := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	m := &mockStore{
		latestEventTimeFn: func(ctx context.Context) (*time.Time, error) {
			return &fixedTime, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["latest_event_at"]; !ok {
		t.Fatalf("expected latest_event_at key in response, got %+v", body)
	}
}

func TestHealth_WithHealthExtra(t *testing.T) {
	m := &mockStore{}
	srv := newTestServer(m, nil).WithHealthExtra(func(ctx context.Context) (map[string]interface{}, error) {
		return map[string]interface{}{"log_queue_depth": int64(5)}, nil
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	depth, ok := body["log_queue_depth"]
	if !ok {
		t.Fatalf("expected log_queue_depth key in response, got %+v", body)
	}
	// JSON numbers decode as float64
	if depth.(float64) != 5 {
		t.Fatalf("expected log_queue_depth=5, got %v", depth)
	}
}

func TestHealth_ExtraProbeFailureIsUnhealthy(t *testing.T) {
	m := &mockStore{}
	srv := newTestServer(m, nil).WithHealthExtra(func(context.Context) (map[string]interface{}, error) {
		return nil, errors.New("queue depth unavailable")
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 rather than false queue depths", rec.Code)
	}
}

func TestSessionList_WithParams(t *testing.T) {
	var capturedFilter store.SessionRecordFilter
	m := &mockStore{
		listSessionRecordsFn: func(ctx context.Context, filter store.SessionRecordFilter) ([]*store.SessionRecord, error) {
			capturedFilter = filter
			return nil, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions?profile_email=x@y.com&session_id=abc&limit=5")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if capturedFilter.ProfileEmail != "x@y.com" {
		t.Fatalf("expected UserEmail=x@y.com, got %q", capturedFilter.ProfileEmail)
	}
	if capturedFilter.SessionID != "abc" {
		t.Fatalf("expected SessionID=abc, got %q", capturedFilter.SessionID)
	}
	if capturedFilter.Limit != 5 {
		t.Fatalf("expected Limit=5, got %d", capturedFilter.Limit)
	}
}

func TestSessionOverview_WithTimeRange(t *testing.T) {
	var capturedFilter store.SessionOverviewFilter
	m := &mockStore{
		listSessionOverviewsFn: func(ctx context.Context, filter store.SessionOverviewFilter) ([]*store.SessionOverview, error) {
			capturedFilter = filter
			return nil, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const since = "2026-08-09T01:00:00Z"
	const until = "2026-08-09T02:00:00Z"
	resp, err := http.Get(ts.URL + "/api/session-overview?profile_email=x@y.com&since=" + since + "&until=" + until)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if capturedFilter.Since == nil || capturedFilter.Since.Format(time.RFC3339) != since {
		t.Fatalf("expected Since=%s, got %v", since, capturedFilter.Since)
	}
	if capturedFilter.Until == nil || capturedFilter.Until.Format(time.RFC3339) != until {
		t.Fatalf("expected Until=%s, got %v", until, capturedFilter.Until)
	}
}

func TestSessionOverview_RejectsInvalidTimeRange(t *testing.T) {
	m := &mockStore{}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/session-overview?since=2026-08-10")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestDailyStats(t *testing.T) {
	m := &mockStore{
		dailyStatsFn: func(ctx context.Context, f store.EventFilter) ([]*store.DailyStat, error) {
			return []*store.DailyStat{
				{Date: "2025-01-02", CostUSD: 1.5, InputTokens: 100, OutputTokens: 200, EventCount: 5},
				{Date: "2025-01-01", CostUSD: 0.5, InputTokens: 50, OutputTokens: 80, EventCount: 2},
			}, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/stats/daily")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var stats []*store.DailyStat
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 stats, got %d", len(stats))
	}
	if stats[0].Date != "2025-01-02" {
		t.Fatalf("expected first date=2025-01-02, got %q", stats[0].Date)
	}
	if stats[0].CostUSD != 1.5 {
		t.Fatalf("expected cost_usd=1.5, got %f", stats[0].CostUSD)
	}
}

func TestDailyStats_StoreError(t *testing.T) {
	m := &mockStore{
		dailyStatsFn: func(ctx context.Context, f store.EventFilter) ([]*store.DailyStat, error) {
			return nil, errors.New("db error")
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/stats/daily")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestLatestActivity_PassesModelFilter(t *testing.T) {
	var captured store.EventFilter
	latest := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m := &mockStore{
		latestActivityTsFn: func(ctx context.Context, f store.EventFilter) (time.Time, bool, error) {
			captured = f
			return latest, true, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/stats/latest-activity?profile_email=profile@example.com&login_email=account@example.com&project_hash=project-a&user_id=user-1&agent=codex&model_category=codex&model=gpt-5.5")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if captured.Model != "gpt-5.5" {
		t.Fatalf("expected model filter gpt-5.5, got %q", captured.Model)
	}
}

func TestCountSessionOverview_PassesAssembledFilter(t *testing.T) {
	var captured store.SessionOverviewFilter
	m := &mockStore{
		countSessionOverviewsFn: func(ctx context.Context, f store.SessionOverviewFilter) (int, error) {
			captured = f
			return 3, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/session-overview/count?login_email=account@example.com&assembled=1&source=interactive&project_hashes=-project-a,-project-b&agent=claude")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !captured.FoldLineage {
		t.Fatal("expected assembled count to fold lineage")
	}
	if captured.LoginEmail != "account@example.com" {
		t.Fatalf("expected login_email account@example.com, got %q", captured.LoginEmail)
	}
	if captured.Source != "interactive" {
		t.Fatalf("expected source interactive, got %q", captured.Source)
	}
	if len(captured.ProjectHashes) != 2 || captured.ProjectHashes[0] != "-project-a" || captured.ProjectHashes[1] != "-project-b" {
		t.Fatalf("unexpected project hashes: %#v", captured.ProjectHashes)
	}
	if captured.Agent != "claude" {
		t.Fatalf("expected agent claude, got %q", captured.Agent)
	}
}

func TestCostByModel(t *testing.T) {
	m := &mockStore{
		costByModelFn: func(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID string) ([]*store.ModelStat, error) {
			return []*store.ModelStat{
				{Model: "claude-sonnet-4-6", ProfileEmail: "a@b.com", TotalCost: 2.5, InputTokens: 100, OutputTokens: 200, RequestCount: 5},
				{Model: "claude-haiku-4-5", ProfileEmail: "b@c.com", TotalCost: 0.5, InputTokens: 50, OutputTokens: 80, RequestCount: 2},
			}, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/cost/by-model")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var stats []*store.ModelStat
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 records, got %d", len(stats))
	}
	if stats[0].Model != "claude-sonnet-4-6" {
		t.Fatalf("expected first model=claude-sonnet-4-6, got %q", stats[0].Model)
	}
}

func TestCostByModel_StoreError(t *testing.T) {
	m := &mockStore{
		costByModelFn: func(ctx context.Context, since, until time.Time, profileEmail, loginEmail, userID string) ([]*store.ModelStat, error) {
			return nil, errors.New("db error")
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/cost/by-model")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestSync_InsertsRecords(t *testing.T) {
	var inserted []*store.SessionRecord
	m := &mockStore{
		insertSessionRecordsFn: func(ctx context.Context, records []*store.SessionRecord) error {
			inserted = records
			return nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"user_email":"a@b.com","agent":"codex","project_hash":"proj","repository_id":"github.com/org/repo","repository_name":"repo","repo_subpath":"web/","commit_sha":"abc123","branch":"main","records":[{"record_type":"assistant","model":"gpt-5.5"}]}`
	resp, err := http.Post(ts.URL+"/api/sync", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(inserted) != 1 || inserted[0].Model != "gpt-5.5" {
		t.Errorf("unexpected inserted records: %+v", inserted)
	}
	if inserted[0].Agent != "codex" || inserted[0].BillingProvider != "openai" {
		t.Errorf("agent context was not applied: %+v", inserted[0])
	}
	if inserted[0].RepositoryID != "github.com/org/repo" || inserted[0].RepositoryName != "repo" || inserted[0].RepoSubpath != "web/" || inserted[0].CommitSHA != "abc123" || inserted[0].Branch != "main" {
		t.Errorf("repository context was not applied: %+v", inserted[0])
	}
}

func TestSync_PropagatesCctraceVersionHeaderToRecords(t *testing.T) {
	var inserted []*store.SessionRecord
	m := &mockStore{
		insertSessionRecordsFn: func(ctx context.Context, records []*store.SessionRecord) error {
			inserted = records
			return nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"user_email":"a@b.com","agent":"claude","records":[{"record_type":"assistant"}]}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/sync", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cctrace-Version", "v0.7.8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(inserted) != 1 || inserted[0].CctraceVersion != "v0.7.8" {
		t.Fatalf("cctrace_version was not propagated: %+v", inserted)
	}
}

func TestSync_MissingCctraceVersionHeaderKeepsRecordsEmpty(t *testing.T) {
	var inserted []*store.SessionRecord
	m := &mockStore{
		insertSessionRecordsFn: func(ctx context.Context, records []*store.SessionRecord) error {
			inserted = records
			return nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"user_email":"a@b.com","agent":"claude","records":[{"record_type":"assistant"}]}`
	resp, err := http.Post(ts.URL+"/api/sync", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(inserted) != 1 || inserted[0].CctraceVersion != "" {
		t.Fatalf("cctrace_version should stay empty without header: %+v", inserted)
	}
}

func TestSync_ReenrichRecordsDoesNotUseNormalInsertPath(t *testing.T) {
	var reenriched []*store.SessionRecord
	m := &mockStore{
		insertSessionRecordsFn: func(ctx context.Context, records []*store.SessionRecord) error {
			return errors.New("normal insert path called for reenrich")
		},
		reenrichSessionRecordsFn: func(ctx context.Context, records []*store.SessionRecord) (int, error) {
			reenriched = records
			return 1, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"profile_email":"a@b.com","user_id":"u1","agent":"claude","reenrich":true,"records":[{"ts":"2026-07-06T10:00:00Z","session_id":"legacy-s1","record_type":"assistant","uuid":"turn-1","source_file":"session.jsonl","parent_uuid":"root-1"}]}`
	resp, err := http.Post(ts.URL+"/api/sync", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var syncResp struct {
		Inserted int `json:"inserted"`
		Updated  int `json:"updated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&syncResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if syncResp.Inserted != 0 || syncResp.Updated != 1 {
		t.Fatalf("sync response = %+v, want inserted=0 updated=1", syncResp)
	}
	if len(reenriched) != 1 {
		t.Fatalf("reenriched records = %d, want 1", len(reenriched))
	}
	got := reenriched[0]
	if got.ProfileEmail != "a@b.com" || got.UserID != "u1" || got.Agent != "claude" || got.BillingProvider != "anthropic" {
		t.Fatalf("envelope context was not applied to reenrich record: %+v", got)
	}
	if got.UUID != "turn-1" || got.SourceFile != "session.jsonl" || got.ParentUUID != "root-1" {
		t.Fatalf("enrichment fields were not preserved: %+v", got)
	}
}

func TestSync_ReenrichEmptyRecordsIncludesUpdatedCount(t *testing.T) {
	m := &mockStore{
		insertSessionRecordsFn: func(ctx context.Context, records []*store.SessionRecord) error {
			return errors.New("normal insert path called for empty reenrich")
		},
		reenrichSessionRecordsFn: func(ctx context.Context, records []*store.SessionRecord) (int, error) {
			t.Fatal("empty reenrich must not call store")
			return 0, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"profile_email":"a@b.com","user_id":"u1","agent":"claude","reenrich":true,"records":[]}`
	resp, err := http.Post(ts.URL+"/api/sync", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got struct {
		Inserted int `json:"inserted"`
		Updated  int `json:"updated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Inserted != 0 || got.Updated != 0 {
		t.Fatalf("sync response = %+v, want inserted=0 updated=0", got)
	}
}

func TestProjectRulesIngest(t *testing.T) {
	var captured *store.ProjectRuleIngestRequest
	m := &mockStore{
		ingestProjectRulesFn: func(ctx context.Context, req *store.ProjectRuleIngestRequest) (*store.ProjectRuleIngestResponse, error) {
			captured = req
			return &store.ProjectRuleIngestResponse{InsertedRules: 1, InsertedVersions: 1}, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"profile_email":"a@b.com","user_id":"u1","agent":"codex","repository_id":"github.com/org/repo","rules":[{"rule_path":"AGENTS.md","rule_kind":"agents","status":"active","content":"# Rules"}]}`
	resp, err := http.Post(ts.URL+"/api/project-rules", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if captured == nil || captured.Agent != "codex" || len(captured.Rules) != 1 || captured.Rules[0].RulePath != "AGENTS.md" {
		t.Fatalf("unexpected captured request: %+v", captured)
	}
}

func TestListProjectRules(t *testing.T) {
	var captured store.ProjectRuleFilter
	m := &mockStore{
		listProjectRulesFn: func(ctx context.Context, f store.ProjectRuleFilter) (*store.ProjectRuleListResponse, error) {
			captured = f
			return &store.ProjectRuleListResponse{
				Items: []*store.ProjectRuleListItem{{
					ProjectRule: store.ProjectRule{
						ID:            1,
						Agent:         "codex",
						RepositoryKey: "github.com/org/repo",
						RulePath:      "AGENTS.md",
						CurrentStatus: "active",
					},
					VersionCount: 1,
				}},
				Total: 1,
			}, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/project-rules?agent=codex&status=active&query=AGENTS")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if captured.Agent != "codex" || captured.Status != "active" || captured.Query != "AGENTS" {
		t.Fatalf("unexpected filter: %+v", captured)
	}
	var out store.ProjectRuleListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 1 || len(out.Items) != 1 || out.Items[0].RulePath != "AGENTS.md" {
		t.Fatalf("unexpected response: %+v", out)
	}
}

func TestProjectRuleDetailAndMutations(t *testing.T) {
	m := &mockStore{
		getProjectRuleDetailFn: func(ctx context.Context, id, contentVersionID int64) (*store.ProjectRuleDetail, error) {
			if id != 7 || contentVersionID != 11 {
				t.Fatalf("detail selection = (%d, %d), want (7, 11)", id, contentVersionID)
			}
			return &store.ProjectRuleDetail{
				Rule:     &store.ProjectRuleListItem{ProjectRule: store.ProjectRule{ID: 7, RulePath: "CLAUDE.md", CurrentStatus: "active"}},
				Versions: []*store.ProjectRuleVersion{{ID: 11, RuleID: 7, VersionNumber: 1, Content: "# Claude"}},
				Comments: []*store.ProjectRuleComment{},
			}, nil
		},
		createProjectRuleCommentFn: func(ctx context.Context, ruleID int64, req *store.CreateProjectRuleCommentRequest, authorProfileEmail, authorUserID string) (*store.ProjectRuleComment, error) {
			if ruleID != 7 || req.Body != "note" {
				t.Fatalf("unexpected comment request: ruleID=%d req=%+v", ruleID, req)
			}
			return &store.ProjectRuleComment{ID: 1, RuleID: ruleID, Body: req.Body, CommentType: "comment"}, nil
		},
		updateProjectRuleChangeReasonFn: func(ctx context.Context, ruleID, versionID int64, changeReason, authorProfileEmail, authorUserID string) (*store.ProjectRuleVersion, error) {
			if ruleID != 7 || versionID != 11 || changeReason != "because" {
				t.Fatalf("unexpected change reason request: ruleID=%d versionID=%d reason=%q", ruleID, versionID, changeReason)
			}
			return &store.ProjectRuleVersion{ID: versionID, RuleID: ruleID, ChangeReason: changeReason}, nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/project-rules/7?selected_version_id=11")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail expected 200, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	commentReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/project-rules/7/comments", strings.NewReader(`{"body":"note"}`))
	if err != nil {
		t.Fatal(err)
	}
	commentReq.Header.Set("Content-Type", "application/json")
	commentReq.Header.Set("Origin", ts.URL)
	commentReq.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err = http.DefaultClient.Do(commentReq)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("comment expected 200, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/project-rules/7/versions/11/change-reason", strings.NewReader(`{"change_reason":"because"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", ts.URL)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("change reason expected 200, got %d", resp.StatusCode)
	}
}

func TestCORS_ExplicitOrigins(t *testing.T) {
	for _, method := range []string{http.MethodOptions, http.MethodGet, http.MethodPost} {
		for _, origin := range []string{"https://dashboard.example.com", "https://evil.example.com", "https://dashboard.example.com:444", "null", "*", ""} {
			t.Run(method+"/"+origin, func(t *testing.T) {
				srv := newTestServer(&mockStore{}, nil).WithAllowedOrigins([]string{" https://dashboard.example.com ", "*", "", "null"})
				req := httptest.NewRequest(method, "https://api.example.com/api/health", strings.NewReader(`{}`))
				req.Header.Set("Origin", origin)
				req.Header.Set("Content-Type", "text/plain")
				req.Header.Set("Access-Control-Request-Method", "POST")
				req.Header.Set("Access-Control-Request-Headers", "X-Requested-With")
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, req)
				want := ""
				if origin == "https://dashboard.example.com" {
					want = origin
				}
				if got := rec.Header().Get("Access-Control-Allow-Origin"); got != want {
					t.Errorf("origin = %q, want %q", got, want)
				}
				if want != "" && rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
					t.Error("missing credentials grant")
				}
				if want == "" && rec.Header().Get("Access-Control-Allow-Headers") != "" {
					t.Error("untrusted preflight granted")
				}
				if rec.Header().Get("Vary") != "Origin" {
					t.Error("missing Vary: Origin")
				}
			})
		}
	}
}
