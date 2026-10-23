import { useLayoutEffect, useRef, useState } from 'react';

export const COUNT_UP_MS = 600;

const easeOut = (t: number) => 1 - (1 - t) ** 3;

const prefersReducedMotion = () =>
  typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;

/**
 * The number to show while a figure animates toward `target`.
 *
 * When `ready` turns true (the data arrived) it climbs from 0; when a ready target
 * changes (a refresh) it moves from what is on screen. A figure that was ready from
 * the start -- a page served from cache -- shows at once. The last frame is exactly
 * `target`.
 */
export function useCountUp(target: number, ready = true): number {
  const [shown, setShown] = useState(target);
  const shownRef = useRef(target);
  const wasReady = useRef(ready);

  // Layout effect, so a fresh load resets to 0 before paint instead of flashing the
  // previous account's number for a frame.
  useLayoutEffect(() => {
    const from = wasReady.current ? shownRef.current : 0;
    wasReady.current = ready;
    if (!ready) return;
    const show = (v: number) => { shownRef.current = v; setShown(v); };
    if (from === target || prefersReducedMotion()) { show(target); return; }
    show(from);
    const start = performance.now();
    let frame = 0;
    const tick = (now: number) => {
      const t = Math.min(1, (now - start) / COUNT_UP_MS);
      show(t >= 1 ? target : from + (target - from) * easeOut(t));
      if (t < 1) frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [target, ready]);

  return shown;
}
