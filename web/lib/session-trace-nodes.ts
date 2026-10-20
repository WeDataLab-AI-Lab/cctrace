import { apiFetch, buildUrl } from './api';
import { TraceRequestError } from './session-trace-api';
import type { TraceScope } from './session-trace-types';
import type { TraceNodesRequest, TraceNodesResponse, TraceNodeDetail, TraceNodeDetailRequest } from './session-trace-node-types';

const nodeUrl = (sessionId: string, path: string, scope?: TraceScope, params?: Record<string, string>) =>
  buildUrl(`/api/sessions/${encodeURIComponent(sessionId)}/trace/nodes${path}`, {
    profile_email: scope?.profileEmail ?? '', login_email: scope?.loginEmail ?? '', user_id: scope?.userID ?? '', ...params,
  });
const readNode = async <T>(url: string, signal: AbortSignal): Promise<T> => {
  const response = await apiFetch(url, { signal });
  if (!response.ok) throw new TraceRequestError(response.status);
  return response.json();
};
const fetchTraceNodes = (sessionId: string, request: TraceNodesRequest, scope: TraceScope | undefined, signal: AbortSignal) =>
  readNode<TraceNodesResponse>(nodeUrl(sessionId, '', scope, {
    view: request.view ?? 'execution', parent_id: request.parentId ?? '', cursor: request.cursor ?? '', limit: String(request.limit ?? (request.view === 'flow' || request.view === 'timing' ? 20 : 100)),
    request_no: request.requestNo ? String(request.requestNo) : '', group_id: request.groupId ?? '', from: request.from ?? '', to: request.to ?? '', q: request.query ?? '',
  }), signal);
const fetchTraceNodeDetail = async (sessionId: string, nodeId: string, scope: TraceScope | undefined, signal: AbortSignal, options: TraceNodeDetailRequest = {}) => {
  const detail = await readNode<TraceNodeDetail>(nodeUrl(sessionId, `/${encodeURIComponent(nodeId)}`, scope, {
    request_parts: options.requestParts ?? '', input_cursor: options.inputCursor ?? '', input_limit: String(options.inputLimit ?? 20),
    compare_to: options.compareTo ?? '', source_ref: options.sourceRef ?? '',
  }), signal);
  if (!detail.request) return detail;
  return { ...detail, request: { ...detail.request, missing_refs: detail.request.missing_refs ?? [], source_refs: detail.request.source_refs ?? [] } };
};

export { fetchTraceNodes, fetchTraceNodeDetail };
