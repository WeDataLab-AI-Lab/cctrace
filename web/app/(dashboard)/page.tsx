'use client';

import { useQuery, useQueryClient, keepPreviousData } from '@tanstack/react-query';
import { useState } from 'react';
import { usePersistedState } from '@/lib/use-persisted-state';
import { POLL_NORMAL, POLL_SLOW } from '@/lib/query-config';
import { TrendChart } from '@/components/common/trend-chart';
import { UsageChart } from '@/components/common/usage-chart';
import type { Granularity, TrendPointSelection, ViewMode } from '@/components/common/trend-chart';
import { FilterBar } from '@/components/common/filter-bar';
import {
  fetchCostByUser,
  fetchCostByModel,
  fetchUserNameMap,
} from '@/lib/api';
import { useAccount } from '@/components/common/account-context';
import { useAgent } from '@/components/common/agent-context';
import { matchAgentScope } from '@/lib/colors';
import type { ModelCategory } from '@/lib/colors';
import type { CostSummary, ModelStat } from '@/lib/types';
import {
  dashboardQueriesEnabled,
  overviewSummaryQueriesEnabled,
  refreshActiveDashboardQueries,
  countActiveUsers,
  userFilterOptionsFromCostRows,
} from '@/lib/dashboard-queries';
import { SummaryCards } from '@/components/overview/summary-cards';
import { ModelPieChart } from '@/components/overview/model-pie-chart';
import { UserStackChart } from '@/components/overview/user-stack-chart';
import { buildCostModelData, buildCostUserStackData } from '@/components/overview/cost-user-stack';
import {
  matchCategory,
  subtitleFromSince,
} from '@/components/overview/overview-helpers';
// Filter VALUE uses shortModel (server-matchable via raw `model LIKE`); the
// dropdown/chart DISPLAY renders modelLabel. Using modelLabel as the value broke
// Claude filters because the server LIKEs the raw id (#91 follow-up).
import { shortModel } from '@/lib/model-label';

