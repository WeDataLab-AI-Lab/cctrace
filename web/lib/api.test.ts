import { QueryClient, QueryObserver } from '@tanstack/react-query';
import type { SessionAnalysisFull } from './session-analysis-types';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { serverCalendar } from '@/components/sessions/calendar/server-calendar.fixture';

import {
  createOwnAPIToken,
  deleteAdminAIProviderKey,
  deleteUserData,
  fetchAdminAIModels,
  listOwnAPITokens,
  setAdminAIProviderKey,
  setAdminAIRuntime,
  updateAdminAISettings,
  fetchLatestActivity,
  fetchProjectRuleDetail,
  fetchProjectRules,
  fetchProjects,
  fetchSessionOverview,
  fetchSessionOverviewCount,
  fetchSessionOverviewCategories,
  fetchSessionCalendar,
  fetchSessionRecordSearch,
  fetchSessionRecordStatus,
  refreshSessionRecordRange,
  regenerateSessionRecord,
  fetchSessionRecordConsent,
  grantSessionRecordConsent,
  withdrawSessionRecordConsent,
  deleteOwnSessionRecords,
  fetchAdminSessionRecordVocabulary,
  setAdminSessionRecordVocabulary,
  mergeAdminSessionRecordVocabulary,
  updateAdminSessionRecordSettings,
  SessionCalendarRequestError,
  fetchSessionAnalysis,
  SessionAnalysisRequestError,
  fetchSessionRecordsPage,
  fetchUserNameMap,
  fetchUsers,
  fetchMyQuotaUsage,
  fetchMyQuotaWindowComparison,
  requestQuotaAccountBinding,
  approveQuotaAccountBinding,
  setAuthFailureHandler,
  revokeOwnAPIToken,
  rotateOwnAPIToken,
  setOwnAPITokenActive,
  setOwnAPITokenExpiration,
} from './api';

interface FetchScenario {
  readonly status?: number;
  readonly body?: unknown;
  readonly error?: Error;
}

class UnexpectedFetchCallError extends Error {
  readonly name = 'UnexpectedFetchCallError';

  constructor(readonly callCount: number) {
    super(`unexpected fetch call ${callCount}`);
  }
}

const jsonResponse = (status: number, body: unknown): Response =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

const createFetchStub = (scenarios: readonly FetchScenario[]): { readonly calls: readonly string[] } => {
  const calls: string[] = [];
  const fetchStub = vi.fn((input: RequestInfo | URL): Promise<Response> => {
    calls.push(String(input));

    const scenario = scenarios[calls.length - 1];
    if (!scenario) {
      return Promise.reject(new UnexpectedFetchCallError(calls.length));
    }
    if (scenario.error) {
      return Promise.reject(scenario.error);
    }

    return Promise.resolve(jsonResponse(scenario.status ?? 200, scenario.body ?? []));
  });

  vi.stubGlobal('fetch', fetchStub);
  return { calls };
};

describe('apiFetch auth refresh handling', () => {
  afterEach(async () => {
    await Promise.resolve();
    setAuthFailureHandler(null);
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it('keeps the current session when refresh returns a transient server error', async () => {
    const { calls } = createFetchStub([
      { status: 401 },
      { status: 503, body: { error: 'deploying' } },
    ]);
    let authFailures = 0;
    setAuthFailureHandler(() => {
      authFailures += 1;
    });

    await expect(fetchUsers()).rejects.toThrow('fetchUsers failed');

    expect(authFailures).toBe(0);
    expect(calls).toStrictEqual([
      'http://localhost:8080/api/users',
      'http://localhost:8080/api/auth/refresh',
    ]);
  });

  it('keeps the current session when refresh hits a network error', async () => {
    createFetchStub([
      { status: 401 },
      { error: new TypeError('connection reset') },
    ]);
    let authFailures = 0;
    setAuthFailureHandler(() => {
      authFailures += 1;
    });

    await expect(fetchUsers()).rejects.toThrow('fetchUsers failed');

    expect(authFailures).toBe(0);
  });

  it('notifies auth failure when refresh is actually expired', async () => {
    createFetchStub([
      { status: 401 },
      { status: 401, body: { error: 'expired' } },
    ]);
    let authFailures = 0;
    setAuthFailureHandler(() => {
      authFailures += 1;
    });

    await expect(fetchUsers()).rejects.toThrow('fetchUsers failed');

    expect(authFailures).toBe(1);
  });

  it('retries the original request after a successful refresh', async () => {
    const user = {
      user_id: 'user-1',
      profile_email: 'user@example.com',
      login_emails: ['user@example.com'],
      total_cost: 12.5,
    };
    const { calls } = createFetchStub([
      { status: 401 },
      { status: 200, body: {} },
      { status: 200, body: [user] },
    ]);

    await expect(fetchUsers()).resolves.toStrictEqual([user]);

    expect(calls).toStrictEqual([
      'http://localhost:8080/api/users',
      'http://localhost:8080/api/auth/refresh',
      'http://localhost:8080/api/users',
    ]);
  });
});

describe('project rules requests', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it('forwards the React Query abort signal to list fetches', async () => {
    const fetchStub = vi.fn().mockResolvedValue(jsonResponse(200, { items: [], total: 0 }));
    vi.stubGlobal('fetch', fetchStub);
    const controller = new AbortController();

    await fetchProjectRules({ query: 'rules' }, controller.signal);

    expect(fetchStub).toHaveBeenCalledWith(
      'http://localhost:8080/api/project-rules?query=rules',
      expect.objectContaining({ credentials: 'include', signal: controller.signal }),
    );
  });

  it('aborts an obsolete list request when React Query supersedes it', async () => {
    let firstSignal: AbortSignal | undefined;
    let markStarted: (() => void) | undefined;
    const started = new Promise<void>((resolve) => {
      markStarted = resolve;
    });
    const fetchStub = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).includes('query=first')) {
        firstSignal = init?.signal as AbortSignal;
        markStarted?.();
        return new Promise<Response>((_resolve, reject) => {
          firstSignal?.addEventListener('abort', () => reject(firstSignal?.reason), { once: true });
        });
      }
      return Promise.resolve(jsonResponse(200, { items: [], total: 0 }));
    });
    vi.stubGlobal('fetch', fetchStub);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const observer = new QueryObserver(client, {
      queryKey: ['project-rules', 'first'],
      queryFn: ({ signal }) => fetchProjectRules({ query: 'first' }, signal),
    });
    const unsubscribe = observer.subscribe(() => {});
    await started;

    observer.setOptions({
      queryKey: ['project-rules', 'second'],
      queryFn: ({ signal }) => fetchProjectRules({ query: 'second' }, signal),
    });

    expect(firstSignal?.aborted).toBe(true);
    unsubscribe();
    client.clear();
  });

  it('requests content for only the selected detail version', async () => {
    const fetchStub = vi.fn().mockResolvedValue(jsonResponse(200, { rule: {}, versions: [], comments: [] }));
    vi.stubGlobal('fetch', fetchStub);

    await fetchProjectRuleDetail(7, 11);

    expect(fetchStub).toHaveBeenCalledWith(
      'http://localhost:8080/api/project-rules/7?selected_version_id=11',
      expect.objectContaining({ credentials: 'include' }),
    );
  });
});

