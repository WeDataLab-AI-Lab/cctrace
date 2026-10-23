'use client';

import { TableCell, TableRow } from '@/components/ui/table';
import { cn } from '@/lib/utils';
import { CollectingLoader } from './collecting-loader';

// Row and column positions for the stagger delays in globals.css. Tailwind only
// generates classes it finds written out, so each position is listed literally.
const ROW = [
  '[--i:0]', '[--i:1]', '[--i:2]', '[--i:3]', '[--i:4]', '[--i:5]', '[--i:6]', '[--i:7]', '[--i:8]', '[--i:9]',
  '[--i:10]', '[--i:11]', '[--i:12]', '[--i:13]', '[--i:14]', '[--i:15]', '[--i:16]', '[--i:17]', '[--i:18]', '[--i:19]',
];
const COLUMN = ['[--j:0]', '[--j:1]', '[--j:2]', '[--j:3]', '[--j:4]', '[--j:5]', '[--j:6]', '[--j:7]', '[--j:8]', '[--j:9]'];

// A fixed pattern, not Math.random: server and client must draw the same rows.
const WIDTHS = ['w-[72%]', 'w-[48%]', 'w-[60%]', 'w-[84%]', 'w-[40%]', 'w-[66%]', 'w-[54%]', 'w-[78%]'];
const width = (row: number, col: number) => WIDTHS[(row * 3 + col) % WIDTHS.length];

const Loader = () => (
  <div role="status">
    <CollectingLoader compact />
  </div>
);

interface TableLoadingRowsProps {
  columns: number;
  rows?: number;
}

/**
 * Ghost rows for a table body while its data loads, with the bar loader under them.
 * Rows fill in from the top, and the light band crosses them on a slant: each row and
 * column starts it a step later (`cctrace-ghost-row` in globals.css).
 */
const TableLoadingRows = ({ columns, rows = 8 }: TableLoadingRowsProps) => (
  <>
    {Array.from({ length: rows }, (_, r) => (
      <TableRow key={r} aria-hidden="true" className={cn('cctrace-row-in cctrace-ghost-row border-border/50 hover:bg-transparent', ROW[r])}>
        {Array.from({ length: columns }, (_, c) => (
          <TableCell key={c} className={cn('px-4 py-3', COLUMN[c])}>
            <span className={cn('cctrace-shimmer block h-3 rounded', width(r, c))} />
          </TableCell>
        ))}
      </TableRow>
    ))}
    <TableRow className="border-border/50 hover:bg-transparent">
      <TableCell colSpan={columns} className="py-3">
        <Loader />
      </TableCell>
    </TableRow>
  </>
);

/** The same ghost rows for a list that is not a table. */
const ListLoadingRows = ({ rows = 8 }: { rows?: number }) => (
  <div>
    {Array.from({ length: rows }, (_, r) => (
      <div key={r} aria-hidden="true" className={cn('cctrace-row-in cctrace-ghost-row flex items-center gap-3 px-4 py-3', ROW[r])}>
        <span className={cn('cctrace-shimmer block h-3 rounded', width(r, 0))} />
        <span className={cn('cctrace-shimmer ml-auto block h-3 w-10 shrink-0 rounded', COLUMN[1])} />
      </div>
    ))}
    <div className="py-3">
      <Loader />
    </div>
  </div>
);

/**
 * Entrance for the real rows once they arrive: the first twenty appear from the top
 * 30ms apart. Rows past them -- a long table, or a page appended by infinite scroll --
 * fade in with no delay rather than queueing behind an ever-longer one.
 *
 * Every row carries the class, so a re-sort that lifts row 25 into the top twenty only
 * changes its delay. Adding the class at that moment would start a fresh entrance and
 * hide the row (fill-mode both) for the length of its new delay.
 */
const rowIn = (index: number): string => `cctrace-row-in ${ROW[index] ?? ROW[0]}`;

export { ListLoadingRows, TableLoadingRows, rowIn };
