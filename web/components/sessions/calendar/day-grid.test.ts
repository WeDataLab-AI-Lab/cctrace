import { describe, expect, it } from 'vitest';
import { columnHeight, hourTicks, hoursOf, referenceColumn, weekColumns, weekWindow, yOf, type DayColumn } from './day-grid';
import { HOUR_MS, type LocalDate } from './time-zone';

const utc = (iso: string): number => Date.parse(iso);
/** 이 시험의 기준 시각. 서울 수요일 15:40(뉴욕으로는 같은 수요일 새벽 2:40)이다. 미리보기의 기준 시각과 무관하다. */
const NOW = utc('2026-10-07T06:40:00Z');
const hoursOfDay = (tz: string, date: LocalDate): number => {
  const [col] = weekColumns(tz, date, 0);
  return hoursOf(col);
};

describe('날짜 열: 지역 자정에서 다음 지역 자정까지의 절대 구간', () => {
  it('서울의 평범한 주: 7열, 모두 24시간, 월요일이 첫 열이고 기준 시각이 든 열만 오늘이다', () => {
    const columns = weekColumns('Asia/Seoul', { year: 2026, month: 10, day: 5 }, NOW);
    expect(columns.map((c) => c.key)).toEqual([
      '2026-10-05',
      '2026-10-06',
      '2026-10-07',
      '2026-10-08',
      '2026-10-09',
      '2026-10-10',
      '2026-10-11',
    ]);
    expect(columns.map((c) => c.weekday)).toEqual([1, 2, 3, 4, 5, 6, 0]);
    expect(columns.every((c) => hoursOf(c) === 24)).toBe(true);
    expect(columns[0].start).toBe(utc('2026-10-04T15:00:00Z'));
    expect(columns[6].end).toBe(utc('2026-10-11T15:00:00Z'));
    // 열은 서로 맞닿는다.
    for (let i = 1; i < 7; i++) expect(columns[i].start).toBe(columns[i - 1].end);
    // 2026-10-07T06:40Z = 서울 수요일 15:40.
    expect(columns.filter((c) => c.isToday).map((c) => c.key)).toEqual(['2026-10-07']);
  });

  it('같은 순간이라도 시간대가 다르면 다른 열이다(뉴욕은 같은 수요일 새벽 2:40)', () => {
    const columns = weekColumns('America/New_York', { year: 2026, month: 10, day: 5 }, NOW);
    expect(columns.filter((c) => c.isToday).map((c) => c.key)).toEqual(['2026-10-07']);
    const late = weekColumns('America/New_York', { year: 2026, month: 10, day: 5 }, utc('2026-10-07T03:30:00Z'));
    // 수요일 03:30Z는 뉴욕에서 화요일 23:30이다.
    expect(late.filter((c) => c.isToday).map((c) => c.key)).toEqual(['2026-10-06']);
  });

  it('일요일 지역 자정은 마지막 열에만 들어가고 다음 월요일은 이 주에서 빠진다', () => {
    const columns = weekColumns('Asia/Seoul', { year: 2026, month: 10, day: 5 }, utc('2026-10-10T15:00:00Z'));
    expect(columns.filter((c) => c.isToday).map((c) => c.key)).toEqual(['2026-10-11']);
    expect(columns[6].start).toBe(utc('2026-10-10T15:00:00Z'));
    expect(columns[6].end).toBe(utc('2026-10-11T15:00:00Z'));
    expect(weekColumns('Asia/Seoul', { year: 2026, month: 10, day: 5 }, columns[6].end).some((c) => c.isToday)).toBe(false);
  });

  it('주 구간 [from, to): 평범한 주는 168시간', () => {
    const w = weekWindow('Asia/Seoul', { year: 2026, month: 10, day: 5 });
    expect((w.to - w.from) / HOUR_MS).toBe(168);
    expect(w.from).toBe(utc('2026-10-04T15:00:00Z'));
  });
});

