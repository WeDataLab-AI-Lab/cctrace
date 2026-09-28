import { planLabel, priceOf } from './plan-prices';
import { advanceBucket, alignBucketStart, bucketKey } from './trend-axis';
import type { TrendGranularity } from './trend-axis';
import type { QuotaSample } from './types';

/**
 * Utilization is a percentage of each account's own quota, so averaging the
 * percentages compares unlike things — 90% of a $100 plan and 40% of a $200 one
 * are not two numbers that can be added. The API never reports the absolute
 * limits either, so a quota-weighted average is not computable.
 *
 * Money is. Dollars add up across providers in a way quotas do not: Claude
 * being exhausted does not hand its work to Codex's quota.
 *
 *   weighted(t) = Σ(price_i × util_i(t)) / Σ(price_i)
 *
 * The result is read as the share of the subscription bill consumed, so the
 * height of the line at any instant is the money still unspent — and the height
 * just before a window resets is what was paid for and thrown away.
 */

/** The five-hour and weekly windows, in minutes. */
const WINDOW_5H = 300;
const WINDOW_7D = 10080;

interface AccountSeries {
  /** Billing account identity, shared by all of its quota buckets. */
  accountId: string;
  /** Drawable series identity for this account's general provider bucket. */
  seriesId: string;
  provider: string;
  label: string;
  plan: string;
  /** Display name for the plan, e.g. "Claude Max 20x". */
  planLabel: string;
  priceUSD: number;
  /** When the sample that named the plan above was taken. An account's plan
   *  changes over time, and what the chart divides by is the current one. */
  planAt: number;
  /** true when any sample in the range was attributed across a gap. */
  hasInferred: boolean;
  points: { t: number; pct: number }[];
}

interface UsagePoint {
  t: number;
  /** Price-weighted utilization across measured accounts, 0-100. */
  weighted: number | null;
  /** Per-account utilization, keyed by account id. */
  byAccount: Record<string, number>;
  /** true when every account contributing to this instant was inferred. */
  inferred: boolean;
}

interface Coverage {
  /** Monthly spend of accounts with a measurable window in range. */
  measuredUSD: number;
  /** Monthly spend of every account we know of, measured or not. */
  totalUSD: number;
  measuredAccounts: number;
  /** Accounts with a price but no readings — these are excluded from the
   *  denominator rather than counted as 0%. */
  unmeasured: { accountId: string; label: string; priceUSD: number }[];
}

/** Spark is a separate model quota, not part of the general Codex subscription
 * meter. The known id covers rows whose app-server omitted the display name;
 * the name covers future Spark ids without coupling this computation to them. */
const isSparkQuotaSample = (s: QuotaSample): boolean =>
  s.billing_provider === 'openai' && (
    s.window_key.startsWith('codex_bengalfox:') || /spark/i.test(s.scope_label ?? '')
  );

/** windowMinutesOf reads a sample's window length, falling back to the numeric
 *  window_key Codex uses when the field is absent. */
const windowMinutesOf = (s: QuotaSample): number => {
  if (typeof s.window_minutes === 'number' && s.window_minutes > 0) return s.window_minutes;
  const parsed = Number(s.window_key);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
};

/**
 * groupByAccount folds samples of one window length into per-account series.
 *
 * Only one window length at a time: 5h and 7d are different time scales, and a
 * series mixing them names no state at all.
 */
