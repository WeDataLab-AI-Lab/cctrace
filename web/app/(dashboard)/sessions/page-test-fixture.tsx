// @vitest-environment jsdom
import { createElement, useState, type ChangeEvent } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, waitFor as waitForWithOptions } from '@testing-library/react';
import { afterEach, beforeEach, vi } from 'vitest';
import SessionsPage from './page';
import { SessionLayoutProvider } from '@/components/common/session-layout-context';
import { fetchMaintenanceStatus, fetchProjects, fetchSessionCalendar, fetchSessionOverview, fetchSessionOverviewCategories, fetchSessionOverviewCount, fetchSessionRecordStatus, refreshSessionRecordRange, fetchUsers } from '@/lib/api';
import { serverCalendar } from '@/components/sessions/calendar/server-calendar.fixture';
import type { SessionCalendarResponse } from '@/lib/api';
import { fetchPersonalNoteOverview } from '@/lib/personal-state-api';
import { fetchCompleteSessionGroup, fetchSessionGroups, updateSessionGroupDismissals } from '@/lib/session-groups-api';
import type { SessionOverview } from '@/lib/types';
import type { SessionClassification } from '@/lib/session-analysis-types';
import { clearSessionsReturnState } from '@/lib/sessions-return-state';

// Bound condition polling to UI settlement rather than paying the default 50ms per assertion.
const waitFor: typeof waitForWithOptions = (callback, options) => waitForWithOptions(callback, { interval: 1, ...options });

const state = vi.hoisted(() => ({
  userId: 7, cctraceUserId: 'owner-uid', account: '', agent: '', accessMode: undefined as boolean | undefined, isAdmin: false, mode: 'assembled', analysisLoginEmail: '',
  since: '', until: '', urlSearch: '', replacedUrl: '', viewerSearchTarget: 'none',
  onModeChange: undefined as undefined | ((mode: 'raw' | 'assembled') => void),
  onOpenTraceTurn: undefined as undefined | ((turnKey: string) => void),
  onInspectRecord: undefined as undefined | ((target: { recordId: string; callId?: string }, resetFilters?: boolean) => void),
  onConversationRecord: undefined as undefined | ((target: { recordId: string }) => void),
  onSourceSelect: undefined as undefined | ((source: 'interactive' | 'headless' | 'all') => void),
  onClassificationSaved: undefined as undefined | ((id: string, classification: SessionClassification) => void),
  viewerRenders: [] as string[],
  viewerInstances: 0,
  onDeleted: undefined as undefined | ((id: string) => void),
  onOpenAnalysis: undefined as undefined | (() => void),
  onProjectDelete: undefined as undefined | ((project: { project_hash: string; project_name: string }) => void),
  onProjectDeleted: undefined as undefined | (() => void),
}));
vi.mock('next/navigation', () => ({
  useSearchParams: () => {
    const params = new URLSearchParams(state.urlSearch);
    return {
      get: (key: string) => params.get(key) ?? (key === 'since' ? state.since : key === 'until' ? state.until : null),
      toString: () => {
        const merged = new URLSearchParams(state.urlSearch);
        if (state.since && !merged.has('since')) merged.set('since', state.since);
        if (state.until && !merged.has('until')) merged.set('until', state.until);
        return merged.toString();
      },
    };
  },
  useRouter: () => ({ push: (url: string) => { state.replacedUrl = url; state.urlSearch = url.split('?')[1] ?? ''; }, replace: (url: string) => {
    state.replacedUrl = url;
    state.urlSearch = url.split('?')[1] ?? '';
  } }),
  usePathname: () => '/sessions',
}));
vi.mock('@/components/common/auth-context', () => ({ useAuth: () => ({ isAdmin: state.isAdmin, user: { id: state.userId, email: 'owner@example.test', cctrace_user_id: state.cctraceUserId, user_id_access_control: state.accessMode } }) }));
vi.mock('@/components/common/account-context', () => ({ useAccount: () => ({ selectedAccount: state.account }) }));
vi.mock('@/components/common/agent-context', () => ({ useAgent: () => ({ selectedAgent: state.agent }) }));
vi.mock('@/lib/api', async () => ({ fetchSessionOverview: vi.fn(), fetchSessionOverviewCount: vi.fn(), fetchSessionOverviewCategories: vi.fn(), fetchMaintenanceStatus: vi.fn(), fetchProjects: vi.fn(),
  fetchSessionCalendar: vi.fn(), fetchSessionRecordStatus: vi.fn(), refreshSessionRecordRange: vi.fn(), fetchUsers: vi.fn(),
  SessionCalendarRequestError: (await vi.importActual<typeof import('@/lib/api')>('@/lib/api')).SessionCalendarRequestError,
  SessionRecordRequestError: (await vi.importActual<typeof import('@/lib/api')>('@/lib/api')).SessionRecordRequestError }));
