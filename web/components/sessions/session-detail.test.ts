import { describe, expect, it } from 'vitest';
import type { AssembledNode, ConversationItem } from './session-utils';
import {
  anchorIndex,
  centerScrollTop,
  decideDetailIntersectionAdvance,
  isWithinDetailAdvanceMargin,
  shouldAdvanceDetailWithoutScroll,
} from './session-detail';

describe('decideDetailIntersectionAdvance', () => {
  it('does not advance beyond the initial page without a new user scroll', () => {
    // The initial observe callback may report an already-visible sentinel.
    expect(decideDetailIntersectionAdvance(false, true)).toEqual({ advance: false, scrollPending: false });

    // One user scroll can advance once. Observer callbacks caused by render/page
    // settlement cannot advance again while the sentinel remains visible.
    expect(decideDetailIntersectionAdvance(true, true)).toEqual({ advance: true, scrollPending: false });
    expect(decideDetailIntersectionAdvance(false, true)).toEqual({ advance: false, scrollPending: false });
  });

  it('re-evaluates geometry on scroll when the sentinel stayed intersecting', () => {
    const root = { getBoundingClientRect: () => ({ top: 100, bottom: 500 }) };
    const sentinel = { getBoundingClientRect: () => ({ top: 850, bottom: 870 }) };

    // No observer callback follows the ignored initial callback in this sequence.
    expect(decideDetailIntersectionAdvance(false, true).advance).toBe(false);
    const stillWithinMargin = isWithinDetailAdvanceMargin(root, sentinel);
    expect(stillWithinMargin).toBe(true);
    expect(decideDetailIntersectionAdvance(true, stillWithinMargin))
      .toEqual({ advance: true, scrollPending: false });
  });

  it('advances on downward intent when the initial chunk cannot scroll', () => {
    const root = {
      scrollTop: 0,
      clientHeight: 500,
      scrollHeight: 400,
      getBoundingClientRect: () => ({ top: 100, bottom: 600 }),
    };
    const sentinel = { getBoundingClientRect: () => ({ top: 550, bottom: 570 }) };

    // Initial IO is ignored, and wheel/touch/keyboard intent cannot change scrollTop,
    // so no scroll event follows. The explicit forward intent must authorize one advance.
    expect(decideDetailIntersectionAdvance(false, true).advance).toBe(false);
    expect(shouldAdvanceDetailWithoutScroll(true, root)).toBe(true);
    expect(decideDetailIntersectionAdvance(
      true,
      isWithinDetailAdvanceMargin(root, sentinel),
    )).toEqual({ advance: true, scrollPending: false });

    expect(shouldAdvanceDetailWithoutScroll(false, root)).toBe(false);
    expect(shouldAdvanceDetailWithoutScroll(true, { ...root, scrollHeight: 800 })).toBe(false);
  });

  it('consumes a scroll signal when direct geometry is outside the advance margin', () => {
    const root = { getBoundingClientRect: () => ({ top: 100, bottom: 500 }) };
    const sentinel = { getBoundingClientRect: () => ({ top: 901, bottom: 921 }) };

    expect(decideDetailIntersectionAdvance(true, isWithinDetailAdvanceMargin(root, sentinel)))
      .toEqual({ advance: false, scrollPending: false });
  });
});

// `shown` grows to cover anchorIndex, so anything this misses is a target the body
// never renders and the jump silently lands nowhere. An item-only test missed every
// aside node.
describe('anchorIndex', () => {
  const itemNode = (anchorId: string): AssembledNode =>
    ({ kind: 'item', seam: 'main', key: `i:${anchorId}`, item: { role: 'user', text: '', anchorId, tools: [] } as ConversationItem });
  const asideNode = (key: string): AssembledNode =>
    ({ kind: 'aside', label: 'sub', key, items: [] } as unknown as AssembledNode);

  const nodes = [itemNode('u-1'), itemNode('h-codex'), asideNode('a:1'), itemNode('u-2')];

  it('finds a record by its anchor id, so uuid links minted elsewhere keep resolving', () => {
    expect(anchorIndex(nodes, 'u-1')).toBe(0);
    expect(anchorIndex(nodes, 'u-2')).toBe(3);
  });

  it('finds a record without a uuid by its hashed anchor id', () => {
    expect(anchorIndex(nodes, 'h-codex')).toBe(1);
  });

  it('finds an aside node, which the item-only test skipped', () => {
    expect(anchorIndex(nodes, 'a:1')).toBe(2);
  });

  it('reports -1 for an anchor that is not in the list', () => {
    expect(anchorIndex(nodes, 'nope')).toBe(-1);
  });
});

describe('centerScrollTop', () => {
  // scrollIntoView scrolls every scrollable ancestor, the document included. The
  // dashboard shell is h-screen over a body with no overflow lock, so a jump moved
  // the whole page and left the app's own background exposed below it -- the sidebar
  // was cut off at the same line as the conversation (#677). Centering has to be
  // computed against the container and applied to the container alone.
  const root = { scrollTop: 1000, top: 200, height: 600 };

  it('centres a target below the viewport without touching ancestors', () => {
    // Element 800px down the viewport, 100px tall: its centre must land at root centre.
    expect(centerScrollTop(root, { top: 800, height: 100 })).toBe(1000 + 600 - 250);
  });

  it('centres a target above the viewport', () => {
    expect(centerScrollTop(root, { top: 0, height: 100 })).toBe(1000 - 200 - 250);
  });

  it('never returns a negative offset', () => {
    // Near the top of a session the ideal centre is above the content start.
    expect(centerScrollTop({ scrollTop: 0, top: 200, height: 600 }, { top: 210, height: 40 })).toBe(0);
  });

  it('leaves an already centred target where it is', () => {
    // top 480 + half of 40 = 500, which is the centre of a 200..800 root.
    expect(centerScrollTop(root, { top: 480, height: 40 })).toBe(1000);
  });
});
