'use client';

import { useRef } from 'react';
import type { CSSProperties, PointerEvent as ReactPointerEvent } from 'react';
import { cn } from '@/lib/utils';
import type { AssembledNode } from './session-utils';
import { buildTimeScale, collectBars, collectMessages } from './timeline-scale';
import type { SessionTraceIndexEntry } from '@/lib/session-trace-index';
import { indexedIntervals, requestDensity } from './timeline-index';
import type { ConversationTimeline } from '@/lib/conversation-timeline';

/**
 * The session's shape, above the session itself.
 *
 * The waterfall used to be a third view mode, which meant finding an interesting stretch
 * of a run and then losing it: switching to the conversation to read what happened threw
 * away the place you had found. The two were always halves of one thing — a map and the
 * ground it maps — and the map belongs over the ground, not behind a tab.
 *
 * So this draws the same axis (real time inside activity, idle collapsed) and nothing
 * else: no detail list, because the body below is the detail. Clicking a mark scrolls
 * the conversation to it; dragging scrolls to where the stretch begins and tints it.
 */
interface TimelineNavigatorProps {
  nodes: AssembledNode[];
  /** Scroll the conversation to this item (an anchor id used as data-anchor). */
  onJump: (anchor: string) => void;
  /** Tint the conversation over this stretch, or clear it when null. */
  onSelectRange: (range: { startMs: number; endMs: number } | null) => void;
  selectedRange: { startMs: number; endMs: number } | null;
  /** Whole-session request marks from the metadata index, including turns outside the loaded window. */
  requestMarks?: SessionTraceIndexEntry[];
  onJumpRequest?: (turnKey: string) => void;
  /** The time span currently visible in the conversation; drawn as the viewport. */
  visibleRange?: { startMs: number; endMs: number };
  /** Every marker of the session, read without bodies. Drawn in place of the loaded marks. */
  timeline?: ConversationTimeline;
  /** The stretch whose records are loaded; a click inside it scrolls in place. */
  loadedRange?: { startMs: number; endMs: number };
  /** Open the conversation at an exact request instant outside the loaded stretch. */
  onOpenAt?: (ts: string) => void;
  /** Messages typed inside a running request: drawn small, never numbered. */
  extraMessages?: number[];
}

const MAIN_LANE = '메인 대화';

interface Packed { left: number; right: number }

/** Joins spans the strip cannot separate, so a long session stays a few hundred elements. */
const packSpans = (spans: readonly { startMs: number; endMs: number }[], toRatio: (ms: number) => number, gap = 0.0004): Packed[] => {
  const out: Packed[] = [];
  for (const span of spans) {
    const left = toRatio(span.startMs);
    const right = Math.max(toRatio(span.endMs), left + 0.0006);
    const last = out[out.length - 1];
    if (last && left <= last.right + gap) last.right = Math.max(last.right, right);
    else out.push({ left, right });
  }
  return out;
};

/** One mark per strip position; nothing is dropped, only stacked. */
const packPoints = (points: readonly number[], toRatio: (ms: number) => number, buckets = 2000): number[] => {
  const seen = new Set<number>();
  for (const ms of points) seen.add(Math.round(toRatio(ms) * buckets));
  return [...seen].sort((a, b) => a - b).map(bucket => bucket / buckets);
};

