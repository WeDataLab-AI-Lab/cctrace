import { afterEach, describe, expect, it, vi } from 'vitest';
import { demandSessionTrace, fetchTraceContext, fetchTraceEvents, fetchTraceOverview, releaseSessionTrace } from './session-trace-api';
import { eventGeometry, pixelGroups, traceCountLabels, zoomRange } from './session-trace-model';
import type { TraceEvent } from './session-trace-types';

const from = '2026-10-13T00:00:00.000Z';
const to = '2026-10-13T00:01:40.000Z';
const event: TraceEvent = { event_id: 'opaque-1', kind: 'tool', timestamp: '2026-10-13T00:00:20Z',
  started_at: '2026-10-13T00:00:20Z', ended_at: '2026-10-13T00:00:40Z', turn_key: null,
  parent_event_id: null, agent_id: null, label: 'Bash', status: 'completed', evidence: [], source_ref: 'opaque-ref' };
afterEach(() => vi.unstubAllGlobals());

describe('trace wire and geometry', () => {
  it('counts density once when measured events overlap it and separates nearby labels', () => {
    const bins = [
      { kind: 'tool' as const, from, to, first_ts: '2026-10-13T00:00:20Z', last_ts: '2026-10-13T00:00:21Z', count: 12 },
      { kind: 'tool' as const, from, to, first_ts: '2026-10-13T00:00:22Z', last_ts: '2026-10-13T00:00:23Z', count: 7 },
      { kind: 'tool' as const, from, to, first_ts: '2026-10-13T00:01:20Z', last_ts: '2026-10-13T00:01:21Z', count: 3 },
    ];
    const before = JSON.stringify(bins);
    const labels = traceCountLabels([event, { ...event, event_id: 'second' }], bins, { from, to }, 1000);
    expect(labels).toEqual([{ x: 200, count: 19 }, { x: 800, count: 3 }]);
    expect(JSON.stringify(bins)).toBe(before);
    expect(traceCountLabels([event, { ...event, event_id: 'second' }], [], { from, to }, 1000)).toEqual([{ x: 200, count: 2 }]);
  });
  it('keeps every request scoped and abortable, encodes opaque IDs, releases only its lease', async () => {
    const fetch = vi.fn().mockImplementation(async () => new Response('{}', { status: 200 }));
    vi.stubGlobal('fetch', fetch);
    const signal = new AbortController().signal;
    const scope = { loginEmail: 'scope@example.test', userID: 'uid', profileEmail: 'profile@example.test' };
    await demandSessionTrace('session/a', { mode: 'range', from, to }, scope, signal);
    await fetchTraceOverview('session/a', 'snapshot', scope, signal);
    await fetchTraceEvents('session/a', { snapshot_id: 'snapshot', from, to, cursor: 'cursor' }, scope, signal);
    await fetchTraceContext('session/a', { snapshot_id: 'snapshot', event_id: 'event/a' }, scope, signal);
    await releaseSessionTrace('session/a', 'lease/a', scope, signal);
    for (const [url, init] of fetch.mock.calls) {
      expect(new URL(url).searchParams.get('user_id')).toBe('uid');
      expect(new URL(url).searchParams.get('login_email')).toBe(scope.loginEmail);
      expect(init.signal).toBe(signal);
      expect(init.credentials).toBe('include');
    }
    expect(fetch.mock.calls[0][0]).toContain('/session%2Fa/trace/demand');
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ mode: 'range', from, to });
    expect(fetch.mock.calls[2][0]).toContain('limit=1000');
    expect(fetch.mock.calls[4][0]).toContain('/demand/lease%2Fa');
    expect(fetch.mock.calls[4][1].method).toBe('DELETE');
  });
  it('preserves time gaps and observed spans, including spans crossing range bounds', () => {
    expect(eventGeometry(event, { from, to }, 1000, false)).toMatchObject({ x: 200, width: 200, height: 3 });
    expect(eventGeometry(event, { from, to }, 1000, true)).toMatchObject({ height: 6 });
    expect(eventGeometry({ ...event, started_at: null, ended_at: null }, { from, to }, 1000, false)).toMatchObject({ x: 200, width: 1, unknown: true });
    expect(eventGeometry({ ...event, timestamp: from, ended_at: null }, { from, to }, 1000, false)).toMatchObject({ x: 200, width: 1, height: 1, unknown: true });
    expect(eventGeometry({ ...event, kind: 'input' }, { from, to }, 1000, false)).toMatchObject({ width: 1, height: 1 });
    expect(eventGeometry(event, { from: '2026-10-13T00:00:30Z', to: '2026-10-13T00:00:50Z' }, 1000, false)).toMatchObject({ x: 0, width: 500 });
    expect(eventGeometry({ ...event, ended_at: '2026-10-13T00:00:10Z' }, { from, to }, 1000, false)?.unknown).toBe(true);
  });
  it('retains events sharing a pixel and zooms around the committed range center', () => {
    expect(pixelGroups([event, { ...event, event_id: 'opaque-2' }], { from, to }, 100).map(g => g.events.length)).toEqual([2]);
    expect(zoomRange({ from, to }, { from, to }, 0.5)).toEqual({ from: '2026-10-13T00:00:25.000Z', to: '2026-10-13T00:01:15.000Z' });
  });
});
