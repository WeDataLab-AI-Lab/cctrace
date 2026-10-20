/**
 * 미리보기 픽스처를 짓는 도구. 합성 데이터 전용이다.
 *
 * 합계(토큰, 비용, 요청 수, 시각, 컨텍스트)는 턴에서 파생한다. 그래서 픽스처가 스스로 합계 불변식을
 * 지킨다: 세션 토큰 = Σ턴 토큰 + 귀속하지 못한 서브에이전트 토큰, 세션 비용 = 턴에 붙은 비용 + 붙지 않은
 * 호출 (p1b-design §4.4). 픽스처 시험이 이를 다시 확인한다.
 *
 * 형 단언(`as`)은 쓰지 않는다. 모든 값은 `lib/session-analysis-types`의 타입으로 검사된다.
 */

import type {
  AnalysisCopies,
  AnalysisHeader,
  AnalysisIdentities,
  AnalysisInput,
  AnalysisLive,
  AnalysisOutputs,
  AnalysisPlan,
  AnalysisQuestion,
  AnalysisRecaps,
  AnalysisSubagents,
  AnalysisTags,
  AnalysisTitle,
  AnalysisTruncated,
  AnalysisTurn,
  ContextMix,
  CostNote,
  CostSource,
  PlanFlags,
  SessionAnalysis,
  SessionAnalysisFull,
  SessionCategory,
  SessionRowAnalysis,
  SessionRowSignals,
  SessionSignals,
  TokenBreakdown,
  TurnContext,
  TurnCost,
} from '@/lib/session-analysis-types';
import type { SessionOverview } from '@/lib/types';
import { isFullAnalysis } from '@/components/sessions/analysis/guards';

/**
 * 미리보기의 "지금". 렌더 중에 시계를 읽지 않도록 모든 상대 시각이 이 값을 기준으로 한다.
 *
 * 서울 2026-10-10(토) 15:40이다. 레퍼런스의 주간 캘린더도 토요일 낮에 찍혀, 월~금이 꽉 차고 토요일은 반쯤 지났으며 일요일은
 * 비어 있다. 같은 모양으로 보이도록 수요일에서 토요일로 옮겼다(분석 탭·목록의 상대 시각은 모두 이 값에 대한 상대값이라 그대로다).
 */
export const PREVIEW_NOW = Date.parse('2026-10-10T06:40:00Z');

/** `minutes`분 전의 ISO 시각. */
export const ago = (minutes: number): string => new Date(PREVIEW_NOW - Math.round(minutes * 60_000)).toISOString();

const round4 = (n: number): number => Math.round(n * 10_000) / 10_000;

export const tok = (input: number, output: number, cache_read: number, cache_create: number): TokenBreakdown => ({
  input,
  output,
  cache_read,
  cache_create,
});

export const totalOf = (t: TokenBreakdown): number => t.input + t.output + t.cache_read + t.cache_create;

export const sumTokens = (turns: readonly AnalysisTurn[]): TokenBreakdown =>
  turns.reduce(
    (acc, t) => ({
      input: acc.input + t.tokens.input,
      output: acc.output + t.tokens.output,
      cache_read: acc.cache_read + t.tokens.cache_read,
      cache_create: acc.cache_create + t.tokens.cache_create,
    }),
    tok(0, 0, 0, 0),
  );

/** 턴에 붙은 비용의 합(값이 있는 출처만). */
export const sumAttachedUsd = (turns: readonly AnalysisTurn[]): number =>
  round4(
    turns.reduce((sum, t) => {
      const attached =
        t.cost.usd !== null &&
        (t.cost.source === 'otel' || t.cost.source === 'otel_partial' || t.cost.source === 'estimate' || t.cost.source === 'estimate_pending');
      return attached ? sum + (t.cost.usd ?? 0) : sum;
    }, 0),
  );

/* ---------------- 턴 ---------------- */

