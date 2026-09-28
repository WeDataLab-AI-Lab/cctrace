export interface UserInfo {
  user_id: string;
  profile_email: string;
  login_emails: string[];
  total_cost: number;
}

export interface CostSummary {
  profile_email: string;
  user_id?: string;
  login_emails?: string[];
  user_team: string;
  model: string;
  agent: string; // "claude" | "codex"
  billing_provider: string; // "anthropic" | "openai"
  total_cost: number;
  total_input_tokens: number;
  total_output_tokens: number;
  request_count: number;
}

export interface ModelStat {
  model: string;
  profile_email?: string;
  user_team?: string;
  agent: string;
  billing_provider: string;
  total_cost: number;
  input_tokens: number;
  output_tokens: number;
  request_count: number;
}

export interface DailyStat {
  date: string; // YYYY-MM-DD
  cost_usd: number;
  input_tokens: number;
  output_tokens: number;
  event_count: number;
}

export interface ToolUsageSummary {
  tool_name: string;
  use_count: number;
  success_count: number;
  fail_count: number;
}

export interface WeeklyInsightHour {
  hour: number;
  session_count: number;
}

export interface WeeklyInsightProject {
  project_hash: string;
  project_hashes: string[];
  project_name: string;
  session_count: number;
  total_tokens: number;
}

export interface WeeklyInsightTask {
  task_type: string;
  prompt_count: number;
}

export interface WeeklyInsightTool {
  tool_name: string;
  use_count: number;
  fail_count: number;
}

export interface WeeklyInsightAgentSessions {
  agent: string;
  session_count: number;
}

export interface WeeklyInsights {
  agent_sessions: WeeklyInsightAgentSessions[];
  segment_count: number;
  uncovered_session_count: number;
  /** How many of the reader's own records the exclusion views hid this week.
   *  Zero means nothing of theirs was excluded — not that no exclusion exists. */
  excluded_record_count: number;
  hours: WeeklyInsightHour[];
  projects: WeeklyInsightProject[];
  tasks: WeeklyInsightTask[];
  tools: WeeklyInsightTool[];
  // Every typed prompt in the window, classified or not -- tasks' counts are a
  // subset of this. Compare against the sum of tasks' prompt_count to see what
  // fraction is actually categorized.
  typed_turn_count: number;
  /** Sessions whose records all carry effectively the same timestamp — real work
   *  with unusable times. Every hour bucket they land in is an artefact of when
   *  the file was written, not when the work happened (#686). */
  collapsed_timeline_sessions?: number;
}

// One work segment behind a Task types row's modal. No prompt or command text --
// the schema has no such column.
/** The drill-down page behind a Task types row.
 *
 *  `total` counts segments over the whole period; the card that opens this counts
 *  *prompts* carrying the task type. One segment can hold several such prompts, so
 *  the two numbers differ legitimately — which is why both belong on screen (#669). */
export interface TaskSegmentPage {
  segments: TaskSegment[];
  total: number;
  truncated: boolean;
}

export interface TaskSegment {
  session_id: string;
  start_ts: string;
  end_ts?: string;
  project_hash?: string;
  project_name?: string;
  tool_call_count: number;
  tool_fail_count: number;
  command_count: number;
  input_tokens: number;
  output_tokens: number;
  /** Whether tool_call_count was measured. Claude calls come from OTEL and
   *  tokens from the JSONL sync, so a machine without an exporter reports real
   *  tokens and zero calls — which is not the same as having used no tools.
   *  Codex calls come from its JSONL and are always measured. */
  tool_evidence: boolean;
  /** Whether tool_fail_count was measured. Codex records calls but no outcome,
   *  so this is false for Codex and its zero failures are unobserved. */
  tool_outcome_evidence: boolean;
  /** This segment's session carries no usable times: its whole record span fits
   *  inside a second. The work is real; the moment shown is when the file was
   *  written (#686). */
  timeline_collapsed?: boolean;
}

export interface OrganizationInsightProject {
  project_hash: string;
  project_hashes: string[];
  contributor_count: number;
  session_count: number;
  total_tokens: number;
}

