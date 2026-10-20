import { describe, expect, it } from 'vitest';

// Mirrors the paging contract of SessionDetail's infinite query. The regression was that
// a window starting mid-page handed its leading page a shortened limit but still decided
// "next page exists" by comparing against the full page size. A refetch then stopped at
// that short page and silently dropped every newer page from the cache.
const PAGE = 1000;
interface Param { offset: number; limit?: number }
const initialParam = (offset: number): Param => ({
  offset,
  limit: offset % PAGE > 0 ? offset % PAGE : PAGE,
});
const next = (lastLength: number, last: Param): Param | undefined =>
  lastLength === (last.limit || PAGE) ? { offset: last.offset + PAGE, limit: PAGE } : undefined;
const previous = (first: Param): Param | undefined => first.offset > 0
  ? { offset: Math.max(0, first.offset - PAGE), limit: first.offset < PAGE ? first.offset : PAGE }
  : undefined;

describe('conversation window paging', () => {
  it('keeps walking after a shortened leading page so a refetch reaches the newest page', () => {
    const first = initialParam(800);
    expect(first.limit).toBe(800);
    expect(next(800, first)).toEqual({ offset: 1800, limit: PAGE });
  });
  it('still stops at the real end of a full page', () => {
    expect(next(500, { offset: 1800, limit: PAGE })).toBeUndefined();
  });
  it('caps the page before a mid-page window at the boundary', () => {
    expect(previous({ offset: 800, limit: PAGE })).toEqual({ offset: 0, limit: 800 });
    expect(previous({ offset: 1800, limit: PAGE })).toEqual({ offset: 800, limit: PAGE });
  });
});
