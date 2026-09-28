import { describe, expect, it } from 'vitest';
import { WINDOW_5H, WINDOW_7D, accountLegendLabel, availableProvidersFor, availableWindowsFor, buildUsageSeries, buildWindows, DASH_7D, combinationHasData, lineDash, coverageOf, defaultWindowFor, mergeRows, remainingUSD, seriesKey, toggleSelection, valueAt, weightedKey, weightedLegendLabel, providersForAgent, scopeSamples, timeWeightedMean, bucketUsagePoints, barOpacity } from "./quota-usage";
import type { QuotaSample } from './types';

// Prices come from the code table, so the tests use the identifiers that really
// appear in quota_samples: Claude's rateLimitTier and Codex's plan_type.
// MAX_5X is $100 and CODEX_PRO is $200 in web/lib/plan-prices.ts.
const MAX_5X = 'default_claude_max_5x';
const CODEX_PRO = 'pro';
const CODEX_PLUS = 'plus';
const UNPRICED = 'some_unlisted_tier';

const sample = (over: Partial<QuotaSample> & Pick<QuotaSample, 'account_id' | 'sampled_at' | 'used_pct'>): QuotaSample => ({
  billing_provider: 'anthropic',
  window_key: 'session',
  window_minutes: WINDOW_5H,
  plan: MAX_5X,
  ...over,
});

describe('subscription burn legend labels', () => {
  it('labels a Claude account with its known email and normalized 5h type', () => {
    const { accounts } = buildUsageSeries([
      sample({
        account_id: 'claude-account-id',
        sampled_at: '2026-08-24T09:00:00Z',
        used_pct: 40,
        login_email: 'claude@example.test',
      }),
    ], WINDOW_5H);

    expect(accountLegendLabel(accounts[0].label, accounts[0].provider, WINDOW_5H)).toBe(
      'claude@example.test (Claude, 5h)',
    );
  });

  it('labels a Codex account with its safe account-id fallback and normalized 7d type', () => {
    const { accounts } = buildUsageSeries([
      sample({
        account_id: 'codex-account-id',
        sampled_at: '2026-08-24T09:00:00Z',
        used_pct: 40,
        billing_provider: 'openai',
        plan: CODEX_PRO,
        window_key: '10080',
        window_minutes: WINDOW_7D,
      }),
    ], WINDOW_7D);

    expect(accountLegendLabel(accounts[0].label, accounts[0].provider, WINDOW_7D)).toBe(
      'codex-account-id (Codex, 7d)',
    );
  });

  it('keeps weighted lines clearly distinguished by normalized type', () => {
    expect(weightedLegendLabel(WINDOW_5H)).toBe('weighted 5h');
    expect(weightedLegendLabel(WINDOW_7D)).toBe('weighted 7d');
  });
});

