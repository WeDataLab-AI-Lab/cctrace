'use client';

import type { KeyboardEvent } from 'react';
import { cn } from '@/lib/utils';

/**
 * 뷰어의 "분석 / 대화" 탭 띠(D5). "대화"는 지금의 대화 화면이다.
 *
 * 제어형이다: 어느 탭인지는 부모가 갖는다. WAI-ARIA 탭 패턴을 따른다 — 선택된 탭만 Tab 으로 닿고,
 * 좌우 화살표·Home·End 로 옮긴다. 패널은 부모가 그린다. 연결에 쓰는 id 는 `viewerTabId`·`viewerPanelId`다.
 */

type ViewerTab = 'analysis' | 'conversation';

const VIEWER_TABS: readonly { value: ViewerTab; label: string }[] = [
  { value: 'analysis', label: '분석' },
  { value: 'conversation', label: '대화' },
];

const viewerTabId = (tab: ViewerTab): string => `session-viewer-tab-${tab}`;
const viewerPanelId = (tab: ViewerTab): string => `session-viewer-panel-${tab}`;

interface SessionViewerTabsProps {
  value: ViewerTab;
  onChange: (tab: ViewerTab) => void;
  className?: string;
}

const SessionViewerTabs = ({ value, onChange, className }: SessionViewerTabsProps) => {
  const move = (to: ViewerTab) => {
    onChange(to);
    // 포커스는 클릭이 아니라 키 이동에서만 따라간다.
    document.getElementById(viewerTabId(to))?.focus();
  };

  const handleKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    const index = VIEWER_TABS.findIndex((t) => t.value === value);
    const last = VIEWER_TABS.length - 1;
    let next = -1;
    if (e.key === 'ArrowRight') next = index === last ? 0 : index + 1;
    else if (e.key === 'ArrowLeft') next = index === 0 ? last : index - 1;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = last;
    if (next < 0) return;
    e.preventDefault();
    move(VIEWER_TABS[next].value);
  };

  return (
    <div className={cn('flex shrink-0 items-end gap-3 border-b border-border bg-surface px-5', className)}>
      <div role="tablist" aria-label="세션 보기" className="flex gap-1">
        {VIEWER_TABS.map((tab) => {
          const selected = tab.value === value;
          return (
            <button
              key={tab.value}
              id={viewerTabId(tab.value)}
              type="button"
              role="tab"
              aria-selected={selected}
              aria-controls={viewerPanelId(tab.value)}
              tabIndex={selected ? 0 : -1}
              onClick={() => onChange(tab.value)}
              onKeyDown={handleKeyDown}
              className={cn(
                '-mb-px h-9 border-b-2 px-3 text-[12.5px] font-medium transition-colors focus-visible:-outline-offset-2!',
                selected ? 'border-brand text-ink' : 'border-transparent text-ink-3 hover:text-ink-2',
              )}
            >
              {tab.label}
            </button>
          );
        })}
      </div>
    </div>
  );
};

export { SessionViewerTabs, viewerTabId, viewerPanelId };
export type { ViewerTab };
