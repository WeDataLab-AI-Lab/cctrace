import { apiFetch, buildUrl } from './api';
import type { TraceContext, TraceContextRequest, TraceDemand, TraceEvents, TraceEventsRequest, TraceLease, TraceOverview, TraceScope } from './session-trace-types';

class TraceRequestError extends Error {
  constructor(readonly status: number) { super(`Trace request failed (${status})`); }
}
const traceUrl = (sessionId: string, path: string, scope?: TraceScope, params?: Record<string, string>) =>
  buildUrl(`/api/sessions/${encodeURIComponent(sessionId)}/trace/${path}`, {
    profile_email: scope?.profileEmail ?? '', login_email: scope?.loginEmail ?? '', user_id: scope?.userID ?? '', ...params,
  });
const readTrace = async <T>(url: string, signal: AbortSignal, init?: RequestInit): Promise<T> => {
  const response = await apiFetch(url, { ...init, signal });
  if (!response.ok) throw new TraceRequestError(response.status);
  return response.json();
};
const demandSessionTrace = (sessionId: string, demand: TraceDemand, scope: TraceScope | undefined, signal: AbortSignal) =>
  readTrace<TraceLease>(traceUrl(sessionId, 'demand', scope), signal, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(demand),
  });
const releaseSessionTrace = async (sessionId: string, leaseId: string, scope: TraceScope | undefined, signal: AbortSignal) => {
  const response = await apiFetch(traceUrl(sessionId, `demand/${encodeURIComponent(leaseId)}`, scope), { method: 'DELETE', signal, keepalive: true });
  if (!response.ok) throw new TraceRequestError(response.status);
};
const fetchTraceOverview = (sessionId: string, snapshotId: string, scope: TraceScope | undefined, signal: AbortSignal) =>
  readTrace<TraceOverview>(traceUrl(sessionId, 'overview', scope, { snapshot_id: snapshotId }), signal);
const fetchTraceEvents = (sessionId: string, request: TraceEventsRequest, scope: TraceScope | undefined, signal: AbortSignal) =>
  readTrace<TraceEvents>(traceUrl(sessionId, 'events', scope, { ...request, cursor: request.cursor ?? '', limit: '1000' }), signal);
const fetchTraceContext = (sessionId: string, request: TraceContextRequest, scope: TraceScope | undefined, signal: AbortSignal) =>
  readTrace<TraceContext>(traceUrl(sessionId, 'context', scope, { ...request, event_id: request.event_id ?? '', turn_key: request.turn_key ?? '', cursor: request.cursor ?? '' }), signal);

export { TraceRequestError, demandSessionTrace, releaseSessionTrace, fetchTraceOverview, fetchTraceEvents, fetchTraceContext };
