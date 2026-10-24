'use client';

import { Info } from 'lucide-react';
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip';
import { useCountUp } from '@/lib/use-count-up';
import { cn } from '@/lib/utils';

interface SummaryCardProps {
  title: string;
  /** What the card shows once the data is in and any count-up has landed. */
  value: string | number;
  /**
   * The figure behind `value`, for the count-up. Defaults to `value` when that is a
   * number; pass it when `value` is a formatted string ("58.0M", "$12.34").
   */
  count?: number;
  /** Formats the count-up's in-between frames. Defaults to a rounded locale number. */
  format?: (n: number) => string;
  /** The data is still on its way: hold a placeholder instead of an empty-list 0. */
  loading?: boolean;
  subtitle?: string;
  note?: string;
  compact?: boolean;
  className?: string;
}

const fmtCount = (n: number) => Math.round(n).toLocaleString();

// Stays mounted through loading so it can tell "the data just arrived" (climb from 0)
// from "the page opened from cache" (show at once).
const SummaryValue = ({ value, count, format = fmtCount, loading = false }: Pick<SummaryCardProps, 'value' | 'count' | 'format' | 'loading'>) => {
  const target = count ?? (typeof value === 'number' ? value : 0);
  const shown = useCountUp(target, !loading);
  if (loading) {
    return <span aria-busy="true" className="cctrace-shimmer inline-block h-[0.8em] w-24 rounded-md align-middle" />;
  }
  return <>{shown === target ? value : format(shown)}</>;
};

const SummaryCard = ({ title, value, count, format, loading, subtitle, note, compact = false, className }: SummaryCardProps) => {
  return (
    <div className={cn('min-w-0 bg-surface rounded-xl border border-border shadow-[var(--sh-sm)]', compact ? 'h-full px-4 py-3 max-md:flex max-md:items-center max-md:justify-between max-md:gap-3' : 'py-5 px-6 group', className)}>
      {compact ? (
        <div className="mb-1 flex items-center justify-between gap-2 max-md:mb-0">
          <p className="text-[13px] text-ink-3">{title}</p>
          {(subtitle || note) && (
            <TooltipProvider>
              <Tooltip>
                <TooltipTrigger asChild>
                  <button type="button" aria-label={`${title} 설명`} className="inline-flex size-11 shrink-0 items-center justify-center rounded-sm text-ink-3 hover:text-ink-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand/30 lg:size-6">
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
      <p className={cn('min-w-0 text-[28px] font-semibold text-ink tabular-nums break-all', compact && 'leading-tight max-md:text-right max-md:text-[24px]')}>
        <SummaryValue value={value} count={count} format={format} loading={loading} />
      </p>
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
