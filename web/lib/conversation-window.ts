import { apiFetch, buildUrl, SESSION_PAGE_SIZE } from './api';
import type { ConversationDestination } from './conversation-reading';
import type { SessionRecordScope } from './session-record-scope';
import type { SessionRecord } from './types';

interface ConversationWindow { records: SessionRecord[]; offset: number; revision?: number }
interface WindowRequest extends ConversationDestination { sessionId: string; scope?: SessionRecordScope; signal?: AbortSignal }
const fetchConversationWindow = async ({ sessionId, scope, signal, start, anchor, offset, fromTs, turnKey, direction, sourceFile }: WindowRequest): Promise<ConversationWindow> => {
  const response = await apiFetch(buildUrl('/api/sessions', {
    session_id: sessionId, lineage: '1', view: 'conversation', raw: '0', order: 'asc',
    limit: String(SESSION_PAGE_SIZE), offset: String(offset ?? 0), start: start ?? '', anchor: anchor ?? '', anchor_ts: fromTs ?? '', turn_key: turnKey ?? '', direction: direction ?? '', source_file: sourceFile ?? '',
    profile_email: scope?.profileEmail ?? '', login_email: scope?.loginEmail ?? '', user_id: scope?.userID ?? '',
  }), { signal });
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    const code = body && typeof body.error === 'string' ? body.error : '';
    throw Object.assign(new Error(response.status === 404 ? '선택한 메시지를 수집 기록에서 찾지 못했습니다.' : `대화를 불러오지 못했습니다. (${response.status})`), { code });
  }
  const resolvedOffset = Number(response.headers.get('X-Session-Start-Offset') ?? offset ?? 0);
  return { records: await response.json(), offset: Number.isSafeInteger(resolvedOffset) && resolvedOffset >= 0 ? resolvedOffset : 0 };
};

export { fetchConversationWindow };
export type { ConversationWindow };
