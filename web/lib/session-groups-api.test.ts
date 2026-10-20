import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fetchCompleteSessionGroup, fetchSessionGroups, SessionGroupError, updateSessionGroupDismissals } from './session-groups-api';

const fetchMock = vi.fn<typeof fetch>();
const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const scope = { source: 'interactive', projectHashes: ['-one', '-two'], agent: 'claude', assembled: true, classification: 'working' as const };
const item = { session_id: 'one/two', ended_at: '2026-10-08T00:00:00Z' };

beforeEach(() => { fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock); });
afterEach(() => vi.unstubAllGlobals());

describe('session groups API', () => {
  it('uses the authenticated request with the exact overview filter scope', async () => {
    const body = { scope: 'filtered_total', grouping_version: 1, running: [], ended: [], truncated: false, overview_total: 0,
      counts: { running: 0, ended: 0, ended_groups: 0, running_by: {} } };
    fetchMock.mockResolvedValueOnce(reply(body));
    await expect(fetchSessionGroups(scope)).resolves.toEqual(body);
    const url = new URL(String(fetchMock.mock.calls[0][0]));
    expect(url.pathname).toBe('/api/me/session-groups');
    expect(Object.fromEntries(url.searchParams)).toEqual({ source: 'interactive', project_hashes: '-one,-two', agent: 'claude', assembled: '1', classification: 'working' });
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ credentials: 'include', headers: { 'X-Requested-With': 'XMLHttpRequest' } });
  });

  it('sends the same analysis and period filters as the session overview', async () => {
    fetchMock.mockResolvedValueOnce(reply({ scope: 'filtered_total', grouping_version: 1, running: [], ended: [], truncated: false,
      overview_total: 0, counts: { running: 0, ended: 0, ended_groups: 0, running_by: {} } }));
    await fetchSessionGroups({ ...scope, since: '2026-10-01T00:00:00Z', until: '2026-10-08T00:00:00Z',
      includeAnalysis: true, query: 'calendar', plan: 'file', category: 'running', sort: 'cost', bucket: 'leftover' });
    const params = new URL(String(fetchMock.mock.calls[0][0])).searchParams;
    expect(Object.fromEntries(params)).toEqual({ source: 'interactive', project_hashes: '-one,-two', agent: 'claude', assembled: '1',
      classification: 'working', since: '2026-10-01T00:00:00Z', until: '2026-10-08T00:00:00Z', include: 'analysis',
      q: 'calendar', plan: 'file', category: 'running', sort: 'cost', bucket: 'leftover' });
  });

  it('uses exact member epochs and server grouping version for PUT and DELETE, waiting for acknowledgement', async () => {
    fetchMock.mockImplementation(async () => reply({ acknowledged: true, count: 1, grouping_version: 1 }));
    await updateSessionGroupDismissals(scope, 1, [item], true);
    await updateSessionGroupDismissals(scope, 1, [item], false);
    expect(fetchMock.mock.calls.map((call) => new URL(String(call[0])).pathname)).toEqual(['/api/me/session-groups/dismiss', '/api/me/session-groups/dismiss']);
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: 'PUT', body: JSON.stringify({ grouping_version: 1, items: [item] }) });
    expect(fetchMock.mock.calls[1][1]).toMatchObject({ method: 'DELETE', body: JSON.stringify({ grouping_version: 1, items: [item] }) });
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ credentials: 'include', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' } });
  });

  it('rejects truncated batches and server failures instead of reporting success', async () => {
    await expect(updateSessionGroupDismissals(scope, 1, [], true)).rejects.toThrow();
    await expect(updateSessionGroupDismissals(scope, 1, Array(101).fill(item), true)).rejects.toThrow();
    expect(fetchMock).not.toHaveBeenCalled();
    fetchMock.mockResolvedValueOnce(reply({ error: 'conflict' }, 409)).mockResolvedValueOnce(reply({ acknowledged: false, count: 0, grouping_version: 1 }));
    await expect(updateSessionGroupDismissals(scope, 1, [item], true)).rejects.toBeInstanceOf(SessionGroupError);
    await expect(updateSessionGroupDismissals(scope, 1, [item], true)).rejects.toBeInstanceOf(SessionGroupError);
  });

  it('reads every group member page under the same filter and rejects an incomplete result', async () => {
    const member = { ...item, dismissed: false };
    fetchMock.mockResolvedValueOnce(reply({ group_key: 'g', grouping_version: 1, total: 2, items: [member], next_cursor: 'page-two' }))
      .mockResolvedValueOnce(reply({ group_key: 'g', grouping_version: 1, total: 2, items: [{ session_id: 'two', ended_at: item.ended_at, dismissed: true }], next_cursor: '' }));
    await expect(fetchCompleteSessionGroup(scope, 'g', 1, 2)).resolves.toHaveLength(2);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    const urls = fetchMock.mock.calls.map((call) => new URL(String(call[0])));
    expect(urls.map((url) => url.pathname)).toEqual(['/api/me/session-groups/members', '/api/me/session-groups/members']);
    expect(urls[1].searchParams.get('cursor')).toBe('page-two');
    expect(urls[1].searchParams.get('project_hashes')).toBe('-one,-two');
    fetchMock.mockResolvedValueOnce(reply({ group_key: 'g', grouping_version: 1, total: 2, items: [member], next_cursor: '' }));
    await expect(fetchCompleteSessionGroup(scope, 'g', 1, 2)).rejects.toBeInstanceOf(SessionGroupError);
  });

  it('accepts a complete first page with an empty terminal cursor', async () => {
    fetchMock.mockResolvedValueOnce(reply({ group_key: 'g', grouping_version: 1, total: 2,
      items: [{ ...item, dismissed: false }, { session_id: 'two', ended_at: item.ended_at, dismissed: false }], next_cursor: '' }));
    await expect(fetchCompleteSessionGroup(scope, 'g', 1, 2)).resolves.toHaveLength(2);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('reads all 501 members through six cursor pages before a group-wide write', async () => {
    const members = Array.from({ length: 501 }, (_, index) => ({ session_id: `member-${index}`, ended_at: item.ended_at, dismissed: index < 100 }));
    fetchMock.mockImplementation(async (input) => {
      const url = new URL(String(input));
      const offset = Number(url.searchParams.get('cursor') || '0');
      const page = members.slice(offset, offset + 100);
      return reply({ group_key: 'g', grouping_version: 1, total: 501, items: page,
        next_cursor: offset + 100 < members.length ? String(offset + 100) : '' });
    });
    await expect(fetchCompleteSessionGroup(scope, 'g', 1, 501)).resolves.toEqual(members);
    expect(fetchMock).toHaveBeenCalledTimes(6);
    expect(fetchMock.mock.calls.map((call) => new URL(String(call[0])).searchParams.get('cursor'))).toEqual([null, '100', '200', '300', '400', '500']);
  });

  it('rejects malformed overview, member pages and acknowledgements without leaking TypeError', async () => {
    const overview = { scope: 'filtered_total', grouping_version: 1, truncated: false, overview_total: 2,
      running: [], ended: [{ key: 'g', ended_at: item.ended_at, total: 2, items: [{ ...item, dismissed: false }] }],
      counts: { running: 0, ended: 2, ended_groups: 1, running_by: {} } };
    for (const body of [null, { ...overview, counts: null }, { ...overview, counts: { ...overview.counts, ended: -1 } },
      { ...overview, ended: [{ ...overview.ended[0], items: [{ session_id: '', ended_at: item.ended_at, dismissed: false }] }] },
      { ...overview, running: null }, { ...overview, overview_total: Infinity }]) {
      fetchMock.mockResolvedValueOnce(reply(body));
      await expect(fetchSessionGroups(scope)).rejects.toBeInstanceOf(SessionGroupError);
    }
    fetchMock.mockResolvedValueOnce(reply({ group_key: 'g', grouping_version: 1, total: 1,
      items: [{ session_id: 'one', ended_at: 'not-a-date', dismissed: false }], next_cursor: '' }));
    await expect(fetchCompleteSessionGroup(scope, 'g', 1, 1)).rejects.toBeInstanceOf(SessionGroupError);
    fetchMock.mockResolvedValueOnce(reply(null));
    await expect(updateSessionGroupDismissals(scope, 1, [item], true)).rejects.toBeInstanceOf(SessionGroupError);
  });

  it('does not send an empty project identity as an unfiltered scope', async () => {
    await expect(fetchSessionGroups({ projectHashes: [] })).rejects.toThrow('project identity member set is empty');
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
