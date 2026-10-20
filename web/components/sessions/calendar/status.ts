/**
 * 완료 여부(B3, C4)와 목록 툴바의 상태 칩.
 *
 * 실행 중은 아직 끝나지 않았으므로 "하다 만 것"에 센다. 판정 자료가 없는 세션은
 * 미완료라고 단정하지 않고 별도 "판정 불가" 칩으로 센다. 네 칩의 합은 세션 수다.
 */

import type { CalendarStatus } from '@/lib/session-calendar-types';

/** 완료 여부 값의 이름·색·설명. 색은 값마다 고정이다(순위로 정하지 않는다). */
const STATUS_META: Record<CalendarStatus, { label: string; token: string; hint: string }> = {
  done: {
    label: '완료',
    token: 'var(--success)',
    hint: '판정 칩이 모두 통과다: 턴 종료, 답 대기 없음, 백그라운드 작업 없음, 커밋됨, 작업 내용 완료.',
  },
  leftover: {
    label: '하다 만 것',
    token: 'var(--ink-3)',
    hint: '남은 것이 있다: 답하지 않은 질문, 백그라운드 작업, 커밋하지 않은 편집, 미완료 작업 항목.',
  },
  midway: {
    label: '도중 종료',
    token: 'var(--warning)',
    hint: '턴이 중단·오류·응답 없음으로 끝났거나, 실행 중이던 세션이 사라졌다.',
  },
  running: {
    label: '실행 중',
    token: 'var(--cat-1)',
    hint: '살아 있는 인스턴스가 일하거나 응답을 기다린다.',
  },
  unknown: {
    label: '판정 불가',
    token: 'var(--ink-4)',
    hint: '판정 자료가 없거나 불완전해 완료 여부를 알 수 없습니다.',
  },
};

/** 색 기준 범례에서 완료 여부 값의 순서(같은 개수일 때). */
const STATUS_ORDER: readonly CalendarStatus[] = ['done', 'leftover', 'midway', 'running', 'unknown'];

export type StatusChipKey = 'done' | 'leftover' | 'midway' | 'unknown';

const STATUS_CHIPS: readonly { key: StatusChipKey; label: string; hint: string }[] = [
  { key: 'done', label: STATUS_META.done.label, hint: STATUS_META.done.hint },
  {
    key: 'leftover',
    label: STATUS_META.leftover.label,
    hint: `${STATUS_META.leftover.hint} 실행 중인 세션도 여기에 셉니다.`,
  },
  { key: 'midway', label: STATUS_META.midway.label, hint: STATUS_META.midway.hint },
  { key: 'unknown', label: STATUS_META.unknown.label, hint: STATUS_META.unknown.hint },
];

/** 상태 → 칩. 실행 중만 "하다 만 것"에 포함한다. */
const chipOfStatus = (status: CalendarStatus | null | undefined): StatusChipKey => {
  if (status === 'done') return 'done';
  if (status === 'midway') return 'midway';
  if (status === 'leftover' || status === 'running') return 'leftover';
  return 'unknown';
};

export interface StatusChipCounts {
  counts: Record<StatusChipKey, number>;
  /** "하다 만 것"에 든 실행 중 세션 수. */
  running: number;
  /** 판정 불가 칩에 든 상태 누락 세션 수. */
  unjudged: number;
  /** 네 칩의 합 = 세션 수. */
  total: number;
}

const statusChipCounts = (statuses: readonly (CalendarStatus | null | undefined)[]): StatusChipCounts => {
  const counts: Record<StatusChipKey, number> = { done: 0, leftover: 0, midway: 0, unknown: 0 };
  let running = 0;
  let unjudged = 0;
  for (const status of statuses) {
    counts[chipOfStatus(status)] += 1;
    if (status === 'running') running += 1;
    if (status === undefined || status === null) unjudged += 1;
  }
  return { counts, running, unjudged, total: statuses.length };
};

export { STATUS_META, STATUS_ORDER, STATUS_CHIPS, chipOfStatus, statusChipCounts };
