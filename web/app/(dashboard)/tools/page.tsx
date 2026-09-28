'use client';

import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { fetchToolUsage } from '@/lib/api';
import { useAccount } from '@/components/common/account-context';
import type { ToolUsageSummary } from '@/lib/types';
import { ToolsSummaryCards } from '@/components/tools/tools-summary-cards';
import { ToolUsageTable } from '@/components/tools/tool-usage-table';
import { ToolsScopeNote } from '@/components/tools/tools-scope-note';

export default function ToolsPage() {
  // Date.now() 기반 값은 매 렌더 변동(refetch 루프)을 막기 위해 최초 1회만 계산(lazy init).
  const [since] = useState(() => new Date(Date.now() - 30 * 86400 * 1000).toISOString());
  const { selectedAccount } = useAccount();
  const [expandedTool, setExpandedTool] = useState<string | null>(null);

  const { data, isLoading } = useQuery<ToolUsageSummary[]>({
    queryKey: ['tools', since, selectedAccount],
    queryFn: () => fetchToolUsage(since, undefined, undefined, selectedAccount || undefined),
    refetchInterval: 5 * 60_000,
  });

  const rows = data ?? [];
  const totalUse = rows.reduce((s, t) => s + t.use_count, 0);
  const totalSuccess = rows.reduce((s, t) => s + t.success_count, 0);
  const totalFail = rows.reduce((s, t) => s + t.fail_count, 0);
  const overallRate = totalUse > 0 ? ((totalSuccess / totalUse) * 100).toFixed(1) : '-';

  const sorted = rows.slice().sort((a, b) => b.use_count - a.use_count);

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h2 className="text-[16px] font-semibold text-ink">Tool Usage</h2>
        <ToolsScopeNote />
      </div>

      <ToolsSummaryCards
        totalUse={totalUse}
        totalSuccess={totalSuccess}
        totalFail={totalFail}
        overallRate={overallRate}
      />

      <ToolUsageTable
        rows={sorted}
        isLoading={isLoading}
        expandedTool={expandedTool}
        onToggleTool={setExpandedTool}
        loginEmail={selectedAccount || undefined}
      />
    </div>
  );
}
