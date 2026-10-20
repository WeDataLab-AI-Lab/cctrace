import { describe, expect, it } from 'vitest';
import { weekColumns } from './day-grid';
import { DENSITY_BUCKET_MS, densityBuckets, densityOpacity, maxBucketCount } from './density';

const t = (iso: string): number => Date.parse(iso);
const [monday] = weekColumns('Asia/Seoul', { year: 2026, month: 10, day: 5 }, 0);
const MIN = 60_000;

describe('발언량 농도 띠(C11): 10분 버킷의 사람 입력 수', () => {
  it('열 시작부터 10분씩 센다: 경계는 아래 버킷에 포함된다', () => {
    const s = monday.start;
    const buckets = densityBuckets(monday, [s, s + 9 * MIN + 59_999, s + 10 * MIN, s + 10 * MIN + 1, s + 25 * MIN]);
    expect(buckets.map((b) => [b.index, b.count])).toEqual([
      [0, 2],
      [1, 2],
      [2, 1],
    ]);
    expect(buckets[0]).toMatchObject({ start: s, end: s + DENSITY_BUCKET_MS });
  });

  it('입력이 없는 버킷은 돌려주지 않는다', () => {
    expect(densityBuckets(monday, [])).toEqual([]);
    expect(densityBuckets(monday, [monday.start + 600 * MIN]).map((b) => b.index)).toEqual([60]);
  });

  it('열 밖의 입력은 세지 않는다(시작 포함, 끝 제외)', () => {
    const times = [monday.start - 1, monday.start, monday.end - 1, monday.end];
    expect(densityBuckets(monday, times).map((b) => b.count)).toEqual([1, 1]);
    expect(densityBuckets(monday, times).map((b) => b.index)).toEqual([0, 143]);
  });

  it('전환일 열은 같은 규칙이라 버킷이 138개(23시간)·150개(25시간)다', () => {
    const [spring] = weekColumns('America/New_York', { year: 2026, month: 3, day: 8 }, 0);
    const [fall] = weekColumns('America/New_York', { year: 2026, month: 11, day: 1 }, 0);
    expect(densityBuckets(spring, [spring.end - 1]).map((b) => b.index)).toEqual([137]);
    expect(densityBuckets(fall, [fall.end - 1]).map((b) => b.index)).toEqual([149]);
    // 마지막 버킷은 열 끝에서 잘리지 않는다(10분 단위가 딱 맞는다).
    expect(densityBuckets(fall, [fall.end - 1])[0].end).toBe(fall.end);
  });

  it('그 주의 최댓값은 열이 아니라 주 전체에서 정한다', () => {
    const tuesday = weekColumns('Asia/Seoul', { year: 2026, month: 10, day: 5 }, 0)[1];
    const mon = densityBuckets(monday, [monday.start, monday.start + 1]);
    const tue = densityBuckets(tuesday, Array.from({ length: 5 }, (_, i) => tuesday.start + i));
    expect(maxBucketCount([mon, tue])).toBe(5);
    expect(maxBucketCount([])).toBe(0);
    expect(maxBucketCount([[], []])).toBe(0);
  });
});

describe('불투명도 0.15 + 0.85 × (n ÷ 최댓값)', () => {
  it('최댓값은 1, 입력 1개는 0.15 + 0.85/최댓값', () => {
    expect(densityOpacity(4, 4)).toBeCloseTo(1, 10);
    expect(densityOpacity(1, 4)).toBeCloseTo(0.15 + 0.85 * 0.25, 10);
    expect(densityOpacity(1, 1)).toBeCloseTo(1, 10);
  });

  it('입력이 없거나 최댓값을 모르면 0(그리지 않는다)', () => {
    expect(densityOpacity(0, 4)).toBe(0);
    expect(densityOpacity(3, 0)).toBe(0);
  });

  it('최댓값보다 큰 값은 1에서 멈춘다', () => {
    expect(densityOpacity(9, 4)).toBe(1);
  });

  it('입력이 많을수록 짙다', () => {
    const o = [1, 2, 3, 4].map((n) => densityOpacity(n, 4));
    expect(o).toEqual([...o].sort((a, b) => a - b));
    expect(t('2026-10-05T00:00:00Z')).toBeGreaterThan(0);
  });
});
