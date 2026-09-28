'use client';

import { cn } from '@/lib/utils';
import { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider } from '@/components/ui/tooltip';

type ViewMode = 'assembled' | 'individual';

interface ModeOption {
  value: ViewMode;
  label: string;
  hint: string;
}

interface ModeToggleProps {
  selected: ViewMode;
  onSelect: (mode: ViewMode) => void;
}

const MODE_OPTIONS: readonly ModeOption[] = [
  { value: 'assembled', label: 'Assembled', hint: 'Merge related JSONL files (subagents, branches) into one conversation flow' },
  { value: 'individual', label: 'Raw', hint: "Show each JSONL file's records as-is, grouped by file" },
];

const ModeToggle = ({ selected, onSelect }: ModeToggleProps) => {
  const handleSelect = (mode: ViewMode) => () => onSelect(mode);
  return (
    <TooltipProvider delayDuration={300}>
            {/* Shrinks with the toolbar instead of pushing it off screen. The labels
          truncate before anything overflows -- a clipped word is recoverable (the
          tooltip still names it, and widening the window restores it); a control
          past the right edge is not. */}
      <div className="flex min-w-0 shrink gap-1.5">
        {MODE_OPTIONS.map(m => (
          <Tooltip key={m.value}>
            <TooltipTrigger asChild>
              <button
                onClick={handleSelect(m.value)}
                className={cn(
                  'min-w-0 truncate rounded px-2 py-1 text-[11px] font-medium transition-colors',
                  selected === m.value
                    ? 'bg-brand text-brand-ink'
                    : 'bg-surface-sunk text-ink-2 hover:bg-[var(--border)]',
                )}
              >
                {m.label}
              </button>
            </TooltipTrigger>
            <TooltipContent className="max-w-[220px]">{m.hint}</TooltipContent>
          </Tooltip>
        ))}
      </div>
    </TooltipProvider>
  );
};

export { ModeToggle };
export type { ViewMode };
