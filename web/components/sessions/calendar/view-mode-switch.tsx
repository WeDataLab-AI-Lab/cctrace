'use client';

import { cn } from '@/lib/utils';

/**
 * 기본 목록, 세션 요약, 캘린더를 전환한다.
 */

type SessionsView = 'list' | 'analysis' | 'calendar';

const VIEW_OPTIONS: readonly { value: SessionsView; label: string; hint: string }[] = [
  { value: 'list', label: '목록', hint: '세션을 목록으로 보고 오른쪽에 뷰어를 둡니다' },
  { value: 'analysis', label: '세션 요약', hint: '세션별 제목·작업 상태·비용을 비교하고 조건별로 찾습니다' },
  { value: 'calendar', label: '캘린더', hint: '세션을 주간 캘린더로 보고 오른쪽에 같은 뷰어를 둡니다' },
];

interface ViewModeSwitchProps {
  value: SessionsView;
  onChange: (next: SessionsView) => void;
  className?: string;
}

const ViewModeSwitch = ({ value, onChange, className }: ViewModeSwitchProps) => (
  <div role="group" aria-label="보기 방식" className={cn('inline-flex shrink-0 overflow-hidden rounded border border-border bg-surface', className)}>
    {VIEW_OPTIONS.map((option, i) => (
      <button
        key={option.value}
        type="button"
        aria-pressed={value === option.value}
        title={option.hint}
        onClick={() => onChange(option.value)}
        className={cn(
          'h-7 px-3 text-[12px] transition-colors focus-visible:-outline-offset-2!',
          i > 0 && 'border-l border-border',
          value === option.value ? 'bg-brand-soft font-semibold text-ink' : 'text-ink-2 hover:bg-surface-sunk',
        )}
      >
        {option.label}
      </button>
    ))}
  </div>
);

export { ViewModeSwitch, VIEW_OPTIONS };
export type { SessionsView };
