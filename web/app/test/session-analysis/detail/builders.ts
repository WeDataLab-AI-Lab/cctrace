/**
 * 패스 C 미리보기(상세 · 타임라인 · 목록 카드 · 프레임)의 픽스처를 짓는 도구. 합성 데이터 전용이다.
 *
 * 패스 A의 `../builders`(턴과 응답 조립, 합계는 턴에서 파생)를 그대로 쓰고, 그 위에 패스 C 필드를 얹는다:
 * 판정(`deriveJudgement`), 세션 기록, 커밋, task, 메모, 분류, 타임라인. 형 단언(`as`)은 쓰지 않는다 — 모든 값은
 * `lib/session-analysis-types`의 타입으로 검사된다.
 *
 * 구성으로 지키는 불변식(시험이 다시 확인한다):
 *   - 턴의 `commits`는 세션 커밋 중 `turn_key`가 있는 것의 분할이다(`finalize`가 `turn_key`로 나눠 담는다)
 *   - 턴의 `work_ids`는 작업 항목 중 `turn_key`가 있는 것의 분할이다
 *   - 타임라인의 사용자 입력은 `analysis.inputs`와 같은 번호·종류·턴이다
 */

import type {
  AnalysisIdentities,
  AnalysisInput,
  AnalysisPlan,
  AnalysisQuestion,
  AnalysisTurn,
  BackgroundSignal,
  CommitItem,
  InputKind,
  PullRequest,
  QuestionSignal,
  RecordWithContent,
  SessionAnalysis,
  SessionAnalysisFull,
  SessionClassification,
  SessionCommits,
  SessionRecord,
  SessionSignals,
  SessionTasks,
  TimelineMessage,
  TodoSignal,
  TokenBreakdown,
  TurnCommit,
  TurnCost,
  TurnSignal,
  TurnSignalValue,
  TurnStatus,
  TurnStatusReason,
  UncommittedSignal,
  WorkChild,
  WorkItem,
  WorkKind,
  SignalUndeterminableReason,
} from '@/lib/session-analysis-types';
import type { SessionOverview } from '@/lib/types';
import { isFullAnalysis } from '@/components/sessions/analysis/guards';
import {
  PREVIEW_NOW,
  ago,
  asNonOwnerAdminView,
  asNonOwnerRow,
  ctxObserved,
  inputsOf,
  otel,
  ready,
  rowAnalysisOf,
  rowOf,
  text,
  tok,
  totalOf,
  turn,
} from '../builders';
import { deriveJudgement, rowJudgementOf } from './judgement-derive';

export { ago, ctxObserved, inputsOf, otel, ready, text, tok, totalOf, turn };

/** 미리보기의 "지금". 패스 A·캘린더와 같은 값이라 상대 시각이 어긋나지 않는다. */
export const DETAIL_NOW = PREVIEW_NOW;

/* ============================================================
   토큰과 턴
   ============================================================ */

/** 턴 하나의 총 토큰을 네 칸으로 나눈다. 캐시 읽기가 대부분이다(실제 세션의 모양). 합은 정확히 `total`이다. */
export const tokensOf = (total: number): TokenBreakdown => {
  const t = Math.round(total);
  const output = Math.round(t * 0.012);
  const input = Math.round(t * 0.003);
  const cacheCreate = Math.round(t * 0.07);
  return tok(input, output, t - input - output - cacheCreate, cacheCreate);
};

const NON_REQUEST_KINDS: ReadonlySet<InputKind> = new Set<InputKind>([
  'answer',
  'notification',
  'peer',
  'channel',
  'auto',
  'scheduled',
  'sdk',
  'system',
  'command',
]);

export interface TurnSpec {
  key: string;
  /** 요청의 첫 줄(턴 목록의 미리보기). null이면 미리보기가 없다. */
  text: string | null;
  /** 타임라인에 보이는 입력 전문(줄바꿈 포함). 없으면 `text`. */
  prompt?: string;
  /** 시작: 지금부터 몇 분 전. */
  at: number;
  /** 걸린 분. */
  took: number;
  /** 이 턴의 총 토큰. */
  tokens: number;
  /** OTEL 비용. null이면 API 호출이 없는 턴이다. */
  usd?: number | null;
  /** 이 턴의 마지막 요청의 컨텍스트 토큰과 창. */
  ctx?: number;
  window?: number;
  /** 비용을 `otel`이 아닌 것으로 덮는다(추정 등). */
  cost?: TurnCost;
  kind?: InputKind;
  status?: TurnStatus;
  reason?: TurnStatusReason | null;
  mode?: string;
  denied?: boolean;
  images?: number;
  extraInputs?: number;
  tools?: number;
  questionHeaders?: AnalysisTurn['question_headers'];
  edited?: AnalysisTurn['edited'];
  prs?: PullRequest[];
  /** 타임라인: 이 턴의 마지막 assistant 메시지. null이면 없다. */
  reply?: string | null;
  /** 타임라인: 도구 호출 사이의 assistant 중간 출력. */
  progress?: string[];
}

