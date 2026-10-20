'use client';

import type { SessionTraceIndex } from '@/lib/session-trace-index';
import { cn } from '@/lib/utils';

interface SessionRequestScopeProps {
  index?: SessionTraceIndex;
  loadedRequests: number;
  /** The reader's request among every request of the session, when the timeline knows them. */
  /** pending: numbered by message because the session's analysis is not ready. */
  position?: { current: number; total: number; pending?: boolean };
  hasPreviousPage: boolean;
  onLoadPrevious: () => void;
  previousPending: boolean;
  onJumpRequest?: (requestNo: number) => void;
}

// The first loaded record window is often a small tail of a long session. The
// request index is the only source of the full count, so it stays visible next
// to the loaded rail instead of being inferred from the current page.
const SessionRequestScope = ({ index, loadedRequests, position, hasPreviousPage, onLoadPrevious, previousPending, onJumpRequest }: SessionRequestScopeProps) => {
  const total = position?.total ?? index?.request_total ?? null;
  const status = position ? `요청 ${position.current}/${position.total}${position.pending ? ' · 분석 전' : ''}`
    : index?.state === 'ready' || index?.state === 'stale'
    ? total === null ? '요청 수 불명' : `요청 ${loadedRequests}/${total}`
    : index?.state === 'building' ? '요청 흐름 계산 중'
      : index?.state === 'none' ? '요청 흐름 없음'
        : '요청 흐름 불명';
  return <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[10px] text-ink-3">
    <span className="font-medium text-ink-2">{status}</span>
    {onJumpRequest && <form className="flex items-center gap-1" onSubmit={event => {
      event.preventDefault();
      const value = Number(new FormData(event.currentTarget).get('request_no'));
      if (Number.isInteger(value) && value > 0 && value <= (total ?? Infinity)) onJumpRequest?.(value);
    }}>
      <input name="request_no" type="number" min={1} max={total ?? undefined} aria-label="작업 요청 번호" placeholder="번호" className="w-12 rounded border border-border bg-surface px-1 py-0.5 text-[10px] text-ink" />
      <button type="submit" className="rounded px-1.5 py-0.5 hover:bg-surface-sunk">이동</button>
    </form>}
    {hasPreviousPage && <button type="button" onClick={onLoadPrevious} disabled={previousPending} className={cn('rounded px-1.5 py-0.5 text-ink-2 hover:bg-surface-sunk disabled:opacity-50')}>{previousPending ? '불러오는 중…' : '이전 기록'}</button>}
  </span>;
};

export { SessionRequestScope };
