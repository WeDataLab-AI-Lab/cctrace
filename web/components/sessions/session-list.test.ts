import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { SessionList, decideIntersectionLoad, isWithinListLoadMargin, requestSessionPage } from './session-list';

describe('empty filtered session results', () => {
  it('offers another page without automatically loading it on render', () => {
    let loadRequests = 0;
    const html = renderToStaticMarkup(createElement(SessionList, {
      sessions: [],
      isLoading: false,
      selectedId: null,
      onSelect: () => undefined,
      hasMore: true,
      onLoadMore: () => { loadRequests += 1; },
    }));

    expect(loadRequests).toBe(0);
    expect(html).toContain('data-testid="empty-session-load-more"');

    requestSessionPage(false, () => { loadRequests += 1; });
    expect(loadRequests).toBe(1);
  });

  it('does not offer another page when the result set is exhausted', () => {
    const html = renderToStaticMarkup(createElement(SessionList, {
      sessions: [],
      isLoading: false,
      selectedId: null,
      onSelect: () => undefined,
      hasMore: false,
      onLoadMore: () => undefined,
    }));

    expect(html).not.toContain('data-testid="empty-session-load-more"');
  });

  it('does not duplicate an explicit request while a page is loading', () => {
    let loadRequests = 0;

    requestSessionPage(true, () => { loadRequests += 1; });

    expect(loadRequests).toBe(0);
  });
});

describe('decideIntersectionLoad', () => {
  it('loads at most one page per new user-scroll-caused intersection', () => {
    // IntersectionObserver reports the already-visible sentinel on initial observe.
    expect(decideIntersectionLoad(false, true)).toEqual({ load: false, scrollPending: false });

    // A user scroll may trigger one page, but settling that page while the sentinel
    // remains visible must not automatically drain another page.
    expect(decideIntersectionLoad(true, true)).toEqual({ load: true, scrollPending: false });
    expect(decideIntersectionLoad(false, true)).toEqual({ load: false, scrollPending: false });
  });

  it('re-evaluates geometry on scroll when no second observer callback occurs', () => {
    const root = { getBoundingClientRect: () => ({ top: 100, bottom: 500 }) };
    const sentinel = { getBoundingClientRect: () => ({ top: 700, bottom: 720 }) };

    // The observer's sole callback was the ignored initial intersection. A later scroll
    // must determine the still-intersecting state directly instead of waiting for IO.
    expect(decideIntersectionLoad(false, true).load).toBe(false);
    const stillWithinMargin = isWithinListLoadMargin(root, sentinel);
    expect(stillWithinMargin).toBe(true);
    expect(decideIntersectionLoad(true, stillWithinMargin)).toEqual({ load: true, scrollPending: false });
  });

  it('consumes a scroll signal when direct geometry is outside the load margin', () => {
    const root = { getBoundingClientRect: () => ({ top: 100, bottom: 500 }) };
    const sentinel = { getBoundingClientRect: () => ({ top: 801, bottom: 821 }) };

    expect(decideIntersectionLoad(true, isWithinListLoadMargin(root, sentinel)))
      .toEqual({ load: false, scrollPending: false });
  });
});