export default function OverviewPage() {
  const [viewMode, setViewMode] = usePersistedState<ViewMode>('filter:viewMode', 'cost');
  const [modelCategory, setModelCategory] = usePersistedState<ModelCategory>('filter:modelCategory', 'all');
  const [groupBy, setGroupBy] = usePersistedState<'model' | 'user'>('filter:groupBy', 'user');
  const [modelFilter, setModelFilter] = usePersistedState('filter:modelFilter', '__all__');
  const [userFilter, setUserFilter] = usePersistedState('filter:userFilter', '__all__');
  // Date.now() 기반 값은 매 렌더 변동 → queryKey 흔들림(refetch 루프)을 막기 위해 최초 1회만 계산(lazy init).
  const [since30d] = useState(() => new Date(Date.now() - 30 * 86400 * 1000).toISOString());
  const [now] = useState(() => Date.now());
  const { selectedAccount, isHydrated: isAccountHydrated } = useAccount();
  const { selectedAgent, isHydrated: isAgentHydrated } = useAgent();
  const queriesEnabled = dashboardQueriesEnabled(isAccountHydrated, isAgentHydrated);
  const [trendSince, setTrendSince] = useState<string>('');
  const [trendUntil, setTrendUntil] = useState<string | undefined>(undefined);
  const [trendGranularity, setTrendGranularity] = useState<Granularity>('minute');
  const [selectedTrendPoint, setSelectedTrendPoint] = useState<TrendPointSelection | null>(null);
  const queryClient = useQueryClient();
  const summaryQueriesEnabled = overviewSummaryQueriesEnabled(queriesEnabled, trendSince);

  const handleTimeRangeChange = (since: string, until: string | undefined, granularity: Granularity) => {
    setTrendSince(since);
    setTrendUntil(until);
    setTrendGranularity(granularity);
    setSelectedTrendPoint(null);
  };

  const handleTrendPointSelect = (point: TrendPointSelection) => setSelectedTrendPoint(point);
  const handleTrendPointClear = () => setSelectedTrendPoint(null);

  const activeSince = trendSince || since30d;
  const costSince = selectedTrendPoint?.since ?? activeSince;
  const costUntil = selectedTrendPoint?.until ?? trendUntil;
  const trendSubtitle = selectedTrendPoint
    ? `Selected ${selectedTrendPoint.date.replace('T', ' ')}`
    : subtitleFromSince(activeSince, now);

  const { data: costData = [], isPending: isCostDataPending } = useQuery<CostSummary[]>({
    queryKey: ['cost-by-user', costSince, costUntil, selectedAccount],
    queryFn: () => fetchCostByUser(costSince, costUntil, undefined, selectedAccount || undefined),
    staleTime: Infinity,
    refetchInterval: POLL_NORMAL,
    placeholderData: keepPreviousData,
    enabled: summaryQueriesEnabled,
  });
  const activeUserFilter = groupBy === 'user' && userFilter !== '__all__' ? userFilter : undefined;
  const { data: modelData = [], isPending: isModelDataPending } = useQuery<ModelStat[]>({
    queryKey: ['cost-by-model', costSince, costUntil, selectedAccount, activeUserFilter],
    queryFn: () => fetchCostByModel(costSince, costUntil, undefined, selectedAccount || undefined, activeUserFilter),
    staleTime: Infinity,
    refetchInterval: POLL_NORMAL,
    placeholderData: keepPreviousData,
    enabled: summaryQueriesEnabled,
  });

  const { data: nameMap = {} } = useQuery<Record<string, string>>({
    queryKey: ['user-name-map'],
    queryFn: fetchUserNameMap,
    staleTime: Infinity,
    refetchInterval: POLL_SLOW,
  });

  const handleRefresh = () => refreshActiveDashboardQueries(queryClient);

  const users = userFilterOptionsFromCostRows(costData);

  // Dropdown options must reflect the agent + category toggle too (not just the charts).
  // Do not filter by modelFilter here, or the option list collapses to the one selection.
  const categoryModelData = modelData.filter((m) =>
    matchAgentScope(m.agent, selectedAgent) &&
    (modelCategory === 'all' ||
      matchCategory(m.agent ?? 'claude', m.billing_provider ?? 'anthropic', modelCategory)),
  );
  const modelTotals = new Map<string, number>();
  for (const m of categoryModelData) {
    const short = shortModel(m.model);
    if (short) modelTotals.set(short, (modelTotals.get(short) || 0) + m.total_cost);
  }
  const models = [...modelTotals.entries()]
    .filter(([, v]) => v > 0)
    .sort((a, b) => b[1] - a[1])
    .map(([m]) => m);

  let filteredCostData = costData;
  if (selectedAgent) filteredCostData = filteredCostData.filter((c) => matchAgentScope(c.agent, selectedAgent));
  if (modelCategory !== 'all') {
    filteredCostData = filteredCostData.filter((c) =>
      matchCategory(c.agent ?? 'claude', c.billing_provider ?? 'anthropic', modelCategory),
    );
  }
  if (modelFilter && modelFilter !== '__all__') {
    filteredCostData = filteredCostData.filter((c) => shortModel(c.model) === modelFilter);
  }
  // userFilter는 by-User 모드 전용 선택이다. groupBy 무관하게 적용하면 by-Model 모드에서
  // 숨은 채로 costData(카드·차트) 전체를 걸러 "No data"가 된다 — model 쿼리의 activeUserFilter 게이팅과 일치시킨다.
  if (activeUserFilter) {
    filteredCostData = filteredCostData.filter((c) => c.user_id === activeUserFilter);
  }

  let filteredModelData = modelData;
  if (selectedAgent) filteredModelData = filteredModelData.filter((m) => matchAgentScope(m.agent, selectedAgent));
  if (modelCategory !== 'all') {
    filteredModelData = filteredModelData.filter((m) =>
      matchCategory(m.agent ?? 'claude', m.billing_provider ?? 'anthropic', modelCategory),
    );
  }
  if (modelFilter && modelFilter !== '__all__') {
    filteredModelData = filteredModelData.filter((m) => shortModel(m.model) === modelFilter);
  }

  const totalCost = filteredCostData.reduce((s, c) => s + c.total_cost, 0);
  const totalInput = filteredCostData.reduce((s, c) => s + c.total_input_tokens, 0);
  const totalOutput = filteredCostData.reduce((s, c) => s + c.total_output_tokens, 0);
  const activeUsers = countActiveUsers(filteredCostData);
  const totalEvents = filteredCostData.reduce((s, c) => s + (c.request_count || 0), 0);

  const { data: userStackData, stackKeys } = buildCostUserStackData({
    rows: filteredCostData,
    nameMap,
    viewMode,
  });
  const { data: modelPieData, otherNames } = buildCostModelData({
    rows: filteredModelData,
    viewMode,
  });

  return (
    <div className="space-y-6">
      <SummaryCards
        viewMode={viewMode}
        totalCost={totalCost}
        totalTokens={totalInput + totalOutput}
        activeUsers={activeUsers}
        totalEvents={totalEvents}
        subtitle={trendSubtitle}
      />

      <FilterBar
        viewMode={viewMode}
        onViewModeChange={setViewMode}
        modelCategory={modelCategory}
        onModelCategoryChange={setModelCategory}
        groupBy={groupBy}
        onGroupByChange={setGroupBy}
        onRefresh={handleRefresh}
        modelFilter={modelFilter}
        onModelFilterChange={setModelFilter}
        models={models}
        showModelFilter={true}
        userFilter={userFilter}
        onUserFilterChange={setUserFilter}
        users={users}
        showUserFilter={true}
        nameMap={nameMap}
      />

      <section className="bg-surface rounded-lg border border-border p-6 mt-2">
        <TrendChart
          showViewToggle={false}
          viewMode={viewMode}
          onViewModeChange={setViewMode}
          showModelFilter={true}
          modelCategory={modelCategory}
          filterAccount={selectedAccount || undefined}
          height={250}
          groupBy={groupBy}
          modelFilter={modelFilter}
          onModelFilterChange={setModelFilter}
          userFilter={userFilter}
          onUserFilterChange={setUserFilter}
          nameMap={nameMap}
          onTimeRangeChange={handleTimeRangeChange}
          selectedPoint={selectedTrendPoint}
          onPointSelect={handleTrendPointSelect}
          onPointClear={handleTrendPointClear}
          queriesEnabled={queriesEnabled}
          showCoverage
        />
      </section>

      {/* The card lives inside UsageChart: collapsed it is one line and wants
          tight padding, expanded it is a plot and wants the full card.

          The height is the expanded plot's, not the collapsed bar's. It was cut
          to 64px once on the reading that the section was too tall; the tall part
          was the collapsed bar's padding, which is now handled above, and at 64px
          the y axis had room for a single tick. */}
      <UsageChart
        height={250}
        heightClass="h-[250px]"
        queriesEnabled={queriesEnabled}
        since={activeSince}
        until={trendUntil}
        granularity={trendGranularity}
        accountEmail={selectedAccount}
        agent={selectedAgent}
      />

      <div className="grid grid-cols-2 gap-4 items-stretch">
        <ModelPieChart viewMode={viewMode} data={modelPieData} otherNames={otherNames} isLoading={isModelDataPending} />
        <UserStackChart viewMode={viewMode} data={userStackData} stackKeys={stackKeys} isLoading={isCostDataPending} />
      </div>
    </div>
  );
}
