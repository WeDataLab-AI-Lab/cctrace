/**
 * 요청 목록(D16)의 순수 로직: 순서, 버려진 분기 분리, 합계, 한 줄 표시용 값.
 *
 * 서버는 활성 턴(`turns`)과 버려진 턴(`abandoned`)을 따로 준다. 이 파일은 둘을 각자의 순서로 놓고,
 * 혹시 한쪽에 다른 분기의 턴이 섞여 와도 `branch`로 다시 가른다. 버려진 턴은 요청 수와 컨텍스트에서는
 * 빠지지만 비용·토큰에는 남는다(계획 §4.3) — 그래서 합계는 두 갈래를 따로 낸다.
 */

import type { AnalysisTurn, PreviewState, TurnEdited, TurnPreview } from '@/lib/session-analysis-types';
import {
  contextView,
  formatClock,
  formatDateTime,
  formatFullDateTime,
  formatTokenCount,
  formatUsdCompact,
  splitPath,
  tokenView,
  turnCostView,
} from './format';
import { inputKindLabel, QUESTION_STATUS_LABEL, TURN_STATUS_HINT, TURN_STATUS_REASON_TEXT, UNKNOWN_TEXT } from './labels';

interface OrderedTurns {
  /** 활성 경로의 턴. 시작 순번 순. */
  active: AnalysisTurn[];
  /** 되감기로 버려진 분기의 턴. 시작 순번 순. */
  abandoned: AnalysisTurn[];
}

const startedMs = (t: AnalysisTurn): number => {
  const ms = Date.parse(t.started_at);
  return Number.isNaN(ms) ? 0 : ms;
};

/** 시작 순번, 같으면 시작 시각, 그래도 같으면 키. 입력 순서와 무관하게 같은 결과가 나온다. */
const byOrder = (a: AnalysisTurn, b: AnalysisTurn): number => {
  if (a.no !== b.no) return a.no - b.no;
  const dt = startedMs(a) - startedMs(b);
  if (dt !== 0) return dt;
  return a.key < b.key ? -1 : a.key > b.key ? 1 : 0;
};

const orderTurns = (turns: readonly AnalysisTurn[], abandoned: readonly AnalysisTurn[]): OrderedTurns => {
  const seen = new Set<string>();
  const all: AnalysisTurn[] = [];
  for (const t of [...turns, ...abandoned]) {
    if (seen.has(t.key)) continue;
    seen.add(t.key);
    all.push(t);
  }
  return {
    active: all.filter((t) => t.branch !== 'abandoned').sort(byOrder),
    abandoned: all.filter((t) => t.branch === 'abandoned').sort(byOrder),
  };
};

interface TurnTotals {
  turns: number;
  requests: number;
  /** 비용을 아는 턴의 합. 모르는 턴은 더하지 않고 `unknownCost`로 센다. */
  usd: number;
  /** 비용을 아는 턴 수. 0이면 `usd`의 0은 "합이 0"이 아니라 "아는 값이 없음"이다. */
  knownCost: number;
  /** 비용을 알 수 없는 턴 수(만료, 요율 없음, 귀속 불가). API 호출이 없는 턴은 세지 않는다. */
  unknownCost: number;
  /** 네 칸(입력·출력·캐시 읽기·캐시 쓰기) 합. */
  tokens: number;
}

const VALUELESS = new Set(['expired', 'no_rate', 'unattributable']);

const turnTotals = (turns: readonly AnalysisTurn[]): TurnTotals => {
  let usd = 0;
  let knownCost = 0;
  let unknownCost = 0;
  let tokens = 0;
  let requests = 0;
  for (const t of turns) {
    if (t.is_request) requests += 1;
    tokens += tokenView(t.tokens).total ?? 0;
    if (t.cost.source === 'none') continue;
    const valueless = VALUELESS.has(t.cost.source) || t.cost.source === 'estimate_pending' && t.cost.usd === 0 ||
      typeof t.cost.usd !== 'number' || !Number.isFinite(t.cost.usd);
    if (valueless) {
      unknownCost += 1;
    } else {
      knownCost += 1;
      usd += t.cost.usd ?? 0;
    }
  }
  return { turns: turns.length, requests, usd, knownCost, unknownCost, tokens };
};

interface UnknownReason {
  key: string;
  /** "턴 상태 판단 불가", "비용 만료" 같은 말머리. */
  label: string;
  /** 이 이유로 값을 모르는 턴 수(받은 턴 기준). */
  turns: number;
  /** 왜 모르는지 풀어 쓴 문장. */
  text: string;
}

/**
 * 값을 모르는 턴을 이유별로 모은다. 행의 "귀속 불가", "판단 불가" 칸은 이유를 툴팁(title)으로만 갖는데, 툴팁은 마우스로만
 * 열려서 키보드와 터치로는 이유에 닿지 못한다. 목록 아래에 이 모음을 글자로 보여 준다. 많이 나온 이유가 먼저다.
 */
