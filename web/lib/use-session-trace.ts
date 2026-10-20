'use client';

import { useEffect, useRef, useState } from 'react';
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query';
import { POLL_LIVE, POLL_NORMAL } from './query-config';
import { demandSessionTrace, fetchTraceContext, fetchTraceEvents, fetchTraceOverview, releaseSessionTrace, TraceRequestError } from './session-trace-api';
import type { TraceIdentity, TraceLease, TraceRange } from './session-trace-types';

const traceIdentityKey = (identity: TraceIdentity) => ['session-trace', identity.userId, identity.access,
  identity.scope?.profileEmail ?? '', identity.scope?.loginEmail ?? '', identity.scope?.userID ?? '', identity.sessionId] as const;
const usePageVisible = () => {
  const [visible, setVisible] = useState(() => typeof document === 'undefined' || document.visibilityState !== 'hidden');
  /** Hidden tabs relinquish demand and stop queries instead of leaving a live lease behind. */
  useEffect(() => {
    const change = () => setVisible(document.visibilityState !== 'hidden');
    document.addEventListener('visibilitychange', change);
    return () => document.removeEventListener('visibilitychange', change);
  }, []);
  return visible;
};

const useTraceLease = (identity: TraceIdentity, active: boolean, range?: TraceRange, restart = 0, running = false, paused = false) => {
  const { sessionId, userId, access } = identity;
  const profileEmail = identity.scope?.profileEmail, loginEmail = identity.scope?.loginEmail, userID = identity.scope?.userID;
  const from = range?.from, to = range?.to;
  const key = JSON.stringify([userId, access, sessionId, profileEmail, loginEmail, userID, from, to, restart]);
  const [result, setResult] = useState<{ key: string; lease?: TraceLease; error?: unknown } | null>(null);
  const pause = useRef(paused);

  /** Each mounted visible scope owns one renewable lease; cleanup also releases late acquisitions. */
  useEffect(() => {
    if (!active) return;
    const controller = new AbortController();
    const scope = profileEmail || loginEmail || userID ? { profileEmail, loginEmail, userID } : undefined;
    let cancelled = false, lease: TraceLease | undefined, timer: ReturnType<typeof setTimeout> | undefined;
    const release = (value: TraceLease) => {
      const signal = new AbortController().signal;
      void releaseSessionTrace(sessionId, value.lease_id, scope, signal).catch(() => undefined);
    };
    const renew = async () => {
      if (pause.current) { timer = setTimeout(renew, POLL_LIVE); return; }
      try {
        if (lease && Date.parse(lease.expires_at) <= Date.now()) { release(lease); lease = undefined; }
        const value = await demandSessionTrace(sessionId, { mode: from && to ? 'range' : 'overview',
          ...(from && to ? { from, to } : {}), ...(lease ? { lease_id: lease.lease_id } : {}) }, scope, controller.signal);
        if (cancelled) { release(value); return; }
        lease = value;
        if (!pause.current) setResult({ key, lease: value });
        if (value.state === 'ready' || value.state === 'unavailable') {
          release(value); lease = undefined;
          if (pause.current) timer = setTimeout(renew, POLL_LIVE);
          else if (value.state === 'ready' && running) timer = setTimeout(renew, POLL_NORMAL);
          return;
        }
      } catch (error) {
        if (cancelled) return;
        setResult({ key, error });
        return;
      }
      if (!cancelled) timer = setTimeout(renew, POLL_LIVE);
    };
    void renew();
    return () => {
      cancelled = true;
      controller.abort();
      clearTimeout(timer);
      if (lease) release(lease);
    };
  }, [key, active, sessionId, profileEmail, loginEmail, userID, from, to, running]);
  /** Pause renewals during a drag without replacing the snapshot beneath the captured pointer. */
  useEffect(() => { pause.current = paused; }, [paused]);

  return result?.key === key ? result : null;
};