describe('buildUsageSeries', () => {
  // The worked example from the issue: Codex Pro $200 at 40% and Claude Max
  // $100 at 90%. A plain mean says 65%; weighting by price says 56.7%, i.e.
  // $170 of $300 consumed. It leans toward the expensive account because an
  // idle $200 plan is the more wasteful one.
  it('weights utilization by subscription price, not by account count', () => {
    const at = '2026-08-24T09:00:00Z';
    const { points } = buildUsageSeries([
        sample({ account_id: 'claude-1', sampled_at: at, used_pct: 90 }),
        sample({ account_id: 'codex-1', sampled_at: at, used_pct: 40, billing_provider: 'openai', plan: CODEX_PRO }),
      ], WINDOW_5H);

    expect(points).toHaveLength(1);
    expect(points[0].weighted).toBeCloseTo(56.666, 2);
    expect(points[0].weighted).not.toBeCloseTo(65, 2);
  });

  // 5h and 7d are different time scales. A series mixing them names no state at
  // all, so the chart asks for one window and the other must not leak in.
  it('keeps window lengths apart', () => {
    const at = '2026-08-24T09:00:00Z';
    const samples = [
      sample({ account_id: 'a', sampled_at: at, used_pct: 10 }),
      sample({ account_id: 'a', sampled_at: at, used_pct: 80, window_key: 'weekly_all', window_minutes: WINDOW_7D }),
    ];

    expect(buildUsageSeries(samples, WINDOW_5H).points[0].weighted).toBeCloseTo(10);
    expect(buildUsageSeries(samples, WINDOW_7D).points[0].weighted).toBeCloseTo(80);
  });

  // Several profiles read one meter at their own moments. They still form one
  // account series, and a small input keeps its exact resolution.
  it('folds reports from different profiles into one account series', () => {
    const { points, accounts } = buildUsageSeries([
        sample({ account_id: 'a', sampled_at: '2026-08-24T09:00:00Z', used_pct: 10, profile_email: 'one@example.test' }),
        sample({ account_id: 'a', sampled_at: '2026-08-24T09:02:00Z', used_pct: 12, profile_email: 'two@example.test' }),
        sample({ account_id: 'a', sampled_at: '2026-08-24T09:04:00Z', used_pct: 14, profile_email: 'three@example.test' }),
      ], WINDOW_5H);

    expect(accounts).toHaveLength(1);
    expect(points).toHaveLength(3);
  });

  // The store bounds each database series separately. The chart then unions
  // their timestamps, so several independently sampled OpenAI buckets can
  // multiply that bound back into thousands of SVG vertices.
  it('bounds the final timeline after combining independently sampled series', () => {
    const start = Date.parse('2026-08-24T09:00:00Z');
    // Each account is already at the store's 1,000-point bound; their
    // timestamps interleave, making a 2,000-point browser timeline.
    const samples = Array.from({ length: 2_000 }, (_, i) =>
      sample({
        account_id: i % 2 === 0 ? 'a' : 'b',
        sampled_at: new Date(start + i * 1_000).toISOString(),
        used_pct: i % 101,
        billing_provider: 'openai',
        plan: CODEX_PRO,
      }),
    );

    const { points } = buildUsageSeries(samples, WINDOW_5H);

    expect(points.length).toBeLessThanOrEqual(1_000);
    expect(points[0].t).toBe(start);
    expect(points.at(-1)?.t).toBe(start + 1_999_000);
    expect(points.at(-1)?.byAccount['openai:b']).toBe(80);
  });

  // The same id under two providers is two meters. Codex being blocked does not
  // free Claude quota.
  it('keeps providers apart even on a shared account id', () => {
    const at = '2026-08-24T09:00:00Z';
    const { accounts } = buildUsageSeries([
        sample({ account_id: 'shared', sampled_at: at, used_pct: 10 }),
        sample({ account_id: 'shared', sampled_at: at, used_pct: 90, billing_provider: 'openai', plan: CODEX_PRO }),
      ], WINDOW_5H);

    expect(accounts).toHaveLength(2);
  });

  it('excludes Spark-only samples from Codex lines, weighting, and coverage', () => {
    const at = '2026-08-24T09:00:00Z';
    const samples = [
      sample({ account_id: 'codex-1', sampled_at: at, used_pct: 4, billing_provider: 'openai', plan: CODEX_PRO,
               window_key: 'codex_bengalfox:300' }),
      sample({ account_id: 'codex-1', sampled_at: at, used_pct: 1, billing_provider: 'openai', plan: CODEX_PRO,
               window_key: 'future_spark_bucket:10080', window_minutes: WINDOW_7D,
               scope_label: 'GPT-5.3-Codex-Spark' }),
    ];

    const five = buildUsageSeries(samples, WINDOW_5H);
    const weekly = buildUsageSeries(samples, WINDOW_7D);
    const rows = mergeRows(buildWindows(samples, [WINDOW_5H, WINDOW_7D], ['openai']));
    const coverage = coverageOf([...five.accounts, ...weekly.accounts], []);

    expect(five).toEqual({ accounts: [], points: [] });
    expect(weekly).toEqual({ accounts: [], points: [] });
    expect(rows).toEqual([]);
    expect(availableWindowsFor(samples)).toEqual([]);
    expect(availableProvidersFor(samples)).toEqual([]);
    expect(defaultWindowFor(samples)).toBeNull();
    expect(coverage).toEqual({ measuredUSD: 0, totalUSD: 0, measuredAccounts: 0, unmeasured: [] });
  });

  it('uses only non-Spark Codex values when generic and scoped buckets are mixed with Spark', () => {
    const at = '2026-08-24T09:00:00Z';
    const samples = [
      sample({ account_id: 'generic', sampled_at: at, used_pct: 42, billing_provider: 'openai', plan: CODEX_PRO,
               window_key: '300' }),
      sample({ account_id: 'generic', sampled_at: at, used_pct: 27, billing_provider: 'openai', plan: CODEX_PRO,
               window_key: '10080', window_minutes: WINDOW_7D }),
      sample({ account_id: 'mixed', sampled_at: at, used_pct: 60, billing_provider: 'openai', plan: CODEX_PRO,
               window_key: 'codex_foxglove:300', scope_label: 'GPT-5.4' }),
      sample({ account_id: 'mixed', sampled_at: at, used_pct: 50, billing_provider: 'openai', plan: CODEX_PRO,
               window_key: 'codex_foxglove:10080', window_minutes: WINDOW_7D, scope_label: 'GPT-5.4' }),
      sample({ account_id: 'mixed', sampled_at: at, used_pct: 99, billing_provider: 'openai', plan: CODEX_PRO,
               window_key: 'codex_bengalfox:300', scope_label: 'GPT-5.3-Codex-Spark' }),
      sample({ account_id: 'mixed', sampled_at: at, used_pct: 98, billing_provider: 'openai', plan: CODEX_PRO,
               window_key: 'codex_bengalfox:10080', window_minutes: WINDOW_7D,
               scope_label: 'GPT-5.3-Codex-Spark' }),
    ];

    const five = buildUsageSeries(samples, WINDOW_5H);
    const weekly = buildUsageSeries(samples, WINDOW_7D);
    const rows = mergeRows(buildWindows(samples, [WINDOW_5H, WINDOW_7D], ['openai']));
    const coverage = coverageOf(five.accounts, []);

    expect(five.points[0]).toMatchObject({
      weighted: 51,
      byAccount: { 'openai:generic': 42, 'openai:mixed': 60 },
    });
    expect(weekly.points[0]).toMatchObject({
      weighted: 38.5,
      byAccount: { 'openai:generic': 27, 'openai:mixed': 50 },
    });
    expect(rows[0]).toMatchObject({
      [seriesKey('openai:generic', WINDOW_5H)]: 42,
      [seriesKey('openai:mixed', WINDOW_5H)]: 60,
      [seriesKey('openai:generic', WINDOW_7D)]: 27,
      [seriesKey('openai:mixed', WINDOW_7D)]: 50,
      [weightedKey(WINDOW_5H)]: 51,
      [weightedKey(WINDOW_7D)]: 38.5,
    });
    expect(five.accounts.map((account) => account.label)).toEqual(['generic', 'mixed']);
    expect(coverage.measuredUSD).toBe(400);
    expect(JSON.stringify({ five, weekly, rows })).not.toContain('Spark');
  });

  it('uses an older sample email for the general series when samples arrive newest-first', () => {
    const accountId = '019db82c-66c5-7160-a6a7-b76dc2dd72c5';
    const { accounts } = buildUsageSeries([
      sample({ account_id: accountId, sampled_at: '2026-08-24T09:05:00Z', used_pct: 20,
               billing_provider: 'openai', plan: CODEX_PRO, window_key: 'codex_bengalfox:300',
               scope_label: 'GPT-5.3-Codex-Spark', login_email: '' }),
      sample({ account_id: accountId, sampled_at: '2026-08-24T09:00:00Z', used_pct: 10,
               billing_provider: 'openai', plan: CODEX_PRO, window_key: 'codex_foxglove:300',
               scope_label: 'GPT-5.4', login_email: 'account@example.com' }),
    ], WINDOW_5H);

    expect(accounts.map((account) => account.label)).toEqual([
      'account@example.com',
    ]);
  });

  // A window holds its value between readings — it only moves when work is
  // done — so carrying the last reading forward is exact, not smoothing.
  it('carries a reading forward to instants another account reported', () => {
    const { points } = buildUsageSeries([
        sample({ account_id: 'a', sampled_at: '2026-08-24T09:00:00Z', used_pct: 50 }),
        sample({ account_id: 'b', sampled_at: '2026-08-24T09:05:00Z', used_pct: 10 }),
      ], WINDOW_5H);

    expect(points).toHaveLength(2);
    expect(points[1].byAccount['anthropic:a']).toBe(50);
  });

  // Before an account's first reading there is nothing to carry. Filling 0
  // would draw "this window is empty" where the truth is "not measured yet".
  it('omits an account before its first reading rather than showing zero', () => {
    const { points } = buildUsageSeries([
        sample({ account_id: 'a', sampled_at: '2026-08-24T09:00:00Z', used_pct: 50 }),
        sample({ account_id: 'b', sampled_at: '2026-08-24T09:05:00Z', used_pct: 10 }),
      ], WINDOW_5H);

    expect(points[0].byAccount['anthropic:b']).toBeUndefined();
    // The first instant is weighted on account a alone, not on a and a zero.
    expect(points[0].weighted).toBeCloseTo(50);
  });

  // An unpriced plan still deserves its own line, but a price of 0 in the
  // denominator would silently drag the average toward the priced accounts as
  // though this subscription were free.
  it('excludes unpriced accounts from the weighted average', () => {
    const at = '2026-08-24T09:00:00Z';
    const { points } = buildUsageSeries([
        sample({ account_id: 'a', sampled_at: at, used_pct: 90 }),
        sample({ account_id: 'b', sampled_at: at, used_pct: 10, plan: UNPRICED }),
      ], WINDOW_5H);

    expect(points[0].weighted).toBeCloseTo(90);
    expect(points[0].byAccount['anthropic:b']).toBe(10);
  });

  // With nothing priced there is no denominator, and null is what stops the
  // chart drawing a confident zero.
  it('reports no weighted value when nothing is priced', () => {
    const { points } = buildUsageSeries([sample({ account_id: 'a', sampled_at: '2026-08-24T09:00:00Z', used_pct: 90, plan: UNPRICED })], WINDOW_5H);

    expect(points[0].weighted).toBeNull();
  });

  // A backfilled instant is present but not certain, and the chart draws that
  // differently.
  it('marks an instant inferred only when every contributor is', () => {
    const at = '2026-08-24T09:00:00Z';
    const mixed = buildUsageSeries([
        sample({ account_id: 'a', sampled_at: at, used_pct: 90, attribution: 'inferred' }),
        sample({ account_id: 'b', sampled_at: at, used_pct: 10, attribution: 'observed' }),
      ], WINDOW_5H);
    expect(mixed.points[0].inferred).toBe(false);

    const all = buildUsageSeries([sample({ account_id: 'a', sampled_at: at, used_pct: 90, attribution: 'inferred' })], WINDOW_5H);
    expect(all.points[0].inferred).toBe(true);
  });

  // Codex reports the window length as a number and uses it as the key, so a
  // sample with no window_minutes field still has to land in the right series.
  it('falls back to a numeric window_key', () => {
    const { points } = buildUsageSeries([
        {
          billing_provider: 'openai',
          account_id: 'codex-1',
          window_key: '300',
          sampled_at: '2026-08-24T09:00:00Z',
          used_pct: 33,
          plan: CODEX_PRO,
        },
      ], WINDOW_5H);

    expect(points[0].weighted).toBeCloseTo(33);
  });

  it('returns nothing when no sample matches the window', () => {
    const { points, accounts } = buildUsageSeries([sample({ account_id: 'a', sampled_at: '2026-08-24T09:00:00Z', used_pct: 10 })], WINDOW_7D);

    expect(points).toEqual([]);
    expect(accounts).toEqual([]);
  });
});

