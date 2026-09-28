package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cctrace/internal/store"
)

func TestOpenAPIRequiresActiveUserBearerToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
		status int
	}{
		{name: "missing bearer", status: http.StatusUnauthorized},
		{name: "invalid bearer", header: "Bearer invalid", status: http.StatusUnauthorized},
		{name: "global ingest key is not a user identity", header: "Bearer global-ingest-key", status: http.StatusUnauthorized},
		{name: "inactive user token", header: "Bearer inactive-token", status: http.StatusUnauthorized},
		{name: "unsupported role", header: "Bearer viewer-token", status: http.StatusForbidden},
		{name: "active user token", header: "Bearer alice-token", status: http.StatusOK},
	}

	m := &mockStore{getDashboardUserByAPITokenFn: func(_ context.Context, token string) (*store.DashboardUser, error) {
		switch token {
		case "alice-token":
			return &store.DashboardUser{ID: 1, Email: "alice@example.com", Role: "user", IsActive: true, CctraceUserID: "alice"}, nil
		case "inactive-token":
			return &store.DashboardUser{ID: 2, Role: "user", IsActive: false, CctraceUserID: "inactive"}, nil
		case "viewer-token":
			return &store.DashboardUser{ID: 3, Role: "viewer", IsActive: true, CctraceUserID: "viewer"}, nil
		default:
			return nil, context.Canceled
		}
	}}
	srv := newTestServer(m, nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := openAPIRequest(t, srv, http.MethodGet, "/api/open/v1/events", tt.header)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.status, rec.Body.String())
			}
		})
	}
}

func TestOpenAPIListSessionsUsesOverviewFiltersAndOwnerScope(t *testing.T) {
	var got store.SessionOverviewFilter
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("alice-token", &store.DashboardUser{Role: "user", IsActive: true, CctraceUserID: "alice"}),
		listSessionOverviewsFn: func(_ context.Context, filter store.SessionOverviewFilter) ([]*store.SessionOverview, error) {
			got = filter
			return nil, nil
		},
	}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet,
		"/api/open/v1/sessions?user_id=bob&profile_email=bob@example.com&login_email=bob-login@example.com&project_hash=-p1,-p2&since=2026-08-01T01:02:03Z&until=2026-08-02T01:02:03Z&limit=25&offset=5", "Bearer alice-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got.UserID != "alice" || got.ProfileEmail != "" || got.LoginEmail != "" {
		t.Fatalf("filter = %+v, want caller-derived owner scope", got)
	}
	if len(got.ProjectHashes) != 2 || got.ProjectHashes[0] != "-p1" || got.ProjectHashes[1] != "-p2" {
		t.Fatalf("project hashes = %v, want [p1 p2]", got.ProjectHashes)
	}
	assertTimeFilter(t, got.Since, "2026-08-01T01:02:03Z")
	assertTimeFilter(t, got.Until, "2026-08-02T01:02:03Z")
	if got.Limit != 25 || got.Offset != 5 {
		t.Fatalf("pagination = %d/%d, want 25/5", got.Limit, got.Offset)
	}
}

func TestOpenAPIListSessionsMissingOwnerIDFailsClosed(t *testing.T) {
	var got store.SessionOverviewFilter
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("alice-token", &store.DashboardUser{Role: "user", Email: "alice@example.com", IsActive: true}),
		listSessionOverviewsFn: func(_ context.Context, filter store.SessionOverviewFilter) ([]*store.SessionOverview, error) {
			got = filter
			return nil, nil
		},
	}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet, "/api/open/v1/sessions?user_id=bob", "Bearer alice-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got.UserID != noAccessSentinel || got.ProfileEmail != "" || got.LoginEmail != "" {
		t.Fatalf("filter = %+v, want no-access sentinel", got)
	}
}

