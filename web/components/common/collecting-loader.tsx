'use client';

import { cn } from '@/lib/utils';

// Per-bar animation-delay creates a left-to-right wave, so the loader reads as data
// being actively collected rather than a frozen spinner.
const BAR_DELAYS = [
  '[animation-delay:0ms]',
  '[animation-delay:110ms]',
  '[animation-delay:220ms]',
  '[animation-delay:330ms]',
  '[animation-delay:440ms]',
];

interface CollectingLoaderProps {
  label?: string;
  className?: string;
  /**
   * Lay the bars and the label on one line at a smaller size.
   *
   * For places that load MORE of something already on screen -- the next page of an
   * infinite list -- where the full-height stacked version would push the rows it is
   * appending to. Same bars either way: one loading idiom, two sizes.
   */
  compact?: boolean;
}

const CollectingLoader = ({ label = 'Loading', className, compact = false }: CollectingLoaderProps) => (
  <div
    className={cn(
      compact
        ? 'flex items-center justify-center gap-2 py-1'
        : 'flex flex-col items-center justify-center gap-2.5 py-8',
      className,
    )}
    aria-busy="true"
  >
    <div className={cn('flex items-end gap-[3px]', compact ? 'h-2.5' : 'h-4')}>
      {BAR_DELAYS.map((delay) => (
        <span
          key={delay}
          className={cn('cctrace-bar h-full rounded-full bg-brand/70', compact ? 'w-[2px]' : 'w-[3px]', delay)}
        />
      ))}
    </div>
    <span className={cn('text-ink-3', compact ? 'text-[11px]' : 'text-[13px]')}>
      {label}
      <span className="animate-pulse">…</span>
    </span>
  </div>
);

export { CollectingLoader };
