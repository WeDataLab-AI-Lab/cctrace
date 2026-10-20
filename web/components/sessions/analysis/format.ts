/**
 * 분석 화면의 순수 서식 함수.
 *
 * 원칙: 모르는 값(null)은 0이 아니다. 기존 `fmt`·`fmtCost`(session-utils)는 0과 null을 둘 다 '-'로
 * 접으므로, 이 파일의 함수가 먼저 둘을 가른 뒤에야 그 함수를 부른다. 양수의 모양(1.2K, $0.1234)은
 * 기존 목록과 같게 유지한다.
 */

import { fmt, fmtCost } from '@/components/sessions/session-utils';
import type {
  AnalysisCost,
  AnalysisCounts,
  AnalysisTitle,
  ContextSource,
  DeepPartial,
  TokenBreakdown,
  TurnCost,
} from '@/lib/session-analysis-types';
import {
  CONTEXT_SOURCE_META,
  COST_NOTE_TEXT,
  COST_SOURCE_META,
  LARGE_WINDOW_HINT,
  TITLE_SOURCE_LABEL,
  TURN_COST_REASON_TEXT,
  TURN_COST_SOURCE_META,
  UNKNOWN_TEXT,
} from './labels';

const isNum = (n: unknown): n is number => typeof n === 'number' && Number.isFinite(n);

/* ---------------- 숫자 ---------------- */

const countFormat = new Intl.NumberFormat('en-US');

/** 개수. 모르면 "알 수 없음", 0이면 "0". */
const formatCount = (n: number | null | undefined): string => (isNum(n) ? countFormat.format(n) : UNKNOWN_TEXT);

/** 토큰 수. 양수는 목록과 같은 축약(1.2K, 3.4M). */
const formatTokenCount = (n: number | null | undefined): string => {
  if (!isNum(n)) return UNKNOWN_TEXT;
  return n === 0 ? '0' : fmt(n);
};

/** 금액. 양수는 목록과 같은 `$0.1234`. */
const formatUsd = (n: number | null | undefined): string => {
  if (!isNum(n)) return UNKNOWN_TEXT;
  return n === 0 ? '$0' : fmtCost(n);
};

/* ---------------- 시각 ---------------- */

const pad2 = (n: number): string => String(n).padStart(2, '0');

const parseTime = (iso: string | null | undefined): Date | null => {
  if (!iso) return null;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? null : d;
};

interface ZonedTime {
  year: string;
  month: string;
  day: string;
  hour: string;
  minute: string;
  second: string;
  offset: string;
}

/** 명시된 시간대는 오프셋을 함께 적어 DST의 반복되는 시각도 구별한다. 잘못된 IANA 이름은 UTC로 대체한다. */
const zonedTime = (date: Date, timeZone: string): ZonedTime => {
  const options: Intl.DateTimeFormatOptions = {
    timeZone,
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
    hourCycle: 'h23', timeZoneName: 'longOffset',
  };
  let formatter: Intl.DateTimeFormat;
  try {
    formatter = new Intl.DateTimeFormat('en-US', options);
  } catch (error) {
    if (!(error instanceof RangeError)) throw error;
    formatter = new Intl.DateTimeFormat('en-US', { ...options, timeZone: 'UTC' });
  }
  const parts = Object.fromEntries(formatter.formatToParts(date).map((part) => [part.type, part.value]));
  return {
    year: parts.year, month: parts.month, day: parts.day,
    hour: parts.hour, minute: parts.minute, second: parts.second,
    offset: formatter.resolvedOptions().timeZone === 'UTC' ? 'UTC' : parts.timeZoneName,
  };
};