export interface OrganizationInsights {
  available: boolean;
  active_users?: number;
  minimum_users: number;
  tasks?: Array<{ task_type: string; contributor_count: number; prompt_count: number }>;
  hours?: Array<{ hour: number; contributor_count: number; session_count: number }>;
  projects?: OrganizationInsightProject[];
  bottlenecks?: Array<{ tool_name: string; contributor_count: number; use_count: number; fail_count: number }>;
  typed_turn_count?: number;
}

export interface ToolTimeBucket {
  date: string;
  success_count: number;
  fail_count: number;
}

export interface ToolFailure {
  ts: string;
  session_id: string;
  user_id: string;
  profile_email: string;
  login_email: string;
  model: string;
  duration_ms?: number;
  attrs?: Record<string, unknown>;
}

export interface ToolDetail {
  timeseries: ToolTimeBucket[];
  failures: ToolFailure[];
}

// Provider-neutral form of one session record, built server-side (internal/sessionview).
// The conversation view reads only this; `raw` is kept for the raw record list.
export type SessionViewKind = 'message' | 'tool_call' | 'tool_result' | 'reasoning' | 'hidden';

export interface SessionViewToolCall {
  id?: string;
  name: string;
  input?: string;
}

export interface SessionViewToolResult {
  id?: string;
  output?: string;
  is_error?: boolean;
  // Recovered from SDK-harness prose, not a genuine tool_result block. Only fills a
  // pairing no genuine result makes; never shown on its own.
  inferred?: boolean;
}

export interface SessionRecordView {
  kind: SessionViewKind;
  role?: 'user' | 'assistant';
  text?: string;
  command?: string;
  command_args?: string;
  agent_task?: boolean;
  compact_summary?: boolean;
  tool_calls?: SessionViewToolCall[];
  tool_results?: SessionViewToolResult[];
  // The record uuid when it has one, otherwise a stable hash — the record's render/scroll identity.
  anchor_id: string;
  // Equal for the legacy and enriched stored copies of one source line; empty = never merged.
  dedupe_key: string;
}

export interface SessionRecord {
  ts: string;
  session_id: string;
  project_hash: string;
  repository_id?: string;
  repository_name?: string;
  repo_subpath?: string;
  commit_sha?: string;
  branch?: string;
  record_type: string;
  profile_email: string;
  model: string;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_create_tokens?: number;
  // Raw session JSON crosses the API boundary without a stable provider schema.
  // Consumers must narrow it before reading provider-specific fields.
  raw?: unknown;
  agent?: string;
  tool_name?: string;
  tool_call_id?: string;
  // Absent only from servers that predate it; such a record is treated as hidden.
  view?: SessionRecordView;
  // Lineage/source fields (forward-only; absent on records collected before the update).
  uuid?: string;
  parent_uuid?: string;
  is_sidechain?: boolean;
  agent_id?: string;
  forked_from_session?: string;
  forked_from_uuid?: string;
  source_file?: string;
  tool_use_id?: string;
  is_compact_summary?: boolean;
  is_meta?: boolean;
  prompt_source?: string;
  entrypoint?: string;
}

export interface Project {
  project_hash: string;
  project_name: string;
  git_remote_url: string;
  repository_id: string;
  repository_name: string;
  repo_subpath: string;
  updated_at: string;
  last_session_at: string | null;
}

export type ProjectRuleAgent = 'claude' | 'codex' | string;
export type ProjectRuleStatus = 'active' | 'missing' | 'deleted' | 'unreadable' | 'archived';
export type ProjectRuleCommentType = 'comment' | 'change_reason';

export interface ProjectRule {
  id: number;
  agent: ProjectRuleAgent;
  project_hash?: string;
  project_name?: string;
  repository_id?: string;
  repository_key: string;
  repository_name?: string;
  rule_path: string;
  rule_kind: string;
  rule_scope: string;
  title?: string;
  current_version_id?: number;
  current_content_hash?: string;
  current_status: ProjectRuleStatus;
  discovered_at: string;
  last_seen_at: string;
  updated_at: string;
}

export interface ProjectRuleListItem extends ProjectRule {
  version_count: number;
  comment_count: number;
}