const groupByAccount = (
  samples: QuotaSample[],
  windowMinutes: number,
): AccountSeries[] => {
  const grouped = new Map<string, QuotaSample[]>();
  const loginEmailByAccount = new Map<string, string>();

  for (const s of samples) {
    if (isSparkQuotaSample(s)) continue;
    const accountId = `${s.billing_provider}:${s.account_id}`;
    // Identity first, and outside the window filter: an account is the same account
    // on every meter it reports. Collecting the address inside the filter meant the
    // 5h series could only be named by 5h rows, and Codex fills login_email unevenly
    // -- the same account came out named on 7d and as a raw 36-character uuid on 5h,
    // side by side in one legend (#679). Never widened past the account key: the
    // address is keyed by account, so no account can borrow another's.
    if (s.login_email) loginEmailByAccount.set(accountId, s.login_email);
    if (windowMinutesOf(s) !== windowMinutes) continue;
    const group = grouped.get(accountId) ?? [];
    group.push(s);
    grouped.set(accountId, group);
  }

  return [...grouped.entries()].map(([accountId, accountSamples]) => {
    const provider = accountSamples[0].billing_provider;
    // The app-server may report the general Codex meter as a bare key alongside
    // non-Spark model-scoped keys. A general meter is authoritative when
    // present. Otherwise the remaining scoped meters are normalized to their
    // pointwise maximum: unlike quota percentages cannot be added or averaged,
    // while the most-consumed active limit is the conservative answer to how
    // close this account is to being blocked.
    //
    // Claude has the same shape on the seven-day window: weekly_all is the
    // account's meter and weekly_scoped narrows the same seven days to one
    // model. Both carry window_minutes 10080, so selecting by duration alone
    // interleaved the two at identical instants and the line zig-zagged between
    // meters. The scoped meter is drawn only when nothing else was reported.
    const canonical = provider === 'openai'
      ? accountSamples.filter((s) => !s.window_key.includes(':'))
      : accountSamples.filter((s) => s.window_key !== 'weekly_scoped');
    const selected = canonical.length > 0 ? canonical : accountSamples;
    const newestPlanSample = selected
      .filter((s) => s.plan)
      .sort((a, b) => Date.parse(b.sampled_at) - Date.parse(a.sampled_at))[0];
    const plan = newestPlanSample?.plan ?? '';

    let points: { t: number; pct: number }[];
    if (provider === 'openai' && canonical.length === 0) {
      const byBucket = new Map<string, { t: number; pct: number }[]>();
      for (const s of selected) {
        const bucket = byBucket.get(s.window_key) ?? [];
        bucket.push({ t: Date.parse(s.sampled_at), pct: s.used_pct });
        byBucket.set(s.window_key, bucket);
      }
      for (const bucket of byBucket.values()) bucket.sort((a, b) => a.t - b.t);
      const instants = [...new Set(selected.map((s) => Date.parse(s.sampled_at)))].sort((a, b) => a - b);
      points = instants.map((t) => ({
        t,
        pct: Math.max(...[...byBucket.values()]
          .map((bucket) => valueAt(bucket, t))
          .filter((pct): pct is number => pct !== null)),
      }));
    } else {
      points = selected
        .map((s) => ({ t: Date.parse(s.sampled_at), pct: s.used_pct }))
        .sort((a, b) => a.t - b.t);
    }

    return {
      accountId,
      seriesId: accountId,
      provider,
      label: loginEmailByAccount.get(accountId) || accountSamples[0].account_id,
      plan,
      planLabel: planLabel(provider, plan),
      priceUSD: priceOf(provider, plan),
      planAt: newestPlanSample ? Date.parse(newestPlanSample.sampled_at) : -Infinity,
      hasInferred: selected.some((s) => s.attribution === 'inferred'),
      points,
    };
  });
};

/**
 * valueAt carries the last reading forward.
 *
 * This is not smoothing. A rate-limit window holds its value between readings —
 * it only moves when work is done — so the previous reading is what the meter
 * actually said, not an approximation of it. Before a series' first reading
 * there is nothing to carry, and null keeps "not measured yet" distinct from 0.
 */
/**
 * How long a reading may stand in for an account that has said nothing since.
 *
 * The chart puts one instant on the timeline per reading from any account, and
 * every other account fills that instant with its last-known value. Unbounded,
 * an account that stopped reporting was drawn as a flat line holding its last
 * percentage: "sat at 25% for three days" where the truth was that nobody was
 * looking (#678) -- the same mistake as showing an unmeasured account as 0%.
 *
 * Measured on prod over 21 days, readings arrive well inside seven minutes: p99
 * is 244s to 411s across both providers and both windows. The next thing out is
 * over an hour. Thirty minutes sits in that empty band, so no ordinary sampling
 * gap is cut and no real silence is bridged. If the client's cadence changes,
 * this is the number to re-measure.
 */
const STALE_AFTER_MS = 30 * 60 * 1000;

const valueAt = (points: { t: number; pct: number }[], t: number): number | null => {
  let latest: { t: number; pct: number } | null = null;
  for (const p of points) {
    if (p.t > t) break;
    latest = p;
  }
  if (latest === null || t - latest.t > STALE_AFTER_MS) return null;
  return latest.pct;
};

