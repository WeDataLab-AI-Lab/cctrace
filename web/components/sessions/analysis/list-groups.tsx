'use client';

import { cn } from '@/lib/utils';
import { formatCount } from './format';
import type { EndedGroup, RunningCounts } from './list-model';

/**
 * 목록 카드 묶음의 머리(F2, F3).
 *   "실행 중인 세션 N건" + 처리 중 · 완료 · 중단 중 개수(작업 도중·판정 불가는 있을 때만)
 *   "MM/DD HH:MM에 함께 끝난 N건" + "한꺼번에 떼기"
 * 개수는 목록의 행에서 `list-model`이 센다. 이 파일은 그리기만 한다.
 */

const CountChip = ({ label, value, tone, always = true }: { label: string; value: number; tone: 'live' | 'success' | 'warning' | 'muted'; always?: boolean }) => {
  if (!always && value === 0) return null;
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-full border px-1.5 py-px text-[10.5px] leading-4 whitespace-nowrap',
        value === 0 ? 'border-border text-ink-4' : '',
        value > 0 && tone === 'live' && 'border-border-strong bg-surface text-ink',
        value > 0 && tone === 'success' && 'border-success/35 bg-success-soft text-success-strong',
        value > 0 && tone === 'warning' && 'border-warning/45 bg-warning-soft text-warning-strong',
        value > 0 && tone === 'muted' && 'border-dashed border-border-strong text-ink-3',
      )}
    >
      {label}
      <span className="font-medium tabular-nums">{formatCount(value)}</span>
    </span>
  );
};

const RunningGroupHeader = ({ counts }: { counts: RunningCounts }) => (
  <div role="group" aria-label="실행 중인 세션" className="flex flex-wrap items-center gap-x-2 gap-y-1 border-b border-border bg-surface-2 px-3 py-1.5">
    <span className="flex items-center gap-1.5 text-[11.5px] font-semibold text-ink">
      <span aria-hidden="true" className="size-1.5 rounded-full bg-success motion-safe:animate-pulse" />
      실행 중인 세션 {formatCount(counts.total)}건
    </span>
    <span className="flex flex-wrap items-center gap-1">
      <CountChip label="처리 중" value={counts.processing} tone="live" />
      <CountChip label="완료" value={counts.done} tone="success" />
      <CountChip label="중단 중" value={counts.interrupted} tone="warning" />
      <CountChip label="응답 대기" value={counts.waiting} tone="warning" always={false} />
      <CountChip label="작업 도중" value={counts.leftover} tone="warning" always={false} />
      <CountChip label="판정 불가" value={counts.unknown} tone="muted" always={false} />
    </span>
  </div>
);

interface EndedGroupHeaderProps {
  group: EndedGroup;
  onDismissAll: (group: EndedGroup) => void;
}

const EndedGroupHeader = ({ group, onDismissAll }: EndedGroupHeaderProps) => (
  <div role="group" aria-label={group.label} className="flex items-center gap-2 border-b border-border bg-surface-2 px-3 py-1.5">
    <span className="min-w-0 flex-1 truncate text-[11.5px] font-semibold text-ink">{group.label}</span>
    <button
      type="button"
      onClick={() => onDismissAll(group)}
      title="이 묶음의 세션을 모두 일반 목록으로 돌려보냅니다"
      className="shrink-0 rounded border border-border bg-surface px-1.5 py-px text-[10.5px] font-medium text-ink-2 hover:bg-surface-sunk"
    >
      한꺼번에 떼기
    </button>
  </div>
);

export { RunningGroupHeader, EndedGroupHeader };
