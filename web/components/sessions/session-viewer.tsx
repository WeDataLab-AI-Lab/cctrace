'use client';

import { lazy, Suspense, useEffect, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { POLL_NORMAL } from '@/lib/query-config';
import { Trash2, Settings, Maximize2, Minimize2, PanelLeftClose, PanelLeftOpen, X } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { KeyboardEvent } from 'react';
import { fetchAppVersionInfo, fetchSessionRepoStatus } from '@/lib/api';
import { formatCctraceVersion, formatClaudeCodeVersion } from '@/lib/version-display';
import { agentLabel } from '@/lib/agent-label';
import type { SessionOverview } from '@/lib/types';
import { needsAccountNotice, projectLabel } from './session-utils';
import { ConversationReader } from './conversation-reader';
import type { InspectionTarget } from '@/lib/session-inspection';
import { AccountSegments } from './account-segments';
import { SessionPersonalEditor } from './session-personal-editor';
import type { SessionClassification } from '@/lib/session-analysis-types';
import { DeleteSessionDialog } from './delete-session-dialog';
import { BlockedProjectsDialog } from './blocked-projects-dialog';
import { useAuth } from '@/components/common/auth-context';
import { fetchDeletionPolicy } from '@/lib/api';
import { FrameStatusBar } from '@/components/sessions/frame/status-bar';
import { repoStatusFromAnalysis } from '@/components/sessions/frame/model';
import type { ViewMode } from './mode-toggle';
import type { ConversationReturnState } from '@/lib/sessions-return-state';
import { fetchConversationWindow } from '@/lib/conversation-window';
import { fetchSessionTraceIndex } from '@/lib/session-trace-index';

type SessionDetailTab = 'conversation' | 'trace' | 'analysis';

interface SessionViewerProps {
  session: SessionOverview | null;
  /** Search can open a result outside the current list pages and filters. */
  searchTarget?: { sessionId: string; turnKey?: string; ownScope?: boolean } | null;
  mode: ViewMode;
  onNavigate: (sessionId: string, anchorUuid?: string) => void;
  onClassificationSaved?: (sessionId: string, classification: SessionClassification) => void;
  analysisLoginEmail?: string;
  analysisProfileEmail?: string;
  analysisUserID?: string;
  onModeChange?: (mode: ViewMode) => void;
  scrollAnchor?: string;
  scrollSourceFile?: string;
  scrollFromTs?: string;
  /** Called after a delete so the list can drop the row and clear the selection. */
  onDeleted?: (sessionId: string) => void;
  /** Capture basic-list return state immediately before explicit detail navigation. */
  onOpenAnalysis?: () => void;
  returnConversation?: ConversationReturnState;
  detailTab?: SessionDetailTab;
  onDetailTabChange?: (tab: SessionDetailTab) => void;
  expanded?: boolean;
  onToggleExpanded?: () => void;
  listCollapsed?: boolean;
  onToggleList?: () => void;
  onClose?: () => void;
  enabled?: boolean;
  inspectionTarget?: InspectionTarget | null;
  inspectionKind?: 'all' | 'context';
  onInspectionTargetChange?: (target: InspectionTarget, resetFilters?: boolean) => void;
  /** Open the trace tab pinned to a request turn. */
  onOpenTraceTurn?: (turnKey: string) => void;
  onInspectionKindChange?: (kind: 'all' | 'context') => void;
  onConversationTargetChange?: (target: InspectionTarget) => void;
  inspectionTool?: string;
  onInspectionToolChange?: (toolId: string | undefined) => void;
}

const SessionTraceWorkspace = lazy(() => import('./trace/trace-workspace').then(module => ({ default: module.SessionTraceWorkspace })));
const SessionInspector = lazy(() => import('./inspection/session-inspector').then(module => ({ default: module.SessionInspector })));

// claude_version 은 Claude Code 전용 이름이지만 Codex 세션도 같은 컬럼에 버전이 실릴 수 있다.
// 서버는 agent 가 비면 'claude' 로 읽으므로(COALESCE) 여기서도 같게 읽는다. Claude 만 버전이
// 비었을 때 '—' 를 보이고, 그 외 에이전트는 모르는 버전을 '—' 로 채우지 않고 생략한다.
const harnessLabel = (session: SessionOverview): string => {
  const agent = session.agent || 'claude';
  if (agent === 'claude') return `${agentLabel(agent)} ${formatClaudeCodeVersion(session.claude_version)}`;
  return session.claude_version ? `${agentLabel(agent)} ${session.claude_version}` : agentLabel(agent);
};

const SessionViewer = ({ session, searchTarget, onNavigate, onClassificationSaved, analysisLoginEmail, analysisProfileEmail, analysisUserID, scrollAnchor, scrollSourceFile, scrollFromTs, onDeleted, onOpenAnalysis, returnConversation, detailTab, onDetailTabChange, expanded = false, onToggleExpanded, listCollapsed = false, onToggleList, onClose, enabled = true, inspectionTarget, inspectionKind, onInspectionTargetChange, onInspectionKindChange, onConversationTargetChange, inspectionTool, onInspectionToolChange, onOpenTraceTurn }: SessionViewerProps) => {
  const { isAdmin, user } = useAuth();
  const qc = useQueryClient();
  const activeSessionId = session?.session_id ?? searchTarget?.sessionId ?? null;
  const [showDelete, setShowDelete] = useState(false);
  const [showSettings, setShowSettings] = useState(false);
  const [localTab, setLocalTab] = useState<SessionDetailTab>(() => searchTarget && !searchTarget.turnKey ? 'analysis' : 'conversation');
  const activeTab = detailTab ?? localTab;
  const showAnalysis = !!activeSessionId && activeTab === 'analysis';
  const showTrace = !!activeSessionId && activeTab === 'trace';
  const traceActive = showTrace || showAnalysis;
  const showConversation = !showAnalysis && !showTrace;
  const [visitedTabs, setVisitedTabs] = useState<SessionDetailTab[]>(() => [activeTab]);
  const ownSearch = !!searchTarget && searchTarget.ownScope !== false;
  const analysisScope = isAdmin && !ownSearch ? { loginEmail: analysisLoginEmail,
    ...(analysisProfileEmail ? { profileEmail: analysisProfileEmail } : {}), ...(analysisUserID ? { userID: analysisUserID } : {}) } : undefined;
  const analysisScopeKey = ownSearch ? 'own-search' : analysisProfileEmail || analysisUserID
    ? JSON.stringify(analysisScope) : analysisLoginEmail ?? '';
  const readingKey = JSON.stringify([user?.id ?? null, isAdmin ? 'admin' : 'owner', analysisScopeKey, activeSessionId]);
  const [inspectionTargets, setInspectionTargets] = useState<Record<string, InspectionTarget>>({});
  const [readingAnchors, setReadingAnchors] = useState<Record<string, string>>({});
  const [conversationTargets, setConversationTargets] = useState<Record<string, string>>({});
  const inspectionOrigin = useRef<HTMLElement | null>(null);
  const target = inspectionTarget !== undefined ? inspectionTarget ?? undefined : inspectionKind === 'context' ? undefined : inspectionTargets[readingKey];
  const inspectionTurn = searchTarget?.turnKey;
  const turnWindow = useQuery({
    queryKey: ['conversation-window', readingKey, { turnKey: inspectionTurn }],
    queryFn: ({ signal }) => fetchConversationWindow({ sessionId: activeSessionId ?? '', scope: analysisScope, turnKey: inspectionTurn, signal }),
    enabled: !!user && enabled && showAnalysis && !!inspectionTurn && !target && inspectionKind !== 'context',
    staleTime: Infinity, retry: false,
  });
  const turnRecord = turnWindow.data?.records[0];
  const turnTarget = turnRecord?.view?.anchor_id ? { recordId: turnRecord.view.anchor_id, sourceFile: turnRecord.source_file } : undefined;
  const retryTurn = () => { void turnWindow.refetch(); };
  const repoStatus = useQuery({
    queryKey: ['session-repo-status', user?.id ?? null, isAdmin ? 'admin' : 'owner', analysisScopeKey, activeSessionId],
    queryFn: () => fetchSessionRepoStatus(activeSessionId ?? '', analysisScope),
    enabled: !!user && !!activeSessionId && enabled,
    staleTime: 30_000,
    refetchInterval: enabled ? POLL_NORMAL : false,
    retry: false,
  });
  const requestIndex = useQuery({
    queryKey: ['session-trace-index', user?.id ?? null, isAdmin ? 'admin' : 'owner', analysisScopeKey, activeSessionId],
    queryFn: ({ signal }) => fetchSessionTraceIndex(activeSessionId ?? '', analysisScope, signal),
    enabled: !!user && !!activeSessionId && enabled && showConversation,
    staleTime: 30_000,
    retry: false,
  });
  const selectTab = (tab: SessionDetailTab) => {
    setVisitedTabs(previous => previous.includes(tab) ? previous : [...previous, tab]);
    if (tab !== 'conversation') onOpenAnalysis?.();
    setLocalTab(tab);
    onDetailTabChange?.(tab);
  };
  const handleConversation = () => selectTab('conversation');
  const handleTrace = () => selectTab('trace');
  const handleTraceNode = () => setLocalTab('trace');
  const handleReturnToConversation = () => {
    selectTab('conversation');
    requestAnimationFrame(() => {
      const origin = inspectionOrigin.current;
      if (origin?.isConnected && !origin.closest('[hidden]')) origin.focus({ preventScroll: true });
      else document.getElementById('viewer-conversation-tab')?.focus({ preventScroll: true });
    });
  };
  const inspect = (next: InspectionTarget, resetFilters = false) => {
    if (!showAnalysis && document.activeElement instanceof HTMLElement) inspectionOrigin.current = document.activeElement;
    setInspectionTargets(previous => ({ ...previous, [readingKey]: next }));
    if (onInspectionTargetChange) onInspectionTargetChange(next, resetFilters);
    else selectTab('analysis');
  };
  const inspectTrace = (next: InspectionTarget) => inspect(next, true);
  const openTraceTurn = (turnKey: string) => { onOpenAnalysis?.(); onOpenTraceTurn?.(turnKey); };
  const rememberReadingAnchor = (anchor: string) => setReadingAnchors(previous => previous[readingKey] === anchor ? previous : { ...previous, [readingKey]: anchor });
  const showInConversation = (next: InspectionTarget) => {
    if (onConversationTargetChange) { onConversationTargetChange(next); return; }
    setConversationTargets(previous => ({ ...previous, [readingKey]: next.recordId }));
    selectTab('conversation');
  };
  const inspectContext = () => {
    if (document.activeElement instanceof HTMLElement) inspectionOrigin.current = document.activeElement;
    if (onInspectionKindChange) onInspectionKindChange('context');
    else selectTab('analysis');
  };
  const handleShowDelete = () => setShowDelete(true);
  const handleShowSettings = () => setShowSettings(true);
  const handleTabKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const tabs: SessionDetailTab[] = ['conversation', 'trace'];
    const index = activeTab === 'conversation' ? 0 : 1;
    const next = event.key === 'Home' ? tabs[0] : event.key === 'End' ? tabs[1] : tabs[(index + 1) % tabs.length];
    selectTab(next);
    event.currentTarget.querySelector<HTMLButtonElement>(`#viewer-${next}-tab`)?.focus();
  };

  // Read by everyone, not just admins: the dashboard needs it to decide whether to
  // draw the button at all, and hiding the answer would only mean drawing a button
  // that 403s.
  const { data: policy } = useQuery({
    queryKey: ['deletion-policy'],
    queryFn: fetchDeletionPolicy,
  });
  // Ownership is settled server-side. Showing the button to a non-admin whenever
  // the policy allows owner deletes keeps this component from re-deriving an access
  // rule that already exists in one place -- at worst the server answers 403.
  const canDelete = isAdmin || !!policy?.allow_owner_delete;
  // Static lookup (staleTime: Infinity) shared with the sidebar and Clients tab's
  // queryKey — session_record_version_since is the confirmed lower bound below
  // which cctrace_version is unrecorded (see version-display.ts).
  const { data: appVersionInfo } = useQuery({
    queryKey: ['app-version-info'],
    queryFn: fetchAppVersionInfo,
    staleTime: Infinity,
  });

  /** 직접 링크로 진입한 트레이스·검사도 탭 왕복에서 선택과 스크롤을 보존한다. */
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- URL 직접 진입 후에도 방문한 패널을 유지한다.
    if (activeSessionId) setVisitedTabs(previous => previous.includes(activeTab) ? previous : [...previous, activeTab]);
  }, [activeSessionId, activeTab]);

  return (
    <div className="flex-1 flex flex-col overflow-hidden">
      {(session || searchTarget) && <>
      <div className="flex-shrink-0 flex items-center gap-3 px-5 py-2 border-b border-border">
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-sm font-semibold text-ink" title={session?.first_line || activeSessionId || ''}>{session?.first_line || session?.analysis?.title?.text || activeSessionId?.slice(0, 8)}</h2>
          {session && <p className="truncate text-[11px] text-ink-3">{projectLabel(session)} · {agentLabel(session.agent || 'claude')} · {new Date(session.start_time).toLocaleString('ko-KR')}</p>}
        </div>
        <details className="max-w-[40%] text-[11px] text-ink-3"><summary className="cursor-pointer">세션 정보</summary>
        <p className="break-all font-mono">{activeSessionId}</p>
        {session && appVersionInfo && (
          <span className="hidden shrink-0 text-[11px] text-ink-3 sm:inline">
            cctrace {formatCctraceVersion(session.cctrace_version, appVersionInfo.session_record_version_since)}
            {' · '}
            {harnessLabel(session)}
          </span>
        )}
        {session && needsAccountNotice(session) && (
          <span className="flex shrink-0 items-center gap-1.5 text-[11px] text-ink-3">
            {session.login_email_inferred && (
              <span
                title="관측된 값이 아닙니다. 이 세션은 계정을 알려주는 로그를 남기지 않아, 같은 사용자의 앞뒤 로그인 기록으로 추정한 계정입니다."
                className="inline-flex items-center rounded bg-warning-soft px-1.5 py-0.5 text-[10px] font-medium text-warning-strong"
              >
                inferred
              </span>
            )}
            {(session.account_count ?? 1) > 1 && (
              <span title={`이 세션은 ${session.account_count}개 계정에 걸쳐 있습니다. 표시된 계정은 대표값입니다.`}>
                대표 · {session.account_count}개 계정
              </span>
            )}
          </span>
        )}
        </details>
        <span className="ml-auto flex shrink-0 items-center gap-1">
          {session && canDelete && session.session_id && (
            <button
              type="button"
              aria-label="세션 삭제"
              title="세션 삭제"
              onClick={handleShowDelete}
              className="rounded p-1 text-ink-3 hover:bg-surface-sunk hover:text-danger"
            >
              <Trash2 className="size-3.5" />
            </button>
          )}
          {isAdmin && (
            <button
              type="button"
              aria-label="세션 수집 설정"
              title="세션 수집 설정"
              onClick={handleShowSettings}
              className="rounded p-1 text-ink-3 hover:bg-surface-sunk hover:text-ink"
            >
              <Settings className="size-3.5" />
            </button>
          )}
        </span>
      </div>
      {session && needsAccountNotice(session) && (
        <AccountSegments sessionId={session.session_id} accountCount={session.account_count} />
      )}
      </>}
      {user && <SessionPersonalEditor key={user.id} sessionId={activeSessionId} userId={user.id} onClassificationSaved={onClassificationSaved} />}
      <div className="flex shrink-0 items-center border-b border-border px-4">
          {(session || searchTarget) && <div role="tablist" aria-label="세션 보기" onKeyDown={handleTabKeyDown} className="flex gap-1">
            <button type="button" role="tab" id="viewer-conversation-tab" aria-controls="viewer-conversation-panel" aria-selected={showConversation} tabIndex={showConversation ? 0 : -1} onClick={handleConversation} className="px-3 py-2 text-xs aria-selected:border-b-2 aria-selected:border-brand aria-selected:text-brand">대화</button>
            <button type="button" role="tab" id="viewer-trace-tab" aria-controls="viewer-trace-panel" aria-selected={traceActive} tabIndex={traceActive ? 0 : -1} onClick={handleTrace} title="요청·응답·도구 실행을 선택해 입력·결과와 원자료를 살펴봅니다" className="px-3 py-2 text-xs aria-selected:border-b-2 aria-selected:border-brand aria-selected:text-brand">트레이스</button>
          </div>}
          <div className="ml-auto flex items-center gap-1">
            {onToggleList && !expanded && <button type="button" aria-label={listCollapsed ? '세션 목록 펼치기' : '세션 목록 접기'} aria-expanded={!listCollapsed} aria-controls="session-navigation" onClick={onToggleList} className="rounded p-1.5 text-ink-3 hover:bg-surface-sunk hover:text-ink">
              {listCollapsed ? <PanelLeftOpen className="size-4" /> : <PanelLeftClose className="size-4" />}
            </button>}
            <button type="button" aria-label={expanded ? '상세 영역 복원' : '상세 영역 확대'} aria-pressed={expanded} onClick={onToggleExpanded} className="rounded p-1.5 text-ink-3 hover:bg-surface-sunk hover:text-ink">
              {expanded ? <Minimize2 className="size-4" /> : <Maximize2 className="size-4" />}
            </button>
            {onClose && <button type="button" aria-label="대화창 닫기" title="대화창 닫기" onClick={onClose} className="rounded p-1.5 text-ink-3 hover:bg-surface-sunk hover:text-ink">
              <X className="size-4" />
            </button>}
          </div>
      </div>
      <div className="flex-1 flex flex-col min-h-0 overflow-hidden">
        <div id="viewer-conversation-panel" role="tabpanel" aria-labelledby="viewer-conversation-tab" hidden={!showConversation} className={cn('min-h-0 flex-1 flex-col overflow-hidden', showConversation ? 'flex' : 'hidden')}>
          {activeSessionId ? <ConversationReader
            key={readingKey}
            memoryKey={readingKey}
            sessionId={activeSessionId}
            onNavigate={onNavigate}
            scrollAnchor={scrollAnchor ?? (searchTarget?.turnKey || scrollFromTs ? undefined : conversationTargets[readingKey])}
            scrollSourceFile={scrollSourceFile}
            scrollFromTs={scrollFromTs}
            restoredState={returnConversation?.sessionId === activeSessionId ? returnConversation : undefined}
            turnKey={searchTarget?.turnKey}
            active={showConversation && enabled}
            onInspect={inspect}
            onOpenTraceTurn={onOpenTraceTurn ? openTraceTurn : undefined}
            onReadingAnchor={rememberReadingAnchor}
            onInspectContext={showConversation ? inspectContext : undefined}
            requestIndex={requestIndex.data}
            scope={analysisScope}
            queryIdentity={user ? { userId: user.id, access: isAdmin ? 'admin' : 'owner' } : undefined}
            hasEnriched={session?.has_enriched}
          /> : (
            <div className="flex-1 flex items-center justify-center text-sm text-ink-3">Select a session to view</div>
          )}
        </div>
        {activeSessionId && (traceActive || visitedTabs.includes('trace') || visitedTabs.includes('analysis')) && (
          <div id="viewer-trace-panel" role="tabpanel" aria-labelledby="viewer-trace-tab" hidden={!traceActive} className={cn('min-h-0 flex-1 flex-col overflow-hidden', traceActive ? 'flex' : 'hidden')}>
            <Suspense fallback={<p role="status" className="p-4 text-sm text-ink-3">트레이스를 불러오는 중입니다.</p>}>
              <SessionTraceWorkspace key={readingKey} sessionId={activeSessionId} scope={ownSearch ? {} : analysisScope}
                enabled={traceActive && enabled} onInspect={inspectTrace} onShowInConversation={showInConversation}
                onSelectNode={handleTraceNode}
                inspectionPanel={(showAnalysis || visitedTabs.includes('analysis')) ? <div hidden={!showAnalysis} className={cn('min-h-0 flex-1 flex-col overflow-hidden', showAnalysis ? 'flex' : 'hidden')}>
                  {inspectionTurn && !target && turnWindow.isError && <p role="alert" className="border-b border-border px-4 py-2 text-xs text-danger">{turnWindow.error.message} <button type="button" onClick={retryTurn} className="underline">다시 시도</button></p>}
                  <Suspense fallback={<p role="status" className="p-4 text-sm text-ink-3">기록을 불러오는 중입니다.</p>}>
                    <SessionInspector key={readingKey} sessionId={activeSessionId} scope={ownSearch ? {} : analysisScope}
                      enabled={showAnalysis && enabled} target={target ?? turnTarget ?? (inspectionKind !== 'context' && readingAnchors[readingKey] ? { recordId: readingAnchors[readingKey] } : null)}
                      initialKind={inspectionKind} onKindChange={onInspectionKindChange} onTargetChange={inspect}
                      toolScope={inspectionTool} onToolScopeChange={onInspectionToolChange}
                      onShowInConversation={showInConversation} onReturnToConversation={handleReturnToConversation} />
                  </Suspense>
                </div> : undefined}
                inspectionActive={showAnalysis} />
            </Suspense>
          </div>
        )}
      {showDelete && session?.session_id && (
        <DeleteSessionDialog
          sessionId={session.session_id}
          projectHash={session.project_hash || ''}
          projectName={projectLabel(session)}
          canActOnProject={isAdmin}
          onDeleted={(res) => {
            // Close first. The server has written a tombstone and nothing more, so
            // there is nothing left to wait on here -- holding the modal open would be
            // waiting on a refetch, not on the delete.
            setShowDelete(false);
            const gone = new Set<string>([session.session_id]);
            // A purge takes the project's other sessions, and this component is never
            // told their ids -- but it is told the project, and every one of them
            // carries it. Dropping by project_hash removes them here rather than
            // leaving them to a refetch: the list is an infinite query holding several
            // pages, and a page that comes back while the frozen order is being reset
            // can carry a row that is already gone. One survivor made it through that
            // way, measured against an API response that no longer had the project.
            const purgedProject = res.purged_sessions > 0 ? res.project_hash : '';
            qc.setQueriesData<{ pages: SessionOverview[][]; pageParams: unknown[] }>(
              { queryKey: ['session-overview'] },
              (old) =>
                old && Array.isArray(old.pages)
                  ? {
                      ...old,
                      pages: old.pages.map((page) =>
                        page.filter(
                          (s) =>
                            !gone.has(s.session_id) &&
                            !(purgedProject && s.project_hash === purgedProject),
                        ),
                      ),
                    }
                  : old,
            );
            // Cached analysis views become stale; deletion only refreshes the visible list.
            // Conversation bodies are cached separately and would otherwise keep the
            // deleted session readable after the list drops it.
            qc.removeQueries({ queryKey: ['session-records', session.session_id] });
            if (purgedProject) {
              qc.removeQueries({ predicate: query => query.queryKey[0] === 'session-records' });
              qc.removeQueries({ predicate: query => query.queryKey[0] === 'conversation-window' });
            } else {
              qc.removeQueries({ predicate: query => query.queryKey[0] === 'conversation-window' && JSON.stringify(query.queryKey).includes(session.session_id) });
            }
            qc.invalidateQueries({ queryKey: ['session-overview'] });
            qc.invalidateQueries({ queryKey: ['session-overview-count'] });
            qc.invalidateQueries({ queryKey: ['projects'], refetchType: 'all' });
            if (res.project_blocked) qc.invalidateQueries({ queryKey: ['blocked-projects'] });
            onDeleted?.(session.session_id);
          }}
          onCancel={() => setShowDelete(false)}
        />
      )}
      {isAdmin && showSettings && <BlockedProjectsDialog onClose={() => setShowSettings(false)} />}
      </div>
      {activeSessionId && <FrameStatusBar repo={repoStatusFromAnalysis(repoStatus.data?.owner_view ? repoStatus.data.repo_status ?? undefined : undefined)} />}
    </div>
  );
};

export { SessionViewer };
export type { SessionDetailTab };