func TestOpenAPIListSessionsAdminPreservesSupportedFilters(t *testing.T) {
	var got store.SessionOverviewFilter
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("admin-token", &store.DashboardUser{Role: "admin", IsActive: true}),
		listSessionOverviewsFn: func(_ context.Context, filter store.SessionOverviewFilter) ([]*store.SessionOverview, error) {
			got = filter
			return nil, nil
		},
	}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet,
		"/api/open/v1/sessions?user_id=bob&profile_email=bob@example.com&login_email=login@example.com", "Bearer admin-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got.UserID != "bob" || got.ProfileEmail != "bob@example.com" || got.LoginEmail != "login@example.com" {
		t.Fatalf("admin filter changed: %+v", got)
	}
}

func TestOpenAPIListSessionsRejectsMalformedTimes(t *testing.T) {
	m := &mockStore{getDashboardUserByAPITokenFn: apiUserTokenLookup("alice-token", &store.DashboardUser{Role: "user", IsActive: true, CctraceUserID: "alice"})}
	for _, query := range []string{"?since=yesterday", "?until=tomorrow"} {
		rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet, "/api/open/v1/sessions"+query, "Bearer alice-token")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", query, rec.Code)
		}
	}
}

func TestOpenAPIListSessionsResponseUsesAllowlist(t *testing.T) {
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("alice-token", &store.DashboardUser{Role: "user", IsActive: true, CctraceUserID: "alice"}),
		listSessionOverviewsFn: func(context.Context, store.SessionOverviewFilter) ([]*store.SessionOverview, error) {
			return []*store.SessionOverview{{
				SessionID: "s1", ProfileEmail: "profile@example.com", LoginEmail: "login@example.com", UserID: "alice",
				ProjectHash: "project", ProjectName: "private name", Model: "sonnet", Agent: "claude",
				Entrypoint: "cli", HasEnriched: true, CctraceVersion: "internal", ClaudeVersion: "internal",
			}}, nil
		},
	}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet, "/api/open/v1/sessions", "Bearer alice-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// project_name is the label /projects already returns to this caller; the
	// path-derived project_hash stays out.
	assertJSONKeysExactly(t, body[0], "session_id", "user_id", "model", "agent", "project_name", "start_time", "end_time", "input_tokens", "output_tokens", "cost_usd", "event_count")
	if body[0]["project_name"] != "private name" {
		t.Errorf("project_name = %v", body[0]["project_name"])
	}
}

func TestOpenAPISessionDetailScopesOwnerAndSanitizesRecords(t *testing.T) {
	var got store.SessionRecordFilter
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("alice-token", &store.DashboardUser{Role: "user", IsActive: true, CctraceUserID: "alice"}),
		listSessionRecordsFn: func(_ context.Context, filter store.SessionRecordFilter) ([]*store.SessionRecord, error) {
			got = filter
			inputTokens, outputTokens, cacheReadTokens, cacheCreateTokens := 1, 2, 3, 4
			return []*store.SessionRecord{{
				SessionID: "session/id", ProjectHash: "project", RecordType: "assistant", ProfileEmail: "profile@example.com",
				UserID: "alice", Model: "sonnet", InputTokens: &inputTokens, OutputTokens: &outputTokens,
				CacheReadTokens: &cacheReadTokens, CacheCreateTokens: &cacheCreateTokens,
				CommandName: "secret-command", Agent: "claude", BillingProvider: "anthropic",
				Raw: json.RawMessage(`{"message":{"content":"secret"}}`), RepositoryID: "private-repo", SourceFile: "secret.jsonl",
				UUID: "lineage", ParentUUID: "lineage-parent", PromptSource: "internal", CctraceVersion: "internal",
				TaskType: "testing",
			}}, nil
		},
	}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet,
		"/api/open/v1/sessions/session%2Fid?user_id=bob&profile_email=bob@example.com&limit=20&offset=2&order=asc", "Bearer alice-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got.SessionID != "session/id" || got.UserID != "alice" || got.ProfileEmail != "" || got.Limit != 20 || got.Offset != 2 || got.Order != "asc" {
		t.Fatalf("filter = %+v", got)
	}
	var body []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	assertJSONKeysExactly(t, body[0], "ts", "session_id", "record_type", "model", "input_tokens", "output_tokens", "cache_read_tokens", "cache_create_tokens", "task_type", "agent", "kind", "tool_call_count")
	if body[0]["task_type"] != "testing" {
		t.Fatalf("task_type = %#v", body[0]["task_type"])
	}
}

