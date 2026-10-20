'use client';

import type { ReactNode, UIEvent } from 'react';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import { useEffect, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useAuth } from '@/components/common/auth-context';
import { useAccount } from '@/components/common/account-context';
import { fetchSessionAnalysis, SessionAnalysisRequestError } from '@/lib/api';
import type { AnalysisTurn, SessionAnalysis } from '@/lib/session-analysis-types';
import type { TraceEvent, TraceIdentity, TraceRange, TraceScope } from '@/lib/session-trace-types';
import type { TraceNodesView } from '@/lib/session-trace-node-types';
import type { InspectionTarget } from '@/lib/session-inspection';
import type { SessionRecord } from '@/lib/types';
import { usePageVisible, useSessionTrace } from '@/lib/use-session-trace';
import { useTraceNodeDetail } from '@/lib/use-trace-nodes';
import { useTurnContext } from '@/lib/use-turn-context';
import { validRange } from '@/lib/session-trace-model';
import { POLL_LIVE, POLL_NORMAL } from '@/lib/query-config';
import { SessionAnalysisTab } from '../analysis/analysis-tab';
import { useSessionRecordRegeneration } from '../analysis/record-regeneration';
import { TraceChart } from './trace-chart';
import { TraceContextPane } from './trace-context-pane';
import { TraceExecutionView } from './trace-execution-view';
import { TraceTimingView } from './trace-timing-view';
import { cn } from '@/lib/utils';
import { TraceNodeInspector } from './trace-node-inspector';
import { dedupeKeys } from '../session-utils';
import { previewView, turnMetrics } from '../analysis/requests';
import { formatCount } from '../analysis/format';

const traceBranchCursors = (raw: string | null): Record<string, string> => {
  if (!raw || raw.length > 65536) return {};
  try {
    const value: unknown = JSON.parse(raw);
    if (!value || typeof value !== 'object' || Array.isArray(value)) return {};
    return Object.fromEntries(Object.entries(value).slice(0, 64).filter((entry): entry is [string, string] => typeof entry[1] === 'string' && entry[1].length <= 4096));
  } catch { return {}; }
};
const sessionTraceStateParams = ['turn_key', 'event_id', 'trace_from', 'trace_to', 'trace_group', 'trace_view', 'trace_pane', 'trace_expanded', 'trace_usage_expanded', 'trace_cursor', 'trace_branch_cursors', 'trace_q', 'trace_request_no', 'trace_compare', 'trace_locate'] as const;
const SelectedTurnSummary = ({ turn }: { turn: AnalysisTurn }) => {
  const preview = previewView(turn.preview);
  const metrics = turnMetrics(turn);
  return <section aria-label="선택 턴 지표" className="space-y-1 border-b border-border-subtle px-3 py-2 text-[11px]">
    <p className="flex min-w-0 items-baseline gap-2"><span className="shrink-0 font-medium text-ink-2">{turn.request_no != null ? `선택 요청 #${turn.request_no}` : `선택 턴 #${turn.no}`}</span><span className="min-w-0 truncate text-ink" title={preview.text}>{preview.text}</span></p>
    <p className="flex flex-wrap gap-x-3 gap-y-1 text-ink-3" title={metrics.title}>
      <span>API 호출 {formatCount(turn.api_requests)}</span><span>도구 {formatCount(turn.tools.calls)}</span><span>하위 에이전트 {formatCount(turn.tools.subagents)}</span>
      {metrics.parts.map(part => <span key={part.key} className={part.unknown ? 'underline decoration-dotted underline-offset-2' : 'tabular-nums'}>{part.text}</span>)}
    </p>
  </section>;
};

