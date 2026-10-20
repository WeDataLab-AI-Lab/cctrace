/**
 * 합성 세션 설계도(`Blueprint`) → 캘린더 응답의 세션, 목록 행, 뷰어의 분석 응답.
 *
 * 하나의 설계도에서 셋이 모두 나오므로 캘린더의 띠, 목록 행, 뷰어의 요청 목록이 같은 사실을 말한다: 띠의 시각 = 분석의
 * 시작·마지막 활동, 입력선 수 = 요청 수, 비용 = 목록의 비용. 분석 응답은 뷰어에서 세션을 고를 때만 만든다(지연).
 */

import type {
  AnalysisState,
  AnalysisTurn,
  SessionAnalysis,
  SessionAnalysisPending,
  SessionCategoryKey,
  SessionRowAnalysis,
  SessionRowSignals,
} from '@/lib/session-analysis-types';
import type { CalendarInput, CalendarSegment, CalendarSession } from '@/lib/session-calendar-types';
import { hasSegments } from '@/lib/session-calendar-types';
import type { SessionOverview } from '@/lib/types';
import { PREVIEW_NOW, ago, ctxObserved, inputsOf, otel, ready, text, tok, turn } from '../builders';
import type { SessionFixture } from '../fixtures';
import { iso } from './calendar-builders';
import type { Blueprint, BlueprintInput } from './generate';

const MIN = 60_000;
const VIEWER_USER = { user_id: 'usr-5c1f02', profile_email: 'dev.kim@example.com' };

const round4 = (n: number): number => Math.round(n * 10_000) / 10_000;

/** 요청(턴을 여는 입력)의 수. 작업 중 입력은 세지 않는다. */
const requestCount = (bp: Blueprint): number => bp.inputs.filter((i) => !i.followUp).length;

/** 사실이 있는 세션인가(조각과 입력 시각을 안다). */
const withFacts = (bp: Blueprint): boolean => hasSegments(bp.facts);

const firstStart = (bp: Blueprint): number => bp.segments[0].start;
const lastEnd = (bp: Blueprint): number => bp.segments[bp.segments.length - 1].end;

/** 조각 안에 든 사람 입력 수. */
const inputsIn = (bp: Blueprint, seg: { start: number; end: number }): number =>
  bp.inputs.filter((i) => i.at >= seg.start && i.at <= seg.end).length;

/* ---------------- 캘린더 응답의 세션 ---------------- */

const calendarSessionOf = (bp: Blueprint): CalendarSession => {
  const facts = withFacts(bp);
  const segments: CalendarSegment[] = facts
    ? bp.segments.map((s) => ({
        start: iso(s.start),
        end: iso(s.end),
        records: Math.round(((s.end - s.start) / MIN) * 1.8) + inputsIn(bp, s) * 3 + 2,
        human_inputs: inputsIn(bp, s),
      }))
    : [];
  const items: CalendarInput[] = facts
    ? bp.inputs.map((i, n) => ({
        ts: iso(i.at),
        turn_key: `t${n + 1}`,
        preview: { text: i.state === 'ok' ? i.text : null, state: i.state },
      }))
    : [];
  return {
    session_id: bp.id,
    title: bp.title,
    colors: {
      project: bp.project,
      agent: bp.agent,
      model: bp.model,
      profile: bp.profile.name,
      components: bp.components,
      screens: bp.screens,
      kinds: bp.kinds,
      status: bp.status,
    },
    requests: facts ? (bp.requestless ? 0 : requestCount(bp)) : null,
    cost: { usd: bp.costUsd, source: bp.costSource },
    facts: bp.facts,
    range: { start: iso(firstStart(bp)), end: iso(lastEnd(bp)) },
    segments,
    inputs: facts ? { state: 'observed', items, truncated: false } : { state: 'undeterminable', items: [], truncated: false },
  };
};

/* ---------------- 목록 행 ---------------- */

/** 마지막 활동이 가까운 정도로 정한 관측 범주(합성). */
const categoryOf = (bp: Blueprint): SessionCategoryKey => {
  if (bp.status === 'running') return 'running';
  return PREVIEW_NOW - lastEnd(bp) < 24 * 60 * MIN ? 'recent' : 'inactive';
};

/** 한 턴에 몫으로 돌아가는 토큰·비용. 합이 세션 합계에 가깝도록 나눈다(목록과 분석이 같은 값을 말한다). */
const perTurn = (bp: Blueprint, n: number) => {
  const divisor = Math.max(1, n);
  return {
    cost: round4((bp.costUsd ?? 0) / divisor),
    input: Math.floor(bp.tokens.input / divisor),
    output: Math.floor(bp.tokens.output / divisor),
    cache_read: Math.floor(bp.tokens.cache_read / divisor),
    cache_create: Math.floor(bp.tokens.cache_create / divisor),
  };
};

