package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/sessionview"
	"cctrace/internal/store"
)

// openAPIMiddleware authenticates external API requests with a per-user API
// token. The ingestion API key is deliberately not accepted here because it
// carries no user identity and therefore cannot establish a read scope.
func (s *Server) openAPIMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if token == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		user, err := s.store.GetDashboardUserByOpenAPIToken(r.Context(), token)
		if refusal := readTokenRefusal(err); refusal != nil {
			writeTokenRefusal(w, refusal)
			return
		}
		if err != nil || user == nil || !user.IsActive {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized", "code": "unauthorized"})
			return
		}
		if user.Role != "admin" && user.Role != "user" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}

		owner := &auth.DashboardUser{
			ID:                 user.ID,
			Email:              user.Email,
			Role:               user.Role,
			Name:               user.Name,
			CctraceUserID:      user.CctraceUserID,
			MustChangePassword: user.MustChangePassword,
		}
		next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), owner)))
	})
}

// readTokenRefusal turns the read-token rule's refusals into what the caller is
// told, or nil when err is not one of them.
func readTokenRefusal(err error) *auth.TokenRefusal {
	switch {
	case errors.Is(err, store.ErrTokenNotOpenAPI):
		// The token works -- for uploading. Saying only "unauthorized" would send
		// someone hunting for a typo in a credential that is doing its job.
		return &auth.TokenRefusal{
			Status:  http.StatusUnauthorized,
			Code:    "ingestion_token",
			Message: "this token is issued for ingestion; create a token under Settings > API Access Tokens to read the API",
		}
	case errors.Is(err, store.ErrCLIReadTokenAdmin):
		return &auth.TokenRefusal{
			Status:  http.StatusForbidden,
			Code:    "admin_read_token_forbidden",
			Message: "CLI read tokens are not accepted for administrators; create a token under Settings > API Access Tokens",
		}
	}
	return nil
}

func writeTokenRefusal(w http.ResponseWriter, r *auth.TokenRefusal) {
	writeJSON(w, r.Status, map[string]string{"error": r.Message, "code": r.Code})
}

func (s *Server) applyOpenAPISessionScope(f *store.SessionRecordFilter, user *auth.DashboardUser) {
	if user.Role == "admin" {
		return
	}
	f.ProfileEmail, _, f.UserID = s.resolveUserAccessParams(user)
}

func (s *Server) openAPIOwnerScope(user *auth.DashboardUser) (profileEmail, loginEmail, userID string) {
	if user.Role == "admin" {
		return "", "", ""
	}
	return s.resolveUserAccessParams(user)
}

