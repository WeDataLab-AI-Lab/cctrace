'use client';

import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Line,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogOverlay, DialogTitle } from '@/components/ui/dialog';
import { fetchQuotaSamples } from '@/lib/api';
import { POLL_SLOW } from '@/lib/query-config';
import { cn } from '@/lib/utils';
import { userColor } from '@/lib/colors';
import {
  PROVIDERS,
  WINDOW_5H,
  WINDOW_7D,
  accountLegendLabel,
  availableProvidersFor,
  availableWindowsFor,
  buildWindows,
  combinationHasData,
  lineDash,
  coverageOf,
  mergeRows,
  remainingUSD,
  SERIES_KINDS,
  barOpacity,
  buildBurnBuckets,
  mergeBurnRows,
  scopeSamples,
  seriesKey,
  toggleSelection,
  weightedKey,
  weightedLegendLabel,
} from '@/lib/quota-usage';
import type { SeriesKind, WindowSeries } from '@/lib/quota-usage';
import type { QuotaSample } from '@/lib/types';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { ChartSkeleton } from '@/components/common/chart-skeleton';
import { formatTrendTick, trendTickInterval } from '@/lib/trend-axis';
import type { TrendGranularity } from '@/lib/trend-axis';

/**
 * The subscription-burn chart.
 *
 * The axis is inverted, so the height of the line at any instant is the money
 * still unspent — and the height just before a window resets is what was paid
 * for and thrown away. Two things follow from that inversion:
 *
 *  - Exhaustion becomes visible. In the ordinary orientation 100% is just a
 *    high value among high values; inverted, the floor is a physical limit and
 *    a line pinned to it reads immediately.
 *  - The domain is fixed at [0, 100]. Autoscaling the way the token axis does
 *    would break the one thing the height is supposed to mean.
 */

const RANGE_DAYS = 7;

interface UsageChartProps {
  height?: number;
  /** Tailwind class matching `height`; the empty state cannot take a number
   *  because inline styles are not allowed (docs/guides/FRONT-RULE.md). */
  heightClass?: string;
  /** Held false until persisted scopes hydrate, matching the trend chart. */
  queriesEnabled?: boolean;
  /**
   * The range the trend chart above is showing, so the two axes line up.
   *
   * They are meant to be read together — usage falls from the ceiling while
   * tokens grow from the floor — and that only works if both cover the same
   * span. Owning a private range here made the pair silently incomparable.
   */
  since?: string;
  until?: string;
  /** Active Cost Trend granularity, used for identical x-axis labels and spacing. */
  granularity?: TrendGranularity;
  /**
   * The page's header scope. Applied once to the samples, so every derivation
   * below -- available chips included -- reads the narrowed set and the chips
   * select within it without any state to reconcile.
   */
  accountEmail?: string;
  agent?: string;
}

const WINDOWS = [
  { minutes: WINDOW_5H, label: '5h' },
  { minutes: WINDOW_7D, label: '7d' },
] as const;

// Stroke widths. Both were a step thinner, which read as faint against the grid
// once the chart opened by default at full width: the account lines are the
// content here -- the weighted average is opt-in -- and at 1px they were the
// lightest marks on the plot. The ratio between them still carries the
// weighted/account distinction, so both move together.
// One width for the weighted line in both registers; colour alone tells them
// apart. Width was carrying the distinction too, and stacking the two channels
// made the line heavy enough to dominate the plot in either state -- which is
// wrong beside the account lines (it is an overlay there) and unnecessary alone
// (nothing is competing with it).
//
// The theme's --accent is #2B313B, so `text-brand` reads as near-black; the ink
// ramp is what actually has steps to spend. Beside the accounts the weighted
// line drops to ink-4, the faintest step, because the accounts are what the
// chart is opened for. Alone it takes full ink, which at this width reads as
// present rather than as the darkest mark on the page.
const WEIGHTED_STROKE = 2;
const ACCOUNT_STROKE = 1.75;

// Step corners are rounded, not smoothed. type="stepAfter" is the data model,
// not a rendering choice: a rate-limit window holds its value between readings
// and only moves when work is done (see valueAt in lib/quota-usage), so a curve
// through those points would draw consumption that never happened and turn a
// reset's vertical drop into a gradual recovery. Rounding the joins softens the
// corners without moving a single plotted value.
//
// The radius a round join produces is half the stroke width, so the effect is
// deliberately small on the thin account lines and most visible on the weighted
// ones -- which is the same order the widths already state.
const STEP_JOIN = { strokeLinejoin: 'round', strokeLinecap: 'round' } as const;