/** A final chart timeline denser than its roughly thousand-pixel plot adds
 * SVG vertices without adding visible resolution. The store applies the same
 * bound per database series, but combining accounts and quota buckets unions
 * their independently sampled timestamps and can multiply it back into many
 * thousands of points. */
const MAX_USAGE_POINTS = 1000;

/**
 * downsampleInstants bounds the combined timeline, retaining the first instant
 * and each time bucket's closing instant. Utilization is a step gauge, so the
 * closing value is the state carried into the next bucket. The final instant is
 * retained explicitly so the headline and line endpoint stay current.
 */
const downsampleInstants = (instants: number[]): number[] => {
  if (instants.length <= MAX_USAGE_POINTS) return instants;

  const first = instants[0];
  const last = instants[instants.length - 1];
  const span = last - first;
  if (span <= 0) return [first];

  const interiorBucketCount = MAX_USAGE_POINTS - 2;
  const bucketLast = new Array<number | undefined>(interiorBucketCount);
  for (let i = 1; i < instants.length - 1; i += 1) {
    const bucket = Math.min(
      interiorBucketCount - 1,
      Math.floor(((instants[i] - first) / span) * interiorBucketCount),
    );
    bucketLast[bucket] = instants[i];
  }

  return [first, ...bucketLast.filter((t): t is number => t !== undefined), last];
};

/**
 * buildUsageSeries turns raw samples into chart points.
 *
 * Small inputs keep every distinct reading instant. Dense inputs are sampled
 * only after all series have been combined, because a per-series bound is
 * undone as soon as independently sampled account timelines are unioned.
 */
const buildUsageSeries = (
  samples: QuotaSample[],
  windowMinutes: number,
): { points: UsagePoint[]; accounts: AccountSeries[] } => {
  const accounts = groupByAccount(samples, windowMinutes);
  if (accounts.length === 0) return { points: [], accounts: [] };

  const instants = downsampleInstants(
    [...new Set(accounts.flatMap((a) => a.points.map((p) => p.t)))].sort((a, b) => a - b),
  );

  const points = instants.map((t) => {
    const byAccount: Record<string, number> = {};
    let weightedSum = 0;
    let priceSum = 0;
    let contributing = 0;
    let inferredContributing = 0;

    for (const a of accounts) {
      const pct = valueAt(a.points, t);
      if (pct === null) continue;
      byAccount[a.seriesId] = pct;
      contributing += 1;
      if (a.hasInferred) inferredContributing += 1;
      // An unpriced account still draws its own line but cannot weight the
      // average: a price of 0 would silently pull the result toward the priced
      // accounts as though this one cost nothing.
      if (a.priceUSD > 0) {
        weightedSum += a.priceUSD * pct;
        priceSum += a.priceUSD;
      }
    }

    return {
      t,
      weighted: priceSum > 0 ? weightedSum / priceSum : null,
      byAccount,
      inferred: contributing > 0 && inferredContributing === contributing,
    };
  });

  return { points, accounts };
};

/**
 * coverageOf reports what the weighted average actually covers.
 *
 * An account we do not measure must not enter the denominator. Counting it at
 * 0% reads as "idle" when the truth is "not looked at", and the whole average
 * comes out low with nothing on screen to say so — "we are not watching" shown
 * as "there is nothing to see". Such accounts are excluded and listed instead.
 */
const coverageOf = (
  accounts: AccountSeries[],
  knownAccounts: { accountId: string; label: string; priceUSD: number }[],
): Coverage => {
  const measured = accounts.filter((a) => a.points.length > 0 && a.priceUSD > 0);
  const measuredIds = new Set(measured.map((a) => a.accountId));
  const measuredUSD = measured.reduce((sum, a) => sum + a.priceUSD, 0);

  const unmeasured = knownAccounts.filter((k) => !measuredIds.has(k.accountId));
  const totalUSD = measuredUSD + unmeasured.reduce((sum, k) => sum + k.priceUSD, 0);

  return {
    measuredUSD,
    totalUSD,
    measuredAccounts: measured.length,
    unmeasured,
  };
};

