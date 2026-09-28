'use client';

import { Button } from '@/components/ui/button';

interface LogsPaginationProps {
  page: number;
  rowCount: number;
  hasMore: boolean;
  onPageChange: (next: number) => void;
}

const PAGER_CLASS =
  'h-auto px-3 py-1 text-[12px] rounded-md shadow-none border-border text-ink-2 disabled:opacity-30 hover:bg-surface-sunk hover:text-ink-2';

const LogsPagination = ({ page, rowCount, hasMore, onPageChange }: LogsPaginationProps) => {
  const handlePrev = () => onPageChange(Math.max(0, page - 1));
  const handleNext = () => onPageChange(page + 1);

  return (
    <div className="flex items-center justify-between shrink-0">
      <span className="text-[11px] text-ink-3">
        Page {page + 1} ({rowCount} rows)
      </span>
      <div className="flex gap-2">
        <Button variant="outline" onClick={handlePrev} disabled={page === 0} className={PAGER_CLASS}>
          Prev
        </Button>
        <Button variant="outline" onClick={handleNext} disabled={!hasMore} className={PAGER_CLASS}>
          Next
        </Button>
      </div>
    </div>
  );
};

export { LogsPagination };
