'use client';

import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { POLL_SLOW } from '@/lib/query-config';
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
} from 'recharts';
import { fetchToolDetail } from '@/lib/api';
import { CollectingLoader } from '@/components/common/collecting-loader';
import type { ToolDetail } from '@/lib/types';
import { FailureList } from './failure-list';

interface ToolDetailPanelProps {
  toolName: string;
  loginEmail?: string;
}

interface ToolStat {
  success: number;
  fail: number;
  total: number;
}

interface ToolStatTileProps {
  label: string;
  stat: ToolStat;
}

const formatTick = (v: string): string => (v.includes('T') ? v.slice(11, 16) : v.slice(5));

const formatTooltipLabel = (v: unknown): string => {
  const s = String(v);
  return s.includes('T') ? s.slice(5, 16).replace('T', ' ') : s;
};

const ToolStatTile = ({ label, stat }: ToolStatTileProps) => (
  <div className="bg-canvas rounded-lg px-4 py-3 space-y-1">
    <div className="text-[11px] text-ink-3 font-medium">{label}</div>
    <div className="flex items-baseline gap-2">
      <span className="text-[18px] font-semibold text-ink">{stat.total.toLocaleString()}</span>
      <span className="text-[11px] text-success">{stat.success.toLocaleString()} ok</span>
      {stat.fail > 0 && <span className="text-[11px] text-danger">{stat.fail} fail</span>}
    </div>
  </div>
);

const ToolDetailPanel = ({ toolName, loginEmail }: ToolDetailPanelProps) => {
  // Date.now() 기반 값은 매 렌더 변동(refetch 루프)을 막기 위해 최초 1회만 계산(lazy init).
  const [now] = useState(() => Date.now());
  const since24h = new Date(now - 24 * 3600 * 1000).toISOString();
  const since30d = new Date(now - 30 * 86400 * 1000).toISOString();

  // Minute chart (24h) + failures
  const { data, isLoading } = useQuery<ToolDetail>({
    queryKey: ['tool-detail-minute', toolName, since24h, loginEmail],
    queryFn: () => fetchToolDetail(toolName, since24h, undefined, loginEmail, 'minute', 50),
    refetchInterval: POLL_SLOW,
  });

  // Daily stats (30d) for summary cards
  const { data: dailyData } = useQuery<ToolDetail>({
    queryKey: ['tool-detail-daily', toolName, since30d, loginEmail],
    queryFn: () => fetchToolDetail(toolName, since30d, undefined, loginEmail, 'day', 0),
    refetchInterval: POLL_SLOW,
  });

  const timeseries = data?.timeseries ?? [];
  const failures = data?.failures ?? [];
  const dailyBuckets = dailyData?.timeseries ?? [];

  // Compute daily/weekly/monthly stats from daily buckets
  const todayStr = new Date(now).toISOString().slice(0, 10);
  const weekAgo = new Date(now - 7 * 86400 * 1000).toISOString().slice(0, 10);

  let daySuccess = 0;
  let dayFail = 0;
  let weekSuccess = 0;
  let weekFail = 0;
  let monthSuccess = 0;
  let monthFail = 0;

  for (const b of dailyBuckets) {
    monthSuccess += b.success_count;
    monthFail += b.fail_count;
    if (b.date >= weekAgo) {
      weekSuccess += b.success_count;
      weekFail += b.fail_count;
    }
    if (b.date >= todayStr) {
      daySuccess += b.success_count;
      dayFail += b.fail_count;
    }
  }

  const stats: { label: string; stat: ToolStat }[] = [
    { label: 'Today', stat: { success: daySuccess, fail: dayFail, total: daySuccess + dayFail } },
    { label: 'This Week', stat: { success: weekSuccess, fail: weekFail, total: weekSuccess + weekFail } },
    { label: 'This Month', stat: { success: monthSuccess, fail: monthFail, total: monthSuccess + monthFail } },
  ];

  if (isLoading) {
    return <CollectingLoader className="px-6 py-8" />;
  }

  return (
    <div className="px-6 pb-5 space-y-5">
      <div className="h-px bg-surface-sunk" />

      {/* Summary Stats: Daily / Weekly / Monthly */}
      <div className="grid grid-cols-3 gap-3">
        {stats.map(({ label, stat }) => (
          <ToolStatTile key={label} label={label} stat={stat} />
        ))}
      </div>

      <div className="h-px bg-surface-sunk" />

      {/* Minute Chart (24h) */}
      <div className="space-y-3">
        <h4 className="text-[13px] font-semibold text-ink">Usage Trend (24h)</h4>
        {timeseries.length === 0 ? (
          <p className="text-sm text-ink-3">No data</p>
        ) : (
          <ResponsiveContainer width="100%" height={160}>
            <BarChart data={timeseries} margin={{ top: 4, right: 4, bottom: 0, left: -20 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="var(--surface-sunk)" />
              <XAxis
                dataKey="date"
                tick={{ fontSize: 10, fill: 'var(--ink-3)' }}
                tickFormatter={formatTick}
                interval="preserveStartEnd"
              />
              <YAxis tick={{ fontSize: 11, fill: 'var(--ink-3)' }} allowDecimals={false} />
              <Tooltip
                contentStyle={{ fontSize: 12, borderRadius: 8, border: '1px solid var(--border)' }}
                labelFormatter={formatTooltipLabel}
              />
              <Bar dataKey="success_count" stackId="a" fill="var(--success)" name="Success" radius={[0, 0, 0, 0]} />
              <Bar dataKey="fail_count" stackId="a" fill="var(--danger)" name="Fail" radius={[2, 2, 0, 0]} />
            </BarChart>
          </ResponsiveContainer>
        )}
      </div>

      {/* Recent Failures */}
      {failures.length > 0 && (
        <>
          <div className="h-px bg-surface-sunk" />
          <div className="space-y-2">
            <h4 className="text-[13px] font-semibold text-ink">Recent Failures ({failures.length})</h4>
            <FailureList failures={failures} />
          </div>
        </>
      )}
    </div>
  );
};

export { ToolDetailPanel };
