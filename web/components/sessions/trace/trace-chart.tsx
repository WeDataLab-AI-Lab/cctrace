'use client';

import { useEffect, useRef, useState, type ChangeEvent, type KeyboardEvent, type MouseEvent, type PointerEvent, type UIEvent } from 'react';
import { cn } from '@/lib/utils';
import { eventGeometry, traceCountLabels, TRACE_KINDS, TRACE_LABELS, timeAtPixel, traceViewport, zoomRange } from '@/lib/session-trace-model';
import type { AnalysisTurn } from '@/lib/session-analysis-types';
import { previewView } from '../analysis/requests';
import { TURN_STATUS_LABEL } from '../analysis/labels';
import type { TraceBin, TraceCoverage, TraceEvent, TraceKind, TraceOverview, TraceRange, TraceState } from '@/lib/session-trace-types';

interface TraceCanvasProps { events: TraceEvent[]; bins?: TraceBin[]; range: TraceRange; selectedId?: string; kind: TraceKind; agent?: string }
const TraceCanvas = ({ events, bins = [], range, selectedId, kind, agent }: TraceCanvasProps) => {
  const ref = useRef<HTMLCanvasElement>(null);
  /** Native canvas follows its CSS width; timestamps stay proportional and density never invents durations. */
  useEffect(() => {
    const canvas = ref.current;
    if (!canvas) return;
    const draw = () => {
      const ctx = canvas.getContext('2d');
      if (!ctx) return;
      const width = canvas.getBoundingClientRect().width || 600, ratio = window.devicePixelRatio || 1;
      const height = 20;
      canvas.width = Math.round(width * ratio); canvas.height = height * ratio;
      ctx.scale(ratio, ratio); ctx.clearRect(0, 0, width, height);
      const css = getComputedStyle(canvas);
      const token = (name: string) => css.getPropertyValue(name).trim();
      const color = kind === 'input' ? token('--ink-3') : agent === 'codex' ? token('--agent-codex') : agent === 'claude' ? token('--agent-claude') : token('--ink-2');
      ctx.fillStyle = token('--border-subtle');
      for (let y = 18; y < height; y += 20) ctx.fillRect(0, y, width, 1);
      for (const bin of bins) {
        // first/last are actual points in a density bin, not a measured tool span.
        for (const ts of new Set([bin.first_ts, bin.last_ts])) {
          const x = (Date.parse(ts) - Date.parse(range.from)) / (Date.parse(range.to) - Date.parse(range.from)) * width;
          ctx.fillStyle = color; ctx.fillRect(Math.floor(x), 9.5, 1, 1);
        }
      }
      for (const event of events) {
        const geometry = eventGeometry(event, range, width, event.event_id === selectedId);
        if (!geometry) continue;
        ctx.fillStyle = event.status === 'error' ? token('--danger') : event.event_id === selectedId ? token('--ink') : event.kind === 'input' ? token('--ink-3') : color;
        ctx.fillRect(Math.floor(geometry.x), (20 - geometry.height) / 2, geometry.width, geometry.height);
      }
      for (const group of traceCountLabels(events, bins, range, width)) {
        if (group.count < 2) continue;
        ctx.fillStyle = token('--ink-3'); ctx.font = `9px ${css.fontFamily}`;
        const label = String(group.count), x = Math.max(0, Math.min(group.x + 2, width - ctx.measureText(label).width - 1));
        ctx.fillText(label, x, 8);
      }
    };
    draw();
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(draw);
    observer?.observe(canvas);
    return () => observer?.disconnect();
  }, [events, bins, range, selectedId, kind, agent]);
  return <canvas ref={ref} aria-hidden="true" data-selected-event={selectedId} className="h-5 w-full font-mono" />;
};

