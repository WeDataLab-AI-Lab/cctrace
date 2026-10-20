import { refreshAfterUnauthorized, setAuthFailureHandler } from './api-auth';
import type { CoverageGap, SessionOverview, SessionRecord, UpdateAdminSessionRecordSettingsRequest } from './types';
import type { CalendarSession, SessionCalendar } from './session-calendar-types';
import type { SessionRecordScope } from './session-record-scope';
import type {
  AnalysisRepoStatus,
  JudgementBucket,
  PlanFilter,
  SessionAnalysis,
  SessionCategoryCounts,
  SessionCategoryKey,
  SessionClassification,
  SessionClassificationCounts,
  SessionRecordConsentInfo,
  SessionRecordRangeStatus,
  SessionRecordSearchField,
  SessionRecordSearchResult,
  SessionRecord as AnalysisSessionRecord,
  SessionRecordVocabulary,
  SessionBucketCounts,
  SessionRepoStatusResponse,
  SessionSort,
} from './session-analysis-types';

const BASE_URL = process.env.NEXT_PUBLIC_API_URL || (typeof window !== 'undefined' ? window.location.origin : 'http://localhost:8080');

export function buildUrl(path: string, params?: Record<string, string>): string {
  const url = new URL(path, BASE_URL);
  if (params) {
    Object.entries(params).forEach(([k, v]) => {
      if (v) url.searchParams.set(k, v);
    });
  }
  return url.toString();
}

interface LatestActivityRequest {
  readonly profileEmail?: string;
  readonly loginEmail?: string;
  readonly projectHash?: string;
  readonly projectHashes?: readonly string[];
  readonly userID?: string;
  readonly agent?: string;
  readonly modelCategory?: string;
  readonly model?: string;
}

const projectHashParams = (projectHash?: string, projectHashes?: readonly string[]): Record<string, string> => {
  if (projectHashes !== undefined) {
    const members = projectHashes.filter((hash) => hash);
    if (members.length === 0) throw new Error('project identity member set is empty');
    return { project_hashes: members.join(',') };
  }
  return { project_hash: projectHash || '' };
};

interface SessionOverviewRequest {
  readonly analysis?: boolean;
  readonly category?: SessionCategoryKey;
  readonly query?: string;
  readonly plan?: PlanFilter;
  readonly sort?: SessionSort;
  readonly bucket?: JudgementBucket;
  readonly profileEmail?: string;
  readonly loginEmail?: string;
  readonly since?: string;
  readonly until?: string;
  readonly limit?: number;
  readonly assembled?: boolean;
  readonly source?: string;
  readonly offset?: number;
  readonly projectHashes?: readonly string[];
  readonly agent?: string;
  readonly classification?: SessionClassification;
}

interface SessionRecordsPageRequest {
  readonly sessionId: string;
  readonly offset: number;
  readonly lineage?: boolean;
  readonly signal?: AbortSignal;
  readonly scope?: SessionRecordScope;
}

// Endpoints that must not trigger an auto-refresh (avoid loops/infinite retry).
const AUTH_PATHS = ['/api/auth/login', '/api/auth/refresh', '/api/auth/setup', '/api/auth/setup-status', '/api/auth/logout'];

export async function apiFetch(url: string, options?: RequestInit): Promise<Response> {
  const doFetch = () => fetch(url, {
    ...options,
    credentials: 'include',
    headers: {
      ...options?.headers,
      'X-Requested-With': 'XMLHttpRequest',
    },
  });

  let res = await doFetch();

  if (res.status === 401 && !AUTH_PATHS.some((p) => url.includes(p))) {
    const retried = await refreshAfterUnauthorized(buildUrl('/api/auth/refresh'), doFetch);
    if (retried) {
      res = retried;
    }
  }

  return res;
}

export interface AppVersionInfo {
  version: string;
  session_record_version_since: string;
  client_version_header_since: string;
}

export async function fetchAppVersion(): Promise<string> {
  const info = await fetchAppVersionInfo();
  return info.version;
}

// Extended form used by the Clients tab (server version for lag comparison)
// and the session viewer (the release that introduced cctrace_version).
export async function fetchAppVersionInfo(): Promise<AppVersionInfo> {
  const res = await fetch(buildUrl('/api/version'));
  if (!res.ok) throw new Error('Failed to fetch app version');
  return res.json();
}

export async function fetchUsers(): Promise<import('./types').UserInfo[]> {
  const res = await apiFetch(buildUrl('/api/users'));
  if (!res.ok) throw new Error('fetchUsers failed');
  return res.json();
}

export async function fetchWeeklyInsights(since: string, until: string, tz: string): Promise<import('./types').WeeklyInsights> {
  const res = await apiFetch(buildUrl('/api/weekly-insights', { since, until, tz }));
  if (!res.ok) throw new Error('Failed to fetch weekly insights');
  return res.json();
}

export async function fetchTaskSegments(since: string, until: string, taskType: string): Promise<import('./types').TaskSegmentPage> {
  const res = await apiFetch(buildUrl('/api/task-segments', { since, until, task_type: taskType }));
  if (!res.ok) throw new Error('Failed to fetch task segments');
  const body = await res.json();
  return { segments: body.segments || [], total: body.total ?? 0, truncated: body.truncated ?? false };
}

export async function fetchOrganizationInsights(since: string, until: string): Promise<import('./types').OrganizationInsights> {
  const res = await apiFetch(buildUrl('/api/admin/organization-insights', { since, until }));
  if (!res.ok) throw new Error('Failed to fetch organization insights');
  return res.json();
}

// Bounded per call (server caps at 1000) so an admin can resume it without a
// startup scan -- click again for the next batch rather than looping here.
export async function backfillTaskTypes(): Promise<{ updated: number }> {
  const res = await apiFetch(buildUrl('/api/admin/task-types/backfill'), { method: 'POST' });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'Failed to backfill task types' }));
    throw new Error(err.error || 'Failed to backfill task types');
  }
  return res.json();
}

export async function fetchAccounts(): Promise<string[]> {
  const res = await apiFetch(buildUrl('/api/accounts'));
  if (!res.ok) throw new Error('Failed to fetch accounts');
  return res.json();
}

export async function fetchCostByUser(since?: string, until?: string, profileEmail?: string, loginEmail?: string) {
  const res = await apiFetch(buildUrl('/api/cost/by-user', { since: since || '', until: until || '', profile_email: profileEmail || '', login_email: loginEmail || '' }));
  if (!res.ok) throw new Error('Failed to fetch cost by user');
  return res.json();
}

export async function fetchCostByModel(since?: string, until?: string, profileEmail?: string, loginEmail?: string, userID?: string) {
  const res = await apiFetch(buildUrl('/api/cost/by-model', { since: since || '', until: until || '', profile_email: profileEmail || '', login_email: loginEmail || '', user_id: userID || '' }));
  if (!res.ok) throw new Error('Failed to fetch cost by model');
  return res.json();
}

export async function fetchTimeSeriesStatsByModel(granularity: string, since?: string, until?: string, profileEmail?: string, loginEmail?: string, tz?: string, projectHash?: string, userID?: string, agent?: string, modelCategory?: string, projectHashes?: readonly string[]) {
  const timezone = tz || Intl.DateTimeFormat().resolvedOptions().timeZone;
  const res = await apiFetch(buildUrl('/api/stats/timeseries-by-model', { granularity, since: since || '', until: until || '', profile_email: profileEmail || '', login_email: loginEmail || '', tz: timezone, ...projectHashParams(projectHash, projectHashes), user_id: userID || '', agent: agent || '', model_category: modelCategory && modelCategory !== 'all' ? modelCategory : '' }));
  if (!res.ok) throw new Error('Failed to fetch timeseries by model');
  return res.json();
}

export async function fetchTimeSeriesStatsByUser(granularity: string, since?: string, until?: string, profileEmail?: string, loginEmail?: string, tz?: string, projectHash?: string, userID?: string, modelCategory?: string, model?: string, agent?: string, projectHashes?: readonly string[]) {
  const timezone = tz || Intl.DateTimeFormat().resolvedOptions().timeZone;
  const res = await apiFetch(buildUrl('/api/stats/timeseries-by-user', { granularity, since: since || '', until: until || '', profile_email: profileEmail || '', login_email: loginEmail || '', tz: timezone, ...projectHashParams(projectHash, projectHashes), user_id: userID || '', model_category: modelCategory && modelCategory !== 'all' ? modelCategory : '', model: model && model !== '__all__' ? model : '', agent: agent || '' }));
  if (!res.ok) throw new Error('Failed to fetch timeseries by user');
  return res.json();
}

export async function fetchCoverageGap(params: { since: string; until: string; loginEmail?: string; granularity?: string; headlineSince?: string; headlineUntil?: string }): Promise<CoverageGap> {
  const res = await apiFetch(buildUrl('/api/stats/coverage-gap', {
    since: params.since, until: params.until, login_email: params.loginEmail || '',
    granularity: params.granularity || '',
    // The sentence's scope, when it differs from the plotted one. Sent with the
    // same request so the server loads the 28-day fit window once instead of
    // once per scope.
    headline_since: params.headlineSince || '', headline_until: params.headlineUntil || '',
    tz: params.granularity ? Intl.DateTimeFormat().resolvedOptions().timeZone : '',
  }));
  if (!res.ok) throw new Error('Failed to fetch coverage gap');
  return res.json();
}

export async function fetchLatestActivity(params: LatestActivityRequest = {}): Promise<{ has_data: boolean; latest_ts?: string }> {
  const res = await apiFetch(buildUrl('/api/stats/latest-activity', {
    profile_email: params.profileEmail || '',
    login_email: params.loginEmail || '',
    ...projectHashParams(params.projectHash, params.projectHashes),
    user_id: params.userID || '',
    agent: params.agent || '',
    model_category: params.modelCategory && params.modelCategory !== 'all' ? params.modelCategory : '',
    model: params.model && params.model !== '__all__' ? params.model : '',
  }));
  if (!res.ok) throw new Error('Failed to fetch latest activity');
  return res.json();
}

