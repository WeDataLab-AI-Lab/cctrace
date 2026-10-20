'use client';

import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { fetchMyQuotaUsage } from '@/lib/api';
import { quotaUsageFrameAccount } from '@/lib/quota-owner-types';
import { POLL_SLOW } from '@/lib/query-config';
import { UsageMeters } from '@/components/sessions/frame/usage-meters';

const noProfileSelection = () => undefined;

const QuotaHeaderMeters = ({ enabled }: { enabled: boolean }) => {
  const [now, setNow] = useState(() => Date.now());
  const { data, isError } = useQuery({
    queryKey: ['my-quota-usage'],
    queryFn: fetchMyQuotaUsage,
    enabled,
    staleTime: POLL_SLOW,
    refetchInterval: POLL_SLOW,
    retry: false,
  });
  /** 분 단위 리셋 표기를 최신화한다. */
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => window.clearInterval(timer);
  }, []);

  if (!enabled) return null;
  if (isError) return <span role="status" className="text-[10px] text-ink-3">사용량 판단 불가</span>;
  if (!data?.accounts.length) return null;
  const accounts = data.accounts.map((account) => quotaUsageFrameAccount(account, { selectable: false }));
  return (
    <div className="min-w-0 max-w-full overflow-x-auto" aria-label="개인 계정 사용량 미터">
      <UsageMeters accounts={accounts} now={now} onSelect={noProfileSelection} showWindowDetails className="gap-1.5" />
    </div>
  );
};

export { QuotaHeaderMeters };
