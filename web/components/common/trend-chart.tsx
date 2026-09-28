'use client';

import { useQuery } from '@tanstack/react-query';
import { useState, useEffect, useId } from 'react';
import type { CSSProperties } from 'react';
import {
  AreaChart, Area, BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip,
  ResponsiveContainer, Legend, ReferenceArea,
  ReferenceLine,
} from 'recharts';
import type { TooltipContentProps } from 'recharts';
import { fetchCoverageGap, fetchLatestActivity, fetchTimeSeriesStatsByModel, fetchTimeSeriesStatsByUser } from '@/lib/api';
import { UNKNOWN_DESCRIPTION, UNKNOWN_KEY, UNKNOWN_LABEL, coverageIneligibleReason, coverageRangeFor, unknownByDate } from '@/lib/coverage-gap';
import { Tooltip as HintTooltip, TooltipContent as HintContent, TooltipProvider as HintProvider, TooltipTrigger as HintTrigger } from '@/components/ui/tooltip';
import { CoverageLine } from './coverage-line';
import { POLL_NORMAL, POLL_SLOW } from '@/lib/query-config';
import { useAgent } from './agent-context';
import { isFable, isAstra, modelColor, userSeriesColorMap } from '@/lib/colors';
import { WEEKLY_USAGE_HINT, isWeeklyUsageKey } from '@/lib/weekly-usage';
import type { ModelCategory } from '@/lib/colors';
import { modelLabel } from '@/lib/model-label';
import { compareModelLabels } from '@/lib/model-order';
import type { CoverageGap, ModelDailyStat, UserDailyStat } from '@/lib/types';
import { cn } from '@/lib/utils';
import { ChartSkeleton } from './chart-skeleton';
import { ChevronDown, ChevronRight, RotateCcw } from 'lucide-react';
import { advanceBucket, alignBucketStart, bucketDate, bucketKey, formatTrendTick, trendTickInterval } from '@/lib/trend-axis';
import type { TrendGranularity } from '@/lib/trend-axis';

type Granularity = TrendGranularity;
export type ViewMode = 'cost' | 'token';

export interface TrendPointSelection {
  date: string;
  since: string;
  until: string;
}

const GRANULARITY_OPTIONS: { value: Granularity; label: string }[] = [
  { value: 'minute', label: 'Minute' },
  { value: 'hour',   label: 'Hourly' },
  { value: 'day',    label: 'Daily' },
  { value: 'week',   label: 'Weekly' },
  { value: 'month',  label: 'Monthly' },
];

const WINDOW_MS: Record<Granularity, number> = {
  minute: 3 * 3600 * 1000,
  hour:   72 * 3600 * 1000,
  day:    30 * 86400 * 1000,
  week:   26 * 7 * 86400 * 1000,
  month:  365 * 86400 * 1000,
};

const GRANULARITY_ORDER: Granularity[] = ['minute', 'hour', 'day', 'week', 'month'];

// Finest granularity whose window still contains an event this old; else the coarsest.
const granularityForAge = (latestTs: string, now: number): Granularity => {
  const age = now - new Date(latestTs).getTime();
  return GRANULARITY_ORDER.find((g) => age <= WINDOW_MS[g]) ?? 'month';
};

const trendQueryEnabled = (
  collapsed: boolean,
  matchesChartState: boolean,
  queriesEnabled = true,
): boolean => queriesEnabled && !collapsed && matchesChartState;

const normalizeTrendProjectHashes = (
  projectHash: string | undefined,
  projectHashes: readonly string[] | undefined,
): string[] | undefined => {
  if (projectHashes !== undefined) return projectHashes.filter(Boolean);
  return projectHash ? [projectHash] : undefined;
};

const trendProjectScopeReady = (projectHashes: readonly string[] | undefined): boolean =>
  projectHashes === undefined || projectHashes.length > 0;

const shortUser = (email: string): string => {
  if (email.includes('@')) return email.split('@')[0];
  if (email.length > 16 && /^[0-9a-f]+$/i.test(email)) return email.slice(0, 8);
  return email;
};

const fmt = (n: number): string =>
  n >= 1_000_000 ? `${(n / 1_000_000).toFixed(1)}M` : n >= 1000 ? `${(n / 1000).toFixed(0)}K` : String(n);

const buildStackData = (
  byDate: Map<string, Record<string, number>>,
  keys: string[],
  g: Granularity,
  since: string,
  until: string | undefined,
  now: number,
): Record<string, number | string>[] => {
  const cur = new Date(since);
  const end = until ? new Date(until) : new Date(now);
  alignBucketStart(cur, g);

  const result: Record<string, number | string>[] = [];
  while (cur <= end) {
    const key = bucketKey(cur, g);
    const row: Record<string, number | string> = { date: key };
    const src = byDate.get(key);
    for (const k of keys) row[k] = src?.[k] ?? 0;
    result.push(row);
    advanceBucket(cur, g);
  }
  return result;
};

const buildModelStackData = (
  raw: ModelDailyStat[],
  models: string[],
  g: Granularity,
  since: string,
  until: string | undefined,
  now: number,
  mode: ViewMode = 'cost',
): Record<string, number | string>[] => {
  const byDate = new Map<string, Record<string, number>>();
  for (const r of raw) {
    const sm = modelLabel(r.model);
    if (!byDate.has(r.date)) byDate.set(r.date, {});
    const row = byDate.get(r.date)!;
    const val = mode === 'token' ? (r.input_tokens + r.output_tokens) : r.cost_usd;
    row[sm] = (row[sm] || 0) + val;
  }
  return buildStackData(byDate, models, g, since, until, now);
};

const buildUserStackData = (
  raw: UserDailyStat[],
  users: string[],
  g: Granularity,
  since: string,
  until: string | undefined,
  now: number,
  mode: ViewMode = 'cost',
): Record<string, number | string>[] => {
  const byDate = new Map<string, Record<string, number>>();
  for (const r of raw) {
    // Keyed on user_id alone. Falling back to profile_email when user_id was
    // blank keyed the same person two ways -- they are different facts, and one
    // user carries several profiles -- so a person split into two stacked
    // series and the nameMap lookup below missed the profile_email half.
    const su = r.user_id;
    if (!su) continue;
    if (!byDate.has(r.date)) byDate.set(r.date, {});
    const row = byDate.get(r.date)!;
    const val = mode === 'token' ? (r.input_tokens + r.output_tokens) : r.cost_usd;
    row[su] = (row[su] || 0) + val;
  }
  return buildStackData(byDate, users, g, since, until, now);
};

