'use client';

import { useEffect, useRef, useState, useSyncExternalStore, type FormEvent } from 'react';
import { ChevronDown, ChevronRight, Search, GitBranch, ListTree, TerminalSquare } from 'lucide-react';
import { cn } from '@/lib/utils';
import { traceNodesKey, useTraceNodes } from '@/lib/use-trace-nodes';
import type { TraceNode, TraceNodeQueryIdentity, TraceNodesView, TraceNavigation } from '@/lib/session-trace-node-types';
import { formatObservedDuration, traceNodeAvailabilityLabel, traceNodeKindLabel, traceNodeStatusLabel, traceNodeStatusTone, traceNodeTimeLabel } from './trace-node-format';
import { traceTurnGroupingInfo } from './trace-node-presentation';

const createPolicyEpochStore = (initialEpoch?: number) => {
  let epoch = initialEpoch;
  const listeners = new Set<() => void>();
  return {
    getSnapshot: () => epoch,
    subscribe: (listener: () => void) => {
      listeners.add(listener);
      return () => { listeners.delete(listener); };
    },
    observe: (observedEpoch: number) => {
      const nextEpoch = Math.max(epoch ?? observedEpoch, observedEpoch);
      if (nextEpoch === epoch) return;
      epoch = nextEpoch;
      for (const listener of listeners) listener();
    },
  };
};

interface TraceExecutionViewProps {
  identity: TraceNodeQueryIdentity;
  enabled: boolean;
  requestNo?: number;
  onJumpRequest?: (requestNo: number) => void;
  onRootCursor?: (cursor: string) => void;
  query?: string;
  rootCursor?: string;
  observedStamp?: Pick<TraceNavigation, 'revision' | 'epoch'>;
  branchCursors?: Readonly<Record<string, string>>;
  revealNodeId?: string;
  onResetCursors?: () => void;
  onInitialNode?: (id: string) => void;
  onSearch?: (value: string) => void;
  nodesView: TraceNodesView;
  selectedNodeId?: string;
  viewMode: 'tree' | 'timeline';
  expandedNodeIds: readonly string[];
  expandedUsageGroupIds: readonly string[];
  onViewModeChange: (mode: 'tree' | 'timeline') => void;
  onNodesViewChange: (view: TraceNodesView) => void;
  onSelectNode: (nodeId: string) => void;
  onToggleNode: (nodeId: string) => void;
  onToggleUsageGroup: (groupId: string) => void;
}

interface TraceNodeBranchProps extends Omit<TraceExecutionViewProps, 'viewMode' | 'onViewModeChange' | 'onNodesViewChange'> {
  parentId?: string;
  parentTitle?: string;
  depth: number;
  root?: boolean;
  rootLabel?: string;
}

interface TraceNodeRowProps {
  node: TraceNode;
  selected: boolean;
  expanded: boolean;
  depth: number;
  reveal?: boolean;
  onSelect: (nodeId: string) => void;
  onToggle: (nodeId: string) => void;
}