describe('coverageOf', () => {
  // The failure this guards against is real: an unmeasured Codex $200 counted
  // at 0% drags the average down and the screen says "idle" where it should
  // say "not looked at".
  it('excludes unmeasured accounts from the denominator and names them', () => {
    const { accounts } = buildUsageSeries([sample({ account_id: 'a', sampled_at: '2026-08-24T09:00:00Z', used_pct: 90 })], WINDOW_5H);

    const coverage = coverageOf(accounts, [
      { accountId: 'anthropic:a', label: 'a@example.test', priceUSD: 100 },
      { accountId: 'openai:codex-1', label: 'codex@example.test', priceUSD: 200 },
    ]);

    expect(coverage.measuredUSD).toBe(100);
    expect(coverage.totalUSD).toBe(300);
    expect(coverage.unmeasured.map((u) => u.accountId)).toEqual(['openai:codex-1']);
  });

  it('reports full coverage when every known account was measured', () => {
    const at = '2026-08-24T09:00:00Z';
    const { accounts } = buildUsageSeries([
        sample({ account_id: 'a', sampled_at: at, used_pct: 90 }),
        sample({ account_id: 'codex-1', sampled_at: at, used_pct: 40, billing_provider: 'openai', plan: CODEX_PRO }),
      ], WINDOW_5H);

    const coverage = coverageOf(accounts, [
      { accountId: 'anthropic:a', label: 'a', priceUSD: 100 },
      { accountId: 'openai:codex-1', label: 'c', priceUSD: 200 },
    ]);

    expect(coverage.measuredUSD).toBe(300);
    expect(coverage.totalUSD).toBe(300);
    expect(coverage.unmeasured).toEqual([]);
  });
});

