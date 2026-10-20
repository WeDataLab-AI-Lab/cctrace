import { describe, expect, it } from 'vitest';
import { quotaComparisonSeries } from './quota-owner-types';
import type { QuotaWindowComparison } from './quota-owner-types';

const comparison: QuotaWindowComparison = {
  billing_provider: 'anthropic', account_id: 'acct-fixture', window_minutes: 300,
  current: { window_start: '2026-10-09T10:00:00Z', resets_at: '2026-10-09T15:00:00Z', window_key: 'session', points: [
    { elapsed_fraction: 0.5, used_pct: 42, sampled_at: '2026-10-09T12:30:00Z', attribution: 'observed' },
  ] },
  history: [{ window_start: '2026-10-08T10:00:00Z', resets_at: '2026-10-08T15:00:00Z', window_key: 'session', points: [
    { elapsed_fraction: 0.5, used_pct: 20, sampled_at: '2026-10-08T12:30:00Z', attribution: 'observed' },
  ] }],
  quantiles: [
    { elapsed_fraction: 0.5, p25: 10, p50: 20, p75: 30, n: 4 },
    { elapsed_fraction: 0.75, p25: null, p50: 30, p75: null, n: 3 },
  ],
  history_count: 1, truncated: true, source: 'client_reported',
};

describe('personal quota comparison series', () => {
  it('preserves current, past, quantile, count, source, and truncation data', () => {
    const series = quotaComparisonSeries(comparison, 'five_hour');
    expect(series.current).toEqual([{ x: 0.5, y: 42 }]);
    expect(series.past).toEqual([[{ x: 0.5, y: 20 }]]);
    expect(series.median).toEqual([{ x: 0.5, y: 20 }, { x: 0.75, y: 30 }]);
    expect(series.band).toEqual([{ x: 0.5, low: 10, high: 30 }]);
    expect(series.sampleCounts).toEqual([{ x: 0.5, n: 4 }, { x: 0.75, n: 3 }]);
    expect(series.historyCount).toBe(1);
    expect(series.truncated).toBe(true);
    expect(series.source).toBe('client_reported');
  });

  it('keeps missing current state and quantile band absent instead of turning them into zero', () => {
    const series = quotaComparisonSeries({ ...comparison, current: null,
      quantiles: [{ elapsed_fraction: 0.5, p25: null, p50: null, p75: null, n: 0 }],
    }, 'seven_day');
    expect(series.window).toBe('seven_day');
    expect(series.current).toEqual([]);
    expect(series.median).toEqual([]);
    expect(series.band).toEqual([]);
    expect(series.sampleCounts).toEqual([{ x: 0.5, n: 0 }]);
  });
});
