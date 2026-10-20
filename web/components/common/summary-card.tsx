import { Info } from 'lucide-react';
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip';
import { cn } from '@/lib/utils';

interface SummaryCardProps {
  title: string;
  value: string | number;
  subtitle?: string;
  note?: string;
  compact?: boolean;
  className?: string;
}

const SummaryCard = ({ title, value, subtitle, note, compact = false, className }: SummaryCardProps) => {
  return (
    <div className={cn('bg-surface rounded-xl border border-border shadow-[var(--sh-sm)]', compact ? 'h-full px-4 py-3' : 'py-5 px-6 group', className)}>
      {compact ? (
        <div className="mb-1 flex items-center justify-between gap-2">
          <p className="text-[13px] text-ink-3">{title}</p>
          {(subtitle || note) && (
            <TooltipProvider>
              <Tooltip>
                <TooltipTrigger asChild>
                  <button type="button" aria-label={`${title} 설명`} className="inline-flex size-6 shrink-0 items-center justify-center rounded-sm text-ink-3 hover:text-ink-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand/30">
                    <Info className="size-3.5" aria-hidden="true" />
                  </button>
                </TooltipTrigger>
                <TooltipContent className="max-w-xs space-y-1">
                  {subtitle && <p>{subtitle}</p>}
                  {note && <p>{note}</p>}
                </TooltipContent>
              </Tooltip>
            </TooltipProvider>
          )}
        </div>
      ) : <p className="text-[13px] text-ink-3 mb-2">{title}</p>}
      <p className={cn('text-[28px] font-semibold text-ink tabular-nums', compact && 'leading-tight')}>{value}</p>
      {!compact && subtitle && <p className="text-[12px] text-ink-3 mt-1">{subtitle}</p>}
      {!compact && note && (
        <p className="text-[10px] text-ink-4 mt-2 leading-tight opacity-0 group-hover:opacity-100 transition-opacity">
          {note}
        </p>
      )}
    </div>
  );
};

export { SummaryCard };
