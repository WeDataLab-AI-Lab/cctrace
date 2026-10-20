/**
 * 응답(`SessionCalendar`)의 문자열 시각을 한 번만 ms 로 바꿔 둔다. 확대·축소와 호버는 같은 데이터를 여러 번
 * 그리므로, 그때마다 `Date.parse`를 하지 않게 하려는 것이다. 읽을 수 없는 시각은 조용히 버리지 않고 걸러서 센다
 * (`skipped`) — 화면이 NaN 좌표를 그리지 않게 하면서도 서버 오류를 숨기지 않기 위해서다.
 */

import { CALENDAR_MAX_INPUTS, hasSegments, type CalendarInput, type CalendarSession } from '@/lib/session-calendar-types';

export interface PreparedSegment {
  index: number;
  start: number;
  end: number;
  records: number | null;
  humanInputs: number | null;
}

export interface PreparedInput {
  at: number;
  turnKey: string;
  preview: CalendarInput['preview'];
}

export interface PreparedSession {
  id: string;
  source: CalendarSession;
  /** 조각이 없어 범위만 흐리게 그리는 세션. */
  faint: boolean;
  rangeStart: number;
  rangeEnd: number;
  /** 시작 순. 읽을 수 없는 조각은 뺐다. */
  segments: PreparedSegment[];
  /** 시각 순. */
  inputs: PreparedInput[];
  /** `inputs[i].at`만 모은 배열(이분 탐색용). 호버마다 만들지 않으려고 한 번만 만든다. */
  inputTimes: number[];
  /** 읽을 수 없어 걸러 낸 조각·입력의 수. 0이 아니면 서버 응답이 계약을 어긴 것이다. */
  skipped: number;
  /** 입력 시각이 모호하거나 앞부분만 온 응답이다. */
  inputIncomplete: boolean;
  /** 응답이 세션별 상한을 어겨 앞의 입력만 렌더한 경우. */
  inputOverflow: boolean;
}

const finite = (n: number): boolean => Number.isFinite(n);
const nonnegative = (value: unknown): value is number => typeof value === 'number' && finite(value) && value >= 0;
const count = (value: unknown): value is number => nonnegative(value) && Number.isSafeInteger(value);
const record = (value: unknown): Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {};
const text = (value: unknown): string | null => typeof value === 'string' ? value : null;
const strings = (value: unknown): string[] => Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [];

