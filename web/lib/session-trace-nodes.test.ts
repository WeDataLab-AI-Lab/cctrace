import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fetchTraceNodes, fetchTraceNodeDetail } from './session-trace-nodes';
import { traceNodesPageKey } from './use-trace-nodes';

const apiFetch = vi.hoisted(() => vi.fn());
vi.mock('./api', () => ({ apiFetch, buildUrl: (path: string, params: Record<string, string>) => `${path}?${new URLSearchParams(params)}` }));

describe('source-backed trace queries', () => {
  beforeEach(() => { apiFetch.mockReset(); });

  it('reads only the requested parent page with the selected scope', async () => {
    apiFetch.mockResolvedValueOnce({ ok: true, json: async () => ({ nodes: [], next_cursor: null }) });
    await fetchTraceNodes('s/one', { view: 'context', parentId: 'parent:one', cursor: 'next' }, { loginEmail: 'a@example.test' }, new AbortController().signal);
    const url = new URL(apiFetch.mock.calls[0][0], 'https://example.test');
    expect(url.pathname).toBe('/api/sessions/s%2Fone/trace/nodes');
    expect(url.searchParams.get('parent_id')).toBe('parent:one');
    expect(url.searchParams.get('cursor')).toBe('next');
    expect(url.searchParams.get('view')).toBe('context');
    expect(url.searchParams.get('login_email')).toBe('a@example.test');
    expect(apiFetch).toHaveBeenCalledTimes(1);
    apiFetch.mockClear();
  });
  it('uses the execution view when no view is selected', async () => {
    apiFetch.mockResolvedValueOnce({ ok: true, json: async () => ({ nodes: [], next_cursor: null }) });
    await fetchTraceNodes('s', {}, undefined, new AbortController().signal);
    const url = new URL(apiFetch.mock.calls[0][0], 'https://example.test');
    expect(url.searchParams.get('view')).toBe('execution');
  });
  it('isolates each view and parent page in the cache key', () => {
    const identity = { userId: 7, access: 'owner' as const, sessionId: 's' };
    expect(traceNodesPageKey(identity, 'execution')).not.toEqual(traceNodesPageKey(identity, 'context'));
    expect(traceNodesPageKey(identity, 'context')).not.toEqual(traceNodesPageKey(identity, 'metadata'));
    expect(traceNodesPageKey(identity, 'execution', 'turn-1')).not.toEqual(traceNodesPageKey(identity, 'execution', 'turn-2'));
  });
  it('fetches a selected node and preserves HTTP freshness failures', async () => {
    apiFetch.mockResolvedValueOnce({ ok: false, status: 409 });
    await expect(fetchTraceNodeDetail('s', 'node/one', undefined, new AbortController().signal)).rejects.toMatchObject({ status: 409 });
    expect(apiFetch.mock.calls[0][0]).toContain('/nodes/node%2Fone?');
    apiFetch.mockClear();
  });
});

it('keeps timing scope, source revision and server search in the request and cache key', async () => {
  apiFetch.mockReset();
  apiFetch.mockResolvedValueOnce({ ok: true, json: async () => ({ nodes: [], next_cursor: null }) });
  const request = { view: 'timing' as const, groupId: 'turn-1', from: '2026-10-19T01:00:00Z', to: '2026-10-19T01:01:00Z', query: 'Bash', revision: 12, epoch: 3 };
  await fetchTraceNodes('s', request, undefined, new AbortController().signal);
  const url = new URL(apiFetch.mock.calls[0][0], 'https://example.test');
  expect(url.searchParams.get('group_id')).toBe('turn-1');
  expect(url.searchParams.get('q')).toBe('Bash');
  expect(url.searchParams.get('from')).toBe(request.from);
  const identity = { userId: 7, access: 'owner' as const, sessionId: 's' };
  expect(traceNodesPageKey(identity, 'timing', undefined, request)).not.toEqual(traceNodesPageKey(identity, 'timing', undefined, { ...request, groupId: 'turn-2' }));
  expect(traceNodesPageKey(identity, 'flow', undefined, { query: 'one' })).not.toEqual(traceNodesPageKey(identity, 'flow', undefined, { query: 'two' }));
});
it('requests a bounded selected input page with explicit comparison and source location', async () => {
  apiFetch.mockReset();
  apiFetch.mockResolvedValueOnce({ ok: true, json: async () => ({}) });
  await fetchTraceNodeDetail('s', 'request-1', undefined, new AbortController().signal, { requestParts: 'page', inputCursor: 'next-block', inputLimit: 20, compareTo: 'request-0', sourceRef: 'exact-ref' });
  const url = new URL(apiFetch.mock.calls[0][0], 'https://example.test');
  expect(url.searchParams.get('request_parts')).toBe('page');
  expect(url.searchParams.get('input_cursor')).toBe('next-block');
  expect(url.searchParams.get('input_limit')).toBe('20');
  expect(url.searchParams.get('compare_to')).toBe('request-0');
  expect(url.searchParams.get('source_ref')).toBe('exact-ref');
});

it('normalizes nullable empty native reference lists from the API before rendering input', async () => {
  apiFetch.mockReset().mockResolvedValueOnce({ ok: true, json: async () => ({ request: { state: 'observed', missing_refs: null, source_refs: null } }) });
  const detail = await fetchTraceNodeDetail('s', 'r', undefined, new AbortController().signal, { requestParts: 'page' });
  expect(detail.request?.missing_refs).toEqual([]);
  expect(detail.request?.source_refs).toEqual([]);
});
