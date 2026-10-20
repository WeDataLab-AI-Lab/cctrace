/**
 * 변형 세션 열셋: 판정의 조합, 세션 기록의 모든 상태, 커밋의 세 상태와 해시 모르는 커밋, 빈 task 목록, Codex.
 * 모두 지어낸 내용이다(제목, 경로, 커밋 제목, 이메일은 example.com).
 *
 * 목록 묶음 규칙(미리보기의 목록이 이 규칙으로 묶는다):
 *   - 살아 있는 세션(범주 waiting·running·idle): PROCESSING(running), INTERRUPTED(중단 중), RECORD_GENERATING, 그리고 대표 세션
 *   - "함께 끝난" 세션: ALL_PASS, LEFTOVER, ENDED_MIDWAY는 범주가 `ended`이고 마지막 활동이 같은 분 안이다(지금으로부터 약 95분 전)
 *   - 나머지 살아 있지 않은 세션은 `recent` 또는 `inactive`다
 */

import type {
  AnalysisIdentities,
  AnalysisHeader,
  AnalysisLive,
  AnalysisOutputs,
  AnalysisPlan,
  AnalysisQuestion,
  AnalysisRecaps,
  AnalysisTags,
  AnalysisTitle,
  CostSource,
  PlanFlags,
  PullRequest,
  SessionAnalysisFull,
  SessionCategory,
  SessionSignals,
} from '@/lib/session-analysis-types';
import { estimated } from '../builders';
import {
  NOT_COLLECTED_COMMITS,
  NO_RECORD,
  S,
  UNDETERMINABLE_COMMITS,
  ago,
  commitOf,
  finalize,
  inputsOf,
  observedCommits,
  ready,
  recordOf,
  turnsOf,
  workOf,
  type DetailFixture,
  type TurnSpec,
  type TurnTimelineExtra,
} from './builders';

/* ============================================================
   공통 도구
   ============================================================ */

interface User {
  user_id: string;
  profile_email: string;
  login_email: string;
}

const USERS = {
  yoon: { user_id: 'usr-b4e8d1', profile_email: 'yoon.jiho@example.com', login_email: 'jiho.work@example.com' },
  jung: { user_id: 'usr-6c0f93', profile_email: 'jung.eunseo@example.com', login_email: 'eunseo.work@example.com' },
  baek: { user_id: 'usr-d27a45', profile_email: 'baek.sunwoo@example.com', login_email: 'sunwoo.work@example.com' },
  moon: { user_id: 'usr-19ce70', profile_email: 'moon.haeun@example.com', login_email: 'haeun.work@example.com' },
} satisfies Record<string, User>;

const PROJECTS = {
  ledger: { hash: '-Users-dev-work-ledger-service', name: 'ledger-service' },
  atlas: { hash: '-Users-dev-work-atlas-docs', name: 'atlas-docs' },
  relay: { hash: '-Users-dev-work-relay-gateway', name: 'relay-gateway' },
  orbit: { hash: '-Users-dev-work-orbit-ios', name: 'orbit-ios' },
  harbor: { hash: '-Users-dev-work-harbor-cli', name: 'harbor-cli' },
};

const identitiesOf = (u: User): AnalysisIdentities => ({
  user_ids: [u.user_id],
  profile_emails: [u.profile_email],
  login_emails: [u.login_email],
  mixed: false,
});

const NO_RECAPS: AnalysisRecaps = { state: 'none', items: [], absent_reason: 'not_away' };
const NO_PLAN_FLAGS: PlanFlags = { has_plan_file: false, used_plan_mode: false };

const outputsOf = (
  list: ReadonlyArray<readonly [string, number]>,
  lines: readonly [number, number] | null,
  prs: PullRequest[] = [],
): AnalysisOutputs => ({
  edited_files: {
    state: 'observed',
    active_total: list.length,
    abandoned_total: 0,
    truncated: false,
    items: list.map(([path, edits]) => ({ path, edits, subagent_edits: 0, abandoned_edits: 0 })),
  },
  lines: lines ? { added: lines[0], removed: lines[1], source: 'cost_state', partial: false } : null,
  prs: { items: prs, truncated: false },
});

const tagsOf = (area: string | null, type: string | null, rules: ReadonlyArray<readonly [string, number]>): AnalysisTags => ({
  change_area: { value: area, state: 'observed' },
  task_type: type ? { value: type, share: 0.6, basis: 'classifier' } : null,
  rules_files: rules.map(([path, version]) => ({ path, version, discovered_at: ago(60 * 24 * 7) })),
});

const edited = (names: string[]) => ({ count: names.length, sample: names, abandoned: 0 });

interface VariantBase {
  id: string;
  user: User;
  project: { hash: string; name: string };
  branch: string;
  agent?: string;
  model?: string;
  title: AnalysisTitle;
  name: AnalysisHeader['name'];
  category: SessionCategory;
  specs: TurnSpec[];
  /** 질문 답 입력. 입력 번호는 시각 순으로 열린 입력과 함께 붙는다. */
  answers?: { turn_key: string; ts: string }[];
  questions?: AnalysisQuestion[];
  plans?: AnalysisPlan[];
  signals: SessionSignals;
  tags: AnalysisTags;
  outputs: AnalysisOutputs;
  recaps?: AnalysisRecaps;
  live?: AnalysisLive;
  /** 마지막 활동: 지금부터 몇 분 전. 기본은 마지막 턴이 끝난 시각이다. */
  lastActivityAgo?: number;
  costSource?: CostSource;
  unattached?: number;
  planFlags?: PlanFlags;
  largeWindow?: boolean;
  /** 질문 한 번에 들어 있는 서브에이전트 수. 모르면 null. */
  subagentsRunning?: number | null;
}

/** 턴 명세에서 분석 응답(`SessionAnalysisFull`)과 타임라인용 글을 만든다. 입력 목록과 입력 수는 항상 턴과 답에서 파생한다. */
const baseOf = (v: VariantBase): { base: SessionAnalysisFull; extras: Record<string, TurnTimelineExtra> } => {
  const { turns, extras } = turnsOf(v.specs);
  const inputs = inputsOf(turns, v.answers ?? []);
  const base = ready({
    session_id: v.id,
    agent: v.agent ?? 'claude',
    model: v.model ?? 'claude-opus-5',
    branch: v.branch,
    project: v.project,
    login_emails: [v.user.login_email],
    title: v.title,
    name: v.name,
    category: v.category,
    turns,
    inputs,
    inputs_count: inputs.length,
    cost_source: v.costSource ?? 'otel',
    unattached_usd: v.unattached ?? 0.04,
    large_window_seen: v.largeWindow ?? false,
    subagents_running: v.subagentsRunning === undefined ? 0 : v.subagentsRunning,
    active_seconds: Math.round(v.specs.reduce((sum, s) => sum + s.took, 0) * 60),
    tags: v.tags,
    plan_flags: v.planFlags ?? NO_PLAN_FLAGS,
    signals: v.signals,
    outputs: v.outputs,
    recaps: v.recaps ?? NO_RECAPS,
    questions: v.questions ?? [],
    plans: v.plans ?? [],
    ...(v.live ? { live: v.live } : {}),
    ...(v.lastActivityAgo !== undefined ? { last_activity_ago: v.lastActivityAgo } : {}),
    computed_ago: 1,
  });
  return { base, extras };
};

/* ============================================================
   1. ALL_PASS — 판정 다섯이 모두 통과, 질문 카드(여러 개 선택과 자유 응답), 빈 task 목록
   ============================================================ */

export const ALL_PASS_ID = 'a9d3f610-5b27-4e8c-9f41-c7d2a6e03b58';