export const otel = (usd: number): TurnCost => ({ usd, source: 'otel', reason: null });
export const estimated = (usd: number): TurnCost => ({ usd, source: 'estimate', reason: null });
export const noApiCall: TurnCost = { usd: null, source: 'none', reason: null };

export const ctxObserved = (tokens: number, window: number): TurnContext => ({
  tokens,
  window,
  percent: Math.round((tokens / window) * 1000) / 10,
  source: 'observed',
});
export const ctxModelDefault = (tokens: number, window: number): TurnContext => ({ ...ctxObserved(tokens, window), source: 'model_default' });
export const ctxTokensOnly = (tokens: number): TurnContext => ({ tokens, window: null, percent: null, source: 'unknown' });
export const ctxNone: TurnContext = { tokens: null, window: null, percent: null, source: 'unknown' };

interface TurnInit extends Partial<AnalysisTurn> {
  key: string;
  no: number;
  /** 시작 시각: 지금부터 몇 분 전. */
  at: number;
  /** 걸린 분. 기본 1분. */
  took?: number;
}

/** 턴 하나. 안 적은 값은 "조용한" 기본값이다(끝남, 직접 입력, 호출 없음). */
export const turn = ({ at, took = 1, ...o }: TurnInit): AnalysisTurn => ({
  request_no: null,
  branch: 'active',
  anchor_ts: ago(at),
  started_at: ago(at),
  ended_at: ago(Math.max(0, at - took)),
  kind: 'typed',
  is_request: true,
  extra_inputs: 0,
  status: 'ended',
  status_reason: null,
  preview: { text: null, state: 'none' },
  images: 0,
  mode: '',
  denied: false,
  login_email: '',
  api_requests: 0,
  tokens: tok(0, 0, 0, 0),
  cost: noApiCall,
  context: ctxNone,
  tools: { calls: 0, subagents: 0 },
  question_headers: [],
  edited: { count: 0, sample: [], abandoned: 0 },
  prs: [],
  last_assistant_message_id: `msg_${o.key}`,
  opener_copied: false,
  ...o,
});

export const text = (value: string): AnalysisTurn['preview'] => ({ text: value, state: 'ok' });

/* ---------------- 분석 응답 ---------------- */

interface ReadyInit {
  session_id: string;
  state?: 'ready' | 'stale';
  /** 소유자 화면인가. 기본 true. */
  owner_view?: boolean;
  agent: string;
  model: string;
  branch: string;
  project: AnalysisHeader['project'];
  login_emails: string[];
  login_inferred?: boolean;
  title: AnalysisTitle;
  name: AnalysisHeader['name'];
  category?: SessionCategory;
  turns: AnalysisTurn[];
  abandoned?: AnalysisTurn[];
  /** 입력 수. 기본은 활성 턴 수. */
  inputs_count?: number | null;
  /**
   * 응답 상한에서 배열이 잘린 세션의 전체 수. 서버는 잘린 뒤에도 요청·입력·턴 수를 전체 기준으로 준다.
   * 주면 배열에서 센 값을 대신한다.
   */
  counts?: { requests?: number; inputs?: number; turns?: number; abandoned_turns?: number; api_requests?: number };
  /** 턴 경계를 판단할 수 없을 때(비식별화). 이때 수 셋과 turns·abandoned는 비운다. */
  boundaries_undeterminable?: boolean;
  cost_source: CostSource;
  /** 턴에 붙지 않은 호출의 비용. 모르면 null. */
  unattached_usd: number | null;
  cost_note?: CostNote | null;
  unattributed?: AnalysisSubagents;
  large_window_seen?: boolean;
  context_mix?: ContextMix | null;
  subagents_running: number | null;
  /** 활동 시간(초). */
  active_seconds: number | null;
  tags: AnalysisTags;
  plan_flags: PlanFlags;
  signals?: SessionSignals;
  outputs: AnalysisOutputs;
  recaps: AnalysisRecaps;
  inputs?: AnalysisInput[];
  questions?: AnalysisQuestion[];
  plans?: AnalysisPlan[];
  copies?: AnalysisCopies;
  live?: AnalysisLive;
  truncated?: Partial<AnalysisTruncated>;
  /** 시작: 지금부터 몇 분 전. 기본은 가장 이른 턴의 시작. */
  started_ago?: number;
  /** 마지막 활동: 지금부터 몇 분 전. 기본은 마지막 턴이 끝난 시각. */
  last_activity_ago?: number;
  /**
   * 턴이 없는 세션(턴 경계를 판단할 수 없음)의 합계. 토큰·비용·활동 범위는 턴과 무관하게 계산되기 때문에
   * 턴에서 파생할 수 없다. 주면 턴에서 파생한 값을 대신한다.
   */
  totals?: {
    tokens: TokenBreakdown;
    api_requests: number;
    tool_calls: number;
    /** 세션 비용. 턴에 나누지 못하므로 붙은 몫과 붙지 않은 몫은 모른다. */
    usd: number | null;
  };
  computed_ago?: number;
  pending_ago?: number | null;
}