const prepareSession = (session: CalendarSession): PreparedSession => {
  let skipped = 0;
  const raw = record(session);
  const rawColors = record(raw.colors);
  const rawTitle = record(raw.title);
  const rawCost = record(raw.cost);
  const rawRange = record(raw.range);
  const rawInputGroup = record(raw.inputs);
  const rawSegments: unknown[] = Array.isArray(raw.segments) ? raw.segments : [];
  const rawInputs: unknown[] = Array.isArray(rawInputGroup.items) ? rawInputGroup.items : [];
  for (const value of [raw.title, raw.colors, raw.cost, raw.range]) {
    if (value === null || value === undefined || Array.isArray(value) || typeof value !== 'object') skipped += 1;
  }
  if (!Array.isArray(raw.segments)) skipped += 1;
  if (!Array.isArray(rawInputGroup.items)) skipped += 1;
  const colorFields = ['components', 'screens', 'kinds'] as const;
  for (const field of colorFields) {
    if (!Array.isArray(rawColors[field])) skipped += 1;
    else skipped += rawColors[field].filter((value: unknown) => typeof value !== 'string').length;
  }
  if (rawColors.project !== null && rawColors.project !== undefined && typeof record(rawColors.project).hash !== 'string') skipped += 1;
  if (rawColors.status !== null && rawColors.status !== undefined && !['done', 'leftover', 'midway', 'running', 'unknown'].includes(String(rawColors.status))) skipped += 1;
  for (const field of ['agent', 'model', 'profile'] as const) {
    if (rawColors[field] !== null && rawColors[field] !== undefined && typeof rawColors[field] !== 'string') skipped += 1;
  }
  const inputState = rawInputGroup.state === 'observed' ? 'observed' : 'undeterminable';
  if (rawInputGroup.state !== 'observed' && rawInputGroup.state !== 'undeterminable') skipped += 1;
  const overflow = rawInputs.length > CALENDAR_MAX_INPUTS;
  const truncated = rawInputGroup.truncated === true || overflow;
  if (rawInputGroup.truncated !== true && rawInputGroup.truncated !== false) skipped += 1;
  const validTitleState = ['ok', 'withheld', 'redacted', 'none'].includes(String(rawTitle.state));
  const validCostSource = ['otel', 'otel_partial', 'estimate', 'estimate_pending', 'none'].includes(String(rawCost.source));
  if (raw.title && !validTitleState) skipped += 1;
  if (raw.title && rawTitle.text !== null && rawTitle.text !== undefined && typeof rawTitle.text !== 'string') skipped += 1;
  if (raw.cost && !validCostSource) skipped += 1;
  // `AnalysisProject.name` is null when the project's name is unknown; only another type is a violation.
  if (raw.colors && rawColors.project !== null && rawColors.project !== undefined && record(rawColors.project).name !== null && typeof record(rawColors.project).name !== 'string') skipped += 1;
  if (raw.facts !== undefined && !['ready', 'stale', 'building', 'none', 'truncated', 'unsupported'].includes(String(raw.facts))) skipped += 1;
  if (typeof raw.session_id !== 'string') skipped += 1;
  if (rawTitle.state === 'ok' && text(rawTitle.text) === null) skipped += 1;
  if (raw.title && !['custom', 'ai', 'agent_name', 'first_request', 'none'].includes(String(rawTitle.source))) skipped += 1;
  if (rawCost.usd !== null && rawCost.usd !== undefined && !nonnegative(rawCost.usd)) skipped += 1;
  if (raw.requests !== null && raw.requests !== undefined && !count(raw.requests)) skipped += 1;
  const normalized: CalendarSession = {
    session_id: text(raw.session_id) ?? '',
    title: { text: text(rawTitle.text), state: validTitleState ? rawTitle.state as CalendarSession['title']['state'] : 'none', source: ['custom', 'ai', 'agent_name', 'first_request', 'none'].includes(String(rawTitle.source)) ? rawTitle.source as CalendarSession['title']['source'] : 'none' },
    colors: {
      project: typeof record(rawColors.project).hash === 'string' ? { hash: record(rawColors.project).hash as string, name: text(record(rawColors.project).name) ?? '' } : null,
      agent: text(rawColors.agent) ?? '', model: text(rawColors.model), profile: text(rawColors.profile),
      components: strings(rawColors.components), screens: strings(rawColors.screens), kinds: strings(rawColors.kinds),
      ...(rawColors.status === undefined ? {} : { status: ['done', 'leftover', 'midway', 'running', 'unknown'].includes(String(rawColors.status)) ? rawColors.status as CalendarSession['colors']['status'] : null }),
    },
    requests: count(raw.requests) ? raw.requests : null,
    cost: { usd: nonnegative(rawCost.usd) ? rawCost.usd : null, source: validCostSource ? rawCost.source as CalendarSession['cost']['source'] : 'none' },
    facts: ['ready', 'stale', 'building', 'none', 'truncated', 'unsupported'].includes(String(raw.facts)) ? raw.facts as CalendarSession['facts'] : 'none',
    range: { start: text(rawRange.start) ?? '', end: text(rawRange.end) ?? '' },
    segments: [],
    inputs: { state: inputState, items: [], truncated },
  };
  const segments: PreparedSegment[] = [];
  rawSegments.forEach((value, index) => {
    const seg = record(value);
    const start = typeof seg.start === 'string' ? Date.parse(seg.start) : NaN;
    const end = typeof seg.end === 'string' ? Date.parse(seg.end) : NaN;
    if (!finite(start) || !finite(end) || end < start) {
      skipped += 1;
      return;
    }
    const records = count(seg.records) ? seg.records : null;
    const humanInputs = count(seg.human_inputs) ? seg.human_inputs : null;
    if (records === null || (seg.human_inputs !== null && humanInputs === null)) skipped += 1;
    segments.push({ index, start, end, records, humanInputs });
    normalized.segments.push({ start: String(seg.start), end: String(seg.end), records: records ?? 0, human_inputs: humanInputs });
  });
  segments.sort((a, b) => a.start - b.start || a.end - b.end);

  const inputs: PreparedInput[] = [];
  for (const value of rawInputs) {
    const item = record(value);
    const at = typeof item.ts === 'string' ? Date.parse(item.ts) : NaN;
    if (!finite(at)) {
      skipped += 1;
      continue;
    }
    const previewValue = record(item.preview);
    const state = previewValue.state;
    const preview: CalendarInput['preview'] = state === 'ok' && typeof previewValue.text === 'string'
      ? { text: previewValue.text, state: 'ok' }
      : { text: null, state: state === 'redacted' || state === 'withheld' ? state : 'none' };
    if (item.preview === null || item.preview === undefined || (state === 'ok' && preview.state !== 'ok') ||
      !['ok', 'redacted', 'withheld', 'none'].includes(String(state))) skipped += 1;
    if (typeof item.turn_key !== 'string') skipped += 1;
    inputs.push({ at, turnKey: text(item.turn_key) ?? '', preview });
    normalized.inputs.items.push({ ts: String(item.ts), turn_key: text(item.turn_key) ?? '', preview });
  }
  inputs.sort((a, b) => a.at - b.at);
  normalized.inputs.items.sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts));
  if (inputs.length > CALENDAR_MAX_INPUTS) inputs.length = CALENDAR_MAX_INPUTS;
  if (normalized.inputs.items.length > CALENDAR_MAX_INPUTS) normalized.inputs.items.length = CALENDAR_MAX_INPUTS;

  const rangeStart = Date.parse(normalized.range.start);
  const rangeEnd = Date.parse(normalized.range.end);
  // A ready/stale session with no segment in the window is a range-only candidate (Codex full
  // facts without activity segments); it is drawn faint, never as activity.
  const faint = !hasSegments(normalized.facts) || !Array.isArray(raw.segments) || segments.length === 0;
  const rangeOk = finite(rangeStart) && finite(rangeEnd) && rangeEnd >= rangeStart;
  if (!rangeOk) skipped += 1;
  if (!rangeOk && segments.length > 0) {
    normalized.range.start = new Date(segments[0].start).toISOString();
    normalized.range.end = new Date(segments.reduce((last, segment) => Math.max(last, segment.end), 0)).toISOString();
  }
  return {
    id: normalized.session_id,
    source: normalized,
    faint,
    rangeStart: rangeOk ? rangeStart : (segments[0]?.start ?? 0),
    rangeEnd: rangeOk ? rangeEnd : segments.reduce((last, segment) => Math.max(last, segment.end), 0),
    segments,
    inputs,
    inputTimes: inputs.map((i) => i.at),
    skipped,
    inputIncomplete: inputState !== 'observed' || truncated || !Array.isArray(rawInputGroup.items),
    inputOverflow: overflow,
  };
};

const prepareSessions = (sessions: readonly CalendarSession[]): PreparedSession[] =>
  (Array.isArray(sessions) ? sessions : []).filter((session): session is CalendarSession => session !== null && typeof session === 'object' && !Array.isArray(session)).map(prepareSession);

export { prepareSession, prepareSessions };