export const allPass = (): DetailFixture => {
  const user = USERS.yoon;
  const question: AnalysisQuestion = {
    tool_use_id: 'toolu_ap_q1',
    turn_key: 'ap1',
    ts: ago(205.4),
    status: 'answered',
    state: 'observed',
    items: [
      {
        header: '포함 범위',
        question: '노트에 넣을 변경 종류를 모두 고르세요.',
        multi_select: true,
        options: [
          { label: '기능 추가', description: 'feat 커밋을 "새 기능"으로 묶습니다.', recommended: true },
          { label: '버그 수정', description: 'fix 커밋을 "고친 것"으로 묶습니다.', recommended: false },
          { label: '문서', description: 'docs 커밋을 "문서"로 묶습니다.', recommended: false },
          { label: '내부 정리', description: 'chore·refactor 커밋까지 보입니다. 사용자에게는 보통 필요하지 않습니다.', recommended: false },
        ],
        answer: { labels: ['기능 추가', '버그 수정'], free_text: null },
      },
      {
        header: '형식',
        question: '노트를 어떤 파일 형식으로 남길까요?',
        multi_select: false,
        options: [
          { label: '마크다운 한 파일', description: '`CHANGELOG.md`에 위에서부터 쌓습니다. 가장 단순합니다.', recommended: true },
          { label: '버전별 파일', description: '릴리스마다 파일을 하나씩 만듭니다. 링크하기 좋지만 파일이 늘어납니다.', recommended: false },
        ],
        answer: { labels: [], free_text: 'CHANGELOG.md 한 파일에 쌓되, 버전 제목에 날짜를 함께 적어 주세요' },
      },
    ],
  };
  const { base, extras } = baseOf({
    id: ALL_PASS_ID,
    user,
    project: PROJECTS.atlas,
    branch: 'chore/release-notes',
    title: { text: '릴리스 노트 자동 생성 스크립트 정리', source: 'ai', state: 'ok' },
    name: { text: 'release-notes-script', source: 'derived' },
    category: { key: 'ended', as_of: ago(94.9) },
    specs: [
      {
        key: 'ap1',
        text: '릴리스 노트를 커밋 메시지에서 자동으로 만들고 싶어',
        prompt: '릴리스 노트를 커밋 메시지에서 자동으로 만들고 싶어.\n지금은 배포할 때마다 `git log`를 보며 손으로 정리하는데 자꾸 빠집니다.\n\n스크립트 하나로 CHANGELOG에 쌓이게 해줘.',
        at: 206,
        took: 14,
        tokens: 2_400_000,
        usd: 0.71,
        ctx: 98_000,
        tools: 31,
        questionHeaders: [{ tool_use_id: 'toolu_ap_q1', headers: ['포함 범위', '형식'], status: 'answered' }],
        edited: edited(['notes.mjs', 'CHANGELOG.md']),
        progress: ['최근 릴리스의 커밋 메시지 형식부터 확인해 보겠습니다.'],
        reply: '커밋 메시지를 읽어 `CHANGELOG.md`에 쌓는 스크립트(`scripts/release/notes.mjs`)를 만들었습니다.',
      },
      { key: 'ap2', text: '분류 규칙은 feat, fix, docs만 쓰자', at: 175, took: 5, tokens: 1_100_000, usd: 0.3, ctx: 104_000, tools: 12, edited: edited(['notes.mjs']), reply: '세 분류만 쓰도록 줄였습니다. 나머지 커밋은 노트에서 빠집니다.' },
      { key: 'ap3', text: '한국어 제목으로 묶어줘', at: 152, took: 8, tokens: 1_600_000, usd: 0.42, ctx: 112_000, tools: 17, edited: edited(['notes.mjs']), reply: '분류마다 한국어 제목("새 기능", "고친 것", "문서")으로 묶었습니다.' },
      { key: 'ap4', text: '스크립트에 테스트도 붙여줘', at: 130, took: 9, tokens: 1_900_000, usd: 0.51, ctx: 121_000, tools: 22, edited: edited(['notes.test.mjs', 'notes.mjs']), reply: '분류, 묶음, 빈 릴리스 세 경우를 다루는 테스트를 붙였습니다.' },
      { key: 'ap5', text: '정리하고 커밋해줘', at: 104.5, took: 9.55, tokens: 800_000, usd: 0.2, ctx: 126_000, tools: 8, edited: edited(['release.md', 'CHANGELOG.md']), reply: '문서에 사용법을 적고 커밋했습니다.' },
    ],
    answers: [{ turn_key: 'ap1', ts: ago(203) }],
    questions: [question],
    signals: { turn: S.turn('ended'), question: S.question(0), background: S.background(0), uncommitted: S.uncommitted(0), todo: S.todo(0) },
    tags: tagsOf('scripts/release', 'implementation', [['AGENTS.md', 2]]),
    outputs: outputsOf(
      [
        ['scripts/release/notes.mjs', 6],
        ['scripts/release/notes.test.mjs', 3],
        ['CHANGELOG.md', 2],
        ['docs/release.md', 1],
      ],
      [312, 18],
    ),
    lastActivityAgo: 94.95,
  });

  const record = recordOf(
    {
      headline: '커밋 메시지로 릴리스 노트를 만드는 스크립트를 넣었다',
      bullets: [
        { text: '`scripts/release/notes.mjs`가 커밋 메시지를 읽어 `CHANGELOG.md`에 위에서부터 쌓는다', commits: ['a7e2c40'] },
        { text: 'feat·fix·docs 세 분류만 쓰고 분류마다 한국어 제목으로 묶는다', commits: ['d91b3f8'] },
        { text: '분류, 묶음, 빈 릴리스를 다루는 테스트와 사용법 문서를 붙였다', commits: ['5c08ae2'] },
      ],
      review: [
        { kind: 'decision', text: '노트에는 feat와 fix만 넣고 형식은 CHANGELOG.md 한 파일로 한다(사용자가 질문에서 고름)' },
        { kind: 'interpretation', text: '"한국어 제목"을 분류 제목에만 적용하고 커밋 제목은 원문 그대로 둔 것으로 읽었다' },
      ],
      tags: { kinds: ['implementation'], components: ['Node.js'], screens: ['문서 사이트'], related: [{ kind: 'task', id: 'task-77' }] },
      work: [
        workOf('w1', 'request', true, '릴리스 노트를 커밋 메시지에서 자동으로 만들어 달라', { turn: 'ap1', commits: ['a7e2c40'] }),
        workOf('w2', 'consultation', true, '노트에 넣을 변경 종류와 파일 형식', { turn: 'ap1', decision: 'feat·fix만 넣고 CHANGELOG.md 한 파일에 쌓는다' }),
        workOf('w3', 'followup', true, '분류 규칙은 feat, fix, docs 셋만 쓰라', { turn: 'ap2', commits: ['d91b3f8'] }),
        workOf('w4', 'followup', true, '분류마다 한국어 제목으로 묶어 달라', { turn: 'ap3', commits: ['d91b3f8'] }),
        workOf('w5', 'followup', true, '스크립트에 테스트도 붙여 달라', { turn: 'ap4', commits: ['5c08ae2'] }),
        workOf('w6', 'finishing', true, '정리하고 커밋한다', { turn: 'ap5', commits: ['5c08ae2'] }),
      ],
    },
    { generatedAgo: 96, coversAgo: 95 },
  );

  return finalize({
    base,
    meta: { session_id: ALL_PASS_ID, ...user },
    label: '판정 다섯이 모두 통과한 세션. 질문 카드(여러 개 선택, 자유 응답), task 갱신 없음, 함께 끝난 묶음의 하나.',
    covers: ['판정 전부 통과', '완료', '기록 ready', '여러 개 선택 질문', '자유 응답', 'task 없음', '함께 끝난 묶음'],
    identities: identitiesOf(user),
    alive: false,
    record,
    commits: observedCommits([
      commitOf({ short: 'a7e2c40', subject: 'feat(release): 커밋 메시지로 릴리스 노트를 만드는 스크립트 추가', at: 191, turn: 'ap1', repository: 'acme/atlas-docs', branch: 'chore/release-notes' }),
      commitOf({ short: 'd91b3f8', subject: 'feat(release): feat·fix·docs 분류와 한국어 제목 묶음', at: 144, turn: 'ap3', repository: 'acme/atlas-docs', branch: 'chore/release-notes' }),
      commitOf({ short: '5c08ae2', subject: 'test(release): 분류와 묶음 테스트, 사용법 문서', at: 95.4, turn: 'ap5', repository: 'acme/atlas-docs', branch: 'chore/release-notes' }),
    ]),
    tasks: { items: [] },
    note: '릴리스 노트의 breaking change 표시 방식은 다음 릴리스 전에 팀과 정할 것.\n지금은 feat와 fix만 모은다.',
    classification: 'done',
    extras,
  });
};

/* ============================================================
   2. LEFTOVER — 커밋됨과 작업 내용 완료가 실패, 해시를 모르는 커밋
   ============================================================ */

export const LEFTOVER_ID = '12b8e4c7-9a35-4d60-b1f8-3e07c5a9d246';

