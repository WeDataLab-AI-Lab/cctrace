import type { TraceBin, TraceEvent, TraceKind, TraceRange } from './session-trace-types';

const TRACE_KINDS: TraceKind[] = ['input', 'response', 'tool', 'agent'];
const TRACE_LABELS: Record<TraceKind, string> = { input: '입력', response: '응답', tool: '도구', agent: '에이전트' };
interface EventGeometry { x: number; width: number; height: number; unknown: boolean }
interface PixelGroup { x: number; events: TraceEvent[] }
const validRange = (from?: string | null, to?: string | null): TraceRange | null =>
  from && to && Number.isFinite(Date.parse(from)) && Date.parse(to) > Date.parse(from) ? { from, to } : null;
// A zero-duration observation gets a visual viewport, never an invented event duration.
const traceViewport = (from?: string | null, to?: string | null): TraceRange | null => {
  if (!from || !to || !Number.isFinite(Date.parse(from)) || Date.parse(to) < Date.parse(from)) return null;
  if (Date.parse(from) === Date.parse(to)) return { from: new Date(Date.parse(from) - 500).toISOString(), to: new Date(Date.parse(to) + 500).toISOString() };
  return { from, to };
};
const eventGeometry = (event: TraceEvent, range: TraceRange, width: number, selected: boolean): EventGeometry | null => {
  const from = Date.parse(range.from), to = Date.parse(range.to);
  const start = Date.parse(event.started_at ?? ''), end = Date.parse(event.ended_at ?? '');
  const span = (event.kind === 'tool' || event.kind === 'agent') && Number.isFinite(start) && Number.isFinite(end) && end >= start;
  const ts = (event.kind === 'tool' || event.kind === 'agent') && Number.isFinite(start) ? start : Date.parse(event.timestamp);
  const first = span ? start : ts, last = span ? end : ts;
  if (!(to > from) || !Number.isFinite(first) || last < from || first > to) return null;
  const x = Math.max(0, Math.min(width - 1, (first - from) / (to - from) * width));
  return { x, width: span ? Math.max(1, (Math.min(to, last) - Math.max(from, first)) / (to - from) * width) : 1,
    height: selected ? 6 : span ? 3 : 1, unknown: !span && (event.kind === 'tool' || event.kind === 'agent') };
};
const pixelGroups = (events: TraceEvent[], range: TraceRange, width: number): PixelGroup[] => {
  const groups = new Map<string, PixelGroup>();
  for (const event of events) {
    const geometry = eventGeometry(event, range, width, false);
    if (!geometry) continue;
    const x = Math.floor(geometry.x), key = `${event.kind}:${x}`;
    const group = groups.get(key) ?? { x, events: [] };
    group.events.push(event); groups.set(key, group);
  }
  return [...groups.values()];
};
// Density already includes the sampled events. Use one source for counts and
// cluster only their labels; each observed mark keeps its original position.
const traceCountLabels = (events: TraceEvent[], bins: TraceBin[], range: TraceRange, width: number): { x: number; count: number }[] => {
  const from = Date.parse(range.from), span = Date.parse(range.to) - from;
  if (!(span > 0) || width <= 0) return [];
  const points = bins.length ? bins.map(bin => ({
    x: Math.floor(Math.max(0, Math.min(width - 1, (Date.parse(bin.first_ts) - from) / span * width))), count: bin.count,
  })) : pixelGroups(events, range, width).map(group => ({ x: group.x, count: group.events.length }));
  const labels: { x: number; count: number }[] = [];
  for (const point of points.filter(p => Number.isFinite(p.x) && p.count > 0).sort((a, b) => a.x - b.x)) {
    const last = labels.at(-1);
    if (last && point.x - last.x < 40) last.count += point.count;
    else labels.push({ ...point });
  }
  return labels;
};
const zoomRange = (range: TraceRange, bounds: TraceRange, factor: number, centerMs?: number): TraceRange => {
  const center = centerMs ?? (Date.parse(range.from) + Date.parse(range.to)) / 2;
  const length = Math.min(Date.parse(bounds.to) - Date.parse(bounds.from), Math.max(1, (Date.parse(range.to) - Date.parse(range.from)) * factor));
  const start = Math.max(Date.parse(bounds.from), Math.min(Date.parse(bounds.to) - length, center - length / 2));
  return { from: new Date(start).toISOString(), to: new Date(start + length).toISOString() };
};
const timeAtPixel = (range: TraceRange, x: number, width: number) => new Date(Date.parse(range.from) + Math.max(0, Math.min(1, x / width)) * (Date.parse(range.to) - Date.parse(range.from))).toISOString();

export { TRACE_KINDS, TRACE_LABELS, validRange, traceViewport, eventGeometry, pixelGroups, traceCountLabels, zoomRange, timeAtPixel };
export type { EventGeometry, PixelGroup };
