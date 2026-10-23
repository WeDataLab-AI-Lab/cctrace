'use client';

import { cn } from '@/lib/utils';

// Per-bar delay creates a left-to-right wave, so the loader reads as data being
// actively collected rather than a frozen spinner. Nine bars 60ms apart span most of
// the 0.6s cycle, so the wave reads as a steady, fast stream.
//
// The delay is a position (--i) that `.cctrace-bar` in globals.css multiplies, not an
// `[animation-delay:..]` utility: that rule's `animation` shorthand sits outside the
// utilities layer and reset every utility delay to 0, so the bars used to pulse in
// unison.
const BAR_POSITIONS = ['[--i:0]', '[--i:1]', '[--i:2]', '[--i:3]', '[--i:4]', '[--i:5]', '[--i:6]', '[--i:7]', '[--i:8]'];

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
      {BAR_POSITIONS.map((position) => (
        <span
          key={position}
          className={cn('cctrace-bar h-full rounded-full bg-brand/70', compact ? 'w-[2px]' : 'w-[3px]', position)}
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
