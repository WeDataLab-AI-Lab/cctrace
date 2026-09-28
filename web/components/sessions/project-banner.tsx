'use client';

import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { TrendChart } from '@/components/common/trend-chart';
import type { TrendPointSelection } from '@/components/common/trend-chart';
import { fetchSessionOverview } from '@/lib/api';
import { cn } from '@/lib/utils';
import { usePersistedState } from '@/lib/use-persisted-state';
import { projectIdentityKey, projectIdentityLabel } from '@/lib/project-identity';
import type { Project, SessionOverview } from '@/lib/types';
import { POLL_NORMAL } from '@/lib/query-config';
import { fmt } from './session-utils';
import { PROJECT_TREND_COLLAPSED_KEY, projectTrendView } from './project-trend';

interface ProjectBannerProps {
  selectedProject: string;
  selectedAccount: string;
  projects: Project[];
  filtered: SessionOverview[];
  assembled: boolean;
  source?: string;
  agent?: string;
}

interface ProjectBannerRangeQueryInput {
  selectedProject: string;
  selectedAccount: string;
  projectHashes: readonly string[];
  since: string;
  until?: string;
  assembled: boolean;
  source?: string;
  agent?: string;
}

const dateFmt = (d: Date): string => d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });

const projectBannerLabel = (project: Project): string => projectIdentityLabel(project);

const projectBannerProjectHashes = (projects: Project[], selectedProject: string): string[] =>
  projects
    .filter(project => projectIdentityKey(project) === selectedProject)
    .map(project => project.project_hash)
    .filter(Boolean);

const projectBannerRangeQuery = (scope: ProjectBannerRangeQueryInput) => ({
  queryKey: [
    'project-sessions-range',
    scope.selectedProject,
    scope.selectedAccount,
    scope.projectHashes.join(','),
    scope.source ?? '',
    scope.agent ?? '',
    scope.assembled,
    scope.since,
    scope.until ?? '',
  ] as const,
  request: {
    loginEmail: scope.selectedAccount || undefined,
    projectHashes: scope.projectHashes,
    since: scope.since,
    until: scope.until,
    limit: 1000,
    assembled: scope.assembled,
    source: scope.source || undefined,
    agent: scope.agent || undefined,
  },
});

