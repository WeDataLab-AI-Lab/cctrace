/**
 * 타임라인 칸(E1~E7)의 순수 로직: 두 토글의 거름, 입력 사이 이동, 턴의 첫 메시지, 메시지 머리 줄의 글자.
 *
 * 입력(`isInput`)은 사용자가 보낸 번호 있는 메시지다. 어떤 거름에서도 입력은 사라지지 않는다 — 이동 버튼의 기준이
 * 입력 번호이므로, 입력이 숨겨지면 "다음 입력"이 화면에 없는 곳으로 가 버린다.
 */

import type { InputKind, TimelineMessage } from '@/lib/session-analysis-types';
import { formatFullDateTime, formatRelative } from '@/components/sessions/analysis/format';
import { UNKNOWN_TEXT } from '@/components/sessions/analysis/labels';

/* ---------------- 거름 (E3) ---------------- */

interface TimelineFilters {
  /** 사용자 입력만 보기. 켜면 assistant와 system 메시지가 숨는다(답하지 않은 질문 카드 포함). */
  userOnly: boolean;
  /** 중간 출력 숨기기. 도구 호출 사이의 assistant 출력(`intermediate`)만 숨긴다. */
  hideIntermediate: boolean;
}

/** 레퍼런스의 첫 상태: 사용자 입력만 보기가 켜져 있고 중간 출력 숨기기는 꺼져 있다. */
const DEFAULT_TIMELINE_FILTERS: TimelineFilters = { userOnly: true, hideIntermediate: false };

type InputMessage = TimelineMessage & { n: number };

/** 번호가 있는 사용자 메시지. 번호가 NaN이면 입력으로 세지 않는다(정렬과 이동이 깨진다). */
const isInput = (m: TimelineMessage): m is InputMessage => m.role === 'user' && typeof m.n === 'number' && Number.isFinite(m.n);

/**
 * 두 토글을 적용한 목록. 전체 입력 번호와 이동은 원본 messages에서 계산한다. 사용자 입력만 보기는 직접 입력·질문 답만 남긴다.
 * system 메시지와 입력 출처가 직접 사람이 아님을 보이는 user 줄은 해당 필터에서 숨긴다.
 */
const filterMessages = (messages: readonly TimelineMessage[], filters: TimelineFilters): TimelineMessage[] =>
  messages.filter((m) => {
    if (m.role === 'user') return !filters.userOnly || (m.source !== undefined && HUMAN_SOURCES.has(m.source));
    if (filters.userOnly) return false;
    if (filters.hideIntermediate && m.role === 'assistant' && m.intermediate === true) return false;
    return true;
  });

/* ---------------- 입력 (E1, E3, E4) ---------------- */

/** 입력만 번호 순으로. 입력 배열을 바꾸지 않는다. */
const inputsOf = (messages: readonly TimelineMessage[]): InputMessage[] => messages.filter(isInput).sort((a, b) => a.n - b.n);

/** "입력 N건"의 N. 입력이 실어 온 전체 수(`of`)의 최댓값이고, 아무도 `of`를 싣지 않았으면 입력 개수다. */
const inputTotal = (messages: readonly TimelineMessage[]): number => {
  const inputs = inputsOf(messages);
  let max = 0;
  for (const m of inputs) {
    if (typeof m.of === 'number' && Number.isFinite(m.of) && m.of > max) max = m.of;
  }
  return max > 0 ? max : inputs.length;
};

/**
 * 이전·다음 입력의 번호. 그 방향에 입력이 없으면 null이다.
 *   - `currentN`이 없는 번호여도 된다: 번호가 비어 있으면(1, 2, 4) 3에서 다음은 4, 이전은 2다.
 *   - `currentN`이 null(위치를 모름)이면 다음은 첫 입력이고, 이전은 null이다(기준이 없으니 거꾸로 갈 곳이 없다).
 */
const stepInput = (messages: readonly TimelineMessage[], currentN: number | null, dir: 1 | -1): number | null => {
  const ns = inputsOf(messages).map((m) => m.n);
  if (ns.length === 0) return null;
  if (currentN === null) return dir === 1 ? ns[0] : null;
  if (dir === 1) return ns.find((n) => n > currentN) ?? null;
  for (let i = ns.length - 1; i >= 0; i -= 1) {
    if (ns[i] < currentN) return ns[i];
  }
  return null;
};

/**
 * 그 턴의 첫 메시지. 사용자 입력이 있으면 번호가 가장 작은 입력이고, 없으면 배열에서 처음 나오는 메시지다.
 * 상세에서 턴이나 작업 항목을 누르면 타임라인이 여기로 옮긴다. 그 턴의 메시지가 없으면 null.
 */
const firstMessageOfTurn = (messages: readonly TimelineMessage[], turnKey: string): TimelineMessage | null => {
  const inTurn = messages.filter((m) => m.turn_key === turnKey);
  if (inTurn.length === 0) return null;
  return inputsOf(inTurn)[0] ?? inTurn[0];
};

