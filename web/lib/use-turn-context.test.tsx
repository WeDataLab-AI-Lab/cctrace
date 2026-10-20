// @vitest-environment jsdom
import { createElement } from 'react';
import { act, cleanup, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { fetchTurnContext, TurnContextRequestError } from './turn-context-api';
import { useTurnContext, turnContextKey } from './use-turn-context';
import type { TurnContextResponse } from './turn-context-types';

vi.mock('./turn-context-api', () => ({ fetchTurnContext: vi.fn(), TurnContextRequestError: class extends Error { constructor(readonly status: number, readonly code?: string) { super(String(status)); } } }));
const identity = { userId: 7, access: 'owner' as const, sessionId: 'A' };
const ready: TurnContextResponse = { session_id: 'A', turn_key: 'first', state: 'ready', revision: 'r1', records: [], next_cursor: null };
beforeEach(() => { vi.mocked(fetchTurnContext).mockReset().mockResolvedValue(ready); });
afterEach(() => { cleanup(); vi.useRealTimers(); });
const setup = (initial = { turn: 'first', revision: 'r1' as string | undefined, enabled: true, session: 'A', scope: '' }) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: React.ReactNode }) => createElement(QueryClientProvider, { client }, children);
  return { client, ...renderHook(({ turn, revision, enabled, session, scope }) => useTurnContext({ identity: { ...identity, sessionId: session, scope: scope ? { loginEmail: scope } : undefined }, turnKey: turn, revision, enabled }), { wrapper, initialProps: initial }) };
};
it('reads canonical originals without trace readiness and never auto-drains pages', async () => {
  vi.mocked(fetchTurnContext).mockResolvedValue({ ...ready, next_cursor: 'page2' });
  const { result } = setup();
  await waitFor(() => expect(result.current.state).toBe('ready'));
  expect(fetchTurnContext).toHaveBeenCalledTimes(1);
  expect(vi.mocked(fetchTurnContext).mock.calls[0]).toEqual(['A', 'first', { revision: 'r1', cursor: undefined, limit: 20 }, undefined, expect.any(AbortSignal)]);
  await act(async () => { await result.current.fetchNextPage({ cancelRefetch: false }); });
  expect(vi.mocked(fetchTurnContext).mock.calls[1][2]).toEqual({ revision: 'r1', cursor: 'page2', limit: 20 });
});
it('creates no read when disabled and cancels the pending read when hidden', async () => {
  vi.mocked(fetchTurnContext).mockImplementation(() => new Promise(() => undefined));
  const { rerender } = setup({ turn: 'first', revision: 'r1', enabled: false, session: 'A', scope: '' });
  expect(fetchTurnContext).not.toHaveBeenCalled();
  rerender({ turn: 'first', revision: 'r1', enabled: true, session: 'A', scope: '' });
  await waitFor(() => expect(fetchTurnContext).toHaveBeenCalledTimes(1));
  const signal = vi.mocked(fetchTurnContext).mock.calls[0][4];
  rerender({ turn: 'first', revision: 'r1', enabled: false, session: 'A', scope: '' });
  expect(signal.aborted).toBe(true);
});
it('isolates fast turn/scope/revision changes and aborts a late old response', async () => {
  let finish!: (value: TurnContextResponse) => void;
  vi.mocked(fetchTurnContext).mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }))
    .mockImplementation(async (session, turn, request) => ({ ...ready, session_id: session, turn_key: turn, revision: request.revision ?? 'r1' }));
  const { result, rerender } = setup();
  await waitFor(() => expect(fetchTurnContext).toHaveBeenCalledTimes(1));
  const signal = vi.mocked(fetchTurnContext).mock.calls[0][4];
  rerender({ turn: 'second', revision: 'r2', enabled: true, session: 'B', scope: 'new-account' });
  expect(result.current.records).toEqual([]);
  await waitFor(() => expect(result.current.state).toBe('ready'));
  await act(async () => finish(ready));
  expect(signal.aborted).toBe(true);
  expect(result.current.data?.pages[0].turn_key).toBe('second');
  expect(vi.mocked(fetchTurnContext).mock.calls[1][3]).toEqual({ loginEmail: 'new-account' });
});
it('reuses the same revision cache immediately when revisiting a turn', async () => {
  vi.mocked(fetchTurnContext).mockImplementation(async (session, turn) => ({ ...ready, session_id: session, turn_key: turn }));
  const { result, rerender } = setup();
  await waitFor(() => expect(result.current.state).toBe('ready'));
  rerender({ turn: 'second', revision: 'r1', enabled: true, session: 'A', scope: '' });
  await waitFor(() => expect(fetchTurnContext).toHaveBeenCalledTimes(2));
  rerender({ turn: 'first', revision: 'r1', enabled: true, session: 'A', scope: '' });
  expect(result.current.state).toBe('ready');
  expect(fetchTurnContext).toHaveBeenCalledTimes(2);
});
it('discovers a revision for a direct link and puts the ready page in the revision cache', async () => {
  const { result, client } = setup({ turn: 'first', revision: undefined, enabled: true, session: 'A', scope: '' });
  await waitFor(() => expect(result.current.state).toBe('ready'));
  expect(vi.mocked(fetchTurnContext).mock.calls[0][2].revision).toBeUndefined();
  expect(client.getQueryData(turnContextKey(identity, 'first', 'r1'))).toBeDefined();
});
it('treats revision fences as freshness and reacquires canonical context without an obsolete revision', async () => {
  vi.useFakeTimers();
  vi.mocked(fetchTurnContext).mockRejectedValueOnce(new TurnContextRequestError(409)).mockResolvedValue({ ...ready, revision: 'r2' });
  const { result } = setup();
  for (let step = 0; step < 20 && !result.current.updating; step++) await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  expect(result.current.updating).toBe(true);
  expect(result.current.records).toEqual([]);
  expect(result.current.error).toBeUndefined();
  await act(async () => { await vi.advanceTimersByTimeAsync(5001); });
  for (let step = 0; step < 20 && result.current.state !== 'ready'; step++) await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  expect(result.current.state).toBe('ready');
  expect(vi.mocked(fetchTurnContext).mock.calls[1][2].revision).toBeUndefined();
});
it.each(['pending', 'unavailable'] as const)('keeps canonical %s distinct from an empty ready turn', async state => {
  vi.mocked(fetchTurnContext).mockResolvedValue({ ...ready, state, revision: '' });
  const { result } = setup();
  await waitFor(() => expect(result.current.state).toBe(state));
  expect(result.current.error).toBeNull();
});
it('hides disclosed pages after a deletion or authorization response on the next page', async () => {
  const record = { session_id: 'A', project_hash: '', profile_email: '', model: '', agent: 'claude', record_type: 'user', ts: '', raw: 'never read', view: { kind: 'message' as const, role: 'user' as const, anchor_id: 'one', dedupe_key: 'one', text: 'old disclosed content' } };
  vi.mocked(fetchTurnContext).mockResolvedValueOnce({ ...ready, records: [record], next_cursor: 'next' }).mockRejectedValue(new TurnContextRequestError(404));
  const { result } = setup();
  await waitFor(() => expect(result.current.records.length).toBe(1));
  await act(async () => { await result.current.fetchNextPage({ cancelRefetch: false }); });
  await waitFor(() => expect(result.current.error).toBeInstanceOf(TurnContextRequestError));
  expect(result.current.records).toEqual([]);
  expect(result.current.error).toBeInstanceOf(TurnContextRequestError);
});
it('retains the ready scoped page while inactive so the original scroll area does not collapse', async () => {
  const record = { session_id: 'A', project_hash: '', profile_email: '', model: '', record_type: 'user', ts: '', view: { kind: 'message' as const, anchor_id: 'kept', dedupe_key: '', text: 'retained original' } };
  vi.mocked(fetchTurnContext).mockResolvedValue({ ...ready, records: [record] });
  const { result, rerender } = setup();
  await waitFor(() => expect(result.current.records.length).toBe(1));
  rerender({ turn: 'first', revision: 'r1', enabled: false, session: 'A', scope: '' });
  expect(result.current.records).toEqual([record]);
  expect(fetchTurnContext).toHaveBeenCalledTimes(1);
});
const disclosed = { session_id: 'A', project_hash: '', profile_email: '', model: '', record_type: 'user', ts: '', view: { kind: 'message' as const, anchor_id: 'disclosed', dedupe_key: '', text: 'disclosed original' } };
it('revokes every revision alias after a direct-link next-page 404 and never restores it from a known token', async () => {
  vi.mocked(fetchTurnContext).mockResolvedValueOnce({ ...ready, records: [disclosed], next_cursor: 'more' }).mockRejectedValue(new TurnContextRequestError(404));
  const { result, client, rerender } = setup({ turn: 'first', revision: undefined, enabled: true, session: 'A', scope: '' });
  await waitFor(() => expect(result.current.records.length).toBe(1));
  expect(client.getQueryData(turnContextKey(identity, 'first', 'r1'))).toBeDefined();
  await act(async () => { await result.current.fetchNextPage({ cancelRefetch: false }); });
  await waitFor(() => expect(result.current.error).toBeInstanceOf(TurnContextRequestError));
  expect(client.getQueryData(turnContextKey(identity, 'first', 'r1'))).toBeUndefined();
  rerender({ turn: 'first', revision: 'r1', enabled: true, session: 'A', scope: '' });
  expect(result.current.records).toEqual([]);
  await waitFor(() => expect(fetchTurnContext).toHaveBeenCalledTimes(3));
  expect(result.current.records).toEqual([]);
});
it('keeps disclosed direct-link records and scroll height while the next page is pending', async () => {
  vi.mocked(fetchTurnContext).mockResolvedValueOnce({ ...ready, records: [disclosed], next_cursor: 'more' }).mockImplementation(() => new Promise(() => undefined));
  const { result } = setup({ turn: 'first', revision: undefined, enabled: true, session: 'A', scope: '' });
  await waitFor(() => expect(result.current.records.length).toBe(1));
  act(() => { void result.current.fetchNextPage({ cancelRefetch: false }); });
  await waitFor(() => expect(result.current.isFetchingNextPage).toBe(true));
  expect(result.current.records).toEqual([disclosed]);
});
it('does not abort another active observer when one consumer of the same selection hides', async () => {
  vi.mocked(fetchTurnContext).mockImplementation(() => new Promise(() => undefined));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: React.ReactNode }) => createElement(QueryClientProvider, { client }, children);
  const one = renderHook(({ enabled }) => useTurnContext({ identity, turnKey: 'first', revision: 'r1', enabled }), { wrapper, initialProps: { enabled: true } });
  const two = renderHook(() => useTurnContext({ identity, turnKey: 'first', revision: 'r1' }), { wrapper });
  await waitFor(() => expect(fetchTurnContext).toHaveBeenCalledTimes(1));
  const signal = vi.mocked(fetchTurnContext).mock.calls[0][4];
  one.rerender({ enabled: false });
  expect(signal.aborted).toBe(false);
  expect(two.result.current.fetchStatus).toBe('fetching');
  two.unmount();
  expect(signal.aborted).toBe(true);
});
it('revokes sibling turns and account scopes for the same user/session while preserving unrelated caches', async () => {
  vi.mocked(fetchTurnContext).mockResolvedValueOnce({ ...ready, records: [disclosed], next_cursor: 'more' }).mockRejectedValue(new TurnContextRequestError(404));
  const { result, client, rerender } = setup();
  await waitFor(() => expect(result.current.records.length).toBe(1));
  const sibling = { ...identity, scope: { loginEmail: 'another-account' } };
  const siblingKey = turnContextKey(sibling, 'second', 'r1');
  const otherSessionKey = turnContextKey({ ...identity, sessionId: 'B' }, 'first', 'r1');
  const otherUserKey = turnContextKey({ ...identity, userId: 8 }, 'first', 'r1');
  for (const key of [siblingKey, otherSessionKey, otherUserKey]) client.setQueryData(key, { pages: [{ ...ready, records: [disclosed] }], pageParams: [undefined] });
  await act(async () => { await result.current.fetchNextPage({ cancelRefetch: false }); });
  await waitFor(() => expect(result.current.error).toBeInstanceOf(TurnContextRequestError));
  expect(client.getQueryData(siblingKey)).toBeUndefined();
  expect(client.getQueryData(otherSessionKey)).toBeDefined();
  expect(client.getQueryData(otherUserKey)).toBeDefined();
  rerender({ turn: 'second', revision: 'r1', enabled: true, session: 'A', scope: 'another-account' });
  expect(result.current.records).toEqual([]);
  await waitFor(() => expect(fetchTurnContext).toHaveBeenCalledTimes(3));
});