type openAPISessionOverviewDTO struct {
	SessionID    string    `json:"session_id"`
	UserID       string    `json:"user_id,omitempty"`
	Model        string    `json:"model,omitempty"`
	Agent        string    `json:"agent,omitempty"`
	ProjectName  string    `json:"project_name,omitempty"`
	StartTime    time.Time `json:"start_time"`
	EndTime      time.Time `json:"end_time"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
	EventCount   int64     `json:"event_count"`
}

func newOpenAPISessionOverviewDTO(overview *store.SessionOverview) openAPISessionOverviewDTO {
	return openAPISessionOverviewDTO{
		SessionID: overview.SessionID, UserID: overview.UserID, Model: overview.Model, Agent: overview.Agent,
		ProjectName: overview.ProjectName,
		StartTime:   overview.StartTime, EndTime: overview.EndTime, InputTokens: overview.InputTokens,
		OutputTokens: overview.OutputTokens, CostUSD: overview.CostUSD, EventCount: overview.EventCount,
	}
}

type openAPISessionRecordDTO struct {
	Ts                time.Time `json:"ts"`
	SessionID         string    `json:"session_id,omitempty"`
	RecordType        string    `json:"record_type"`
	Model             string    `json:"model,omitempty"`
	InputTokens       *int      `json:"input_tokens,omitempty"`
	OutputTokens      *int      `json:"output_tokens,omitempty"`
	CacheReadTokens   *int      `json:"cache_read_tokens,omitempty"`
	CacheCreateTokens *int      `json:"cache_create_tokens,omitempty"`
	TaskType          string    `json:"task_type,omitempty"`
	// record_type is each agent's own spelling. kind is the server's display
	// classification, not a tool-call vocabulary: Claude/omo/gjc calls sit inside
	// message records and gjc's sibling tool_call rows are hidden duplicates. To count
	// calls across agents, sum tool_call_count. The body (text, tool input, tool
	// output) is never included.
	Agent         string           `json:"agent,omitempty"`
	Kind          sessionview.Kind `json:"kind"`
	ToolCallCount int              `json:"tool_call_count"`
	ToolName      string           `json:"tool_name,omitempty"`
	ToolCallID    string           `json:"tool_call_id,omitempty"`
}

func newOpenAPISessionRecordDTO(record *store.SessionRecord) openAPISessionRecordDTO {
	summary := sessionview.Summarize(record)
	return openAPISessionRecordDTO{
		Ts: record.Ts, SessionID: record.SessionID,
		RecordType: record.RecordType, Model: record.Model, InputTokens: record.InputTokens, OutputTokens: record.OutputTokens,
		CacheReadTokens: record.CacheReadTokens, CacheCreateTokens: record.CacheCreateTokens,
		TaskType: record.TaskType,
		Agent:    record.Agent, Kind: summary.Kind, ToolCallCount: summary.ToolCallCount,
		ToolName: record.ToolName, ToolCallID: record.ToolCallID,
	}
}

func writeOpenAPIInternalError(w http.ResponseWriter, err error) {
	log.Printf("[open-api] error: %v", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
}

func (s *Server) handleOpenAPIListSessions(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	since, until, err := optionalQueryTimeRange(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	projectHashes := queryProjectHashes(r, "project_hash")
	projectHashes = append(projectHashes, queryProjectHashes(r, "project_hashes")...)
	f := store.SessionOverviewFilter{
		ProfileEmail: r.URL.Query().Get("profile_email"), LoginEmail: r.URL.Query().Get("login_email"),
		UserID: r.URL.Query().Get("user_id"), Since: since, Until: until,
		ProjectHashes: projectHashes, Limit: queryInt(r, "limit", 100), Offset: queryInt(r, "offset", 0),
	}
	if user.Role != "admin" {
		f.ProfileEmail, f.LoginEmail, f.UserID = s.openAPIOwnerScope(user)
	}

	overviews, err := s.store.ListSessionOverviews(r.Context(), f)
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	response := make([]openAPISessionOverviewDTO, 0, len(overviews))
	for _, overview := range overviews {
		response = append(response, newOpenAPISessionOverviewDTO(overview))
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleOpenAPIGetSession(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	f := store.SessionRecordFilter{
		SessionID: r.PathValue("session_id"), ProfileEmail: r.URL.Query().Get("profile_email"),
		UserID: r.URL.Query().Get("user_id"), Limit: queryInt(r, "limit", 100),
		Offset: queryInt(r, "offset", 0), Order: r.URL.Query().Get("order"),
	}
	s.applyOpenAPISessionScope(&f, user)
	records, err := s.store.ListSessionRecords(r.Context(), f)
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	response := make([]openAPISessionRecordDTO, 0, len(records))
	for _, record := range records {
		response = append(response, newOpenAPISessionRecordDTO(record))
	}
	writeJSON(w, http.StatusOK, response)
}

type openAPIEventDTO struct {
	Ts                time.Time `json:"ts"`
	EventName         string    `json:"event_name"`
	SessionID         string    `json:"session_id,omitempty"`
	UserID            string    `json:"user_id,omitempty"`
	Model             string    `json:"model,omitempty"`
	CostUSD           *float64  `json:"cost_usd,omitempty"`
	InputTokens       *int      `json:"input_tokens,omitempty"`
	OutputTokens      *int      `json:"output_tokens,omitempty"`
	CacheReadTokens   *int      `json:"cache_read_tokens,omitempty"`
	CacheCreateTokens *int      `json:"cache_create_tokens,omitempty"`
	DurationMs        *int      `json:"duration_ms,omitempty"`
	ToolName          string    `json:"tool_name,omitempty"`
	ToolDecision      string    `json:"tool_decision,omitempty"`
	ToolSuccess       *bool     `json:"tool_success,omitempty"`
	Speed             string    `json:"speed,omitempty"`
}

func newOpenAPIEventDTO(event *store.OtelEvent) openAPIEventDTO {
	return openAPIEventDTO{
		Ts: event.Ts, EventName: event.EventName, SessionID: event.SessionID, UserID: event.UserID,
		Model: event.Model, CostUSD: event.CostUSD, InputTokens: event.InputTokens,
		OutputTokens: event.OutputTokens, CacheReadTokens: event.CacheReadTokens,
		CacheCreateTokens: event.CacheCreateTokens, DurationMs: event.DurationMs, ToolName: event.ToolName,
		ToolDecision: event.ToolDecision, ToolSuccess: event.ToolSuccess, Speed: event.Speed,
	}
}

func (s *Server) handleOpenAPIListEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	since, until, err := optionalQueryTimeRange(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	f := store.EventFilter{
		SessionID: r.URL.Query().Get("session_id"), ProjectHash: queryProjectHash(r, "project_hash"),
		ProfileEmail: r.URL.Query().Get("profile_email"), LoginEmail: r.URL.Query().Get("login_email"),
		UserID: r.URL.Query().Get("user_id"), Since: since, Until: until,
		Limit: queryInt(r, "limit", 100), Offset: queryInt(r, "offset", 0),
	}
	if user.Role != "admin" {
		f.ProfileEmail, f.LoginEmail, f.UserID = s.openAPIOwnerScope(user)
	}
	events, err := s.store.ListEvents(r.Context(), f)
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	response := make([]openAPIEventDTO, 0, len(events))
	for _, event := range events {
		response = append(response, newOpenAPIEventDTO(event))
	}
	writeJSON(w, http.StatusOK, response)
}

type openAPIUsageModelDTO struct {
	Model        string `json:"model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
}

