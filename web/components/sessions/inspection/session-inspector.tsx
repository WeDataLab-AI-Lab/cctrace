'use client';

import { useEffect, useState } from 'react';
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query';
import { cn } from '@/lib/utils';
import { useAuth } from '@/components/common/auth-context';
import { fetchInspectionPage, inspectionEvents } from '@/lib/session-inspection';
import type { InspectionKind, InspectionTarget } from '@/lib/session-inspection';
import type { SessionRecordScope } from '@/lib/session-record-scope';
import { InspectionDetail, InspectionEventButton } from './inspection-detail';

interface SessionInspectorProps {
  sessionId: string;
  enabled?: boolean;
  scope?: SessionRecordScope;
  target?: InspectionTarget | null;
  initialKind?: InspectionKind;
  toolScope?: string;
  onToolScopeChange?: (id: string | undefined) => void;
  onTargetChange?: (target: InspectionTarget) => void;
  onKindChange?: (kind: InspectionKind) => void;
  onShowInConversation?: (target: InspectionTarget) => void;
  onReturnToConversation: () => void;
}

const SessionInspector = ({ sessionId, enabled = true, scope, target, initialKind = 'all', toolScope: controlledToolScope, onToolScopeChange, onTargetChange, onKindChange, onShowInConversation, onReturnToConversation }: SessionInspectorProps) => {
  const { user } = useAuth();
  const queryClient = useQueryClient();
  const [localKind, setKind] = useState<InspectionKind>(initialKind);
  const kind = onKindChange ? initialKind : localKind;
  const [localTarget, setLocalTarget] = useState<InspectionTarget>();
  const [query, setQuery] = useState('');
  const [showList, setShowList] = useState(false);
  const [localToolScope, setLocalToolScope] = useState<string>();
  const toolScope = onToolScopeChange ? controlledToolScope : localToolScope;
  const selectedTarget = target !== undefined ? target ?? undefined : localTarget;
  const key = ['session-inspection', user?.id ?? null, user?.role ?? '', sessionId, scope?.profileEmail ?? '', scope?.loginEmail ?? '', scope?.userID ?? ''];
  // Inspection is an explicit snapshot. No polling may move selection, scroll or ordering.
  const recordsQuery = useInfiniteQuery({
    queryKey: [...key, kind, toolScope], initialPageParam: 0,
    queryFn: ({ pageParam, signal }) => fetchInspectionPage({ sessionId, scope, kind, toolUseId: toolScope, offset: pageParam, signal }),
    getNextPageParam: page => page.next_offset,
    enabled: enabled && !!user, staleTime: Infinity, retry: false,
  });
  const records = recordsQuery.data?.pages.flatMap(page => page.records) ?? [];
  const loadedRecord = selectedTarget ? records.find(record => (record.view?.anchor_id ?? record.uuid) === selectedTarget.recordId && (!selectedTarget.sourceFile || record.source_file === selectedTarget.sourceFile)) : undefined;
  const selectedQuery = useQuery({
    queryKey: [...key, 'record', selectedTarget?.recordId, selectedTarget?.sourceFile],
    queryFn: ({ signal }) => fetchInspectionPage({ sessionId, scope, target: selectedTarget, signal }),
    enabled: enabled && !!user && !!selectedTarget && !loadedRecord, staleTime: Infinity, retry: false,
  });
  const selectedRecord = selectedTarget ? loadedRecord ?? selectedQuery.data?.selected_record : undefined;
  const selectedEvents = selectedRecord ? inspectionEvents(selectedRecord) : [];
  const selectedEvent = selectedTarget?.contentPath ? selectedEvents.find(event => event.target.contentPath === selectedTarget.contentPath)
    : selectedTarget?.callId ? selectedEvents.find(event => event.callId === selectedTarget.callId) : selectedEvents[0];
  const relatedQuery = useInfiniteQuery({
    queryKey: [...key, 'related', selectedEvent?.callId], initialPageParam: 0,
    queryFn: ({ pageParam, signal }) => fetchInspectionPage({ sessionId, scope, toolUseId: selectedEvent?.callId, offset: pageParam, signal }),
    getNextPageParam: page => page.next_offset,
    enabled: enabled && !!user && !!selectedEvent?.callId, staleTime: Infinity, retry: false,
  });
  const related = (relatedQuery.data?.pages.flatMap(page => page.records).flatMap(inspectionEvents) ?? []).filter(event => (event.callId === selectedEvent?.callId || event.record.tool_use_id === selectedEvent?.callId) && event.id !== selectedEvent?.id);
  const events = records.flatMap(inspectionEvents).filter(event => !toolScope || event.callId === toolScope || event.record.tool_use_id === toolScope);
  const visibleEvents = query.trim() ? events.filter(event => `${event.label}\n${event.text}\n${event.callId ?? ''}`.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase())) : events;
  const setToolScope = (id: string | undefined) => { setLocalToolScope(id); onToolScopeChange?.(id); };
  const handleSelect = (next: InspectionTarget) => { setLocalTarget(next); setShowList(false); onTargetChange?.(next); };
  const handleAll = () => { setToolScope(undefined); setKind('all'); onKindChange?.('all'); };
  const handleContext = () => { setToolScope(undefined); setKind('context'); onKindChange?.('context'); };
  const handleScopeTool = () => setToolScope(selectedEvent?.callId);
  const handleClearScope = () => setToolScope(undefined);
  const handleQuery = (event: React.ChangeEvent<HTMLInputElement>) => setQuery(event.target.value);
  const handleBackToList = () => setShowList(true);
  const handleRetry = () => { void recordsQuery.refetch(); };
  const handleRetrySelected = () => { void selectedQuery.refetch(); };
  const handleMore = () => { void recordsQuery.fetchNextPage(); };
  const handleRetryRelated = () => { void relatedQuery.refetch(); };
  const handleMoreRelated = () => { void relatedQuery.fetchNextPage(); };
  const handleKindKeys = (event: React.KeyboardEvent<HTMLElement>) => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const next = event.key === 'Home' ? 'all' : event.key === 'End' ? 'context' : kind === 'all' ? 'context' : 'all';
    if (next === 'all') handleAll(); else handleContext();
    document.getElementById(next === 'all' ? 'inspection-all-tab' : 'inspection-context-tab')?.focus();
  };
  const handleRefresh = () => {
    void recordsQuery.refetch();
    if (selectedTarget && !loadedRecord) void selectedQuery.refetch();
    if (selectedEvent?.callId) void relatedQuery.refetch();
  };
  /** Abort hidden, replaced or unmounted inspection requests while retaining cached reading state. */
  useEffect(() => {
    const prefix = ['session-inspection', user?.id ?? null, user?.role ?? '', sessionId, scope?.profileEmail ?? '', scope?.loginEmail ?? '', scope?.userID ?? ''];
    if (!enabled) void queryClient.cancelQueries({ queryKey: prefix });
    return () => { void queryClient.cancelQueries({ queryKey: prefix }); };
  }, [queryClient, enabled, user?.id, user?.role, sessionId, scope?.profileEmail, scope?.loginEmail, scope?.userID]);
  return <section aria-label="기록검사" className="flex min-h-0 flex-1 flex-col overflow-hidden">
    <header className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border-subtle px-3 py-2">
      <nav role="tablist" aria-label="검사 기록 종류" onKeyDown={handleKindKeys} className="flex rounded border border-border p-0.5">
        <button id="inspection-all-tab" role="tab" tabIndex={kind === 'all' ? 0 : -1} aria-controls="inspection-records-panel" type="button" aria-selected={kind === 'all'} onClick={handleAll} className={cn('rounded px-3 py-1.5 text-xs', kind === 'all' ? 'bg-surface-sunk font-medium text-ink' : 'text-ink-3 hover:text-ink')}>전체 기록</button>
        <button id="inspection-context-tab" role="tab" tabIndex={kind === 'context' ? 0 : -1} aria-controls="inspection-records-panel" type="button" aria-selected={kind === 'context'} onClick={handleContext} className={cn('rounded px-3 py-1.5 text-xs', kind === 'context' ? 'bg-surface-sunk font-medium text-ink' : 'text-ink-3 hover:text-ink')}>지시문·훅</button>
      </nav>
      {selectedEvent?.callId && !toolScope && <button type="button" onClick={handleScopeTool} className="rounded border border-border px-2 py-1.5 text-xs text-ink-2">이 호출과 연결된 기록</button>}
      {toolScope && <span className="flex items-center gap-2 text-xs text-ink-2">호출 <code className="max-w-32 truncate text-[10px]">{toolScope}</code><button type="button" onClick={handleClearScope} className="text-brand underline">전체 범위로</button></span>}
      <button type="button" onClick={handleRefresh} disabled={!user || recordsQuery.isFetching} className="ml-auto rounded px-2 py-1.5 text-xs text-ink-2 hover:bg-surface-sunk disabled:opacity-50">다시 읽기</button>
      <button type="button" onClick={onReturnToConversation} className="rounded px-2 py-1.5 text-xs text-ink-2 hover:bg-surface-sunk">대화로 돌아가기</button>
    </header>
    <div id="inspection-records-panel" role="tabpanel" aria-labelledby={kind === 'all' ? 'inspection-all-tab' : 'inspection-context-tab'} className="flex min-h-0 flex-1">
      <section aria-label="수집 기록 목록" className={cn('min-h-0 min-w-0 flex-1 flex-col overflow-hidden', selectedEvent && !showList ? 'hidden lg:flex lg:w-[35%] lg:flex-none lg:border-r lg:border-border-subtle' : 'flex', selectedEvent && 'lg:w-[35%] lg:flex-none lg:border-r lg:border-border-subtle')}>
        <div className="shrink-0 border-b border-border-subtle p-3">
          <label className="sr-only" htmlFor="inspection-search">불러온 기록 검색</label>
          <input id="inspection-search" value={query} onChange={handleQuery} placeholder="불러온 기록에서 검색" className="w-full rounded border border-border bg-surface px-3 py-2 text-xs outline-none focus:border-brand" />
          <p className="mt-2 text-[11px] text-ink-3">{kind === 'context' ? '연결된 세션의 지시문과 훅 기록' : '연결된 세션의 모든 수집 기록'} · 불러온 기록 {records.length}건</p>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">
          {recordsQuery.isPending && <p role="status" className="p-4 text-xs text-ink-3">기록을 읽는 중입니다.</p>}
          {recordsQuery.isError && records.length === 0 && <p role="alert" className="p-4 text-xs text-ink-3">{recordsQuery.error.message} <button type="button" onClick={handleRetry} className="text-brand underline">다시 시도</button></p>}
          {!recordsQuery.isPending && !recordsQuery.isError && visibleEvents.length === 0 && <p role="status" className="p-4 text-xs text-ink-3">{query ? '불러온 범위에 검색 결과가 없습니다.' : kind === 'context' ? '이 범위에서 수집된 지시문·훅 기록을 확인할 수 없습니다.' : '이 범위에 수집된 기록이 없습니다.'}</p>}
          <div className="divide-y divide-border-subtle">{visibleEvents.map(event => <InspectionEventButton key={event.id} event={event} selectedSessionId={sessionId} selected={event.id === selectedEvent?.id} onSelect={handleSelect} />)}</div>
          {recordsQuery.hasNextPage && <div className="border-t border-border-subtle p-3 text-center">
            {recordsQuery.isFetchNextPageError && <p role="alert" className="mb-2 text-xs text-ink-3">추가 기록을 읽지 못했습니다. 읽던 내용은 유지됩니다.</p>}
            <button type="button" onClick={handleMore} disabled={recordsQuery.isFetchingNextPage} className="rounded border border-border px-3 py-2 text-xs text-ink-2 disabled:opacity-50">{recordsQuery.isFetchingNextPage ? '읽는 중…' : recordsQuery.isFetchNextPageError ? '추가 기록 다시 시도' : '다음 기록 읽기'}</button>
          </div>}
        </div>
      </section>
      {selectedEvent && <div className={cn('min-h-0 min-w-0 flex-1 flex-col', showList ? 'hidden lg:flex' : 'flex')}>
        <InspectionDetail event={selectedEvent} selectedSessionId={sessionId} related={related} relatedLoading={relatedQuery.isFetching && !related.length} relatedError={relatedQuery.isError} onRetryRelated={handleRetryRelated} onSelect={handleSelect} onShowInConversation={onShowInConversation} onBackToList={handleBackToList} />
        {relatedQuery.hasNextPage && <button type="button" onClick={handleMoreRelated} disabled={relatedQuery.isFetchingNextPage} className="shrink-0 border-t border-border-subtle px-3 py-2 text-xs text-ink-2">{relatedQuery.isFetchingNextPage ? '읽는 중…' : '같은 호출의 관련 기록 더 읽기'}</button>}
      </div>}
      {selectedTarget && !selectedRecord && <div role={selectedQuery.isError ? 'alert' : 'status'} className="p-4 text-xs text-ink-3">{selectedQuery.isError ? <>{selectedQuery.error.message} <button type="button" onClick={handleRetrySelected} className="text-brand underline">다시 시도</button></> : '선택한 기록을 읽는 중입니다.'}</div>}
      {selectedRecord && !selectedEvent && <p role="alert" className="p-4 text-xs text-ink-3">선택한 기록 안에서 지정된 내용 위치를 확인할 수 없습니다. 기록 목록에서 검사할 항목을 선택해 주세요.</p>}
    </div>
  </section>;
};

export { SessionInspector };
export type { SessionInspectorProps };