const usd = (n: number) => `$${n.toFixed(0)}`;

/** Recharts hands the formatter `ValueType | undefined`, so the null case has
 *  to be handled here rather than assumed away. */
const formatTooltipValue = (v: unknown, name: unknown): [string, string] => {
  const label = String(name ?? '');
  return typeof v === 'number' ? [`${v.toFixed(1)}%`, label] : ['—', label];
};

const timeLabel = (t: number) =>
  new Date(t).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });

const formatTooltipLabel = (t: unknown) => timeLabel(Number(t));

/** Bucket mode plots averages, so the tooltip says so -- the same number without
 *  the qualifier reads as an instantaneous reading like the line mode's. */
const formatBucketTooltipValue = (v: unknown, name: unknown): [string, string] => {
  const label = String(name ?? '');
  return typeof v === 'number' ? [`${v.toFixed(1)}% avg`, label] : ['\u2014', label];
};
const formatBucketTooltipLabel = (key: unknown) => String(key ?? '');
const formatPercentTick = (v: number) => `${v}%`;

const UsageChart = ({
  height = 250,
  heightClass = 'h-[250px]',
  queriesEnabled = true,
  since,
  until,
  granularity = 'minute',
  accountEmail = '',
  agent = '',
}: UsageChartProps) => {
  // Open by default. It was collapsed while the feature settled, because a
  // window a provider does not publish drew an empty plot that reads as "nothing
  // was spent" rather than "not collected" -- Codex's general meter carries a
  // five-hour window only on some plans (Plus since 2026-08-25, not Pro since
  // 2026-07-12), and Claude's own five-hour history only began on
  // 2026-08-26. The chart now refuses such a pair and says which it is, so the
  // misreading the collapse was hedging against no longer has a way to happen.
  const [expanded, setExpanded] = useState(true);

  // A chip isolates its window/provider; clicking the sole selected chip
  // restores the full set. This keeps comparisons one click away without an
  // ordinary click ever turning the chart completely empty.
  const [windows, setWindows] = useState<number[]>([WINDOW_5H, WINDOW_7D]);
  const [providers, setProviders] = useState<string[]>(PROVIDERS.map((p) => p.id));

  // The cross-provider average is off until asked for. It is the one line that
  // belongs to no account: it answers "how much of the whole subscription bill
  // is spent", which is a different question from the one the chart is usually
  // opened with -- whose quota is running out -- and drawing it by default put
  // the thickest stroke on screen in front of the lines that answer that.
  const [series, setSeries] = useState<SeriesKind[]>(['accounts']);

  // The pair a click asked for and was refused, held only to name it in the
  // notice. Null whenever nothing has been refused.
  const [blocked, setBlocked] = useState<{ windows: number[]; providers: string[] } | null>(null);

  // The fallback is pinned at mount rather than recomputed per render: read
  // during render it would differ every time, changing the query key and
  // turning the five-minute poll into a refetch on every render.
  const [mountedAt] = useState(() => Date.now());
  const [defaultFrom] = useState(() =>
    new Date(Date.now() - RANGE_DAYS * 24 * 60 * 60 * 1000).toISOString(),
  );
  const from = since || defaultFrom;

  // The x axis spans the range that was ASKED FOR, not the range the data
  // happens to occupy. Those differ whenever a window is sparse — the 5h series
  // here starts twelve days into a 30-day query — and letting the data set the
  // domain stretches that fragment across the full width. The chart above then
  // puts the same instant at a different x, which defeats reading the pair
  // together: usage falling from the ceiling is supposed to line up with tokens
  // growing from the floor.
  // The open end is pinned at mount for the same reason `from` is: read during
  // render it would move every time, and an axis that slides under the reader on
  // each poll is the state-preservation rule the polling guidance protects.
  const xDomain: [number, number] = [Date.parse(from), until ? Date.parse(until) : mountedAt];

  const { data: samples = [], isPending: samplesPending } = useQuery<QuotaSample[]>({
    // Both windows are fetched together, not one at a time. Which of them holds
    // anything is a property of the data, not of the reader's last click: a
    // Codex Pro account reports only the weekly window, a Codex Plus account and
    // Claude report both, so a range can be full for one and empty for the
    // other. Asking for both is what lets the default land on a window that has
    // something in it, and it is why combinationHasData reads availability off
    // these samples — Spark exclusion included — rather than naming providers
    // and windows in code: what a provider publishes is not fixed, and a table
    // of pairs in the source would go stale without anything failing.
    queryKey: ['quota-samples', from, until],
    queryFn: () => fetchQuotaSamples({ from, to: until }),
    // POLL_SLOW because the source poll is itself five-minutely; anything
    // faster would re-ask for readings that cannot have changed.
    refetchInterval: POLL_SLOW,
    enabled: queriesEnabled && expanded,
  });


  // Applied once, before anything derives from the samples. Everything below --
  // the available chips included -- then reads the narrowed set, which is what
  // makes the precedence rule (header narrows, chips select within) fall out
  // without a single line of reconciliation.
  const scoped = scopeSamples(samples, { accountEmail, agent });
  const showAccounts = series.includes('accounts');
  const showWeighted = series.includes('weighted');
  const availableWindows = availableWindowsFor(scoped);
  const availableProviders = availableProvidersFor(scoped);
  const selectedWindows = windows.filter((window) => availableWindows.includes(window));
  const selectedProviders = providers.filter((provider) => availableProviders.includes(provider));
  const windowSeries = buildWindows(scoped, selectedWindows, selectedProviders);
  // Week and month buckets hold dozens of window resets each, so the step line
  // collapses into vertical hatching that cannot be read -- the plot is there but
  // says nothing. At those granularities the same series is drawn as one bar per
  // bucket carrying its time-weighted average instead.
  const isBucketMode = granularity === 'week' || granularity === 'month';
  const burnBuckets = isBucketMode
    ? buildBurnBuckets(scoped, selectedWindows, selectedProviders, granularity, from, until ?? new Date(mountedAt).toISOString())
    : [];
  const chartData = isBucketMode ? mergeBurnRows(burnBuckets) : mergeRows(windowSeries);
  const xTickInterval = trendTickInterval(granularity, chartData.length);
  const formatXAxisTick = (value: number) => formatTrendTick(granularity, value);

  // Coverage is reported across everything currently drawn: an account counts
  // once even when both of its windows are on.
  const drawnAccounts = [...new Map(
    windowSeries.flatMap((w) => w.accounts).map((a) => [a.accountId, a]),
  ).values()];
  const coverage = coverageOf(
    drawnAccounts,
    drawnAccounts.map((a) => ({ accountId: a.accountId, label: a.label, priceUSD: a.priceUSD })),
  );

  // Keep one colour per billing account across both normalized windows.
  const colorIndex = new Map(drawnAccounts.map((a, i) => [a.accountId, i]));

  // The headline reads from the shortest window on screen — the one that says
  // what is being spent right now rather than over a week.
  const headlineWindow = windowSeries
    .filter((w) => w.points.length > 0)
    .sort((a, b) => a.windowMinutes - b.windowMinutes)[0];
  const points = headlineWindow?.points ?? [];

  const latest = points.length > 0 ? points[points.length - 1] : null;

  // A chip whose pair holds nothing is refused rather than applied, and the
  // refusal says which pair and why. Applying it would draw an empty chart,
  // which reads as "nothing was used" when the truth is "this pair was never
  // collected" -- and the reader cannot tell those apart from an empty plot.
  const applySelection = (
    nextWindows: number[],
    nextProviders: string[],
    apply: () => void,
  ) => {
    if (combinationHasData(scoped, nextWindows, nextProviders)) {
      apply();
      return;
    }
    setBlocked({ windows: nextWindows, providers: nextProviders });
  };

  const handleToggleWindow = (minutes: number) => () => {
    const next = toggleSelection(selectedWindows, minutes, availableWindows);
    applySelection(next, selectedProviders, () => setWindows(next));
  };
  const handleToggleProvider = (id: string) => () => {
    const next = toggleSelection(selectedProviders, id, availableProviders);
    applySelection(selectedWindows, next, () => setProviders(next));
  };

  // The same isolate-and-restore rule the window and provider chips use, so all
  // four chip groups behave identically. Deliberately not routed through
  // applySelection: that guard is about pairs that were never collected, and both
  // series are always derivable from whatever is drawn.
  const handleToggleSeries = (kind: SeriesKind) => () => setSeries(toggleSelection(series, kind, SERIES_KINDS));

  const handleCloseBlocked = () => setBlocked(null);

  const handleToggleExpanded = () => setExpanded((v) => !v);

  // Collapsed: a single muted control and nothing else. No query has run, so
  // there is no data to summarise and nothing to say about coverage.
  if (!expanded) {
    return (
      <button
        type="button"
        onClick={handleToggleExpanded}
        className={cn(
          'flex w-full items-center gap-2 rounded-lg border border-border bg-surface px-4 py-2.5',
          'text-left text-sm font-medium text-muted-foreground transition-colors hover:text-fg',
        )}
      >
        <ChevronRight size={14} aria-hidden />
        Subscription burn
        <span className="text-xs font-normal text-muted-foreground">preview</span>
      </button>
    );
  }

  // isPending, not isFetching: isFetching is true on every poll, so gating the
  // loader on it covers the chart with a spinner every five minutes even though
  // the data on screen is fine.
  if (samplesPending) {
    return (
      <section className="rounded-lg border border-border bg-surface p-6">
        <ChartSkeleton variant="trend" />
      </section>
    );
  }

  return (
    <section className="rounded-lg border border-border bg-surface p-6">
      <header className="flex items-center justify-between gap-4 mb-3">
        <div className="flex items-baseline gap-3">
          <button
            type="button"
            onClick={handleToggleExpanded}
            className="flex items-center gap-2 text-sm font-medium text-fg transition-colors hover:text-muted-foreground"
          >
            <ChevronDown size={14} aria-hidden />
            Subscription burn
          </button>
          {latest?.weighted != null && headlineWindow && (
            // The window is named because the number means nothing without it:
            // 12% of a five-hour budget and 12% of a weekly one are different
            // facts, and the reader cannot tell them apart from the figure.
            <p className="text-xs text-muted-foreground">
              <span className="text-fg">{headlineWindow.windowMinutes === WINDOW_5H ? '5h' : '7d'}</span>{' '}
              {/* The headline is the latest INSTANT. In bucket mode the plot beside
                  it is averages, so without the qualifier the two numbers look like
                  the same measurement disagreeing. */}
              {latest.weighted.toFixed(1)}% consumed{isBucketMode ? ' now' : ''} ·{' '}
              <span className="text-fg">{usd(remainingUSD(latest.weighted, coverage.measuredUSD))}</span> of{' '}
              {usd(coverage.measuredUSD)} left
            </p>
          )}
        </div>

        <UsageControls
          availableWindows={availableWindows}
          availableProviders={availableProviders}
          windows={selectedWindows}
          providers={selectedProviders}
          onToggleWindow={handleToggleWindow}
          onToggleProvider={handleToggleProvider}
          series={series}
          onToggleSeries={handleToggleSeries}
          soloWeightedAvailable={drawnAccounts.filter((a) => a.priceUSD > 0).length > 1}
        />
      </header>

      {blocked && <UncollectedPairNotice pair={blocked} onClose={handleCloseBlocked} />}

      {chartData.length === 0 ? (
        <EmptyState
          noWindows={selectedWindows.length === 0}
          noProviders={selectedProviders.length === 0}
          unmeteredAgent={agent === 'other' || agent === 'weekly'}
          scopedAccount={accountEmail !== '' && scoped.length === 0}
          heightClass={heightClass}
        />
      ) : (
        <div>
          <ResponsiveContainer width="100%" height={height}>
            {isBucketMode ? (
            <BarChart data={chartData} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
              <CartesianGrid strokeDasharray="3 3" className="stroke-border" vertical={false} />
              <XAxis
                dataKey="date"
                tick={{ fontSize: 10 }}
                tickFormatter={formatXAxisTick}
                interval={xTickInterval}
              />
              {/* Still reversed. The axis direction is a property of the card, not
                  of the granularity: a y axis that flips when a chip elsewhere on
                  the page changes is a mode nobody can see until it has already
                  misled them. The caption below carries the reading. */}
              <YAxis reversed domain={[0, 100]} tick={{ fontSize: 10 }} tickFormatter={formatPercentTick} />
              <Tooltip labelFormatter={formatBucketTooltipLabel} formatter={formatBucketTooltipValue} />

              {/* No stackId. These are percentages of different denominators --
                  each account's own quota -- and stacking them would build a 300%
                  column out of three accounts that were each half spent. */}
              {(showWeighted ? burnBuckets : []).map((w) => (
                <Bar
                  key={weightedKey(w.windowMinutes)}
                  dataKey={weightedKey(w.windowMinutes)}
                  name={weightedLegendLabel(w.windowMinutes)}
                  fill="currentColor"
                  className={cn(showAccounts ? 'text-ink-4' : 'text-ink')}
                  fillOpacity={barOpacity(w.windowMinutes, burnBuckets.length)}
                  isAnimationActive={false}
                />
              ))}
              {(showAccounts ? burnBuckets : []).flatMap((w) =>
                w.accounts.map((a) => (
                  <Bar
                    key={seriesKey(a.seriesId, w.windowMinutes)}
                    dataKey={seriesKey(a.seriesId, w.windowMinutes)}
                    name={accountLegendLabel(a.label, a.provider, w.windowMinutes)}
                    fill={userColor(colorIndex.get(a.accountId) ?? 0)}
                    fillOpacity={barOpacity(w.windowMinutes, burnBuckets.length)}
                    isAnimationActive={false}
                  />
                )),
              )}
            </BarChart>
            ) : (
            <AreaChart data={chartData} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
            <CartesianGrid strokeDasharray="3 3" className="stroke-border" vertical={false} />
            <XAxis
              dataKey="t"
              type="number"
              domain={xDomain}
              scale="time"
              tick={{ fontSize: 10 }}
              tickFormatter={formatXAxisTick}
              interval={xTickInterval}
            />
            {/* reversed + a fixed [0,100] domain: the floor is the limit, so the
                height of the line is the money still unspent. */}
            <YAxis reversed domain={[0, 100]} tick={{ fontSize: 10 }} tickFormatter={formatPercentTick} />
            <Tooltip labelFormatter={formatTooltipLabel} formatter={formatTooltipValue} />

            {/* A band behind the instants whose account was chosen across a gap
                in the observation log. Present is not the same as certain. */}
            <Area
              dataKey="inferredBand"
              name="account inferred"
              stroke="none"
              fill="currentColor"
              className="text-muted-foreground"
              fillOpacity={0.08}
              isAnimationActive={false}
              connectNulls={false}
              legendType="none"
            />

            {(showWeighted ? windowSeries : []).map((w) => (
              <Line
                key={weightedKey(w.windowMinutes)}
                dataKey={weightedKey(w.windowMinutes)}
                name={weightedLegendLabel(w.windowMinutes)}
                type="stepAfter"
                stroke="currentColor"
                className={cn(showAccounts ? 'text-ink-4' : 'text-ink')}
                strokeWidth={WEIGHTED_STROKE}
                strokeDasharray={lineDash(w.windowMinutes, windowSeries.length)}
                {...STEP_JOIN}
                dot={false}
                isAnimationActive={false}
                // Same reason as the account lines below: the weighted average is
                // only defined where something was measured.
                connectNulls={false}
              />
            ))}

            {/* One line per account per window, and nothing above them but the
                optional cross-provider average. A per-harness weighted line used
                to sit here too; it answered a question nobody was asking of this
                chart -- accounts are already grouped by harness in the legend,
                and the roll-up only added a stroke between the reader and the
                lines they came for. */}
            {(showAccounts ? windowSeries : []).flatMap((w) =>
              w.accounts.map((a) => (
                <Line
                  key={seriesKey(a.seriesId, w.windowMinutes)}
                  dataKey={seriesKey(a.seriesId, w.windowMinutes)}
                  name={accountLegendLabel(a.label, a.provider, w.windowMinutes)}
                  type="stepAfter"
                  stroke={userColor(colorIndex.get(a.accountId) ?? 0)}
                  strokeWidth={ACCOUNT_STROKE}
                  strokeDasharray={lineDash(w.windowMinutes, windowSeries.length)}
                  {...STEP_JOIN}
                  dot={false}
                  isAnimationActive={false}
                  // A gap here is an account that stopped reporting, not a dip in
                  // its quota. Bridging it drew the silence as a held value (#678);
                  // valueAt stops carrying a reading past STALE_AFTER_MS and this
                  // lets the break through instead of joining across it.
                  connectNulls={false}
                />
              )),
            )}
            </AreaChart>
            )}
          </ResponsiveContainer>
          {isBucketMode && (
            // The bar grows downward from 0%, which is the card's rule but the
            // opposite of the Cost Trend bars directly above. Say which way to
            // read it rather than leaving the reader to infer it from the axis.
            <p className="mt-1 text-center text-[10px] text-muted-foreground">
              Average quota consumed per bucket &middot; longer bars burned more
            </p>
          )}
          <UsageLegend windowSeries={windowSeries} colorIndex={colorIndex} showWeighted={showWeighted} showAccounts={showAccounts} />
        </div>
      )}

      <CoverageNote coverage={coverage} />
    </section>
  );
};

