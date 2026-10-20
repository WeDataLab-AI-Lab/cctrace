'use client';

import { ArrowLeft, Maximize2, ZoomIn, ZoomOut } from 'lucide-react';
import { useTraceNodes } from '@/lib/use-trace-nodes';
import type { TraceNode, TraceNodeQueryIdentity } from '@/lib/session-trace-node-types';
import type { TraceRange } from '@/lib/session-trace-types';
import { cn } from '@/lib/utils';
import { formatObservedDuration, traceNodeKindLabel, traceNodeTimeLabel } from './trace-node-format';

interface TraceTimingViewProps {
  identity: TraceNodeQueryIdentity; enabled: boolean; groupId?: string; selectedNodeId?: string;
  range?: TraceRange; onRange?: (range: TraceRange | undefined) => void;
  onSelectNode: (id: string) => void; onClose: () => void;
}
interface TimingRowProps { node: TraceNode; from: number; to: number; selected: boolean; onSelect: (id: string) => void }
const TimingRow = ({ node, from, to, selected, onSelect }: TimingRowProps) => {
  const select = () => onSelect(node.node_id);
  const start = Date.parse(node.started_at ?? node.timestamp ?? '');
  const end = Date.parse(node.ended_at ?? '');
  const interval = Number.isFinite(start) && Number.isFinite(end) && end >= start && node.time_basis === 'call_result_observation';
  const position = Number.isFinite(start) && Number.isFinite(from) && Number.isFinite(to) ? Math.max(0, Math.min(1000, (start - from) / (to - from || 1) * 1000)) : undefined;
  const overlap = interval ? Math.max(0, Math.min(end, to) - Math.max(start, from)) : 0;
  const width = overlap > 0 ? Math.min(1000 - (position ?? 0), Math.max(2, overlap / (to - from || 1) * 1000)) : 0;
  return <button type="button" onClick={select} aria-label={`시간 분석 · ${node.title}`} aria-pressed={selected} data-node-id={node.node_id}
    className={cn('grid min-h-12 w-full min-w-0 grid-cols-[minmax(8rem,1fr)_minmax(4rem,1.2fr)_4rem] items-center gap-2 border-b border-border-subtle px-3 text-left hover:bg-surface-sunk focus-visible:ring-1 focus-visible:ring-accent', selected && 'bg-surface-sunk')}>
    <span className="min-w-0"><span className="block text-[10px] text-ink-3">{traceNodeKindLabel(node.kind)}</span><span className="block truncate text-xs text-ink" title={node.title}>{node.title}</span></span>
    <svg viewBox="0 0 1000 20" preserveAspectRatio="none" aria-hidden="true" className="h-5 w-full">
      {position !== undefined && (interval ? <rect x={position} y="6" width={width} height="8" rx="2" data-observed-interval="true" className={cn('fill-accent', (node.status === 'error' || node.status === 'failed') && 'fill-danger')} /> : <circle cx={position} cy="10" r="4" className="fill-ink-3" />)}
    </svg>
    <span className="text-right font-mono text-[10px] text-ink-3">{interval ? formatObservedDuration(node) : position === undefined ? '시각 없음' : '시점'}</span>
  </button>;
};
const TraceTimingView = ({ identity, enabled, groupId, selectedNodeId, range, onRange, onSelectNode, onClose }: TraceTimingViewProps) => {
  const query = useTraceNodes({ identity, enabled: enabled && !!groupId, view: 'timing', groupId, from: range?.from, to: range?.to });
  const from = Date.parse(range?.from ?? query.coverage?.from ?? '');
  const to = Date.parse(range?.to ?? query.coverage?.to ?? '');
  const valid = Number.isFinite(from) && Number.isFinite(to) && to > from;
  const zoom = (factor: number) => {
    if (!valid) return;
    const midpoint = (from + to) / 2, half = (to - from) * factor / 2;
    onRange?.({ from: new Date(midpoint - half).toISOString(), to: new Date(midpoint + half).toISOString() });
  };
  const zoomIn = () => zoom(0.5), zoomOut = () => zoom(2), reset = () => onRange?.(undefined);
  const loadMore = () => { void query.fetchNextPage({ cancelRefetch: false }); };
  const retry = () => query.refresh();
  return <section aria-label="선택 요청 시간 분석" className="flex min-h-0 min-w-0 flex-1 flex-col">
    <header className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border-subtle px-3 py-3">
      <button type="button" onClick={onClose} className="flex items-center gap-1 text-xs text-ink-2"><ArrowLeft size={14} aria-hidden="true" />요청으로 돌아가기</button>
      <h3 className="ml-auto text-xs font-medium text-ink">선택 요청 시간 분석</h3>
      <button type="button" aria-label="시간 구간 확대" onClick={zoomIn} disabled={!valid || !onRange} className="rounded p-1 text-ink-3 disabled:opacity-40"><ZoomIn size={14} /></button>
      <button type="button" aria-label="시간 구간 축소" onClick={zoomOut} disabled={!valid || !onRange} className="rounded p-1 text-ink-3 disabled:opacity-40"><ZoomOut size={14} /></button>
      <button type="button" aria-label="선택 요청 전체 구간" onClick={reset} disabled={!range} className="rounded p-1 text-ink-3 disabled:opacity-40"><Maximize2 size={14} /></button>
    </header>
    {!groupId ? <p className="p-4 text-xs text-ink-3">시간 분석할 요청을 먼저 선택하세요.</p> : <>
      {valid && <p className="flex justify-between border-b border-border-subtle px-3 py-2 font-mono text-[10px] text-ink-3"><span>{traceNodeTimeLabel(new Date(from).toISOString())}</span><span>{traceNodeTimeLabel(new Date(to).toISOString())}</span></p>}
      <p className="border-b border-border-subtle px-3 py-2 text-[10px] text-ink-3">막대는 호출·결과 사이 관측 구간이며 실제 실행 시간과 다를 수 있습니다. 점은 기록 시점입니다.</p>
      <div role="region" aria-label="시간축" className="min-h-0 flex-1 overflow-auto">
        {query.isPending && <p role="status" className="p-4 text-xs text-ink-3">선택 요청의 시간을 읽는 중…</p>}
        {query.isError && <p role="alert" className="p-4 text-xs text-ink-3">시간 분석을 읽지 못했습니다. <button type="button" onClick={retry} className="underline">다시 시도</button></p>}
        {!query.isPending && !query.isError && query.nodes.length === 0 && <p className="p-4 text-xs text-ink-3">이 구간에 관측된 실행이 없습니다.</p>}
        {query.nodes.filter(node => node.kind !== 'usage').map(node => <TimingRow key={node.node_id} node={node} from={from} to={to} selected={selectedNodeId === node.node_id} onSelect={onSelectNode} />)}
        {query.hasNextPage && <button type="button" onClick={loadMore} disabled={query.isFetchingNextPage} className="m-3 rounded border border-border px-3 py-1.5 text-xs text-ink-2">구간 실행 더 읽기</button>}
      </div>
    </>}
  </section>;
};
export { TraceTimingView };
export type { TraceTimingViewProps };
