'use client';

import type { MouseEvent } from 'react';
import { cn } from '@/lib/utils';
import type { JudgementBucket, SessionBucketCounts } from '@/lib/session-analysis-types';

const BUCKETS: readonly { key: JudgementBucket; label: string; classes: string }[] = [
  { key: 'done', label: '완료', classes: 'border-success text-success-strong aria-pressed:bg-success-soft' },
  { key: 'leftover', label: '하다 만 것', classes: 'border-border-strong text-ink-2 aria-pressed:bg-surface-sunk' },
  { key: 'ended_midway', label: '도중 종료', classes: 'border-warning text-warning-strong aria-pressed:bg-warning-soft' },
  { key: 'unknown', label: '판정 불가', classes: 'border-dashed border-border-strong text-ink-3 aria-pressed:bg-surface-sunk' },
];

interface JudgementCountFiltersProps {
  counts?: SessionBucketCounts;
  value: JudgementBucket | null;
  onChange: (bucket: JudgementBucket | null) => void;
  loading?: boolean;
  error?: string | null;
}

const JudgementCountFilters = ({ counts, value, onChange, loading = false, error }: JudgementCountFiltersProps) => {
  const handleClick = (event: MouseEvent<HTMLButtonElement>) => {
    const bucket = event.currentTarget.dataset.bucket as JudgementBucket | undefined;
    if (!bucket) return;
    onChange(value === bucket ? null : bucket);
  };

  return (
    <div className="flex flex-wrap items-center gap-1.5" aria-label="작업 완료 판정 필터">
      {BUCKETS.map((bucket) => (
        <button
          key={bucket.key}
          type="button"
          data-bucket={bucket.key}
          aria-pressed={value === bucket.key}
          onClick={handleClick}
          disabled={!counts || loading || !!error}
          className={cn('inline-flex h-6 items-center gap-1 rounded-full border px-2.5 text-[11px] font-medium whitespace-nowrap disabled:cursor-not-allowed disabled:opacity-60', bucket.classes, value !== null && value !== bucket.key && 'opacity-55')}
        >
          {bucket.label} <span className="tabular-nums">{counts ? counts[bucket.key].toLocaleString('en-US') : '?'}</span>
        </button>
      ))}
      {loading && !counts && <p role="status" className="text-[10.5px] text-ink-3">판정 개수를 불러오는 중입니다.</p>}
      {error && <p role="alert" className="text-[10.5px] text-danger">{error}</p>}
      {!loading && !counts && !error && <p role="status" className="text-[10.5px] text-ink-3">판정 개수를 받지 못했습니다.</p>}
    </div>
  );
};

export { JudgementCountFilters, BUCKETS };
export type { JudgementCountFiltersProps };
