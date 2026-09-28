import { describe, expect, it } from 'vitest';
import { commitLiveOrder, reconcileList } from './live-list';

interface Row { id: string; cost: number }

const row = (id: string, cost = 0): Row => ({ id, cost });
const getId = (r: Row) => r.id;
const run = (committedIds: string[], live: Row[], lastKnown: Map<string, Row> = new Map()) =>
  reconcileList({ committedIds, live, lastKnown, getId });

describe('reconcileList', () => {
  it('commits the whole live page when nothing is committed yet', () => {
    const live = [row('a'), row('b')];
    const r = run([], live);
    expect(r.nextCommittedIds).toEqual(['a', 'b']);
    expect(r.displayed.map(getId)).toEqual(['a', 'b']);
    expect(r.pendingIds).toEqual([]);
  });

  it('holds head arrivals as pending instead of rendering them', () => {
    const r = run(['a', 'b'], [row('new1'), row('new2'), row('a'), row('b')]);
    expect(r.pendingIds).toEqual(['new1', 'new2']);
    expect(r.displayed.map(getId)).toEqual(['a', 'b']);
    expect(r.nextCommittedIds).toEqual(['a', 'b']);
  });

  it('absorbs tail growth from fetchNextPage without a badge', () => {
    const r = run(['a', 'b'], [row('a'), row('b'), row('c'), row('d')]);
    expect(r.pendingIds).toEqual([]);
    expect(r.displayed.map(getId)).toEqual(['a', 'b', 'c', 'd']);
    expect(r.nextCommittedIds).toEqual(['a', 'b', 'c', 'd']);
  });

  it('has no pending left after committing the live order', () => {
    const live = [row('new1'), row('a'), row('b')];
    const committed = commitLiveOrder(live, getId);
    const r = run(committed, live);
    expect(r.pendingIds).toEqual([]);
    expect(r.displayed.map(getId)).toEqual(['new1', 'a', 'b']);
  });

  it('refreshes displayed values from live while the order stays frozen', () => {
    const r = run(['a', 'b'], [row('new1'), row('a', 42), row('b', 7)]);
    expect(r.displayed).toEqual([row('a', 42), row('b', 7)]);
  });

  it('keeps an id that fell out of live, rendered from lastKnown', () => {
    const lastKnown = new Map([['b', row('b', 3)]]);
    const r = run(['a', 'b'], [row('a', 1)], lastKnown);
    expect(r.displayed).toEqual([row('a', 1), row('b', 3)]);
    expect(r.nextCommittedIds).toEqual(['a', 'b']);
  });

  it('drops an id that is neither live nor known', () => {
    const r = run(['a', 'gone'], [row('a')]);
    expect(r.displayed.map(getId)).toEqual(['a']);
  });

  it('recommits wholesale when no committed id survives in live', () => {
    const r = run(['a', 'b'], [row('x'), row('y')]);
    expect(r.pendingIds).toEqual([]);
    expect(r.nextCommittedIds).toEqual(['x', 'y']);
    expect(r.displayed.map(getId)).toEqual(['x', 'y']);
  });
});