export const leftover = (): DetailFixture => {
  const user = USERS.jung;
  const { base, extras } = baseOf({
    id: LEFTOVER_ID,
    user,
    project: PROJECTS.ledger,
    branch: 'fix/settlement-retry',
    title: { text: '정산 배치 재시도 로그 정리', source: 'ai', state: 'ok' },
    name: null,
    category: { key: 'ended', as_of: ago(94.9) },
    specs: [
      {
        key: 'lp1',
        text: '정산 배치가 실패했을 때 재시도 로그가 중복으로 남아요',
        prompt: '정산 배치가 실패했을 때 재시도 로그가 중복으로 남아요.\n같은 배치가 세 번 도는 것처럼 보이는데 실제로는 한 번만 도는 것 같습니다.\n\n원인부터 찾아줘.',
        at: 238,
        took: 14,
        tokens: 3_100_000,
        usd: 0.88,
        ctx: 120_000,
        tools: 34,
        edited: edited(['retry-log.ts']),
        reply: '로그 키에 배치 ID가 빠져 있어서 재시도마다 새 줄이 생겼습니다. 실제 배치 실행은 한 번이 맞습니다.',
      },
      { key: 'lp2', text: '중복 키를 배치 ID 기준으로 막아줘', at: 214, took: 11, tokens: 2_200_000, usd: 0.61, ctx: 128_000, tools: 25, edited: edited(['retry-log.ts', 'retry-log.test.ts']), reply: '로그 키에 배치 ID를 넣고 같은 키는 한 줄로 합쳤습니다. 테스트를 더했습니다.' },
      { key: 'lp3', text: '로그 보존 기간 설정도 따로 빼줘', at: 160, took: 22, tokens: 2_700_000, usd: 0.74, ctx: 141_000, tools: 29, edited: edited(['settings.ts', 'retention.ts']), reply: '보존 기간 설정을 분리하는 중입니다. 설정 파일은 나눴지만 마이그레이션은 아직 쓰지 않았습니다.' },
      { key: 'lp4', text: '실패 알림 메일도 같은 키로 한 번만 가게 해줘', at: 108, took: 13.1, tokens: 1_500_000, usd: 0.42, ctx: 150_000, tools: 18, edited: edited(['notify.ts']), reply: '알림 메일의 중복 방지는 키만 정해 두었고 발송 쪽 연결은 하지 못했습니다.' },
    ],
    signals: { turn: S.turn('ended'), question: S.question(0), background: S.background(0), uncommitted: S.uncommitted(3, 48), todo: S.todo(0) },
    tags: tagsOf('src/settlement', 'debugging', [['AGENTS.md', 4]]),
    outputs: outputsOf(
      [
        ['src/settlement/retry-log.ts', 5],
        ['src/settlement/retry-log.test.ts', 3],
        ['src/settlement/retention.ts', 2],
        ['src/settlement/settings.ts', 2],
        ['src/notify/notify.ts', 1],
        ['docs/settlement.md', 1],
      ],
      [247, 58],
    ),
    lastActivityAgo: 94.9,
  });

  const record = recordOf(
    {
      headline: '정산 배치 재시도 로그의 중복을 막았고, 보존 기간과 알림 중복은 남았다',
      bullets: [
        { text: '원인은 로그 키에 배치 ID가 빠져 재시도마다 새 줄이 생긴 것이었다. 배치는 한 번만 돈다', commits: ['e3a70b1'] },
        { text: '`retry-log.ts`가 배치 ID를 키로 쓰고 같은 키는 한 줄로 합친다', commits: ['e3a70b1'] },
        { text: '보존 기간 설정은 파일만 나눴고 마이그레이션이 없다. 알림 메일의 중복 방지는 키만 정했다', commits: [] },
      ],
      review: [
        { kind: 'decision', text: '재시도 로그의 중복은 배치 ID를 키로 막는다' },
        { kind: 'unverified', text: '알림 메일을 실제 발송 경로에서 한 번만 보내는지는 확인하지 못했다' },
      ],
      tags: { kinds: ['bugfix'], components: ['정산 배치'], screens: [], related: [{ kind: 'task', id: 'task-210' }] },
      work: [
        workOf('w1', 'request', true, '정산 배치 실패 시 재시도 로그가 중복으로 남는 원인을 찾아 달라', { turn: 'lp1' }),
        workOf('w2', 'consultation', true, '중복을 어느 키로 막을지', { turn: 'lp1', decision: '배치 ID를 키로 쓴다' }),
        workOf('w3', 'followup', true, '중복 키를 배치 ID 기준으로 막아 달라', { turn: 'lp2', commits: ['e3a70b1'], tasks: ['task-210'] }),
        workOf('w4', 'followup', false, '로그 보존 기간 설정을 따로 빼 달라', { turn: 'lp3', note: '설정 파일은 나눴지만 마이그레이션을 아직 쓰지 않음' }),
        workOf('w5', 'followup', false, '실패 알림 메일도 같은 키로 한 번만 가게 해 달라', { turn: 'lp4', note: '발송 쪽 연결이 남음' }),
      ],
    },
    { generatedAgo: 96, coversAgo: 94.9 },
  );

  return finalize({
    base,
    meta: { session_id: LEFTOVER_ID, ...user },
    label: '남은 것이 있는 세션. 커밋됨(미커밋 파일 3)과 작업 내용 완료(미완 작업 2건)가 실패하고, 해시를 모르는 커밋이 있다.',
    covers: ['판정 실패 2', '작업 도중', '해시 불명 커밋', 'task 갱신', '함께 끝난 묶음'],
    identities: identitiesOf(user),
    alive: false,
    record,
    commits: observedCommits([
      commitOf({ short: 'e3a70b1', subject: 'fix(settlement): 재시도 로그를 배치 ID 키로 중복 제거', at: 203, turn: 'lp2', tasks: ['task-210'], repository: 'acme/ledger-service', branch: 'fix/settlement-retry' }),
      commitOf({ short: null, subject: 'wip: 로그 보존 기간 설정 분리', at: 140, turn: 'lp3', repository: 'acme/ledger-service', branch: 'fix/settlement-retry' }),
    ]),
    tasks: {
      items: [
        {
          id: 'task-210',
          kind: 'task',
          title: '정산 재시도 로그 중복 제거',
          actions: { added: false, completed: false, edited: true, commit_linked: true },
          summary: '재시도 로그의 중복을 배치 ID 키로 막는다. 보존 기간 설정과 알림 중복은 이 task의 남은 일이다',
        },
      ],
    },
    classification: 'later',
    extras,
  });
};

/* ============================================================
   3. PROCESSING — 살아 있고 일하는 중(범주 running), 턴이 열려 있다
   ============================================================ */

export const PROCESSING_ID = 'e50c7a93-1d68-4b2f-8a04-96c3f1d7e5b2';

