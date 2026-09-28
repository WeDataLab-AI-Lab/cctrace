'use client';

import { useEffect, useRef, useState } from 'react';
import { cn } from '@/lib/utils';

/**
 * A single row that scrolls sideways instead of growing.
 *
 * The header lays its filter groups out with flex-wrap, so a row that outgrows the
 * space pushes everything after it onto the next line. Twelve accounts was enough
 * to do that. The row has to be told it may be narrower than its contents --
 * min-w-0 on the flex child, overflow-x-auto on the track -- or the browser sizes
 * it to fit and there is nothing to scroll.
 *
 * The edge fades are driven by scroll position, not applied statically. A fade
 * painted on a row that fits says "there is more over here" when there is not, and
 * a fade that stays on the left edge after scrolling to the start says the same.
 * Both ends are checked on scroll and on resize, so the gradient appears exactly
 * where content is actually cut off.
 *
 * The scrollbar is hidden rather than styled: the fades already carry the message,
 * and a horizontal bar under a row of pills reads as a divider between the row and
 * whatever follows it. Wheel and trackpad scrolling still work, and keyboard focus
 * still scrolls the row into view -- hiding the bar takes away the indicator, not
 * the ability.
 */
const ScrollFadeRow = ({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) => {
  const ref = useRef<HTMLDivElement>(null);
  const [edges, setEdges] = useState({ start: false, end: false });

  const measure = () => {
    const el = ref.current;
    if (!el) return;
    // 1px of slack: fractional scroll positions never land exactly on the maximum,
    // so an exact comparison leaves the end fade on forever.
    const maxScroll = el.scrollWidth - el.clientWidth;
    setEdges({
      start: el.scrollLeft > 1,
      end: maxScroll > 1 && el.scrollLeft < maxScroll - 1,
    });
  };

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    measure();
    // ResizeObserver, not a window listener: the row's width changes when siblings
    // in the header wrap, which does not resize the window.
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    for (const child of Array.from(el.children)) ro.observe(child);
    return () => ro.disconnect();
  }, [children]);

  return (
    <div
      ref={ref}
      onScroll={measure}
      data-fade-start={edges.start || undefined}
      data-fade-end={edges.end || undefined}
      className={cn('scroll-fade-row flex min-w-0 gap-1 overflow-x-auto', className)}
    >
      {children}
    </div>
  );
};

export { ScrollFadeRow };
