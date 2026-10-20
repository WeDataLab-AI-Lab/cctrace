import { describe, expect, it } from 'vitest';
import { nearestIndex, toleranceMsFor } from './hit-test';

describe('입력선 호버: 가장 가까운 입력 찾기(C16)', () => {
  const times = [100, 200, 300, 1000];

  it('정확히 맞으면 그 순번', () => {
    expect(nearestIndex(times, 200, 0)).toBe(1);
    expect(nearestIndex(times, 1000, 0)).toBe(3);
  });

  it('허용 오차 안에서 가까운 쪽을 고른다', () => {
    expect(nearestIndex(times, 140, 50)).toBe(0);
    expect(nearestIndex(times, 160, 50)).toBe(1);
    expect(nearestIndex(times, 290, 50)).toBe(2);
  });

  it('허용 오차 밖이면 -1(입력이 아니라 띠 호버다)', () => {
    expect(nearestIndex(times, 500, 50)).toBe(-1);
    expect(nearestIndex(times, 50, 10)).toBe(-1);
    expect(nearestIndex(times, 2000, 100)).toBe(-1);
  });

  it('처음보다 앞, 끝보다 뒤도 허용 오차 안이면 찾는다', () => {
    expect(nearestIndex(times, 90, 20)).toBe(0);
    expect(nearestIndex(times, 1010, 20)).toBe(3);
  });

  it('같은 거리면 앞 입력을 고른다', () => {
    expect(nearestIndex([100, 200], 150, 60)).toBe(0);
  });

  it('입력이 없거나 시각이 NaN이면 -1', () => {
    expect(nearestIndex([], 100, 50)).toBe(-1);
    expect(nearestIndex(times, Number.NaN, 50)).toBe(-1);
  });

  it('입력이 하나뿐이어도 된다', () => {
    expect(nearestIndex([500], 505, 10)).toBe(0);
    expect(nearestIndex([500], 600, 10)).toBe(-1);
  });

  it('허용 오차(px)는 배율에 반비례한 시간이다', () => {
    // 시간당 30px에서 4px = 8분.
    expect(toleranceMsFor(4, 30)).toBeCloseTo(8 * 60_000, 6);
    expect(toleranceMsFor(4, 240)).toBeLessThan(toleranceMsFor(4, 30));
  });
});