func TestOpenAPIEventsUsesFiltersOwnerScopeAndAllowlist(t *testing.T) {
	var got store.EventFilter
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("alice-token", &store.DashboardUser{Role: "user", IsActive: true, CctraceUserID: "alice"}),
		listEventsFn: func(_ context.Context, filter store.EventFilter) ([]*store.OtelEvent, error) {
			got = filter
			costUSD := 0.1
			inputTokens, outputTokens, cacheReadTokens, cacheCreateTokens, durationMs := 1, 2, 3, 4, 5
			toolSuccess := true
			return []*store.OtelEvent{{
				SessionID: "s1", EventName: "api_request", UserID: "alice", ProfileEmail: "profile@example.com",
				LoginEmail: "login@example.com", UserName: "Alice", UserTeam: "private-team", Model: "sonnet",
				CostUSD: &costUSD, InputTokens: &inputTokens, OutputTokens: &outputTokens, CacheReadTokens: &cacheReadTokens,
				CacheCreateTokens: &cacheCreateTokens, DurationMs: &durationMs, ToolName: "tool", ToolDecision: "allow",
				ToolSuccess: &toolSuccess, Speed: "fast", PromptID: "internal", OrgID: "internal",
				ServiceVersion: "internal", Attrs: map[string]interface{}{"secret": true}, Agent: "claude", BillingProvider: "anthropic",
			}}, nil
		},
	}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet,
		"/api/open/v1/events?session_id=s1&project_hash=-p1&user_id=bob&profile_email=bob@example.com&login_email=login@example.com&since=2026-08-01T01:02:03Z&until=2026-08-02T01:02:03Z&limit=10&offset=3", "Bearer alice-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got.SessionID != "s1" || got.ProjectHash != "-p1" || got.UserID != "alice" || got.ProfileEmail != "" || got.LoginEmail != "" || got.Limit != 10 || got.Offset != 3 {
		t.Fatalf("filter = %+v", got)
	}
	assertTimeFilter(t, got.Since, "2026-08-01T01:02:03Z")
	assertTimeFilter(t, got.Until, "2026-08-02T01:02:03Z")
	var body []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	assertJSONKeysExactly(t, body[0], "ts", "event_name", "session_id", "user_id", "model", "cost_usd", "input_tokens", "output_tokens", "cache_read_tokens", "cache_create_tokens", "duration_ms", "tool_name", "tool_decision", "tool_success", "speed")
}