describe('self-service API token requests', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it('uses the signed-in token lifecycle endpoint', async () => {
    const fetchStub = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(200, []))
      .mockResolvedValueOnce(jsonResponse(201, { id: 7, api_token: 'cct_new' }))
      .mockResolvedValueOnce(jsonResponse(200, { id: 7, api_token: 'cct_rotated' }))
      .mockResolvedValueOnce(jsonResponse(200, { id: 7, is_active: false }))
      .mockResolvedValueOnce(jsonResponse(200, { id: 7, expires_at: null }))
      .mockResolvedValueOnce(jsonResponse(200, { status: 'ok' }));
    vi.stubGlobal('fetch', fetchStub);

    await expect(listOwnAPITokens()).resolves.toStrictEqual([]);
    await expect(createOwnAPIToken('CI')).resolves.toMatchObject({ id: 7, api_token: 'cct_new' });
    await expect(rotateOwnAPIToken(7)).resolves.toMatchObject({ id: 7, api_token: 'cct_rotated' });
    await expect(setOwnAPITokenActive(7, false)).resolves.toMatchObject({ id: 7, is_active: false });
    await expect(setOwnAPITokenExpiration(7, null)).resolves.toMatchObject({ id: 7, expires_at: null });
    await expect(revokeOwnAPIToken(7)).resolves.toBeUndefined();

    expect(fetchStub).toHaveBeenNthCalledWith(
      1,
      'http://localhost:8080/api/auth/api-tokens',
      expect.objectContaining({ credentials: 'include' }),
    );
    expect(fetchStub).toHaveBeenNthCalledWith(
      2,
      'http://localhost:8080/api/auth/api-tokens',
      expect.objectContaining({ method: 'POST' }),
    );
    expect(fetchStub).toHaveBeenNthCalledWith(
      3,
      'http://localhost:8080/api/auth/api-tokens/7/rotate',
      expect.objectContaining({ method: 'POST' }),
    );
    expect(fetchStub).toHaveBeenNthCalledWith(
      4,
      'http://localhost:8080/api/auth/api-tokens/7',
      expect.objectContaining({ method: 'PATCH', body: JSON.stringify({ is_active: false }) }),
    );
    expect(fetchStub).toHaveBeenNthCalledWith(
      5,
      'http://localhost:8080/api/auth/api-tokens/7',
      expect.objectContaining({ method: 'PATCH', body: JSON.stringify({ expiration_mode: 'unlimited' }) }),
    );
    expect(fetchStub).toHaveBeenNthCalledWith(
      6,
      'http://localhost:8080/api/auth/api-tokens/7',
      expect.objectContaining({ method: 'DELETE' }),
    );
    expect(fetchStub.mock.calls[1]?.[1]).toMatchObject({ body: JSON.stringify({ name: 'CI', expiration_mode: 'unlimited' }) });
  });
});

describe('admin AI runtime and provider key requests', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it('scopes models and settings to a runtime and writes runtime and keys', async () => {
    const fetchStub = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(200, { models: [] }))
      .mockResolvedValueOnce(jsonResponse(200, { settings: {} }))
      .mockResolvedValueOnce(jsonResponse(200, { selected_runtime: 'claude-api', runtime_source: 'admin', settings: null }))
      .mockResolvedValueOnce(jsonResponse(200, { credential: { source: 'admin' } }))
      .mockResolvedValueOnce(jsonResponse(200, { credential: { source: 'none' } }));
    vi.stubGlobal('fetch', fetchStub);

    await fetchAdminAIModels('claude-api');
    await updateAdminAISettings({ model: 'm', reasoning_effort: '' }, 'claude-api');
    await expect(setAdminAIRuntime('claude-api')).resolves.toMatchObject({ selected_runtime: 'claude-api' });
    await expect(setAdminAIProviderKey('anthropic', 'sk-ant-x')).resolves.toMatchObject({ credential: { source: 'admin' } });
    await expect(deleteAdminAIProviderKey('openai')).resolves.toMatchObject({ credential: { source: 'none' } });

    expect(fetchStub).toHaveBeenNthCalledWith(1, 'http://localhost:8080/api/admin/ai/models?runtime=claude-api', expect.anything());
    expect(fetchStub).toHaveBeenNthCalledWith(
      2,
      'http://localhost:8080/api/admin/ai/settings?runtime=claude-api',
      expect.objectContaining({ method: 'PUT' }),
    );
    expect(fetchStub).toHaveBeenNthCalledWith(
      3,
      'http://localhost:8080/api/admin/ai/runtime',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify({ runtime: 'claude-api' }) }),
    );
    expect(fetchStub).toHaveBeenNthCalledWith(
      4,
      'http://localhost:8080/api/admin/ai/providers/anthropic/key',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify({ api_key: 'sk-ant-x' }) }),
    );
    expect(fetchStub).toHaveBeenNthCalledWith(
      5,
      'http://localhost:8080/api/admin/ai/providers/openai/key',
      expect.objectContaining({ method: 'DELETE' }),
    );
  });

  it('keeps the server message on a refused change', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(409, { error: 'runtime_unconfigured', message: 'API 키가 없습니다' })),
    );

    await expect(setAdminAIRuntime('openai-api')).rejects.toMatchObject({
      status: 409,
      code: 'runtime_unconfigured',
      detail: 'API 키가 없습니다',
    });
  });
});

