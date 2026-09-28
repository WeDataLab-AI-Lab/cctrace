import { describe, expect, it } from 'vitest';
import { UNKNOWN_KEY, coverageGapEligible, coverageIneligibleReason, coverageRangeFor, coverageSummary, coverageExplanation, unknownByDate } from './coverage-gap';
import type { CoverageGap } from './types';

const clean = {
  hasProjectFilter: false,
  hasUserFilter: false,
  hasProfileFilter: false,
  hasModelFilter: false,
  agent: '',
  modelCategory: 'all',
};

const gap = (over: Partial<CoverageGap> = {}): CoverageGap => ({
  eligible: true,
  measured_usd: 780,
  implied_usd: 1000,
  coverage_ratio: 0.78,
  sample_coverage: 0.86,
  censored_fraction: 0.02,
  accounts: [],
  unfitted: [],
  k_method: 'weighted-mean-usd/28d',
  ...over,
});

describe('coverageGapEligible', () => {
  it('allows the unfiltered day view', () => {
    expect(coverageGapEligible(clean)).toBe(true);
  });

  // Burn is account-level: any subset on the measured side has no counterpart.
  it.each([
    ['project', { hasProjectFilter: true }],
    ['user', { hasUserFilter: true }],
    ['profile', { hasProfileFilter: true }],
    ['model', { hasModelFilter: true }],
    ['agent', { agent: 'codex' }],
    ['compatible', { modelCategory: 'compatible' }],
  ])('refuses the %s scope', (_, over) => {
    expect(coverageGapEligible({ ...clean, ...over })).toBe(false);
  });
});

describe('coverageIneligibleReason', () => {
  it('is null for the clean scope and names the blocker otherwise', () => {
    expect(coverageIneligibleReason(clean)).toBeNull();
    expect(coverageIneligibleReason({ ...clean, agent: 'codex' })).toMatch(/filters/);
  });
});

// The default chart view is three hours; the integer weekly meter has not moved
// in that time, so the line asks for the trailing week and says so.
describe('coverageRangeFor', () => {
  it('keeps a range of a day or more', () => {
    const r = coverageRangeFor('2026-08-24T00:00:00.000Z', '2026-08-25T00:00:00.000Z');
    expect(r).toEqual({ since: '2026-08-24T00:00:00.000Z', until: '2026-08-25T00:00:00.000Z', trailingWeek: false });
  });

  it('widens a shorter range to the week ending at its end', () => {
    const r = coverageRangeFor('2026-08-25T15:00:00.000Z', '2026-08-25T18:00:00.000Z');
    expect(r).toEqual({ since: '2026-08-18T18:00:00.000Z', until: '2026-08-25T18:00:00.000Z', trailingWeek: true });
  });
});

describe('coverageSummary', () => {
  it('states the ratio as an estimate with its sampling caveat', () => {
    expect(coverageSummary(gap())).toBe('Measured usage covers ≈78% of subscription burn over this range · readings span 86% of the range');
  });

  it('names the trailing week when the chart range was too short', () => {
    expect(coverageSummary(gap(), true)).toContain('over the last 7 days');
  });

  it('names unfitted accounts instead of dropping them silently', () => {
    expect(coverageSummary(gap({ unfitted: ['a@x.test', 'b@x.test'] }))).toContain('2 accounts unfitted');
  });

  it('reports why it could not answer', () => {
    expect(coverageSummary(gap({ eligible: false, reason: 'since and until are required' })))
      .toBe('Coverage unavailable: since and until are required');
  });
});

describe('unknownByDate', () => {
  const buckets = [
    { date: '2026-08-24', implied_usd: 10, measured_usd: 5, unknown_tokens: 300, unknown_cost_usd: 1.5 },
    { date: '2026-08-25', implied_usd: 5, measured_usd: 5, unknown_tokens: 0, unknown_cost_usd: 0 },
  ];

  it('picks the unit the chart is showing and drops empty buckets', () => {
    expect([...unknownByDate(gap({ buckets }), 'token')]).toEqual([['2026-08-24', 300]]);
    expect([...unknownByDate(gap({ buckets }), 'cost')]).toEqual([['2026-08-24', 1.5]]);
  });

  it('is empty without buckets or when ineligible', () => {
    expect(unknownByDate(gap(), 'token').size).toBe(0);
    expect(unknownByDate(gap({ eligible: false, buckets }), 'token').size).toBe(0);
  });

  it('uses a sentinel key that no model label can collide with', () => {
    expect(UNKNOWN_KEY.startsWith('__')).toBe(true);
  });
});

describe('coverageExplanation', () => {
  it('explains a ratio above one rather than presenting it as over-coverage', () => {
    const { caveats } = coverageExplanation(gap({ coverage_ratio: 1.04 }));
    expect(caveats.join('\n')).toMatch(/above 100%/);
  });

  it('keeps the range-specific caveats out when they do not apply', () => {
    expect(coverageExplanation(gap({ censored_fraction: 0, coverage_ratio: 0.78 })).caveats).toEqual([]);
  });

  it('says what readings span measures, since the word alone reads as poll coverage', () => {
    const { method } = coverageExplanation(gap({}));
    expect(method.join('\n')).toMatch(/gaps inside that stretch are not subtracted/);
  });

  it('warns that the estimate can overstate the true gap', () => {
    const { method } = coverageExplanation(gap({}));
    expect(method.join('\n')).toMatch(/overstated the true gap by more than 3x/);
  });

  it('returns each account as figures, not as a formatted sentence', () => {
    const { accounts } = coverageExplanation(gap({
      accounts: [
        { login_email: 'a@x.test', account_id: 'x', plan: 'max', window_key: 'session', k_usd_per_pct: 500, fit_intervals: 40, measured_usd: 1, implied_usd: 1, sample_coverage: 1, censored_seconds: 0 },
        { login_email: 'b@x.test', account_id: '', plan: '', window_key: '', k_usd_per_pct: 0, fit_intervals: 3, measured_usd: 0, implied_usd: 0, sample_coverage: 0, censored_seconds: 0, unfitted: 'fewer than 30 fully measured intervals' },
      ],
    }));
    expect(accounts[0]).toEqual({ email: 'a@x.test', ratio: 1, kUsdPerPct: 500, fitIntervals: 40, unfitted: undefined });
    // ratio is null, not 0: an unfitted account has no ratio, and 0 would render
    // as a measured zero.
    expect(accounts[1].ratio).toBeNull();
    expect(accounts[1].unfitted).toBe('fewer than 30 fully measured intervals');
  });
});