describe('valueAt', () => {
  it('returns null before the first reading', () => {
    expect(valueAt([{ t: 100, pct: 5 }], 50)).toBeNull();
  });

  it('holds the last reading at and after its instant', () => {
    const points = [
      { t: 100, pct: 5 },
      { t: 200, pct: 9 },
    ];

    expect(valueAt(points, 100)).toBe(5);
    expect(valueAt(points, 150)).toBe(5);
    expect(valueAt(points, 999)).toBe(9);
  });
});

describe('remainingUSD', () => {
  // The axis is inverted so the height of the line is the money still unspent;
  // this is the number that height stands for.
  it('converts consumed share into unspent subscription money', () => {
    expect(remainingUSD(56.666, 300)).toBeCloseTo(130, 0);
    expect(remainingUSD(0, 300)).toBe(300);
    expect(remainingUSD(100, 300)).toBe(0);
  });
});

describe('plan changes over time', () => {
  // A plan is not a property of the account, it is a property of the reading:
  // the local logs on the development machine walk null -> plus/prolite -> pro
  // as the subscription was changed. Pricing old readings at today's rate would
  // misstate what was actually being burned back then, which is the one thing
  // the chart is for.
  it('prices each reading by the plan it was taken under', () => {
    const { points, accounts } = buildUsageSeries(
      [
        sample({ account_id: 'a', sampled_at: '2026-05-01T00:00:00Z', used_pct: 50, billing_provider: 'openai', plan: CODEX_PLUS }),
      ],
      WINDOW_5H,
    );

    // ChatGPT Plus is $20 in the table; the account is weighted at that, not at
    // the $200 its later Pro readings would carry.
    expect(accounts[0].priceUSD).toBe(20);
    expect(points[0].weighted).toBeCloseTo(50);
  });

  // The 'null' plan_type predates Codex reporting the field at all. There is no
  // way to know what it was, so it must stay unpriced rather than be guessed.
  it('leaves a reading with no plan unpriced', () => {
    const { accounts, points } = buildUsageSeries(
      [
        sample({ account_id: 'old', sampled_at: '2026-01-01T00:00:00Z', used_pct: 80, billing_provider: 'openai', plan: '' }),
      ],
      WINDOW_5H,
    );

    expect(accounts[0].priceUSD).toBe(0);
    expect(points[0].weighted).toBeNull();
  });
});

describe('empty window', () => {
  // A window with no readings is not a window full of unpriced accounts. The
  // two look identical in the totals — both have totalUSD 0 — so the chart has
  // to tell them apart by whether any account exists at all, or it names the
  // wrong problem and sends the reader to fix a price table that is fine.
  it('reports no accounts at all, not unpriced ones', () => {
    const { accounts } = buildUsageSeries([], WINDOW_5H);
    const coverage = coverageOf(accounts, []);

    expect(coverage.measuredAccounts).toBe(0);
    expect(coverage.unmeasured).toEqual([]);
    expect(coverage.totalUSD).toBe(0);
  });

  // Contrast: an account IS present but its plan is not in the price table.
  // Here totalUSD is also 0, and this one really is the "unpriced" case.
  it('distinguishes an unpriced account from an absent one', () => {
    const { accounts } = buildUsageSeries(
      [sample({ account_id: 'a', sampled_at: '2026-08-24T09:00:00Z', used_pct: 50, plan: UNPRICED })],
      WINDOW_5H,
    );
    const coverage = coverageOf(accounts, [{ accountId: 'anthropic:a', label: 'a', priceUSD: 0 }]);

    expect(coverage.measuredAccounts).toBe(0);
    expect(coverage.unmeasured).toHaveLength(1);
  });
});

describe('defaultWindowFor', () => {
  const at = '2026-08-24T09:00:00Z';

  // Codex stopped reporting its five-hour window in July; Claude still does. On
  // a Codex-only install, opening on 5h shows nothing — and once other
  // providers are present it shows SOME of them, which reads as all of them.
  it('falls to the weekly window when the five-hour one is empty', () => {
    const weeklyOnly = [
      sample({ account_id: 'codex', sampled_at: at, used_pct: 40, billing_provider: 'openai',
               plan: CODEX_PRO, window_key: 'weekly_all', window_minutes: WINDOW_7D }),
    ];

    expect(defaultWindowFor(weeklyOnly)).toBe(WINDOW_7D);
  });

  // 5h pins a session, so it wins whenever it has anything at all — even one
  // reading against thousands of weekly ones.
  it('prefers the five-hour window whenever it is populated', () => {
    const mixed = [
      sample({ account_id: 'codex', sampled_at: at, used_pct: 40, billing_provider: 'openai',
               plan: CODEX_PRO, window_key: 'weekly_all', window_minutes: WINDOW_7D }),
      sample({ account_id: 'claude', sampled_at: at, used_pct: 30 }),
    ];

    expect(defaultWindowFor(mixed)).toBe(WINDOW_5H);
  });

  // With nothing at all the empty state still needs a window to name.
  it('names the five-hour window when there is no data', () => {
    expect(defaultWindowFor([])).toBe(WINDOW_5H);
  });

  it('does not select a window when every sample is excluded Spark', () => {
    const sparkOnly = [
      sample({ account_id: 'codex', sampled_at: at, used_pct: 4, billing_provider: 'openai',
               plan: CODEX_PRO, window_key: 'codex_bengalfox:300' }),
    ];

    expect(defaultWindowFor(sparkOnly)).toBeNull();
  });
});