export interface TurnTimelineExtra {
  prompt: string | null;
  reply: string | null;
  progress: string[];
}

/** 턴 명세의 순서대로 턴을 만들고, 타임라인에 쓸 글(전문, 답, 진행 메시지)을 턴 키로 묶어 돌려준다. */
export const turnsOf = (specs: readonly TurnSpec[]): { turns: AnalysisTurn[]; extras: Record<string, TurnTimelineExtra> } => {
  let requestNo = 0;
  const extras: Record<string, TurnTimelineExtra> = {};
  const turns = specs.map((s, i) => {
    const kind = s.kind ?? 'typed';
    const isRequest = !NON_REQUEST_KINDS.has(kind);
    if (isRequest) requestNo += 1;
    const noCall = s.usd === null && s.cost === undefined;
    extras[s.key] = { prompt: s.prompt ?? s.text, reply: s.reply === undefined ? null : s.reply, progress: s.progress ?? [] };
    return turn({
      key: s.key,
      no: i + 1,
      at: s.at,
      took: s.took,
      request_no: isRequest ? requestNo : null,
      kind,
      is_request: isRequest,
      status: s.status ?? 'ended',
      status_reason: s.reason ?? null,
      preview: s.text === null ? { text: null, state: 'none' } : text(s.text),
      images: s.images ?? 0,
      mode: s.mode ?? '',
      denied: s.denied ?? false,
      extra_inputs: s.extraInputs ?? 0,
      api_requests: noCall ? 0 : Math.max(1, Math.round(s.tokens / 90_000)),
      tokens: noCall ? tok(0, 0, 0, 0) : tokensOf(s.tokens),
      cost: s.cost ?? (noCall ? { usd: null, source: 'none', reason: null } : otel(s.usd ?? 0)),
      context: noCall ? { tokens: null, window: null, percent: null, source: 'unknown' } : ctxObserved(s.ctx ?? 60_000, s.window ?? 200_000),
      tools: { calls: s.tools ?? (noCall ? 0 : 6), subagents: 0 },
      question_headers: s.questionHeaders ?? [],
      edited: s.edited ?? { count: 0, sample: [], abandoned: 0 },
      prs: s.prs ?? [],
    });
  });
  return { turns, extras };
};

/* ============================================================
   신호
   ============================================================ */

export const S = {
  turn: (value: TurnSignalValue): TurnSignal => ({ state: 'observed', value }),
  turnUnknown: (reason: SignalUndeterminableReason): TurnSignal => ({ state: 'undeterminable', value: null, reason }),
  question: (value: number): QuestionSignal => ({ state: 'observed', value }),
  questionUnknown: (reason: SignalUndeterminableReason): QuestionSignal => ({ state: 'undeterminable', value: null, reason }),
  background: (value: number): BackgroundSignal => ({ state: 'observed', value }),
  backgroundUnknown: (reason: SignalUndeterminableReason): BackgroundSignal => ({ state: 'undeterminable', value: null, reason, parts: null }),
  uncommitted: (value: number, changedAgo: number | null = null): UncommittedSignal => ({
    state: 'observed',
    value,
    at_least: false,
    changed_at: changedAgo === null ? null : ago(changedAgo),
    reevaluating: true,
  }),
  uncommittedUnknown: (reason: SignalUndeterminableReason): UncommittedSignal => ({ state: 'undeterminable', value: null, reason }),
  todo: (value: number): TodoSignal => ({ state: 'observed', value }),
  todoUnknown: (reason: SignalUndeterminableReason): TodoSignal => ({ state: 'undeterminable', value: null, reason }),
};

/* ============================================================
   커밋 · 작업 항목 · 기록
   ============================================================ */

/** 짧은 해시에서 40자 전체 해시를 만든다. 같은 입력은 같은 출력이다. */
export const fullHash = (short: string): string => `${short}${'9e1c4a7d2b8f3065'}${'a1d4c7e9b2f08136'}`.slice(0, 40);

