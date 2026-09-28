'use client';

import { useEffect, useRef, useState } from 'react';
import type {
  KeyboardEvent as ReactKeyboardEvent,
  TouchEvent as ReactTouchEvent,
  WheelEvent as ReactWheelEvent,
} from 'react';
import { useInfiniteQuery } from '@tanstack/react-query';
import { fetchSessionRecordsPage, SESSION_PAGE_SIZE } from '@/lib/api';
import { cn } from '@/lib/utils';
import { UserPromptRail } from './user-prompt-rail';
import { CollectingLoader } from '@/components/common/collecting-loader';
import type { SessionRecord } from '@/lib/types';
import type { AssembledNode, ConversationItem } from './session-utils';
import { buildAssembled, collectUserPrompts, isLegacySession, dedupeRecords, isRecentlyActive, conversationItemKey, dedupeKeys, anchorAtOrAfter } from './session-utils';
import { ToolPairBlock } from './tool-pair-block';
import { ConversationMarkdown, ConversationContent } from './conversation-markdown';
import type { ViewMode } from './mode-toggle';
import { IndividualView } from './raw-record-list';
import { TimelineNavigator } from './timeline-navigator';
import { anchorOf } from './timeline-scale';

type NavigateFn = (sessionId: string, anchorUuid?: string) => void;

interface SessionDetailProps {
  sessionId: string;
  mode: ViewMode;
  onNavigate: NavigateFn;
  scrollAnchor?: string;
  /** ISO instant to land on, for callers that know a time and not a message. */
  scrollFromTs?: string;
  /** The overview's whole-session has_enriched. Undefined when the API build omits it. */
  hasEnriched?: boolean;
}

interface BubbleProps {
  item: ConversationItem;
}

const ConversationBubble = ({ item }: BubbleProps) => {
  // Local/slash command scaffolding renders as a small system chip, not a user turn.
  if (item.command) {
    return (
      <div className="flex items-center gap-2 py-0.5 text-[11px] text-ink-3">
        <span className="font-mono px-1.5 py-0.5 rounded bg-surface-sunk text-ink-2">{item.command}</span>
        {item.commandArgs && <span className="font-mono text-ink-2">{item.commandArgs}</span>}
        <span>command</span>
      </div>
    );
  }
  const isUser = item.role === 'user';
  return (
    <div className="flex gap-3">
      <div className={cn(
        'flex-shrink-0 w-6 h-6 rounded-full flex items-center justify-center text-xs font-bold mt-0.5',
        isUser ? 'bg-surface-sunk text-brand' : 'bg-border text-ink-2',
      )}>
        {isUser ? 'U' : 'A'}
      </div>
      <div className={cn(
        // min-w-0: without it a flex child won't shrink below its content's intrinsic
        // width, so a long unbreakable run (a ───── rule, a long LaTeX token) pushes the
        // bubble — and its right border — past the container. With it, break-words wraps.
        'flex-1 min-w-0 rounded-lg px-3 py-2 text-xs',
        isUser ? 'bg-surface-sunk/50 text-ink' : 'bg-surface border border-border text-ink',
      )}>
        {item.agentTask && (
          <div className="mb-1 font-mono text-[10px] tracking-wide text-brand">상위 에이전트 지시</div>
        )}
        {item.text && <ConversationContent text={item.text} />}
        {item.tools.map((t, j) => <ToolPairBlock key={t.id ?? `${t.name}@${t.startTs}#${j}`} pair={t} />)}
      </div>
    </div>
  );
};

interface AsideGroupProps {
  label: string;
  items: ConversationItem[];
}