export const ready = (init: ReadyInit): SessionAnalysisFull => {
  const all = [...init.turns, ...(init.abandoned ?? [])];
  const unattributed = init.unattributed ?? { unattributed_requests: 0, unattributed_tokens: 0 };
  const derived = sumTokens(all);
  // 귀속하지 못한 서브에이전트 토큰은 세션 합계에만 들어 있다. 캐시 읽기 칸에 얹는다.
  const tokens = init.totals
    ? init.totals.tokens
    : { ...derived, cache_read: derived.cache_read + unattributed.unattributed_tokens };
  const attached = init.totals ? null : sumAttachedUsd(all);
  const usd = init.totals
    ? init.totals.usd
    : init.cost_source === 'none'
      ? null
      : round4((attached ?? 0) + (init.unattached_usd ?? 0));
  const requests = init.turns.filter((t) => t.is_request).length;
  const lastTurn = init.turns[init.turns.length - 1];
  const earliest =
    all.length > 0 ? all.reduce((a, t) => (Date.parse(t.started_at) < Date.parse(a) ? t.started_at : a), all[0].started_at) : null;
  const started = init.started_ago !== undefined ? ago(init.started_ago) : earliest;
  const lastAgo = init.last_activity_ago ?? (lastTurn ? (PREVIEW_NOW - Date.parse(lastTurn.ended_at)) / 60_000 : 0);
  const lastActivity = started ? ago(lastAgo) : null;
  const undeterminable = init.boundaries_undeterminable === true;
  // 세션의 컨텍스트는 마지막 활성 턴의 값이다(§3.11).
  const ctxTurn = lastTurn?.context ?? ctxNone;

  const header: AnalysisHeader = {
    title: init.title,
    name: init.name,
    ...(init.category ? { category: init.category } : {}),
    agent: init.agent,
    model: init.model,
    branch: init.branch,
    project: init.project,
    accounts: { login_emails: init.login_emails, inferred: init.login_inferred ?? false },
    started_at: started,
    last_activity_at: lastActivity,
    duration_seconds: started && lastActivity ? Math.max(0, Math.round((Date.parse(lastActivity) - Date.parse(started)) / 1000)) : null,
    active_seconds: init.active_seconds,
    counts: {
      requests: undeterminable ? null : (init.counts?.requests ?? requests),
      inputs: undeterminable ? null : (init.counts?.inputs ?? (init.inputs_count === undefined ? init.turns.length : init.inputs_count)),
      turns: undeterminable ? null : (init.counts?.turns ?? init.turns.length),
      abandoned_turns: undeterminable ? null : (init.counts?.abandoned_turns ?? (init.abandoned ?? []).length),
      api_requests:
        init.counts?.api_requests ?? (init.totals ? init.totals.api_requests : all.reduce((s, t) => s + t.api_requests, 0)),
      turn_boundaries: undeterminable ? { state: 'undeterminable', reason: 'redacted' } : { state: 'observed', reason: null },
    },
    tokens: { ...tokens, basis: 'request_set' },
    cost: {
      usd,
      source: init.cost_source,
      // 턴을 만들지 못한 세션은 모든 호출이 "턴에 붙지 않은 호출"이다(§4.2). 턴이 있는데 합계만 준 경우는 나누지 못한다.
      turn_attached_usd: init.cost_source === 'none' ? null : init.totals ? (undeterminable ? 0 : null) : attached,
      unattached_turn_usd: init.cost_source === 'none' ? null : init.totals ? (undeterminable ? init.totals.usd : null) : init.unattached_usd,
      note: init.cost_note ?? null,
    },
    context: {
      tokens: ctxTurn.tokens,
      window: ctxTurn.window,
      percent: ctxTurn.percent,
      source: ctxTurn.source,
      large_window_seen: init.large_window_seen ?? false,
      mix: init.context_mix ?? null,
    },
    tools: { calls: init.totals ? init.totals.tool_calls : all.reduce((s, t) => s + t.tools.calls, 0), subagents_running: init.subagents_running },
    tags: init.tags,
    plan_flags: init.plan_flags,
  };

  return {
    session_id: init.session_id,
    short_id: init.session_id.slice(0, 8),
    state: init.state ?? 'ready',
    rules_version: init.state === 'stale' ? 1 : 2,
    computed_at: ago(init.computed_ago ?? 1),
    pending_since: init.pending_ago === undefined || init.pending_ago === null ? null : ago(init.pending_ago),
    owner_view: init.owner_view ?? true,
    header,
    ...(init.signals ? { signals: init.signals } : {}),
    outputs: init.outputs,
    recaps: init.recaps,
    turns: undeterminable ? [] : init.turns,
    abandoned: undeterminable ? [] : (init.abandoned ?? []),
    inputs: init.inputs ?? [],
    questions: init.questions ?? [],
    plans: init.plans ?? [],
    ...(init.copies ? { copies: init.copies } : {}),
    subagents: unattributed,
    ...(init.live ? { live: init.live } : {}),
    truncated: {
      turns: false,
      abandoned: false,
      inputs: false,
      questions: false,
      plans: false,
      recaps: false,
      bodies: false,
      ...init.truncated,
    },
  };
};

