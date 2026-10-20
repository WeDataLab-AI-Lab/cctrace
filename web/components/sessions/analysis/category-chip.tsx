import { cn } from '@/lib/utils';
import type { SessionCategoryKey, WaitingKind } from '@/lib/session-analysis-types';
import { Dot, type DotKind } from './atoms';
import { categoryMeta, waitingKindLabel, type CategoryTone } from './labels';

/**
 * 관측 범주 칩(D2). 색은 "지금 사람이 볼 일이 있는가"만 말한다.
 *   응답 대기 = 주의(앰버)   진행 중 = 살아 있음(초록, 맥박)   열린 질문 = 주의지만 옅게(속 빈 점)
 *   종료됨·최근 활동 = 조용한 회색   쉬는 중 = 속 빈 점   판단 불가·수집 지연 = 점선(모른다)
 * 에이전트 색(Claude/Codex)과 겹치지 않도록 의미 색만 쓴다.
 */

const TONE_CLASS: Record<CategoryTone, string> = {
  attention: 'bg-warning-soft text-warning-strong',
  pending: 'border-warning/50 bg-surface text-warning-strong',
  live: 'bg-success-soft text-success-strong',
  quiet: 'bg-surface-sunk text-ink-2',
  idle: 'bg-surface-sunk text-ink-2',
  unknown: 'border-dashed border-border-strong bg-transparent text-ink-3',
  faded: 'border-border bg-transparent text-ink-3',
};

const TONE_DOT: Record<CategoryTone, DotKind> = {
  attention: 'solid-warning',
  pending: 'ring-warning',
  live: 'solid-success',
  quiet: 'solid-muted',
  idle: 'ring',
  unknown: 'dashed',
  faded: 'ring',
};

interface CategoryChipProps {
  category: SessionCategoryKey;
  /** 응답 대기라면 무엇을 기다리는지. 머리에서만 붙인다. */
  waitingKind?: WaitingKind | '' | null;
  /** 좁은 자리(목록 행)의 줄임말. */
  short?: boolean;
  className?: string;
}

const CategoryChip = ({ category, waitingKind, short = false, className }: CategoryChipProps) => {
  const meta = categoryMeta(category);
  const waiting = category === 'waiting' && waitingKind ? waitingKindLabel(waitingKind) : null;
  const text = short ? meta.shortLabel : meta.label;
  return (
    <span
      title={`${meta.label} — ${meta.hint}${waiting ? ` (${waiting})` : ''}`}
      className={cn(
        'inline-flex max-w-full shrink-0 items-center gap-1.5 rounded border border-transparent px-1.5 py-px text-[10.5px] leading-4 font-medium whitespace-nowrap',
        TONE_CLASS[meta.tone],
        className,
      )}
    >
      <Dot kind={TONE_DOT[meta.tone]} className="size-1.5" />
      <span className="truncate">
        {text}
        {waiting && <span className="font-normal opacity-80"> · {waiting}</span>}
      </span>
    </span>
  );
};

export { CategoryChip, TONE_CLASS as CATEGORY_TONE_CLASS, TONE_DOT as CATEGORY_TONE_DOT };
