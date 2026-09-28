import { QueryClient, QueryObserver } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  createOwnAPIToken,
  deleteAdminAIProviderKey,
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
  fetchSessionRecordsPage,
  fetchUserNameMap,
  fetchUsers,
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
    expect(url.searchParams.get('since')).toBe('2026-08-09T01:00:00.000Z');
    expect(url.searchParams.get('until')).toBe('2026-08-09T02:00:00.000Z');
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
      .resolves.toEqual({ count: 7, unattributed: 12 });
  });

  it('sends assembled count filters so the total matches the folded list', async () => {
    const { calls } = createFetchStub([{ body: { count: 7 } }]);

    await expect(fetchSessionOverviewCount({
      loginEmail: 'account@example.com',
      assembled: true,
      source: 'interactive',
      projectHashes: ['project-a'],
      agent: 'codex',
      since: '2026-08-09T01:00:00.000Z',
      until: '2026-08-09T02:00:00.000Z',
    })).resolves.toEqual({ count: 7, unattributed: 0 });

    const url = new URL(calls[0] ?? '');
    expect(url.pathname).toBe('/api/session-overview/count');
    expect(url.searchParams.get('assembled')).toBe('1');
    expect(url.searchParams.get('login_email')).toBe('account@example.com');
    expect(url.searchParams.get('source')).toBe('interactive');
    expect(url.searchParams.get('project_hashes')).toBe('project-a');
    expect(url.searchParams.get('agent')).toBe('codex');
    expect(url.searchParams.get('since')).toBe('2026-08-09T01:00:00.000Z');
    expect(url.searchParams.get('until')).toBe('2026-08-09T02:00:00.000Z');
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
  });

  it('keeps raw for the individual raw-record view', async () => {
    const { calls } = createFetchStub([{ body: [] }]);

    await fetchSessionRecordsPage({ sessionId: 'session-1', offset: 0, lineage: false });

    const url = new URL(calls[0] ?? '');
    expect(url.searchParams.get('raw')).toBeNull();
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