/**
 * defaultWindowFor picks the window to open on when the reader has not chosen.
 *
 * The five-hour window is the more useful of the two — it is the one that
 * actually pins a session — so it wins whenever it has anything to show.
 *
 * It is not always there to show. Whether Codex's general meter has a
 * five-hour window depends on the plan, not on how it is collected: OpenAI
 * removed it for every plan on 2026-07-12 and brought it back for Plus only
 * around 2026-08-25, so a Pro account reports the weekly window alone. On a
 * Codex Pro-only installation the 5h window is empty for reasons that have
 * nothing to do with usage. Opening there would not merely show a blank plot:
 * with several providers it shows SOME of them and reads as though that were
 * all of them.
 *
 * Preference, then, is 5h if populated, else 7d if populated. A truly empty
 * response keeps 5h for the empty state, while an excluded-only response has
 * no default: naming one would turn hidden metadata into a phantom window.
 */
const defaultWindowFor = (samples: QuotaSample[]): number | null => {
  let hasWeekly = false;
  for (const s of samples) {
    if (isSparkQuotaSample(s)) continue;
    const m = windowMinutesOf(s);
    if (m === WINDOW_5H) return WINDOW_5H;
    if (m === WINDOW_7D) hasWeekly = true;
  }
  if (hasWeekly) return WINDOW_7D;
  // A truly empty response keeps the historical empty-state default. A
  // non-empty response containing only excluded/unsupported meters must not
  // manufacture a visible window from data the chart cannot draw.
  return samples.length === 0 ? WINDOW_5H : null;
};

/** remainingUSD converts a weighted percentage into unspent subscription money. */
const remainingUSD = (weightedPct: number, measuredUSD: number): number =>
  measuredUSD * (1 - weightedPct / 100);

export { WINDOW_5H, WINDOW_7D, buildUsageSeries, coverageOf, defaultWindowFor, remainingUSD, valueAt };
export type { AccountSeries, Coverage, UsagePoint };

/** BILLING_PROVIDERS are the two that meter a subscription window. */
const PROVIDERS = [
  { id: 'anthropic', label: 'Claude' },
  { id: 'openai', label: 'Codex' },
] as const;

interface WindowSeries {
  windowMinutes: number;
  points: UsagePoint[];
  accounts: AccountSeries[];
}

/**
 * buildWindows folds the enabled windows into one drawable set.
 *
 * Each window is built independently and keeps its own weighted line. The two
 * are different time scales — a five-hour window and a weekly one answer
 * different questions — so a single average over both would name no state at
 * all. Drawing them side by side lets each be read for what it is.
 */
const buildWindows = (
  samples: QuotaSample[],
  windows: number[],
  providers: string[],
): WindowSeries[] => {
  const inScope = providers.length === 0
    ? []
    : samples.filter((s) => providers.includes(s.billing_provider));

  return windows.flatMap((windowMinutes) => {
    const { points, accounts } = buildUsageSeries(inScope, windowMinutes);
    return points.length > 0 ? [{ windowMinutes, points, accounts }] : [];
  });
};

/** Filter choices come from drawable general meters, not raw window metadata.
 * Otherwise an excluded model quota can leave behind a selectable empty shell. */
const availableWindowsFor = (samples: QuotaSample[]): number[] => {
  const providers = new Set<string>(PROVIDERS.map((provider) => provider.id));
  const present = new Set<number>();
  for (const sample of samples) {
    if (!isSparkQuotaSample(sample) && providers.has(sample.billing_provider)) {
      present.add(windowMinutesOf(sample));
    }
  }
  return [WINDOW_5H, WINDOW_7D].filter((windowMinutes) => present.has(windowMinutes));
};

const availableProvidersFor = (samples: QuotaSample[]): string[] => {
  const present = new Set(
    samples
      .filter((sample) => !isSparkQuotaSample(sample) && (
        windowMinutesOf(sample) === WINDOW_5H || windowMinutesOf(sample) === WINDOW_7D
      ))
      .map((sample) => sample.billing_provider),
  );
  return PROVIDERS.filter((provider) => present.has(provider.id)).map((provider) => provider.id);
};

/** The dash that marks the weekly window when both windows are drawn together. */
const DASH_7D = '5 3';

/**
 * lineDash decides whether a series is drawn broken, from how many windows are
 * on screen rather than from the window alone.
 *
 * A dash is a code the reader has to decode, and it only earns that cost while
 * there is something to tell apart. Every line used to carry one whether or not
 * a second window was drawn, so a chart showing 5h alone was full of broken
 * strokes that encoded nothing -- the window was already named by the chip above
 * and by every legend entry.
 *
 * When both are on the dash is the only channel left: colour is spent on
 * accounts, width on the weighted/account distinction. The shorter window keeps
 * the unbroken stroke, because it is the one that says what is being spent right
 * now -- the same reason the headline reads from it.
 */