interface SourceActionsProps {
  record: SessionRecord;
  onInspect?: (target: InspectionTarget) => void;
  onShowInConversation?: (target: InspectionTarget) => void;
}
interface SourceCallActionProps { target: InspectionTarget; label: string; onInspect: (target: InspectionTarget) => void }
const SourceCallAction = ({ target, label, onInspect }: SourceCallActionProps) => {
  const inspect = () => onInspect(target);
  return <button type="button" onClick={inspect} className="rounded border border-border px-2 py-1 text-[11px] text-ink-2 hover:bg-surface-sunk">{label}</button>;
};
// Trace IDs and source_ref are opaque: only returned source records can provide
// conversation anchors, and only their actual call IDs can choose a tool block.
const SourceActions = ({ record, onInspect, onShowInConversation }: SourceActionsProps) => {
  const anchor = record.view?.anchor_id || record.uuid;
  if (!anchor) return null;
  const target = { recordId: anchor, sourceFile: record.source_file };
  const label = record.view?.kind === 'tool_result' ? '도구 결과' : record.view?.kind === 'tool_call' ? '도구 입력' : record.view?.role === 'assistant' ? '응답' : record.view?.role === 'user' ? '입력' : '수집 기록';
  const inspect = () => onInspect?.(target);
  const showConversation = () => onShowInConversation?.(target);
  const visible = record.view && record.view.kind !== 'hidden' && record.view.kind !== 'reasoning';
  const calls = dedupeKeys((record.view?.tool_calls ?? []).filter(call => call.id).map(call => ({ key: `input:${call.id}`, id: call.id!, label: `${call.name || '도구'} 입력 검사` })));
  const results = dedupeKeys((record.view?.tool_results ?? []).filter(result => result.id && !result.inferred).map(result => ({ key: `result:${result.id}`, id: result.id!, label: '도구 결과 검사' })));
  return <div className="flex basis-full flex-wrap items-center gap-1.5">
    {record.source_file && <code className="max-w-36 truncate text-[10px] text-ink-3" title={record.source_file}>{record.source_file}</code>}
    {onInspect && calls.length + results.length === 0 && <button type="button" onClick={inspect} className="rounded border border-border px-2 py-1 text-[11px] text-ink-2 hover:bg-surface-sunk">{label} 기록 검사</button>}
    {onInspect && [...calls, ...results].map(call => <SourceCallAction key={call.key} target={{ ...target, callId: call.id }} label={call.label} onInspect={onInspect} />)}
    {onShowInConversation && visible && <button type="button" onClick={showConversation} className="rounded border border-border px-2 py-1 text-[11px] text-ink-2 hover:bg-surface-sunk">{label} 대화에서 보기</button>}
  </div>;
};

