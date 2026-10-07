'use client';

import { useState, type CSSProperties } from 'react';
import { useQuery } from '@tanstack/react-query';
import { fetchCostByModel, fetchSessionOverview } from '@/lib/api';
import { matchCategory, modelColor } from '@/lib/colors';
import type { ModelCategory } from '@/lib/colors';
import { TrendChart } from '@/components/common/trend-chart';
import type { TrendPointSelection, ViewMode } from '@/components/common/trend-chart';
import type { ModelStat, SessionOverview } from '@/lib/types';
import { POLL_NORMAL } from '@/lib/query-config';
import { subtitleFromSince } from '@/components/overview/overview-helpers';
import { formatRelativeTime } from '@/lib/format';
import { daysAgo, fmt } from './user-utils';

interface UserDetailProps {
  email: string;
  avatarColor: string;
  displayName: string;
  viewMode: ViewMode;
  filterAccount?: string;
  modelFilter?: string;
  modelCategory?: ModelCategory;
  userId?: string;
  /** false 면 세션 쿼리를 보내지 않고 안내 문구만 표시 (#799) */
  canViewSessions: boolean;
  /** true 면 서버가 정한 본인 범위의 세션이 카드와 무관하게 온다 — 카드의 프로필·계정 필터는 적용되지 않는다 (#809) */
  sessionsAcrossProfiles?: boolean;
}

interface ModelBreakdownRow {
  name: string;
  val: number;
  pct: number;
  barPct: number;
  color: string;
}