export async function fetchPluginUsage(params: {
  since?: string;
  until?: string;
  profile_email?: string;
  login_email?: string;
  user_id?: string;
  agent?: string;
}): Promise<import('./types').PluginUsageSummary[]> {
  const p: Record<string, string> = {};
  if (params.since) p.since = params.since;
  if (params.until) p.until = params.until;
  if (params.profile_email) p.profile_email = params.profile_email;
  if (params.login_email) p.login_email = params.login_email;
  if (params.user_id) p.user_id = params.user_id;
  if (params.agent) p.agent = params.agent;
  const res = await apiFetch(buildUrl('/api/plugins', p));
  if (!res.ok) throw new Error('Failed to fetch plugin usage');
  return res.json();
}

export async function fetchSkillUsage(params: {
  since?: string;
  until?: string;
  profile_email?: string;
  login_email?: string;
  user_id?: string;
  agent?: string;
}): Promise<import('./types').SkillUsageSummary[]> {
  const p: Record<string, string> = {};
  if (params.since) p.since = params.since;
  if (params.until) p.until = params.until;
  if (params.profile_email) p.profile_email = params.profile_email;
  if (params.login_email) p.login_email = params.login_email;
  if (params.user_id) p.user_id = params.user_id;
  if (params.agent) p.agent = params.agent;
  const res = await apiFetch(buildUrl('/api/skills', p));
  if (!res.ok) throw new Error('Failed to fetch skill usage');
  return res.json();
}

export async function fetchToolUsage(since?: string, until?: string, profileEmail?: string, loginEmail?: string) {
  const res = await apiFetch(buildUrl('/api/tools', { since: since || '', until: until || '', profile_email: profileEmail || '', login_email: loginEmail || '' }));
  if (!res.ok) throw new Error('Failed to fetch tool usage');
  return res.json();
}

export async function fetchToolDetail(toolName: string, since?: string, until?: string, loginEmail?: string, granularity?: string, limit?: number, tz?: string): Promise<import('./types').ToolDetail> {
  const params: Record<string, string> = { tool_name: toolName };
  if (since) params.since = since;
  if (until) params.until = until;
  if (loginEmail) params.login_email = loginEmail;
  if (granularity) params.granularity = granularity;
  if (limit) params.limit = String(limit);
  // The chart renders the returned bucket string by slicing it, so the server
  // has to bucket in the viewer's timezone -- same contract as the stats fetchers.
  params.tz = tz || Intl.DateTimeFormat().resolvedOptions().timeZone;
  const res = await apiFetch(buildUrl('/api/tools/detail', params));
  if (!res.ok) throw new Error('Failed to fetch tool detail');
  return res.json();
}

export async function fetchSessionAccountSegments(sessionId: string): Promise<import('./types').SessionAccountSegment[]> {
  const res = await apiFetch(buildUrl('/api/session-account-segments', { session_id: sessionId }));
  if (!res.ok) throw new Error('Failed to fetch session account segments');
  return res.json();
}

const overviewAnalysisParams = (params: SessionOverviewRequest): Record<string, string> => params.analysis ? {
  include: 'analysis', category: params.category ?? '', q: params.query ?? '', plan: params.plan ?? '', sort: params.sort ?? '', bucket: params.bucket ?? '',
} : {};

export async function fetchSessionOverview(params: SessionOverviewRequest = {}, signal?: AbortSignal): Promise<SessionOverview[]> {
  const res = await apiFetch(buildUrl('/api/session-overview', {
    ...overviewAnalysisParams(params),
    profile_email: params.profileEmail || '',
    login_email: params.loginEmail || '',
    since: params.since || '',
    until: params.until || '',
    limit: String(params.limit ?? 200),
    assembled: params.assembled ? '1' : '',
    source: params.source || '', // interactive | headless | '' (all)
    offset: String(params.offset ?? 0),
    project_hashes: (params.projectHashes ?? []).join(','), // server-side project filter (identity → hashes)
    agent: params.agent || '',
    classification: params.classification || '',
  }), { signal });
  if (!res.ok) throw new Error('Failed to fetch session overview');
  const data: unknown = await res.json();
  if (params.analysis && (!Array.isArray(data) || data.some((row) => !isAnalysisOverviewRow(row)))) {
    throw new Error('Invalid session overview analysis response');
  }
  return params.analysis ? (data as SessionOverview[]).map(withoutUncomputedRowValues) : data as SessionOverview[];
}

export interface SessionOverviewCount {
  count: number;
  /**
   * Sessions the account filter necessarily dropped: they never emitted OTEL, so
   * no account is known for them. Surfacing this keeps a narrowed list honest --
   * these sessions are real usage, not an empty result.
   */
  unattributed: number;
  classification_counts?: SessionClassificationCounts;
  /** Owner-scoped server counts for the four judgement categories. */
  judgement_bucket_counts?: SessionBucketCounts;
}

const isSessionBucketCounts = (value: unknown): value is SessionBucketCounts =>
  isObject(value) && ['done', 'leftover', 'ended_midway', 'unknown'].every((key) =>
    Number.isSafeInteger(value[key]) && (value[key] as number) >= 0);

interface SessionOverviewCountRequest extends Omit<SessionOverviewRequest, 'limit' | 'offset'> {
  readonly includeClassificationCounts?: boolean;
}

export async function fetchSessionOverviewCount(params: SessionOverviewCountRequest = {}, signal?: AbortSignal): Promise<SessionOverviewCount> {
  const res = await apiFetch(buildUrl('/api/session-overview/count', {
    ...overviewAnalysisParams(params),
    profile_email: params.profileEmail || '',
    login_email: params.loginEmail || '',
    since: params.since || '',
    until: params.until || '',
    assembled: params.assembled ? '1' : '',
    source: params.source || '',
    project_hashes: (params.projectHashes ?? []).join(','),
    agent: params.agent || '',
    classification: params.classification || '',
    include_classification_counts: params.includeClassificationCounts === undefined ? '' : params.includeClassificationCounts ? '1' : '0',
  }), { signal });
  if (!res.ok) throw new Error('Failed to fetch session overview count');
  const data: unknown = await res.json();
  if (!isObject(data) || !Number.isSafeInteger(data.count) || (data.count as number) < 0) throw new Error('Invalid session overview count');
  if (data.judgement_bucket_counts !== undefined && !isSessionBucketCounts(data.judgement_bucket_counts)) {
    throw new Error('Invalid session judgement bucket counts');
  }
  return { count: data.count as number, unattributed: typeof data.unattributed === 'number' ? data.unattributed : 0,
    classification_counts: data.classification_counts as SessionClassificationCounts | undefined,
    judgement_bucket_counts: data.judgement_bucket_counts as SessionBucketCounts | undefined };
}

const SESSION_CATEGORY_KEYS: readonly SessionCategoryKey[] = ['waiting', 'running', 'ended', 'stale_live', 'open_question', 'idle', 'collector_delayed', 'recent', 'inactive'];

type SessionOverviewCategoriesRequest = Omit<SessionOverviewRequest, 'limit' | 'offset' | 'category' | 'query' | 'plan' | 'sort' | 'source'> & {
  readonly source?: 'interactive' | 'headless' | 'all';
};

export async function fetchSessionOverviewCategories(params: SessionOverviewCategoriesRequest = {}): Promise<SessionCategoryCounts> {
  const res = await apiFetch(buildUrl('/api/session-overview/categories', {
    source: params.source || 'interactive',
    assembled: params.assembled ? '1' : '',
    project_hashes: (params.projectHashes ?? []).join(','),
    agent: params.agent || '',
    classification: params.classification || '',
    since: params.since || '',
    until: params.until || '',
  }));
  if (!res.ok) throw new Error('Failed to fetch session overview categories');
  const data: unknown = await res.json();
  if (!isObject(data) || Object.keys(data).length !== SESSION_CATEGORY_KEYS.length ||
      SESSION_CATEGORY_KEYS.some((key) => !Object.hasOwn(data, key)) ||
      Object.entries(data).some(([key, value]) =>
        !SESSION_CATEGORY_KEYS.includes(key as SessionCategoryKey) || !Number.isSafeInteger(value) || (value as number) < 0)) {
    throw new Error('Invalid session overview categories');
  }
  return data as SessionCategoryCounts;
}

export class SessionAnalysisRequestError extends Error {
  constructor(readonly status: number) {
    super(status === 404 ? '이 세션의 분석이 없거나 볼 수 없습니다.' : '세션 분석을 읽지 못했습니다.');
  }
}

const isObject = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const ANALYSIS_ROW_STATES = ['ready', 'stale', 'building', 'none', 'truncated', 'unsupported', 'withheld'];
const ANALYSIS_ROW_TITLE_STATES = ['ok', 'withheld', 'redacted', 'none'];
const ANALYSIS_ROW_TITLE_SOURCES = ['custom', 'ai', 'agent_name', 'first_request', 'none'];

// The truncated fallback computes identity and time only. Its fact row keeps the
// title and context columns at their NULL/'' defaults, which name no source.
const isUncomputedRowTitle = (title: unknown): boolean =>
  isObject(title) && title.text === null && title.source === '' && title.state === '';
const isUncomputedRowContext = (context: unknown): boolean =>
  isObject(context) && context.percent === null && context.tokens === null && context.source === '';

