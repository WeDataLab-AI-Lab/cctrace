'use client';

import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { SummaryCard } from '@/components/common/summary-card';
import { useAgent } from '@/components/common/agent-context';
import {
  createProjectRuleComment,
  fetchProjectRuleDetail,
  fetchProjectRules,
} from '@/lib/api';
import type { ProjectRuleDetail, ProjectRuleListResponse } from '@/lib/types';
import { POLL_NORMAL } from '@/lib/query-config';
import { useDebouncedValue } from '@/lib/rules-query';
import { RulesFilters } from '@/components/rules/rules-filters';
import { RuleList } from '@/components/rules/rule-list';
import { RuleDetail } from '@/components/rules/rule-detail';
import { buildRuleRows } from '@/components/rules/rule-helpers';
import type { RuleRow, StatusFilter } from '@/components/rules/rule-helpers';
import type { RepositoryOption } from '@/components/rules/rules-filters';
import type { RuleGroup } from '@/components/rules/rule-list';

export default function RulesPage() {
  const queryClient = useQueryClient();
  const [searchInput, setSearchInput] = useState('');
  const search = useDebouncedValue(searchInput, 300);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all');
  const [repositoryFilter, setRepositoryFilter] = useState('');
  const [selectedRuleKey, setSelectedRuleKey] = useState<string | null>(null);
  const [selectedVersionId, setSelectedVersionId] = useState<number | null>(null);
  const [commentBody, setCommentBody] = useState('');
  const { selectedAgent } = useAgent();
  const ruleAgent = selectedAgent === 'claude' || selectedAgent === 'codex' ? selectedAgent : '';
  const hasActiveFilter = searchInput.trim() !== '' || statusFilter !== 'all' || repositoryFilter !== '';

  const {
    data: ruleList,
    isLoading: isLoadingRules,
    isError: isRuleListError,
  } = useQuery<ProjectRuleListResponse>({
    queryKey: ['project-rules', ruleAgent, search, statusFilter, repositoryFilter],
    queryFn: ({ signal }) => fetchProjectRules({
      agent: ruleAgent || undefined,
      repository_key: repositoryFilter || undefined,
      status: statusFilter,
      query: search || undefined,
      limit: 200,
    }, signal),
    staleTime: 60_000,
    refetchInterval: POLL_NORMAL,
  });

  const repositoryOptions: RepositoryOption[] = (() => {
    const options = new Map<string, string>();
    for (const rule of ruleList?.items ?? []) {
      options.set(rule.repository_key, rule.repository_name || rule.project_name || rule.repository_key);
    }
    if (repositoryFilter && !options.has(repositoryFilter)) {
      options.set(repositoryFilter, repositoryFilter);
    }
    return Array.from(options.entries()).map(([key, name]) => ({ key, name }));
  })();

  const rows = buildRuleRows(ruleList?.items ?? []);

  const selectedRow = rows.find(row => row.key === selectedRuleKey) ?? rows[0] ?? null;
  const {
    data: apiDetail,
    isLoading: isLoadingDetail,
    isError: isDetailError,
  } = useQuery<ProjectRuleDetail>({
    queryKey: ['project-rule-detail', selectedRow?.rule.id, selectedVersionId],
    queryFn: ({ signal }) => fetchProjectRuleDetail(
      selectedRow?.rule.id ?? 0,
      selectedVersionId ?? undefined,
      signal,
    ),
    enabled: !!selectedRow,
    staleTime: 60_000,
  });
  const selectedDetail = apiDetail;
  const versions = selectedDetail?.versions ?? [];
  const selectedVersion = versions.find(version => version.id === selectedVersionId)
    ?? versions.find(version => version.id === selectedDetail?.rule.current_version_id)
    ?? versions[0];
  const canWrite = !!selectedDetail && !!selectedVersion;

  const invalidateRuleQueries = (ruleID: number) => {
    queryClient.invalidateQueries({ queryKey: ['project-rule-detail', ruleID] });
    queryClient.invalidateQueries({ queryKey: ['project-rules'] });
  };

  const commentMutation = useMutation({
    mutationFn: (params: { ruleID: number; versionID: number; body: string }) => createProjectRuleComment(params.ruleID, {
      version_id: params.versionID,
      comment_type: 'comment',
      body: params.body,
    }),
    onSuccess: (_comment, params) => {
      setCommentBody('');
      invalidateRuleQueries(params.ruleID);
    },
  });

  const handleCreateComment = () => {
    const body = commentBody.trim();
    if (!canWrite || !body || !selectedDetail || !selectedVersion) return;
    commentMutation.mutate({
      ruleID: selectedDetail.rule.id,
      versionID: selectedVersion.id,
      body,
    });
  };

  const resetSelection = () => {
    setSelectedRuleKey(null);
    setSelectedVersionId(null);
  };

  const handleSearchChange = (value: string) => {
    setSearchInput(value);
    resetSelection();
  };

  const handleStatusChange = (value: StatusFilter) => {
    setStatusFilter(value);
    resetSelection();
  };

  const handleRepositoryChange = (value: string) => {
    setRepositoryFilter(value);
    resetSelection();
  };

  const handleSelectRule = (key: string) => {
    setSelectedRuleKey(key);
    setSelectedVersionId(null);
    setCommentBody('');
  };

  const repositories = new Set(rows.map(({ rule }) => rule.repository_key));
  const groupedDetails: RuleGroup[] = (() => {
    const groups = new Map<string, { name: string; rows: RuleRow[] }>();
    for (const row of rows) {
      const key = row.rule.repository_key;
      const existing = groups.get(key);
      if (existing) {
        existing.rows.push(row);
      } else {
        groups.set(key, { name: row.rule.repository_name || row.rule.project_name || key, rows: [row] });
      }
    }
    return Array.from(groups.entries()).map(([key, group]) => ({ key, ...group }));
  })();
  const isLoading = isLoadingRules;
  const emptyMessage = isRuleListError
    ? 'Project rules API unavailable'
    : hasActiveFilter
      ? 'No rules match the current filters'
      : 'No project rules yet';

  return (
    <div className="h-[calc(100vh-56px-64px)] flex flex-col gap-5">
      <div className="flex flex-col gap-4 shrink-0">
        <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
          <h2 className="text-[16px] font-semibold text-ink">Project Rules</h2>
          <RulesFilters
            search={searchInput}
            statusFilter={statusFilter}
            repositoryFilter={repositoryFilter}
            repositoryOptions={repositoryOptions}
            onSearchChange={handleSearchChange}
            onStatusChange={handleStatusChange}
            onRepositoryChange={handleRepositoryChange}
          />
        </div>

        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <SummaryCard title="Repositories" value={repositories.size.toLocaleString()} />
          <SummaryCard title="Rule Files" value={rows.length.toLocaleString()} />
        </div>
      </div>

      <div className="grid flex-1 min-h-0 grid-cols-1 gap-4 xl:grid-cols-[380px_minmax(0,1fr)]">
        <div className="min-h-0 overflow-hidden rounded-lg border border-border bg-surface">
          <div className="flex h-full min-h-0 flex-col">
            <div className="border-b border-border px-4 py-3">
              <div className="text-[12px] font-medium uppercase text-ink-3">Repositories</div>
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto">
              <RuleList
                groups={groupedDetails}
                selectedKey={selectedRow?.key ?? null}
                isLoading={isLoading}
                emptyMessage={emptyMessage}
                onSelectRule={handleSelectRule}
              />
            </div>
          </div>
        </div>

        <div className="min-h-0 overflow-hidden rounded-lg border border-border bg-surface">
          <RuleDetail
            hasSelection={!!selectedRow}
            isLoading={isLoadingDetail}
            isError={isDetailError}
            detail={selectedDetail}
            version={selectedVersion}
            selectedVersionId={selectedVersion?.id ?? -1}
            commentBody={commentBody}
            canWrite={canWrite}
            isCommentPending={commentMutation.isPending}
            onSelectVersion={setSelectedVersionId}
            onCommentBodyChange={setCommentBody}
            onSubmitComment={handleCreateComment}
          />
        </div>
      </div>
    </div>
  );
}
