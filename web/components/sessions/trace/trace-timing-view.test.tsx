// @vitest-environment jsdom
import { createElement } from 'react';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { TraceTimingView } from './trace-timing-view';
const state = vi.hoisted(() => ({ calls: [] as unknown[], nodes: [] as unknown[] }));
vi.mock('@/lib/use-trace-nodes', () => ({ useTraceNodes: (options: unknown) => {
  state.calls.push(options);
  return { nodes: state.nodes, coverage: { from: '2026-10-19T00:00:00Z', to: '2026-10-19T00:01:00Z' }, isPending: false, isError: false, hasNextPage: true, fetchNextPage: vi.fn(), refresh: vi.fn() };
} }));
afterEach(() => { cleanup(); state.calls = []; state.nodes = []; });
it('queries the selected group independently and selects stable IDs without changing its range', () => {
  state.nodes = [{ node_id: 'tool-1', parent_id: null, kind: 'tool', title: 'Read settings.json', started_at: '2026-10-19T00:00:10Z', ended_at: '2026-10-19T00:00:11Z', time_basis: 'call_result_observation', status: 'completed', evidence: [], availability: 'observed' }];
  const onSelectNode = vi.fn(), onClose = vi.fn();
  render(createElement(TraceTimingView, { identity: { userId: 7, access: 'owner', sessionId: 's' }, enabled: true, groupId: 'turn-1', selectedNodeId: 'tool-1', onSelectNode, onClose }));
  expect(state.calls.at(-1)).toMatchObject({ view: 'timing', groupId: 'turn-1', enabled: true });
  const row = screen.getByRole('button', { name: '時間分析 · Read settings.json'.replace('時間分析', '시간 분석') });
  expect(row.getAttribute('aria-pressed')).toBe('true');
  expect(row.querySelector('[data-observed-interval]')).toBeTruthy();
  fireEvent.click(row); expect(onSelectNode).toHaveBeenCalledWith('tool-1');
  fireEvent.click(screen.getByRole('button', { name: '요청으로 돌아가기' })); expect(onClose).toHaveBeenCalledTimes(1);
});
it('does not read or invent a global timing view without a selected group', () => {
  render(createElement(TraceTimingView, { identity: { userId: 7, access: 'owner', sessionId: 's' }, enabled: true, onSelectNode: vi.fn(), onClose: vi.fn() }));
  expect(state.calls.at(-1)).toMatchObject({ enabled: false });
  expect(screen.getByText('시간 분석할 요청을 먼저 선택하세요.')).toBeTruthy();
});

it.each([
  { start: '00', end: '15', x: '0', width: '500' },
  { start: '15', end: '25', x: '500', width: '500' },
])('clips observed interval $start–$end to the selected 10–20 second window', ({ start, end, x, width }) => {
  state.nodes = [{ node_id: 'crossing', parent_id: null, kind: 'tool', title: 'Crossing call', started_at: `2026-10-19T00:00:${start}Z`, ended_at: `2026-10-19T00:00:${end}Z`, time_basis: 'call_result_observation', status: 'completed', evidence: [], availability: 'observed' }];
  render(createElement(TraceTimingView, { identity: { userId: 7, access: 'owner', sessionId: 's' }, enabled: true, groupId: 'turn-1', range: { from: '2026-10-19T00:00:10Z', to: '2026-10-19T00:00:20Z' }, onSelectNode: vi.fn(), onClose: vi.fn() }));
  const interval = screen.getByRole('button', { name: '시간 분석 · Crossing call' }).querySelector('[data-observed-interval]');
  expect(interval?.getAttribute('x')).toBe(x);
  expect(interval?.getAttribute('width')).toBe(width);
});