export interface ProjectRuleListQuery {
  agent?: ProjectRuleAgent;
  repository_key?: string;
  repository_id?: string;
  project_hash?: string;
  status?: ProjectRuleStatus | 'all';
  query?: string;
  limit?: number;
  offset?: number;
}

export interface ProjectRuleListResponse {
  items: ProjectRuleListItem[];
  total: number;
}

export interface ProjectRuleVersion {
  id: number;
  rule_id: number;
  version_number: number;
  content_hash: string;
  content?: string;
  size_bytes: number;
  change_reason?: string;
  commit_sha?: string;
  branch?: string;
  applies_to?: string[];
  frontmatter?: Record<string, unknown>;
  raw_metadata?: Record<string, unknown>;
  created_by_profile_email?: string;
  created_by_user_id?: string;
  discovered_at: string;
}

export interface ProjectRuleComment {
  id: number;
  rule_id: number;
  version_id?: number;
  comment_type: ProjectRuleCommentType;
  author_profile_email?: string;
  author_user_id?: string;
  body: string;
  created_at: string;
  updated_at: string;
}

export interface ProjectRuleDetail {
  rule: ProjectRuleListItem;
  versions: ProjectRuleVersion[];
  comments: ProjectRuleComment[];
}

export interface ProjectRuleSnapshot {
  rule_path: string;
  rule_kind: string;
  rule_scope?: string;
  title?: string;
  status: ProjectRuleStatus;
  content_hash?: string;
  content?: string;
  size_bytes?: number;
  applies_to?: string[];
  frontmatter?: Record<string, unknown>;
  raw_metadata?: Record<string, unknown>;
  read_error?: string;
}

export interface ProjectRuleIngestRequest {
  profile_email: string;
  user_id: string;
  agent: ProjectRuleAgent;
  project_hash?: string;
  project_name?: string;
  repository_id?: string;
  repository_key?: string;
  repository_name?: string;
  commit_sha?: string;
  branch?: string;
  rules: ProjectRuleSnapshot[];
}

export interface ProjectRuleIngestResponse {
  inserted_rules: number;
  updated_rules: number;
  inserted_versions: number;
  unchanged_rules: number;
}

export interface CreateProjectRuleCommentRequest {
  version_id?: number;
  comment_type?: ProjectRuleCommentType;
  body: string;
}

export interface UpdateProjectRuleChangeReasonRequest {
  change_reason: string;
}

export interface SessionSummary {
  session_id: string;
  profile_email: string;
  project_hash: string;
  project_name?: string;
  model: string;
  start_time: string;
  end_time: string;
  input_tokens: number;
  output_tokens: number;
  record_count: number;
}

/** One contiguous stretch of a session that belonged to a single account. */
export interface SessionAccountSegment {
  /** Login email (Claude) or account uuid (Codex). Empty = no account identity. */
  account: string;
  agent: string;
  start_time: string;
  end_time: string;
  input_tokens: number;
  output_tokens: number;
  cost_usd: number;
  event_count: number;
}

export interface SessionOverview {
  session_id: string;
  profile_email: string;
  user_id?: string;
  login_email?: string;
  /**
   * How many distinct accounts this session's records belong to. Normally 1;
   * more means the account was switched mid-session, so login_email above is
   * only the dominant account and the token totals span all of them.
   */
  account_count?: number;
  /**
   * True when any record of this session had its account derived from the user's
   * login timeline rather than observed on the session itself — a session that
   * emitted no telemetry of its own carries no account until something infers
   * one. The detail header marks it so a guess is never read as a measurement.
   */
  login_email_inferred?: boolean;
  model: string;
  agent: string; // "claude" | "codex"
  start_time: string;
  end_time: string;
  input_tokens: number;
  output_tokens: number;
  cost_usd: number;
  event_count: number;
  has_sync: boolean;
  project_hash?: string;
  project_name?: string;
  entrypoint?: string; // cli | sdk-cli (headless) | claude-desktop; '' for legacy
  // Whole-session aggregate: true when any record in the session carries source_file.
  // Optional because older API builds don't send it — undefined means "unknown", not false.
  has_enriched?: boolean;
  // cctrace collector version, aggregated from session_records.cctrace_version.
  // Optional for the same reason as has_enriched: older API builds don't send it.
  cctrace_version?: string;
  // Claude Code client version, aggregated from otel_events.service_version.
  claude_version?: string;
}

