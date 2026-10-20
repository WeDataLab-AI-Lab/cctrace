import { describe, expect, it } from 'vitest';
import {
  addDays,
  calendarTimeZone,
  dateKey,
  formatClockIn,
  formatDateTimeIn,
  formatMonthDayIn,
  isValidTimeZone,
  localDateOf,
  mondayOf,
  offsetLabel,
  offsetMinutesAt,
  parseDateKey,
  weekdayOf,
  zonedMidnight,
} from './time-zone';

const utc = (iso: string): number => Date.parse(iso);

describe('달력 날짜 산술', () => {
  it('일 수를 더하면 달·해를 넘고 윤년을 안다', () => {
    expect(addDays({ year: 2026, month: 10, day: 31 }, 1)).toEqual({ year: 2026, month: 11, day: 1 });
    expect(addDays({ year: 2026, month: 12, day: 31 }, 1)).toEqual({ year: 2027, month: 1, day: 1 });
    expect(addDays({ year: 2028, month: 2, day: 28 }, 1)).toEqual({ year: 2028, month: 2, day: 29 });
    expect(addDays({ year: 2026, month: 3, day: 1 }, -1)).toEqual({ year: 2026, month: 2, day: 28 });
  });

  it('요일: 0이 일요일', () => {
    expect(weekdayOf({ year: 2026, month: 10, day: 7 })).toBe(3); // 수
    expect(weekdayOf({ year: 2026, month: 11, day: 1 })).toBe(0); // 일
  });

  it('주의 월요일: 월요일은 그대로, 일요일은 엿새 전', () => {
    expect(mondayOf({ year: 2026, month: 10, day: 7 })).toEqual({ year: 2026, month: 10, day: 5 });
    expect(mondayOf({ year: 2026, month: 10, day: 5 })).toEqual({ year: 2026, month: 10, day: 5 });
    expect(mondayOf({ year: 2026, month: 11, day: 1 })).toEqual({ year: 2026, month: 10, day: 26 });
  });

  it('날짜 키 왕복, 없는 날짜는 거른다', () => {
    expect(dateKey({ year: 2026, month: 3, day: 8 })).toBe('2026-03-08');
    expect(parseDateKey('2026-03-08')).toEqual({ year: 2026, month: 3, day: 8 });
    expect(parseDateKey('2026-02-30')).toBeNull();
    expect(parseDateKey('2026-3-8')).toBeNull();
    expect(parseDateKey('abc')).toBeNull();
  });
});

describe('시간대와 오프셋', () => {
  it('서울은 늘 +9시간, 뉴욕은 여름 -4시간·겨울 -5시간, 인도는 +5:30', () => {
    expect(offsetMinutesAt('Asia/Seoul', utc('2026-01-15T12:00:00Z'))).toBe(540);
    expect(offsetMinutesAt('Asia/Seoul', utc('2026-07-15T12:00:00Z'))).toBe(540);
    expect(offsetMinutesAt('America/New_York', utc('2026-07-01T12:00:00Z'))).toBe(-240);
    expect(offsetMinutesAt('America/New_York', utc('2026-01-15T12:00:00Z'))).toBe(-300);
    expect(offsetMinutesAt('Asia/Kolkata', utc('2026-01-15T12:00:00Z'))).toBe(330);
  });

  it('오프셋 표기', () => {
    expect(offsetLabel(540)).toBe('UTC+9');
    expect(offsetLabel(-240)).toBe('UTC-4');
    expect(offsetLabel(330)).toBe('UTC+5:30');
    expect(offsetLabel(-210)).toBe('UTC-3:30');
    expect(offsetLabel(0)).toBe('UTC');
  });

  it('IANA 이름이 아니면 거른다', () => {
    expect(isValidTimeZone('Asia/Seoul')).toBe(true);
    expect(isValidTimeZone('Mars/Olympus')).toBe(false);
    expect(isValidTimeZone('')).toBe(false);
    expect(calendarTimeZone('Mars/Olympus')).toBe('UTC');
    expect(calendarTimeZone('Asia/Seoul')).toBe('Asia/Seoul');
  });

  it('지역 날짜와 표기는 그 시간대 기준이다', () => {
    const t = utc('2026-10-07T06:40:00Z');
    expect(localDateOf('Asia/Seoul', t)).toEqual({ year: 2026, month: 10, day: 7 });
    expect(formatClockIn('Asia/Seoul', t)).toBe('15:40');
    // 뉴욕은 같은 순간이 수요일 새벽 2:40이다.
    expect(formatClockIn('America/New_York', t)).toBe('02:40');
    expect(formatMonthDayIn('America/New_York', utc('2026-10-07T03:00:00Z'))).toBe('10/6');
    expect(formatDateTimeIn('Asia/Seoul', t)).toBe('10/07 15:40');
  });
});

describe('지역 자정의 절대 시각', () => {
  it('서울: 전날 15:00Z', () => {
    expect(zonedMidnight('Asia/Seoul', { year: 2026, month: 10, day: 5 })).toBe(utc('2026-10-04T15:00:00Z'));
  });

  it('뉴욕: 여름(EDT) 04:00Z, 겨울(EST) 05:00Z', () => {
    expect(zonedMidnight('America/New_York', { year: 2026, month: 10, day: 5 })).toBe(utc('2026-10-05T04:00:00Z'));
    expect(zonedMidnight('America/New_York', { year: 2026, month: 12, day: 15 })).toBe(utc('2026-12-15T05:00:00Z'));
  });

  it('뉴욕의 두 전환일(2026): 3/8은 EST 자정, 3/9는 EDT 자정; 11/1은 EDT 자정, 11/2는 EST 자정', () => {
    expect(zonedMidnight('America/New_York', { year: 2026, month: 3, day: 8 })).toBe(utc('2026-03-08T05:00:00Z'));
    expect(zonedMidnight('America/New_York', { year: 2026, month: 3, day: 9 })).toBe(utc('2026-03-09T04:00:00Z'));
    expect(zonedMidnight('America/New_York', { year: 2026, month: 11, day: 1 })).toBe(utc('2026-11-01T04:00:00Z'));
    expect(zonedMidnight('America/New_York', { year: 2026, month: 11, day: 2 })).toBe(utc('2026-11-02T05:00:00Z'));
  });

  it('자정이 없는 날(상파울루 2018-11-04, 자정에 1시로 건너뜀)은 건너뛴 직후가 그 날의 시작이다', () => {
    const start = zonedMidnight('America/Sao_Paulo', { year: 2018, month: 11, day: 4 });
    expect(start).toBe(utc('2018-11-04T03:00:00Z'));
    expect(formatClockIn('America/Sao_Paulo', start)).toBe('01:00');
    // 전날은 24시간이고 이 날은 23시간이다.
    const prev = zonedMidnight('America/Sao_Paulo', { year: 2018, month: 11, day: 3 });
    const next = zonedMidnight('America/Sao_Paulo', { year: 2018, month: 11, day: 5 });
    expect((start - prev) / 3_600_000).toBe(24);
    expect((next - start) / 3_600_000).toBe(23);
  });
});
