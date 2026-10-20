'use client';

import { useId } from 'react';
import { cn } from '@/lib/utils';
import { Hint } from '@/components/sessions/analysis/atoms';
import { TooltipProvider } from '@/components/ui/tooltip';
import type { FrameAccount } from '@/lib/session-analysis-types';
import { identityKey, meterRows, type IdentityKey, type MeterRow } from './model';

/**
 * 계정별 사용량 막대(A1)와 다음 세션에 쓸 계정 선택(A2).
 *
 * 계정마다 작은 테두리 칸 하나: [라디오] [식별 색 네모] 이름, 그 옆에 가는 막대 두 줄과 퍼센트 두 줄.
 * 위 줄이 5시간 창, 아래 줄이 7일 창이다. Codex에는 계정을 고르는 일이 없어 라디오가 없다.
 * 사용률을 모르면 0%가 아니라 점선 빈 막대와 점선 "?"다. 막대 길이는 SVG 속성(%)이라 인라인 style이 필요 없다.
 */

/** Tailwind가 클래스 이름을 찾을 수 있게 완성된 문자열로 둔다. */
const IDENTITY_SQUARE: Record<IdentityKey, string> = {
  'slot-0': 'bg-cat-1',
  'slot-1': 'bg-cat-4',
  'slot-2': 'bg-cat-3',
  'slot-3': 'bg-cat-5',
  codex: 'bg-agent-codex',
};

const IDENTITY_FILL: Record<IdentityKey, string> = {
  'slot-0': 'fill-cat-1',
  'slot-1': 'fill-cat-4',
  'slot-2': 'fill-cat-3',
  'slot-3': 'fill-cat-5',
  codex: 'fill-agent-codex',
};

/** 0보다 큰 값이 1px도 안 보이지 않게 하는 최소 폭(%). 0은 그대로 0이다. */
const MIN_VISIBLE_PCT = 2;

const MeterBar = ({ row, fill }: { row: MeterRow; fill: string }) => {
  if (row.pct === null) {
    return <span aria-hidden="true" className="block h-1.5 w-14 shrink-0 rounded-full border border-dashed border-border-strong" />;
  }
  const width = row.pct > 0 ? Math.max(row.pct, MIN_VISIBLE_PCT) : 0;
  return (
    <svg aria-hidden="true" className="block h-1.5 w-14 shrink-0">
      <rect width="100%" height="100%" rx="3" className="fill-border" />
      {width > 0 && <rect width={`${width}%`} height="100%" rx="3" className={fill} />}
    </svg>
  );
};

const MeterPct = ({ row }: { row: MeterRow }) => {
  if (row.pct === null) {
    return (
      <span
        role="img"
        aria-label="사용률 알 수 없음"
        className="inline-flex h-3.5 min-w-5 items-center justify-center rounded border border-dashed border-border-strong px-0.5 text-[9.5px] leading-none font-medium text-ink-3"
      >
        ?
      </span>
    );
  }
  return (
    <span className={cn('w-8 text-right text-[10px] leading-none tabular-nums', row.level === 'high' ? 'font-semibold text-danger-strong' : 'text-ink-2')}>
      {row.text}
    </span>
  );
};

const statusHint = (account: FrameAccount, row: MeterRow): string => {
  const state = row.window === 'five_hour' ? account.five_hour_state : account.seven_day_state;
  const reason = row.window === 'five_hour' ? account.five_hour_reason : account.seven_day_reason;
  const parts: string[] = [];
  if (state === 'stale') parts.push('낡은 측정값');
  else if (state === 'unmeasured') {
    const reasons: Record<string, string> = {
      ownership_unverified: '계정 소유 검증 대기',
      no_current_window_sample: '새 창 측정 대기',
      reset_not_yet_observed: '재설정 후 새 측정 대기',
      excluded: '제외된 계정',
      no_sample: '아직 측정 없음',
      no_observations: '관측 없음',
    };
    parts.push(reasons[reason ?? ''] ?? '측정 불가');
  }
  if (account.source === 'client_reported') parts.push('클라이언트 보고');
  if (account.attribution === 'observed') parts.push('관측 귀속');
  else if (account.attribution === 'inferred') parts.push('추론 귀속');
  return parts.join(' · ');
};

