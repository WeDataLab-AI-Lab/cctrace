import { describe, expect, it } from 'vitest';
import { baseFilter, isRequestless, requestlessCount, sortSessions, statusFilter, type FilterableSession } from './session-filters';
import { statusChipCounts } from './status';

const item = (id: string, o: Partial<FilterableSession> = {}): FilterableSession => ({
  id,
  status: 'leftover',
  requests: 3,
  planFile: false,
  startMs: 0,
  endMs: 0,
  costUsd: 1,
  ...o,
});

const ids = (items: readonly FilterableSession[]): string[] => items.map((s) => s.id);

describe('기본 필터: 요청 없는 세션(B5)과 플랜 파일(B6)', () => {
  const items = [
    item('a'),
    item('b', { requests: 0 }),
    item('c', { requests: null }),
    item('d', { planFile: true }),
    item('e', { requests: 0, planFile: true }),
  ];

  it('요청 없는 세션은 기본으로 숨긴다. 요청 수를 모르는 세션은 "요청 없음"이 아니라 보인다', () => {
    expect(isRequestless(items[1])).toBe(true);
    expect(isRequestless(items[2])).toBe(false);
    expect(ids(baseFilter(items, { showRequestless: false, planFileOnly: false }))).toEqual(['a', 'c', 'd']);
  });

  it('"요청 없는 세션도 보기"를 켜면 모두 보인다', () => {
    expect(ids(baseFilter(items, { showRequestless: true, planFileOnly: false }))).toEqual(['a', 'b', 'c', 'd', 'e']);
  });

  it('"플랜 파일 있는 것만"은 두 조건이 함께 걸린다', () => {
    expect(ids(baseFilter(items, { showRequestless: false, planFileOnly: true }))).toEqual(['d']);
    expect(ids(baseFilter(items, { showRequestless: true, planFileOnly: true }))).toEqual(['d', 'e']);
  });

  it('"요청 없는 세션도 보기 (N건)"의 N은 켰을 때 더해지는 수다', () => {
    expect(requestlessCount(items, false)).toBe(2);
    // 플랜 파일 필터가 켜져 있으면 그 필터를 통과한 것만 센다.
    expect(requestlessCount(items, true)).toBe(1);
    const before = baseFilter(items, { showRequestless: false, planFileOnly: true }).length;
    const after = baseFilter(items, { showRequestless: true, planFileOnly: true }).length;
    expect(after - before).toBe(requestlessCount(items, true));
  });
});

describe('상태 칩 필터(B3)', () => {
  const items = [
    item('d1', { status: 'done' }),
    item('d2', { status: 'done' }),
    item('l1', { status: 'leftover' }),
    item('r1', { status: 'running' }),
    item('u1', { status: null }),
    item('m1', { status: 'midway' }),
  ];

  it('칩이 눌려 있지 않으면 그대로', () => {
    expect(ids(statusFilter(items, null))).toEqual(['d1', 'd2', 'l1', 'r1', 'u1', 'm1']);
  });

  it('실행 중과 판정 없는 세션은 서로 다른 칩에서 고른다', () => {
    expect(ids(statusFilter(items, 'done'))).toEqual(['d1', 'd2']);
    expect(ids(statusFilter(items, 'leftover'))).toEqual(['l1', 'r1']);
    expect(ids(statusFilter(items, 'midway'))).toEqual(['m1']);
    expect(ids(statusFilter(items, 'unknown'))).toEqual(['u1']);
  });

  it('칩의 개수는 필터를 걸기 전 집합에서 세므로, 칩을 눌러도 다른 칩의 개수가 바뀌지 않고 합이 세션 수다', () => {
    const counts = statusChipCounts(items.map((s) => s.status));
    expect(Object.values(counts.counts).reduce((sum, count) => sum + count, 0)).toBe(items.length);
    for (const chip of ['done', 'leftover', 'midway', 'unknown'] as const) {
      expect(statusFilter(items, chip)).toHaveLength(counts.counts[chip]);
    }
  });
});

describe('정렬(B7)', () => {
  const items = [
    item('old', { startMs: 100, endMs: 900, costUsd: 5, requests: 10 }),
    item('new', { startMs: 300, endMs: 400, costUsd: 1, requests: 2 }),
    item('mid', { startMs: 200, endMs: 700, costUsd: 9, requests: 30 }),
  ];

  it('시작 최신순, 마지막 활동순, 비용순, 요청 수순은 큰 것이 먼저', () => {
    expect(ids(sortSessions(items, 'start'))).toEqual(['new', 'mid', 'old']);
    expect(ids(sortSessions(items, 'activity'))).toEqual(['old', 'mid', 'new']);
    expect(ids(sortSessions(items, 'cost'))).toEqual(['mid', 'old', 'new']);
    expect(ids(sortSessions(items, 'requests'))).toEqual(['mid', 'old', 'new']);
  });

  it('값을 모르는 세션은 어떤 정렬에서든 맨 뒤다(0보다 뒤)', () => {
    const withUnknown = [item('zero', { requests: 0, costUsd: 0, startMs: 1 }), item('unknown', { requests: null, costUsd: null, startMs: 2 }), ...items];
    expect(ids(sortSessions(withUnknown, 'requests')).slice(-2)).toEqual(['zero', 'unknown']);
    expect(ids(sortSessions(withUnknown, 'cost')).slice(-2)).toEqual(['zero', 'unknown']);
  });

  it('입력 배열은 바뀌지 않는다', () => {
    const before = ids(items);
    sortSessions(items, 'cost');
    expect(ids(items)).toEqual(before);
  });
});
