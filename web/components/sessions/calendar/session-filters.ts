/**
 * 세션 툴바(B3~B7)의 필터와 정렬. 목록과 캘린더가 **같은 세션 집합**을 보도록 한 곳에서 계산한다.
 *
 * 순서가 중요하다.
 *   1. 기본 필터: "요청 없는 세션도 보기"(B5)가 꺼져 있으면 요청 없는 세션을 뺀다. "플랜 파일 있는 것만"(B6)이면 그것만 남긴다.
 *   2. 이 집합으로 세션 기록 N건과 상태 칩 셋의 개수를 센다 — 칩의 합이 N이어야 하므로 칩 필터는 이 뒤에 건다.
 *   3. 상태 칩 필터(B3): 칩 하나를 누르면 그 상태의 세션만 남는다.
 *   4. 정렬(B7).
 */

import type { SessionSort } from '@/lib/session-analysis-types';
import type { CalendarStatus } from '@/lib/session-calendar-types';
import { chipOfStatus, type StatusChipKey } from './status';

export interface FilterableSession {
  id: string;
  status: CalendarStatus | null | undefined;
  /** 요청 수. 모르면 null(요청 없는 세션이 아니다). 0은 "요청 없는 세션"이다. */
  requests: number | null;
  /** 플랜 파일이 있는 세션인가(B6). */
  planFile: boolean;
  startMs: number;
  endMs: number;
  costUsd: number | null;
}

export interface BaseFilters {
  /** "요청 없는 세션도 보기"가 켜져 있는가. */
  showRequestless: boolean;
  /** "플랜 파일 있는 것만"이 켜져 있는가. */
  planFileOnly: boolean;
}

const isRequestless = (s: Pick<FilterableSession, 'requests'>): boolean => s.requests === 0;

/** 1단계. 상태 칩 필터 전의 집합. */
const baseFilter = <T extends FilterableSession>(items: readonly T[], filters: BaseFilters): T[] =>
  items.filter((s) => (filters.showRequestless || !isRequestless(s)) && (!filters.planFileOnly || s.planFile));

/** 3단계. 칩이 눌려 있지 않으면(null) 그대로. */
const statusFilter = <T extends FilterableSession>(items: readonly T[], chip: StatusChipKey | null): T[] =>
  chip === null ? [...items] : items.filter((s) => chipOfStatus(s.status) === chip);

/**
 * "요청 없는 세션도 보기 (N건)"의 N: 이 체크를 켜면 **더해지는** 세션 수. 플랜 파일 필터가 켜져 있으면 그 필터를 통과한
 * 것만 센다 — 켰을 때 실제로 늘어나는 수와 같아야 하기 때문이다.
 */
const requestlessCount = <T extends FilterableSession>(items: readonly T[], planFileOnly: boolean): number =>
  items.filter((s) => isRequestless(s) && (!planFileOnly || s.planFile)).length;

/** 큰 것이 먼저. 값을 모르는 세션(null)은 어떤 정렬에서든 맨 뒤다. 같은 값끼리는 시작이 늦은 쪽이 먼저다. */
const sortValue = (s: FilterableSession, sort: SessionSort): number | null => {
  switch (sort) {
    case 'start':
      return s.startMs;
    case 'activity':
      return s.endMs;
    case 'cost':
      return s.costUsd;
    case 'requests':
      return s.requests;
  }
};

const sortSessions = <T extends FilterableSession>(items: readonly T[], sort: SessionSort): T[] =>
  [...items].sort((a, b) => {
    const va = sortValue(a, sort);
    const vb = sortValue(b, sort);
    if (va === null && vb === null) return b.startMs - a.startMs;
    if (va === null) return 1;
    if (vb === null) return -1;
    return vb - va || b.startMs - a.startMs;
  });

export { isRequestless, baseFilter, statusFilter, requestlessCount, sortSessions };
