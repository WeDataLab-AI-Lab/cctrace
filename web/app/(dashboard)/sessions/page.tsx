'use client';

import { useEffect, useRef, useState } from 'react';
import { useSearchParams } from 'next/navigation';
import { useQuery, useInfiniteQuery, keepPreviousData, useQueryClient } from '@tanstack/react-query';
import { POLL_NORMAL } from '@/lib/query-config';
import { fetchSessionOverview, fetchSessionOverviewCount, fetchProjects } from '@/lib/api';
import type { SessionOverviewCount } from '@/lib/api';
import { projectIdentityKey, projectIdentityLabel } from '@/lib/project-identity';
import { deepLinkSelection, selectionProjectHashes, type ProjectSelection } from '@/lib/project-selection';
import { useAccount } from '@/components/common/account-context';
import { useAgent } from '@/components/common/agent-context';
import { FilterBar } from '@/components/common/filter-bar';
import type { SessionOverview, Project } from '@/lib/types';
import { SourceFilter, type SourceValue } from '@/components/sessions/source-filter';
import { ModeToggle, type ViewMode } from '@/components/sessions/mode-toggle';
import { ProjectBanner } from '@/components/sessions/project-banner';
import { SessionList } from '@/components/sessions/session-list';
import { SessionViewer } from '@/components/sessions/session-viewer';
import { DeleteProjectDialog } from '@/components/sessions/delete-project-dialog';
import { useAuth } from '@/components/common/auth-context';
import { reconcileList, commitLiveOrder } from '@/components/sessions/live-list';

const sessionId = (s: SessionOverview): string => s.session_id;

