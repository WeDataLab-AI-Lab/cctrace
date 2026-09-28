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
}

const SOURCE_OPTIONS: readonly SourceOption[] = [
  { value: 'interactive', label: 'Interactive', hint: 'Sessions with real human turns' },
  { value: 'headless', label: 'Headless', hint: 'Machine-driven sessions with no human turns (claude -p, SDK/queue)' },
  { value: 'all', label: 'All', hint: 'Every session, interactive and headless' },
];

// Headless (claude -p, SDK/queue) sessions are machine-driven one-shots; hidden by
// default so they don't bury interactive work. Legacy ('') counts as interactive.
const SourceFilter = ({ selected, onSelect }: SourceFilterProps) => {
  const handleSelect = (value: SourceValue) => () => onSelect(value);
  return (
    <TooltipProvider delayDuration={300}>
            {/* Shrinks with the toolbar instead of pushing it off screen. The labels
          truncate before anything overflows -- a clipped word is recoverable (the
          tooltip still names it, and widening the window restores it); a control
          past the right edge is not. */}
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
    </TooltipProvider>
  );
};

export { SourceFilter };
export type { SourceValue };
