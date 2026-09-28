'use client';

import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip';
import { coverageExplanation, coverageSummary, pct } from '@/lib/coverage-gap';
import type { CoverageAccountRow } from '@/lib/coverage-gap';
import type { CoverageGap } from '@/lib/types';
import { cn } from '@/lib/utils';

interface CoverageLineProps {
  gap: CoverageGap | undefined;
  /** The chart's range was too short and the trailing week was asked for instead. */
  trailingWeek?: boolean;
  /**
   * Whether an answer is coming. Distinguishes "not asked" -- coverage is off for
   * this chart, or the card is collapsed -- from "asked, still in flight". Only the
   * second reserves space; the first must take none, or every chart that never
   * shows coverage would carry a permanent empty strip.
   */
  pending?: boolean;
}

/**
 * The box the sentence lives in, applied to the reservation and to the sentence
 * itself so the two are the same height by construction rather than by two
 * numbers that have to be kept equal.
 *
 * Two lines, not one. The eligible summary runs ~150 characters and gains a
 * "N accounts unfitted" clause, which at a 1280px viewport lands close enough to
 * the card's inner width to wrap. Reserving one line would hold the card still on
 * a wide window and let it jump on a narrow one, which is the same bug with a
 * smaller audience.
 */
const COVERAGE_LINE_BOX = 'mt-1 flex h-8 items-center justify-center';

/** One account's line in the panel: email, its ratio, and the fit underneath. */
const CoverageAccountEntry = ({ row }: { row: CoverageAccountRow }) => (
  <li>
    <div className="flex items-baseline justify-between gap-3">
      <span className="truncate text-[11px]">{row.email}</span>
      {/* tabular-nums is the point of this panel. Ratios that do not line up in a
          column are read as prose, which is what made the old flat list unreadable. */}
      <span className={cn('shrink-0 text-[11px] tabular-nums', row.ratio === null ? 'opacity-60' : 'font-medium')}>
        {row.ratio === null ? 'unfitted' : `≈${pct(row.ratio)}`}
      </span>
    </div>
    <p className="text-[10px] opacity-60">
      {row.unfitted ?? `k $${row.kUsdPerPct.toFixed(2)}/% · ${row.fitIntervals} intervals`}
    </p>
  </li>
);

/**
 * One sentence under the legend answering "is our accounting complete for this
 * range", with the working shown on hover. It sits below the chart and centred
 * because it is a footnote about the whole plot, not a subtitle of the title.
 *
 * An ineligible answer is rendered too, in the same muted tone, so that the
 * absence of a number never reads as "nothing to report".
 *
 * While the answer is in flight the space is reserved but nothing is drawn. This
 * is not the placeholder this component used to refuse: the objection was that a
 * placeholder SENTENCE sits where the answer goes and gets mistaken for it, and
 * an empty box cannot be mistaken for a sentence. Drawing nothing while occupying
 * the height is what keeps the card from growing when the answer lands.
 *
 * The hover panel is ordered by how much of it is about THIS range. The method
 * paragraph is identical on every hover, so it is demoted; the caveats and the
 * per-account figures are not, so they are not. Hierarchy is carried by size and
 * opacity rather than colour -- the panel is already a dark surface, where colour
 * is the scarcest channel there is.
 */
const CoverageLine = ({ gap, trailingWeek = false, pending = false }: CoverageLineProps) => {
  if (!gap) return pending ? <div aria-hidden className={COVERAGE_LINE_BOX} /> : null;
  const explanation = coverageExplanation(gap);
  return (
    <TooltipProvider delayDuration={300}>
      <Tooltip>
        <TooltipTrigger asChild>
          <div className={COVERAGE_LINE_BOX}>
            <p className={cn('cursor-default text-center text-[10px] leading-4 opacity-50', gap.eligible ? 'text-ink-2' : 'text-ink-3')}>
              {coverageSummary(gap, trailingWeek)}
            </p>
          </div>
        </TooltipTrigger>
        {gap.eligible && (
          <TooltipContent className="max-w-[360px]">
            <div className="space-y-1 text-[10px] leading-relaxed opacity-70">
              {explanation.method.map((line) => <p key={line}>{line}</p>)}
            </div>
            {explanation.caveats.length > 0 && (
              <div className="mt-1.5 space-y-1 text-[10px] leading-relaxed">
                {explanation.caveats.map((line) => <p key={line}>{line}</p>)}
              </div>
            )}
            {explanation.accounts.length > 0 && (
              <>
                <hr className="my-1.5 border-border/40" />
                <ul className="space-y-1">
                  {explanation.accounts.map((row) => <CoverageAccountEntry key={row.email} row={row} />)}
                </ul>
              </>
            )}
          </TooltipContent>
        )}
      </Tooltip>
    </TooltipProvider>
  );
};

export { CoverageLine };