func TestOpenAPIToolsScopeAndSanitizeResponses(t *testing.T) {
	var gotUserID string
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("alice-token", &store.DashboardUser{Role: "user", IsActive: true, CctraceUserID: "alice"}),
		toolUsageFn: func(_ context.Context, _, _ time.Time, profileEmail, loginEmail, userID string) ([]*store.ToolUsageSummary, error) {
			if profileEmail != "" || loginEmail != "" {
				t.Fatalf("unexpected email scope: %q/%q", profileEmail, loginEmail)
			}
			gotUserID = userID
			return []*store.ToolUsageSummary{{ToolName: "shell", UseCount: 3, SuccessCount: 2, FailCount: 1}}, nil
		},
		toolTimeSeriesFn: func(_ context.Context, toolName string, _, _ time.Time, _, _, userID, granularity string) ([]*store.ToolTimeBucket, error) {
			if toolName != "shell/run" || userID != "alice" || granularity != "hour" {
				t.Fatalf("detail scope = %q/%q/%q", toolName, userID, granularity)
			}
			return []*store.ToolTimeBucket{{Date: "2026-08-01T01:00:00", SuccessCount: 2, FailCount: 1}}, nil
		},
		toolFailuresFn: func(_ context.Context, toolName string, _, _ time.Time, _, _, userID string, limit int) ([]*store.ToolFailure, error) {
			duration := 25
			if toolName != "shell/run" || userID != "alice" || limit != 7 {
				t.Fatalf("failure scope = %q/%q/%d", toolName, userID, limit)
			}
			return []*store.ToolFailure{{
				SessionID: "s1", UserID: "private-user", ProfileEmail: "private@example.com",
				LoginEmail: "login@example.com", Model: "sonnet", DurationMs: &duration,
				Attrs: map[string]interface{}{"secret": true},
			}}, nil
		},
	}
	srv := newTestServer(m, nil)
	list := openAPIRequest(t, srv, http.MethodGet,
		"/api/open/v1/tools?user_id=bob&profile_email=bob@example.com&since=2026-08-01T00:00:00Z&until=2026-08-02T00:00:00Z", "Bearer alice-token")
	if list.Code != http.StatusOK || gotUserID != "alice" {
		t.Fatalf("list status/scope = %d/%q; body=%s", list.Code, gotUserID, list.Body.String())
	}
	var listBody []map[string]interface{}
	if err := json.Unmarshal(list.Body.Bytes(), &listBody); err != nil {
		t.Fatal(err)
	}
	assertJSONKeysExactly(t, listBody[0], "tool_name", "use_count", "success_count", "fail_count")

	detail := openAPIRequest(t, srv, http.MethodGet,
		"/api/open/v1/tools/shell%2Frun?user_id=bob&granularity=hour&failure_limit=7", "Bearer alice-token")
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d; body=%s", detail.Code, detail.Body.String())
	}
	var detailBody map[string]interface{}
	if err := json.Unmarshal(detail.Body.Bytes(), &detailBody); err != nil {
		t.Fatal(err)
	}
	assertJSONKeysExactly(t, detailBody, "tool_name", "timeseries", "failures")
	failure := detailBody["failures"].([]interface{})[0].(map[string]interface{})
	assertJSONKeysExactly(t, failure, "ts", "session_id", "model", "duration_ms")
}

func TestOpenAPIPluginsAndSkillsScopeAndSanitizeResponses(t *testing.T) {
	var pluginAgent, skillAgent, pluginUserID, skillUserID string
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("alice-token", &store.DashboardUser{Role: "user", IsActive: true, CctraceUserID: "alice"}),
		pluginUsageFn: func(_ context.Context, _, _ time.Time, _, _, userID, agent string) ([]*store.PluginUsageSummary, error) {
			pluginAgent, pluginUserID = agent, userID
			return []*store.PluginUsageSummary{{
				CommandName: "/review", Agent: "claude", ProfileEmail: "private@example.com", UserID: "alice",
				ProjectHash: "private", RepositoryName: "private", InvocationCount: 4, TotalTokens: 10, InputTokens: 7, OutputTokens: 3,
			}}, nil
		},
		skillUsageFn: func(_ context.Context, _, _ time.Time, _, _, userID, agent string) ([]*store.SkillUsageSummary, error) {
			skillAgent, skillUserID = agent, userID
			return []*store.SkillUsageSummary{{
				SkillName: "analyze", Agent: "codex", InvokeType: "explicit", ProfileEmail: "private@example.com",
				RepositoryName: "private", SuccessCount: 3, FailCount: 1, TotalCount: 4,
			}}, nil
		},
	}
	srv := newTestServer(m, nil)
	plugin := openAPIRequest(t, srv, http.MethodGet, "/api/open/v1/plugins?agent=claude&user_id=bob", "Bearer alice-token")
	skill := openAPIRequest(t, srv, http.MethodGet, "/api/open/v1/skills?agent=codex&user_id=bob", "Bearer alice-token")
	if plugin.Code != http.StatusOK || skill.Code != http.StatusOK {
		t.Fatalf("statuses = %d/%d", plugin.Code, skill.Code)
	}
	if pluginAgent != "claude" || skillAgent != "codex" || pluginUserID != "alice" || skillUserID != "alice" {
		t.Fatalf("filters = %q/%q/%q/%q", pluginAgent, skillAgent, pluginUserID, skillUserID)
	}
	var plugins, skills []map[string]interface{}
	if err := json.Unmarshal(plugin.Body.Bytes(), &plugins); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(skill.Body.Bytes(), &skills); err != nil {
		t.Fatal(err)
	}
	assertJSONKeysExactly(t, plugins[0], "command_name", "agent", "invocation_count", "total_tokens", "input_tokens", "output_tokens")
	assertJSONKeysExactly(t, skills[0], "skill_name", "agent", "invoke_type", "success_count", "fail_count", "total_count")
}

