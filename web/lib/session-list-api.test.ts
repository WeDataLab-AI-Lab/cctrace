import { afterEach, expect, it, vi } from 'vitest';
import { fetchSessionOverview, fetchSessionOverviewCount, fetchSessionRepoStatus } from './api';

afterEach(() => vi.unstubAllGlobals());

it('passes the request signal through overview and count HTTP reads', async () => {
  const fetcher = vi.fn().mockResolvedValueOnce(Response.json([])).mockResolvedValueOnce(Response.json({ count: 0 }));
  vi.stubGlobal('fetch', fetcher);
  const { signal } = new AbortController();
  await fetchSessionOverview({}, signal);
  await fetchSessionOverviewCount({}, signal);
  expect(fetcher).toHaveBeenNthCalledWith(1, expect.stringContaining('/api/session-overview?'), expect.objectContaining({ signal }));
  expect(fetcher).toHaveBeenNthCalledWith(2, expect.stringContaining('/api/session-overview/count'), expect.objectContaining({ signal }));
});

it.each(['ready', 'stale', 'building', 'none', 'truncated', 'unsupported', undefined])('accepts optional repo-status analysis state %s', async analysis_state => {
  const body = { session_id: 'S', owner_view: true, ...(analysis_state ? { analysis_state } : {}) };
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json(body)));
  await expect(fetchSessionRepoStatus('S')).resolves.toEqual(body);
});

it.each(['withheld', 'unknown', null, 1, ['building']])('rejects malformed repo-status analysis state %s', async analysis_state => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json({ session_id: 'S', owner_view: true, analysis_state })));
  await expect(fetchSessionRepoStatus('S')).rejects.toThrow('Invalid session repository status response');
});