const trendTitle = (g: Granularity, mode: ViewMode, modelFilter?: string): string => {
  const prefix = mode === 'token' ? 'Token Trend' : 'Cost Trend';
  const base = {
    minute: `${prefix} — Per Minute (3h)`,
    hour:   `${prefix} — Hourly (72h)`,
    day:    `${prefix} — Daily (30d)`,
    week:   `${prefix} — Weekly (26w)`,
    month:  `${prefix} — Monthly (12m)`,
  }[g];
  if (modelFilter && modelFilter !== '__all__') return `${base} — ${modelFilter}`;
  return base;
};

export interface TrendChartProps {
  filterEmail?: string;
  filterAccount?: string;
  filterUserId?: string;

  viewMode?: ViewMode;
  onViewModeChange?: (mode: ViewMode) => void;
  showViewToggle?: boolean;

  showModelFilter?: boolean;
  modelCategory?: ModelCategory;
  defaultGranularity?: Granularity;
  height?: number;
  className?: string;
  groupBy?: 'model' | 'user';

  modelFilter?: string;
  onModelFilterChange?: (filter: string) => void;
  userFilter?: string;
  onUserFilterChange?: (filter: string) => void;
  projectHash?: string;
  projectHashes?: readonly string[];
  collapsed?: boolean;
  collapseLabel?: string;
  onCollapseToggle?: () => void;
  nameMap?: Record<string, string>;
  onTimeRangeChange?: (since: string, until: string | undefined, granularity: Granularity) => void;
  selectedPoint?: TrendPointSelection | null;
  onPointSelect?: (point: TrendPointSelection) => void;
  onPointClear?: () => void;
  onRangeSelect?: (since: string, until: string) => void;
  queriesEnabled?: boolean;
  /**
   * Show the subscription-coverage line under the title. Off by default: the
   * question only has an answer on the unfiltered overview, and the query
   * would otherwise be added to every page that mounts a trend chart.
   */
  showCoverage?: boolean;
}

interface TooltipItem {
  name: string;
  value: number;
  color?: string;
}

interface SelectionMarkerProps {
  date: string;
}

const SelectionMarker = ({ date }: SelectionMarkerProps) => {
  const gradientId = useId();

  return (
    <>
      <defs>
        <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="var(--success-strong)" />
          <stop offset="45%" stopColor="var(--success)" />
          <stop offset="100%" stopColor="var(--success-strong)" />
        </linearGradient>
      </defs>
      <ReferenceLine x={date} stroke="var(--success)" strokeOpacity={0.16} strokeWidth={11} />
      <ReferenceLine x={date} stroke={`url(#${gradientId})`} strokeWidth={3} />
    </>
  );
};

// The unknown segment is the one series that is an estimate; a muted ink at a
// fraction of the others' opacity keeps it out of the palettes and reads as a
// faint backdrop behind the measured series rather than a series of its own.
const UNKNOWN_FILL = 'var(--ink-3)';
const UNKNOWN_OPACITY = 0.18;

const unknownOpacity = (key: string, hovered: string | null, normal: number, focused: number): number => {
  if (key !== UNKNOWN_KEY) return hovered ? (hovered === key ? focused : 0.15) : normal;
  return hovered ? (hovered === key ? UNKNOWN_OPACITY * 2 : UNKNOWN_OPACITY / 3) : UNKNOWN_OPACITY;
};

// Unknown 은 언제나 스택 맨 위에 있어야 한다 -- 측정된 계열 위에 얹힌 추정치라서,
// 그 아래로 내려가면 어떤 사람의 실측 사용량이 추정치 위에 쌓인 것처럼 보인다.
//
// 그런데 recharts 3.x 는 스택 순서를 자식 순서가 아니라 등록 순서로 정한다:
// graphicalItemsSlice 의 addCartesianGraphicalItem 이 마운트되는 계열을 배열 끝에
// push 하고, combineStackGroups 가 그 배열 순서를 그대로 스택 순서로 쓴다. 자식을
// 재정렬해도 등록된 항목은 움직이지 않는다. 그래서 Unknown 이 자리를 잡은 뒤에
// 새 계열이 나타나면 -- 3시간 창이 밀리며 사용자가 들고 나는 동안 늘 일어난다 --
// Unknown 을 마지막에 그려도 그 계열이 위에 쌓인다.
//
// 계열 집합이 바뀔 때마다 Unknown 의 React key 를 바꿔 재마운트시키면, 같은 커밋
// 안에서 옛 항목이 먼저 제거되고 새 계열보다 뒤에 다시 등록되어 맨 위로 돌아온다.
// 순서만 바뀐 경우는 등록 순서에 영향이 없으므로 정렬된 집합으로 서명을 만들어
// 폴링마다 헛되이 재마운트하지 않게 한다.
const unknownSeriesKey = (activeKeys: string[]): string =>
  `${UNKNOWN_KEY}@${[...activeKeys].sort().join('\u0000')}`;

interface UnknownHintProps {
  children: React.ReactNode;
}

/** The same explanation on the legend entry and on the toggle: hover either to learn what the segment is. */
const UnknownHint = ({ children }: UnknownHintProps) => (
  <HintProvider delayDuration={300}>
    <HintTooltip>
      <HintTrigger asChild>{children}</HintTrigger>
      <HintContent className="max-w-[320px]">{UNKNOWN_DESCRIPTION}</HintContent>
    </HintTooltip>
  </HintProvider>
);

/** The weekly line in the by-user view is not a person: hover it to learn whose tokens they are. */
const WeeklyHint = ({ children }: UnknownHintProps) => (
  <HintProvider delayDuration={300}>
    <HintTooltip>
      <HintTrigger asChild>{children}</HintTrigger>
      <HintContent className="max-w-[320px]">{WEEKLY_USAGE_HINT}</HintContent>
    </HintTooltip>
  </HintProvider>
);

// recharts 의 `content` 는 함수를 받으면 그 함수를 **컴포넌트 타입**으로 쓴다
// (`React.createElement(props.content, ...)`). 컴포넌트 본문에서 만든 화살표 함수는
// 렌더마다 새 참조라 타입이 매번 달라지고, React 는 타입이 바뀌면 서브트리를
// 언마운트하고 새로 마운트한다 -- 범례 DOM 이 리렌더마다 통째로 교체된다.
//
// 그러면 포인터가 올라가 있던 <li> 가 제거되므로 브라우저가 떠남 이벤트를 내지 않고,
// hoveredLegend 가 풀리지 않아 차트가 흐린 채로 남는다 (#552). 그래서 두 컴포넌트를
// 모듈 최상위에 두어 타입을 고정하고, 아래에서는 엘리먼트로 넘긴다 -- 엘리먼트는
// recharts 가 cloneElement 로 갱신하므로 재마운트가 없다.
//
// useCallback 은 해법이 아니다: 의존성이 하나라도 바뀌면 다시 새 타입이 된다.

