/**
 * 뉴욕의 서머타임 전환일을 눈으로 보기 위한 캘린더 전용 합성 세션. 전환일 열(23·25시간)에서 띠와 입력선이 절대 시간에 맞게 서는지
 * 볼 수 있게, 건너뛴 시와 반복된 시를 가로지르는 세션을 둔다.
 *
 * - 봄(2026-03-08 일요일): 02:00 EST(07:00Z)에 03:00 EDT로 건너뛴다. 그 날은 23시간이다.
 * - 가을(2025-11-02 일요일): 02:00 EDT(06:00Z)에 01:00 EST로 되돌아간다. 그 날은 25시간이다. 2026-11-01은 미리보기의 기준 시각
 *   (2026-10-10)보다 뒤라 세션을 둘 수 없어, 가장 가까운 지난 전환일을 쓴다. 2026-11-01 주도 ◀▶로 갈 수 있고(비어 있다) 같은 열 높이다.
 *
 * 분석 응답이 없는 세션이라 뷰어는 "캘린더 전용 합성 세션" 안내를 보인다.
 */

import type { CalendarSession } from '@/lib/session-calendar-types';
import { calInput, calSeg, calSession } from './calendar-builders';

const Z = (iso: string): number => Date.parse(iso);

const demo = (
  id: string,
  title: string,
  segments: [string, string][],
  inputs: string[],
  colors: { screens: string[]; components: string[]; kinds: string[]; status: 'done' | 'leftover' | 'midway' },
): CalendarSession =>
  calSession(id, {
    title: { text: title, source: 'custom', state: 'ok' },
    segments: segments.map(([a, b], i) => calSeg(Z(a), Z(b), { records: 30 + i * 7, human_inputs: inputs.filter((t) => Z(t) >= Z(a) && Z(t) <= Z(b)).length })),
    inputs: inputs.map((t, i) => calInput(Z(t), { turn_key: `${id}-t${i + 1}`, text: `${title} — ${i + 1}번째 요청` })),
    requests: inputs.length,
    colors: { project: { hash: '-demo-web-console', name: 'web-console' }, agent: 'claude', model: 'claude-opus-5', profile: '메인', ...colors },
  });

const DST_DEMO: CalendarSession[] = [
  // ---- 봄: 2026-03-08(일) 23시간. 00:20 EST ~ 03:40 EDT는 절대 시간으로 2시간 20분이다.
  demo(
    'dst-spring-over-the-gap',
    '건너뛴 시를 가로지르는 세션',
    [['2026-03-08T05:20:00Z', '2026-03-08T07:40:00Z']],
    ['2026-03-08T05:30:00Z', '2026-03-08T06:30:00Z', '2026-03-08T07:10:00Z', '2026-03-08T07:35:00Z'],
    { screens: ['대시보드'], components: ['웹'], kinds: ['구현'], status: 'done' },
  ),
  demo(
    'dst-spring-morning',
    '전환일 아침 작업',
    [['2026-03-08T13:00:00Z', '2026-03-08T14:30:00Z']],
    ['2026-03-08T13:05:00Z', '2026-03-08T13:40:00Z', '2026-03-08T14:10:00Z'],
    { screens: ['설정'], components: ['디자인 시스템'], kinds: ['정비'], status: 'leftover' },
  ),
  demo(
    'dst-spring-weekday',
    '전환 주 평일 작업',
    [['2026-03-03T15:00:00Z', '2026-03-03T16:10:00Z'], ['2026-03-05T19:00:00Z', '2026-03-05T20:20:00Z']],
    ['2026-03-03T15:02:00Z', '2026-03-03T15:40:00Z', '2026-03-05T19:05:00Z'],
    { screens: ['리포트'], components: ['배치'], kinds: ['조사'], status: 'done' },
  ),

  // ---- 가을: 2025-11-02(일) 25시간. 00:20 EDT ~ 03:10 EST는 절대 시간으로 3시간 50분이다.
  demo(
    'dst-fall-over-the-repeat',
    '반복된 시를 가로지르는 세션',
    [['2025-11-02T04:20:00Z', '2025-11-02T08:10:00Z']],
    ['2025-11-02T04:30:00Z', '2025-11-02T05:30:00Z', '2025-11-02T06:30:00Z', '2025-11-02T07:30:00Z'],
    { screens: ['대시보드'], components: ['웹'], kinds: ['버그 수정'], status: 'leftover' },
  ),
  demo(
    'dst-fall-from-saturday-night',
    '토요일 밤에서 이어진 세션',
    [['2025-11-02T02:30:00Z', '2025-11-02T05:10:00Z']],
    ['2025-11-02T02:40:00Z', '2025-11-02T03:50:00Z', '2025-11-02T04:55:00Z'],
    { screens: ['알림'], components: ['알림 워커'], kinds: ['구현'], status: 'midway' },
  ),
  demo(
    'dst-fall-morning',
    '전환일 오전 작업',
    [['2025-11-02T14:00:00Z', '2025-11-02T15:30:00Z']],
    ['2025-11-02T14:05:00Z', '2025-11-02T14:50:00Z'],
    { screens: ['결제'], components: ['결제 모듈'], kinds: ['상담'], status: 'done' },
  ),
  demo(
    'dst-fall-weekday',
    '전환 주 평일 작업',
    [['2025-10-28T14:00:00Z', '2025-10-28T15:00:00Z'], ['2025-10-30T18:00:00Z', '2025-10-30T19:30:00Z']],
    ['2025-10-28T14:02:00Z', '2025-10-30T18:05:00Z', '2025-10-30T18:45:00Z'],
    { screens: ['검색'], components: ['검색 인덱서'], kinds: ['문서'], status: 'done' },
  ),
];

/** 시연용 세션의 id인가(분석 픽스처가 없다). */
const isDemoSession = (id: string): boolean => DST_DEMO.some((s) => s.session_id === id);

/** 뉴욕의 전환 주(월요일)와 전환일. 미리보기의 "전환일로 이동" 단추가 쓴다. */
const DST_JUMPS = [
  { key: 'spring', label: '봄 전환 3/8(23시간)', weekStart: { year: 2026, month: 3, day: 2 }, timeZone: 'America/New_York' },
  { key: 'fall', label: '가을 전환 2025년 11/2(25시간)', weekStart: { year: 2025, month: 10, day: 27 }, timeZone: 'America/New_York' },
] as const;

export { DST_DEMO, DST_JUMPS, isDemoSession };
