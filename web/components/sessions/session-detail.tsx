'use client';

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type {
  KeyboardEvent as ReactKeyboardEvent,
  TouchEvent as ReactTouchEvent,
  WheelEvent as ReactWheelEvent,
} from 'react';
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query';
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
import { conversationRequests, type ConversationRequest } from './timeline-index';
import { anchorOf } from './timeline-scale';
import type { ConversationReturnState } from '@/lib/sessions-return-state';
import type { RequestDirection } from '@/lib/conversation-reading';
import type { ConversationWindow } from '@/lib/conversation-window';
import type { InspectionTarget } from '@/lib/session-inspection';
import { sessionRecordsQueryKey, type SessionRecordScope, type SessionRecordQueryIdentity } from '@/lib/session-record-scope';
import { CONVERSATION_AVATAR_ALIGNMENT, formatConversationTime } from './conversation-layout';
import type { SessionTraceIndex } from '@/lib/session-trace-index';
import { fetchConversationTimeline } from '@/lib/conversation-timeline';
import { SessionRequestScope } from './session-request-scope';

type NavigateFn = (sessionId: string, anchorUuid?: string) => void;

/** A page's offset plus how many rows this particular page was allowed to hold. */
interface PageParam { offset: number; limit?: number }

interface SessionDetailProps {
  sessionId: string;
  mode: ViewMode;
  onNavigate: NavigateFn;
  scrollAnchor?: string;
  /** ISO instant to land on, for callers that know a time and not a message. */
  scrollFromTs?: string;
  /** An explicit analysis turn takes priority over an earlier navigation or prompt anchor. */
  analysisAnchorTs?: string;
  /** Revisit the same analysis turn without remounting the conversation. */
  analysisAnchorSequence?: number;
  /** Hidden tabs retain their instance but must not scroll until visible. */
  active?: boolean;
  /** The overview's whole-session has_enriched. Undefined when the API build omits it. */
  hasEnriched?: boolean;
  restoredState?: ConversationReturnState;
  abortWhenInactive?: boolean;
  scope?: SessionRecordScope;
  queryIdentity?: SessionRecordQueryIdentity;
  initialPage?: ConversationWindow;
  onLatestRequest?: () => void;
  onRequestNavigate?: (direction: RequestDirection, anchor: string) => void;
  /** Open a turn known only from the metadata index, outside the loaded window. */
  onOpenTurn?: (turnKey: string) => void;
  /** Open the window at an exact record instant from the whole-session timeline. */
  onOpenAt?: (ts: string) => void;
  /** The window was opened at a jump target: land on its first message instead of its bottom. */
  landAtStart?: boolean;
  /** Open the trace tab pinned to this turn. */
  onOpenTrace?: (turnKey: string) => void;
  requestNavigationPending?: boolean;
  onRememberState?: (state: ConversationReturnState) => void;
  onInspect?: (target: InspectionTarget) => void;
  onInspectContext?: () => void;
  onReadingAnchor?: (anchor: string) => void;
  requestIndex?: SessionTraceIndex;
}

interface BubbleProps {
  item: ConversationItem;
  onInspect?: (target: InspectionTarget) => void;
  openTools?: string[];
}

const InspectableTool = ({ item, pair, onInspect, openTools }: BubbleProps & { pair: ConversationItem['tools'][number] }) => {
  const inspect = () => onInspect?.({ recordId: item.anchorId, ...(item.sourceFile ? { sourceFile: item.sourceFile } : {}), ...(pair.id ? { callId: pair.id } : {}) });
  const memoryKey = `${item.anchorId}:${pair.id ?? pair.name}`;
  return <ToolPairBlock pair={pair} memoryKey={memoryKey} defaultOpen={openTools?.includes(memoryKey) ?? false} onInspect={onInspect ? inspect : undefined} />;
};

const ConversationBubble = ({ item, onInspect, openTools }: BubbleProps) => {
  const inspect = () => onInspect?.({ recordId: item.anchorId, ...(item.sourceFile ? { sourceFile: item.sourceFile } : {}) });
  const at = formatConversationTime(item.ts);
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
  // Tool-only assistant fragments continue the execution list instead of
  // repeating an avatar and a second message box around every tool row.
  if (!isUser && !item.text && item.tools.length > 0) {
    return <div aria-label="도구 실행" className="min-w-0 pl-9">
      {item.tools.map((tool, index) => <InspectableTool key={tool.id ?? `${tool.name}@${tool.startTs}#${index}`} item={item} pair={tool} onInspect={onInspect} openTools={openTools} />)}
    </div>;
  }
  return (
    <div className="flex gap-3">
      <div className={cn(
        'flex-shrink-0 w-6 h-6 rounded-full flex items-center justify-center text-xs font-bold border border-transparent',
        CONVERSATION_AVATAR_ALIGNMENT,
        isUser ? 'bg-surface-sunk text-brand' : 'bg-border text-ink-2',
      )}>
        {isUser ? 'U' : 'A'}
      </div>
      <div className={cn(
        // min-w-0: without it a flex child won't shrink below its content's intrinsic
        // width, so a long unbreakable run (a ───── rule, a long LaTeX token) pushes the
        // bubble — and its right border — past the container. With it, break-words wraps.
        'flex-1 min-w-0 rounded-lg border px-3 py-2 text-xs',
        isUser
          // The request is a raised white card with the stronger edge; the answer
          // stays on the canvas. White is the emphasis, not a darker fill.
          ? 'border-border-strong bg-surface font-medium text-ink shadow-[var(--sh-xs)]'
          : 'border-transparent bg-transparent text-ink',
      )} data-conversation-role={isUser ? 'user' : 'agent'}>
        <div className="mb-1 flex min-w-0 flex-wrap items-center gap-2">
          {at && <time dateTime={at.iso} className="shrink-0 font-mono text-[10px] tabular-nums text-ink-3">{at.label}</time>}
          <span className="min-w-0 flex-1" />
          {onInspect && <button type="button" onClick={inspect} aria-label="이 메시지 상세 트레이스" title="이 메시지의 원본 기록과 실행 근거를 엽니다" className="inline-flex min-h-11 shrink-0 items-center whitespace-nowrap rounded px-1.5 py-0.5 text-[10px] text-ink-3 hover:bg-surface-sunk hover:text-ink lg:min-h-0">상세 트레이스</button>}
        </div>
        {item.agentTask && (
          <div className="mb-1 font-mono text-[10px] tracking-wide text-brand">상위 에이전트 지시</div>
        )}
        {item.text && <ConversationContent text={item.text} />}
        {item.tools.map((t, j) => <InspectableTool key={t.id ?? `${t.name}@${t.startTs}#${j}`} item={item} pair={t} onInspect={onInspect} openTools={openTools} />)}
      </div>
    </div>
  );
};

