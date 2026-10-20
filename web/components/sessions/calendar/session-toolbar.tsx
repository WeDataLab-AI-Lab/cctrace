'use client';

import { RefreshCw } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { SessionSort } from '@/lib/session-analysis-types';
import { ScrollFadeRow } from '@/components/common/scroll-fade-row';
import { STATUS_CHIPS, type StatusChipCounts, type StatusChipKey } from './status';
import { ViewModeSwitch, type SessionsView } from './view-mode-switch';

/**
 * 세션 화면의 툴바(B1~B8). 레퍼런스의 한 줄을 그대로 따른다:
 *   [목록|캘린더] 세션 기록 N건 [완료 N][하다 만 것 N][도중 종료 N][판정 불가 N] 이 저장소만 · HH:MM에 읽음
 *   [ ] 요청 없는 세션도 보기 (N건)  [ ] 플랜 파일 있는 것만  정렬 [시작 최신순] ...... [다시 읽기]
 *
 * 네 칩의 합은 "세션 기록 N건"과 같다. 칩은 필터로 동작한다(누르면 그 상태만, 다시 누르면 전체). 목록과 캘린더가 같은
 * 툴바를 쓰므로 두 모드가 같은 세션 집합을 본다. 제어형이다.
 */

const SORT_OPTIONS: readonly { value: SessionSort; label: string }[] = [
  { value: 'start', label: '시작 최신순' },
  { value: 'activity', label: '마지막 활동순' },
  { value: 'cost', label: '비용순' },
  { value: 'requests', label: '요청 수순' },
];

/** 칩의 색: 완료는 성공, 도중 종료는 경고, 미완료와 판정 불가는 중립. */
const CHIP_STYLE: Record<StatusChipKey, { idle: string; pressed: string }> = {
  done: { idle: 'border-success text-success-strong', pressed: 'border-success bg-success-soft text-success-strong' },
  leftover: { idle: 'border-border-strong text-ink-2', pressed: 'border-ink-3 bg-surface-sunk text-ink' },
  midway: { idle: 'border-warning text-warning-strong', pressed: 'border-warning bg-warning-soft text-warning-strong' },
  unknown: { idle: 'border-dashed border-border-strong text-ink-3', pressed: 'border-dashed border-ink-3 bg-surface-sunk text-ink' },
};

interface SessionToolbarProps {
  view: SessionsView;
  onViewChange: (next: SessionsView) => void;
  /** "세션 기록 N건": 상태 칩을 걸기 전, 나머지 필터를 건 뒤의 세션 수. */
  total: number;
  statusCounts: StatusChipCounts;
  statusFilter: StatusChipKey | null;
  onStatusFilterChange: (next: StatusChipKey | null) => void;
  /** 남의 세션을 보는 관리자: 완료 판정은 API가 돌려주지 않으므로 칩을 쓸 수 없다. */
  statusUnavailable?: boolean;
  /** "이 저장소만" 같은 범위 안내. */
  scopeLabel: string;
  /** 마지막으로 읽은 시각("13:12"). 아직 읽지 않았으면 null. */
  readAt: string | null;
  /** 켜면 더해지는 요청 없는 세션 수. */
  requestlessCount: number;
  showRequestless: boolean;
  onShowRequestlessChange: (next: boolean) => void;
  planFileOnly: boolean;
  onPlanFileOnlyChange: (next: boolean) => void;
  sort: SessionSort;
  onSortChange: (next: SessionSort) => void;
  onReload: () => void;
  reloading?: boolean;
  className?: string;
}

const Check = ({ checked, onChange, children }: { checked: boolean; onChange: (next: boolean) => void; children: React.ReactNode }) => (
  <label className="inline-flex shrink-0 cursor-pointer items-center gap-1.5 text-[11.5px] whitespace-nowrap text-ink-2">
    <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} className="size-3.5 accent-[var(--accent)]" />
    {children}
  </label>
);