// Drop the uncomputed truncated title/context: absent means "not drawn", not "none".
const withoutUncomputedRowValues = (row: SessionOverview): SessionOverview => {
  if (row.analysis?.state !== 'truncated') return row;
  const { title, context, ...analysis } = row.analysis;
  return { ...row, analysis: {
    ...analysis,
    ...(isUncomputedRowTitle(title) ? {} : { title }),
    ...(isUncomputedRowContext(context) ? {} : { context }),
  } };
};

const isAnalysisOverviewRow = (row: unknown): boolean => {
  if (!isObject(row) || typeof row.session_id !== 'string' || row.session_id === '' ||
      typeof row.profile_email !== 'string' || typeof row.agent !== 'string' ||
      typeof row.model !== 'string' || typeof row.start_time !== 'string' ||
      typeof row.end_time !== 'string' || !Number.isFinite(row.input_tokens) ||
      !Number.isFinite(row.output_tokens) || !Number.isFinite(row.cost_usd) ||
      !Number.isFinite(row.event_count) || typeof row.has_sync !== 'boolean' ||
      !isObject(row.analysis) || !ANALYSIS_ROW_STATES.includes(row.analysis.state as string)) return false;
  const analysis = row.analysis;
  if (analysis.state === 'withheld') {
    return analysis.reason === 'mixed_identity' && analysis.title === undefined &&
      analysis.category === undefined && analysis.signals === undefined;
  }
  if (analysis.state === 'building' || analysis.state === 'unsupported') {
    return analysis.title === undefined && analysis.requests === undefined &&
      analysis.inputs === undefined && analysis.category === undefined && analysis.signals === undefined;
  }
  const title = analysis.state === 'truncated' && isUncomputedRowTitle(analysis.title) ||
    isObject(analysis.title) && ANALYSIS_ROW_TITLE_STATES.includes(analysis.title.state as string) &&
      ANALYSIS_ROW_TITLE_SOURCES.includes(analysis.title.source as string) &&
      (analysis.title.text === null || typeof analysis.title.text === 'string');
  if (!title ||
      analysis.requests !== null && (!Number.isSafeInteger(analysis.requests) || (analysis.requests as number) < 0) ||
      analysis.inputs !== null && (!Number.isSafeInteger(analysis.inputs) || (analysis.inputs as number) < 0) ||
      !isObject(analysis.plan_flags) || typeof analysis.plan_flags.has_plan_file !== 'boolean' ||
      typeof analysis.plan_flags.used_plan_mode !== 'boolean') return false;
  if (analysis.category !== undefined && !SESSION_CATEGORY_KEYS.includes(analysis.category as SessionCategoryKey)) return false;
  if (analysis.headline !== undefined && analysis.headline !== null && typeof analysis.headline !== 'string') return false;
  if (analysis.record_state !== undefined && !['ready', 'stale', 'generating', 'none', 'withheld', 'redacted', 'failed', 'disabled', 'missing', 'blocked', 'waiting_budget'].includes(analysis.record_state as string)) return false;
  if (analysis.record_reason !== undefined && analysis.record_reason !== null && typeof analysis.record_reason !== 'string') return false;
  if (analysis.judgement !== undefined && !isRowJudgement(analysis.judgement)) return false;
  return analysis.signals === undefined || isObject(analysis.signals);
};

const isRowJudgement = (value: unknown): boolean => {
  if (!isObject(value) || !['running', 'done', 'leftover', 'ended_midway', 'interrupted', 'unknown'].includes(String(value.status)) ||
      !['done', 'leftover', 'ended_midway', 'unknown', null].includes(value.bucket as string | null) ||
      !Array.isArray(value.checks) || value.checks.length !== 5 ||
      !value.checks.every((state) => ['pass', 'fail', 'undeterminable'].includes(String(state)))) return false;
  const allPass = value.checks.every((state) => state === 'pass');
  return (value.status !== 'done' || allPass) && (value.bucket !== 'done' || allPass);
};

export interface SessionCalendarRequest {
  readonly from: string;
  readonly to: string;
  /** An administrator's one selected user. Omitted, the server reads the caller's own sessions. */
  readonly userId?: string;
  readonly profileEmail?: string;
  /** An administrator's account filter. It narrows the selected user's sessions; it never selects a user. */
  readonly loginEmail?: string;
  readonly projectHashes?: readonly string[];
  readonly agent?: string;
  readonly source?: string;
  readonly assembled?: boolean;
  readonly classification?: SessionClassification;
}

type CalendarTagField = 'components' | 'screens' | 'kinds';

/**
 * A calendar session with the overview row the server read in the same access scope.
 * `unreported_tags` names the tag fields the server sent as null (not collected), which
 * the screen shows as [] but must not describe as an observed empty list.
 */
export type CalendarSessionWithOverview = CalendarSession & { overview?: SessionOverview; unreported_tags?: CalendarTagField[] };
export interface SessionCalendarResponse extends SessionCalendar {
  sessions: CalendarSessionWithOverview[];
}

export class SessionCalendarRequestError extends Error {
  constructor(readonly status: number, readonly code: string) {
    super(status === 404 && code === 'session_analysis_disabled' ? '세션 분석이 서버에서 꺼져 있어 캘린더를 읽을 수 없습니다.'
      : status === 400 ? '서버가 캘린더 요청 범위를 거부했습니다.' : '세션 캘린더를 읽지 못했습니다.');
  }
}

const isCalendarOverview = (value: unknown, sessionId: unknown): boolean =>
  isObject(value) && value.session_id === sessionId && typeof value.profile_email === 'string' &&
  typeof value.agent === 'string' && typeof value.model === 'string' && typeof value.start_time === 'string' &&
  typeof value.end_time === 'string' && Number.isFinite(value.input_tokens) && Number.isFinite(value.output_tokens) &&
  Number.isFinite(value.cost_usd) && Number.isFinite(value.event_count) && typeof value.has_sync === 'boolean';

// The server sends values it has not collected as null or '': tags come from session
// records (P5), and a session without facts or with the truncated fallback has no
// computed title or cost source. Only those exact shapes are normalized; any other
// violation is left for prepareSessions to count and report.
const normalizeCalendarSession = (raw: Record<string, unknown>): CalendarSessionWithOverview => {
  const session: Record<string, unknown> = { ...raw };
  if (isObject(raw.colors)) {
    const colors: Record<string, unknown> = { ...raw.colors };
    const unreported = (['components', 'screens', 'kinds'] as const).filter((key) => colors[key] === null);
    for (const key of unreported) colors[key] = [];
    session.colors = colors;
    if (unreported.length > 0) session.unreported_tags = unreported;
  }
  const title = raw.title;
  if (isObject(title) && title.text === null && (title.source === null || title.source === '') && (title.state === 'none' || title.state === '')) {
    session.title = { text: null, source: 'none', state: 'none' };
  }
  if (isObject(raw.cost) && raw.cost.usd === null && raw.cost.source === '') session.cost = { usd: null, source: 'none' };
  if (raw.overview !== undefined && !isCalendarOverview(raw.overview, raw.session_id)) delete session.overview;
  return session as unknown as CalendarSessionWithOverview;
};

export async function fetchSessionCalendar(params: SessionCalendarRequest): Promise<SessionCalendarResponse> {
  const res = await apiFetch(buildUrl('/api/session-calendar', {
    from: params.from, to: params.to,
    user_id: params.userId ?? '', profile_email: params.profileEmail ?? '', login_email: params.loginEmail ?? '',
    project_hashes: (params.projectHashes ?? []).join(','), agent: params.agent ?? '', source: params.source ?? '',
    assembled: params.assembled ? '1' : '', classification: params.classification ?? '',
  }));
  if (!res.ok) {
    const body: unknown = await res.json().catch(() => null);
    throw new SessionCalendarRequestError(res.status, isObject(body) && typeof body.error === 'string' ? body.error : '');
  }
  const data: unknown = await res.json();
  if (!isObject(data) || typeof data.from !== 'string' || typeof data.to !== 'string' || typeof data.owner_view !== 'boolean' ||
      !Array.isArray(data.sessions) || data.sessions.some((row) => !isObject(row) || typeof row.session_id !== 'string' || row.session_id === '')) {
    throw new Error('Invalid session calendar response');
  }
  return { from: data.from, to: data.to, owner_view: data.owner_view,
    sessions: (data.sessions as Record<string, unknown>[]).map(normalizeCalendarSession) };
}

const isAnalysisHeader = (value: unknown): boolean => {
  if (!isObject(value) || !isObject(value.title) || !isObject(value.counts) ||
      !isObject(value.project) || !isObject(value.accounts) || !Array.isArray(value.accounts.login_emails) ||
      !isObject(value.tags) || !Array.isArray(value.tags.rules_files) ||
      !isObject(value.tokens) || !isObject(value.cost) || !isObject(value.context) ||
      !isObject(value.tools) || !isObject(value.plan_flags)) return false;
  if (value.repo_status !== undefined && value.repo_status !== null && !isAnalysisRepoStatus(value.repo_status)) return false;
  return typeof value.agent === 'string' && typeof value.model === 'string' &&
    typeof value.project.hash === 'string' && (value.project.name === null || typeof value.project.name === 'string') &&
    typeof value.title.state === 'string' && typeof value.counts.api_requests === 'number' &&
    typeof value.tokens.basis === 'string' && typeof value.cost.source === 'string';
};

const isAnalysisRepoStatus = (value: unknown): value is AnalysisRepoStatus =>
  isObject(value) &&
  (typeof value.branch === 'string' || value.branch === null) &&
  (value.branch_state === 'observed' || value.branch_state === 'undeterminable') &&
  (value.branch_source === 'session_record_git_branch' || value.branch_source === 'unknown') &&
  (typeof value.uncommitted_edited_files === 'number' || value.uncommitted_edited_files === null) &&
  (value.uncommitted_state === 'observed' || value.uncommitted_state === 'undeterminable') &&
  (value.uncommitted_source === 'session_git_snapshot' || value.uncommitted_source === 'unknown') &&
  value.uncommitted_scope === 'session_edited_files' &&
  (value.uncommitted_reason === null || value.uncommitted_reason === 'no_git_snapshot' ||
    value.uncommitted_reason === 'truncated_snapshot' || value.uncommitted_reason === 'partial_snapshot' ||
    value.uncommitted_reason === 'unresolved_git_paths' || value.uncommitted_reason === 'invalid_git_snapshot') &&
  (typeof value.changed_at === 'string' || value.changed_at === null);