export interface CommitInit {
  short: string | null;
  subject: string;
  /** 지금부터 몇 분 전. */
  at: number;
  turn?: string;
  tasks?: string[];
  repository?: string;
  branch?: string;
}

export const commitOf = (c: CommitInit): CommitItem => ({
  hash: c.short === null ? null : fullHash(c.short),
  short: c.short,
  subject: c.subject,
  ts: ago(c.at),
  ...(c.turn ? { turn_key: c.turn } : {}),
  ...(c.repository ? { repository: c.repository } : {}),
  ...(c.branch ? { branch: c.branch } : {}),
  tasks: c.tasks ?? [],
});

export const observedCommits = (items: CommitItem[], total?: number): SessionCommits => ({
  state: 'observed',
  total: total ?? items.length,
  items,
  truncated: total !== undefined && total > items.length,
});

export const NOT_COLLECTED_COMMITS: SessionCommits = { state: 'not_collected', total: null, items: [], truncated: false };
export const UNDETERMINABLE_COMMITS: SessionCommits = { state: 'undeterminable', total: null, items: [], truncated: false };

export interface WorkInit {
  decision?: string;
  note?: string;
  turn?: string;
  commits?: string[];
  tasks?: string[];
  children?: WorkChild[];
}

export const workOf = (id: string, kind: WorkKind, done: boolean | null, body: string, o: WorkInit = {}): WorkItem => ({
  id,
  kind,
  done,
  text: body,
  ...(o.decision ? { decision: o.decision } : {}),
  ...(o.note ? { note: o.note } : {}),
  ...(o.turn ? { turn_key: o.turn } : {}),
  commits: o.commits ?? [],
  tasks: o.tasks ?? [],
  ...(o.children ? { children: o.children } : {}),
});

export const recordOf = (
  content: Pick<RecordWithContent, 'headline' | 'bullets' | 'review' | 'tags' | 'work'>,
  opts: { state?: 'ready' | 'stale'; generatedAgo: number; coversAgo: number },
): RecordWithContent => ({
  state: opts.state ?? 'ready',
  generated_at: ago(opts.generatedAgo),
  covers_until: ago(opts.coversAgo),
  ...content,
});

export const NO_RECORD: SessionRecord = { state: 'none', generated_at: null, covers_until: null };

/* ============================================================
   조립
   ============================================================ */

const firstLineOf = (value: string): string => value.split('\n')[0]?.trim() ?? '';

const uniq = (items: readonly string[]): string[] => [...new Set(items)];

const group = <T>(items: readonly T[], keyOf: (item: T) => string | undefined): Map<string, T[]> => {
  const map = new Map<string, T[]>();
  for (const item of items) {
    const key = keyOf(item);
    if (key === undefined) continue;
    map.set(key, [...(map.get(key) ?? []), item]);
  }
  return map;
};

/** 턴마다 커밋·task·작업 항목을 `turn_key`로 나눠 담는다. 분할은 구성으로 성립한다. */
export const withTurnFacts = (
  turns: readonly AnalysisTurn[],
  commits: SessionCommits,
  record: SessionRecord,
): AnalysisTurn[] => {
  const commitsByTurn = group(commits.items, (c) => c.turn_key);
  const work = record.state === 'ready' || record.state === 'stale' ? record.work : [];
  const workByTurn = group(work, (w) => w.turn_key);
  return turns.map((t) => {
    const mine = commitsByTurn.get(t.key) ?? [];
    const turnCommits: TurnCommit[] = mine.map((c) => ({ short: c.short, subject: c.subject, tasks: c.tasks }));
    const items = workByTurn.get(t.key) ?? [];
    return {
      ...t,
      commits: turnCommits,
      tasks: uniq([...mine.flatMap((c) => c.tasks), ...items.flatMap((w) => w.tasks)]),
      work_ids: items.map((w) => w.id),
    };
  });
};

export const commitsCount = (commits: SessionCommits): number | null =>
  commits.state === 'observed' ? (commits.total ?? commits.items.length) : null;

export const headlineOf = (record: SessionRecord): string | null =>
  record.state === 'ready' || record.state === 'stale' ? record.headline : null;

export interface RowMetaInput {
  session_id: string;
  profile_email: string;
  user_id: string;
  login_email: string;
  account_count?: number;
  has_sync?: boolean;
  entrypoint?: string;
}

