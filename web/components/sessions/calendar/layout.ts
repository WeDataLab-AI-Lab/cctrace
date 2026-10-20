/**
 * 한 주의 화면 배치. 위의 순수 함수들(날짜 열, 띠, 레인, 밀도)을 묶어 "그릴 숫자"를 만든다.
 * 컴포넌트는 이 결과를 그대로 그리기만 한다 — 좌표 계산이 화면 코드에 섞이지 않게 하려는 것이다.
 *
 * 길이는 모두 px, 위치는 열 안의 y(px, 자정이 0)와 레인 영역 안의 가로 비율(%)이다.
 */

import { connectorsFor, dayBands } from './bands';
import { densityBuckets, densityOpacity, maxBucketCount, type DensityBucket } from './density';
import { hourTicks, hoursOf, referenceColumn, yOf, type DayColumn } from './day-grid';
import { assignLanes } from './lanes';
import type { PreparedSession } from './prepare';
import { HOUR_MS } from './time-zone';
import { workingHours, type WorkingHours } from './working-hours';

/** 띠의 최소 높이(px). 짧은 띠도 눌러 볼 수 있어야 한다. */
const MIN_BAND_PX = 4;
/**
 * 이 배율(px/시간) 이상으로 확대하면 입력 가로선이 말풍선 아이콘으로 바뀐다(C16). 표준 배율에서는 가로선이 줄무늬로
 * 읽히지만, 확대하면 입력 하나하나가 따로 보이므로 "여기서 사용자가 말했다"는 표시가 더 알맞다.
 */
const BUBBLE_MIN_PX_PER_HOUR = 96;
/** 말풍선끼리 최소 간격(px). 더 가까운 입력은 아이콘을 생략하고(호버는 그대로 모든 입력을 찾는다) 겹치지 않게 한다. */
const BUBBLE_GAP_PX = 11;
/** 입력 가로선 사이의 최소 간격(px). 입력이 아주 많은 띠도 줄무늬로 보이게 하려고 이보다 가까운 선은 생략한다. */
const STRIPE_GAP_PX = 3;

const showBubbles = (pxPerHour: number): boolean => pxPerHour >= BUBBLE_MIN_PX_PER_HOUR;

export interface PlacedTick {
  at: number;
  y: number;
  hour: number;
  offset: string;
  repeated: boolean;
  /** "9", 반복된 시는 "1 UTC-4". */
  label: string;
  /**
   * 이 열 안에 글자를 쓸지. 시간 축이 이미 알려 주는 눈금은 거짓이다. 전환일 열에서 축과 위치가 어긋나는
   * 눈금(전환 뒤)과 반복된 시는 열 안에 직접 적는다.
   */
  inColumnLabel: boolean;
}

export interface PlacedBand {
  key: string;
  sessionId: string;
  segmentIndex: number;
  faint: boolean;
  start: number;
  end: number;
  segmentStart: number;
  segmentEnd: number;
  continuesBefore: boolean;
  continuesAfter: boolean;
  records: number | null;
  humanInputs: number | null;
  y: number;
  h: number;
  lane: number;
  lanes: number;
  /** 레인 영역 안에서의 왼쪽(%)과 너비(%). */
  leftPct: number;
  widthPct: number;
  /**
   * 띠 안의 입력 가로선(y와 두께, px, 띠 위쪽 기준). 같은 픽셀 줄은 하나로 합치고, 이어진 줄은 한 덩어리로 묶어
   * 입력이 몰린 곳이 굵은 줄무늬가 된다.
   */
  /** 입력 가로선의 y(px, 띠 위쪽 기준). `STRIPE_GAP_PX`보다 가까운 선은 솎은 뒤의 것이다. */
  stripes: number[];
  /** 확대했을 때(`showBubbles`) 가로선 대신 그릴 말풍선의 y(px, 띠 위쪽 기준). 겹치지 않게 솎았다. 확대하지 않았으면 빈 배열. */
  bubbles: number[];
  /** 띠 안에 든 입력 수(합치기 전). */
  inputCount: number;
}

