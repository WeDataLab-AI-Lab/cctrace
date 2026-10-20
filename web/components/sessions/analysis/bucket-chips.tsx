'use client';

import { Lock } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { JudgementBucket, SessionBucketCounts } from '@/lib/session-analysis-types';
import { formatCount } from './format';
import { BUCKET_HINT, BUCKET_LABEL, BUCKET_ORDER } from './judgement';

/**
 * 세션 화면 툴바의 네 개수(B3): 완료 · 하다 만 것 · 도중 종료 · 판정 불가. 필터로 동작한다 — 누른 칩이 `value`가 되고
 * 다시 누르면 해제다. 네 범주의 합이 현재 판정 범위의 세션 수다. 판정은 세션 주인에게만 온다.
 * 툴바 자체는 다른 곳이 갖는다; 이 칩 셋이 그 안에 들어간다.
 */

const CHIP_CLASS: Record<JudgementBucket, string> = {
  done: 'border-success/40 text-success-strong',
  leftover: 'border-border-strong text-ink-2',
  ended_midway: 'border-warning/50 text-warning-strong',
  unknown: 'border-dashed border-border-strong text-ink-3',
};

interface BucketCountChipsProps {
  counts: SessionBucketCounts;
  value: JudgementBucket | null;
  onChange: (next: JudgementBucket | null) => void;
  /** 비소유 관리자: 판정 개수는 API가 주지 않는다(집계 경로 보호). */
  unavailable?: boolean;
}

const BucketCountChips = ({ counts, value, onChange, unavailable = false }: BucketCountChipsProps) => {
  if (unavailable) {
    return (
      <p className="flex items-center gap-1.5 text-[11px] text-ink-3">
        <Lock size={12} strokeWidth={1.75} aria-hidden="true" />
        상태별 개수는 세션 주인에게만 제공됩니다
      </p>
    );
  }
  return (
    <div role="group" aria-label="상태별 개수" className="flex flex-wrap items-center gap-1">
      {BUCKET_ORDER.map((bucket) => {
        const selected = value === bucket;
        return (
          <button
            key={bucket}
            type="button"
            aria-pressed={selected}
            title={BUCKET_HINT[bucket]}
            onClick={() => onChange(selected ? null : bucket)}
            className={cn(
              'inline-flex h-[22px] items-center gap-1 rounded-full border px-2 text-[11px] font-medium whitespace-nowrap transition-colors',
              selected ? 'bg-surface-sunk' : 'bg-surface hover:bg-surface-sunk',
              CHIP_CLASS[bucket],
            )}
          >
            {BUCKET_LABEL[bucket]}
            <span className="tabular-nums">{formatCount(counts[bucket])}</span>
          </button>
        );
      })}
    </div>
  );
};

export { BucketCountChips };