interface UseSessionTraceOptions {
  identity: TraceIdentity; enabled?: boolean; dragging?: boolean; range?: TraceRange;
  eventId?: string; turnKey?: string; running?: boolean; contextEnabled?: boolean;
}
const useSessionTrace = ({ identity, enabled = true, dragging = false, range, eventId, turnKey, running = false, contextEnabled = true }: UseSessionTraceOptions) => {
  const visible = usePageVisible();
  const leaseActive = enabled && visible;
  const active = leaseActive && !dragging;
  const client = useQueryClient();
  const [restart, setRestart] = useState(0);
  const overviewLease = useTraceLease(identity, leaseActive, undefined, restart, running, dragging);
  const rangeLease = useTraceLease(identity, leaseActive && !!range, range, restart, running, dragging);
  const key = traceIdentityKey(identity), keyString = JSON.stringify(key);
  const overviewSnapshot = overviewLease?.lease?.snapshot_id ?? '';
  const rangeSnapshot = rangeLease?.lease?.snapshot_id ?? '';
  const snapshot = range ? rangeSnapshot : overviewSnapshot;
  const poll = (state?: string): number | false => !active ? false
    : state === 'building' || state === 'stale' ? POLL_LIVE : running ? POLL_NORMAL : false;
  const overview = useQuery({
    queryKey: [...key, 'overview', overviewSnapshot],
    queryFn: ({ signal }) => fetchTraceOverview(identity.sessionId, overviewSnapshot, identity.scope, signal),
    enabled: active && !!overviewSnapshot, retry: false,
    refetchInterval: query => poll(query.state.data?.state),
  });
  const sampleRange = overview.data?.state === 'ready' && overview.data.from && overview.data.to
    ? { from: overview.data.from, to: new Date(Date.parse(overview.data.to) + 1).toISOString() } : undefined;
  // One bounded page overlays measured spans on density; it neither enqueues a range nor drains cursors.
  const sample = useQuery({
    queryKey: [...key, 'sample', overviewSnapshot, sampleRange?.from ?? '', sampleRange?.to ?? ''],
    queryFn: ({ signal }) => fetchTraceEvents(identity.sessionId, { snapshot_id: overviewSnapshot,
      from: sampleRange?.from ?? '', to: sampleRange?.to ?? '' }, identity.scope, signal),
    enabled: active && !!sampleRange && overview.data?.events_count !== 0, retry: false,
    refetchInterval: query => poll(query.state.data?.state),
  });
  const requestTo = range && range.to === overview.data?.to ? new Date(Date.parse(range.to) + 1).toISOString() : range?.to;
  const events = useInfiniteQuery({
    queryKey: [...key, 'events', rangeSnapshot, range?.from ?? '', range?.to ?? ''],
    queryFn: ({ signal, pageParam }) => fetchTraceEvents(identity.sessionId, { snapshot_id: rangeSnapshot,
      from: range?.from ?? '', to: requestTo ?? '', cursor: pageParam }, identity.scope, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: page => page.next_cursor ?? undefined,
    enabled: active && !!range && !!rangeSnapshot && rangeLease?.lease?.state === 'ready', retry: false,
    refetchInterval: query => poll(query.state.data?.pages[0]?.state),
  });
  const contextReady = range ? rangeLease?.lease?.state === 'ready' || events.data?.pages[0]?.state === 'ready'
    : overview.data?.state === 'ready';
  const context = useInfiniteQuery({
    queryKey: [...key, 'context', snapshot, eventId ?? '', turnKey ?? ''],
    queryFn: ({ signal, pageParam }) => fetchTraceContext(identity.sessionId, { snapshot_id: snapshot,
      event_id: eventId || undefined, turn_key: eventId ? undefined : turnKey || undefined, cursor: pageParam }, identity.scope, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: page => page.next_cursor ?? undefined,
    enabled: active && contextEnabled && contextReady && !!snapshot && !!(eventId || turnKey), retry: false, staleTime: Infinity,
  });
  const failures = [overviewLease?.error, rangeLease?.error, overview.error, sample.error, events.error];
  const fenced = [...failures, context.error].find(error => error instanceof TraceRequestError && error.status === 409);
  const error = failures.find(error => error != null && !(error instanceof TraceRequestError && error.status === 409));
  const retry = () => { setRestart(value => value + 1); };

  /** On navigation, visibility changes and dragging, abort this screen's scoped reads. */
  useEffect(() => {
    if (!active) void client.cancelQueries({ queryKey: JSON.parse(keyString) });
    return () => { void client.cancelQueries({ queryKey: JSON.parse(keyString) }); };
  }, [active, keyString, client]);
  /** A collapsed conversation keeps its cache but no pending context read. */
  useEffect(() => {
    if (!contextEnabled) void client.cancelQueries({ queryKey: [...JSON.parse(keyString), 'context'] });
  }, [contextEnabled, keyString, client]);
  /** A revision/mapping fence is normal freshness: reacquire a snapshot with a bounded retry cadence. */
  useEffect(() => {
    if (!active || !fenced) return;
    const timer = setTimeout(() => setRestart(value => value + 1), POLL_LIVE);
    return () => clearTimeout(timer);
  }, [active, fenced]);

  return { overview, sample, events, context, active, error, updating: !!fenced,
    ready: !!overviewLease?.lease, rangeState: rangeLease?.lease?.state, retry };
};

export { useSessionTrace, usePageVisible, traceIdentityKey };
export type { UseSessionTraceOptions };
