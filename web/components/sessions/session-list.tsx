'use client';

import { useEffect, useRef } from 'react';
import { cn } from '@/lib/utils';
import { CollectingLoader } from '@/components/common/collecting-loader';
import type { SessionOverview } from '@/lib/types';
import { CLASSIFICATION_LABEL } from './analysis/list-model';
import { CategoryChip } from './analysis/category-chip';
import { SessionAnalysisRow } from './analysis/session-analysis-row';
import { SessionGroupList } from './session-group-list';
import type { SessionGroupMember, SessionGroups } from '@/lib/session-groups-api';
import { fmt, fmtCost, fmtSessionPeriod, createSessionPeriodFormatters, projectLabel, isRecentlyActive } from './session-utils';

// Both agents are badged. Badging only codex made claude the unmarked default, but the
// product covers the two equally. Keyed by the raw agent value so a session with no agent
// recorded stays unbadged rather than being asserted as claude.
const AGENT_BADGE: Record<string, string> = {
  claude: 'bg-agent-claude-soft text-agent-claude',
  codex: 'bg-agent-codex-soft text-agent-codex',
};

const ANALYSIS_STATE_LABEL: Record<NonNullable<SessionOverview['analysis']>['state'], string> = {
  ready: '분석 완료',
  stale: '분석 규칙 갱신 중',
  building: '분석을 만드는 중',
  none: '분석 없음',
  truncated: '분석 범위 제한',
  unsupported: '분석 미지원',
  withheld: '분석 보류',
};

interface IntersectionLoadDecision {
  load: boolean;
  scrollPending: boolean;
}

interface VerticalBoundsSource {
  getBoundingClientRect: () => Pick<DOMRect, 'top' | 'bottom'>;
}

const LIST_LOAD_MARGIN = 300;

// An observer reports an already-visible target immediately when observation starts.
// Consume a separate scroll signal so that callback cannot turn initial render (or page
// settlement) into permission to fetch another page.
const decideIntersectionLoad = (scrollPending: boolean, isIntersecting: boolean): IntersectionLoadDecision => ({
  load: scrollPending && isIntersecting,
  scrollPending: false,
});

// IntersectionObserver is edge-triggered, so a target that stays inside its root margin
// does not report again after the ignored initial callback. The scroll path uses the same
// vertical margin against current geometry to re-evaluate that state synchronously.
const isWithinListLoadMargin = (root: VerticalBoundsSource, target: VerticalBoundsSource): boolean => {
  const rootBounds = root.getBoundingClientRect();
  const targetBounds = target.getBoundingClientRect();
  return targetBounds.bottom >= rootBounds.top - LIST_LOAD_MARGIN &&
    targetBounds.top <= rootBounds.bottom + LIST_LOAD_MARGIN;
};

const requestSessionPage = (isLoadingMore: boolean | undefined, onLoadMore: (() => void) | undefined) => {
  if (!isLoadingMore) onLoadMore?.();
};

interface SessionListProps {
  sessions: SessionOverview[];
  isLoading: boolean;
  selectedId: string | null;
  onSelect: (session: SessionOverview) => void;
  hasMore?: boolean;
  isLoadingMore?: boolean;
  onLoadMore?: () => void;
  total?: number;
  /** Sessions that arrived above the frozen order and are waiting to be shown. */
  pendingCount?: number;
  /**
   * Sessions the active account filter dropped because no account is known for
   * them (they never emitted OTEL). Shown rather than silently omitted: a
   * shorter list with no explanation reads as "there was nothing here".
   */
  unattributedCount?: number;
  onCommitPending?: () => void;
  groups?: SessionGroups;
  groupsBusy?: boolean;
  groupReadError?: boolean;
  groupWriteError?: boolean;
  onDismissGroup?: (members: readonly SessionGroupMember[], group?: SessionGroups['ended'][number], allMembers?: boolean) => void;
  onRestoreGroup?: (members: readonly SessionGroupMember[], group?: SessionGroups['ended'][number]) => void;
  /** Keep server row order under groups while an analysis filter or sort is active. */
  preserveGroupOrder?: boolean;
  /** Render server-provided analysis and owner-scoped P5 record fields in the real list. */
  analysisMode?: boolean;
}

