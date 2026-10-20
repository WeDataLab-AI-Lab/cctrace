'use client';

import { useEffect, useState } from 'react';
import Link from 'next/link';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { fetchMyQuotaUsage, fetchMyQuotaWindowComparison } from '@/lib/api';
import type { UsageWindowSeries } from '@/lib/session-analysis-types';
import { quotaAccountSelectionKey, quotaComparisonSeries, quotaUsageFrameAccount } from '@/lib/quota-owner-types';
import type { QuotaUsageAccount } from '@/lib/quota-owner-types';
import { POLL_SLOW } from '@/lib/query-config';
import { createLocalSessionApi, LocalSessionApiError } from '@/lib/local-session-api';
import type { LocalConnectionPair } from '@/lib/local-session-connection';
import { LOCAL_CONNECTION_CHANGED, loadConnection } from '@/lib/local-session-connection';
import { UsageMeters } from '@/components/sessions/frame/usage-meters';
import { UsageMiniChart } from '@/components/sessions/frame/usage-mini-chart';

const emptySeries = (window: UsageWindowSeries['window'], reason?: string): UsageWindowSeries => ({
  window, current: [], past: [], median: [], band: [], sampleCounts: [], historyCount: 0, reason,
});

const PersonalQuotaUsage = () => {
  const queryClient = useQueryClient();
  const [connection, setConnection] = useState<LocalConnectionPair | null>(() => loadConnection());
  const [now, setNow] = useState(() => Date.now());
  const [selectionMessage, setSelectionMessage] = useState('');
  useEffect(() => {
    const syncConnection = () => setConnection(loadConnection());
    window.addEventListener(LOCAL_CONNECTION_CHANGED, syncConnection);
    return () => window.removeEventListener(LOCAL_CONNECTION_CHANGED, syncConnection);
  }, []);
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => window.clearInterval(timer);
  }, []);

  const usage = useQuery({
    queryKey: ['my-quota-usage'],
    queryFn: fetchMyQuotaUsage,
    staleTime: POLL_SLOW,
    refetchInterval: POLL_SLOW,
  });
  const local = useQuery({
    queryKey: ['local-quota-profiles', connection?.origin],
    queryFn: () => {
      if (!connection) throw new LocalSessionApiError('unreachable');
      return createLocalSessionApi(connection).profileCatalog();
    },
    enabled: !!connection,
    staleTime: 30_000,
    refetchInterval: connection ? POLL_SLOW : false,
    retry: false,
  });

  const accounts = usage.data?.accounts ?? [];
  const selectedLocal = local.data?.profiles.find((entry) => entry.selected);
  const active = (selectedLocal && accounts.find((account) => account.billing_provider === selectedLocal.billing_provider && account.account_id === selectedLocal.account_id)) ||
    accounts.find((account) => account.binding_status === 'approved') || accounts[0];
  const localProfilesByAccount = new Map<string, NonNullable<typeof local.data>['profiles'][number]>();
  for (const entry of local.data?.profiles ?? []) {
    const key = quotaAccountSelectionKey(entry);
    if (entry.selected || !localProfilesByAccount.has(key)) localProfilesByAccount.set(key, entry);
  }
  const meterAccounts = accounts.map((account) => {
    const localProfile = localProfilesByAccount.get(quotaAccountSelectionKey(account));
    const approved = account.binding_status === 'approved';
    return quotaUsageFrameAccount(account, { selected: !!approved && localProfile?.selected === true, selectable: !!approved && !!localProfile });
  });
  const activeKey = active ? quotaAccountSelectionKey(active) : '';
  const comparisonKey = (windowMinutes: 300 | 10080) => ['my-quota-usage-comparison', activeKey, windowMinutes] as const;
  const fiveHour = useQuery({
    queryKey: comparisonKey(300),
    queryFn: () => active ? fetchMyQuotaWindowComparison(active.billing_provider, active.account_id, 300) : Promise.reject(new Error('no account')),
    enabled: !!active && active.binding_status === 'approved',
    staleTime: POLL_SLOW,
    refetchInterval: POLL_SLOW,
    retry: false,
  });
  const sevenDay = useQuery({
    queryKey: comparisonKey(10080),
    queryFn: () => active ? fetchMyQuotaWindowComparison(active.billing_provider, active.account_id, 10080) : Promise.reject(new Error('no account')),
    enabled: !!active && active.binding_status === 'approved',
    staleTime: POLL_SLOW,
    refetchInterval: POLL_SLOW,
    retry: false,
  });
  const fiveSeries = fiveHour.data ? quotaComparisonSeries(fiveHour.data, 'five_hour') : emptySeries('five_hour', fiveHour.error ? 'comparison_unavailable' : 'no_observations');
  const sevenSeries = sevenDay.data ? quotaComparisonSeries(sevenDay.data, 'seven_day') : emptySeries('seven_day', sevenDay.error ? 'comparison_unavailable' : 'no_observations');
  const selectedLabel = active?.label ?? '계정';

  const selectProfile = async (key: string) => {
    if (!connection || !local.data || !active) return;
    const entry = localProfilesByAccount.get(key);
    if (!entry || active.binding_status !== 'approved') return;
    setSelectionMessage('');
    try {
      const client = createLocalSessionApi(connection);
      const catalog = await client.profileCatalog();
      const currentEntry = catalog.profiles.find((candidate) => candidate.agent === entry.agent && candidate.profile_name === entry.profile_name &&
        candidate.billing_provider === entry.billing_provider && candidate.account_id === entry.account_id);
      if (!currentEntry) throw new LocalSessionApiError('invalid_request');
      await client.selectProfile(currentEntry, catalog.revision);
      setSelectionMessage('다음 실행에 쓸 로컬 프로필을 저장했습니다.');
      await queryClient.invalidateQueries({ queryKey: ['local-quota-profiles', connection.origin] });
    } catch (error) {
      setSelectionMessage(error instanceof LocalSessionApiError && error.code === 'revision_conflict'
        ? '다른 선택이 먼저 저장되었습니다. 프로필 목록을 새로 읽었습니다.'
        : '로컬 프로필을 저장하지 못했습니다. 로컬 브리지 연결을 확인하세요.');
      await queryClient.invalidateQueries({ queryKey: ['local-quota-profiles', connection.origin] });
    }
  };

  return (
    <section className="space-y-4 rounded-lg border border-border bg-surface p-5" aria-labelledby="personal-quota-title">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="personal-quota-title" className="text-sm font-semibold text-ink">개인 계정 사용량</h2>
          <p className="mt-1 text-xs text-ink-3">승인된 계정의 5시간·7일 측정값과 과거 창 비교입니다. 측정은 로컬 클라이언트가 보고합니다.</p>
        </div>
        <Link href="/settings" className="text-xs font-medium text-brand hover:underline">계정 등록 및 검증</Link>
      </header>

      {usage.isPending && <p className="text-xs text-ink-3">개인 측정값을 불러오는 중입니다.</p>}
      {usage.isError && <p role="status" className="text-xs text-danger">개인 사용량을 불러오지 못했습니다.</p>}
      {!usage.isPending && !usage.isError && accounts.length === 0 && (
        <p className="text-xs text-ink-3">등록된 개인 계정이 없습니다. 계정을 등록하면 독립 확인 승인 후 측정이 시작됩니다.</p>
      )}
      {accounts.length > 0 && (
        <>
          <UsageMeters accounts={meterAccounts} now={now} onSelect={(key) => { void selectProfile(key); }} />
          {meterAccounts.some((account) => account.selectable) && <p className="text-[11px] text-ink-3">라디오 선택은 일치가 확인된 로컬 프로필에만 적용됩니다. 원격 서버는 로컬 자격 설정에 접근하지 않습니다.</p>}
          {!connection && <p className="text-[11px] text-ink-3">로컬 브리지가 연결되지 않아 프로필 선택을 사용할 수 없습니다. 세션 화면에서 로컬 브리지를 연결한 뒤 다시 여세요.</p>}
          {connection && local.isError && <p className="text-[11px] text-ink-3">로컬 프로필을 읽을 수 없습니다. 연결 주소와 브리지 실행 상태를 확인하세요.</p>}
          {selectionMessage && <p role="status" className="text-[11px] text-ink-2">{selectionMessage}</p>}
          {active?.binding_status !== 'approved' && <p className="text-[11px] text-ink-3">이 계정은 관리자 확인이 완료되지 않아 사용량을 측정하지 않습니다.</p>}
          <div className="grid gap-4 xl:grid-cols-2">
            <div>
              <UsageMiniChart label="5H" series={fiveSeries} accountLabel={selectedLabel} />
              {active?.binding_status === 'approved' && fiveHour.isError && <p role="status" className="mt-1 text-[11px] text-ink-3">5시간 창 비교 응답을 불러오지 못했습니다.</p>}
              {active?.binding_status === 'approved' && !fiveHour.isError && fiveHour.data && fiveSeries.historyCount === 0 && <p className="mt-1 text-[11px] text-ink-3">비교할 과거 5시간 창이 아직 없습니다. 새 관측이 쌓이면 표시됩니다.</p>}
            </div>
            <div>
              <UsageMiniChart label="7D" series={sevenSeries} accountLabel={selectedLabel} />
              {active?.binding_status === 'approved' && sevenDay.isError && <p role="status" className="mt-1 text-[11px] text-ink-3">7일 창 비교 응답을 불러오지 못했습니다.</p>}
              {active?.binding_status === 'approved' && !sevenDay.isError && sevenDay.data && sevenSeries.historyCount === 0 && <p className="mt-1 text-[11px] text-ink-3">비교할 과거 7일 창이 아직 없습니다. 새 관측이 쌓이면 표시됩니다.</p>}
            </div>
          </div>
        </>
      )}
    </section>
  );
};

export { PersonalQuotaUsage, emptySeries };