const unknownReasons = (turns: readonly AnalysisTurn[]): UnknownReason[] => {
  const groups = new Map<string, UnknownReason>();
  const add = (key: string, label: string, text: string) => {
    const group = groups.get(key);
    if (group) group.turns += 1;
    else groups.set(key, { key, label, turns: 1, text });
  };
  for (const t of turns) {
    if (t.status === 'undeterminable') {
      const text = (t.status_reason ? TURN_STATUS_REASON_TEXT[t.status_reason] : undefined) ?? TURN_STATUS_HINT.undeterminable;
      add(`status:${t.status_reason ?? ''}`, '턴 상태 판단 불가', text);
    }
    const cost = turnCostView(t.cost);
    if (cost.kind === 'unknown') add(`cost:${t.cost.source}:${t.cost.reason ?? ''}`, `비용 ${cost.sourceLabel}`, cost.hint);
  }
  return [...groups.values()].sort((a, b) => b.turns - a.turns || (a.key < b.key ? -1 : 1));
};

/** 번호 칸. 요청이면 "#3", 아니면 점. */
const turnNumberLabel = (turn: AnalysisTurn): string => (turn.request_no != null ? `#${turn.request_no}` : '·');

/**
 * 번호 옆에 붙는 출처 라벨. 요청 아닌 턴은 무엇인지 늘 적는다(알림, 명령, 다른 세션, ...). 요청인 턴은
 * 사람이 직접 친 것(typed)이 기본이라 적지 않고, 작업 중 입력이나 명령처럼 알아야 할 때만 적는다.
 */
const turnKindBadge = (turn: AnalysisTurn): string | null => {
  if (!turn.is_request) return inputKindLabel(turn.kind);
  return turn.kind === 'typed' || turn.kind === 'unknown' ? null : inputKindLabel(turn.kind);
};

const PREVIEW_PLACEHOLDER: Record<Exclude<PreviewState, 'ok'>, string> = {
  redacted: '첫 줄이 가려졌습니다 (비식별화)',
  withheld: '첫 줄을 서버에 올리지 않았습니다 (보류됨)',
  none: '첫 줄 없음',
};

interface PreviewView {
  kind: 'text' | 'placeholder';
  text: string;
}

/** 첫 줄. 가려졌거나 보류됐으면 무엇이 일어났는지를 적는다(빈칸이 아니다). */
const previewView = (preview: TurnPreview): PreviewView => {
  if (preview.state === 'ok') {
    const line = preview.text?.split('\n')[0]?.trim();
    if (line) return { kind: 'text', text: line };
    return { kind: 'placeholder', text: PREVIEW_PLACEHOLDER.none };
  }
  return { kind: 'placeholder', text: PREVIEW_PLACEHOLDER[preview.state] ?? PREVIEW_PLACEHOLDER.none };
};

/** 세션이 여러 날에 걸치면 시각에 날짜도 붙인다. */
const spansMultipleDays = (turns: readonly AnalysisTurn[], timeZone?: string): boolean => {
  const days = new Set<string>();
  for (const t of turns) {
    const d = new Date(t.started_at);
    if (Number.isNaN(d.getTime())) continue;
    days.add(timeZone ? formatFullDateTime(t.started_at, timeZone).slice(0, 10) : `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`);
    if (days.size > 1) return true;
  }
  return false;
};

const turnTimeLabel = (iso: string, multiDay: boolean, timeZone?: string, showOffset = true): string => {
  const label = multiDay ? formatDateTime(iso, timeZone) : formatClock(iso, timeZone);
  return timeZone && !showOffset ? label.replace(/ GMT[+-]\d{2}:\d{2}$/, '') : label;
};

/** 같은 현지 분이 DST 전환으로 두 오프셋에 나타날 때만 행의 오프셋을 남긴다. */
const repeatedTurnTimes = (turns: readonly AnalysisTurn[], timeZone?: string): ReadonlySet<string> => {
  const repeated = new Set<string>();
  if (!timeZone) return repeated;
  const wallTimes = new Map<string, Map<string, string[]>>();
  for (const turn of turns) {
    if (Number.isNaN(Date.parse(turn.started_at))) continue;
    const full = formatFullDateTime(turn.started_at, timeZone);
    const wallMinute = full.slice(0, 16);
    const offset = full.slice(20);
    const offsets = wallTimes.get(wallMinute) ?? new Map<string, string[]>();
    offsets.set(offset, [...(offsets.get(offset) ?? []), turn.started_at]);
    wallTimes.set(wallMinute, offsets);
  }
  for (const offsets of wallTimes.values()) {
    if (offsets.size > 1) {
      for (const instants of offsets.values()) {
        for (const instant of instants) repeated.add(instant);
      }
    }
  }
  return repeated;
};

interface EditedView {
  /** 활성 경로의 편집 이벤트 수. 고유 파일 수가 아니다. */
  count: number;
  /** 편집 이벤트의 경로 표본(폴더 뺀). 앞의 둘만. */
  files: string;
  /** 표본에 다 담지 못한 편집 이벤트 수. */
  more: number;
  abandoned: number;
}

