import { describe, expect, it } from 'vitest';

import { normalizeTrendProjectHashes, trendProjectScopeReady } from './trend-chart';

describe('TrendChart project identity scope', () => {
  it('keeps the complete root member set', () => {
    expect(normalizeTrendProjectHashes('h-worktree', ['h-main', 'h-worktree'])).toEqual(['h-main', 'h-worktree']);
  });

  it('keeps an actual subpath as its own member set', () => {
    expect(normalizeTrendProjectHashes('h-subpath', ['h-subpath'])).toEqual(['h-subpath']);
  });

  it('normalizes scalar callers without duplicating the path', () => {
    expect(normalizeTrendProjectHashes('h-main', undefined)).toEqual(['h-main']);
  });

  it('does not enable queries for an explicitly empty identity set', () => {
    expect(normalizeTrendProjectHashes('h-main', [])).toEqual([]);
    expect(trendProjectScopeReady([])).toBe(false);
    expect(trendProjectScopeReady(undefined)).toBe(true);
  });
});