export interface ModelDailyStat {
  date: string;
  model: string;
  cost_usd: number;
  input_tokens: number;
  output_tokens: number;
  event_count: number;
}

export interface UserDailyStat {
  date: string;
  user_id: string;
  profile_email: string;
  cost_usd: number;
  input_tokens: number;
  output_tokens: number;
  event_count: number;
}

export interface OtelEvent {
  ts: string;
  event_name: string;
  session_id?: string;
  prompt_id?: string;
  user_id?: string;
  profile_email?: string;
  login_email?: string;
  user_team?: string;
  org_id?: string;
  model?: string;
  cost_usd?: number;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_create_tokens?: number;
  duration_ms?: number;
  tool_name?: string;
  tool_decision?: string;
  tool_success?: boolean;
  speed?: string;
  service_version?: string;
  attrs?: Record<string, unknown>;
}

export interface OtelMetric {
  ts: string;
  metric_name: string;
  session_id?: string;
  user_id?: string;
  profile_email?: string;
  login_email?: string;
  user_team?: string;
  model?: string;
  value_double?: number;
  value_int?: number;
  dimensions?: Record<string, unknown>;
  agent?: string;
  billing_provider?: string;
}

export interface AuthUser {
  id: number;
  email: string;
  role: 'admin' | 'user';
  name: string;
  cctrace_user_id?: string;
  must_change_password?: boolean;
}

export interface DashboardUserInfo {
  id: number;
  email: string;
  role: 'admin' | 'user';
  name: string;
  team: string;
  is_active: boolean;
  created_at: string;
  cctrace_user_id: string;
  must_change_password?: boolean;
  has_api_token?: boolean;
}

export interface CreateUserRequest {
  email: string;
  name: string;
  team: string;
  role: 'admin' | 'user';
  cctrace_user_id: string;
}

export interface UpdateUserRequest {
  role?: 'admin' | 'user';
  name?: string;
  team?: string;
  is_active?: boolean;
  cctrace_user_id?: string;
}

export interface PrivacySetting {
  id: number;
  profile_email: string;
  scope_type: 'session' | 'project' | 'user';
  scope_value: string;
  created_at: string;
}

export interface ClientVersionInfo {
  profile_email: string;
  /** Display name of the dashboard account owning this address. Absent when the
   *  profile has no dashboard_users row. */
  name?: string;
  user_id?: string;
  client_version: string;
  client_os: string;
  client_arch: string;
  last_seen_at: string;
  /** Distinct client versions this account's records carried over the last day.
   *  Two is an ordinary upgrade on the day it happens; two that persist are a
   *  daemon replaced on disk while an old resident process kept running (#623).
   *  `client_versions` keeps one string per account and cannot show this. */
  concurrent_versions?: number;
  /** Whether this client is new enough to report its self-update state at all.
   *  False means unknown, not healthy — the installs furthest behind are
   *  exactly the ones that cannot report (#750). */
  update_reported?: boolean;
  /** The version the client is failing to reach, not the one it runs. */
  update_target_version?: string;
  update_fail_count?: number;
  update_first_failed_at?: string;
  update_last_failed_at?: string;
  /** The client's own error text. It names local paths, which is what makes it
   *  actionable. */
  update_fail_reason?: string;
}

export interface TableRetention {
  table: string;
  retention_days: number | null;
  compression_days: number | null;
  rows_approx: number;
  size_bytes: number;
}

export interface VolumeInfo {
  path: string;
  total_bytes: number;
  free_bytes: number;
  used_bytes: number;
}

export interface StorageReport {
  tables: TableRetention[];
  volume: VolumeInfo | null;
  // Axes pinned by an env var; the UI disables editing there (boot reconcile
  // would otherwise re-assert the env value over any UI edit).
  env_managed?: Record<string, boolean>;
}

export type RetentionAxis = 'otel' | 'session';

export interface RetentionPreview {
  table: string;
  current_days: number | null;
  new_days: number;
  oldest_ts: string | null;
  rows_to_drop: number;
}