describe('toggleSelection', () => {
  const all = ['anthropic', 'openai'];

  // The chips are a filter, and a filter with two members has to be able to
  // express both. Isolating on every click made "Claude and Codex" reachable
  // only by clicking the lone selected chip a second time -- an undo gesture,
  // not a selection -- so from one provider the other was never one click away.
  it('adds a clicked item to an existing selection', () => {
    expect(toggleSelection(['anthropic'], 'openai', all)).toEqual(all);
  });

  it('removes a clicked item while another remains selected', () => {
    expect(toggleSelection(all, 'openai', all)).toEqual(['anthropic']);
  });

  // Order follows the canonical list rather than click order, so the same
  // selection always renders the same way.
  it('keeps the canonical order regardless of click order', () => {
    expect(toggleSelection(['openai'], 'anthropic', all)).toEqual(all);
  });

  // An ordinary click must never blank the chart, so the last remaining chip
  // restores the full set instead of clearing it.
  it('restores all when the sole selected item is clicked', () => {
    expect(toggleSelection(['openai'], 'openai', all)).toEqual(all);
  });
});

describe('combinationHasData', () => {
  const claude5h = sample({ account_id: 'a', sampled_at: '2026-08-27T00:00:00Z', used_pct: 10 });
  const codex7d = sample({
    account_id: 'b',
    sampled_at: '2026-08-27T00:00:00Z',
    used_pct: 20,
    billing_provider: 'openai',
    window_key: '10080',
    window_minutes: WINDOW_7D,
    plan: CODEX_PRO,
  });

  // Availability is a property of the pair, not of either axis alone. Both
  // chips can be individually backed by samples while the combination the
  // reader just asked for has none -- which is the empty chart this predicate
  // exists to see coming.
  it('sees a pair that no sample satisfies even though each side has data', () => {
    expect(combinationHasData([claude5h, codex7d], [WINDOW_7D], ['anthropic'])).toBe(false);
  });

  it('sees a pair a sample satisfies', () => {
    expect(combinationHasData([claude5h, codex7d], [WINDOW_5H], ['anthropic'])).toBe(true);
  });

  it('sees a wider selection satisfied by any one of its members', () => {
    expect(combinationHasData([claude5h, codex7d], [WINDOW_5H, WINDOW_7D], ['anthropic', 'openai'])).toBe(true);
  });

  it('reports an empty axis as unsatisfiable rather than matching everything', () => {
    expect(combinationHasData([claude5h, codex7d], [], ['anthropic'])).toBe(false);
    expect(combinationHasData([claude5h, codex7d], [WINDOW_5H], [])).toBe(false);
  });
});

describe('buildWindows + mergeRows', () => {
  const at = '2026-08-24T09:00:00Z';
  const claude5h = sample({ account_id: 'c', sampled_at: at, used_pct: 30 });
  const claude7d = sample({ account_id: 'c', sampled_at: at, used_pct: 60, window_key: 'weekly_all', window_minutes: WINDOW_7D });
  const codex7d = sample({ account_id: 'x', sampled_at: at, used_pct: 90, billing_provider: 'openai', plan: CODEX_PRO, window_key: 'weekly_all', window_minutes: WINDOW_7D });

  // The two windows are different time scales, so each keeps its own weighted
  // line. One average over both would name no state at all.
  it('does not build a phantom window from Spark-only input', () => {
    const sparkOnly = sample({
      account_id: 'codex-spark',
      sampled_at: at,
      used_pct: 4,
      billing_provider: 'openai',
      plan: CODEX_PRO,
      window_key: 'codex_bengalfox:300',
      scope_label: 'GPT-5.3-Codex-Spark',
    });

    expect(buildWindows([sparkOnly], [WINDOW_5H], ['openai'])).toEqual([]);
  });

  it('gives each window its own weighted line', () => {
    const rows = mergeRows(buildWindows([claude5h, claude7d], [WINDOW_5H, WINDOW_7D], ['anthropic']));

    expect(rows).toHaveLength(1);
    expect(rows[0][weightedKey(WINDOW_5H)]).toBeCloseTo(30);
    expect(rows[0][weightedKey(WINDOW_7D)]).toBeCloseTo(60);
  });

  it('keeps two accounts under one provider distinct from its weighted line', () => {
    const codexAccounts = [
      sample({ account_id: 'alice', login_email: 'alice@example.test', sampled_at: at, used_pct: 20,
               billing_provider: 'openai', plan: CODEX_PRO, window_key: '300' }),
      sample({ account_id: 'bob', login_email: 'bob@example.test', sampled_at: at, used_pct: 80,
               billing_provider: 'openai', plan: CODEX_PRO, window_key: '300' }),
    ];

    const rows = mergeRows(buildWindows(codexAccounts, [WINDOW_5H], ['openai']));

    expect(rows[0][seriesKey('openai:alice', WINDOW_5H)]).toBe(20);
    expect(rows[0][seriesKey('openai:bob', WINDOW_5H)]).toBe(80);
  });

  // One account appears once per window, and those are different series. A
  // shared key would make the weekly reading overwrite the five-hour one.
  it('keeps one account\'s two windows as separate series', () => {
    const rows = mergeRows(buildWindows([claude5h, claude7d], [WINDOW_5H, WINDOW_7D], ['anthropic']));

    expect(rows[0][seriesKey('anthropic:c', WINDOW_5H)]).toBe(30);
    expect(rows[0][seriesKey('anthropic:c', WINDOW_7D)]).toBe(60);
  });

  // Turning a provider off removes it from the lines AND from the weighted
  // denominator — otherwise the average would still be counting an account the
  // reader asked not to see.
  it('excludes a disabled provider from lines and from the average', () => {
    const both = mergeRows(buildWindows([claude7d, codex7d], [WINDOW_7D], ['anthropic', 'openai']));
    const claudeOnly = mergeRows(buildWindows([claude7d, codex7d], [WINDOW_7D], ['anthropic']));

    // Claude $100 at 60%, Codex $200 at 90% -> (100*60 + 200*90)/300 = 80
    expect(both[0][weightedKey(WINDOW_7D)]).toBeCloseTo(80);
    expect(claudeOnly[0][weightedKey(WINDOW_7D)]).toBeCloseTo(60);
    expect(claudeOnly[0][seriesKey('openai:x', WINDOW_7D)]).toBeUndefined();
  });

  it('draws nothing when every provider is off', () => {
    expect(mergeRows(buildWindows([claude5h, codex7d], [WINDOW_5H, WINDOW_7D], []))).toEqual([]);
  });

  it('draws nothing when every window is off', () => {
    expect(mergeRows(buildWindows([claude5h, codex7d], [], ['anthropic', 'openai']))).toEqual([]);
  });
});