/* ---------------- 목록 행 ---------------- */

/** 신호 응답을 목록 행의 값만 오는 꼴로 줄인다. 판단 불가는 null, 키가 없으면 해당 없음. */
export const rowSignalsOf = (signals: SessionSignals): SessionRowSignals => {
  const row: SessionRowSignals = {};
  if (signals.turn) row.turn = signals.turn.state === 'observed' ? signals.turn.value : null;
  if (signals.question) row.question = signals.question.state === 'observed' ? signals.question.value : null;
  if (signals.background) row.background = signals.background.state === 'observed' ? signals.background.value : null;
  if (signals.uncommitted) {
    const u = signals.uncommitted;
    // 세션 중간부터 모은 값의 0은 판단 불가다(§3.18): 목록 행에서도 0이 아니라 null이다.
    row.uncommitted = u.state === 'observed' && !(u.at_least && u.value === 0) ? u.value : null;
  }
  if (signals.todo) row.todo = signals.todo.state === 'observed' ? signals.todo.value : null;
  return row;
};

/** 분석 응답에서 목록 행의 `analysis` 객체를 만든다(§8.3). 범주와 신호는 세션 주인 범위에서만 온다. */
export const rowAnalysisOf = (a: SessionAnalysisFull): SessionRowAnalysis => ({
  state: a.state,
  title: a.header.title,
  requests: a.header.counts.requests,
  inputs: a.header.counts.inputs,
  context: {
    percent: a.header.context.percent,
    tokens: a.header.context.tokens,
    source: a.header.context.source,
    large_window_seen: a.header.context.large_window_seen,
  },
  plan_flags: a.header.plan_flags,
  ...(a.header.category ? { category: a.header.category.key } : {}),
  ...(a.signals ? { signals: rowSignalsOf(a.signals) } : {}),
});