export interface SetRetentionBody {
  axis: RetentionAxis;
  days: number;
  confirmed: boolean;
}

export interface PluginUsageSummary {
  command_name: string;
  agent?: string;
  profile_email: string;
  user_id: string;
  project_hash?: string;
  project_name?: string;
  repository_id?: string;
  repository_name?: string;
  repo_subpath?: string;
  has_git?: boolean;
  invocation_count: number;
  total_tokens: number;
  input_tokens: number;
  output_tokens: number;
}

export interface ExcludedAccount {
  login_email: string;
  reason: string;
  created_by: string;
  created_at: string;
  event_count: number;
  cost_usd: number;
  first_ts?: string;
  last_ts?: string;
  /** Billing accounts this address was seen with; the exclusion hides them too (#715). */
  linked_billing_accounts?: BillingAccountRef[];
}

/**
 * An exclusion list, with whether the usage rebuild an exclusion change queues is
 * still pending. The flag is on the list, not an item: after the last exclusion
 * is removed the list is empty and the rebuild is still owed.
 */
export interface ExclusionList<T> {
  accounts: T[];
  /** Left out (or null) when the server could not read it: not known. */
  usage_rebuild_pending?: boolean | null;
}

export interface BillingAccountRef {
  billing_provider: string;
  account_id: string;
}

/**
 * An account excluded by the id it is billed under, for accounts with no login
 * email to exclude by. Codex is the case: its rows carry no login email at all,
 * so an ExcludedAccount entry -- keyed on one, and checked to look like an
 * address -- can never match them.
 */
/**
 * Usage no rate matched, which cost figures count as $0 (#441). `model` is the
 * raw id as stored, never a display name, so it can be copied into a rate table.
 */
export interface UnpricedModel {
  agent: string;
  model: string;
  rows: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  first_ts: string;
  last_ts: string;
}

/** A model an admin declared "$0 is the right cost"; it leaves the unpriced list. */
export interface FlatRateModel {
  agent: string;
  model: string;
  reason: string;
  created_by: string;
  created_at: string;
}

export interface UnpricedModelsResponse {
  unpriced: UnpricedModel[];
  flat_rate: FlatRateModel[];
}

export interface ExcludedBillingAccount {
  billing_provider: string;
  account_id: string;
  reason: string;
  created_by: string;
  created_at: string;
  sample_count: number;
  event_count: number;
  first_ts?: string;
  last_ts?: string;
  /** Registered by the account's owner from Settings rather than by an admin (#716). */
  self_registered: boolean;
}

/**
 * A billing account seen in the viewer's own data, with whether it is excluded
 * and whether that exclusion is theirs to take back (#716).
 */
export interface ObservedBillingAccount {
  billing_provider: string;
  account_id: string;
  /** Seen with more than one login address: it bills several people. */
  shared: boolean;
  excluded: boolean;
  self_registered: boolean;
}

/**
 * One account's contribution to the coverage ratio. `unfitted` names why the
 * account is on neither side of the ratio; empty when it is on both.
 */
export interface CoverageGapAccount {
  login_email: string;
  account_id: string;
  plan: string;
  window_key: string;
  k_usd_per_pct: number;
  fit_intervals: number;
  measured_usd: number;
  implied_usd: number;
  sample_coverage: number;
  censored_seconds: number;
  unfitted?: string;
}

/**
 * How much of the subscription burn in a range cctrace measured. The burn side
 * is the provider's utilization percentage, so the dollar equivalent is fitted
 * (`k_method`) rather than read; `eligible: false` with a `reason` is a normal
 * answer, not a failure. Both sides are dollars: the factor is fitted on
 * cost_usd, not on a token count.
 */
/** One chart bucket's share of the unknown usage, in the chart's two units. */
export interface CoverageBucket {
  date: string;
  implied_usd: number;
  measured_usd: number;
  unknown_tokens: number;
  unknown_cost_usd: number;
}

export interface CoverageGap {
  eligible: boolean;
  reason?: string;
  measured_usd: number;
  implied_usd: number;
  coverage_ratio: number;
  sample_coverage: number;
  censored_fraction: number;
  accounts: CoverageGapAccount[];
  unfitted: string[];
  k_method: string;
  /** Present only when the request asked for the chart's bucketing. */
  buckets?: CoverageBucket[];
  /** The same computation over the sentence's wider scope, present only when it
   *  differs from the plotted range. */
  headline?: CoverageGap;
}

