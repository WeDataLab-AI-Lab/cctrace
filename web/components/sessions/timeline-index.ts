import type { SessionTraceIndex, SessionTraceIndexEntry } from '@/lib/session-trace-index';
import type { ConversationTimeline } from '@/lib/conversation-timeline';

export interface IndexedInterval {
  key: string;
  requestNo: number;
  startMs: number;
  endMs: number;
  preview: string;
}

/** Keeps only turns the axis can place: unparseable timestamps would collapse to zero width. */
const indexedIntervals = (marks: SessionTraceIndexEntry[] | undefined): IndexedInterval[] =>
  (marks ?? []).flatMap(mark => {
    const startMs = Date.parse(mark.started_at);
    const endMs = Date.parse(mark.ended_at);
    if (!Number.isFinite(startMs) || !Number.isFinite(endMs)) return [];
    return [{ key: mark.turn_key, requestNo: mark.request_no, startMs, endMs: Math.max(endMs, startMs), preview: mark.preview }];
  });

// A long session can hold hundreds of requests. Drawing every one turns the
// strip into a hatch, so keep an evenly spaced sample (always the first and
// last) and let the scale still cover the whole session.
const sampleIntervals = (marks: IndexedInterval[], limit = 48): IndexedInterval[] => {
  if (marks.length <= limit) return marks;
  const stride = Math.ceil(marks.length / limit);
  const sampled = marks.filter((_, index) => index % stride === 0);
  const last = marks[marks.length - 1];
  if (sampled[sampled.length - 1]?.key !== last.key) sampled.push(last);
  return sampled;
};

export { indexedIntervals, sampleIntervals };

export interface DensityBin {
  key: number;
  /** A value in [0,1] along the axis. */
  at: number;
  count: number;
}

/**
 * Every request contributes to a fixed number of bins. Drawing each request as its
 * own element made a long session a solid hatch, and sampling the requests dropped
 * most of them; binning keeps the element count constant while still counting every
 * request exactly once.
 */
const requestDensity = (
  intervals: readonly IndexedInterval[],
  toRatio: (ms: number) => number,
  bins = 180,
): DensityBin[] => {
  if (intervals.length === 0 || bins <= 0) return [];
  const counts = new Array<number>(bins).fill(0);
  for (const interval of intervals) {
    const ratio = Math.min(1, Math.max(0, toRatio((interval.startMs + interval.endMs) / 2)));
    counts[Math.min(bins - 1, Math.floor(ratio * bins))] += 1;
  }
  return counts.flatMap((count, index) => count === 0 ? [] : [{ key: index, at: (index + 0.5) / bins, count }]);
};

export { requestDensity };

export interface ConversationRequest {
  /** An exact record instant: the window opens there. */
  ts: string;
  ms: number;
  no: number;
}

export interface ConversationRequests {
  /** turns: numbered like the request list and the trace. messages: no analysis yet. */
  basis: 'turns' | 'messages';
  requests: ConversationRequest[];
  /** Messages a person added inside a running request: drawn, never numbered. */
  extras: number[];
}

/**
 * One numbering for the conversation, the request list and the trace. A finished
 * analysis numbers requests by turn and the timeline's other messages are the
 * ones typed while a turn ran. Without one the timeline's messages are counted.
 */
const conversationRequests = (index: SessionTraceIndex | undefined, timeline: ConversationTimeline | undefined): ConversationRequests | undefined => {
  const usable = index && (index.state === 'ready' || index.state === 'stale') && index.requests_complete && index.requests.length > 0;
  if (usable) {
    const requests = index.requests.flatMap(request => {
      const ts = request.opened_at || request.started_at;
      const ms = Date.parse(ts);
      return Number.isFinite(ms) ? [{ ts, ms, no: request.request_no }] : [];
    });
    const openers = new Set(requests.map(request => request.ms));
    const extras = (timeline?.requests ?? []).filter(message => !openers.has(message.ms)).map(message => message.ms);
    return { basis: 'turns', requests, extras };
  }
  if (!timeline || timeline.requests.length === 0) return undefined;
  return { basis: 'messages', requests: timeline.requests.map((message, i) => ({ ts: message.ts, ms: message.ms, no: i + 1 })), extras: [] };
};

export { conversationRequests };