// The inferred band marks instants whose account was chosen across a gap in the
// observation log. It survived the move to merged rows: dropping it would have
// made an inferred reading indistinguishable from an observed one, which is the
// one distinction the band exists to draw.
//
// inferred is a per-account flag, not a per-sample one, so an account with any
// inferred attribution stays banded across its whole series.
describe('mergeRows inferred band', () => {
  const rowsFor = (samples: QuotaSample[]) =>
    mergeRows(buildWindows(samples, [WINDOW_5H], ['anthropic'])).map((r) => r.inferredBand);

  it('bands an account whose attribution was inferred', () => {
    expect(
      rowsFor([
        sample({ account_id: 'a', sampled_at: '2026-08-24T09:00:00Z', used_pct: 40, attribution: 'inferred' }),
        sample({ account_id: 'a', sampled_at: '2026-08-24T10:00:00Z', used_pct: 50 }),
      ]),
    ).toEqual([100, 100]);
  });

  it('leaves an all-observed series unbanded', () => {
    expect(
      rowsFor([
        sample({ account_id: 'a', sampled_at: '2026-08-24T09:00:00Z', used_pct: 40, attribution: 'observed' }),
        sample({ account_id: 'a', sampled_at: '2026-08-24T10:00:00Z', used_pct: 50, attribution: 'observed' }),
      ]),
    ).toEqual([null, null]);
  });
});

// An account's plan changes over time and each reading keeps the plan it was
// taken under, so one series spans several. What the chart divides by is the
// subscription being burned now, so the newest reading names it.
describe('plan resolution', () => {
  it('prices an account by its newest plan, not its first', () => {
    // The real shape: a Codex account that started on Plus ($20) and has been on
    // Pro ($200) since. Taking the first non-empty plan meant the oldest,
    // because samples arrive in sampled_at order.
    const { accounts } = buildUsageSeries(
      [
        sample({ account_id: 'a', sampled_at: '2026-03-04T00:00:00Z', used_pct: 10, billing_provider: 'openai', plan: CODEX_PLUS }),
        sample({ account_id: 'a', sampled_at: '2026-08-20T00:00:00Z', used_pct: 20, billing_provider: 'openai', plan: CODEX_PRO }),
      ],
      WINDOW_5H,
    );

    expect(accounts).toHaveLength(1);
    expect(accounts[0].priceUSD).toBe(200);
  });

  // Order of arrival must not decide it: the timestamps do.
  it('is not fooled by an out-of-order batch', () => {
    const { accounts } = buildUsageSeries(
      [
        sample({ account_id: 'a', sampled_at: '2026-08-20T00:00:00Z', used_pct: 20, billing_provider: 'openai', plan: CODEX_PRO }),
        sample({ account_id: 'a', sampled_at: '2026-03-04T00:00:00Z', used_pct: 10, billing_provider: 'openai', plan: CODEX_PLUS }),
      ],
      WINDOW_5H,
    );
    expect(accounts[0].priceUSD).toBe(200);
  });

  // A reading taken before the plan was recorded must not blank an account that
  // a later reading did name.
  it('keeps a plan that a later reading supplied', () => {
    const { accounts } = buildUsageSeries(
      [
        sample({ account_id: 'a', sampled_at: '2026-03-04T00:00:00Z', used_pct: 10, billing_provider: 'openai', plan: '' }),
        sample({ account_id: 'a', sampled_at: '2026-08-20T00:00:00Z', used_pct: 20, billing_provider: 'openai', plan: CODEX_PRO }),
      ],
      WINDOW_5H,
    );
    expect(accounts[0].priceUSD).toBe(200);
  });
});

describe('lineDash', () => {
  // A dash is a code the reader has to decode, and it only earns that cost while
  // there are two windows to tell apart. With one window on screen every line
  // belongs to it, so the dash encodes nothing and only breaks the stroke up.
  it('draws every line solid while a single window is on screen', () => {
    expect(lineDash(WINDOW_5H, 1)).toBeUndefined();
    expect(lineDash(WINDOW_7D, 1)).toBeUndefined();
  });

  // With both on screen the dash is the only thing separating them: colour is
  // spent on accounts and width on the weighted/account distinction.
  it('separates the two windows once both are drawn', () => {
    expect(lineDash(WINDOW_5H, 2)).toBeUndefined();
    expect(lineDash(WINDOW_7D, 2)).toBe(DASH_7D);
  });

  // The shorter window keeps the unbroken stroke because it is the one that says
  // what is being spent right now -- the same reason the headline reads from it.
  it('keeps the shorter window solid rather than the longer one', () => {
    expect(lineDash(WINDOW_5H, 2)).toBeUndefined();
    expect(lineDash(WINDOW_7D, 2)).toBeDefined();
  });
});