export interface APITokenInfo {
  id: number;
  name: string;
  token_hint: string;
  created_via: 'web' | 'api' | 'cli_read';
  is_active: boolean;
  is_expired: boolean;
  expires_at: string | null;
  created_at: string;
  rotated_at: string | null;
}

export interface APITokenSecret extends APITokenInfo {
  api_token: string;
}

export interface SkillUsageSummary {
  skill_name: string;
  agent: string;
  invoke_type: 'explicit' | 'implicit' | string;
  profile_email?: string;
  login_email?: string;
  user_id?: string;
  project_hash?: string;
  project_name?: string;
  repository_id?: string;
  repository_name?: string;
  repo_subpath?: string;
  has_git?: boolean;
  success_count: number;
  fail_count: number;
  total_count: number;
}

/**
 * One reading of one rate-limit window of one billing account.
 *
 * Keyed by (billing_provider, account_id, window_key, sampled_at): several
 * profiles report the same account at their own moments, which densifies one
 * series rather than duplicating it.
 */
export interface QuotaSample {
  billing_provider: string;
  account_id: string;
  window_key: string;
  sampled_at: string;
  used_pct: number;
  resets_at?: string;
  window_minutes?: number;
  severity?: string;
  is_active?: boolean;
  scope_label?: string;
  plan?: string;
  login_email?: string;
  profile_email?: string;
  /** 'observed' when the account was known at this instant, 'inferred' when it
   *  was chosen across a gap in the observation log. */
  attribution?: string;
}

/* ---------- Weekly AI report (slice 2) ---------- */

export type AIReportRunStatus = 'running' | 'completed' | 'failed' | 'canceled';

export type AIToolCallStatus = 'running' | 'ok' | 'failed' | 'timeout';

export interface AIRuntimeState {
  /** False while AI reports are switched off; nothing can start. */
  enabled: boolean;
  key: string;
  configured: boolean;
  available: boolean;
  reason: string | null;
}

export interface AIConsentState {
  granted: boolean;
  runtime_key: string;
  disclosure_version: string;
}

export interface AIToolCall {
  seq: number;
  tool: string;
  args: Record<string, unknown>;
  status: AIToolCallStatus;
  result_rows?: number | null;
  duration_ms?: number | null;
}

export interface AIRunError {
  code: string;
  message: string;
}

export interface AIReportRun {
  id: number;
  status: AIReportRunStatus;
  started_at: string;
  finished_at: string | null;
  error?: AIRunError | null;
  tool_calls: AIToolCall[];
}

/** Tokens are absent when the runtime did not report them — not zero. */
export interface AIReportUsage {
  reported: boolean;
  input_tokens?: number | null;
  cached_input_tokens?: number | null;
  output_tokens?: number | null;
}

/** Filled from the segment on every read; items no longer visible are dropped. */
export interface AIReportItemMeta {
  session_id: string;
  start_ts: string;
  project_name: string;
  agent: string;
  typed_turn_count: number;
  tool_call_count: number;
  tool_fail_count: number;
}

export interface AIReportItem {
  /** boundary_record_id as a string. */
  segment_id: string;
  title: string;
  reason: string;
  meta: AIReportItemMeta;
}

export interface AIReportProcess {
  tool_calls: AIToolCall[];
  segments_read: number;
}

export interface AIReport {
  run_id: number;
  generated_at: string;
  tz: string;
  runtime: string;
  model: string;
  usage: AIReportUsage;
  duration_ms: number;
  summary: string;
  items: AIReportItem[];
  process: AIReportProcess;
}

/** Which layer decided one part of a schedule. There is no environment layer:
 *  no variable sets a weekly firing. */
export type AIScheduleSource = 'user' | 'admin' | 'default';

/** When this user's report runs by itself, already resolved across their own
 *  setting, the admin's default and the built-in one. The sources let the
 *  screen mark a value the user has not chosen. */
