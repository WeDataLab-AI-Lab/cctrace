'use client';

import { useState } from 'react';
import { usePersistedState } from '@/lib/use-persisted-state';
import { useRouter } from 'next/navigation';
import { useQuery, useQueryClient, keepPreviousData } from '@tanstack/react-query';
import { fetchCostByUser, fetchUserNameMap } from '@/lib/api';
import { POLL_NORMAL, POLL_SLOW } from '@/lib/query-config';
import { useAccount } from '@/components/common/account-context';
import { useAgent } from '@/components/common/agent-context';
import type { CostSummary } from '@/lib/types';
import {
  dashboardQueriesEnabled,
  refreshActiveDashboardQueries,
  userFilterOptionsFromCostRows,
} from '@/lib/dashboard-queries';
import { TrendChart } from '@/components/common/trend-chart';
import type { TrendPointSelection, ViewMode } from '@/components/common/trend-chart';
import { matchCategory } from '@/lib/colors';
import type { ModelCategory } from '@/lib/colors';
import { shortModel } from '@/lib/model-label';
import { FilterBar } from '@/components/common/filter-bar';
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip';
import { WEEKLY_USAGE_HINT, WEEKLY_USAGE_KEY, isWeeklyUsageKey } from '@/lib/weekly-usage';
import { CollectingLoader } from '@/components/common/collecting-loader';
import {
  Table,
  TableBody,
  TableCell,
  TableFooter,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';

const daysAgo = (n: number) =>
  new Date(Date.now() - n * 86400 * 1000).toISOString();

const fmt = (n: number) =>
  n >= 1_000_000
    ? `${(n / 1_000_000).toFixed(1)}M`
    : n >= 1000
      ? `${(n / 1000).toFixed(0)}K`
      : String(n);

// The weekly report row belongs to no person: no user page to open and no profile to
// flag as unset, only an explanation of whose tokens these are. Focusable, so the
// explanation opens from the keyboard too; the tooltip describes the trigger while open.
const WeeklyUserLabel = () => (
  <TooltipProvider delayDuration={300}>
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={0} className="block font-medium text-ink text-sm cursor-help">{WEEKLY_USAGE_KEY}</span>
      </TooltipTrigger>
      <TooltipContent className="max-w-[260px]">{WEEKLY_USAGE_HINT}</TooltipContent>
    </Tooltip>
  </TooltipProvider>
);

export default function CostPage() {
  const router = useRouter();
  const [search, setSearch] = useState('');
  const [viewMode, setViewMode] = usePersistedState<ViewMode>('filter:viewMode', 'cost');
  const [modelCategory, setModelCategory] = usePersistedState<ModelCategory>('filter:modelCategory', 'all');
  const [groupBy, setGroupBy] = usePersistedState<'model' | 'user'>('filter:groupBy', 'model');
  const [modelFilter, setModelFilter] = usePersistedState('filter:modelFilter', '__all__');
  const [userFilter, setUserFilter] = usePersistedState('filter:userFilter', '__all__');
  // Date.now() 기반 값은 매 렌더 변동을 막기 위해 최초 1회만 계산(lazy init).
  const [since] = useState(() => daysAgo(30));
  const [selectedSince, setSelectedSince] = useState('');
  const [selectedUntil, setSelectedUntil] = useState<string | undefined>(undefined);
  const { selectedAccount, isHydrated: isAccountHydrated } = useAccount();
  const { isHydrated: isAgentHydrated } = useAgent();
  const queriesEnabled = dashboardQueriesEnabled(isAccountHydrated, isAgentHydrated);
  const queryClient = useQueryClient();

  const handleTrendRangeSelect = (nextSince: string, nextUntil: string) => {
    setSelectedSince(nextSince);
    setSelectedUntil(nextUntil);
  };
  const handleTrendPointSelect = (point: TrendPointSelection) => handleTrendRangeSelect(point.since, point.until);
  const activeSince = selectedSince || since;

  const { data, isLoading } = useQuery<CostSummary[]>({
    queryKey: ['cost-by-user', activeSince, selectedUntil, selectedAccount],
    queryFn: () => fetchCostByUser(activeSince, selectedUntil, undefined, selectedAccount || undefined),
    refetchInterval: POLL_NORMAL,
    // The trend range is part of the queryKey; without this every brush/point selection
    // empties the table until the new range lands.
    placeholderData: keepPreviousData,
    enabled: queriesEnabled,
  });

  const rows = data ?? [];

  const { data: nameMap = {} } = useQuery<Record<string, string>>({
    queryKey: ['user-name-map'],
    queryFn: fetchUserNameMap,
    staleTime: Infinity,
    refetchInterval: POLL_SLOW,
  });

  const users = userFilterOptionsFromCostRows(rows);

  const modelTotals = new Map<string, number>();
  for (const c of rows) {
    const short = shortModel(c.model);
    if (short) modelTotals.set(short, (modelTotals.get(short) || 0) + c.total_cost);
  }
  const models = [...modelTotals.entries()]
    .filter(([, v]) => v > 0)
    .sort((a, b) => b[1] - a[1])
    .map(([m]) => m);

  const mergedByKey = rows.reduce<Record<string, CostSummary>>((acc, c) => {
    // Include billing_provider in key to prevent cross-provider summing.
    const key = `${c.profile_email}__${c.billing_provider ?? 'anthropic'}__${c.model}`;
    if (!acc[key]) {
      acc[key] = { ...c };
    } else {
      acc[key].total_cost += c.total_cost;
      acc[key].total_input_tokens += c.total_input_tokens;
      acc[key].total_output_tokens += c.total_output_tokens;
      acc[key].request_count += c.request_count;
    }
    return acc;
  }, {});
  const sorted = Object.values(mergedByKey).sort((a, b) => b.total_cost - a.total_cost);

  const categorySorted =
    modelCategory === 'all'
      ? sorted
      : sorted.filter((c) => matchCategory(c.agent, c.billing_provider, modelCategory));

  const filtered = categorySorted.filter((c) => {
    if (modelFilter && modelFilter !== '__all__' && shortModel(c.model) !== modelFilter) return false;
    if (userFilter && userFilter !== '__all__' && c.user_id !== userFilter) return false;
    if (search) {
      const q = search.toLowerCase();
      return c.profile_email.toLowerCase().includes(q) || c.model.toLowerCase().includes(q);
    }
    return true;
  });

  const totals = filtered.reduce(
    (acc, c) => ({
      requests: acc.requests + c.request_count,
      input: acc.input + c.total_input_tokens,
      output: acc.output + c.total_output_tokens,
      cost: acc.cost + c.total_cost,
    }),
    { requests: 0, input: 0, output: 0, cost: 0 }
  );

  const handleRefresh = () => refreshActiveDashboardQueries(queryClient);

  return (
    <section className="space-y-6">
      <h2 className="text-[16px] font-semibold text-ink">Usage Details</h2>

      <FilterBar
        viewMode={viewMode}
        onViewModeChange={setViewMode}
        modelCategory={modelCategory}
        onModelCategoryChange={setModelCategory}
        groupBy={groupBy}
        onGroupByChange={setGroupBy}
        onRefresh={handleRefresh}
        search={search}
        onSearchChange={setSearch}
        showSearch={true}
        searchPlaceholder="Search user or model..."
        modelFilter={modelFilter}
        onModelFilterChange={setModelFilter}
        models={models}
        showModelFilter={true}
        userFilter={userFilter}
        onUserFilterChange={setUserFilter}
        users={users}
        showUserFilter={true}
      />

      <TrendChart
        filterAccount={selectedAccount || undefined}
        viewMode={viewMode}
        onViewModeChange={setViewMode}
        showViewToggle={false}
        showModelFilter={true}
        modelCategory={modelCategory}
        groupBy={groupBy}
        className="bg-surface rounded-lg border border-border p-6"
        modelFilter={modelFilter}
        onModelFilterChange={setModelFilter}
        userFilter={userFilter}
        onUserFilterChange={setUserFilter}
        nameMap={nameMap}
        onPointSelect={handleTrendPointSelect}
        onRangeSelect={handleTrendRangeSelect}
        queriesEnabled={queriesEnabled}
      />

      <div className="bg-surface rounded-lg border border-border overflow-hidden">
        <Table className="text-sm">
          <TableHeader className="bg-canvas text-ink-3 text-xs uppercase">
            <TableRow className="hover:bg-transparent border-0">
              <TableHead className="px-4 py-3 text-left text-ink-3">User</TableHead>
              <TableHead className="px-4 py-3 text-left text-ink-3">Model</TableHead>
              <TableHead className="px-4 py-3 text-right text-ink-3">Requests</TableHead>
              <TableHead className="px-4 py-3 text-right text-ink-3">Input Tokens</TableHead>
              <TableHead className="px-4 py-3 text-right text-ink-3">Output Tokens</TableHead>
              <TableHead className="px-4 py-3 text-right text-ink-3">
                {viewMode === 'token' ? 'Total Tokens' : 'Cost'}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {isLoading ? (
              <TableRow>
                <TableCell colSpan={6} className="px-4 py-8 text-center text-ink-3">
                  <CollectingLoader />
                </TableCell>
              </TableRow>
            ) : filtered.length === 0 ? (
              <TableRow>
                <TableCell colSpan={6} className="px-4 py-8 text-center text-ink-3">
                  No data
                </TableCell>
              </TableRow>
            ) : (
              filtered.map((c) => (
                <TableRow
                  key={`${c.profile_email}-${c.model}`}
                  className="hover:bg-canvas border-b border-border/50"
                >
                  <TableCell className="px-4 py-3">
                    {isWeeklyUsageKey(c.user_id) ? <WeeklyUserLabel /> : (
                    <button
                      type="button"
                      className="cursor-pointer hover:opacity-80 transition-opacity inline-block text-left"
                      onClick={() => router.push(`/users?user=${encodeURIComponent(c.profile_email || '')}`)}
                    >
                      <span className="block font-medium text-ink text-sm">
                        {c.user_id && nameMap[c.user_id] ? (
                          <>
                            <span className="font-semibold">{nameMap[c.user_id]}</span>{' '}
                            <span className="text-ink-3 text-xs font-normal">({c.user_id})</span>
                          </>
                        ) : c.user_id ? (
                          c.user_id.slice(0, 8)
                        ) : (
                          '-'
                        )}
                        {!c.profile_email && (
                          <span className="ml-2 text-[10px] font-medium px-1.5 py-0.5 rounded bg-warning-soft text-warning-strong border border-warning/40">
                            미설정
                          </span>
                        )}
                      </span>
                      <span className="block text-[11px] text-ink-3">{c.profile_email || '-'}</span>
                    </button>
                    )}
                  </TableCell>
                  <TableCell className="px-4 py-3 text-ink-2 text-xs">{c.model}</TableCell>
                  <TableCell className="px-4 py-3 text-right text-ink-2">
                    {c.request_count.toLocaleString()}
                  </TableCell>
                  <TableCell className="px-4 py-3 text-right text-ink-2">{fmt(c.total_input_tokens)}</TableCell>
                  <TableCell className="px-4 py-3 text-right text-ink-2">{fmt(c.total_output_tokens)}</TableCell>
                  <TableCell className="px-4 py-3 text-right font-medium text-ink">
                    {viewMode === 'token'
                      ? fmt(c.total_input_tokens + c.total_output_tokens)
                      : `$${c.total_cost.toFixed(4)}`}
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
          {filtered.length > 0 && (
            <TableFooter className="bg-canvas font-semibold text-ink">
              <TableRow className="hover:bg-transparent">
                <TableCell className="px-4 py-3" colSpan={2}>
                  Total ({filtered.length})
                </TableCell>
                <TableCell className="px-4 py-3 text-right">{totals.requests.toLocaleString()}</TableCell>
                <TableCell className="px-4 py-3 text-right">{fmt(totals.input)}</TableCell>
                <TableCell className="px-4 py-3 text-right">{fmt(totals.output)}</TableCell>
                <TableCell className="px-4 py-3 text-right">
                  {viewMode === 'token' ? fmt(totals.input + totals.output) : `$${totals.cost.toFixed(4)}`}
                </TableCell>
              </TableRow>
            </TableFooter>
          )}
        </Table>
      </div>
    </section>
  );
}
