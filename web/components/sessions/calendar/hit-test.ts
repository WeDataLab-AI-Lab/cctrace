/**
 * 입력 시각 가로선의 호버(C16). 입력선은 한두 픽셀 두께라 정확히 맞히기 어려우므로, 포인터가 띠 위에 있을 때
 * **포인터 시각에서 가장 가까운 입력**을 허용 오차(px) 안에서 찾는다. 시각 순으로 정렬된 배열을 이분 탐색한다.
 */

/** 정렬된 시각 배열에서 target에 가장 가까운 것의 순번. 허용 오차 밖이면 -1. */
const nearestIndex = (sortedTimes: readonly number[], target: number, toleranceMs: number): number => {
  if (sortedTimes.length === 0 || !Number.isFinite(target)) return -1;
  let lo = 0;
  let hi = sortedTimes.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (sortedTimes[mid] < target) lo = mid + 1;
    else hi = mid;
  }
  // lo는 target 이상인 첫 순번이다. 그 앞과 비교한다.
  const after = lo;
  const before = lo - 1;
  const dAfter = Math.abs(sortedTimes[after] - target);
  const dBefore = before >= 0 ? Math.abs(sortedTimes[before] - target) : Number.POSITIVE_INFINITY;
  const best = dBefore <= dAfter ? before : after;
  return Math.min(dBefore, dAfter) <= toleranceMs ? best : -1;
};

/** 허용 오차(px)를 시간으로. */
const toleranceMsFor = (tolerancePx: number, pxPerHour: number): number => (tolerancePx / pxPerHour) * 3_600_000;

export { nearestIndex, toleranceMsFor };