// Retrieval boundaries include the final observed point; they never claim an execution duration.
const turnRange = (turn: AnalysisTurn | undefined, turns: AnalysisTurn[], lastActivity?: string | null): TraceRange | undefined => {
  if (!turn || !Number.isFinite(Date.parse(turn.anchor_ts))) return;
  const start = Date.parse(turn.anchor_ts);
  const next = turns.find(candidate => Date.parse(candidate.anchor_ts) > start)?.anchor_ts;
  const end = Date.parse(turn.ended_at || next || lastActivity || '');
  return { from: turn.anchor_ts, to: new Date(Number.isFinite(end) && end >= start ? end + (turn.ended_at || !next ? 1 : 0) : start + 1).toISOString() };
};
interface WorkspaceSessionProps {
  identity: TraceIdentity;
  enabled: boolean;
  traceOnly?: boolean;
  inspectionPanel?: ReactNode;
  inspectionActive?: boolean;
  onSelectNode?: () => void;
  onInspect?: (target: InspectionTarget) => void;
  onShowInConversation?: (target: InspectionTarget) => void;
}
const firstTurn = (analysis?: SessionAnalysis) => {
  if (analysis?.state !== 'ready' && analysis?.state !== 'stale') return;
  const turns = [...analysis.turns, ...analysis.abandoned].sort((a, b) => a.no - b.no);
  return turns.find(turn => turn.is_request) ?? turns[0];
};
const WorkspaceSession = ({ identity, enabled, traceOnly = false, inspectionPanel, inspectionActive = false, onSelectNode, onInspect, onShowInConversation }: WorkspaceSessionProps) => {
  const router = useRouter(), pathname = usePathname(), params = useSearchParams(), client = useQueryClient();
  const selectionMatchesSession = params.get('session_id') === identity.sessionId;
  const sessionSelection = selectionMatchesSession ? params : null;
  const initialSelectionAllowed = !params.get('session_id') || selectionMatchesSession;
  const nextUrlParams = () => {
    const next = new URLSearchParams(params.toString());
    if (!selectionMatchesSession) {
      next.set('session_id', identity.sessionId);
      for (const key of sessionTraceStateParams) next.delete(key);
    }
    return next;
  };
  const visible = usePageVisible(), active = enabled && visible;
  const [now, setNow] = useState(() => Date.now());
  const [dragging, setDragging] = useState(false);
  const [contextOpen, setContextOpen] = useState(true);
  const key = ['session-analysis', identity.userId, identity.access, identity.scope?.profileEmail ?? '', identity.scope?.loginEmail ?? '', identity.scope?.userID ?? '', identity.sessionId];
  const analysisKeyString = JSON.stringify(key);
  const explicitRange = validRange(sessionSelection?.get('trace_from'), sessionSelection?.get('trace_to'));
  const rangeOnly = !!explicitRange && !sessionSelection?.get('turn_key') && !sessionSelection?.get('event_id');
  const [pinnedTurn, setPinnedTurn] = useState<string | null>(() => sessionSelection?.get('turn_key') ?? (sessionSelection?.get('event_id') || rangeOnly ? '' : firstTurn(client.getQueryData<SessionAnalysis>(key))?.key ?? null));
  const eventId = sessionSelection?.get('event_id') || undefined, turnKey = sessionSelection?.get('turn_key') || (rangeOnly ? undefined : pinnedTurn || undefined);
  const traceViewMode = sessionSelection?.get('trace_view') === 'timeline' ? 'timeline' : 'tree';
  const traceNodesView: TraceNodesView = 'flow';
  const analysis = useQuery({
    queryKey: key, queryFn: ({ signal }) => fetchSessionAnalysis(identity.sessionId, identity.scope, signal),
    enabled: active && !dragging && !traceOnly, retry: false,
    refetchInterval: query => {
      if (!active || dragging || traceOnly) return false;
      const data = query.state.data;
      const record = data && 'record' in data ? data.record : undefined;
      if (data?.analysis_pending || data?.state === 'building' || data?.state === 'stale'
        || record?.state === 'generating' || record?.state === 'stale' || record?.reason === 'queued') return POLL_LIVE;
      return data && 'live' in data && data.live?.instances.some(instance => !instance.stale) ? POLL_NORMAL : false;
    },
  });
  const inaccessible = analysis.error instanceof SessionAnalysisRequestError && [401, 403, 404].includes(analysis.error.status);
  const data = !inaccessible && analysis.data?.session_id === identity.sessionId ? analysis.data : undefined;
  const full = data?.state === 'ready' || data?.state === 'stale' ? data : undefined;
  const turns = [...full?.turns ?? [], ...full?.abandoned ?? []].sort((a, b) => a.no - b.no);
  const selectedTurn = turns.find(turn => turn.key === turnKey);
  const range = explicitRange ?? turnRange(selectedTurn, turns, data?.header?.last_activity_at);
  const recordRegeneration = useSessionRecordRegeneration(identity.userId, identity.sessionId, full?.record?.state);
  const running = full?.live?.instances.some(instance => !instance.stale) ?? false;
  const canonical = !!turnKey && !eventId;
  const trace = useSessionTrace({ identity, enabled: enabled && !inaccessible && !traceOnly, range, eventId, dragging, running, contextEnabled: !traceOnly && contextOpen && !!eventId });
  const original = useTurnContext({ identity, turnKey, revision: data?.turn_context_revision, enabled: active && contextOpen && !inaccessible && canonical && !traceOnly });
  const nodeDetail = useTraceNodeDetail({ identity, enabled: traceOnly && active && !inspectionActive, nodeId: eventId, options: { requestParts: 'page', compareTo: sessionSelection?.get('trace_compare') || undefined } });
  const traceWorkspaceRef = useRef<HTMLDivElement>(null);
  const readingPositions = useRef(new Map<string, number>());
  const readingDisclosures = useRef(new Map<string, boolean[]>());
  const rememberTraceReading = (event: UIEvent<HTMLDivElement>) => {
    if (!traceOnly || !eventId || nodeDetail.isRefetching || !(event.target instanceof HTMLElement)) return;
    const target = event.target;
    if (target.id === 'trace-node-panel-content') readingPositions.current.set(eventId, target.scrollTop);
    else if (target.tagName === 'PRE') {
      const blocks = [...(traceWorkspaceRef.current?.querySelectorAll('#trace-node-panel-content pre') ?? [])];
      const index = blocks.indexOf(target);
      if (index >= 0) readingPositions.current.set(`${eventId}:pre:${index}`, target.scrollTop);
    }
  };
  const autoExpandedUsageNode = useRef<string | undefined>(undefined);
  const events = inaccessible || trace.updating || trace.error ? [] : range ? trace.events.data?.pages.flatMap(page => page.events) ?? [] : trace.sample.data?.events ?? [];
  const selected = events.find(event => event.event_id === eventId);
  const setSelection = (changes: Record<string, string | undefined>) => {
    const next = nextUrlParams();
    for (const [key, value] of Object.entries(changes)) { if (value) next.set(key, value); else next.delete(key); }
    router.replace(`${pathname}?${next}`, { scroll: false });
  };
  const setTraceView = (mode: 'tree' | 'timeline') => {
    if (mode === traceViewMode) return;
    const next = nextUrlParams();
    next.set('trace_view', mode);
    next.delete('trace_pane');
    router.push(`${pathname}?${next}`, { scroll: false });
  };
  const setTraceNodesView = () => undefined;
  const timingGroup = (traceViewMode === 'timeline' ? sessionSelection?.get('trace_group') : undefined) || nodeDetail.data?.navigation?.group_id || nodeDetail.data?.node.group_id
    || (nodeDetail.data?.node.kind === 'turn' ? nodeDetail.data.node.node_id : undefined);
  const openTiming = () => {
    if (!timingGroup) return;
    const next = nextUrlParams();
    next.set('trace_group', timingGroup); next.set('trace_view', 'timeline'); next.delete('trace_pane');
    next.delete('trace_from'); next.delete('trace_to');
    router.push(`${pathname}?${next}`, { scroll: false });
  };
  const closeTiming = () => setTraceView('tree');
  const setTimingRange = (next: TraceRange | undefined) => setSelection({ trace_from: next?.from, trace_to: next?.to });
  const pinInitialTraceNode = (nodeId: string) => setSelection({ event_id: nodeId, detail: 'trace' });
  const searchTrace = (query: string) => setSelection({ trace_q: query, trace_cursor: undefined, trace_request_no: undefined });
  const jumpRequest = (number: number) => setSelection({ trace_request_no: String(number), trace_cursor: undefined, trace_q: undefined });
  const resetTraceCursors = () => setSelection({ trace_cursor: undefined, trace_branch_cursors: undefined, trace_request_no: undefined });
  const pageRequests = (cursor: string) => setSelection({ trace_cursor: cursor });
  const loadInputPage = () => { void nodeDetail.fetchNextPage({ cancelRefetch: false }); };
  const compareRequest = (_nodeId: string, previousRequestId?: string) => setSelection({ trace_compare: previousRequestId });
  const openSource = (sourceRef: string) => {
    const location = nodeDetail.data?.source_locations?.find(item => item.source_ref === sourceRef && item.node_id);
    if (!location?.node_id) return;
    const next = nextUrlParams();
    next.set('event_id', location.node_id); next.delete('trace_compare');
    router.push(`${pathname}?${next}`, { scroll: false });
  };
  const locateSelected = () => {
    const navigation = nodeDetail.data?.navigation;
    if (!navigation?.root_node_id) return;
    const next = nextUrlParams();
    next.delete('trace_q'); next.delete('trace_request_no'); next.set('trace_view', 'tree');
    if (navigation.root_cursor) next.set('trace_cursor', navigation.root_cursor); else next.delete('trace_cursor');
    for (const id of [navigation.group_id, ...navigation.ancestor_ids]) if (id && id !== eventId && !next.getAll('trace_expanded').includes(id)) next.append('trace_expanded', id);
    if (navigation.child_cursors) next.set('trace_branch_cursors', JSON.stringify(navigation.child_cursors)); else next.delete('trace_branch_cursors');
    if (eventId) next.set('trace_locate', eventId);
    next.set('trace_pane', 'tree');
    router.replace(`${pathname}?${next}`, { scroll: false });
  };
  const showRequestTree = () => setSelection({ trace_pane: 'tree' });
  const toggleExpandedNode = (nodeId: string) => {
    const next = nextUrlParams();
    const expanded = next.getAll('trace_expanded');
    next.delete('trace_expanded');
    for (const id of expanded) if (id !== nodeId) next.append('trace_expanded', id);
    if (!expanded.includes(nodeId)) next.append('trace_expanded', nodeId);
    router.replace(`${pathname}?${next}`, { scroll: false });
  };
  const toggleUsageGroup = (groupId: string) => {
    const next = nextUrlParams();
    const expanded = next.getAll('trace_usage_expanded');
    next.delete('trace_usage_expanded');
    for (const id of expanded) if (id !== groupId) next.append('trace_usage_expanded', id);
    if (!expanded.includes(groupId)) next.append('trace_usage_expanded', groupId);
    router.replace(`${pathname}?${next}`, { scroll: false });
  };
  const selectTraceNode = (nodeId: string) => {
    setPinnedTurn(''); setContextOpen(true);
    setSelection({ detail: 'trace', event_id: nodeId, turn_key: undefined, trace_compare: undefined, trace_pane: traceViewMode === 'timeline' ? 'detail' : undefined, ...(traceViewMode === 'tree' ? { trace_group: undefined, trace_from: undefined, trace_to: undefined } : {}) });
    onSelectNode?.();
  };
  const selectEvent = (event: TraceEvent) => {
    setPinnedTurn(event.turn_key ?? ''); setContextOpen(true);
    const turn = turns.find(turn => turn.key === event.turn_key);
    const next = range && events.some(candidate => candidate.event_id === event.event_id) ? range : turnRange(turn, turns, data?.header?.last_activity_at);
    setSelection({ event_id: event.event_id, turn_key: event.turn_key ?? undefined, ...(next ? { trace_from: next.from, trace_to: next.to } : {}) });
  };
  const selectRange = (next: TraceRange | undefined) => {
    setPinnedTurn('');
    setSelection({ trace_from: next?.from, trace_to: next?.to, event_id: undefined, turn_key: undefined });
  };
  const selectTurn = (turn: AnalysisTurn) => {
    setPinnedTurn(turn.key); setContextOpen(true);
    const next = turnRange(turn, turns, data?.header?.last_activity_at);
    setSelection({ turn_key: turn.key, event_id: undefined, trace_from: next?.from, trace_to: next?.to });
  };
  const closeContext = () => setContextOpen(false);
  const openContext = () => setContextOpen(true);
  const retryAnalysis = () => void analysis.refetch();
  const loadEvents = () => void trace.events.fetchNextPage({ cancelRefetch: false });
  const loadContext = () => void (canonical ? original : trace.context).fetchNextPage({ cancelRefetch: false });
  const retryContext = () => void (canonical ? original : trace.context).refetch();
  const retryNodeDetail = () => void nodeDetail.refetch();
  const records = inaccessible ? [] : canonical ? original.records : trace.updating || trace.context.isError ? [] : trace.context.data?.pages.flatMap(page => page.records) ?? [];
  const context = canonical ? original : trace.context;
  const renderSourceActions = (record: SessionRecord) => traceOnly && (onInspect || onShowInConversation)
    ? <SourceActions record={record} onInspect={onInspect} onShowInConversation={onShowInConversation} /> : null;
  const flow = <>
    {traceOnly && selectedTurn && <SelectedTurnSummary turn={selectedTurn} />}
    <TraceChart overview={inaccessible || trace.updating || trace.error ? undefined : trace.overview.data} events={events} overviewEvents={inaccessible ? [] : trace.sample.data?.events}
      turns={turns} selectedTurnKey={turnKey} onTurn={selectTurn} range={range ?? undefined} selectedId={eventId} agent={data?.header?.agent}
      rangeState={trace.events.data?.pages[0]?.state ?? trace.rangeState} rangeCoverage={trace.events.data?.pages[0]?.coverage}
      onSelect={selectEvent} onRange={selectRange} onDragging={setDragging} />
    {trace.updating && <p role="status" className="px-2 text-[11px] text-ink-3">기록이 갱신되어 현재 구간을 다시 준비 중입니다.</p>}
    {trace.sample.data?.next_cursor && <p role="status" className="px-2 text-[10px] text-ink-3">전체 밀도 위의 실행 표식은 먼저 읽은 사건입니다. 선택 턴과 구간의 나머지 사건은 더 읽을 수 있습니다.</p>}
    {trace.error && <p role="alert" className="flex items-center gap-2 px-2 py-1 text-xs text-ink-3">실행 기록을 읽지 못했습니다.
      <button type="button" onClick={trace.retry} className="text-brand underline">다시 시도</button>
    </p>}
    {range && trace.events.isFetching && <p role="status" className="px-2 text-[11px] text-ink-3">선택 구간을 읽는 중입니다.</p>}
    {trace.events.hasNextPage && <button type="button" onClick={loadEvents} disabled={trace.events.isFetchingNextPage} className="m-2 rounded border border-border px-2 py-1 text-xs text-ink-2">사건 더 읽기</button>}
    {!contextOpen && <button type="button" onClick={openContext} className="m-2 text-xs text-ink-2">선택 원문 열기</button>}
  </>;

  /** The first successful external analysis read pins one request; refreshing its head never moves the selection. */
  useEffect(() => {
    return client.getQueryCache().subscribe(event => {
      if (traceOnly || eventId || rangeOnly || event.type !== 'updated' || event.action.type !== 'success' || JSON.stringify(event.query.queryKey) !== analysisKeyString) return;
      const turn = firstTurn(event.query.state.data as SessionAnalysis | undefined);
      if (turn) setPinnedTurn(current => current ?? turn.key);
    });
  }, [traceOnly, eventId, rangeOnly, analysisKeyString, client]);
  /** The visible clock starts a fresh tick on activation and stops every timer while hidden. */
  useEffect(() => {
    if (!active || traceOnly) return;
    const tick = () => setNow(Date.now());
    const first = window.setTimeout(tick, 0);
    const timer = window.setInterval(tick, POLL_NORMAL);
    return () => { window.clearTimeout(first); window.clearInterval(timer); };
  }, [active, traceOnly]);
  /** Hidden analysis and pointer interactions cancel only this identity's reads, retaining its completed cache. */
  useEffect(() => {
    if (!active || dragging || traceOnly) void client.cancelQueries({ queryKey: JSON.parse(analysisKeyString), exact: true });
    return () => { void client.cancelQueries({ queryKey: JSON.parse(analysisKeyString), exact: true }); };
  }, [active, dragging, traceOnly, analysisKeyString, client]);
  const queryString = sessionSelection?.toString() ?? '';
  /** A direct usage link reveals only its observed parent branch and source group. */
  useEffect(() => {
    const selectedNode = nodeDetail.data?.node;
    if (!traceOnly || !eventId || selectedNode?.node_id !== eventId) {
      autoExpandedUsageNode.current = undefined;
      return;
    }
    if (selectedNode.kind !== 'usage') {
      autoExpandedUsageNode.current = undefined;
      return;
    }
    if (autoExpandedUsageNode.current === selectedNode.node_id) return;
    autoExpandedUsageNode.current = selectedNode.node_id;
    const groupId = selectedNode.parent_id ?? 'root';
    const next = new URLSearchParams(queryString);
    let changed = false;
    if (selectedNode.parent_id && !next.getAll('trace_expanded').includes(selectedNode.parent_id)) {
      next.append('trace_expanded', selectedNode.parent_id);
      changed = true;
    }
    if (!next.getAll('trace_usage_expanded').includes(groupId)) {
      next.append('trace_usage_expanded', groupId);
      changed = true;
    }
    if (changed) router.replace(`${pathname}?${next}`, { scroll: false });
  }, [traceOnly, eventId, nodeDetail.data?.node, queryString, pathname, router]);

  /** Native toggle capture preserves disclosures before protected content remounts. */
  useEffect(() => {
    const workspace = traceWorkspaceRef.current;
    if (!workspace || !traceOnly || !eventId) return;
    const remember = (event: Event) => {
      if (nodeDetail.isRefetching || !(event.target instanceof HTMLDetailsElement)) return;
      const details = [...workspace.querySelectorAll<HTMLDetailsElement>('[aria-label="선택 노드 상세"] details')];
      if (details.includes(event.target)) readingDisclosures.current.set(eventId, details.map(item => item.open));
    };
    workspace.addEventListener('toggle', remember, true);
    return () => workspace.removeEventListener('toggle', remember, true);
  }, [traceOnly, eventId, nodeDetail.isRefetching]);

  /** Reauthorization remounts protected content; keep the user's reading
   * position for the same selected node and its already-read input blocks. */
  useEffect(() => {
    if (!traceOnly || !eventId || !nodeDetail.data || nodeDetail.data.node.node_id !== eventId) return;
    const disclosures = readingDisclosures.current.get(eventId);
    traceWorkspaceRef.current?.querySelectorAll<HTMLDetailsElement>('[aria-label="선택 노드 상세"] details').forEach((item, index) => {
      if (disclosures?.[index] !== undefined) item.open = disclosures[index];
    });
    const panel = traceWorkspaceRef.current?.querySelector<HTMLElement>('#trace-node-panel-content');
    if (panel) panel.scrollTop = readingPositions.current.get(eventId) ?? 0;
    const blocks = traceWorkspaceRef.current?.querySelectorAll<HTMLElement>('#trace-node-panel-content pre');
    blocks?.forEach((block, index) => { block.scrollTop = readingPositions.current.get(`${eventId}:pre:${index}`) ?? 0; });
  }, [traceOnly, eventId, nodeDetail.data]);

  return <div ref={traceWorkspaceRef} onScrollCapture={rememberTraceReading} className="@container/trace flex h-full min-h-0 min-w-0 flex-col" data-trace-workspace="true">
    <div className="flex min-h-0 flex-1 flex-col gap-2 @[1100px]/trace:flex-row">
      {traceOnly ? <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-2 overflow-hidden p-2 @[800px]/trace:flex-row" data-trace-pane-inset="true">
        <div className={cn('min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-lg border border-border-subtle bg-surface @[800px]/trace:flex @[800px]/trace:grow-0 @[800px]/trace:basis-[34%]', eventId && (sessionSelection?.get('trace_pane') === 'detail' || sessionSelection?.get('trace_pane') !== 'tree' && traceViewMode !== 'timeline') ? 'hidden' : 'flex', traceViewMode === 'timeline' && '@[800px]/trace:basis-[52%]')}>
          <div hidden={traceViewMode !== 'tree'} className="flex min-h-0 flex-1 flex-col">
            <TraceExecutionView identity={identity} enabled={active && traceViewMode === 'tree'} selectedNodeId={eventId} viewMode="tree"
              nodesView={traceNodesView} expandedNodeIds={sessionSelection?.getAll('trace_expanded') ?? []} expandedUsageGroupIds={sessionSelection?.getAll('trace_usage_expanded') ?? []}
              query={sessionSelection?.get('trace_q') || ''} rootCursor={sessionSelection?.get('trace_cursor') || undefined} observedStamp={nodeDetail.data?.navigation} branchCursors={traceBranchCursors(sessionSelection?.get('trace_branch_cursors') ?? null)}
              revealNodeId={sessionSelection?.get('trace_locate') || undefined} onResetCursors={resetTraceCursors} onSearch={searchTrace}
              requestNo={Number(sessionSelection?.get('trace_request_no')) || undefined} onJumpRequest={jumpRequest} onRootCursor={pageRequests} onInitialNode={inspectionActive || !initialSelectionAllowed ? undefined : pinInitialTraceNode}
              onViewModeChange={setTraceView} onNodesViewChange={setTraceNodesView}
              onSelectNode={selectTraceNode} onToggleNode={toggleExpandedNode} onToggleUsageGroup={toggleUsageGroup} />
          </div>
          {traceViewMode === 'timeline' && <TraceTimingView identity={identity} enabled={active} groupId={timingGroup} selectedNodeId={eventId}
            range={explicitRange ?? undefined} onRange={setTimingRange} onSelectNode={selectTraceNode} onClose={closeTiming} />}
        </div>
        {inspectionPanel !== undefined && <aside aria-label="기록 검사" hidden={!inspectionActive} className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-lg border border-border-subtle bg-surface">
          {inspectionPanel}
        </aside>}
        <div hidden={inspectionActive} className={cn('min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-lg border border-border-subtle bg-surface @[800px]/trace:flex', (!eventId || sessionSelection?.get('trace_pane') === 'tree' || traceViewMode === 'timeline' && sessionSelection?.get('trace_pane') !== 'detail') ? 'hidden' : 'flex')}>
          {eventId && <nav aria-label="선택 항목 탐색" className="flex shrink-0 items-center gap-3 border-b border-border-subtle px-4 py-2 text-[11px] text-ink-3">
            <button type="button" onClick={showRequestTree} className="@[800px]/trace:hidden">{traceViewMode === 'timeline' ? '시간 분석으로' : '요청 목록'}</button>
            {nodeDetail.data?.navigation?.root_node_id && <button type="button" onClick={locateSelected} className="hover:text-ink">트리에서 찾기</button>}
          </nav>}
          <TraceNodeInspector selectedNodeId={eventId} detail={nodeDetail.data} loading={nodeDetail.isPending || nodeDetail.isRefetching && !nodeDetail.isFetchingNextPage} error={nodeDetail.isError} onRetry={retryNodeDetail}
            onOpenTiming={openTiming} timingDisabledReason={timingGroup ? undefined : '연결된 요청 범위가 없습니다.'}
            onLoadInputPage={loadInputPage} inputPageLoading={nodeDetail.isFetchingNextPage} inputPageError={nodeDetail.isFetchNextPageError}
            onCompareRequest={compareRequest} onOpenSource={openSource} recordActions={renderSourceActions} />
        </div>
      </div> : <section aria-label="상세 분석" className="flex min-h-0 min-w-0 flex-1 flex-col">
        {data ? <SessionAnalysisTab analysis={data} now={now} executionFlow={flow} selectedTurnKey={turnKey}
          onOpenRequest={selectTurn} openTarget="waterfall" recordControlsEnabled={active && data.owner_view}
          onRegenerateRecord={data.owner_view ? recordRegeneration.regenerate : undefined}
          recordActionPending={recordRegeneration.pending} recordActionAccepted={recordRegeneration.accepted}
          recordActionAcceptedMessage={recordRegeneration.acceptedMessage} recordActionError={recordRegeneration.error} />
          : <div className="min-h-0 overflow-auto">{flow}<p role={analysis.isError ? 'alert' : 'status'} className="p-3 text-xs text-ink-3">{inaccessible ? '이 세션의 분석이 없거나 볼 권한이 없습니다.' : analysis.isError ? '분석을 읽지 못했습니다.' : '분석을 불러오는 중입니다.'}{analysis.isError && <button type="button" onClick={retryAnalysis} className="ml-2 text-brand underline">다시 시도</button>}</p></div>}
      </section>}
      {!traceOnly && contextOpen && <aside aria-label="선택 원문" className="flex max-h-[45%] min-h-48 min-w-0 shrink-0 flex-col overflow-hidden rounded-xl border border-border-subtle bg-surface @[1100px]/trace:max-h-none @[1100px]/trace:w-96 @[1400px]/trace:w-[440px]">
        {selected && <div className="shrink-0 border-b border-border-subtle px-3 py-2 text-[11px] text-ink-2">{selected.label} · {selected.status}<p className="mt-1 font-mono text-[10px] text-ink-3">{selected.evidence.join(', ') || '추가 근거 없음'}{selected.parent_event_id ? ` · 상위 사건 ${selected.parent_event_id}` : ''}</p></div>}
        <TraceContextPane key={eventId ?? turnKey ?? 'none'} records={records} loading={canonical ? original.loading || original.updating : !!eventId && (trace.context.isPending || trace.updating)}
          state={canonical ? original.state : undefined} updating={canonical ? original.updating : trace.updating}
          selected={!!(turnKey || eventId)} loadError={canonical ? !!original.error : trace.context.isError && !trace.updating}
          recordActions={traceOnly ? renderSourceActions : undefined}
          onRetry={retryContext} onClose={closeContext} />
        {context.hasNextPage && <button type="button" onClick={loadContext} disabled={context.isFetchingNextPage} className="shrink-0 border-t border-border px-3 py-2 text-xs text-ink-2">원문 더 읽기</button>}
      </aside>}
    </div>
  </div>;
};

