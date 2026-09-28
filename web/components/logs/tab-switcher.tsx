'use client';

import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';
import type { Tab } from './logs-helpers';

interface TabSwitcherProps {
  tab: Tab;
  onTabChange: (next: Tab) => void;
}

const TABS: Tab[] = ['events', 'metrics'];

const TabSwitcher = ({ tab, onTabChange }: TabSwitcherProps) => (
  <div className="flex gap-1">
    {TABS.map((t) => (
      <Button
        key={t}
        onClick={() => onTabChange(t)}
        className={cn(
          'h-auto px-3 py-1 text-[12px] rounded-md font-medium transition-colors capitalize shadow-none',
          tab === t
            ? 'bg-brand text-brand-ink hover:bg-brand'
            : 'bg-surface-sunk text-ink-2 hover:bg-canvas hover:text-ink-2'
        )}
      >
        {t}
      </Button>
    ))}
  </div>
);

export { TabSwitcher };