describe('session and chart query parameters', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it('sends assembled overview filters when listing sessions', async () => {
    const { calls } = createFetchStub([{ body: [] }]);

    await fetchSessionOverview({
      loginEmail: 'account@example.com',
      limit: 100,
      assembled: true,
      source: 'interactive',
      offset: 200,
      projectHashes: ['project-a', 'project-b'],
      agent: 'claude',
      classification: 'working',
      since: '2026-08-09T01:00:00.000Z',
      until: '2026-08-09T02:00:00.000Z',
    });

    const url = new URL(calls[0] ?? '');
    expect(url.pathname).toBe('/api/session-overview');
    expect(url.searchParams.get('assembled')).toBe('1');
    expect(url.searchParams.get('login_email')).toBe('account@example.com');
    expect(url.searchParams.get('limit')).toBe('100');
    expect(url.searchParams.get('source')).toBe('interactive');
    expect(url.searchParams.get('offset')).toBe('200');
    expect(url.searchParams.get('project_hashes')).toBe('project-a,project-b');
    expect(url.searchParams.get('agent')).toBe('claude');
    expect(url.searchParams.get('classification')).toBe('working');
    expect(url.searchParams.get('since')).toBe('2026-08-09T01:00:00.000Z');
    expect(url.searchParams.get('until')).toBe('2026-08-09T02:00:00.000Z');
  });

  it('sends analysis filters only with include=analysis and keeps count in the same scope', async () => {
    const { calls } = createFetchStub([{ body: [] }, { body: { count: 13 } }, { body: [] }]);
    const filters = { analysis: true, category: 'ended' as const, query: 'literal %_title', plan: 'file' as const,
      sort: 'requests' as const, source: 'interactive', projectHashes: ['a', 'b'], classification: 'working' as const };
    await fetchSessionOverview(filters);
    await fetchSessionOverviewCount(filters);
    await fetchSessionOverview({ category: 'ended', query: 'ignored', plan: 'file', sort: 'requests' });
    const list = new URL(calls[0]);
    const count = new URL(calls[1]);
    for (const url of [list, count]) {
      expect(url.searchParams.get('include')).toBe('analysis');
      expect(url.searchParams.get('category')).toBe('ended');
      expect(url.searchParams.get('q')).toBe('literal %_title');
      expect(url.searchParams.get('plan')).toBe('file');
      expect(url.searchParams.get('sort')).toBe('requests');
      expect(url.searchParams.get('project_hashes')).toBe('a,b');
      expect(url.searchParams.get('classification')).toBe('working');
    }
    expect(new URL(calls[2]).searchParams.has('include')).toBe(false);
    expect(new URL(calls[2]).searchParams.has('category')).toBe(false);
    expect(new URL(calls[2]).searchParams.has('q')).toBe(false);
    expect(new URL(calls[2]).searchParams.has('plan')).toBe(false);
    expect(new URL(calls[2]).searchParams.has('sort')).toBe(false);
  });

  it('rejects malformed analysis overview rows but retains the legacy list response', async () => {
    const row = { session_id: 'observed', profile_email: 'owner@example.test', model: '', agent: 'claude',
      start_time: '2026-10-01T00:00:00Z', end_time: '2026-10-01T01:00:00Z',
      input_tokens: 0, output_tokens: 0, cost_usd: 0, event_count: 0, has_sync: false,
      analysis: { state: 'ready', title: { state: 'ok', text: 'Observed', source: 'ai' },
        requests: 0, inputs: 0, plan_flags: { has_plan_file: false, used_plan_mode: false } } };
    createFetchStub([{ body: [row] }, { body: [{ ...row, analysis: { state: 'invented' } }] },
      { body: [{ ...row, analysis: { ...row.analysis, category: 'invented' } }] },
      { body: [{ ...row, analysis: { ...row.analysis, inputs: '0' } }] },
      { body: [null] }, { body: [{ session_id: 'legacy' }] }]);
    await expect(fetchSessionOverview({ analysis: true })).resolves.toEqual([row]);
    for (let index = 0; index < 4; index++) {
      await expect(fetchSessionOverview({ analysis: true })).rejects.toThrow('Invalid session overview analysis response');
    }
    await expect(fetchSessionOverview()).resolves.toEqual([{ session_id: 'legacy' }]);
  });

  it('accepts the truncated fallback row the list SQL publishes without a computed title or context', async () => {
    const base = { session_id: 'truncated', profile_email: 'owner@example.test', user_id: 'u1', login_email: '',
      account_count: 1, login_email_inferred: false, model: '', agent: 'claude',
      start_time: '2026-10-01T00:00:00Z', end_time: '2026-10-01T01:00:00Z',
      input_tokens: 0, output_tokens: 0, cost_usd: 0, event_count: 0, has_sync: false,
      project_hash: '', project_name: '', entrypoint: '', has_enriched: false };
    // session_analysis_facts stores the fallback's uncomputed title/context as NULL and ''.
    const truncated = { state: 'truncated', title: { text: null, source: '', state: '' }, requests: null, inputs: null,
      context: { percent: null, tokens: null, source: '', large_window_seen: false },
      plan_flags: { has_plan_file: false, used_plan_mode: false } };
    createFetchStub([{ body: [{ ...base, analysis: truncated }] },
      { body: [{ ...base, analysis: { ...truncated, state: 'ready' } }] },
      { body: [{ ...base, analysis: { ...truncated, title: { text: null, source: 'invented', state: '' } } }] },
      { body: [{ ...base, analysis: { ...truncated, plan_flags: { has_plan_file: null, used_plan_mode: false } } }] }]);
    const [row] = await fetchSessionOverview({ analysis: true });
    expect(row.analysis).toEqual({ state: 'truncated', requests: null, inputs: null,
      plan_flags: { has_plan_file: false, used_plan_mode: false } });
    for (let index = 0; index < 3; index++) {
      await expect(fetchSessionOverview({ analysis: true })).rejects.toThrow('Invalid session overview analysis response');
    }
  });

  it('sends the calendar scope with the server parameter names and nothing else', async () => {
    const { calls } = createFetchStub([{ body: serverCalendar() }, { body: serverCalendar() }]);
    await fetchSessionCalendar({ from: '2026-10-04T15:00:00.000Z', to: '2026-10-11T15:00:00.000Z', userId: 'picked-uid',
      loginEmail: 'login@example.test', projectHashes: ['p1', 'p2'], agent: 'codex', source: 'interactive', assembled: true, classification: 'working' });
    await fetchSessionCalendar({ from: '2026-10-04T15:00:00.000Z', to: '2026-10-11T15:00:00.000Z', profileEmail: 'picked@example.test' });
    const scoped = new URL(calls[0]);
    expect(scoped.pathname).toBe('/api/session-calendar');
    expect(Object.fromEntries(scoped.searchParams)).toEqual({ from: '2026-10-04T15:00:00.000Z', to: '2026-10-11T15:00:00.000Z',
      user_id: 'picked-uid', login_email: 'login@example.test', project_hashes: 'p1,p2', agent: 'codex', source: 'interactive',
      assembled: '1', classification: 'working' });
    expect(Object.fromEntries(new URL(calls[1]).searchParams)).toEqual({ from: '2026-10-04T15:00:00.000Z', to: '2026-10-11T15:00:00.000Z',
      profile_email: 'picked@example.test' });
  });

  it('normalizes only the uncollected values the calendar server currently sends', async () => {
    const body = serverCalendar();
    const malformed = serverCalendar();
    malformed.sessions[0].title = { text: '관측 제목', source: 'invented', state: 'ok' };
    createFetchStub([{ body }, { body: malformed }]);
    const calendar = await fetchSessionCalendar({ from: body.from, to: body.to });
    const [ready, truncated, building, capped] = calendar.sessions;
    expect(ready.colors).toEqual({ project: { hash: 'p1', name: 'cctrace' }, agent: 'claude', model: 'claude-test', profile: null,
      components: [], screens: [], kinds: [], status: null });
    expect(truncated.colors).not.toHaveProperty('status');
    expect(truncated.title).toEqual({ text: null, source: 'none', state: 'none' });
    expect(truncated.cost).toEqual({ usd: null, source: 'none' });
    expect(building.title).toEqual({ text: null, source: 'none', state: 'none' });
    expect(capped.inputs.items).toHaveLength(2000);
    expect(capped.inputs.truncated).toBe(true);
    expect(ready.segments).toEqual(body.sessions[0].segments);
    expect(ready.overview?.session_id).toBe('ready-owned');
    // A ready title with an unknown source is a contract violation, not an uncollected value.
    expect((await fetchSessionCalendar({ from: body.from, to: body.to })).sessions[0].title.source).toBe('invented');
  });

  it('drops an overview that does not describe its own session instead of selecting the wrong one', async () => {
    const body = serverCalendar();
    body.sessions[0].overview = { ...body.sessions[0].overview, session_id: 'other-session' };
    body.sessions[1].overview = { ...body.sessions[1].overview, start_time: 7 as unknown as string };
    createFetchStub([{ body }]);
    const calendar = await fetchSessionCalendar({ from: body.from, to: body.to });
    expect(calendar.sessions[0]).not.toHaveProperty('overview');
    expect(calendar.sessions[1]).not.toHaveProperty('overview');
    expect(calendar.sessions[2].overview?.session_id).toBe('building-row');
  });

  it('keeps disabled, refused and malformed calendar reads distinct', async () => {
    createFetchStub([{ status: 404, body: { error: 'session_analysis_disabled' } }, { status: 400, body: { error: 'invalid calendar scope' } },
      { status: 503, body: { error: 'deploying' } }, { body: { from: 'a', to: 'b', sessions: [] } },
      { body: { ...serverCalendar(), sessions: [{ session_id: '' }] } }]);
    const disabled = await fetchSessionCalendar({ from: 'a', to: 'b' }).catch((error: unknown) => error);
    expect(disabled).toBeInstanceOf(SessionCalendarRequestError);
    expect(disabled).toMatchObject({ status: 404, code: 'session_analysis_disabled' });
    await expect(fetchSessionCalendar({ from: 'a', to: 'b' })).rejects.toMatchObject({ status: 400, code: 'invalid calendar scope' });
    await expect(fetchSessionCalendar({ from: 'a', to: 'b' })).rejects.toMatchObject({ status: 503 });
    await expect(fetchSessionCalendar({ from: 'a', to: 'b' })).rejects.toThrow('Invalid session calendar response');
    await expect(fetchSessionCalendar({ from: 'a', to: 'b' })).rejects.toThrow('Invalid session calendar response');
  });

  it('reads owner category totals for the full scope, independently of page filters', async () => {
    const counts = { waiting: 0, running: 0, ended: 213, stale_live: 0, open_question: 0, idle: 0,
      collector_delayed: 0, recent: 0, inactive: 0 };
    const { calls } = createFetchStub([{ body: counts }]);
    await expect(fetchSessionOverviewCategories({ source: 'all', assembled: true, projectHashes: ['p1', 'p2'], agent: 'codex', classification: 'done' }))
      .resolves.toEqual(counts);
    const url = new URL(calls[0]);
    expect(url.pathname).toBe('/api/session-overview/categories');
    expect(url.searchParams.get('source')).toBe('all');
    expect(url.searchParams.get('assembled')).toBe('1');
    expect(url.searchParams.get('project_hashes')).toBe('p1,p2');
    expect(url.searchParams.get('agent')).toBe('codex');
    expect(url.searchParams.get('classification')).toBe('done');
    expect(url.searchParams.has('limit')).toBe(false);
    expect(url.searchParams.has('category')).toBe(false);
  });

  it('rejects a failed or malformed category response rather than showing zero totals', async () => {
    const valid = { waiting: 0, running: 0, ended: 0, stale_live: 0, open_question: 0, idle: 0,
      collector_delayed: 0, recent: 0, inactive: 0 };
    createFetchStub([{ status: 503 }, { body: { ...valid, ended: -1 } },
      { body: { ...valid, unexpected: 4 } }, { body: { ended: 4 } }]);
    await expect(fetchSessionOverviewCategories()).rejects.toThrow('Failed to fetch session overview categories');
    for (let index = 0; index < 3; index++) {
      await expect(fetchSessionOverviewCategories()).rejects.toThrow('Invalid session overview categories');
    }
  });

  it('scopes the project picker with every filter the session list sends', async () => {
    const { calls } = createFetchStub([{ body: [] }]);

    await fetchProjects({
      source: 'interactive',
      agent: 'claude',
      loginEmail: 'account@example.com',
      assembled: true,
    });

    const url = new URL(calls[0] ?? '');
    expect(url.pathname).toBe('/api/projects');
    expect(url.searchParams.get('source')).toBe('interactive');
    expect(url.searchParams.get('agent')).toBe('claude');
    expect(url.searchParams.get('login_email')).toBe('account@example.com');
    expect(url.searchParams.get('assembled')).toBe('1');
  });

  it('asks for every project when no scope is given', async () => {
    const { calls } = createFetchStub([{ body: [] }]);

    await fetchProjects();

    const url = new URL(calls[0] ?? '');
    expect(url.pathname).toBe('/api/projects');
    expect([...url.searchParams.keys()]).toEqual([]);
  });

  it('surfaces sessions the account filter dropped for lack of a known account', async () => {
    createFetchStub([{ body: { count: 7, unattributed: 12 } }]);

    await expect(fetchSessionOverviewCount({ loginEmail: 'account@example.com' }))
      .resolves.toEqual({ count: 7, unattributed: 12, classification_counts: undefined });
  });

  it('does not invent an all-tab total when the count response lacks one', async () => {
    createFetchStub([{ body: { classification_counts: { working: 3 } } }]);
    await expect(fetchSessionOverviewCount()).rejects.toThrow('Invalid session overview count');
  });

  it('sends assembled count filters so the total matches the folded list', async () => {
    const classificationCounts = { unsorted: 2, consulting: 3, working: 5, later: 1, done: 4 };
    const { calls } = createFetchStub([{ body: { count: 7, classification_counts: classificationCounts } }]);

    await expect(fetchSessionOverviewCount({
      loginEmail: 'account@example.com',
      assembled: true,
      source: 'interactive',
      projectHashes: ['project-a'],
      agent: 'codex',
      classification: 'later',
      since: '2026-08-09T01:00:00.000Z',
      until: '2026-08-09T02:00:00.000Z',
    })).resolves.toEqual({ count: 7, unattributed: 0, classification_counts: classificationCounts });

    const url = new URL(calls[0] ?? '');
    expect(url.pathname).toBe('/api/session-overview/count');
    expect(url.searchParams.get('assembled')).toBe('1');
    expect(url.searchParams.get('login_email')).toBe('account@example.com');
    expect(url.searchParams.get('source')).toBe('interactive');
    expect(url.searchParams.get('project_hashes')).toBe('project-a');
    expect(url.searchParams.get('agent')).toBe('codex');
    expect(url.searchParams.get('classification')).toBe('later');
    expect(url.searchParams.get('since')).toBe('2026-08-09T01:00:00.000Z');
    expect(url.searchParams.get('until')).toBe('2026-08-09T02:00:00.000Z');
  });

  const readerAnalysis = (id: string): SessionAnalysisFull => ({
    session_id: id, short_id: id.slice(0, 8), owner_view: false, state: 'ready',
    rules_version: 3, computed_at: '2026-10-08T00:00:00Z', pending_since: null,
    header: {
      title: { text: 'Work', source: 'first_request', state: 'ok' }, name: null,
      agent: 'claude', model: '', branch: '', project: { hash: '', name: null },
      accounts: { login_emails: [], inferred: false }, started_at: null,
      last_activity_at: null, duration_seconds: null, active_seconds: null,
      counts: { requests: 1, inputs: 1, turns: 1, abandoned_turns: 0, api_requests: 1,
        turn_boundaries: { state: 'observed', reason: null } },
      tokens: { input: 1, output: 2, cache_read: 0, cache_create: 0, basis: 'request_set' },
      cost: { usd: null, source: 'otel', turn_attached_usd: null, unattached_turn_usd: null, note: null },
      context: { tokens: null, window: null, percent: null, source: 'unknown', large_window_seen: false, mix: null },
      tools: { calls: 0, subagents_running: null },
      tags: { change_area: { value: null, state: 'observed' }, task_type: null, rules_files: [] },
      plan_flags: { has_plan_file: false, used_plan_mode: false },
    },
    outputs: { edited_files: { state: 'observed', active_total: 0, abandoned_total: 0, truncated: false, items: [] },
      lines: null, prs: { items: [], truncated: false } },
    recaps: { state: 'none', items: [] },
    turns: [{ key: 'turn-one', no: 1, request_no: 1, branch: 'active',
      anchor_ts: '2026-10-08T12:00:00Z', started_at: '2026-10-08T12:00:00Z', ended_at: '2026-10-08T12:01:00Z',
      kind: 'typed', is_request: true, extra_inputs: 0, status: 'ended', status_reason: null,
      preview: { text: 'Work', state: 'ok' }, images: 0, mode: '', denied: false, login_email: '', api_requests: 1,
      tokens: { input: 1, output: 2, cache_read: 0, cache_create: 0 },
      cost: { usd: null, source: 'otel', reason: null },
      context: { tokens: null, window: null, percent: null, source: 'unknown' },
      tools: { calls: 0, subagents: 0 }, question_headers: [{ tool_use_id: 'q', headers: [], status: 'answered' }],
      edited: { count: 0, sample: [], abandoned: 0 }, prs: [], last_assistant_message_id: '', opener_copied: false,
    }],
    abandoned: [], inputs: [{ uuid: 'input-one', ts: '2026-10-08T12:00:00Z', n: 1, kind: 'typed', turn_key: 'turn-one', images: 0 }],
    questions: [], plans: [], subagents: { unattributed_requests: 0, unattributed_tokens: 0 },
    truncated: { turns: false, abandoned: false, inputs: false, questions: false, plans: false, recaps: false, bodies: false },
  });

  it('accepts a reader-shaped ready analysis with an unknown project name and sends the access scope', async () => {
    const body = readerAnalysis('id/with space');
    const { calls } = createFetchStub([{ body }]);
    await expect(fetchSessionAnalysis('id/with space', { loginEmail: 'account@example.com' })).resolves.toEqual(body);
    const url = new URL(calls[0] ?? '');
    expect(url.pathname).toBe('/api/sessions/id%2Fwith%20space/analysis');
    expect(url.searchParams.get('login_email')).toBe('account@example.com');
  });

  it('calls the owner-scoped session search and keeps turn keys in each match', async () => {
    const body = [{ session_id: 's-1', title: '검색 화면', last_activity_at: '2026-10-08T12:00:00Z',
      matches: [{ field: 'task', text: 'task-9 · 검색 결과 이동', turn_key: 'turn-9', item_key: 'repo-key:task-9' }] }];
    const { calls } = createFetchStub([{ body }]);
    const controller = new AbortController();
    await expect(fetchSessionRecordSearch('검색', 50, controller.signal)).resolves.toEqual(body);
    const url = new URL(calls[0] ?? '');
    expect(url.pathname).toBe('/api/session-search');
    expect(url.searchParams.get('q')).toBe('검색');
    expect(url.searchParams.get('limit')).toBe('50');
    const init = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]?.[1] as RequestInit;
    expect(init.signal).toBe(controller.signal);
  });

  it('does not accept a done bucket unless all five judgement checks pass', async () => {
    const body = readerAnalysis('x');
    body.judgement = {
      status: 'done', bucket: 'done',
      checks: [
        { key: 'turn_ended', state: 'pass' },
        { key: 'no_pending_reply', state: 'pass' },
        { key: 'no_background', state: 'pass' },
        { key: 'committed', state: 'pass' },
        { key: 'work_done', state: 'undeterminable', reason: 'unvalidated_model' },
      ],
    };
    createFetchStub([{ body }]);
    await expect(fetchSessionAnalysis('x')).rejects.toThrow('Invalid session analysis response');
  });

  it('keeps record reasons, review turn evidence, and P4 task item keys', async () => {
    const body = readerAnalysis('x');
    body.record = {
      state: 'ready', generated_at: '2026-10-08T12:02:00Z', covers_until: '2026-10-08T12:00:00Z', reason: null,
      headline: '검색 결과 연결', bullets: [{ text: '작업', commits: ['abc1234'] }],
      review: [{ kind: 'decision', text: '세션 범위를 유지한다', turn_key: 'turn-one' }],
      tags: { kinds: ['implementation'], components: ['web'], screens: ['sessions'], related: [{ kind: 'task', id: '#9', item_key: 'repo-a:#9' }] },
      work: [{ id: 'w1', kind: 'request', done: null, text: '검색을 연결한다', turn_key: 'turn-one', commits: [], tasks: ['#9'], task_keys: ['repo-a:#9'] }],
    };
    createFetchStub([{ body }]);
    await expect(fetchSessionAnalysis('x')).resolves.toMatchObject({
      record: { review: [{ turn_key: 'turn-one' }], tags: { related: [{ item_key: 'repo-a:#9' }] }, work: [{ task_keys: ['repo-a:#9'] }] },
    });
  });

  it('rejects malformed nested turn arrays and project names at the fetch boundary', async () => {
    const body = readerAnalysis('x');
    createFetchStub([
      { body: { ...body, header: { ...body.header, project: { hash: '', name: 42 } } } },
      { body: { ...body, turns: [{ ...body.turns[0], edited: { ...body.turns[0].edited, sample: null } }] } },
      { body: { ...body, turns: [{ ...body.turns[0], question_headers: [{ tool_use_id: 'q', headers: null, status: 'answered' }] }] } },
    ]);
    for (let i = 0; i < 3; i++) await expect(fetchSessionAnalysis('x')).rejects.toThrow('Invalid session analysis response');
  });

  it('keeps missing analysis separate from malformed and unavailable responses', async () => {
    createFetchStub([{ status: 404, body: { error: 'not found' } }, { status: 503 }, { body: { session_id: 'x', state: 'ready' } }]);
    await expect(fetchSessionAnalysis('x')).rejects.toMatchObject({ status: 404 });
    await expect(fetchSessionAnalysis('x')).rejects.toBeInstanceOf(SessionAnalysisRequestError);
    await expect(fetchSessionAnalysis('x')).rejects.toThrow('Invalid session analysis response');
  });

  it('rejects a full response missing a required nested shape or mismatched id', async () => {
    const minimal = { session_id: 'x', short_id: 'x', owner_view: true, state: 'building', header: {} };
    createFetchStub([{ body: { ...minimal, session_id: 'other' } }, { body: { ...minimal, state: 'ready' } }, { body: minimal }]);
    await expect(fetchSessionAnalysis('x')).rejects.toThrow('Invalid session analysis response');
    await expect(fetchSessionAnalysis('x')).rejects.toThrow('Invalid session analysis response');
    await expect(fetchSessionAnalysis('x')).resolves.toEqual(minimal);
  });

  it('sends lineage when loading assembled session records', async () => {
    const { calls } = createFetchStub([{ body: [] }]);

    await fetchSessionRecordsPage({ sessionId: 'session-1', offset: 1000, lineage: true });

    const url = new URL(calls[0] ?? '');
    expect(url.pathname).toBe('/api/sessions');
    expect(url.searchParams.get('session_id')).toBe('session-1');
    expect(url.searchParams.get('offset')).toBe('1000');
    expect(url.searchParams.get('lineage')).toBe('1');
    // The assembled view renders from view alone, so it skips raw.
    expect(url.searchParams.get('raw')).toBe('0');
    expect(url.searchParams.get('view')).toBe('conversation');
  });

  it('keeps raw for the individual raw-record view', async () => {
    const { calls } = createFetchStub([{ body: [] }]);

    await fetchSessionRecordsPage({ sessionId: 'session-1', offset: 0, lineage: false });

    const url = new URL(calls[0] ?? '');
    expect(url.searchParams.get('raw')).toBeNull();
    expect(url.searchParams.get('view')).toBeNull();
  });

  it('sends the selected model to the latest-activity probe', async () => {
    const { calls } = createFetchStub([{ body: { has_data: true, latest_ts: '2026-01-02T03:04:05Z' } }]);

    await fetchLatestActivity({
      profileEmail: 'profile@example.com',
      loginEmail: 'account@example.com',
      projectHash: 'project-a',
      userID: 'user-1',
      agent: 'codex',
      modelCategory: 'codex',
      model: 'gpt-5.5',
    });

    const url = new URL(calls[0] ?? '');
    expect(url.pathname).toBe('/api/stats/latest-activity');
    expect(url.searchParams.get('profile_email')).toBe('profile@example.com');
    expect(url.searchParams.get('login_email')).toBe('account@example.com');
    expect(url.searchParams.get('project_hash')).toBe('project-a');
    expect(url.searchParams.get('user_id')).toBe('user-1');
    expect(url.searchParams.get('agent')).toBe('codex');
    expect(url.searchParams.get('model_category')).toBe('codex');
    expect(url.searchParams.get('model')).toBe('gpt-5.5');
  });
});

