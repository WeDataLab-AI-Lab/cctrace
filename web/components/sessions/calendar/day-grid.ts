/**
 * 날짜 열과 시간 눈금(계획 §5.4 "시간대와 DST").
 *
 * 날짜 열 하나는 "지역 자정 ~ 다음 지역 자정"의 **절대 시간 구간**이다. y 좌표는 자정부터 흐른 절대 시간에
 * 비례하므로, 봄에 한 시간을 건너뛰는 날(23시간)과 가을에 한 시간이 반복되는 날(25시간)은 열 높이가 다르다.
 * 시간 눈금은 지역 시각의 정시마다 긋는다. 반복되는 시는 두 번 긋고 UTC 오프셋을 적는다. 건너뛴 시는 긋지 않는다.
 *
 * 이 파일은 React를 모른다. 화면은 여기서 나온 숫자를 그대로 그린다.
 */

import {
  HOUR_MS,
  addDays,
  dateKey,
  offsetLabel,
  offsetMinutesAt,
  weekdayOf,
  zonedMidnight,
  type LocalDate,
} from './time-zone';

export interface DayColumn {
  /** `YYYY-MM-DD`(그 시간대의 지역 날짜). */
  key: string;
  date: LocalDate;
  /** 0 = 일요일 ... 6 = 토요일. */
  weekday: number;
  /** 지역 자정의 절대 시각(ms). 포함. */
  start: number;
  /** 다음 지역 자정의 절대 시각(ms). 제외. */
  end: number;
  /** 기준 시각이 이 열 안에 있는가. */
  isToday: boolean;
}

/** 열이 흐르는 시간(시간 단위). 보통 24, 전환일은 23이나 25. 30분 단위 전환(로드하우)은 23.5·24.5. */
const hoursOf = (column: Pick<DayColumn, 'start' | 'end'>): number => (column.end - column.start) / HOUR_MS;

/** 한 주(월요일부터 7열). `weekStart`는 월요일이어야 한다. */
const weekColumns = (timeZone: string, weekStart: LocalDate, nowMs: number): DayColumn[] => {
  const dates = Array.from({ length: 8 }, (_, i) => addDays(weekStart, i));
  const starts = dates.map((d) => zonedMidnight(timeZone, d));
  return dates.slice(0, 7).map((date, i) => ({
    key: dateKey(date),
    date,
    weekday: weekdayOf(date),
    start: starts[i],
    end: starts[i + 1],
    isToday: nowMs >= starts[i] && nowMs < starts[i + 1],
  }));
};

/** 한 주의 요청 구간 `[from, to)`. 서버에 보내는 값이다(UTC 절대 시각). */
const weekWindow = (timeZone: string, weekStart: LocalDate): { from: number; to: number } => ({
  from: zonedMidnight(timeZone, weekStart),
  to: zonedMidnight(timeZone, addDays(weekStart, 7)),
});

/** 열 안의 절대 시각의 y(px). 자정이 0이다. */
const yOf = (column: Pick<DayColumn, 'start'>, ms: number, pxPerHour: number): number => ((ms - column.start) / HOUR_MS) * pxPerHour;

const columnHeight = (column: Pick<DayColumn, 'start' | 'end'>, pxPerHour: number): number => hoursOf(column) * pxPerHour;

export interface HourTick {
  /** 눈금의 절대 시각(ms). */
  at: number;
  /** 지역 시(0~23). */
  hour: number;
  /** 그 순간의 UTC 오프셋(분). */
  offsetMinutes: number;
  /** "UTC-4" 같은 표기. */
  offset: string;
  /** 같은 지역 시가 이 열에 두 번 있다(가을 전환). 두 눈금 모두 참이다. */
  repeated: boolean;
}

/**
 * 열의 시 눈금. 지역 시각이 정시(00분)인 순간마다 하나다.
 *
 * 지역 정시 L(지역 벽시계 값)에 대응하는 절대 시각은 `L - 오프셋`이다. 하루 안에서 오프셋은 많아야 둘(전환 전·후)이라,
 * 열의 매 30분에서 오프셋을 모아 후보를 만들고 "실제 오프셋이 후보를 만든 오프셋과 같은가"로 거른다.
 * 건너뛴 시는 눈금이 없고, 반복된 시는 눈금이 둘이다.
 */
const hourTicks = (timeZone: string, column: Pick<DayColumn, 'date' | 'start' | 'end'>): HourTick[] => {
  const offsets = Array.from(new Set(
    Array.from({ length: Math.ceil(hoursOf(column) * 2) + 1 }, (_, halfHour) =>
      offsetMinutesAt(timeZone, Math.min(column.end - 1, column.start + halfHour * HOUR_MS / 2))),
  ));
  const found: HourTick[] = [];
  for (let hour = 0; hour < 24; hour++) {
    const wall = Date.UTC(column.date.year, column.date.month - 1, column.date.day, hour, 0, 0);
    const candidates = new Map<number, number>();
    for (const offset of offsets) {
      const at = wall - offset * 60_000;
      if (at < column.start || at >= column.end) continue;
      if (offsetMinutesAt(timeZone, at) !== offset) continue;
      candidates.set(at, offset);
    }
    const repeated = candidates.size > 1;
    for (const [at, offset] of candidates) {
      found.push({ at, hour, offsetMinutes: offset, offset: offsetLabel(offset), repeated });
    }
  }
  return found.sort((a, b) => a.at - b.at);
};

/**
 * 시간 축에 쓸 기준 열. 정상적인 24시간 열이 있으면 그것, 없으면 첫 열이다.
 * 전환일의 열은 축과 어긋나므로(23·25시간) 기준으로 삼지 않는다.
 */
const referenceColumn = <T extends Pick<DayColumn, 'start' | 'end'>>(columns: readonly T[]): T | undefined =>
  columns.find((c) => hoursOf(c) === 24) ?? columns[0];

export { hoursOf, weekColumns, weekWindow, yOf, columnHeight, hourTicks, referenceColumn };
