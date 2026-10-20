import { afterEach, expect, it, vi } from 'vitest';
import { fetchTurnContext, TurnContextRequestError } from './turn-context-api';
afterEach(() => vi.unstubAllGlobals());
it('encodes canonical turn IDs, scope, revision and cursor and limits each page to 20 by default', async () => {
  const fetch = vi.fn().mockResolvedValue(new Response('{}', { status: 200 }));
  vi.stubGlobal('fetch', fetch);
  const signal = new AbortController().signal;
  await fetchTurnContext('session/a', 'turn/a', { revision: 'policy/revision', cursor: 'next' }, { loginEmail: 'scope', profileEmail: 'profile', userID: '8' }, signal);
  const [url, init] = fetch.mock.calls[0];
  expect(new URL(url).pathname).toBe('/api/sessions/session%2Fa/turns/turn%2Fa/context');
  expect(Object.fromEntries(new URL(url).searchParams)).toMatchObject({ revision: 'policy/revision', cursor: 'next', limit: '20', login_email: 'scope', profile_email: 'profile', user_id: '8' });
  expect(init.signal).toBe(signal);
  expect(init.credentials).toBe('include');
});
it('retains a 409 fence as an explicit status without retrying', async () => {
  const fetch = vi.fn().mockResolvedValue(new Response('{}', { status: 409 }));
  vi.stubGlobal('fetch', fetch);
  await expect(fetchTurnContext('s', 't', {}, undefined, new AbortController().signal)).rejects.toEqual(new TurnContextRequestError(409));
  expect(fetch).toHaveBeenCalledTimes(1);
});

it('distinguishes an authorized missing turn from a denied session', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{"error":"turn_not_found"}', { status: 404 })));
  await expect(fetchTurnContext('s', 't', {}, undefined, new AbortController().signal)).rejects.toMatchObject({ status: 404, code: 'turn_not_found' });
});
