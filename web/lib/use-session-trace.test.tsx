// @vitest-environment jsdom
import { createElement } from 'react';
import { act, cleanup, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { demandSessionTrace, releaseSessionTrace, fetchTraceOverview, fetchTraceEvents, fetchTraceContext, TraceRequestError } from './session-trace-api';
import { useSessionTrace } from './use-session-trace';

vi.mock('./session-trace-api', () => ({ demandSessionTrace: vi.fn(), releaseSessionTrace: vi.fn(), fetchTraceOverview: vi.fn(), fetchTraceEvents: vi.fn(), fetchTraceContext: vi.fn(), TraceRequestError: class extends Error { constructor(readonly status: number) { super(String(status)); } } }));
const identity = { userId: 7, access: 'owner' as const, sessionId: 'A' };
const coverage = { state: 'partial' as const, from: null, to: null, processed_rows: 0, row_limit: 200000, reason: null };
beforeEach(() => {
  vi.mocked(demandSessionTrace).mockReset().mockImplementation(async id => ({ session_id: id, lease_id: 'lease', snapshot_id: 'snapshot', state: 'building', expires_at: '2026-10-13T00:00:15Z' }));
  vi.mocked(releaseSessionTrace).mockReset().mockResolvedValue();
  vi.mocked(fetchTraceOverview).mockReset().mockImplementation(async id => ({ session_id: id, snapshot_id: 'snapshot', source_revision: '1', state: 'building', coverage, from: null, to: null, bins: [], events_count: 0 }));
  vi.mocked(fetchTraceEvents).mockReset();
  vi.mocked(fetchTraceContext).mockReset().mockResolvedValue({ session_id: 'A', snapshot_id: 'snapshot', event_id: null, records: [], next_cursor: null });
});
afterEach(() => { cleanup(); vi.useRealTimers(); });
const setup = (enabled = true) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: React.ReactNode }) => createElement(QueryClientProvider, { client }, children);
  return { client, ...renderHook(({ active, id }) => useSessionTrace({ identity: { ...identity, sessionId: id }, enabled: active }), { wrapper, initialProps: { active: enabled, id: 'A' } }) };
};
it('creates no demand/range in a disabled/default view and waits for ready overview before sampling', async () => {
  const { rerender } = setup(false);
  expect(demandSessionTrace).not.toHaveBeenCalled();
  rerender({ active: true, id: 'A' });
  await waitFor(() => expect(fetchTraceOverview).toHaveBeenCalled());
  expect(fetchTraceEvents).not.toHaveBeenCalled();
});
const readyOverview = { session_id: 'A', snapshot_id: 'snapshot', source_revision: '1', state: 'ready' as const, coverage,
  from: '2026-10-13T00:00:00Z', to: '2026-10-13T00:01:00Z', bins: [], events_count: 1100 };
