package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func openAPITimeRange(r *http.Request) (time.Time, time.Time, error) {
	now := time.Now()
	since := now.AddDate(0, 0, -7)
	until := now

	sinceParam, untilParam, err := optionalQueryTimeRange(r)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if sinceParam != nil {
		since = *sinceParam
	}
	if untilParam != nil {
		until = *untilParam
	}
	if !since.Before(until) {
		return time.Time{}, time.Time{}, &openAPIParameterError{message: "since must be earlier than until"}
	}
	return since, until, nil
}

type openAPIParameterError struct {
	message string
}

func (e *openAPIParameterError) Error() string { return e.message }

func openAPIUser(w http.ResponseWriter, r *http.Request) (*auth.DashboardUser, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return nil, false
	}
	return user, true
}

func writeOpenAPITimeRangeError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}

type openAPIAggregateScope struct {
	Since        time.Time
	Until        time.Time
	ProfileEmail string
	LoginEmail   string
	UserID       string
}

func (s *Server) openAPIAggregateScope(w http.ResponseWriter, r *http.Request) (openAPIAggregateScope, bool) {
	user, ok := openAPIUser(w, r)
	if !ok {
		return openAPIAggregateScope{}, false
	}
	since, until, err := openAPITimeRange(r)
	if err != nil {
		writeOpenAPITimeRangeError(w, err)
		return openAPIAggregateScope{}, false
	}
	scope := openAPIAggregateScope{
		Since: since, Until: until,
		ProfileEmail: r.URL.Query().Get("profile_email"),
		LoginEmail:   r.URL.Query().Get("login_email"),
		UserID:       r.URL.Query().Get("user_id"),
	}
	if user.Role != "admin" {
		scope.ProfileEmail, scope.LoginEmail, scope.UserID = s.openAPIOwnerScope(user)
	}
	return scope, true
}

type openAPIToolUsageDTO struct {
	ToolName     string `json:"tool_name"`
	UseCount     int64  `json:"use_count"`
	SuccessCount int64  `json:"success_count"`
	FailCount    int64  `json:"fail_count"`
}

const organizationInsightsMinimumUsers int64 = 5

type organizationInsightsReader interface {
	OrganizationInsights(ctx context.Context, since, until time.Time, minUsers int64) (*store.OrganizationInsights, error)
}

// handleOpenAPIOrganizationInsights deliberately has a separate aggregate
// contract. Admins cannot turn the regular user-scoped endpoints into a report
// by composing filters, and non-admin callers never receive aggregate rows.
func (s *Server) handleOpenAPIOrganizationInsights(w http.ResponseWriter, r *http.Request) {
	user, ok := openAPIUser(w, r)
	if !ok {
		return
	}
	if user.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	s.handleOrganizationInsights(w, r)
}

func (s *Server) handleDashboardOrganizationInsights(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || !requireAdmin(user, w) {
		return
	}
	s.handleOrganizationInsights(w, r)
}