/** The chart library's default legend is one flex-like stream. This renderer
 * keeps the visual keys in accessible provider columns without adding another
 * layer of cards around the chart, while retaining the exact series names used
 * by the chart and tooltip. */
const UsageLegend = ({
  windowSeries,
  colorIndex,
  showWeighted = true,
  showAccounts = true,
}: {
  windowSeries: WindowSeries[];
  colorIndex: ReadonlyMap<string, number>;
  /** Whether the cross-provider weighted line is drawn. Its column goes with it:
   *  a key for a line that is not on the chart sends the reader hunting. */
  showWeighted?: boolean;
  /** Same contract for the account columns. */
  showAccounts?: boolean;
}) => {
  const providerGroups = PROVIDERS.map((provider) => {
    const accountIds = [...new Set(
      windowSeries.flatMap((window) => window.accounts)
        .filter((account) => account.provider === provider.id)
        .map((account) => account.accountId),
    )];
    const entries = accountIds.flatMap((accountId) =>
      windowSeries.flatMap((window) => {
        const account = window.accounts.find((candidate) => candidate.accountId === accountId);
        return account ? [{ account, windowMinutes: window.windowMinutes }] : [];
      }),
    );
    return { ...provider, entries };
  }).filter((group) => showAccounts && group.entries.length > 0);

  return (
    <section aria-label="Subscription burn legend" className="mt-3">
      <div className="mx-auto w-fit max-w-full grid grid-cols-1 gap-y-2 sm:grid-cols-3 sm:gap-x-6">
        {showWeighted && (
        <div role="group" aria-label="Weighted">
          <ul className="space-y-1">
            {windowSeries.map((window) => (
              <li key={weightedKey(window.windowMinutes)} className="flex min-w-0 items-center gap-2 text-[11px] text-ink-2">
                <LegendLine weighted solo={!showAccounts} windowMinutes={window.windowMinutes} drawnWindowCount={windowSeries.length} />
                <span className="break-words">{weightedLegendLabel(window.windowMinutes)}</span>
              </li>
            ))}
          </ul>
        </div>
        )}

        {providerGroups.map((group) => (
          <div key={group.id} role="group" aria-label={group.label}>
            <ul className="space-y-1">
              {group.entries.map(({ account, windowMinutes }) => (
                <li
                  key={seriesKey(account.seriesId, windowMinutes)}
                  className="flex min-w-0 items-center gap-2 text-[11px] text-ink-2"
                >
                  <LegendLine
                    color={userColor(colorIndex.get(account.accountId) ?? 0)}
                    windowMinutes={windowMinutes}
                    drawnWindowCount={windowSeries.length}
                  />
                  <span className="break-words">
                    {accountLegendLabel(account.label, account.provider, windowMinutes)}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
    </section>
  );
};

const LegendLine = ({
  color,
  weighted = false,
  solo = false,
  windowMinutes,
  drawnWindowCount,
}: {
  color?: string;
  weighted?: boolean;
  /** The weighted line is alone on the plot, so the key takes its darker ink.
   *  A key that does not match the stroke is a key that lies. */
  solo?: boolean;
  windowMinutes: number;
  /** How many windows the chart is drawing, so the key matches the stroke. */
  drawnWindowCount: number;
}) => (
  <svg aria-hidden="true" focusable="false" className={cn('h-2 w-4 shrink-0', weighted && (solo ? 'text-ink' : 'text-ink-4'))} viewBox="0 0 16 8">
    <line
      x1="0"
      y1="4"
      x2="16"
      y2="4"
      stroke={color ?? 'currentColor'}
      strokeWidth={weighted ? WEIGHTED_STROKE : ACCOUNT_STROKE}
      strokeDasharray={lineDash(windowMinutes, drawnWindowCount)}
    />
  </svg>
);

/**
 * Three different things look like zero on this chart, and saying which is
 * which is not decoration.
 *
 *  - structurally no subscription (Bedrock, metered API keys) — nothing to draw
 *  - subscribed but not collected yet — we are not looking
 *  - collected and genuinely 0% — a window that just reset
 *
 * Showing the second as the first restates "we are not watching" as "there is
 * nothing to see". That has already happened once here: 264 sessions and 37.6M
 * tokens of openai-codex usage sat uncollected because a design assumption was
 * wrong, and nothing on screen said so.
 */
const CoverageNote = ({ coverage }: { coverage: ReturnType<typeof coverageOf> }) => {
  // Nothing was read in this window at all. Saying the accounts are unpriced
  // here would name the wrong problem and send the reader to the price table to
  // fix something that is not broken — the same "wrong but plausible" failure
  // this chart exists to remove. The empty plot already says there is no data;
  // the note stays silent rather than inventing a reason for it.
  if (coverage.measuredAccounts === 0 && coverage.unmeasured.length === 0) return null;

  if (coverage.totalUSD === 0) {
    return (
      <p className="mt-2 text-xs text-muted-foreground">
        No account here is on a priced plan, so there is nothing to weight against. Plans and their prices
        live in <code className="text-fg">web/lib/plan-prices.ts</code>; an unlisted plan is drawn but left out
        of the average.
      </p>
    );
  }

  if (coverage.unmeasured.length === 0) {
    return (
      <p className="mt-2 text-xs text-muted-foreground">
        Covering {usd(coverage.measuredUSD)}/mo across {coverage.measuredAccounts} accounts.
      </p>
    );
  }

  return (
    <p className="mt-2 text-xs text-muted-foreground">
      Covering <span className="text-fg">{usd(coverage.measuredUSD)}</span> of {usd(coverage.totalUSD)}/mo.{' '}
      {coverage.unmeasured.length} account{coverage.unmeasured.length > 1 ? 's are' : ' is'} not being measured and{' '}
      {coverage.unmeasured.length > 1 ? 'are' : 'is'} excluded from the average rather than counted as idle:{' '}
      {coverage.unmeasured.map((u) => u.label).join(', ')}.
    </p>
  );
};

/**
 * An empty plot with no explanation reads as "there is nothing", when the usual
 * cause is "you are looking at the other window". The 5h and 7d series are
 * collected independently — a machine that ran Codex weeks ago has weekly
 * readings inside the range and five-hour ones long expired — so the window in
 * hand is named, and the other one is offered.
 */
const EmptyState = ({ noWindows, noProviders, unmeteredAgent, scopedAccount, heightClass }: EmptyStateProps) => {
  const reason = unmeteredAgent
    ? 'This scope runs on no metered subscription tracked here, so there is no quota window to draw.'
    : scopedAccount
      ? 'No quota readings for this account in range.'
      : noWindows
        ? 'No window selected.'
        : noProviders
          ? 'No provider selected.'
          : 'No readings in this range.';
  return (
    <p className={cn('flex items-center justify-center text-xs text-muted-foreground', heightClass)}>{reason}</p>
  );
};

interface EmptyStateProps {
  noWindows: boolean;
  noProviders: boolean;
  /** The header narrowed to a harness with no metered subscription. Saying so
   *  beats a blank plot, which reads as "nothing was spent". */
  unmeteredAgent: boolean;
  /** The header named an account that resolves to no readings here. Deliberately
   *  not a fallback to every account: other people's quota under a named pill is
   *  worse than an empty state. */
  scopedAccount: boolean;
  /** Tailwind height for the plot this stands in for, so the section does not
   *  jump when the chart has nothing to draw. */
  heightClass: string;
}

/** Only filters backed by drawable samples are controls. Raw Spark window
 * metadata cannot create a selected chip for a series that does not exist.
 *
 * The arrays name what is included in the chart; the pressed state names an
 * explicit focus. When every option is included the chart is in its unfiltered
 * show-all state, so no chip is selected. */
/**
 * UncollectedPairNotice explains a refused chip click.
 *
 * The chips are enabled per axis, so each can be individually backed by samples
 * while the pair has none. Letting such a click through draws an empty plot,
 * and an empty plot is read as "nothing was used" -- the opposite of the truth,
 * which is that nothing was ever collected for this pair. Refusing it and
 * saying so keeps the reader from concluding the wrong thing from a blank.
 */
const UncollectedPairNotice = ({
  pair,
  onClose,
}: {
  pair: { windows: number[]; providers: string[] };
  onClose: () => void;
}) => {
  const handleOpenChange = (open: boolean) => {
    if (!open) onClose();
  };

  const windowLabel = pair.windows
    .map((minutes) => WINDOWS.find((window) => window.minutes === minutes)?.label ?? String(minutes))
    .join(' + ');
  const providerLabel = pair.providers
    .map((id) => PROVIDERS.find((provider) => provider.id === id)?.label ?? id)
    .join(' + ');

  return (
    <Dialog open onOpenChange={handleOpenChange}>
      <DialogOverlay className="bg-black/30" />
      <DialogContent
        showCloseButton={false}
        className="block w-[360px] max-w-[360px] gap-0 space-y-3 rounded-xl border border-border bg-surface p-5 shadow-2xl"
      >
        <DialogTitle className="text-[14px] font-semibold text-fg">Not collected</DialogTitle>
        <p className="text-[13px] text-muted-foreground">
          <span className="font-medium text-fg">{providerLabel}</span> has no{' '}
          <span className="font-medium text-fg">{windowLabel}</span> readings in this range, so the
          chart would be empty rather than flat. The selection was left as it was.
        </p>
        <Button
          onClick={onClose}
          className="h-auto w-full rounded-lg bg-brand py-2 text-sm font-semibold text-brand-ink hover:bg-brand-hover"
        >
          Got it
        </Button>
      </DialogContent>
    </Dialog>
  );
};

const UsageControls = ({
  availableWindows,
  availableProviders,
  windows,
  providers,
  onToggleWindow,
  onToggleProvider,
  series,
  onToggleSeries,
  soloWeightedAvailable,
}: {
  availableWindows: number[];
  availableProviders: string[];
  windows: number[];
  providers: string[];
  onToggleWindow: (minutes: number) => () => void;
  onToggleProvider: (id: string) => () => void;
  series: SeriesKind[];
  onToggleSeries: (kind: SeriesKind) => () => void;
  /** False once the scope leaves a single priced account. The price-weighted mean
   *  of one series IS that series, so the weighted line would land on top of the
   *  account line in a heavier stroke and read as two measures agreeing. */
  soloWeightedAvailable: boolean;
}) => {
  const windowOptions = WINDOWS.filter((window) => availableWindows.includes(window.minutes));
  const providerOptions = PROVIDERS.filter((provider) => availableProviders.includes(provider.id));

  // Every selected chip is drawn selected. While a click could only isolate,
  // "all" and "one" were the sole reachable states and the all state was drawn
  // with nothing lit; now that a pair is reachable, a chip that is on has to
  // look on or the control cannot show which pair is in force.

  return (
    <div className="flex items-center gap-1.5">
      <div role="group" aria-label="Quota window" className="flex items-center gap-1.5">
        {windowOptions.map((window) => (
          <ToggleChip
            key={window.minutes}
            selected={windows.includes(window.minutes)}
            onClick={onToggleWindow(window.minutes)}
            label={window.label}
          />
        ))}
      </div>
      {windowOptions.length > 0 && providerOptions.length > 0 && (
        <span className="mx-1 h-4 w-px bg-border" aria-hidden />
      )}
      <div role="group" aria-label="Billing provider" className="flex items-center gap-1.5">
        {providerOptions.map((provider) => (
          <ToggleChip
            key={provider.id}
            selected={providers.includes(provider.id)}
            onClick={onToggleProvider(provider.id)}
            label={provider.label}
          />
        ))}
      </div>
      {/* Its own group: this one is not a filter over the data but a choice about
          which series to draw, so it does not belong in either axis above. Two
          chips rather than one cycling chip, so the state is legible from the
          chips themselves and any state is one click away. */}
      {soloWeightedAvailable && (
        <>
          <span className="mx-1 h-4 w-px bg-border" aria-hidden />
          <div role="group" aria-label="Series" className="flex items-center gap-1.5">
            <ToggleChip selected={series.includes('accounts')} onClick={onToggleSeries('accounts')} label="Accounts" />
            <ToggleChip selected={series.includes('weighted')} onClick={onToggleSeries('weighted')} label="Weighted" />
          </div>
        </>
      )}
    </div>
  );
};

/** ToggleChip exposes the chart's explicit focus, not mere show-all inclusion. */
const ToggleChip = ({ selected, onClick, label }: { selected: boolean; onClick: () => void; label: string }) => (
  <button
    type="button"
    onClick={onClick}
    aria-pressed={selected}
    className={cn(
      'rounded border px-2 py-0.5 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand/30',
      selected ? 'border-brand bg-brand text-brand-ink' : 'border-border bg-surface text-ink-2 hover:text-fg',
    )}
  >
    {label}
  </button>
);

export { ToggleChip, UsageChart, UsageControls, UsageLegend };
export type { UsageChartProps };