export const processing = (): DetailFixture => {
  const user = USERS.baek;
  const { base, extras } = baseOf({
    id: PROCESSING_ID,
    user,
    project: PROJECTS.relay,
    branch: 'feat/rate-limit-rules',
    title: { text: 'API 게이트웨이 요청 제한 규칙 추가', source: 'ai', state: 'ok' },
    name: { text: 'rate-limit-rules', source: 'auto' },
    category: { key: 'running', as_of: ago(0.3) },
    specs: [
      { key: 'pr1', text: '게이트웨이에 클라이언트별 요청 제한 규칙을 넣고 싶어요', at: 62, took: 12, tokens: 2_100_000, usd: 0.58, ctx: 76_000, tools: 26, edited: edited(['limiter.go', 'rules.go']), reply: '토큰 버킷 방식의 제한기(`limiter.go`)와 규칙 로더를 만들었습니다.' },
      { key: 'pr2', text: '규칙은 YAML로 읽게 하고 잘못된 규칙은 시작할 때 막아줘', at: 45, took: 9, tokens: 1_400_000, usd: 0.39, ctx: 88_000, tools: 19, edited: edited(['rules.go', 'rules_test.go']), reply: '시작할 때 규칙을 검증하고, 잘못된 규칙이 있으면 줄 번호와 함께 종료합니다.' },
      { key: 'pr3', text: '제한에 걸리면 429와 Retry-After를 돌려줘', at: 27, took: 7, tokens: 900_000, usd: 0.26, ctx: 94_000, tools: 11, edited: edited(['middleware.go']), reply: '429 응답에 `Retry-After`를 붙였습니다.' },
      { key: 'pr4', text: '지금 규칙을 부하 테스트로 확인해 줘', at: 9, took: 8.5, tokens: 700_000, usd: 0.19, ctx: 99_000, tools: 9, status: 'open', reply: null, progress: ['부하 테스트 스크립트를 만들어 돌리는 중입니다.'] },
    ],
    signals: { turn: S.turn('open'), question: S.question(0), background: S.backgroundUnknown('redacted'), uncommitted: S.uncommitted(2, 12), todo: S.todo(1) },
    tags: tagsOf('internal/gateway', 'implementation', [['AGENTS.md', 3]]),
    outputs: outputsOf(
      [
        ['internal/gateway/limiter.go', 4],
        ['internal/gateway/rules.go', 4],
        ['internal/gateway/rules_test.go', 2],
        ['internal/gateway/middleware.go', 2],
      ],
      [418, 36],
    ),
    live: {
      instances: [{
        status: 'busy', waiting_kind: '', heartbeat_at: ago(0.2), name: 'rate-limit-rules', stale: false,
        // 미리보기 합성값. 실제 클라이언트의 프로세스 측정이 아니다.
        process: { pid: 28734, input_lines: 154, output_lines: 1298, input_bytes: 2048, output_bytes: 2_621_440, io_source: 'process_stream' },
      }],
      collector: { heartbeat_at: ago(0.2), last_stored_at: ago(0.4) },
    },
    lastActivityAgo: 0.5,
    subagentsRunning: 1,
  });

  const record = recordOf(
    {
      headline: '게이트웨이에 클라이언트별 요청 제한을 넣는 중이다',
      bullets: [
        { text: '토큰 버킷 제한기와 YAML 규칙 로더를 만들었고 잘못된 규칙은 시작할 때 막는다', commits: ['b60d4e9'] },
        { text: '제한에 걸리면 `429`와 `Retry-After`를 돌려준다. 부하 테스트로 확인하는 중이다', commits: [] },
      ],
      review: [{ kind: 'unverified', text: '부하 테스트가 끝나지 않아 제한이 실제 부하에서 맞게 걸리는지는 확인하지 못했다' }],
      tags: { kinds: ['implementation'], components: ['Go'], screens: [], related: [{ kind: 'feature', id: 'feature-52' }] },
      work: [
        workOf('w1', 'request', true, '게이트웨이에 클라이언트별 요청 제한 규칙을 넣어 달라', { turn: 'pr1', commits: ['b60d4e9'] }),
        workOf('w2', 'followup', true, '규칙은 YAML로 읽고 잘못된 규칙은 시작할 때 막아 달라', { turn: 'pr2', commits: ['b60d4e9'] }),
        workOf('w3', 'followup', true, '제한에 걸리면 429와 Retry-After를 돌려 달라', { turn: 'pr3' }),
        workOf('w4', 'followup', false, '지금 규칙을 부하 테스트로 확인해 달라', { turn: 'pr4', note: '진행 중' }),
      ],
    },
    { generatedAgo: 11, coversAgo: 27 },
  );

  return finalize({
    base,
    meta: { session_id: PROCESSING_ID, ...user },
    label: '지금 일하는 세션(관측 범주 진행 중). 턴이 열려 있어 턴 종료가 실패하고, 백그라운드 작업은 비식별화로 판단 불가.',
    covers: ['실행 중(처리 중)', '턴 열림', '판단 불가(redacted)', '미커밋 2', '실시간 busy'],
    identities: identitiesOf(user),
    alive: true,
    record,
    commits: observedCommits([
      commitOf({ short: 'b60d4e9', subject: 'feat(gateway): 토큰 버킷 제한기와 YAML 규칙 로더', at: 33, turn: 'pr2', repository: 'acme/relay-gateway', branch: 'feat/rate-limit-rules' }),
    ]),
    tasks: {
      items: [
        {
          id: 'task-318',
          kind: 'task',
          title: '게이트웨이 요청 제한',
          actions: { added: true, completed: false, edited: false, commit_linked: true },
          summary: '클라이언트별 요청 제한 규칙을 게이트웨이에 넣는다',
        },
      ],
    },
    classification: 'working',
    extras,
  });
};

/* ============================================================
   4. INTERRUPTED — 살아 있고 사용자가 마지막 턴을 중단했다, 거절한 질문 카드
   ============================================================ */

export const INTERRUPTED_ID = '67f1b2d8-0c93-4e5a-b7d4-2a81f6c09e35';

export const interrupted = (): DetailFixture => {
  const user = USERS.moon;
  const rejected: AnalysisQuestion = {
    tool_use_id: 'toolu_ip_q1',
    turn_key: 'ip4',
    ts: ago(5.2),
    status: 'rejected',
    state: 'observed',
    items: [
      {
        header: '적용 범위',
        question: '접근성 라벨을 온보딩 화면 전체에 한 번에 적용할까요?',
        multi_select: false,
        options: [
          { label: '온보딩 전체', description: '네 화면에 모두 적용합니다. 변경이 크지만 한 번에 끝납니다.', recommended: true },
          { label: '첫 화면만', description: '첫 화면에서 모양을 확인한 뒤 나머지에 적용합니다.', recommended: false },
        ],
        answer: null,
      },
    ],
  };
  const { base, extras } = baseOf({
    id: INTERRUPTED_ID,
    user,
    project: PROJECTS.orbit,
    branch: 'feat/onboarding-a11y',
    title: { text: '온보딩 화면 접근성 라벨 보강', source: 'ai', state: 'ok' },
    name: null,
    category: { key: 'idle', as_of: ago(0.6) },
    specs: [
      { key: 'ip1', text: '온보딩 첫 화면의 버튼에 접근성 라벨이 없어요', at: 88, took: 10, tokens: 1_300_000, usd: 0.36, ctx: 70_000, tools: 17, edited: edited(['WelcomeView.swift']), reply: '버튼 세 개에 읽는 이름을 붙였습니다.' },
      { key: 'ip2', text: '이미지에는 장식인지 설명인지 구분해서 달아줘', at: 70, took: 8, tokens: 1_100_000, usd: 0.3, ctx: 78_000, tools: 14, edited: edited(['WelcomeView.swift', 'Assets.swift']), reply: '장식 이미지는 숨기고 설명이 필요한 이미지에만 라벨을 달았습니다.' },
      { key: 'ip3', text: '다이나믹 타입으로 키워도 레이아웃이 안 깨지는지 봐줘', at: 44, took: 12, tokens: 1_900_000, usd: 0.52, ctx: 90_000, tools: 21, edited: edited(['WelcomeView.swift']), reply: '가장 큰 글꼴에서도 줄바꿈으로 잘 들어갑니다. 한 곳은 최소 높이를 풀었습니다.' },
      { key: 'ip4', text: '나머지 세 화면에도 같은 방식으로 적용해줘', at: 8, took: 3.6, tokens: 600_000, usd: 0.17, ctx: 94_000, tools: 6, status: 'interrupted', questionHeaders: [{ tool_use_id: 'toolu_ip_q1', headers: ['적용 범위'], status: 'rejected' }], reply: null },
    ],
    questions: [rejected],
    signals: { turn: S.turn('interrupted'), question: S.question(0), background: S.background(0), uncommitted: S.uncommitted(1, 20), todo: S.todoUnknown('no_list') },
    tags: tagsOf('Sources/Onboarding', 'implementation', [['AGENTS.md', 1]]),
    outputs: outputsOf(
      [
        ['Sources/Onboarding/WelcomeView.swift', 6],
        ['Sources/Onboarding/Assets.swift', 1],
      ],
      [64, 9],
    ),
    live: {
      instances: [{ status: 'idle', waiting_kind: '', heartbeat_at: ago(0.6), name: 'onboarding-a11y', stale: false }],
      collector: { heartbeat_at: ago(0.5), last_stored_at: ago(0.9) },
    },
    lastActivityAgo: 4.4,
  });

  const record = recordOf(
    {
      headline: '온보딩 첫 화면에 접근성 라벨을 붙였고, 나머지 화면은 중단됐다',
      bullets: [
        { text: '첫 화면 버튼 세 개와 이미지에 읽는 이름을 붙였다. 장식 이미지는 읽지 않게 숨겼다', commits: ['7d1a9c3'] },
        { text: '가장 큰 다이나믹 타입에서도 레이아웃이 유지된다. 나머지 세 화면에는 아직 적용하지 않았다', commits: [] },
      ],
      review: [{ kind: 'interpretation', text: '"같은 방식"을 첫 화면과 같은 라벨 규칙으로 읽었다. 적용 범위 질문은 사용자가 중단해 답하지 않았다' }],
      tags: { kinds: ['implementation'], components: ['SwiftUI'], screens: ['온보딩'], related: [] },
      work: [
        workOf('w1', 'request', true, '온보딩 첫 화면 버튼에 접근성 라벨을 달아 달라', { turn: 'ip1', commits: ['7d1a9c3'] }),
        workOf('w2', 'followup', true, '이미지에는 장식인지 설명인지 구분해 달아 달라', { turn: 'ip2', commits: ['7d1a9c3'] }),
        workOf('w3', 'followup', true, '다이나믹 타입으로 키워도 레이아웃이 유지되는지 봐 달라', { turn: 'ip3' }),
        workOf('w4', 'followup', false, '나머지 세 화면에도 같은 방식으로 적용해 달라', { turn: 'ip4', note: '사용자가 중단함' }),
      ],
    },
    { generatedAgo: 6, coversAgo: 8 },
  );

  return finalize({
    base,
    meta: { session_id: INTERRUPTED_ID, ...user },
    label: '사용자가 마지막 턴을 중단한 살아 있는 세션(중단 중). 거절한 질문 카드와 중단 안내가 타임라인에 있다.',
    covers: ['중단 중', '턴 중단', '거절한 질문', '판단 불가(no_list)', '실시간 idle'],
    identities: identitiesOf(user),
    alive: true,
    interrupted: true,
    record,
    commits: observedCommits([
      commitOf({ short: '7d1a9c3', subject: 'feat(onboarding): 첫 화면 버튼과 이미지에 접근성 라벨', at: 62, turn: 'ip2', repository: 'acme/orbit-ios', branch: 'feat/onboarding-a11y' }),
    ]),
    tasks: { items: [] },
    classification: 'unsorted',
    extras,
  });
};

