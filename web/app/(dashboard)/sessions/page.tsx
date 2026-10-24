'use client';

import { useEffect, useRef, useState } from 'react';
import { ChevronDown } from 'lucide-react';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import { useQuery, useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { POLL_LIVE, POLL_NORMAL } from '@/lib/query-config';
import { fetchMaintenanceStatus, fetchSessionCalendar, fetchSessionOverview, fetchSessionOverviewCategories, fetchSessionOverviewCount, fetchSessionRecordStatus, refreshSessionRecordRange, fetchProjects, fetchUsers, SessionCalendarRequestError, SessionRecordRequestError } from '@/lib/api';
import type { SessionOverviewCount } from '@/lib/api';
import { projectIdentityKey, projectIdentityLabel } from '@/lib/project-identity';
import { deepLinkSelection, selectionProjectHashes, type ProjectSelection } from '@/lib/project-selection';
import { useAccount } from '@/components/common/account-context';
import { useAgent } from '@/components/common/agent-context';
import { FilterBar } from '@/components/common/filter-bar';
import type { SessionOverview, Project } from '@/lib/types';
import type { PlanFilter, SessionCategoryCounts, SessionCategoryKey, SessionClassification, SessionSort } from '@/lib/session-analysis-types';
import type { CalendarColorKey } from '@/lib/session-calendar-types';
import { SessionsCalendarPanel } from '@/components/sessions/calendar/sessions-calendar-panel';
import { ViewModeSwitch, type SessionsView } from '@/components/sessions/calendar/view-mode-switch';
import { calendarTimeZone, type LocalDate } from '@/components/sessions/calendar/time-zone';
import { todayWeekStart, weekQueryRange } from '@/components/sessions/calendar/week';
import { DEFAULT_PX_PER_HOUR } from '@/components/sessions/calendar/zoom';
import { CategoryCountChips, PlanFilterToggle, SortControl } from '@/components/sessions/analysis/list-controls';
import { SourceFilter, type SourceValue } from '@/components/sessions/source-filter';
import type { ViewMode } from '@/components/sessions/mode-toggle';
import { ProjectBanner } from '@/components/sessions/project-banner';
import { SessionList } from '@/components/sessions/session-list';
import { ClassificationTabs, type ClassificationFilter } from '@/components/sessions/analysis/classification-tabs';
import { CLASSIFICATION_LABEL } from '@/components/sessions/analysis/list-model';
import { CATEGORY_META, SORT_META } from '@/components/sessions/analysis/labels';
import { SessionViewer, type SessionDetailTab } from '@/components/sessions/session-viewer';
import { useSessionLayout } from '@/components/common/session-layout-context';
import { useNarrowSession } from '@/lib/use-narrow-session';
import { cn } from '@/lib/utils';
import { LocalSessionsPanel } from '@/components/sessions/local/local-sessions-panel';
import { DeleteProjectDialog } from '@/components/sessions/delete-project-dialog';
import { useAuth } from '@/components/common/auth-context';
import { reconcileList, commitLiveOrder } from '@/components/sessions/live-list';
import { fetchPersonalNoteOverview } from '@/lib/personal-state-api';
import { createPersonalNoteNavigator } from '@/lib/personal-note-navigation';
import { fetchCompleteSessionGroup, fetchSessionGroups, updateSessionGroupDismissals, type SessionGroupMember, type SessionGroupScope, type SessionGroups } from '@/lib/session-groups-api';
import { createSerialQueryRunner, runSerialSessionRead } from '@/lib/serial-query';
import type { InspectionTarget } from '@/lib/session-inspection';
import { captureConversationState, captureSessionsScroll, readSessionsReturnState, restoreSessionsScroll, saveSessionsReturnState } from '@/lib/sessions-return-state';

// Basic totals must never wait for analysis or group reads from another view.
const runSerialBasicCount = createSerialQueryRunner();

const sessionId = (s: SessionOverview): string => s.session_id;
const sessionTraceStateParams = ['turn_key', 'event_id', 'trace_from', 'trace_to', 'trace_group', 'trace_view', 'trace_pane', 'trace_expanded', 'trace_usage_expanded', 'trace_cursor', 'trace_branch_cursors', 'trace_q', 'trace_request_no', 'trace_compare', 'trace_locate'] as const;
const clearSessionTraceState = (params: URLSearchParams) => {
  for (const key of sessionTraceStateParams) params.delete(key);
};
const browserTimeZone = (): string => Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
const recordStatusErrorText = (error: unknown): string => {
  if (!(error instanceof SessionRecordRequestError)) return '세션 기록 생성 상태를 읽지 못했습니다.';
  if (error.code === 'consent_required') return '현재 런타임의 세션 기록 동의가 필요합니다.';
  if (error.code === 'records_disabled') return '관리자가 세션 기록 생성을 껐습니다.';
  if (error.code === 'runtime_unconfigured' || error.code === 'runtime_unavailable') return '선택된 AI 런타임을 사용할 수 없습니다.';
  return `세션 기록 상태 API가 ${error.code} 응답을 보냈습니다.`;
};

interface SearchNavigationTarget {
  sessionId: string;
  turnKey?: string;
  ownScope?: boolean;
}

interface BasicClassificationTabsProps {
  queryKey: readonly unknown[];
  params: Parameters<typeof fetchSessionOverviewCount>[0];
  enabled: boolean;
  value: ClassificationFilter;
  onChange: (value: ClassificationFilter) => void;
}

// Mounted only while the basic filter panel is visible; unmounting cancels its read.
const BasicClassificationTabs = ({ queryKey, params, enabled, value, onChange }: BasicClassificationTabsProps) => {
  const { data, isError } = useQuery<SessionOverviewCount>({
    queryKey,
    queryFn: ({ signal }) => runSerialSessionRead(() => fetchSessionOverviewCount(params, signal), signal),
    enabled,
    staleTime: 30_000,
    refetchInterval: POLL_NORMAL,
  });
  return <>
    {isError && <p role="alert" className="mb-2 px-1 text-xs text-danger">개인 분류 개수를 읽지 못했습니다.</p>}
    <ClassificationTabs value={value} onChange={onChange} panelId="sessions-list-panel"
      counts={isError ? undefined : data?.classification_counts} total={isError ? undefined : data?.count}
      unavailable={!isError && !!data && !data.classification_counts} />
  </>;
};

export default function SessionsPage() {
  const qc = useQueryClient();
  const router = useRouter();
  const pathname = usePathname();
  const { isAdmin, user } = useAuth();
  const { expanded, setExpanded, listCollapsed, toggleList, toggleDetailMenu } = useSessionLayout();
  const narrow = useNarrowSession();
  const [narrowListOpen, setNarrowListOpen] = useState(false);
  const { selectedAccount: pickedAccount } = useAccount();
  const { selectedAgent } = useAgent();
  const selectedAccount = user?.role === 'user' ? '' : pickedAccount;
  const returnScope = JSON.stringify([isAdmin ? 'admin' : 'owner', selectedAccount, selectedAgent]);
  const [returnState] = useState(() => readSessionsReturnState(user?.id ?? null, returnScope));
  const [search, setSearch] = useState(returnState?.search ?? '');
  const [showLocal, setShowLocal] = useState(false);
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [analysisCategory, setAnalysisCategory] = useState<SessionCategoryKey | null>(returnState?.analysisCategory ?? null);
  const [analysisPlan, setAnalysisPlan] = useState<PlanFilter | null>(returnState?.analysisPlan ?? null);
  const [analysisSort, setAnalysisSort] = useState<SessionSort>(returnState?.analysisSort ?? 'activity');
  const { data: maintenance } = useQuery({
    queryKey: ['maintenance-status'], queryFn: fetchMaintenanceStatus, staleTime: 60_000,
  });
  const analysisEnabled = maintenance?.session_analysis?.enabled === true;
  const sessionRecordsEnabled = maintenance?.session_records?.enabled === true;
  const [sessionsView, setSessionsView] = useState<SessionsView>(returnState?.sessionsView ?? 'list');
  const analysisView = analysisEnabled && sessionsView === 'analysis';
  const effectiveCategory = analysisView && !isAdmin ? analysisCategory : null;
  const effectivePlan = analysisView ? analysisPlan : null;
  const effectiveSort = analysisView ? analysisSort : 'activity';
  const analysisScope = analysisView ? { analysis: true, category: effectiveCategory ?? undefined,
    plan: effectivePlan ?? undefined, sort: effectiveSort, query: search.trim() } : {};
  const overviewSearch = analysisView ? search.trim() : '';
  // Calendar controls live here so switching views preserves the adjacent viewer.
  const [calendarZoneName, setCalendarZoneName] = useState(() => returnState?.calendarZoneName ?? browserTimeZone());
  const [calendarWeek, setCalendarWeek] = useState<LocalDate | null>(returnState?.calendarWeek ?? null);
  const [calendarZoom, setCalendarZoom] = useState(returnState?.calendarZoom ?? DEFAULT_PX_PER_HOUR);
  const [calendarColorKey, setCalendarColorKey] = useState<CalendarColorKey>(returnState?.calendarColorKey ?? 'project');
  const [calendarNowMs, setCalendarNowMs] = useState(() => Date.now());
  const [calendarUser, setCalendarUser] = useState(returnState?.calendarUser ?? '');
  const calendarView = analysisEnabled && sessionsView === 'calendar';
  // The selection is applied only in the identity the server's access mode reads; without a
  // reported mode the calendar stays on the caller's own sessions.
  const userScope = user?.user_id_access_control === true ? 'user_id' : user?.user_id_access_control === false ? 'profile_email' : undefined;
  const appliedCalendarUser = isAdmin && (userScope === 'user_id' && calendarUser.startsWith('uid:') ||
    userScope === 'profile_email' && calendarUser.startsWith('email:')) ? calendarUser : '';
  // Selection is an id, not a snapshot object: the polled list is the single source of the
  // values, so the viewer header refreshes with the list card instead of freezing at the
  // moment of the click.
  const [selectedId, setSelectedId] = useState<string | null>(returnState?.selectedId ?? null);
  const [searchTarget, setSearchTarget] = useState<SearchNavigationTarget | null>(returnState?.searchTarget ?? null);
  // Scoped to the moment after a delete. isFetching alone would also be true on the
  // 30s poll, so the list would blank out every cycle -- a spinner has to mean
  // "something you did is settling", not "a timer fired".
  const [reloadingAfterDelete, setReloadingAfterDelete] = useState(false);
  const [projectToDelete, setProjectToDelete] = useState<{
    project_hash: string;
    project_name: string;
    member_hashes?: string[];
  } | null>(null);
  // The default selection, fixed once per filter scope. Not derived from the list head:
  // deriving it would re-point the viewer at whatever session polling floats to the top.
  const [pinnedDefaultId, setPinnedDefaultId] = useState<string | null>(returnState?.pinnedDefaultId ?? null);
  // Last value seen for every session id. State, not a ref: it is read during render, and
  // react-hooks/refs (rightly) bans reading a ref there.
  const [lastKnown, setLastKnown] = useState<Map<string, SessionOverview>>(() => returnState?.lastKnown ?? new Map());
  // The display order the user is looking at. Frozen against polling; values are not.
  const [basicCommittedIds, setBasicCommittedIds] = useState<string[]>(returnState?.sessionsView === 'analysis' ? [] : returnState?.committedIds ?? []);
  const [analysisCommittedIds, setAnalysisCommittedIds] = useState<string[]>(returnState?.sessionsView === 'analysis' ? returnState.committedIds ?? [] : []);
  const committedIds = analysisView ? analysisCommittedIds : basicCommittedIds;
  const setCommittedIds = analysisView ? setAnalysisCommittedIds : setBasicCommittedIds;
  // The server pins a role=user session list to the caller's own user_id and drops
  // login_email (#811), and the header says so instead of showing chips. A selection made
  // on another screen is still in AccountContext; sending it here would narrow the project
  // chart (which applies login_email as sent) under a header claiming every account shows.
  // Keyed on role === 'user' like the header: the server allows only admin|user, so a missing
  // user (the dashboard layout does not mount this page before auth settles) is never read as
  // restricted.

  const [selectedProject, setSelectedProject] = useState<ProjectSelection | null>(returnState?.selectedProject ?? null);
  // Arriving from a link elsewhere (the weekly report's project list) with
  // ?project_hash=... . The hash is what those views carry; the selector here keys on
  // project identity (repository + subpath), so the registry does the translation --
  // several hashes can answer to one identity, and picking any of them lands on the
  // same filter.
  const searchParams = useSearchParams();
  const projectHashParam = searchParams.get('project_hash') ?? '';
  // Work Segments link straight to the conversation they describe: ?session_id
  // says which one, ?from says where in it. The segment knows a time, not a
  // message, so the detail view resolves the instant to the first item at or
  // after it (#434).
  const sessionIdParam = searchParams.get('session_id') ?? '';
  const fromTsParam = searchParams.get('from') ?? '';
  const sessionSearchParam = searchParams.get('session_search') === '1';
  const turnKeyParam = searchParams.get('turn_key') ?? '';
  const detailParam = searchParams.get('detail');
  const expandedParam = searchParams.get('expanded') === '1';
  const explicitDetailScope = !sessionSearchParam && detailParam !== null;
  const sessionNavigationKey = sessionIdParam ? [sessionIdParam, sessionSearchParam ? 'search' : explicitDetailScope ? 'detail' : 'list', explicitDetailScope ? '' : turnKeyParam].join('|') : '';
  const appliedSessionParam = useRef(returnState ? sessionNavigationKey : '');
  // URL-scoped date filters share one exact range across the list, totals, category counts, and P7 groups.
  const overviewSince = searchParams.get('since') ?? undefined;
  const overviewUntil = searchParams.get('until') ?? undefined;
  const appliedProjectParam = useRef(!!returnState?.selectedProject);
  // The header's Agent pills are the one place this is chosen. The page used to keep its
  // own useState and its own row of identical-looking pills, so the header control simply
  // did nothing here — two selectors, one of them inert, and no way to tell which.
  const [selectedSource, setSelectedSource] = useState<SourceValue>(returnState?.selectedSource ?? 'interactive'); // hides headless by default
  const [selectedClassification, setSelectedClassification] = useState<ClassificationFilter>(returnState?.selectedClassification ?? 'all');
  const [navNotice, setNavNotice] = useState(''); // origin session id that couldn't be found
  const [personalNavNotice, setPersonalNavNotice] = useState('');
  const [groupWriteError, setGroupWriteError] = useState(false);
  const [personalNavigator] = useState(() => createPersonalNoteNavigator(
    fetchPersonalNoteOverview,
    (overview) => {
      setLastKnown((previous) => new Map(previous).set(overview.session_id, overview));
      setSelectedId(overview.session_id);
      setScrollAnchor(undefined);
      setNavNotice('');
      setPersonalNavNotice('');
    },
    setPersonalNavNotice,
  ));
  const [scrollAnchor, setScrollAnchor] = useState<string | undefined>(returnState?.scrollAnchor); // fork point to scroll to after navigation
  // Global view mode: assembled folds branch/clear children into their root and
  // shows the merged thread; raw lists every session/file separately.
  const mode: ViewMode = 'assembled';

  const overviewSource = selectedSource === 'all' ? '' : selectedSource;
  const overviewCategoriesSource = selectedSource;
  const classification = selectedClassification === 'all' ? undefined : selectedClassification;
  const assembled = mode === 'assembled';

  // The picker is scoped exactly like the list below it — every filter the list sends
  // server-side, this sends too. Left unscoped it offered projects whose every session
  // the list then filtered away, so picking one opened an empty list. The filters have
  // to be in the key too, or the picker keeps the answer to the previous scope.
  const { data: projects } = useQuery<Project[]>({
    queryKey: ['projects', overviewSource, selectedAgent, selectedAccount, assembled],
    queryFn: () => fetchProjects({
      source: overviewSource,
      agent: selectedAgent,
      loginEmail: selectedAccount || undefined,
      assembled,
    }),
    staleTime: 60_000,
    refetchInterval: POLL_NORMAL,
  });

  // A deep link has to resolve against every project, not the ones the current scope
  // admits. weekly/ and the task-segment modal link here with a project_hash that can
  // belong to a headless-only project, and the default source filter is 'interactive':
  // resolving against the scoped registry above dropped the selection on the floor, and
  // the one-shot flag below meant there was no second attempt. Only fetched when a
  // project_hash is actually in the URL.
  const { data: linkProjects } = useQuery<Project[]>({
    queryKey: ['projects', 'deep-link'],
    queryFn: () => fetchProjects(),
    enabled: !!projectHashParam,
    staleTime: 60_000,
  });

  // A project identity maps to one or more per-machine project_hash values; resolve the
  // selected identity to its hashes so the server can filter by them. Client-side project
  // filtering would only see the loaded pages (missing a project's older sessions) and the
  // count wouldn't match the paged list. Search stays client-side (below).
  // See selectionProjectHashes for why a live miss falls back instead of resolving empty.
  const projectHashes = selectionProjectHashes(selectedProject, projects);
  const projectKey = projectHashes.join(',');
  const groupScope: SessionGroupScope = {
    source: overviewSource, projectHashes: selectedProject ? projectHashes : undefined,
    agent: selectedAgent, assembled, classification,
    loginEmail: isAdmin ? selectedAccount : undefined,
    since: overviewSince, until: overviewUntil, includeAnalysis: analysisView,
    query: overviewSearch || undefined, plan: effectivePlan ?? undefined,
    category: effectiveCategory ?? undefined, sort: analysisView ? effectiveSort : undefined,
  };
  const groupQueryKey = ['session-groups', user?.id ?? null, user?.cctrace_user_id ?? '', user?.user_id_access_control ?? null,
    selectedAccount, selectedSource, projectKey, selectedAgent, assembled, selectedClassification,
    overviewSince ?? '', overviewUntil ?? '', analysisView, overviewSearch, effectiveCategory ?? '', effectivePlan ?? '', effectiveSort];
  const personalGroupIdentityAvailable = !!user && (user.user_id_access_control === false ? !!user.email.trim() : !!user.cctrace_user_id?.trim());
  const groupQueryEnabled = personalGroupIdentityAvailable && (!selectedProject || projectHashes.length > 0);
  const groupScopeIdentity = JSON.stringify(groupQueryKey);
  const currentGroupScope = useRef(groupScopeIdentity);
  const currentGroupUser = useRef(user?.id ?? null);
  const activeGroupScope = useRef(groupScopeIdentity);
  if (activeGroupScope.current !== groupScopeIdentity) {
    currentGroupScope.current = groupScopeIdentity;
    currentGroupUser.current = user?.id ?? null;
    activeGroupScope.current = groupScopeIdentity;
  }
  useEffect(() => { setGroupWriteError(false); }, [groupScopeIdentity]);
  const groupMutation = useMutation({
    mutationFn: async ({ items, dismiss, group, allMembers, scopeIdentity, scope, queryKey, version, userId }: {
      items: readonly SessionGroupMember[]; dismiss: boolean; group?: SessionGroups['ended'][number]; allMembers: boolean;
      scopeIdentity: string; scope: SessionGroupScope; queryKey: readonly (string | number | boolean | null)[]; version: number; userId: number | null;
    }) => {
      const isCurrent = () => currentGroupScope.current === scopeIdentity && currentGroupUser.current === userId && userId !== null;
      if (!isCurrent()) return;
      const members = allMembers && group
        ? await fetchCompleteSessionGroup(scope, group.key, version, group.total)
        : items;
      if (!isCurrent()) return;
      const pending = members.filter((item) => item.dismissed !== dismiss);
      let acknowledged = 0;
      for (let offset = 0; offset < pending.length; offset += 100) {
        if (!isCurrent()) break;
        const batch = pending.slice(offset, offset + 100);
        await updateSessionGroupDismissals(scope, version,
          batch.map(({ session_id, ended_at }) => ({ session_id, ended_at })), dismiss, group?.key);
        acknowledged += batch.length;
      }
      if (acknowledged > 0) await qc.invalidateQueries({ queryKey: [...queryKey], exact: true });
    },
    onSuccess: (_data, variables) => {
      if (currentGroupScope.current === variables.scopeIdentity && currentGroupUser.current === variables.userId) setGroupWriteError(false);
    },
    onError: (_error, variables) => {
      if (currentGroupScope.current === variables.scopeIdentity && currentGroupUser.current === variables.userId) setGroupWriteError(true);
      // A previous batch may have succeeded before a later batch failed.
      void qc.invalidateQueries({ queryKey: [...variables.queryKey], exact: true });
    },
  });

  // Infinite scroll: page the overview by offset. source, project, and agent are filtered
  // server-side so the LIMIT and count reflect exactly what will be shown (a client-side
  // filter would only see the loaded pages). Only the analysis view searches server-side.
  const OVERVIEW_PAGE = 100;
  // The server-side scope of the list. A change here means a different list entirely, which
  // is the one moment the default selection may move.
  // An administrator's calendar user is part of the selection scope: a session chosen for one
  // user is hidden from the first render after switching to another.
  const selectionScopeKey = [user?.id ?? '', selectedAccount, selectedSource, projectKey, selectedAgent, selectedClassification, appliedCalendarUser, overviewSince ?? '', overviewUntil ?? ''].join('|');
  const [calendarSelectionScope, setCalendarSelectionScope] = useState<string | null>(() =>
    returnState?.sessionsView === 'calendar' && returnState.selectedId ? selectionScopeKey : null);
  const resetKey = [selectionScopeKey, assembled, analysisView, overviewSearch, effectiveCategory, effectivePlan, effectiveSort].join('|');
  const basicScopeKey = [selectionScopeKey, assembled].join('|');
  const basicScopeRef = useRef(basicScopeKey);
  const basicScopeChanged = basicScopeRef.current !== basicScopeKey;
  const resetKeyRef = useRef(resetKey);
  const selectionScopeRef = useRef(selectionScopeKey);
  const selectionScopeChanged = selectionScopeRef.current !== selectionScopeKey;
  const listScopeChanged = resetKeyRef.current !== resetKey;
  useEffect(() => {
    personalNavigator.cancel();
    return () => personalNavigator.cancel();
  }, [personalNavigator, user?.id]);
  const overviewBaseQueryKey = ['session-overview', user?.id ?? null, selectedAccount, selectedSource, projectKey, selectedAgent,
    assembled, selectedClassification, overviewSince ?? '', overviewUntil ?? ''] as const;
  const overviewQueryKey = analysisView
    ? [...overviewBaseQueryKey, true, overviewSearch, effectiveCategory, effectivePlan, effectiveSort]
    : overviewBaseQueryKey;
  const overviewRequestScope = {
    loginEmail: selectedAccount || undefined,
    limit: OVERVIEW_PAGE,
    assembled,
    source: overviewSource,
    since: overviewSince,
    until: overviewUntil,
    projectHashes,
    agent: selectedAgent,
    classification,
  };
  const { data, isLoading, isFetching, isError: overviewError, dataUpdatedAt: overviewReadAt,
    fetchNextPage, hasNextPage, isFetchingNextPage } = useInfiniteQuery({
    queryKey: overviewQueryKey,
    queryFn: ({ pageParam, signal }) => fetchSessionOverview({
      ...overviewRequestScope, ...analysisScope, offset: pageParam,
    }, signal),
    initialPageParam: 0,
    refetchInterval: POLL_NORMAL,
    getNextPageParam: (lastPage: SessionOverview[], allPages: SessionOverview[][]) =>
      lastPage.length === OVERVIEW_PAGE ? allPages.length * OVERVIEW_PAGE : undefined,
    gcTime: Infinity,
  });
  const initialOverviewLoaded = !!data?.pages.length;
  const runCountRead = analysisView ? runSerialSessionRead : runSerialBasicCount;
  const { data: sessionGroups, isError: groupReadError, error: groupReadFailure, isFetching: groupsFetching } = useQuery({
    queryKey: groupQueryKey,
    queryFn: ({ signal }) => runSerialSessionRead(() => fetchSessionGroups(groupScope), signal),
    enabled: groupQueryEnabled && initialOverviewLoaded,
    refetchInterval: POLL_NORMAL,
  });
  const groupReadStatus = groupReadFailure !== null && typeof groupReadFailure === 'object' && 'status' in groupReadFailure
    ? groupReadFailure.status : undefined;
  const groupReadUnavailable = !groupQueryEnabled || groupReadError && (groupReadStatus === 403 || groupReadStatus === 404);

  // Total session count for the current scope+source+project+agent, so the list shows "N / total".
  const { data: countData, isError: countError, isLoading: countLoading } = useQuery<SessionOverviewCount>({
    queryKey: ['session-overview-count', user?.id ?? null, selectedAccount, selectedSource, projectKey, selectedAgent, assembled, selectedClassification, overviewSince ?? '', overviewUntil ?? '',
      ...(analysisView ? [true, overviewSearch, effectiveCategory, effectivePlan, effectiveSort] : [])],
    queryFn: ({ signal }) => runCountRead(() => fetchSessionOverviewCount({
      ...analysisScope,
      ...(!analysisView ? { includeClassificationCounts: false } : {}),
      loginEmail: selectedAccount || undefined,
      assembled,
      source: overviewSource,
      since: overviewSince,
      until: overviewUntil,
      projectHashes,
      agent: selectedAgent,
      classification,
    }, signal), signal),
    enabled: initialOverviewLoaded,
    staleTime: 30_000,
    refetchInterval: POLL_NORMAL,
  });
  // The all-classification tab has its own count; its category buckets describe that
  // all-classification population, not the selected personal classification.
  const { data: allCountData, isError: allCountError } = useQuery<SessionOverviewCount>({
    queryKey: ['session-overview-count', user?.id ?? null, selectedAccount, selectedSource, projectKey, selectedAgent, assembled, 'all', overviewSince ?? '', overviewUntil ?? '',
      ...(analysisView ? [true, overviewSearch, effectiveCategory, effectivePlan, effectiveSort] : [])],
    queryFn: ({ signal }) => runCountRead(() => fetchSessionOverviewCount({
      ...analysisScope,
      ...(!analysisView ? { includeClassificationCounts: false } : {}),
      loginEmail: selectedAccount || undefined,
      assembled,
      source: overviewSource,
      since: overviewSince,
      until: overviewUntil,
      projectHashes,
      agent: selectedAgent,
    }, signal), signal),
    enabled: initialOverviewLoaded && analysisView && selectedClassification !== 'all',
    staleTime: 30_000,
    refetchInterval: POLL_NORMAL,
  });
  const { data: allSourceCountData, isError: allSourceCountError, isLoading: allSourceCountLoading } = useQuery<SessionOverviewCount>({
    queryKey: ['session-overview-count', user?.id ?? null, selectedAccount, 'hidden-source-all', selectedSource,
      projectKey, selectedAgent, assembled, selectedClassification, overviewSince ?? '', overviewUntil ?? '',
      analysisView, overviewSearch, effectiveCategory ?? '', effectivePlan ?? '', effectiveSort],
    queryFn: ({ signal }) => runCountRead(() => fetchSessionOverviewCount({
      ...analysisScope,
      ...(!analysisView ? { includeClassificationCounts: false } : {}),
      loginEmail: selectedAccount || undefined,
      assembled,
      source: 'all',
      since: overviewSince,
      until: overviewUntil,
      projectHashes,
      agent: selectedAgent,
      classification,
    }, signal), signal),
    enabled: initialOverviewLoaded && selectedSource !== 'all',
    staleTime: 30_000,
    refetchInterval: POLL_NORMAL,
  });
  const currentSourceCountData = countData;
  const currentSourceCountError = countError;
  const currentSourceCountLoading = countLoading;
  const auxiliaryCountReadError = countError || (analysisView && allCountError) || allSourceCountError;
  const hiddenSessionCount = selectedSource === 'all' ? 0
    : currentSourceCountError || allSourceCountError || !currentSourceCountData || !allSourceCountData ||
      !Number.isSafeInteger(currentSourceCountData.count) || currentSourceCountData.count < 0 ||
      !Number.isSafeInteger(allSourceCountData.count) || allSourceCountData.count < currentSourceCountData.count
      ? null : allSourceCountData.count - currentSourceCountData.count;
  const hiddenCountLoading = selectedSource !== 'all' && !currentSourceCountError && !allSourceCountError &&
    ((!currentSourceCountData && currentSourceCountLoading) || (!allSourceCountData && allSourceCountLoading));
  const { data: categoryCounts, isError: categoryCountError } = useQuery<SessionCategoryCounts>({
    queryKey: ['session-overview-categories', user?.id ?? null, selectedSource, projectKey, selectedAgent, assembled, selectedClassification, overviewSince ?? '', overviewUntil ?? ''],
    queryFn: ({ signal }) => runSerialSessionRead(() => fetchSessionOverviewCategories({ assembled, source: overviewCategoriesSource, projectHashes,
      agent: selectedAgent, classification, since: overviewSince, until: overviewUntil }), signal),
    enabled: initialOverviewLoaded && analysisView && !isAdmin && !!user && (!selectedProject || projectHashes.length > 0),
    staleTime: 30_000, refetchInterval: POLL_NORMAL,
  });
  // The today column and now line move with the clock, but only while the calendar shows.
  useEffect(() => {
    if (!calendarView) return;
    const timer = setInterval(() => setCalendarNowMs(Date.now()), 60_000);
    return () => clearInterval(timer);
  }, [calendarView]);
  const calendarZone = calendarTimeZone(calendarZoneName);
  const calendarWeekStart = calendarWeek ?? todayWeekStart(calendarZone, calendarNowMs);
  const calendarRange = weekQueryRange(calendarZone, calendarWeekStart);
  // An administrator reads one selected user, or themself by default. The account filter
  // is a login email that narrows that user's sessions; it never names the user.
  const calendarUserScope = !appliedCalendarUser ? {}
    : userScope === 'user_id' ? { userId: appliedCalendarUser.slice(4) } : { profileEmail: appliedCalendarUser.slice(6) };
  const calendarAgentSupported = selectedAgent !== 'weekly';
  const calendarQueryKey = ['session-calendar', user?.id ?? null, isAdmin ? 'admin' : 'user', appliedCalendarUser, isAdmin ? selectedAccount : '',
    calendarZone, calendarRange.from, calendarRange.to, selectedSource, projectKey, selectedAgent, assembled, selectedClassification] as const;
  const { data: calendarData, error: calendarError, isFetching: calendarFetching, refetch: refetchCalendar } = useQuery({
    queryKey: calendarQueryKey,
    queryFn: () => fetchSessionCalendar({ ...calendarRange, ...calendarUserScope,
      loginEmail: isAdmin ? selectedAccount || undefined : undefined, projectHashes: selectedProject ? projectHashes : undefined,
      agent: selectedAgent || undefined, source: overviewSource || undefined, assembled, classification }),
    enabled: calendarView && !!user && calendarAgentSupported && (!selectedProject || projectHashes.length > 0),
    retry: (failures, error) => !(error instanceof SessionCalendarRequestError && error.status < 500) && failures < 2,
    refetchInterval: POLL_NORMAL,
  });
  const canReadOwnRecordGeneration = calendarView && !!user && sessionRecordsEnabled && (!isAdmin || !appliedCalendarUser);
  const recordStatusKey = ['session-record-status', user?.id ?? null, isAdmin ? 'admin' : 'user', calendarRange.from, calendarRange.to, selectedAccount];
  const recordStatus = useQuery({
    queryKey: recordStatusKey,
    queryFn: () => fetchSessionRecordStatus(calendarRange.from, calendarRange.to),
    enabled: canReadOwnRecordGeneration,
    retry: false,
    staleTime: 10_000,
    refetchInterval: (query) => query.state.data?.state === 'writing' ? POLL_LIVE
      : query.state.data?.state === 'stale' ? POLL_NORMAL : false,
  });
  const recordRefresh = useMutation({
    mutationFn: (range: { from: string; to: string }) => refreshSessionRecordRange(range.from, range.to),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['session-record-status', user?.id ?? null] });
      void qc.invalidateQueries({ queryKey: ['session-analysis'] });
      void qc.invalidateQueries({ queryKey: ['session-overview'] });
      void qc.invalidateQueries({ queryKey: ['session-overview-count'] });
      void qc.invalidateQueries({ queryKey: ['session-calendar'] });
      void qc.invalidateQueries({ queryKey: ['admin-ai'] });
    },
  });
  const handleRefreshRecordRange = () => {
    recordRefresh.mutate({ from: calendarRange.from, to: calendarRange.to });
  };
  const { data: calendarUsers, isError: calendarUsersError } = useQuery({
    queryKey: ['users'], queryFn: fetchUsers, enabled: calendarView && isAdmin && !!userScope, staleTime: 60_000,
  });
  const classificationCountData = selectedClassification === 'all' ? countData : allCountData;
  const classificationCountError = selectedClassification === 'all' ? countError : allCountError;
  // The server's personal buckets cover the base population, not the selected analysis
  // query/category/plan. Hide their numbers rather than presenting a different denominator.
  const analysisNarrowed = analysisView && !!(overviewSearch || effectiveCategory || effectivePlan);
  const analysisReordered = analysisView && (analysisNarrowed || effectiveSort !== 'activity');
  const recordGenerationUnavailable = isAdmin && appliedCalendarUser
    ? '다른 사용자의 캘린더에서는 본인 세션 기록 생성 상태를 표시하지 않습니다.'
    : maintenance && !sessionRecordsEnabled
      ? maintenance.session_records ? '관리자가 세션 기록 생성을 껐습니다.' : '서버가 세션 기록 생성 상태를 보고하지 않습니다.'
      : undefined;
  const recordStatusError = recordRefresh.error || recordStatus.error;

  // Flatten pages and dedup by session_id (offset pages can overlap when new sessions
  // arrive at the head between fetches).
  const seenSession = new Set<string>();
  const sessions: SessionOverview[] = [];
  for (const page of data?.pages ?? []) {
    for (const s of page) {
      if (seenSession.has(s.session_id)) continue;
      seenSession.add(s.session_id);
      sessions.push(s);
    }
  }

  // Dropdown options deduped by identity (collapses duplicate project_hash rows). Merged rows
  // can carry different last_session_at values, so the most recent one wins rather than
  // whichever row is encountered first.
  // member_hashes carries the real project_hash values behind each row. The row's own
  // project_hash is an identity key, not a hash, so anything that acts on the project
  // itself -- deleting it -- has to use these instead.
  const projectOptions: { project_hash: string; project_name: string; last_session_at: string | null; member_hashes: string[] }[] = [];
  const identityIndex = new Map<string, number>();
  for (const p of projects ?? []) {
    const key = projectIdentityKey(p);
    const existingIndex = identityIndex.get(key);
    if (existingIndex === undefined) {
      identityIndex.set(key, projectOptions.length);
      projectOptions.push({
        project_hash: key,
        project_name: projectIdentityLabel(p),
        last_session_at: p.last_session_at,
        member_hashes: [p.project_hash],
      });
      continue;
    }
    const existing = projectOptions[existingIndex];
    if (!existing.member_hashes.includes(p.project_hash)) existing.member_hashes.push(p.project_hash);
    if (p.last_session_at && (!existing.last_session_at || new Date(p.last_session_at) > new Date(existing.last_session_at))) {
      existing.last_session_at = p.last_session_at;
    }
  }
  // The current scope may no longer contain the chosen project (a filter changed under
  // it, or a deep link named one outside the default scope). The list stays filtered to
  // it either way, so the picker has to keep saying so — dropping the row would leave a
  // narrowed list under a control reading "all projects".
  const selectionOutOfScope = !!selectedProject && !identityIndex.has(selectedProject.key);
  if (selectedProject && selectionOutOfScope) {
    projectOptions.push({
      project_hash: selectedProject.key,
      project_name: selectedProject.name,
      last_session_at: null,
      member_hashes: selectedProject.hashes,
    });
  }

  // Codex JSONL has no tokens/cost; treat presence of records as activity. Applied before
  // reconcile: it is a data-quality filter, not a user filter, so it cannot pollute the
  // pending decision the way a search term would.
  const active = analysisView ? sessions : sessions.filter((s) =>
    (s.input_tokens ?? 0) > 0 || (s.output_tokens ?? 0) > 0 || (s.cost_usd ?? 0) > 0 || (s.event_count ?? 0) > 0,
  );

  // Freeze the order the user is looking at; sessions that arrived above it become a badge
  // instead of pushing the list down. [WARN] search must be applied *after* this — running it
  // first would hide committed rows from `live` and mis-read them as head arrivals.
  const { displayed, pendingIds, nextCommittedIds } = reconcileList({
    committedIds: (analysisView ? listScopeChanged || analysisReordered : basicScopeChanged) ? [] : committedIds,
    live: active,
    lastKnown: selectionScopeChanged ? new Map() : lastKnown,
    getId: sessionId,
  });

  const filtered = displayed.filter((s) => {
    // Source, project, and agent are filtered server-side; only search stays client-side.
    if (!search || analysisView) return true;
    const q = search.toLowerCase();
    return !!s.session_id?.toLowerCase().includes(q) ||
      !!s.profile_email?.toLowerCase().includes(q) ||
      !!s.project_name?.toLowerCase().includes(q);
  });

  // Resolve the selected id against the live page first, then the last value seen for it —
  // a session that scrolled out of the loaded pages (or off the filter) still renders.
  const liveById = new Map(sessions.map(s => [s.session_id, s]));
  // Another user's calendar never falls back to the head of the caller's list; only a session
  // picked from that user's calendar opens.
  const effectiveId = selectionScopeChanged ? null : selectedId ?? (calendarView && appliedCalendarUser ? null : pinnedDefaultId);
  const detailAvailable = !calendarView || (calendarSelectionScope === selectionScopeKey && !!selectedId && !!effectiveId);
  const showSessionDetail = detailAvailable && (!narrow || !narrowListOpen);
  // A direct detail link or a fresh selection folds the list for reading width. The
  // fold is scoped to this detail; leaving it restores the saved preference.
  const hideSessionNavigation = detailAvailable && (expanded || listCollapsed || narrow && !narrowListOpen);
  // Opening a session detail folds the app menu so the transcript gets the width.
  useEffect(() => {
    toggleDetailMenu(detailAvailable);
  }, [detailAvailable, toggleDetailMenu]);
  const effectiveSelected = effectiveId
    ? liveById.get(effectiveId) ?? lastKnown.get(effectiveId) ?? null
    : null;
  const effectiveSearchTarget = effectiveId && searchTarget?.sessionId === effectiveId ? searchTarget : null;

  const showSourceFilter = true; // server-side source filter; always offer the toggle

  useEffect(() => {
    if (reloadingAfterDelete && !isFetching) setReloadingAfterDelete(false);
  }, [reloadingAfterDelete, isFetching]);

  /**
   * Apply ?project_hash=... once, against the unscoped registry.
   *
   * Re-running would fight the selector every time the user changed it while the
   * parameter stayed in the URL, so the flag is one-shot — which is exactly why the
   * lookup may not depend on the current filter scope: a miss here is permanent.
   */
  useEffect(() => {
    if (appliedProjectParam.current || !projectHashParam || !linkProjects?.length) return;
    appliedProjectParam.current = true;
    const selection = deepLinkSelection(projectHashParam, linkProjects);
    if (selection) setSelectedProject(selection);
  }, [projectHashParam, linkProjects]);

  /**
   * ?session_id: open that conversation. One-shot for the same reason as the
   * project parameter -- re-running would drag the selection back every time the
   * user picked another session while the URL still carried the old one.
   *
   * Waits for the list rather than selecting blind: the id has to be a session
   * this view can actually show, and a miss goes through the same "not collected"
   * notice as a lineage pointer that lands nowhere, instead of a silent no-op.
   */
  const sessionListArrived = sessions.length > 0;
  const linkedSessionPresent = sessionIdParam !== '' && sessions.some(s => s.session_id === sessionIdParam);
  useEffect(() => {
    if (!sessionNavigationKey) {
      appliedSessionParam.current = '';
      return;
    }
    if (appliedSessionParam.current === sessionNavigationKey) return;
    if (sessionSearchParam || explicitDetailScope) {
      appliedSessionParam.current = sessionNavigationKey;
      personalNavigator.cancel();
      setSearchTarget({ sessionId: sessionIdParam, turnKey: turnKeyParam || undefined, ...(explicitDetailScope ? { ownScope: false } : {}) });
      setSelectedId(sessionIdParam);
      setScrollAnchor(undefined);
      setNavNotice('');
      return;
    }
    if (!sessionListArrived) return;
    appliedSessionParam.current = sessionNavigationKey;
    setSearchTarget(null);
    if (linkedSessionPresent) setSelectedId(sessionIdParam);
    else setNavNotice(sessionIdParam);
    // Primitives, not the sessions array: that array is rebuilt on every render,
    // and depending on it would re-run this on every render -- which the one-shot
    // ref already prevents, leaving only the noise.
  }, [sessionNavigationKey, sessionIdParam, sessionSearchParam, explicitDetailScope, turnKeyParam, sessionListArrived, linkedSessionPresent, personalNavigator]);

  const handleSource = (v: SourceValue) => setSelectedSource(v);
  const handleProjectChange = (key: string) => {
    const option = projectOptions.find((o) => o.project_hash === key);
    if (!option) {
      setSelectedProject(null);
      return;
    }
    setSelectedProject({ key, name: option.project_name, hashes: option.member_hashes });
  };
  const handleGroupChange = (items: readonly SessionGroupMember[], dismiss: boolean, group?: SessionGroups['ended'][number], allMembers = false) => {
    setGroupWriteError(false);
    if (!sessionGroups || !user || !groupQueryEnabled || groupReadUnavailable) return;
    groupMutation.mutate({ items, dismiss, group, allMembers, scopeIdentity: groupScopeIdentity,
      scope: { ...groupScope, projectHashes: groupScope.projectHashes?.slice() }, queryKey: groupQueryKey.slice(),
      version: sessionGroups.grouping_version, userId: user.id });
  };
  const replaceDetailSelection = (sid: string, profileEmail = '', userID = '') => {
    if (!sessionIdParam && detailParam === null) return;
    const params = new URLSearchParams(searchParams.toString());
    for (const key of ['session_search', 'anchor', 'anchor_file', 'from', 'inspect_record', 'inspect_path', 'inspect_file', 'inspect_call', 'inspection_kind', 'inspection_tool']) params.delete(key);
    clearSessionTraceState(params);
    params.set('session_id', sid);
    if (isAdmin) {
      params.set('login_email', selectedAccount);
      params.set('profile_email', profileEmail);
      params.set('user_id', userID);
    }
    router.replace(`${pathname}?${params}`, { scroll: false });
  };
  const handleSelectSession = (session: SessionOverview) => {
    setNarrowListOpen(false);
    personalNavigator.cancel();
    setPersonalNavNotice('');
    setSearchTarget(null);
    setSelectedId(session.session_id);
    replaceDetailSelection(session.session_id);
    setScrollAnchor(undefined);
  };
  // A calendar band may lie outside the loaded list pages. Its overview row was read in the
  // same access scope, so the viewer can open it without inventing list metadata.
  const handleCalendarSelect = (sid: string, turnKey?: string, anchorTs?: string) => {
    setNarrowListOpen(false);
    const calendarOverview = calendarData?.sessions.find((session) => session.session_id === sid)?.overview;
    const overview = appliedCalendarUser ? calendarOverview : liveById.get(sid) ?? calendarOverview;
    personalNavigator.cancel();
    setPersonalNavNotice('');
    setSearchTarget(null);
    if (!overview) {
      setNavNotice(sid);
      return;
    }
    if (overview !== liveById.get(sid)) setLastKnown((previous) => new Map(previous).set(sid, overview));
    setSelectedId(sid);
    setCalendarSelectionScope(selectionScopeKey);
    replaceDetailSelection(sid, calendarUserScope.profileEmail, calendarUserScope.userId);
    const params = new URLSearchParams(searchParams.toString());
    for (const key of ['session_search', 'anchor', 'anchor_file', 'inspect_record', 'inspect_path', 'inspect_file', 'inspect_call']) params.delete(key);
    clearSessionTraceState(params);
    params.set('session_id', sid); params.set('detail', 'conversation');
    if (turnKey) params.set('turn_key', turnKey); else params.delete('turn_key');
    if (anchorTs) params.set('from', anchorTs); else params.delete('from');
    if (isAdmin) { params.set('login_email', selectedAccount); params.set('profile_email', calendarUserScope.profileEmail ?? ''); params.set('user_id', calendarUserScope.userId ?? ''); }
    router.replace(`${pathname}?${params}`, { scroll: false });
    setSearchTarget(turnKey ? { sessionId: sid, turnKey, ownScope: false } : null);
    setScrollAnchor(undefined);
    setNavNotice('');
  };
  const handleSessionsView = (next: SessionsView) => {
    if (next === 'calendar') {
      setCalendarNowMs(Date.now());
      setCalendarSelectionScope(null);
    }
    setSessionsView(next);
  };
  // Guard here rather than in SessionList: the sentinel can report an intersection while a
  // page is already in flight, and fetchNextPage would then queue a duplicate request.
  const handleLoadMore = () => {
    if (!hasNextPage || isFetchingNextPage) return;
    fetchNextPage();
  };
  // A lineage or subagent target is resolved in the same access scope, independently of loaded list pages.
  const handleNavigate = (sid: string, anchorUuid?: string) => {
    setNarrowListOpen(false);
    personalNavigator.cancel();
    setPersonalNavNotice('');
    const ownSearch = !!effectiveSearchTarget && effectiveSearchTarget.ownScope !== false;
    setSelectedId(sid);
    setSearchTarget({ sessionId: sid, ownScope: ownSearch });
    setScrollAnchor(anchorUuid);
    setNavNotice('');
    const params = new URLSearchParams(searchParams.toString());
    for (const key of ['session_search', 'anchor', 'anchor_file', 'from', 'inspect_record', 'inspect_path', 'inspect_file', 'inspect_call']) params.delete(key);
    clearSessionTraceState(params);
    params.set('session_id', sid); params.set('detail', 'conversation');
    if (ownSearch) params.set('session_search', '1');
    if (anchorUuid) params.set('anchor', anchorUuid); else params.delete('anchor');
    if (isAdmin) { params.set('login_email', ownSearch ? '' : detailLoginEmail); params.set('profile_email', ownSearch ? '' : detailProfileEmail ?? ''); params.set('user_id', ownSearch ? '' : detailUserID ?? ''); }
    router.push(`${pathname}?${params}`, { scroll: false });
  };
  const handleClassificationSaved = (sid: string, saved: SessionClassification) => {
    if (selectedClassification === 'all') return;
    setBasicCommittedIds([]);
    setAnalysisCommittedIds([]);
    setLastKnown((previous) => {
      const row = previous.get(sid);
      return row ? new Map(previous).set(sid, { ...row, classification: saved }) : previous;
    });
    qc.setQueriesData<{ pages: SessionOverview[][]; pageParams: unknown[] }>(
      { queryKey: ['session-overview', user?.id ?? null, selectedAccount, selectedSource, projectKey, selectedAgent, assembled, selectedClassification] },
      (old) => old && saved !== selectedClassification
        ? { ...old, pages: old.pages.map((page) => page.filter((row) => row.session_id !== sid)) }
        : old,
    );
  };
  const dismissNotice = () => setNavNotice('');
  // Badge click: adopt the live order wholesale. Explicit user action, so the list is
  // allowed to move here — and only here.
  const handleCommitPending = () => setCommittedIds(commitLiveOrder(active, sessionId));

  /**
   * Remember the last value seen for every session, so a selected session that later falls
   * out of the loaded pages (or off the current filter) keeps rendering instead of blanking
   * the viewer. Written after commit, never during render; returning the previous map when
   * nothing changed keeps this from looping.
   */
  useEffect(() => {
    setLastKnown(prev => {
      let next: Map<string, SessionOverview> | null = null;
      for (const s of sessions) {
        if (prev.get(s.session_id) === s) continue;
        next = next ?? new Map(prev);
        next.set(s.session_id, s);
      }
      return next ?? prev;
    });
    // Depend on `data` (the query result), not the derived `sessions` array: `sessions` is
    // rebuilt with a new array identity on every render (e.g. each keystroke in the search
    // box), which would re-run this effect and re-walk every loaded session on each render.
    // `data` only changes identity when react-query actually lands new pages, and `sessions`
    // is a pure function of `data.pages`, so gating on `data` skips exactly the renders where
    // sessions' *values* did not change. FRONT-RULE bans useMemo, so this dependency swap is
    // the memoization-free way to avoid useMemo(() => sessions, ...) here.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data]);

  /**
   * Pin the default selection to the first row, once per filter scope.
   *
   * Not a derived value and therefore not the "no separate state for a derivable selection"
   * case FRONT-RULE bans: the pin depends on history (which row was first the moment the
   * scope's first page landed), which a render-time expression cannot reconstruct. Deriving
   * it from the current head is exactly the bug being fixed — polling changes the head, the
   * viewer key changes with it, and the session the user is reading is swapped out.
   *
   * A filter scope change (resetKey) releases the pin so the next list can pin its own head.
   */
  useEffect(() => {
    if (resetKeyRef.current !== resetKey) {
      const selectionChanged = selectionScopeRef.current !== selectionScopeKey;
      personalNavigator.cancel();
      resetKeyRef.current = resetKey;
      selectionScopeRef.current = selectionScopeKey;
      // A view switch leaves the basic order frozen; a real base-scope change releases it.
      if (basicScopeRef.current !== basicScopeKey) {
        basicScopeRef.current = basicScopeKey;
        setBasicCommittedIds([]);
      }
      setAnalysisCommittedIds([]);
      if (selectionChanged) {
        // A different account or filter cannot reuse a previously selected session.
        // A raw/assembled-only switch keeps both explicit and default selection.
        setPinnedDefaultId(null);
        setLastKnown(new Map());
        setSelectedId(null);
        setCalendarSelectionScope(null);
        setSearchTarget(null);
        setPersonalNavNotice('');
      }
      return;
    }
    if (pinnedDefaultId === null && filtered.length > 0) setPinnedDefaultId(filtered[0].session_id);
  }, [resetKey, basicScopeKey, selectionScopeKey, pinnedDefaultId, filtered, personalNavigator]);
  /**
   * Persist the order reconcile settled on. This only ever appends tail growth pulled in by
   * fetchNextPage (head arrivals stay pending) or re-commits wholesale when the committed
   * order no longer intersects the live one, so it converges immediately — the guard makes
   * the no-change case a no-op. Compared by content, not length: a wholesale re-commit can
   * land on the same length with different ids.
   */
  useEffect(() => {
    if (analysisReordered || nextCommittedIds.join(',') === committedIds.join(',')) return;
    setCommittedIds(nextCommittedIds);
  }, [analysisReordered, nextCommittedIds, committedIds, setCommittedIds]);

  const handleOpenAnalysis = () => {
    if (!user) return;
    const query = searchParams.toString();
    saveSessionsReturnState(user.id, { scopeKey: returnScope, returnUrl: `${pathname}${query ? `?${query}` : ''}`, search,
      analysisCategory, analysisPlan, analysisSort, selectedId: effectiveId, searchTarget,
      pinnedDefaultId, committedIds, lastKnown, selectedProject, selectedSource, selectedClassification,
      mode, scrollAnchor, sessionsView, calendarZoneName, calendarWeek, calendarZoom, calendarColorKey,
      calendarUser, calendarQueryKey, conversation: captureConversationState(), scroll: captureSessionsScroll() });
  };
  const scrollRestored = useRef(false);
  /** Restore tab-memory positions once the cached overview and conversation have mounted. */
  useEffect(() => {
    if (!returnState || returnState.scopeKey !== returnScope || !data || scrollRestored.current) return;
    scrollRestored.current = true;
    restoreSessionsScroll(returnState.scroll);
  }, [returnState, returnScope, data]);

  const handleRereadSessions = () => {
    for (const key of ['session-overview', 'session-overview-count', 'session-overview-categories', 'session-groups', 'session-analysis', 'session-repo-status', 'session-record-status']) {
      void qc.invalidateQueries({ queryKey: [key] });
    }
  };

  const detailTab: SessionDetailTab = detailParam === 'trace' || detailParam === 'analysis' ? 'trace' : detailParam === 'inspection' ? 'analysis' : detailParam === 'conversation' ? 'conversation'
    : effectiveSearchTarget && !effectiveSearchTarget.turnKey ? 'analysis' : 'conversation';
  const handleDetailTabChange = (tab: SessionDetailTab) => {
    const params = new URLSearchParams(searchParams.toString());
    params.set('detail', tab === 'analysis' ? 'inspection' : tab);
    if (effectiveId && effectiveId !== sessionIdParam) {
      for (const key of ['from', 'inspect_record', 'inspect_path', 'inspect_file', 'inspect_call', 'inspection_kind', 'inspection_tool']) params.delete(key);
      clearSessionTraceState(params);
    }
    if (effectiveId) params.set('session_id', effectiveId);
    if (isAdmin) {
      const ownSearch = !!effectiveSearchTarget && effectiveSearchTarget.ownScope !== false;
      params.set('login_email', ownSearch ? '' : detailLoginEmail);
      params.set('profile_email', ownSearch ? '' : detailProfileEmail ?? '');
      params.set('user_id', ownSearch ? '' : detailUserID ?? '');
    }
    router.push(`${pathname}?${params}`, { scroll: false });
  };
  const inspectionUrl = (target?: InspectionTarget, kind?: 'all' | 'context') => {
    const params = new URLSearchParams(searchParams.toString());
    const ownSearch = !!effectiveSearchTarget && effectiveSearchTarget.ownScope !== false;
    params.set('detail', 'inspection');
    if (effectiveId) params.set('session_id', effectiveId);
    if (kind) {
      params.set('inspection_kind', kind);
      params.delete('inspection_tool');
      for (const key of ['inspect_record', 'inspect_path', 'inspect_file', 'inspect_call']) params.delete(key);
    }
    if (target) {
      params.set('inspect_record', target.recordId);
      if (target.callId) params.set('inspect_call', target.callId); else params.delete('inspect_call');
      if (target.contentPath) params.set('inspect_path', target.contentPath); else params.delete('inspect_path');
      if (target.sourceFile) params.set('inspect_file', target.sourceFile); else params.delete('inspect_file');
    }
    if (isAdmin) { params.set('login_email', ownSearch ? '' : detailLoginEmail); params.set('profile_email', ownSearch ? '' : detailProfileEmail ?? ''); params.set('user_id', ownSearch ? '' : detailUserID ?? ''); }
    return `${pathname}?${params}`;
  };
  const handleConversationTarget = (target: InspectionTarget) => {
    const params = new URLSearchParams(inspectionUrl().split('?')[1]);
    const ownSearch = !!effectiveSearchTarget && effectiveSearchTarget.ownScope !== false;
    for (const key of ['turn_key', 'from']) params.delete(key);
    if (ownSearch) params.set('session_search', '1'); else params.delete('session_search');
    params.set('detail', 'conversation'); params.set('anchor', target.recordId);
    if (target.sourceFile) params.set('anchor_file', target.sourceFile); else params.delete('anchor_file');
    if (effectiveId) params.set('session_id', effectiveId);
    setSearchTarget(effectiveId ? { sessionId: effectiveId, ownScope: ownSearch } : null);
    router.push(`${pathname}?${params}`, { scroll: false });
  };
  const handleInspectionTool = (toolId: string | undefined) => {
    const params = new URLSearchParams(inspectionUrl().split('?')[1]);
    if (toolId) params.set('inspection_tool', toolId); else params.delete('inspection_tool');
    router.push(`${pathname}?${params}`, { scroll: false });
  };
  const handleInspectionTarget = (target: InspectionTarget, resetFilters = false) => router.push(inspectionUrl(target, resetFilters ? 'all' : undefined), { scroll: false });
  const handleInspectionKind = (kind: 'all' | 'context') => router.push(inspectionUrl(undefined, kind), { scroll: false });
  const handleOpenTraceTurn = (turnKey: string) => {
    const params = new URLSearchParams(inspectionUrl().split('?')[1]);
    params.set('detail', 'trace');
    params.set('turn_key', turnKey);
    for (const key of ['event_id', 'trace_from', 'trace_to', 'trace_group', 'trace_request_no']) params.delete(key);
    router.push(`${pathname}?${params}`, { scroll: false });
  };
  const handleToggleExpanded = () => {
    const params = new URLSearchParams(searchParams.toString());
    if (expanded) params.delete('expanded');
    else params.set('expanded', '1');
    if (effectiveId) params.set('session_id', effectiveId);
    setExpanded(!expanded);
    const query = params.toString();
    router.replace(`${pathname}${query ? `?${query}` : ''}`, { scroll: false });
  };
  const closeCalendarDetail = () => {
    setNarrowListOpen(false);
    setCalendarSelectionScope(null);
    if (expanded) handleToggleExpanded();
  };
  const handleToggleSessionList = () => {
    if (narrow) {
      if (listCollapsed) toggleList();
      setNarrowListOpen(open => !open);
    } else toggleList();
  };
  const handleReturnToReading = () => setNarrowListOpen(false);
  const detailScopeFromUrl = effectiveId === sessionIdParam && !sessionSearchParam;
  const toggleFilters = () => setFiltersOpen(open => !open);
  const clearCalendarClassification = () => setSelectedClassification('all');
  const filterSummary = showLocal ? '내 기기와 서버' : [
    selectedSource === 'all' ? '모든 실행 방식' : selectedSource === 'headless' ? '자동 실행' : '대화형',
    selectedProject?.name || '전체 프로젝트',
    calendarView ? '캘린더' : analysisView && SORT_META[effectiveSort].label,
    selectedClassification !== 'all' && CLASSIFICATION_LABEL[selectedClassification],
    analysisView && effectivePlan && (effectivePlan === 'file' ? '플랜 파일' : '플랜 모드'),
    analysisView && effectiveCategory && CATEGORY_META[effectiveCategory].label,
    overviewSince && `시작 ${overviewSince}`,
    overviewUntil && `종료 ${overviewUntil}`,
    search.trim() && `검색: ${search.trim()}`,
  ].filter(Boolean).join(' · ');
  const detailLoginEmail = detailScopeFromUrl ? searchParams.get('login_email') ?? selectedAccount : selectedAccount;
  const detailProfileEmail = detailScopeFromUrl ? searchParams.get('profile_email') ?? calendarUserScope.profileEmail : calendarUserScope.profileEmail;
  const detailUserID = detailScopeFromUrl ? searchParams.get('user_id') ?? calendarUserScope.userId : calendarUserScope.userId;

  /** URL의 확대 상태를 chrome에 반영하고 페이지 이탈 때 임시 배치를 복원한다. */
  useEffect(() => { setExpanded(expandedParam); }, [expandedParam, setExpanded]);
  /** 다른 페이지에는 세션 확대 배치를 남기지 않는다. */
  useEffect(() => () => setExpanded(false), [setExpanded]);
  /** 인증 shell이 교체되면 진행 중인 그룹 쓰기의 다음 배치를 중단한다. */
  useEffect(() => {
    currentGroupUser.current = user?.id ?? null;
    return () => { currentGroupUser.current = null; };
  }, [user?.id]);
  const detailQuery = searchParams.toString();
  /** 팝업의 Escape를 우선 처리하고 다음 Escape에서 상세 확대만 복원한다. */
  useEffect(() => {
    if (!expanded) return;
    const restoreOnEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || event.defaultPrevented) return;
      const popups = document.querySelectorAll<HTMLElement>('[role="dialog"], [role="alertdialog"], [role="menu"], [role="listbox"], [data-radix-popper-content-wrapper]');
      if ([...popups].some(popup => popup.dataset.state !== 'closed' && !popup.closest('[hidden]'))) return;
      event.preventDefault();
      const params = new URLSearchParams(detailQuery);
      params.delete('expanded');
      setExpanded(false);
      const query = params.toString();
      router.replace(`${pathname}${query ? `?${query}` : ''}`, { scroll: false });
    };
    document.addEventListener('keydown', restoreOnEscape);
    return () => document.removeEventListener('keydown', restoreOnEscape);
  }, [expanded, detailQuery, pathname, router, setExpanded]);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div hidden={expanded} className={cn('scroll-fade-row mb-2 min-w-0 shrink-0 items-center gap-3 overflow-x-auto', expanded ? 'hidden' : 'flex')}>
        <h2 className="shrink-0 text-[16px] font-semibold text-ink">Sessions</h2>
        <p aria-label="현재 세션 조회 조건" title={filterSummary} className="min-w-0 flex-1 truncate text-xs text-ink-3">{filterSummary}</p>
        <button type="button" onClick={toggleFilters} aria-expanded={filtersOpen}
          aria-controls="session-source-controls session-filter-controls"
          className="flex min-h-11 shrink-0 items-center gap-1 rounded border border-border px-2 py-1.5 text-xs text-ink-2 hover:bg-surface-sunk">
          추가 필터 {filtersOpen ? '접기' : '펼치기'}
          <ChevronDown aria-hidden="true" size={14} className={cn('transition-transform motion-reduce:transition-none', filtersOpen && 'rotate-180')} />
        </button>
      </div>
      <div id="session-source-controls" hidden={expanded || !filtersOpen} className={cn('mb-3 shrink-0 items-center gap-2', expanded || !filtersOpen ? 'hidden' : 'flex')}>
        <button type="button" aria-pressed={!showLocal} onClick={() => setShowLocal(false)} className="min-h-11 rounded border border-border px-3 py-1.5 text-xs aria-pressed:border-brand aria-pressed:text-brand">서버 세션</button>
        <button type="button" aria-pressed={showLocal} onClick={() => setShowLocal(true)} className="min-h-11 rounded border border-border px-3 py-1.5 text-xs aria-pressed:border-brand aria-pressed:text-brand">내 기기와 서버</button>
      </div>
      {showLocal && <div className="min-h-0 flex-1 overflow-auto"><LocalSessionsPanel embedded serverSessions={filtered}
        ownerScope={user?.cctrace_user_id ? { user_id: user.cctrace_user_id } : undefined}
        onSelectServer={(id) => {
          const row = sessions.find((session) => session.session_id === id);
          if (row) handleSelectSession(row);
          setShowLocal(false);
        }} /></div>}
      <div className={cn('min-h-0 flex-1 flex-col', showLocal ? 'hidden' : 'flex')}>
      <div role="group" aria-label="세션 기본 탐색" hidden={expanded}
        className={cn('scroll-fade-row mb-2 min-w-0 shrink-0 items-center gap-3 overflow-x-auto', expanded ? 'hidden' : 'flex')}>
        {analysisEnabled && <ViewModeSwitch value={sessionsView} onChange={handleSessionsView} />}
        <FilterBar
          showViewToggle={false}
          showModelCategory={false}
          showGroupBy={false}
          showRefresh={false}
          showSearch={true}
          search={search}
          onSearchChange={setSearch}
          searchPlaceholder={analysisView ? '관측된 세션 제목 검색...' : 'Search session, user, project...'}
          showProjectFilter={true}
          projects={projectOptions}
          selectedProject={selectedProject?.key ?? ''}
          onProjectChange={handleProjectChange}
          onProjectDelete={isAdmin ? setProjectToDelete : undefined}
          nowrap
        />
      </div>
      <section id="session-filter-controls" hidden={expanded || !filtersOpen} aria-label="세션 탐색 필터" className={expanded || !filtersOpen ? 'hidden' : 'contents'}>
        <div className="mb-3 flex shrink-0 flex-wrap items-center gap-3">
          {showSourceFilter && <SourceFilter selected={selectedSource} onSelect={handleSource}
            hiddenSessionCount={hiddenSessionCount} hiddenCountLoading={hiddenCountLoading} />}
        </div>
      {!calendarView && <div className="mb-2 flex shrink-0 items-center gap-2 text-[11px] text-ink-3">
        <span>{selectedProject ? '선택한 프로젝트' : '전체 프로젝트'} · </span>
        {overviewReadAt > 0 ? <time aria-label="세션 목록 읽은 시각" dateTime={new Date(overviewReadAt).toISOString()}>
          {new Date(overviewReadAt).toLocaleTimeString('ko-KR')}에 읽음
        </time> : <span>읽은 시각 판단 불가</span>}
        {overviewError && <span>최근 읽기 실패 · 이전 자료</span>}
        <button type="button" aria-label="세션 목록 다시 읽기" disabled={isFetching} onClick={handleRereadSessions}
          className="ml-auto rounded border border-border px-2 py-1 text-ink-2 hover:bg-surface-sunk disabled:opacity-50">
          {isFetching ? '읽는 중…' : '다시 읽기'}
        </button>
      </div>}

      {/* Project info banner */}
      {/* The banner aggregates the project's sessions from the registry row; when the
          project is outside the current scope there is no row and it renders nothing,
          which is the truthful reading — there is nothing in scope to aggregate. But
          then the page would show an empty list under a picker naming a project with no
          word of why, so say why in the banner's place. */}
      {selectionOutOfScope && (
        <div className="mb-3 px-4 py-3 bg-canvas rounded-lg border border-border flex-shrink-0 text-xs text-ink-3">
          {selectedProject?.name} has no session in this view — the current source and agent filters exclude it.
        </div>
      )}
      {selectedProject && (
        <ProjectBanner
          selectedProject={selectedProject.key}
          selectedAccount={selectedAccount}
          userId={user?.id}
          projects={projects ?? []}
          filtered={filtered}
          assembled={assembled}
          source={overviewSource}
          agent={selectedAgent}
          classification={classification}
        />
      )}

      <div className="mb-2 flex-shrink-0">
        {auxiliaryCountReadError && (!analysisView || isAdmin || !sessionRecordsEnabled || allCountError || allSourceCountError) &&
          <p role="alert" className="mb-2 px-1 text-xs text-danger">{analysisView ? '세션 판정 개수를 읽지 못했습니다.' : '세션 개수를 읽지 못했습니다.'}</p>}
        {analysisView ? <ClassificationTabs
          value={selectedClassification}
          counts={classificationCountError || analysisNarrowed ? undefined : classificationCountData?.classification_counts}
          total={classificationCountError ? undefined : classificationCountData?.count}
          onChange={setSelectedClassification}
          panelId="sessions-list-panel"
          unavailable={!classificationCountError && !!classificationCountData && !classificationCountData.classification_counts}
        /> : filtersOpen && !expanded && !showLocal ? <BasicClassificationTabs
          queryKey={['session-overview-count', 'classification-summary', user?.id ?? null, selectedAccount, selectedSource,
            projectKey, selectedAgent, assembled, overviewSince ?? '', overviewUntil ?? '']}
          params={{ includeClassificationCounts: true, loginEmail: selectedAccount || undefined, assembled, source: overviewSource,
            since: overviewSince, until: overviewUntil, projectHashes, agent: selectedAgent }}
          enabled={initialOverviewLoaded} value={selectedClassification} onChange={setSelectedClassification}
        /> : <ClassificationTabs value={selectedClassification} onChange={setSelectedClassification} panelId="sessions-list-panel" />}
        <p className="px-1 text-[11px] text-ink-3">전체는 현재 목록 범위, 분류는 내 개인 분류가 있는 세션입니다.{analysisNarrowed && ' 분석 검색·플랜·범주·판정 필터 중에는 분류별 개수를 표시하지 않습니다.'}</p>
        {analysisView && (
          <div className="mt-2 flex flex-wrap items-center gap-3">
            <SortControl value={analysisSort} onChange={setAnalysisSort} />
            <PlanFilterToggle value={analysisPlan} onChange={setAnalysisPlan} />
            {!isAdmin && (categoryCountError
              ? <p role="alert" className="text-xs text-danger">전체 범주 개수를 읽지 못했습니다.</p>
              : categoryCounts
                ? <CategoryCountChips counts={categoryCounts} value={effectiveCategory} onChange={setAnalysisCategory} />
                : <p role="status" className="text-xs text-ink-3">범주 개수를 불러오는 중입니다.</p>)}
            {!isAdmin && <p className="text-[11px] text-ink-3">범주 개수는 현재 개인 분류·프로젝트·소스·에이전트 범위 전체입니다. 제목 검색·플랜·선택한 범주와 관계없이 같은 분포를 표시합니다.</p>}
          </div>
        )}
        {analysisReordered && <p className="px-1 text-[11px] text-ink-3">세션 묶음은 현재 검색·플랜·범주·기간·프로젝트·계정 범위 전체에서 계산합니다. 전체 떼기는 현재 필터의 전체 그룹 멤버를 모두 읽고 저장한 뒤 완료됩니다.</p>}
      </div>

      </section>

      {/* Split layout */}
      <div id="sessions-list-panel" role="tabpanel" aria-labelledby={`classification-tab-${selectedClassification}`} className="flex flex-1 min-h-0 bg-surface rounded-lg border border-border overflow-hidden">
        {/* Left panel - session list or calendar. The viewer below keeps its position, so
            switching views never remounts it. */}
        <div id="session-navigation" hidden={hideSessionNavigation} className={cn('min-h-0 min-w-0 flex-col', hideSessionNavigation ? 'hidden' : 'flex', calendarView ? 'shrink grow-[3] basis-[480px]' : narrow ? 'flex-1' : 'max-w-[288px] shrink grow basis-[220px]')}>
        {narrow && narrowListOpen && effectiveId && <button type="button" onClick={handleReturnToReading} className="min-h-11 shrink-0 border-b border-border px-3 text-left text-xs text-ink-2">읽던 대화로 돌아가기</button>}
        {calendarView ? (
          <SessionsCalendarPanel
            timeZone={calendarZone}
            browserTimeZone={browserTimeZone()}
            onTimeZoneChange={setCalendarZoneName}
            weekStart={calendarWeekStart}
            onWeekChange={setCalendarWeek}
            nowMs={calendarNowMs}
            pxPerHour={calendarZoom}
            onPxPerHourChange={setCalendarZoom}
            colorKey={calendarColorKey}
            onColorKeyChange={setCalendarColorKey}
            data={calendarData}
            error={calendarError}
            reloading={calendarFetching}
            onReload={() => { void refetchCalendar(); }}
            selectedId={showSessionDetail ? effectiveId : null}
            onSelect={handleCalendarSelect}
            isAdmin={isAdmin}
            users={calendarUsers}
            usersError={calendarUsersError}
            selectedUser={appliedCalendarUser}
            onSelectedUserChange={setCalendarUser}
            userScope={userScope}
            loginEmail={selectedAccount}
            agentUnsupported={!calendarAgentSupported}
            listOnlyFilters={analysisReordered}
            classification={classification}
            onClearClassification={clearCalendarClassification}
            summary={canReadOwnRecordGeneration ? recordStatus.data : undefined}
            summaryLoading={canReadOwnRecordGeneration && recordStatus.isPending}
            summaryRefreshing={canReadOwnRecordGeneration && recordRefresh.isPending}
            summaryError={canReadOwnRecordGeneration && recordStatusError ? recordStatusErrorText(recordStatusError) : null}
            summaryScopeNote={isAdmin && selectedAccount && !appliedCalendarUser ? '계정 필터와 무관하게 로그인 사용자 본인의 해당 주간 세션 전체를 집계합니다.' : undefined}
            recordGenerationUnavailable={recordGenerationUnavailable}
            onRegenerateSummaries={canReadOwnRecordGeneration && recordStatus.data?.state === 'stale' ? handleRefreshRecordRange : undefined}
          />
        ) : <SessionList
          sessions={filtered}
          // The session pane deliberately keeps showing what it had; only the list
          // waits, because only the list is wrong.
          isLoading={isLoading || reloadingAfterDelete}
          selectedId={effectiveId}
          onSelect={handleSelectSession}
          hasMore={!!hasNextPage}
          isLoadingMore={isFetchingNextPage}
          onLoadMore={handleLoadMore}
          total={countError ? undefined : countData?.count}
          unattributedCount={countError ? undefined : countData?.unattributed}
          pendingCount={analysisReordered ? 0 : pendingIds.length}
          onCommitPending={handleCommitPending}
          groups={groupReadUnavailable ? undefined : sessionGroups}
          preserveGroupOrder={analysisReordered}
          groupsBusy={groupMutation.isPending || groupsFetching}
          groupReadError={groupQueryEnabled && groupReadError && !groupReadUnavailable}
          groupWriteError={groupQueryEnabled && groupWriteError}
          analysisMode={analysisView}
          onDismissGroup={(items, group, allMembers) => handleGroupChange(items, true, group, allMembers)}
          onRestoreGroup={(items, group) => handleGroupChange(items, false, group)}
        />}

        </div>

        {/* Right panel - session viewer */}
        {/* min-w-0: a flex child defaults to min-width:auto (its content width), so a single
            wide descendant (bash line, table, KaTeX) would push this whole panel past the
            viewport and every child below would lose its right boundary. Pinning min-w-0
            bounds the panel to the available width so inner overflow-x containers can scroll. */}
        {/* A floor of its own, then twice the list's share of whatever is left. The
            floor matters more than the ratio: with basis 0 this pane was the only thing
            that could give, so it was the only thing that did. */}
        <div id="session-detail" hidden={!showSessionDetail} className={cn("relative min-h-0 min-w-0 shrink grow-[2] basis-[280px] flex-col lg:min-w-[280px]", showSessionDetail ? "flex" : "hidden")}>
          {personalNavNotice && (
            <div role="alert" className="absolute top-2 left-1/2 -translate-x-1/2 z-20 flex items-center gap-3 rounded-lg border border-border bg-surface px-3 py-2 text-[11px] text-ink shadow-sm">
              <span>세션 <span className="font-mono text-ink-2">{personalNavNotice.slice(0, 8)}</span>의 상세 정보를 읽지 못했습니다. 삭제되었거나 접근 권한이 없는 세션일 수 있습니다.</span>
              <button onClick={() => setPersonalNavNotice('')} className="text-ink-3 hover:text-ink font-mono">X</button>
            </div>
          )}
          {!personalNavNotice && navNotice && (
            <div className="absolute top-2 left-1/2 -translate-x-1/2 z-20 flex items-center gap-3 rounded-lg border border-border bg-surface px-3 py-2 text-[11px] text-ink shadow-sm">
              <span>이전 세션 <span className="font-mono text-ink-2">{navNotice.slice(0, 8)}</span>을 현재 목록·필터에서 확인하지 못했습니다.</span>
              {classification && (
                <button type="button" onClick={() => { setSelectedClassification('all'); setNavNotice(''); }} className="text-brand hover:underline">전체로 보기</button>
              )}
              <button onClick={dismissNotice} className="text-ink-3 hover:text-ink font-mono">X</button>
            </div>
          )}
          <SessionViewer
            session={effectiveSelected}
            searchTarget={effectiveSearchTarget}
            mode={mode}
            analysisLoginEmail={detailLoginEmail}
            analysisProfileEmail={detailProfileEmail}
            analysisUserID={detailUserID}
            onOpenAnalysis={handleOpenAnalysis}
            detailTab={detailTab}
            inspectionTarget={effectiveId === sessionIdParam && searchParams.get('inspect_record') ? { recordId: searchParams.get('inspect_record')!, contentPath: searchParams.get('inspect_path') || undefined, callId: searchParams.get('inspect_call') || undefined, sourceFile: searchParams.get('inspect_file') || undefined } : null}
            inspectionKind={searchParams.get('inspection_kind') === 'context' ? 'context' : 'all'}
            onInspectionTargetChange={handleInspectionTarget}
            onInspectionKindChange={handleInspectionKind}
            inspectionTool={searchParams.get('inspection_tool') || undefined}
            onInspectionToolChange={handleInspectionTool}
            onConversationTargetChange={handleConversationTarget}
            onDetailTabChange={handleDetailTabChange}
            onOpenTraceTurn={handleOpenTraceTurn}
            expanded={expanded}
            onToggleExpanded={handleToggleExpanded}
            listCollapsed={hideSessionNavigation}
            onToggleList={handleToggleSessionList}
            onClose={calendarView ? closeCalendarDetail : undefined}
            enabled={!showLocal && showSessionDetail}
            returnConversation={returnState?.scopeKey === returnScope ? returnState.conversation : undefined}
            onNavigate={handleNavigate}
            onClassificationSaved={handleClassificationSaved}
            scrollAnchor={effectiveId === sessionIdParam && (explicitDetailScope || sessionSearchParam) ? searchParams.get('anchor') || undefined : scrollAnchor}
            scrollSourceFile={effectiveId === sessionIdParam ? searchParams.get('anchor_file') || undefined : undefined}
            scrollFromTs={effectiveId === sessionIdParam ? fromTsParam || undefined : undefined}
            onDeleted={(deletedId) => {
              personalNavigator.cancel();
              setSelectedId((cur) => (cur === deletedId ? null : cur));
              setSearchTarget((cur) => (cur?.sessionId === deletedId ? null : cur));
              if (sessionIdParam === deletedId) {
                const params = new URLSearchParams(searchParams.toString());
                for (const key of ['session_id', 'session_search', 'anchor', 'anchor_file', 'from', 'detail', 'expanded']) params.delete(key);
                clearSessionTraceState(params);
                const query = params.toString();
                router.replace(`${pathname}${query ? `?${query}` : ''}`, { scroll: false });
              }
              setReloadingAfterDelete(true);
              // Drop the frozen order and the last-seen values. reconcileList keeps a
              // committed id that has fallen out of the server response and renders it
              // from lastKnown -- right for a session that blinked out during a poll,
              // wrong for one that was deleted. A project-wide purge takes sessions
              // this component cannot name, so the refetch is the only thing that
              // knows they are gone, and the frozen order is what hides that from it.
              setBasicCommittedIds([]);
              setAnalysisCommittedIds([]);
              setLastKnown(new Map());
              setPinnedDefaultId(null);
              void qc.invalidateQueries({ queryKey: ['session-calendar'] });
            }}
          />
        </div>
      </div>

      </div>
      {isAdmin && projectToDelete && (
        <DeleteProjectDialog
          projectHashes={projectToDelete.member_hashes ?? [projectToDelete.project_hash]}
          projectName={projectToDelete.project_name}
          onDeleted={() => {
            personalNavigator.cancel();
            // The chosen project is gone, so a filter pointing at it would leave the
            // list empty with no way back other than reopening the picker.
            if (selectedProject?.key === projectToDelete.project_hash) setSelectedProject(null);
            setProjectToDelete(null);
            setReloadingAfterDelete(true);
            // Same reason as the session delete: a committed id that survives in
            // lastKnown outlives the rows it stood for.
            setBasicCommittedIds([]);
            setAnalysisCommittedIds([]);
            setLastKnown(new Map());
            setPinnedDefaultId(null);
            setSelectedId(null);
            const deletedHashes = new Set(projectToDelete.member_hashes ?? [projectToDelete.project_hash]);
            qc.setQueriesData<{ pages: SessionOverview[][]; pageParams: unknown[] }>(
              { queryKey: ['session-overview'] },
              (old) => old && Array.isArray(old.pages)
                ? { ...old, pages: old.pages.map((page) => page.filter((session) => !deletedHashes.has(session.project_hash ?? ''))) }
                : old,
            );
            qc.invalidateQueries({ queryKey: ['projects'], refetchType: 'all' });
            // Preserve invalidation for other views without prefetching their analysis.
            qc.invalidateQueries({ queryKey: ['session-overview'] });
            qc.invalidateQueries({ queryKey: ['session-overview-count'] });
            qc.invalidateQueries({ queryKey: ['session-calendar'], refetchType: 'all' });
          }}
          onCancel={() => setProjectToDelete(null)}
        />
      )}
    </div>
  );
}