interface ChartLegendProps {
  legendKeys: string[];
  unknownActive: boolean;
  colorMap: Record<string, string>;
  formatLabel: (value: string) => React.ReactNode;
  onItemEnter: (e: React.MouseEvent<HTMLLIElement>) => void;
  onItemLeave: () => void;
}

const ChartLegend = ({ legendKeys, unknownActive, colorMap, formatLabel, onItemEnter, onItemLeave }: ChartLegendProps) => (
  // 떠남을 항목과 컨테이너 양쪽에서 받는다. 항목 하나가 사라지면서 그 항목의
  // 떠남이 유실되어도, 포인터가 범례 밖으로 나가면 컨테이너가 잡는다.
  <ul
    className="flex flex-wrap items-center justify-center gap-x-3 gap-y-0.5 px-2 text-[10px] leading-4"
    onMouseLeave={onItemLeave}
  >
    {legendKeys.map((k) => {
      const reserved = k === UNKNOWN_KEY && !unknownActive;
      const item = (
        <li
          key={k}
          data-series-key={k}
          aria-hidden={reserved || undefined}
          // 설명이 붙는 weekly 항목은 키보드로도 힌트를 열 수 있게 포커스를 받는다.
          tabIndex={isWeeklyUsageKey(k) ? 0 : undefined}
          className={cn('flex items-center gap-1', reserved && 'invisible')}
          onMouseEnter={onItemEnter}
          onMouseLeave={onItemLeave}
        >
          {/* 색은 OKLCH·CSS 변수라 Tailwind 클래스로 표현할 수 없다. SVG fill 속성으로 칠한다. */}
          <svg width={8} height={8} aria-hidden="true"><rect width={8} height={8} fill={k === UNKNOWN_KEY ? UNKNOWN_FILL : colorMap[k]} fillOpacity={k === UNKNOWN_KEY ? UNKNOWN_OPACITY * 2 : 1} /></svg>
          {formatLabel(k)}
        </li>
      );
      // 자리만 잡은 항목에는 힌트를 붙이지 않는다. 보이지 않는 것에 대한 설명이다.
      if (k === UNKNOWN_KEY && !reserved) return <UnknownHint key={k}>{item}</UnknownHint>;
      return isWeeklyUsageKey(k) ? <WeeklyHint key={k}>{item}</WeeklyHint> : item;
    })}
  </ul>
);

// recharts 가 cloneElement 로 주입하는 props 는 호출부에서 줄 수 없으므로 선택적이다.
interface TrendTooltipProps extends Partial<TooltipContentProps<number, string>> {
  groupBy: 'model' | 'user';
  nameMap?: Record<string, string>;
  granularity: Granularity;
  formatValue: (v: number) => string;
}

const TrendTooltip = ({ active, payload, label, groupBy, nameMap, granularity, formatValue }: TrendTooltipProps) => {
  if (!active || !payload) return null;
  const items: TooltipItem[] = payload
    .flatMap((item) => typeof item.name === 'string' && typeof item.value === 'number'
      ? [{ name: item.name, value: item.value, color: item.color }]
      : [])
    .filter(p => p.name !== '_anchor' && p.value > 0)
    .sort((a, b) => b.value - a.value);
  if (items.length === 0) return null;
  const resolveName = (key: string) =>
    key === UNKNOWN_KEY ? UNKNOWN_LABEL
      : (groupBy === 'user' && nameMap?.[key]) ? `${nameMap[key]} (${key})` : key;
  return (
    <div className="bg-surface border border-border rounded-[4px] px-2 py-1 text-[11px] shadow-[var(--sh-pop)]">
      <p className="m-0 font-semibold">{formatTrendTick(granularity, String(label))}</p>
      {items.map((p) => {
        const itemColor = p.name === UNKNOWN_KEY
          ? UNKNOWN_FILL
          : p.color?.startsWith('url(')
            ? isAstra(p.name)
              ? 'var(--model-astra)'
              : 'var(--model-fable)'
            : p.color;

        return (
          <p
            key={p.name}
            className="m-0 [color:var(--item-color)]"
            // CSS variable drives the per-series tooltip color (Tailwind cannot express dynamic values).
            // Flagship gradient fills are invalid as CSS colors, so fall back to their solid anchors.
            style={{ '--item-color': itemColor } as CSSProperties} // eslint-disable-line no-restricted-syntax
          >
            {resolveName(p.name)} : {p.name === UNKNOWN_KEY ? '≈ ' : ''}{formatValue(p.value)}
          </p>
        );
      })}
    </div>
  );
};

// Cache detected granularity across re-mounts (tab switches)
const _detectedCache = new Map<string, Granularity>();