interface RowMeta {
  session_id: string;
  profile_email: string;
  user_id: string;
  login_email: string;
  account_count?: number;
  has_sync?: boolean;
  entrypoint?: string;
}

/** 목록 행(`SessionOverview`)을 만든다. 토큰·비용·시각은 분석 응답의 머리에서 가져온다(롤업과 같은 값). */
export const rowOf = (meta: RowMeta, a: SessionAnalysis, analysis: SessionRowAnalysis | undefined): SessionOverview => {
  const h = a.header;
  const started = h?.started_at ?? ago(60);
  return {
    session_id: meta.session_id,
    profile_email: meta.profile_email,
    user_id: meta.user_id,
    login_email: meta.login_email,
    account_count: meta.account_count ?? 1,
    model: h?.model ?? '',
    agent: h?.agent ?? 'claude',
    start_time: started,
    end_time: h?.last_activity_at ?? started,
    input_tokens: h?.tokens?.input ?? 0,
    output_tokens: h?.tokens?.output ?? 0,
    cost_usd: h?.cost?.usd ?? 0,
    event_count: 1,
    has_sync: meta.has_sync ?? true,
    project_hash: h?.project?.hash ?? '',
    project_name: h?.project?.name ?? '',
    entrypoint: meta.entrypoint ?? 'cli',
    ...(analysis ? { analysis } : {}),
  };
};

/* ---------------- 비소유 관리자 화면 ---------------- */

/**
 * 같은 세션을 주인이 아닌 관리자가 볼 때의 응답. 신호·범주·실시간은 오지 않고(화면 정책), 신원 정보가 온다.
 * 분석 API가 하는 일(§8.2 처리 순서 6)과 같다.
 */
export const asNonOwnerAdminView = (a: SessionAnalysis, identities: AnalysisIdentities): SessionAnalysis => {
  if (isFullAnalysis(a)) {
    const { signals, live, header, ...rest } = a;
    void signals;
    void live;
    const { category, ...headerRest } = header;
    void category;
    return { ...rest, header: headerRest, owner_view: false, identities };
  }
  if (a.header) {
    const { category, ...headerRest } = a.header;
    void category;
    return { ...a, header: headerRest, owner_view: false, identities };
  }
  return { ...a, owner_view: false, identities };
};

/** 목록 행도 같다: 범주와 신호는 집계 경로라 비소유 범위에는 오지 않는다. */
export const asNonOwnerRow = (row: SessionOverview): SessionOverview => {
  if (!row.analysis) return row;
  const { category, signals, ...rest } = row.analysis;
  void category;
  void signals;
  return { ...row, analysis: rest };
};

/* ---------------- 입력 목록 (E4, E5) ---------------- */

type InputDraft = Omit<AnalysisInput, 'n'>;

/** 활성 턴을 연 입력과 질문 답을 시각 순으로 늘어놓고 번호를 붙인다. */
export const inputsOf = (turns: readonly AnalysisTurn[], answers: readonly { turn_key: string; ts: string }[] = []): AnalysisInput[] => {
  const openers: InputDraft[] = turns.map((t) => ({
    uuid: `u_${t.key}`,
    ts: t.started_at,
    kind: t.kind,
    turn_key: t.key,
    images: t.images,
  }));
  const answered: InputDraft[] = answers.map((a) => ({
    uuid: `a_${a.turn_key}`,
    ts: a.ts,
    kind: 'answer',
    turn_key: a.turn_key,
    images: 0,
  }));
  return [...openers, ...answered]
    .sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts))
    .map((draft, i) => ({ ...draft, n: i + 1 }));
};
