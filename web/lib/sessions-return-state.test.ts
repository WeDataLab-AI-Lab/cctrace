import { afterEach, expect, it } from 'vitest';
import { clearSessionsReturnState, readSessionsReturnState, saveSessionsReturnState } from './sessions-return-state';
afterEach(clearSessionsReturnState);
it('preserves tab state only for its authenticated owner and discards it on logout/change', () => {
  saveSessionsReturnState(7, { selectedId: 'session', committedIds: ['b', 'a'], search: 'query', returnUrl: '/sessions/?since=2026-10-01', scroll: { list: 321 } });
  expect(readSessionsReturnState(7)?.committedIds).toEqual(['b', 'a']);
  expect(readSessionsReturnState(8)).toBeNull();
  expect(readSessionsReturnState(7)).toBeNull();
  saveSessionsReturnState(7, { selectedId: 'session' });
  clearSessionsReturnState();
  expect(readSessionsReturnState(7)).toBeNull();
});
it('discards frozen order and positions when account or agent scope changes', () => {
  saveSessionsReturnState(7, { scopeKey: 'account-a|claude', committedIds: ['private-a'], scroll: { list: 321 } });
  expect(readSessionsReturnState(7, 'account-a|codex')).toBeNull();
  expect(readSessionsReturnState(7, 'account-a|claude')).toBeNull();
});
