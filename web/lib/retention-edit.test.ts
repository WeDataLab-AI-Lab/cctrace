import { describe, expect, it } from 'vitest';
import {
  canApplyRetention,
  previewIsFresh,
  retentionChangeIsDangerous,
  totalRowsToDrop,
} from './retention-edit';
import type { RetentionPreview } from './types';

const p = (rows: number): RetentionPreview => ({
  table: 't',
  current_days: 90,
  new_days: 30,
  oldest_ts: null,
  rows_to_drop: rows,
});

describe('retentionChangeIsDangerous', () => {
  it('is dangerous when any table drops rows', () => {
    expect(retentionChangeIsDangerous([p(0), p(5)])).toBe(true);
  });
  it('is safe when nothing drops', () => {
    expect(retentionChangeIsDangerous([p(0), p(0)])).toBe(false);
    expect(retentionChangeIsDangerous([])).toBe(false);
  });
});

describe('totalRowsToDrop', () => {
  it('sums across tables', () => {
    expect(totalRowsToDrop([p(3), p(4)])).toBe(7);
    expect(totalRowsToDrop([])).toBe(0);
  });
});

describe('previewIsFresh (fail-closed)', () => {
  const base = { valid: true, daysMatch: true, isFetching: false, isError: false };
  it('is fresh only when all conditions hold', () => {
    expect(previewIsFresh(base)).toBe(true);
  });
  it('is not fresh while typing/debouncing (days mismatch)', () => {
    expect(previewIsFresh({ ...base, daysMatch: false })).toBe(false);
  });
  it('is not fresh while fetching', () => {
    expect(previewIsFresh({ ...base, isFetching: true })).toBe(false);
  });
  it('is not fresh on fetch error', () => {
    expect(previewIsFresh({ ...base, isError: true })).toBe(false);
  });
  it('is not fresh on invalid input', () => {
    expect(previewIsFresh({ ...base, valid: false })).toBe(false);
  });
});

describe('canApplyRetention (confirm gate cannot be bypassed)', () => {
  const base = { previewFresh: true, pending: false, dangerous: false, confirmedTyped: false };
  it('allows a safe change with a fresh preview', () => {
    expect(canApplyRetention(base)).toBe(true);
  });
  it('BLOCKS while the preview is not fresh, even if it looks safe', () => {
    // The core fix: a stale/loading/errored preview must never enable Apply.
    expect(canApplyRetention({ ...base, previewFresh: false })).toBe(false);
  });
  it('BLOCKS a confirmed destructive change while the preview is not fresh', () => {
    // Safety-critical: even with the confirm typed, a stale preview must block.
    expect(canApplyRetention({ previewFresh: false, pending: false, dangerous: true, confirmedTyped: true })).toBe(false);
  });
  it('blocks a destructive change without typed confirmation', () => {
    expect(canApplyRetention({ ...base, dangerous: true, confirmedTyped: false })).toBe(false);
  });
  it('allows a destructive change once confirmed', () => {
    expect(canApplyRetention({ ...base, dangerous: true, confirmedTyped: true })).toBe(true);
  });
  it('blocks while a mutation is pending', () => {
    expect(canApplyRetention({ ...base, pending: true })).toBe(false);
  });
});
