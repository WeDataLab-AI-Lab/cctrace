import { afterEach, describe, expect, it, vi } from 'vitest';

import { fetchLatestActivity, fetchTimeSeriesStatsByModel, fetchTimeSeriesStatsByUser } from './api';

const invoke = (fn: unknown, args: unknown[]): Promise<unknown> =>
  (fn as (...callArgs: unknown[]) => Promise<unknown>)(...args);

const createFetchStub = (): { readonly calls: readonly string[] } => {
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
    calls.push(String(input));
    return Promise.resolve(new Response('[]', { status: 200 }));
  }));
  return { calls };
};

describe('stats API project identity filters', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it('sends the complete member set and omits the representative hash', async () => {
    const { calls } = createFetchStub();
    const members = ['h-main', 'h-worktree'];

    await invoke(fetchTimeSeriesStatsByModel, [
      'day', '2026-08-01T00:00:00.000Z', '2026-08-02T00:00:00.000Z',
      'profile@example.com', 'account@example.com', 'UTC', 'h-main', 'user-1', 'claude', 'all', members,
    ]);
    await invoke(fetchTimeSeriesStatsByUser, [
      'day', '2026-08-01T00:00:00.000Z', '2026-08-02T00:00:00.000Z',
      'profile@example.com', 'account@example.com', 'UTC', 'h-main', 'user-1', 'all', '__all__', 'claude', members,
    ]);
    await invoke(fetchLatestActivity, [{
      profileEmail: 'profile@example.com',
      loginEmail: 'account@example.com',
      projectHash: 'h-main',
      projectHashes: members,
    }]);

    for (const raw of calls) {
      const url = new URL(raw);
      expect(url.searchParams.get('project_hashes')).toBe('h-main,h-worktree');
      expect(url.searchParams.get('project_hash')).toBeNull();
    }
  });

  it('does not fall back to a scalar identity when the member set is explicitly empty', async () => {
    const { calls } = createFetchStub();

    await expect(invoke(fetchTimeSeriesStatsByModel, [
      'day', undefined, undefined, undefined, undefined, 'UTC', 'h-main', undefined, undefined, undefined, [],
    ])).rejects.toThrow('project identity member set is empty');
    await expect(invoke(fetchTimeSeriesStatsByUser, [
      'day', undefined, undefined, undefined, undefined, 'UTC', 'h-main', undefined, undefined, '__all__', undefined, [],
    ])).rejects.toThrow('project identity member set is empty');
    await expect(invoke(fetchLatestActivity, [{ projectHash: 'h-main', projectHashes: [] }]))
      .rejects.toThrow('project identity member set is empty');

    expect(calls).toHaveLength(0);
  });
});
