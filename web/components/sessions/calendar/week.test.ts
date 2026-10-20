import { describe, expect, it } from 'vitest';
import { PREVIEW_NOW } from '@/app/test/session-analysis/builders';
import { CALENDAR_MAX_DAYS } from '@/lib/session-calendar-types';
import { weekColumns } from './day-grid';
import { sameWeek, shiftWeek, todayWeekStart, weekLabel, weekQueryRange, weekStartFromKey } from './week';

const date = (year: number, month: number, day: number) => ({ year, month, day });
const utc = (iso: string): number => Date.parse(iso);

describe('주 이동(C1)', () => {
  it('오늘이 든 주의 월요일은 시간대로 정한다', () => {
    const today = todayWeekStart('Asia/Seoul', PREVIEW_NOW);
    const monday = weekColumns('Asia/Seoul', today, PREVIEW_NOW).filter((c) => c.isToday);
    expect(monday).toHaveLength(1);
    expect(today.day).toBeGreaterThan(0);
  });

  it('일요일 밤과 월요일 새벽은 다른 주다', () => {
    // 서울 2026-10-11(일) 23:30 = 14:30Z, 2026-10-12(월) 00:30 = 15:30Z.
    expect(todayWeekStart('Asia/Seoul', utc('2026-10-11T14:30:00Z'))).toEqual(date(2026, 10, 5));
    expect(todayWeekStart('Asia/Seoul', utc('2026-10-11T15:30:00Z'))).toEqual(date(2026, 10, 12));
  });

  it('같은 순간도 시간대가 다르면 다른 주일 수 있다', () => {
    const t = utc('2026-10-11T15:30:00Z');
    expect(todayWeekStart('Asia/Seoul', t)).toEqual(date(2026, 10, 12));
    expect(todayWeekStart('America/New_York', t)).toEqual(date(2026, 10, 5));
  });

  it('이전·다음 주는 7일씩', () => {
    expect(shiftWeek(date(2026, 10, 5), -1)).toEqual(date(2026, 9, 28));
    expect(shiftWeek(date(2026, 10, 5), 1)).toEqual(date(2026, 10, 12));
    expect(shiftWeek(date(2026, 12, 28), 1)).toEqual(date(2027, 1, 4));
    expect(shiftWeek(date(2026, 10, 5), 0)).toEqual(date(2026, 10, 5));
  });

  it('날짜 키는 어느 요일이든 그 주의 월요일로 맞춘다', () => {
    expect(weekStartFromKey('2026-10-07')).toEqual(date(2026, 10, 5));
    expect(weekStartFromKey('2026-11-01')).toEqual(date(2026, 10, 26));
    expect(weekStartFromKey('nope')).toBeNull();
  });

  it('같은 주인가', () => {
    expect(sameWeek(date(2026, 10, 5), date(2026, 10, 5))).toBe(true);
    expect(sameWeek(date(2026, 10, 5), date(2026, 10, 12))).toBe(false);
  });
});

describe('서버에 보낼 구간 [from, to)', () => {
  it('서울의 한 주: 168시간, 14일 이하', () => {
    const range = weekQueryRange('Asia/Seoul', date(2026, 10, 5));
    expect(range).toEqual({ from: '2026-10-04T15:00:00.000Z', to: '2026-10-11T15:00:00.000Z' });
    expect((Date.parse(range.to) - Date.parse(range.from)) / 86_400_000).toBeLessThanOrEqual(CALENDAR_MAX_DAYS);
  });

  it('뉴욕 전환 주는 167·169시간이다', () => {
    const spring = weekQueryRange('America/New_York', date(2026, 3, 2));
    const fall = weekQueryRange('America/New_York', date(2026, 10, 26));
    expect((Date.parse(spring.to) - Date.parse(spring.from)) / 3_600_000).toBe(167);
    expect((Date.parse(fall.to) - Date.parse(fall.from)) / 3_600_000).toBe(169);
  });
});

describe('주 이름표', () => {
  it('"2026년 10/5 – 10/11"', () => {
    const columns = weekColumns('Asia/Seoul', date(2026, 10, 5), 0);
    expect(weekLabel(columns)).toBe('2026년 10/5 – 10/11');
  });

  it('해를 넘으면 뒤에도 해를 적는다', () => {
    const columns = weekColumns('Asia/Seoul', date(2026, 12, 28), 0);
    expect(weekLabel(columns)).toBe('2026년 12/28 – 2027년 1/3');
  });

  it('열이 없으면 빈 글자', () => {
    expect(weekLabel([])).toBe('');
  });
});