describe('fetchUserNameMap', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it('returns the name map on success', async () => {
    createFetchStub([{ status: 200, body: { 'user-1': 'Alice' } }]);
    await expect(fetchUserNameMap()).resolves.toStrictEqual({ 'user-1': 'Alice' });
  });

  it('throws on a non-ok response so React Query keeps the last map instead of blanking to email/id', async () => {
    createFetchStub([{ status: 500 }]);
    await expect(fetchUserNameMap()).rejects.toThrow();
  });
});

describe('P5 세션 기록 제어 API', () => {
  const from = '2026-10-05T00:00:00Z';
  const to = '2026-10-12T00:00:00Z';

  it('상태 응답의 blocked, waiting_budget, writing 수를 유지한다', async () => {
    const status = { state: 'stale', written: 4, total: 12, stale: 5, writing: 2,
      blocked: { consent_required: 3, disabled: 2 }, waiting_budget: true };
    const { calls } = createFetchStub([{ body: status }]);
    await expect(fetchSessionRecordStatus(from, to)).resolves.toEqual(status);
    const url = new URL(calls[0]);
    expect(url.pathname).toBe('/api/session-records/status');
    expect(url.searchParams.get('from')).toBe(from);
    expect(url.searchParams.get('to')).toBe(to);
  });

  it('범위 refresh, 동의·철회, 내 기록 삭제를 각 서버 경로에 보낸다', async () => {
    const { calls } = createFetchStub([
      { status: 202 }, { body: { runtime_key: 'openai-api:api_key', disclosure_version: 'v1', granted: false,
        granted_at: null, runtime: 'openai-api', provider: 'OpenAI API', provider_retention: '미확인', disclosure: 'notice',
        sends: [], not_sends: [], enabled: true, runtime_available: true } },
      { status: 200 }, { status: 200 }, { body: { deleted: 2 } },
    ]);
    await refreshSessionRecordRange(from, to);
    const info = await fetchSessionRecordConsent();
    await grantSessionRecordConsent(info);
    await withdrawSessionRecordConsent();
    await expect(deleteOwnSessionRecords()).resolves.toEqual({ deleted: 2 });
    expect(calls.map((call) => new URL(call).pathname)).toEqual([
      '/api/session-records/refresh', '/api/session-records/consent', '/api/session-records/consent',
      '/api/session-records/consent', '/api/session-records',
    ]);
    const grant = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[2]?.[1] as RequestInit;
    expect(JSON.parse(String(grant.body))).toEqual({ runtime_key: info.runtime_key, disclosure_version: info.disclosure_version });
    expect(new URL(calls[4]).searchParams.get('scope')).toBe('mine');
  });

  it('개별 세션 재생성은 세션 ID를 인코딩해 POST한다', async () => {
    const { calls } = createFetchStub([{ status: 202, body: { state: 'queued' } }]);
    await regenerateSessionRecord('session/id 1');
    const url = new URL(calls[0]);
    expect(url.pathname).toBe('/api/sessions/session%2Fid%201/record/regenerate');
    expect((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]?.[1]).toMatchObject({ method: 'POST' });
  });

  it('개별 세션 재생성은 예산 대기 사유와 서버 재시도 시각을 반환한다', async () => {
    const response = { state: 'waiting_budget', reason: 'waiting_budget', retry_at: '2026-10-10T00:00:00Z' };
    createFetchStub([{ status: 202, body: response }]);
    await expect(regenerateSessionRecord('session-1')).resolves.toEqual(response);
  });

  it('관리자 기록 설정과 어휘 저장·병합이 P5 경로에 연결된다', async () => {
    const { calls } = createFetchStub([
      { body: {} },
      { body: { repository_key: 'github.com/acme/app', components: ['web'], screens: ['sessions'], components_closed: true, screens_closed: true } },
      { body: { repository_key: 'github.com/acme/app', components: ['web'], screens: ['sessions'], components_closed: true, screens_closed: true } },
      { body: { repository_key: 'github.com/acme/app', components: ['frontend'], screens: ['sessions'], components_closed: true, screens_closed: true } },
    ]);
    await updateAdminSessionRecordSettings({ enabled: true, runtime: 'openai-api', daily_token_cap: 1000,
      backfill_enabled: false, backfill_days: null, backfill_token_budget: null, model: '', reasoning_effort: '' });
    await fetchAdminSessionRecordVocabulary('github.com/acme/app');
    await setAdminSessionRecordVocabulary('github.com/acme/app', ['web'], ['sessions']);
    await mergeAdminSessionRecordVocabulary('github.com/acme/app', 'component', 'web', 'frontend');
    expect(calls.map((call) => new URL(call).pathname)).toEqual([
      '/api/admin/ai/records', '/api/admin/ai/records/vocabulary', '/api/admin/ai/records/vocabulary',
      '/api/admin/ai/records/vocabulary/merge',
    ]);
  });
});

