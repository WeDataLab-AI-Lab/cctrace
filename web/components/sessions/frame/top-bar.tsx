'use client';

import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';
import { TooltipProvider } from '@/components/ui/tooltip';
import type { SessionFrame } from '@/lib/session-analysis-types';
import { GlobalSearch } from './global-search';
import { UsageMeters } from './usage-meters';
import { UsageMiniChart } from './usage-mini-chart';

/**
 * 전역 프레임의 윗줄(A1~A4): 계정별 사용량 막대와 선택 라디오, 5H·7D 겹침 차트, 전역 검색.
 * 한 줄로 놓고, 왼쪽(`leading`)과 오른쪽(`trailing`)은 부모가 채운다. 1280px 폭에서 계정 셋이 한 줄에 들어가며,
 * 넘치면 줄이 접히지 않고 가로로 스크롤한다(막대가 눌려 읽을 수 없게 되는 것보다 낫다).
 */

interface FrameTopBarProps {
  frame: SessionFrame;
  now: number;
  onSelectAccount: (label: string) => void;
  query: string;
  onQueryChange: (query: string) => void;
  resultCount?: number | null;
  leading?: ReactNode;
  trailing?: ReactNode;
  className?: string;
}

const FrameTopBar = ({
  frame,
  now,
  onSelectAccount,
  query,
  onQueryChange,
  resultCount,
  leading,
  trailing,
  className,
}: FrameTopBarProps) => {
  // 차트는 선택한 계정의 것이다. 선택한 계정이 없으면(모두 해제) 첫 계정으로 이름을 붙인다.
  const chartAccount = frame.accounts.find((a) => a.selected)?.label ?? frame.accounts[0]?.label ?? '';
  return (
    <TooltipProvider delayDuration={200}>
      <div
        role="group"
        aria-label="전역 프레임"
        className={cn('flex min-w-0 shrink-0 items-center gap-3 overflow-x-auto border-b border-border bg-surface px-3 py-1.5', className)}
      >
        {leading}
        <div aria-hidden="true" className="min-w-0 flex-1" />
        <UsageMeters accounts={frame.accounts} now={now} onSelect={onSelectAccount} />
        <UsageMiniChart label="5H" series={frame.series.five_hour} accountLabel={chartAccount} />
        <UsageMiniChart label="7D" series={frame.series.seven_day} accountLabel={chartAccount} />
        <GlobalSearch value={query} onChange={onQueryChange} resultCount={resultCount} />
        {trailing}
      </div>
    </TooltipProvider>
  );
};

export { FrameTopBar };
