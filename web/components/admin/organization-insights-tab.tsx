'use client';

import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertCircle, LockKeyhole } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { backfillTaskTypes, fetchOrganizationInsights, fetchProjects } from '@/lib/api';
import { projectAggregateKey, projectAggregateLabel } from '@/lib/project-aggregate';
import type { OrganizationInsightProject, OrganizationInsights, Project } from '@/lib/types';

const numberFormat = new Intl.NumberFormat('en-US');
const PROJECT_REGISTRY_ERROR_MESSAGE = '프로젝트 정보를 불러오지 못했습니다.';

const formatTokens = (tokens: number) => {
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(1)}M`;
  if (tokens >= 1_000) return `${(tokens / 1_000).toFixed(1)}K`;
  return numberFormat.format(tokens);
};

const EmptyRows = ({ children }: { readonly children: React.ReactNode }) => (
  <p className="px-4 py-5 text-center text-[13px] text-ink-3">{children}</p>
);

interface OrganizationProjectView {
  key: string;
  label: string;
  contributor_count: number;
  session_count: number;
  total_tokens: number;
}

interface OrganizationProjectRowProps {
  project: OrganizationInsightProject;
  registry: readonly Project[];
}

interface OrganizationProjectListProps {
  projects: readonly OrganizationInsightProject[];
  registry: readonly Project[];
  loading: boolean;
  registryError: boolean;
}

const organizationProjectView = (
  project: OrganizationInsightProject,
  registry: readonly Project[],
): OrganizationProjectView => ({
  key: projectAggregateKey(project, registry),
  label: projectAggregateLabel(project, registry),
  contributor_count: project.contributor_count,
  session_count: project.session_count,
  total_tokens: project.total_tokens,
});

const OrganizationProjectRow = ({ project, registry }: OrganizationProjectRowProps) => {
  const view = organizationProjectView(project, registry);
  return (
    <div className="flex justify-between gap-4 border-b border-border px-4 py-3 text-[13px] last:border-0">
      <span className="truncate font-mono text-ink">{view.label}</span>
      <span className="shrink-0 tabular-nums text-ink-2">{formatTokens(view.total_tokens)} tokens</span>
    </div>
  );
};

const OrganizationProjectList = ({ projects, registry, loading, registryError }: OrganizationProjectListProps) => {
  if (registryError) return <EmptyRows>{PROJECT_REGISTRY_ERROR_MESSAGE}</EmptyRows>;
  if (loading) return <EmptyRows>프로젝트 정보를 불러오는 중입니다.</EmptyRows>;
  if (projects.length === 0) return <EmptyRows>기여자 기준을 충족한 프로젝트가 없습니다.</EmptyRows>;
  return projects.slice(0, 8).map((project) => (
    <OrganizationProjectRow key={organizationProjectView(project, registry).key} project={project} registry={registry} />
  ));
};

const OrganizationInsightsTab = () => {
  const [until] = useState(() => new Date().toISOString());
  const [since] = useState(() => new Date(Date.now() - 7 * 86400 * 1000).toISOString());
  const { data, isLoading, isError } = useQuery<OrganizationInsights>({
    queryKey: ['organization-insights', since, until],
    queryFn: () => fetchOrganizationInsights(since, until),
  });
  const {
    data: projectRegistry = [],
    isLoading: isProjectRegistryLoading,
    isError: isProjectRegistryError,
  } = useQuery<Project[]>({
    queryKey: ['projects', 'identity-registry'],
    queryFn: () => fetchProjects(),
    staleTime: 60_000,
  });
  const queryClient = useQueryClient();
  // Bounded per call (server caps at 1000 rows) -- the button label below reports
  // that call's count, and a re-click resumes rather than looping automatically.
  // Previously only reachable via curl; the report page has said "can be
  // backfilled by an administrator" since the coverage gap became visible on
  // screen, with no button behind it.
  const backfillMutation = useMutation({
    mutationFn: backfillTaskTypes,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['organization-insights'] });
      queryClient.invalidateQueries({ queryKey: ['weekly-insights'] });
    },
  });

  const backfillButton = (
    <div className="flex items-center gap-3">
      <Button variant="outline" size="sm" onClick={() => backfillMutation.mutate()} disabled={backfillMutation.isPending}>
        {backfillMutation.isPending ? '분류 중…' : '이전 프롬프트 분류하기'}
      </Button>
      {backfillMutation.isSuccess && (
        <span className="text-[12px] text-ink-3">{numberFormat.format(backfillMutation.data.updated)}건 분류함 -- 남은 것이 있으면 다시 눌러주세요.</span>
      )}
      {backfillMutation.isError && (
        <span className="text-[12px] text-danger">{backfillMutation.error instanceof Error ? backfillMutation.error.message : '분류에 실패했습니다.'}</span>
      )}
    </div>
  );

  if (isError) {
    return (
      <div className="space-y-3">
        {backfillButton}
        <p className="flex items-center gap-2 text-[13px] text-danger"><AlertCircle size={15} /> 조직 집계를 불러오지 못했습니다.</p>
      </div>
    );
  }

  if (data && !data.available) {
    return (
      <div className="space-y-3">
        {backfillButton}
        <div className="rounded-lg border border-border bg-surface px-4 py-5 text-[13px] text-ink-2">
          <div className="flex items-center gap-2 font-medium text-ink"><LockKeyhole size={16} /> 익명 집계 보호</div>
          <p className="mt-2">최근 7일 동안 식별 가능한 기여자가 {data.minimum_users}명 이상일 때만 조직 집계를 표시합니다.</p>
        </div>
      </div>
    );
  }

  const tasks = data?.tasks ?? [];
  const hours = data?.hours ?? [];
  const projects = data?.projects ?? [];
  const bottlenecks = data?.bottlenecks ?? [];

  return (
    <div className="space-y-5">
      {backfillButton}
      <div className="rounded-lg border border-border bg-surface px-4 py-3 text-[13px] text-ink-2">
        {isLoading ? '조직 집계를 불러오는 중입니다.' : `최근 7일, ${numberFormat.format(data?.active_users ?? 0)}명 이상 기여자 기반 집계입니다. 개인·세션·프롬프트 원문은 표시하지 않습니다.`}
      </div>

      <div className="grid gap-5 xl:grid-cols-2">
        <section className="rounded-lg border border-border bg-surface">
          <div className="border-b border-border px-4 py-3"><h3 className="text-[14px] font-semibold text-ink">작업 유형</h3></div>
          {tasks.length === 0 ? <EmptyRows>표시 가능한 작업 유형이 없습니다.</EmptyRows> : tasks.map((task) => (
            <div key={task.task_type} className="flex justify-between border-b border-border px-4 py-3 text-[13px] last:border-0">
              <span className="capitalize text-ink">{task.task_type}</span><span className="tabular-nums text-ink-2">{numberFormat.format(task.prompt_count)} prompts</span>
            </div>
          ))}
        </section>
        <section className="rounded-lg border border-border bg-surface">
          <div className="border-b border-border px-4 py-3"><h3 className="text-[14px] font-semibold text-ink">활동 시간대</h3></div>
          {hours.length === 0 ? <EmptyRows>표시 가능한 활동 시간대가 없습니다.</EmptyRows> : hours.slice(0, 8).map((hour) => (
            <div key={hour.hour} className="flex justify-between border-b border-border px-4 py-3 text-[13px] last:border-0">
              <span className="text-ink">{String(hour.hour).padStart(2, '0')}:00 UTC</span><span className="tabular-nums text-ink-2">{numberFormat.format(hour.session_count)} sessions</span>
            </div>
          ))}
        </section>
        <section className="rounded-lg border border-border bg-surface">
          <div className="border-b border-border px-4 py-3"><h3 className="text-[14px] font-semibold text-ink">공유 프로젝트</h3></div>
          <OrganizationProjectList
            projects={projects}
            registry={projectRegistry}
            loading={isProjectRegistryLoading}
            registryError={isProjectRegistryError}
          />
        </section>
        <section className="rounded-lg border border-border bg-surface">
          <div className="border-b border-border px-4 py-3"><h3 className="text-[14px] font-semibold text-ink">도구 병목</h3></div>
          {bottlenecks.length === 0 ? <EmptyRows>기여자 기준을 충족한 도구 실패가 없습니다.</EmptyRows> : bottlenecks.slice(0, 8).map((tool) => (
            <div key={tool.tool_name} className="flex justify-between gap-4 border-b border-border px-4 py-3 text-[13px] last:border-0">
              <span className="text-ink">{tool.tool_name}</span><span className="shrink-0 tabular-nums text-ink-2">{numberFormat.format(tool.fail_count)}/{numberFormat.format(tool.use_count)} failed</span>
            </div>
          ))}
        </section>
      </div>
    </div>
  );
};

export {
  OrganizationInsightsTab,
  OrganizationProjectList,
  OrganizationProjectRow,
  PROJECT_REGISTRY_ERROR_MESSAGE,
  organizationProjectView,
};