/* ============================================================
   5. NO_RECORD — 세션 기록이 없어 작업 내용이 판단 불가(no_record), 커밋은 수집하지 않음
   ============================================================ */

export const NO_RECORD_ID = '9c24e8a1-7f05-4d39-a6b2-e4c18d9073f6';

export const noRecord = (): DetailFixture => {
  const user = USERS.yoon;
  const { base, extras } = baseOf({
    id: NO_RECORD_ID,
    user,
    project: PROJECTS.harbor,
    branch: 'docs/cli-help-copy',
    title: { text: 'CLI 도움말 문구 맞춤법 정리', source: 'ai', state: 'ok' },
    name: null,
    category: { key: 'recent', as_of: ago(0.5) },
    specs: [
      { key: 'nr1', text: '도움말 문구에서 맞춤법이 틀린 곳을 찾아줘', at: 85, took: 6, tokens: 700_000, usd: 0.2, ctx: 52_000, tools: 12, edited: edited(['help.go']), reply: '열한 곳을 찾아 고쳤습니다. 명령 이름은 건드리지 않았습니다.' },
      { key: 'nr2', text: '옵션 설명 끝에는 마침표를 통일해줘', at: 66, took: 4, tokens: 500_000, usd: 0.14, ctx: 58_000, tools: 8, edited: edited(['help.go']), reply: '설명 문장 끝에 마침표를 붙였습니다.' },
      { key: 'nr3', text: '됐어, 고마워', at: 41.5, took: 1.5, tokens: 120_000, usd: 0.04, ctx: 60_000, tools: 0, reply: '도움이 되었다니 다행입니다.' },
    ],
    signals: { turn: S.turn('ended'), question: S.question(0), background: S.background(0), uncommitted: S.uncommitted(0), todo: S.todo(0) },
    tags: tagsOf('cmd/harbor', 'documentation', [['AGENTS.md', 1]]),
    outputs: outputsOf([['cmd/harbor/help.go', 4]], [26, 26]),
    lastActivityAgo: 40,
  });
  return finalize({
    base,
    meta: { session_id: NO_RECORD_ID, ...user },
    label: '세션 기록이 없는 세션. 작업 내용 칩이 "작업 내용 미기입"(no_record)이고, 커밋은 수집하지 않는 클라이언트라 모름이다.',
    covers: ['기록 none', '작업 내용 미기입', '커밋 수집 안 함', '상태 판정 불가', '버킷 null'],
    identities: identitiesOf(user),
    alive: false,
    record: NO_RECORD,
    commits: NOT_COLLECTED_COMMITS,
    tasks: { items: [] },
    classification: 'consulting',
    extras,
  });
};

/* ============================================================
   6~10. 세션 기록의 상태: stale, generating, withheld, redacted, failed
   ============================================================ */

export const RECORD_STALE_ID = '38d0f5b6-c4a9-4e17-8d02-b5f7a1e69c40';

export const recordStale = (): DetailFixture => {
  const user = USERS.jung;
  const { base, extras } = baseOf({
    id: RECORD_STALE_ID,
    user,
    project: PROJECTS.ledger,
    branch: 'feat/close-report-csv',
    title: { text: '월말 마감 리포트 CSV 내보내기', source: 'ai', state: 'ok' },
    name: { text: 'close-report-csv', source: 'derived' },
    category: { key: 'recent', as_of: ago(0.5) },
    specs: [
      { key: 'rs1', text: '월말 마감 리포트를 CSV로 내보내는 버튼을 넣어줘', at: 140, took: 11, tokens: 2_000_000, usd: 0.55, ctx: 84_000, tools: 24, edited: edited(['export.ts', 'ReportPage.tsx']), reply: '리포트 화면에 CSV 내보내기 버튼과 `export.ts`를 만들었습니다.' },
      { key: 'rs2', text: '열 이름은 한국어로 하고 금액은 천 단위 쉼표 없이', at: 118, took: 6, tokens: 1_000_000, usd: 0.28, ctx: 92_000, tools: 12, edited: edited(['export.ts']), reply: '열 이름은 한국어이고 금액은 쉼표 없는 숫자입니다.' },
      { key: 'rs3', text: 'UTF-8 BOM을 붙여서 엑셀에서 안 깨지게 해줘', at: 88, took: 5, tokens: 800_000, usd: 0.22, ctx: 97_000, tools: 9, edited: edited(['export.ts']), reply: 'BOM을 붙여 엑셀에서 한글이 깨지지 않습니다.' },
      { key: 'rs4', text: '행이 만 건이 넘으면 나눠서 내려받게 해줘', at: 47, took: 9, tokens: 1_700_000, usd: 0.46, ctx: 105_000, tools: 19, edited: edited(['export.ts', 'ReportPage.tsx']), reply: '만 건마다 파일을 나눠 압축해서 내려줍니다.' },
      { key: 'rs5', text: '나눠 받을 때 파일 이름에 순번을 붙여줘', at: 28, took: 3, tokens: 600_000, usd: 0.17, ctx: 109_000, tools: 6, edited: edited(['export.ts']), reply: '`마감리포트_2026-09_01.csv`처럼 순번을 붙입니다.' },
    ],
    signals: { turn: S.turn('ended'), question: S.question(0), background: S.background(0), uncommitted: S.uncommitted(0), todo: S.todo(0) },
    tags: tagsOf('src/reports', 'implementation', [['AGENTS.md', 4]]),
    outputs: outputsOf(
      [
        ['src/reports/export.ts', 8],
        ['src/reports/ReportPage.tsx', 3],
        ['src/reports/export.test.ts', 2],
      ],
      [236, 41],
    ),
    lastActivityAgo: 25,
  });
  const record = recordOf(
    {
      headline: '월말 마감 리포트를 CSV로 내보내는 기능을 넣었다',
      bullets: [
        { text: '리포트 화면에 CSV 내보내기 버튼과 `export.ts`를 만들었다', commits: ['2f6b8d1'] },
        { text: '열 이름은 한국어, 금액은 쉼표 없는 숫자이고 UTF-8 BOM을 붙여 엑셀에서 깨지지 않는다', commits: ['2f6b8d1'] },
      ],
      review: [{ kind: 'interpretation', text: '"쉼표 없이"를 천 단위 구분 기호에만 적용하고 CSV 구분자는 쉼표로 둔 것으로 읽었다' }],
      tags: { kinds: ['implementation'], components: ['React'], screens: ['마감 리포트'], related: [{ kind: 'task', id: 'task-305' }] },
      work: [
        workOf('w1', 'request', true, '월말 마감 리포트를 CSV로 내보내는 버튼을 넣어 달라', { turn: 'rs1', commits: ['2f6b8d1'] }),
        workOf('w2', 'followup', true, '열 이름은 한국어로, 금액은 천 단위 쉼표 없이', { turn: 'rs2', commits: ['2f6b8d1'] }),
        workOf('w3', 'followup', true, 'UTF-8 BOM을 붙여 엑셀에서 안 깨지게 해 달라', { turn: 'rs3' }),
      ],
    },
    { state: 'stale', generatedAgo: 66, coversAgo: 70 },
  );
  return finalize({
    base,
    meta: { session_id: RECORD_STALE_ID, ...user },
    label: '세션 기록이 낡은 세션(stale). 기록은 70분 전까지의 대화만 반영하고 그 뒤 두 요청은 담기지 않았다.',
    covers: ['기록 stale', '반영 시각 이전 활동', '작업 상황은 기록 기준', '완료'],
    identities: identitiesOf(user),
    alive: false,
    record,
    commits: observedCommits([
      commitOf({ short: '2f6b8d1', subject: 'feat(reports): 마감 리포트 CSV 내보내기', at: 108, turn: 'rs2', tasks: ['task-305'], repository: 'acme/ledger-service', branch: 'feat/close-report-csv' }),
      commitOf({ short: '90c3e7a', subject: 'feat(reports): 만 건 단위로 나눠 내려받기', at: 38, turn: 'rs4', tasks: ['task-305'], repository: 'acme/ledger-service', branch: 'feat/close-report-csv' }),
    ]),
    tasks: {
      items: [
        {
          id: 'task-305',
          kind: 'task',
          title: '마감 리포트 CSV',
          actions: { added: false, completed: false, edited: true, commit_linked: true },
          summary: '월말 마감 리포트를 CSV로 내보낸다',
        },
      ],
    },
    note: '마감 리포트 열 이름은 회계팀 용어에 맞춰 한 번 더 확인할 것.\nCSV 인코딩 문의의 답변을 기다리는 중.',
    classification: 'working',
    extras,
  });
};