const SessionList = ({ sessions, isLoading, selectedId, onSelect, hasMore, isLoadingMore, onLoadMore, total, pendingCount = 0, unattributedCount = 0, onCommitPending, groups, groupsBusy = false, groupReadError = false, groupWriteError = false, onDismissGroup, onRestoreGroup, preserveGroupOrder = false, analysisMode = false }: SessionListProps) => {
  const periodFormatters = createSessionPeriodFormatters();
  const scrollRef = useRef<HTMLDivElement>(null);
  const sentinelRef = useRef<HTMLDivElement>(null);
  const handleSelect = (session: SessionOverview) => () => onSelect(session);
  // This component owns the scroll container, so it also owns scrolling back to the top when
  // the newly revealed sessions land at the head.
  const handleCommitPending = () => {
    onCommitPending?.();
    scrollRef.current?.scrollTo({ top: 0 });
  };

  // `onLoadMore` is a fresh identity on every parent render (polling re-renders the page),
  // so depending on it would tear down and rebuild the observer each poll — and a rebuilt
  // observer fires immediately when the sentinel is already in view, sending duplicate
  // fetchNextPage calls. Hold the latest callback in a ref (same pattern as
  // session-detail.tsx) and key the observer on `hasMore` alone. React Compiler bans
  // manual memo, so useCallback is not an option.
  const onLoadMoreRef = useRef(onLoadMore);
  const scrollPending = useRef(false);

  useEffect(() => { onLoadMoreRef.current = onLoadMore; });

  // Infinite scroll is deliberately user-driven. Observation starts with an intersection
  // callback when a short first page leaves the sentinel visible; only a scroll occurring
  // after observation began may authorize that callback to load one more page.
  useEffect(() => {
    const s = sentinelRef.current;
    if (!s) return;
    scrollPending.current = false;
    const io = new IntersectionObserver(
      (es) => {
        const decision = decideIntersectionLoad(scrollPending.current, es[0].isIntersecting);
        scrollPending.current = decision.scrollPending;
        if (decision.load) onLoadMoreRef.current?.();
      },
      { root: scrollRef.current, rootMargin: `${LIST_LOAD_MARGIN}px` },
    );
    io.observe(s);
    return () => io.disconnect();
  }, [hasMore]);

  const handleScroll = () => {
    const root = scrollRef.current;
    const sentinel = sentinelRef.current;
    if (!root || !sentinel) {
      scrollPending.current = false;
      return;
    }
    scrollPending.current = true;
    const decision = decideIntersectionLoad(
      scrollPending.current,
      isWithinListLoadMargin(root, sentinel),
    );
    scrollPending.current = decision.scrollPending;
    if (decision.load) onLoadMoreRef.current?.();
  };

  return (
    // basis is the FLOOR here, not the preferred size. Flex hands out leftover space by
    // grow and takes shortfall by basis, so a list with basis 360 beside a pane with
    // basis 0 keeps its 360 at every width and the pane absorbs the whole squeeze --
    // measured: 754px of pane at 1400 down to 134px at 780, list unchanged at 359.
    //
    // Starting both at a small basis and letting them grow inverts that: 220 is what
    // the list is guaranteed, 360 is as wide as it may get, and everything past the
    // two floors is split by grow -- twice as much to the pane, which is what the page
    // is for. Below 220 the list's own rows wrap into noise, hence min-w.
    <div className="relative flex min-h-0 w-auto min-w-[220px] max-w-[288px] shrink grow basis-[220px] flex-col border-r border-border">
      {pendingCount > 0 && (
        <div className="flex shrink-0 items-center justify-end border-b border-border-subtle bg-surface px-4 py-1.5">
          <button
            type="button"
            onClick={handleCommitPending}
            className="rounded border border-border px-2 py-0.5 text-[10px] text-ink-2 hover:bg-surface-sunk"
          >
            새 세션 {pendingCount.toLocaleString()}개
          </button>
        </div>
      )}
      {unattributedCount > 0 && (
        <div className="border-b border-border bg-surface-sunk px-4 py-2 text-[11px] leading-relaxed text-ink-3">
          계정 정보가 없는 세션 <span className="font-medium text-ink-2">{unattributedCount.toLocaleString()}개</span>가 이 목록에서 제외됐습니다.
          <br />
          OTEL 수집 이전에 기록된 세션이라 어느 계정의 것인지 확인할 수 없습니다.
        </div>
      )}
      {groupReadError && <div role="alert" className="border-b border-border px-4 py-2 text-xs text-danger">세션 묶음을 읽지 못했습니다. 다시 불러오세요.</div>}
      {groupWriteError && <div role="alert" className="border-b border-border px-4 py-2 text-xs text-danger">세션 묶음 변경을 저장하지 못했습니다. 일부 변경이 저장되지 않았을 수 있으니 목록을 다시 확인해 주세요.</div>}
      <div
        ref={scrollRef}
        data-session-return-scroll="list"
        // min-w-0: this scroller is the list's only flexible child, so its min-content
        // width becomes the list's floor. Left at auto it held the column at 359px no
        // matter what basis or min-w said above it.
        className="min-w-0 flex-1 overflow-y-auto"
        onScroll={handleScroll}
      >
        {isLoading ? (
          <CollectingLoader className="py-8" />
        ) : sessions.length === 0 ? (
          <div className="px-4 py-8 text-center text-sm text-ink-3">
            <div>No sessions</div>
            {hasMore && onLoadMore && (
              <button
                type="button"
                data-testid="empty-session-load-more"
                onClick={() => requestSessionPage(isLoadingMore, onLoadMoreRef.current)}
                disabled={isLoadingMore}
                className="mt-3 rounded-md border border-border bg-surface px-3 py-1.5 text-xs font-medium text-ink-2 hover:bg-canvas disabled:cursor-not-allowed disabled:opacity-60"
              >
                {isLoadingMore ? 'Searching next page…' : 'Search next page'}
              </button>
            )}
          </div>
        ) : (
          <>
            <SessionGroupList rows={sessions} groups={groups} preserveOrder={preserveGroupOrder} busy={groupsBusy || !onDismissGroup || !onRestoreGroup} onDismiss={(members, group, allMembers) => onDismissGroup?.(members, group, allMembers)} onRestore={(members, group) => onRestoreGroup?.(members, group)} renderRow={(s, action) => {
              const isSelected = selectedId === s.session_id;
              if (analysisMode && s.analysis) {
                const live = groups ? groups.running.some((item) => item.session_id === s.session_id) : isRecentlyActive(s.end_time);
                return (
                  <div key={s.session_id}>
                    <SessionAnalysisRow session={s} selected={isSelected} onSelect={onSelect} live={live} />
                    {action && <div className="border-b border-border/50 px-3 pb-2">{action}</div>}
                  </div>
                );
              }
              return (
                <div key={s.session_id} className="relative border-b border-border/50">
                <button
                  type="button"
                  onClick={handleSelect(s)}
                  aria-pressed={isSelected}
                  className={cn(
                    'w-full px-4 py-3 text-left cursor-pointer hover:bg-canvas focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-brand',
                    isSelected && 'bg-surface-sunk border-l-2 border-l-brand hover:bg-surface-sunk',
                  )}
                >
                  <div className="flex items-center gap-2 mb-1">
                    <span className="font-mono text-xs text-ink-2">
                      {s.session_id?.slice(0, 8) ?? '-'}
                    </span>
                    {s.has_sync && (
                      <span className="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium bg-surface-sunk text-ink-3">
                        sync
                      </span>
                    )}
                    {(() => {
                      // An observed live instance is a fact; a recent record is only activity.
                      // Both stay neutral so a healthy run never reads as an error.
                      const running = groups ? groups.running.some((item) => item.session_id === s.session_id) : false;
                      const activity = !running && (groups ? isRecentlyActive(s.end_time) : isRecentlyActive(s.end_time));
                      if (!running && !activity) return null;
                      return <span className="inline-flex items-center gap-1 border-l border-border pl-1.5 text-[10px] text-ink-2">
                        <span className="size-1.5 rounded-full border border-ink-3" aria-hidden="true" />
                        {running ? '실행 중' : '활동 중'}
                      </span>;
                    })()}
                    {AGENT_BADGE[s.agent] && (
                      <span className={cn('inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium', AGENT_BADGE[s.agent])}>
                        {s.agent}
                      </span>
                    )}
                    {s.classification && Object.hasOwn(CLASSIFICATION_LABEL, s.classification) && (
                      <span className="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium bg-brand-soft text-brand">
                        {CLASSIFICATION_LABEL[s.classification]}
                      </span>
                    )}
                    {s.analysis?.category && <CategoryChip category={s.analysis.category} />}
                  </div>
                  {s.analysis?.title?.state === 'ok' && s.analysis.title.text && (
                    <div className="mb-0.5 truncate text-xs text-ink" title={s.analysis.title.text}>{s.analysis.title.text}</div>
                  )}
                  {s.first_line && <div className="text-xs text-ink mb-0.5 truncate" title={s.first_line}>{s.first_line}</div>}
                  <div className="text-xs text-ink mb-0.5 truncate">{s.profile_email || '-'}</div>
                  {(s.project_name || s.project_hash) && (
                    <div className="text-[11px] text-ink-3 mb-0.5 truncate">{projectLabel(s)}</div>
                  )}
                  {s.user_id && (
                    <div className="text-[11px] text-ink-3 mb-1 truncate">
                      <span>ID: {s.user_id}</span>
                    </div>
                  )}
                  {s.login_email && (
                    <div className="flex items-center gap-1.5 text-[11px] text-ink-3 mb-1">
                      <span className="truncate">{s.login_email}</span>
                      {/* The session spans an account switch, so the row's account is
                          only the dominant one and the totals below cover both. */}
                      {(s.account_count ?? 1) > 1 && (
                        <span
                          title={`이 세션은 계정 ${s.account_count}개에 걸쳐 있습니다. 표시된 계정은 사용량이 가장 많은 쪽이며, 아래 토큰·비용은 전체 합계입니다.`}
                          className="flex-shrink-0 inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium bg-warning-soft text-warning-strong"
                        >
                          계정 {s.account_count}
                        </span>
                      )}
                    </div>
                  )}
                  {/* A row still being analyzed carries no analysis values (the server sends only
                      its state), so the line is left out rather than shown as "분석을 만드는 중". */}
                  {s.analysis && s.analysis.state !== 'building' && (
                    <div className="mb-1 flex flex-wrap gap-2 text-[11px] text-ink-3">
                      <span>{ANALYSIS_STATE_LABEL[s.analysis.state]}</span>
                      {s.analysis.requests != null && <span>요청 {s.analysis.requests.toLocaleString()}건</span>}
                      {s.analysis.inputs != null && <span>입력 {s.analysis.inputs.toLocaleString()}건</span>}
                      {s.analysis.plan_flags?.has_plan_file && <span>플랜 파일</span>}
                      {s.analysis.plan_flags?.used_plan_mode && <span>플랜 모드</span>}
                    </div>
                  )}
                  <div className="flex gap-3 text-[11px] text-ink-3">
                    <span>In: <span className="text-ink-2 font-medium">{fmt(s.input_tokens)}</span></span>
                    <span>Out: <span className="text-ink-2 font-medium">{fmt(s.output_tokens)}</span></span>
                    <span>Cost: <span className="text-ink-2 font-medium">{fmtCost(s.cost_usd)}</span></span>
                  </div>
                  <div className="text-[11px] text-ink-3 mt-1">
                    {fmtSessionPeriod(s.start_time, s.end_time, periodFormatters)}
                  </div>
                </button>
                {action && <div className="px-4 pb-2">{action}</div>}
                </div>
              );
            }} />
            {hasMore && (
              <div ref={sentinelRef} className="px-4 py-3 text-center">
                {isLoadingMore && <CollectingLoader compact label="더 불러오는 중" />}
              </div>
            )}
          </>
        )}
      </div>
      {sessions.length > 0 && (
        <div className="flex-shrink-0 px-4 py-2 bg-canvas border-t border-border text-xs text-ink-3">
          {total != null
            ? `${sessions.length.toLocaleString()} / ${total.toLocaleString()}개`
            : `${sessions.length.toLocaleString()}개${hasMore ? '+' : ''} 로드됨`}
        </div>
      )}
    </div>
  );
};

export { SessionList, decideIntersectionLoad, isWithinListLoadMargin, requestSessionPage };
