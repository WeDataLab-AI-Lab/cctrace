/**
 * 미리보기 화면의 목록 로직(필터, 정렬). 서버가 하는 일을 픽스처 위에서 흉내 낸다.
 * 실제 화면은 `category=`, `plan=`, `sort=` 쿼리로 서버에 맡긴다(P2).
 */

import type { PlanFilter, SessionCategoryKey, SessionSort } from '@/lib/session-analysis-types';
import type { SessionOverview } from '@/lib/types';

interface ListQuery {
  category: SessionCategoryKey | null;
  plan: PlanFilter | null;
  sort: SessionSort;
}

const startMs = (s: SessionOverview): number => Date.parse(s.start_time) || 0;
const endMs = (s: SessionOverview): number => Date.parse(s.end_time) || 0;

/** 정렬 기준 값. 요청 수를 모르거나 분석이 없는 행은 null 이고 맨 뒤로 간다. */
const sortValue = (s: SessionOverview, sort: SessionSort): number | null => {
  switch (sort) {
    case 'start':
      return startMs(s);
    case 'activity':
      return endMs(s);
    case 'cost':
      return s.cost_usd;
    case 'requests':
      return typeof s.analysis?.requests === 'number' ? s.analysis.requests : null;
  }
};

/** 큰 것이 먼저. 값을 모르는 행은 어떤 정렬에서든 맨 뒤(같은 값끼리는 시작이 늦은 순). */
const compareRows = (sort: SessionSort) => (a: SessionOverview, b: SessionOverview): number => {
  const va = sortValue(a, sort);
  const vb = sortValue(b, sort);
  if (va === null && vb === null) return startMs(b) - startMs(a);
  if (va === null) return 1;
  if (vb === null) return -1;
  return vb - va || startMs(b) - startMs(a);
};

const matchesPlan = (s: SessionOverview, plan: PlanFilter): boolean => {
  const flags = s.analysis?.plan_flags;
  if (!flags) return false;
  return plan === 'file' ? flags.has_plan_file : flags.used_plan_mode;
};

/** 필터 뒤 정렬. 입력 배열은 건드리지 않는다. */
const queryRows = (rows: readonly SessionOverview[], q: ListQuery): SessionOverview[] =>
  rows
    .filter((s) => (q.category === null ? true : s.analysis?.category === q.category))
    .filter((s) => (q.plan === null ? true : matchesPlan(s, q.plan)))
    .sort(compareRows(q.sort));

const LIVE_WINDOW_MS = 5 * 60_000;

/** 기존 LIVE 배지의 기준(마지막 기록이 5분 이내)을 기준 시각으로 계산한다. */
const isLiveAt = (s: SessionOverview, nowMs: number): boolean => {
  const age = nowMs - endMs(s);
  return age >= 0 && age < LIVE_WINDOW_MS;
};

export { queryRows, compareRows, matchesPlan, isLiveAt };
export type { ListQuery };