/** HH:MM. 시간대가 있으면 GMT 오프셋도 붙인다. */
const formatClock = (iso: string | null | undefined, timeZone?: string): string => {
  const d = parseTime(iso);
  if (!d) return UNKNOWN_TEXT;
  if (!timeZone) return `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
  const p = zonedTime(d, timeZone);
  return `${p.hour}:${p.minute} ${p.offset}`;
};

/** MM/DD HH:MM. 시간대가 있으면 GMT 오프셋도 붙인다. */
const formatDateTime = (iso: string | null | undefined, timeZone?: string): string => {
  const d = parseTime(iso);
  if (!d) return UNKNOWN_TEXT;
  if (!timeZone) return `${pad2(d.getMonth() + 1)}/${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
  const p = zonedTime(d, timeZone);
  return `${p.month}/${p.day} ${p.hour}:${p.minute} ${p.offset}`;
};

/** YYYY/MM/DD HH:MM:SS. 타임라인의 절대 시각. */
const formatFullDateTime = (iso: string | null | undefined, timeZone?: string): string => {
  const d = parseTime(iso);
  if (!d) return UNKNOWN_TEXT;
  if (!timeZone) return `${d.getFullYear()}/${pad2(d.getMonth() + 1)}/${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
  const p = zonedTime(d, timeZone);
  return `${p.year}/${p.month}/${p.day} ${p.hour}:${p.minute}:${p.second} ${p.offset}`;
};

const isSameLocalDay = (a: Date, b: Date): boolean =>
  a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();

/** 기준 시각과 같은 날이면 시각만, 아니면 날짜도 붙인다. 두 날짜 모두 선택한 시간대에서 비교한다. */
const formatClockNear = (iso: string | null | undefined, nowMs: number, timeZone?: string): string => {
  const d = parseTime(iso);
  if (!d) return UNKNOWN_TEXT;
  if (timeZone) {
    const first = zonedTime(d, timeZone);
    const today = zonedTime(new Date(nowMs), timeZone);
    return first.year === today.year && first.month === today.month && first.day === today.day
      ? `${first.hour}:${first.minute} ${first.offset}`
      : `${first.month}/${first.day} ${first.hour}:${first.minute} ${first.offset}`;
  }
  return isSameLocalDay(d, new Date(nowMs)) ? formatClock(iso) : formatDateTime(iso);
};

/** 걸린 시간. 초 → 분 → 시간 → 일 중 가장 큰 두 단위까지만. */
const formatDuration = (seconds: number | null | undefined): string => {
  if (!isNum(seconds) || seconds < 0) return UNKNOWN_TEXT;
  const s = Math.round(seconds);
  if (s < 60) return `${s}초`;
  const totalMinutes = Math.floor(s / 60);
  if (totalMinutes < 60) return `${totalMinutes}분`;
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  if (hours < 24) return minutes === 0 ? `${hours}시간` : `${hours}시간 ${minutes}분`;
  const days = Math.floor(hours / 24);
  const restHours = hours % 24;
  return restHours === 0 ? `${days}일` : `${days}일 ${restHours}시간`;
};

/** 기준 시각으로부터 얼마나 지났는가. 미래의 시계 어긋남은 "방금"으로 접는다. */
const formatRelative = (iso: string | null | undefined, nowMs: number): string => {
  const d = parseTime(iso);
  if (!d) return UNKNOWN_TEXT;
  const seconds = Math.round((nowMs - d.getTime()) / 1000);
  if (seconds < 45) return '방금';
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}분 전`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}시간 전`;
  return `${Math.round(hours / 24)}일 전`;
};

/* ---------------- 요청 · 입력 · 턴 수 ---------------- */

interface CountPart {
  label: '요청' | '입력' | '턴';
  value: number | null;
}

type CountsLike = Partial<Pick<AnalysisCounts, 'requests' | 'inputs' | 'turns'>> | undefined;

/** 세 수를 묶어 둔다. 턴 경계를 판단할 수 없는 세션은 셋 다 null이다. */
const countsParts = (counts: CountsLike): CountPart[] => [
  { label: '요청', value: isNum(counts?.requests) ? counts.requests : null },
  { label: '입력', value: isNum(counts?.inputs) ? counts.inputs : null },
  { label: '턴', value: isNum(counts?.turns) ? counts.turns : null },
];

/** "요청 N · 입력 M · 턴 K". 모르는 수는 "알 수 없음"으로 적는다(0이 아니다). */
const countsLine = (counts: CountsLike): string =>
  countsParts(counts)
    .map((p) => `${p.label} ${formatCount(p.value)}`)
    .join(' · ');

/* ---------------- 토큰 ---------------- */

interface TokenView {
  /** 네 칸을 모두 알 때만. 일부만 알면 null (일부 합을 총합처럼 보이지 않는다). */
  total: number | null;
  parts: { key: keyof TokenBreakdown; label: string; value: number | null }[];
}

const TOKEN_PART_LABEL: Record<keyof TokenBreakdown, string> = {
  input: '입력',
  output: '출력',
  cache_read: '캐시 읽기',
  cache_create: '캐시 쓰기',
};

const TOKEN_KEYS: (keyof TokenBreakdown)[] = ['input', 'output', 'cache_read', 'cache_create'];

const tokenView = (tokens: Partial<TokenBreakdown> | undefined): TokenView => {
  const parts = TOKEN_KEYS.map((key) => ({
    key,
    label: TOKEN_PART_LABEL[key],
    value: isNum(tokens?.[key]) ? (tokens[key] as number) : null,
  }));
  const known = parts.every((p) => p.value !== null);
  return { total: known ? parts.reduce((sum, p) => sum + (p.value ?? 0), 0) : null, parts };
};

/* ---------------- 비용 (§4.4) ---------------- */

type CostTone = 'measured' | 'estimated' | 'partial' | 'unknown';

interface CostView {
  /** 금액 문자열. 모르면 "알 수 없음". */
  amount: string;
  known: boolean;
  tone: CostTone;
  sourceLabel: string;
  sourceHint: string;
  /** `otel_expired` 같은 주석. */
  note: string | null;
  /** 턴에 붙은 몫과 붙지 않은 호출의 몫. 둘 다 모르면 null. */
  split: { attached: string; unattached: string } | null;
}

const COST_TONE: Record<AnalysisCost['source'], CostTone> = {
  otel: 'measured',
  otel_partial: 'partial',
  estimate: 'estimated',
  estimate_pending: 'partial',
  none: 'unknown',
};

const costView = (cost: DeepPartial<AnalysisCost> | undefined): CostView => {
  const source = cost?.source;
  const usd = isNum(cost?.usd) ? cost.usd : null;
  const meta = source ? COST_SOURCE_META[source] : undefined;
  const attached = isNum(cost?.turn_attached_usd) ? cost.turn_attached_usd : null;
  const unattached = isNum(cost?.unattached_turn_usd) ? cost.unattached_turn_usd : null;
  const note = cost?.note ? COST_NOTE_TEXT[cost.note] ?? null : null;
  // 출처가 `none`이면 붙은 OTEL도 추정 행도 없다. 서버가 0을 실어 보내도 "0달러"가 아니라 모르는 값이다.
  const pendingZero = source === 'estimate_pending' && usd === 0;
  const known = usd !== null && source !== 'none' && !pendingZero;
  return {
    amount: formatUsd(known ? usd : null),
    known,
    tone: !known ? 'unknown' : source ? COST_TONE[source] : 'unknown',
    sourceLabel: meta?.label ?? '',
    sourceHint: pendingZero ? '추정 비용이 아직 반영되지 않았습니다. 현재 0은 확인된 0달러가 아닙니다.' : meta?.hint ?? '비용의 출처를 알 수 없습니다.',
    note,
    split: pendingZero || attached === null && unattached === null ? null : { attached: formatUsd(attached), unattached: formatUsd(unattached) },
  };
};

type TurnCostKind = 'amount' | 'unknown' | 'none';

interface TurnCostView {
  kind: TurnCostKind;
  /** kind === 'amount' 일 때만 금액, 아니면 빈 문자열. */
  amount: string;
  sourceLabel: string;
  /** 툴팁. `unattributable`이면 이유를 풀어 쓴 문장이다. */
  hint: string;
  tone: CostTone;
}

/** 이 출처들은 값이 없다. 서버가 0을 실어 보내도 모르는 값으로 그린다. */
const VALUELESS_TURN_COST: ReadonlySet<TurnCost['source']> = new Set(['expired', 'no_rate', 'unattributable']);

const turnCostView = (cost: TurnCost): TurnCostView => {
  const meta = TURN_COST_SOURCE_META[cost.source];
  const label = meta?.label ?? cost.source;
  if (cost.source === 'none') {
    return { kind: 'none', amount: '', sourceLabel: label, hint: meta?.hint ?? '', tone: 'unknown' };
  }
  const reasonText = cost.reason ? TURN_COST_REASON_TEXT[cost.reason] : undefined;
  const pendingZero = cost.source === 'estimate_pending' && cost.usd === 0;
  if (VALUELESS_TURN_COST.has(cost.source) || pendingZero || !isNum(cost.usd)) {
    return {
      kind: 'unknown',
      amount: '',
      sourceLabel: label,
      hint: pendingZero ? '추정 비용이 아직 반영되지 않았습니다. 현재 0은 확인된 0달러가 아닙니다.' : reasonText ?? meta?.hint ?? '이 턴의 비용을 알 수 없습니다.',
      tone: 'unknown',
    };
  }
  return {
    kind: 'amount',
    amount: formatUsd(cost.usd),
    sourceLabel: label,
    hint: meta?.hint ?? '',
    tone: cost.source === 'otel' ? 'measured' : cost.source === 'estimate' ? 'estimated' : 'partial',
  };
};

/* ---------------- 컨텍스트 (§3.11) ---------------- */

interface ContextLike {
  tokens?: number | null;
  window?: number | null;
  percent?: number | null;
  source?: ContextSource;
  large_window_seen?: boolean;
}

type ContextKind = 'percent' | 'tokens_only' | 'unknown';

interface ContextView {
  kind: ContextKind;
  /** "42%". 퍼센트가 없으면 빈 문자열. */
  percentLabel: string;
  /** 막대 길이용 0~100. 퍼센트가 없으면 null. */
  percentValue: number | null;
  tokensLabel: string;
  windowLabel: string;
  sourceLabel: string;
  sourceHint: string;
  /** 퍼센트가 없을 때만 붙는 "큰 창을 쓴 흔적". */
  largeWindowHint: string | null;
  /** 접근성·툴팁용 한 줄 요약. */
  summary: string;
}

const percentText = (p: number): string => {
  if (p > 0 && p < 1) return '<1%';
  return `${Math.round(p)}%`;
};

const contextView = (ctx: ContextLike | undefined): ContextView => {
  const source: ContextSource = ctx?.source ?? 'unknown';
  const meta = CONTEXT_SOURCE_META[source];
  const tokens = isNum(ctx?.tokens) ? ctx.tokens : null;
  const window = isNum(ctx?.window) ? ctx.window : null;
  const percent = isNum(ctx?.percent) ? ctx.percent : null;
  const windowLabel = window === null ? '' : formatTokenCount(window);
  const tokensLabel = formatTokenCount(tokens);
  const large = ctx?.large_window_seen ? LARGE_WINDOW_HINT : null;

  // 퍼센트만 오고 창 크기는 안 오는 곳이 있다(목록 행의 `context`). 퍼센트와 출처가 있으면 퍼센트로 그린다.
  if (percent !== null && source !== 'unknown') {
    const clamped = Math.min(100, Math.max(0, percent));
    const size = windowLabel ? `${tokensLabel} / ${windowLabel}` : tokensLabel;
    return {
      kind: 'percent',
      percentLabel: percentText(percent),
      percentValue: clamped,
      tokensLabel,
      windowLabel,
      sourceLabel: meta.label,
      sourceHint: meta.hint,
      largeWindowHint: null,
      summary: `컨텍스트 ${percentText(percent)} (${size} · ${meta.label})`,
    };
  }
  if (tokens !== null) {
    return {
      kind: 'tokens_only',
      percentLabel: '',
      percentValue: null,
      tokensLabel,
      windowLabel: '',
      sourceLabel: CONTEXT_SOURCE_META.unknown.label,
      sourceHint: CONTEXT_SOURCE_META.unknown.hint,
      largeWindowHint: large,
      summary: `컨텍스트 ${tokensLabel} 토큰 (창 크기 모름)`,
    };
  }
  return {
    kind: 'unknown',
    percentLabel: '',
    percentValue: null,
    tokensLabel: UNKNOWN_TEXT,
    windowLabel: '',
    sourceLabel: CONTEXT_SOURCE_META.unknown.label,
    sourceHint: CONTEXT_SOURCE_META.unknown.hint,
    largeWindowHint: large,
    summary: `컨텍스트 ${UNKNOWN_TEXT}`,
  };
};

/* ---------------- 제목 (§3.18) ---------------- */

type TitleKind = 'text' | 'withheld' | 'redacted' | 'none';

interface TitleView {
  kind: TitleKind;
  /** 보일 문장. 제목(ok)이거나, 제목이 보류됐을 때 대신 보이는 첫 요청의 첫 줄(withheld). */
  text: string | null;
  /** text 가 없을 때 대신 보이는 안내. */
  placeholder: string | null;
  /** 출처 칩의 문구. */
  tag: string | null;
  hint: string | null;
}

/**
 * 제목의 네 상태.
 *   ok        값이 있다 → 그 값과 출처
 *   withheld  서버가 제목을 아직 받지 않았다 → 첫 요청을 대신 보이고 "제목 보류됨(첫 요청)"
 *   redacted  비식별화로 가려졌다 → 첫 요청으로 대신하지 않는다
 *   none      그런 줄이 없었다
 */
const titleView = (title: DeepPartial<AnalysisTitle> | undefined): TitleView => {
  const text = title?.text?.trim() || null;
  switch (title?.state) {
    case 'ok':
      if (text) {
        return { kind: 'text', text, placeholder: null, tag: title.source ? TITLE_SOURCE_LABEL[title.source] ?? null : null, hint: null };
      }
      return { kind: 'none', text: null, placeholder: '제목 없음', tag: null, hint: null };
    case 'withheld':
      return {
        kind: 'withheld',
        text,
        placeholder: text ? null : '제목 보류됨',
        tag: '제목 보류됨(첫 요청)',
        hint: '서버가 아직 제목을 받지 않아 첫 요청의 첫 줄을 대신 보입니다. 제목이 없다는 뜻이 아닙니다.',
      };
    case 'redacted':
      return {
        kind: 'redacted',
        text: null,
        placeholder: '제목 가려짐',
        tag: null,
        hint: '비식별화 설정으로 제목이 가려졌습니다. 가린 제목을 첫 요청으로 대신하지 않습니다.',
      };
    default:
      return { kind: 'none', text: null, placeholder: '제목 없음', tag: null, hint: null };
  }
};

/* ---------------- 기타 ---------------- */

/** 경로를 폴더와 파일 이름으로 가른다. 폴더는 줄어들어도 파일 이름은 끝까지 보이게 하려는 것이다. */
const splitPath = (path: string): { dir: string; base: string } => {
  const normalized = path.replace(/\\/g, '/');
  const cut = normalized.lastIndexOf('/');
  if (cut < 0) return { dir: '', base: normalized };
  return { dir: normalized.slice(0, cut + 1), base: normalized.slice(cut + 1) };
};

/** 서버가 준 URL은 http(s)일 때만 링크로 쓴다. */
const safeHref = (url: string | null | undefined): string | undefined => {
  if (!url) return undefined;
  return /^https?:\/\//i.test(url) ? url : undefined;
};

/** `task_type.share`(0~1)를 퍼센트 문자열로. */
const shareText = (share: number | null | undefined): string => (isNum(share) ? `${Math.round(share * 100)}%` : UNKNOWN_TEXT);

/** 작업 유형 라벨. `unknown`은 규칙으로 분류하지 못한 요청이다 (internal/insights/classifier.go). */
const taskTypeLabel = (value: string): string => (value === 'unknown' ? '분류 못함' : value);


/* ---------------- 패스 C: 조밀한 칩용 서식 ---------------- */

/**
 * 칩에 넣는 금액. 목록의 `fmtCost`(소수 넷)와 달리 레퍼런스처럼 짧다("$7.57", "$0.19").
 * 모름은 "알 수 없음", 0은 "$0"이다.
 */
const formatUsdCompact = (n: number | null | undefined): string => {
  if (!isNum(n)) return UNKNOWN_TEXT;
  if (n === 0) return '$0';
  if (n >= 100) return `$${Math.round(n).toLocaleString('en-US')}`;
  if (n >= 0.01) return `$${n.toFixed(2)}`;
  return `$${n.toFixed(3)}`;
};

/** 총 토큰 칩의 글자("26.3M tok"). 모르면 null이라 호출하는 쪽이 점선 칩을 그린다. */
const formatTokensChip = (total: number | null | undefined): string | null => (isNum(total) ? `${formatTokenCount(total)} tok` : null);

/** 앞 N글자만 남기고 "…"을 붙인다. 이미 짧으면 그대로. */
const clip = (text: string, max: number): string => (text.length <= max ? text : `${text.slice(0, Math.max(0, max - 1)).trimEnd()}…`);

export {
  formatCount,
  formatTokenCount,
  formatUsd,
  formatClock,
  formatDateTime,
  formatFullDateTime,
  formatClockNear,
  formatDuration,
  formatRelative,
  formatUsdCompact,
  formatTokensChip,
  clip,
  countsParts,
  countsLine,
  tokenView,
  costView,
  turnCostView,
  contextView,
  titleView,
  splitPath,
  safeHref,
  shareText,
  taskTypeLabel,
};
export type { TitleView, TitleKind, CountPart, TokenView, CostView, CostTone, TurnCostView, TurnCostKind, ContextView, ContextKind, ContextLike };