export interface DetailFixture {
  id: string;
  /** 이 세션이 보여 주는 것(한 문장). */
  label: string;
  covers: string[];
  /** 주인 범위의 목록 행. */
  row: SessionOverview;
  /** 주인 범위의 분석 응답. null은 404(신원이 섞인 세션). */
  analysis: SessionAnalysis | null;
  identities: AnalysisIdentities;
  timeline: TimelineMessage[];
}

export interface FinalizeInput {
  base: SessionAnalysisFull;
  meta: RowMetaInput;
  label: string;
  covers: string[];
  identities: AnalysisIdentities;
  /** 살아 있는 실행 인스턴스가 있다(범주 waiting·running·idle). 판정의 상태를 정한다. */
  alive: boolean;
  interrupted?: boolean;
  record: SessionRecord;
  commits: SessionCommits;
  tasks: SessionTasks;
  /** 메모 전문. 첫 줄이 카드에 보인다. */
  note?: string;
  classification: SessionClassification;
  /** 직접 쓴 타임라인. 없으면 응답에서 만든다. */
  timeline?: TimelineMessage[];
  extras?: Record<string, TurnTimelineExtra>;
  /** 응답에서 만든 타임라인에 더 끼우는 메시지(시스템 줄 등). */
  extraMessages?: TimelineMessage[];
}

export const finalize = (input: FinalizeInput): DetailFixture => {
  const { base, record, commits, tasks } = input;
  const judgement = deriveJudgement({ alive: input.alive, interrupted: input.interrupted ?? false, signals: base.signals, record });
  const noteText = input.note?.trim() ? input.note : null;
  const analysis: SessionAnalysisFull = {
    ...base,
    turns: withTurnFacts(base.turns, commits, record),
    abandoned: withTurnFacts(base.abandoned, { ...commits, items: [] }, NO_RECORD),
    judgement,
    record,
    commits,
    tasks,
    note: noteText ? { text: noteText, updated_at: ago(40) } : null,
    classification: input.classification,
  };
  const rowAnalysis = {
    ...rowAnalysisOf(analysis),
    judgement: rowJudgementOf(judgement),
    commits: commitsCount(commits),
    note: noteText ? firstLineOf(noteText) : null,
    classification: input.classification,
    headline: headlineOf(record),
    tokens_total: totalOf(analysis.header.tokens),
  };
  return {
    id: base.session_id,
    label: input.label,
    covers: input.covers,
    row: rowOf(input.meta, analysis, rowAnalysis),
    analysis,
    identities: input.identities,
    timeline: input.timeline ?? sortMessages([...buildTimeline(analysis, input.extras ?? {}), ...(input.extraMessages ?? [])]),
  };
};

/* ============================================================
   타임라인 (응답에서 만든다)
   ============================================================ */

const REPLIES: readonly string[] = [
  '정리했습니다. 관련 코드를 고치고 테스트로 확인했습니다.\n\n- 변경한 곳은 위 요청의 범위 안입니다.\n- 동작은 기존과 같고 새 경우만 더했습니다.',
  '반영했습니다. 화면에서 바로 확인할 수 있습니다.',
  '요청하신 대로 바꿨습니다. 영향 범위가 넓지 않아 다른 파일은 건드리지 않았습니다.',
  '확인해 보니 원인은 한 곳이었습니다. 그 부분을 고치고 같은 경우를 다루는 테스트를 더했습니다.',
  '작업을 마쳤습니다. 남은 것은 없습니다.',
  '두 가지 방법이 있어 더 단순한 쪽으로 구현했습니다. 필요하면 다른 쪽도 보여 드리겠습니다.',
];

const PROGRESS: readonly string[] = [
  '관련 파일을 먼저 읽어 보겠습니다.',
  '영향 범위를 확인하는 중입니다. 같은 이름을 쓰는 곳이 더 있는지 찾아보겠습니다.',
  '테스트를 돌려 보겠습니다.',
];

const isAnswerInput = (input: AnalysisInput): boolean => input.kind === 'answer';

const SYSTEM_TEXT: Partial<Record<TurnStatus, string>> = {
  interrupted: '사용자가 이 턴을 중단했습니다.',
  error: '마지막 응답이 API 오류로 끝났습니다.',
};

/**
 * 분석 응답에서 타임라인을 만든다. 사용자 입력은 `inputs`와 같은 번호·종류·턴이고, 답한 질문은 답 입력에, 답하지 않은 질문과
 * 플랜은 assistant 메시지에 붙는다. `extras`는 턴 키로 찾는 입력 전문과 assistant 글이다.
 */