func TestOpenAPIMetricsScopeFiltersAndSanitizesResponse(t *testing.T) {
	var got store.MetricFilter
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("alice-token", &store.DashboardUser{Role: "user", IsActive: true, CctraceUserID: "alice"}),
		listMetricsFn: func(_ context.Context, filter store.MetricFilter) ([]*store.OtelMetric, error) {
			got = filter
			valueDouble, valueInt := 2.5, int64(3)
			return []*store.OtelMetric{{
				MetricName: "codex.skill.injected", SessionID: "s1", UserID: "alice", ProfileEmail: "private@example.com",
				LoginEmail: "login@example.com", UserTeam: "private", Model: "gpt", ValueDouble: &valueDouble,
				ValueInt: &valueInt, Dimensions: map[string]interface{}{"secret": true}, Agent: "codex", BillingProvider: "openai",
			}}, nil
		},
	}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet,
		"/api/open/v1/metrics?metric_name=codex.skill.injected&agent=codex&model=gpt&user_id=bob&limit=20&offset=2", "Bearer alice-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if got.UserID != "alice" || got.MetricName != "codex.skill.injected" || got.Agent != "codex" || got.Model != "gpt" || got.Limit != 20 || got.Offset != 2 {
		t.Fatalf("filter = %+v", got)
	}
	var body []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	assertJSONKeysExactly(t, body[0], "ts", "metric_name", "session_id", "user_id", "model", "value_double", "value_int", "agent")
}