// An async side thread (btw/subagent), collapsed by default on an indigo rail.
const AsideGroup = ({ label, items }: AsideGroupProps) => {
  const [open, setOpen] = useState(false);
  const toggle = () => setOpen(o => !o);
  return (
    <div className="pl-3 border-l-2 border-brand">
      <button onClick={toggle} className="flex items-center gap-2 py-1 w-full text-left">
        <span className="w-1.5 h-1.5 rounded-full bg-brand flex-shrink-0" />
        <span className="font-mono text-[10px] tracking-wide text-brand">{label}</span>
        <span className="rounded bg-[var(--accent-soft)] px-1 py-px text-[9px] font-mono text-brand">async</span>
        <span className="text-[10px] text-ink-3">{items.length}</span>
        <span className="ml-auto text-[10px] text-ink-3">{open ? '▾' : '▸'}</span>
      </button>
      {open && (
        <div className="space-y-3 pt-1 pb-1">
          {dedupeKeys(items.map(item => ({ key: conversationItemKey(item), item }))).map(({ key, item }) => <ConversationBubble key={key} item={item} />)}
        </div>
      )}
    </div>
  );
};

interface CompactSummaryProps {
  item: ConversationItem;
}

// The /compact context summary — collapsed by default; expand for the full summary
// (never truncated). The full pre-compaction conversation lives one pointer away.
const CompactSummary = ({ item }: CompactSummaryProps) => {
  const [open, setOpen] = useState(false);
  const toggle = () => setOpen(o => !o);
  return (
    <div className="border-l-2 border-[var(--warning)] pl-3">
      <button onClick={toggle} className="flex items-center gap-2 py-1 w-full text-left">
        <span className="font-mono text-[10px] tracking-wide text-[var(--warning-strong)]">압축 요약</span>
        <span className="text-[10px] text-ink-3">컨텍스트 압축본</span>
        <span className="ml-auto text-[10px] text-ink-3">{open ? '▾' : '▸'}</span>
      </button>
      {open && item.text && (
        <div className="text-xs text-ink py-1 opacity-90">
          <ConversationMarkdown text={item.text} />
        </div>
      )}
    </div>
  );
};

interface PointerProps {
  targetSession: string;
  anchorUuid?: string;
  label: string;
  onNavigate: NavigateFn;
}

// A branch / compaction points back to its origin session instead of embedding it
// (audit rule: don't duplicate; link). Clicking navigates; missing targets pop up.
const LineagePointer = ({ targetSession, anchorUuid, label, onNavigate }: PointerProps) => {
  const go = () => onNavigate(targetSession, anchorUuid);
  return (
    <button
      onClick={go}
      className="flex items-center gap-2 rounded border border-border bg-surface px-2.5 py-1 text-[11px] text-ink-2 hover:border-brand hover:text-brand transition-colors"
    >
      <span>←</span>
      <span>{label}</span>
      <span className="font-mono text-ink-3">{targetSession.slice(0, 8)}</span>
      <span className="text-brand">→</span>
    </button>
  );
};

const SEAM_LABEL: Record<string, string> = {
  aside: 'text-brand',
  main: 'text-ink-4',
  compaction: 'text-[var(--warning-strong)]',
};
const SEAM_DOT: Record<string, string> = {
  aside: 'bg-brand',
  main: '',
  compaction: 'bg-[var(--warning)]',
};

const INITIAL_RENDER = 10; // paint the first turns instantly
const RENDER_CHUNK = 60;   // then grow in small steps as the sentinel scrolls into view

interface DetailIntersectionDecision {
  advance: boolean;
  scrollPending: boolean;
}

interface VerticalBoundsSource {
  getBoundingClientRect: () => Pick<DOMRect, 'top' | 'bottom'>;
}

interface DetailScrollMetrics {
  scrollTop: number;
  clientHeight: number;
  scrollHeight: number;
}

const DETAIL_ADVANCE_MARGIN = 400;
const FORWARD_DETAIL_KEYS = new Set(['ArrowDown', 'PageDown', 'End', ' ']);

// Observation reports an already-visible sentinel immediately. Require a separate user
// scroll signal so initial observation and render/page settlement cannot advance paging.
const decideDetailIntersectionAdvance = (scrollPending: boolean, isIntersecting: boolean): DetailIntersectionDecision => ({
  advance: scrollPending && isIntersecting,
  scrollPending: false,
});

