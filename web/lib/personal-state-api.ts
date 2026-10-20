import { apiFetch, buildUrl } from './api';
import type { SessionClassification } from './session-analysis-types';
import type { SessionOverview } from './types';

export interface PersonalSessionState {
  session_id: string;
  classification: SessionClassification;
  note: { text: string; updated_at: string } | null;
  revision: number;
  expires_at: string;
}

export interface PersonalNotePage {
  items: Array<{ session_id: string; short_id: string; title: string; first_line: string; updated_at: string }>;
  next_cursor?: string;
  truncated: boolean;
}

export const personalKeys = {
  session: (userId: number, sessionId: string) => ['personal-session', userId, sessionId] as const,
  global: (userId: number) => ['personal-global-note', userId] as const,
  notes: (userId: number) => ['personal-session-notes', userId] as const,
  counts: (userId: number) => ['personal-classification-counts', userId] as const,
};

export interface PersonalGlobalNote {
  text: string;
  updated_at: string;
  revision: number;
  expires_at: string;
}

export class PersonalStateError extends Error {
  constructor(public readonly status: number, public readonly current: PersonalSessionState | PersonalGlobalNote | null = null) {
    super(status === 409 ? '다른 곳에서 값이 바뀌었습니다. 최신값을 확인하고 다시 저장해 주세요.' : status === 404 ? '이 세션의 개인값은 소유권 또는 출처를 확인할 수 없어 사용할 수 없습니다.' : '개인값을 저장하거나 읽지 못했습니다.');
  }
}

const response = async <T>(result: Response): Promise<T> => {
  if (!result.ok) {
    const payload = await result.json().catch(() => null);
    throw new PersonalStateError(result.status, result.status === 409 ? payload?.current ?? null : null);
  }
  return result.json() as Promise<T>;
};

const sessionUrl = (sessionId: string) => buildUrl(`/api/sessions/${encodeURIComponent(sessionId)}/personal`);
const globalUrl = () => buildUrl('/api/me/note');
const jsonOptions = (method: 'PUT' | 'DELETE', body: object): RequestInit => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });

export const fetchPersonalSession = async (sessionId: string): Promise<PersonalSessionState> => response(await apiFetch(sessionUrl(sessionId)));
export const fetchPersonalNoteOverview = async (sessionId: string): Promise<SessionOverview> =>
  response(await apiFetch(buildUrl(`/api/sessions/${encodeURIComponent(sessionId)}/personal/overview`)));
export const savePersonalSession = async (sessionId: string, change: { expected_revision: number; note?: string; classification?: SessionClassification }): Promise<PersonalSessionState> =>
  response(await apiFetch(sessionUrl(sessionId), jsonOptions('PUT', change)));
export const deletePersonalSession = async (sessionId: string, expectedRevision: number): Promise<PersonalSessionState> =>
  response(await apiFetch(sessionUrl(sessionId), jsonOptions('DELETE', { expected_revision: expectedRevision })));
export const fetchPersonalNotePage = async (cursor?: string): Promise<PersonalNotePage> =>
  response(await apiFetch(buildUrl('/api/me/session-notes', { limit: '50', cursor: cursor ?? '' })));
export const fetchPersonalGlobalNote = async (): Promise<PersonalGlobalNote> => response(await apiFetch(globalUrl()));
export const savePersonalGlobalNote = async (text: string, expectedRevision: number): Promise<PersonalGlobalNote> =>
  response(await apiFetch(globalUrl(), jsonOptions('PUT', { text, expected_revision: expectedRevision })));
export const deletePersonalGlobalNote = async (expectedRevision: number): Promise<PersonalGlobalNote> =>
  response(await apiFetch(globalUrl(), jsonOptions('DELETE', { expected_revision: expectedRevision })));
