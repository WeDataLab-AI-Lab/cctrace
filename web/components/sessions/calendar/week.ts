/**
 * 주 이동(C1). 주는 월요일에 시작하는 지역 날짜 7칸이다. "보는 주"는 그 월요일의 지역 날짜(`YYYY-MM-DD`)로 들고,
 * 그 주가 어떤 절대 구간인지는 시간대로 계산한다 — 같은 월요일이라도 시간대가 다르면 다른 구간이다.
 */

import { CALENDAR_MAX_DAYS } from '@/lib/session-calendar-types';
import { weekWindow, type DayColumn } from './day-grid';
import { DAY_MS, addDays, dateKey, localDateOf, mondayOf, parseDateKey, type LocalDate } from './time-zone';

/** 기준 시각이 든 주의 월요일. */
const todayWeekStart = (timeZone: string, nowMs: number): LocalDate => mondayOf(localDateOf(timeZone, nowMs));

/** 주를 n주 옮긴다(음수는 이전 주). */
const shiftWeek = (weekStart: LocalDate, weeks: number): LocalDate => addDays(weekStart, weeks * 7);

/** 날짜 키를 월요일로 맞춘다. 읽을 수 없으면 null. 어느 요일을 줘도 그 주의 월요일이 된다. */
const weekStartFromKey = (key: string): LocalDate | null => {
  const date = parseDateKey(key);
  return date ? mondayOf(date) : null;
};

/** 서버에 보낼 쿼리 구간(UTC ISO). `[from, to)`이고 14일을 넘지 않는다. */
const weekQueryRange = (timeZone: string, weekStart: LocalDate): { from: string; to: string } => {
  const { from, to } = weekWindow(timeZone, weekStart);
  if ((to - from) / DAY_MS > CALENDAR_MAX_DAYS) throw new Error('week range exceeds the calendar limit');
  return { from: new Date(from).toISOString(), to: new Date(to).toISOString() };
};

/** "2026년 10/5 – 10/11". 해를 넘으면 뒤에도 해를 적는다. */
const weekLabel = (columns: readonly Pick<DayColumn, 'date'>[]): string => {
  if (columns.length === 0) return '';
  const first = columns[0].date;
  const last = columns[columns.length - 1].date;
  const tail = last.year === first.year ? `${last.month}/${last.day}` : `${last.year}년 ${last.month}/${last.day}`;
  return `${first.year}년 ${first.month}/${first.day} – ${tail}`;
};

const sameWeek = (a: LocalDate, b: LocalDate): boolean => dateKey(a) === dateKey(b);

export { todayWeekStart, shiftWeek, weekStartFromKey, weekQueryRange, weekLabel, sameWeek };