export const RECORD_GENERATING_ID = 'b7a1c943-2e60-4f8d-9b15-d03a7c6e8f21';

export const recordGenerating = (): DetailFixture => {
  const user = USERS.baek;
  const { base, extras } = baseOf({
    id: RECORD_GENERATING_ID,
    user,
    project: PROJECTS.relay,
    branch: 'refactor/webhook-signature',
    title: { text: '웹훅 서명 검증 모듈 분리', source: 'ai', state: 'ok' },
    name: null,
    category: { key: 'idle', as_of: ago(0.4) },
    specs: [
      { key: 'rg1', text: '웹훅 서명 검증 코드를 별도 모듈로 분리해줘', at: 38, took: 9, tokens: 1_500_000, usd: 0.42, ctx: 72_000, tools: 22, edited: edited(['signature.go', 'handler.go']), reply: '검증 로직을 `signature` 패키지로 옮기고 핸들러는 그것만 부르게 했습니다.' },
      { key: 'rg2', text: '공급자별 서명 방식은 인터페이스 뒤로 숨겨줘', at: 20, took: 7, tokens: 1_100_000, usd: 0.3, ctx: 80_000, tools: 15, edited: edited(['signature.go', 'provider.go']), reply: '`Verifier` 인터페이스를 두고 공급자마다 구현을 하나씩 뒀습니다.' },
      { key: 'rg3', text: '기존 테스트가 다 통과하는지 확인해줘', at: 9, took: 5.5, tokens: 500_000, usd: 0.14, ctx: 84_000, tools: 7, edited: edited([]), reply: '기존 테스트 마흔두 개가 모두 통과했습니다.' },
    ],
    signals: { turn: S.turn('ended'), question: S.question(0), background: S.background(0), uncommitted: S.uncommitted(0), todo: S.todo(0) },
    tags: tagsOf('internal/webhook', 'refactoring', [['AGENTS.md', 3]]),
    outputs: outputsOf(
      [
        ['internal/webhook/signature.go', 5],
        ['internal/webhook/provider.go', 2],
        ['internal/webhook/handler.go', 2],
      ],
      [188, 142],
    ),
    live: {
      instances: [{
        status: 'idle', waiting_kind: '', heartbeat_at: ago(0.4), name: 'webhook-signature', stale: false,
        // 미리보기 합성값. 대화 기록량이며 실제 프로세스 스트림 양이 아니다.
        process: { pid: null, input_lines: 42, output_lines: 318, input_bytes: 12288, output_bytes: 92160, io_source: 'transcript' },
      }],
      collector: { heartbeat_at: ago(0.3), last_stored_at: ago(0.5) },
    },
    lastActivityAgo: 3.5,
  });
  return finalize({
    base,
    meta: { session_id: RECORD_GENERATING_ID, ...user },
    label: '세션 기록을 만드는 중인 살아 있는 세션(generating). 작업 상황이 아직 비어 있고 작업 내용은 판단 불가다.',
    covers: ['기록 generating', '작업 내용 미기입', '실행 중(쉬는 중)', '실시간 idle'],
    identities: identitiesOf(user),
    alive: true,
    record: { state: 'generating', generated_at: null, covers_until: null },
    commits: observedCommits([
      commitOf({ short: '3ea91c5', subject: 'refactor(webhook): 서명 검증을 signature 패키지로 분리', at: 29, turn: 'rg1', repository: 'acme/relay-gateway', branch: 'refactor/webhook-signature' }),
      commitOf({ short: 'c82f40b', subject: 'refactor(webhook): 공급자별 Verifier 인터페이스', at: 12.5, turn: 'rg2', repository: 'acme/relay-gateway', branch: 'refactor/webhook-signature' }),
    ]),
    tasks: { items: [] },
    classification: 'unsorted',
    extras,
  });
};

export const RECORD_WITHHELD_ID = '04e6d2f9-8b13-4c75-a0e9-7d5f2b4c1a68';

export const recordWithheld = (): DetailFixture => {
  const user = USERS.moon;
  const { base, extras } = baseOf({
    id: RECORD_WITHHELD_ID,
    user,
    project: PROJECTS.orbit,
    branch: 'fix/checkout-loading',
    title: { text: '결제 화면 로딩 상태 정리', source: 'ai', state: 'ok' },
    name: null,
    category: { key: 'recent', as_of: ago(0.5) },
    specs: [
      { key: 'rw1', text: '결제 화면에서 로딩 중에 버튼을 두 번 누를 수 있어요', at: 330, took: 8, tokens: 1_200_000, usd: 0.33, ctx: 66_000, tools: 16, edited: edited(['CheckoutView.swift']), reply: '로딩 중에는 버튼을 비활성화했습니다.' },
      { key: 'rw2', text: '로딩 표시는 스피너 대신 스켈레톤으로 바꿔줘', at: 305, took: 10, tokens: 1_400_000, usd: 0.38, ctx: 74_000, tools: 18, edited: edited(['CheckoutView.swift', 'Skeleton.swift']), reply: '결제 요약 영역에 스켈레톤을 넣었습니다.' },
      { key: 'rw3', text: '실패하면 다시 시도 버튼을 보여줘', at: 270, took: 7, tokens: 900_000, usd: 0.25, ctx: 79_000, tools: 11, edited: edited(['CheckoutView.swift']), reply: '실패 화면에 "다시 시도"를 넣고 같은 요청을 다시 보냅니다.' },
    ],
    signals: { turn: S.turn('ended'), question: S.question(0), background: S.background(0), uncommitted: S.uncommitted(0), todo: S.todo(0) },
    tags: tagsOf('Sources/Checkout', 'bugfix', [['AGENTS.md', 1]]),
    outputs: outputsOf(
      [
        ['Sources/Checkout/CheckoutView.swift', 7],
        ['Sources/Checkout/Skeleton.swift', 1],
      ],
      [97, 33],
    ),
    lastActivityAgo: 263,
  });
  return finalize({
    base,
    meta: { session_id: RECORD_WITHHELD_ID, ...user },
    label: '세션 기록을 만들지 않은 세션(withheld). AI 입력 동의가 없어 보류됐고 커밋은 관측한 0건이다.',
    covers: ['기록 withheld', '커밋 관측 0건', '작업 내용 미기입'],
    identities: identitiesOf(user),
    alive: false,
    record: { state: 'withheld', generated_at: null, covers_until: null },
    commits: observedCommits([]),
    tasks: { items: [] },
    classification: 'later',
    extras,
  });
};

export const RECORD_REDACTED_ID = 'f812a5c0-3d97-4e26-b8a1-6c0e9f4d7253';

