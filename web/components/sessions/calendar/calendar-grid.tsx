'use client';

import { useId, useRef, useState, type MouseEvent as ReactMouseEvent, type PointerEvent as ReactPointerEvent, type RefObject } from 'react';
import { cn } from '@/lib/utils';
import { gridKey, moveFocus, moveOfKey, tabStopKey, type FocusDay } from './band-focus';
import { Abs, AbsButton } from './css-box';
import type { ColorAssignment } from './colors';
import { CARD_GAP, CARD_HEIGHT, CARD_WIDTH, HoverCard, type HoverTarget, type Placed } from './hover-card';
import { bandLabel, inputHover, titleOf } from './hover';
import { dayLengthHint, dayLengthLabel, WEEKDAY_LABEL } from './labels';
import { nearestIndex, toleranceMsFor } from './hit-test';
import { type DayLayout, type PlacedBand, type WeekLayout } from './layout';
import type { PreparedSession } from './prepare';
import { HOUR_MS } from './time-zone';

/**
 * 주간 격자(C6~C16). 날짜 열 일곱 개, 시간 축, 띠, 입력선(또는 말풍선), 발언량 농도 띠, 현재 시각 선, 선택 강조.
 *
 * 좌표는 `layoutWeek`가 계산해 준 숫자를 그대로 그린다. 띠는 절대 배치한 버튼이고 위치·크기는 CSS 변수(`css-box.tsx`)로만
 * 넘긴다. 격자선·농도 띠·입력선·현재 시각 선은 SVG 속성으로 그려서 요소 수가 많아도(입력 2,000개) 가볍다.
 */

/** 날짜 머리의 높이(px). 스크롤 위치 계산이 이 값을 쓴다. */
const HEADER_HEIGHT = 38;
const AXIS_WIDTH = 46;
const COLUMN_MIN_WIDTH = 84;
const MIN_LANE_WIDTH = 8;
const LANE_GUTTER = 12;
const dayMinWidth = (day: DayLayout): number => Math.max(COLUMN_MIN_WIDTH, day.peakLanes * MIN_LANE_WIDTH + LANE_GUTTER);
/** 열 왼쪽 끝의 발언량 농도 띠 너비(px) (계획 C11). */
const DENSITY_WIDTH = 6;
/** 입력선을 맞히는 허용 오차(px). */
const INPUT_HIT_PX = 4;
/** 시간 축 글자의 최소 간격(px). 배율이 낮으면 몇 시간마다 적는다. */
const AXIS_LABEL_GAP = 13;

interface CalendarGridProps {
  layout: WeekLayout;
  sessionsById: ReadonlyMap<string, PreparedSession>;
  assignment: ColorAssignment;
  /** 강조 중인 범례 칸의 키. 있으면 그 값이 아닌 띠는 흐려진다. */
  highlightKey: string | null;
  selectedId: string | null;
  onSelect: (sessionId: string, turnKey?: string, anchorTs?: string) => void;
  timeZone: string;
  pxPerHour: number;
  scrollerRef: RefObject<HTMLDivElement | null>;
  inert?: boolean;
}

/* ---------------- 시간 축 ---------------- */

const TimeAxis = ({ layout, pxPerHour }: { layout: WeekLayout; pxPerHour: number }) => {
  const every = Math.max(1, Math.ceil(AXIS_LABEL_GAP / pxPerHour));
  return (
    // 시각은 띠의 이름과 열의 이름이 이미 말한다. 눈금 글자는 눈으로 읽는 용도다.
    <div className="sticky left-0 z-20 shrink-0 bg-surface" aria-hidden="true">
      <Abs vars={{ '--w': `${AXIS_WIDTH}px`, '--h': `${layout.bodyHeight}px` }} className="relative h-[var(--h)] w-[var(--w)]">
        {layout.axis
          .filter((tick) => tick.hour > 0 && (tick.repeated || tick.hour % every === 0))
          .map((tick) => (
            <Abs
              key={tick.at}
              vars={{ '--y': `${tick.y}px` }}
              className="absolute top-[var(--y)] right-1.5 -translate-y-1/2 text-[10px] leading-none tabular-nums text-ink-3"
            >
              {tick.hour}:00{tick.repeated ? ` ${tick.offset}` : ''}
            </Abs>
          ))}
      </Abs>
    </div>
  );
};

/* ---------------- 열 ---------------- */