interface SessionTraceWorkspaceProps {
  sessionId: string;
  scope?: TraceScope;
  enabled?: boolean;
  inspectionPanel?: ReactNode;
  inspectionActive?: boolean;
  onSelectNode?: () => void;
  onInspect?: (target: InspectionTarget) => void;
  onShowInConversation?: (target: InspectionTarget) => void;
}
interface SessionAnalysisWorkspaceProps extends SessionTraceWorkspaceProps { traceOnly?: boolean }
const SessionAnalysisWorkspace = ({ sessionId, scope, enabled = true, traceOnly = false, inspectionPanel, inspectionActive, onSelectNode, onInspect, onShowInConversation }: SessionAnalysisWorkspaceProps) => {
  const { user, isAdmin } = useAuth();
  const { selectedAccount } = useAccount();
  if (!user || !sessionId) return <p role="status" className="p-4 text-sm text-ink-3">분석할 세션을 목록에서 선택하세요.</p>;
  const identity: TraceIdentity = { userId: user.id, access: isAdmin ? 'admin' : 'owner', sessionId,
    scope: scope ?? (isAdmin ? { loginEmail: selectedAccount || undefined } : undefined) };
  return <WorkspaceSession key={JSON.stringify(identity)} identity={identity} enabled={enabled} traceOnly={traceOnly} inspectionPanel={inspectionPanel}
    inspectionActive={inspectionActive} onSelectNode={onSelectNode} onInspect={onInspect} onShowInConversation={onShowInConversation} />;
};
const SessionTraceWorkspace = (props: SessionTraceWorkspaceProps) => <SessionAnalysisWorkspace {...props} traceOnly />;

export { SessionAnalysisWorkspace, SessionTraceWorkspace };
export type { SessionAnalysisWorkspaceProps, SessionTraceWorkspaceProps };
