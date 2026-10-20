// @vitest-environment jsdom
import { createElement } from 'react';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { TraceChart } from './trace-chart';
import type { TraceEvent, TraceOverview } from '@/lib/session-trace-types';

const from = '2026-10-13T00:00:00Z', to = '2026-10-13T00:10:00Z';
const overview: TraceOverview = { session_id: 'A', snapshot_id: 'snapshot', source_revision: '1', state: 'ready',
  from, to, bins: [], events_count: 2, coverage: { state: 'partial', from, to, processed_rows: 100, row_limit: 200000, reason: 'incomplete' } };
const events: TraceEvent[] = [1, 2].map(n => ({ event_id: `event-${n}`, kind: 'tool', timestamp: `2026-10-13T00:0${n}:00Z`, started_at: `2026-10-13T00:0${n}:00Z`, ended_at: `2026-10-13T00:0${n}:20Z`, turn_key: `turn-${n}`, parent_event_id: null, agent_id: null, label: `Bash ${n}`, status: 'completed', evidence: [], source_ref: `opaque-${n}` }));
beforeEach(() => { vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null); vi.stubGlobal('PointerEvent', MouseEvent); });
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
it('distinguishes an unavailable bounded trace from normal preparation', () => {
  render(createElement(TraceChart, { overview: { ...overview, state: 'unavailable', coverage: { ...overview.coverage, state: 'unavailable', reason: 'database_budget' } }, events: [], onSelect: vi.fn(), onRange: vi.fn(), onDragging: vi.fn() }));
  expect(screen.getByText(/이 범위의 기록을 준비하지 못했습니다/)).toBeTruthy();
  expect(screen.getByRole('button', { name: '확대' }).hasAttribute('disabled')).toBe(false);
  expect(screen.queryByRole('alert')).toBeNull();
});
it('supports track keyboard selection and explicit zoom, shows partial coverage without an error alert', () => {
  const onSelect = vi.fn(), onRange = vi.fn();
  render(createElement(TraceChart, { overview, events, selectedId: undefined, range: { from, to }, onSelect, onRange, onDragging: vi.fn() }));
  const track = screen.getByRole('button', { name: /도구 트랙/ });
  fireEvent.keyDown(track, { key: 'ArrowRight' });
  fireEvent.keyDown(track, { key: 'Enter' });
  expect(onSelect).toHaveBeenCalledWith(events[1]);
  fireEvent.click(screen.getByRole('button', { name: '확대' }));
  expect(onRange).toHaveBeenCalledWith({ from: '2026-10-13T00:00:00.000Z', to: '2026-10-13T00:05:00.000Z' });
  expect(screen.getByText(/미수집/)).toBeTruthy();
  expect(screen.queryByRole('alert')).toBeNull();
  expect(screen.getByRole('button', { name: /Bash 1/ })).toBeTruthy();
});
it('commits a drag range only at pointer release', () => {
  const onRange = vi.fn(), onDragging = vi.fn();
  render(createElement(TraceChart, { overview, events: [], onSelect: vi.fn(), onRange, onDragging }));
  const track = screen.getByRole('button', { name: /입력 트랙/ });
  vi.spyOn(track, 'getBoundingClientRect').mockReturnValue({ left: 0, width: 600 } as DOMRect);
  fireEvent.pointerDown(track, { clientX: 100, pointerId: 1 });
  fireEvent.pointerMove(track, { clientX: 200, pointerId: 1 });
  expect(onRange).not.toHaveBeenCalled();
  fireEvent.pointerUp(track, { clientX: 300, pointerId: 1 });
  expect(onRange).toHaveBeenCalledTimes(1);
  expect(onRange).toHaveBeenCalledWith({ from: '2026-10-13T00:01:40.000Z', to: '2026-10-13T00:05:00.000Z' });
  fireEvent.click(track, { detail: 1 });
  expect(onRange).toHaveBeenCalledTimes(1);
  expect(onDragging.mock.calls.map(call => call[0])).toEqual([true, false]);
});
it('preserves pointer selection through the following browser click and never substitutes keyboard focus', () => {
  const onSelect = vi.fn();
  render(createElement(TraceChart, { overview, events, selectedId: 'event-2', range: { from, to }, onSelect, onRange: vi.fn(), onDragging: vi.fn() }));
  const track = screen.getByRole('button', { name: /도구 트랙/ });
  vi.spyOn(track, 'getBoundingClientRect').mockReturnValue({ left: 0, width: 600 } as DOMRect);
  fireEvent.pointerDown(track, { clientX: 120 }); fireEvent.pointerUp(track, { clientX: 120 });
  fireEvent.click(track, { detail: 1 });
  expect(onSelect).toHaveBeenCalledTimes(1);
  expect(onSelect).toHaveBeenCalledWith(events[1]);
  expect(track.querySelector('canvas')?.getAttribute('data-selected-event')).toBe('event-2');
});
it('renders only the execution viewport and keeps the last thin full row reachable by scrolling', () => {
  const many = Array.from({ length: 1000 }, (_, n) => ({ ...events[0], event_id: `e${n}`, label: `Call ${n}` }));
  render(createElement(TraceChart, { overview, events: many, range: { from, to }, onSelect: vi.fn(), onRange: vi.fn(), onDragging: vi.fn() }));
  expect(document.querySelectorAll('canvas').length).toBeLessThanOrEqual(104);
  expect(screen.queryByRole('button', { name: /Call 999/ })).toBeNull();
  const viewport = screen.getByRole('region', { name: '턴 실행 목록' });
  expect(viewport.querySelectorAll('[data-event-row]').length).toBeLessThanOrEqual(24);
  fireEvent.scroll(viewport, { target: { scrollTop: 19960 } });
  expect(screen.getByRole('button', { name: 'Call 999' })).toBeTruthy();
});
it('keeps parallel geometry and only renders parent links backed by event IDs', () => {
  const onSelect = vi.fn();
  const parallel = [events[0], { ...events[1], timestamp: events[0].timestamp, started_at: events[0].started_at, parent_event_id: events[0].event_id }];
  render(createElement(TraceChart, { overview, events: parallel, range: { from, to }, onSelect, onRange: vi.fn(), onDragging: vi.fn() }));
  const first = screen.getByRole('button', { name: 'Bash 1' });
  const second = screen.getByRole('button', { name: 'Bash 2' });
  expect(first.querySelector('[data-execution-mark]')?.getAttribute('x')).toBe(second.querySelector('[data-execution-mark]')?.getAttribute('x'));
  expect(first.querySelector('[data-execution-mark]')?.getAttribute('height')).toBe('3');
  expect(second.getAttribute('data-parent-event')).toBe('event-1');
  expect(first.getAttribute('data-parent-event')).toBeNull();
  fireEvent.click(second.querySelector('[data-row-hit]')!);
  expect(onSelect).toHaveBeenCalledWith(parallel[1]);
  fireEvent.keyDown(first, { key: 'ArrowDown' });
  expect(onSelect).toHaveBeenLastCalledWith(parallel[1]);
});
it.each(['ready', 'unavailable'] as const)('reports an empty %s trace without claiming it is still building', state => {
  render(createElement(TraceChart, { overview: { ...overview, state, from: null, to: null, events_count: 0 }, events: [], onSelect: vi.fn(), onRange: vi.fn(), onDragging: vi.fn() }));
  expect(screen.queryByText('실행 기록을 준비 중입니다.')).toBeNull();
  expect(screen.getByText(state === 'ready' ? '관측된 실행 사건이 없습니다.' : '실행 시각을 확인할 수 없습니다.')).toBeTruthy();
});
it('shows a single real timestamp using only an expanded visual axis', () => {
  const point = { ...events[0], kind: 'input' as const, timestamp: from, started_at: null, ended_at: null };
  render(createElement(TraceChart, { overview: { ...overview, from, to: from, bins: [{ kind: 'input', from, to: from, first_ts: from, last_ts: from, count: 1 }], events_count: 1 }, events: [point], onSelect: vi.fn(), onRange: vi.fn(), onDragging: vi.fn() }));
  expect(screen.getByRole('button', { name: /입력 트랙/ })).toBeTruthy();
  expect(screen.getByText(/단일 시점/)).toBeTruthy();
});
it('zooms around a density point outside the bounded sample instead of selecting a distant event', () => {
  const onSelect = vi.fn(), onRange = vi.fn();
  render(createElement(TraceChart, { overview, events, onSelect, onRange, onDragging: vi.fn() }));
  const track = screen.getByRole('button', { name: /도구 트랙/ });
  vi.spyOn(track, 'getBoundingClientRect').mockReturnValue({ left: 0, width: 600 } as DOMRect);
  fireEvent.pointerDown(track, { clientX: 540 }); fireEvent.pointerUp(track, { clientX: 540 }); fireEvent.click(track, { detail: 1 });
  expect(onSelect).not.toHaveBeenCalled();
  expect(onRange).toHaveBeenCalledWith({ from: '2026-10-13T00:05:00.000Z', to: '2026-10-13T00:10:00.000Z' });
});
