import { afterEach, expect, it, vi } from 'vitest';
import { fetchSessionRecordsPage } from './api';
import { sessionRecordsQueryKey } from './session-record-scope';

afterEach(() => vi.unstubAllGlobals());
it('keeps basic record keys/requests unchanged while isolating workspace identity and scope', async () => {
  const scope = { profileEmail: 'profile@example.test', loginEmail: 'account@example.test', userID: 'other-user' };
  expect(sessionRecordsQueryKey('A', true)).toEqual(['session-records', 'A', true]);
  const key = sessionRecordsQueryKey('A', true, scope, { userId: 7, access: 'admin' });
  expect(key).not.toEqual(sessionRecordsQueryKey('A', true, scope, { userId: 8, access: 'admin' }));
  expect(key).not.toEqual(sessionRecordsQueryKey('A', true, { ...scope, loginEmail: 'another@example.test' }, { userId: 7, access: 'admin' }));
  const fetch = vi.fn().mockImplementation(async () => new Response('[]'));
  vi.stubGlobal('fetch', fetch);
  const signal = new AbortController().signal;
  await fetchSessionRecordsPage({ sessionId: 'A', offset: 0, lineage: true, scope, signal });
  const url = new URL(fetch.mock.calls[0][0]);
  expect(url.searchParams.get('profile_email')).toBe(scope.profileEmail);
  expect(url.searchParams.get('login_email')).toBe(scope.loginEmail);
  expect(url.searchParams.get('user_id')).toBe(scope.userID);
  expect(fetch.mock.calls[0][1].signal).toBe(signal);
  await fetchSessionRecordsPage({ sessionId: 'A', offset: 0, lineage: true, signal });
  expect(new URL(fetch.mock.calls[1][0]).searchParams.has('login_email')).toBe(false);
});