const rowSignals = (bp: Blueprint): SessionRowSignals | undefined => {
  if (!withFacts(bp) || bp.requestless) return undefined;
  return {
    turn: bp.status === 'running' ? 'open' : bp.status === 'midway' ? 'interrupted' : 'ended',
    question: bp.status === 'leftover' && bp.inputs.length % 3 === 0 ? 1 : 0,
    background: 0,
    uncommitted: bp.status === 'leftover' ? 1 + (bp.inputs.length % 4) : 0,
    todo: null,
  };
};

const rowOfBlueprint = (bp: Blueprint): SessionOverview => {
  const facts = withFacts(bp);
  const n = requestCount(bp);
  const t = perTurn(bp, n);
  const rowAnalysis: SessionRowAnalysis = {
    state: bp.facts,
    title: bp.title,
    requests: facts ? (bp.requestless ? 0 : n) : null,
    inputs: facts ? bp.inputs.length : null,
    context: {
      percent: bp.contextPercent,
      tokens: bp.contextPercent === null ? null : Math.round(bp.contextPercent * 10_000),
      source: bp.contextPercent === null ? 'unknown' : 'observed',
      large_window_seen: bp.contextPercent !== null && bp.contextPercent > 50,
    },
    plan_flags: { has_plan_file: bp.planFile, used_plan_mode: bp.planMode },
    ...(facts ? { category: categoryOf(bp) } : {}),
    ...(rowSignals(bp) ? { signals: rowSignals(bp) } : {}),
  };
  return {
    session_id: bp.id,
    profile_email: VIEWER_USER.profile_email,
    user_id: VIEWER_USER.user_id,
    login_email: bp.profile.loginEmail,
    account_count: 1,
    model: bp.model,
    agent: bp.agent,
    start_time: iso(firstStart(bp)),
    end_time: iso(lastEnd(bp)),
    input_tokens: bp.requestless ? bp.tokens.input : t.input * Math.max(1, n),
    output_tokens: bp.requestless ? bp.tokens.output : t.output * Math.max(1, n),
    cost_usd: bp.requestless ? (bp.costUsd ?? 0) : round4(t.cost * Math.max(1, n)),
    event_count: bp.inputs.length * 6 + 12,
    has_sync: true,
    project_hash: bp.project.hash,
    project_name: bp.project.name ?? undefined,
    // 요청 없는 세션은 자동 실행(헤드리스)이다.
    entrypoint: bp.requestless ? 'sdk-cli' : 'cli',
    analysis: rowAnalysis,
  };
};

/* ---------------- 뷰어의 분석 응답(지연) ---------------- */

const turnsOf = (bp: Blueprint): AnalysisTurn[] => {
  const requests = bp.inputs.filter((i) => !i.followUp);
  const n = requests.length;
  const t = perTurn(bp, n);
  const turns: AnalysisTurn[] = [];
  let index = -1;
  const extras = new Map<number, number>();
  bp.inputs.forEach((input: BlueprintInput) => {
    if (!input.followUp) index += 1;
    else extras.set(Math.max(index, 0), (extras.get(Math.max(index, 0)) ?? 0) + 1);
  });
  requests.forEach((input, i) => {
    const segment = bp.segments.find((s) => input.at >= s.start && input.at <= s.end) ?? bp.segments[0];
    const next = requests[i + 1]?.at ?? Number.POSITIVE_INFINITY;
    const end = Math.min(segment.end, next - 30_000, input.at + 12 * MIN);
    const tookMin = Math.max(0.2, (end - input.at) / MIN);
    const last = i === n - 1;
    const running = last && bp.status === 'running';
    const midway = last && bp.status === 'midway';
    turns.push(
      turn({
        key: `t${i + 1}`,
        no: i + 1,
        at: (PREVIEW_NOW - input.at) / MIN,
        took: tookMin,
        request_no: i + 1,
        status: running ? 'open' : midway ? 'interrupted' : 'ended',
        extra_inputs: extras.get(i) ?? 0,
        preview: input.state === 'ok' ? text(input.text) : { text: null, state: input.state },
        api_requests: 2 + (i % 5),
        tokens: tok(t.input, t.output, t.cache_read, t.cache_create),
        cost: otel(t.cost),
        context: ctxObserved(120_000 + ((i * 7_000) % 300_000), 1_000_000),
        tools: { calls: 3 + (i % 9), subagents: 0 },
      }),
    );
  });
  return turns;
};