export interface PlacedConnector {
  key: string;
  sessionId: string;
  y: number;
  h: number;
  /** 앞 띠의 레인 가운데(%). 세로선이 이 자리에 선다. */
  xPct: number;
  /** 뒤 띠가 다른 레인이면 뒤 띠 위쪽에서 가로로 꺾어 이어 준다. */
  elbow: { y: number; fromPct: number; toPct: number } | null;
}

export interface PlacedDensity {
  y: number;
  h: number;
  opacity: number;
  count: number;
  start: number;
}

export interface DayLayout {
  column: DayColumn;
  hours: number;
  height: number;
  ticks: PlacedTick[];
  bands: PlacedBand[];
  /** 가장 조밀한 실제 배치 묶음의 레인 수. 열의 읽을 수 있는 너비를 정한다. */
  peakLanes: number;
  connectors: PlacedConnector[];
  density: PlacedDensity[];
  /** 오늘 열이면 현재 시각의 y(px). */
  nowY: number | null;
}

export interface WeekLayout {
  days: DayLayout[];
  /** 시간 축에 쓸 눈금(기준 열의 것). */
  axis: PlacedTick[];
  /** 가장 높은 열의 높이(px). 전환일(25시간) 열이 있으면 그 높이다. */
  bodyHeight: number;
  maxDensity: number;
  working: WorkingHours;
}

export interface LayoutInput {
  timeZone: string;
  columns: readonly DayColumn[];
  sessions: readonly PreparedSession[];
  pxPerHour: number;
  nowMs: number;
}

/** 오름차순 y 중에서 앞의 것과 `gap` 이상 떨어진 것만 남긴다. */
const thin = (ys: readonly number[], gap: number): number[] => {
  const kept: number[] = [];
  for (const y of ys) if (kept.length === 0 || y - kept[kept.length - 1] >= gap) kept.push(y);
  return kept;
};

/** 정렬된 `at` 배열에서 [from, to) 구간. 길이 0인 조각은 그 시각의 입력을 포함한다. */
const sliceInputs = <T extends { at: number }>(sorted: readonly T[], from: number, to: number): T[] => {
  let lo = 0;
  let hi = sorted.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (sorted[mid].at < from) lo = mid + 1;
    else hi = mid;
  }
  const out: T[] = [];
  for (let i = lo; i < sorted.length && (from === to ? sorted[i].at === to : sorted[i].at < to); i++) out.push(sorted[i]);
  return out;
};

const tickLabel = (hour: number, repeated: boolean, offset: string): string => (repeated ? `${hour} ${offset}` : String(hour));

const placeTicks = (
  timeZone: string,
  column: DayColumn,
  pxPerHour: number,
  reference: ReadonlyMap<number, number> | null,
): PlacedTick[] =>
  hourTicks(timeZone, column).map((tick) => {
    const y = yOf(column, tick.at, pxPerHour);
    const axisY = reference?.get(tick.hour);
    // 축의 같은 시와 0.5px 넘게 어긋나는 눈금(전환 뒤)은 열 안에 직접 적는다. 축 자체(reference가 null)는 쓰지 않는다.
    const shifted = reference !== null && axisY !== undefined && Math.abs(axisY - y) > 0.5;
    return {
      at: tick.at,
      y,
      hour: tick.hour,
      offset: tick.offset,
      repeated: tick.repeated,
      label: tickLabel(tick.hour, tick.repeated, tick.offset),
      inColumnLabel: tick.repeated || shifted,
    };
  });

