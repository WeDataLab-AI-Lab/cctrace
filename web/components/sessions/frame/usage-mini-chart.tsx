'use client';

import { useId, useRef, useState, type KeyboardEvent, type PointerEvent } from 'react';
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog';
import { cn } from '@/lib/utils';
import type { UsageWindowSeries } from '@/lib/session-analysis-types';
import { chartLabel, chartPaths, chartSamples, pctText, type ChartGeometry, type ChartSample } from './model';

/**
 * 5H·7D 미니 차트(A3, F9): 지금 창을 지난 창들과 겹쳐 본다.
 *   지금 창 = 굵은 선, 지난 창들 = 옅은 가는 선, 중앙값 = 점선, 25~75% 범위 = 옅은 면.
 * 가로는 창이 시작된 뒤 지난 비율, 세로는 사용률(높을수록 위)이다. 값 없이 빈 배열이어도 틀과 바닥선만 그린다.
 * 경로 문자열은 model.ts가 계산하고, 여기서는 SVG 속성으로만 그린다(인라인 style 없음).
 */

const CHART_GEOMETRY: ChartGeometry = { width: 120, height: 30, padX: 2, padY: 3 };
const EXPANDED_GEOMETRY: ChartGeometry = { width: 640, height: 240, padX: 38, padY: 20 };

const readValue = (value: number | null): string => value === null ? '판단 불가' : pctText(value);
const rangeText = (sample: ChartSample): string => {
  if (sample.n !== null && sample.n < 4) return `판단 불가 (n=${sample.n})`;
  if (sample.low === null || sample.high === null) return '판단 불가';
  return `${pctText(sample.low)}~${pctText(sample.high)}`;
};
const positionText = (x: number, label: '5H' | '7D'): string => {
  const elapsed = x * (label === '5H' ? 5 : 7);
  return label === '5H' ? `${Math.round(elapsed * 60)}분` : `${Math.round(elapsed * 10) / 10}일`;
};

const ChartLines = ({ series, geometry, expanded = false }: {
  series: UsageWindowSeries;
  geometry: ChartGeometry;
  expanded?: boolean;
}) => {
  const paths = chartPaths(series, geometry);
  const { width, height, padX, padY } = geometry;
  return (
    <>
      <line x1={padX} x2={width - padX} y1={height - padY} y2={height - padY} strokeWidth="1" className="stroke-border-strong" />
      {expanded && [0, 25, 50, 75, 100].map((value) => (
        <g key={value}>
          {value > 0 && <line x1={padX} x2={width - padX} y1={height - padY - (value / 100) * (height - 2 * padY)} y2={height - padY - (value / 100) * (height - 2 * padY)} strokeWidth="1" className="stroke-border" />}
          <text x={padX - 6} y={height - padY - (value / 100) * (height - 2 * padY) + 3} textAnchor="end" className="fill-ink-3 text-[10px]">{value}%</text>
        </g>
      ))}
      {paths.hasBand && <path d={paths.band} className="fill-ink-4 opacity-25" />}
      {paths.past.map((d, i) => (
        <path key={`past-${i}`} d={d} strokeWidth={expanded ? '1' : '0.75'} className="fill-none stroke-ink-4 opacity-50" />
      ))}
      {paths.hasMedian && <path d={paths.median} strokeWidth={expanded ? '2' : '1'} strokeDasharray="3 2" className="fill-none stroke-ink-2" />}
      {paths.hasCurrent && <path d={paths.current} strokeWidth={expanded ? '2.5' : '1.75'} strokeLinecap="round" strokeLinejoin="round" className={expanded ? 'fill-none stroke-brand' : 'fill-none stroke-ink'} />}
    </>
  );
};

const ChartReadout = ({ sample, label }: { sample: ChartSample; label: '5H' | '7D' }) => (
  <div role="status" className="max-h-48 overflow-y-auto rounded-md border border-border bg-surface px-3 py-2 text-xs text-ink shadow-sm">
    <strong className="font-semibold">창 시작 후 {positionText(sample.x, label)}</strong>
    <div className="mt-1 grid grid-cols-2 gap-x-5 gap-y-1">
      <span className="text-ink-2">지금 창</span><strong className="text-right tabular-nums">{readValue(sample.current)}</strong>
      <span className="text-ink-2">지난 창 중앙값</span><strong className="text-right tabular-nums">{readValue(sample.median)}</strong>
      <span className="text-ink-2">지난 창 25~75%</span><strong className="text-right tabular-nums">{rangeText(sample)}</strong>
      <span className="text-ink-2">지난 창 표본 수</span><strong className="text-right tabular-nums">{sample.n === null ? '판단 불가' : sample.n}</strong>
      {sample.past.map((past, index) => (
        <span key={`past-${index}`} className="contents"><span className="text-ink-2">지난 창 {index + 1}</span><strong className="text-right tabular-nums">{readValue(past)}</strong></span>
      ))}
    </div>
  </div>
);