/** 턴의 편집 이벤트 요약. 편집이 없으면 null. */
const editedView = (edited: TurnEdited): EditedView | null => {
  if (edited.count <= 0 && edited.abandoned <= 0) return null;
  const names = edited.sample.map((p) => splitPath(p).base).filter(Boolean);
  const shown = names.slice(0, 2);
  return {
    count: edited.count,
    files: shown.join(', '),
    more: Math.max(0, edited.count - shown.length),
    abandoned: edited.abandoned,
  };
};

/* ---------------- 패스 C: 턴 상세의 한 줄 ---------------- */

interface TurnMetricPart {
  key: 'tokens' | 'cost' | 'ctx';
  /** "6.2M tok", "$2.26", "ctx 18%". 모르면 "?"가 들어간 점선 조각이다. */
  text: string;
  unknown: boolean;
}

interface TurnMetrics {
  parts: TurnMetricPart[];
  /** 칩의 툴팁(행이 버튼이라 title 속성을 쓴다). 구성과 출처를 적는다. */
  title: string;
}

/**
 * 턴 줄 끝의 지표 칩: "6.2M tok · $2.26 · ctx 18%". 모르는 값은 0이 아니라 "?"다.
 * API 호출이 없는 턴(명령 등)은 세 값이 모두 의미가 없어 칩을 그리지 않는다.
 */
const turnMetrics = (turn: AnalysisTurn): TurnMetrics => {
  if (turn.api_requests === 0 && turn.cost.source === 'none') return { parts: [], title: 'API 호출이 없는 턴입니다.' };
  const tokens = tokenView(turn.tokens);
  const cost = turnCostView(turn.cost);
  const ctx = contextView(turn.context);
  const parts: TurnMetricPart[] = [];
  parts.push(
    tokens.total === null
      ? { key: 'tokens', text: '? tok', unknown: true }
      : { key: 'tokens', text: `${formatTokenCount(tokens.total)} tok`, unknown: false },
  );
  // API 호출이 있는 턴인데 비용 출처가 없으면(kind 'none') 비용이 없는 것이 아니라 모르는 것이다. 칩에서 조용히 빼지 않는다.
  if (cost.kind === 'amount') parts.push({ key: 'cost', text: formatUsdCompact(turn.cost.usd), unknown: false });
  else parts.push({ key: 'cost', text: '$?', unknown: true });
  parts.push(
    ctx.kind === 'percent'
      ? { key: 'ctx', text: `ctx ${ctx.percentLabel}`, unknown: false }
      : ctx.kind === 'tokens_only'
        ? { key: 'ctx', text: `ctx ${ctx.tokensLabel}`, unknown: false }
        : { key: 'ctx', text: 'ctx ?', unknown: true },
  );
  const tokenParts = tokens.parts.map((p) => `${p.label} ${p.value === null ? UNKNOWN_TEXT : p.value.toLocaleString('en-US')}`).join(' · ');
  const costText = cost.kind === 'amount' ? `${formatUsdCompact(turn.cost.usd)} (${cost.sourceLabel})` : `${cost.sourceLabel} — ${cost.hint}`;
  return { parts, title: `토큰: ${tokenParts}\n비용: ${costText}\n컨텍스트: ${ctx.summary}` };
};

interface TurnQuestionView {
  /** "질문: 표시 위치 / 데이터 출처". */
  text: string;
  /** 답 없음·거절됨처럼 알아야 하는 상태. 모두 답했으면 비어 있다. */
  flags: string[];
}

const turnQuestionView = (turn: AnalysisTurn): TurnQuestionView | null => {
  const asked = turn.question_headers.filter((q) => q.headers.length > 0);
  if (asked.length === 0) return null;
  const flags = [...new Set(asked.filter((q) => q.status !== 'answered').map((q) => QUESTION_STATUS_LABEL[q.status]))];
  return { text: `질문: ${asked.flatMap((q) => q.headers).join(' / ')}`, flags };
};

/** 커밋 줄에 이미 칩으로 나온 task를 뺀, 이 턴만의 task. */
const turnExtraTasks = (turn: AnalysisTurn): string[] => {
  const shown = new Set((turn.commits ?? []).flatMap((c) => c.tasks));
  return [...new Set(turn.tasks ?? [])].filter((id) => !shown.has(id));
};

export {
  orderTurns,
  turnTotals,
  unknownReasons,
  turnNumberLabel,
  turnKindBadge,
  previewView,
  spansMultipleDays,
  turnTimeLabel,
  repeatedTurnTimes,
  editedView,
  turnMetrics,
  turnQuestionView,
  turnExtraTasks,
};
export type { OrderedTurns, TurnTotals, UnknownReason, PreviewView, EditedView, TurnMetricPart, TurnMetrics, TurnQuestionView };