export default function SessionsPage() {
  const qc = useQueryClient();
  const { user, isAdmin } = useAuth();
  const [search, setSearch] = useState('');
  // Selection is an id, not a snapshot object: the polled list is the single source of the
  // values, so the viewer header refreshes with the list card instead of freezing at the
  // moment of the click.
  const [selectedId, setSelectedId] = useState<string | null>(null);
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
  const [pinnedDefaultId, setPinnedDefaultId] = useState<string | null>(null);
  // Last value seen for every session id. State, not a ref: it is read during render, and
  // react-hooks/refs (rightly) bans reading a ref there.
  const [lastKnown, setLastKnown] = useState<Map<string, SessionOverview>>(new Map());
  // The display order the user is looking at. Frozen against polling; values are not.
  const [committedIds, setCommittedIds] = useState<string[]>([]);
  const { selectedAccount: pickedAccount } = useAccount();
  // The server pins a role=user session list to the caller's own user_id and drops
  // login_email (#811), and the header says so instead of showing chips. A selection made
  // on another screen is still in AccountContext; sending it here would narrow the project
  // chart (which applies login_email as sent) under a header claiming every account shows.
  // Keyed on role === 'user' like the header: the server allows only admin|user, so a missing
  // user (the dashboard layout does not mount this page before auth settles) is never read as
  // restricted.
  const selectedAccount = user?.role === 'user' ? '' : pickedAccount;

  const [selectedProject, setSelectedProject] = useState<ProjectSelection | null>(null);
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
  const appliedSessionParam = useRef(false);
  const appliedProjectParam = useRef(false);
  // The header's Agent pills are the one place this is chosen. The page used to keep its
  // own useState and its own row of identical-looking pills, so the header control simply
  // did nothing here — two selectors, one of them inert, and no way to tell which.
  const { selectedAgent } = useAgent();
  const [selectedSource, setSelectedSource] = useState<SourceValue>('interactive'); // hides headless by default
  const [navNotice, setNavNotice] = useState(''); // origin session id that couldn't be found
  const [scrollAnchor, setScrollAnchor] = useState<string | undefined>(); // fork point to scroll to after navigation
  // Global view mode: assembled folds branch/clear children into their root and
  // shows the merged thread; raw lists every session/file separately.
  const [mode, setMode] = useState<ViewMode>('assembled');

  const overviewSource = selectedSource === 'all' ? '' : selectedSource;
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

  // Infinite scroll: page the overview by offset. source, project, and agent are filtered
  // server-side so the LIMIT and count reflect exactly what will be shown (a client-side
  // filter would only see the loaded pages). Search stays client-side.
  const OVERVIEW_PAGE = 100;
  // The server-side scope of the list. A change here means a different list entirely, which
  // is the one moment the default selection may move.
  const resetKey = [selectedAccount, selectedSource, projectKey, selectedAgent, assembled].join('|');
  const resetKeyRef = useRef(resetKey);
  const { data, isLoading, isFetching, fetchNextPage, hasNextPage, isFetchingNextPage } = useInfiniteQuery({
    queryKey: ['session-overview', selectedAccount, selectedSource, projectKey, selectedAgent, assembled],
    queryFn: ({ pageParam }: { pageParam: number }) =>
      fetchSessionOverview({
        loginEmail: selectedAccount || undefined,
        limit: OVERVIEW_PAGE,
        assembled,
        source: overviewSource,
        offset: pageParam,
        projectHashes,
        agent: selectedAgent,
      }),
    initialPageParam: 0,
    refetchInterval: POLL_NORMAL,
    getNextPageParam: (lastPage: SessionOverview[], allPages) =>
      lastPage.length === OVERVIEW_PAGE ? allPages.length * OVERVIEW_PAGE : undefined,
  });

  // Total session count for the current scope+source+project+agent, so the list shows "N / total".
  const { data: countData } = useQuery<SessionOverviewCount>({
    queryKey: ['session-overview-count', selectedAccount, selectedSource, projectKey, selectedAgent, assembled],
    queryFn: () => fetchSessionOverviewCount({
      loginEmail: selectedAccount || undefined,
      assembled,
      source: overviewSource,
      projectHashes,
      agent: selectedAgent,
    }),
    staleTime: 30_000,
    refetchInterval: POLL_NORMAL,
    // The count sits next to the list; without this the "N / total" tail blanks out on
    // every filter change while the new count is in flight.
    placeholderData: keepPreviousData,
  });

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
  const active = sessions.filter((s) =>
    (s.input_tokens ?? 0) > 0 || (s.output_tokens ?? 0) > 0 || (s.cost_usd ?? 0) > 0 || (s.event_count ?? 0) > 0,
  );

  // Freeze the order the user is looking at; sessions that arrived above it become a badge
  // instead of pushing the list down. [WARN] search must be applied *after* this — running it
  // first would hide committed rows from `live` and mis-read them as head arrivals.
  const { displayed, pendingIds, nextCommittedIds } = reconcileList({
    committedIds,
    live: active,
    lastKnown,
    getId: sessionId,
  });

  const filtered = displayed.filter((s) => {
    // Source, project, and agent are filtered server-side; only search stays client-side.
    if (!search) return true;
    const q = search.toLowerCase();
    return !!s.session_id?.toLowerCase().includes(q) ||
      !!s.profile_email?.toLowerCase().includes(q) ||
      !!s.project_name?.toLowerCase().includes(q);
  });

  // Resolve the selected id against the live page first, then the last value seen for it —
  // a session that scrolled out of the loaded pages (or off the filter) still renders.
  const liveById = new Map(sessions.map(s => [s.session_id, s]));
  const effectiveId = selectedId ?? pinnedDefaultId;
  const effectiveSelected = effectiveId
    ? liveById.get(effectiveId) ?? lastKnown.get(effectiveId) ?? null
    : null;

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
    if (appliedSessionParam.current || !sessionIdParam || !sessionListArrived) return;
    appliedSessionParam.current = true;
    if (linkedSessionPresent) setSelectedId(sessionIdParam);
    else setNavNotice(sessionIdParam);
    // Primitives, not the sessions array: that array is rebuilt on every render,
    // and depending on it would re-run this on every render -- which the one-shot
    // ref already prevents, leaving only the noise.
  }, [sessionIdParam, sessionListArrived, linkedSessionPresent]);

  const handleSource = (v: SourceValue) => setSelectedSource(v);
  const handleProjectChange = (key: string) => {
    const option = projectOptions.find((o) => o.project_hash === key);
    if (!option) {
      setSelectedProject(null);
      return;
    }
    setSelectedProject({ key, name: option.project_name, hashes: option.member_hashes });
  };
  const handleSelectSession = (session: SessionOverview) => {
    setSelectedId(session.session_id);
    setScrollAnchor(undefined);
  };
  const handleMode = (m: ViewMode) => setMode(m);
  // Guard here rather than in SessionList: the sentinel can report an intersection while a
  // page is already in flight, and fetchNextPage would then queue a duplicate request.
  const handleLoadMore = () => {
    if (!hasNextPage || isFetchingNextPage) return;
    fetchNextPage();
  };
  // Lineage pointer navigation: jump to the origin session, or notify if it wasn't collected.
  const handleNavigate = (sid: string, anchorUuid?: string) => {
    const target = sessions.find(s => s.session_id === sid);
    if (target) {
      setSelectedId(target.session_id);
      setScrollAnchor(anchorUuid);
      setNavNotice('');
    } else {
      setNavNotice(sid);
    }
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
      resetKeyRef.current = resetKey;
      setPinnedDefaultId(null);
      setCommittedIds([]);
      // lastKnown and selectedId belong to the old scope: clearing them keeps the Map
      // from growing unbounded across scope changes and stops a stale session from a
      // previous account/filter from reappearing in the viewer via the lastKnown fallback.
      setLastKnown(new Map());
      setSelectedId(null);
      return;
    }
    if (pinnedDefaultId === null && filtered.length > 0) setPinnedDefaultId(filtered[0].session_id);
  }, [resetKey, pinnedDefaultId, filtered]);
  /**
   * Persist the order reconcile settled on. This only ever appends tail growth pulled in by
   * fetchNextPage (head arrivals stay pending) or re-commits wholesale when the committed
   * order no longer intersects the live one, so it converges immediately — the guard makes
   * the no-change case a no-op. Compared by content, not length: a wholesale re-commit can
   * land on the same length with different ids.
   */
  useEffect(() => {
    if (nextCommittedIds.join(',') === committedIds.join(',')) return;
    setCommittedIds(nextCommittedIds);
  }, [nextCommittedIds, committedIds]);

  return (
    <div className="flex flex-col h-full">
      {/* Top bar: title + search */}
      {/* One line, always. Nothing here wraps: the toggles are fixed-size and marked
          shrink-0, and the two things that CAN give -- the project picker and the
          search box -- shrink to half their width first. Wrapping was worse in both
          directions: with no gap the title and the first toggle collided, and once a
          gap was added the toolbar spread over three rows. min-w-0 at every level is
          what lets the shrink reach the controls; without it each container sizes to
          its contents and the row overflows instead. */}
      <div className="mb-4 flex flex-shrink-0 items-center justify-between gap-x-4">
        <h2 className="shrink-0 text-[16px] font-semibold text-ink">Sessions</h2>
        {/* No justify-end: in a scroll container it pins the content to the right edge,
            so the row starts scrolled and the leftmost controls are the ones cut off.
            ms-auto on the group keeps it right-aligned while there is room to spare. */}
        <div className="scroll-fade-row ms-auto flex min-w-0 flex-1 items-center gap-x-3 overflow-x-auto">
          <ModeToggle selected={mode} onSelect={handleMode} />
          {showSourceFilter && (
            <>
              <span className="h-4 w-px shrink-0 bg-[var(--border-strong)]" />
              <SourceFilter selected={selectedSource} onSelect={handleSource} />
            </>
          )}
          <FilterBar
            showViewToggle={false}
            showModelCategory={false}
            showGroupBy={false}
            showRefresh={false}
            showSearch={true}
            search={search}
            onSearchChange={setSearch}
            searchPlaceholder="Search session, user, project..."
            showProjectFilter={true}
            projects={projectOptions}
            selectedProject={selectedProject?.key ?? ''}
            onProjectChange={handleProjectChange}
            onProjectDelete={isAdmin ? setProjectToDelete : undefined}
            nowrap
          />
        </div>
      </div>

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
          projects={projects ?? []}
          filtered={filtered}
          assembled={assembled}
          source={overviewSource}
          agent={selectedAgent}
        />
      )}

      {/* Split layout */}
      <div className="flex flex-1 min-h-0 bg-surface rounded-lg border border-border overflow-hidden">
        {/* Left panel - session list */}
        <SessionList
          sessions={filtered}
          // The session pane deliberately keeps showing what it had; only the list
          // waits, because only the list is wrong.
          isLoading={isLoading || reloadingAfterDelete}
          selectedId={effectiveId}
          onSelect={handleSelectSession}
          hasMore={!!hasNextPage}
          isLoadingMore={isFetchingNextPage}
          onLoadMore={handleLoadMore}
          total={countData?.count}
          unattributedCount={countData?.unattributed ?? 0}
          pendingCount={pendingIds.length}
          onCommitPending={handleCommitPending}
        />

        {/* Right panel - session viewer */}
        {/* min-w-0: a flex child defaults to min-width:auto (its content width), so a single
            wide descendant (bash line, table, KaTeX) would push this whole panel past the
            viewport and every child below would lose its right boundary. Pinning min-w-0
            bounds the panel to the available width so inner overflow-x containers can scroll. */}
        {/* A floor of its own, then twice the list's share of whatever is left. The
            floor matters more than the ratio: with basis 0 this pane was the only thing
            that could give, so it was the only thing that did. */}
        <div className="relative flex min-h-0 min-w-[280px] shrink grow-[2] basis-[280px] flex-col">
          {navNotice && (
            <div className="absolute top-2 left-1/2 -translate-x-1/2 z-20 flex items-center gap-3 rounded-lg border border-border bg-surface px-3 py-2 text-[11px] text-ink shadow-sm">
              <span>이전 세션 <span className="font-mono text-ink-2">{navNotice.slice(0, 8)}</span> 이 수집되지 않아 이동할 수 없습니다.</span>
              <button onClick={dismissNotice} className="text-ink-3 hover:text-ink font-mono">X</button>
            </div>
          )}
          <SessionViewer
            session={effectiveSelected}
            mode={mode}
            onNavigate={handleNavigate}
            scrollAnchor={scrollAnchor}
            scrollFromTs={effectiveSelected?.session_id === sessionIdParam ? fromTsParam || undefined : undefined}
            onDeleted={(deletedId) => {
              setSelectedId((cur) => (cur === deletedId ? null : cur));
              setReloadingAfterDelete(true);
              // Drop the frozen order and the last-seen values. reconcileList keeps a
              // committed id that has fallen out of the server response and renders it
              // from lastKnown -- right for a session that blinked out during a poll,
              // wrong for one that was deleted. A project-wide purge takes sessions
              // this component cannot name, so the refetch is the only thing that
              // knows they are gone, and the frozen order is what hides that from it.
              setCommittedIds([]);
              setLastKnown(new Map());
              setPinnedDefaultId(null);
            }}
          />
        </div>
      </div>

      {isAdmin && projectToDelete && (
        <DeleteProjectDialog
          projectHashes={projectToDelete.member_hashes ?? [projectToDelete.project_hash]}
          projectName={projectToDelete.project_name}
          onDeleted={() => {
            // The chosen project is gone, so a filter pointing at it would leave the
            // list empty with no way back other than reopening the picker.
            if (selectedProject?.key === projectToDelete.project_hash) setSelectedProject(null);
            setProjectToDelete(null);
            setReloadingAfterDelete(true);
            // Same reason as the session delete: a committed id that survives in
            // lastKnown outlives the rows it stood for.
            setCommittedIds([]);
            setLastKnown(new Map());
            setPinnedDefaultId(null);
            setSelectedId(null);
            qc.invalidateQueries({ queryKey: ['projects'], refetchType: 'all' });
            qc.invalidateQueries({ queryKey: ['session-overview'], refetchType: 'all' });
            qc.invalidateQueries({ queryKey: ['session-overview-count'], refetchType: 'all' });
          }}
          onCancel={() => setProjectToDelete(null)}
        />
      )}
    </div>
  );
}
