import type { CoverageGap } from './types';

/**
 * The stack key for the unknown segment. A sentinel rather than the label:
 * modelLabel() could plausibly emit "Unknown" for an unrecognised model and
 * the two would collide in the stack and the colour map.
 */
const UNKNOWN_KEY = '__unknown__';
const UNKNOWN_LABEL = 'Unknown';
/** What the segment is, for the legend entry and the toggle. */
const UNKNOWN_DESCRIPTION =
  'Subscription burn cctrace did not measure, estimated from the gap between the provider\'s usage meter and OTEL/JSONL usage. ' +
  'Account-level: it belongs to no user or model. Mostly usage on machines without cctrace, with fitting error mixed in.';

/** Below this the integer weekly meter has barely moved; ask for the trailing week instead. */
const COVERAGE_MIN_RANGE_MS = 24 * 60 * 60 * 1000;
const COVERAGE_FALLBACK_MS = 7 * 24 * 60 * 60 * 1000;

/**
 * The scope the trend chart is showing, reduced to what the coverage question
 * cares about. Subscription burn is account-level with no session, project,
 * model or profile attribution, so a measured subset has nothing to be compared
 * with. Bucket size is not part of the scope: the ratio is a range total.
 */
interface CoverageGapScope {
  hasProjectFilter: boolean;
  hasUserFilter: boolean;
  hasProfileFilter: boolean;
  hasModelFilter: boolean;
  agent: string;
  modelCategory: string;
}

/** Why this scope cannot be answered, or null when it can. */
const coverageIneligibleReason = (s: CoverageGapScope): string | null => {
  if (s.hasProjectFilter || s.hasUserFilter || s.hasProfileFilter || s.hasModelFilter || s.agent !== '') {
    return 'account-level estimate; clear the project, user, model and agent filters';
  }
  if (s.modelCategory === 'compatible') return 'compatible-provider traffic burns no subscription';
  return null;
};

interface CoverageRange {
  since: string;
  until: string;
  /** True when the chart's own range was too short and the trailing week was asked for instead. */
  trailingWeek: boolean;
}

/**
 * The range to ask coverage for. The chart's range when it is long enough for
 * the integer meter to have moved; otherwise the trailing week ending at the
 * chart's end, so the line is present on the default three-hour view instead
 * of explaining why it is not.
 */
const coverageRangeFor = (since: string, until: string): CoverageRange => {
  const untilMs = Date.parse(until);
  if (untilMs - Date.parse(since) >= COVERAGE_MIN_RANGE_MS) return { since, until, trailingWeek: false };
  return { since: new Date(untilMs - COVERAGE_FALLBACK_MS).toISOString(), until, trailingWeek: true };
};

const coverageGapEligible = (s: CoverageGapScope): boolean => coverageIneligibleReason(s) === null;

const pct = (ratio: number) => `${Math.round(ratio * 100)}%`;

/**
 * One line for the chart header. The ratio is an estimate, and says so.
 *
 * The second clause says "span", not "cover". sample_coverage is the stretch
 * from an account's first reading to its last, divided by the range and averaged
 * over fitted accounts (internal/store/coverage_gap.go:375, :220) -- gaps INSIDE
 * that stretch are not subtracted, so a reading at each end of the range scores
 * 100%. "cover" said the opposite of what is computed: it reads as "we polled the
 * meter across 88% of the period", which sends a reader who sees 2% on a 26-week
 * view hunting for a polling fault instead of the truth, that readings exist only
 * for the last few weeks of it.
 */
const coverageSummary = (gap: CoverageGap, trailingWeek = false): string => {
  if (!gap.eligible) return `Coverage unavailable: ${gap.reason ?? 'unknown'}`;
  const span = trailingWeek ? 'the last 7 days' : 'this range';
  let line = `Measured usage covers ≈${pct(gap.coverage_ratio)} of subscription burn over ${span} · readings span ${pct(gap.sample_coverage)} of the range`;
  if (gap.unfitted.length > 0) {
    line += ` · ${gap.unfitted.length} account${gap.unfitted.length > 1 ? 's' : ''} unfitted`;
  }
  return line;
};

/** One account's fit, as figures rather than a sentence. The panel aligns them
 *  in a column, which a pre-formatted string cannot be made to do. */
interface CoverageAccountRow {
  email: string;
  /** null when the account was not fitted -- there is no ratio to show. */
  ratio: number | null;
  /** Dollars of measured spend per percentage point of the window. */
  kUsdPerPct: number;
  fitIntervals: number;
  unfitted?: string;
}

/**
 * The "why" behind the summary, split by what it answers.
 *
 * `method` is the same on every hover -- it explains the estimator, not this
 * range -- so the panel can demote it. `caveats` are about THIS range and are
 * present only when true. `accounts` are figures, and stay figures: formatting
 * them here produced a flat list of sentences whose numbers did not line up, so
 * the column read as prose.
 */
interface CoverageExplanation {
  method: string[];
  caveats: string[];
  accounts: CoverageAccountRow[];
}

const coverageExplanation = (gap: CoverageGap): CoverageExplanation => {
  const method = [
    `Estimated from the gap between subscription burn (${gap.k_method}) and OTEL/JSONL usage. Account-level; not attributable to a user or model.`,
    'The main cause of a low ratio is usage on machines without cctrace; fitting error is mixed in.',
    'The factor is the spend-weighted average over the fit window, so a blind spot present throughout that window is absorbed into it. The ratio shows how far this range departs from the 28-day average, not an absolute blind spot.',
    'The estimate can be wrong: on the worst stretch measured so far, it overstated the true gap by more than 3x.',
    'An account with no measured usage at all cannot be fitted and is listed as unfitted, not as 0%.',
    'Readings span is the stretch from an account\'s first reading to its last, not the share of time a reading exists for: gaps inside that stretch are not subtracted. A low value means the range reaches back further than the readings do.',
  ];
  const caveats: string[] = [];
  if (gap.coverage_ratio > 1) {
    caveats.push('A ratio above 100% means measured spend exceeded what the meter moved: clock skew between reporters, or burn censored across a window reset.');
  }
  if (gap.censored_fraction > 0) {
    caveats.push(`${pct(gap.censored_fraction)} of the range spans a window reset, where only the post-reset burn is counted.`);
  }
  const accounts = gap.accounts.map((a): CoverageAccountRow => ({
    email: a.login_email,
    ratio: a.unfitted ? null : (a.implied_usd > 0 ? a.measured_usd / a.implied_usd : 0),
    kUsdPerPct: a.k_usd_per_pct,
    fitIntervals: a.fit_intervals,
    unfitted: a.unfitted,
  }));
  return { method, caveats, accounts };
};

/** Per-bucket unknown in the chart's current unit, keyed by the chart's date key. */
const unknownByDate = (gap: CoverageGap | undefined, viewMode: 'cost' | 'token'): Map<string, number> => {
  const out = new Map<string, number>();
  if (!gap?.eligible || !gap.buckets) return out;
  for (const b of gap.buckets) {
    const v = viewMode === 'token' ? b.unknown_tokens : b.unknown_cost_usd;
    if (v > 0) out.set(b.date, v);
  }
  return out;
};

export { UNKNOWN_KEY, UNKNOWN_LABEL, UNKNOWN_DESCRIPTION, coverageGapEligible, coverageIneligibleReason, coverageRangeFor, coverageSummary, coverageExplanation, pct, unknownByDate };
export type { CoverageAccountRow, CoverageExplanation, CoverageGapScope, CoverageRange };