describe('P5 판정 범주 목록 API', () => {
  it('bucket filter를 보내고 서버의 네 범주 count를 그대로 받는다', async () => {
    const counts = { done: 1, leftover: 2, ended_midway: 3, unknown: 4 };
    const { calls } = createFetchStub([{ body: { count: 10, unattributed: 0, judgement_bucket_counts: counts } }]);
    await expect(fetchSessionOverviewCount({ analysis: true, bucket: 'ended_midway' })).resolves.toMatchObject({
      count: 10, judgement_bucket_counts: counts,
    });
    expect(new URL(calls[0]).searchParams.get('bucket')).toBe('ended_midway');
  });

  it('서버가 bucket counts를 주지 않으면 0으로 채우지 않는다', async () => {
    createFetchStub([{ body: { count: 5, unattributed: 0 } }]);
    const result = await fetchSessionOverviewCount({ analysis: true });
    expect(result.judgement_bucket_counts).toBeUndefined();
  });
});

describe('API token password change gate', () => {
  it.each([
    ['create', () => createOwnAPIToken('CI')],
    ['rotate', () => rotateOwnAPIToken(7)],
    ['reactivate', () => setOwnAPITokenActive(7, true)],
    ['extend', () => setOwnAPITokenExpiration(7, null)],
  ] as const)('shows the server password instruction for %s', async (_name, action) => {
    createFetchStub([{ status: 403, body: {
      code: 'password_change_required',
      error: 'Please change your password on the dashboard before managing API tokens.',
    } }]);
    await expect(action()).rejects.toThrow('Please change your password on the dashboard');
  });
});