const isSessionRepoStatusResponse = (value: unknown, sessionId: string): value is SessionRepoStatusResponse =>
  isObject(value) && value.session_id === sessionId && typeof value.owner_view === 'boolean' &&
  (value.analysis_state === undefined || typeof value.analysis_state === 'string' &&
    ['ready', 'stale', 'building', 'none', 'truncated', 'unsupported'].includes(value.analysis_state)) &&
  (value.repo_status === undefined || value.repo_status === null || isAnalysisRepoStatus(value.repo_status));

const isSessionRecord = (value: unknown): value is AnalysisSessionRecord => {
  if (!isObject(value) || !['ready', 'stale', 'generating', 'none', 'withheld', 'redacted', 'failed', 'disabled', 'missing', 'blocked', 'waiting_budget'].includes(String(value.state)) ||
      (value.reason !== undefined && value.reason !== null && typeof value.reason !== 'string') ||
      (value.generated_at !== null && typeof value.generated_at !== 'string') ||
      (value.covers_until !== null && typeof value.covers_until !== 'string')) return false;
  if (value.state !== 'ready' && value.state !== 'stale') return true;
  return typeof value.headline === 'string' && Array.isArray(value.bullets) && value.bullets.every((item) =>
    isObject(item) && typeof item.text === 'string' && Array.isArray(item.commits) && item.commits.every((short) => typeof short === 'string')) &&
    Array.isArray(value.review) && value.review.every((item) => isObject(item) &&
      ['decision', 'interpretation', 'unverified'].includes(String(item.kind)) && typeof item.text === 'string' &&
      (item.turn_key === undefined || typeof item.turn_key === 'string')) &&
    isObject(value.tags) && Array.isArray(value.tags.kinds) && value.tags.kinds.every((tag) => typeof tag === 'string') &&
    Array.isArray(value.tags.components) && value.tags.components.every((tag) => typeof tag === 'string') &&
    Array.isArray(value.tags.screens) && value.tags.screens.every((tag) => typeof tag === 'string') &&
    Array.isArray(value.tags.related) && value.tags.related.every((item) => isObject(item) &&
      ['task', 'issue', 'pr', 'feature'].includes(String(item.kind)) && typeof item.id === 'string' &&
      (item.item_key === undefined || typeof item.item_key === 'string') && (item.url === undefined || typeof item.url === 'string')) &&
    Array.isArray(value.work) && value.work.every((item) => isObject(item) &&
      typeof item.id === 'string' && ['consultation', 'request', 'followup', 'finishing'].includes(String(item.kind)) &&
      (typeof item.done === 'boolean' || item.done === null) && typeof item.text === 'string' &&
      Array.isArray(item.commits) && Array.isArray(item.tasks) &&
      (item.task_keys === undefined || Array.isArray(item.task_keys) && item.task_keys.every((key) => typeof key === 'string')));
};

const isSessionJudgement = (value: unknown): boolean => {
  if (!isObject(value) || !['running', 'done', 'leftover', 'ended_midway', 'interrupted', 'unknown'].includes(String(value.status)) ||
      !['done', 'leftover', 'ended_midway', 'unknown', null].includes(value.bucket as string | null) ||
      !Array.isArray(value.checks) || value.checks.length !== 5) return false;
  const keys = ['turn_ended', 'no_pending_reply', 'no_background', 'committed', 'work_done'];
  const validChecks = value.checks.every((check, i) => isObject(check) && check.key === keys[i] &&
    ['pass', 'fail', 'undeterminable'].includes(String(check.state)) &&
    (check.count === undefined || Number.isSafeInteger(check.count) && (check.count as number) >= 0) &&
    (check.reason === undefined || typeof check.reason === 'string') &&
    (check.source === undefined || typeof check.source === 'string'));
  if (!validChecks) return false;
  const allPass = value.checks.every((check) => isObject(check) && check.state === 'pass');
  return (value.status !== 'done' || allPass) && (value.bucket !== 'done' || allPass);
};

const isAnalysis = (value: unknown, sessionId: string): value is SessionAnalysis => {
  if (!isObject(value) || value.session_id !== sessionId || typeof value.short_id !== 'string' || typeof value.owner_view !== 'boolean') return false;
  if (value.state === 'building' || value.state === 'none' || value.state === 'truncated' || value.state === 'unsupported') {
    return value.header === undefined || isObject(value.header);
  }
  if (value.state !== 'ready' && value.state !== 'stale') return false;
  if (value.record !== undefined && !isSessionRecord(value.record)) return false;
  if (value.judgement !== undefined && !isSessionJudgement(value.judgement)) return false;
  if (typeof value.rules_version !== 'number' || typeof value.computed_at !== 'string') return false;
  if (!isAnalysisHeader(value.header)) return false;
  const outputs = value.outputs;
  const recaps = value.recaps;
  const subagents = value.subagents;
  const truncated = value.truncated;
  if (!isObject(outputs) || !isObject(outputs.edited_files) || !Array.isArray(outputs.edited_files.items) ||
      !isObject(outputs.prs) || !Array.isArray(outputs.prs.items) ||
      !isObject(recaps) || !Array.isArray(recaps.items) || !isObject(subagents) ||
      typeof subagents.unattributed_requests !== 'number' || typeof subagents.unattributed_tokens !== 'number' ||
      !isObject(truncated)) return false;
  if (!['turns', 'abandoned', 'inputs', 'questions', 'plans', 'recaps', 'bodies'].every((field) =>
    typeof truncated[field] === 'boolean')) return false;
  if (!Array.isArray(value.turns) || !Array.isArray(value.abandoned) ||
      !Array.isArray(value.inputs) || !Array.isArray(value.questions) || !Array.isArray(value.plans)) return false;
  return [...value.turns, ...value.abandoned].every((turn) =>
    isObject(turn) && typeof turn.key === 'string' && typeof turn.anchor_ts === 'string' &&
    isObject(turn.preview) && isObject(turn.tokens) && isObject(turn.cost) && isObject(turn.context) &&
    isObject(turn.tools) && isObject(turn.edited) && Array.isArray(turn.edited.sample) &&
    turn.edited.sample.every((path) => typeof path === 'string') &&
    Array.isArray(turn.question_headers) && turn.question_headers.every((question) =>
      isObject(question) && Array.isArray(question.headers) &&
      question.headers.every((header) => typeof header === 'string')) && Array.isArray(turn.prs));
};

export async function fetchSessionAnalysis(sessionId: string, scope?: { profileEmail?: string; loginEmail?: string; userID?: string }, signal?: AbortSignal): Promise<SessionAnalysis> {
  const res = await apiFetch(buildUrl(`/api/sessions/${encodeURIComponent(sessionId)}/analysis`, {
    profile_email: scope?.profileEmail ?? '', login_email: scope?.loginEmail ?? '', user_id: scope?.userID ?? '',
  }), { signal });
  if (!res.ok) throw new SessionAnalysisRequestError(res.status);
  const body: unknown = await res.json();
  if (!isAnalysis(body, sessionId)) throw new Error('Invalid session analysis response');
  return body;
}

const SESSION_RECORD_SEARCH_FIELDS: readonly SessionRecordSearchField[] = ['title', 'headline', 'bullet', 'review', 'work', 'commit', 'task'];

const isSessionRecordSearchResult = (value: unknown): value is SessionRecordSearchResult =>
  isObject(value) && typeof value.session_id === 'string' && value.session_id !== '' &&
  (value.title === null || typeof value.title === 'string') &&
  (value.last_activity_at === null || typeof value.last_activity_at === 'string') && Array.isArray(value.matches) &&
  value.matches.every((match) => isObject(match) && SESSION_RECORD_SEARCH_FIELDS.includes(match.field as SessionRecordSearchField) &&
    typeof match.text === 'string' &&
    (match.turn_key === undefined || typeof match.turn_key === 'string') &&
    (match.item_key === undefined || typeof match.item_key === 'string'));

export class SessionRecordRequestError extends Error {
  constructor(readonly status: number, readonly code: string) {
    super(status === 403 ? '세션 기록 권한 또는 동의를 확인할 수 없습니다.'
      : status === 404 ? '세션 기록 기능이 꺼져 있거나 대상을 찾을 수 없습니다.'
        : '세션 기록 요청을 처리하지 못했습니다.');
  }
}

const sessionRecordRequestError = async (res: Response): Promise<SessionRecordRequestError> => {
  const body: unknown = await res.json().catch(() => null);
  return new SessionRecordRequestError(res.status, isObject(body) && typeof body.error === 'string' ? body.error
    : isObject(body) && typeof body.code === 'string' ? body.code : 'request_failed');
};

/** Owner-only search. The API caps results at 50 sessions and accepts no page offset. */
export async function fetchSessionRecordSearch(query: string, limit = 50, signal?: AbortSignal): Promise<SessionRecordSearchResult[]> {
  const res = await apiFetch(buildUrl('/api/session-search', { q: query, limit: String(limit) }), { signal });
  if (!res.ok) throw await sessionRecordRequestError(res);
  const body: unknown = await res.json();
  if (!Array.isArray(body) || body.some((row) => !isSessionRecordSearchResult(row))) {
    throw new Error('Invalid session record search response');
  }
  return body;
}