func TestOpenAPIRulesScopeAccessAndSanitizeResponses(t *testing.T) {
	var gotFilter store.ProjectRuleFilter
	visibleChecked := false
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	rule := &store.ProjectRuleListItem{ProjectRule: store.ProjectRule{
		ID: 42, Agent: "codex", ProjectHash: "private", RepositoryKey: "private", RepositoryName: "private",
		RulePath: "/private/AGENTS.md", RuleKind: "agents", RuleScope: "repository", Title: "Agent rules",
		CurrentContentHash: "private", CurrentStatus: "active", DiscoveredAt: now, LastSeenAt: now, UpdatedAt: now,
	}, VersionCount: 2, CommentCount: 1}
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("alice-token", &store.DashboardUser{Role: "user", IsActive: true, CctraceUserID: "alice"}),
		listProjectRulesFn: func(_ context.Context, filter store.ProjectRuleFilter) (*store.ProjectRuleListResponse, error) {
			gotFilter = filter
			return &store.ProjectRuleListResponse{Items: []*store.ProjectRuleListItem{rule}, Total: 1}, nil
		},
		projectRuleVisibleToFn: func(_ context.Context, ruleID int64, userID, profileEmail string) (bool, error) {
			visibleChecked = ruleID == 42 && userID == "alice" && profileEmail == ""
			return visibleChecked, nil
		},
		getProjectRuleDetailFn: func(_ context.Context, id, _ int64) (*store.ProjectRuleDetail, error) {
			return &store.ProjectRuleDetail{
				Rule: rule,
				Versions: []*store.ProjectRuleVersion{{
					ID: 9, RuleID: id, VersionNumber: 2, ContentHash: "private", Content: "secret body",
					SizeBytes: 123, ChangeReason: "updated", CommitSHA: "private", Branch: "private", AppliesTo: []string{"*.go"},
					RawMetadata: map[string]interface{}{"secret": true}, CreatedByProfileEmail: "private@example.com", DiscoveredAt: now,
				}},
				Comments: []*store.ProjectRuleComment{{Body: "private comment", AuthorProfileEmail: "private@example.com"}},
			}, nil
		},
	}
	srv := newTestServer(m, nil)
	list := openAPIRequest(t, srv, http.MethodGet, "/api/open/v1/rules?agent=codex&status=active&query=agent&user_id=bob&limit=10&offset=3", "Bearer alice-token")
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d; body=%s", list.Code, list.Body.String())
	}
	if gotFilter.UserID != "alice" || gotFilter.Agent != "codex" || gotFilter.Status != "active" || gotFilter.Query != "agent" || gotFilter.Limit != 10 || gotFilter.Offset != 3 {
		t.Fatalf("filter = %+v", gotFilter)
	}
	var listBody map[string]interface{}
	if err := json.Unmarshal(list.Body.Bytes(), &listBody); err != nil {
		t.Fatal(err)
	}
	assertJSONKeysExactly(t, listBody, "items", "total")
	item := listBody["items"].([]interface{})[0].(map[string]interface{})
	assertJSONKeysExactly(t, item, "id", "agent", "rule_kind", "rule_scope", "title", "current_status", "version_count", "comment_count", "discovered_at", "last_seen_at", "updated_at")

	detail := openAPIRequest(t, srv, http.MethodGet, "/api/open/v1/rules/42", "Bearer alice-token")
	if detail.Code != http.StatusOK || !visibleChecked {
		t.Fatalf("detail status/access = %d/%v; body=%s", detail.Code, visibleChecked, detail.Body.String())
	}
	var detailBody map[string]interface{}
	if err := json.Unmarshal(detail.Body.Bytes(), &detailBody); err != nil {
		t.Fatal(err)
	}
	assertJSONKeysExactly(t, detailBody, "rule", "versions")
	version := detailBody["versions"].([]interface{})[0].(map[string]interface{})
	assertJSONKeysExactly(t, version, "id", "version_number", "size_bytes", "change_reason", "applies_to", "discovered_at")
}

func TestOpenAPIRoutesHideInternalErrors(t *testing.T) {
	secretErr := errors.New("database password=secret")
	tests := []struct {
		name   string
		target string
		store  *mockStore
	}{
		{
			name:   "list sessions",
			target: "/api/open/v1/sessions",
			store: &mockStore{listSessionOverviewsFn: func(context.Context, store.SessionOverviewFilter) ([]*store.SessionOverview, error) {
				return nil, secretErr
			}},
		},
		{
			name:   "session detail",
			target: "/api/open/v1/sessions/s1",
			store: &mockStore{listSessionRecordsFn: func(context.Context, store.SessionRecordFilter) ([]*store.SessionRecord, error) {
				return nil, secretErr
			}},
		},
		{
			name:   "events",
			target: "/api/open/v1/events",
			store: &mockStore{listEventsFn: func(context.Context, store.EventFilter) ([]*store.OtelEvent, error) {
				return nil, secretErr
			}},
		},
		{
			name:   "usage",
			target: "/api/open/v1/usage?since=2026-08-01T00:00:00Z&until=2026-08-31T00:00:00Z",
			store: &mockStore{usageAggregatesFn: func(context.Context, store.SessionOverviewFilter) (*store.UsageAggregate, error) {
				return nil, secretErr
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.store.getDashboardUserByAPITokenFn = apiUserTokenLookup("alice-token", &store.DashboardUser{Role: "user", IsActive: true, CctraceUserID: "alice"})
			rec := openAPIRequest(t, newTestServer(tt.store, nil), http.MethodGet, tt.target, "Bearer alice-token")
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
			}
			var body map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			assertJSONKeysExactly(t, body, "error")
			if body["error"] != "internal server error" {
				t.Fatalf("error = %q, want generic message", body["error"])
			}
		})
	}
}