// openAPIUsageGroupDTO is one row of a grouped usage answer. `key` is the value of
// whatever axis was requested -- a user, a team, a model -- so a client can render
// any grouping without knowing which one it asked for.
type openAPIUsageGroupDTO struct {
	Key          string  `json:"key"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	RequestCount int64   `json:"request_count"`
}

type openAPIUsageGroupListDTO struct {
	Items []openAPIUsageGroupDTO `json:"items"`
	Total int                    `json:"total"`
}

type openAPIUsageDTO struct {
	SessionCount    int64                  `json:"session_count"`
	CostUSD         float64                `json:"cost_usd"`
	InputTokens     int64                  `json:"input_tokens"`
	OutputTokens    int64                  `json:"output_tokens"`
	TotalTokens     int64                  `json:"total_tokens"`
	WorkTimeSeconds int64                  `json:"work_time_seconds"`
	ByModel         []openAPIUsageModelDTO `json:"by_model"`
}

// openAPIUsageGroups maps the axis a caller asked for onto the store method that
// already answers it. Adding an axis means adding a store method, not a new query
// here -- these are the same aggregations the dashboard renders.
var openAPIUsageGroups = map[string]bool{"user": true, "team": true, "model": true}

// openAPIUsageWindow unwraps the window the handler already required. Both bounds
// are present by the time this runs -- the handler refuses the request otherwise --
// so there is no default to invent here, and the cost and token halves of an answer
// cannot end up describing different spans.
func (s *Server) openAPIUsageWindow(f store.SessionOverviewFilter) (time.Time, time.Time) {
	return *f.Since, *f.Until
}

// openAPIUsageCost totals cost over the same window and scope as the ungrouped
// usage answer.
func (s *Server) openAPIUsageCost(ctx context.Context, f store.SessionOverviewFilter) (float64, error) {
	since, until := s.openAPIUsageWindow(f)
	rows, err := s.store.CostByModel(ctx, since, until, f.ProfileEmail, f.LoginEmail, f.UserID)
	if err != nil {
		return 0, err
	}
	var total float64
	for _, row := range rows {
		total += row.TotalCost
	}
	return total, nil
}

// writeOpenAPIUsageGrouped folds the store's rows onto the requested axis.
//
// The cost aggregations group by model in addition to the axis asked for, so a user
// who touched three models arrives as three rows. `cctrace report` prints them
// unfolded today, which is why the same email can appear three times on screen. The
// API does not inherit that: one key, one row.
func (s *Server) writeOpenAPIUsageGrouped(w http.ResponseWriter, r *http.Request, groupBy string, f store.SessionOverviewFilter) {
	if !openAPIUsageGroups[groupBy] {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "unknown group_by; valid values are user, team, model",
			"code":  "invalid_group_by",
		})
		return
	}
	since, until := s.openAPIUsageWindow(f)
	ctx := r.Context()

	totals := map[string]*openAPIUsageGroupDTO{}
	var order []string
	add := func(key string, cost float64, in, out, count int64) {
		row, ok := totals[key]
		if !ok {
			row = &openAPIUsageGroupDTO{Key: key}
			totals[key] = row
			order = append(order, key)
		}
		row.CostUSD += cost
		row.InputTokens += in
		row.OutputTokens += out
		row.TotalTokens += in + out
		row.RequestCount += count
	}

	switch groupBy {
	case "user":
		rows, err := s.store.CostByUser(ctx, since, until, f.ProfileEmail, f.LoginEmail, f.UserID)
		if err != nil {
			writeOpenAPIInternalError(w, err)
			return
		}
		for _, row := range rows {
			add(row.ProfileEmail, row.TotalCost, row.TotalInput, row.TotalOutput, row.RequestCount)
		}
	case "team":
		rows, err := s.store.CostByTeam(ctx, since, until, f.ProfileEmail, f.UserID)
		if err != nil {
			writeOpenAPIInternalError(w, err)
			return
		}
		for _, row := range rows {
			add(row.UserTeam, row.TotalCost, row.TotalInput, row.TotalOutput, row.RequestCount)
		}
	case "model":
		rows, err := s.store.CostByModel(ctx, since, until, f.ProfileEmail, f.LoginEmail, f.UserID)
		if err != nil {
			writeOpenAPIInternalError(w, err)
			return
		}
		for _, row := range rows {
			add(row.Model, row.TotalCost, row.InputTokens, row.OutputTokens, row.RequestCount)
		}
	}

	items := make([]openAPIUsageGroupDTO, 0, len(order))
	for _, key := range order {
		items = append(items, *totals[key])
	}
	writeJSON(w, http.StatusOK, openAPIUsageGroupListDTO{Items: items, Total: len(items)})
}

func (s *Server) handleOpenAPIUsage(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	since, until, err := optionalQueryTimeRange(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	projectHashes := queryProjectHashes(r, "project_hash")
	projectHashes = append(projectHashes, queryProjectHashes(r, "project_hashes")...)
	f := store.SessionOverviewFilter{
		ProfileEmail: r.URL.Query().Get("profile_email"), LoginEmail: r.URL.Query().Get("login_email"),
		UserID: r.URL.Query().Get("user_id"), Since: since, Until: until, ProjectHashes: projectHashes,
	}
	if user.Role != "admin" {
		f.ProfileEmail, f.LoginEmail, f.UserID = s.openAPIOwnerScope(user)
	}
	// Cost and tokens come from different tables. Token totals read session records,
	// which carry project_hash; the cost aggregations read unified_events, which has
	// no such column. Honouring the filter on one half and not the other would put
	// one project's tokens and the whole fleet's cost in the same object -- a wrong
	// number in the one field this product exists to report. Refuse instead, for the
	// same reason an unknown group_by is refused rather than ignored.
	if len(projectHashes) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "usage cannot be filtered by project: cost is aggregated over a source that does not carry project_hash",
			"code":  "unsupported_filter",
		})
		return
	}
	// An open-ended window makes one request aggregate all of history, and it does it
	// twice -- once for tokens, once for cost. Requiring a bound also keeps the two
	// halves describing the same span, which an implicit "until now" would not.
	if since == nil || until == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "usage requires both since and until (RFC3339)",
			"code":  "unbounded_window",
		})
		return
	}

	// Grouped answers come from the cost aggregations the dashboard already uses, so
	// the open surface cannot drift from what the screen shows.
	if groupBy := r.URL.Query().Get("group_by"); groupBy != "" {
		s.writeOpenAPIUsageGrouped(w, r, groupBy, f)
		return
	}

	usage, err := s.store.UsageAggregates(r.Context(), f)
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	// UsageAggregates counts tokens and nothing else, so cost comes from the same
	// per-model aggregation the dashboard reads. Summing it here keeps one source of
	// truth for the money rather than adding a second cost query.
	cost, err := s.openAPIUsageCost(r.Context(), f)
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	response := openAPIUsageDTO{
		CostUSD:      cost,
		SessionCount: usage.SessionCount, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		TotalTokens: usage.InputTokens + usage.OutputTokens, WorkTimeSeconds: usage.WorkTimeSeconds,
		ByModel: make([]openAPIUsageModelDTO, 0, len(usage.ByModel)),
	}
	for _, row := range usage.ByModel {
		model := openAPIUsageModelDTO{
			Model: row.Model, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
			TotalTokens: row.InputTokens + row.OutputTokens,
		}
		response.ByModel = append(response.ByModel, model)
	}
	writeJSON(w, http.StatusOK, response)
}
