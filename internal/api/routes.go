package api

import (
	"net/http"
)

func (s *Server) routes() {
	// Public routes (no auth required)
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/version", s.handleVersion)
	s.mux.HandleFunc("GET /api/auth/setup-status", s.handleSetupStatus)
	s.mux.Handle("POST /api/auth/setup", SyncRateLimiter(1, 5)(s.withCSRF(withRequestBodyLimit(maxRequestBodyBytes, http.HandlerFunc(s.handleSetup)))))
	// A small team normally logs in at human typing speed; allow five
	// simultaneous attempts and one additional attempt per second per route.
	loginRL := SyncRateLimiter(1, 5)
	s.mux.Handle("POST /api/auth/login", loginRL(s.withCSRF(withRequestBodyLimit(maxRequestBodyBytes, http.HandlerFunc(s.handleLogin)))))
	s.mux.Handle("POST /api/auth/refresh", s.withCSRF(withRequestBodyLimit(maxRequestBodyBytes, http.HandlerFunc(s.handleRefresh))))

	// CLI sync route (API_KEY auth)
	syncRL := SyncRateLimiter(200, 1000)
	syncHandler := s.withSyncBodyLimit(http.HandlerFunc(s.handleSync))
	if s.authMW != nil {
		syncHandler = s.authMW(syncHandler)
	}
	s.mux.Handle("POST /api/sync", syncRL(syncHandler))
	var syncCapabilitiesHandler http.Handler = http.HandlerFunc(s.handleSyncCapabilities)
	if s.authMW != nil {
		syncCapabilitiesHandler = s.authMW(syncCapabilitiesHandler)
	}
	s.mux.Handle("GET /api/sync/capabilities", syncRL(syncCapabilitiesHandler))
	syncExclusionsHandler := withRequestBodyLimit(maxExclusionQueryBodyBytes, http.HandlerFunc(s.handleSyncExclusions))
	if s.authMW != nil {
		syncExclusionsHandler = s.authMW(syncExclusionsHandler)
	}
	s.mux.Handle("POST /api/sync/exclusions", syncRL(syncExclusionsHandler))

	// CLI project rule snapshot route (API_KEY auth, same as sync)
	projectRulesIngestHandler := withRequestBodyLimit(maxProjectRulesRequestBodyBytes, http.HandlerFunc(s.handleProjectRulesIngest))
	if s.authMW != nil {
		projectRulesIngestHandler = s.authMW(projectRulesIngestHandler)
	}
	s.mux.Handle("POST /api/project-rules", syncRL(projectRulesIngestHandler))

	// CLI policy-target report (API_KEY auth, same as sync)
	clientPolicyReportHandler := withRequestBodyLimit(maxClientPolicyReportBodyBytes, http.HandlerFunc(s.handleClientPolicyReport))
	if s.authMW != nil {
		clientPolicyReportHandler = s.authMW(clientPolicyReportHandler)
	}
	s.mux.Handle("POST /api/client-policy-report", syncRL(clientPolicyReportHandler))

	// Quota snapshot write route (API_KEY auth, same as sync)
	quotaWriteHandler := withRequestBodyLimit(maxQuotaRequestBodyBytes, http.HandlerFunc(s.handleQuotaWrite))
	if s.authMW != nil {
		quotaWriteHandler = s.authMW(quotaWriteHandler)
	}
	s.mux.Handle("POST /api/quota", quotaWriteHandler)

	// Quota history ingest (API_KEY auth, same as sync).
	//
	// Rate-limited, unlike POST /api/quota above: this one accepts batches, and
	// the Codex backfill sends thousands of rows in a burst.
	quotaSamplesHandler := withRequestBodyLimit(maxQuotaSamplesRequestBodyBytes, http.HandlerFunc(s.handleQuotaSamplesWrite))
	if s.authMW != nil {
		quotaSamplesHandler = s.authMW(quotaSamplesHandler)
	}
	s.mux.Handle("POST /api/quota-samples", syncRL(quotaSamplesHandler))

	// Session observation ingest (API_KEY auth, same as sync). Rate-limited like
	// the sync routes: the collector posts it on its own timer, beside sync.
	sessionObservationsHandler := withRequestBodyLimit(maxSessionObservationsRequestBodyBytes, http.HandlerFunc(s.handleSessionObservationsWrite))
	if s.authMW != nil {
		sessionObservationsHandler = s.authMW(sessionObservationsHandler)
	}
	s.mux.Handle("POST /api/session-observations", syncRL(sessionObservationsHandler))

	// CLI user_id check route (public — used during cctrace init before token issuance)
	checkUIDRL := SyncRateLimiter(1, 5)
	s.mux.Handle("GET /api/users/check-uid", checkUIDRL(http.HandlerFunc(s.handleCheckUserID)))

	// CLI auth route (public — password-based auth, issues per-user token)
	cliAuthRL := SyncRateLimiter(1, 5)
	s.mux.Handle("POST /api/cli/auth", cliAuthRL(withRequestBodyLimit(maxRequestBodyBytes, http.HandlerFunc(s.handleCLIAuth))))
	s.mux.Handle("POST /api/cli/read-token", cliAuthRL(withRequestBodyLimit(maxRequestBodyBytes, http.HandlerFunc(s.handleCLIReadToken))))

	// External read API (per-user API token auth; separate from ingestion auth).
	// Per-token, not global: the point of this API is that people write their own
	// scripts against it, and a shared bucket means one person's loop refuses everyone
	// else. Unverified tokens share the anonymous bucket so an attacker cannot buy a
	// fresh budget by inventing a new token per request -- this middleware runs ahead
	// of authentication and cannot ask the database who is calling.
	// Two layers, and the sizing between them is the whole design.
	//
	// The outer gate is shared by everyone. It exists to cap what unauthenticated
	// traffic can make the auth lookup do -- not to ration honest callers. If its
	// budget is anywhere near what real users consume, it is spent by whoever is
	// loudest and the per-user limiter behind it never gets to speak: a caller who
	// sent nothing is refused for someone else's burst, which is the exact symptom
	// this design set out to remove.
	//
	// So the gate must outrun the per-user rate times the callers we expect at once.
	// TestOpenAPIGateOutrunsConcurrentUsers pins that relationship; changing one
	// constant without the other turns the gate back into a shared ration.
	openReadRL := func(next http.Handler) http.Handler {
		return SyncRateLimiter(openAPIGateRPS, openAPIGateBurst)(
			s.openAPIMiddleware(
				UserRateLimiter(openAPIUserRPS, openAPIUserBurst)(next)))
	}
	s.mux.Handle("GET /api/open/v1/sessions", openReadRL((http.HandlerFunc(s.handleOpenAPIListSessions))))
	s.mux.Handle("GET /api/open/v1/sessions/{session_id}", openReadRL((http.HandlerFunc(s.handleOpenAPIGetSession))))
	s.mux.Handle("GET /api/open/v1/events", openReadRL((http.HandlerFunc(s.handleOpenAPIListEvents))))
	s.mux.Handle("GET /api/open/v1/usage", openReadRL((http.HandlerFunc(s.handleOpenAPIUsage))))
	s.mux.Handle("GET /api/open/v1/tools", openReadRL((http.HandlerFunc(s.handleOpenAPITools))))
	s.mux.Handle("GET /api/open/v1/tools/{tool_name}", openReadRL((http.HandlerFunc(s.handleOpenAPIToolDetail))))
	s.mux.Handle("GET /api/open/v1/projects", openReadRL(http.HandlerFunc(s.handleOpenAPIProjects)))
	s.mux.Handle("GET /api/open/v1/plugins", openReadRL((http.HandlerFunc(s.handleOpenAPIPlugins))))
	s.mux.Handle("GET /api/open/v1/skills", openReadRL((http.HandlerFunc(s.handleOpenAPISkills))))
	s.mux.Handle("GET /api/open/v1/rules", openReadRL((http.HandlerFunc(s.handleOpenAPIRules))))
	s.mux.Handle("GET /api/open/v1/rules/{rule_id}", openReadRL((http.HandlerFunc(s.handleOpenAPIRuleDetail))))
	s.mux.Handle("GET /api/open/v1/metrics", openReadRL((http.HandlerFunc(s.handleOpenAPIMetrics))))
	s.mux.Handle("GET /api/open/v1/organization-insights", openReadRL(http.HandlerFunc(s.handleOpenAPIOrganizationInsights)))
	s.mux.Handle("GET /api/open/v1/weekly-insights", openReadRL(http.HandlerFunc(s.handleOpenAPIWeeklyInsights)))
	s.mux.Handle("GET /api/open/v1/practices", openReadRL(http.HandlerFunc(s.handleOpenAPIPractices)))

	// Dashboard routes (JWT auth required)
	dashMW := func(next http.Handler) http.Handler {
		return s.dashboardMW(s.withCSRF(withRequestBodyLimit(maxRequestBodyBytes, next)))
	}

	// Auth routes requiring login
	s.mux.Handle("POST /api/auth/logout", dashMW(http.HandlerFunc(s.handleLogout)))
	s.mux.Handle("GET /api/auth/me", dashMW(http.HandlerFunc(s.handleMe)))
	s.mux.Handle("POST /api/auth/change-password", dashMW(http.HandlerFunc(s.handleChangePassword)))
	s.mux.Handle("GET /api/auth/api-tokens", dashMW(http.HandlerFunc(s.handleListOwnAPITokens)))
	s.mux.Handle("POST /api/auth/api-tokens", dashMW(s.requireTokenPasswordChanged(http.HandlerFunc(s.handleCreateOwnAPIToken))))
	s.mux.Handle("PATCH /api/auth/api-tokens/{id}", dashMW(s.requireTokenPasswordChanged(http.HandlerFunc(s.handleUpdateOwnAPIToken))))
	s.mux.Handle("POST /api/auth/api-tokens/{id}/rotate", dashMW(s.requireTokenPasswordChanged(http.HandlerFunc(s.handleRotateOwnAPIToken))))
	s.mux.Handle("DELETE /api/auth/api-tokens/{id}", dashMW(http.HandlerFunc(s.handleDeleteOwnAPIToken)))
	s.mux.Handle("GET /api/weekly-insights", dashMW(http.HandlerFunc(s.handleDashboardWeeklyInsights)))
	s.mux.Handle("GET /api/task-segments", dashMW(http.HandlerFunc(s.handleTaskSegments)))
	// Where the token recount stands, for the note every dashboard page shows
	// while it runs.
	s.mux.Handle("GET /api/maintenance/status", dashMW(http.HandlerFunc(s.handleMaintenanceStatus)))

	// Weekly AI reports. Runs are addressed by run id and visible only to their
	// owner; the events route is SSE and clears its own write deadline.
	s.mux.Handle("GET /api/ai-reports", dashMW(http.HandlerFunc(s.handleGetAIReports)))
	s.mux.Handle("POST /api/ai-reports", dashMW(http.HandlerFunc(s.handleStartAIReport)))
	s.mux.Handle("GET /api/ai-reports/runs/{id}/events", dashMW(http.HandlerFunc(s.handleAIRunEvents)))
	s.mux.Handle("DELETE /api/ai-reports/runs/{id}", dashMW(http.HandlerFunc(s.handleCancelAIRun)))
	s.mux.Handle("GET /api/ai/consent", dashMW(http.HandlerFunc(s.handleGetAIConsent)))
	s.mux.Handle("POST /api/ai/consent", dashMW(http.HandlerFunc(s.handlePostAIConsent)))
	s.mux.Handle("PUT /api/ai/schedule", dashMW(http.HandlerFunc(s.handleSetAIUserSchedule)))
	s.mux.Handle("GET /api/admin/ai", dashMW(http.HandlerFunc(s.handleAdminAI)))
	s.mux.Handle("GET /api/admin/ai/models", dashMW(http.HandlerFunc(s.handleAdminAIModels)))
	s.mux.Handle("PUT /api/admin/ai/settings", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleSetAdminAISettings))))
	s.mux.Handle("PUT /api/admin/ai/records", dashMW(http.HandlerFunc(s.handleSetAdminAIRecords)))
	s.mux.Handle("GET /api/admin/ai/records/vocabulary", dashMW(http.HandlerFunc(s.handleGetAdminAIRecordVocabulary)))
	s.mux.Handle("PUT /api/admin/ai/records/vocabulary", dashMW(http.HandlerFunc(s.handleSetAdminAIRecordVocabulary)))
	s.mux.Handle("POST /api/admin/ai/records/vocabulary/merge", dashMW(http.HandlerFunc(s.handleMergeAdminAIRecordVocabulary)))
	s.mux.Handle("PUT /api/admin/ai/runtime", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleSetAdminAIRuntime))))
	s.mux.Handle("PUT /api/admin/ai/enabled", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleSetAdminAIEnabled))))
	s.mux.Handle("PUT /api/admin/ai/schedule", dashMW(http.HandlerFunc(s.handleSetAdminAISchedule)))
	s.mux.Handle("PUT /api/admin/ai/run-policy", dashMW(http.HandlerFunc(s.handleSetAdminAIRunPolicy)))
	s.mux.Handle("PUT /api/admin/ai/providers/{provider}/key", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleSetAdminAIProviderKey))))
	s.mux.Handle("DELETE /api/admin/ai/providers/{provider}/key", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleDeleteAdminAIProviderKey))))
	s.mux.Handle("GET /api/admin/ai/account", dashMW(http.HandlerFunc(s.handleAdminAIAccount)))
	s.mux.Handle("POST /api/admin/ai/account/login", dashMW(http.HandlerFunc(s.handleAdminAILogin)))
	s.mux.Handle("GET /api/admin/ai/account/login/{id}", dashMW(http.HandlerFunc(s.handleAdminAILoginStatus)))
	s.mux.Handle("DELETE /api/admin/ai/account/login/{id}", dashMW(http.HandlerFunc(s.handleCancelAdminAILogin)))
	s.mux.Handle("POST /api/admin/ai/account/logout", dashMW(http.HandlerFunc(s.handleAdminAILogout)))

	// Admin routes
	s.mux.Handle("GET /api/admin/users", dashMW(http.HandlerFunc(s.handleListDashboardUsers)))
	s.mux.Handle("POST /api/admin/users", dashMW(http.HandlerFunc(s.handleCreateDashboardUser)))
	s.mux.Handle("PATCH /api/admin/users/{id}", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleUpdateDashboardUser))))
	s.mux.Handle("POST /api/admin/users/{id}/reset-password", dashMW(http.HandlerFunc(s.handleResetPassword)))
	s.mux.Handle("POST /api/admin/users/{id}/revoke-token", dashMW(http.HandlerFunc(s.handleRevokeApiToken)))
	s.mux.Handle("GET /api/admin/otel-user-ids", dashMW(http.HandlerFunc(s.handleOtelUserIDs)))
	s.mux.Handle("GET /api/admin/account-switches", dashMW(http.HandlerFunc(s.handleAccountSwitchStats)))
	s.mux.Handle("GET /api/admin/backfill-preview", dashMW(http.HandlerFunc(s.handleBackfillPreview)))
	s.mux.Handle("GET /api/admin/inference-preview", dashMW(http.HandlerFunc(s.handleInferencePreview)))
	s.mux.Handle("GET /api/admin/codex-inferred-revert-preview", dashMW(http.HandlerFunc(s.handleCodexInferredRevertPreview)))
	s.mux.Handle("GET /api/admin/codex-account-preview", dashMW(http.HandlerFunc(s.handleCodexAccountPreview)))
	s.mux.Handle("GET /api/admin/codex-attribution-preview", dashMW(http.HandlerFunc(s.handleCodexAttributionPreview)))
	s.mux.Handle("GET /api/admin/client-versions", dashMW(http.HandlerFunc(s.handleListClientVersions)))
	s.mux.Handle("GET /api/admin/retention", dashMW(http.HandlerFunc(s.handleRetention)))
	s.mux.Handle("GET /api/admin/organization-insights", dashMW(http.HandlerFunc(s.handleDashboardOrganizationInsights)))
	s.mux.Handle("POST /api/admin/task-types/backfill", dashMW(http.HandlerFunc(s.handleTaskTypeBackfill)))
	s.mux.Handle("GET /api/admin/retention/preview", dashMW(http.HandlerFunc(s.handleRetentionPreview)))
	s.mux.Handle("PATCH /api/admin/retention", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleSetRetention))))
	s.mux.Handle("GET /api/admin/excluded-accounts", dashMW(http.HandlerFunc(s.handleListExcludedAccounts)))
	s.mux.Handle("POST /api/admin/excluded-accounts", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleExcludeAccount))))
	s.mux.Handle("DELETE /api/admin/excluded-accounts", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleRemoveExcludedAccount))))

	// The billing-id counterpart, for accounts with no login email to exclude by.
	s.mux.Handle("GET /api/admin/excluded-billing-accounts", dashMW(http.HandlerFunc(s.handleListExcludedBillingAccounts)))
	s.mux.Handle("POST /api/admin/excluded-billing-accounts", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleExcludeBillingAccount))))
	s.mux.Handle("DELETE /api/admin/excluded-billing-accounts", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleRemoveExcludedBillingAccount))))

	// Usage no rate matched, and the models an admin declared free by design (#441).
	s.mux.Handle("GET /api/admin/unpriced-models", dashMW(http.HandlerFunc(s.handleListUnpricedModels)))
	s.mux.Handle("POST /api/admin/flat-rate-models", dashMW(http.HandlerFunc(s.handleMarkFlatRateModel)))
	s.mux.Handle("DELETE /api/admin/flat-rate-models", dashMW(http.HandlerFunc(s.handleUnmarkFlatRateModel)))

	// Any signed-in user, for billing accounts seen in their own data (#716). Lands
	// in the same table as the admin routes above.
	s.mux.Handle("GET /api/self-exclusions/billing-accounts", dashMW(http.HandlerFunc(s.handleListObservedBillingAccounts)))
	// One budget for both writes, keyed on the user: alternating them is the loop.
	selfExclusionRL := UserRateLimiter(selfExclusionRPS, selfExclusionBurst)
	s.mux.Handle("POST /api/self-exclusions/billing-accounts", dashMW(s.withSessionRecordPolicyMutation(selfExclusionRL(http.HandlerFunc(s.handleSelfExcludeBillingAccount)))))
	s.mux.Handle("DELETE /api/self-exclusions/billing-accounts", dashMW(s.withSessionRecordPolicyMutation(selfExclusionRL(http.HandlerFunc(s.handleRemoveSelfExcludedBillingAccount)))))

	// Session deletion. Unlike excluded accounts above -- which hide rows and can be
	// undone -- these remove rows and write a tombstone ingest honours, so a delete
	// survives the next sync. Authorization is per-handler, not per-route: deleting
	// your own session and blocking a whole project are different privileges.
	s.mux.Handle("DELETE /api/sessions/{session_id}", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleDeleteSession))))
	s.mux.Handle("GET /api/deletion-policy", dashMW(http.HandlerFunc(s.handleGetDeletionPolicy)))
	s.mux.Handle("DELETE /api/projects", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleDeleteProject))))
	s.mux.Handle("GET /api/projects/session-count", dashMW(http.HandlerFunc(s.handleProjectSessionCount)))
	s.mux.Handle("PUT /api/admin/deletion-policy", dashMW(http.HandlerFunc(s.handleSetDeletionPolicy)))
	s.mux.Handle("GET /api/blocked-projects", dashMW(http.HandlerFunc(s.handleListBlockedProjects)))
	s.mux.Handle("DELETE /api/blocked-projects", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleUnblockProject))))

	s.mux.Handle("GET /api/sessions/{session_id}/analysis", dashMW(http.HandlerFunc(s.handleSessionAnalysis)))
	s.mux.Handle("GET /api/sessions/{session_id}/trace/index", dashMW(http.HandlerFunc(s.handleSessionTraceIndex)))
	s.mux.Handle("GET /api/sessions/{session_id}/conversation/timeline", dashMW(http.HandlerFunc(s.handleConversationTimeline)))
	s.mux.Handle("GET /api/sessions/{session_id}/inspection", dashMW(http.HandlerFunc(s.handleSessionInspection)))
	s.mux.Handle("POST /api/sessions/{session_id}/trace/demand", dashMW(http.HandlerFunc(s.handleTraceDemand)))
	s.mux.Handle("DELETE /api/sessions/{session_id}/trace/demand/{lease_id}", dashMW(http.HandlerFunc(s.handleTraceRelease)))
	s.mux.Handle("GET /api/sessions/{session_id}/trace/overview", dashMW(http.HandlerFunc(s.handleTraceOverview)))
	s.mux.Handle("GET /api/sessions/{session_id}/trace/events", dashMW(http.HandlerFunc(s.handleTraceEvents)))
	s.mux.Handle("GET /api/sessions/{session_id}/trace/context", dashMW(http.HandlerFunc(s.handleTraceContext)))
	s.mux.Handle("GET /api/sessions/{session_id}/trace/nodes", dashMW(http.HandlerFunc(s.handleTraceNodes)))
	s.mux.Handle("GET /api/sessions/{session_id}/trace/nodes/{node_id}", dashMW(http.HandlerFunc(s.handleTraceNodeDetail)))
	s.mux.Handle("GET /api/sessions/{session_id}/turns/{turn_key}/context", dashMW(http.HandlerFunc(s.handleTurnContext)))
	s.mux.Handle("GET /api/session-calendar", dashMW(http.HandlerFunc(s.handleSessionCalendar)))
	s.mux.Handle("GET /api/session-search", dashMW(http.HandlerFunc(s.handleSessionRecordSearch)))
	s.mux.Handle("GET /api/session-records/consent", dashMW(http.HandlerFunc(s.handleGetSessionRecordConsent)))
	s.mux.Handle("POST /api/session-records/consent", dashMW(http.HandlerFunc(s.handlePostSessionRecordConsent)))
	s.mux.Handle("DELETE /api/session-records/consent", dashMW(http.HandlerFunc(s.handleDeleteSessionRecordConsent)))
	s.mux.Handle("GET /api/session-records/status", dashMW(http.HandlerFunc(s.handleSessionRecordStatus)))
	s.mux.Handle("POST /api/session-records/refresh", dashMW(http.HandlerFunc(s.handleSessionRecordRefresh)))
	s.mux.Handle("POST /api/sessions/{session_id}/record/regenerate", dashMW(http.HandlerFunc(s.handleSessionRecordRegenerate)))
	s.mux.Handle("DELETE /api/session-records", dashMW(http.HandlerFunc(s.handleDeleteOwnSessionRecords)))

	// Private dashboard notes and classifications.
	s.mux.Handle("GET /api/sessions/{session_id}/personal", dashMW(http.HandlerFunc(s.handleGetPersonalSession)))
	s.mux.Handle("GET /api/sessions/{session_id}/personal/overview", dashMW(http.HandlerFunc(s.handleGetPersonalSessionOverview)))
	s.mux.Handle("PUT /api/sessions/{session_id}/personal", dashMW(http.HandlerFunc(s.handlePutPersonalSession)))
	s.mux.Handle("DELETE /api/sessions/{session_id}/personal", dashMW(http.HandlerFunc(s.handleDeletePersonalSession)))
	s.mux.Handle("GET /api/me/note", dashMW(http.HandlerFunc(s.handleGetPersonalGlobalNote)))
	s.mux.Handle("PUT /api/me/note", dashMW(http.HandlerFunc(s.handlePutPersonalGlobalNote)))
	s.mux.Handle("DELETE /api/me/note", dashMW(http.HandlerFunc(s.handleDeletePersonalGlobalNote)))
	s.mux.Handle("GET /api/me/session-notes", dashMW(http.HandlerFunc(s.handlePersonalSessionNotes)))
	s.mux.Handle("GET /api/me/session-classifications/counts", dashMW(http.HandlerFunc(s.handlePersonalSessionCounts)))
	s.mux.Handle("GET /api/me/session-groups", dashMW(http.HandlerFunc(s.handleGetSessionGroups)))
	s.mux.Handle("GET /api/me/session-groups/members", dashMW(http.HandlerFunc(s.handleGetSessionGroupMembers)))
	s.mux.Handle("PUT /api/me/session-groups/dismiss", dashMW(http.HandlerFunc(s.handleUpdateSessionGroupDismissals)))
	s.mux.Handle("DELETE /api/me/session-groups/dismiss", dashMW(http.HandlerFunc(s.handleUpdateSessionGroupDismissals)))

	// Privacy routes
	s.mux.Handle("GET /api/privacy", dashMW(http.HandlerFunc(s.handleListPrivacy)))
	s.mux.Handle("POST /api/privacy", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleSetPrivacy))))
	s.mux.Handle("DELETE /api/privacy", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleRemovePrivacy))))

	// Data query routes (JWT auth, with role-based filtering)
	s.mux.Handle("GET /api/events", dashMW(http.HandlerFunc(s.handleListEvents)))
	s.mux.Handle("GET /api/events/count", dashMW(http.HandlerFunc(s.handleCountEvents)))
	s.mux.Handle("GET /api/metrics", dashMW(http.HandlerFunc(s.handleListMetrics)))
	s.mux.Handle("GET /api/cost/by-user", dashMW(http.HandlerFunc(s.handleCostByUser)))
	s.mux.Handle("GET /api/cost/by-team", dashMW(http.HandlerFunc(s.handleCostByTeam)))
	s.mux.Handle("GET /api/cost/by-model", dashMW(http.HandlerFunc(s.handleCostByModel)))
	s.mux.Handle("GET /api/tools", dashMW(http.HandlerFunc(s.handleToolUsage)))
	s.mux.Handle("GET /api/tools/detail", dashMW(http.HandlerFunc(s.handleToolDetail)))
	s.mux.Handle("GET /api/plugins", dashMW(http.HandlerFunc(s.handlePluginUsage)))
	s.mux.Handle("GET /api/skills", dashMW(http.HandlerFunc(s.handleSkillUsage)))
	s.mux.Handle("GET /api/stats/daily", dashMW(http.HandlerFunc(s.handleDailyStats)))
	s.mux.Handle("GET /api/stats/timeseries", dashMW(http.HandlerFunc(s.handleTimeSeriesStats)))
	s.mux.Handle("GET /api/stats/timeseries-by-model", dashMW(http.HandlerFunc(s.handleTimeSeriesStatsByModel)))
	s.mux.Handle("GET /api/stats/timeseries-by-user", dashMW(http.HandlerFunc(s.handleTimeSeriesStatsByUser)))
	s.mux.Handle("GET /api/stats/latest-activity", dashMW(http.HandlerFunc(s.handleLatestActivity)))
	s.mux.Handle("GET /api/stats/coverage-gap", dashMW(http.HandlerFunc(s.handleCoverageGap)))
	s.mux.Handle("GET /api/accounts", dashMW(http.HandlerFunc(s.handleAccounts)))
	s.mux.Handle("GET /api/sessions", dashMW(http.HandlerFunc(s.handleListSessions)))
	s.mux.Handle("GET /api/session-summaries", dashMW(http.HandlerFunc(s.handleListSessionSummaries)))
	s.mux.Handle("GET /api/session-overview", dashMW(http.HandlerFunc(s.handleListSessionOverview)))
	s.mux.Handle("GET /api/session-overview/count", dashMW(http.HandlerFunc(s.handleCountSessionOverview)))
	s.mux.Handle("GET /api/session-overview/categories", dashMW(http.HandlerFunc(s.handleSessionOverviewCategories)))
	s.mux.Handle("GET /api/session-account-segments", dashMW(http.HandlerFunc(s.handleSessionAccountSegments)))
	s.mux.Handle("GET /api/projects", dashMW(http.HandlerFunc(s.handleListProjects)))
	s.mux.Handle("GET /api/project-rules", dashMW(http.HandlerFunc(s.handleListProjectRules)))
	s.mux.Handle("GET /api/project-rules/{id}", dashMW(http.HandlerFunc(s.handleGetProjectRuleDetail)))
	s.mux.Handle("POST /api/project-rules/{id}/comments", dashMW(http.HandlerFunc(s.handleCreateProjectRuleComment)))
	s.mux.Handle("PATCH /api/project-rules/{id}/versions/{version_id}/change-reason", dashMW(http.HandlerFunc(s.handleUpdateProjectRuleChangeReason)))
	s.mux.Handle("GET /api/users", dashMW(http.HandlerFunc(s.handleListUsers)))
	s.mux.Handle("GET /api/users/names", dashMW(http.HandlerFunc(s.handleUserNameMap)))
	s.mux.Handle("GET /api/quota", dashMW(http.HandlerFunc(s.handleQuotaRead)))
	s.mux.Handle("GET /api/quota-samples", dashMW(http.HandlerFunc(s.handleQuotaSamplesRead)))
	s.mux.Handle("GET /api/me/quota-bindings", dashMW(http.HandlerFunc(s.handleListMyQuotaBindings)))
	s.mux.Handle("POST /api/me/quota-bindings", dashMW(http.HandlerFunc(s.handleApplyQuotaBinding)))
	s.mux.Handle("GET /api/me/quota-usage", dashMW(http.HandlerFunc(s.handleGetMyQuotaUsage)))
	s.mux.Handle("GET /api/me/quota-usage/window-comparison", dashMW(http.HandlerFunc(s.handleGetMyQuotaWindowComparison)))
	quotaOwnerSamplesHandler := withRequestBodyLimit(maxQuotaOwnerSamplesBody, http.HandlerFunc(s.handleInsertMyQuotaOwnerSamples))
	if s.authMW != nil {
		quotaOwnerSamplesHandler = s.authMW(quotaOwnerSamplesHandler)
	}
	s.mux.Handle("POST /api/me/quota-usage/samples", syncRL(quotaOwnerSamplesHandler))
	s.mux.Handle("GET /api/admin/quota-bindings", dashMW(http.HandlerFunc(s.handleListAdminQuotaBindings)))
	s.mux.Handle("POST /api/admin/quota-bindings/{id}/approve", dashMW(http.HandlerFunc(s.handleApproveQuotaBinding)))
	s.mux.Handle("POST /api/admin/quota-bindings/{id}/revoke", dashMW(http.HandlerFunc(s.handleRevokeQuotaBinding)))
	s.mux.Handle("DELETE /api/users/data", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleDeleteUserData))))
	s.mux.Handle("POST /api/users/merge", dashMW(s.withSessionRecordPolicyMutation(http.HandlerFunc(s.handleMergeUsers))))
}