vi.mock('@/lib/personal-state-api', () => ({ fetchPersonalNoteOverview: vi.fn() }));
vi.mock('@/lib/session-groups-api', () => ({ fetchSessionGroups: vi.fn(), fetchCompleteSessionGroup: vi.fn(), updateSessionGroupDismissals: vi.fn() }));
vi.mock('@/components/common/filter-bar', () => ({ FilterBar: ({ search, onSearchChange, projects, selectedProject, onProjectChange, onProjectDelete }: {
  search: string;
  onSearchChange: (value: string) => void;
  projects?: { project_hash: string }[];
  selectedProject?: string;
  onProjectChange?: (hash: string) => void;
  onProjectDelete?: (project: { project_hash: string; project_name: string }) => void;
}) => {
  state.onProjectDelete = onProjectDelete;
  return createElement('div', null,
  createElement('input', { 'aria-label': '세션 검색', value: search, onChange: (event: ChangeEvent<HTMLInputElement>) => onSearchChange(event.currentTarget.value) }),
  createElement('span', { 'data-testid': 'select-first-project', onClick: () => {
    const project = projects?.[0];
    if (project) onProjectChange?.(project.project_hash);
  } }, selectedProject ?? ''),
); } }));
vi.mock('@/components/sessions/source-filter', () => ({
  SourceFilter: ({ selected, onSelect, hiddenSessionCount, hiddenCountLoading }: {
    selected: 'interactive' | 'headless' | 'all';
    onSelect: (value: 'interactive' | 'headless' | 'all') => void;
    hiddenSessionCount?: number | null;
    hiddenCountLoading?: boolean;
  }) => {
    state.onSourceSelect = onSelect;
    return createElement('div', null,
    createElement('span', { 'data-testid': 'source-selection' }, selected),
    createElement('span', { 'data-testid': 'hidden-session-count' }, hiddenSessionCount === undefined && hiddenCountLoading === undefined ? 'missing'
      : hiddenCountLoading ? 'loading' : hiddenSessionCount == null ? 'unknown' : `${hiddenSessionCount} hidden`),
    createElement('span', { 'data-testid': 'select-headless', onClick: () => onSelect('headless') }),
    createElement('span', { 'data-testid': 'select-all', onClick: () => onSelect('all') }),
  );
  },
}));
vi.mock('@/components/sessions/mode-toggle', () => ({ ModeToggle: () => null }));
vi.mock('@/components/sessions/project-banner', () => ({ ProjectBanner: () => null }));
vi.mock('@/components/sessions/delete-project-dialog', () => ({ DeleteProjectDialog: ({ onDeleted }: { onDeleted: () => void }) => {
  state.onProjectDeleted = onDeleted;
  return null;
} }));
vi.mock('@/components/sessions/session-viewer', () => ({ SessionViewer: ({ session, searchTarget, mode, onNavigate, onClassificationSaved, analysisLoginEmail, onModeChange, onDeleted, onOpenAnalysis, detailTab, expanded, listCollapsed, onToggleExpanded, onToggleList, onDetailTabChange, onClose, enabled, onInspectionTargetChange, onConversationTargetChange, scrollAnchor, onOpenTraceTurn }: {
  session: SessionOverview | null;
  searchTarget: { sessionId: string; turnKey?: string } | null;
  mode: 'raw' | 'assembled';
  onNavigate: (id: string, anchor?: string) => void;
  onClassificationSaved: (id: string, classification: SessionClassification) => void;
  analysisLoginEmail: string;
  onModeChange: (mode: 'raw' | 'assembled') => void;
  onDeleted: (id: string) => void;
  onOpenAnalysis?: () => void;
  onClose?: () => void; enabled?: boolean;
  onOpenTraceTurn?: (turnKey: string) => void;
  onInspectionTargetChange?: (target: { recordId: string; callId?: string }, resetFilters?: boolean) => void;
  onConversationTargetChange?: (target: { recordId: string }) => void; scrollAnchor?: string;
  detailTab?: 'conversation' | 'trace' | 'analysis'; expanded?: boolean; listCollapsed?: boolean;
  onToggleExpanded?: () => void; onToggleList?: () => void; onDetailTabChange?: (tab: 'conversation' | 'trace' | 'analysis') => void;
}) => {
  // One id per mounted viewer instance: a remount (which would drop a draft or tab) gets a new one.
  const [instance] = useState(() => ++state.viewerInstances);
  state.onOpenTraceTurn = onOpenTraceTurn;
  state.onInspectRecord = onInspectionTargetChange;
  state.onConversationRecord = onConversationTargetChange;
  state.onDeleted = onDeleted;
  state.onOpenAnalysis = onOpenAnalysis;
  state.onClassificationSaved = onClassificationSaved;
  state.onModeChange = onModeChange;
  state.mode = mode;
  state.analysisLoginEmail = analysisLoginEmail;
  state.viewerSearchTarget = searchTarget ? `${searchTarget.sessionId}:${searchTarget.turnKey ?? ''}` : 'none';
  state.viewerRenders.push(session?.session_id ?? 'none');
  return createElement('div', null,
    createElement('span', { 'data-testid': 'viewer-anchor' }, scrollAnchor ?? ''),
    createElement('span', { 'data-testid': 'viewer-enabled' }, String(enabled)),
    onClose && createElement('button', { onClick: onClose }, '대화창 닫기'),
    createElement('span', { 'data-testid': 'viewer-tab' }, detailTab),
    createElement('button', { onClick: onToggleExpanded }, expanded ? '상세 영역 복원' : '상세 영역 확대'),
    createElement('button', { onClick: onToggleList }, listCollapsed ? '세션 목록 펼치기' : '세션 목록 접기'),
    createElement('button', { onClick: () => onDetailTabChange?.('analysis') }, '상세 분석'),
    createElement('button', { onClick: () => onDetailTabChange?.('trace') }, '트레이스'),
    createElement('span', { 'data-testid': 'viewer-session' }, session?.session_id ?? 'none'),
    createElement('span', { 'data-testid': 'viewer-search-target' }, state.viewerSearchTarget),
    createElement('span', { 'data-testid': 'viewer-instance' }, String(instance)),
    createElement('button', { onClick: () => onNavigate('parent', 'fork-anchor') }, '부모로 이동'));
} }));

