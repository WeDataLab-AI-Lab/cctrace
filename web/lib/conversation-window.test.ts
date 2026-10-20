import { afterEach, expect, it, vi } from 'vitest';
import { fetchConversationWindow } from './conversation-window';

afterEach(() => vi.unstubAllGlobals());
it('sends the bounded adjacent-request origin with its exact caller scope', async () => {
  const fetch = vi.fn().mockResolvedValue(new Response('[]', { headers: { 'X-Session-Start-Offset': '3200' } }));
  vi.stubGlobal('fetch', fetch);
  const signal = new AbortController().signal;
  const result = await fetchConversationWindow({ sessionId: 'session', direction: 'next-request', anchor: 'current-message', sourceFile: '/exact/session.jsonl', scope: { userID: 'owner', profileEmail: 'profile', loginEmail: 'account' }, signal });
  const url = new URL(fetch.mock.calls[0][0]);
  expect(Object.fromEntries(url.searchParams)).toMatchObject({ session_id: 'session', direction: 'next-request', anchor: 'current-message', source_file: '/exact/session.jsonl', user_id: 'owner', profile_email: 'profile', login_email: 'account', view: 'conversation', lineage: '1', raw: '0' });
  expect(fetch.mock.calls[0][1].signal).toBe(signal);
  expect(result).toMatchObject({ offset: 3200, records: [] });
});
it('keeps a missing adjacent request distinguishable from a missing source record', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: 'no_adjacent_request' }), { status: 404 })));
  await expect(fetchConversationWindow({ sessionId: 'session', direction: 'previous-request', anchor: 'first' })).rejects.toMatchObject({ code: 'no_adjacent_request' });
});