const ExpandedChart = ({ label, series, accountLabel }: Omit<UsageMiniChartProps, 'className'>) => {
  const id = useId();
  const [activeIndex, setActiveIndex] = useState<number | null>(null);
  const [tableView, setTableView] = useState(false);
  const samples = chartSamples(series);
  const geometry = EXPANDED_GEOMETRY;
  const active = activeIndex === null ? null : samples[activeIndex] ?? null;
  const title = `${accountLabel ? `${accountLabel} ` : ''}${label === '5H' ? '5시간' : '7일'} 창 사용률`;
  const nearest = (x: number): number => samples.reduce((best, sample, index) =>
    Math.abs(sample.x - x) < Math.abs(samples[best].x - x) ? index : best, 0);
  const onPointerMove = (event: PointerEvent<SVGSVGElement>) => {
    if (samples.length === 0) return;
    const box = event.currentTarget.getBoundingClientRect();
    if (box.width === 0) return;
    const x = ((event.clientX - box.left) / box.width * geometry.width - geometry.padX) / (geometry.width - 2 * geometry.padX);
    setActiveIndex(nearest(x));
  };
  const onKeyDown = (event: KeyboardEvent<SVGSVGElement>) => {
    if (samples.length === 0) return;
    if (event.key === 'ArrowRight' || event.key === 'ArrowLeft' || event.key === 'Home' || event.key === 'End') {
      event.preventDefault();
      if (event.key === 'Home') setActiveIndex(0);
      else if (event.key === 'End') setActiveIndex(samples.length - 1);
      else setActiveIndex(Math.max(0, Math.min(samples.length - 1, (activeIndex ?? (event.key === 'ArrowRight' ? -1 : samples.length)) + (event.key === 'ArrowRight' ? 1 : -1))));
    }
  };
  return (
    <>
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <DialogTitle className="text-base font-semibold text-ink">{title}</DialogTitle>
          <p className="mt-1 text-xs text-ink-3">창 시작 후 경과 비율로 지난 창과 비교합니다. 화살표 키로 관측 위치를 이동할 수 있습니다. {series.source === 'client_reported' ? '출처: 로컬 클라이언트 보고.' : ''} {series.truncated ? '표시 지점을 줄였습니다.' : ''}</p>
        </div>
        <button type="button" aria-pressed={tableView} onClick={() => setTableView(!tableView)} className="rounded-md border border-border px-3 py-1.5 text-xs text-ink hover:bg-surface-2 focus-visible:outline-2 focus-visible:outline-brand">
          {tableView ? '차트 보기' : '표로 보기'}
        </button>
      </header>
      <div className="flex flex-wrap gap-x-5 gap-y-1 text-xs text-ink-2" aria-label="차트 범례">
        <span className="inline-flex items-center gap-1.5"><span aria-hidden="true" className="h-0.5 w-4 bg-brand" />지금 창</span>
        {series.past.length > 0 && <span className="inline-flex items-center gap-1.5"><span aria-hidden="true" className="h-0.5 w-4 bg-ink-4" />지난 창 {series.historyCount ?? series.past.length}개</span>}
        {series.median.length > 0 && <span className="inline-flex items-center gap-1.5"><span aria-hidden="true" className="w-4 border-t-2 border-dashed border-ink-2" />지난 창 중앙값</span>}
        {(series.past.length > 0 || series.median.length > 0 || series.band.length > 0 || (series.historyCount ?? 0) > 0) && <span className="inline-flex items-center gap-1.5"><span aria-hidden="true" className="h-2 w-4 bg-ink-4/25" />25~75% 범위 (표본 4개 이상)</span>}
      </div>
      {tableView ? (
        <div className="max-h-[55vh] overflow-auto rounded-md border border-border">
          <table className="w-full min-w-[340px] text-left text-xs text-ink">
            <caption className="sr-only">{title} 관측 위치별 사용률</caption>
            <thead className="sticky top-0 bg-surface-2 text-ink-2"><tr>
              <th scope="col" className="px-2 py-2">경과</th><th scope="col" className="px-2 py-2">지금</th><th scope="col" className="px-2 py-2">중앙값</th><th scope="col" className="px-2 py-2">25~75%</th>
              <th scope="col" className="px-2 py-2">표본 n</th>
              {series.past.map((_, index) => <th scope="col" className="px-2 py-2" key={index}>지난 {index + 1}</th>)}
            </tr></thead>
            <tbody>{samples.map((sample) => <tr key={sample.x} className="border-t border-border">
              <th scope="row" className="whitespace-nowrap px-2 py-1.5 font-medium">{positionText(sample.x, label)}</th>
              <td className="px-2 py-1.5 tabular-nums">{readValue(sample.current)}</td>
              <td className="px-2 py-1.5 tabular-nums">{readValue(sample.median)}</td>
              <td className="whitespace-nowrap px-2 py-1.5 tabular-nums">{rangeText(sample)}</td>
              <td className="px-2 py-1.5 tabular-nums">{sample.n === null ? '판단 불가' : sample.n}</td>
              {sample.past.map((past, index) => <td key={index} className="px-2 py-1.5 tabular-nums">{readValue(past)}</td>)}
            </tr>)}</tbody>
          </table>
          {samples.length === 0 && <p className="p-3 text-xs text-ink-3">표시할 관측값이 없습니다.</p>}
        </div>
      ) : (
        <div>
          <svg role="img" tabIndex={0} aria-label={`${title} 겹침 차트. 좌우 화살표 키로 관측 위치 이동`} aria-describedby={id} viewBox={`0 0 ${geometry.width} ${geometry.height}`} onPointerMove={onPointerMove} onPointerLeave={() => setActiveIndex(null)} onFocus={() => { if (samples.length > 0) setActiveIndex((previous) => previous ?? 0); }} onKeyDown={onKeyDown} className="block w-full rounded-md border border-border bg-surface-2 outline-none focus-visible:ring-2 focus-visible:ring-brand">
            <ChartLines series={series} geometry={geometry} expanded />
            {active && <line x1={geometry.padX + active.x * (geometry.width - 2 * geometry.padX)} x2={geometry.padX + active.x * (geometry.width - 2 * geometry.padX)} y1={geometry.padY} y2={geometry.height - geometry.padY} strokeWidth="1" className="stroke-ink-2" />}
          </svg>
          <p id={id} className="mt-1 text-xs text-ink-3">가로축: 창 시작 후 경과 · 세로축: 사용률 0~100%</p>
          <div className="mt-2 min-h-9">{active ? <ChartReadout sample={active} label={label} /> : <p className="text-xs text-ink-3">{samples.length > 0 ? '차트에 포인터를 올리거나 초점을 맞추면 해당 위치의 값을 볼 수 있습니다.' : '표시할 관측값이 없습니다.'}</p>}</div>
        </div>
      )}
    </>
  );
};

