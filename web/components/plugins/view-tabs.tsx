'use client';

import { cn } from '@/lib/utils';
import type { PluginView } from './usage-types';

interface ViewTabsProps {
  view: PluginView;
  onSelect: (view: PluginView) => void;
}

const TABS: PluginView[] = ['skills', 'users', 'projects'];

const tabLabel = (tab: PluginView) => {
  if (tab === 'skills') return 'By Skill';
  if (tab === 'users') return 'By User';
  return 'By Project';
};

const ViewTabs = ({ view, onSelect }: ViewTabsProps) => (
  <nav className="flex gap-0 border-b border-border">
    {TABS.map((tab) => (
      <button
        key={tab}
        onClick={() => onSelect(tab)}
        className={cn(
          'px-4 py-2 text-[13px] font-medium border-b-2 -mb-px transition-colors',
          view === tab
            ? 'border-ink text-ink'
            : 'border-transparent text-ink-3 hover:text-ink-2'
        )}
      >
        {tabLabel(tab)}
      </button>
    ))}
  </nav>
);

export { ViewTabs };