const UserDetail = ({ email, avatarColor, displayName, viewMode, filterAccount, modelFilter, modelCategory, userId, canViewSessions, sessionsAcrossProfiles = false }: UserDetailProps) => {
  // Date.now() 기반 값은 매 렌더 변동(=queryKey 흔들림→refetch 루프)을 막기 위해 1회만 계산.
  const [since30d] = useState(() => daysAgo(30));
  const [now] = useState(() => Date.now());
  const [trendSince, setTrendSince] = useState('');
  const [trendUntil, setTrendUntil] = useState<string | undefined>(undefined);
  const activeSince = trendSince || since30d;
  const handleTimeRangeChange = (since: string, until: string | undefined) => {
    setTrendSince(since);
    setTrendUntil(until);
  };
  const handleTrendPointSelect = (point: TrendPointSelection) => handleTimeRangeChange(point.since, point.until);

  const { data: modelData = [] } = useQuery<ModelStat[]>({
    queryKey: ['model-user', email, activeSince, trendUntil, filterAccount, userId],
    queryFn: () => fetchCostByModel(activeSince, trendUntil, email, filterAccount, userId),
    refetchInterval: POLL_NORMAL,
  });
  const { data: allModelData = [] } = useQuery<ModelStat[]>({
    queryKey: ['model-user-all', email, activeSince, trendUntil, userId],
    queryFn: () => fetchCostByModel(activeSince, trendUntil, email, undefined, userId),
    refetchInterval: POLL_NORMAL,
  });
  const { data: sessions = [] } = useQuery<SessionOverview[]>({
    queryKey: ['sessions-user', email, activeSince, trendUntil, filterAccount],
    queryFn: () => fetchSessionOverview({
      profileEmail: email,
      loginEmail: filterAccount,
      since: activeSince,
      until: trendUntil,
      limit: 5,
    }),
    refetchInterval: POLL_NORMAL,
    enabled: canViewSessions,
  });

  // Aggregate model data into breakdown with percentages
  const filteredModels = modelCategory && modelCategory !== 'all'
    ? modelData.filter((m) => matchCategory(m.agent, m.billing_provider, modelCategory))
    : modelData;
  const agg = filteredModels.reduce<Record<string, number>>((acc, m) => {
    const name = m.model.replace('claude-', '').split('-20')[0];
    const val = viewMode === 'token' ? m.input_tokens + m.output_tokens : m.total_cost;
    acc[name] = (acc[name] || 0) + val;
    return acc;
  }, {});
  const filteredTotal = Object.values(agg).reduce((s, v) => s + v, 0);
  // Overall total (across all accounts) for consistent bar widths
  const overallTotal = allModelData.reduce(
    (s, m) => s + (viewMode === 'token' ? m.input_tokens + m.output_tokens : m.total_cost),
    0,
  );
  const barBase = overallTotal > 0 ? overallTotal : filteredTotal;
  const modelBreakdown: ModelBreakdownRow[] = Object.entries(agg)
    .filter(([, v]) => v > 0)
    .sort((a, b) => b[1] - a[1])
    .map(([name, val]) => ({
      name,
      val,
      pct: filteredTotal > 0 ? Math.round((val / filteredTotal) * 100) : 0,
      barPct: barBase > 0 ? (val / barBase) * 100 : 0,
      color: modelColor(name),
    }));

  const uniqueSessions = (sessions ?? [])
    .filter((s) => s.session_id)
    .filter((s, i, arr) => arr.findIndex((x) => x.session_id === s.session_id) === i)
    .slice(0, 5);

  return (
    <div className="bg-surface rounded-lg border border-border p-6 space-y-5">
      <header className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <span
            className="w-8 h-8 rounded-full flex items-center justify-center text-white text-[13px] font-semibold [background-color:var(--avatar-color)]"
            // CSS variable drives the runtime avatar color (Tailwind cannot express dynamic values)
            style={{ '--avatar-color': avatarColor } as CSSProperties} // eslint-disable-line no-restricted-syntax
          >
            {displayName[0]?.toUpperCase()}
          </span>
          <span className="text-[15px] font-semibold text-ink">{displayName} — Detail View</span>
        </div>
        <span className="text-[12px] text-ink-3">{subtitleFromSince(activeSince, now)}</span>
      </header>

      <div className="h-px bg-surface-sunk" />

      <TrendChart
        filterEmail={email}
        filterAccount={filterAccount}
        filterUserId={userId}
        viewMode={viewMode}
        showViewToggle={false}
        showModelFilter={true}
        modelFilter={modelFilter}
        modelCategory={modelCategory}
        defaultGranularity="minute"
        height={130}
        onTimeRangeChange={handleTimeRangeChange}
        onPointSelect={handleTrendPointSelect}
      />

      <div className="h-px bg-surface-sunk" />

      <section className="space-y-3">
        <h4 className="text-[13px] font-semibold text-ink">Model Breakdown</h4>
        {modelBreakdown.length === 0 ? (
          <p className="text-sm text-ink-3">No data</p>
        ) : (
          modelBreakdown.map((m) => (
            <div key={m.name} className="flex items-center gap-3">
              <span className="text-[12px] text-ink-2 w-[52px] shrink-0">{m.name}</span>
              <div className="flex-1 h-2 rounded bg-surface-sunk overflow-hidden">
                <div
                  className="h-full rounded w-[var(--bar-pct)] [background:var(--bar-color)]"
                  // CSS variables drive runtime bar width/color (Tailwind cannot express dynamic values)
                  style={{ '--bar-pct': `${m.barPct}%`, '--bar-color': m.color } as CSSProperties} // eslint-disable-line no-restricted-syntax
                />
              </div>
              <span className="text-[11px] text-ink-3 w-[52px] text-right">{viewMode === 'cost' ? `$${m.val.toFixed(4)}` : fmt(m.val)}</span>
              <span className="text-[11px] text-ink-3 w-[28px] text-right">{m.pct}%</span>
            </div>
          ))
        )}
      </section>

      <div className="h-px bg-surface-sunk" />

      <section className="space-y-2">
        <h4 className="text-[13px] font-semibold text-ink">Recent Sessions</h4>
        {canViewSessions && sessionsAcrossProfiles && (
          <p className="text-[11px] text-ink-3">서버가 정한 본인 범위의 세션을 표시합니다. 이 카드의 프로필·계정 필터는 적용되지 않습니다.</p>
        )}
        {!canViewSessions ? (
          <p className="text-sm text-ink-3">다른 사용자의 세션은 볼 수 없습니다</p>
        ) : uniqueSessions.length === 0 ? (
          <p className="text-sm text-ink-3">No sessions</p>
        ) : (
          <div>
            <div className="flex items-center py-1.5 border-b border-surface-sunk">
              <span className="flex-1 text-[11px] font-medium text-ink-3">Session ID</span>
              <span className="w-[120px] text-[11px] font-medium text-ink-3">Model</span>
              <span className="w-[80px] text-[11px] font-medium text-ink-3 text-right">Tokens</span>
              <span className="w-[80px] text-[11px] font-medium text-ink-3 text-right">Time</span>
            </div>
            {uniqueSessions.map((s, index) => (
              <div key={s.session_id ?? index} className="flex items-center py-2 border-b border-surface-sunk last:border-b-0">
                <span className="flex-1 text-[12px] text-ink font-mono truncate">{s.session_id?.slice(0, 11) ?? '-'}</span>
                <span className="w-[120px] text-[12px] text-ink-2">{s.model?.replace('claude-', '') || '-'}</span>
                <span className="w-[80px] text-[12px] text-ink text-right">{fmt((s.input_tokens || 0) + (s.output_tokens || 0))}</span>
                <span className="w-[80px] text-[12px] text-ink-3 text-right">{formatRelativeTime(s.end_time)}</span>
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  );
};

export { UserDetail };
export type { UserDetailProps };