interface TraceTrackProps extends TraceCanvasProps {
  label?: string; onSelect: (event: TraceEvent) => void; onRange: (range: TraceRange) => void; onDragging: (dragging: boolean) => void;
}
const TraceTrack = ({ events, bins, range, selectedId, kind, agent, label, onSelect, onRange, onDragging }: TraceTrackProps) => {
  const [cursor, setCursor] = useState(0);
  const start = useRef<number | null>(null);
  const pointerClick = useRef(false);
  const active = events[Math.min(cursor, events.length - 1)];
  const activeBin = bins?.[Math.min(cursor, bins.length - 1)];
  const count = events.length || bins?.length || 0;
  const handleKey = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (event.key === 'ArrowRight' || event.key === 'ArrowLeft') {
      event.preventDefault(); setCursor(value => Math.max(0, Math.min(count - 1, value + (event.key === 'ArrowRight' ? 1 : -1))));
    } else if (event.key === 'Enter') {
      event.preventDefault();
      if (active) onSelect(active); else onRange(zoomRange(range, range, 0.5, activeBin ? Date.parse(activeBin.first_ts) : undefined));
    }
  };
  const handleDown = (event: PointerEvent<HTMLButtonElement>) => {
    const rect = event.currentTarget.getBoundingClientRect();
    start.current = event.clientX - rect.left;
    pointerClick.current = false;
    event.currentTarget.setPointerCapture?.(event.pointerId);
    onDragging(true);
  };
  const handleUp = (event: PointerEvent<HTMLButtonElement>) => {
    const first = start.current;
    start.current = null; onDragging(false); pointerClick.current = true;
    if (first === null) return;
    const rect = event.currentTarget.getBoundingClientRect(), last = event.clientX - rect.left;
    if (Math.abs(first - last) > 4) {
      onRange({ from: timeAtPixel(range, Math.min(first, last), rect.width), to: timeAtPixel(range, Math.max(first, last), rect.width) });
      return;
    }
    const time = Date.parse(timeAtPixel(range, last, rect.width));
    let nearest: { event: TraceEvent; distance: number } | null = null;
    for (const candidate of events) {
      const geometry = eventGeometry(candidate, range, rect.width, false);
      if (!geometry) continue;
      const distance = Math.max(geometry.x - last, last - geometry.x - geometry.width, 0);
      if (!nearest || distance < nearest.distance) nearest = { event: candidate, distance };
    }
    if (nearest && nearest.distance <= 8) onSelect(nearest.event); else onRange(zoomRange(range, range, 0.5, time));
  };
  const handleCancel = () => { start.current = null; onDragging(false); };
  const handleClick = (event: MouseEvent<HTMLButtonElement>) => {
    if (pointerClick.current || event.detail > 0) { pointerClick.current = false; return; }
    if (active) onSelect(active);
    else onRange(zoomRange(range, range, 0.5, activeBin ? Date.parse(activeBin.first_ts) : undefined));
  };
  return (
    <div className="flex items-center gap-2">
      <span className="w-14 shrink-0 text-[10px] text-ink-3">{label ?? TRACE_LABELS[kind]}</span>
      <button type="button" aria-label={`${label ?? TRACE_LABELS[kind]} 트랙${active ? ` · ${active.label}` : ''}`} onKeyDown={handleKey}
        onPointerDown={handleDown} onPointerUp={handleUp} onPointerCancel={handleCancel} onClick={handleClick}
        className="min-w-0 flex-1 touch-none rounded focus-visible:ring-1 focus-visible:ring-brand">
        <TraceCanvas events={events} bins={bins} range={range} selectedId={selectedId} kind={kind} agent={agent} />
        <span className="sr-only" aria-live="polite">{active ? `${cursor + 1}/${count} · ${active.label} · ${active.timestamp}` : activeBin ? `${activeBin.count}건 · ${activeBin.first_ts}` : '읽은 사건 없음'}</span>
      </button>
    </div>
  );
};