const row = (id: string): SessionOverview => ({
  session_id: id, profile_email: 'owner@example.test', model: '', agent: 'claude',
  start_time: '2026-10-01T00:00:00Z', end_time: '2026-10-01T01:00:00Z',
  input_tokens: 1, output_tokens: 2, cost_usd: 0.01, event_count: 1, has_sync: false,
  classification: 'working',
});
// Page navigation tests need observed input marks, while the full 2,000-input cap is covered by calendar-grid tests.
const pageCalendar = () => {
  const calendar = serverCalendar();
  return { ...calendar, sessions: calendar.sessions.map(session => ({ ...session, inputs: { ...session.inputs, items: session.inputs.items.slice(0, 4) } })) };
};
const buckets = { unsorted: 1, consulting: 2, working: 3, later: 4, done: 5 };
const mount = (maintenance?: Awaited<ReturnType<typeof fetchMaintenanceStatus>>, retry: boolean | number = false, withLayout = false, openFilters = true) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry, retryDelay: 0 } } });
  if (maintenance) client.setQueryData(['maintenance-status'], maintenance);
  const page = withLayout ? createElement(SessionLayoutProvider, { userId: state.userId }, createElement(SessionsPage)) : createElement(SessionsPage);
  const view = render(createElement(QueryClientProvider, { client }, page));
  // Existing filter scenarios start by opening the default-collapsed controls.
  const toggle = view.queryByRole('button', { name: '추가 필터 펼치기' });
  if (openFilters && toggle) fireEvent.click(toggle);
  return { client, view };
};