const fmtDuration = (ms: number): string => {
  if (ms < 1000) return `${ms}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  return `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`;
};

const fmtClock = (ms: number): string => new Date(ms).toLocaleTimeString();

const TimelineNavigator = ({ nodes, onJump, onSelectRange, selectedRange, requestMarks, onJumpRequest, visibleRange, timeline, loadedRange, onOpenAt, extraMessages }: TimelineNavigatorProps) => {
  const dragRef = useRef<{ pointerId: number; startMs: number; startClientX: number; left: number; width: number; moved: boolean } | null>(null);

  const tl = timeline && timeline.requests.length + timeline.spans.length + timeline.replies.length > 0 ? timeline : undefined;
  const tlEndMs = tl
    ? Math.max(tl.requests.at(-1)?.ms ?? -Infinity, tl.replies.at(-1) ?? -Infinity, ...tl.spans.slice(-50).map(span => span.endMs))
    : -Infinity;
  const loadedBars = collectBars(nodes);
  const loadedMessages = collectMessages(nodes);
  // The timeline already holds every loaded marker; only records newer than its read
  // (an active session) still come from the loaded pages.
  const bars = tl ? loadedBars.filter(bar => bar.startMs > tlEndMs) : loadedBars;
  const messages = tl ? loadedMessages.filter(message => message.atMs > tlEndMs) : loadedMessages;
  const indexed = tl ? [] : indexedIntervals(requestMarks);
  if (!tl && bars.length === 0 && messages.length === 0 && indexed.length === 0) return null;

  // Messages join the intervals so a turn outside any call still gets width around it —
  // otherwise a question asked during a long idle stretch would sit inside a collapsed gap.
  const scale = buildTimeScale([
    ...loadedBars.map(b => ({ startMs: b.startMs, endMs: b.endMs })),
    ...loadedMessages.map(m => ({ startMs: m.atMs, endMs: m.atMs })),
  // The index spans the session, so the axis keeps the shape of turns that the
  // loaded conversation window has not reached yet. Turn spans also keep idle
  // stretches out of the collapsed gaps, so a long session reads as activity.
  ...indexed.map(r => ({ startMs: r.startMs, endMs: r.endMs })),
    ...(tl ? [
      ...tl.spans,
      ...tl.requests.map(request => ({ startMs: request.ms, endMs: request.ms })),
      ...tl.replies.map(ms => ({ startMs: ms, endMs: ms })),
    ] : []),
  ]);
  const pct = (ms: number): number => scale.toRatio(ms) * 100;
  const density = requestDensity(indexed, scale.toRatio);
  const tlMain = tl ? packSpans(tl.spans.filter(span => !span.aside && !span.error), scale.toRatio) : [];
  const tlErrors = tl ? packSpans(tl.spans.filter(span => !span.aside && span.error), scale.toRatio) : [];
  const tlAside = tl ? packSpans(tl.spans.filter(span => span.aside), scale.toRatio) : [];
  const tlRequests = tl ? packPoints(tl.requests.map(request => request.ms), scale.toRatio) : [];
  const tlReplies = tl ? packPoints(tl.replies, scale.toRatio) : [];
  const tlExtras = tl && extraMessages ? packPoints(extraMessages, scale.toRatio) : [];

  // Outside the loaded stretch there is nothing to scroll to, so a click opens the
  // request that moment belongs to: the last one asked at or before it.
  const openOutside = (atMs: number): boolean => {
    if (!tl || !onOpenAt || tl.requests.length === 0) return false;
    if (loadedRange && atMs >= loadedRange.startMs && atMs <= loadedRange.endMs) return false;
    let target = tl.requests[0];
    for (const request of tl.requests) {
      if (request.ms > atMs) break;
      target = request;
    }
    onOpenAt(target.ts);
    return true;
  };

  // One track. The waterfall separated lanes because it had room for them; here the
  // subagent marks ride the same line, since the question this answers is "where in the
  // session am I", not "which thread ran what".
  const mainBars = bars.filter(b => b.lane === MAIN_LANE);
  const asideBars = bars.filter(b => b.lane !== MAIN_LANE);

  const rangeAt = (clientX: number, startMs: number, left: number, width: number) => {
    const ratio = Math.min(1, Math.max(0, (clientX - left) / width));
    const endMs = scale.fromRatio(ratio);
    return { startMs: Math.min(startMs, endMs), endMs: Math.max(startMs, endMs) };
  };

  const handlePointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return;
    const rect = event.currentTarget.getBoundingClientRect();
    const at = rangeAt(event.clientX, scale.fromRatio(0), rect.left, rect.width).endMs;
    dragRef.current = { pointerId: event.pointerId, startMs: at, startClientX: event.clientX, left: rect.left, width: rect.width, moved: false };
    event.currentTarget.setPointerCapture(event.pointerId);
  };

  const handlePointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const drag = dragRef.current;
    if (!drag || drag.pointerId !== event.pointerId) return;
    if (Math.abs(event.clientX - drag.startClientX) <= 3) return;
    drag.moved = true;
    onSelectRange(rangeAt(event.clientX, drag.startMs, drag.left, drag.width));
  };

  const nearestAnchor = (atMs: number): string | undefined => {
    let best: { anchor: string; distance: number } | undefined;
    for (const candidate of [
      ...loadedMessages.map(m => ({ anchor: m.anchor, at: m.atMs })),
      ...loadedBars.map(b => ({ anchor: b.anchor, at: b.startMs })),
      ...indexed.map(r => ({ anchor: r.key, at: r.startMs })),
    ]) {
      if (!candidate.anchor) continue;
      const distance = Math.abs(candidate.at - atMs);
      if (!best || distance < best.distance) best = { anchor: candidate.anchor, distance };
    }
    return best?.anchor;
  };

  // One destination, one navigation. Turns from the index may sit outside the
  // loaded window, so they open by key; loaded anchors still scroll in place.
  const jumpTo = (anchor: string) => {
    if (onJumpRequest && indexed.some(r => r.key === anchor)) onJumpRequest(anchor);
    else onJump(anchor);
  };

  const handlePointerUp = (event: ReactPointerEvent<HTMLDivElement>) => {
    const drag = dragRef.current;
    dragRef.current = null;
    if (!drag || drag.pointerId !== event.pointerId) return;
    event.currentTarget.releasePointerCapture(event.pointerId);
    if (drag.moved) {
      // A dragged stretch also jumps: tinting a range the reader cannot see would be a
      // decoration. The body goes to where it starts, and the tint says how far it runs.
      const range = rangeAt(event.clientX, drag.startMs, drag.left, drag.width);
      onSelectRange(range);
      if (openOutside(range.startMs)) return;
      const anchor = nearestAnchor(range.startMs);
      if (anchor) jumpTo(anchor);
      return;
    }
    // A click with no selection to clear is a seek, not a deselect.
    if (selectedRange) onSelectRange(null);
    if (openOutside(drag.startMs)) return;
    const anchor = nearestAnchor(drag.startMs);
    if (anchor) jumpTo(anchor);
  };

  const selectionStyle = selectedRange
    ? ({
        '--nav-selection-left': `${pct(selectedRange.startMs)}%`,
        '--nav-selection-width': `${Math.max(pct(selectedRange.endMs) - pct(selectedRange.startMs), 0.4)}%`,
      } as CSSProperties)
    : undefined;

  return (
    <div className="flex flex-shrink-0 items-center gap-3 border-b border-border px-6 py-2">
      <span className="flex-shrink-0 font-mono text-[10px] text-ink-3">{fmtClock(scale.fromRatio(0))}</span>
      <div
        // eslint-disable-next-line no-restricted-syntax
        style={selectionStyle}
        className="relative h-9 flex-1 cursor-crosshair touch-none overflow-hidden rounded-md border border-border-subtle bg-surface-sunk"
        title="옅은 영역이 지금 보이는 대화입니다. 클릭하면 그 지점으로, 드래그하면 그 구간으로 이동합니다"
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={handlePointerUp}
        onPointerCancel={handlePointerUp}
      >
        {selectedRange && (
          <span
            aria-hidden
            className="pointer-events-none absolute inset-y-0 z-0 bg-success/20 left-[var(--nav-selection-left)] w-[var(--nav-selection-width)]"
          />
        )}
        {visibleRange && (
          <span
            aria-hidden
            className="pointer-events-none absolute inset-y-0 z-[5] border-x border-brand/40 bg-brand/10 left-[var(--nav-view-left)] w-[var(--nav-view-width)]"
            // eslint-disable-next-line no-restricted-syntax
            style={{ '--nav-view-left': `${pct(visibleRange.startMs)}%`, '--nav-view-width': `${Math.max(pct(visibleRange.endMs) - pct(visibleRange.startMs), 0.6)}%` } as CSSProperties}
          />
        )}
        {scale.gaps.filter((_, index) => index % Math.max(1, Math.ceil(scale.gaps.length / 12)) === 0 || index === scale.gaps.length - 1).map(gap => (
          <span
            key={gap.atRatio}
            title={`${fmtDuration(gap.skippedMs)} 대기 — 생략됨`}
            // eslint-disable-next-line no-restricted-syntax
            style={{ '--gap-left': `${gap.atRatio * 100}%` } as CSSProperties}
            className="absolute inset-y-0 z-0 w-0.5 -translate-x-1/2 border-l border-dashed border-ink-4/60 left-[var(--gap-left)]"
          />
        ))}
        {tlAside.map(span => (
          <span
            key={`tl-aside-${span.left}`}
            aria-hidden
            // eslint-disable-next-line no-restricted-syntax
            style={{ '--bar-left': `${span.left * 100}%`, '--bar-width': `${(span.right - span.left) * 100}%` } as CSSProperties}
            className="absolute top-1 z-10 h-2 rounded-[1px] bg-ink-4/50 left-[var(--bar-left)] w-[var(--bar-width)]"
          />
        ))}
        {[...tlMain.map(span => ({ span, error: false })), ...tlErrors.map(span => ({ span, error: true }))].map(({ span, error }) => (
          <span
            key={`tl-main-${error ? 'e' : 'o'}-${span.left}`}
            aria-hidden
            // eslint-disable-next-line no-restricted-syntax
            style={{ '--bar-left': `${span.left * 100}%`, '--bar-width': `${(span.right - span.left) * 100}%` } as CSSProperties}
            className={cn('absolute top-3.5 h-3 rounded-[1px] left-[var(--bar-left)] w-[var(--bar-width)]', error ? 'z-[11] bg-danger/70' : 'z-10 bg-brand/55')}
          />
        ))}
        {tlReplies.map(at => (
          <span
            key={`tl-reply-${at}`}
            aria-hidden
            // eslint-disable-next-line no-restricted-syntax
            style={{ '--msg-left': `${at * 100}%` } as CSSProperties}
            className="pointer-events-none absolute bottom-0.5 z-20 h-2 w-px -translate-x-1/2 bg-ink-4 left-[var(--msg-left)]"
          />
        ))}
        {tlExtras.map(at => (
          <span
            key={`tl-extra-${at}`}
            aria-hidden
            // eslint-disable-next-line no-restricted-syntax
            style={{ '--msg-left': `${at * 100}%` } as CSSProperties}
            className="pointer-events-none absolute bottom-0.5 z-20 h-2 w-px -translate-x-1/2 bg-brand/40 left-[var(--msg-left)]"
          />
        ))}
        {tlRequests.map(at => (
          <span
            key={`tl-request-${at}`}
            aria-hidden
            // eslint-disable-next-line no-restricted-syntax
            style={{ '--msg-left': `${at * 100}%` } as CSSProperties}
            className="pointer-events-none absolute bottom-0.5 z-20 h-3.5 w-0.5 -translate-x-1/2 bg-brand left-[var(--msg-left)]"
          />
        ))}
        {asideBars.map(bar => (
          <span
            key={bar.key}
            aria-hidden
            // eslint-disable-next-line no-restricted-syntax
            style={{ '--bar-left': `${pct(bar.startMs)}%`, '--bar-width': `${Math.max(pct(bar.endMs) - pct(bar.startMs), 0.06)}%` } as CSSProperties}
            className="absolute top-1 z-10 h-2 rounded-[1px] bg-ink-4/50 left-[var(--bar-left)] w-[var(--bar-width)]"
          />
        ))}
        {mainBars.map(bar => (
          <span
            key={bar.key}
            aria-hidden
            // eslint-disable-next-line no-restricted-syntax
            style={{ '--bar-left': `${pct(bar.startMs)}%`, '--bar-width': `${Math.max(pct(bar.endMs) - pct(bar.startMs), 0.06)}%` } as CSSProperties}
            className={cn(
              'absolute top-3.5 z-10 h-3 rounded-[1px] left-[var(--bar-left)] w-[var(--bar-width)]',
              bar.pair.isError ? 'bg-danger/70' : 'bg-brand/55',
            )}
          />
        ))}
        {messages.map(message => (
          <span
            key={message.key}
            title={`${message.role === 'user' ? '사용자' : '어시스턴트'} · ${fmtClock(message.atMs)}\n${message.text.slice(0, 200)}`}
            // eslint-disable-next-line no-restricted-syntax
            style={{ '--msg-left': `${pct(message.atMs)}%` } as CSSProperties}
            className={cn(
              'pointer-events-none absolute bottom-0.5 z-20 -translate-x-1/2 left-[var(--msg-left)]',
              message.role === 'user' ? 'h-3.5 w-0.5 bg-brand' : 'h-2 w-px bg-ink-4',
            )}
          />
        ))}
        {density.map(bin => (
          <span
            key={`bin-${bin.key}`}
            title={`이 구간의 요청 ${bin.count}개 (전체 ${indexed.length}개 모두 반영)`}
            // eslint-disable-next-line no-restricted-syntax
            style={{ '--bin-left': `${bin.at * 100}%`, '--bin-height': `${Math.min(12, 2 + bin.count * 2)}px` } as CSSProperties}
            className="pointer-events-none absolute bottom-0.5 z-10 w-px -translate-x-1/2 bg-ink-2/60 left-[var(--bin-left)] h-[var(--bin-height)]"
          />
        ))}
      </div>
      <span className="flex-shrink-0 font-mono text-[10px] text-ink-3">
        {scale.gaps.length > 0 ? `${fmtDuration(scale.gaps.reduce((sum, g) => sum + g.skippedMs, 0))} 생략 · ${scale.gaps.length}곳` : fmtClock(scale.fromRatio(1))}
      </span>
    </div>
  );
};

export { TimelineNavigator };