const lineDash = (windowMinutes: number, drawnWindowCount: number): string | undefined => {
  if (drawnWindowCount < 2) return undefined;
  return windowMinutes === WINDOW_5H ? undefined : DASH_7D;
};

/** One bucket's average utilization, in the same shape mergeRows expects. */
interface BurnBucket {
  /** The trend chart's bucket key, so the two plots share an x axis. */
  date: string;
  weighted: number | null;
  byAccount: Record<string, number>;
}

/**
 * timeWeightedMean averages a step series over [from, to).
 *
 * Time-weighted, not the mean of the samples in the bucket. Readings are not
 * evenly spaced -- one account may report twice in an hour and another twenty
 * times -- so a sample mean would weight by reporting frequency instead of by
 * time. The step model is already what valueAt encodes: a rate-limit window
 * holds its value between readings, so each reading owns the span until the
 * next one, and a window reset's fall to 0 is handled by that span alone
 * without a special case.
 *
 * Divides by the COVERED duration, not the bucket's. An account whose readings
 * begin mid-week would otherwise be averaged against zero for the half nobody
 * measured, and come out quietly low. A bucket no reading covers is null, never
 * 0 -- "not measured" is not "not used", the distinction valueAt and coverageOf
 * already keep.
 */
const timeWeightedMean = (
  points: { t: number; v: number | null }[],
  from: number,
  to: number,
): number | null => {
  let weighted = 0;
  let covered = 0;
  for (let i = 0; i < points.length; i += 1) {
    const { t, v } = points[i];
    if (v === null) continue;
    const start = Math.max(t, from);
    const end = Math.min(i + 1 < points.length ? points[i + 1].t : to, to);
    if (end <= start) continue;
    weighted += v * (end - start);
    covered += end - start;
  }
  return covered > 0 ? weighted / covered : null;
};

/**
 * bucketUsagePoints folds the per-instant series into the trend chart's buckets.
 *
 * Built on buildUsageSeries' output rather than re-derived from raw samples, so
 * bucket mode and line mode share one definition of "weighted" instead of two
 * that can drift.
 */
const bucketUsagePoints = (
  points: UsagePoint[],
  granularity: TrendGranularity,
  since: string,
  until: string,
): BurnBucket[] => {
  if (points.length === 0) return [];
  const accountIds = [...new Set(points.flatMap((p) => Object.keys(p.byAccount)))];
  const sorted = [...points].sort((a, b) => a.t - b.t);

  const out: BurnBucket[] = [];
  const cursor = new Date(Date.parse(since));
  alignBucketStart(cursor, granularity);
  const end = Date.parse(until);
  // A guard, not a policy: a range and granularity that would produce thousands
  // of bars is a mistake upstream, and looping forever is the worse failure.
  for (let guard = 0; cursor.getTime() < end && guard < 1000; guard += 1) {
    const from = cursor.getTime();
    const date = bucketKey(cursor, granularity);
    advanceBucket(cursor, granularity);
    const to = Math.min(cursor.getTime(), end);

    const weighted = timeWeightedMean(sorted.map((p) => ({ t: p.t, v: p.weighted })), from, to);
    const byAccount: Record<string, number> = {};
    for (const id of accountIds) {
      const mean = timeWeightedMean(
        sorted.map((p) => ({ t: p.t, v: id in p.byAccount ? p.byAccount[id] : null })),
        from,
        to,
      );
      if (mean !== null) byAccount[id] = mean;
    }
    out.push({ date, weighted, byAccount });
  }
  return out;
};

/**
 * buildBurnBuckets mirrors buildWindows for bucket mode.
 */
const buildBurnBuckets = (
  samples: QuotaSample[],
  windows: number[],
  providers: string[],
  granularity: TrendGranularity,
  since: string,
  until: string,
): { windowMinutes: number; buckets: BurnBucket[]; accounts: AccountSeries[] }[] =>
  buildWindows(samples, windows, providers).map((w) => ({
    windowMinutes: w.windowMinutes,
    buckets: bucketUsagePoints(w.points, granularity, since, until),
    accounts: w.accounts,
  }));