describe('personal quota usage APIs', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it('uses the caller-scoped usage and comparison routes', async () => {
    const { calls } = createFetchStub([
      { body: { from: '2026-10-02T00:00:00Z', to: '2026-10-09T00:00:00Z', accounts: [] } },
      { body: { billing_provider: 'anthropic', account_id: 'acct-local', window_minutes: 300, current: null,
        history: [], quantiles: [], history_count: 0, truncated: false, source: 'client_reported' } },
    ]);
    await fetchMyQuotaUsage();
    await fetchMyQuotaWindowComparison('anthropic', 'acct-local', 300);
    expect(calls.map((call) => new URL(call).pathname)).toEqual([
      '/api/me/quota-usage', '/api/me/quota-usage/window-comparison',
    ]);
    const query = new URL(calls[1]).searchParams;
    expect(query.get('billing_provider')).toBe('anthropic');
    expect(query.get('account_id')).toBe('acct-local');
    expect(query.get('window_minutes')).toBe('300');
  });

  it('requests user binding and records admin console verification explicitly', async () => {
    const { calls } = createFetchStub([
      { body: { id: 1, billing_provider: 'openai', account_id: 'acct-local', status: 'pending', requested_at: '2026-10-09T00:00:00Z' } },
      { body: { id: 1, dashboard_user_id: 4, owner_email: 'owner@example.test', billing_provider: 'openai', account_id: 'acct-local', status: 'approved', requested_at: '2026-10-09T00:00:00Z' } },
    ]);
    await requestQuotaAccountBinding({ billing_provider: 'openai', account_id: 'acct-local' });
    await approveQuotaAccountBinding(1, 'VERIFY-1');
    expect(new URL(calls[0]).pathname).toBe('/api/me/quota-bindings');
    const requestInit = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]?.[1] as RequestInit;
    expect(requestInit.method).toBe('POST');
    expect(JSON.parse(String(requestInit.body))).toEqual({ billing_provider: 'openai', account_id: 'acct-local' });
    const approveInit = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[1]?.[1] as RequestInit;
    expect(JSON.parse(String(approveInit.body))).toEqual({
      verification_method: 'provider_console', verification_reference: 'VERIFY-1', verified_externally: true,
    });
  });
});

describe('user data deletion request', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  // The route answers DELETE only (routes.go). A request without a method is a GET,
  // which is not the route: the dialog reports a failure and the data stays.
  it('sends DELETE for the account it names', async () => {
    const { calls } = createFetchStub([{ body: { status: 'deleted' } }]);
    await deleteUserData('alice@example.com', 'u-1');
    const init = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]?.[1] as RequestInit;
    expect(init.method).toBe('DELETE');
    const url = new URL(calls[0]);
    expect(url.pathname).toBe('/api/users/data');
    expect(url.searchParams.get('profile_email')).toBe('alice@example.com');
    expect(url.searchParams.get('user_id')).toBe('u-1');
  });
});
