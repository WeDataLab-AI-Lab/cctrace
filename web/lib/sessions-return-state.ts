import type { SessionOverview } from './types';
import type { SessionCategoryKey, PlanFilter, SessionSort } from './session-analysis-types';
import type { ProjectSelection } from './project-selection';
import type { LocalDate } from '@/components/sessions/calendar/time-zone';
import type { CalendarColorKey } from './session-calendar-types';
import type { ClassificationFilter } from '@/components/sessions/analysis/classification-tabs';
import type { ViewMode } from '@/components/sessions/mode-toggle';
import type { SessionsView } from '@/components/sessions/calendar/view-mode-switch';

interface SessionsReturnState {
  scopeKey?: string;
  calendarQueryKey?: readonly unknown[];
  returnUrl?: string; selectedId?: string | null; pinnedDefaultId?: string | null; searchTarget?: { sessionId: string; turnKey?: string; ownScope?: boolean } | null;
  search?: string; committedIds?: string[]; lastKnown?: Map<string, SessionOverview>; selectedProject?: ProjectSelection | null;
  analysisCategory?: SessionCategoryKey | null; analysisPlan?: PlanFilter | null; analysisSort?: SessionSort;
  selectedSource?: 'interactive' | 'headless' | 'all'; selectedClassification?: ClassificationFilter;
  mode?: ViewMode; scrollAnchor?: string; sessionsView?: SessionsView; calendarZoneName?: string;
  calendarWeek?: LocalDate | null; calendarZoom?: number; calendarColorKey?: CalendarColorKey; calendarUser?: string;
  scroll?: Record<string, number>;
  conversation?: ConversationReturnState;
}
interface ConversationReturnState { sessionId: string; mode: ViewMode; visible: number; promptsOnly: boolean; scrollTop: number; anchor?: string; readingAnchor?: string; anchorOffset?: number; openTools?: string[]; openAsides?: string[] }
// Tab memory only: no trace, context bodies or return positions are persisted to disk.
let saved: { userId: number; state: SessionsReturnState } | null = null;
const clearSessionsReturnState = () => { saved = null; };
const saveSessionsReturnState = (userId: number, state: SessionsReturnState) => { saved = { userId, state }; };
const readSessionsReturnState = (userId: number | null, scopeKey?: string): SessionsReturnState | null => {
  if (saved?.userId !== userId || scopeKey !== undefined && saved?.state.scopeKey !== scopeKey) { saved = null; return null; }
  return saved?.state ?? null;
};
const captureSessionsScroll = (): Record<string, number> => {
  const positions: Record<string, number> = {};
  for (const node of document.querySelectorAll<HTMLElement>('[data-session-return-scroll]')) {
    positions[node.dataset.sessionReturnScroll ?? ''] = node.scrollTop;
  }
  return positions;
};
const captureConversationState = (): ConversationReturnState | undefined => {
  const node = document.querySelector<HTMLElement>('[data-session-return-scroll="conversation"]');
  const sessionId = node?.dataset.sessionId;
  if (!node || !sessionId) return undefined;
  return { sessionId, mode: node.dataset.mode === 'individual' ? 'individual' : 'assembled', visible: Number(node.dataset.renderCount) || 10,
    promptsOnly: node.dataset.promptsOnly === 'true', scrollTop: node.scrollTop, anchor: node.dataset.jumpAnchor || undefined };
};
const restoreSessionsScroll = (positions?: Record<string, number>) => {
  for (const node of document.querySelectorAll<HTMLElement>('[data-session-return-scroll]')) {
    const top = positions?.[node.dataset.sessionReturnScroll ?? ''];
    if (top !== undefined) node.scrollTop = top;
  }
};

export { saveSessionsReturnState, readSessionsReturnState, clearSessionsReturnState, captureSessionsScroll, captureConversationState, restoreSessionsScroll };
export type { SessionsReturnState, ConversationReturnState };