const TraceNodeRow = ({ node, selected, expanded, depth, onSelect, onToggle, reveal }: TraceNodeRowProps) => {
  const select = () => onSelect(node.node_id);
  const toggle = () => onToggle(node.node_id);
  const duration = formatObservedDuration(node);
  const statusLabel = traceNodeStatusLabel(node.status);
  const exceptionalStatus = node.status === 'error' || node.status === 'failed';
  const exceptionalAvailability = ['redacted', 'withheld', 'truncated'].includes(node.availability)
    || node.availability === 'error' && !exceptionalStatus;
  const grouping = traceTurnGroupingInfo(node);
  const verifiedNo = node.request_no?.state === 'verified' ? node.request_no.value : undefined;
  const verifiedTools = node.tool_count?.state === 'verified' ? node.tool_count.value : undefined;
  const timestamp = node.timestamp ?? node.started_at ?? node.ended_at;
  const hasTimestamp = !!timestamp && Number.isFinite(Date.parse(timestamp));
  const padding = depth === 0 ? 'pl-2' : depth === 1 ? 'pl-5' : depth === 2 ? 'pl-8' : depth === 3 ? 'pl-11' : 'pl-14';
  const rowRef = useRef<HTMLDivElement>(null);
  /** Explicit source-location navigation reveals its selected row once it arrives. */
  useEffect(() => { if (selected && reveal) rowRef.current?.scrollIntoView?.({ block: 'nearest' }); }, [selected, reveal]);
  return <div ref={rowRef} className={cn('flex min-h-10 min-w-0 items-center gap-1 border-b border-border-subtle pr-2', padding, selected && 'bg-surface-sunk')} data-node-row={node.node_id}>
    {node.has_children ? <button type="button" aria-label={`${node.title} 하위 실행 ${expanded ? '접기' : '펼치기'}`} aria-expanded={expanded} onClick={toggle}
      className="flex h-7 w-7 shrink-0 items-center justify-center rounded text-ink-3 hover:bg-surface-sunk focus-visible:ring-1 focus-visible:ring-accent">
      {expanded ? <ChevronDown aria-hidden="true" size={14} /> : <ChevronRight aria-hidden="true" size={14} />}
    </button> : <span aria-hidden="true" className="w-7 shrink-0" />}
    <button type="button" aria-label={node.title} aria-pressed={selected} onClick={select}
      className="flex min-w-0 flex-1 items-center gap-2 rounded-sm py-1 text-left focus-visible:ring-1 focus-visible:ring-accent">
      <span className="hidden shrink-0 items-center gap-1 rounded border border-border px-1.5 py-0.5 text-[10px] text-ink-3 sm:inline-flex">
        {node.kind === 'tool' ? <TerminalSquare aria-hidden="true" size={11} /> : node.kind === 'agent' ? <GitBranch aria-hidden="true" size={11} /> : <ListTree aria-hidden="true" size={11} />}
        {grouping?.label ?? traceNodeKindLabel(node.kind)}
      </span>
      {grouping && <span role="note" aria-label={grouping.description} title={grouping.description} className="shrink-0 text-[10px] text-ink-3">ⓘ</span>}
      {verifiedNo !== undefined && <span className="shrink-0 font-mono text-[10px] text-ink-3">#{verifiedNo}</span>}
      <span className="min-w-0 flex-1 truncate text-xs text-ink" title={node.title}>{node.title}</span>
      {exceptionalStatus && <span className={cn('shrink-0 text-[10px]', traceNodeStatusTone(node.status))}>{statusLabel}</span>}
      {exceptionalAvailability && <span aria-label={`자료 상태 ${traceNodeAvailabilityLabel(node.availability)}`} title={`자료 상태 · ${traceNodeAvailabilityLabel(node.availability)}`} className="shrink-0 text-[10px] text-ink-3">{traceNodeAvailabilityLabel(node.availability)}</span>}
      {verifiedTools !== undefined && <span className="shrink-0 text-[10px] text-ink-3">도구 {verifiedTools}</span>}
      {node.child_count !== null && node.has_children && <span className="shrink-0 text-[10px] tabular-nums text-ink-3">{node.child_count}개</span>}
      {hasTimestamp && <span className="hidden shrink-0 font-mono text-[10px] tabular-nums text-ink-3 sm:inline">{traceNodeTimeLabel(timestamp)}</span>}
      {duration && <span className="hidden shrink-0 font-mono text-[10px] tabular-nums text-ink-3 md:inline">{duration}</span>}
    </button>
  </div>;
};

interface UsageRecordGroupProps {
  nodes: TraceNode[];
  groupId: string;
  depth: number;
  rowDepth: number;
  expanded: boolean;
  selectedNodeId?: string;
  onSelectNode: (nodeId: string) => void;
  onToggle: (groupId: string) => void;
}

const UsageRecordGroup = ({ nodes, groupId, depth, rowDepth, expanded, selectedNodeId, onSelectNode, onToggle }: UsageRecordGroupProps) => {
  const toggle = () => onToggle(groupId);
  const padding = depth === 0 ? 'pl-2' : depth === 1 ? 'pl-5' : depth === 2 ? 'pl-8' : depth === 3 ? 'pl-11' : 'pl-14';
  const label = `사용량 기록 · 읽은 범위 ${nodes.length}건`;
  return <li role="none" className="min-w-0 border-b border-border-subtle">
    <div className={cn('flex min-h-10 min-w-0 items-center gap-1 pr-2', padding)}>
      <button type="button" aria-label={label} aria-expanded={expanded} onClick={toggle}
        className="flex min-w-0 flex-1 items-center gap-2 rounded-sm py-1 text-left text-xs text-ink-2 hover:bg-surface-sunk focus-visible:ring-1 focus-visible:ring-accent">
        {expanded ? <ChevronDown aria-hidden="true" size={14} className="shrink-0" /> : <ChevronRight aria-hidden="true" size={14} className="shrink-0" />}
        <span className="truncate">{label}</span>
      </button>
    </div>
    {expanded && <>
      <p className="py-1 pl-11 pr-3 text-[10px] text-ink-3">
        이 수는 읽은 범위의 기록 수이며 전체 합계가 아닙니다.
      </p>
      <ul role="group" aria-label="사용량 원자료" className="min-w-0">
        {nodes.map(node => <li key={node.node_id} role="treeitem" aria-level={rowDepth + 1} aria-selected={selectedNodeId === node.node_id} className="min-w-0">
          <TraceNodeRow node={node} selected={selectedNodeId === node.node_id} expanded={false} depth={rowDepth} onSelect={onSelectNode} onToggle={onToggle} />
        </li>)}
      </ul>
    </>}
  </li>;
};

const TraceNodeBranch = ({ identity, enabled, nodesView, parentId, parentTitle, depth, root = false, query: searchQuery, onInitialNode, requestNo, onJumpRequest, onRootCursor, rootCursor, branchCursors, observedStamp, revealNodeId, onResetCursors, rootLabel, selectedNodeId, expandedNodeIds, expandedUsageGroupIds, onSelectNode, onToggleNode, onToggleUsageGroup }: TraceNodeBranchProps) => {
  const cursor = root ? rootCursor : parentId ? branchCursors?.[parentId] : undefined;
  const query = useTraceNodes({ identity, enabled, parentId, view: nodesView, ...(root && searchQuery ? { query: searchQuery } : {}), ...(cursor ? { cursor } : {}), ...(root && requestNo ? { requestNo } : {}) });
  const usageNodes = (nodesView === 'execution' || nodesView === 'flow') ? query.nodes.filter(node => node.kind === 'usage') : [];
  const nodes = query.nodes.filter(node => node.kind !== 'usage');
  const loadMore = () => { void query.fetchNextPage({ cancelRefetch: false }); };
  const retry = () => {
    if (cursor && (query.error as { status?: number } | null)?.status === 409 && onResetCursors) onResetCursors();
    else if (root && rootCursor && (query.error as { status?: number } | null)?.status === 409 && onRootCursor) onRootCursor('');
    else query.refresh();
  };
  const observedEpoch = observedStamp?.epoch;
  const [policyStore] = useState(() => createPolicyEpochStore(observedEpoch));
  const policyEpoch = useSyncExternalStore(policyStore.subscribe, policyStore.getSnapshot, policyStore.getSnapshot);
  const knownEpoch = observedEpoch === undefined ? policyEpoch : Math.max(observedEpoch, policyEpoch ?? observedEpoch);
  const policyChanged = root && knownEpoch !== undefined && query.meta?.epoch !== undefined && knownEpoch !== query.meta.epoch;
  const sourceChanged = policyChanged || root && observedStamp !== undefined && query.meta?.revision !== undefined && query.meta.epoch !== undefined
    && observedStamp.revision !== query.meta.revision;
  const reloadObserved = () => {
    if (onResetCursors && (rootCursor || requestNo || Object.keys(branchCursors ?? {}).length > 0)) {
      onResetCursors();
      if (!rootCursor && !requestNo) query.refresh();
    } else if (rootCursor && onRootCursor) onRootCursor('');
    else query.refresh();
  };
  const previousPage = () => { if (query.meta?.prev_cursor) onRootCursor?.(query.meta.prev_cursor); };
  const isExpanded = (nodeId: string) => expandedNodeIds.includes(nodeId);
  const usageGroupId = parentId ?? 'root';
  const usageGroupExpanded = expandedUsageGroupIds.includes(usageGroupId);
  const usageRowDepth = parentId ? depth + 1 : depth;
  const currentRootLabel = rootLabel ?? (nodesView === 'context' ? '지시문·설정' : nodesView === 'metadata' ? '세션 기록' : '실행 트리');
  const initialPinned = useRef(false);
  const treeRef = useRef<HTMLUListElement>(null);
  const savedScroll = useRef(0);
  const rememberScroll = () => { if (enabled && !query.isFetching && treeRef.current) savedScroll.current = treeRef.current.scrollTop; };
  /** Reauthorization hides old rows; restore the reading position when the same page is available again. */
  useEffect(() => { if (root && enabled && !query.isFetching && treeRef.current) treeRef.current.scrollTop = savedScroll.current; }, [root, enabled, query.isFetching]);
  /** Keep a policy change observed while the next selected detail temporarily
   * has no navigation during reauthorization. This never starts a query. */
  useEffect(() => {
    if (!root || observedEpoch === undefined) return;
    policyStore.observe(observedEpoch);
  }, [root, observedEpoch, policyStore]);
  /** The first successful root page pins one readable request once; returning
   * from timing or refreshing never replaces the user's selection. */
  useEffect(() => {
    if (!root || !enabled || policyChanged || initialPinned.current || selectedNodeId || !onInitialNode || query.isPending || query.isError) return;
    const first = nodes.find(node => node.kind === 'turn') ?? nodes.find(node => node.kind === 'request') ?? nodes[0];
    if (first) { initialPinned.current = true; onInitialNode(first.node_id); }
  }, [root, enabled, policyChanged, selectedNodeId, onInitialNode, query.isPending, query.isError, nodes]);
  /** Opening a request from the conversation should show its execution, not one
   * collapsed turn row. Expand that turn once when the tree arrives. */
  const turnExpanded = useRef(false);
  useEffect(() => {
    if (!root || !enabled || query.isPending || query.isError || turnExpanded.current) return;
    const turn = nodes.find(node => node.kind === 'turn' && node.has_children);
    if (!turn) return;
    turnExpanded.current = true;
    if (!expandedNodeIds.includes(turn.node_id)) onToggleNode(turn.node_id);
  }, [root, enabled, query.isPending, query.isError, nodes, expandedNodeIds, onToggleNode]);
  return <ul ref={root ? treeRef : undefined} onScroll={root ? rememberScroll : undefined} role={root ? 'tree' : 'group'} aria-label={root ? currentRootLabel : `${parentTitle ?? '선택한 실행'} 하위 실행`} className={cn('min-w-0', root && 'min-h-0 flex-1 overflow-y-auto')}>
    {sourceChanged && <li role="none" className="border-b border-border-subtle px-3 py-2">
      <p role="status" className="flex flex-wrap items-center gap-2 text-[11px] text-ink-3"><span>기록이 갱신되었습니다</span>
        <button type="button" onClick={reloadObserved} disabled={query.isFetching} className="rounded border border-border px-2 py-1 text-ink-2 disabled:opacity-50 focus-visible:ring-1 focus-visible:ring-accent">다시 읽기</button>
      </p>
    </li>}
    {!policyChanged && <>
    {root && query.meta?.request_total?.state === 'verified' && <li role="none" className="flex flex-wrap items-center gap-2 border-b border-border-subtle px-3 py-2 text-[11px] text-ink-3">
      <span>작업 요청 {query.meta.request_total.value}개</span>
      {onJumpRequest && <RequestNumberJump onJump={onJumpRequest} total={query.meta.request_total.value} />}
    </li>}
    {root && query.meta?.prev_cursor && onRootCursor && <li role="none"><button type="button" onClick={previousPage} className="m-2 rounded border border-border px-2 py-1 text-[11px] text-ink-2">이전 요청 페이지</button></li>}
    {query.isPending && nodes.length === 0 && <li role="none"><p role="status" className="px-3 py-4 text-xs text-ink-3">{root ? '실행 노드를 읽는 중…' : '하위 실행을 읽는 중…'}</p></li>}
    {query.isError && <li role="none"><p role="alert" className="flex items-center gap-2 px-3 py-3 text-xs text-ink-3">
      {root ? '실행 트리를 읽지 못했습니다.' : '하위 실행을 읽지 못했습니다.'}
      <button type="button" onClick={retry} className="text-accent underline focus-visible:ring-1 focus-visible:ring-accent">다시 시도</button>
    </p></li>}
    {!query.isPending && !query.isError && nodes.length === 0 && usageNodes.length === 0 && <li role="none"><p role="status" className="px-3 py-4 text-xs text-ink-3">{root ? '관측된 실행 노드가 없습니다.' : '연결된 하위 실행이 없습니다.'}</p></li>}
    {nodes.map(node => {
      const expanded = isExpanded(node.node_id);
      return <li key={node.node_id} role="treeitem" aria-level={depth + 1} aria-selected={selectedNodeId === node.node_id} aria-expanded={node.has_children ? expanded : undefined} className="min-w-0">
        <TraceNodeRow node={node} selected={selectedNodeId === node.node_id} expanded={expanded} depth={depth} reveal={revealNodeId === node.node_id} onSelect={onSelectNode} onToggle={onToggleNode} />
        {expanded && node.has_children && <TraceNodeBranch identity={identity} enabled={enabled} nodesView={nodesView} parentId={node.node_id} parentTitle={node.title} depth={depth + 1} branchCursors={branchCursors} revealNodeId={revealNodeId} onResetCursors={onResetCursors}
          selectedNodeId={selectedNodeId} expandedNodeIds={expandedNodeIds} expandedUsageGroupIds={expandedUsageGroupIds} onSelectNode={onSelectNode} onToggleNode={onToggleNode} onToggleUsageGroup={onToggleUsageGroup} />}
      </li>;
    })}
    {usageNodes.length > 0 && <UsageRecordGroup nodes={usageNodes} groupId={usageGroupId} depth={depth} rowDepth={usageRowDepth} expanded={usageGroupExpanded}
      selectedNodeId={selectedNodeId} onSelectNode={onSelectNode} onToggle={onToggleUsageGroup} />}
    {query.hasNextPage && <li role="none" className="px-3 py-2">
      <button type="button" onClick={loadMore} disabled={query.isFetchingNextPage} className="rounded px-2 py-1 text-[11px] text-ink-2 hover:bg-surface-sunk disabled:opacity-50 focus-visible:ring-1 focus-visible:ring-accent">
        {query.isFetchingNextPage ? '실행을 읽는 중…' : root ? '실행 더 읽기' : `하위 실행 더 읽기: ${parentTitle ?? '선택한 실행'}`}
      </button>
    </li>}
    </>}
  </ul>;
};

const RequestNumberJump = ({ onJump, total }: { onJump: (number: number) => void; total?: number }) => {
  const [value, setValue] = useState('');
  const change = (event: React.ChangeEvent<HTMLInputElement>) => setValue(event.target.value);
  const submit = (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); const number = Number(value); if (Number.isInteger(number) && number > 0 && (total === undefined || number <= total)) onJump(number); };
  return <form onSubmit={submit} className="ml-auto flex items-center gap-1"><input type="number" aria-label="작업 요청 번호" min="1" max={total} value={value} onChange={change} className="w-14 rounded border border-border bg-surface px-1 py-0.5 text-xs text-ink" /><button type="submit" className="rounded border border-border px-2 py-0.5">이동</button></form>;
};
const TraceSearchForm = ({ query, onSearch }: { query: string; onSearch?: (value: string) => void }) => {
  const [draft, setDraft] = useState(query);
  const changeSearch = (event: React.ChangeEvent<HTMLInputElement>) => setDraft(event.target.value);
  const submitSearch = (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); onSearch?.(draft.trim()); };
  return <form role="search" onSubmit={submitSearch} className="flex items-center gap-1 rounded-md border border-border bg-surface">
        <input type="search" maxLength={256} aria-label="요청·메시지·도구 검색" placeholder="요청·메시지·도구 검색" value={draft} onChange={changeSearch}
          className="min-w-0 flex-1 bg-transparent px-2 py-1.5 text-xs text-ink outline-none focus-visible:ring-1 focus-visible:ring-accent" />
        <button type="submit" aria-label="검색" className="rounded p-2 text-ink-3 hover:bg-surface-sunk"><Search size={14} aria-hidden="true" /></button>
      </form>;
};
const TraceExecutionView = ({ identity, enabled, nodesView, selectedNodeId, expandedNodeIds, expandedUsageGroupIds, onSelectNode, onToggleNode, onToggleUsageGroup, query = '', rootCursor, branchCursors, observedStamp, revealNodeId, onResetCursors, onSearch, onInitialNode, requestNo, onJumpRequest, onRootCursor }: TraceExecutionViewProps) => {
  return <section aria-label="요청 탐색" className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
    <header className="shrink-0 border-b border-border-subtle px-3 py-3">
      <h3 className="mb-2 text-xs font-medium text-ink">요청과 실행</h3>
      <TraceSearchForm key={query} query={query} onSearch={onSearch} />
      {query && <p role="status" className="mt-2 text-[11px] text-ink-3">서버에 저장된 요청·메시지·도구 검색 · 원문 본문 검색 제외</p>}
    </header>
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden" data-trace-tree-scroll="true">
      <TraceNodeBranch key={JSON.stringify(traceNodesKey(identity))} identity={identity} enabled={enabled} nodesView={nodesView} depth={0} root rootLabel="실행 트리" query={query} rootCursor={rootCursor} observedStamp={observedStamp} branchCursors={branchCursors} revealNodeId={revealNodeId} onResetCursors={onResetCursors} requestNo={requestNo} onJumpRequest={onJumpRequest} onRootCursor={onRootCursor} onInitialNode={onInitialNode}
        selectedNodeId={selectedNodeId} expandedNodeIds={expandedNodeIds} expandedUsageGroupIds={expandedUsageGroupIds}
        onSelectNode={onSelectNode} onToggleNode={onToggleNode} onToggleUsageGroup={onToggleUsageGroup} />
    </div>
  </section>;
};

export { TraceExecutionView };
export type { TraceExecutionViewProps };