// Claude reports the seven-day window twice: weekly_all for every model, and
// weekly_scoped for one model. Both carry window_minutes 10080, so selecting by
// duration interleaved two unrelated meters at the same instants and the line
// zig-zagged between them. The all-model meter is the account's 7d state; the
// scoped one is only used when nothing else was reported.
describe('anthropic seven-day meter selection', () => {
  const weekly = (key: string, pct: number, at: string): QuotaSample => sample({
    account_id: 'claude-account-id', sampled_at: at, used_pct: pct,
    window_key: key, window_minutes: WINDOW_7D,
  });

  it('draws weekly_all alone when weekly_scoped shares its timestamps', () => {
    const { accounts } = buildUsageSeries([
      weekly('weekly_all', 40, '2026-08-24T09:00:00Z'),
      weekly('weekly_scoped', 90, '2026-08-24T09:00:00Z'),
      weekly('weekly_all', 42, '2026-08-24T09:05:00Z'),
      weekly('weekly_scoped', 91, '2026-08-24T09:05:00Z'),
    ], WINDOW_7D);
    expect(accounts[0].points.map((p) => p.pct)).toEqual([40, 42]);
  });

  it('falls back to weekly_scoped when no all-model meter was reported', () => {
    const { accounts } = buildUsageSeries([
      weekly('weekly_scoped', 90, '2026-08-24T09:00:00Z'),
    ], WINDOW_7D);
    expect(accounts[0].points.map((p) => p.pct)).toEqual([90]);
  });
});

describe('scopeSamples', () => {
  const at = '2026-03-01T00:00:00Z';
  // The shape that actually exists on prod: anthropic rows carry the email,
  // openai rows mostly do not, and the account id is the only thing every row of
  // one account shares.
  const samples = [
    sample({ account_id: 'ant-1', sampled_at: at, used_pct: 10, login_email: 'a@x.test' }),
    sample({ account_id: 'ant-2', sampled_at: at, used_pct: 20, login_email: 'b@x.test' }),
    sample({ account_id: 'oai-1', sampled_at: at, used_pct: 30, billing_provider: 'openai', login_email: 'a@x.test' }),
    sample({ account_id: 'oai-1', sampled_at: at, used_pct: 31, billing_provider: 'openai', login_email: '' }),
    sample({ account_id: 'oai-2', sampled_at: at, used_pct: 40, billing_provider: 'openai', login_email: '' }),
  ];

  it('is a no-op when nothing is scoped', () => {
    expect(scopeSamples(samples, {})).toBe(samples);
  });

  // The bug this function exists to prevent: filtering on login_email drops the
  // anonymous rows, which is most of Codex.
  it('keeps an account\'s rows that carry no login email', () => {
    const scoped = scopeSamples(samples, { accountEmail: 'a@x.test' });
    expect(scoped.map((s) => s.used_pct).sort((x, y) => x - y)).toEqual([10, 30, 31]);
  });

  it('leaves out an account that never names the email anywhere', () => {
    const scoped = scopeSamples(samples, { accountEmail: 'a@x.test' });
    expect(scoped.some((s) => s.account_id === 'oai-2')).toBe(false);
  });

  it('maps the agent scope onto billing providers', () => {
    expect(scopeSamples(samples, { agent: 'claude' }).every((s) => s.billing_provider === 'anthropic')).toBe(true);
    expect(scopeSamples(samples, { agent: 'codex' }).every((s) => s.billing_provider === 'openai')).toBe(true);
  });

  // 'other' harnesses run on no metered subscription. Empty is the honest answer
  // and the caller says so; a wildcard would show Claude quota under an Other pill.
  it('selects nothing for an agent with no metered subscription', () => {
    expect(scopeSamples(samples, { agent: 'other' })).toEqual([]);
    expect(providersForAgent('other')).toEqual([]);
  });

  it('intersects the two axes rather than picking one', () => {
    const scoped = scopeSamples(samples, { accountEmail: 'a@x.test', agent: 'codex' });
    expect(scoped.map((s) => s.used_pct).sort((x, y) => x - y)).toEqual([30, 31]);
  });
});

describe('timeWeightedMean', () => {
  const H = 3_600_000;

  // Weighting by duration, not by sample count: 90% held for one hour and 10%
  // held for three is 30%, however many times each was reported.
  it('weights each reading by the span it holds, not by how often it was reported', () => {
    const points = [
      { t: 0, v: 90 },
      { t: H, v: 10 },
      { t: 1.5 * H, v: 10 },
      { t: 2 * H, v: 10 },
    ];
    expect(timeWeightedMean(points, 0, 4 * H)).toBe(30);
  });

  // Dividing by the bucket instead would average the unmeasured half against
  // zero and report half the truth.
  it('divides by the covered span, not the bucket', () => {
    expect(timeWeightedMean([{ t: 2 * H, v: 50 }], 0, 4 * H)).toBe(50);
  });

  it('returns null for a bucket no reading covers', () => {
    expect(timeWeightedMean([{ t: 10 * H, v: 50 }], 0, H)).toBeNull();
    expect(timeWeightedMean([], 0, H)).toBeNull();
  });

  // A reset is a step down to 0 like any other step. If it needed a special
  // case, the model would be wrong somewhere else too.
  it('needs no special case for a window reset', () => {
    expect(timeWeightedMean([{ t: 0, v: 80 }, { t: H, v: 0 }], 0, 2 * H)).toBe(40);
  });
});

