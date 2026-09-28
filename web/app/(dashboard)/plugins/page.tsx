'use client';

import { useState } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { useQuery } from '@tanstack/react-query';
import { fetchPluginUsage, fetchSkillUsage } from '@/lib/api';
import { useAccount } from '@/components/common/account-context';
import { useAgent } from '@/components/common/agent-context';
import type { PluginUsageSummary, SkillUsageSummary } from '@/lib/types';
import {
  combineUsage,
  combineUsageBySkillUser,
  combineUsageByUser,
  groupUsageByDimension,
  hasGitProject,
  projectGroup,
} from '@/components/plugins/usage-aggregate';
import type { Period, PluginView } from '@/components/plugins/usage-types';
import { PeriodFilter } from '@/components/plugins/period-filter';
import { ViewTabs } from '@/components/plugins/view-tabs';
import { UsageSummary } from '@/components/plugins/usage-summary';
import { SkillsTable } from '@/components/plugins/skills-table';
import { UsersTable } from '@/components/plugins/users-table';
import { DimensionUsageTable } from '@/components/plugins/dimension-usage-table';

const PERIODS: Period[] = [
  { label: '7d', days: 7 },
  { label: '30d', days: 30 },
  { label: '90d', days: 90 },
  { label: 'All', days: 0 },
];

const PAGE_LOADED_AT = Date.now();

export default function PluginsPage() {
  const { selectedAccount } = useAccount();
  const { selectedAgent } = useAgent();
  const router = useRouter();
  const searchParams = useSearchParams();
  const rawView = searchParams.get('view');
  const view: PluginView =
    rawView === 'users' || rawView === 'projects' ? rawView : 'skills';
  const period = searchParams.get('period') ?? '30d';
  const [expandedSkill, setExpandedSkill] = useState<string | null>(null);
  const [expandedUser, setExpandedUser] = useState<string | null>(null);
  const [expandedProject, setExpandedProject] = useState<string | null>(null);

  const setParam = (key: string, value: string) => {
    const p = new URLSearchParams(searchParams.toString());
    p.set(key, value);
    router.replace(`/plugins?${p.toString()}`);
  };

  const handlePeriodSelect = (label: string) => setParam('period', label);
  const handleViewSelect = (next: PluginView) => setParam('view', next);
  const handleSkillToggle = (name: string) =>
    setExpandedSkill(expandedSkill === name ? null : name);
  const handleUserToggle = (user: string) =>
    setExpandedUser(expandedUser === user ? null : user);
  const handleProjectToggle = (key: string) =>
    setExpandedProject(expandedProject === key ? null : key);

  const since = period === 'All'
    ? new Date(0).toISOString()
    : new Date(
      PAGE_LOADED_AT - (PERIODS.find(p => p.label === period)?.days ?? 30) * 86400_000
    ).toISOString();

  const pluginAgent = selectedAgent === 'claude' || selectedAgent === 'codex' ? selectedAgent : undefined;
  const includePlugins = true;
  const includeSkills = selectedAgent !== 'claude';

  const { data: pluginRows = [], isLoading: isLoadingPlugins } = useQuery<PluginUsageSummary[]>({
    queryKey: ['plugin-usage', selectedAccount, selectedAgent, period],
    queryFn: () => fetchPluginUsage({ login_email: selectedAccount || undefined, since, agent: pluginAgent }),
    enabled: includePlugins,
    refetchInterval: 5 * 60_000,
  });

  const { data: skillRows = [], isLoading: isLoadingSkills } = useQuery<SkillUsageSummary[]>({
    queryKey: ['skill-usage', selectedAccount, selectedAgent, period],
    queryFn: () => fetchSkillUsage({
      login_email: selectedAccount || undefined,
      since,
      agent: selectedAgent === 'codex' ? 'codex' : undefined,
    }),
    enabled: includeSkills,
    refetchInterval: 5 * 60_000,
  });

  const rows = combineUsage(includePlugins ? pluginRows : [], includeSkills ? skillRows : []);
  const bySkillUser = combineUsageBySkillUser(includePlugins ? pluginRows : [], includeSkills ? skillRows : []);
  const byUser = combineUsageByUser(includePlugins ? pluginRows : [], includeSkills ? skillRows : []);
  const gitPluginRows = includePlugins ? pluginRows.filter(hasGitProject) : [];
  const gitSkillRows = includeSkills ? skillRows.filter(hasGitProject) : [];
  const byProject = groupUsageByDimension(
    gitPluginRows,
    gitSkillRows,
    projectGroup,
    projectGroup
  );
  const isLoading = (includePlugins && isLoadingPlugins) || (includeSkills && isLoadingSkills);

  const pluginCalls = rows.reduce((s, r) => s + r.pluginCalls, 0);
  const skillCalls = rows.reduce((s, r) => s + r.skillCalls, 0);
  const calls = pluginCalls + skillCalls;
  const totalTokens = rows.reduce((s, r) => s + r.totalTokens, 0);
  const inputTokens = rows.reduce((s, r) => s + r.inputTokens, 0);
  const outputTokens = rows.reduce((s, r) => s + r.outputTokens, 0);

  return (
    <div className="space-y-6">
      <header className="flex items-center justify-between">
        <h2 className="text-[16px] font-semibold text-ink">Plugins & Skills</h2>
        <PeriodFilter periods={PERIODS} period={period} onSelect={handlePeriodSelect} />
      </header>

      <UsageSummary
        pluginCalls={pluginCalls}
        skillCalls={skillCalls}
        calls={calls}
        totalTokens={totalTokens}
        inputTokens={inputTokens}
        outputTokens={outputTokens}
      />

      <p className="bg-canvas border border-border rounded-lg px-4 py-3 text-[12px] text-ink-2">
        <span className="font-medium text-ink">Claude</span> slash commands/skills and <span className="font-medium text-ink">Codex</span> skill injection metrics are grouped by the same plugin or skill name.
      </p>

      <ViewTabs view={view} onSelect={handleViewSelect} />

      <div className="bg-surface rounded-lg border border-border overflow-x-auto">
        {view === 'skills' ? (
          <SkillsTable
            rows={rows}
            bySkillUser={bySkillUser}
            expandedSkill={expandedSkill}
            onToggle={handleSkillToggle}
            isLoading={isLoading}
          />
        ) : view === 'users' ? (
          <UsersTable
            byUser={byUser}
            expandedUser={expandedUser}
            onToggle={handleUserToggle}
            isLoading={isLoading}
          />
        ) : (
          <DimensionUsageTable
            title="Project"
            groups={byProject}
            expanded={expandedProject}
            onToggle={handleProjectToggle}
            isLoading={isLoading}
          />
        )}
      </div>
    </div>
  );
}