const isSessionRecordRangeStatus = (value: unknown): value is SessionRecordRangeStatus =>
  isObject(value) && ['fresh', 'stale', 'writing'].includes(String(value.state)) &&
  ['total', 'stale', 'writing'].every((key) => Number.isSafeInteger(value[key]) && (value[key] as number) >= 0) &&
  (value.written === undefined || typeof value.written === 'number' && Number.isSafeInteger(value.written) && value.written >= 0) &&
  typeof value.waiting_budget === 'boolean' && isObject(value.blocked) &&
  Object.values(value.blocked).every((count) => Number.isSafeInteger(count) && (count as number) >= 0);

export async function fetchSessionRecordStatus(from: string, to: string, signal?: AbortSignal): Promise<SessionRecordRangeStatus> {
  const res = await apiFetch(buildUrl('/api/session-records/status', { from, to }), { signal });
  if (!res.ok) throw await sessionRecordRequestError(res);
  const body: unknown = await res.json();
  if (!isSessionRecordRangeStatus(body)) throw new Error('Invalid session record status response');
  return body;
}

export async function refreshSessionRecordRange(from: string, to: string): Promise<void> {
  const res = await apiFetch(buildUrl('/api/session-records/refresh'), {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ from, to }),
  });
  if (!res.ok) throw await sessionRecordRequestError(res);
}

export interface SessionRecordRegenerationResult {
  state: 'queued' | 'waiting_budget';
  reason?: 'waiting_budget';
  retry_at?: string;
}

const isSessionRecordRegenerationResult = (value: unknown): value is SessionRecordRegenerationResult =>
  isObject(value) && (value.state === 'queued' || value.state === 'waiting_budget') &&
  (value.state !== 'waiting_budget' || value.reason === 'waiting_budget' && typeof value.retry_at === 'string' && Number.isFinite(Date.parse(value.retry_at)));

export async function regenerateSessionRecord(sessionId: string): Promise<SessionRecordRegenerationResult> {
  const res = await apiFetch(buildUrl(`/api/sessions/${encodeURIComponent(sessionId)}/record/regenerate`), { method: 'POST' });
  if (!res.ok) throw await sessionRecordRequestError(res);
  const body: unknown = await res.json().catch(() => null);
  if (body === null) return { state: 'queued' };
  if (!isSessionRecordRegenerationResult(body)) throw new Error('Invalid session record regeneration response');
  return body;
}

const isSessionRecordConsentInfo = (value: unknown): value is SessionRecordConsentInfo =>
  isObject(value) && typeof value.runtime_key === 'string' && typeof value.disclosure_version === 'string' &&
  typeof value.granted === 'boolean' && (value.granted_at === null || typeof value.granted_at === 'string') &&
  typeof value.runtime === 'string' && typeof value.provider === 'string' && typeof value.provider_retention === 'string' &&
  typeof value.disclosure === 'string' && Array.isArray(value.sends) && value.sends.every((line) => typeof line === 'string') &&
  Array.isArray(value.not_sends) && value.not_sends.every((line) => typeof line === 'string') &&
  typeof value.enabled === 'boolean' && typeof value.runtime_available === 'boolean' &&
  (value.runtime_reason === undefined || typeof value.runtime_reason === 'string');

export async function fetchSessionRecordConsent(signal?: AbortSignal): Promise<SessionRecordConsentInfo> {
  const res = await apiFetch(buildUrl('/api/session-records/consent'), { signal });
  if (!res.ok) throw await sessionRecordRequestError(res);
  const body: unknown = await res.json();
  if (!isSessionRecordConsentInfo(body)) throw new Error('Invalid session record consent response');
  return body;
}

export async function grantSessionRecordConsent(info: Pick<SessionRecordConsentInfo, 'runtime_key' | 'disclosure_version'>): Promise<void> {
  const res = await apiFetch(buildUrl('/api/session-records/consent'), {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ runtime_key: info.runtime_key, disclosure_version: info.disclosure_version }),
  });
  if (!res.ok) throw await sessionRecordRequestError(res);
}

export async function withdrawSessionRecordConsent(): Promise<void> {
  const res = await apiFetch(buildUrl('/api/session-records/consent'), { method: 'DELETE' });
  if (!res.ok) throw await sessionRecordRequestError(res);
}

export async function deleteOwnSessionRecords(): Promise<{ deleted: number }> {
  const res = await apiFetch(buildUrl('/api/session-records', { scope: 'mine' }), { method: 'DELETE' });
  if (!res.ok) throw await sessionRecordRequestError(res);
  const body: unknown = await res.json();
  if (!isObject(body) || !Number.isSafeInteger(body.deleted) || (body.deleted as number) < 0) {
    throw new Error('Invalid session record deletion response');
  }
  return { deleted: body.deleted as number };
}

export async function fetchAdminSessionRecordVocabulary(repositoryKey: string, signal?: AbortSignal): Promise<SessionRecordVocabulary> {
  const res = await apiFetch(buildUrl('/api/admin/ai/records/vocabulary', { repository_key: repositoryKey }), { signal });
  if (!res.ok) throw await sessionRecordRequestError(res);
  const body: unknown = await res.json();
  if (!isObject(body) || body.repository_key !== repositoryKey || !Array.isArray(body.components) ||
      !body.components.every((term) => typeof term === 'string') || !Array.isArray(body.screens) ||
      !body.screens.every((term) => typeof term === 'string') || typeof body.components_closed !== 'boolean' ||
      typeof body.screens_closed !== 'boolean') throw new Error('Invalid session record vocabulary response');
  return body as unknown as SessionRecordVocabulary;
}

export async function setAdminSessionRecordVocabulary(repositoryKey: string, components: string[], screens: string[]): Promise<SessionRecordVocabulary> {
  const res = await apiFetch(buildUrl('/api/admin/ai/records/vocabulary'), {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ repository_key: repositoryKey, components, screens }),
  });
  if (!res.ok) throw await sessionRecordRequestError(res);
  return res.json();
}

export async function mergeAdminSessionRecordVocabulary(repositoryKey: string, axis: 'component' | 'screen', from: string, to: string): Promise<SessionRecordVocabulary> {
  const res = await apiFetch(buildUrl('/api/admin/ai/records/vocabulary/merge'), {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ repository_key: repositoryKey, axis, from, to }),
  });
  if (!res.ok) throw await sessionRecordRequestError(res);
  return res.json();
}

export async function fetchSessionRepoStatus(sessionId: string, scope?: { profileEmail?: string; loginEmail?: string; userID?: string }): Promise<SessionRepoStatusResponse> {
  const res = await apiFetch(buildUrl(`/api/sessions/${encodeURIComponent(sessionId)}/analysis`, {
    profile_email: scope?.profileEmail ?? '', login_email: scope?.loginEmail ?? '', user_id: scope?.userID ?? '', view: 'repo-status',
  }));
  if (!res.ok) throw new SessionAnalysisRequestError(res.status);
  const body: unknown = await res.json();
  if (!isSessionRepoStatusResponse(body, sessionId)) throw new Error('Invalid session repository status response');
  return body;
}

// params scope the picker to the same population the session list shows; omitting them
// keeps the unfiltered list every other caller expects.
export async function fetchProjects(
  params: { source?: string; agent?: string; loginEmail?: string; assembled?: boolean } = {},
): Promise<import('./types').Project[]> {
  const res = await apiFetch(buildUrl('/api/projects', {
    source: params.source || '',
    agent: params.agent || '',
    login_email: params.loginEmail || '',
    assembled: params.assembled ? '1' : '',
  }));
  if (!res.ok) throw new Error('Failed to fetch projects');
  return res.json();
}

export async function fetchProjectRules(
  params?: import('./types').ProjectRuleListQuery,
  signal?: AbortSignal,
): Promise<import('./types').ProjectRuleListResponse> {
  const p: Record<string, string> = {};
  if (params?.agent) p.agent = params.agent;
  if (params?.repository_key) p.repository_key = params.repository_key;
  if (params?.repository_id) p.repository_id = params.repository_id;
  if (params?.project_hash) p.project_hash = params.project_hash;
  if (params?.status) p.status = params.status;
  if (params?.query) p.query = params.query;
  if (params?.limit != null) p.limit = String(params.limit);
  if (params?.offset != null) p.offset = String(params.offset);
  const res = await fetch(buildUrl('/api/project-rules', p), { credentials: 'include', signal });
  if (!res.ok) throw new Error('Failed to fetch project rules');
  return res.json();
}

export async function fetchProjectRuleDetail(
  id: number,
  selectedVersionID?: number,
  signal?: AbortSignal,
): Promise<import('./types').ProjectRuleDetail> {
  const res = await fetch(buildUrl(`/api/project-rules/${id}`, {
    selected_version_id: selectedVersionID == null ? '' : String(selectedVersionID),
  }), { credentials: 'include', signal });
  if (!res.ok) throw new Error('Failed to fetch project rule detail');
  return res.json();
}