export interface AIUserSchedule {
  enabled: boolean;
  enabled_source: AIScheduleSource;
  weekday: number;
  hour: number;
  minute: number;
  when_source: AIScheduleSource;
  tz: string;
  /** Null while automatic runs are off: there is no next firing to name. */
  next_run: string | null;
}

/** PUT /api/ai/schedule. A null clears that part and puts it back under the
 *  admin's default; a missing field is left as it was. */
export interface SetAIUserScheduleRequest {
  enabled?: boolean | null;
  weekday?: number | null;
  hour?: number | null;
  minute?: number | null;
  tz?: string;
}

/** GET /api/ai-reports?week=&tz= — facts only; the screen state is derived client-side. */
export interface AIReportsResponse {
  week: string;
  tz: string;
  since: string;
  until: string;
  in_progress: boolean;
  runtime: AIRuntimeState;
  consent: AIConsentState;
  segment_count: number;
  schedule: AIUserSchedule;
  /** Latest run for the week. */
  run: AIReportRun | null;
  report: AIReport | null;
}

export interface StartAIReportRequest {
  week: string;
  tz: string;
}

/** POST /api/ai-reports → 202. */
export interface StartAIReportResponse {
  run: AIReportRun;
}

export type AIReportErrorCode =
  | 'invalid_week'
  | 'future_week'
  | 'consent_required'
  | 'already_running'
  | 'no_records'
  | 'runtime_unconfigured'
  | 'runtime_unavailable'
  | 'runtime_account_changing';

/** GET /api/ai-reports/runs/{id}/events — SSE payloads by event name. */
export interface AIReportStreamToolCall {
  seq: number;
  tool: string;
  args: Record<string, unknown>;
}

export interface AIReportStreamToolResult {
  seq: number;
  status: AIToolCallStatus;
  result_rows?: number | null;
  duration_ms?: number | null;
}

export interface AIReportStreamStatus {
  status: AIReportRunStatus;
  error?: AIRunError | null;
}

export interface AIReportStreamReport {
  run_id: number;
}

/** GET /api/ai/consent. */
export interface AIConsentInfo {
  runtime_key: string;
  /** The active runtime's key. */
  runtime: string;
  /** Display label of the provider the data goes to, e.g. "OpenAI (Codex)". */
  provider: string;
  disclosure_version: string;
  granted: boolean;
  granted_at: string | null;
  sends: string[];
  not_sends: string[];
  provider_retention: string;
}

/** POST /api/ai/consent — 409 when either value is not current, 204 otherwise. */
export interface AIConsentRequest {
  runtime_key: string;
  disclosure_version: string;
}

/** Where a provider API key comes from. */
export type AdminAICredentialSource = 'env' | 'admin' | 'none';

/** A provider API key as the admin screen sees it: never the key, only its last 4 characters. */
export interface AdminAICredential {
  provider: string;
  source: AdminAICredentialSource;
  key_hint: string | null;
  env_var: string;
  /** Why the stored key cannot be used, e.g. it cannot be decrypted. */
  reason: string | null;
  updated_at: string | null;
}

/** One entry of GET /api/admin/ai `runtimes`. credential is null for codex-app-server,
 *  whose account is managed in the account section. */
export interface AdminAIRuntime {
  key: string;
  implemented: boolean;
  configured: boolean;
  available: boolean;
  reason: string | null;
  selected: boolean;
  provider: string;
  auth_mode: string;
  account_email: string;
  plan_type: string;
  used_percent: number | null;
  personal_account_warning: boolean;
  credential: AdminAICredential | null;
}

export interface AdminAIUsage {
  runs: number;
  failed: number;
  input_tokens: number;
  output_tokens: number;
}

/** Where an effective AI setting came from: an admin's choice, the server's env var, or the built-in default. */
export type AdminAISettingSource = 'admin' | 'env' | 'default';

/** Effective model settings for the next run. reasoning_effort '' with source 'default' means the model's own default. */
export interface AdminAISettings {
  /** The runtime these settings belong to. */
  runtime: string;
  model: string;
  reasoning_effort: string;
  base_url: string;
  source: { model: AdminAISettingSource; reasoning_effort: AdminAISettingSource; base_url: AdminAISettingSource };
  env_model: string;
  env_reasoning_effort: string;
  env_base_url: string;
}

