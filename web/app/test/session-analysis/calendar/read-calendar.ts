/**
 * 서버 단계(P3)가 하게 될 `GET /api/session-calendar?from=&to=`를 픽스처 위에서 흉내 낸다. 서버가 지킬 규칙을 한 곳에 적어
 * 두고 시험으로 고정한다 — 서버는 같은 시험을 통과해야 한다.
 *
 * - 구간은 반열린 `[from, to)`, 최대 14일.
 * - 세션 전체 구간이 아니라 **조각**이 구간과 겹치는 세션을 고른다(`tstzrange(seg_start, seg_end, '[]') && [from, to)`).
 *   조각이 없는 세션(`truncated` 등)은 세션 범위가 겹치면 고른다.
 * - 조각은 자르지 않고 준다. 입력 시각은 구간 안의 것만, 시각 순으로 세션마다 최대 2,000개이고 넘치면 `truncated`다.
 * - 비소유 관리자에게는 `colors.status`가 없다(남의 세션의 판정은 신호에서 나오므로 API가 내주지 않는다).
 */

import {
  CALENDAR_MAX_DAYS,
  CALENDAR_MAX_INPUTS,
  hasSegments,
  type CalendarSession,
  type SessionCalendar,
} from '@/lib/session-calendar-types';

const DAY_MS = 86_400_000;

export type CalendarRole = 'owner' | 'admin';

/** 구간과 겹치는가. 조각의 끝은 포함한다(기록 한 줄뿐인 조각은 시작과 끝이 같다). */
const overlapsRange = (start: number, end: number, from: number, to: number): boolean => start < to && end >= from;

/** 그 구간에서 이 세션을 골라야 하는가. */
const selectedFor = (s: CalendarSession, from: number, to: number): boolean => {
  if (hasSegments(s.facts)) return s.segments.some((seg) => overlapsRange(Date.parse(seg.start), Date.parse(seg.end), from, to));
  return overlapsRange(Date.parse(s.range.start), Date.parse(s.range.end), from, to);
};

/** 입력 시각을 구간으로 거르고 상한에서 자른다. */
const clipInputs = (s: CalendarSession, from: number, to: number): CalendarSession['inputs'] => {
  if (s.inputs.state !== 'observed') return s.inputs;
  const inRange = s.inputs.items
    .filter((i) => {
      const at = Date.parse(i.ts);
      return at >= from && at < to;
    })
    .sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts));
  const truncated = inRange.length > CALENDAR_MAX_INPUTS;
  return { state: 'observed', items: truncated ? inRange.slice(0, CALENDAR_MAX_INPUTS) : inRange, truncated };
};

/** 비소유 관리자 범위: 완료 여부를 내주지 않는다. */
const forAdmin = (s: CalendarSession): CalendarSession => {
  const { status, ...colors } = s.colors;
  void status;
  return { ...s, colors };
};

const readCalendar = (sessions: readonly CalendarSession[], fromIso: string, toIso: string, role: CalendarRole): SessionCalendar => {
  const from = Date.parse(fromIso);
  const to = Date.parse(toIso);
  if (!Number.isFinite(from) || !Number.isFinite(to) || to <= from) throw new Error('invalid calendar range');
  if ((to - from) / DAY_MS > CALENDAR_MAX_DAYS) throw new Error(`calendar range exceeds ${CALENDAR_MAX_DAYS} days`);
  const picked = sessions
    .filter((s) => selectedFor(s, from, to))
    .map((s) => ({ ...s, inputs: clipInputs(s, from, to) }))
    .map((s) => (role === 'admin' ? forAdmin(s) : s));
  return { from: fromIso, to: toIso, owner_view: role === 'owner', sessions: picked };
};

export { overlapsRange, selectedFor, clipInputs, readCalendar };