export async function createProjectRuleComment(
  ruleID: number,
  body: import('./types').CreateProjectRuleCommentRequest
): Promise<import('./types').ProjectRuleComment> {
  const res = await apiFetch(buildUrl(`/api/project-rules/${ruleID}/comments`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error('Failed to create project rule comment');
  return res.json();
}

// Session records page size. The detail view lazy-loads a session one page at a time via
// useInfiniteQuery with order=asc (oldest first), so the conversation start paints
// immediately and newer records page in on scroll — no upfront full-session load.
export const SESSION_PAGE_SIZE = 1000; // server caps a single query at 1000

export async function fetchSessionRecordsPage(params: SessionRecordsPageRequest): Promise<SessionRecord[]> {
  const res = await apiFetch(buildUrl('/api/sessions', {
    session_id: params.sessionId,
    limit: String(SESSION_PAGE_SIZE),
    offset: String(params.offset),
    order: 'asc',
    lineage: params.lineage ? '1' : '',
    // The assembled view renders from view alone and polls every loaded page; only the
    // individual raw-record view needs raw.
    raw: params.lineage ? '0' : '',
    view: params.lineage ? 'conversation' : '',
    ...(params.scope ? { profile_email: params.scope.profileEmail ?? '', login_email: params.scope.loginEmail ?? '', user_id: params.scope.userID ?? '' } : {}),
  }), { signal: params.signal });
  if (!res.ok) throw new Error('Failed to fetch session records');
  return res.json();
}

export async function fetchEvents(params?: { session_id?: string; profile_email?: string; login_email?: string; event_name?: string; since?: string; until?: string; limit?: number; offset?: number }) {
  const p: Record<string, string> = {};
  if (params?.session_id) p.session_id = params.session_id;
  if (params?.profile_email) p.profile_email = params.profile_email;
  if (params?.login_email) p.login_email = params.login_email;
  if (params?.event_name) p.event_name = params.event_name;
  if (params?.since) p.since = params.since;
  if (params?.until) p.until = params.until;
  if (params?.limit) p.limit = String(params.limit);
  if (params?.offset) p.offset = String(params.offset);
  const res = await apiFetch(buildUrl('/api/events', p));
  if (!res.ok) throw new Error('Failed to fetch events');
  return res.json();
}

export async function fetchMetrics(params?: { metric_name?: string; profile_email?: string; agent?: string; model?: string; since?: string; until?: string; limit?: number; offset?: number }) {
  const p: Record<string, string> = {};
  if (params?.metric_name) p.metric_name = params.metric_name;
  if (params?.profile_email) p.profile_email = params.profile_email;
  if (params?.agent) p.agent = params.agent;
  // Partial match server-side, so the model dropdown's stripped values
  // ('sonnet-4-6') reach the stored 'claude-sonnet-4-6' rows.
  if (params?.model) p.model = params.model;
  if (params?.since) p.since = params.since;
  if (params?.until) p.until = params.until;
  if (params?.limit) p.limit = String(params.limit);
  if (params?.offset) p.offset = String(params.offset);
  const res = await apiFetch(buildUrl('/api/metrics', p));
  if (!res.ok) throw new Error('Failed to fetch metrics');
  return res.json();
}

export async function deleteUserData(profileEmail: string, userID: string) {
  const params: Record<string, string> = {};
  if (profileEmail) params.profile_email = profileEmail;
  if (userID) params.user_id = userID;
  const res = await apiFetch(buildUrl('/api/users/data', params), { method: 'DELETE' });
  if (!res.ok) throw new Error('Failed to delete user data');
  return res.json();
}

export async function mergeUsers(fromProfileEmail: string, fromUserID: string, toProfileEmail: string) {
  const res = await apiFetch(buildUrl('/api/users/merge'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ from_profile_email: fromProfileEmail, from_user_id: fromUserID, to_profile_email: toProfileEmail }),
  });
  if (!res.ok) throw new Error('Failed to merge users');
  return res.json();
}

// Auth
export async function fetchSetupStatus(): Promise<{ needs_setup: boolean }> {
  const res = await apiFetch(buildUrl('/api/auth/setup-status'));
  if (!res.ok) throw new Error('Failed to fetch setup status');
  return res.json();
}

export async function setup(email: string, name: string, password: string, setupToken: string): Promise<{ user: import('./types').AuthUser }> {
  const res = await apiFetch(buildUrl('/api/auth/setup'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Setup-Token': setupToken },
    body: JSON.stringify({ email, name, password }),
  });
  if (res.status === 403) {
    throw new Error('Setup token is invalid or setup is disabled. Check the current server logs or CCTRACE_SETUP_TOKEN configuration.');
  }
  if (!res.ok) {
    const data = await res.json().catch(() => ({ error: 'Setup failed' }));
    throw new Error(data.error || 'Setup failed');
  }
  return res.json();
}

export async function login(email: string, password: string): Promise<{ user: import('./types').AuthUser }> {
  const res = await apiFetch(buildUrl('/api/auth/login'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, password }),
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({ error: 'Login failed' }));
    throw new Error(data.error || 'Login failed');
  }
  return res.json();
}

export async function refreshToken(): Promise<{ user: import('./types').AuthUser }> {
  const res = await apiFetch(buildUrl('/api/auth/refresh'), { method: 'POST' });
  if (!res.ok) throw new Error('Token refresh failed');
  return res.json();
}

export async function logout(): Promise<void> {
  await apiFetch(buildUrl('/api/auth/logout'), { method: 'POST' });
}

export async function fetchMe(): Promise<import('./types').AuthUser> {
  const res = await apiFetch(buildUrl('/api/auth/me'));
  if (!res.ok) throw new Error('Not authenticated');
  return res.json();
}

export async function listOwnAPITokens(): Promise<import('./types').APITokenInfo[]> {
  const res = await apiFetch(buildUrl('/api/auth/api-tokens'));
  if (!res.ok) throw new Error('Failed to list API tokens');
  return res.json();
}

const apiTokenMutationError = async (res: Response, fallback: string): Promise<Error> => {
  const body = await res.json().catch(() => null);
  if (res.status === 403 && body?.code === 'password_change_required') {
    return new Error('Please change your password on the dashboard before managing API tokens.');
  }
  return new Error(fallback);
};

export async function createOwnAPIToken(name: string, expiresAt: string | null = null): Promise<import('./types').APITokenSecret> {
  const res = await apiFetch(buildUrl('/api/auth/api-tokens'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(expiresAt
      ? { name, expiration_mode: 'custom', expires_at: expiresAt }
      : { name, expiration_mode: 'unlimited' }),
  });
  if (!res.ok) throw await apiTokenMutationError(res, 'Failed to create API token');
  return res.json();
}

export async function rotateOwnAPIToken(id: number): Promise<import('./types').APITokenSecret> {
  const res = await apiFetch(buildUrl(`/api/auth/api-tokens/${id}/rotate`), { method: 'POST' });
  if (!res.ok) throw await apiTokenMutationError(res, 'Failed to rotate API token');
  return res.json();
}

export async function setOwnAPITokenActive(id: number, isActive: boolean): Promise<import('./types').APITokenInfo> {
  const res = await apiFetch(buildUrl(`/api/auth/api-tokens/${id}`), {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ is_active: isActive }),
  });
  if (!res.ok) throw await apiTokenMutationError(res, 'Failed to update API token mode');
  return res.json();
}

export async function setOwnAPITokenExpiration(id: number, expiresAt: string | null): Promise<import('./types').APITokenInfo> {
  const res = await apiFetch(buildUrl(`/api/auth/api-tokens/${id}`), {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(expiresAt
      ? { expiration_mode: 'custom', expires_at: expiresAt }
      : { expiration_mode: 'unlimited' }),
  });
  if (!res.ok) throw await apiTokenMutationError(res, 'Failed to update API token expiration');
  return res.json();
}

export async function revokeOwnAPIToken(id: number): Promise<void> {
  const res = await apiFetch(buildUrl(`/api/auth/api-tokens/${id}`), { method: 'DELETE' });
  if (!res.ok) throw new Error('Failed to revoke API token');
}

// User name mapping (cctrace_user_id -> name) - available to all authenticated users
export async function fetchUserNameMap(): Promise<Record<string, string>> {
  const res = await apiFetch(buildUrl('/api/users/names'));
  // Throw (don't return {}) so React Query retains the last good map on a failed
  // background refetch, instead of blanking names to email/id. See issue #24.
  if (!res.ok) throw new Error('Failed to fetch user name map');
  return res.json();
}

// Admin - Dashboard Users
export async function listDashboardUsers(): Promise<import('./types').DashboardUserInfo[]> {
  const res = await apiFetch(buildUrl('/api/admin/users'));
  if (!res.ok) throw new Error('Failed to list users');
  return res.json();
}

export async function fetchClientVersions(): Promise<import('./types').ClientVersionInfo[]> {
  const res = await apiFetch(buildUrl('/api/admin/client-versions'));
  if (!res.ok) throw new Error('Failed to fetch client versions');
  return res.json();
}

export async function fetchStorageReport(): Promise<import('./types').StorageReport> {
  const res = await apiFetch(buildUrl('/api/admin/retention'));
  if (!res.ok) throw new Error('Failed to fetch storage report');
  return res.json();
}

export async function fetchRetentionPreview(
  axis: import('./types').RetentionAxis,
  days: number,
): Promise<import('./types').RetentionPreview[]> {
  const res = await apiFetch(buildUrl('/api/admin/retention/preview', { axis, days: String(days) }));
  if (!res.ok) throw new Error('Failed to fetch retention preview');
  return res.json();
}

export async function setRetention(
  body: import('./types').SetRetentionBody,
): Promise<import('./types').StorageReport> {
  const res = await apiFetch(buildUrl('/api/admin/retention'), {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || 'Failed to set retention');
  }
  return res.json();
}

export async function fetchMaintenanceStatus(): Promise<import('./types').MaintenanceStatus> {
  const res = await apiFetch(buildUrl('/api/maintenance/status'));
  if (!res.ok) throw new Error('Failed to fetch maintenance status');
  return res.json();
}

export async function listExcludedAccounts(): Promise<import('./types').ExclusionList<import('./types').ExcludedAccount>> {
  const res = await apiFetch(buildUrl('/api/admin/excluded-accounts'));
  if (!res.ok) throw new Error('Failed to list excluded accounts');
  return res.json();
}

export async function listExcludedBillingAccounts(): Promise<import('./types').ExcludedBillingAccount[]> {
  const res = await apiFetch(buildUrl('/api/admin/excluded-billing-accounts'));
  if (!res.ok) throw new Error('Failed to list excluded billing accounts');
  return res.json();
}

