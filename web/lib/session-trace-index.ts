import { apiFetch, buildUrl } from './api';
import type { SessionRecordScope } from './session-record-scope';

interface SessionTraceIndexEntry {
  request_no: number;
  turn_key: string;
  started_at: string;
  ended_at: string;
  /** When the message that opened the request was written; a Codex turn starts later. */
  opened_at?: string;
  preview: string;
  preview_state: string;
  status: string;
  api_requests: number;
  tools: number;
  subagents: number;
  abandoned: boolean;
}

interface SessionTraceIndex {
  session_id: string;
  state: string;
  request_total: number | null;
  requests: SessionTraceIndexEntry[];
  from?: string;
  to?: string;
  coverage: string;
  request_limit: number;
  requests_complete: boolean;
}

const fetchSessionTraceIndex = async (sessionId: string, scope?: SessionRecordScope, signal?: AbortSignal): Promise<SessionTraceIndex> => {
  const response = await apiFetch(buildUrl(`/api/sessions/${encodeURIComponent(sessionId)}/trace/index`, {
    profile_email: scope?.profileEmail ?? '', login_email: scope?.loginEmail ?? '', user_id: scope?.userID ?? '',
  }), { signal });
  if (!response.ok) throw new Error(`요청 흐름을 읽지 못했습니다. (${response.status})`);
  return response.json() as Promise<SessionTraceIndex>;
};

export { fetchSessionTraceIndex };
export type { SessionTraceIndex, SessionTraceIndexEntry };