func (s *Server) handleOrganizationInsights(w http.ResponseWriter, r *http.Request) {
	since, until, err := openAPITimeRange(r)
	if err != nil {
		writeOpenAPITimeRangeError(w, err)
		return
	}
	reader, ok := s.store.(organizationInsightsReader)
	if !ok {
		writeOpenAPIInternalError(w, fmt.Errorf("organization insights are not supported by this store"))
		return
	}
	insights, err := reader.OrganizationInsights(r.Context(), since, until, organizationInsightsMinimumUsers)
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	if insights == nil {
		writeOpenAPIInternalError(w, fmt.Errorf("organization insights store returned no result"))
		return
	}
	if !insights.Available {
		writeJSON(w, http.StatusOK, map[string]any{
			"available":     false,
			"minimum_users": insights.MinimumUsers,
		})
		return
	}
	response := map[string]any{
		"available":     true,
		"active_users":  insights.ActiveUsers,
		"minimum_users": insights.MinimumUsers,
		"models":        insights.Models,
		"tools":         insights.Tools,
	}
	if len(insights.Tasks) > 0 {
		response["tasks"] = insights.Tasks
	}
	if len(insights.Hours) > 0 {
		response["hours"] = insights.Hours
	}
	if len(insights.Projects) > 0 {
		response["projects"] = insights.Projects
	}
	if len(insights.Bottlenecks) > 0 {
		response["bottlenecks"] = insights.Bottlenecks
	}
	if insights.TypedTurnCount > 0 {
		response["typed_turn_count"] = insights.TypedTurnCount
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleOpenAPITools(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.openAPIAggregateScope(w, r)
	if !ok {
		return
	}
	rows, err := s.store.ToolUsage(r.Context(), scope.Since, scope.Until, scope.ProfileEmail, scope.LoginEmail, scope.UserID)
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	response := make([]openAPIToolUsageDTO, 0, len(rows))
	for _, row := range rows {
		response = append(response, openAPIToolUsageDTO{
			ToolName: row.ToolName, UseCount: row.UseCount, SuccessCount: row.SuccessCount, FailCount: row.FailCount,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

type openAPIToolTimeBucketDTO struct {
	Date         string `json:"date"`
	SuccessCount int64  `json:"success_count"`
	FailCount    int64  `json:"fail_count"`
}

type openAPIToolFailureDTO struct {
	Ts         time.Time `json:"ts"`
	SessionID  string    `json:"session_id"`
	Model      string    `json:"model"`
	DurationMs *int      `json:"duration_ms,omitempty"`
}

type openAPIToolDetailDTO struct {
	ToolName   string                     `json:"tool_name"`
	Timeseries []openAPIToolTimeBucketDTO `json:"timeseries"`
	Failures   []openAPIToolFailureDTO    `json:"failures"`
}

func (s *Server) handleOpenAPIToolDetail(w http.ResponseWriter, r *http.Request) {
	user, ok := openAPIUser(w, r)
	if !ok {
		return
	}
	toolName := r.PathValue("tool_name")
	if toolName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tool_name required"})
		return
	}
	since, until, err := openAPITimeRange(r)
	if err != nil {
		writeOpenAPITimeRangeError(w, err)
		return
	}
	profileEmail, loginEmail, userID := r.URL.Query().Get("profile_email"), r.URL.Query().Get("login_email"), r.URL.Query().Get("user_id")
	if user.Role != "admin" {
		profileEmail, loginEmail, userID = s.openAPIOwnerScope(user)
	}
	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "day"
	}
	// UTC buckets, deliberately. The dashboard passes the viewer's timezone
	// because it slices the returned string into a local-looking clock; a
	// machine-facing API has no viewer, and unambiguous UTC is the stable
	// contract for callers already consuming this endpoint.
	buckets, err := s.store.ToolTimeSeries(r.Context(), toolName, since, until, profileEmail, loginEmail, userID, granularity, "")
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	failures, err := s.store.ToolFailures(r.Context(), toolName, since, until, profileEmail, loginEmail, userID, queryInt(r, "failure_limit", 50))
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	response := openAPIToolDetailDTO{
		ToolName:   toolName,
		Timeseries: make([]openAPIToolTimeBucketDTO, 0, len(buckets)),
		Failures:   make([]openAPIToolFailureDTO, 0, len(failures)),
	}
	for _, bucket := range buckets {
		response.Timeseries = append(response.Timeseries, openAPIToolTimeBucketDTO{
			Date: bucket.Date, SuccessCount: bucket.SuccessCount, FailCount: bucket.FailCount,
		})
	}
	for _, failure := range failures {
		response.Failures = append(response.Failures, openAPIToolFailureDTO{
			Ts: failure.Ts, SessionID: failure.SessionID, Model: failure.Model, DurationMs: failure.DurationMs,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

type openAPIPluginUsageDTO struct {
	CommandName     string `json:"command_name"`
	Agent           string `json:"agent"`
	InvocationCount int64  `json:"invocation_count"`
	TotalTokens     int64  `json:"total_tokens"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
}

func (s *Server) handleOpenAPIPlugins(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.openAPIAggregateScope(w, r)
	if !ok {
		return
	}
	rows, err := s.store.PluginUsage(r.Context(), scope.Since, scope.Until, scope.ProfileEmail, scope.LoginEmail, scope.UserID, r.URL.Query().Get("agent"))
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	response := make([]openAPIPluginUsageDTO, 0, len(rows))
	for _, row := range rows {
		response = append(response, openAPIPluginUsageDTO{
			CommandName: row.CommandName, Agent: row.Agent, InvocationCount: row.InvocationCount,
			TotalTokens: row.TotalTokens, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

type openAPISkillUsageDTO struct {
	SkillName    string `json:"skill_name"`
	Agent        string `json:"agent"`
	InvokeType   string `json:"invoke_type"`
	SuccessCount int64  `json:"success_count"`
	FailCount    int64  `json:"fail_count"`
	TotalCount   int64  `json:"total_count"`
}

func (s *Server) handleOpenAPISkills(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.openAPIAggregateScope(w, r)
	if !ok {
		return
	}
	rows, err := s.store.SkillUsage(r.Context(), scope.Since, scope.Until, scope.ProfileEmail, scope.LoginEmail, scope.UserID, r.URL.Query().Get("agent"))
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	response := make([]openAPISkillUsageDTO, 0, len(rows))
	for _, row := range rows {
		response = append(response, openAPISkillUsageDTO{
			SkillName: row.SkillName, Agent: row.Agent, InvokeType: row.InvokeType,
			SuccessCount: row.SuccessCount, FailCount: row.FailCount, TotalCount: row.TotalCount,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

type openAPIMetricDTO struct {
	Ts          time.Time `json:"ts"`
	MetricName  string    `json:"metric_name"`
	SessionID   string    `json:"session_id,omitempty"`
	UserID      string    `json:"user_id,omitempty"`
	Model       string    `json:"model,omitempty"`
	ValueDouble *float64  `json:"value_double,omitempty"`
	ValueInt    *int64    `json:"value_int,omitempty"`
	Agent       string    `json:"agent,omitempty"`
}

func (s *Server) handleOpenAPIMetrics(w http.ResponseWriter, r *http.Request) {
	user, ok := openAPIUser(w, r)
	if !ok {
		return
	}
	since, until, err := optionalQueryTimeRange(r)
	if err != nil {
		writeOpenAPITimeRangeError(w, err)
		return
	}
	f := store.MetricFilter{
		MetricName: r.URL.Query().Get("metric_name"), UserID: r.URL.Query().Get("user_id"),
		ProfileEmail: r.URL.Query().Get("profile_email"), LoginEmail: r.URL.Query().Get("login_email"),
		UserTeam: r.URL.Query().Get("user_team"), Agent: r.URL.Query().Get("agent"), Model: r.URL.Query().Get("model"),
		Since: since, Until: until, Limit: queryInt(r, "limit", 100), Offset: queryInt(r, "offset", 0),
	}
	if user.Role != "admin" {
		f.ProfileEmail, f.LoginEmail, f.UserID = s.openAPIOwnerScope(user)
		f.UserTeam = ""
	}
	rows, err := s.store.ListMetrics(r.Context(), f)
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	response := make([]openAPIMetricDTO, 0, len(rows))
	for _, row := range rows {
		response = append(response, openAPIMetricDTO{
			Ts: row.Ts, MetricName: row.MetricName, SessionID: row.SessionID, UserID: row.UserID,
			Model: row.Model, ValueDouble: row.ValueDouble, ValueInt: row.ValueInt, Agent: row.Agent,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

type openAPIRuleDTO struct {
	ID            int64     `json:"id"`
	Agent         string    `json:"agent"`
	RuleKind      string    `json:"rule_kind"`
	RuleScope     string    `json:"rule_scope"`
	Title         string    `json:"title"`
	CurrentStatus string    `json:"current_status"`
	VersionCount  int64     `json:"version_count"`
	CommentCount  int64     `json:"comment_count"`
	DiscoveredAt  time.Time `json:"discovered_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func newOpenAPIRuleDTO(rule *store.ProjectRuleListItem) openAPIRuleDTO {
	return openAPIRuleDTO{
		ID: rule.ID, Agent: rule.Agent, RuleKind: rule.RuleKind, RuleScope: rule.RuleScope, Title: rule.Title,
		CurrentStatus: rule.CurrentStatus, VersionCount: rule.VersionCount, CommentCount: rule.CommentCount,
		DiscoveredAt: rule.DiscoveredAt, LastSeenAt: rule.LastSeenAt, UpdatedAt: rule.UpdatedAt,
	}
}

type openAPIRuleListDTO struct {
	Items []openAPIRuleDTO `json:"items"`
	Total int64            `json:"total"`
}

func (s *Server) handleOpenAPIRules(w http.ResponseWriter, r *http.Request) {
	if _, ok := openAPIUser(w, r); !ok {
		return
	}
	f := store.ProjectRuleFilter{
		Agent: r.URL.Query().Get("agent"), Status: r.URL.Query().Get("status"), Query: r.URL.Query().Get("query"),
		Limit: queryInt(r, "limit", 100), Offset: queryInt(r, "offset", 0),
	}
	if userID, profileEmail, restricted := s.projectRuleScope(r); restricted {
		f.UserID, f.ProfileEmail = userID, profileEmail
	}
	rows, err := s.store.ListProjectRules(r.Context(), f)
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	response := openAPIRuleListDTO{Items: []openAPIRuleDTO{}}
	if rows != nil {
		response.Total = rows.Total
		response.Items = make([]openAPIRuleDTO, 0, len(rows.Items))
		for _, row := range rows.Items {
			response.Items = append(response.Items, newOpenAPIRuleDTO(row))
		}
	}
	writeJSON(w, http.StatusOK, response)
}

type openAPIRuleVersionDTO struct {
	ID            int64     `json:"id"`
	VersionNumber int       `json:"version_number"`
	SizeBytes     int       `json:"size_bytes"`
	ChangeReason  string    `json:"change_reason"`
	AppliesTo     []string  `json:"applies_to"`
	DiscoveredAt  time.Time `json:"discovered_at"`
}

type openAPIRuleDetailDTO struct {
	Rule     openAPIRuleDTO          `json:"rule"`
	Versions []openAPIRuleVersionDTO `json:"versions"`
}

func (s *Server) handleOpenAPIRuleDetail(w http.ResponseWriter, r *http.Request) {
	user, ok := openAPIUser(w, r)
	if !ok {
		return
	}
	id, ok := pathInt64(w, r, "rule_id")
	if !ok {
		return
	}
	if user.Role != "admin" {
		profileEmail, _, userID := s.openAPIOwnerScope(user)
		visible, err := s.store.ProjectRuleVisibleTo(r.Context(), id, userID, profileEmail)
		if err != nil {
			writeOpenAPIInternalError(w, err)
			return
		}
		if !visible {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
	}
	detail, err := s.store.GetProjectRuleDetail(r.Context(), id, -1)
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	if detail == nil || detail.Rule == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	response := openAPIRuleDetailDTO{
		Rule:     newOpenAPIRuleDTO(detail.Rule),
		Versions: make([]openAPIRuleVersionDTO, 0, len(detail.Versions)),
	}
	for _, version := range detail.Versions {
		appliesTo := version.AppliesTo
		if appliesTo == nil {
			appliesTo = []string{}
		}
		response.Versions = append(response.Versions, openAPIRuleVersionDTO{
			ID: version.ID, VersionNumber: version.VersionNumber, SizeBytes: version.SizeBytes,
			ChangeReason: version.ChangeReason, AppliesTo: appliesTo, DiscoveredAt: version.DiscoveredAt,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

// openAPIProjectDTO carries what a caller needs to use project_hash as a filter and
// to recognise which project it belongs to.
//
// project_hash is NOT opaque despite the name: it is the session's working directory
// with the separators replaced (/Users/alice/myapp -> Users-alice-myapp, see
// projectHashFromCWD). It therefore carries an OS username and a directory layout.
// Callers who log or forward these values are forwarding paths, so say so rather than
// implying the value is safe to treat as a random key.
//
// git_remote_url and repository_id are left out: they add nothing to a filter key and
// a remote URL is the field most likely to carry a credential.
type openAPIProjectDTO struct {
	ProjectHash    string    `json:"project_hash"`
	ProjectName    string    `json:"project_name,omitempty"`
	RepositoryName string    `json:"repository_name,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type openAPIProjectListDTO struct {
	Items []openAPIProjectDTO `json:"items"`
	Total int                 `json:"total"`
}

// handleOpenAPIProjects lists the projects a caller may filter by.
//
// project_hash already filters sessions and events, but nothing told a caller which
// hashes exist -- a filter whose values cannot be discovered is one nobody outside
// this repository can use.
func (s *Server) handleOpenAPIProjects(w http.ResponseWriter, r *http.Request) {
	user, ok := openAPIUser(w, r)
	if !ok {
		return
	}
	profileEmail, _, userID := s.openAPIOwnerScope(user)
	rows, err := s.store.ListProjects(r.Context(), store.ProjectFilter{ProfileEmail: profileEmail, UserID: userID})
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	// projects is keyed by (agent, project_hash), so a directory worked on with more
	// than one agent arrives as several rows carrying the same hash. The caller asked
	// what the filter accepts; one accepted value is one entry. Keep the most recently
	// updated row of each, since that is the one whose name and repository are current.
	seen := make(map[string]int, len(rows))
	response := openAPIProjectListDTO{Items: make([]openAPIProjectDTO, 0, len(rows))}
	for _, p := range rows {
		dto := openAPIProjectDTO{
			ProjectHash: p.ProjectHash, ProjectName: p.ProjectName,
			RepositoryName: p.RepositoryName, UpdatedAt: p.UpdatedAt,
		}
		if i, ok := seen[p.ProjectHash]; ok {
			if dto.UpdatedAt.After(response.Items[i].UpdatedAt) {
				response.Items[i] = dto
			}
			continue
		}
		seen[p.ProjectHash] = len(response.Items)
		response.Items = append(response.Items, dto)
	}
	response.Total = len(response.Items)
	writeJSON(w, http.StatusOK, response)
}