beforeEach(() => {
  // Calendar fixture belongs to this week; wall-clock drift must not move its bands out of view.
  vi.useFakeTimers({ toFake: ['Date'] });
  vi.setSystemTime(new Date('2026-10-07T12:00:00Z'));
  clearSessionsReturnState();
  localStorage.clear();
  state.userId = 7;
  state.cctraceUserId = 'owner-uid';
  state.since = '';
  state.until = '';
  state.urlSearch = '';
  state.replacedUrl = '';
  state.viewerSearchTarget = 'none';
  state.account = '';
  state.agent = '';
  state.accessMode = undefined;
  state.isAdmin = false;
  state.mode = 'assembled';
  state.analysisLoginEmail = '';
  state.onInspectRecord = undefined;
  state.onConversationRecord = undefined;
  state.onModeChange = undefined;
  state.onSourceSelect = undefined;
  state.onClassificationSaved = undefined;
  state.viewerRenders = [];
  state.onDeleted = undefined;
  state.onProjectDelete = undefined;
  state.onProjectDeleted = undefined;
  vi.mocked(fetchSessionCalendar).mockReset().mockImplementation(async ({ from, to }) =>
    ({ ...pageCalendar(), from, to }) as unknown as SessionCalendarResponse);
  vi.mocked(fetchSessionRecordStatus).mockReset();
  vi.mocked(refreshSessionRecordRange).mockReset().mockResolvedValue(undefined);
  vi.mocked(fetchUsers).mockReset().mockResolvedValue([
    { user_id: 'other-uid', profile_email: 'other@example.test', login_emails: [], total_cost: 0 },
    { user_id: '', profile_email: 'legacy@example.test', login_emails: [], total_cost: 0 },
  ]);
  vi.mocked(fetchMaintenanceStatus).mockReset().mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: false } });
  vi.mocked(fetchSessionOverviewCategories).mockReset().mockResolvedValue({ ended: 11, running: 2 });
  vi.mocked(fetchProjects).mockReset().mockResolvedValue([]);
  vi.mocked(fetchSessionOverview).mockReset().mockImplementation(async (params) => params?.classification === 'working' ? [row('working-row')] : [row('working-row'), row('other-row')]);
  vi.mocked(fetchSessionOverviewCount).mockReset().mockImplementation(async (params) => ({
    count: params?.classification ? 1 : 9, unattributed: 0, classification_counts: buckets,
  }));
  vi.mocked(fetchPersonalNoteOverview).mockReset().mockRejectedValue(new Error('parent has no personal note'));
  vi.mocked(fetchSessionGroups).mockReset().mockResolvedValue({
    scope: 'filtered_total', grouping_version: 1, truncated: false, overview_total: 9,
    running: [], ended: [], counts: { running: 0, ended: 0, ended_groups: 0, running_by: {} },
  });
  vi.mocked(updateSessionGroupDismissals).mockReset().mockResolvedValue(undefined);
  vi.mocked(fetchCompleteSessionGroup).mockReset().mockResolvedValue([
    { session_id: 'working-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false },
    { session_id: 'other-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false },
  ]);
  vi.stubGlobal('IntersectionObserver', class { observe() {} disconnect() {} });
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} });
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.useRealTimers(); clearSessionsReturnState(); });


const PageUnderTest = SessionsPage;
export { state, row, buckets, mount, pageCalendar, waitFor, PageUnderTest as SessionsPage };