it('reads one bounded ready snapshot sample including the final point without a range demand or auto-drain', async () => {
  vi.mocked(demandSessionTrace).mockResolvedValue({ session_id: 'A', lease_id: 'ready-lease', snapshot_id: 'snapshot', state: 'ready', expires_at: '' });
  vi.mocked(fetchTraceOverview).mockResolvedValue(readyOverview);
  vi.mocked(fetchTraceEvents).mockResolvedValue({ ...readyOverview, events: [], next_cursor: 'more' });
  setup();
  await waitFor(() => expect(fetchTraceEvents).toHaveBeenCalledTimes(1));
  expect(vi.mocked(fetchTraceEvents).mock.calls[0][1]).toEqual({ snapshot_id: 'snapshot', from: readyOverview.from, to: '2026-10-13T00:01:00.001Z' });
  expect(vi.mocked(demandSessionTrace).mock.calls.every(call => call[1].mode === 'overview')).toBe(true);
});
it('waits for ready before caching selected context and stops completed demand renewals', async () => {
  vi.useFakeTimers();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: React.ReactNode }) => createElement(QueryClientProvider, { client }, children);
  renderHook(() => useSessionTrace({ identity, turnKey: 'turn' }), { wrapper });
  for (let step = 0; step < 20 && !vi.mocked(fetchTraceOverview).mock.calls.length; step++) {
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  }
  expect(fetchTraceOverview).toHaveBeenCalled();
  expect(fetchTraceContext).not.toHaveBeenCalled();
  vi.mocked(demandSessionTrace).mockResolvedValue({ session_id: 'A', lease_id: 'ready-lease', snapshot_id: 'snapshot', state: 'ready', expires_at: '' });
  vi.mocked(fetchTraceOverview).mockResolvedValue(readyOverview);
  vi.mocked(fetchTraceEvents).mockResolvedValue({ ...readyOverview, events: [], next_cursor: null });
  await act(async () => { await vi.advanceTimersByTimeAsync(5000); await client.invalidateQueries({ queryKey: ['session-trace'] }); });
  await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  expect(fetchTraceContext).toHaveBeenCalledTimes(1);
  const count = vi.mocked(demandSessionTrace).mock.calls.length;
  await act(async () => { await vi.advanceTimersByTimeAsync(120000); });
  expect(demandSessionTrace).toHaveBeenCalledTimes(count);
});
it('treats a fenced snapshot as updating and automatically acquires a new snapshot', async () => {
  vi.mocked(fetchTraceOverview).mockRejectedValueOnce(new TraceRequestError(409)).mockResolvedValue({ ...readyOverview, snapshot_id: 'fresh' });
  vi.mocked(demandSessionTrace).mockResolvedValueOnce({ session_id: 'A', lease_id: 'lease', snapshot_id: 'snapshot', state: 'ready', expires_at: '' })
    .mockResolvedValue({ session_id: 'A', lease_id: 'new-lease', snapshot_id: 'fresh', state: 'ready', expires_at: '' });
  vi.mocked(fetchTraceEvents).mockResolvedValue({ ...readyOverview, snapshot_id: 'fresh', events: [], next_cursor: null });
  vi.useFakeTimers();
  const { result } = setup();
  for (let step = 0; step < 20 && !result.current.updating; step++) {
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  }
  expect(result.current.updating).toBe(true);
  expect(result.current.error).toBeUndefined();
  await act(async () => { await vi.advanceTimersByTimeAsync(5001); });
  expect(vi.mocked(demandSessionTrace).mock.calls.length).toBe(2);
  for (let step = 0; step < 20 && result.current.overview.data?.snapshot_id !== 'fresh'; step++) {
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  }
  expect(result.current.overview.data?.snapshot_id).toBe('fresh');
});
it('aborts reads and returns its lease when hidden or unmounted', async () => {
  vi.mocked(fetchTraceOverview).mockImplementation(() => new Promise(() => undefined));
  const { rerender, unmount } = setup();
  await waitFor(() => expect(fetchTraceOverview).toHaveBeenCalled());
  const signal = vi.mocked(fetchTraceOverview).mock.calls[0][3];
  rerender({ active: false, id: 'A' });
  await waitFor(() => expect(releaseSessionTrace).toHaveBeenCalledTimes(1));
  expect(signal.aborted).toBe(true);
  unmount();
  expect(releaseSessionTrace).toHaveBeenCalledTimes(1);
});
it('returns a late acquired lease without publishing it into the next session', async () => {
  let finish!: (lease: Awaited<ReturnType<typeof demandSessionTrace>>) => void;
  vi.mocked(demandSessionTrace).mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
  const { rerender } = setup();
  rerender({ active: true, id: 'B' });
  await act(async () => finish({ session_id: 'A', lease_id: 'old', snapshot_id: 'old-snapshot', state: 'ready', expires_at: '' }));
  await waitFor(() => expect(releaseSessionTrace).toHaveBeenCalledWith('A', 'old', undefined, expect.any(AbortSignal)));
  expect(vi.mocked(fetchTraceOverview).mock.calls.every(call => call[0] === 'B')).toBe(true);
});
it('does not publish a late context response over a newly selected turn', async () => {
  vi.mocked(demandSessionTrace).mockResolvedValue({ session_id: 'A', lease_id: 'ready-lease', snapshot_id: 'snapshot', state: 'ready', expires_at: '' });
  vi.mocked(fetchTraceOverview).mockResolvedValue(readyOverview);
  vi.mocked(fetchTraceEvents).mockResolvedValue({ ...readyOverview, events: [], next_cursor: null });
  let finish!: (value: Awaited<ReturnType<typeof fetchTraceContext>>) => void;
  vi.mocked(fetchTraceContext).mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }))
    .mockResolvedValue({ session_id: 'A', snapshot_id: 'snapshot', event_id: 'new-event', records: [], next_cursor: null });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: React.ReactNode }) => createElement(QueryClientProvider, { client }, children);
  const { result, rerender } = renderHook(({ turn }) => useSessionTrace({ identity, turnKey: turn }), { wrapper, initialProps: { turn: 'old-turn' } });
  await waitFor(() => expect(fetchTraceContext).toHaveBeenCalledTimes(1));
  const signal = vi.mocked(fetchTraceContext).mock.calls[0][3];
  rerender({ turn: 'new-turn' });
  await waitFor(() => expect(result.current.context.data?.pages[0]?.event_id).toBe('new-event'));
  await act(async () => finish({ session_id: 'A', snapshot_id: 'snapshot', event_id: 'old-event', records: [], next_cursor: null }));
  expect(signal.aborted).toBe(true);
  expect(result.current.context.data?.pages[0]?.event_id).toBe('new-event');
});
it('retains the visible snapshot throughout a drag while pausing requests', async () => {
  vi.mocked(demandSessionTrace).mockResolvedValue({ session_id: 'A', lease_id: 'ready-lease', snapshot_id: 'snapshot', state: 'ready', expires_at: '' });
  vi.mocked(fetchTraceOverview).mockResolvedValue(readyOverview);
  vi.mocked(fetchTraceEvents).mockResolvedValue({ ...readyOverview, events: [], next_cursor: null });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: React.ReactNode }) => createElement(QueryClientProvider, { client }, children);
  const { result, rerender } = renderHook(({ dragging }) => useSessionTrace({ identity, dragging }), { wrapper, initialProps: { dragging: false } });
  await waitFor(() => expect(result.current.overview.data?.snapshot_id).toBe('snapshot'));
  const count = vi.mocked(demandSessionTrace).mock.calls.length;
  rerender({ dragging: true });
  expect(result.current.overview.data?.snapshot_id).toBe('snapshot');
  expect(result.current.active).toBe(false);
  rerender({ dragging: false });
  expect(result.current.overview.data?.snapshot_id).toBe('snapshot');
  expect(demandSessionTrace).toHaveBeenCalledTimes(count);
});
it('retains a completed scoped snapshot when the shell hides the mounted analysis', async () => {
  vi.mocked(demandSessionTrace).mockResolvedValue({ session_id: 'A', lease_id: 'ready-lease', snapshot_id: 'snapshot', state: 'ready', expires_at: '' });
  vi.mocked(fetchTraceOverview).mockResolvedValue(readyOverview);
  vi.mocked(fetchTraceEvents).mockResolvedValue({ ...readyOverview, events: [], next_cursor: null });
  const { result, rerender } = setup();
  await waitFor(() => expect(result.current.overview.data?.state).toBe('ready'));
  const count = vi.mocked(demandSessionTrace).mock.calls.length;
  rerender({ active: false, id: 'A' });
  expect(result.current.overview.data?.snapshot_id).toBe('snapshot');
  expect(result.current.active).toBe(false);
  expect(demandSessionTrace).toHaveBeenCalledTimes(count);
});
