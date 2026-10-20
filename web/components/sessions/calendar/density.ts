/**
 * 발언량 농도 띠(C11). 날짜 열 왼쪽 끝에 10분 단위로 **사람 입력 수**의 농도를 그린다.
 *
 * 불투명도 = 0.15 + 0.85 × (n ÷ 그 주의 최댓값). 최댓값은 열마다가 아니라 **그 주 전체**의 한 버킷 최댓값이라,
 * 요일을 가로질러 농도를 비교할 수 있다. 입력이 없는 버킷(n = 0)은 그리지 않는다.
 * 응답(에이전트의 출력)까지 세면 에이전트가 길게 일한 구간이 사람의 활동처럼 보이므로 사람 입력만 센다(계획 C11).
 *
 * 버킷은 지역 자정에서 10분씩이다. 전환일(23·25시간)도 같은 규칙이라 138·150개다.
 */

import type { DayColumn } from './day-grid';

/** 버킷 길이(ms). */
const DENSITY_BUCKET_MS = 10 * 60_000;
const MIN_OPACITY = 0.15;

export interface DensityBucket {
  /** 열 안에서 몇 번째 버킷인가(0부터). */
  index: number;
  /** 버킷의 절대 시각(ms). 포함. */
  start: number;
  /** 버킷의 끝(ms). 열 끝에서 잘린다. */
  end: number;
  /** 이 버킷의 사람 입력 수. 1 이상이다. */
  count: number;
}

/** 열 안의 입력만 10분 버킷으로 센다. 입력이 있는 버킷만 돌려준다(시간 순). */
const densityBuckets = (column: Pick<DayColumn, 'start' | 'end'>, inputTimes: readonly number[]): DensityBucket[] => {
  const counts = new Map<number, number>();
  for (const at of inputTimes) {
    if (at < column.start || at >= column.end) continue;
    const index = Math.floor((at - column.start) / DENSITY_BUCKET_MS);
    counts.set(index, (counts.get(index) ?? 0) + 1);
  }
  return [...counts.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([index, count]) => {
      const start = column.start + index * DENSITY_BUCKET_MS;
      return { index, start, end: Math.min(start + DENSITY_BUCKET_MS, column.end), count };
    });
};

/** 그 주의 가장 진한 버킷의 입력 수. 입력이 하나도 없으면 0. */
const maxBucketCount = (days: readonly (readonly DensityBucket[])[]): number =>
  days.reduce((max, buckets) => buckets.reduce((m, b) => Math.max(m, b.count), max), 0);

/** 버킷의 불투명도. 입력이 없거나 최댓값을 모르면 0(그리지 않는다). */
const densityOpacity = (count: number, max: number): number => {
  if (count <= 0 || max <= 0) return 0;
  return MIN_OPACITY + (1 - MIN_OPACITY) * Math.min(1, count / max);
};

export { DENSITY_BUCKET_MS, MIN_OPACITY, densityBuckets, maxBucketCount, densityOpacity };