func TestOpenAPIEventsAdminPreservesSupportedFilters(t *testing.T) {
	var got store.EventFilter
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("admin-token", &store.DashboardUser{Role: "admin", IsActive: true}),
		listEventsFn: func(_ context.Context, filter store.EventFilter) ([]*store.OtelEvent, error) {
			got = filter
			return nil, nil
		},
	}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet,
		"/api/open/v1/events?user_id=bob&profile_email=bob@example.com&login_email=login@example.com", "Bearer admin-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got.UserID != "bob" || got.ProfileEmail != "bob@example.com" || got.LoginEmail != "login@example.com" {
		t.Fatalf("admin filter changed: %+v", got)
	}
}

func TestOpenAPIEventsMissingOwnerIDFailsClosed(t *testing.T) {
	var got store.EventFilter
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("alice-token", &store.DashboardUser{Role: "user", Email: "alice@example.com", IsActive: true}),
		listEventsFn: func(_ context.Context, filter store.EventFilter) ([]*store.OtelEvent, error) {
			got = filter
			return nil, nil
		},
	}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet,
		"/api/open/v1/events?user_id=bob&profile_email=bob@example.com&login_email=login@example.com", "Bearer alice-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got.UserID != noAccessSentinel || got.ProfileEmail != "" || got.LoginEmail != "" {
		t.Fatalf("filter = %+v, want no-access sentinel", got)
	}
}

func TestOpenAPIEventsRejectsMalformedTimes(t *testing.T) {
	m := &mockStore{getDashboardUserByAPITokenFn: apiUserTokenLookup("alice-token", &store.DashboardUser{Role: "user", IsActive: true, CctraceUserID: "alice"})}
	for _, query := range []string{"?since=yesterday", "?until=tomorrow"} {
		rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet, "/api/open/v1/events"+query, "Bearer alice-token")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", query, rec.Code)
		}
	}
}

func TestOpenAPIUsageAggregatesFiltersScopeAndUsesAllowlist(t *testing.T) {
	var got store.SessionOverviewFilter
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("alice-token", &store.DashboardUser{Role: "user", IsActive: true, CctraceUserID: "alice"}),
		usageAggregatesFn: func(_ context.Context, filter store.SessionOverviewFilter) (*store.UsageAggregate, error) {
			got = filter
			return &store.UsageAggregate{
				SessionCount: 2, InputTokens: 16, OutputTokens: 7, WorkTimeSeconds: 90,
				ByModel: []*store.ModelUsageAggregate{
					{Model: "opus", InputTokens: 6, OutputTokens: 2},
					{Model: "sonnet", InputTokens: 10, OutputTokens: 5},
				},
			}, nil
		},
	}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet,
		"/api/open/v1/usage?user_id=bob&profile_email=bob@example.com&since=2026-08-01T01:02:03Z&until=2026-08-02T01:02:03Z", "Bearer alice-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// project_hash used to be part of this request. It was a sound filter while the
	// answer was tokens only; once cost joined it, the two halves read different
	// tables and only one of them carries project_hash, so the endpoint now refuses
	// the filter instead of honouring half of it. See
	// TestOpenAPIUsageRefusesProjectFilterItCannotCost.
	if got.UserID != "alice" || got.ProfileEmail != "" {
		t.Fatalf("filter = %+v, want caller-derived scope", got)
	}
	assertTimeFilter(t, got.Since, "2026-08-01T01:02:03Z")
	assertTimeFilter(t, got.Until, "2026-08-02T01:02:03Z")
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// cost_usd is deliberate: cctrace exists to answer what the agents cost, and the
	// usage answer shipped without it. Registering it here rather than loosening the
	// assertion keeps this test doing its job -- catching fields nobody meant to expose.
	assertJSONKeysExactly(t, body, "session_count", "cost_usd", "input_tokens", "output_tokens", "total_tokens", "work_time_seconds", "by_model")
	if body["session_count"] != float64(2) || body["total_tokens"] != float64(23) || body["work_time_seconds"] != float64(90) {
		t.Fatalf("totals = %+v", body)
	}
	models := body["by_model"].([]interface{})
	assertJSONKeysExactly(t, models[0].(map[string]interface{}), "model", "input_tokens", "output_tokens", "total_tokens")
}