/** GET /api/admin/ai (admin). settings is null when no runtime is chosen or the
 *  server has no AI report storage. */
export interface AdminAIResponse {
  /** Whether AI reports may run, and who decided. */
  enabled: boolean;
  enabled_source: AdminAISettingSource;
  /** CCTRACE_AI_ENABLED, or true for a set CCTRACE_AI_RUNTIME; null when neither. */
  env_enabled: boolean | null;
  runtimes: AdminAIRuntime[];
  /** '' when neither an admin nor CCTRACE_AI_RUNTIME chose a runtime. */
  selected_runtime: string;
  runtime_source: AdminAISettingSource;
  /** Why reports cannot run on the selection: nothing chosen, or the chosen
   *  runtime cannot run here. Null when the chosen runtime can run. */
  selection_reason: string | null;
  selection_reason_code: 'runtime_not_selected' | 'runtime_unavailable' | null;
  /** Why API keys cannot be registered, null when they can. */
  secrets_reason: string | null;
  /** CCTRACE_AI_RUNTIME, '' when unset. */
  env_runtime: string;
  model: string;
  settings: AdminAISettings | null;
  /** The default weekly firing every user follows until they change it. Present
   *  even with no runtime built: it still says what turning it on would do. */
  schedule: AdminAISchedule;
  usage_this_week: AdminAIUsage;
}

/** The administrator's default for automatic weekly runs, resolved against the
 *  built-in one so there is always a weekday and time to show. */
export interface AdminAISchedule {
  enabled: boolean;
  enabled_source: AIScheduleSource;
  weekday: number;
  hour: number;
  minute: number;
  when_source: AIScheduleSource;
  /** The zone the time is read in for a user who has saved none and has no
   *  report yet: the server's CCTRACE_AI_DEFAULT_TZ. Empty is UTC. */
  tz: string;
}

/** PUT /api/admin/ai/schedule. A null clears that part, handing it back to the
 *  built-in default; a missing field is left as it was. */
export interface UpdateAdminAIScheduleRequest {
  enabled?: boolean | null;
  weekday?: number | null;
  hour?: number | null;
  minute?: number | null;
}

export interface AdminAIReasoningEffort {
  reasoning_effort: string;
  description: string;
}

/** One entry of GET /api/admin/ai/models (hidden models excluded). */
export interface AdminAIModel {
  id: string;
  display_name: string;
  description: string;
  is_default: boolean;
  default_reasoning_effort: string;
  supported_reasoning_efforts: AdminAIReasoningEffort[];
}

export interface AdminAIModelsResponse {
  models: AdminAIModel[];
}

export type AdminAILoginStatus = 'pending' | 'succeeded' | 'failed' | 'canceled' | 'expired';

/** A ChatGPT device code login. error is set only when status is 'failed'. */
export interface AdminAIDeviceLogin {
  login_id: string;
  verification_url: string;
  user_code: string;
  expires_at: string;
  status: AdminAILoginStatus;
  error: string | null;
}

/** GET /api/admin/ai/account (admin). login is the pending device login, null when none. */
export interface AdminAIAccount {
  auth_mode: string;
  email: string;
  plan_type: string;
  env_managed: boolean;
  auth_file_is_symlink: boolean;
  login: AdminAIDeviceLogin | null;
}

/** POST /api/admin/ai/account/login. api_key is sent once and never returned. */
export type StartAdminAILoginRequest = { type: 'chatgpt_device_code' } | { type: 'api_key'; api_key: string };

/** PUT /api/admin/ai/runtime. */
/** PUT /api/admin/ai/enabled. */
export interface SetAdminAIEnabledResponse {
  enabled: boolean;
  enabled_source: AdminAISettingSource;
  env_enabled: boolean | null;
}

export interface SetAdminAIRuntimeResponse {
  selected_runtime: string;
  runtime_source: AdminAISettingSource;
  settings: AdminAISettings | null;
}

/** PUT /api/admin/ai/settings. Both fields are required; '' clears that admin override. */
export interface UpdateAdminAISettingsRequest {
  model: string;
  reasoning_effort: string;
  base_url?: string;
}