export const recordRedacted = (): DetailFixture => {
  const user = USERS.yoon;
  const { base, extras } = baseOf({
    id: RECORD_REDACTED_ID,
    user,
    project: PROJECTS.atlas,
    branch: 'main',
    title: { text: null, source: 'none', state: 'redacted' },
    name: null,
    category: { key: 'inactive', as_of: ago(0.5) },
    specs: [
      { key: 'rr1', text: null, at: 1_840, took: 9, tokens: 1_600_000, usd: 0.44, ctx: 62_000, tools: 14, reply: '요청을 처리했습니다.' },
      { key: 'rr2', text: null, at: 1_815, took: 6, tokens: 900_000, usd: 0.25, ctx: 68_000, tools: 9, reply: '반영했습니다.' },
      { key: 'rr3', text: '이 한 줄만 가려지지 않았다', at: 1_790, took: 5, tokens: 700_000, usd: 0.2, ctx: 72_000, tools: 7, reply: '확인했습니다.' },
    ],
    signals: {
      turn: S.turn('ended'),
      question: S.questionUnknown('redacted'),
      background: S.background(0),
      uncommitted: S.uncommittedUnknown('no_git'),
      todo: S.todoUnknown('redacted'),
    },
    tags: { change_area: { value: null, state: 'redacted' }, task_type: null, rules_files: [] },
    outputs: {
      edited_files: { state: 'redacted', active_total: 6, abandoned_total: 0, truncated: false, items: [] },
      lines: null,
      prs: { items: [], truncated: false },
    },
    lastActivityAgo: 1_785,
  });
  // 프롬프트 비식별화: 앞의 두 턴은 첫 줄 자체가 가려졌다(미리보기 상태 redacted).
  const redactedTurns = base.turns.map((t, i) => (i < 2 ? { ...t, preview: { text: null, state: 'redacted' as const } } : t));
  return finalize({
    base: { ...base, turns: redactedTurns },
    meta: { session_id: RECORD_REDACTED_ID, ...user },
    label: '대화 본문이 가려진 세션(redacted). 제목과 첫 줄이 가려졌고, 세션 기록을 만들 수 없으며, 커밋은 판단 불가다.',
    covers: ['기록 redacted', '제목 redacted', '첫 줄 redacted', '커밋 판단 불가', '질문·할 일 판단 불가(redacted)', '24시간 이상 활동 없음'],
    identities: identitiesOf(user),
    alive: false,
    record: { state: 'redacted', generated_at: null, covers_until: null },
    commits: UNDETERMINABLE_COMMITS,
    tasks: { items: [] },
    classification: 'unsorted',
    extras,
  });
};

export const RECORD_FAILED_ID = '2ad59e37-6b08-4a91-9e4c-f1d82c7a05b3';

export const recordFailed = (): DetailFixture => {
  const user = USERS.jung;
  const { base, extras } = baseOf({
    id: RECORD_FAILED_ID,
    user,
    project: PROJECTS.ledger,
    branch: 'fix/fx-cache-expiry',
    title: { text: '환율 캐시 만료 처리', source: 'ai', state: 'ok' },
    name: null,
    category: { key: 'recent', as_of: ago(0.5) },
    specs: [
      { key: 'rf1', text: '환율 캐시가 만료돼도 오래된 값을 계속 쓰는 것 같아', at: 72, took: 10, tokens: 1_700_000, usd: 0.47, ctx: 70_000, tools: 20, edited: edited(['fx-cache.ts']), reply: '만료 시각 비교가 초 단위와 밀리초 단위로 섞여 있었습니다. 단위를 맞췄습니다.' },
      { key: 'rf2', text: '만료 직후 요청이 몰릴 때 한 번만 갱신하게 해줘', at: 52, took: 9, tokens: 1_300_000, usd: 0.36, ctx: 78_000, tools: 15, edited: edited(['fx-cache.ts', 'fx-cache.test.ts']), reply: '갱신 중인 요청을 하나로 합쳐 나머지는 그 결과를 기다리게 했습니다.' },
      { key: 'rf3', text: '테스트도 돌려서 확인해줘', at: 34, took: 4, tokens: 600_000, usd: 0.16, ctx: 82_000, tools: 6, reply: '캐시 테스트 열여덟 개가 모두 통과했습니다.' },
    ],
    signals: { turn: S.turn('ended'), question: S.question(0), background: S.background(0), uncommitted: S.uncommitted(0), todo: S.todo(0) },
    tags: tagsOf('src/fx', 'debugging', [['AGENTS.md', 4]]),
    outputs: outputsOf(
      [
        ['src/fx/fx-cache.ts', 5],
        ['src/fx/fx-cache.test.ts', 2],
      ],
      [73, 19],
    ),
    lastActivityAgo: 30,
  });
  return finalize({
    base,
    meta: { session_id: RECORD_FAILED_ID, ...user },
    label: '세션 기록 생성에 실패한 세션(failed). 마지막 시도 시각만 남아 있고 작업 내용은 판단 불가다.',
    covers: ['기록 failed', '작업 내용 미기입', '커밋 2건'],
    identities: identitiesOf(user),
    alive: false,
    record: { state: 'failed', generated_at: ago(18), covers_until: null },
    commits: observedCommits([
      commitOf({ short: 'f0a37d2', subject: 'fix(fx): 캐시 만료 시각 비교의 단위를 맞춤', at: 62, turn: 'rf1', repository: 'acme/ledger-service', branch: 'fix/fx-cache-expiry' }),
      commitOf({ short: '6b94c1e', subject: 'fix(fx): 만료 직후 갱신 요청을 하나로 합침', at: 43, turn: 'rf2', repository: 'acme/ledger-service', branch: 'fix/fx-cache-expiry' }),
    ]),
    tasks: { items: [] },
    classification: 'consulting',
    extras,
  });
};

/* ============================================================
   11. ENDED_MIDWAY — 살아 있지 않고 마지막 응답이 오류로 끝났다
   ============================================================ */

export const ENDED_MIDWAY_ID = '75c0b8e2-9f41-4d07-a3b6-8e2d51c4f970';

export const endedMidway = (): DetailFixture => {
  const user = USERS.baek;
  const { base, extras } = baseOf({
    id: ENDED_MIDWAY_ID,
    user,
    project: PROJECTS.relay,
    branch: 'feat/deploy-rollback',
    title: { text: '배포 스크립트 롤백 단계 추가', source: 'ai', state: 'ok' },
    name: null,
    category: { key: 'ended', as_of: ago(95) },
    specs: [
      { key: 'em1', text: '배포 스크립트에 롤백 단계를 넣어줘', at: 160, took: 12, tokens: 2_000_000, usd: 0.55, ctx: 92_000, tools: 25, edited: edited(['deploy.sh', 'rollback.sh']), reply: '이전 릴리스 링크로 되돌리는 `rollback.sh`를 만들었습니다.' },
      { key: 'em2', text: '롤백 전에 헬스체크 실패를 확인하게 해줘', at: 131, took: 8, tokens: 1_200_000, usd: 0.33, ctx: 101_000, tools: 14, edited: edited(['deploy.sh']), reply: '헬스체크가 두 번 연속 실패해야 롤백합니다.' },
      { key: 'em3', text: '롤백 결과를 슬랙으로 알려줘', at: 104.4, took: 9.4, tokens: 900_000, usd: 0.26, ctx: 108_000, tools: 10, status: 'error', reply: null },
    ],
    signals: { turn: S.turn('error'), question: S.question(0), background: S.background(0), uncommitted: S.uncommitted(0), todo: S.todo(0) },
    tags: tagsOf('scripts/deploy', 'implementation', [['AGENTS.md', 3]]),
    outputs: outputsOf(
      [
        ['scripts/deploy/deploy.sh', 4],
        ['scripts/deploy/rollback.sh', 3],
      ],
      [91, 12],
    ),
    lastActivityAgo: 95,
  });
  const record = recordOf(
    {
      headline: '배포 스크립트에 롤백 단계를 넣었고, 슬랙 알림은 오류로 끊겼다',
      bullets: [
        { text: '`rollback.sh`가 이전 릴리스 링크로 되돌린다. 헬스체크가 두 번 연속 실패해야 롤백한다', commits: ['8a2d6f4'] },
        { text: '롤백 결과를 슬랙으로 알리는 단계는 API 오류로 응답이 끝나 시작하지 못했다', commits: [] },
      ],
      review: [{ kind: 'unverified', text: '슬랙 알림 단계는 만들지 못해 롤백 결과가 사람에게 전달되는지는 확인하지 못했다' }],
      tags: { kinds: ['implementation'], components: ['Bash'], screens: [], related: [] },
      work: [
        workOf('w1', 'request', true, '배포 스크립트에 롤백 단계를 넣어 달라', { turn: 'em1', commits: ['8a2d6f4'] }),
        workOf('w2', 'followup', true, '롤백 전에 헬스체크 실패를 확인하게 해 달라', { turn: 'em2', commits: ['8a2d6f4'] }),
        workOf('w3', 'followup', false, '롤백 결과를 슬랙으로 알려 달라', { turn: 'em3', note: 'API 오류로 끝남' }),
      ],
    },
    { generatedAgo: 94, coversAgo: 95 },
  );
  return finalize({
    base,
    meta: { session_id: ENDED_MIDWAY_ID, ...user },
    label: '턴이 끝나기 전에 멈춘 세션(도중 종료). 마지막 응답이 API 오류로 끝났고, 함께 끝난 묶음의 하나다.',
    covers: ['도중 종료', '턴 오류', '작업 내용 실패(1건)', '함께 끝난 묶음'],
    identities: identitiesOf(user),
    alive: false,
    record,
    commits: observedCommits([
      commitOf({ short: '8a2d6f4', subject: 'feat(deploy): 롤백 스크립트와 헬스체크 기준', at: 124, turn: 'em2', repository: 'acme/relay-gateway', branch: 'feat/deploy-rollback' }),
    ]),
    tasks: { items: [] },
    classification: 'unsorted',
    extras,
  });
};

