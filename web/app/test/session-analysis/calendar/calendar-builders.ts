/**
 * 캘린더 시험과 픽스처를 짓는 도구. 합성 데이터 전용이다. 형 단언(`as`)을 쓰지 않는다 — 모든 값은
 * `lib/session-calendar-types`의 타입으로 검사된다.
 */

import type { AnalysisProject, AnalysisState, AnalysisTitle, PreviewState } from '@/lib/session-analysis-types';
import type {
  CalendarColors,
  CalendarCost,
  CalendarInput,
  CalendarInputs,
  CalendarSegment,
  CalendarSession,
} from '@/lib/session-calendar-types';

/** ISO 문자열이나 ms 를 UTC ISO 문자열로. */
export const iso = (t: string | number): string => new Date(t).toISOString();

export const calSeg = (
  start: string | number,
  end: string | number,
  o: { records?: number; human_inputs?: number | null } = {},
): CalendarSegment => ({
  start: iso(start),
  end: iso(end),
  records: o.records ?? 12,
  human_inputs: o.human_inputs === undefined ? 0 : o.human_inputs,
});

export const calInput = (
  ts: string | number,
  o: { turn_key?: string; text?: string | null; state?: PreviewState } = {},
): CalendarInput => {
  const state = o.state ?? (o.text === null ? 'none' : 'ok');
  return {
    ts: iso(ts),
    turn_key: o.turn_key ?? `t_${iso(ts)}`,
    preview: { text: state === 'ok' ? (o.text ?? `요청 ${iso(ts)}`) : null, state },
  };
};

export interface CalSessionInit {
  title?: Partial<AnalysisTitle>;
  colors?: Partial<CalendarColors>;
  requests?: number | null;
  cost?: Partial<CalendarCost>;
  facts?: AnalysisState;
  /** 세션 전체 범위. 안 주면 조각의 바깥 끝이다. */
  range?: [string | number, string | number];
  segments?: CalendarSegment[];
  inputs?: (string | number | CalendarInput)[];
  inputsState?: CalendarInputs['state'];
  inputsTruncated?: boolean;
}

/** 조각도 범위도 안 준 시험용 세션의 기준 순간. */
const DEFAULT_INSTANT = Date.UTC(2026, 9, 5, 3, 0, 0);
const DEFAULT_PROJECT: AnalysisProject = { hash: '-work-proj-a', name: 'proj-a' };

/** 시험용 세션. 안 적은 값은 "조용한" 기본값이다. */
export const calSession = (id: string, init: CalSessionInit = {}): CalendarSession => {
  const segments = init.segments ?? [];
  const starts = segments.map((s) => Date.parse(s.start));
  const ends = segments.map((s) => Date.parse(s.end));
  const range = init.range
    ? { start: iso(init.range[0]), end: iso(init.range[1]) }
    : segments.length > 0
      ? { start: iso(Math.min(...starts)), end: iso(Math.max(...ends)) }
      : { start: iso(DEFAULT_INSTANT), end: iso(DEFAULT_INSTANT) };
  const items = (init.inputs ?? []).map((i) => (typeof i === 'object' ? i : calInput(i)));
  return {
    session_id: id,
    title: { text: id, source: 'ai', state: 'ok', ...init.title },
    colors: {
      project: DEFAULT_PROJECT,
      agent: 'claude',
      model: 'claude-opus-5',
      profile: '메인',
      components: [],
      screens: [],
      kinds: [],
      status: 'leftover',
      ...init.colors,
    },
    requests: init.requests === undefined ? 3 : init.requests,
    cost: { usd: 1.25, source: 'otel', ...init.cost },
    facts: init.facts ?? 'ready',
    range,
    segments,
    inputs: { state: init.inputsState ?? 'observed', items, truncated: init.inputsTruncated ?? false },
  };
};
