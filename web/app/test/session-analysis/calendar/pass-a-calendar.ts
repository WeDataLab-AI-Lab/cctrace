/**
 * 패스 A의 열여섯 픽스처(`PREVIEW_SESSIONS`)가 캘린더에 서는 모양. 분석 응답의 턴에서 활동 조각과 입력 시각을 **파생**한다 —
 * 그래서 뷰어의 요청 목록과 캘린더의 띠·입력선이 같은 사실을 말한다. 15분 이상 비면 조각을 끊는다(p1b-design §3.17).
 *
 * 색 기준의 값(완료 여부, 컴포넌트, 화면, 종별, 프로필)은 세션 기록(P5)이 정하므로 패스 A의 분석 응답에는 없다.
 * 여기에서 픽스처마다 합성해 붙인다.
 */

import type { AnalysisTitle } from '@/lib/session-analysis-types';
import { isFullAnalysis } from '@/components/sessions/analysis/guards';
import type { CalendarInput, CalendarSegment, CalendarSession, CalendarStatus } from '@/lib/session-calendar-types';
import { hasSegments } from '@/lib/session-calendar-types';
import { P_SCHEDULE, A_ID, B_ID, C_ID, D_ID, E_ID, F_ID, G_ID, H_ID, I_ID, K_ID, L_ID, M_ID, N_ID, O_ID, P_ID, type SessionFixture } from '../fixtures';
import { iso } from './calendar-builders';

const MIN = 60_000;
/** 활동 공백이 이만큼 이상이면 조각을 끊는다(p1b-design §3.17). */
const GAP_MS = 15 * MIN;

export interface FixtureMeta {
  status: CalendarStatus | null;
  profile: string | null;
  components: string[];
  screens: string[];
  kinds: string[];
}

const none = (profile: string | null = null): FixtureMeta => ({ status: null, profile, components: [], screens: [], kinds: [] });

/** 픽스처별로 합성한 색 기준 값. J(혼합 신원)는 캘린더에 오지 않는다. */
const META: Record<string, FixtureMeta> = {
  [A_ID]: { status: 'running', profile: '메인', components: ['결제 모듈'], screens: ['결제'], kinds: ['버그 수정', '조사'] },
  [K_ID]: { status: 'leftover', profile: '서브', components: ['디자인 시스템'], screens: ['설정'], kinds: ['정비'] },
  [D_ID]: { status: 'running', profile: 'Codex', components: ['CLI'], screens: [], kinds: ['구현'] },
  [E_ID]: { status: 'leftover', profile: '메인', components: ['결제 모듈'], screens: ['결제'], kinds: ['구현'] },
  [L_ID]: { status: 'midway', profile: '서브', components: ['배치'], screens: ['리포트'], kinds: ['구현', '문서'] },
  [M_ID]: { status: 'leftover', profile: '메인', components: ['배치'], screens: ['대시보드'], kinds: ['버그 수정'] },
  [B_ID]: { status: 'done', profile: '서브', components: ['검색 인덱서'], screens: ['검색'], kinds: ['문서', '정비'] },
  // 비식별화로 판정에 필요한 신호를 모두 모른다: 완료 여부는 모름이고 세션 기록의 태그도 없다.
  [C_ID]: none('서브'),
  [N_ID]: { status: 'done', profile: '서브', components: ['CLI'], screens: ['설정'], kinds: ['조사'] },
  [O_ID]: { status: 'done', profile: '메인', components: [], screens: [], kinds: ['정비'] },
  [P_ID]: { status: 'leftover', profile: '메인', components: ['배치'], screens: ['리포트'], kinds: ['정비'] },
  [F_ID]: none('서브'),
  [G_ID]: none('메인'),
  [H_ID]: none(null),
  [I_ID]: none(null),
};

const metaOf = (id: string): FixtureMeta => META[id] ?? none(null);

interface Span {
  start: number;
  end: number;
  records: number;
}

/** 시각 순의 구간을 15분 이상의 공백에서 끊어 조각으로 묶는다. */
const mergeSpans = (spans: Span[]): Span[] => {
  const sorted = [...spans].sort((a, b) => a.start - b.start);
  const merged: Span[] = [];
  for (const span of sorted) {
    const last = merged[merged.length - 1];
    if (last && span.start - last.end < GAP_MS) {
      last.end = Math.max(last.end, span.end);
      last.records += span.records;
    } else merged.push({ ...span });
  }
  return merged;
};

const segmentsOf = (spans: Span[], inputTimes: number[]): CalendarSegment[] =>
  mergeSpans(spans).map((s) => ({
    start: iso(s.start),
    end: iso(s.end),
    records: s.records + 2,
    human_inputs: inputTimes.filter((t) => t >= s.start && t <= s.end).length,
  }));

const titleOf = (a: NonNullable<SessionFixture['analysis']>): AnalysisTitle => {
  const t = a.header?.title;
  return { text: t?.text ?? null, source: t?.source ?? 'none', state: t?.state ?? 'none' };
};

