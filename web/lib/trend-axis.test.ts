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