describe('bucketUsagePoints', () => {
  // Local midnights, not UTC ones. Buckets are keyed in local calendar parts to
  // agree with the server's `ts AT TIME ZONE tz`, so a range written in UTC
  // straddles a different number of local days depending on where it runs.
  const day = (n: number) => new Date(2026, 2, n).toISOString();

  it('keys buckets the way the trend chart does, so the two axes line up', () => {
    const points = [
      { t: Date.parse(day(2)) + 3_600_000, weighted: 40, byAccount: { a: 40 }, inferred: false },
    ];
    const buckets = bucketUsagePoints(points, 'day', day(2), day(4));
    expect(buckets.map((b) => b.date)).toEqual(['2026-03-02', '2026-03-03']);
  });

  it('leaves a bucket with no covering reading null rather than zero', () => {
    const points = [
      { t: Date.parse(day(3)) + 3_600_000, weighted: 40, byAccount: { a: 40 }, inferred: false },
    ];
    const buckets = bucketUsagePoints(points, 'day', day(2), day(4));
    expect(buckets[0].weighted).toBeNull();
    expect(buckets[0].byAccount).toEqual({});
    expect(buckets[1].weighted).toBe(40);
  });
});

describe('barOpacity', () => {
  it('spends no channel while one window is drawn', () => {
    expect(barOpacity(WINDOW_5H, 1)).toBe(1);
    expect(barOpacity(WINDOW_7D, 1)).toBe(1);
  });

  it('lightens the weekly window only when both are drawn', () => {
    expect(barOpacity(WINDOW_5H, 2)).toBe(1);
    expect(barOpacity(WINDOW_7D, 2)).toBeLessThan(1);
  });
});

describe('account identity does not depend on the window being drawn', () => {
  // An account is the same account on every meter it reports. The label was
  // collected inside the window filter, so a series could only be named by rows of
  // its own window -- and login_email is not filled evenly across windows (#679).
  //
  // Claude carries both windows on every plan, so it is what this can be written
  // against. The symptom that first showed this was a Codex 5h row; a Codex general
  // 5h meter exists only on some plans (Plus, not Pro -- #687), and the invariant
  // here does not depend on which.
  const a7d = sample({
    account_id: 'acct',
    window_key: 'weekly_all',
    window_minutes: WINDOW_7D,
    sampled_at: '2026-09-01T00:00:00Z',
    used_pct: 30,
    login_email: 'known@example.test',
  });
  const a5h = sample({
    account_id: 'acct',
    window_key: 'session',
    window_minutes: WINDOW_5H,
    sampled_at: '2026-09-01T00:00:00Z',
    used_pct: 12,
  });

  it('names the 5h series from the same account\'s 7d rows', () => {
    const { accounts } = buildUsageSeries([a5h, a7d], WINDOW_5H);
    expect(accounts).toHaveLength(1);
    expect(accounts[0].label).toBe('known@example.test');
  });

  it('still names the 7d series', () => {
    const { accounts } = buildUsageSeries([a5h, a7d], WINDOW_7D);
    expect(accounts[0].label).toBe('known@example.test');
  });

  it('falls back to the account id only when no window carries an address', () => {
    const { accounts } = buildUsageSeries([a5h], WINDOW_5H);
    expect(accounts[0].label).toBe('acct');
  });

  it('does not lend one account\'s address to another', () => {
    const other = sample({
      account_id: 'other',
      window_key: 'session',
      window_minutes: WINDOW_5H,
      sampled_at: '2026-09-01T00:00:00Z',
      used_pct: 5,
    });
    const { accounts } = buildUsageSeries([a5h, a7d, other], WINDOW_5H);
    const labels = Object.fromEntries(accounts.map(a => [a.accountId, a.label]));
    expect(labels['anthropic:acct']).toBe('known@example.test');
    expect(labels['anthropic:other']).toBe('other');
  });
});

describe('valueAt does not carry a reading forever', () => {
  // The burn chart draws one instant per reading from any account, and every other
  // account fills that instant with its own last-known value. With no bound on how
  // old that value may be, an account that stopped reporting was drawn as a flat
  // line holding its last percentage -- "sat at 25% for three days" where the truth
  // was "nobody was looking" (#678).
  //
  // Measured on prod over 21 days, readings arrive well inside 7 minutes (p99 244s
  // to 411s across both providers and both windows) and the next thing out is over
  // an hour. STALE_AFTER_MS sits in that empty band, so no ordinary sampling gap is
  // cut and no real outage is bridged.
  const points = [
    { t: Date.parse('2026-09-01T00:00:00Z'), pct: 25 },
    { t: Date.parse('2026-09-04T00:00:00Z'), pct: 60 },
  ];

  it('reports the reading at its own instant', () => {
    expect(valueAt(points, Date.parse('2026-09-01T00:00:00Z'))).toBe(25);
  });

  it('carries a reading across an ordinary sampling gap', () => {
    expect(valueAt(points, Date.parse('2026-09-01T00:05:00Z'))).toBe(25);
  });

  it('stops carrying once the reading is stale', () => {
    // Three days later the account had said nothing. It must not be drawn at 25%.
    expect(valueAt(points, Date.parse('2026-09-03T00:00:00Z'))).toBeNull();
  });

  it('picks the reading up again at the next one', () => {
    expect(valueAt(points, Date.parse('2026-09-04T00:00:00Z'))).toBe(60);
  });

  it('still reports nothing before the first reading', () => {
    expect(valueAt(points, Date.parse('2026-08-31T00:00:00Z'))).toBeNull();
  });
});