interface DayColumnProps {
  day: DayLayout;
  sessionsById: ReadonlyMap<string, PreparedSession>;
  assignment: ColorAssignment;
  highlightKey: string | null;
  selectedId: string | null;
  /** 이 격자의 Tab 정지점(띠 하나). 나머지 띠는 방향키로 간다. */
  tabStop: string | null;
  symbolId: string;
  onBandSelect: (event: ReactMouseEvent<HTMLButtonElement>, band: PlacedBand) => void;
  onBandPointer: (event: ReactPointerEvent<HTMLButtonElement>, band: PlacedBand) => void;
  onBandFocus: (element: HTMLButtonElement, band: PlacedBand) => void;
  inputLabel: (band: PlacedBand) => string | undefined;
  onLeave: () => void;
  labelOf: (band: PlacedBand) => string;
}

const DayColumnView = ({
  day,
  sessionsById,
  assignment,
  highlightKey,
  selectedId,
  tabStop,
  symbolId,
  onBandSelect,
  onBandPointer,
  onBandFocus,
  inputLabel,
  onLeave,
  labelOf,
}: DayColumnProps) => {
  const { column } = day;
  // 겹침은 z-index로 정한다(범위만 그리는 흐린 띠가 아래). DOM 순서는 시간 순으로 두어 Tab·방향키 순서와 맞춘다.
  const bands = day.bands;
  const colorOf = (sessionId: string) => assignment.bySession.get(sessionId);
  // 열의 이름은 "월 10/5"(전환일이면 ", 23시간"). 눈으로 보는 날짜 머리는 같은 말을 되풀이하므로 보조 기술에서 뺀다.
  const name = `${WEEKDAY_LABEL[column.weekday]} ${column.date.month}/${column.date.day}${day.hours !== 24 ? `, ${dayLengthLabel(day.hours)}` : ''}`;
  return (
    <Abs
      role="group"
      aria-label={name}
      aria-current={column.isToday ? 'date' : undefined}
      vars={{ '--h': `${day.height}px`, '--min': `${dayMinWidth(day)}px` }}
      data-day={column.key}
      className={cn(
        'relative h-[var(--h)] min-w-[var(--min)] flex-1 basis-0 border-l border-border-subtle',
        column.isToday ? 'bg-brand-soft/35' : 'bg-surface',
      )}
    >
      {/* 배경: 시 눈금, 발언량 농도 띠. */}
      <svg className="pointer-events-none absolute inset-0 h-full w-full" aria-hidden="true">
        {day.ticks
          .filter((t) => t.y > 0)
          .map((t) => (
            <line key={t.at} x1="0" x2="100%" y1={t.y} y2={t.y} shapeRendering="crispEdges" className={t.repeated ? 'stroke-warning/60' : 'stroke-border-subtle'} />
          ))}
        {day.density.map((d) => (
          <rect key={d.start} x="0" y={d.y} width={DENSITY_WIDTH} height={Math.max(1, d.h - 0.5)} fillOpacity={d.opacity} className="fill-success" />
        ))}
      </svg>

      {/* 전환일 열: 축과 어긋나는 눈금과 반복된 시는 띠보다 위에 직접 적는다. */}
      {day.ticks
        .filter((t) => t.inColumnLabel)
        .map((t) => (
          <Abs
            key={`label-${t.at}`}
            vars={{ '--y': `${t.y}px` }}
            className="pointer-events-none absolute top-[var(--y)] left-[11px] z-[6] -translate-y-1/2 rounded bg-surface/95 px-0.5 text-[9px] leading-none tabular-nums text-warning-strong"
          >
            {t.label}
          </Abs>
        ))}

      {/* 레인 영역: 왼쪽 9px은 농도 띠(6px)와 간격이다. */}
      <div className="absolute inset-y-0 right-[3px] left-[9px]">
        {/* 연결선: 같은 세션의 띠 사이의 공백(C8). */}
        {day.connectors.map((c) => {
          const color = colorOf(c.sessionId);
          const dimmed = highlightKey !== null && color?.entryKey !== highlightKey;
          return (
            <span key={c.key}>
              <Abs
                aria-hidden="true"
                vars={{ '--y': `${c.y}px`, '--h': `${c.h}px`, '--x': `${c.xPct}%`, '--c': color?.token ?? 'var(--ink-4)' }}
                className={cn(
                  'pointer-events-none absolute top-[var(--y)] left-[var(--x)] z-[1] h-[var(--h)] w-px -translate-x-1/2 bg-[color-mix(in_oklch,var(--c)_75%,transparent)]',
                  dimmed && 'opacity-20',
                )}
              />
              {c.elbow && (
                <Abs
                  aria-hidden="true"
                  vars={{ '--y': `${c.elbow.y}px`, '--l': `${c.elbow.fromPct}%`, '--w': `${c.elbow.toPct - c.elbow.fromPct}%`, '--c': color?.token ?? 'var(--ink-4)' }}
                  className={cn(
                    'pointer-events-none absolute top-[var(--y)] left-[var(--l)] z-[1] h-px w-[var(--w)] bg-[color-mix(in_oklch,var(--c)_75%,transparent)]',
                    dimmed && 'opacity-20',
                  )}
                />
              )}
            </span>
          );
        })}

        {/* 띠. 범위만 그리는 흐린 띠가 아래, 활동 띠가 위다. */}
        {bands.map((band) => {
          const color = colorOf(band.sessionId);
          const session = sessionsById.get(band.sessionId);
          const title = session ? titleOf(session.source).text : band.sessionId;
          const project = session?.source.colors.project?.name || session?.source.colors.project?.hash || '프로젝트 미확인';
          const selected = band.sessionId === selectedId;
          const dimmed = highlightKey !== null && color?.entryKey !== highlightKey;
          return (
            <AbsButton
              key={band.key}
              type="button"
              data-calendar-band=""
              data-band-key={gridKey(column.key, band.key)}
              data-faint={band.faint ? '' : undefined}
              data-session-id={band.sessionId}
              tabIndex={gridKey(column.key, band.key) === tabStop ? 0 : -1}
              title={`${title} · 프로젝트 ${project}`}
              aria-label={labelOf(band)}
              aria-description={inputLabel(band)}
              aria-current={selected ? 'true' : undefined}
              vars={{
                '--y': `${band.y}px`,
                '--h': `${band.h}px`,
                '--l': `${band.leftPct}%`,
                '--w': `${band.widthPct}%`,
                '--c': color?.token ?? 'var(--ink-4)',
              }}
              onClick={(event) => onBandSelect(event, band)}
              onPointerEnter={(event) => onBandPointer(event, band)}
              onPointerMove={(event) => onBandPointer(event, band)}
              onPointerLeave={onLeave}
              onFocus={(event) => onBandFocus(event.currentTarget, band)}
              onBlur={onLeave}
              className={cn(
                '@container absolute top-[var(--y)] left-[calc(var(--l)_+_1px)] box-border h-[var(--h)] w-[calc(var(--w)_-_2px)] scroll-mt-[46px] scroll-ml-[54px] cursor-pointer overflow-hidden rounded-[4px] border-[1.5px] border-[var(--c)] p-0 transition-opacity focus-visible:z-[4]',
                band.faint
                  ? 'z-0 border-dashed bg-[color-mix(in_oklch,var(--c)_6%,var(--surface))] opacity-55'
                  : 'z-[2] bg-[color-mix(in_oklch,var(--c)_17%,var(--surface))]',
                // 선택(C13): 굵은 윤곽선.
                selected && 'z-[3] ring-[3px] ring-ink',
                dimmed && !selected && 'opacity-20',
                band.continuesBefore && 'rounded-t-none border-t-0',
                band.continuesAfter && 'rounded-b-none border-b-0',
              )}
            >
              {band.h >= 20 && (
                <span aria-hidden="true" className="pointer-events-none absolute inset-x-0 top-0 z-[1] hidden bg-surface/90 px-1 py-0.5 text-left text-[10px] leading-3 text-ink @min-[56px]:block">
                  <span className="block truncate font-medium">{title}</span>
                  {band.h >= 36 && <span className="block truncate text-[9px] text-ink-3">{project}</span>}
                </span>
              )}
              {(band.stripes.length > 0 || band.bubbles.length > 0) && (
                <svg className="pointer-events-none absolute inset-0 h-full w-full" aria-hidden="true">
                  {/* 입력 가로선(C9): 입력이 몰린 띠는 줄무늬로 보인다. 선 하나는 2px이고 가운데가 입력 시각이다. */}
                  {band.bubbles.length === 0 &&
                    band.stripes.map((y) => <rect key={y} x="0" y={y - 0.5} width="100%" height="2" fillOpacity={0.9} className="fill-[var(--c)]" />)}
                  {/* 확대하면 입력 하나하나가 말풍선이다(C16). */}
                  {band.bubbles.map((y) => (
                    <use key={y} href={`#${symbolId}`} x="3" y={Math.max(0, y - 5)} width="12" height="11" className="fill-[var(--c)]" />
                  ))}
                </svg>
              )}
            </AbsButton>
          );
        })}
      </div>

      {/* 현재 시각 선(C12): 오늘 열에만, 띠 위에 그린다. */}
      {day.nowY !== null && (
        <svg className="pointer-events-none absolute inset-0 z-[5] h-full w-full" aria-hidden="true" data-now-line="">
          <line x1="0" x2="100%" y1={day.nowY} y2={day.nowY} strokeWidth={2} className="stroke-danger" />
          <circle cx="4" cy={day.nowY} r="3.5" className="fill-danger" />
        </svg>
      )}
    </Abs>
  );
};