const layoutWeek = ({ timeZone, columns, sessions, pxPerHour, nowMs }: LayoutInput): WeekLayout => {
  const ref = referenceColumn(columns);
  const axis = ref ? placeTicks(timeZone, ref, pxPerHour, null) : [];
  // 시 → 축에서의 y. 반복된 시는 첫 눈금을 쓴다.
  const axisByHour = new Map<number, number>();
  for (const t of axis) if (!axisByHour.has(t.hour)) axisByHour.set(t.hour, t.y);

  const allInputTimes = sessions.flatMap((s) => (s.faint ? [] : s.inputs.map((i) => i.at)));
  const bucketsByDay: DensityBucket[][] = columns.map((c) => densityBuckets(c, allInputTimes));
  const maxDensity = maxBucketCount(bucketsByDay);
  const sessionById = new Map(sessions.map((s) => [s.id, s]));

  const days: DayLayout[] = columns.map((column, dayIndex) => {
    const bands = dayBands(sessions, column);
    // 범위만 그리는 흐린 띠는 활동이 아니라 범위라서 레인을 차지하지 않는다: 열 너비로 깔고 그 위에 다른 띠가 선다.
    const placements = assignLanes(bands.filter((b) => !b.faint).map((b) => ({ key: b.key, sessionId: b.sessionId, start: b.start, end: b.end })));
    let peakLanes = 0;
    for (const place of placements.values()) peakLanes = Math.max(peakLanes, place.lanes);

    const placedBands: PlacedBand[] = bands.map((band) => {
      const place = placements.get(band.key) ?? { lane: 0, lanes: 1, group: 0 };
      const y = yOf(column, band.start, pxPerHour);
      const rawH = ((band.end - band.start) / HOUR_MS) * pxPerHour;
      const session = sessionById.get(band.sessionId);
      const inside = session && !session.faint ? sliceInputs(session.inputs, band.start, band.end) : [];
      const ys = inside.map((input) => ((input.at - band.start) / HOUR_MS) * pxPerHour);
      return {
        ...band,
        y,
        h: Math.max(MIN_BAND_PX, rawH),
        lane: place.lane,
        lanes: place.lanes,
        leftPct: (place.lane / place.lanes) * 100,
        widthPct: 100 / place.lanes,
        stripes: thin(ys.map(Math.round), STRIPE_GAP_PX),
        bubbles: showBubbles(pxPerHour) ? thin(ys, BUBBLE_GAP_PX) : [],
        inputCount: inside.length,
      };
    });

    const byKey = new Map(placedBands.map((b) => [b.key, b]));
    const connectors: PlacedConnector[] = connectorsFor(bands).flatMap((c) => {
      const from = byKey.get(c.fromKey);
      const to = byKey.get(c.toKey);
      if (!from || !to) return [];
      const y = yOf(column, c.from, pxPerHour);
      const xPct = from.leftPct + from.widthPct / 2;
      const toPct = to.leftPct + to.widthPct / 2;
      return [
        {
          key: `${c.fromKey}>${c.toKey}`,
          sessionId: c.sessionId,
          y,
          h: Math.max(0, yOf(column, c.to, pxPerHour) - y),
          xPct,
          elbow: Math.abs(toPct - xPct) > 0.5 ? { y: yOf(column, c.to, pxPerHour), fromPct: Math.min(xPct, toPct), toPct: Math.max(xPct, toPct) } : null,
        },
      ];
    });

    const density: PlacedDensity[] = bucketsByDay[dayIndex].map((b) => ({
      y: yOf(column, b.start, pxPerHour),
      h: ((b.end - b.start) / HOUR_MS) * pxPerHour,
      opacity: densityOpacity(b.count, maxDensity),
      count: b.count,
      start: b.start,
    }));

    return {
      column,
      hours: hoursOf(column),
      height: hoursOf(column) * pxPerHour,
      ticks: placeTicks(timeZone, column, pxPerHour, column === ref ? null : axisByHour),
      bands: placedBands,
      peakLanes,
      connectors,
      density,
      nowY: nowMs >= column.start && nowMs < column.end ? yOf(column, nowMs, pxPerHour) : null,
    };
  });

  return {
    days,
    axis,
    bodyHeight: days.reduce((m, d) => Math.max(m, d.height), 0),
    maxDensity,
    working: workingHours(columns.map((column, i) => ({ start: column.start, bands: days[i].bands }))),
  };
};

export { MIN_BAND_PX, BUBBLE_MIN_PX_PER_HOUR, BUBBLE_GAP_PX, STRIPE_GAP_PX, showBubbles, thin, layoutWeek, sliceInputs };