/**
 * 입력 번호의 일관성 검사. 화면은 이 검사를 하지 않는다 — 픽스처와 서버 응답을 검증하는 쪽이 쓴다.
 * 입력이 없으면 정상(빈 배열)이다. 규칙: 모든 입력에 `of`가 있고 하나로 같다 / 번호가 겹치지 않는다 /
 * 번호가 1부터 전체 수까지 빠짐없이 이어진다(입력 수가 `of`와 같다).
 */
const inputNumberingIssues = (messages: readonly TimelineMessage[]): string[] => {
  const inputs = inputsOf(messages);
  if (inputs.length === 0) return [];
  const issues: string[] = [];
  for (const m of inputs) {
    if (typeof m.of !== 'number') issues.push(`입력 ${m.n}에 of 없음`);
  }
  const ofs = [...new Set(inputs.flatMap((m) => (typeof m.of === 'number' ? [m.of] : [])))].sort((a, b) => a - b);
  if (ofs.length > 1) issues.push(`of 불일치: ${ofs.join(', ')}`);
  const ns = inputs.map((m) => m.n);
  for (const n of new Set(ns.filter((v, i) => ns.indexOf(v) !== i))) issues.push(`번호 ${n} 중복`);
  // 기대하는 전체 수: `of`가 하나로 정해졌으면 그 값, 아니면 가장 큰 번호.
  const total = ofs.length === 1 ? ofs[0] : Math.max(...ns);
  if (ofs.length === 1 && inputs.length !== total) issues.push(`입력 ${inputs.length}개인데 of는 ${total}`);
  const missing: number[] = [];
  for (let k = 1; k <= total; k += 1) {
    if (!ns.includes(k)) missing.push(k);
  }
  if (missing.length > 0) {
    const shown = missing.slice(0, 8).join(', ');
    issues.push(`번호 ${shown}${missing.length > 8 ? ` 외 ${missing.length - 8}개` : ''} 누락`);
  }
  for (const n of new Set(ns)) {
    if (n < 1 || n > total) issues.push(`번호 ${n} 범위 밖(1~${total})`);
  }
  return issues;
};

/* ---------------- 머리 줄 (E4, E7) ---------------- */

/** 입력 출처의 화면 이름. `typed`는 적지 않는다(사용자가 직접 친 것이 기본이다). */
const SOURCE_LABEL: Record<Exclude<InputKind, 'typed'>, string> = {
  answer: '질문에 답함',
  queued: '작업 중 입력',
  command: '명령',
  notification: '백그라운드 알림',
  peer: '다른 세션',
  channel: '외부 채널',
  auto: '자동 이어가기',
  scheduled: '예약 실행',
  sdk: 'SDK 호출',
  system: '시스템',
  unknown: '출처 모름',
};

/** 사람이 직접 보낸 입력의 출처. 명령·외부·백그라운드·미상 출처에는 "나"를 붙이지 않는다. */
const HUMAN_SOURCES: ReadonlySet<InputKind> = new Set<InputKind>(['typed', 'queued', 'answer']);

const sourceLabel = (source: string | undefined): string | null => {
  if (source === undefined || source === 'typed') return null;
  // 서버가 이 화면보다 새 값을 낼 수 있다. 빈 글자로 두지 않고 받은 값을 그대로 보인다.
  return (SOURCE_LABEL as Record<string, string | undefined>)[source] ?? source;
};

/** "YYYY/MM/DD HH:MM:SS"(선택한 시간대, 없으면 브라우저). 읽을 수 없는 시각은 null. */
const formatFullTime = (iso: string, timeZone?: string): string | null => {
  const formatted = formatFullDateTime(iso, timeZone);
  return formatted === UNKNOWN_TEXT ? null : formatted;
};

interface MessageMeta {
  /** "2026/10/03 11:56:22". 읽을 수 없으면 "알 수 없음". */
  time: string;
  /** "5시간 전". 시각을 읽을 수 없으면 빈 글자라 화면이 괄호째 뺀다. */
  relative: string;
  /** "1/15". 번호와 전체 수가 모두 있을 때만, 아니면 null. */
  position: string | null;
  /** 출처 이름. `typed`와 출처가 없는 메시지는 null. */
  source: string | null;
  /** "나"(사람이 보낸 입력), "Claude"(assistant), 그 밖은 빈 글자. */
  actor: string;
}

const messageMeta = (message: TimelineMessage, now: number, timeZone?: string): MessageMeta => {
  const time = formatFullTime(message.ts, timeZone);
  const position = typeof message.n === 'number' && typeof message.of === 'number' ? `${message.n}/${message.of}` : null;
  let actor = '';
  if (message.role === 'assistant') actor = 'Claude';
  else if (message.role === 'user' && message.source !== undefined && HUMAN_SOURCES.has(message.source)) actor = '나';
  return {
    time: time ?? UNKNOWN_TEXT,
    relative: time === null ? '' : formatRelative(message.ts, now),
    position,
    source: sourceLabel(message.source),
    actor,
  };
};

export {
  DEFAULT_TIMELINE_FILTERS,
  SOURCE_LABEL,
  isInput,
  filterMessages,
  inputsOf,
  inputTotal,
  stepInput,
  firstMessageOfTurn,
  inputNumberingIssues,
  messageMeta,
  formatFullTime,
};
export type { TimelineFilters, InputMessage, MessageMeta };