interface AccountBlockProps {
  account: FrameAccount;
  identity: IdentityKey;
  groupName: string;
  now: number;
  onSelect: (label: string) => void;
  showWindowDetails: boolean;
}

const AccountBlock = ({ account, identity, groupName, now, onSelect, showWindowDetails }: AccountBlockProps) => {
  const rows = meterRows(account, now);
  const selectable = account.selectable ?? account.provider === 'claude';
  const name = (
    <>
      <span aria-hidden="true" className={cn('size-2 shrink-0 rounded-[2px]', IDENTITY_SQUARE[identity])} />
      <span className="text-[11.5px] font-medium whitespace-nowrap text-ink">{account.label}</span>
    </>
  );
  return (
    <div
      data-provider={account.provider}
      className={cn(
        'flex shrink-0 items-center gap-2.5 rounded-md border bg-surface px-2 py-1',
        account.selected ? 'border-border-strong bg-surface-2' : 'border-border',
      )}
    >
      {selectable ? (
        <label className="flex cursor-pointer items-center gap-1.5">
          <input
            type="radio"
            name={groupName}
            checked={account.selected}
            onChange={() => onSelect(account.selectionKey ?? account.label)}
            className="size-3 shrink-0 cursor-pointer accent-brand"
          />
          {name}
        </label>
      ) : (
        <span className="flex items-center gap-1.5">{name}</span>
      )}
      <div className="flex flex-col gap-[3px]">
        {rows.map((row) => {
          const status = statusHint(account, row);
          const title = status ? `${row.title} · ${status}` : row.title;
          return (
          <Hint key={row.window} label={title} ariaLabel={`${account.label} ${title}`} className="block">
            <span className="flex items-center gap-1.5">
              <MeterBar row={row} fill={IDENTITY_FILL[identity]} />
              <MeterPct row={row} />
              {showWindowDetails && (
                <span title={row.resetText} className="max-w-20 truncate text-[9px] leading-none text-ink-3">
                  {row.window === 'five_hour' ? '5H' : '7D'} · {row.resetText}
                </span>
              )}
            </span>
          </Hint>
          );
        })}
      </div>
    </div>
  );
};

interface UsageMetersProps {
  accounts: readonly FrameAccount[];
  /** 재설정까지 남은 시간의 기준. 호출하는 쪽이 정한다(렌더 중에 시계를 읽지 않는다). */
  now: number;
  /** 다음 세션에 쓸 계정을 골랐다. 값은 `FrameAccount.label`이다. */
  onSelect: (label: string) => void;
  /** 전역 A1 미터에서 창 이름과 reset 시간을 각 행에 항상 표시한다. */
  showWindowDetails?: boolean;
  className?: string;
}

const UsageMeters = ({ accounts, now, onSelect, showWindowDetails = false, className }: UsageMetersProps) => {
  const groupName = useId();
  if (accounts.length === 0) {
    return (
      <span
        className={cn('inline-flex shrink-0 items-center rounded-md border border-dashed border-border-strong px-2 py-1 text-[11px] text-ink-3', className)}
      >
        연결된 계정 없음
      </span>
    );
  }
  const selectableAccounts = accounts.filter((account) => account.selectable ?? account.provider === 'claude');
  return (
    <TooltipProvider delayDuration={200}>
      <div role={selectableAccounts.length > 0 ? 'radiogroup' : undefined} aria-label={selectableAccounts.length > 0 ? '다음 세션에 쓸 계정' : undefined} className={cn('flex shrink-0 items-center gap-2', className)}>
        {accounts.map((account, index) => (
          <AccountBlock
            key={account.label}
            account={account}
            identity={identityKey(accounts, index)}
            groupName={groupName}
            now={now}
            onSelect={onSelect}
            showWindowDetails={showWindowDetails}
          />
        ))}
      </div>
    </TooltipProvider>
  );
};

export { UsageMeters };