const KindFilter = ({ kind, active, onToggle }: { kind: TraceKind; active: boolean; onToggle: (kind: TraceKind) => void }) => {
  const toggle = () => onToggle(kind);
  return <button type="button" aria-pressed={active} onClick={toggle} className="rounded border border-border px-2 py-0.5 text-[10px] text-ink-3 aria-pressed:bg-brand-soft aria-pressed:text-ink">{TRACE_LABELS[kind]}</button>;
};
interface EventRowProps {
  event: TraceEvent; index: number; range: TraceRange; width: number; selectedId?: string; agent?: string;
  parent?: TraceEvent; onSelect: (event: TraceEvent) => void; onStep: (index: number) => void;
}
const EventRow = ({ event, index, range, width, selectedId, agent, parent, onSelect, onStep }: EventRowProps) => {
  const select = () => onSelect(event);
  const handleKey = (key: KeyboardEvent<SVGGElement>) => {
    if (key.key === 'Enter' || key.key === ' ') { key.preventDefault(); select(); }
    if (key.key === 'ArrowDown' || key.key === 'ArrowUp') { key.preventDefault(); onStep(index + (key.key === 'ArrowDown' ? 1 : -1)); }
  };
  const selected = event.event_id === selectedId;
  const geometry = eventGeometry(event, range, Math.max(1, width - 300), selected);
  const duration = event.started_at && event.ended_at ? Date.parse(event.ended_at) - Date.parse(event.started_at) : null;
  const summary = geometry?.unknown ? '구간 미확인' : duration !== null && duration >= 0 && (event.kind === 'tool' || event.kind === 'agent') ? `${(duration / 1000).toFixed(1)}s 관측` : '시점';
  const title = `${event.label} · ${event.timestamp} · ${event.status} · ${summary} · ${event.parent_event_id ? `상위: ${parent?.label ?? event.parent_event_id}` : '상위 근거 없음'} · ${event.evidence.join(', ') || '추가 근거 없음'}`;
  return <g role="button" tabIndex={0} aria-label={event.label} aria-pressed={selected} data-event-row={event.event_id}
    data-parent-event={event.parent_event_id ?? undefined} transform={`translate(0 ${index * 20})`} onClick={select} onKeyDown={handleKey}
    className="cursor-pointer outline-none group">
    <title>{title}</title>
    <rect data-row-hit width="100%" height={20} className={cn('fill-transparent group-hover:fill-surface-sunk group-focus:fill-brand-soft', selected && 'fill-brand-soft')} />
    <line x1={0} y1={19.5} x2={width} y2={19.5} className="stroke-border-subtle" />
    <text x={8} y={14} className="pointer-events-none fill-ink-2 text-[11px]">{`${parent ? '↳ ' : ''}${event.label.slice(0, 22)}`}</text>
    {geometry && <rect data-execution-mark x={200 + geometry.x} y={(20 - geometry.height) / 2} width={geometry.width} height={geometry.height}
      className={cn('pointer-events-none', event.status === 'error' ? 'fill-danger' : selected ? 'fill-ink' : event.kind === 'input' || event.kind === 'response' ? 'fill-ink-3' : agent === 'codex' ? 'fill-agent-codex' : agent === 'claude' ? 'fill-agent-claude' : 'fill-ink-2')} />}
    <text x={width - 4} y={14} textAnchor="end" className="pointer-events-none fill-ink-3 font-mono text-[10px] tabular-nums">{summary}</text>
  </g>;
};
interface WaterfallProps { events: TraceEvent[]; range: TraceRange; selectedId?: string; agent?: string; onSelect: (event: TraceEvent) => void }
const ExecutionWaterfall = ({ events, range, selectedId, agent, onSelect }: WaterfallProps) => {
  const ref = useRef<HTMLDivElement>(null);
  const [viewport, setViewport] = useState({ start: 0, width: 600 });
  const start = Math.max(0, Math.min(Math.max(0, events.length - 20), viewport.start));
  const parents = new Map(events.map(event => [event.event_id, event]));
  const handleScroll = (event: UIEvent<HTMLDivElement>) => setViewport({ start: Math.max(0, Math.floor(event.currentTarget.scrollTop / 20) - 2), width: Math.max(520, event.currentTarget.clientWidth || 600) });
  const step = (index: number) => {
    const target = events[index];
    if (!target || !ref.current) return;
    onSelect(target);
    const top = index * 20;
    if (top < ref.current.scrollTop || top + 20 > ref.current.scrollTop + (ref.current.clientHeight || 320)) {
      ref.current.scrollTop = Math.max(0, top - 40);
      setViewport({ start: Math.max(0, index - 2), width: viewport.width });
    }
    // Focus the selected full row after the viewport commits its SVG children.
    requestAnimationFrame(() => ref.current?.querySelector<SVGGElement>(`[data-event-row="${CSS.escape(target.event_id)}"]`)?.focus());
  };
  /** Resize changes only the visual scale; the real timestamps and 20px row offsets stay unchanged. */
  useEffect(() => {
    const element = ref.current;
    if (!element) return;
    const resize = () => setViewport(value => ({ ...value, width: Math.max(520, element.clientWidth || 600) }));
    resize();
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(resize);
    observer?.observe(element);
    return () => observer?.disconnect();
  }, []);
  return <div ref={ref} role="region" aria-label="턴 실행 목록" tabIndex={0} onScroll={handleScroll} className="max-h-80 overflow-auto">
    <svg width="100%" height={events.length * 20} className="block min-w-[520px]">
      {events.slice(start, start + 20).map((event, offset) => <EventRow key={event.event_id} event={event} index={start + offset} range={range} width={viewport.width}
        selectedId={selectedId} agent={agent} parent={event.parent_event_id ? parents.get(event.parent_event_id) : undefined} onSelect={onSelect} onStep={step} />)}
    </svg>
  </div>;
};
interface TurnSelectionProps { turns: AnalysisTurn[]; selectedKey?: string; onSelect: (turn: AnalysisTurn) => void }
const TurnSelection = ({ turns, selectedKey, onSelect }: TurnSelectionProps) => {
  const index = turns.findIndex(turn => turn.key === selectedKey), turn = turns[index];
  const select = (event: ChangeEvent<HTMLSelectElement>) => { const next = turns.find(turn => turn.key === event.target.value); if (next) onSelect(next); };
  const previous = () => { if (index > 0) onSelect(turns[index - 1]); };
  const next = () => { if (index + 1 < turns.length) onSelect(turns[index + 1]); };
  return <nav aria-label="턴 선택" className="my-2 flex flex-wrap items-center gap-2 border-t border-border-subtle pt-2 text-[11px] text-ink-2">
    <label className="flex min-w-0 flex-1 items-center gap-2">턴
      <select aria-label="턴 선택" value={selectedKey ?? ''} onChange={select} className="min-h-11 min-w-0 flex-1 rounded border border-border bg-surface px-2 py-1">
        {!turn && <option value={selectedKey ?? ''}>{selectedKey ? '선택 턴 메타데이터 준비 중' : '턴을 선택하세요'}</option>}
        {turns.map(turn => <option key={turn.key} value={turn.key}>{`#${turn.no} · ${turn.request_no !== null ? `요청 ${turn.request_no}` : '자동 입력'} · ${previewView(turn.preview).text}`}</option>)}
      </select>
    </label>
    {turn && <span>{TURN_STATUS_LABEL[turn.status]}{turn.branch === 'abandoned' ? ' · 버려진 분기' : ''}</span>}
    <button type="button" onClick={previous} disabled={index <= 0} className="min-h-11 rounded px-2 disabled:opacity-40">이전 턴</button>
    <button type="button" onClick={next} disabled={index < 0 || index + 1 >= turns.length} className="min-h-11 rounded px-2 disabled:opacity-40">다음 턴</button>
  </nav>;
};
interface TraceChartProps {
  overview?: TraceOverview; events: TraceEvent[]; overviewEvents?: TraceEvent[]; range?: TraceRange; selectedId?: string; agent?: string;
  rangeState?: TraceState; rangeCoverage?: TraceCoverage;
  turns?: AnalysisTurn[]; selectedTurnKey?: string; onTurn?: (turn: AnalysisTurn) => void;
  onSelect: (event: TraceEvent) => void; onRange: (range: TraceRange | undefined) => void; onDragging: (dragging: boolean) => void;
}
const TraceChart = ({ overview, events, overviewEvents, range, rangeState, rangeCoverage, selectedId, agent, turns = [], selectedTurnKey, onTurn, onSelect, onRange, onDragging }: TraceChartProps) => {
  const [kinds, setKinds] = useState<TraceKind[]>(TRACE_KINDS);
  const bounds = traceViewport(overview?.from, overview?.to), shownRange = range ?? bounds;
  const multiDay = bounds && new Date(bounds.from).toDateString() !== new Date(bounds.to).toDateString();
  const axisTime = (value: string) => multiDay ? new Date(value).toLocaleString('ko-KR', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }) : new Date(value).toLocaleTimeString('ko-KR');
  const focusEvent = events.find(event => event.event_id === selectedId) ?? events.find(event => !selectedTurnKey || event.turn_key === selectedTurnKey);
  const zoomIn = () => { if (shownRange && bounds) onRange(zoomRange(shownRange, bounds, 0.5, focusEvent ? Date.parse(focusEvent.timestamp) : undefined)); };
  const zoomOut = () => { if (shownRange && bounds) onRange(zoomRange(shownRange, bounds, 2)); };
  const reset = () => onRange(undefined);
  const toggleKind = (kind: TraceKind) => setKinds(value => value.includes(kind) ? value.filter(item => item !== kind) : [...value, kind]);
  const visibleEvents = events.filter(event => kinds.includes(event.kind) && (!selectedTurnKey || event.turn_key === selectedTurnKey));
  const boundaries: TraceEvent[] = turns.filter(turn => Number.isFinite(Date.parse(turn.anchor_ts))).map(turn => ({ event_id: `turn-boundary:${turn.key}`, kind: 'input', timestamp: turn.anchor_ts, started_at: null, ended_at: null,
    turn_key: turn.key, parent_event_id: null, agent_id: null, label: `턴 ${turn.no}`, status: 'unknown', evidence: [], source_ref: '' }));
  const selectBoundary = (event: TraceEvent) => { const turn = turns.find(turn => turn.key === event.turn_key); if (turn) onTurn?.(turn); };
  return <section aria-label="실행 흐름" className="rounded-xl border border-border-subtle bg-surface px-3 py-2">
    <div className="mb-1 flex flex-wrap items-center gap-2">
      <h2 className="text-[12px] font-medium text-ink">전체 실행 기록</h2>
      <span className="ml-auto font-mono text-[10px] tabular-nums text-ink-3">{overview ? `${overview.events_count} 사건` : '준비 중'}</span>
      <button type="button" disabled={!bounds} onClick={zoomIn} className="text-[11px] text-ink-2 disabled:opacity-40">확대</button>
      <button type="button" disabled={!bounds} onClick={zoomOut} className="text-[11px] text-ink-2 disabled:opacity-40">축소</button>
      <button type="button" onClick={reset} className="text-[11px] text-ink-2">전체 보기</button>
    </div>
    {bounds ? <>
      {TRACE_KINDS.filter(kind => kinds.includes(kind)).map(kind => <TraceTrack key={kind} kind={kind} range={bounds}
        events={(overviewEvents ?? events).filter(event => event.kind === kind)} bins={overview?.bins.filter(bin => bin.kind === kind)}
        selectedId={selectedId} agent={agent} onSelect={onSelect} onRange={onRange} onDragging={onDragging} />)}
      {onTurn && boundaries.length > 0 && <TraceTrack label="턴 경계" kind="input" range={bounds} events={boundaries} selectedId={`turn-boundary:${selectedTurnKey}`} onSelect={selectBoundary} onRange={onRange} onDragging={onDragging} />}
      <div className="ml-16 flex justify-between font-mono text-[10px] tabular-nums text-ink-3"><time dateTime={bounds.from}>{axisTime(bounds.from)}</time><time dateTime={bounds.to}>{axisTime(bounds.to)}</time></div>
    </> : <p role="status" className="py-4 text-xs text-ink-3">{overview?.state === 'unavailable' ? '실행 시각을 확인할 수 없습니다.' : overview?.state === 'ready' ? '관측된 실행 사건이 없습니다.' : '실행 기록을 준비 중입니다.'}</p>}
    {bounds && overview?.from === overview?.to && <p role="status" className="mt-1 text-[10px] text-ink-3">단일 시점 관측입니다. 시각화 축만 넓혀 표시하며 경과 시간은 늘리지 않았습니다.</p>}
    {onTurn && <TurnSelection turns={turns} selectedKey={selectedTurnKey} onSelect={onTurn} />}
    <div className="mt-2 flex flex-wrap items-center gap-1" aria-label="사건 종류 필터">
      {TRACE_KINDS.map(kind => <KindFilter key={kind} kind={kind} active={kinds.includes(kind)} onToggle={toggleKind} />)}
      <span className="ml-auto text-[10px] text-ink-3">시점 · 관측 실행 · 선택한 실행</span>
    </div>
    {overview?.coverage.state !== 'complete' && overview && <p role="status" className="mt-1 text-[10px] text-ink-3">{overview.state === 'unavailable' || overview.coverage.state === 'unavailable' ? '이 범위의 기록을 준비하지 못했습니다. 더 작은 구간으로 확대해 읽으세요.' : overview.coverage.state === 'truncated' ? '수집 상한에 도달했습니다. 구간을 확대해 읽으세요.' : '일부 기록을 준비 중이거나 미수집 상태입니다. 공백이 비활동을 뜻하지 않습니다.'}</p>}
    {range && <div className="mt-2 border-t border-border-subtle pt-2">
      <h3 className="mb-1 text-[11px] font-medium text-ink-2">{selectedTurnKey ? '턴 워터폴' : '구간 워터폴'}</h3>
      <div className="mb-1 flex justify-between font-mono text-[10px] tabular-nums text-ink-3"><time dateTime={range.from}>{axisTime(range.from)}</time><time dateTime={range.to}>{axisTime(range.to)}</time></div>
      <div aria-label="조회 시작 기준 경과 축" className="mr-[100px] ml-[200px] flex justify-between font-mono text-[10px] tabular-nums text-ink-3">
        <span>+0.0s</span><span>+{((Date.parse(range.to) - Date.parse(range.from)) / 2000).toFixed(1)}s</span><span>+{((Date.parse(range.to) - Date.parse(range.from)) / 1000).toFixed(1)}s</span>
      </div>
      <ExecutionWaterfall key={JSON.stringify([selectedTurnKey ?? range, kinds])} events={visibleEvents} range={range} selectedId={selectedId} agent={agent} onSelect={onSelect} />
      {rangeCoverage?.state === 'truncated' && <p role="status" className="text-[10px] text-ink-3">선택 구간이 수집 상한에서 잘렸습니다. 더 작은 구간을 선택해 읽으세요.</p>}
      {visibleEvents.length === 0 && <p role="status" className="text-xs text-ink-3">{rangeState === 'unavailable' ? '선택 구간의 실행 기록을 준비하지 못했습니다.' : (rangeState ?? overview?.state) === 'ready' && (rangeCoverage ?? overview?.coverage)?.state === 'complete' ? '읽은 범위에 표시할 사건이 없습니다.' : '선택 구간을 읽는 중이거나 관측 기록이 불완전합니다.'}</p>}
    </div>}
  </section>;
};

export { TraceChart };