interface UsageMiniChartProps {
  label: '5H' | '7D';
  series: UsageWindowSeries;
  /** 차트가 보이는 계정의 이름. 접근성 설명에만 쓴다. */
  accountLabel: string;
  className?: string;
}

const UsageMiniChart = ({ label, series, accountLabel, className }: UsageMiniChartProps) => {
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const { width, height } = CHART_GEOMETRY;
  const description = chartLabel(series, accountLabel);
  return (
    <div className={cn('flex shrink-0 items-center gap-1.5', className)}>
      <span aria-hidden="true" className="text-[10.5px] font-semibold text-ink-3">{label}</span>
      <Dialog open={open} onOpenChange={setOpen}>
        <button ref={trigger} type="button" aria-label={`${description} 자세히 보기`} aria-haspopup="dialog" onClick={() => setOpen(true)} className="shrink-0 rounded-[var(--r-xs)] focus-visible:outline-2 focus-visible:outline-brand">
          <svg role="img" aria-label={description} viewBox={`0 0 ${width} ${height}`} className="block h-[30px] w-[120px] rounded-[var(--r-xs)] border border-border bg-surface-2">
            <title>{description}</title>
            <ChartLines series={series} geometry={CHART_GEOMETRY} />
          </svg>
        </button>
        {open && <DialogContent showCloseButton={false} aria-describedby={undefined} onCloseAutoFocus={(event) => { event.preventDefault(); trigger.current?.focus(); }} className="max-h-[calc(100dvh-2rem)] w-[min(760px,calc(100vw-2rem))] max-w-[calc(100vw-2rem)] gap-4 overflow-y-auto border-border bg-surface p-4 sm:p-6">
          <ExpandedChart label={label} series={series} accountLabel={accountLabel} />
          <button type="button" onClick={() => setOpen(false)} className="justify-self-end rounded-md border border-border px-3 py-1.5 text-xs text-ink hover:bg-surface-2 focus-visible:outline-2 focus-visible:outline-brand">닫기</button>
        </DialogContent>}
      </Dialog>
    </div>
  );
};

export { UsageMiniChart, CHART_GEOMETRY };
