'use client';

import { useQuery } from '@tanstack/react-query';
import { fetchSessionAccountSegments } from '@/lib/api';
import type { SessionAccountSegment } from '@/lib/types';
import { fmt, fmtCost } from './session-utils';

interface AccountSegmentsProps {
  sessionId: string;
  /** From the session row. Rendering is skipped unless this is above 1. */
  accountCount?: number;
}

const timeLabel = (iso: string): string =>
  new Date(iso).toLocaleString(undefined, {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  });

/**
 * The stretches of one session that belonged to each account.
 *
 * The session list shows a single row per session because a session is one
 * conversation; this is the drill-down where a mid-session /login becomes
 * readable — which account held which stretch, and what each spent.
 */
const AccountSegments = ({ sessionId, accountCount }: AccountSegmentsProps) => {
  const multi = (accountCount ?? 1) > 1;

  const { data: segments } = useQuery({
    queryKey: ['session-account-segments', sessionId],
    queryFn: () => fetchSessionAccountSegments(sessionId),
    // A single-account session has nothing to split, so the request is not worth
    // making for the overwhelming majority of rows.
    enabled: multi && !!sessionId,
    staleTime: 60_000,
  });

  if (!multi || !segments || segments.length < 2) return null;

  return (
    <div className="mt-2 mb-3 rounded-md border border-border bg-surface-sunk px-3 py-2">
      <div className="text-[11px] font-medium text-ink-2 mb-1.5">
        계정 구간 {segments.length}개
      </div>
      <ol className="space-y-1">
        {segments.map((seg: SessionAccountSegment, i: number) => (
          <li key={`${seg.start_time}-${i}`} className="flex items-baseline gap-2 text-[11px]">
            <span className="text-ink-3 font-mono flex-shrink-0">
              {timeLabel(seg.start_time)}
            </span>
            <span className={seg.account ? 'text-ink truncate' : 'text-ink-3 italic truncate'}>
              {/* An empty account is a real state, not a rendering gap: those rows
                  predate account observation and were never backfillable. */}
              {seg.account || '계정 미상'}
            </span>
            <span className="ml-auto flex-shrink-0 text-ink-3">
              {fmt(seg.input_tokens)} / {fmt(seg.output_tokens)}
              {seg.cost_usd > 0 && <> · {fmtCost(seg.cost_usd)}</>}
            </span>
          </li>
        ))}
      </ol>
    </div>
  );
};

export { AccountSegments };