describe('뉴욕의 전환일 2026: 열 높이가 달라진다', () => {
  it('봄(3/8 일요일): 그 열은 23시간, 주 구간은 167시간', () => {
    const week = { year: 2026, month: 3, day: 2 };
    const columns = weekColumns('America/New_York', week, 0);
    expect(columns.map(hoursOf)).toEqual([24, 24, 24, 24, 24, 24, 23]);
    expect(columns[6].key).toBe('2026-03-08');
    expect(columns[6].start).toBe(utc('2026-03-08T05:00:00Z'));
    expect(columns[6].end).toBe(utc('2026-03-09T04:00:00Z'));
    const w = weekWindow('America/New_York', week);
    expect((w.to - w.from) / HOUR_MS).toBe(167);
  });

  it('가을(11/1 일요일): 그 열은 25시간, 주 구간은 169시간', () => {
    const week = { year: 2026, month: 10, day: 26 };
    const columns = weekColumns('America/New_York', week, 0);
    expect(columns.map(hoursOf)).toEqual([24, 24, 24, 24, 24, 24, 25]);
    expect(columns[6].key).toBe('2026-11-01');
    expect(columns[6].start).toBe(utc('2026-11-01T04:00:00Z'));
    expect(columns[6].end).toBe(utc('2026-11-02T05:00:00Z'));
    const w = weekWindow('America/New_York', week);
    expect((w.to - w.from) / HOUR_MS).toBe(169);
  });

  it('y는 자정부터 흐른 절대 시간에 비례하고, 열 높이는 그 날의 길이를 따른다', () => {
    const [spring] = weekColumns('America/New_York', { year: 2026, month: 3, day: 8 }, 0);
    // 3시(EDT) = 07:00Z는 자정(05:00Z)에서 2시간 뒤다. 벽시계로는 3시간째지만 y는 2시간이다.
    expect(yOf(spring, utc('2026-03-08T07:00:00Z'), 30)).toBe(60);
    expect(columnHeight(spring, 30)).toBe(23 * 30);
    const [fall] = weekColumns('America/New_York', { year: 2026, month: 11, day: 1 }, 0);
    // 반복된 1시의 둘째 번(EST) = 06:00Z는 자정(04:00Z)에서 2시간 뒤다.
    expect(yOf(fall, utc('2026-11-01T06:00:00Z'), 30)).toBe(60);
    expect(columnHeight(fall, 30)).toBe(25 * 30);
  });

  it('다른 시간대의 같은 날짜는 영향받지 않는다(서울은 늘 24시간)', () => {
    expect(hoursOfDay('Asia/Seoul', { year: 2026, month: 3, day: 8 })).toBe(24);
    expect(hoursOfDay('Asia/Seoul', { year: 2026, month: 11, day: 1 })).toBe(24);
  });
});

