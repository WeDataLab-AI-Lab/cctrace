import { describe, expect, it } from 'vitest';
import {
  currentWeekId,
  formatWeekLabel,
  isFutureWeek,
  parseWeekId,
  shiftWeek,
  weekRange,
} from './weekly-period';

const HOUR_MS = 3600 * 1000;
const at = (iso: string): number => new Date(iso).getTime();
const spanHours = ({ since, until }: { since: string; until: string }): number =>
  (at(until) - at(since)) / HOUR_MS;

describe('parseWeekId', () => {
  it('accepts well-formed ISO week identifiers', () => {
    expect(parseWeekId('2026-W37')).toBe('2026-W37');
    expect(parseWeekId('2026-W01')).toBe('2026-W01');
  });

  it('accepts week 53 only in years that have one', () => {
    // 2026 starts on a Thursday, 2020 is a leap year starting on a Wednesday.
    expect(parseWeekId('2026-W53')).toBe('2026-W53');
    expect(parseWeekId('2020-W53')).toBe('2020-W53');
    expect(parseWeekId('2025-W53')).toBeNull();
    expect(parseWeekId('2027-W53')).toBeNull();
  });

  it('rejects malformed text and week numbers that do not exist', () => {
    for (const text of ['', '2026-W00', '2026-W54', '2026-W7', '2026W37', '2026-w37', ' 2026-W37', '2026-W37x', '26-W37']) {
      expect(parseWeekId(text)).toBeNull();
    }
  });
});

describe('currentWeekId', () => {
  it('reads the calendar date in the given time zone', () => {
    // Sunday 23:30 in UTC is already Monday 08:30 in Seoul.
    const now = at('2026-09-13T23:30:00Z');
    expect(currentWeekId(now, 'UTC')).toBe('2026-W37');
    expect(currentWeekId(now, 'Asia/Seoul')).toBe('2026-W38');
    expect(currentWeekId(at('2026-09-14T03:30:00Z'), 'America/New_York')).toBe('2026-W37');
  });

  it('assigns days near New Year to the ISO week-year', () => {
    // 2024-12-30 is the Monday of 2025-W01; 2027-01-03 is the Sunday of 2026-W53.
    expect(currentWeekId(at('2024-12-30T12:00:00Z'), 'UTC')).toBe('2025-W01');
    expect(currentWeekId(at('2027-01-03T12:00:00Z'), 'UTC')).toBe('2026-W53');
    expect(currentWeekId(at('2027-01-04T12:00:00Z'), 'UTC')).toBe('2027-W01');
    expect(currentWeekId(at('2021-01-03T12:00:00Z'), 'UTC')).toBe('2020-W53');
  });
});

describe('weekRange', () => {
  it('runs from Monday 00:00 to the next Monday 00:00 in Asia/Seoul', () => {
    const range = weekRange('2026-W37', 'Asia/Seoul');
    expect(range).toEqual({
      since: '2026-09-06T15:00:00.000Z',
      until: '2026-09-13T15:00:00.000Z',
    });
    expect(spanHours(range)).toBe(168);
  });

  it('spans the ISO year boundary', () => {
    expect(weekRange('2025-W01', 'UTC')).toEqual({
      since: '2024-12-30T00:00:00.000Z',
      until: '2025-01-06T00:00:00.000Z',
    });
    expect(weekRange('2026-W53', 'UTC')).toEqual({
      since: '2026-12-28T00:00:00.000Z',
      until: '2027-01-04T00:00:00.000Z',
    });
  });

  it('follows DST in America/New_York: 167 hours in spring, 169 in autumn', () => {
    // DST starts 2026-03-08 and ends 2026-11-01, both Sundays.
    const spring = weekRange('2026-W10', 'America/New_York');
    expect(spring).toEqual({
      since: '2026-03-02T05:00:00.000Z',
      until: '2026-03-09T04:00:00.000Z',
    });
    expect(spanHours(spring)).toBe(167);

    const autumn = weekRange('2026-W44', 'America/New_York');
    expect(autumn).toEqual({
      since: '2026-10-26T04:00:00.000Z',
      until: '2026-11-02T05:00:00.000Z',
    });
    expect(spanHours(autumn)).toBe(169);
  });

  it('meets the next week without gap or overlap', () => {
    expect(weekRange('2026-W10', 'America/New_York').until).toBe(
      weekRange('2026-W11', 'America/New_York').since,
    );
  });
});

describe('shiftWeek', () => {
  it('moves by whole weeks across year boundaries', () => {
    expect(shiftWeek('2026-W37', 1)).toBe('2026-W38');
    expect(shiftWeek('2026-W37', -1)).toBe('2026-W36');
    expect(shiftWeek('2026-W37', 0)).toBe('2026-W37');
    expect(shiftWeek('2026-W53', 1)).toBe('2027-W01');
    expect(shiftWeek('2027-W01', -1)).toBe('2026-W53');
    expect(shiftWeek('2025-W52', 1)).toBe('2026-W01');
    expect(shiftWeek('2026-W01', -1)).toBe('2025-W52');
    expect(shiftWeek('2026-W37', -52)).toBe('2025-W37');
  });
});

describe('isFutureWeek', () => {
  it('treats the week in progress as current, not future', () => {
    const now = at('2026-09-14T06:00:00Z'); // Monday 15:00 in Seoul
    expect(isFutureWeek('2026-W38', now, 'Asia/Seoul')).toBe(false);
    expect(isFutureWeek('2026-W37', now, 'Asia/Seoul')).toBe(false);
    expect(isFutureWeek('2026-W39', now, 'Asia/Seoul')).toBe(true);
  });

  it('depends on the time zone at the week boundary', () => {
    // Monday 08:30 in Seoul, still Sunday in New York.
    const now = at('2026-09-13T23:30:00Z');
    expect(isFutureWeek('2026-W38', now, 'Asia/Seoul')).toBe(false);
    expect(isFutureWeek('2026-W38', now, 'America/New_York')).toBe(true);
  });
});

describe('formatWeekLabel', () => {
  it('shows Monday through Sunday as month/day', () => {
    expect(formatWeekLabel('2026-W37', 'Asia/Seoul')).toBe('9/7–9/13');
    expect(formatWeekLabel('2026-W37', 'America/New_York')).toBe('9/7–9/13');
  });

  it('keeps Sunday as the end across the year boundary and DST', () => {
    expect(formatWeekLabel('2026-W53', 'UTC')).toBe('12/28–1/3');
    expect(formatWeekLabel('2026-W44', 'America/New_York')).toBe('10/26–11/1');
  });
});
