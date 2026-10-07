import { describe, expect, it } from 'vitest';
import { formatTrendTick, trendTickInterval } from './trend-axis';

describe('formatTrendTick', () => {
  it('uses the Cost Trend labels for every granularity', () => {
    expect(formatTrendTick('minute', '2026-08-24T09:07:00')).toBe('09:07');
    expect(formatTrendTick('hour', '2026-08-24T09:00:00')).toBe('08/24 09h');
    expect(formatTrendTick('day', '2026-08-24')).toBe('08-24');
    expect(formatTrendTick('week', '2026-08-24')).toBe('08-24');
    expect(formatTrendTick('month', '2026-08-01')).toBe('2026-08');
  });

  it('formats numeric Subscription Burn timestamps with the same rules', () => {
    const timestamp = new Date(2026, 7, 24, 9, 7).getTime();

    expect(formatTrendTick('minute', timestamp)).toBe('09:07');
    expect(formatTrendTick('hour', timestamp)).toBe('08/24 09h');
    expect(formatTrendTick('day', timestamp)).toBe('08-24');
    expect(formatTrendTick('month', timestamp)).toBe('2026-08');
  });
});

describe('formatTrendTick with full ISO timestamps (period label)', () => {
  // sinceTs/untilTs are UTC ISO strings; labels follow the local calendar like the minute label.
  const iso = (...args: [number, number, number, number, number]) => new Date(...args).toISOString();

  it('shows day and week as local MM-DD, not a raw ISO slice', () => {
    const ts = iso(2026, 8, 1, 8, 38);
    expect(formatTrendTick('day', ts)).toBe('09-01');
    expect(formatTrendTick('week', ts)).toBe('09-01');
  });

  it('shows month as local YYYY-MM', () => {
    expect(formatTrendTick('month', iso(2026, 8, 1, 8, 38))).toBe('2026-09');
  });

  it('keeps minute and hour on local clock parts', () => {
    const ts = iso(2026, 8, 1, 15, 32);
    expect(formatTrendTick('minute', ts)).toBe('15:32');
    expect(formatTrendTick('hour', ts)).toBe('09/01 15h');
  });

  it('follows the local date across midnight and month boundaries', () => {
    expect(formatTrendTick('day', iso(2026, 8, 1, 0, 30))).toBe('09-01');
    expect(formatTrendTick('day', iso(2026, 7, 31, 23, 30))).toBe('08-31');
    expect(formatTrendTick('month', iso(2026, 8, 1, 0, 30))).toBe('2026-09');
    expect(formatTrendTick('month', iso(2026, 7, 31, 23, 30))).toBe('2026-08');
  });

  it('leaves date-only axis keys unchanged', () => {
    expect(formatTrendTick('day', '2026-12-31')).toBe('12-31');
    expect(formatTrendTick('week', '2026-01-05')).toBe('01-05');
    expect(formatTrendTick('month', '2026-12-01')).toBe('2026-12');
  });
});

describe('trendTickInterval', () => {
  it('shares Cost Trend minute spacing with Subscription Burn', () => {
    expect(trendTickInterval('minute', 181)).toBe(15);
    expect(trendTickInterval('minute', 12)).toBe(1);
  });

  it('preserves the Cost Trend endpoints at coarser granularities', () => {
    expect(trendTickInterval('hour', 73)).toBe('preserveStartEnd');
    expect(trendTickInterval('day', 31)).toBe('preserveStartEnd');
  });

  it('uses Cost Trend zoom spacing for a short zoomed range', () => {
    expect(trendTickInterval('hour', 24, true)).toBe(2);
  });
});
