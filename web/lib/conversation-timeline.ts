import { apiFetch, buildUrl } from './api';
import type { SessionRecordScope } from './session-record-scope';

interface ConversationTimelineWire {
  base: string;
  requests: string[];
  replies: number[];
  spans: [number, number, number][];
}

/** The whole session's markers, without bodies; times are epoch milliseconds. */
interface ConversationTimeline {
  /** Exact record instants, kept as sent so a click can open the window there. */
  requests: { ts: string; ms: number }[];
  replies: number[];
  spans: { startMs: number; endMs: number; aside: boolean; error: boolean }[];
}

const decodeConversationTimeline = (wire: ConversationTimelineWire): ConversationTimeline => {
  const base = Date.parse(wire.base);
  if (!Number.isFinite(base)) return { requests: [], replies: [], spans: [] };
  return {
    requests: (wire.requests ?? []).flatMap(ts => { const ms = Date.parse(ts); return Number.isFinite(ms) ? [{ ts, ms }] : []; }),
    replies: (wire.replies ?? []).map(offset => base + offset),
    spans: (wire.spans ?? []).map(([start, duration, flags]) => ({ startMs: base + start, endMs: base + start + duration, aside: (flags & 1) !== 0, error: (flags & 2) !== 0 })),
  };
};

const fetchConversationTimeline = async (sessionId: string, scope?: SessionRecordScope, signal?: AbortSignal): Promise<ConversationTimeline> => {
  const response = await apiFetch(buildUrl(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/timeline`, {
    profile_email: scope?.profileEmail ?? '', login_email: scope?.loginEmail ?? '', user_id: scope?.userID ?? '',
  }), { signal });
  if (!response.ok) throw new Error(`대화 타임라인을 읽지 못했습니다. (${response.status})`);
  return decodeConversationTimeline(await response.json() as ConversationTimelineWire);
};

export { decodeConversationTimeline, fetchConversationTimeline };
export type { ConversationTimeline };

