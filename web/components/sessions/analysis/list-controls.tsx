'use client';

import { Lock } from 'lucide-react';
import { cn } from '@/lib/utils';
import { ScrollFadeRow } from '@/components/common/scroll-fade-row';
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip';
import type { PlanFilter, SessionCategoryCounts, SessionCategoryKey, SessionSort } from '@/lib/session-analysis-types';
import { CATEGORY_TONE_CLASS, CATEGORY_TONE_DOT } from './category-chip';
import { Dot } from './atoms';
import { formatCount } from './format';
import { CATEGORY_META, CATEGORY_ORDER, SORT_META, SORT_ORDER } from './labels';

/**
 * 세션 목록 위의 제어 셋: 범주 개수 칩(B3), 정렬(B7), 플랜 필터(B6). 모두 제어형이다 — 값과 바꾸는 법을
 * 부모가 갖는다. 서버 쪽 필터링은 P2가 이 값을 `category=`, `sort=`, `plan=` 쿼리로 보낸다.
 */

/* ---------------- 범주 칩 (B3) ---------------- */

interface CategoryCountChipsProps {
  counts: SessionCategoryCounts;
  /** 고른 범주. null 이면 전체. 같은 칩을 다시 누르면 null 로 돌아간다. */
  value: SessionCategoryKey | null;
  onChange: (next: SessionCategoryKey | null) => void;
  /** 비소유 관리자: 범주 개수는 API가 주지 않는다(집계 경로 보호). */
  unavailable?: boolean;
}

const CategoryCountChips = ({ counts, value, onChange, unavailable = false }: CategoryCountChipsProps) => {
  if (unavailable) {
    return (
      <p className="flex items-center gap-1.5 text-[11px] text-ink-3">
        <Lock size={12} strokeWidth={1.75} aria-hidden="true" />
        범주 개수는 세션 주인에게만 제공됩니다
      </p>
    );
  }
  // 개수가 있는 범주만 놓는다. 고른 범주는 0이어도 남겨 해제할 수 있게 한다.
  const keys = CATEGORY_ORDER.filter((key) => (counts[key] ?? 0) > 0 || key === value);
  const total = CATEGORY_ORDER.reduce((sum, key) => sum + (counts[key] ?? 0), 0);

  return (
    <TooltipProvider delayDuration={300}>
      <ScrollFadeRow className="-my-1 items-center gap-1.5 py-1">
        <div role="group" aria-label="관측 범주" className="flex shrink-0 items-center gap-1.5">
          <button
            type="button"
            aria-pressed={value === null}
            onClick={() => onChange(null)}
            className={cn(
              'inline-flex h-6 shrink-0 items-center gap-1.5 rounded border border-transparent px-2 text-[11px] font-medium whitespace-nowrap transition-colors',
              value === null ? 'bg-brand text-brand-ink' : 'bg-surface-sunk text-ink-2 hover:bg-border',
            )}
          >
            전체
            <span className="tabular-nums opacity-70">{formatCount(total)}</span>
          </button>
          {keys.map((key) => {
            const meta = CATEGORY_META[key];
            const selected = value === key;
            return (
              <Tooltip key={key}>
                <TooltipTrigger asChild>
                  <button
                    type="button"
                    aria-pressed={selected}
                    onClick={() => onChange(selected ? null : key)}
                    className={cn(
                      'inline-flex h-6 shrink-0 items-center gap-1.5 rounded border border-transparent px-2 text-[11px] font-medium whitespace-nowrap transition-colors',
                      selected ? 'bg-brand text-brand-ink' : cn(CATEGORY_TONE_CLASS[meta.tone], 'hover:brightness-95'),
                    )}
                  >
                    <Dot kind={CATEGORY_TONE_DOT[meta.tone]} className={cn('size-1.5', selected && 'border-brand-ink bg-brand-ink')} />
                    {meta.label}
                    <span className="tabular-nums opacity-70">{formatCount(counts[key] ?? 0)}</span>
                  </button>
                </TooltipTrigger>
                <TooltipContent className="max-w-[240px] leading-relaxed">{meta.hint}</TooltipContent>
              </Tooltip>
            );
          })}
        </div>
      </ScrollFadeRow>
    </TooltipProvider>
  );
};

/* ---------------- 정렬 (B7) ---------------- */

interface SortControlProps {
  value: SessionSort;
  onChange: (next: SessionSort) => void;
}

const SortControl = ({ value, onChange }: SortControlProps) => (
  <TooltipProvider delayDuration={300}>
    <div role="group" aria-label="정렬" className="flex shrink-0 items-center gap-1.5">
      <span className="text-[11px] text-ink-3">정렬</span>
      <div className="flex gap-1">
        {SORT_ORDER.map((key) => (
          <Tooltip key={key}>
            <TooltipTrigger asChild>
              <button
                type="button"
                aria-pressed={value === key}
                onClick={() => onChange(key)}
                className={cn(
                  'rounded px-2 py-1 text-[11px] font-medium whitespace-nowrap transition-colors',
                  value === key ? 'bg-brand text-brand-ink' : 'bg-surface-sunk text-ink-2 hover:bg-border',
                )}
              >
                {SORT_META[key].label}
              </button>
            </TooltipTrigger>
            <TooltipContent className="max-w-[220px]">{SORT_META[key].hint}</TooltipContent>
          </Tooltip>
        ))}
      </div>
    </div>
  </TooltipProvider>
);

/* ---------------- 플랜 필터 (B6) ---------------- */

const PLAN_FILTERS: readonly { value: PlanFilter; label: string; hint: string }[] = [
  { value: 'file', label: '플랜 파일', hint: '플랜 파일이 있는 세션만 봅니다.' },
  { value: 'mode', label: '플랜 모드', hint: '플랜 모드를 쓴 세션만 봅니다. 플랜 파일이 없어도 플랜 모드는 쓸 수 있어, 위와는 다른 사실입니다.' },
];

interface PlanFilterToggleProps {
  /** 고른 필터. null 이면 거르지 않는다. 같은 버튼을 다시 누르면 null 로 돌아간다. */
  value: PlanFilter | null;
  onChange: (next: PlanFilter | null) => void;
}

const PlanFilterToggle = ({ value, onChange }: PlanFilterToggleProps) => (
  <TooltipProvider delayDuration={300}>
    <div role="group" aria-label="플랜 필터" className="flex shrink-0 items-center gap-1.5">
      <span className="text-[11px] text-ink-3">플랜만</span>
      <div className="flex gap-1">
        {PLAN_FILTERS.map((f) => (
          <Tooltip key={f.value}>
            <TooltipTrigger asChild>
              <button
                type="button"
                aria-pressed={value === f.value}
                onClick={() => onChange(value === f.value ? null : f.value)}
                className={cn(
                  'rounded px-2 py-1 text-[11px] font-medium whitespace-nowrap transition-colors',
                  value === f.value ? 'bg-brand text-brand-ink' : 'bg-surface-sunk text-ink-2 hover:bg-border',
                )}
              >
                {f.label}
              </button>
            </TooltipTrigger>
            <TooltipContent className="max-w-[220px]">{f.hint}</TooltipContent>
          </Tooltip>
        ))}
      </div>
    </div>
  </TooltipProvider>
);

export { CategoryCountChips, SortControl, PlanFilterToggle };
