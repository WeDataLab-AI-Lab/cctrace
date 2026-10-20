import { describe, expect, it } from 'vitest';
import type { SessionTraceIndex, SessionTraceIndexEntry } from '@/lib/session-trace-index';
import { conversationRequests, indexedIntervals, requestDensity, sampleIntervals } from './timeline-index';

const entry = (overrides: Partial<SessionTraceIndexEntry> = {}): SessionTraceIndexEntry => ({
  request_no: 1, turn_key: 'turn-1', started_at: '2026-10-20T00:00:00Z', ended_at: '2026-10-20T00:00:05Z',
  preview: '질문', preview_state: 'ok', status: 'ok', api_requests: 1, tools: 0, subagents: 0, abandoned: false, ...overrides,
});

describe('indexedIntervals', () => {
  it('places turns outside the loaded conversation window on the session axis', () => {
    expect(indexedIntervals([entry()])).toEqual([{ key: 'turn-1', requestNo: 1, startMs: Date.parse('2026-10-20T00:00:00Z'), endMs: Date.parse('2026-10-20T00:00:05Z'), preview: '질문' }]);
  });
  it('drops turns with unparseable timestamps and never inverts an interval', () => {
    expect(indexedIntervals([entry({ started_at: 'nope' })])).toEqual([]);
    const [turn] = indexedIntervals([entry({ ended_at: '2026-10-19T00:00:00Z' })]);
    expect(turn.endMs).toBe(turn.startMs);
  });
});

describe('sampleIntervals', () => {
  it('keeps a long request list from becoming a solid hatch', () => {
    const marks = Array.from({ length: 345 }, (_, i) => ({ key: `t${i}`, requestNo: i + 1, startMs: i * 1000, endMs: i * 1000 + 500, preview: '' }));
    const sampled = sampleIntervals(marks);
    expect(sampled.length).toBeLessThanOrEqual(49);
    expect(sampled[0].key).toBe('t0');
    expect(sampled[sampled.length - 1].key).toBe('t344');
  });
  it('leaves a short list untouched', () => {
    const marks = indexedIntervals([entry()]);
    expect(sampleIntervals(marks)).toEqual(marks);
  });
});

describe('requestDensity', () => {
  it('counts every request while keeping the drawn element count bounded', () => {
    const marks = Array.from({ length: 367 }, (_, i) => ({ key: `t${i}`, requestNo: i + 1, startMs: i * 1000, endMs: i * 1000 + 500, preview: '' }));
    const bins = requestDensity(marks, (ms) => ms / 367_000, 180);
    expect(bins.reduce((sum, bin) => sum + bin.count, 0)).toBe(367);
    expect(bins.length).toBeLessThanOrEqual(180);
  });
  it('separates a dense burst from the quiet stretch beside it', () => {
    const marks = [0, 1, 2, 3, 4].map(i => ({ key: `d${i}`, requestNo: i + 1, startMs: i, endMs: i, preview: '' }));
    const bins = requestDensity(marks, (ms) => ms / 100, 10);
    expect(bins[0].count).toBe(5);
    expect(bins.length).toBe(1);
  });
});


const at = (s: number) => new Date(Date.UTC(2026, 9, 12, 4, 0, s)).toISOString();
const turnEntry = (no: number, opened: number, started: number): SessionTraceIndexEntry => ({
  request_no: no, turn_key: `t${no}`, started_at: at(started), ended_at: at(started + 5), opened_at: at(opened),
  preview: '', preview_state: 'ok', status: 'ended', api_requests: 1, tools: 0, subagents: 0, abandoned: false,
});
const index = (state: string, requests: SessionTraceIndexEntry[], complete = true): SessionTraceIndex => ({
  session_id: 's', state, request_total: requests.length, requests, coverage: 'complete', request_limit: 2001, requests_complete: complete,
});
const timeline = (seconds: number[]) => ({ requests: seconds.map(s => ({ ts: at(s), ms: Date.parse(at(s)) })), replies: [], spans: [] });

describe('conversationRequests', () => {
  it('numbers by turn and keeps messages typed inside a turn apart', () => {
    // The turn starts after its message (Codex); the message at 20s joined turn 1.
    const got = conversationRequests(index('ready', [turnEntry(1, 0, 8), turnEntry(2, 30, 41)]), timeline([0, 20, 30]));
    expect(got?.basis).toBe('turns');
    expect(got?.requests.map(r => [r.no, r.ts])).toEqual([[1, at(0)], [2, at(30)]]);
    expect(got?.extras).toEqual([Date.parse(at(20))]);
  });

  it('counts the messages while the analysis is not ready or is capped', () => {
    for (const idx of [undefined, index('building', []), index('ready', [turnEntry(1, 0, 0)], false)]) {
      const got = conversationRequests(idx, timeline([0, 20]));
      expect(got?.basis).toBe('messages');
      expect(got?.requests.map(r => r.no)).toEqual([1, 2]);
      expect(got?.extras).toEqual([]);
    }
  });
});