interface AsideGroupProps {
  label: string;
  items: ConversationItem[];
  onInspect?: (target: InspectionTarget) => void;
  activeAnchor?: string;
  openTools?: string[];
  openAsides?: string[];
}

// An async side thread (btw/subagent), collapsed by default on an indigo rail.
const AsideGroup = ({ label, items, onInspect, activeAnchor, openTools, openAsides }: AsideGroupProps) => {
  const memoryKey = items[0]?.anchorId ?? label;
  const [open, setOpen] = useState(openAsides?.includes(memoryKey) ?? false);
  const selected = items.some(item => item.anchorId === activeAnchor);
  const toggle = () => setOpen(o => !o);
  return (
    <div data-aside-key={memoryKey} data-aside-open={open || selected} className="pl-3 border-l-2 border-brand">
      <button type="button" aria-expanded={open || selected} onClick={toggle} className="flex items-center gap-2 py-1 w-full text-left">
        <span className="w-1.5 h-1.5 rounded-full bg-brand flex-shrink-0" />
        <span className="font-mono text-[10px] tracking-wide text-brand">{label}</span>
        <span className="rounded bg-[var(--accent-soft)] px-1 py-px text-[9px] font-mono text-brand">async</span>
        <span className="text-[10px] text-ink-3">{items.length}</span>
        <span className="ml-auto text-[10px] text-ink-3">{open ? '▾' : '▸'}</span>
      </button>
      {(open || selected) && (
        <div className="space-y-3 pt-1 pb-1">
          {dedupeKeys(items.map(item => ({ key: conversationItemKey(item), item }))).map(({ key, item }) => <div key={key} data-anchor={item.anchorId} data-reading-item><ConversationBubble item={item} onInspect={onInspect} openTools={openTools} /></div>)}
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
  nodes.findIndex(n => anchorOf(n) === anchor || n.kind === 'aside' && n.items.some(item => item.anchorId === anchor));

export const detailActiveAnchor = (
  nodes: AssembledNode[], assembled: boolean, jumpAnchor?: string,
  analysisAnchorTs?: string, scrollAnchor?: string, scrollFromTs?: string,
): string | undefined => jumpAnchor ??
  (assembled && analysisAnchorTs ? anchorAtOrAfter(nodes, analysisAnchorTs) :
    scrollAnchor ?? (assembled ? anchorAtOrAfter(nodes, scrollFromTs) : undefined));

export const needsAnalysisAnchorPage = (
  nodes: AssembledNode[], assembled: boolean, active: boolean, analysisAnchorTs?: string,
  jumpAnchor?: string, hasNextPage?: boolean, isFetchingNextPage?: boolean, isFetchNextPageError?: boolean,
): boolean => !!(assembled && active && analysisAnchorTs && !jumpAnchor &&
  !anchorAtOrAfter(nodes, analysisAnchorTs) && hasNextPage && !isFetchingNextPage && !isFetchNextPageError);

const lastRecordTs = (pages: SessionRecord[][] | undefined): string | undefined => {
  const last = pages?.[pages.length - 1];
  return last?.[last.length - 1]?.ts;
};

const readingSnapshot = (root: HTMLDivElement, sessionId: string, mode: ViewMode): ConversationReturnState => {
  const top = root.getBoundingClientRect().top;
  const current = [...root.querySelectorAll<HTMLElement>('[data-reading-item]')].find(el => el.getBoundingClientRect().bottom >= top);
  return { sessionId, mode, visible: Number(root.dataset.renderCount) || INITIAL_RENDER,
    promptsOnly: root.dataset.promptsOnly === 'true', scrollTop: root.scrollTop,
    readingAnchor: current?.dataset.anchor, anchorOffset: current ? current.getBoundingClientRect().top - top : undefined,
    openTools: [...root.querySelectorAll<HTMLElement>('[data-tool-open="true"]')].flatMap(node => node.dataset.toolKey ? [node.dataset.toolKey] : []),
    openAsides: [...root.querySelectorAll<HTMLElement>('[data-aside-open="true"]')].flatMap(node => node.dataset.asideKey ? [node.dataset.asideKey] : []),
  };
};

const SessionDetail = ({ sessionId, mode, onNavigate, scrollAnchor, scrollFromTs, analysisAnchorTs, analysisAnchorSequence,
  active = true, hasEnriched, restoredState, abortWhenInactive = false, scope, queryIdentity, initialPage, onRememberState, onInspect, onReadingAnchor, onLatestRequest, onRequestNavigate, onOpenTurn, onOpenAt, landAtStart, onOpenTrace, requestNavigationPending, onInspectContext, requestIndex }: SessionDetailProps) => {
  const queryClient = useQueryClient();
  const assembled = mode === 'assembled';
  // The stretch the navigator has selected, tinted in the body below. Null when nothing
  // is selected, which is the ordinary state.
  const [navRange, setNavRange] = useState<{ startMs: number; endMs: number } | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const sentinelRef = useRef<HTMLDivElement>(null);
  const topSentinelRef = useRef<HTMLDivElement>(null);
  const topScrollPending = useRef(false);
  // True while the first window follows late layout growth at its bottom.
  const bottomPinned = useRef(false);
  const highlighted = useRef<HTMLElement | null>(null);
  const highlightTimer = useRef<number | undefined>(undefined);
  const scrollPending = useRef(false);
  const touchStartY = useRef<number | null>(null);
  const touchIntentConsumed = useRef(false);
  const scrolledFor = useRef<string | undefined>(undefined);
  const loadingNextPage = useRef(false);
  const positionRestored = useRef(false);
  const publishedAnchor = useRef<string | undefined>(undefined);
  const baseQueryKey = sessionRecordsQueryKey(sessionId, assembled, scope, queryIdentity);
  const recordsQueryKey = initialPage ? [...baseQueryKey, 'window', initialPage.offset, initialPage.revision ?? 0] : baseQueryKey;
  const recordsKeyString = JSON.stringify(recordsQueryKey);
  // A window that begins mid-page must cap its first earlier page at that boundary,
  // otherwise rows shared with the leading page appear twice after prepending.
  const initialWindowParam: PageParam = {
    offset: initialPage?.offset ?? 0,
    limit: initialPage && initialPage.offset % SESSION_PAGE_SIZE > 0 ? initialPage.offset % SESSION_PAGE_SIZE : SESSION_PAGE_SIZE,
  };
  // The window can begin mid-page, so a page's length is not always SESSION_PAGE_SIZE.
  // Carrying the limit with each page keeps a refetch on the same boundaries; without
  // it TanStack stops at the short leading page and drops every page after it.
  const { data, isLoading, isError, error, refetch, fetchNextPage, fetchPreviousPage, hasPreviousPage, isFetchingPreviousPage, isFetchPreviousPageError, hasNextPage, isFetchingNextPage, isFetchNextPageError } = useInfiniteQuery<SessionRecord[], Error, { pages: SessionRecord[][]; pageParams: PageParam[] }, readonly unknown[], PageParam>({
    queryKey: recordsQueryKey,
    queryFn: async ({ pageParam, signal }) => {
      const page = await fetchSessionRecordsPage({ sessionId, offset: pageParam.offset, lineage: assembled, signal, ...(scope ? { scope } : {}) });
      return pageParam.limit && page.length > pageParam.limit ? page.slice(0, pageParam.limit) : page;
    },
    initialPageParam: initialWindowParam,
    initialData: initialPage ? { pages: [initialPage.records], pageParams: [initialWindowParam] } : undefined,
    enabled: active,
    getNextPageParam: (lastPage: SessionRecord[], _allPages, lastParam) =>
      lastPage.length === (lastParam.limit || SESSION_PAGE_SIZE) ? { offset: lastParam.offset + SESSION_PAGE_SIZE, limit: SESSION_PAGE_SIZE } : undefined,
    getPreviousPageParam: (_firstPage, _allPages, firstParam) => firstParam.offset > 0
      ? { offset: Math.max(0, firstParam.offset - SESSION_PAGE_SIZE), limit: firstParam.offset < SESSION_PAGE_SIZE ? firstParam.offset : SESSION_PAGE_SIZE }
      : undefined,
    // The newest window must notice new records; an anchored window keeps its snapshot.
    staleTime: initialPage ? 15_000 : Infinity,
    // Bound tab memory: four 1,000-record pages per open window.
    maxPages: 4,
    gcTime: 10 * 60_000,
    retry: false,
    refetchInterval: (query) => active && isRecentlyActive(lastRecordTs(query.state.data?.pages)) ? ACTIVE_POLL_MS : false,
  });

  // Server returns ascending (oldest first), so pages flatten straight into reading order;
  // then collapse legacy/enriched duplicate copies before assembling. (React Compiler memoizes.)
  const records = dedupeRecords(data?.pages.flat() ?? []);
  const nodes = buildAssembled(records);
  const legacy = isLegacySession(records, hasEnriched);
  const total = assembled ? nodes.length : records.length;
  // The loaded pages cover only the stretch around the reader. The axis needs every
  // marker of the session, so a body-free timeline is read once the window is on screen.
  const timelineQuery = useQuery({
    queryKey: [...baseQueryKey, 'timeline'],
    queryFn: ({ signal }) => fetchConversationTimeline(sessionId, scope, signal),
    enabled: active && assembled && !isLoading,
    staleTime: 30_000,
    gcTime: 10 * 60_000,
    retry: false,
  });
  const timeline = timelineQuery.data;
  const firstLoadedMs = records.length ? Date.parse(records[0].ts) : undefined;
  const lastLoadedMs = records.length ? Date.parse(records[records.length - 1].ts) : undefined;


  // Two-level lazy render: grow the render window (visible) over the loaded pages; once it
  // reaches the end of what's loaded, pull the next page. `shown` also always covers a
  // pointer's target node (when loaded) so scroll-to-anchor lands. visible resets via a key
  // on this component (session/mode change remounts it).
  const [visible, setVisible] = useState(restoredState?.visible ?? INITIAL_RENDER);
  // Jumping from the prompt rail reuses the pointer machinery below: same anchor lookup,
  // same window growth, same scroll-and-highlight. Only the source of the anchor differs.
  const [jumpAnchor, setJumpAnchor] = useState<string | undefined>(scrollAnchor ? undefined : restoredState?.anchor);
  const [promptsOnly, setPromptsOnly] = useState(restoredState?.promptsOnly ?? false);
  // centerMs: the message at the middle of the screen, where a jump puts its target.
  const [visibleRange, setVisibleRange] = useState<{ startMs: number; endMs: number; centerMs?: number } | undefined>();
  // A Work Segment knows when it happened, not which message started it, so it
  // arrives as a timestamp and is resolved here against the loaded records. The
  // first item at or after that instant is the one to land on; everything after
  // is the segment. Resolving to an anchor id rather than adding a second anchor kind
  // keeps one scroll-and-highlight path instead of two (#434).
  // Frozen at mount: pages prepended later must not move the landing target.
  const [startAnchor] = useState(() => {
    const first = landAtStart ? nodes.find(node => node.kind === 'item' || node.kind === 'aside') : undefined;
    return first ? anchorOf(first) : undefined;
  });
  const activeAnchor = detailActiveAnchor(nodes, assembled, jumpAnchor, analysisAnchorTs, scrollAnchor ?? startAnchor, scrollFromTs);
  const anchorIdx = assembled && activeAnchor ? anchorIndex(nodes, activeAnchor) : -1;
  const shown = anchorIdx >= 0 ? Math.max(visible, anchorIdx + 20) : visible;
  // The first window is the newest stretch of the session. Render its tail so the
  // reader lands on the last message; the render window then grows upward instead
  // of exposing only the oldest few nodes of that page.
  const bottomFirst = anchorIdx < 0 && !promptsOnly && restoredState?.readingAnchor === undefined;
  const renderStart = bottomFirst ? Math.max(0, total - shown) : 0;
  const renderedNodes = nodes.slice(renderStart, bottomFirst ? total : shown);
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

  // Growing the transcript above the reader must not move what the reader sees. The
  // first message on screen is remembered before the change and put back after it,
  // which also holds when a bounded cache drops the newest page at the bottom.
  const pendingShift = useRef<{ anchor: string; offset: number } | null>(null);
  const topMessage = (): { anchor: string; offset: number } | null => {
    const root = scrollRef.current;
    if (!root) return null;
    const top = root.getBoundingClientRect().top;
    for (const el of root.querySelectorAll<HTMLElement>('[data-anchor]')) {
      const box = el.getBoundingClientRect();
      if (box.bottom > top && el.dataset.anchor) return { anchor: el.dataset.anchor, offset: box.top - top };
    }
    return null;
  };
  useLayoutEffect(() => {
    const root = scrollRef.current;
    const pending = pendingShift.current;
    if (!root || !pending) return;
    pendingShift.current = null;
    const el = root.querySelector<HTMLElement>(`[data-anchor="${CSS.escape(pending.anchor)}"]`);
    if (!el || bottomPinned.current) return;
    root.scrollTop += el.getBoundingClientRect().top - root.getBoundingClientRect().top - pending.offset;
  });
  // Earlier records: render loaded ones first, then fetch the page above and render it
  // too. Fetching alone left the new page in cache, outside the rendered tail.
  const fetchAbove = () => {
    if (!hasPreviousPage || isFetchingPreviousPage || topScrollPending.current) return;
    topScrollPending.current = true;
    const before = total;
    // Captured before the fetch: the cache may drop the newest page when this one
    // arrives, and that shift must be undone together with the prepend.
    const anchor = topMessage();
    void fetchPreviousPage().then(result => {
      if (!result.data) return;
      const all = dedupeRecords(result.data.pages.flat());
      const count = assembled ? buildAssembled(all).length : all.length;
      pendingShift.current = anchor;
      setVisible(value => value + Math.max(0, count - before));
    }).finally(() => { topScrollPending.current = false; });
  };
  const pullAbove = () => {
    if (bottomFirst && shown < total) {
      pendingShift.current = topMessage();
      setVisible(value => Math.min(total, value + RENDER_CHUNK));
      return;
    }
    fetchAbove();
  };
  const pullAboveRef = useRef(pullAbove);
  const fetchAboveRef = useRef(fetchAbove);
  useEffect(() => { pullAboveRef.current = pullAbove; fetchAboveRef.current = fetchAbove; });


  // The first window starts at the newest request, so the conversation opens at its
  // bottom the way a coding agent's transcript does. Reading memory wins when the
  // reader has already been somewhere in this session.
  const openedAtBottom = useRef(false);
  const updateVisibleRangeRef = useRef<() => void>(() => undefined);
  const updateVisibleRangeNow = () => updateVisibleRangeRef.current();
  useEffect(() => {
    if (!active || restoredState || activeAnchor || !scrollRef.current || total === 0) return;
    const root = scrollRef.current;
    bottomPinned.current = !openedAtBottom.current;
    openedAtBottom.current = true;
    root.scrollTop = root.scrollHeight;
  }, [active, restoredState, activeAnchor, total]);
  // Late markdown and images grow the first window after paint; follow that growth
  // only until the reader scrolls, then leave their position alone.
  useEffect(() => {
    const root = scrollRef.current;
    if (!root || !bottomPinned.current) return;
    const keep = () => { if (bottomPinned.current) root.scrollTop = root.scrollHeight; };
    keep();
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(keep);
    observer?.observe(root);
    // Markdown and tool blocks settle over several frames; follow them until the
    // reader touches the transcript.
    let frame = 0;
    const until = performance.now() + 1500;
    const tick = () => {
      if (!bottomPinned.current) return;
      keep();
      if (performance.now() < until) frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick);
    const visibleTimer = window.setTimeout(updateVisibleRangeNow, 1200);
    return () => { observer?.disconnect(); cancelAnimationFrame(frame); window.clearTimeout(visibleTimer); };
  }, [total]);

  // The newest window paints first. Once it is on screen and the reader is idle,
  // fill the stretch immediately above it in the background so scrolling up finds
  // context already rendered instead of waiting for a round trip per screen.
  const prefetched = useRef(0);
  useEffect(() => {
    if (!active || !hasPreviousPage || isLoading || prefetched.current > 0) return;
    const win = window as Window & { requestIdleCallback?: (cb: () => void, o?: { timeout: number }) => number; cancelIdleCallback?: (id: number) => void };
    let idleId = 0;
    let timer = 0;
    const run = () => {
      if (!scrollRef.current) return;
      prefetched.current = 1;
      fetchAboveRef.current();
    };
    if (typeof win.requestIdleCallback === 'function') idleId = win.requestIdleCallback(run, { timeout: 1500 });
    else timer = window.setTimeout(run, 700);
    return () => {
      if (idleId && win.cancelIdleCallback) win.cancelIdleCallback(idleId);
      window.clearTimeout(timer);
    };
  }, [active, hasPreviousPage, isLoading]);

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

  const captureReading = () => {
    const root = scrollRef.current;
    if (!root) return;
    const snapshot = readingSnapshot(root, sessionId, mode);
    if (snapshot.readingAnchor) onReadingAnchor?.(snapshot.readingAnchor);
    onRememberState?.(snapshot);
  };
  /** The time span of the messages the reader can actually see, for the axis band. */
  function updateVisibleRange() {
    const root = scrollRef.current;
    if (!root) return;
    const box = root.getBoundingClientRect();
    const middle = box.top + box.height / 2;
    const all = [...root.querySelectorAll<HTMLTimeElement>('time[datetime]')];
    const times = all
      .filter((el) => { const r = el.getBoundingClientRect(); return r.bottom >= box.top && r.top <= box.bottom; })
      .map((el) => Date.parse(el.dateTime))
      .filter((at) => Number.isFinite(at));
    if (times.length === 0) return;
    // Collapsed blocks keep their <time> with a zero box at the top; only drawn ones count.
    let above: { top: number; ms: number } | undefined;
    for (const el of all) {
      const r = el.getBoundingClientRect();
      const ms = Date.parse(el.dateTime);
      if (r.width === 0 && r.height === 0) continue;
      if (r.top <= middle && Number.isFinite(ms) && (!above || r.top >= above.top)) above = { top: r.top, ms };
    }
    const centerMs = above?.ms;
    setVisibleRange({ startMs: Math.min(...times), endMs: Math.max(...times), ...(centerMs !== undefined && Number.isFinite(centerMs) ? { centerMs } : {}) });
  }
  useEffect(() => { updateVisibleRangeRef.current = updateVisibleRange; });
  const handleScroll = () => {
    const root = scrollRef.current;
    // Once the reader scrolls up, stop following late layout growth at the bottom.
    if (root && bottomPinned.current && root.scrollTop + root.clientHeight + 8 < root.scrollHeight) bottomPinned.current = false;
    // Reaching the top is the pull gesture. The intersection observer can miss it
    // when the container mounts after the observer, so the scroll handler is the
    // dependable trigger; both paths share the same in-flight guard.
    if (root && root.scrollTop <= 24 && !bottomPinned.current) pullAbove();
    updateVisibleRange();
    captureReading();
    advanceFromCurrentGeometry();
  };
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

  // An analysis turn may be beyond the loaded records, so resolve its time against
  // successive pages before asking the ordinary DOM-anchor path to scroll to it.
  useEffect(() => {
    if (needsAnalysisAnchorPage(nodes, assembled, active, analysisAnchorTs, jumpAnchor, hasNextPage, isFetchingNextPage, isFetchNextPageError)) {
      fetchNextPage();
    }
  }, [nodes, assembled, active, analysisAnchorTs, jumpAnchor, hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage]);



  // After navigating via a pointer, scroll to and briefly highlight the fork point — once
  // per anchor. If the target isn't rendered yet it may live in an unloaded page, so pull
  // more and retry (the effect re-runs as records grow).
  useEffect(() => {
    if (!active) return;
    if (!activeAnchor) { scrolledFor.current = undefined; return; }
    const scrollKey = `${analysisAnchorSequence ?? ''}:${activeAnchor}`;
    if (scrolledFor.current === scrollKey) return;
    const root = scrollRef.current;
    if (!root) return;
    // scrollAnchor is an id from stored data; escape it before injecting into a CSS
    // selector so a malformed value can't throw and blank the session view.
    const el = root.querySelector(`[data-anchor="${CSS.escape(activeAnchor)}"]`);
    if (!(el instanceof HTMLElement)) {
      if (hasNextPage && !isFetchingNextPage && !isFetchNextPageError) fetchNextPage();
      return;
    }
    scrolledFor.current = scrollKey;
    const rootBox = root.getBoundingClientRect();
    const elBox = el.getBoundingClientRect();
    root.scrollTop = centerScrollTop(
      { scrollTop: root.scrollTop, top: rootBox.top, height: root.clientHeight },
      { top: elBox.top, height: elBox.height },
    );
    // One ring at a time. The timer is not tied to this effect: it re-runs whenever
    // the nodes change, and clearing the timer then left the ring on for good.
    highlighted.current?.classList.remove('ring-2', 'ring-brand', 'rounded-lg');
    highlighted.current = el;
    el.classList.add('ring-2', 'ring-brand', 'rounded-lg');
    // An earlier timer must not cut a fresh highlight on the same message short.
    window.clearTimeout(highlightTimer.current);
    highlightTimer.current = window.setTimeout(() => el.classList.remove('ring-2', 'ring-brand', 'rounded-lg'), 2200);
  }, [active, activeAnchor, analysisAnchorSequence, nodes, hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage]);

  /** Restore the rendered window before its exact scroll position, without re-fetching cached pages. */
  useEffect(() => {
    if (!active || !restoredState || !data || !scrollRef.current || positionRestored.current) return;
    positionRestored.current = true;
    const root = scrollRef.current;
    const anchor = restoredState.readingAnchor ? root.querySelector<HTMLElement>(`[data-anchor="${CSS.escape(restoredState.readingAnchor)}"]`) : null;
    root.scrollTop = anchor ? root.scrollTop + anchor.getBoundingClientRect().top - root.getBoundingClientRect().top - (restoredState.anchorOffset ?? 0) : restoredState.scrollTop;
    if (activeAnchor) scrolledFor.current = `${analysisAnchorSequence ?? ''}:${activeAnchor}`;
  }, [active, restoredState, data, activeAnchor, analysisAnchorSequence]);
  /** The dedicated workspace aborts a hidden conversation read while retaining its mounted state. */
  useEffect(() => {
    if (abortWhenInactive && !active) void queryClient.cancelQueries({ queryKey: JSON.parse(recordsKeyString), exact: true });
  }, [abortWhenInactive, active, queryClient, recordsKeyString]);
  /** Save presentation state before a session switch; message bodies never enter reading memory. */
  useEffect(() => {
    const root = scrollRef.current;
    return () => {
      if (root) onRememberState?.(readingSnapshot(root, sessionId, mode));
    };
  }, [onRememberState, sessionId, mode, isLoading]);
  /** The first inspection follows the current reading target without selecting unrelated records. */
  const firstReadingNode = nodes.find(node => node.kind === 'item' || node.kind === 'aside');
  const initialReadingAnchor = activeAnchor ?? (firstReadingNode ? anchorOf(firstReadingNode) : undefined);
  useEffect(() => {
    if (!active || !initialReadingAnchor || publishedAnchor.current === initialReadingAnchor) return;
    publishedAnchor.current = initialReadingAnchor;
    onReadingAnchor?.(initialReadingAnchor);
  }, [active, initialReadingAnchor, onReadingAnchor]);

  // The newest window opens at the bottom; scrolling to its top pulls the previous
  // page, keeping the message under the reader in place. The first observation is
  // ignored so an already-visible sentinel cannot chain history by itself.
  useEffect(() => {
    const s = topSentinelRef.current;
    if (!s || isLoading) return;
    if (typeof IntersectionObserver === 'undefined') return;
    // Arm as soon as the first page has settled: reaching the top is enough to pull.
    const io = new IntersectionObserver((entries) => {
      if (!entries[0]?.isIntersecting) return;
      const root = scrollRef.current;
      if (!root || root.scrollTop > 24 || bottomPinned.current) return;
      pullAboveRef.current();
    }, { root: scrollRef.current, rootMargin: '240px 0px 0px 0px' });
    io.observe(s);
    return () => io.disconnect();
  }, [isLoading, total, hasPreviousPage, isFetchingPreviousPage]);

  if (isLoading) {
    return <CollectingLoader className="px-6 py-4" />;
  }

  const userPrompts = assembled ? collectUserPrompts(nodes) : [];
  const indexedPrompts = requestIndex?.requests.length
    ? requestIndex.requests.map(request => ({
        anchorId: request.turn_key,
        key: request.turn_key,
        text: request.preview,
        ts: request.started_at,
        requestNo: request.request_no,
        status: request.status,
        tools: request.tools,
        subagents: request.subagents,
      }))
    : userPrompts;
  const indexRange = requestIndex?.requests.length
    ? `요청 ${requestIndex.requests[0].request_no}–${requestIndex.requests[requestIndex.requests.length - 1].request_no} · 전체 ${requestIndex.request_total ?? requestIndex.requests.length}개`
    : undefined;
  // The opening window starts at the most recent request. Count the requests
  // above it so the reader can see that earlier work exists without reading the
  // whole session first.
  const loadedTurnKeys = new Set(userPrompts.map(prompt => prompt.anchorId));
  const loadedIndexed = requestIndex?.requests.filter(request => loadedTurnKeys.has(request.turn_key)) ?? [];
  const earliestLoadedNo = loadedIndexed.length
    ? Math.min(...loadedIndexed.map(request => request.request_no))
    : requestIndex?.requests.length ? requestIndex.requests[requestIndex.requests.length - 1].request_no + 1 : undefined;
  const indexEarlierCount = hasPreviousPage || earliestLoadedNo !== undefined
    ? (requestIndex?.requests ?? []).filter(request => request.request_no < (earliestLoadedNo ?? 0)).length
    : 0;
  // One numbering for the bar, the counts, the buttons and the request list: by
  // turn once the analysis is ready, by message until then.
  const basis = conversationRequests(requestIndex, timeline);
  const basisRequests = basis?.requests ?? [];
  const earlierCount = basis
    ? (firstLoadedMs === undefined ? 0 : basisRequests.filter(request => request.ms < firstLoadedMs).length)
    : indexEarlierCount;
  const readingAtMs = visibleRange?.centerMs ?? visibleRange?.startMs ?? lastLoadedMs;
  // The request the middle of the screen belongs to: the last one opened at or before it.
  const currentRequest = readingAtMs === undefined ? -1 : basisRequests.findLastIndex(request => request.ms <= readingAtMs);
  const requestPosition = basis && basisRequests.length > 0 && readingAtMs !== undefined
    ? { current: Math.max(1, currentRequest + 1), total: basisRequests.length, pending: basis.basis === 'messages' }
    : undefined;
  // A request in the loaded stretch scrolls in place; one outside it opens the window there.
  const openRequest = (request: ConversationRequest) => {
    const inLoaded = firstLoadedMs !== undefined && lastLoadedMs !== undefined && request.ms >= firstLoadedMs && request.ms <= lastLoadedMs;
    const node = inLoaded ? nodes.find(n => n.kind === 'item' && !!n.item.ts && Date.parse(n.item.ts) >= request.ms) : undefined;
    setPromptsOnly(false);
    if (node) {
      scrolledFor.current = undefined;
      setJumpAnchor(anchorOf(node));
      return;
    }
    if (onOpenAt) onOpenAt(request.ts);
  };
  const handleTogglePromptsOnly = () => setPromptsOnly(on => !on);
  const handleLoadNextPage = () => {
    if (!hasNextPage || isFetchingNextPage || loadingNextPage.current) return;
    loadingNextPage.current = true;
    void fetchNextPage({ cancelRefetch: false }).finally(() => { loadingNextPage.current = false; });
  };
  const retry = () => { void refetch(); };
  const handleLoadPrevious = () => { fetchAbove(); };
  // The server resolves a reading message to its containing request before finding its neighbour.
  const currentReadingAnchor = () => scrollRef.current ? readingSnapshot(scrollRef.current, sessionId, mode).readingAnchor ?? activeAnchor ?? initialReadingAnchor : activeAnchor ?? initialReadingAnchor;
  const jumpPrevious = () => {
    if (basis && basisRequests.length > 0 && readingAtMs !== undefined) {
      // Mid-request, the start of the request on screen comes first.
      const current = basisRequests[currentRequest];
      const target = current && current.ms < readingAtMs ? current : basisRequests[currentRequest - 1];
      if (target) openRequest(target);
      return;
    }
    const readingAnchor = currentReadingAnchor();
    if (onRequestNavigate && readingAnchor) { onRequestNavigate('previous-request', readingAnchor); return; }
    const index = userPrompts.findIndex(prompt => prompt.anchorId === activeAnchor);
    const target = userPrompts[Math.max(0, index - 1)];
    if (target) handleJumpToPrompt(target.anchorId);
  };
  const jumpNext = () => {
    if (basis && basisRequests.length > 0) {
      const target = basisRequests[currentRequest + 1];
      if (target) openRequest(target);
      return;
    }
    const readingAnchor = currentReadingAnchor();
    if (onRequestNavigate && readingAnchor) { onRequestNavigate('next-request', readingAnchor); return; }
    const index = userPrompts.findIndex(prompt => prompt.anchorId === activeAnchor);
    const target = userPrompts[index + 1];
    if (target) handleJumpToPrompt(target.anchorId);
  };
  const jumpRecent = () => {
    if (onLatestRequest) { onLatestRequest(); return; }
    const target = userPrompts.at(-1);
    if (target) handleJumpToPrompt(target.anchorId);
  };
  // Whether a turn falls inside the navigator's selection. The tint is what makes a
  // dragged stretch mean something in the body — without it the selection would live
  // only on the strip, and the reader would be told where to look but not what is there.
  const inNavRange = (ts?: string): boolean => {
    if (!navRange || !ts) return false;
    const atMs = Date.parse(ts);
    return !Number.isNaN(atMs) && atMs >= navRange.startMs && atMs <= navRange.endMs;
  };

  const handleJumpToPrompt = (anchorId: string) => {
    const indexed = requestIndex?.requests.find(request => request.turn_key === anchorId);
    const numbered = indexed && basis?.basis === 'turns' ? basisRequests.find(request => request.no === indexed.request_no) : undefined;
    if (numbered) {
      // The turn's own start can follow its message by seconds (Codex); open at the message.
      openRequest(numbered);
      return;
    }
    if (indexed && onOpenTurn) {
      // An indexed turn may sit outside the loaded window. Ask the server for
      // that turn directly instead of walking backward through every page.
      onOpenTurn(indexed.turn_key);
      setPromptsOnly(false);
      return;
    }
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
    <div className="flex min-w-0 flex-1 min-h-0 overflow-hidden">
      <div className="flex min-w-0 flex-1 flex-col min-h-0 overflow-hidden">
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
          requestMarks={requestIndex?.requests}
          onJumpRequest={onOpenTurn}
          visibleRange={visibleRange}
          timeline={timeline && basis ? { ...timeline, requests: basisRequests.map(request => ({ ts: request.ts, ms: request.ms })) } : timeline}
          extraMessages={basis?.extras}
          loadedRange={firstLoadedMs !== undefined && lastLoadedMs !== undefined ? { startMs: firstLoadedMs, endMs: lastLoadedMs } : undefined}
          onOpenAt={onOpenAt}
        />
      )}
      {((assembled && (userPrompts.length > 0 || total > 0 && (onRequestNavigate || onLatestRequest))) || onInspectContext || hasPreviousPage) && (
        <div role="toolbar" aria-label="대화 도구" className="flex flex-shrink-0 flex-wrap items-center gap-2 border-b border-border-subtle px-4 py-2 text-[11px]">
          {assembled && (userPrompts.length > 0 || total > 0 && (onRequestNavigate || onLatestRequest)) && <>
          <button type="button" onClick={jumpPrevious} disabled={requestNavigationPending} aria-label="이전 요청" title={earlierCount > 0 ? `읽지 않은 이전 요청 ${earlierCount}개` : undefined} className="rounded px-2 py-1 text-[11px] text-ink-2 hover:bg-surface-sunk">이전 요청{earlierCount > 0 && <span aria-hidden="true" className="ml-1 text-ink-3">{earlierCount > 1 ? `${earlierCount}개 남음` : '1개'}</span>}</button>
          <button type="button" onClick={jumpNext} disabled={requestNavigationPending} className="rounded px-2 py-1 text-[11px] text-ink-2 hover:bg-surface-sunk">다음 요청</button>
          <button type="button" onClick={jumpRecent} disabled={requestNavigationPending} className="rounded px-2 py-1 text-[11px] text-ink-2 hover:bg-surface-sunk">최근으로</button>
          <label className="flex cursor-pointer items-center gap-1.5 text-[11px] text-ink-2 opacity-50">
            <input
              type="checkbox"
              checked={promptsOnly}
              onChange={handleTogglePromptsOnly}
              className="h-3 w-3 cursor-pointer accent-[var(--brand)]"
            />
            요청 목록 ({indexedPrompts.length})
          </label>
          </>}
          {isFetchPreviousPageError && <p role="alert">이전 기록을 불러오지 못했습니다.</p>}
          {assembled && <SessionRequestScope index={requestIndex} loadedRequests={userPrompts.length} position={requestPosition} hasPreviousPage={hasPreviousPage}
            onLoadPrevious={handleLoadPrevious} previousPending={isFetchingPreviousPage}
            onJumpRequest={requestNo => {
              const target = basisRequests.find(request => request.no === requestNo);
              if (target) { openRequest(target); return; }
              const match = requestIndex?.requests.find(request => request.request_no === requestNo);
              if (match) onOpenTurn?.(match.turn_key);
            }} />}
          {onInspectContext && <button type="button" onClick={onInspectContext} className="ml-auto rounded px-2 py-1 text-[11px] text-ink-3 hover:bg-surface-sunk hover:text-ink">지시문·훅</button>}
        </div>
      )}
      {isLoading ? (
        <CollectingLoader className="px-6 py-4" />
      ) : isError && !data ? <div role="alert" className="p-4 text-sm text-danger"><p>{error.message}</p><button type="button" onClick={retry} className="mt-2 rounded border border-border px-3 py-1.5 text-ink">다시 시도</button></div> : total === 0 ? (
        assembled && hasNextPage ? (
          <div className="flex flex-col items-start gap-2 px-6 py-4 text-sm text-ink-3">
            <span>{isFetchNextPageError ? '다음 대화 페이지를 불러오지 못했습니다.' : '아직 표시할 대화가 없습니다. 다음 페이지에서 계속 찾을 수 있습니다.'}</span>
            <button type="button" onClick={handleLoadNextPage} disabled={isFetchingNextPage}
              className="rounded border border-border px-3 py-1.5 text-ink hover:border-brand hover:text-brand disabled:opacity-50">
              {isFetchingNextPage ? '불러오는 중…' : isFetchNextPageError ? '다시 시도' : '더 읽기'}
            </button>
          </div>
        ) : (
          <div className="px-6 py-4 text-sm text-ink-3">{assembled ? 'No conversation data' : 'No records'}</div>
        )
      ) : (
        <div className="relative flex min-h-0 flex-1 flex-col">
        <div
          ref={scrollRef}
          data-session-return-scroll="conversation"
          data-session-id={sessionId}
          data-mode={mode}
          data-render-count={shown}
          data-prompts-only={promptsOnly}
          data-jump-anchor={jumpAnchor}
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
            <UserPromptRail prompts={indexedPrompts} onJump={handleJumpToPrompt} rangeLabel={indexRange} onOpenTrace={onOpenTrace} />
          ) : assembled ? (
            <div className="px-6 py-4 bg-canvas space-y-3">
              <div ref={topSentinelRef} aria-hidden={!isFetchingPreviousPage} aria-busy={isFetchingPreviousPage} className="flex h-7 items-center justify-center overflow-hidden">
                {isFetchingPreviousPage
                  ? <span role="status" className="flex items-end gap-[3px] h-2.5">
                      {['[animation-delay:0ms]', '[animation-delay:110ms]', '[animation-delay:220ms]', '[animation-delay:330ms]', '[animation-delay:440ms]'].map(delay => <span key={delay} className={cn('cctrace-bar h-full w-[3px] rounded-full bg-brand/70', delay)} />)}
                    </span>
                  : hasPreviousPage && <span className="h-1.5 w-14 rounded-full bg-border-strong/70" />}
              </div>
              {renderedNodes.map((node) => (
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
                    <AsideGroup label={node.label} items={node.items} onInspect={onInspect} activeAnchor={activeAnchor} openTools={restoredState?.openTools} openAsides={restoredState?.openAsides} />
                  </div>
                ) : (
                  <div
                    key={node.key}
                    // The same anchor the navigator jumps to (timeline-scale anchorOf).
                    data-anchor={anchorOf(node)}
                    data-reading-item
                    className={cn(inNavRange(node.item.ts) && 'rounded-lg bg-success/10 ring-1 ring-success/25')}
                  >
                    {node.item.compactSummary
                      ? <CompactSummary item={node.item} />
                      : <ConversationBubble item={node.item} onInspect={onInspect} openTools={restoredState?.openTools} />}
                  </div>
                )
              ))}
            </div>
          ) : (
            <IndividualView records={records.slice(0, shown)} />
          )}
          {showSentinel && (
            <div ref={sentinelRef} className="h-8 flex items-center justify-center gap-2 text-[11px] text-ink-3">
              {isFetchingNextPage ? <span role="status">더 불러오는 중…</span> : <>
                {isFetchNextPageError && <span role="alert">다음 기록을 불러오지 못했습니다.</span>}
                <button type="button" onClick={advance} className="rounded px-2 py-1 hover:text-ink">{isFetchNextPageError ? '다시 시도' : '더 읽기'}</button>
              </>}
            </div>
          )}
        </div>
        {/* The fade marks that the transcript continues above without covering a line. */}
        <div aria-hidden="true" className="pointer-events-none absolute inset-x-0 top-0 z-10 h-10 bg-gradient-to-b from-[var(--canvas)] to-transparent" />
        </div>
      )}
      </div>

    </div>
  );
};

export {
  SessionDetail,
  decideDetailIntersectionAdvance,
  isWithinDetailAdvanceMargin,
  shouldAdvanceDetailWithoutScroll,
};
