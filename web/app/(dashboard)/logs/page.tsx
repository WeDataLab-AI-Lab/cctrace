'use client';

import { useState } from 'react';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { fetchEvents, fetchMetrics } from '@/lib/api';
import { POLL_NORMAL } from '@/lib/query-config';
import { useAccount } from '@/components/common/account-context';
import { Skeleton } from '@/components/ui/skeleton';
import type { OtelEvent, OtelMetric } from '@/lib/types';
import type { Tab } from '@/components/logs/logs-helpers';
import { TabSwitcher } from '@/components/logs/tab-switcher';
import { EventsTable } from '@/components/logs/events-table';
import { MetricsTable } from '@/components/logs/metrics-table';
import { LogsPagination } from '@/components/logs/logs-pagination';

const LIMIT = 50;

export default function LogsPage() {
  const [tab, setTab] = useState<Tab>('events');
  const [page, setPage] = useState(0);
  const { selectedAccount } = useAccount();

  // Date.now() 기반 값은 매 렌더 변동으로 queryKey가 흔들려 refetch 루프가 나므로
  // 최초 1회만 계산(lazy init). 렌더 중 new Date 직접 호출(react-hooks/purity)도 함께 해소.
  const [since30d] = useState(() => new Date(Date.now() - 30 * 86400 * 1000).toISOString());

  const { data: events = [], isLoading: eventsLoading } = useQuery<OtelEvent[]>({
    queryKey: ['events', page, selectedAccount],
    queryFn: () =>
      fetchEvents({
        since: since30d,
        limit: LIMIT,
        offset: page * LIMIT,
        login_email: selectedAccount || undefined,
      }),
    enabled: tab === 'events',
    staleTime: Infinity,
    refetchInterval: POLL_NORMAL,
    // The page number is part of the queryKey, so paging without this drops the table to
    // the loading skeleton on every step.
    placeholderData: keepPreviousData,
  });

  const { data: metrics = [], isLoading: metricsLoading } = useQuery<OtelMetric[]>({
    queryKey: ['metrics', page, selectedAccount],
    queryFn: () =>
      fetchMetrics({
        since: since30d,
        limit: LIMIT,
        offset: page * LIMIT,
        profile_email: selectedAccount || undefined,
      }),
    enabled: tab === 'metrics',
    staleTime: Infinity,
    refetchInterval: POLL_NORMAL,
    placeholderData: keepPreviousData,
  });

  const isLoading = tab === 'events' ? eventsLoading : metricsLoading;
  const hasData = tab === 'events' ? events.length > 0 : metrics.length > 0;
  const hasMore = tab === 'events' ? events.length === LIMIT : metrics.length === LIMIT;
  const rowCount = tab === 'events' ? events.length : metrics.length;

  const handleTabChange = (next: Tab) => {
    setTab(next);
    setPage(0);
  };

  const renderTable = () => {
    if (isLoading) {
      return (
        <div className="p-8 space-y-2">
          {Array.from({ length: 8 }).map((_, i) => (
            <Skeleton key={i} className="h-6 w-full" />
          ))}
        </div>
      );
    }
    if (!hasData) {
      return <div className="p-8 text-center text-sm text-ink-3">No data</div>;
    }
    return (
      <div className="flex-1 min-h-0 overflow-auto">
        {tab === 'events' ? <EventsTable events={events} /> : <MetricsTable metrics={metrics} />}
      </div>
    );
  };

  return (
    <div className="h-[calc(100vh-56px-64px)] flex flex-col gap-4">
      <div className="flex items-center justify-between shrink-0">
        <h2 className="text-[16px] font-semibold text-ink">Logs</h2>
        <TabSwitcher tab={tab} onTabChange={handleTabChange} />
      </div>

      <div className="flex-1 min-h-0 bg-surface rounded-lg border border-border flex flex-col overflow-hidden">
        {renderTable()}
      </div>

      {hasData && (
        <LogsPagination page={page} rowCount={rowCount} hasMore={hasMore} onPageChange={setPage} />
      )}
    </div>
  );
}
