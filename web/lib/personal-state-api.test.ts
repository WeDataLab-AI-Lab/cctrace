import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  deletePersonalGlobalNote,
  deletePersonalSession,
  fetchPersonalGlobalNote,
  fetchPersonalNotePage,
  fetchPersonalNoteOverview,
  fetchPersonalSession,
  PersonalStateError,
  savePersonalGlobalNote,
  savePersonalSession,
} from './personal-state-api';

const serverSession = { session_id: 'a/b', classification: 'working', note: { text: 'server', updated_at: '2026-10-08T00:00:00Z' }, revision: 4, expires_at: '2026-11-01T00:00:00Z' };
const globalNote = { text: 'global', revision: 3, updated_at: '2026-10-08T00:00:00Z', expires_at: '2026-11-01T00:00:00Z' };
const fetchMock = vi.fn<typeof fetch>();
const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

beforeEach(() => { fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock); });
afterEach(() => vi.unstubAllGlobals());

describe('personal state API', () => {
  it('uses authenticated dashboard requests and encoded session paths for GET/PUT/DELETE', async () => {
    fetchMock.mockImplementation(async () => reply(serverSession));
    await fetchPersonalSession('a/b');
    await savePersonalSession('a/b', { note: 'draft', classification: 'done', expected_revision: 4 });
    await deletePersonalSession('a/b', 5);
    expect(fetchMock).toHaveBeenCalledTimes(3);
    for (const [url, options] of fetchMock.mock.calls) {
      expect(String(url)).toMatch(/\/api\/sessions\/a%2Fb\/personal$/);
      expect(options).toMatchObject({ credentials: 'include', headers: { 'X-Requested-With': 'XMLHttpRequest' } });
    }
    expect(fetchMock.mock.calls[1][1]).toMatchObject({ method: 'PUT', body: JSON.stringify({ note: 'draft', classification: 'done', expected_revision: 4 }) });
    expect(fetchMock.mock.calls[2][1]).toMatchObject({ method: 'DELETE', body: JSON.stringify({ expected_revision: 5 }) });
  });

  it('reads an owned note session overview through the authenticated single-session endpoint', async () => {
    const overview = { session_id: 'a/b', profile_email: 'owner@example.com', start_time: '2026-10-08T00:00:00Z', end_time: '2026-10-08T01:00:00Z' };
    fetchMock.mockResolvedValueOnce(reply(overview)).mockResolvedValueOnce(reply({ error: 'not found' }, 404));
    await expect(fetchPersonalNoteOverview('a/b')).resolves.toEqual(overview);
    expect(fetchMock.mock.calls[0][0]).toMatch(/\/api\/sessions\/a%2Fb\/personal\/overview$/);
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ credentials: 'include', headers: { 'X-Requested-With': 'XMLHttpRequest' } });
    await expect(fetchPersonalNoteOverview('a/b')).rejects.toMatchObject({ status: 404 });
  });

  it('uses revision CAS for global note GET/PUT/DELETE', async () => {
    fetchMock.mockImplementation(async () => reply(globalNote));
    await fetchPersonalGlobalNote();
    await savePersonalGlobalNote('draft', 3);
    await deletePersonalGlobalNote(4);
    expect(fetchMock.mock.calls.map(([url]) => String(url))).toEqual(Array(3).fill(expect.stringMatching(/\/api\/me\/note$/)));
    expect(fetchMock.mock.calls[1][1]).toMatchObject({ credentials: 'include', method: 'PUT', body: JSON.stringify({ text: 'draft', expected_revision: 3 }) });
    expect(fetchMock.mock.calls[2][1]).toMatchObject({ method: 'DELETE', body: JSON.stringify({ expected_revision: 4 }) });
  });

  it('fetches paged personal notes using the same authenticated request contract', async () => {
    fetchMock.mockImplementation(async () => reply({ items: [], truncated: false }));
    await fetchPersonalNotePage();
    await fetchPersonalNotePage('next/cursor');
    expect(fetchMock.mock.calls[0][0]).toMatch(/\/api\/me\/session-notes\?limit=50$/);
    expect(fetchMock.mock.calls[1][0]).toMatch(/\/api\/me\/session-notes\?limit=50&cursor=next%2Fcursor$/);
    expect(fetchMock.mock.calls[1][1]).toMatchObject({ credentials: 'include', headers: { 'X-Requested-With': 'XMLHttpRequest' } });
  });

  it('preserves server current on conflict and distinguishes ownership 404', async () => {
    fetchMock.mockResolvedValueOnce(reply({ error: 'revision conflict', current: serverSession }, 409))
      .mockResolvedValueOnce(reply({ error: 'not found' }, 404));
    await expect(savePersonalSession('a/b', { note: 'draft', expected_revision: 3 })).rejects.toMatchObject({ status: 409, current: serverSession });
    await expect(fetchPersonalSession('a/b')).rejects.toBeInstanceOf(PersonalStateError);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});