it('keeps a valid active sibling when an authorized missing turn returns turn_not_found', async () => {
  vi.mocked(fetchTurnContext).mockImplementation(async (_session, turn) => {
    if (turn === 'missing') throw new TurnContextRequestError(404, 'turn_not_found');
    return { ...ready, records: [disclosed] };
  });
  const { result, client } = setup();
  await waitFor(() => expect(result.current.records).toEqual([disclosed]));
  const wrapper = ({ children }: { children: React.ReactNode }) => createElement(QueryClientProvider, { client }, children);
  const missing = renderHook(() => useTurnContext({ identity, turnKey: 'missing', revision: 'r1' }), { wrapper });
  await waitFor(() => expect(missing.result.current.error).toBeInstanceOf(TurnContextRequestError));
  expect(result.current.records).toEqual([disclosed]);
  expect(result.current.error).toBeNull();
});

it('finishes an in-flight direct-link read when analysis supplies its revision instead of aborting and restarting it', async () => {
  let finish!: (value: TurnContextResponse) => void;
  vi.mocked(fetchTurnContext).mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
  const { result, rerender } = setup({ turn: 'first', revision: undefined, enabled: true, session: 'A', scope: '' });
  await waitFor(() => expect(fetchTurnContext).toHaveBeenCalledTimes(1));
  const signal = vi.mocked(fetchTurnContext).mock.calls[0][4];
  rerender({ turn: 'first', revision: 'r1', enabled: true, session: 'A', scope: '' });
  expect(signal.aborted).toBe(false);
  await act(async () => finish({ ...ready, records: [disclosed] }));
  await waitFor(() => expect(result.current.records).toEqual([disclosed]));
  expect(fetchTurnContext).toHaveBeenCalledTimes(1);
});