export interface DeleteSessionResult {
  session_records: number;
  otel_events: number;
  otel_metrics: number;
  project_hash: string;
  project_name: string;
  project_blocked: boolean;
  purged_sessions: number;
}

export interface BlockedProject {
  project_hash: string;
  project_name: string;
  /** Directories inside the repository that sessions started in; '' when all at root. */
  subpaths: string;
  created_by: string;
  reason: string;
  created_at: string;
}

export interface DeletionPolicy {
  allow_owner_delete: boolean;
  updated_by: string;
  updated_at: string;
}

/**
 * Deletes one session outright -- rows removed, not hidden. `blockProject` also
 * stops the project being collected again; it requires an admin and the server
 * refuses the pair rather than silently dropping the flag, so a caller never
 * believes a block landed when it did not.
 */
export async function deleteSession(
  sessionId: string,
  opts: { blockProject?: boolean; purgeProject?: boolean; reason?: string } = {},
): Promise<DeleteSessionResult> {
  const res = await apiFetch(buildUrl(`/api/sessions/${encodeURIComponent(sessionId)}`), {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      block_project: !!opts.blockProject,
      purge_project: !!opts.purgeProject,
      reason: opts.reason || '',
    }),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'Failed to delete session' }));
    throw new Error(err.error || 'Failed to delete session');
  }
  return res.json();
}

/**
 * Delete a whole project: every session it holds, and optionally the project itself
 * from then on.
 *
 * Project-keyed rather than session-keyed because the picker has no session in hand,
 * and the project most worth removing is the one whose sessions are already gone.
 *
 * Takes a list because a picker row is an identity (#304): one line can stand for
 * several project hashes, the same repository opened from different worktrees. One
 * request keeps them from half-succeeding.
 */
export async function deleteProject(
  projectHashes: string[],
  opts: { blockProject?: boolean; reason?: string } = {},
): Promise<DeleteSessionResult> {
  const res = await apiFetch(buildUrl('/api/projects'), {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      project_hashes: projectHashes,
      block_project: !!opts.blockProject,
      reason: opts.reason || '',
    }),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'Failed to delete project' }));
    throw new Error(err.error || 'Failed to delete project');
  }
  return res.json();
}

/** How many OTHER sessions the project holds — the number the purge step shows. */
export async function fetchProjectSessionCount(
  projectHash: string,
  excludeSessionId: string,
): Promise<number> {
  const res = await apiFetch(
    buildUrl('/api/projects/session-count', {
      project_hash: projectHash,
      exclude_session_id: excludeSessionId,
    }),
  );
  if (!res.ok) throw new Error('Failed to fetch project session count');
  const body = await res.json();
  return body.count ?? 0;
}

export async function fetchDeletionPolicy(): Promise<DeletionPolicy> {
  const res = await apiFetch(buildUrl('/api/deletion-policy'));
  if (!res.ok) throw new Error('Failed to fetch deletion policy');
  return res.json();
}

export async function setDeletionPolicy(allowOwnerDelete: boolean): Promise<{ allow_owner_delete: boolean }> {
  const res = await apiFetch(buildUrl('/api/admin/deletion-policy'), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ allow_owner_delete: allowOwnerDelete }),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'Failed to update deletion policy' }));
    throw new Error(err.error || 'Failed to update deletion policy');
  }
  return res.json();
}

export async function fetchBlockedProjects(): Promise<BlockedProject[]> {
  const res = await apiFetch(buildUrl('/api/blocked-projects'));
  if (!res.ok) throw new Error('Failed to fetch blocked projects');
  return res.json();
}

export async function unblockProject(projectHash: string): Promise<{ unblocked: boolean }> {
  const res = await apiFetch(buildUrl('/api/blocked-projects'), {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ project_hash: projectHash }),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'Failed to unblock project' }));
    throw new Error(err.error || 'Failed to unblock project');
  }
  return res.json();
}

export async function excludeAccount(loginEmail: string, reason: string): Promise<{ status: string; hidden_events: number }> {
  const res = await apiFetch(buildUrl('/api/admin/excluded-accounts'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ login_email: loginEmail, reason }),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'Failed to exclude account' }));
    throw new Error(err.error || 'Failed to exclude account');
  }
  return res.json();
}

export async function excludeBillingAccount(
  billingProvider: string,
  accountID: string,
  reason: string,
): Promise<{ status: string; hidden_samples: number; hidden_events: number }> {
  const res = await apiFetch(buildUrl('/api/admin/excluded-billing-accounts'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ billing_provider: billingProvider, account_id: accountID, reason }),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'Failed to exclude billing account' }));
    throw new Error(err.error || 'Failed to exclude billing account');
  }
  return res.json();
}

export async function removeExcludedBillingAccount(
  billingProvider: string,
  accountID: string,
): Promise<{ status: string }> {
  const res = await apiFetch(
    buildUrl('/api/admin/excluded-billing-accounts', { billing_provider: billingProvider, account_id: accountID }),
    { method: 'DELETE' },
  );
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'Failed to remove excluded billing account' }));
    throw new Error(err.error || 'Failed to remove excluded billing account');
  }
  return res.json();
}

export async function listUnpricedModels(): Promise<import('./types').UnpricedModelsResponse> {
  const res = await apiFetch(buildUrl('/api/admin/unpriced-models'));
  if (!res.ok) throw new Error('Failed to list unpriced models');
  return res.json();
}

export async function markFlatRateModel(agent: string, model: string, reason: string): Promise<{ status: string }> {
  const res = await apiFetch(buildUrl('/api/admin/flat-rate-models'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ agent, model, reason }),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'Failed to mark model flat-rate' }));
    throw new Error(err.error || 'Failed to mark model flat-rate');
  }
  return res.json();
}

export async function unmarkFlatRateModel(agent: string, model: string): Promise<{ status: string }> {
  const res = await apiFetch(buildUrl('/api/admin/flat-rate-models', { agent, model }), { method: 'DELETE' });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'Failed to unmark model' }));
    throw new Error(err.error || 'Failed to unmark model');
  }
  return res.json();
}

export async function listObservedBillingAccounts(): Promise<
  import('./types').ExclusionList<import('./types').ObservedBillingAccount>
> {
  const res = await apiFetch(buildUrl('/api/self-exclusions/billing-accounts'));
  if (!res.ok) throw new Error('Failed to list your billing accounts');
  return res.json();
}

export async function selfExcludeBillingAccount(billingProvider: string, accountID: string): Promise<{ status: string }> {
  const res = await apiFetch(buildUrl('/api/self-exclusions/billing-accounts'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ billing_provider: billingProvider, account_id: accountID }),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'Failed to exclude billing account' }));
    throw new Error(err.error || 'Failed to exclude billing account');
  }
  return res.json();
}

export async function removeSelfExcludedBillingAccount(billingProvider: string, accountID: string): Promise<{ status: string }> {
  const res = await apiFetch(
    buildUrl('/api/self-exclusions/billing-accounts', { billing_provider: billingProvider, account_id: accountID }),
    { method: 'DELETE' },
  );
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'Failed to include billing account' }));
    throw new Error(err.error || 'Failed to include billing account');
  }
  return res.json();
}

export async function removeExcludedAccount(loginEmail: string): Promise<{ status: string }> {
  const res = await apiFetch(buildUrl('/api/admin/excluded-accounts', { login_email: loginEmail }), {
    method: 'DELETE',
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'Failed to remove excluded account' }));
    throw new Error(err.error || 'Failed to remove excluded account');
  }
  return res.json();
}

export async function createDashboardUser(data: import('./types').CreateUserRequest): Promise<import('./types').DashboardUserInfo & { temp_password?: string }> {
  const res = await apiFetch(buildUrl('/api/admin/users'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: 'Failed to create user' }));
    throw new Error(err.error || 'Failed to create user');
  }
  return res.json();
}

export async function updateDashboardUser(id: number, data: import('./types').UpdateUserRequest): Promise<import('./types').DashboardUserInfo> {
  const res = await apiFetch(buildUrl(`/api/admin/users/${id}`), {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data),
  });
  if (!res.ok) throw new Error('Failed to update user');
  return res.json();
}

export async function resetUserPassword(id: number): Promise<{ status: string; temp_password: string }> {
  const res = await apiFetch(buildUrl(`/api/admin/users/${id}/reset-password`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({}),
  });
  if (!res.ok) throw new Error('Failed to reset password');
  return res.json();
}

export async function revokeUserApiToken(id: number): Promise<{ status: string }> {
  const res = await apiFetch(buildUrl(`/api/admin/users/${id}/revoke-token`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({}),
  });
  if (!res.ok) throw new Error('Failed to revoke API token');
  return res.json();
}

export async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  const res = await apiFetch(buildUrl('/api/auth/change-password'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({ error: 'Failed to change password' }));
    throw new Error(data.error || 'Failed to change password');
  }
}

// Quota history
//
// The chart asks for one window length at a time: 5h and 7d are different time
// scales and a series mixing them names no state at all.
export async function fetchQuotaSamples(params: {
  from?: string;
  to?: string;
  windowMinutes?: number;
  billingProvider?: string;
}): Promise<import('./types').QuotaSample[]> {
  const q = new URLSearchParams();
  if (params.from) q.set('from', params.from);
  if (params.to) q.set('to', params.to);
  if (params.windowMinutes) q.set('window_minutes', String(params.windowMinutes));
  if (params.billingProvider) q.set('billing_provider', params.billingProvider);
  const res = await apiFetch(buildUrl(`/api/quota-samples?${q.toString()}`));
  if (!res.ok) throw new Error('Failed to fetch quota samples');
  return res.json();
}

