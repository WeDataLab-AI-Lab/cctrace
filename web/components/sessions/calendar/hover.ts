/**
 * 호버 카드의 내용(C14, C16). 순수 함수라 "모르는 값은 0이 아니라 알 수 없음", "첫 줄이 가려졌으면 시각만" 같은 규칙을 시험으로 고정한다.
 *
 * 띠: 제목, 시간, 요청 수, 비용(C14). 입력 가로선: 그 요청의 첫 줄, 비식별화·보류면 시각만(C16).
 */

import { formatDuration, formatUsd, titleView } from '@/components/sessions/analysis/format';
import { COST_SOURCE_META, UNKNOWN_TEXT } from '@/components/sessions/analysis/labels';
import type { CalendarSession } from '@/lib/session-calendar-types';
import { STATUS_META } from './status';
import { formatClockIn, formatDateTimeIn, localDateOf, sameDate } from './time-zone';
import type { PreparedInput, PreparedSegment, PreparedSession } from './prepare';

export interface HoverStat {
  label: string;
  value: string;
  /** 값을 모른다(점선으로 그린다). */
  unknown: boolean;
}

export interface BandHover {
  kind: 'band';
  title: string;
  /** 제목이 없거나 가려져 자리표시 문구다. */
  titleMuted: boolean;
  shortId: string;
  /** "10/05 09:10 – 10:05 · 55분". */
  time: string;
  /** 조각이 없는 세션의 안내. */
  note: string | null;
  stats: HoverStat[];
  tags: { label: string; value: string }[];
  /** 세션 전체 범위. 조각이 여럿이거나 이 조각보다 넓을 때만. */
  whole: string | null;
  /** 자료가 누락되거나 상한으로 잘렸을 때의 안내. */
  warnings: string[];
}

export interface InputHover {
  kind: 'input';
  title: string;
  titleMuted: boolean;
  /** "10/05 09:30". */
  time: string;
  /** 요청의 첫 줄. 비식별화·보류·없음이면 null이다(시각만 보인다). */
  line: string | null;
  /** 첫 줄이 없는 이유(없으면 null). */
  note: string | null;
  /** 입력이 가리킨 세션 띠의 전체 정보. */
  band: BandHover;
}

const PREVIEW_NOTE = {
  redacted: '첫 줄은 비식별화로 가려졌습니다.',
  withheld: '첫 줄을 서버에 올리지 않았습니다(보류됨).',
  none: '첫 줄이 없는 입력입니다.',
} as const;

/** 제목 표기: 본문이 있으면 그것, 없으면 상태별 자리표시와 짧은 ID. */
const titleOf = (session: CalendarSession): { text: string; muted: boolean } => {
  const view = titleView(session.title ?? { text: null, source: 'none', state: 'none' });
  if (view.text) return { text: view.text, muted: false };
  // A missing computed title can use a collected request; redacted/withheld titles stay concealed.
  if (session.title?.state === 'none') {
    const request = session.inputs?.items?.find((input) => input.preview?.state === 'ok' && input.preview.text?.trim());
    const firstLine = request?.preview.text?.trim().split(/\r?\n/, 1)[0];
    if (firstLine) return { text: firstLine, muted: false };
  }
  return { text: view.placeholder ?? '제목 없음', muted: true };
};

/** 시작–끝을 그 시간대의 시각으로. 같은 날이면 끝은 시각만 적는다. */
const rangeText = (timeZone: string, start: number, end: number): string => {
  const sameDay = sameDate(localDateOf(timeZone, start), localDateOf(timeZone, end));
  const from = formatDateTimeIn(timeZone, start);
  const to = sameDay ? formatClockIn(timeZone, end) : formatDateTimeIn(timeZone, end);
  return `${from} – ${to}`;
};

const lengthText = (start: number, end: number): string => (end > start ? ` · ${formatDuration(Math.round((end - start) / 1000))}` : '');

