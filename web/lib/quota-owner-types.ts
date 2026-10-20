import type { FrameAccount, SeriesBand, SeriesPoint, UsageWindowSeries } from './session-analysis-types';

export type QuotaBillingProvider = 'anthropic' | 'openai';
export type QuotaBindingStatus = 'pending' | 'approved' | 'revoked';
export type QuotaWindowState = 'measured' | 'stale' | 'unmeasured';
export type QuotaAttribution = 'observed' | 'inferred' | 'unknown';

export interface QuotaUsageWindow {
  used_pct: number | null;
  resets_at: string | null;
  sampled_at: string | null;
  attribution: QuotaAttribution;
  state: QuotaWindowState;
  source: 'client_reported' | 'unknown';
  reason?: string;
}

export interface QuotaUsageAccount {
  billing_provider: QuotaBillingProvider;
  account_id: string;
  label: string;
  plan: string;
  binding_status: QuotaBindingStatus;
  windows: { five_hour: QuotaUsageWindow; seven_day: QuotaUsageWindow };
}

export interface QuotaFrameOptions {
  selected?: boolean;
  selectable?: boolean;
}

export const quotaAccountSelectionKey = (account: Pick<QuotaUsageAccount, 'billing_provider' | 'account_id'>): string =>
  `${account.billing_provider}:${account.account_id}`;

export function quotaUsageFrameAccount(
  account: QuotaUsageAccount,
  options: QuotaFrameOptions = {},
): FrameAccount {
  return {
    label: account.label,
    provider: account.billing_provider === 'anthropic' ? 'claude' : 'codex',
    five_hour_pct: account.windows.five_hour.used_pct,
    seven_day_pct: account.windows.seven_day.used_pct,
    resets_at: { five_hour: account.windows.five_hour.resets_at, seven_day: account.windows.seven_day.resets_at },
    selected: options.selected ?? false,
    selectionKey: quotaAccountSelectionKey(account),
    selectable: options.selectable ?? false,
    five_hour_state: account.windows.five_hour.state,
    seven_day_state: account.windows.seven_day.state,
    five_hour_reason: account.windows.five_hour.reason,
    seven_day_reason: account.windows.seven_day.reason,
    attribution: account.windows.five_hour.attribution === 'unknown' ? account.windows.seven_day.attribution : account.windows.five_hour.attribution,
    source: account.windows.five_hour.source === 'client_reported' || account.windows.seven_day.source === 'client_reported' ? 'client_reported' : 'unknown',
  };
}

export interface MyQuotaUsageResponse {
  from: string;
  to: string;
  accounts: QuotaUsageAccount[];
}

export interface QuotaAccountBinding {
  id: number;
  billing_provider: QuotaBillingProvider;
  account_id: string;
  status: QuotaBindingStatus;
  requested_at: string;
  verified_at?: string;
}

export interface AdminQuotaAccountBinding extends QuotaAccountBinding {
  dashboard_user_id: number;
  owner_email: string;
  approved_by?: number;
  verification_method?: string;
  verification_reference?: string;
}

export interface QuotaComparisonWindow {
  window_start: string;
  resets_at: string;
  window_key: string;
  points: { elapsed_fraction: number; used_pct: number; sampled_at: string; attribution: Exclude<QuotaAttribution, 'unknown'> }[];
}

export interface QuotaQuantilePoint {
  elapsed_fraction: number;
  p25: number | null;
  p50: number | null;
  p75: number | null;
  n: number;
}

export interface QuotaWindowComparison {
  billing_provider: QuotaBillingProvider;
  account_id: string;
  window_minutes: 300 | 10080;
  current: QuotaComparisonWindow | null;
  history: QuotaComparisonWindow[];
  quantiles: QuotaQuantilePoint[];
  history_count: number;
  truncated: boolean;
  source: 'client_reported';
  reason?: string;
}

const asSeriesPoints = (points: QuotaComparisonWindow['points']): SeriesPoint[] =>
  points.map((point) => ({ x: point.elapsed_fraction, y: point.used_pct }));

export function quotaComparisonSeries(value: QuotaWindowComparison, window: 'five_hour' | 'seven_day'): UsageWindowSeries {
  const median: SeriesPoint[] = [];
  const band: SeriesBand[] = [];
  const sampleCounts: { x: number; n: number }[] = [];
  for (const point of value.quantiles) {
    sampleCounts.push({ x: point.elapsed_fraction, n: point.n });
    if (point.p50 !== null) median.push({ x: point.elapsed_fraction, y: point.p50 });
    if (point.p25 !== null && point.p75 !== null) {
      band.push({ x: point.elapsed_fraction, low: point.p25, high: point.p75 });
    }
  }
  return {
    window,
    current: value.current ? asSeriesPoints(value.current.points) : [],
    past: value.history.map((item) => asSeriesPoints(item.points)),
    median,
    band,
    sampleCounts,
    historyCount: value.history_count,
    truncated: value.truncated,
    source: value.source,
    reason: value.reason,
  };
}