const pendingState = (facts: AnalysisState): SessionAnalysisPending['state'] =>
  facts === 'truncated' ? 'truncated' : facts === 'none' ? 'none' : facts === 'unsupported' ? 'unsupported' : 'building';

const analysisOfBlueprint = (bp: Blueprint): SessionAnalysis => {
  if (!withFacts(bp)) {
    // 사실이 없는 세션: 분석 탭은 그 상태의 안내를 보인다.
    return {
      session_id: bp.id,
      short_id: bp.id.slice(0, 8),
      state: pendingState(bp.facts),
      owner_view: true,
      header: bp.facts === 'none' ? undefined : { agent: bp.agent, model: bp.model, project: bp.project },
    };
  }
  const turns = turnsOf(bp);
  const n = turns.length;
  const t = perTurn(bp, n);
  const answers: { turn_key: string; ts: string }[] = [];
  const firstS = firstStart(bp);
  const lastE = lastEnd(bp);
  return ready({
    session_id: bp.id,
    state: bp.facts === 'stale' ? 'stale' : 'ready',
    agent: bp.agent,
    model: bp.model,
    branch: bp.branch,
    project: bp.project,
    login_emails: [bp.profile.loginEmail],
    title: bp.title,
    name: null,
    category: { key: categoryOf(bp), as_of: ago(0.5) },
    turns,
    inputs: inputsOf(turns, answers),
    inputs_count: bp.inputs.length,
    cost_source: bp.costSource,
    unattached_usd: bp.requestless ? (bp.costUsd ?? 0) : round4(Math.max(0, (bp.costUsd ?? 0) - t.cost * n)),
    ...(bp.requestless
      ? {
          totals: {
            tokens: tok(bp.tokens.input, bp.tokens.output, bp.tokens.cache_read, bp.tokens.cache_create),
            api_requests: 8,
            tool_calls: 0,
            usd: bp.costUsd,
          },
        }
      : {}),
    subagents_running: 0,
    active_seconds: Math.round(bp.segments.reduce((s, x) => s + (x.end - x.start), 0) / 1000),
    started_ago: (PREVIEW_NOW - firstS) / MIN,
    last_activity_ago: (PREVIEW_NOW - lastE) / MIN,
    tags: {
      change_area: { value: null, state: 'observed' },
      task_type: { value: bp.kinds[0] === '구현' ? 'implementation' : bp.kinds[0] === '버그 수정' ? 'debugging' : 'operations', share: 0.6, basis: 'classifier' },
      rules_files: [],
    },
    plan_flags: { has_plan_file: bp.planFile, used_plan_mode: bp.planMode },
    signals: {
      turn: { state: 'observed', value: bp.status === 'running' ? 'open' : bp.status === 'midway' ? 'interrupted' : 'ended' },
      question: { state: 'observed', value: 0 },
      background: { state: 'observed', value: 0, parts: null },
      uncommitted: { state: 'observed', value: bp.status === 'leftover' ? 2 : 0, at_least: false, changed_at: ago(30), reevaluating: false },
      todo: { state: 'undeterminable', value: null, reason: 'no_list' },
    },
    outputs: {
      edited_files: {
        state: 'observed',
        active_total: bp.editedFiles,
        abandoned_total: 0,
        truncated: false,
        items: Array.from({ length: Math.min(bp.editedFiles, 6) }, (_, i) => ({
          path: `src/${bp.screens[0] ? 'screens' : 'core'}/module-${i + 1}.ts`,
          edits: 2 + i,
          subagent_edits: 0,
          abandoned_edits: 0,
        })),
      },
      lines: null,
      prs: { items: [], truncated: false },
    },
    recaps: { state: 'none', items: [], absent_reason: n < 3 ? 'too_few_turns' : 'not_away' },
    computed_ago: 2,
  });
};

/** 설계도 → 미리보기 픽스처. 분석 응답은 처음 읽을 때 만든다. */
const fixtureOfBlueprint = (bp: Blueprint): SessionFixture => {
  let cached: SessionAnalysis | undefined;
  return {
    id: bp.id,
    label: `합성 세션 · ${bp.project.name} · ${bp.screens[0] ?? '화면 없음'} · ${bp.kinds.join('·') || '종별 없음'}`,
    covers: [bp.scenario],
    row: rowOfBlueprint(bp),
    get analysis(): SessionAnalysis {
      cached ??= analysisOfBlueprint(bp);
      return cached;
    },
    identities: {
      user_ids: [VIEWER_USER.user_id],
      profile_emails: [VIEWER_USER.profile_email],
      login_emails: [bp.profile.loginEmail],
      mixed: false,
    },
  };
};

export { requestCount, withFacts, calendarSessionOf, rowOfBlueprint, analysisOfBlueprint, fixtureOfBlueprint };