/** P: 나흘 × 10시간의 일정에서 2,812턴과 작업 중 입력 1,424개 전부(분석 응답은 상한에서 잘려 있다). */
const pCalendar = (f: SessionFixture, base: CalendarSession): CalendarSession => {
  const items: CalendarInput[] = [];
  for (let i = 0; i < P_SCHEDULE.totalTurns; i++) {
    items.push({
      ts: iso(P_SCHEDULE.turnAtMs(i)),
      turn_key: `tp${Math.min(i + 1, 2_000)}`,
      preview: { text: `로그 샘플 ${i + 1}번 묶음을 정리하고 요약 표를 갱신해줘`, state: 'ok' },
    });
  }
  for (let i = 0; i < P_SCHEDULE.totalTurns; i += 2) {
    items.push({ ts: iso(P_SCHEDULE.turnAtMs(i) + 25_000), turn_key: `tp${Math.min(i + 1, 2_000)}`, preview: { text: '(작업 중 입력)', state: 'ok' } });
  }
  for (let i = 1; i < 36; i += 2) {
    items.push({ ts: iso(P_SCHEDULE.turnAtMs(i) + 25_000), turn_key: `tp${i + 1}`, preview: { text: '(작업 중 입력)', state: 'ok' } });
  }
  items.sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts));
  const times = items.map((i) => Date.parse(i.ts));
  const segments: CalendarSegment[] = P_SCHEDULE.dayStartsMs.map((start) => {
    const end = start + P_SCHEDULE.windowMin * MIN;
    return { start: iso(start), end: iso(end), records: P_SCHEDULE.perDay * 9, human_inputs: times.filter((t) => t >= start && t <= end).length };
  });
  void f;
  return {
    ...base,
    range: { start: segments[0].start, end: segments[segments.length - 1].end },
    segments,
    inputs: { state: 'observed', items, truncated: false },
  };
};

/** C: 턴 경계를 판단할 수 없다. 활동 조각은 기록의 시각만으로 만들 수 있지만 사람 입력은 모른다. */
const redactedCalendar = (base: CalendarSession): CalendarSession => {
  const start = Date.parse(base.range.start);
  const span = Date.parse(base.range.end) - start;
  const cut = (a: number, b: number): CalendarSegment => ({
    start: iso(start + span * a),
    end: iso(start + span * b),
    records: Math.round(span / MIN * (b - a) * 1.6),
    human_inputs: null,
  });
  return { ...base, segments: [cut(0, 0.24), cut(0.34, 0.7), cut(0.82, 1)], inputs: { state: 'undeterminable', items: [], truncated: false } };
};

/** 패스 A 픽스처 하나의 캘린더 세션. 혼합 신원(J)처럼 분석이 없는 세션은 null이다. */
const calendarOfFixture = (f: SessionFixture): CalendarSession | null => {
  const a = f.analysis;
  if (a === null) return null;
  const meta = metaOf(f.id);
  const row = f.row;
  const header = a.header;
  const headerProject = header?.project;
  const project = headerProject?.hash
    ? { hash: headerProject.hash, name: headerProject.name ?? headerProject.hash }
    : row.project_hash
      ? { hash: row.project_hash, name: row.project_name ?? row.project_hash }
      : null;
  const base: CalendarSession = {
    session_id: f.id,
    title: titleOf(a),
    colors: {
      project,
      agent: header?.agent ?? row.agent,
      model: header?.model ?? (row.model || null),
      profile: meta.profile,
      components: meta.components,
      screens: meta.screens,
      kinds: meta.kinds,
      status: meta.status,
    },
    requests: isFullAnalysis(a) ? a.header.counts.requests : null,
    cost: { usd: header?.cost?.usd ?? null, source: header?.cost?.source ?? 'none' },
    facts: a.state,
    range: { start: header?.started_at ?? row.start_time, end: header?.last_activity_at ?? row.end_time },
    segments: [],
    inputs: { state: 'undeterminable', items: [], truncated: false },
  };
  if (!isFullAnalysis(a) || !hasSegments(a.state)) return base;
  if (f.id === P_ID) return pCalendar(f, base);
  if (a.header.counts.turn_boundaries.state === 'undeterminable') return redactedCalendar(base);

  const turns = [...a.turns, ...a.abandoned];
  const items: CalendarInput[] = [];
  const spans: Span[] = turns.map((t) => ({
    start: Date.parse(t.started_at),
    end: Date.parse(t.ended_at),
    records: 2 * t.api_requests + t.tools.calls,
  }));
  for (const t of turns) {
    if (!t.is_request || t.opener_copied) continue;
    items.push({ ts: t.started_at, turn_key: t.key, preview: { text: t.preview.state === 'ok' ? t.preview.text : null, state: t.preview.state } });
    // 작업 중 입력은 그 턴이 진행되는 동안 고르게 들어온 것으로 합성한다.
    const from = Date.parse(t.started_at);
    const to = Date.parse(t.ended_at);
    for (let i = 0; i < t.extra_inputs; i++) {
      items.push({
        ts: iso(from + ((i + 1) / (t.extra_inputs + 1)) * Math.max(to - from, 60_000)),
        turn_key: t.key,
        preview: { text: '(작업 중 입력)', state: 'ok' },
      });
    }
  }
  items.sort((x, y) => Date.parse(x.ts) - Date.parse(y.ts));
  return {
    ...base,
    segments: segmentsOf(spans, items.map((i) => Date.parse(i.ts))),
    inputs: { state: 'observed', items, truncated: false },
  };
};

export { META, metaOf, mergeSpans, calendarOfFixture };
