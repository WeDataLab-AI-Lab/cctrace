import { describe, expect, it } from 'vitest';
import { bucketDate, shortUser, trendQueryEnabled, unknownSeriesKey } from './trend-chart';

describe('bucketDate', () => {
  it('parses date-only bucket labels in local time', () => {
    const want = new Date(2026, 7, 10);

    expect(bucketDate('2026-08-10').getTime()).toBe(want.getTime());
  });

  it('keeps date-time bucket labels as local timestamps', () => {
    const want = new Date(2026, 7, 10, 14, 37);

    expect(bucketDate('2026-08-10T14:37:00').getTime()).toBe(want.getTime());
  });
});

describe('trendQueryEnabled', () => {
  it('stops chart queries while the chart is collapsed', () => {
    expect(trendQueryEnabled(true, true)).toBe(false);
  });

  it('keeps the active grouping query enabled while expanded', () => {
    expect(trendQueryEnabled(false, true)).toBe(true);
    expect(trendQueryEnabled(false, false)).toBe(false);
  });

  it('defers chart and latest-activity queries until persisted scopes hydrate', () => {
    expect(trendQueryEnabled(false, true, false)).toBe(false);
    expect(trendQueryEnabled(false, true, true)).toBe(true);
  });
});

// A user_id the nameMap does not cover used to reach the legend as a full
// 64-character hash sitting where a person's name goes. Shortening keeps it an
// opaque name for a known user; the alternative -- filling the gap from
// profile_email -- would key the same person two ways, because a user_id and a
// profile email are different facts and one user carries several profiles.
describe('shortUser', () => {
  it('shortens a long hash so it cannot fill a name slot raw', () => {
    expect(shortUser('a'.repeat(8) + 'b'.repeat(56))).toBe('aaaaaaaa');
  });

  it('keeps a readable identifier as it is', () => {
    expect(shortUser('someone@example.test')).toBe('someone');
    expect(shortUser('short-id')).toBe('short-id');
  });
});

// recharts 3.x decides stack order by registration order, not child order: its
// graphical-item reducer appends every newly mounted series to the end of one
// array. So a user who first appears in a later poll -- routine on a rolling
// window -- registers after the Unknown area and gets stacked on top of it,
// even though Unknown is rendered last. Changing Unknown's React key whenever
// the set of series changes remounts it, so it re-registers after the newcomer
// in that same commit and returns to the top of the stack.
describe('unknownSeriesKey', () => {
  it('changes when a new series joins the stack', () => {
    expect(unknownSeriesKey(['a', 'b'])).not.toBe(unknownSeriesKey(['a', 'b', 'c']));
  });

  it('changes when a series leaves the stack', () => {
    expect(unknownSeriesKey(['a', 'b'])).not.toBe(unknownSeriesKey(['a']));
  });

  // Reordering alone cannot move a registered item, so remounting on it would
  // buy nothing -- and the stack is re-sorted by spend on every poll.
  it('stays put when the same series are merely reordered', () => {
    expect(unknownSeriesKey(['a', 'b'])).toBe(unknownSeriesKey(['b', 'a']));
  });

  it('never collides with a series key', () => {
    expect(unknownSeriesKey(['a'])).not.toBe('a');
    expect(unknownSeriesKey([])).not.toBe('');
  });
});