export async function fetchMyQuotaUsage(): Promise<import('./quota-owner-types').MyQuotaUsageResponse> {
  const res = await apiFetch(buildUrl('/api/me/quota-usage'));
  if (!res.ok) throw new Error('Failed to fetch personal quota usage');
  return res.json();
}

export async function fetchMyQuotaWindowComparison(
  billingProvider: import('./quota-owner-types').QuotaBillingProvider,
  accountID: string,
  windowMinutes: 300 | 10080,
): Promise<import('./quota-owner-types').QuotaWindowComparison> {
  const res = await apiFetch(buildUrl('/api/me/quota-usage/window-comparison', {
    billing_provider: billingProvider,
    account_id: accountID,
    window_minutes: String(windowMinutes),
  }));
  if (!res.ok) throw new Error('Failed to fetch quota window comparison');
  return res.json();
}

export async function fetchMyQuotaBindings(): Promise<import('./quota-owner-types').QuotaAccountBinding[]> {
  const res = await apiFetch(buildUrl('/api/me/quota-bindings'));
  if (!res.ok) throw new Error('Failed to fetch quota account bindings');
  return res.json();
}

export async function requestQuotaAccountBinding(input: {
  billing_provider: import('./quota-owner-types').QuotaBillingProvider;
  account_id: string;
}): Promise<import('./quota-owner-types').QuotaAccountBinding> {
  const res = await apiFetch(buildUrl('/api/me/quota-bindings'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  if (!res.ok) throw new Error('Failed to request quota account verification');
  return res.json();
}

export async function fetchAdminQuotaBindings(status?: import('./quota-owner-types').QuotaBindingStatus): Promise<import('./quota-owner-types').AdminQuotaAccountBinding[]> {
  const res = await apiFetch(buildUrl('/api/admin/quota-bindings', { status: status || '' }));
  if (!res.ok) throw new Error('Failed to fetch quota verification queue');
  return res.json();
}

export async function approveQuotaAccountBinding(id: number, verificationReference: string): Promise<import('./quota-owner-types').AdminQuotaAccountBinding> {
  const res = await apiFetch(buildUrl(`/api/admin/quota-bindings/${id}/approve`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      verification_method: 'provider_console',
      verification_reference: verificationReference,
      verified_externally: true,
    }),
  });
  if (!res.ok) throw new Error('Failed to approve quota account verification');
  return res.json();
}

export async function revokeQuotaAccountBinding(id: number): Promise<void> {
  const res = await apiFetch(buildUrl(`/api/admin/quota-bindings/${id}/revoke`), { method: 'POST' });
  if (!res.ok) throw new Error('Failed to revoke quota account verification');
}

/* ---------- Weekly AI report ---------- */

/** A refused AI report request. `code` is the server's error key
 *  (`consent_required`, `already_running`, …) so the screen can say why. */
export class AIReportRequestError extends Error {
  readonly name = 'AIReportRequestError';

  /** `detail` is the server's message, '' when it sent none. */
  constructor(readonly status: number, readonly code: string, readonly detail = '') {
    super(code);
  }
}

const aiReportError = async (res: Response): Promise<AIReportRequestError> => {
  const body = await res.json().catch(() => ({}));
  return new AIReportRequestError(
    res.status,
    typeof body.error === 'string' ? body.error : `http_${res.status}`,
    typeof body.message === 'string' ? body.message : '',
  );
};

export async function fetchAIReports(week: string, tz: string): Promise<import('./types').AIReportsResponse> {
  const res = await apiFetch(buildUrl('/api/ai-reports', { week, tz }));
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

export async function startAIReport(req: import('./types').StartAIReportRequest): Promise<import('./types').StartAIReportResponse> {
  const res = await apiFetch(buildUrl('/api/ai-reports'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  });
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

export async function cancelAIReportRun(runId: number): Promise<void> {
  const res = await apiFetch(buildUrl(`/api/ai-reports/runs/${runId}`), { method: 'DELETE' });
  if (!res.ok) throw await aiReportError(res);
}

/** Opens a run's progress stream. Through apiFetch rather than EventSource so an
 *  expired access token is refreshed and the request retried (plan §3). */
export async function openAIReportRunEvents(runId: number, signal: AbortSignal): Promise<Response> {
  return apiFetch(buildUrl(`/api/ai-reports/runs/${runId}/events`), {
    headers: { Accept: 'text/event-stream' },
    signal,
  });
}

export async function fetchAIConsent(): Promise<import('./types').AIConsentInfo> {
  const res = await apiFetch(buildUrl('/api/ai/consent'));
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

/** 409 when the runtime key or disclosure version is no longer current. */
export async function grantAIConsent(req: import('./types').AIConsentRequest): Promise<void> {
  const res = await apiFetch(buildUrl('/api/ai/consent'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  });
  if (!res.ok) throw await aiReportError(res);
}

/** Saves this user's own run time. Answers with what now applies, so the screen
 *  shows the resolved value rather than combining the layers itself. */
export async function setAIUserSchedule(
  req: import('./types').SetAIUserScheduleRequest,
): Promise<{ schedule: import('./types').AIUserSchedule }> {
  const res = await apiFetch(buildUrl('/api/ai/schedule'), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  });
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

export async function fetchAdminAI(): Promise<import('./types').AdminAIResponse> {
  const res = await apiFetch(buildUrl('/api/admin/ai'));
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

export async function updateAdminSessionRecordSettings(req: UpdateAdminSessionRecordSettingsRequest): Promise<void> {
  const res = await apiFetch(buildUrl('/api/admin/ai/records'), {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(req),
  });
  if (!res.ok) throw await sessionRecordRequestError(res);
}

/** The catalog of `runtime`. Errors carry runtime_unconfigured,
 *  runtime_not_logged_in or catalog_unavailable. */
export async function fetchAdminAIModels(runtime: string): Promise<import('./types').AdminAIModelsResponse> {
  const res = await apiFetch(buildUrl('/api/admin/ai/models', { runtime }));
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

export async function updateAdminAISettings(
  req: import('./types').UpdateAdminAISettingsRequest,
  runtime: string,
): Promise<{ settings: import('./types').AdminAISettings }> {
  const res = await apiFetch(buildUrl('/api/admin/ai/settings', { runtime }), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  });
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

/** Saves the default weekly firing. Errors: invalid_request, invalid_schedule. */
export async function updateAdminAISchedule(
  req: import('./types').UpdateAdminAIScheduleRequest,
): Promise<{ schedule: import('./types').AdminAISchedule }> {
  const res = await apiFetch(buildUrl('/api/admin/ai/schedule'), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  });
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

/** Errors: invalid_request, invalid_run_policy. */
export async function updateAdminAIRunPolicy(
  req: import('./types').UpdateAdminAIRunPolicyRequest,
): Promise<{ run_policy: import('./types').AdminAIRunPolicy }> {
  const res = await apiFetch(buildUrl('/api/admin/ai/run-policy'), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  });
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

/** '' clears the admin choice. Errors: invalid_request, unknown_runtime,
 *  runtime_unconfigured (detail says why), run_in_progress. */
export async function setAdminAIRuntime(runtime: string): Promise<import('./types').SetAdminAIRuntimeResponse> {
  const res = await apiFetch(buildUrl('/api/admin/ai/runtime'), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ runtime }),
  });
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

/** null hands the switch back to the environment. Errors: invalid_request,
 *  runtime_not_selected, session_required. */
export async function setAdminAIEnabled(enabled: boolean | null): Promise<import('./types').SetAdminAIEnabledResponse> {
  const res = await apiFetch(buildUrl('/api/admin/ai/enabled'), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled }),
  });
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

/** The key is sent once and never returned. Errors: invalid_request, unknown_provider,
 *  env_managed, secrets_unavailable, credential_store_failed. */
export async function setAdminAIProviderKey(
  provider: string,
  apiKey: string,
): Promise<{ credential: import('./types').AdminAICredential }> {
  const res = await apiFetch(buildUrl(`/api/admin/ai/providers/${encodeURIComponent(provider)}/key`), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ api_key: apiKey }),
  });
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

export async function deleteAdminAIProviderKey(provider: string): Promise<{ credential: import('./types').AdminAICredential }> {
  const res = await apiFetch(buildUrl(`/api/admin/ai/providers/${encodeURIComponent(provider)}/key`), { method: 'DELETE' });
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

/** Errors carry runtime_unconfigured. */
export async function fetchAdminAIAccount(): Promise<import('./types').AdminAIAccount> {
  const res = await apiFetch(buildUrl('/api/admin/ai/account'));
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

/** A device code start answers with the login; an API key login answers once stored.
 *  Errors: session_required, env_managed, auth_file_is_symlink, login_in_progress, run_in_progress,
 *  invalid_request, login_failed. */
export async function startAdminAILogin(
  req: import('./types').StartAdminAILoginRequest,
): Promise<import('./types').AdminAIDeviceLogin | { status: 'succeeded' }> {
  const res = await apiFetch(buildUrl('/api/admin/ai/account/login'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  });
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

export async function fetchAdminAILogin(loginId: string): Promise<import('./types').AdminAIDeviceLogin> {
  const res = await apiFetch(buildUrl(`/api/admin/ai/account/login/${encodeURIComponent(loginId)}`));
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

export async function cancelAdminAILogin(loginId: string): Promise<import('./types').AdminAIDeviceLogin> {
  const res = await apiFetch(buildUrl(`/api/admin/ai/account/login/${encodeURIComponent(loginId)}`), { method: 'DELETE' });
  if (!res.ok) throw await aiReportError(res);
  return res.json();
}

export async function logoutAdminAI(): Promise<void> {
  const res = await apiFetch(buildUrl('/api/admin/ai/account/logout'), { method: 'POST' });
  if (!res.ok) throw await aiReportError(res);
}

export { setAuthFailureHandler };