// Recompute the observer's vertical root-margin test on scroll. This covers the event
// sequence where the sentinel was intersecting at observe time and never crosses an edge.
const isWithinDetailAdvanceMargin = (root: VerticalBoundsSource, target: VerticalBoundsSource): boolean => {
  const rootBounds = root.getBoundingClientRect();
  const targetBounds = target.getBoundingClientRect();
  return targetBounds.bottom >= rootBounds.top - DETAIL_ADVANCE_MARGIN &&
    targetBounds.top <= rootBounds.bottom + DETAIL_ADVANCE_MARGIN;
};

/**
 * The container scrollTop that puts `el` in the middle of `root`.
 *
 * Replaces scrollIntoView, which scrolls every scrollable ancestor -- the document
 * included. The dashboard shell is h-screen over a body that only sets min-height,
 * so a jump moved the page itself and left the shell's background exposed below the
 * app: the sidebar was cut off at the same line as the conversation, and scrolling
 * the conversation could not put it back because the page is a different scroller
 * (#677). Applying the offset to the container alone keeps the jump inside the view
 * that owns it.
 */
export const centerScrollTop = (
  root: { scrollTop: number; top: number; height: number },
  el: { top: number; height: number },
): number => Math.max(0, root.scrollTop + (el.top - root.top) - (root.height - el.height) / 2);

// Forward intent needs a direct path only when the browser cannot move the container and
// therefore cannot emit the scroll event that normally authorizes an advance.
const shouldAdvanceDetailWithoutScroll = (forward: boolean, root: DetailScrollMetrics): boolean =>
  forward && root.scrollTop + root.clientHeight >= root.scrollHeight;

const ACTIVE_POLL_MS = 5_000;

/**
 * Where in the node list an anchor lands, or -1.
 *
 * `shown` grows to cover this index, so a target the list has not rendered yet can
 * still be scrolled to. Aside nodes count too: an item-only test left them at -1,
 * `shown` never grew, the element was never rendered, and the jump ended in a
 * querySelector that found nothing -- no error, no movement.
 *
 * anchorOf is the one definition the strip, the DOM attribute and this lookup all
 * read, so the three cannot drift apart again.
 */
export const anchorIndex = (nodes: AssembledNode[], anchor: string): number =>
  nodes.findIndex(n => anchorOf(n) === anchor);

const lastRecordTs = (pages: SessionRecord[][] | undefined): string | undefined => {
  const last = pages?.[pages.length - 1];
  return last?.[last.length - 1]?.ts;
};

