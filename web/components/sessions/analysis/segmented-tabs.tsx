'use client';

import type { KeyboardEvent } from 'react';
import { cn } from '@/lib/utils';
import { formatCount } from './format';

/**
 * 작은 탭 띠: 분류 탭(F5)과 메모 편집기의 탭(F6)이 함께 쓴다. WAI-ARIA 탭 패턴을 따른다 — 선택된 탭만 Tab 으로 닿고,
 * 좌우 화살표·Home·End 로 옮긴다. 제어형이다: 어느 탭인지는 부모가 갖는다. 패널은 부모가 그리고 `panelId`로 이어 준다.
 */

interface TabItem<T extends string> {
  value: T;
  label: string;
  /** 있으면 라벨 옆에 작게 적는다. */
  count?: number;
  /** 툴팁(title). */
  hint?: string;
}

interface SegmentedTabsProps<T extends string> {
  items: readonly TabItem<T>[];
  value: T;
  onChange: (value: T) => void;
  ariaLabel: string;
  /** 탭 id의 접두. 같은 화면에 띠가 둘 이상일 때 서로 달라야 한다. */
  idPrefix: string;
  /** 선택된 탭이 가리키는 패널의 id. */
  panelId?: string;
  className?: string;
}

const tabDomId = (prefix: string, value: string): string => `${prefix}-tab-${value}`;

const SegmentedTabs = <T extends string>({ items, value, onChange, ariaLabel, idPrefix, panelId, className }: SegmentedTabsProps<T>) => {
  const move = (to: T) => {
    onChange(to);
    // 포커스는 클릭이 아니라 키 이동에서만 따라간다.
    document.getElementById(tabDomId(idPrefix, to))?.focus();
  };

  const handleKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    const index = items.findIndex((t) => t.value === value);
    const last = items.length - 1;
    let next = -1;
    if (e.key === 'ArrowRight') next = index === last ? 0 : index + 1;
    else if (e.key === 'ArrowLeft') next = index <= 0 ? last : index - 1;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = last;
    if (next < 0) return;
    e.preventDefault();
    move(items[next].value);
  };

  return (
    <div role="tablist" aria-label={ariaLabel} className={cn('flex items-end gap-px overflow-x-auto [scrollbar-width:none]', className)}>
      {items.map((tab) => {
        const selected = tab.value === value;
        return (
          <button
            key={tab.value}
            id={tabDomId(idPrefix, tab.value)}
            type="button"
            role="tab"
            title={tab.hint}
            aria-selected={selected}
            aria-controls={selected ? panelId : undefined}
            tabIndex={selected ? 0 : -1}
            onClick={() => onChange(tab.value)}
            onKeyDown={handleKeyDown}
            className={cn(
              '-mb-px inline-flex h-7 shrink-0 items-center gap-0.5 border-b-2 px-1 text-[11px] font-medium whitespace-nowrap transition-colors focus-visible:-outline-offset-2!',
              selected ? 'border-brand text-ink' : 'border-transparent text-ink-3 hover:text-ink-2',
            )}
          >
            {tab.label}
            {typeof tab.count === 'number' && <span className={cn('text-[10px] tabular-nums', selected ? 'text-ink-2' : 'text-ink-4')}>{formatCount(tab.count)}</span>}
          </button>
        );
      })}
    </div>
  );
};

export { SegmentedTabs };
export type { TabItem };