const bandHover = (session: PreparedSession, segmentIndex: number, timeZone: string): BandHover => {
  const source = session.source;
  const { text, muted } = titleOf(source);
  const segment: PreparedSegment | undefined = session.segments.find((s) => s.index === segmentIndex);
  const faint = session.faint;
  const start = segment ? segment.start : session.rangeStart;
  const end = segment ? segment.end : session.rangeEnd;

  const requests: HoverStat = {
    label: '세션 전체 요청',
    value: typeof source.requests === 'number' && Number.isFinite(source.requests) ? source.requests.toLocaleString('en-US') : UNKNOWN_TEXT,
    unknown: typeof source.requests !== 'number' || !Number.isFinite(source.requests),
  };
  const pendingZeroCost = source.cost?.source === 'estimate_pending' && source.cost.usd === 0;
  const costKnown = typeof source.cost?.usd === 'number' && Number.isFinite(source.cost.usd) && source.cost.source !== 'none' && !pendingZeroCost;
  const cost: HoverStat = {
    label: '세션 전체 비용',
    value: costKnown ? `${formatUsd(source.cost.usd)} ${COST_SOURCE_META[source.cost.source]?.label ?? ''}`.trim()
      : pendingZeroCost ? `${UNKNOWN_TEXT} (${COST_SOURCE_META.estimate_pending.label})` : UNKNOWN_TEXT,
    unknown: !costKnown,
  };
  const stats: HoverStat[] = [requests, cost];
  if (segment) {
    stats.push({
      label: '선택 구간 입력',
      value: segment.humanInputs === null ? UNKNOWN_TEXT : segment.humanInputs.toLocaleString('en-US'),
      unknown: segment.humanInputs === null,
    });
  }

  const colors = source.colors;
  const tags: BandHover['tags'] = [];
  if (colors?.status) tags.push({ label: '완료 여부', value: STATUS_META[colors.status]?.label ?? STATUS_META.unknown.label });
  if (Array.isArray(colors?.components) && colors.components.length > 0) tags.push({ label: '컴포넌트', value: colors.components.join(', ') });
  if (Array.isArray(colors?.screens) && colors.screens.length > 0) tags.push({ label: '화면', value: colors.screens.join(', ') });
  if (Array.isArray(colors?.kinds) && colors.kinds.length > 0) tags.push({ label: '종별', value: colors.kinds.join(', ') });
  if (colors?.project) tags.push({ label: '프로젝트', value: colors.project.name || colors.project.hash || UNKNOWN_TEXT });
  if (colors?.profile) tags.push({ label: '프로필', value: colors.profile });

  const wholeStart = session.rangeStart;
  const wholeEnd = session.rangeEnd;
  const wider = wholeStart < start || wholeEnd > end;
  const whole =
    !faint && (session.segments.length > 1 || wider)
      ? `세션 전체 ${rangeText(timeZone, wholeStart, wholeEnd)} · 조각 ${session.segments.length}개`
      : null;
  const warnings: string[] = [];
  if (source.inputs?.truncated) warnings.push(session.inputOverflow
    ? '응답이 입력 시각 상한을 넘었습니다. 시각순으로 이른 입력 2,000건만 표시하고 나머지는 생략했습니다.'
    : '입력 시각은 상한까지만 표시했습니다. 뒤의 입력은 이 캘린더에 없습니다.');
  else if (session.inputIncomplete) warnings.push('입력 시각을 판별할 수 없습니다. 빈 입력선이 입력 0건을 뜻하지 않습니다.');
  if (session.skipped > 0) warnings.push(`읽을 수 없는 활동 자료 ${session.skipped}건을 표시하지 못했습니다.`);

  return {
    kind: 'band',
    title: text,
    titleMuted: muted,
    shortId: source.session_id?.slice(0, 8) ?? '',
    time: `${rangeText(timeZone, start, end)}${lengthText(start, end)}`,
    note: faint ? '활동 조각을 확인할 수 없어 세션 전체 범위만 흐리게 그립니다. 조각과 입력 시각은 알 수 없습니다.' : null,
    stats,
    tags,
    whole,
    warnings,
  };
};

const inputHover = (session: PreparedSession, input: PreparedInput, timeZone: string): InputHover => {
  const { text, muted } = titleOf(session.source);
  const state = input.preview.state;
  return {
    kind: 'input',
    title: text,
    titleMuted: muted,
    time: formatDateTimeIn(timeZone, input.at),
    line: state === 'ok' && input.preview.text ? input.preview.text : null,
    note: state === 'ok' ? (input.preview.text ? null : PREVIEW_NOTE.none) : (PREVIEW_NOTE[state] ?? PREVIEW_NOTE.none),
    band: bandHover(session, session.segments.find((s) => s.start <= input.at && (s.start === s.end ? input.at === s.end : input.at < s.end))?.index ?? -1, timeZone),
  };
};

/** 띠의 접근성 이름. 마우스 없이도 띠가 무엇인지 알 수 있어야 한다. */
const bandLabel = (session: PreparedSession, segmentIndex: number, timeZone: string): string => {
  const h = bandHover(session, segmentIndex, timeZone);
  const stats = h.stats.map((s) => `${s.label} ${s.value}`).join(', ');
  const status = session.source.colors?.status ? `, ${STATUS_META[session.source.colors.status]?.label ?? STATUS_META.unknown.label}` : '';
  const warnings = h.warnings.length > 0 ? `, ${h.warnings.join(' ')}` : '';
  return `${h.title}, ${h.time}, ${stats}${status}${warnings}`;
};

export { bandHover, inputHover, bandLabel, rangeText, titleOf };