describe('시간 눈금: 지역 벽시계의 정시마다', () => {
  const ticksOf = (tz: string, date: LocalDate) => {
    const [col] = weekColumns(tz, date, 0);
    return { col, ticks: hourTicks(tz, col) };
  };

  it('서울의 평범한 날: 0시~23시 24개, 한 시간 간격, 반복 없음, 오프셋 UTC+9', () => {
    const { col, ticks } = ticksOf('Asia/Seoul', { year: 2026, month: 10, day: 5 });
    expect(ticks.map((t) => t.hour)).toEqual(Array.from({ length: 24 }, (_, i) => i));
    expect(ticks[0].at).toBe(col.start);
    for (let i = 1; i < ticks.length; i++) expect(ticks[i].at - ticks[i - 1].at).toBe(HOUR_MS);
    expect(ticks.every((t) => !t.repeated && t.offset === 'UTC+9' && t.offsetMinutes === 540)).toBe(true);
  });

  it('뉴욕 봄 전환일(2026-03-08): 2시는 건너뛰어 긋지 않는다 — 23개', () => {
    const { col, ticks } = ticksOf('America/New_York', { year: 2026, month: 3, day: 8 });
    expect(ticks.map((t) => t.hour)).toEqual([0, 1, ...Array.from({ length: 21 }, (_, i) => i + 3)]);
    expect(ticks).toHaveLength(23);
    expect(ticks.some((t) => t.hour === 2)).toBe(false);
    // 3시는 EDT(-4)이고 1시는 EST(-5)다.
    expect(ticks.find((t) => t.hour === 1)?.offset).toBe('UTC-5');
    expect(ticks.find((t) => t.hour === 3)?.offset).toBe('UTC-4');
    // 1시와 3시 눈금 사이는 절대 시간으로 한 시간이다.
    const one = ticks.find((t) => t.hour === 1);
    const three = ticks.find((t) => t.hour === 3);
    expect((three?.at ?? 0) - (one?.at ?? 0)).toBe(HOUR_MS);
    expect(ticks.every((t) => !t.repeated)).toBe(true);
    expect(ticks.every((t) => t.at >= col.start && t.at < col.end)).toBe(true);
  });

  it('뉴욕 가을 전환일(2026-11-01): 1시가 두 번 — 먼저 EDT(UTC-4), 다음 EST(UTC-5) — 25개', () => {
    const { col, ticks } = ticksOf('America/New_York', { year: 2026, month: 11, day: 1 });
    expect(ticks).toHaveLength(25);
    const ones = ticks.filter((t) => t.hour === 1);
    expect(ones).toHaveLength(2);
    expect(ones.map((t) => t.offset)).toEqual(['UTC-4', 'UTC-5']);
    expect(ones.every((t) => t.repeated)).toBe(true);
    expect(ones[0].at).toBe(utc('2026-11-01T05:00:00Z'));
    expect(ones[1].at).toBe(utc('2026-11-01T06:00:00Z'));
    // 반복되지 않은 시는 repeated가 거짓이다.
    expect(ticks.filter((t) => t.hour !== 1).every((t) => !t.repeated)).toBe(true);
    // 눈금은 시각 순이고 모두 한 시간 간격이다.
    for (let i = 1; i < ticks.length; i++) expect(ticks[i].at - ticks[i - 1].at).toBe(HOUR_MS);
    // 열의 마지막 눈금은 23시(EST)이고 열 끝 한 시간 전이다.
    expect(ticks[ticks.length - 1].hour).toBe(23);
    expect(ticks[ticks.length - 1].at).toBe(col.end - HOUR_MS);
  });

  it('전환이 없는 뉴욕의 날은 24개이고 반복이 없다', () => {
    const { ticks } = ticksOf('America/New_York', { year: 2026, month: 10, day: 5 });
    expect(ticks).toHaveLength(24);
    expect(ticks.every((t) => t.offset === 'UTC-4' && !t.repeated)).toBe(true);
  });

  it('30분 오프셋 시간대(인도)의 정시는 UTC로는 30분이다', () => {
    const { ticks } = ticksOf('Asia/Kolkata', { year: 2026, month: 10, day: 5 });
    expect(ticks).toHaveLength(24);
    expect(ticks.every((t) => new Date(t.at).getUTCMinutes() === 30)).toBe(true);
    expect(ticks[0].offset).toBe('UTC+5:30');
  });

  it('30분만 건너뛰는 시간대(로드하우 2026-10-04)는 23.5시간 열이고 2시 눈금만 없다', () => {
    const { col, ticks } = ticksOf('Australia/Lord_Howe', { year: 2026, month: 10, day: 4 });
    expect(hoursOf(col)).toBe(23.5);
    expect(ticks.some((t) => t.hour === 2)).toBe(false);
    expect(ticks).toHaveLength(23);
  });
});

describe('시간 축의 기준 열', () => {
  const col = (hours: number, start = 0): Pick<DayColumn, 'start' | 'end'> => ({ start, end: start + hours * HOUR_MS });

  it('24시간 열이 있으면 그것(전환일 열은 축과 어긋나므로 기준이 아니다)', () => {
    const columns = [col(23, 0), col(24, 100), col(25, 200)];
    expect(referenceColumn(columns)).toBe(columns[1]);
  });

  it('24시간 열이 없으면 첫 열, 열이 없으면 undefined', () => {
    const columns = [col(23, 0), col(25, 100)];
    expect(referenceColumn(columns)).toBe(columns[0]);
    expect(referenceColumn([])).toBeUndefined();
  });
});
