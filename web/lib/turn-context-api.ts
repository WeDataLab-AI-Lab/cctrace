import { apiFetch, buildUrl } from './api';
import type { TraceScope } from './session-trace-types';
import type { TurnContextRequest, TurnContextResponse } from './turn-context-types';

class TurnContextRequestError extends Error {
  constructor(readonly status: number, readonly code?: string) { super(`Turn context request failed (${status})`); }
}
const fetchTurnContext = async (sessionId: string, turnKey: string, request: TurnContextRequest, scope: TraceScope | undefined, signal: AbortSignal): Promise<TurnContextResponse> => {
  const url = buildUrl(`/api/sessions/${encodeURIComponent(sessionId)}/turns/${encodeURIComponent(turnKey)}/context`, {
    profile_email: scope?.profileEmail ?? '', login_email: scope?.loginEmail ?? '', user_id: scope?.userID ?? '',
    revision: request.revision ?? '', cursor: request.cursor ?? '', limit: String(Math.max(1, Math.min(100, request.limit ?? 20))),
  });
  const response = await apiFetch(url, { signal });
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new TurnContextRequestError(response.status, body?.error);
  }
  return response.json();
};

export { fetchTurnContext, TurnContextRequestError };