func TestOpenAPIUsageAdminPreservesFiltersAndRejectsMalformedTimes(t *testing.T) {
	var got store.SessionOverviewFilter
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("admin-token", &store.DashboardUser{Role: "admin", IsActive: true}),
		usageAggregatesFn: func(_ context.Context, filter store.SessionOverviewFilter) (*store.UsageAggregate, error) {
			got = filter
			return &store.UsageAggregate{}, nil
		},
	}
	srv := newTestServer(m, nil)
	rec := openAPIRequest(t, srv, http.MethodGet,
		"/api/open/v1/usage?user_id=bob&profile_email=bob@example.com&login_email=login@example.com&since=2026-08-01T00:00:00Z&until=2026-08-31T00:00:00Z", "Bearer admin-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got.UserID != "bob" || got.ProfileEmail != "bob@example.com" || got.LoginEmail != "login@example.com" {
		t.Fatalf("admin filter changed: %+v", got)
	}
	bad := openAPIRequest(t, srv, http.MethodGet, "/api/open/v1/usage?since=yesterday", "Bearer admin-token")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("malformed time status = %d, want 400", bad.Code)
	}
}

func TestOpenAPIUsageMissingOwnerIDFailsClosed(t *testing.T) {
	var got store.SessionOverviewFilter
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("alice-token", &store.DashboardUser{Role: "user", Email: "alice@example.com", IsActive: true}),
		usageAggregatesFn: func(_ context.Context, filter store.SessionOverviewFilter) (*store.UsageAggregate, error) {
			got = filter
			return &store.UsageAggregate{}, nil
		},
	}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet,
		"/api/open/v1/usage?user_id=bob&profile_email=bob@example.com&since=2026-08-01T00:00:00Z&until=2026-08-31T00:00:00Z", "Bearer alice-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got.UserID != noAccessSentinel || got.ProfileEmail != "" || got.LoginEmail != "" {
		t.Fatalf("filter = %+v, want no-access sentinel", got)
	}
}

func TestDashboardSessionResponseStillIncludesRaw(t *testing.T) {
	m := &mockStore{listSessionRecordsFn: func(context.Context, store.SessionRecordFilter) ([]*store.SessionRecord, error) {
		return []*store.SessionRecord{{SessionID: "s1", RecordType: "assistant", Raw: json.RawMessage(`{"message":"kept for dashboard"}`)}}, nil
	}}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet, "/api/sessions", "")
	var body []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body[0]["raw"]; !ok {
		t.Fatal("legacy dashboard response lost raw field")
	}
}

func apiUserTokenLookup(token string, user *store.DashboardUser) func(context.Context, string) (*store.DashboardUser, error) {
	return func(_ context.Context, got string) (*store.DashboardUser, error) {
		if got == token {
			return user, nil
		}
		return nil, context.Canceled
	}
}

func openAPIRequest(t *testing.T, srv *Server, method, target, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func assertTimeFilter(t *testing.T, got *time.Time, want string) {
	t.Helper()
	if got == nil || got.Format(time.RFC3339) != want {
		t.Fatalf("time = %v, want %s", got, want)
	}
}

func assertJSONKeysExactly(t *testing.T, value map[string]interface{}, keys ...string) {
	t.Helper()
	want := make(map[string]bool, len(keys))
	for _, key := range keys {
		want[key] = true
	}
	for key := range value {
		if !want[key] {
			t.Errorf("unexpected key %q present in %+v", key, value)
		}
	}
	for _, key := range keys {
		if _, ok := value[key]; !ok {
			t.Errorf("allowed key %q missing from %+v", key, value)
		}
	}
}