export const buildTimeline = (a: SessionAnalysisFull, extras: Record<string, TurnTimelineExtra> = {}): TimelineMessage[] => {
  const all = [...a.turns, ...a.abandoned];
  const turnByKey = new Map(all.map((t) => [t.key, t] as const));
  const of = a.inputs.length;
  const out: TimelineMessage[] = [];
  const answeredKeys = new Set<string>();
  // 한 턴에 입력이 여럿(작업 중 입력 등)이어도 턴의 assistant 메시지는 한 번만 만든다.
  const emittedTurns = new Set<string>();

  a.inputs.forEach((input, i) => {
    const t = turnByKey.get(input.turn_key);
    const base = { id: `m-${input.uuid}`, ts: input.ts, role: 'user' as const, turn_key: input.turn_key, n: input.n, of, source: input.kind };
    if (isAnswerInput(input)) {
      const q = a.questions.find((x) => x.turn_key === input.turn_key && x.status === 'answered' && !answeredKeys.has(x.tool_use_id));
      if (q) answeredKeys.add(q.tool_use_id);
      out.push({ ...base, ...(q ? { question: q } : {}) });
      return;
    }
    const prompt = extras[input.turn_key]?.prompt ?? (t && t.preview.state === 'ok' ? t.preview.text : null);
    out.push({ ...base, ...(prompt ? { text: prompt } : {}), ...(input.images > 0 ? { images: input.images } : {}) });
    if (!t || emittedTurns.has(t.key)) return;
    emittedTurns.add(t.key);

    const x = extras[t.key];
    const progress = x ? x.progress : t.tools.calls >= 8 ? [PROGRESS[i % PROGRESS.length]] : [];
    progress.forEach((line, k) =>
      out.push({ id: `m-${t.key}-p${k}`, ts: new Date(Date.parse(input.ts) + (k + 1) * 20_000).toISOString(), role: 'assistant', turn_key: t.key, text: line, intermediate: true }),
    );
    for (const q of a.questions.filter((x) => x.turn_key === t.key && x.status !== 'answered')) {
      out.push({ id: `m-${q.tool_use_id}`, ts: q.ts, role: 'assistant', turn_key: t.key, question: q });
    }
    for (const p of a.plans.filter((x) => x.turn_key === t.key)) {
      out.push({ id: `m-${t.key}-plan`, ts: p.ts, role: 'assistant', turn_key: t.key, plan: p });
    }
    const reply = x ? x.reply : t.status === 'ended' && t.api_requests > 0 ? REPLIES[i % REPLIES.length] : null;
    if (reply) out.push({ id: `m-${t.key}-r`, ts: t.ended_at, role: 'assistant', turn_key: t.key, text: reply });
    const system = SYSTEM_TEXT[t.status];
    if (system) out.push({ id: `m-${t.key}-s`, ts: t.ended_at, role: 'system', turn_key: t.key, text: system });
  });

  return sortMessages(out);
};

/** 시각 순으로 놓는다. 같은 시각이면 만든 순서를 지킨다(안정 정렬). */
export const sortMessages = (messages: readonly TimelineMessage[]): TimelineMessage[] =>
  messages
    .map((m, index) => ({ m, index }))
    .sort((l, r) => Date.parse(l.m.ts) - Date.parse(r.m.ts) || l.index - r.index)
    .map((e) => e.m);

/* ============================================================
   비소유 관리자 화면
   ============================================================ */

/** 비소유 관리자에게는 집계 성격의 값(판정, 신호, 범주, 실시간)과 주인의 개인 값(메모, 분류)이 가지 않는다. 내용(기록, 커밋, task)은 간다. */
export const stripOwnerOnly = (a: SessionAnalysis): SessionAnalysis => {
  if (!isFullAnalysis(a)) return a;
  const { judgement, note, classification, ...rest } = a;
  void judgement;
  void note;
  void classification;
  return rest;
};

export const adminAnalysis = (a: SessionAnalysis, identities: AnalysisIdentities): SessionAnalysis => stripOwnerOnly(asNonOwnerAdminView(a, identities));

export const adminRow = (row: SessionOverview): SessionOverview => {
  const stripped = asNonOwnerRow(row);
  if (!stripped.analysis) return stripped;
  const { judgement, note, classification, ...rest } = stripped.analysis;
  void judgement;
  void note;
  void classification;
  return { ...stripped, analysis: rest };
};

/** 질문·플랜 카드 만드는 도구에서 쓰는 타입 재노출(픽스처 파일이 한 곳에서 가져오게). */
export type { AnalysisPlan, AnalysisQuestion, SessionSignals };