const ProjectBanner = ({ selectedProject, selectedAccount, projects, filtered, assembled, source, agent }: ProjectBannerProps) => {
  // Collapsed by default. Picking a project is usually a step toward finding a
  // session, not toward reading a cost chart, and the chart pushed the list down on
  // every selection. Collapsed it also costs nothing: the range query below is gated
  // on a range being selected, and no range can be selected while it is closed.
  //
  // usePersistedState only writes once the value is actually toggled, so this changes
  // the default for people who never chose, and leaves the choice alone for those who
  // did.
  const [isTrendCollapsed, setIsTrendCollapsed] = usePersistedState(PROJECT_TREND_COLLAPSED_KEY, true);
  const [selectedSince, setSelectedSince] = useState('');
  const [selectedUntil, setSelectedUntil] = useState<string | undefined>(undefined);
  // selectedProject is a project identity (see lib/project-identity); resolve it
  // to a representative project from the registry.
  const proj = projects.find(p => projectIdentityKey(p) === selectedProject);
  const projectHashes = projectBannerProjectHashes(projects, selectedProject);
  const hasSelectedRange = selectedSince !== '';
  const rangeQuery = projectBannerRangeQuery({
    selectedProject,
    selectedAccount,
    projectHashes,
    since: selectedSince,
    until: selectedUntil,
    assembled,
    source,
    agent,
  });
  const { data: selectedSessions = [] } = useQuery<SessionOverview[]>({
    queryKey: rangeQuery.queryKey,
    queryFn: () => fetchSessionOverview(rangeQuery.request),
    refetchInterval: POLL_NORMAL,
    enabled: hasSelectedRange && !!proj,
  });
  const scopedFiltered = hasSelectedRange ? selectedSessions : filtered;
  const handleTrendRangeSelect = (since: string, until: string) => {
    setSelectedSince(since);
    setSelectedUntil(until);
  };
  const handleTrendPointSelect = (point: TrendPointSelection) => handleTrendRangeSelect(point.since, point.until);
  const trendView = projectTrendView(isTrendCollapsed);
  const toggleTrend = () => setIsTrendCollapsed(collapsed => !collapsed);

  if (!proj) return null;

  const totalCost = scopedFiltered.reduce((sum, s) => sum + (s.cost_usd ?? 0), 0);
  const totalInput = scopedFiltered.reduce((sum, s) => sum + (s.input_tokens ?? 0), 0);
  const totalOutput = scopedFiltered.reduce((sum, s) => sum + (s.output_tokens ?? 0), 0);
  const earliest = scopedFiltered.length > 0 ? new Date(Math.min(...scopedFiltered.map(s => new Date(s.start_time).getTime()))) : null;
  const latest = scopedFiltered.length > 0 ? new Date(Math.max(...scopedFiltered.map(s => new Date(s.end_time).getTime()))) : null;
  const hasRemote = !!proj.git_remote_url;
  const users = [...new Set(scopedFiltered.map(s => s.user_id).filter(Boolean))];

  // 세로가 좁으면 이 배너가 대화에 쓸 공간을 먼저 가져간다. 차트는 이미 접힌 상태가
  // 기본(PROJECT_TREND_COLLAPSED_KEY)이므로 줄일 것은 본체 쪽이다.
  return (
    <div className="mb-3 px-4 py-3 short:mb-2 short:py-2 bg-canvas rounded-lg border border-border flex-shrink-0">
      <div className="flex items-center gap-3 mb-2 short:mb-1">
        <span className="text-sm font-medium text-ink">{projectBannerLabel(proj)}</span>
        <span className={cn(
          'inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium',
          hasRemote ? 'bg-success-soft text-success-strong' : 'bg-surface-sunk text-ink-3',
        )}>
          {hasRemote ? 'remote' : 'local'}
        </span>
        {hasRemote && (
          <span className="text-[11px] text-ink-3 truncate max-w-[400px]">{proj.git_remote_url}</span>
        )}
      </div>
      <div className="flex items-center gap-5 text-xs text-ink-3">
        <span><span className="text-ink-2 font-medium">{scopedFiltered.length}</span> sessions</span>
        <span>Cost: <span className="text-ink-2 font-medium">${totalCost.toFixed(2)}</span></span>
        <span>In: <span className="text-ink-2 font-medium">{fmt(totalInput)}</span></span>
        <span>Out: <span className="text-ink-2 font-medium">{fmt(totalOutput)}</span></span>
        {earliest && latest && (
          <span>{dateFmt(earliest)} — {dateFmt(latest)}</span>
        )}
      </div>
      {users.length > 0 && (
        <div className="flex items-center gap-2 mt-2 text-xs text-ink-3 short:hidden">
          <span>Users:</span>
          {users.map(u => (
            <span key={u} className="inline-flex items-center px-1.5 py-0.5 rounded bg-surface-sunk text-ink-2 text-[11px]">{u}</span>
          ))}
        </div>
      )}
      {/* Project trend chart */}
      <div className="mt-3 pt-3 short:mt-2 short:pt-2 border-t border-border/50">
        <TrendChart
          projectHash={proj.project_hash}
          projectHashes={projectHashes}
          filterAccount={selectedAccount || undefined}
          height={160}
          showViewToggle={true}
          showModelFilter={false}
          defaultGranularity="hour"
          collapsed={!trendView.chartVisible}
          collapseLabel={trendView.toggleLabel}
          onCollapseToggle={toggleTrend}
          onPointSelect={handleTrendPointSelect}
          onRangeSelect={handleTrendRangeSelect}
        />
      </div>
    </div>
  );
};

export { ProjectBanner, projectBannerLabel, projectBannerProjectHashes, projectBannerRangeQuery };