const TrendChart = ({
  filterEmail,
  filterAccount,
  filterUserId,
  viewMode: controlledViewMode,
  onViewModeChange,
  showViewToggle = true,
  showModelFilter = false,
  modelCategory = 'all',
  defaultGranularity = 'minute',
  height = 200,
  className,
  groupBy = 'model',
  modelFilter: controlledModelFilter,
  userFilter: controlledUserFilter,
  projectHash,
  projectHashes,
  collapsed = false,
  collapseLabel,
  onCollapseToggle,
  nameMap,
  onTimeRangeChange,
  selectedPoint,
  onPointSelect,
  onPointClear,
  onRangeSelect,
  queriesEnabled = true,
  showCoverage = false,
}: TrendChartProps) => {
  const [internalViewMode, setInternalViewMode] = useState<ViewMode>('cost');
  // On by default: seen on real data the overlay reads as a plain extra
  // series, and the point of the chart is to show what is not measured.
  const [showUnknown, setShowUnknown] = useState(true);
  const handleToggleUnknown = () => setShowUnknown((v) => !v);
  const [internalModelFilter] = useState<string>('__all__');
  const [internalUserFilter] = useState<string>('__all__');
  const modelFilter = controlledModelFilter ?? internalModelFilter;
  const userFilter = controlledUserFilter ?? internalUserFilter;
  const { selectedAgent } = useAgent();
  const normalizedProjectHashes = normalizeTrendProjectHashes(projectHash, projectHashes);
  const projectHashKey = normalizedProjectHashes?.join(',') ?? '';
  const projectScopeReady = trendProjectScopeReady(normalizedProjectHashes);

  const viewMode = controlledViewMode ?? internalViewMode;
  const setViewMode = (mode: ViewMode) => {
    if (onViewModeChange) onViewModeChange(mode);
    else setInternalViewMode(mode);
  };

  const drillToModel = groupBy === 'user' && userFilter !== '__all__';
  const drillToUser = groupBy === 'model' && showModelFilter && modelFilter !== '__all__';
  const effectiveGroupBy: 'model' | 'user' = drillToModel ? 'model' : drillToUser ? 'user' : groupBy;
  const modelQueryUserId = drillToModel ? userFilter : filterUserId;
  const probeUserId = effectiveGroupBy === 'model' ? modelQueryUserId : filterUserId;
  const probeModel = effectiveGroupBy === 'user' && modelFilter !== '__all__' ? modelFilter : undefined;
  const cacheKey = [
    filterEmail ?? '',
    filterAccount ?? '',
    probeUserId ?? '',
    projectHashKey,
    selectedAgent ?? '',
    modelCategory,
    effectiveGroupBy,
    probeModel ?? '',
  ].join('|');
  const cached = _detectedCache.get(cacheKey);
  const [granularity, setGranularity] = useState<Granularity>(cached ?? defaultGranularity);
  const [autoGranularity, setAutoGranularity] = useState<Granularity | null>(null);
  const [hasFoundData, setHasFoundData] = useState(cached != null);
  const [activeCacheKey, setActiveCacheKey] = useState(cacheKey);
  if (activeCacheKey !== cacheKey) {
    setActiveCacheKey(cacheKey);
    setGranularity(cached ?? defaultGranularity);
    setAutoGranularity(null);
    setHasFoundData(cached != null);
  }
  const effectiveGranularity = autoGranularity ?? granularity;

  // Time navigation: offset shifts window into the past; customRange overrides completely.
  // nowAnchor is captured once at mount so the window stays stable across re-renders (purity).
  const [nowAnchor] = useState(() => Date.now());
  const [offset, setOffset] = useState(0); // ms offset from "now"
  const [customSince, setCustomSince] = useState('');
  const [customUntil, setCustomUntil] = useState('');
  const [showDatePicker, setShowDatePicker] = useState(false);

  const windowMs = WINDOW_MS[effectiveGranularity];

  const sinceTs = customSince
    ? new Date(customSince).toISOString()
    : new Date(nowAnchor - windowMs - offset).toISOString();

  const untilTs = customUntil
    ? new Date(customUntil).toISOString()
    : offset === 0
      ? undefined
      : new Date(nowAnchor - offset).toISOString();

  const { data: tsModelData = [], isFetching: isFetchingModel, isPending: isPendingModel } = useQuery<ModelDailyStat[]>({
    queryKey: ['timeseries-by-model', effectiveGranularity, sinceTs, untilTs, filterEmail, filterAccount, projectHashKey, modelQueryUserId, selectedAgent, modelCategory],
    queryFn: () => fetchTimeSeriesStatsByModel(effectiveGranularity, sinceTs, untilTs, filterEmail, filterAccount, undefined, projectHash, modelQueryUserId, selectedAgent || undefined, modelCategory, normalizedProjectHashes),
    staleTime: 5 * 60 * 1000,
    gcTime: 10 * 60 * 1000,
    refetchInterval: POLL_NORMAL,
    enabled: projectScopeReady && trendQueryEnabled(collapsed, effectiveGroupBy === 'model', queriesEnabled),
  });

  const { data: tsUserData = [], isFetching: isFetchingUser, isPending: isPendingUser } = useQuery<UserDailyStat[]>({
    queryKey: ['timeseries-by-user', effectiveGranularity, sinceTs, untilTs, filterEmail, filterAccount, projectHashKey, filterUserId, modelCategory, modelFilter, selectedAgent],
    queryFn: () => fetchTimeSeriesStatsByUser(effectiveGranularity, sinceTs, untilTs, filterEmail, filterAccount, undefined, projectHash, filterUserId, modelCategory, modelFilter, selectedAgent || undefined, normalizedProjectHashes),
    staleTime: 5 * 60 * 1000,
    gcTime: 10 * 60 * 1000,
    refetchInterval: POLL_NORMAL,
    enabled: projectScopeReady && trendQueryEnabled(collapsed, effectiveGroupBy === 'user', queriesEnabled),
  });

  const { data: probeData } = useQuery<{ has_data: boolean; latest_ts?: string }>({
    queryKey: ['latest-activity', filterEmail, filterAccount, probeUserId, projectHashKey, selectedAgent, modelCategory, probeModel],
    queryFn: () => fetchLatestActivity({
      profileEmail: filterEmail,
      loginEmail: filterAccount,
      projectHash: normalizedProjectHashes === undefined ? projectHash : undefined,
      projectHashes: normalizedProjectHashes,
      userID: probeUserId,
      agent: selectedAgent || undefined,
      modelCategory,
      model: probeModel,
    }),
    staleTime: 5 * 60 * 1000,
    refetchInterval: POLL_NORMAL,
    enabled: projectScopeReady && trendQueryEnabled(collapsed, cached == null && !hasFoundData, queriesEnabled),
  });

  const isFetching = isFetchingModel || isFetchingUser;

  const tsDataLength = effectiveGroupBy === 'user' ? tsUserData.length : tsModelData.length;

  const buildUniqueKeys = <T extends ModelDailyStat | UserDailyStat>(
    rows: T[],
    keyOf: (row: T) => string,
  ): string[] => {
    const vals = new Map<string, number>();
    for (const r of rows) {
      const k = keyOf(r);
      if (!k) continue;
      const v = viewMode === 'token' ? (r.input_tokens + r.output_tokens) : r.cost_usd;
      vals.set(k, (vals.get(k) || 0) + v);
    }
    return [...vals.entries()]
      .filter(([, v]) => v > 0)
      .sort((a, b) => b[1] - a[1])
      .map(([k]) => k);
  };

  const uniqueModels = buildUniqueKeys(tsModelData, (row) => modelLabel(row.model));

  const modelColorMap: Record<string, string> = {};
  uniqueModels.forEach((m) => { modelColorMap[m] = modelColor(m); });

  const uniqueUsers = effectiveGroupBy !== 'user'
    ? []
    : buildUniqueKeys(tsUserData, (row) => row.user_id);

  const userColorMap = userSeriesColorMap(uniqueUsers);

  const activeKeys = effectiveGroupBy === 'user' ? uniqueUsers : uniqueModels;

  const colorMap = effectiveGroupBy === 'user' ? userColorMap : modelColorMap;

  const stackedData = effectiveGroupBy === 'user'
    ? buildUserStackData(tsUserData, activeKeys, effectiveGranularity, sinceTs, untilTs, nowAnchor, viewMode)
    : buildModelStackData(tsModelData, activeKeys, effectiveGranularity, sinceTs, untilTs, nowAnchor, viewMode);

  // Zoom state: drag to select range, double-click to reset
  const [dragStart, setDragStart] = useState<string | null>(null);
  const [dragEnd, setDragEnd] = useState<string | null>(null);
  const [zoomRange, setZoomRange] = useState<[string, string] | null>(null);
  const [internalSelectedPoint, setInternalSelectedPoint] = useState<TrendPointSelection | null>(null);
  const activeSelectedPoint = selectedPoint === undefined ? internalSelectedPoint : selectedPoint;

  // Reset zoom when granularity changes
  const [prevGranularity, setPrevGranularity] = useState(granularity);
  if (prevGranularity !== granularity) {
    setPrevGranularity(granularity);
    setAutoGranularity(null);
    setZoomRange(null);
    setOffset(0);
    setCustomSince('');
    setCustomUntil('');
  }

  const displayData = (() => {
    if (!zoomRange) return stackedData;
    const [start, end] = zoomRange;
    const startIdx = stackedData.findIndex(d => String(d.date) >= start);
    const endIdx = [...stackedData].reverse().findIndex(d => String(d.date) <= end);
    const realEnd = endIdx === -1 ? stackedData.length - 1 : stackedData.length - 1 - endIdx;
    if (startIdx === -1 || startIdx > realEnd) return stackedData;
    return stackedData.slice(startIdx, realEnd + 1);
  })();

  const handleMouseDown = (e: { activeLabel?: string | number }) => {
    if (e?.activeLabel != null) setDragStart(String(e.activeLabel));
  };
  const handleMouseMove = (e: { activeLabel?: string | number }) => {
    if (dragStart && e?.activeLabel != null) setDragEnd(String(e.activeLabel));
  };
  const handleMouseUp = (event: { activeLabel?: string | number }) => {
    if (dragStart && dragEnd && dragStart !== dragEnd) {
      const [a, b] = dragStart < dragEnd ? [dragStart, dragEnd] : [dragEnd, dragStart];
      setZoomRange([a, b]);
      const start = bucketDate(a);
      const end = bucketDate(b);
      advanceBucket(end, effectiveGranularity);
      onRangeSelect?.(start.toISOString(), end.toISOString());
    } else if (dragStart) {
      const date = String(event.activeLabel ?? dragStart);
      const start = bucketDate(date);
      if (!Number.isNaN(start.getTime())) {
        const end = new Date(start);
        advanceBucket(end, effectiveGranularity);
        const point = { date, since: start.toISOString(), until: end.toISOString() };
        if (selectedPoint === undefined) setInternalSelectedPoint(point);
        onPointSelect?.(point);
      }
    }
    setDragStart(null);
    setDragEnd(null);
  };
  const clearPointSelection = () => {
    if (selectedPoint === undefined) setInternalSelectedPoint(null);
    onPointClear?.();
  };
  const handleDoubleClick = () => { setZoomRange(null); setOffset(0); setCustomSince(''); setCustomUntil(''); clearPointSelection(); };

  const tickInterval = trendTickInterval(effectiveGranularity, displayData.length, zoomRange !== null);

  const yAxisFmt = (v: number) => viewMode === 'token' ? fmt(v) : `$${v}`;
  const tooltipFmt = (v: number) => viewMode === 'token' ? fmt(v) : `$${Number(v).toFixed(4)}`;

  // follow the dragged sub-range. zoomRange holds bucket date keys; convert to ISO, advancing
  // the end by one bucket so the selected end bucket is included.
  const reportedSince = zoomRange ? bucketDate(zoomRange[0]).toISOString() : sinceTs;
  const reportedUntil = zoomRange
    ? (() => { const end = bucketDate(zoomRange[1]); advanceBucket(end, effectiveGranularity); return end.toISOString(); })()
    : untilTs;

  // Coverage is asked for the range on screen, zoom included, and only in the
  // scopes where "measured vs burn" is a comparison of like with like. The
  // client decides eligibility so an ineligible scope costs no request; the
  // server repeats the check because it is the one that must not be fooled.
  const coverageReason = coverageIneligibleReason({
    hasProjectFilter: normalizedProjectHashes !== undefined,
    hasUserFilter: !!filterUserId || drillToModel,
    hasProfileFilter: !!filterEmail,
    hasModelFilter: drillToUser,
    agent: selectedAgent,
    modelCategory,
  });
  const chartUntil = reportedUntil ?? new Date(nowAnchor).toISOString();
  const coverageRange = coverageRangeFor(reportedSince, chartUntil);
  // Whether a coverage answer is coming at all. The line reserves its height only
  // in that case, so charts that never show coverage carry no empty strip.
  const coverageWanted = showCoverage && !collapsed;
  // The overlay is opt-in, so the default view still asks for no bucketing.
  const bucketsWanted = showUnknown && coverageWanted && coverageReason === null;
  // Unknown 이 나타날 수 있는 상황인가. 값이 도착하기 전부터 범례 자리를 잡아야
  // 하므로 응답을 기다리지 않는다.
  const unknownSlotReserved = bucketsWanted;

  // One request, both scopes. The sentence widens to a trailing week on short
  // ranges while the overlay stays on the plotted one, and asking separately
  // meant two requests each re-loading the same 28 days of fit data -- with the
  // second unable to start until the first returned, since it was gated on the
  // first's eligibility. That serial pair is what the reader saw as a third
  // render stage.
  const { data: coverageResponse } = useQuery<CoverageGap>({
    queryKey: ['coverage-gap', coverageRange.since, coverageRange.until, filterAccount, bucketsWanted ? effectiveGranularity : '', bucketsWanted ? reportedSince : '', bucketsWanted ? chartUntil : ''],
    queryFn: () => (bucketsWanted
      ? fetchCoverageGap({
        since: reportedSince, until: chartUntil, loginEmail: filterAccount, granularity: effectiveGranularity,
        headlineSince: coverageRange.since, headlineUntil: coverageRange.until,
      })
      : fetchCoverageGap({ since: coverageRange.since, until: coverageRange.until, loginEmail: filterAccount })),
    staleTime: POLL_SLOW,
    // The readings behind this arrive every five minutes at best, and the query
    // aggregates 28 days of events to fit its factor; asking more often re-asks
    // for an answer that cannot have changed.
    refetchInterval: POLL_SLOW,
    // keepPreviousData is deliberately absent. The payload now carries the
    // Unknown buckets too, and holding the previous range's buckets would paint
    // stale segments onto bars whose measured part has already updated -- a chart
    // that is briefly, silently wrong. A one-tick gap in the sentence is the
    // cheaper failure, and its height is reserved either way.
    enabled: queriesEnabled && coverageWanted && coverageReason === null,
  });
  // The top level is whichever scope was plotted; `headline` appears only when
  // the two differ.
  const coverageGap = coverageResponse?.headline ?? coverageResponse;
  const coverageShown: CoverageGap | undefined = !showCoverage || collapsed
    ? undefined
    : coverageReason !== null
      ? { eligible: false, reason: coverageReason, measured_usd: 0, implied_usd: 0, coverage_ratio: 0, sample_coverage: 0, censored_fraction: 0, accounts: [], unfitted: [], k_method: '' }
      : coverageGap;
  const unknownWanted = bucketsWanted && coverageGap?.eligible === true;
  const unknownGap = coverageResponse;
  const unknownSeries = unknownWanted ? unknownByDate(unknownGap, viewMode) : new Map<string, number>();
  const unknownActive = unknownSeries.size > 0;

  const [hoveredLegend, setHoveredLegend] = useState<string | null>(null);
  // Fable legend text can't take the series' url(#…) fill as a CSS color —
  // paint it with the gradient itself (clip-text), matching the legend icon.
  const legendFormatter = (value: string) => {
    if (value === UNKNOWN_KEY) return UNKNOWN_LABEL;
    // A user_id the nameMap does not cover is shown shortened, not raw: an
    // unnamed user used to reach the legend as a full 64-character hash sitting
    // where a person's name goes. Shortening keeps it an opaque name for a
    // known user rather than borrowing some other identity to fill the gap.
    const label = effectiveGroupBy === 'user' ? (nameMap?.[value] ?? shortUser(value)) : value;
    if (effectiveGroupBy === 'model' && isFable(value)) {
      return <span className="text-model-fable">{label}</span>;
    }
    if (effectiveGroupBy === 'model' && isAstra(value)) {
      return <span className="text-model-astra">{label}</span>;
    }
    return label;
  };
  const handleLegendItemEnter = (e: React.MouseEvent<HTMLLIElement>) => setHoveredLegend(e.currentTarget.dataset.seriesKey ?? null);
  const handleLegendLeave = () => setHoveredLegend(null);

  // recharts v3는 범례 항목 순서를 자기가 정하고 payload 주입도 막아두었다. 그래서
  // 범례를 직접 그린다 — 모델별 보기에서는 계열 → 티어 → 버전 순으로 고정해, 기간을
  // 바꿔도 같은 모델이 같은 자리에 남는다. 스택은 계속 금액 큰 순(activeKeys)으로 쌓인다.
  // Unknown 자리는 값이 오기 전부터 잡아 둔다. 범례는 flex-wrap 이라 항목이 하나
  // 늘면 줄이 늘 수 있고, recharts 는 고정 높이를 범례와 플롯으로 나눠 쓰므로 그때
  // 플롯이 눌린다 — 사용자가 본 3단계 중 마지막 흔들림이 이것이다. 자리를 고정
  // 높이로 잘라 막으면 계열이 많은 범위에서 범례가 잘리므로, 폭이 같은 빈 항목을
  // 미리 두어 줄바꿈 자체가 변하지 않게 한다.
  const legendKeys = [
    ...(effectiveGroupBy === 'model' ? [...activeKeys].sort(compareModelLabels) : activeKeys),
    ...(unknownActive || unknownSlotReserved ? [UNKNOWN_KEY] : []),
  ];
  // 호버 중이던 계열이 범례에서 사라지면(폴링으로 키 집합이 바뀌거나 그룹 기준을
  // 바꿀 때) 그 항목의 떠남 이벤트는 영영 오지 않는다. 그대로 두면 강조 대상이
  // 없는 채로 모든 계열이 0.15 로 떨어져 차트 전체가 흐려진다 (#552). 현재 범례에
  // 없는 키는 호버가 아닌 것으로 본다 -- 상태가 어긋나도 화면은 정상으로 돌아온다.
  const focusedKey = hoveredLegend !== null && legendKeys.includes(hoveredLegend) ? hoveredLegend : null;

  const isAtPresent = offset === 0 && !customSince && !customUntil;

  const goBack = () => {
    setCustomSince('');
    setCustomUntil('');
    setZoomRange(null);
    setOffset(o => o + windowMs);
  };
  const goForward = () => {
    setCustomSince('');
    setCustomUntil('');
    setZoomRange(null);
    setOffset(o => Math.max(0, o - windowMs));
  };
  const goToPresent = () => {
    setOffset(0);
    setCustomSince('');
    setCustomUntil('');
    setZoomRange(null);
  };
  const applyCustomRange = () => {
    setOffset(0);
    setZoomRange(null);
    setShowDatePicker(false);
  };
  const toggleDatePicker = () => setShowDatePicker(v => !v);
  const handleCustomSinceChange = (e: React.ChangeEvent<HTMLInputElement>) => setCustomSince(e.target.value);
  const handleCustomUntilChange = (e: React.ChangeEvent<HTMLInputElement>) => setCustomUntil(e.target.value);
  const handleViewCost = () => setViewMode('cost');
  const handleViewToken = () => setViewMode('token');
  const handleGranularitySelect = (value: Granularity) => { setGranularity(value); setAutoGranularity(null); };
  const handleZoomReset = () => {
    setZoomRange(null);
    clearPointSelection();
    onRangeSelect?.(sinceTs, untilTs ?? new Date(nowAnchor).toISOString());
  };

  const titleSuffix = drillToModel ? (nameMap?.[userFilter] ?? userFilter)
    : drillToUser ? modelLabel(modelFilter)
    : (showModelFilter && modelFilter !== '__all__' ? modelLabel(modelFilter) : undefined);
  const title = trendTitle(effectiveGranularity, viewMode, titleSuffix);


  // Unknown usage rides on top of the stack when asked for. It is folded into
  // the rows after the stack is built rather than threaded through the two
  // builders: it belongs to neither the model nor the user decomposition, and
  // "the total is bigger than the sum of known series" is the only claim it makes.
  const renderKeys = unknownActive ? [...activeKeys, UNKNOWN_KEY] : activeKeys;
  // 마지막에 그리는 것만으로는 Unknown 이 맨 위에 남지 않는다 -- unknownSeriesKey 주석 참고.
  const unknownKey = unknownSeriesKey(activeKeys);
  const seriesKey = (key: string): string => (key === UNKNOWN_KEY ? unknownKey : key);
  const noData = tsDataLength === 0 && !unknownActive;
  const stackedRows = unknownActive
    ? displayData.map((row): Record<string, number | string> => ({ ...row, [UNKNOWN_KEY]: unknownSeries.get(String(row.date)) ?? 0 }))
    : displayData;
  const currentMax = noData ? 0 : stackedRows.reduce((mx, row) => {
    let sum = 0;
    for (const key of renderKeys) {
      const value = row[key];
      if (typeof value === 'number') sum += value;
    }
    return Math.max(mx, sum);
  }, 0);
  // An empty window gets a fixed scale rather than the last populated one.
  // Remembering the previous max meant a setState during render, and nothing
  // is plotted on an empty chart for the remembered scale to keep in place.
  const yMax = currentMax > 0 ? currentMax : viewMode === 'token' ? 100 : 0.1;
  // When no data, inject an anchor value into first data point to force Y axis scale
  const chartData = (!noData || stackedRows.length === 0)
    ? stackedRows
    : stackedRows.map((d, i) => i === 0 ? { ...d, _anchor: yMax } : { ...d, _anchor: 0 });
  const isAutoDetecting = !hasFoundData && (isFetching || (noData && GRANULARITY_ORDER.indexOf(effectiveGranularity) < GRANULARITY_ORDER.length - 1));
  // isPending, not isFetching: a background refetch keeps the rendered chart and swaps
  // the values when it lands. Gating on isFetching replaced a chart the user was reading
  // with a placeholder every poll interval, which is the thing the polling rules call out
  // — auto-refresh must not disturb what is on screen. isFetching still drives the
  // granularity probe below, where "a request is in flight" is the actual question.
  const isChartLoading = isAutoDetecting || (effectiveGroupBy === 'user' ? isPendingUser : isPendingModel);

  /**
   * Lock the display granularity to the finest one that has data. Fast path: if the
   * current (initial) granularity already returned rows, use it. Otherwise consult the
   * latest-activity probe (fired in parallel) and jump directly to the granularity whose
   * window contains the most recent event — one hop instead of stepping through all five.
   * setState here reacts to async query results (the sanctioned effect-driven case).
   */
  useEffect(() => {
    if (isFetching || hasFoundData) return;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (tsDataLength > 0) { setHasFoundData(true); _detectedCache.set(cacheKey, effectiveGranularity); return; }
    if (!probeData) return; // wait for the probe before deciding
    if (!probeData.has_data || !probeData.latest_ts) {
      setHasFoundData(true); // nothing anywhere in scope; stop probing
      return;
    }
    const detected = granularityForAge(probeData.latest_ts, nowAnchor);
    if (detected !== effectiveGranularity) {
      setAutoGranularity(detected);
    } else {
      setHasFoundData(true); // already at the detected granularity but no rows; stop
    }
  }, [tsDataLength, effectiveGranularity, isFetching, hasFoundData, probeData]); // eslint-disable-line react-hooks/exhaustive-deps

  // A drag-zoom narrows the reported range so consumers (e.g. user-detail Model Breakdown)


  /** Notify the parent whenever the resolved time range changes. */
  useEffect(() => {
    onTimeRangeChange?.(reportedSince, reportedUntil, effectiveGranularity);
  }, [reportedSince, reportedUntil, effectiveGranularity]); // eslint-disable-line react-hooks/exhaustive-deps

  const chartCursorVar = { '--chart-cursor': dragStart ? 'col-resize' : 'crosshair' } as CSSProperties;
  const chartClassName = 'select-none [cursor:var(--chart-cursor)]';
  const chartSkeletonHeightClassName = height <= 130 ? 'h-[130px]' : height <= 160 ? 'h-[160px]' : height <= 200 ? 'h-[200px]' : 'h-[250px]';

  return (
    <div className={className}>
      <div className={cn('flex items-center justify-between gap-2', !collapsed && 'mb-3')}>
        <div className="flex min-w-0 items-center gap-1.5">
          <h4 className="truncate text-[13px] font-semibold text-ink">{title}</h4>
          {!collapsed && (
            <button
              type="button"
              onClick={handleZoomReset}
              disabled={!zoomRange}
              title="Reset trend zoom"
              aria-label="Reset trend zoom"
              className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-sm text-ink-3 transition-colors hover:bg-surface-sunk hover:text-ink-2 disabled:cursor-not-allowed disabled:opacity-35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-ring)]"
            >
              <RotateCcw size={14} aria-hidden />
            </button>
          )}
        </div>
        <div className="flex items-center gap-1.5 flex-wrap justify-end">
          {!collapsed && <>
          {/* Arrow navigation */}
          <button onClick={goBack} className="px-2.5 py-0.5 text-[11px] rounded border border-border bg-surface text-ink-2 hover:bg-surface-sunk transition-colors">←</button>
          <button onClick={goForward} disabled={isAtPresent} className="px-2.5 py-0.5 text-[11px] rounded border border-border bg-surface text-ink-2 hover:bg-surface-sunk disabled:opacity-30 transition-colors">→</button>
          <span className="text-[11px] text-ink-3 shrink-0">{formatTrendTick(effectiveGranularity, sinceTs)} – {formatTrendTick(effectiveGranularity, untilTs ?? new Date(nowAnchor).toISOString())}</span>
          <button onClick={toggleDatePicker} className={cn('px-2.5 py-0.5 text-[11px] rounded border font-medium transition-colors', showDatePicker ? 'border-brand bg-brand-soft text-brand' : 'border-border bg-surface text-ink-2 hover:bg-surface-sunk')}>Date</button>
          {!isAtPresent && <>
            <button onClick={goToPresent} className="px-2.5 py-0.5 text-[11px] rounded font-medium bg-brand text-brand-ink hover:bg-brand-hover transition-colors">Now</button>
            <button onClick={handleDoubleClick} className="px-2.5 py-0.5 text-[11px] rounded border border-border bg-surface text-ink-2 hover:bg-surface-sunk transition-colors">Reset</button>
          </>}
          {showDatePicker && <>
            <input type="datetime-local" value={customSince} onChange={handleCustomSinceChange} className="text-[11px] border border-border rounded px-1.5 py-0.5 outline-none focus:border-brand" />
            <span className="text-[11px] text-ink-3">–</span>
            <input type="datetime-local" value={customUntil} onChange={handleCustomUntilChange} className="text-[11px] border border-border rounded px-1.5 py-0.5 outline-none focus:border-brand" />
            <button onClick={applyCustomRange} className="px-2.5 py-0.5 text-[11px] rounded font-medium bg-brand text-brand-ink hover:bg-brand-hover transition-colors">Apply</button>
          </>}
          {/* Separator */}
          <div className="w-px h-4 bg-border mx-0.5" />
          {showCoverage && (
            <UnknownHint>
              <button
                type="button"
                onClick={handleToggleUnknown}
                disabled={coverageReason !== null || !coverageGap?.eligible}
                className={cn('px-2.5 py-0.5 text-[11px] rounded border font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-35', showUnknown ? 'border-brand bg-brand-soft text-brand' : 'border-border bg-surface text-ink-2 hover:bg-surface-sunk')}
              >
                Unknown
              </button>
            </UnknownHint>
          )}
          {showViewToggle && (
            <div className="flex rounded-lg overflow-hidden border border-border">
              <button onClick={handleViewCost} className={cn('px-2.5 py-0.5 text-[11px] font-medium transition-colors', viewMode === 'cost' ? 'bg-brand text-brand-ink' : 'bg-surface text-ink-2')}>Cost</button>
              <button onClick={handleViewToken} className={cn('px-2.5 py-0.5 text-[11px] font-medium transition-colors', viewMode === 'token' ? 'bg-brand text-brand-ink' : 'bg-surface text-ink-2')}>Token</button>
            </div>
          )}
          <div className="flex gap-1">
            {GRANULARITY_OPTIONS.map(opt => (
              <button key={opt.value} onClick={() => handleGranularitySelect(opt.value)} className={cn('px-2.5 py-0.5 text-[11px] rounded font-medium transition-colors', effectiveGranularity === opt.value ? 'bg-brand text-brand-ink' : 'bg-surface-sunk text-ink-2 hover:bg-border')}>
                {opt.label}
              </button>
            ))}
          </div>
          </>}
          {onCollapseToggle && (
            <button
              type="button"
              onClick={onCollapseToggle}
              title={collapseLabel}
              aria-label={collapseLabel}
              aria-expanded={!collapsed}
              className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-sm text-ink-3 transition-colors hover:bg-surface-sunk hover:text-ink-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-ring)]"
            >
              {collapsed ? <ChevronRight size={14} aria-hidden /> : <ChevronDown size={14} aria-hidden />}
            </button>
          )}
        </div>
      </div>
      {!collapsed && <div onDoubleClick={handleDoubleClick}>
        {isChartLoading ? (
          <ChartSkeleton className={chartSkeletonHeightClassName} />
        ) : (effectiveGranularity === 'minute' || effectiveGranularity === 'hour') ? (
          <ResponsiveContainer width="100%" height={height}>
            <AreaChart
              data={chartData}
              margin={{ top: 0, right: 0, bottom: 0, left: 0 }}
              onMouseDown={handleMouseDown}
              onMouseMove={handleMouseMove}
              onMouseUp={handleMouseUp}
              className={chartClassName}
              // CSS variable drives the dynamic drag cursor (Tailwind cannot express runtime values)
              style={chartCursorVar} // eslint-disable-line no-restricted-syntax
            >
              <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
              <XAxis dataKey="date" tick={{ fontSize: 10 }} tickFormatter={(v) => formatTrendTick(effectiveGranularity, v)} interval={tickInterval} />
              <YAxis tick={{ fontSize: 10 }} tickFormatter={yAxisFmt} />
              <Tooltip content={<TrendTooltip groupBy={effectiveGroupBy} nameMap={nameMap} granularity={effectiveGranularity} formatValue={tooltipFmt} />} isAnimationActive={false} />
              <Legend content={<ChartLegend legendKeys={legendKeys} unknownActive={unknownActive} colorMap={colorMap} formatLabel={legendFormatter} onItemEnter={handleLegendItemEnter} onItemLeave={handleLegendLeave} />} />
              {noData && <Area type="monotone" dataKey="_anchor" stroke="transparent" fill="transparent" isAnimationActive={false} />}
              {renderKeys.map(m => (
                <Area key={seriesKey(m)} type="monotone" dataKey={m} stackId="1" stroke="none" fill={m === UNKNOWN_KEY ? UNKNOWN_FILL : colorMap[m]} fillOpacity={unknownOpacity(m, focusedKey, 0.85, 0.95)} dot={false} activeDot={false} isAnimationActive={false} />
              ))}
              {dragStart && dragEnd && (
                <ReferenceArea
                  x1={dragStart < dragEnd ? dragStart : dragEnd}
                  x2={dragStart < dragEnd ? dragEnd : dragStart}
                  fill="var(--accent)"
                  fillOpacity={0.15}
                  strokeOpacity={0.3}
                />
              )}
              {activeSelectedPoint && <SelectionMarker date={activeSelectedPoint.date} />}
            </AreaChart>
          </ResponsiveContainer>
        ) : (
          <ResponsiveContainer width="100%" height={height}>
            <BarChart
              data={chartData}
              margin={{ top: 0, right: 0, bottom: 0, left: 0 }}
              onMouseDown={handleMouseDown}
              onMouseMove={handleMouseMove}
              onMouseUp={handleMouseUp}
              className={chartClassName}
              // CSS variable drives the dynamic drag cursor (Tailwind cannot express runtime values)
              style={chartCursorVar} // eslint-disable-line no-restricted-syntax
            >
              <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
              <XAxis dataKey="date" tick={{ fontSize: 10 }} tickFormatter={(v) => formatTrendTick(effectiveGranularity, v)} interval={tickInterval} />
              <YAxis tick={{ fontSize: 10 }} tickFormatter={yAxisFmt} />
              <Tooltip content={<TrendTooltip groupBy={effectiveGroupBy} nameMap={nameMap} granularity={effectiveGranularity} formatValue={tooltipFmt} />} isAnimationActive={false} />
              <Legend content={<ChartLegend legendKeys={legendKeys} unknownActive={unknownActive} colorMap={colorMap} formatLabel={legendFormatter} onItemEnter={handleLegendItemEnter} onItemLeave={handleLegendLeave} />} />
              {noData && <Bar dataKey="_anchor" fill="transparent" isAnimationActive={false} />}
              {renderKeys.map((m, i) => (
                <Bar key={seriesKey(m)} dataKey={m} stackId="a" fill={m === UNKNOWN_KEY ? UNKNOWN_FILL : colorMap[m]} fillOpacity={unknownOpacity(m, focusedKey, 1, 1)} radius={i === renderKeys.length - 1 ? [3, 3, 0, 0] : [0, 0, 0, 0]} isAnimationActive={false} />
              ))}
              {dragStart && dragEnd && (
                <ReferenceArea
                  x1={dragStart < dragEnd ? dragStart : dragEnd}
                  x2={dragStart < dragEnd ? dragEnd : dragStart}
                  fill="var(--accent)"
                  fillOpacity={0.15}
                  strokeOpacity={0.3}
                />
              )}
              {activeSelectedPoint && <SelectionMarker date={activeSelectedPoint.date} />}
            </BarChart>
          </ResponsiveContainer>
        )}
        <CoverageLine gap={coverageShown} trailingWeek={coverageRange.trailingWeek} pending={coverageWanted} />
      </div>}
    </div>
  );
}

export { TrendChart, bucketDate, shortUser, trendQueryEnabled, unknownSeriesKey, normalizeTrendProjectHashes, trendProjectScopeReady };
export type { Granularity };