/** mergeBurnRows is mergeRows for buckets: one row per bucket key. */
const mergeBurnRows = (
  series: { windowMinutes: number; buckets: BurnBucket[] }[],
): Record<string, string | number | null>[] => {
  const byDate = new Map<string, Record<string, string | number | null>>();
  for (const w of series) {
    for (const b of w.buckets) {
      const row = byDate.get(b.date) ?? { date: b.date };
      if (b.weighted !== null) row[weightedKey(w.windowMinutes)] = b.weighted;
      for (const [id, v] of Object.entries(b.byAccount)) row[seriesKey(id, w.windowMinutes)] = v;
      byDate.set(b.date, row);
    }
  }
  return [...byDate.values()];
};

/**
 * barOpacity is lineDash's contract for bars: a channel spent only while there
 * is something to tell apart. One window drawn, no channel; two, the weekly one
 * is lightened -- the shorter window keeps the solid fill for the same reason it
 * keeps the unbroken stroke.
 */
const barOpacity = (windowMinutes: number, drawnWindowCount: number): number => {
  if (drawnWindowCount < 2) return 1;
  return windowMinutes === WINDOW_5H ? 1 : 0.55;
};

/**
 * The two series the chart can draw. Not a filter over the data -- both are
 * derivable from whatever is on screen -- but a choice about which to show, and
 * so it takes the same isolate-and-restore shape as the window and provider chips
 * rather than a bespoke control.
 */
const SERIES_KINDS = ['accounts', 'weighted'] as const;
type SeriesKind = (typeof SERIES_KINDS)[number];

/**
 * The account keys a login email selects, resolved through account_id rather
 * than by matching the email column.
 *
 * Matching login_email directly looks correct and silently drops most of Codex:
 * openai rows carry login_email only some of the time, while every anthropic row
 * carries one. The share has moved as clients updated -- it was 12% of openai
 * rows when this was written and is 39% over a recent two-week window on prod,
 * with the earliest months at zero -- so the gap narrows but does not close, and
 * past ranges keep their anonymous rows for good. The rows that
 * do have it name the account, and the account is what the rest share -- so the
 * email is resolved to account ids first, and the anonymous rows are collected
 * by that id.
 */
const accountKeysForEmail = (samples: QuotaSample[], email: string): Set<string> => {
  const target = email.toLowerCase();
  const accountIds = new Set<string>();
  for (const s of samples) {
    if (s.login_email && s.login_email.toLowerCase() === target) accountIds.add(s.account_id);
  }
  const keys = new Set<string>();
  for (const s of samples) {
    if (accountIds.has(s.account_id)) keys.add(`${s.billing_provider}:${s.account_id}`);
  }
  return keys;
};

/**
 * The billing providers an agent scope selects.
 *
 * 'other' selects none, and an empty list is unsatisfiable rather than a
 * wildcard -- the same convention buildWindows already uses. That is the honest
 * answer: the harnesses behind 'other' run on no metered subscription, so there
 * is no quota window to draw for them. The caller is expected to say so rather
 * than render a blank plot.
 */
const providersForAgent = (agent: string): string[] => {
  if (agent === '') return PROVIDERS.map((p) => p.id);
  if (agent === 'claude') return ['anthropic'];
  if (agent === 'codex') return ['openai'];
  return [];
};

/**
 * scopeSamples narrows the samples to the page's header scope before any of the
 * chart's own derivations run.
 *
 * Applying it once, here, is what makes the precedence rule free: the header
 * narrows the universe and the card's chips select within it, because
 * availableWindowsFor/availableProvidersFor and buildWindows all read the
 * narrowed set. No chip state has to be reconciled, and no effect has to sync
 * one to the other.
 */
const scopeSamples = (
  samples: QuotaSample[],
  scope: { accountEmail?: string; agent?: string },
): QuotaSample[] => {
  const { accountEmail = '', agent = '' } = scope;
  if (!accountEmail && !agent) return samples;
  const providers = new Set(providersForAgent(agent));
  const keys = accountEmail ? accountKeysForEmail(samples, accountEmail) : null;
  return samples.filter((s) => (
    providers.has(s.billing_provider)
    && (keys === null || keys.has(`${s.billing_provider}:${s.account_id}`))
  ));
};

/** seriesKey names one account's normalized line within one window. */
const seriesKey = (seriesId: string, windowMinutes: number) => `${seriesId}#${windowMinutes}`;

const windowTypeLabel = (windowMinutes: number) => windowMinutes === WINDOW_5H ? '5h' : '7d';

