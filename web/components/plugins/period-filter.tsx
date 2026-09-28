'use client';

import { cn } from '@/lib/utils';
import type { Period } from './usage-types';

interface PeriodFilterProps {
  periods: Period[];
  period: string;
  onSelect: (label: string) => void;
}

const PeriodFilter = ({ periods, period, onSelect }: PeriodFilterProps) => (
  <nav className="flex gap-1">
    {periods.map((p) => (
      <button
        key={p.label}
        onClick={() => onSelect(p.label)}
        className={cn(
          'px-3 py-1 rounded text-[12px] font-medium transition-colors',
          period === p.label
            ? 'bg-brand text-brand-ink'
            : 'bg-surface-sunk text-ink-2 hover:bg-canvas'
        )}
      >
        {p.label}
      </button>
    ))}
  </nav>
);

export { PeriodFilter };