const SessionDetail = ({ sessionId, mode, onNavigate, scrollAnchor, scrollFromTs, hasEnriched }: SessionDetailProps) => {
  const assembled = mode === 'assembled';
  // The stretch the navigator has selected, tinted in the body below. Null when nothing
  // is selected, which is the ordinary state.
  const [navRange, setNavRange] = useState<{ startMs: number; endMs: number } | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const sentinelRef = useRef<HTMLDivElement>(null);
  const scrollPending = useRef(false);
  const touchStartY = useRef<number | null>(null);
  const touchIntentConsumed = useRef(false);
  const scrolledFor = useRef<string | undefined>(undefined);
  const { data, isLoading, fetchNextPage, hasNextPage, isFetchingNextPage } = useInfiniteQuery({
    queryKey: ['session-records', sessionId, assembled],
    queryFn: ({ pageParam }: { pageParam: number }) => fetchSessionRecordsPage({ sessionId, offset: pageParam, lineage: assembled }),
    initialPageParam: 0,
    getNextPageParam: (lastPage: SessionRecord[], allPages) =>
      lastPage.length === SESSION_PAGE_SIZE ? allPages.length * SESSION_PAGE_SIZE : undefined,
    staleTime: Infinity,
    refetchInterval: (query) => isRecentlyActive(lastRecordTs(query.state.data?.pages)) ? ACTIVE_POLL_MS : false,
  });

  // Server returns ascending (oldest first), so pages flatten straight into reading order;
  // then collapse legacy/enriched duplicate copies before assembling. (React Compiler memoizes.)
  const records = dedupeRecords(data?.pages.flat() ?? []);
  const nodes = buildAssembled(records);
  const legacy = isLegacySession(records, hasEnriched);
  const total = assembled ? nodes.length : records.length;

  // Two-level lazy render: grow the render window (visible) over the loaded pages; once it
  // reaches the end of what's loaded, pull the next page. `shown` also always covers a
  // pointer's target node (when loaded) so scroll-to-anchor lands. visible resets via a key
  // on this component (session/mode change remounts it).
  const [visible, setVisible] = useState(INITIAL_RENDER);
  // Jumping from the prompt rail reuses the pointer machinery below: same anchor lookup,
  // same window growth, same scroll-and-highlight. Only the source of the anchor differs.
  const [jumpAnchor, setJumpAnchor] = useState<string | undefined>(undefined);
  const [promptsOnly, setPromptsOnly] = useState(false);
  // A Work Segment knows when it happened, not which message started it, so it
  // arrives as a timestamp and is resolved here against the loaded records. The
  // first item at or after that instant is the one to land on; everything after
  // is the segment. Resolving to an anchor id rather than adding a second anchor kind
  // keeps one scroll-and-highlight path instead of two (#434).
  const timeAnchor = assembled ? anchorAtOrAfter(nodes, scrollFromTs) : undefined;
  const activeAnchor = jumpAnchor ?? scrollAnchor ?? timeAnchor;
  const anchorIdx = assembled && activeAnchor ? anchorIndex(nodes, activeAnchor) : -1;
  const shown = anchorIdx >= 0 ? Math.max(visible, anchorIdx + 20) : visible;
  const showSentinel = shown < total || hasNextPage;

  // Sentinel advance: render more of the loaded pages first, then pull the next page. Held
  // in a ref so the observer is created only while the sentinel exists yet always sees
  // fresh state (React Compiler bans manual memo, so no useCallback).
  const advance = () => {
    if (visible < total) setVisible(v => Math.min(v + RENDER_CHUNK, total));
    else if (hasNextPage && !isFetchingNextPage) fetchNextPage();
  };
  const advanceRef = useRef(advance);
  useEffect(() => { advanceRef.current = advance; });

  // Lazy rendering and page fetches are user-driven. In particular, the observer's initial
  // callback must not drain pages merely because the sentinel remains inside the root margin.
  useEffect(() => {
    const s = sentinelRef.current;
    if (!s || !showSentinel) return;
    scrollPending.current = false;
    const io = new IntersectionObserver(
      (es) => {
        const decision = decideDetailIntersectionAdvance(scrollPending.current, es[0].isIntersecting);
        scrollPending.current = decision.scrollPending;
        if (decision.advance) advanceRef.current();
      },
      { root: scrollRef.current, rootMargin: `${DETAIL_ADVANCE_MARGIN}px` },
    );
    io.observe(s);
    return () => io.disconnect();
  }, [showSentinel]);

  const advanceFromCurrentGeometry = (): boolean => {
    const root = scrollRef.current;
    const sentinel = sentinelRef.current;
    if (!root || !sentinel) {
      scrollPending.current = false;
      return false;
    }
    scrollPending.current = true;
    const decision = decideDetailIntersectionAdvance(
      scrollPending.current,
      isWithinDetailAdvanceMargin(root, sentinel),
    );
    scrollPending.current = decision.scrollPending;
    if (decision.advance) advanceRef.current();
    return decision.advance;
  };

  const handleScroll = () => { advanceFromCurrentGeometry(); };
  const handleForwardIntent = (forward: boolean): boolean => {
    const root = scrollRef.current;
    return !!root && shouldAdvanceDetailWithoutScroll(forward, root) && advanceFromCurrentGeometry();
  };
  const handleWheel = (event: ReactWheelEvent<HTMLDivElement>) => {
    if (handleForwardIntent(event.deltaY > 0)) event.preventDefault();
  };
  const handleTouchStart = (event: ReactTouchEvent<HTMLDivElement>) => {
    touchStartY.current = event.touches[0]?.clientY ?? null;
    touchIntentConsumed.current = false;
  };
  const handleTouchMove = (event: ReactTouchEvent<HTMLDivElement>) => {
    const y = event.touches[0]?.clientY;
    if (y == null || touchStartY.current == null || touchIntentConsumed.current) return;
    touchIntentConsumed.current = handleForwardIntent(y < touchStartY.current);
    if (touchIntentConsumed.current) event.preventDefault();
  };
  const handleTouchEnd = () => {
    touchStartY.current = null;
    touchIntentConsumed.current = false;
  };
  const handleKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (event.target !== event.currentTarget || event.altKey || event.ctrlKey || event.metaKey) return;
    const advanced = handleForwardIntent(
      FORWARD_DETAIL_KEYS.has(event.key) && !(event.key === ' ' && event.shiftKey),
    );
    if (advanced) event.preventDefault();
  };

  // After navigating via a pointer, scroll to and briefly highlight the fork point — once
  // per anchor. If the target isn't rendered yet it may live in an unloaded page, so pull
  // more and retry (the effect re-runs as records grow).
  useEffect(() => {
    if (!activeAnchor) { scrolledFor.current = undefined; return; }
    if (scrolledFor.current === activeAnchor) return;
    const root = scrollRef.current;
    if (!root) return;
    // scrollAnchor is an id from stored data; escape it before injecting into a CSS
    // selector so a malformed value can't throw and blank the session view.
    const el = root.querySelector(`[data-anchor="${CSS.escape(activeAnchor)}"]`);
    if (!(el instanceof HTMLElement)) {
      if (hasNextPage && !isFetchingNextPage) fetchNextPage();
      return;
    }
    scrolledFor.current = activeAnchor;
    const rootBox = root.getBoundingClientRect();
    const elBox = el.getBoundingClientRect();
    root.scrollTop = centerScrollTop(
      { scrollTop: root.scrollTop, top: rootBox.top, height: root.clientHeight },
      { top: elBox.top, height: elBox.height },
    );
    el.classList.add('ring-2', 'ring-brand', 'rounded-lg');
    const t = setTimeout(() => el.classList.remove('ring-2', 'ring-brand', 'rounded-lg'), 2200);
    return () => clearTimeout(t);
  }, [activeAnchor, nodes, hasNextPage, isFetchingNextPage, fetchNextPage]);

  if (isLoading) {
    return <CollectingLoader className="px-6 py-4" />;
  }

  const userPrompts = assembled ? collectUserPrompts(nodes) : [];
  const handleTogglePromptsOnly = () => setPromptsOnly(on => !on);
  // Whether a turn falls inside the navigator's selection. The tint is what makes a
  // dragged stretch mean something in the body — without it the selection would live
  // only on the strip, and the reader would be told where to look but not what is there.
  const inNavRange = (ts?: string): boolean => {
    if (!navRange || !ts) return false;
    const atMs = Date.parse(ts);
    return !Number.isNaN(atMs) && atMs >= navRange.startMs && atMs <= navRange.endMs;
  };

  const handleJumpToPrompt = (anchorId: string) => {
    // Clearing scrolledFor lets the same prompt be revisited after scrolling away; the
    // effect skips an anchor it has already handled.
    scrolledFor.current = undefined;
    setJumpAnchor(anchorId);
    setPromptsOnly(false);
  };

  const forkRec = records.find(r => r.forked_from_session);
  const isCompaction = records.some(r => r.is_compact_summary);
  const pointer = forkRec?.forked_from_session
    ? {
        session: forkRec.forked_from_session,
        uuid: forkRec.forked_from_uuid,
        label: isCompaction ? '압축 전 세션 (전체 대화)' : '분기 원본',
      }
    : null;

  return (
    <div className="flex flex-col flex-1 min-h-0">
      {(legacy || pointer) && (
        <div className="px-6 pt-3 flex-shrink-0 flex flex-wrap items-center gap-2">
          {pointer && (
            <LineagePointer
              targetSession={pointer.session}
              anchorUuid={pointer.uuid}
              label={pointer.label}
              onNavigate={onNavigate}
            />
          )}
          {legacy && (
            <span className="inline-flex items-center rounded bg-surface-sunk px-2 py-0.5 text-[11px] text-ink-3">
              파일 분리·조립 정보 없음 · 재수집하면 복원됩니다
            </span>
          )}
        </div>
      )}
      {assembled && !promptsOnly && (
        <TimelineNavigator
          nodes={nodes}
          onJump={handleJumpToPrompt}
          onSelectRange={setNavRange}
          selectedRange={navRange}
        />
      )}
      {/* py, not pt: with padding only on top, the row had no bottom breathing room
          and the control read as stuck to the divider below it. */}
      {assembled && userPrompts.length > 0 && (
        <div className="flex flex-shrink-0 items-center gap-2 border-b border-border-subtle px-6 py-2">
          <label className="flex cursor-pointer items-center gap-1.5 text-[11px] text-ink-2 opacity-50">
            <input
              type="checkbox"
              checked={promptsOnly}
              onChange={handleTogglePromptsOnly}
              className="h-3 w-3 cursor-pointer accent-[var(--brand)]"
            />
            내 메시지만 ({userPrompts.length})
          </label>
        </div>
      )}
      {total === 0 ? (
        <div className="px-6 py-4 text-sm text-ink-3">{assembled ? 'No conversation data' : 'No records'}</div>
      ) : (
        <div
          ref={scrollRef}
          className="overflow-y-auto overflow-x-hidden flex-1 min-h-0"
          tabIndex={0}
          onScroll={handleScroll}
          onWheel={handleWheel}
          onTouchStart={handleTouchStart}
          onTouchMove={handleTouchMove}
          onTouchEnd={handleTouchEnd}
          onTouchCancel={handleTouchEnd}
          onKeyDown={handleKeyDown}
        >
          {promptsOnly && assembled ? (
            <UserPromptRail prompts={userPrompts} onJump={handleJumpToPrompt} />
          ) : assembled ? (
            <div className="px-6 py-4 bg-canvas space-y-3">
              {nodes.slice(0, shown).map((node) => (
                node.kind === 'boundary' ? (
                  <div key={node.key} className="flex items-center gap-2 pt-3 pb-0.5">
                    {node.seam !== 'main' && (
                      <span className={cn('w-1.5 h-1.5 rounded-full flex-shrink-0', SEAM_DOT[node.seam])} />
                    )}
                    <span className={cn('font-mono text-[10px] tracking-wide flex-shrink-0', SEAM_LABEL[node.seam])}>
                      {node.label}
                    </span>
                    <span className="h-px flex-1 bg-[var(--border-subtle)]" />
                  </div>
                ) : node.kind === 'aside' ? (
                  // Wrapped rather than threaded through AsideGroup: the navigator can
                  // anchor to a subagent stretch, and without a data-anchor here the jump
                  // would land nowhere for exactly the sessions that have asides.
                  <div key={node.key} data-anchor={anchorOf(node)}>
                    <AsideGroup label={node.label} items={node.items} />
                  </div>
                ) : (
                  <div
                    key={node.key}
                    // The same anchor the navigator jumps to (timeline-scale anchorOf).
                    data-anchor={anchorOf(node)}
                    className={cn(inNavRange(node.item.ts) && 'rounded-lg bg-success/10 ring-1 ring-success/25')}
                  >
                    {node.item.compactSummary
                      ? <CompactSummary item={node.item} />
                      : <ConversationBubble item={node.item} />}
                  </div>
                )
              ))}
            </div>
          ) : (
            <IndividualView records={records.slice(0, shown)} />
          )}
          {showSentinel && (
            <div ref={sentinelRef} className="h-8 flex items-center justify-center gap-2 text-[11px] text-ink-3">
              <span className="h-3 w-3 rounded-full border border-ink-3 border-t-transparent animate-spin" />
              <span className="animate-pulse">더 불러오는 중…</span>
            </div>
          )}
        </div>
      )}
    </div>
  );
};

export {
  SessionDetail,
  decideDetailIntersectionAdvance,
  isWithinDetailAdvanceMargin,
  shouldAdvanceDetailWithoutScroll,
};