/** Account and weighted legends share the normalized window type shown by the
 * chart rather than exposing raw provider bucket keys. */
const accountLegendLabel = (accountName: string, provider: string, windowMinutes: number): string => {
  const providerLabel = PROVIDERS.find((candidate) => candidate.id === provider)?.label ?? provider;
  return `${accountName} (${providerLabel}, ${windowTypeLabel(windowMinutes)})`;
};
const weightedLegendLabel = (windowMinutes: number): string => `weighted ${windowTypeLabel(windowMinutes)}`;

/** Weighted keys keep the all-provider and provider-level aggregates distinct. */
const weightedKey = (windowMinutes: number) => `weighted#${windowMinutes}`;

/**
 * mergeRows turns the per-window series into the single row-per-instant shape
 * recharts wants. Every window contributes its own columns, so an instant that
 * only one window reported leaves the other's columns absent rather than zero.
 */
interface MergedUsageRow {
  t: number;
  [key: string]: number | null;
}

const mergeRows = (windows: WindowSeries[]): MergedUsageRow[] => {
  const byInstant = new Map<number, MergedUsageRow>();
  const rowAt = (t: number) => {
    let row = byInstant.get(t);
    if (!row) {
      row = { t, inferredBand: null };
      byInstant.set(t, row);
    }
    return row;
  };

  for (const w of windows) {
    for (const p of w.points) {
      const row = rowAt(p.t);
      row[weightedKey(w.windowMinutes)] = p.weighted;
      // One band for all windows rather than one each: it marks that the
      // account behind this instant was chosen across a gap in the observation
      // log, and that doubt belongs to the instant, not to a window length.
      if (p.inferred) row.inferredBand = 100;
      for (const [seriesId, pct] of Object.entries(p.byAccount)) {
        row[seriesKey(seriesId, w.windowMinutes)] = pct;
      }
    }
  }
  return [...byInstant.values()].sort((a, b) => a.t - b.t);
};

/**
 * Clicking a filter adds or removes it; clicking the sole selected filter
 * restores the full set rather than clearing it.
 *
 * These chips used to isolate on every click, which meant a two-member axis
 * could express one member or all of them but never a deliberate pair, and the
 * only route from one to both was clicking the lone selected chip again -- an
 * undo, not a selection. With two members that reads as "the other one is not
 * selectable alongside this one", which is precisely what these axes are for.
 *
 * The last remaining chip still restores rather than clears, so an ordinary
 * click cannot blank the chart.
 *
 * Order follows the canonical list rather than click order: the selection is a
 * set, and rendering it in the order it was assembled would give the same set
 * two appearances.
 */
const toggleSelection = <T extends string | number>(selected: T[], clicked: T, all: readonly T[]): T[] => {
  if (!selected.includes(clicked)) {
    return all.filter((candidate) => candidate === clicked || selected.includes(candidate));
  }
  if (selected.length === 1) return [...all];
  return all.filter((candidate) => candidate !== clicked && selected.includes(candidate));
};

/**
 * combinationHasData reports whether any drawable sample satisfies both axes at
 * once.
 *
 * Availability is a property of the pair, not of either axis alone. The chips
 * are enabled from per-axis availability, so both can be individually backed by
 * samples while the pair the reader just asked for has none -- and the chart
 * answers that by drawing nothing, which reads as "no usage" rather than "not
 * collected for this pair".
 *
 * An empty axis is unsatisfiable rather than a wildcard, matching buildWindows,
 * where no provider selects no samples.
 */
const combinationHasData = (
  samples: QuotaSample[],
  windows: number[],
  providers: string[],
): boolean => {
  if (windows.length === 0 || providers.length === 0) return false;
  return samples.some((sample) => (
    !isSparkQuotaSample(sample)
    && providers.includes(sample.billing_provider)
    && windows.includes(windowMinutesOf(sample))
  ));
};

export { DASH_7D, PROVIDERS, SERIES_KINDS, accountKeysForEmail, barOpacity, bucketUsagePoints, buildBurnBuckets, mergeBurnRows, timeWeightedMean, accountLegendLabel, availableProvidersFor, availableWindowsFor, buildWindows, combinationHasData, lineDash, mergeRows, seriesKey, providersForAgent, scopeSamples, toggleSelection, weightedKey, weightedLegendLabel };
export type { BurnBucket, SeriesKind };
export type { WindowSeries };