/* ---------------- 격자 ---------------- */

const CalendarGrid = ({ layout, sessionsById, assignment, highlightKey, selectedId, onSelect, timeZone, pxPerHour, scrollerRef, inert }: CalendarGridProps) => {
  const reactId = useId();
  const tipId = `${reactId}-tip`;
  const symbolId = `${reactId}-bubble`;
  const bodyRef = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<HoverTarget | null>(null);
  const [lastFocused, setLastFocused] = useState<string | null>(null);

  // Tab 정지점은 하나: 마지막에 포커스가 있던 띠, 없으면 고른 세션의 첫 띠. 나머지는 방향키로 간다.
  const focusDays: FocusDay[] = layout.days.map((day) => ({
    columnKey: day.column.key,
    start: day.column.start,
    bands: day.bands.map((b) => ({ key: b.key, sessionId: b.sessionId, faint: b.faint, start: b.start })),
  }));
  const tabStop = tabStopKey(focusDays, lastFocused, selectedId);

  /** 포인터(화면 좌표)를 격자 안의 좌표로 바꾸고, 카드가 보이는 영역을 넘칠지 본다. */
  const placeAt = (clientX: number, clientY: number, kind: 'band' | 'input'): Placed | null => {
    const body = bodyRef.current;
    if (!body) return null;
    const rect = body.getBoundingClientRect();
    const scroller = scrollerRef.current;
    const x = clientX - rect.left;
    const y = clientY - rect.top;
    const visibleRight = (scroller?.scrollLeft ?? 0) + (scroller?.clientWidth ?? rect.width);
    // 본문의 위쪽은 스크롤 내용 안에서 날짜 머리 아래에 있다. 보이는 아래쪽 끝을 본문 좌표로 환산한다.
    const visibleBottom = (scroller?.scrollTop ?? 0) + (scroller?.clientHeight ?? rect.height) - HEADER_HEIGHT;
    return {
      x,
      y,
      flip: x + CARD_WIDTH + 2 * CARD_GAP > visibleRight,
      flipY: y + CARD_GAP + CARD_HEIGHT[kind] > visibleBottom && y - CARD_GAP - CARD_HEIGHT[kind] > (scroller?.scrollTop ?? 0) - HEADER_HEIGHT,
    };
  };

  const show = (next: HoverTarget) => setHover((prev) => (prev && prev.key === next.key && prev.kind === next.kind && prev.keyboard === next.keyboard && (prev.kind !== 'input' || next.kind !== 'input' || prev.inputIndex === next.inputIndex) ? prev : next));

  const pointedInputIndex = (element: HTMLButtonElement, clientY: number, band: PlacedBand, session: PreparedSession): number => {
    if (band.faint || session.inputs.length === 0) return -1;
    const at = band.start + ((clientY - element.getBoundingClientRect().top) / pxPerHour) * HOUR_MS;
    const index = nearestIndex(session.inputTimes, at, toleranceMsFor(INPUT_HIT_PX, pxPerHour));
    return index >= 0 && session.inputTimes[index] >= band.start &&
      (band.start === band.end ? session.inputTimes[index] === band.end : session.inputTimes[index] < band.end) ? index : -1;
  };

  /** 입력선을 선택하면 그 입력의 정확한 턴과 시각을 대화 목적지로 넘긴다. */
  const handleBandSelect = (event: ReactMouseEvent<HTMLButtonElement>, band: PlacedBand) => {
    const session = sessionsById.get(band.sessionId);
    const index = !session ? -1 : event.detail > 0
      ? pointedInputIndex(event.currentTarget, event.clientY, band, session)
      : hover?.keyboard && hover.kind === 'input' && hover.key === band.key ? hover.inputIndex : -1;
    const input = session?.inputs[index];
    if (input) onSelect(band.sessionId, input.turnKey || undefined, new Date(input.at).toISOString());
    else onSelect(band.sessionId);
  };

  /** 포인터가 띠 위에 있다: 가까운 입력선이 있으면 그것, 없으면 띠. */
  const handleBandPointer = (event: ReactPointerEvent<HTMLButtonElement>, band: PlacedBand) => {
    const session = sessionsById.get(band.sessionId);
    if (!session) return;
    if (!band.faint && session.inputs.length > 0) {
      const index = pointedInputIndex(event.currentTarget, event.clientY, band, session);
      if (index >= 0) {
        const placed = placeAt(event.clientX, event.clientY, 'input');
        if (placed) {
          show({ kind: 'input', key: `${band.key}:${index}`, sessionId: band.sessionId, inputIndex: index, ...placed });
          return;
        }
      }
    }
    const placed = placeAt(event.clientX, event.clientY, 'band');
    if (placed) show({ kind: 'band', key: band.key, sessionId: band.sessionId, segmentIndex: band.segmentIndex, ...placed });
  };

  const inputIndicesFor = (session: PreparedSession, band: PlacedBand): number[] => {
    if (band.faint) return [];
    const indices: number[] = [];
    for (let i = 0; i < session.inputs.length; i++) {
      const at = session.inputs[i].at;
      if (at >= band.start && (band.start === band.end ? at === band.end : at < band.end)) indices.push(i);
      if (at > band.end) break;
    }
    return indices;
  };

  /** 키보드로 포커스가 닿았다: 띠의 위쪽 모서리 옆에 카드를 띄운다. PageDown/Up으로 이 띠의 개별 입력을 순서대로 읽는다. */
  const handleBandFocus = (element: HTMLButtonElement, band: PlacedBand) => {
    if (element.dataset.bandKey) setLastFocused(element.dataset.bandKey);
    const rect = element.getBoundingClientRect();
    const placed = placeAt(rect.left + 10, rect.top + 6, 'band');
    if (placed) show({ kind: 'band', key: band.key, sessionId: band.sessionId, segmentIndex: band.segmentIndex, keyboard: true, ...placed });
  };

  const hoveredSession = hover ? sessionsById.get(hover.sessionId) : undefined;
  const keyboardInput = hover?.keyboard && hover.kind === 'input' && hoveredSession ? hoveredSession.inputs[hover.inputIndex] : undefined;
  const keyboardInputContent = keyboardInput && hoveredSession ? inputHover(hoveredSession, keyboardInput, timeZone) : undefined;
  const inputAnnouncement = keyboardInputContent ? `${keyboardInputContent.time}, 입력, ${keyboardInputContent.line ?? keyboardInputContent.note ?? ''}, ${keyboardInputContent.title}` : '';
  const labelOf = (band: PlacedBand): string => {
    const session = sessionsById.get(band.sessionId);
    return session ? bandLabel(session, band.segmentIndex, timeZone) : band.sessionId;
  };

  return (
    <div
      ref={scrollerRef}
      inert={inert}
      role="region"
      aria-label="주간 세션 캘린더"
      tabIndex={0}
      onKeyDown={(event) => {
        if (event.key === 'Escape') {
          setHover(null);
          return;
        }
        const focusedBand = event.target instanceof HTMLElement ? event.target.dataset.bandKey : undefined;
        if ((event.key === 'PageDown' || event.key === 'PageUp') && focusedBand && !event.ctrlKey && !event.metaKey && !event.altKey) {
          event.preventDefault();
          const band = layout.days.flatMap((day) => day.bands.map((entry) => ({ key: gridKey(day.column.key, entry.key), entry }))).find((item) => item.key === focusedBand)?.entry;
          const session = band && sessionsById.get(band.sessionId);
          const indices = band && session ? inputIndicesFor(session, band) : [];
          if (band && session && indices.length > 0) {
            const previous = hover?.keyboard && hover.kind === 'input' && hover.key === band.key ? indices.indexOf(hover.inputIndex) : -1;
            const next = event.key === 'PageDown' ? Math.min(indices.length - 1, previous + 1) : Math.max(0, previous < 0 ? indices.length - 1 : previous - 1);
            const element = event.target as HTMLElement;
            const rect = element.getBoundingClientRect();
            const placed = placeAt(rect.left + 10, rect.top + 6, 'input');
            if (placed) show({ kind: 'input', key: band.key, sessionId: band.sessionId, inputIndex: indices[next], keyboard: true, ...placed });
          }
          return;
        }
        // 띠에서 방향키: 이웃 띠로 포커스를 옮긴다(Tab 정지점이 하나뿐이라서).
        const move = moveOfKey(event.key);
        const from = event.target instanceof HTMLElement ? event.target.dataset.bandKey : undefined;
        if (!move || !from || event.ctrlKey || event.metaKey || event.altKey) return;
        const to = moveFocus(focusDays, from, move);
        event.preventDefault();
        if (!to) return;
        const el = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('[data-calendar-band]')).find((b) => b.dataset.bandKey === to);
        el?.focus();
      }}
      className="relative min-h-0 flex-1 overflow-auto bg-surface focus-visible:-outline-offset-2!"
    >
      <span aria-live="polite" className="sr-only">{inputAnnouncement}</span>
      {/* 말풍선 기호. 입력마다 `<use>`로 가져다 쓴다. */}
      <svg width="0" height="0" className="absolute" aria-hidden="true" focusable="false">
        <defs>
          <symbol id={symbolId} viewBox="0 0 12 11">
            <path d="M2 .5h8A1.5 1.5 0 0 1 11.5 2v4.5A1.5 1.5 0 0 1 10 8H6.2L3.6 10.4V8H2A1.5 1.5 0 0 1 .5 6.5V2A1.5 1.5 0 0 1 2 .5z" />
          </symbol>
        </defs>
      </svg>

      <Abs vars={{ '--min': `${AXIS_WIDTH + layout.days.reduce((sum, day) => sum + dayMinWidth(day), 0)}px` }} className="min-w-[var(--min)]">
        {/* 날짜 머리(C6): 오늘 열은 머리를 강조한다. */}
        <Abs
          vars={{ '--h': `${HEADER_HEIGHT}px` }}
          className="sticky top-0 z-30 flex h-[var(--h)] border-b border-border bg-surface"
          aria-hidden="true"
        >
          <Abs vars={{ '--w': `${AXIS_WIDTH}px` }} className="sticky left-0 z-40 w-[var(--w)] shrink-0 bg-surface" aria-hidden="true" />
          {layout.days.map((day) => {
            const { column } = day;
            const irregular = day.hours !== 24;
            return (
              <Abs
                key={column.key}
                data-day-header={column.key}
                data-today={column.isToday ? '' : undefined}
                vars={{ '--min': `${dayMinWidth(day)}px` }}
                className={cn(
                  'flex min-w-[var(--min)] flex-1 basis-0 flex-col items-center justify-center border-l border-border-subtle text-[12px] leading-tight',
                  column.isToday ? 'bg-brand-soft font-bold text-brand' : 'text-ink-2',
                )}
              >
                <span className="max-w-full overflow-hidden whitespace-nowrap tabular-nums text-ellipsis" title={`${WEEKDAY_LABEL[column.weekday]} ${column.date.month}/${column.date.day}`}>
                  {WEEKDAY_LABEL[column.weekday]} {column.date.month}/{column.date.day}
                </span>
                {irregular && (
                  <span title={dayLengthHint(day.hours)} className="mt-0.5 rounded bg-warning-soft px-1 text-[9.5px] font-medium text-warning-strong">
                    {dayLengthLabel(day.hours)}
                  </span>
                )}
              </Abs>
            );
          })}
        </Abs>

        <div ref={bodyRef} className="relative flex items-start bg-surface-sunk">
          <TimeAxis layout={layout} pxPerHour={pxPerHour} />
          {layout.days.map((day) => (
            <DayColumnView
              key={day.column.key}
              day={day}
              sessionsById={sessionsById}
              assignment={assignment}
              highlightKey={highlightKey}
              selectedId={selectedId}
              tabStop={tabStop}
              symbolId={symbolId}
              onBandSelect={handleBandSelect}
              onBandPointer={handleBandPointer}
              onBandFocus={handleBandFocus}
              inputLabel={(band) => {
                const session = sessionsById.get(band.sessionId);
                const count = session ? inputIndicesFor(session, band).length : 0;
                return count > 0 ? `입력 ${count}건. PageDown과 PageUp으로 입력 첫 줄을 읽습니다.` : undefined;
              }}
              onLeave={() => setHover(null)}
              labelOf={labelOf}
            />
          ))}
          {hover && hoveredSession && <HoverCard id={tipId} target={hover} session={hoveredSession} timeZone={timeZone} />}
        </div>
      </Abs>
    </div>
  );
};

export { CalendarGrid, HEADER_HEIGHT, AXIS_WIDTH, COLUMN_MIN_WIDTH };
