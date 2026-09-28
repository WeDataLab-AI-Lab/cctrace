'use client';

import { useRef } from 'react';
import type { CSSProperties, PointerEvent as ReactPointerEvent } from 'react';
import { cn } from '@/lib/utils';
import type { AssembledNode } from './session-utils';
import { buildTimeScale, collectBars, collectMessages } from './timeline-scale';

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
}

const MAIN_LANE = '메인 대화';

const fmtDuration = (ms: number): string => {
  if (ms < 1000) return `${ms}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  return `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`;
};

const fmtClock = (ms: number): string => new Date(ms).toLocaleTimeString();

const TimelineNavigator = ({ nodes, onJump, onSelectRange, selectedRange }: TimelineNavigatorProps) => {
  const dragRef = useRef<{ pointerId: number; startMs: number; startClientX: number; left: number; width: number; moved: boolean } | null>(null);

  const bars = collectBars(nodes);
  const messages = collectMessages(nodes);
  if (bars.length === 0 && messages.length === 0) return null;

  // Messages join the intervals so a turn outside any call still gets width around it —
  // otherwise a question asked during a long idle stretch would sit inside a collapsed gap.
  const scale = buildTimeScale([
    ...bars.map(b => ({ startMs: b.startMs, endMs: b.endMs })),
    ...messages.map(m => ({ startMs: m.atMs, endMs: m.atMs })),
  ]);
  const pct = (ms: number): number => scale.toRatio(ms) * 100;

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
    for (const candidate of [...messages.map(m => ({ anchor: m.anchor, at: m.atMs })), ...bars.map(b => ({ anchor: b.anchor, at: b.startMs }))]) {
      if (!candidate.anchor) continue;
      const distance = Math.abs(candidate.at - atMs);
      if (!best || distance < best.distance) best = { anchor: candidate.anchor, distance };
    }
    return best?.anchor;
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
      const anchor = nearestAnchor(range.startMs);
      if (anchor) onJump(anchor);
      return;
    }
    // A click with no selection to clear is a seek, not a deselect.
    if (selectedRange) onSelectRange(null);
    const anchor = nearestAnchor(drag.startMs);
    if (anchor) onJump(anchor);
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
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={handlePointerUp}
        onPointerCancel={handlePointerUp}
        title="클릭하면 그 지점으로, 드래그하면 그 구간으로 이동합니다"
      >
        {selectedRange && (
          <span
            aria-hidden
            className="pointer-events-none absolute inset-y-0 z-0 bg-success/20 left-[var(--nav-selection-left)] w-[var(--nav-selection-width)]"
          />
        )}
        {scale.gaps.map(gap => (
          <span
            key={gap.atRatio}
            title={`${fmtDuration(gap.skippedMs)} 대기 — 생략됨`}
            // eslint-disable-next-line no-restricted-syntax
            style={{ '--gap-left': `${gap.atRatio * 100}%` } as CSSProperties}
            className="absolute inset-y-0 z-0 w-0.5 -translate-x-1/2 border-l border-dashed border-ink-4/60 left-[var(--gap-left)]"
          />
        ))}
        {asideBars.map(bar => (
          <span
            key={bar.key}
            aria-hidden
            // eslint-disable-next-line no-restricted-syntax
            style={{ '--bar-left': `${pct(bar.startMs)}%`, '--bar-width': `${Math.max(pct(bar.endMs) - pct(bar.startMs), 0.4)}%` } as CSSProperties}
            className="absolute top-1 z-10 h-2 rounded-sm bg-ink-4/50 left-[var(--bar-left)] w-[var(--bar-width)]"
          />
        ))}
        {mainBars.map(bar => (
          <span
            key={bar.key}
            aria-hidden
            // eslint-disable-next-line no-restricted-syntax
            style={{ '--bar-left': `${pct(bar.startMs)}%`, '--bar-width': `${Math.max(pct(bar.endMs) - pct(bar.startMs), 0.4)}%` } as CSSProperties}
            className={cn(
              'absolute top-3.5 z-10 h-3 rounded-sm left-[var(--bar-left)] w-[var(--bar-width)]',
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
              'pointer-events-none absolute bottom-0.5 z-20 w-1.5 -translate-x-1/2 rounded-sm left-[var(--msg-left)]',
              message.role === 'user' ? 'h-3.5 bg-brand' : 'h-2 bg-ink-4',
            )}
          />
        ))}
      </div>
      <span className="flex-shrink-0 font-mono text-[10px] text-ink-3">
        {scale.gaps.length > 0 ? `${fmtDuration(scale.gaps.reduce((sum, g) => sum + g.skippedMs, 0))} 생략` : fmtClock(scale.fromRatio(1))}
      </span>
    </div>
  );
};

export { TimelineNavigator };