/* ============================================================
   12. CODEX — 신호는 턴 하나, 나머지는 해당 없음
   ============================================================ */

export const CODEX_ID = '1e9f4d06-a572-4c38-b9e1-0d6a3f8c7b24';

export const codex = (): DetailFixture => {
  const user = USERS.moon;
  const { base, extras } = baseOf({
    id: CODEX_ID,
    user,
    project: PROJECTS.harbor,
    branch: 'docs/log-filter-help',
    agent: 'codex',
    model: 'gpt-6-astra',
    title: { text: '로그 필터 옵션 설명 보강', source: 'first_request', state: 'ok' },
    name: null,
    category: { key: 'recent', as_of: ago(0.5) },
    costSource: 'estimate',
    specs: [
      { key: 'cx1', text: '로그 명령의 필터 옵션 설명이 너무 짧아요', at: 125, took: 7, tokens: 380_000, cost: estimated(0.11), ctx: 22_000, window: 258_400, tools: 6, reply: '옵션마다 한 줄 설명과 예시를 넣겠습니다. 먼저 현재 도움말을 확인했습니다.' },
      { key: 'cx2', text: '--since 값의 상대 표기 예시도 적어줘', at: 100, took: 5, tokens: 260_000, cost: estimated(0.08), ctx: 28_000, window: 258_400, tools: 4, reply: '`2h`, `3d` 같은 예시를 설명에 넣었습니다.' },
      { key: 'cx3', text: '--until도 같은 형식으로 맞춰줘', at: 74, took: 4, tokens: 240_000, cost: estimated(0.07), ctx: 31_000, window: 258_400, tools: 3, reply: '`--until`도 같은 형식으로 맞췄습니다.' },
      { key: 'cx4', text: '도움말 출력이 80열을 넘지 않는지 봐줘', at: 52, took: 4, tokens: 210_000, cost: estimated(0.06), ctx: 34_000, window: 258_400, tools: 3, reply: '가장 긴 줄이 78열이라 넘지 않습니다.' },
    ],
    signals: { turn: S.turn('ended') },
    tags: { change_area: { value: null, state: 'observed' }, task_type: { value: 'documentation', share: 0.75, basis: 'classifier' }, rules_files: [{ path: 'AGENTS.md', version: 2, discovered_at: ago(60 * 24 * 9) }] },
    outputs: {
      edited_files: { state: 'observed', active_total: null, abandoned_total: null, truncated: false, items: [] },
      lines: null,
      prs: { items: [], truncated: false },
    },
    recaps: { state: 'none', items: [], absent_reason: null },
    unattached: 0.02,
    subagentsRunning: null,
    lastActivityAgo: 48,
  });
  const record = recordOf(
    {
      headline: '로그 명령의 필터 옵션 설명을 보강했다',
      bullets: [
        { text: '옵션마다 한 줄 설명과 예시를 넣었고 `--since`와 `--until`은 같은 상대 표기(`2h`, `3d`)를 쓴다', commits: [] },
        { text: '도움말 출력의 가장 긴 줄은 78열이라 80열을 넘지 않는다', commits: [] },
      ],
      review: [{ kind: 'unverified', text: 'Codex 세션은 편집한 파일을 수집하지 않아 실제로 어느 파일이 바뀌었는지는 기록으로 확인하지 못했다' }],
      tags: { kinds: ['maintenance'], components: ['harbor CLI'], screens: [], related: [] },
      work: [
        workOf('w1', 'request', true, '로그 명령의 필터 옵션 설명을 보강해 달라', { turn: 'cx1' }),
        workOf('w2', 'followup', true, '--since 값의 상대 표기 예시를 적어 달라', { turn: 'cx2' }),
        workOf('w3', 'followup', true, '--until도 같은 형식으로 맞춰 달라', { turn: 'cx3' }),
        workOf('w4', 'followup', true, '도움말 출력이 80열을 넘지 않는지 확인해 달라', { turn: 'cx4' }),
      ],
    },
    { generatedAgo: 46, coversAgo: 48 },
  );
  return finalize({
    base,
    meta: { session_id: CODEX_ID, ...user },
    label: 'Codex 세션. 신호는 턴 하나뿐이라 답 대기·백그라운드·미커밋 판정은 해당 없음(판단 불가)이고, 작업 내용만 기록으로 정해진다.',
    covers: ['Codex', '판정 해당 없음(not_applicable)', '커밋 수집 안 함', '추정 비용', 'task 없음'],
    identities: identitiesOf(user),
    alive: false,
    record,
    commits: NOT_COLLECTED_COMMITS,
    tasks: { items: [] },
    classification: 'unsorted',
    extras,
  });
};

/* ============================================================
   13. UNKNOWN_STATUS — 턴이 끝났는지 판단할 수 없어 상태가 "판정 불가"(수집 규칙 이전의 기록)
   ============================================================ */

export const UNKNOWN_STATUS_ID = 'e5b1c07a-3d94-4f28-a6e1-9c70d2f8b413';

export const unknownStatus = (): DetailFixture => {
  const user = USERS.jung;
  const { base, extras } = baseOf({
    id: UNKNOWN_STATUS_ID,
    user,
    project: PROJECTS.orbit,
    branch: 'chore/legacy-sync',
    title: { text: '옛 동기화 코드 정리', source: 'ai', state: 'ok' },
    name: null,
    category: { key: 'inactive', as_of: ago(0.5) },
    specs: [
      {
        key: 'us1',
        text: '옛 동기화 코드에서 쓰지 않는 분기를 찾아줘',
        at: 60 * 30,
        took: 9,
        tokens: 900_000,
        usd: 0.26,
        ctx: 70_000,
        status: 'undeterminable',
        reason: 'pre_collect',
        tools: 14,
        edited: edited(['sync/legacy.swift']),
        reply: null,
      },
      {
        key: 'us2',
        text: '그 분기들을 지워줘',
        at: 60 * 30 - 15,
        took: 6,
        tokens: 600_000,
        usd: 0.17,
        ctx: 74_000,
        status: 'undeterminable',
        reason: 'pre_collect',
        tools: 9,
        edited: edited(['sync/legacy.swift', 'sync/queue.swift']),
        reply: null,
      },
    ],
    signals: {
      turn: S.turnUnknown('pre_collect'),
      question: S.questionUnknown('pre_collect'),
      background: S.backgroundUnknown('pre_collect'),
      uncommitted: S.uncommittedUnknown('no_git'),
      todo: S.todoUnknown('no_list'),
    },
    tags: tagsOf('sync', 'refactor', [['AGENTS.md', 1]]),
    outputs: outputsOf(
      [
        ['sync/legacy.swift', 3],
        ['sync/queue.swift', 1],
      ],
      null,
    ),
  });
  return finalize({
    base,
    meta: { session_id: UNKNOWN_STATUS_ID, ...user },
    label: '수집 규칙 이전의 기록이라 턴이 끝났는지 판단할 수 없는 세션. 상태가 "판정 불가"이고 판정 칩이 모두 회색 점선이다.',
    covers: ['상태 판정 불가', '신호 판단 불가(pre_collect, no_git)', '기록 none', '버킷 null'],
    identities: identitiesOf(user),
    alive: false,
    record: NO_RECORD,
    commits: NOT_COLLECTED_COMMITS,
    tasks: { items: [] },
    classification: 'unsorted',
    extras,
  });
};

/** 변형을 작성한 순서로. */
export const variantSessions = (): DetailFixture[] => [
  allPass(),
  leftover(),
  processing(),
  interrupted(),
  noRecord(),
  recordStale(),
  recordGenerating(),
  recordWithheld(),
  recordRedacted(),
  recordFailed(),
  endedMidway(),
  codex(),
  unknownStatus(),
];