const SessionToolbar = ({
  view,
  onViewChange,
  total,
  statusCounts,
  statusFilter,
  onStatusFilterChange,
  statusUnavailable = false,
  scopeLabel,
  readAt,
  requestlessCount,
  showRequestless,
  onShowRequestlessChange,
  planFileOnly,
  onPlanFileOnlyChange,
  sort,
  onSortChange,
  onReload,
  reloading = false,
  className,
}: SessionToolbarProps) => (
  <div className={cn('flex items-center gap-x-3', className)}>
    <ScrollFadeRow className="flex-1 items-center gap-x-3">
      <ViewModeSwitch value={view} onChange={onViewChange} />
      <p className="shrink-0 text-[12.5px] whitespace-nowrap text-ink">
        <strong className="font-semibold">세션 기록</strong> <b className="font-semibold tabular-nums">{total.toLocaleString('en-US')}</b>건
      </p>

      <div role="group" aria-label="완료 여부로 거르기" className="flex shrink-0 items-center gap-1.5">
        {STATUS_CHIPS.map((chip) => {
          const pressed = statusFilter === chip.key;
          const count = statusCounts.counts[chip.key];
          const mixed = chip.key === 'leftover' ? ` 지금은 실행 중 ${statusCounts.running}건이 들어 있습니다.` : '';
          return (
            <button
              key={chip.key}
              type="button"
              aria-pressed={pressed}
              disabled={statusUnavailable}
              title={statusUnavailable ? '남의 세션의 완료 판정은 API가 돌려주지 않아 쓸 수 없습니다.' : `${chip.hint}${mixed}`}
              onClick={() => onStatusFilterChange(pressed ? null : chip.key)}
              className={cn(
                'inline-flex h-6 items-center gap-1 rounded-full border px-2.5 text-[11.5px] font-medium whitespace-nowrap transition-colors hover:bg-surface-sunk focus-visible:-outline-offset-2!',
                pressed ? CHIP_STYLE[chip.key].pressed : CHIP_STYLE[chip.key].idle,
                statusFilter !== null && !pressed && 'opacity-55',
                statusUnavailable && 'cursor-not-allowed border-dashed border-border-strong text-ink-4 opacity-100 hover:bg-transparent',
              )}
            >
              {chip.label}{' '}
              <span className="tabular-nums">{statusUnavailable ? '' : count.toLocaleString('en-US')}</span>
            </button>
          );
        })}
      </div>

      <span className="shrink-0 text-[11.5px] whitespace-nowrap text-ink-3" data-testid="scope-read-at">
        {scopeLabel}
        {readAt ? ` · ${readAt}에 읽음` : ''}
      </span>

      <Check checked={showRequestless} onChange={onShowRequestlessChange}>
        요청 없는 세션도 보기 ({requestlessCount.toLocaleString('en-US')}건)
      </Check>
      <Check checked={planFileOnly} onChange={onPlanFileOnlyChange}>
        플랜 파일 있는 것만
      </Check>

      <label className="inline-flex shrink-0 items-center gap-1.5 text-[11.5px] whitespace-nowrap text-ink-3">
        정렬
        <select
          value={sort}
          onChange={(e) => {
            const next = SORT_OPTIONS.find((o) => o.value === e.target.value);
            if (next) onSortChange(next.value);
          }}
          className="h-7 rounded border border-border bg-surface px-1.5 text-[11.5px] text-ink"
        >
          {SORT_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      </label>
    </ScrollFadeRow>

    <button
      type="button"
      onClick={onReload}
      disabled={reloading}
      aria-busy={reloading || undefined}
      className="inline-flex h-7 shrink-0 items-center gap-1 rounded border border-border bg-surface px-2.5 text-[11.5px] font-medium text-ink hover:bg-surface-sunk disabled:cursor-not-allowed disabled:text-ink-4"
    >
      <RefreshCw size={12} strokeWidth={2} className={cn(reloading && 'animate-spin motion-reduce:animate-none')} aria-hidden="true" />
      다시 읽기
    </button>
  </div>
);

export { SessionToolbar, SORT_OPTIONS };
