'use client';

import { cn } from '@/lib/utils';
import { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider } from '@/components/ui/tooltip';

type SourceValue = 'interactive' | 'headless' | 'all';

interface SourceOption {
  value: SourceValue;
  label: string;
  hint: string;
}

interface SourceFilterProps {
  selected: SourceValue;
  onSelect: (value: SourceValue) => void;
  hiddenSessionCount: number | null;
  hiddenCountLoading: boolean;
}

const SOURCE_OPTIONS: readonly SourceOption[] = [
  { value: 'interactive', label: '대화형', hint: '대화형 실행으로 분류된 세션입니다. 실행 방식이 기록되지 않은 이전 세션도 포함합니다.' },
  { value: 'headless', label: '자동 실행', hint: 'claude -p, SDK 등 자동 실행으로 분류된 세션입니다.' },
  { value: 'all', label: '전체', hint: '실행 방식에 관계없이 모든 세션을 표시합니다.' },
];

// Headless (claude -p, SDK/queue) sessions are machine-driven one-shots; hidden by
// default so they don't bury interactive work. Legacy ('') counts as interactive.
const SourceFilter = ({ selected, onSelect, hiddenSessionCount, hiddenCountLoading }: SourceFilterProps) => {
  const handleSelect = (value: SourceValue) => () => onSelect(value);
  const countMessage = selected === 'all' ? '모든 세션 표시 중 · 숨김 0건'
    : hiddenCountLoading ? '숨긴 세션 수 확인 중'
      : hiddenSessionCount === null ? '숨긴 세션 수 판단 불가'
        : `현재 조건에서 숨긴 세션 ${hiddenSessionCount.toLocaleString('ko-KR')}건`;
  return (
    <TooltipProvider delayDuration={300}>
            {/* Shrinks with the toolbar instead of pushing it off screen. The labels
          truncate before anything overflows -- a clipped word is recoverable (the
          tooltip still names it, and widening the window restores it); a control
          past the right edge is not. */}
      <div role="group" aria-label="실행 방식" className="flex min-w-0 shrink items-center gap-1.5">
        <span className="text-[11px] text-ink-3">실행 방식</span>
        <div className="flex min-w-0 shrink gap-1.5">
          {SOURCE_OPTIONS.map(o => (
            <Tooltip key={o.value}>
              <TooltipTrigger asChild>
                <button
                  onClick={handleSelect(o.value)}
                  className={cn(
                    'min-w-0 truncate rounded px-2 py-1 text-[11px] font-medium transition-colors',
                    selected === o.value
                      ? 'bg-brand text-brand-ink'
                      : 'bg-surface-sunk text-ink-2 hover:bg-[var(--border)]',
                  )}
                >
                  {o.label}
                </button>
              </TooltipTrigger>
              <TooltipContent className="max-w-[220px]">{o.hint}</TooltipContent>
            </Tooltip>
          ))}
        </div>
        <span role="status" aria-live="polite" title={countMessage}
          className="max-w-[180px] truncate whitespace-nowrap text-[10px] text-ink-3">
          {countMessage}
        </span>
      </div>
    </TooltipProvider>
  );
};

export { SourceFilter };
export type { SourceValue };
