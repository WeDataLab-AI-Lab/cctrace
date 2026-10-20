/**
 * 세션 분석 미리보기의 합성 픽스처. 제목, 경로, 요청 문장, 이메일(example.com)은 모두 지어낸 것이고
 * 실제 대화에서 가져온 것이 하나도 없다. 서버가 아직 없으므로 계약(`lib/session-analysis-types`)대로 만든
 * 값을 화면 컴포넌트에 먹이는 용도다.
 *
 * 열여섯 개 세션이 계약의 모든 상태를 한 번 이상 보인다. 각 항목의 `covers`가 무엇을 보이는지 적는다.
 * 합계는 턴에서 파생(builders.ts)하므로 합계 불변식은 구성상 성립한다. 계약이 함께 나올 수 없다고 한 조합
 * (복사된 입력의 턴은 요청이 아님, 범주와 시각, 변경 영역 규칙, 상한과 전체 수 등)은 fixtures.test.ts가 확인한다.
 */

import type {
  AnalysisIdentities,
  AnalysisInput,
  AnalysisPlan,
  AnalysisQuestion,
  PullRequest,
  SessionAnalysis,
  SessionAnalysisPending,
  SessionCategoryCounts,
  SessionCategoryKey,
  SessionRowAnalysis,
} from '@/lib/session-analysis-types';
import type { SessionOverview } from '@/lib/types';
import {
  PREVIEW_NOW,
  ago,
  asNonOwnerAdminView,
  asNonOwnerRow,
  ctxModelDefault,
  ctxObserved,
  ctxTokensOnly,
  estimated,
  inputsOf,
  otel,
  ready,
  rowAnalysisOf,
  rowOf,
  text,
  tok,
  turn,
} from './builders';

export interface SessionFixture {
  id: string;
  /** 이 세션이 보여 주는 것. 미리보기 화면의 설명 띠에 쓴다. */
  label: string;
  covers: string[];
  row: SessionOverview;
  /** 분석 API의 응답. 404인 세션(목록에서 `withheld`)은 null. */
  analysis: SessionAnalysis | null;
  /** 비소유 관리자가 볼 때의 신원 정보(관리자 응답의 `identities`). */
  identities: AnalysisIdentities;
}

export const USERS = {
  kim: { user_id: 'usr-5c1f02', profile_email: 'dev.kim@example.com', login_email: 'kim.work@example.com' },
  park: { user_id: 'usr-91ad7e', profile_email: 'park.minseo@example.com', login_email: 'park.work@example.com' },
  choi: { user_id: 'usr-30be58', profile_email: 'choi.junho@example.com', login_email: 'choi.work@example.com' },
  lee: { user_id: 'usr-e4720c', profile_email: 'lee.harin@example.com', login_email: 'lee.work@example.com' },
  han: { user_id: 'usr-7a19d3', profile_email: 'han.seoyun@example.com', login_email: 'han.work@example.com' },
};

export const PROJECTS = {
  billing: { hash: '-Users-dev-work-billing-api', name: 'billing-api' },
  docs: { hash: '-Users-dev-work-docs-site', name: 'docs-site' },
  mobile: { hash: '-Users-dev-work-mobile-app', name: 'mobile-app' },
  pipeline: { hash: '-Users-dev-work-data-pipeline', name: 'data-pipeline' },
  infra: { hash: '-Users-dev-work-infra-tools', name: 'infra-tools' },
  tokens: { hash: '-Users-dev-work-design-tokens', name: 'design-tokens' },
};

const rulesFiles = (claude: number, agents: number) => [
  { path: 'CLAUDE.md', version: claude, discovered_at: ago(60 * 24 * 9) },
  { path: 'AGENTS.md', version: agents, discovered_at: ago(60 * 24 * 21) },
];

const identitiesOf = (u: { user_id: string; profile_email: string; login_email: string }, mixed = false): AnalysisIdentities => ({
  user_ids: mixed ? [u.user_id, 'usr-ffee01'] : [u.user_id],
  profile_emails: mixed ? [u.profile_email, 'ex.member@example.com'] : [u.profile_email],
  login_emails: [u.login_email],
  mixed,
});

/* ============================================================
   A. 풍부한 Claude 세션 — 응답 대기, OTEL 비용, 모든 턴 상태
   ============================================================ */

const A_ID = 'ad704e10-3b5f-4c1e-9a62-0f8d1c7be201';

const PR_412: PullRequest = {
  url: 'https://git.example.com/acme/billing-api/pull/412',
  number: 412,
  repository: 'acme/billing-api',
  first_seen_at: ago(3),
  turn_key: 'ta16',
};
const PR_409: PullRequest = {
  url: 'https://git.example.com/acme/billing-api/pull/409',
  number: 409,
  repository: 'acme/billing-api',
  first_seen_at: ago(112),
  turn_key: 'ta07',
};

const aTurns = [
  // 수집 규칙 이전(P0 이전)에 쌓인 앞부분: 턴 상태를 판단할 수 없고, 비용은 어느 호출이 이 턴의 것인지 알 수 없다.
  turn({
    key: 'ta01', no: 1, at: 188, took: 9, request_no: 1, status: 'undeterminable', status_reason: 'pre_collect',
    preview: text('웹훅 재시도가 중복 결제를 만드는 것 같아. 원인부터 찾아줘'),
    api_requests: 12, tokens: tok(38_200, 5_400, 1_120_000, 84_000),
    cost: { usd: null, source: 'unattributable', reason: 'pre_p0' },
    context: ctxTokensOnly(96_400), tools: { calls: 31, subagents: 2 },
  }),
  turn({
    key: 'ta02', no: 2, at: 171, took: 6, request_no: 2, images: 2,
    preview: text('이 로그 캡처 보고 재현 조건 정리해줘'),
    api_requests: 9, tokens: tok(21_400, 3_900, 980_000, 41_000), cost: otel(0.52),
    context: ctxObserved(142_000, 1_000_000), tools: { calls: 19, subagents: 0 },
    question_headers: [{ tool_use_id: 'tu_a_q1', headers: ['재현 범위'], status: 'answered' }],
  }),
  // 명령 턴: 커스텀 명령이 모델에게 일을 시켜 실제 응답이 있으므로 요청이다(§3.9. 응답 없는 /clear·/model은 요청이 아니다).
  // 뒤 입력 턴과 prompt_id를 공유해 비용을 나눌 수 없다.
  turn({
    key: 'ta03', no: 3, at: 150, took: 1.5, kind: 'command', request_no: 3,
    preview: text('/review-diff 결제 웹훅 변경분'),
    api_requests: 3, tokens: tok(2_400, 640, 180_000, 6_000),
    cost: { usd: null, source: 'unattributable', reason: 'shared_prompt_id' },
    context: ctxObserved(168_000, 1_000_000), tools: { calls: 4, subagents: 0 },
  }),
  turn({
    key: 'ta04', no: 4, at: 149, took: 8, request_no: 4, mode: 'plan', denied: true,
    preview: text('재시도 큐를 idempotency key 기반으로 바꾸는 계획을 세워줘'),
    api_requests: 6, tokens: tok(18_600, 4_800, 640_000, 52_000),
    cost: { usd: null, source: 'unattributable', reason: 'shared_prompt_id' },
    context: ctxObserved(190_000, 1_000_000), tools: { calls: 11, subagents: 0 },
  }),
  turn({
    key: 'ta06', no: 6, at: 131, took: 5, request_no: 5, mode: 'plan',
    preview: text('큐 말고 DB 락으로 가자. 계획 다시 짜줘'),
    api_requests: 5, tokens: tok(14_300, 3_600, 710_000, 29_000), cost: otel(0.61),
    context: ctxObserved(205_000, 1_000_000), tools: { calls: 9, subagents: 0 },
  }),
  turn({
    key: 'ta07', no: 7, at: 118, took: 12, request_no: 6,
    preview: text('락 범위는 결제 건 단위로 줄여서 진행해줘'),
    api_requests: 17, tokens: tok(52_000, 11_800, 2_460_000, 96_000), cost: otel(1.38),
    context: ctxObserved(262_000, 1_000_000), tools: { calls: 44, subagents: 3 },
    edited: { count: 3, sample: ['src/payments/lock.ts', 'src/webhooks/retry.ts', 'src/webhooks/handler.ts'], abandoned: 0 },
    prs: [PR_409],
  }),
  // 백그라운드 알림: 요청이 아니라 알림으로 열린 턴.
  turn({
    key: 'ta08', no: 8, at: 104, took: 0.5, kind: 'notification', is_request: false,
    preview: text('백그라운드 작업 완료: 통합 테스트 스위트 (exit 0)'),
    api_requests: 2, tokens: tok(2_100, 600, 280_000, 0), cost: otel(0.07),
    context: ctxObserved(266_000, 1_000_000), tools: { calls: 2, subagents: 0 },
  }),
  turn({
    key: 'ta10', no: 10, at: 96, took: 18, request_no: 7, status: 'interrupted',
    preview: text('락 적용하고 통합 테스트까지 돌려줘'),
    api_requests: 21, tokens: tok(61_000, 14_200, 3_300_000, 112_000), cost: otel(1.92),
    context: ctxObserved(310_000, 1_000_000), tools: { calls: 58, subagents: 4 },
    edited: {
      count: 4,
      sample: ['src/payments/lock.ts', 'src/payments/ledger.ts', 'test/payments/lock.test.ts', 'test/webhooks/retry.test.ts'],
      abandoned: 0,
    },
    question_headers: [{ tool_use_id: 'tu_a_q2', headers: ['테스트 DB'], status: 'unanswered' }],
  }),
  turn({
    key: 'ta11', no: 11, at: 90, took: 2, kind: 'queued', request_no: 8,
    preview: text('참, 로그 레벨은 debug로 유지해줘'),
    api_requests: 4, tokens: tok(5_200, 1_000, 330_000, 0), cost: otel(0.23),
    context: ctxObserved(318_000, 1_000_000), tools: { calls: 3, subagents: 0 },
  }),
  turn({
    key: 'ta12', no: 12, at: 76, took: 1, request_no: 9, status: 'error',
    preview: text('마이그레이션 스크립트도 만들어줘'),
    api_requests: 2, tokens: tok(3_800, 120, 190_000, 0), cost: otel(0.03),
    context: ctxObserved(322_000, 1_000_000), tools: { calls: 1, subagents: 0 },
  }),
  turn({
    key: 'ta13', no: 13, at: 61, took: 0.2, request_no: 10, status: 'no_response',
    preview: text('그럼 롤백 절차도 문서로 남겨줘'),
  }),
  turn({
    key: 'ta15', no: 15, at: 40, took: 15, request_no: 11,
    preview: text('배포 순서와 롤백 범위를 정해줘'),
    api_requests: 8, tokens: tok(19_000, 4_100, 940_000, 38_000), cost: otel(0.58),
    context: ctxObserved(352_000, 1_000_000), tools: { calls: 12, subagents: 0 },
    edited: { count: 2, sample: ['docs/runbook/rollback.md', 'scripts/rollback.sh'], abandoned: 0 },
    question_headers: [{ tool_use_id: 'tu_a_q3', headers: ['배포 방식'], status: 'rejected' }],
  }),
  // 지금 열려 있는 턴: 플랜 승인(권한)을 기다린다.
  turn({
    key: 'ta16', no: 16, at: 4, took: 3, request_no: 12, status: 'open', mode: 'plan',
    preview: text('승인되면 바로 PR까지 올려줘'),
    api_requests: 11, tokens: tok(24_000, 5_600, 1_480_000, 61_000), cost: otel(0.88),
    context: ctxObserved(520_000, 1_000_000), tools: { calls: 22, subagents: 1 },
    edited: { count: 3, sample: ['src/payments/lock.ts', 'src/webhooks/retry.ts', 'migrations/20261006_lock_table.sql'], abandoned: 0 },
    prs: [PR_412],
  }),
];

// 되감기로 버려진 분기. 요청 수와 컨텍스트에서는 빠지지만 비용·토큰은 세션 합계에 남는다.
const aAbandoned = [
  turn({
    key: 'ta05', no: 5, at: 141, took: 7, branch: 'abandoned',
    preview: text('이벤트 소싱으로 바꿔서 구현부터 시작해줘'),
    api_requests: 7, tokens: tok(16_900, 3_100, 520_000, 33_000), cost: otel(0.74),
    context: ctxObserved(168_000, 1_000_000), tools: { calls: 14, subagents: 1 },
    edited: { count: 2, sample: ['src/events/store.ts', 'src/events/replay.ts'], abandoned: 2 },
  }),
  turn({
    key: 'ta09', no: 9, at: 101, took: 3, branch: 'abandoned', status: 'interrupted',
    preview: text('락 없이 낙관적 동시성으로 해볼까?'),
    api_requests: 3, tokens: tok(6_400, 700, 210_000, 0), cost: otel(0.19),
    context: ctxObserved(240_000, 1_000_000), tools: { calls: 4, subagents: 0 },
  }),
  turn({
    key: 'ta14', no: 14, at: 58, took: 3, branch: 'abandoned',
    preview: text('롤백 문서 말고 코드 주석으로 정리해줘'),
    api_requests: 3, tokens: tok(7_800, 1_900, 260_000, 12_000), cost: otel(0.21),
    context: ctxObserved(330_000, 1_000_000), tools: { calls: 5, subagents: 0 },
  }),
];

const PLAN_QUEUE = [
  '## 재시도 큐를 idempotency key 기반으로 바꾸기',
  '',
  '1. 웹훅 수신 시 `event_id`로 idempotency key를 만든다.',
  '2. 재시도 큐에 같은 key가 있으면 새로 넣지 않는다.',
  '3. 처리 완료 시 key를 7일간 보관한다.',
  '',
  '위험: 큐가 비는 순간 같은 이벤트가 다시 들어오면 중복이 생길 수 있다.',
].join('\n');

const PLAN_LOCK = [
  '## 결제 건 단위 DB 락으로 재시도 직렬화',
  '',
  '### 변경',
  '1. `payments` 행에 `webhook_lock_until` 컬럼을 추가한다(마이그레이션 포함).',
  '2. 웹훅 핸들러는 `SELECT ... FOR UPDATE SKIP LOCKED`로 결제 건을 잡고, 못 잡으면 재시도 큐로 돌려보낸다.',
  '3. 락은 처리 시작 시 30초로 걸고, 끝나면 즉시 푼다. 프로세스가 죽어도 30초 뒤 자동으로 풀린다.',
  '4. 재시도 간격은 지수 백오프(1s, 4s, 16s)로 하고 최대 5회에서 멈춘다.',
  '',
  '### 검증',
  '- 같은 이벤트를 동시에 10번 보내도 결제가 한 번만 생기는지 통합 테스트로 확인한다.',
  '- 락 대기 중 프로세스를 죽였을 때 30초 뒤 재처리되는지 확인한다.',
  '- 기존 웹훅 테스트 48개가 그대로 통과해야 한다.',
  '',
  '### 위험',
  '- 락 시간이 길면 같은 결제 건의 후속 웹훅이 밀린다. 핸들러 안의 외부 호출에 타임아웃을 둔다.',
].join('\n');

const PLAN_ROLLOUT = [
  '## 배포 순서와 롤백',
  '',
  '1. 마이그레이션을 먼저 배포한다(컬럼 추가만, 기본값 NULL).',
  '2. 핸들러를 카나리 5%로 올리고 30분 지켜본다.',
  '3. 이상 없으면 100%로 올린다.',
  '',
  '롤백: 핸들러만 이전 버전으로 되돌린다. 컬럼은 남겨 둔다.',
].join('\n');

const aPlans: AnalysisPlan[] = [
  {
    turn_key: 'ta04', ts: ago(145), file_path: '/Users/alice/.claude/plans/idempotency-queue.md',
    decision: 'rejected', body: PLAN_QUEUE, state: 'observed',
  },
  {
    turn_key: 'ta06', ts: ago(128), file_path: '/Users/alice/.claude/plans/payment-lock.md',
    decision: 'approved', body: PLAN_LOCK, state: 'observed',
  },
  {
    turn_key: 'ta16', ts: ago(3), file_path: '/Users/alice/.claude/plans/rollout.md',
    decision: 'pending', body: PLAN_ROLLOUT, state: 'observed',
  },
];

const aQuestions: AnalysisQuestion[] = [
  {
    tool_use_id: 'tu_a_q1', turn_key: 'ta02', ts: ago(169), status: 'answered', state: 'observed',
    items: [
      {
        header: '재현 범위', question: '재현 환경은 어디까지 맞출까요?', multi_select: false,
        options: [
          { label: '로컬 도커', description: 'compose로 결제 목업까지 함께 띄웁니다', recommended: true },
          { label: '스테이징', description: '스테이징 웹훅 엔드포인트를 그대로 씁니다', recommended: false },
        ],
        answer: { labels: ['로컬 도커'], free_text: null },
      },
    ],
  },
  {
    tool_use_id: 'tu_a_q2', turn_key: 'ta10', ts: ago(80), status: 'unanswered', state: 'observed',
    items: [
      {
        header: '테스트 DB', question: '통합 테스트는 어느 DB로 돌릴까요?', multi_select: false,
        options: [
          { label: '임시 컨테이너', description: '테스트마다 Postgres 컨테이너를 새로 띄웁니다', recommended: true },
          { label: '공유 개발 DB', description: '느리지만 실제 스키마와 같습니다', recommended: false },
        ],
        answer: null,
      },
    ],
  },
  {
    tool_use_id: 'tu_a_q3', turn_key: 'ta15', ts: ago(33), status: 'rejected', state: 'observed',
    items: [
      {
        header: '배포 방식', question: '카나리로 배포할까요, 한 번에 올릴까요?', multi_select: false,
        options: [
          { label: '카나리 5%', description: '30분 지켜본 뒤 전체로 올립니다', recommended: true },
          { label: '한 번에', description: '바로 전체에 배포합니다', recommended: false },
        ],
        answer: null,
      },
    ],
  },
];

const aAnalysis = ready({
  session_id: A_ID,
  agent: 'claude',
  model: 'claude-opus-5',
  branch: 'fix/webhook-idempotency',
  project: PROJECTS.billing,
  login_emails: [USERS.kim.login_email],
  title: { text: '결제 웹훅 재시도 중복 처리 정리', source: 'ai', state: 'ok' },
  name: { text: 'webhook-retry-cleanup', source: 'auto' },
  category: { key: 'waiting', waiting_kind: 'permission', as_of: ago(0.2) },
  turns: aTurns,
  abandoned: aAbandoned,
  inputs_count: 14,
  cost_source: 'otel',
  unattached_usd: 1.35,
  unattributed: { unattributed_requests: 6, unattributed_tokens: 412_000 },
  large_window_seen: true,
  context_mix: { input: 24_000, cache_read: 480_000, cache_create: 16_000 },
  subagents_running: 1,
  active_seconds: 4_320,
  tags: {
    // 활성 편집이 가장 많은 앞 두 단계 폴더: src/webhooks 25회, src/payments 12회.
    change_area: { value: 'src/webhooks', state: 'observed' },
    task_type: { value: 'debugging', share: 0.45, basis: 'classifier' },
    rules_files: rulesFiles(7, 3),
  },
  plan_flags: { has_plan_file: true, used_plan_mode: true },
  // 관측과 판단 불가가 섞인 신호.
  signals: {
    turn: { state: 'observed', value: 'open' },
    question: { state: 'observed', value: 1, waiting_kind: 'permission' },
    background: { state: 'observed', value: 2, parts: { agents: 1, workflows: 0, shells: 1 } },
    uncommitted: { state: 'observed', value: 3, at_least: false, changed_at: ago(95), reevaluating: true },
    todo: { state: 'undeterminable', value: null, reason: 'no_list' },
  },
  outputs: {
    edited_files: {
      state: 'observed',
      active_total: 10,
      abandoned_total: 3,
      truncated: false,
      items: [
        { path: 'src/webhooks/retry.ts', edits: 14, subagent_edits: 3, abandoned_edits: 1 },
        { path: 'test/webhooks/retry.test.ts', edits: 11, subagent_edits: 4, abandoned_edits: 0 },
        { path: 'src/payments/lock.ts', edits: 9, subagent_edits: 2, abandoned_edits: 0 },
        { path: 'test/payments/lock.test.ts', edits: 7, subagent_edits: 2, abandoned_edits: 0 },
        { path: 'src/webhooks/handler.ts', edits: 6, subagent_edits: 0, abandoned_edits: 0 },
        { path: 'src/webhooks/idempotency.ts', edits: 5, subagent_edits: 1, abandoned_edits: 0 },
        { path: 'src/payments/ledger.ts', edits: 3, subagent_edits: 0, abandoned_edits: 0 },
        { path: 'migrations/20261006_lock_table.sql', edits: 2, subagent_edits: 0, abandoned_edits: 0 },
        { path: 'docs/runbook/rollback.md', edits: 2, subagent_edits: 0, abandoned_edits: 0 },
        { path: 'scripts/rollback.sh', edits: 1, subagent_edits: 0, abandoned_edits: 0 },
        { path: 'src/events/store.ts', edits: 0, subagent_edits: 0, abandoned_edits: 3 },
        { path: 'src/events/replay.ts', edits: 0, subagent_edits: 0, abandoned_edits: 2 },
      ],
    },
    lines: { added: 1_204, removed: 318, source: 'cost_state', partial: false },
    prs: { items: [PR_409, PR_412], truncated: false },
  },
  recaps: {
    state: 'observed',
    items: [
      {
        ts: ago(125),
        text: '재시도 큐가 같은 이벤트를 idempotency key 없이 두 번 처리해 중복 결제가 생기는 것으로 좁혔고, 큐 방식 대신 결제 건 단위 DB 락으로 방향을 정했습니다. 다음은 락 범위를 줄이는 구현입니다.',
        repeats: 1,
      },
      {
        ts: ago(55),
        text: '락 적용과 통합 테스트 일부를 마쳤습니다. 마이그레이션 스크립트를 쓰던 중 API 오류로 한 번 끊겼고, 롤백 문서는 코드 주석으로 바꾸기로 했습니다.',
        repeats: 2,
      },
    ],
    absent_reason: null,
  },
  inputs: inputsOf(aTurns, [{ turn_key: 'ta02', ts: ago(169) }]),
  questions: aQuestions,
  plans: aPlans,
  live: {
    instances: [{ status: 'waiting', waiting_kind: 'permission', heartbeat_at: ago(0.2), name: 'webhook-retry-cleanup', stale: false }],
    collector: { heartbeat_at: ago(0.4), last_stored_at: ago(1) },
  },
  computed_ago: 1,
  pending_ago: 3,
});

/* ============================================================
   B. 추정 비용 세션 — 이틀에 걸침, P0 이전 앞부분, 보류·가려진 미리보기, stale
   ============================================================ */

const B_ID = 'b19c42f7-8d3a-47e0-b6f1-5a2e9c40d7a3';

const bTurns = [
  turn({
    key: 'tb01', no: 1, at: 3_200, took: 4, request_no: 1,
    preview: text('검색 인덱스를 빌드하는 스크립트가 어디 있는지 찾아줘'),
    api_requests: 6, tokens: tok(12_000, 1_800, 310_000, 20_000), cost: estimated(0.21),
    context: ctxModelDefault(48_000, 200_000), tools: { calls: 12, subagents: 0 },
  }),
  turn({
    key: 'tb02', no: 2, at: 3_170, took: 6, request_no: 2,
    preview: { text: null, state: 'withheld' },
    api_requests: 9, tokens: tok(18_400, 2_900, 520_000, 31_000), cost: estimated(0.44),
    context: ctxModelDefault(72_000, 200_000), tools: { calls: 17, subagents: 0 },
  }),
  // 마지막 응답이 합성(API 오류) 줄이라 P0 이전 행으로는 끝났는지 알 수 없다.
  turn({
    key: 'tb03', no: 3, at: 3_120, took: 2, request_no: 3, status: 'undeterminable', status_reason: 'pre_collect',
    preview: { text: null, state: 'withheld' },
    api_requests: 3, tokens: tok(9_100, 400, 160_000, 0), cost: estimated(0.09),
    context: ctxModelDefault(81_000, 200_000), tools: { calls: 4, subagents: 0 },
  }),
  // 이 구간은 프롬프트 비식별화가 켜져 있어 마지막 응답의 내용을 읽을 수 없고, 끝 표식도 없다.
  turn({
    key: 'tb04', no: 4, at: 2_900, took: 5, request_no: 4, mode: 'plan', status: 'undeterminable', status_reason: 'redacted',
    preview: { text: null, state: 'redacted' },
    api_requests: 7, tokens: tok(15_200, 3_100, 480_000, 22_000), cost: estimated(0.38),
    context: ctxModelDefault(94_000, 200_000), tools: { calls: 13, subagents: 0 },
  }),
  turn({
    key: 'tb05', no: 5, at: 2_860, took: 3, request_no: 5,
    preview: text('인덱스 JSON 스키마 설명을 README에 추가해줘'),
    api_requests: 5, tokens: tok(11_800, 2_200, 350_000, 15_000), cost: estimated(0.29),
    context: ctxModelDefault(88_000, 200_000), tools: { calls: 9, subagents: 0 },
    edited: { count: 1, sample: ['README.md'], abandoned: 0 },
  }),
  // 사진만 붙인 요청. 요율을 모르는 모델이라 비용을 추정하지 못했다.
  turn({
    key: 'tb06', no: 6, at: 2_000, took: 4, request_no: 6, images: 1,
    preview: { text: null, state: 'none' },
    api_requests: 4, tokens: tok(8_900, 1_700, 240_000, 10_000),
    cost: { usd: null, source: 'no_rate', reason: null },
    context: ctxModelDefault(102_000, 200_000), tools: { calls: 6, subagents: 0 },
  }),
  turn({
    key: 'tb07', no: 7, at: 1_500, took: 3, request_no: 7,
    preview: text('마지막으로 린트 오류만 고쳐줘'),
    api_requests: 3, tokens: tok(6_300, 900, 410_000, 4_000),
    cost: { usd: 0.12, source: 'estimate_pending', reason: null },
    context: ctxModelDefault(143_000, 200_000), tools: { calls: 5, subagents: 0 },
    edited: { count: 2, sample: ['scripts/build-index.mjs', 'docs/search/schema.json'], abandoned: 0 },
  }),
];

const bAnalysis = ready({
  session_id: B_ID,
  state: 'stale',
  agent: 'claude',
  model: 'claude-haiku-4-5',
  branch: 'main',
  project: PROJECTS.docs,
  login_emails: [USERS.park.login_email],
  login_inferred: true,
  title: { text: '문서 사이트 검색 인덱스 재생성 스크립트', source: 'custom', state: 'ok' },
  name: null,
  category: { key: 'ended', as_of: ago(0.5) },
  turns: bTurns,
  cost_source: 'estimate_pending',
  unattached_usd: 0.06,
  large_window_seen: false,
  subagents_running: null,
  active_seconds: 1_860,
  tags: {
    change_area: { value: 'scripts', state: 'observed' },
    task_type: { value: 'documentation', share: 0.3, basis: 'classifier' },
    rules_files: [{ path: 'CLAUDE.md', version: 2, discovered_at: ago(60 * 24 * 30) }],
  },
  // 플랜 모드는 썼지만 플랜 파일은 없다: 두 사실은 따로다(계획 B6).
  plan_flags: { has_plan_file: false, used_plan_mode: true },
  // 수집 규칙 이전에 시작한 세션: 마지막 턴은 규칙 이후라 끝남을 관측했지만, 나머지 신호 대부분은 판단 불가다.
  signals: {
    turn: { state: 'observed', value: 'ended' },
    question: { state: 'observed', value: 0 },
    background: { state: 'undeterminable', value: null, reason: 'pre_collect', parts: null },
    uncommitted: { state: 'undeterminable', value: null, reason: 'no_git' },
    todo: { state: 'undeterminable', value: null, reason: 'no_list' },
  },
  outputs: {
    edited_files: {
      state: 'observed', active_total: 3, abandoned_total: 0, truncated: false,
      items: [
        { path: 'scripts/build-index.mjs', edits: 6, subagent_edits: 0, abandoned_edits: 0 },
        { path: 'docs/search/schema.json', edits: 2, subagent_edits: 0, abandoned_edits: 0 },
        { path: 'README.md', edits: 1, subagent_edits: 0, abandoned_edits: 0 },
      ],
    },
    // 줄 수 스냅샷이 없다: 0이 아니라 알 수 없음.
    lines: null,
    prs: { items: [], truncated: false },
  },
  recaps: { state: 'pre_collect', items: [], absent_reason: 'pre_collect' },
  inputs: inputsOf(bTurns),
  plans: [
    {
      turn_key: 'tb03', ts: ago(3_118), file_path: null,
      decision: 'interrupted', body: null, state: 'withheld',
    },
    {
      turn_key: 'tb04', ts: ago(2_896), file_path: null,
      decision: 'approved', body: null, state: 'redacted',
    },
  ],
  copies: { state: 'skipped_large_group', original_session_id: null, original_visible: false },
  computed_ago: 38,
  pending_ago: null,
});

/* ============================================================
   C. 가려진 세션 — 턴 경계 판단 불가, 제목 가려짐, 계정 둘, 원본을 볼 수 없는 복사본
   ============================================================ */

const C_ID = 'c5e0a8d3-62b1-4d97-8e04-9b13f7a6c2d5';

const cAnalysis = ready({
  session_id: C_ID,
  agent: 'claude',
  model: 'claude-opus-5',
  branch: 'feature/offline-sync',
  project: PROJECTS.mobile,
  login_emails: [USERS.choi.login_email, 'choi.personal@example.com'],
  title: { text: null, source: 'none', state: 'redacted' },
  name: null,
  category: { key: 'idle', as_of: ago(0.4) },
  // 사람 입력과 도구 결과를 가를 수 없어 턴을 만들지 않았다. 토큰·비용·활동 범위는 계산했다.
  turns: [],
  boundaries_undeterminable: true,
  started_ago: 640,
  last_activity_ago: 12,
  totals: { tokens: tok(214_000, 38_400, 9_700_000, 402_000), api_requests: 96, tool_calls: 87, usd: 3.12 },
  cost_source: 'otel',
  unattached_usd: null,
  large_window_seen: true,
  subagents_running: null,
  active_seconds: 11_400,
  tags: {
    change_area: { value: null, state: 'redacted' },
    task_type: null,
    rules_files: [{ path: 'CLAUDE.md', version: 4, discovered_at: ago(60 * 24 * 14) }],
  },
  plan_flags: { has_plan_file: false, used_plan_mode: false },
  signals: {
    turn: { state: 'undeterminable', value: null, reason: 'redacted' },
    question: { state: 'undeterminable', value: null, reason: 'redacted' },
    background: { state: 'undeterminable', value: null, reason: 'redacted', parts: null },
    uncommitted: { state: 'observed', value: 0, at_least: false, changed_at: ago(180), reevaluating: true },
    todo: { state: 'undeterminable', value: null, reason: 'redacted' },
  },
  outputs: {
    // 프롬프트 정책이라 경로뿐 아니라 편집 호출 수도 모른다.
    edited_files: { state: 'redacted', active_total: null, abandoned_total: null, truncated: false, items: [] },
    lines: { added: 412, removed: 96, source: 'cost_state', partial: false },
    prs: { items: [], truncated: false },
  },
  recaps: { state: 'redacted', items: [], absent_reason: 'redacted' },
  copies: { state: 'judged', original_session_id: null, original_visible: false, copied_lines: 604, copied_until: ago(700) },
  live: {
    instances: [{ status: 'idle', waiting_kind: '', heartbeat_at: ago(0.4), name: '', stale: false }],
    collector: { heartbeat_at: ago(0.5), last_stored_at: ago(0.7) },
  },
  computed_ago: 2,
});

/* ============================================================
   D. Codex 세션 — 신호는 턴 하나, Codex 턴 상태, 관측한 창 크기
   ============================================================ */

const D_ID = '019a3c7e-5d21-7b84-a0c6-3e91f2d84b17';

const dTurns = [
  // 턴 ID가 없던 옛 형식: 입력 시각으로 턴을 열었고(키 'cx:'+ts) 시작·끝 기록이 없다.
  turn({
    key: `cx:${ago(1_520)}`, no: 1, at: 1_520, took: 0, request_no: 1, status: 'undeterminable', status_reason: 'no_lifecycle',
    preview: text('cmd/logs 패키지 구조부터 설명해줘'),
    api_requests: 2, tokens: tok(6_200, 900, 40_000, 0), cost: estimated(0.12),
    context: ctxObserved(24_000, 258_400), tools: { calls: 4, subagents: 0 },
  }),
  // 끝 기록이 없고 오래된 턴: 끝나지 않았다고 단정하지 않는다.
  // 계약(§3.8)은 빈 상태가 "rollout이 하루 넘게 자라지 않았을 때만" no_end_line이라고 한다. 이 픽스처는 화면 상태를 보이려고 진행 중인
  // 세션의 중간 턴에 넣었다. 서버가 마지막 턴에서만 낼 수 있다면 이 턴은 open이어야 하고, 이 상태는 오래된 세션으로 옮겨야 한다.
  turn({
    key: 'td01', no: 2, at: 1_500, took: 0, request_no: 2, status: 'undeterminable', status_reason: 'no_end_line',
    preview: text('logs 명령에 --since 필터를 추가해줘'),
    api_requests: 4, tokens: tok(12_400, 2_300, 96_000, 0), cost: estimated(0.34),
    context: ctxObserved(41_000, 258_400), tools: { calls: 9, subagents: 0 },
  }),
  turn({
    key: 'td02', no: 3, at: 180, took: 6, request_no: 3,
    preview: text('--since 값은 2h, 3d 같은 상대 표기도 받게 해줘'),
    api_requests: 6, tokens: tok(18_900, 3_100, 188_000, 0), cost: estimated(0.52),
    context: ctxObserved(66_000, 258_400), tools: { calls: 14, subagents: 0 },
  }),
  turn({
    key: 'td03', no: 4, at: 150, took: 2, request_no: 4, status: 'interrupted',
    preview: text('테스트도 같이 돌려봐'),
    api_requests: 2, tokens: tok(5_600, 700, 90_000, 0), cost: estimated(0.11),
    context: ctxObserved(71_000, 258_400), tools: { calls: 3, subagents: 0 },
  }),
  turn({
    key: 'td04', no: 5, at: 120, took: 1, request_no: 5, status: 'error',
    preview: text('다시 돌려줘'),
    api_requests: 1, tokens: tok(2_400, 0, 40_000, 0), cost: estimated(0.04),
    context: ctxObserved(72_000, 258_400), tools: { calls: 0, subagents: 0 },
  }),
  turn({
    key: 'td05', no: 6, at: 90, took: 9, request_no: 6,
    preview: text('--until도 같은 방식으로 맞춰줘'),
    api_requests: 8, tokens: tok(22_100, 4_200, 262_000, 0), cost: estimated(0.66),
    context: ctxObserved(98_000, 258_400), tools: { calls: 21, subagents: 0 },
  }),
  turn({
    key: 'td06', no: 7, at: 8, took: 4, request_no: 7, status: 'open',
    preview: text('README에 사용 예를 넣어줘'),
    api_requests: 3, tokens: tok(9_400, 1_100, 120_000, 0), cost: estimated(0.2),
    context: ctxObserved(104_500, 258_400), tools: { calls: 7, subagents: 0 },
  }),
];

const dAnalysis = ready({
  session_id: D_ID,
  agent: 'codex',
  model: 'gpt-6-astra',
  branch: 'feature/log-filter',
  project: PROJECTS.infra,
  login_emails: [USERS.lee.login_email],
  title: { text: 'cmd/logs 패키지 구조부터 설명해줘', source: 'first_request', state: 'ok' },
  name: null,
  category: { key: 'running', as_of: ago(0.3) },
  turns: dTurns,
  cost_source: 'estimate',
  unattached_usd: 0.04,
  large_window_seen: false,
  context_mix: { input: 12_500, cache_read: 92_000, cache_create: 0 },
  subagents_running: null,
  active_seconds: 3_240,
  tags: {
    // Codex는 편집을 수집하지 않아 변경 영역을 만들 수 없다(칩을 그리지 않는다).
    change_area: { value: null, state: 'observed' },
    task_type: { value: 'implementation', share: 0.6, basis: 'classifier' },
    rules_files: [{ path: 'AGENTS.md', version: 5, discovered_at: ago(60 * 24 * 11) }],
  },
  plan_flags: { has_plan_file: false, used_plan_mode: false },
  // Codex는 질문·백그라운드·미커밋·할 일 신호를 낼 수 없다(해당 없음): 키가 없다.
  signals: { turn: { state: 'observed', value: 'open' } },
  outputs: {
    // Codex는 편집 파일과 줄 수를 남기지 않는다.
    edited_files: { state: 'observed', active_total: null, abandoned_total: null, truncated: false, items: [] },
    lines: null,
    prs: { items: [], truncated: false },
  },
  recaps: { state: 'none', items: [], absent_reason: null },
  inputs: inputsOf(dTurns),
  computed_ago: 2,
});

/* ============================================================
   E. 복사 세션 — 원본(A)을 볼 수 있고, 미커밋 "N개 이상", 가장 이른 턴의 입력은 복사된 줄
   ============================================================ */

const E_ID = 'e72b90a4-1f6c-4a38-b5d7-c0a49e3f8812';

const eTurns = [
  // 이어받은 앞부분의 입력으로 열린 턴: 요청이 아니고, 복사된 입력은 입력 수에도 들어가지 않는다(§3.16).
  turn({
    key: 'te01', no: 1, at: 74, took: 8, is_request: false, opener_copied: true,
    preview: text('이 분기에서는 DB 락으로 계속하자. 테스트 DB부터 정리해줘'),
    api_requests: 7, tokens: tok(16_200, 3_400, 620_000, 18_000), cost: otel(0.55),
    context: ctxObserved(212_000, 1_000_000), tools: { calls: 15, subagents: 0 },
  }),
  turn({
    key: 'te02', no: 2, at: 52, took: 9, request_no: 1,
    preview: text('락 대기 시간 초과 케이스 테스트를 추가해줘'),
    api_requests: 10, tokens: tok(26_800, 5_100, 1_130_000, 34_000), cost: otel(0.83),
    context: ctxObserved(251_000, 1_000_000), tools: { calls: 24, subagents: 1 },
    edited: { count: 2, sample: ['test/payments/lock_timeout.test.ts', 'src/payments/lock.ts'], abandoned: 0 },
  }),
  turn({
    key: 'te03', no: 3, at: 20, took: 6, request_no: 2, images: 1,
    preview: text('이 실패 스크린샷 원인도 같이 봐줘'),
    api_requests: 6, tokens: tok(13_900, 2_700, 760_000, 21_000), cost: otel(0.47),
    context: ctxObserved(283_000, 1_000_000), tools: { calls: 11, subagents: 0 },
  }),
];

const eAnalysis = ready({
  session_id: E_ID,
  agent: 'claude',
  model: 'claude-opus-5',
  branch: 'fix/webhook-idempotency-lock',
  project: PROJECTS.billing,
  login_emails: [USERS.kim.login_email],
  title: { text: '락 대기 시간 초과 케이스 테스트를 추가해줘', source: 'first_request', state: 'withheld' },
  name: { text: 'lock-branch', source: 'user' },
  category: { key: 'recent', as_of: ago(0.5) },
  turns: eTurns,
  inputs_count: 2,
  cost_source: 'otel',
  unattached_usd: 0.09,
  large_window_seen: true,
  subagents_running: 0,
  active_seconds: 1_980,
  tags: {
    change_area: { value: 'src/payments', state: 'observed' },
    task_type: { value: 'testing', share: 0.67, basis: 'classifier' },
    rules_files: rulesFiles(7, 3),
  },
  // 이어받은 앞부분에서 만든 플랜 파일이 있지만 이 세션 자체는 플랜 모드를 쓰지 않았다.
  plan_flags: { has_plan_file: true, used_plan_mode: false },
  signals: {
    turn: { state: 'observed', value: 'ended' },
    question: { state: 'observed', value: 0 },
    background: { state: 'observed', value: 0, parts: null },
    // 세션 중간부터 편집을 모았다: "2개 이상".
    uncommitted: { state: 'observed', value: 2, at_least: true, changed_at: ago(30), reevaluating: true },
    todo: { state: 'observed', value: 0 },
  },
  outputs: {
    edited_files: {
      state: 'observed', active_total: 2, abandoned_total: 0, truncated: false,
      items: [
        { path: 'src/payments/lock.ts', edits: 5, subagent_edits: 1, abandoned_edits: 0 },
        { path: 'test/payments/lock_timeout.test.ts', edits: 4, subagent_edits: 1, abandoned_edits: 0 },
      ],
    },
    lines: { added: 188, removed: 21, source: 'cost_state', partial: true },
    prs: { items: [], truncated: false },
  },
  recaps: { state: 'none', items: [], absent_reason: 'not_away' },
  // 복사된 입력(te01의 입력)은 입력이 아니다.
  inputs: inputsOf(eTurns.filter((t) => !t.opener_copied)),
  // 원본 A의 앞부분 1,842줄을 이어받았다. 시작 시각은 복사된 앞부분을 뺀 값이다.
  copies: { state: 'judged', original_session_id: A_ID, original_visible: true, copied_lines: 1_842, copied_until: ago(96) },
  computed_ago: 3,
});

/* ============================================================
   F~I. 사실이 없거나 일부만 있는 상태 — building, truncated, unsupported, none
   ============================================================ */

const F_ID = 'f3a81d06-7c2e-4b95-8d10-a64e2b9c5f33';
const G_ID = '91c7be55-0a3d-4e6f-b2c8-7d15e0a9f6b4';
const H_ID = '0be4d2a9-c618-4f07-9a35-e28b71d0c4f6';
const I_ID = '5d09c3f1-84ab-4c72-b6e9-1f3a0d7c82e5';
const J_ID = '7a26f4b8-3e91-4d05-a7c2-9b60e5f1d348';

const fAnalysis: SessionAnalysisPending = {
  session_id: F_ID,
  short_id: F_ID.slice(0, 8),
  state: 'building',
  owner_view: true,
  pending_since: ago(2),
  // 롤업 머리만 있다: 캐시 토큰과 비용의 나눔은 아직 모른다.
  header: {
    agent: 'claude',
    model: 'claude-sonnet-5',
    project: PROJECTS.mobile,
    accounts: { login_emails: [USERS.han.login_email], inferred: false },
    started_at: ago(14),
    last_activity_at: ago(1),
    tokens: { input: 18_200, output: 4_100 },
    cost: { usd: 0.84, source: 'otel' },
  },
};

const gAnalysis: SessionAnalysisPending = {
  session_id: G_ID,
  short_id: G_ID.slice(0, 8),
  state: 'truncated',
  owner_view: true,
  computed_at: ago(25),
  header: {
    agent: 'claude',
    model: 'claude-opus-5',
    project: PROJECTS.pipeline,
    accounts: { login_emails: [USERS.kim.login_email], inferred: false },
    started_at: ago(60 * 40),
    last_activity_at: ago(60 * 3),
    duration_seconds: 37 * 60 * 60,
    tokens: { input: 2_410_000, output: 612_000, cache_read: 188_000_000, cache_create: 9_800_000, basis: 'rollup' },
    cost: { usd: 142.71, source: 'otel' },
  },
};

const hAnalysis: SessionAnalysisPending = {
  session_id: H_ID,
  short_id: H_ID.slice(0, 8),
  state: 'unsupported',
  owner_view: true,
  header: {
    agent: 'gjc',
    model: 'qwen-3-coder',
    project: PROJECTS.infra,
    accounts: { login_emails: [], inferred: false },
    started_at: ago(500),
    last_activity_at: ago(480),
    tokens: { input: 120_000, output: 34_000 },
    // 붙은 OTEL도 추정 비용도 없다.
    cost: { usd: null, source: 'none' },
  },
};

const iAnalysis: SessionAnalysisPending = {
  session_id: I_ID,
  short_id: I_ID.slice(0, 8),
  state: 'none',
  owner_view: true,
};

/* ============================================================
   K~N. 범주 변주 — 열린 질문, 오래된 생존 행, 수집 지연(+OTEL 일부 만료), 활동 없음(모두 0)
   ============================================================ */

const K_ID = '4f81d7c2-9ab0-4e63-8c15-d27f0a3b96e1';
const kTurns = [
  turn({
    key: 'tk01', no: 1, at: 150, took: 12, request_no: 1, mode: 'plan',
    preview: text('색상 토큰 이름을 역할 기준으로 정리하자'),
    api_requests: 10, tokens: tok(24_100, 4_800, 880_000, 40_000), cost: otel(0.71),
    context: ctxTokensOnly(133_000), tools: { calls: 21, subagents: 0 },
    edited: { count: 3, sample: ['tokens/color.json', 'tokens/semantic.json', 'docs/naming.md'], abandoned: 0 },
  }),
  turn({
    key: 'tk02', no: 2, at: 110, took: 7, request_no: 2,
    preview: text('기존 이름은 deprecated 표시만 해두자'),
    api_requests: 6, tokens: tok(13_000, 2_100, 520_000, 12_000), cost: otel(0.36),
    context: ctxTokensOnly(206_000), tools: { calls: 11, subagents: 0 },
    edited: { count: 2, sample: ['tokens/color.json', 'tokens/deprecated.json'], abandoned: 0 },
  }),
  turn({
    key: 'tk03', no: 3, at: 36, took: 3, request_no: 3,
    preview: text('간격 토큰도 같은 규칙으로 갈까?'),
    api_requests: 3, tokens: tok(6_900, 1_200, 270_000, 0), cost: otel(0.18),
    context: ctxTokensOnly(238_000), tools: { calls: 4, subagents: 0 },
    question_headers: [{ tool_use_id: 'tu_k_q1', headers: ['간격 기준'], status: 'unanswered' }],
  }),
];
const kAnalysis = ready({
  session_id: K_ID,
  agent: 'claude',
  model: 'claude-sonnet-5',
  branch: 'chore/token-naming',
  project: PROJECTS.tokens,
  login_emails: [USERS.han.login_email],
  title: { text: '디자인 토큰 네이밍 규칙 정리', source: 'ai', state: 'ok' },
  name: { text: 'token-naming', source: 'derived' },
  category: { key: 'open_question', as_of: ago(0.5) },
  turns: kTurns,
  cost_source: 'otel',
  unattached_usd: 0.12,
  // 200K를 넘은 요청이 있는데 창 크기를 정할 근거는 없다: 퍼센트 없이 토큰 수와 "큰 창 흔적"만.
  large_window_seen: true,
  subagents_running: 0,
  active_seconds: 1_500,
  tags: {
    change_area: { value: 'tokens', state: 'observed' },
    task_type: { value: 'planning', share: 0.5, basis: 'classifier' },
    rules_files: rulesFiles(3, 1),
  },
  plan_flags: { has_plan_file: true, used_plan_mode: true },
  signals: {
    turn: { state: 'observed', value: 'ended' },
    question: { state: 'observed', value: 1 },
    background: { state: 'observed', value: 0, parts: null },
    uncommitted: { state: 'observed', value: 4, at_least: false, changed_at: ago(105), reevaluating: true },
    todo: { state: 'observed', value: 2 },
  },
  // 본문 합계 상한에 걸려 이 플랜의 본문은 담기지 않았다(truncated.bodies).
  plans: [
    {
      turn_key: 'tk01', ts: ago(148), file_path: '/Users/alice/.claude/plans/token-naming.md',
      decision: 'approved', body: null, state: 'observed',
    },
  ],
  truncated: { bodies: true },
  outputs: {
    edited_files: {
      state: 'observed', active_total: 4, abandoned_total: 0, truncated: false,
      items: [
        { path: 'tokens/color.json', edits: 7, subagent_edits: 0, abandoned_edits: 0 },
        { path: 'tokens/semantic.json', edits: 3, subagent_edits: 0, abandoned_edits: 0 },
        { path: 'tokens/deprecated.json', edits: 2, subagent_edits: 0, abandoned_edits: 0 },
        { path: 'docs/naming.md', edits: 2, subagent_edits: 0, abandoned_edits: 0 },
      ],
    },
    lines: { added: 233, removed: 61, source: 'cost_state', partial: false },
    prs: { items: [], truncated: false },
  },
  recaps: {
    state: 'observed',
    items: [{ ts: ago(70), text: '색상 토큰을 역할 기준(surface, ink, accent)으로 다시 이름 붙였고 기존 이름에는 deprecated 표시를 달았습니다.', repeats: 1 }],
  },
  questions: [
    {
      tool_use_id: 'tu_k_q1', turn_key: 'tk03', ts: ago(34), status: 'unanswered', state: 'observed',
      items: [
        {
          header: '간격 기준', question: '간격 토큰은 4px 배수로 통일할까요?', multi_select: false,
          options: [
            { label: '4px 배수', description: '기존 값을 가까운 배수로 맞춥니다', recommended: true },
            { label: '현행 유지', description: '이름만 바꾸고 값은 그대로 둡니다', recommended: false },
          ],
          answer: null,
        },
      ],
    },
  ],
  inputs: inputsOf(kTurns),
  computed_ago: 2,
});

const L_ID = '2c93ae60-b1d4-4f78-90e3-5a8c7d21f0b9';
const lTurns = [
  turn({
    key: 'tl01', no: 1, at: 560, took: 20, request_no: 1,
    preview: text('릴리스 노트 초안 만드는 스크립트를 짜자'),
    api_requests: 14, tokens: tok(31_000, 6_200, 1_240_000, 52_000), cost: otel(0.97),
    context: ctxObserved(176_000, 1_000_000), tools: { calls: 33, subagents: 0 },
    edited: { count: 2, sample: ['tools/release-notes.ts', 'tools/changelog.ts'], abandoned: 0 },
  }),
  turn({
    key: 'tl02', no: 2, at: 520, took: 6, request_no: 2,
    preview: text('커밋 메시지에서 PR 번호를 뽑아 링크로 바꿔줘'),
    api_requests: 8, tokens: tok(17_500, 3_300, 910_000, 18_000), cost: otel(0.64),
    context: ctxObserved(204_000, 1_000_000), tools: { calls: 15, subagents: 0 },
  }),
  turn({
    key: 'tl03', no: 3, at: 485, took: 6, request_no: 3, status: 'open',
    preview: text('끝나면 테스트도 한 번 돌려봐'),
    api_requests: 5, tokens: tok(9_800, 1_400, 640_000, 0), cost: otel(0.41),
    context: ctxObserved(231_000, 1_000_000), tools: { calls: 9, subagents: 0 },
  }),
];
const lAnalysis = ready({
  session_id: L_ID,
  agent: 'claude',
  model: 'claude-opus-5',
  branch: 'feature/release-notes',
  project: PROJECTS.mobile,
  login_emails: [USERS.park.login_email],
  title: { text: '릴리스 노트 자동화', source: 'custom', state: 'ok' },
  name: null,
  category: { key: 'stale_live', as_of: ago(0.5) },
  turns: lTurns,
  cost_source: 'otel',
  unattached_usd: 0.05,
  large_window_seen: true,
  subagents_running: null,
  active_seconds: 1_860,
  last_activity_ago: 480,
  tags: {
    change_area: { value: 'tools', state: 'observed' },
    task_type: { value: 'drafting', share: 0.67, basis: 'classifier' },
    rules_files: rulesFiles(5, 2),
  },
  plan_flags: { has_plan_file: false, used_plan_mode: false },
  signals: {
    turn: { state: 'observed', value: 'open' },
    question: { state: 'observed', value: 0 },
    background: { state: 'observed', value: 1, parts: null },
    uncommitted: { state: 'observed', value: 2, at_least: false, changed_at: ago(500), reevaluating: true },
    todo: { state: 'observed', value: 3 },
  },
  outputs: {
    edited_files: {
      state: 'observed', active_total: 2, abandoned_total: 0, truncated: false,
      items: [
        { path: 'tools/release-notes.ts', edits: 5, subagent_edits: 0, abandoned_edits: 0 },
        { path: 'tools/changelog.ts', edits: 3, subagent_edits: 0, abandoned_edits: 0 },
      ],
    },
    lines: { added: 144, removed: 12, source: 'cost_state', partial: false },
    prs: { items: [], truncated: false },
  },
  // 재수집에서 내용을 올리지 않아 recap이 있었는지 모른다(보류됨).
  recaps: { state: 'withheld', items: [], absent_reason: 'withheld' },
  inputs: inputsOf(lTurns),
  // 실행 중이라고 보고된 인스턴스의 상태가 7시간 전이다: 진행 중으로 세지 않는다.
  live: {
    instances: [{ status: 'busy', waiting_kind: '', heartbeat_at: ago(0.5), name: 'release-notes', stale: true }],
    collector: { heartbeat_at: ago(0.6), last_stored_at: ago(1) },
  },
  computed_ago: 2,
});

const M_ID = '8e1f5b37-6d02-4a94-b1c7-f0e3a58d2c19';
const mTurns = [
  // 100일 전 세션의 앞부분: OTEL 기록이 보존 기간을 넘어 지워졌다.
  turn({
    key: 'tm01', no: 1, at: 60 * 24 * 100, took: 30, request_no: 1,
    preview: text('일일 집계 파이프라인이 가끔 중복 실행돼. 재시도 정책부터 보자'),
    api_requests: 18, tokens: tok(44_000, 9_100, 1_700_000, 70_000),
    cost: { usd: null, source: 'expired', reason: null },
    context: ctxObserved(180_000, 1_000_000), tools: { calls: 36, subagents: 2 },
  }),
  turn({
    key: 'tm02', no: 2, at: 60 * 24 * 96, took: 20, request_no: 2,
    preview: text('재시도 횟수를 환경별로 나눠서 설정으로 빼줘'),
    api_requests: 12, tokens: tok(29_000, 5_400, 1_050_000, 33_000),
    cost: { usd: 0.31, source: 'otel_partial', reason: null },
    context: ctxObserved(210_000, 1_000_000), tools: { calls: 24, subagents: 0 },
    edited: { count: 3, sample: ['pipeline/retry.py', 'config/staging.yaml', 'config/prod.yaml'], abandoned: 0 },
  }),
  turn({
    key: 'tm03', no: 3, at: 60 * 22, took: 15, request_no: 3,
    preview: text('어제 장애 로그를 보고 백오프 상한을 다시 잡아줘'),
    api_requests: 9, tokens: tok(21_000, 3_800, 790_000, 21_000), cost: otel(0.77),
    context: ctxObserved(255_000, 1_000_000), tools: { calls: 17, subagents: 0 },
  }),
  turn({
    key: 'tm04', no: 4, at: 60 * 21, took: 10, request_no: 4,
    preview: text('테스트까지 통과하면 PR 설명 초안도 써줘'),
    api_requests: 7, tokens: tok(15_500, 2_900, 640_000, 14_000), cost: otel(0.58),
    context: ctxObserved(281_000, 1_000_000), tools: { calls: 12, subagents: 0 },
    edited: { count: 2, sample: ['pipeline/backoff.py', 'tests/test_backoff.py'], abandoned: 0 },
  }),
];
const mAnalysis = ready({
  session_id: M_ID,
  agent: 'claude',
  model: 'claude-opus-5',
  branch: 'fix/retry-policy',
  project: PROJECTS.pipeline,
  login_emails: [USERS.kim.login_email],
  title: { text: '일일 집계 파이프라인 재시도 정책', source: 'ai', state: 'ok' },
  name: null,
  category: { key: 'collector_delayed', as_of: ago(0.5) },
  turns: mTurns,
  cost_source: 'otel_partial',
  unattached_usd: 0.22,
  large_window_seen: true,
  subagents_running: 0,
  active_seconds: 9_300,
  last_activity_ago: 60 * 20 + 20,
  tags: {
    change_area: { value: 'pipeline/jobs', state: 'observed' },
    task_type: { value: 'debugging', share: 0.5, basis: 'classifier' },
    rules_files: rulesFiles(6, 2),
  },
  plan_flags: { has_plan_file: false, used_plan_mode: false },
  signals: {
    turn: { state: 'observed', value: 'ended' },
    question: { state: 'observed', value: 0 },
    background: { state: 'observed', value: 0, parts: null },
    uncommitted: { state: 'undeterminable', value: null, reason: 'no_git' },
    // 재수집에서 할 일 제목을 올리지 않았다(보류됨).
    todo: { state: 'undeterminable', value: null, reason: 'withheld' },
  },
  outputs: {
    // 활성 편집 호출 128회, 경로 목록은 100개 상한에서 잘렸다(truncated). 호출 합계는 전체 기준이다.
    edited_files: {
      state: 'observed', active_total: 128, abandoned_total: 0, truncated: true,
      items: Array.from({ length: 100 }, (_, i) => ({
        path: `pipeline/jobs/job_${String(i + 1).padStart(3, '0')}.py`,
        edits: Math.max(1, 30 - Math.floor(i / 4)),
        subagent_edits: i % 7 === 0 ? 2 : 0,
        abandoned_edits: 0,
      })),
    },
    lines: { added: 402, removed: 87, source: 'cost_state', partial: false },
    prs: { items: [], truncated: false },
  },
  recaps: { state: 'none', items: [], absent_reason: 'headless' },
  inputs: inputsOf(mTurns),
  // 실행 중인 인스턴스가 없고, 수집기가 20분째 신호가 없다.
  live: { instances: [], collector: { heartbeat_at: ago(20), last_stored_at: ago(25) } },
  computed_ago: 4,
});

const N_ID = 'd6a40e92-5b83-4c1f-a7d0-e19c3f68b254';
const nTurns = [
  turn({
    key: 'tn01', no: 1, at: 60 * 24 * 40 + 70, took: 8, request_no: 1,
    preview: text('옛 설정 파일 마이그레이션이 빠진 곳이 있는지 검토해줘'),
    api_requests: 6, tokens: tok(14_000, 2_400, 280_000, 9_000), cost: estimated(0.18),
    context: ctxTokensOnly(58_000), tools: { calls: 11, subagents: 0 },
  }),
  turn({
    key: 'tn02', no: 2, at: 60 * 24 * 40 + 40, took: 5, request_no: 2,
    preview: text('빠진 항목은 이슈로 정리해서 알려줘'),
    api_requests: 4, tokens: tok(8_200, 1_600, 190_000, 0), cost: estimated(0.11),
    context: ctxTokensOnly(66_000), tools: { calls: 6, subagents: 0 },
  }),
];
const nAnalysis = ready({
  session_id: N_ID,
  agent: 'claude',
  model: 'claude-sonnet-5',
  branch: 'main',
  project: PROJECTS.infra,
  login_emails: [USERS.lee.login_email],
  title: { text: '오래된 설정 마이그레이션 검토', source: 'ai', state: 'ok' },
  name: null,
  category: { key: 'inactive', as_of: ago(0.5) },
  turns: nTurns,
  // OTEL 기록이 모두 만료되어 추정으로 대신한 값.
  cost_source: 'estimate',
  cost_note: 'otel_expired',
  unattached_usd: 0.02,
  subagents_running: 0,
  active_seconds: 780,
  tags: {
    change_area: { value: null, state: 'observed' },
    task_type: { value: 'review', share: 1, basis: 'classifier' },
    rules_files: [{ path: 'CLAUDE.md', version: 1, discovered_at: ago(60 * 24 * 60) }],
  },
  plan_flags: { has_plan_file: false, used_plan_mode: false },
  // 관측했고 모두 0: 판단 불가가 아니다. 미커밋은 더는 다시 확인하지 않는다.
  signals: {
    turn: { state: 'observed', value: 'ended' },
    question: { state: 'observed', value: 0 },
    background: { state: 'observed', value: 0, parts: null },
    uncommitted: { state: 'observed', value: 0, at_least: false, changed_at: ago(60 * 24 * 40), reevaluating: false },
    todo: { state: 'observed', value: 0 },
  },
  outputs: {
    edited_files: { state: 'observed', active_total: 0, abandoned_total: 0, truncated: false, items: [] },
    lines: null,
    prs: { items: [], truncated: false },
  },
  // 턴이 두 개뿐이라 recap이 생길 조건(3개 이상)이 안 된다.
  recaps: { state: 'none', items: [], absent_reason: 'too_few_turns' },
  inputs: inputsOf(nTurns),
  computed_ago: 60 * 24 * 3,
});

/* ============================================================
   O. 요청이 하나도 없는 세션 — 요청 0은 "모름"이 아니다. 명령·예약·다른 세션·자동 이어가기 턴, 제목 없음
   ============================================================ */

const O_ID = '3b6e19d4-7a05-4c82-9e1f-60d4b2a8c7f1';
const oTurns = [
  // 응답이 없는 명령(/clear, /model 같은 것)은 요청이 아니고 API 호출도 없다(§3.9).
  turn({
    key: 'to01', no: 1, at: 125, took: 0.1, kind: 'command', is_request: false,
    preview: text('/model sonnet'),
  }),
  turn({
    key: 'to02', no: 2, at: 95, took: 1, kind: 'scheduled', is_request: false,
    preview: text('예약 실행: 스테이징 배포 상태 확인'),
    api_requests: 2, tokens: tok(1_100, 260, 88_000, 0), cost: otel(0.05),
    context: ctxObserved(51_000, 1_000_000), tools: { calls: 3, subagents: 0 },
  }),
  turn({
    key: 'to03', no: 3, at: 70, took: 1, kind: 'peer', is_request: false,
    preview: text('다른 세션의 메시지: 스테이징 배포가 끝났습니다'),
    api_requests: 1, tokens: tok(640, 120, 52_000, 0), cost: otel(0.03),
    context: ctxObserved(53_000, 1_000_000), tools: { calls: 0, subagents: 0 },
  }),
  turn({
    key: 'to04', no: 4, at: 65, took: 1, kind: 'scheduled', is_request: false,
    preview: text('예약 실행: 스테이징 배포 상태 확인'),
    api_requests: 2, tokens: tok(1_000, 240, 90_000, 0), cost: otel(0.05),
    context: ctxObserved(56_000, 1_000_000), tools: { calls: 3, subagents: 0 },
  }),
  turn({
    key: 'to05', no: 5, at: 35, took: 1, kind: 'auto', is_request: false,
    preview: text('자동 이어가기: 이전 작업을 계속합니다'),
    api_requests: 1, tokens: tok(520, 90, 58_000, 0), cost: otel(0.02),
    context: ctxObserved(58_000, 1_000_000), tools: { calls: 1, subagents: 0 },
  }),
  turn({
    key: 'to06', no: 6, at: 30, took: 1, kind: 'channel', is_request: false,
    preview: text('외부 채널 메시지: 배포 승인 요청이 도착했습니다'),
    api_requests: 1, tokens: tok(480, 110, 60_000, 0), cost: otel(0.02),
    context: ctxObserved(60_000, 1_000_000), tools: { calls: 0, subagents: 0 },
  }),
  turn({
    key: 'to07', no: 7, at: 25, took: 1, kind: 'sdk', is_request: false,
    preview: text('SDK 호출: 상태 점검 스크립트'),
    api_requests: 1, tokens: tok(430, 70, 61_000, 0), cost: otel(0.01),
    context: ctxObserved(61_000, 1_000_000), tools: { calls: 1, subagents: 0 },
  }),
  turn({
    key: 'to08', no: 8, at: 20, took: 1, kind: 'system', is_request: false,
    preview: text('시스템 알림: 컨텍스트 정리를 권고합니다'),
    api_requests: 0,
  }),
];
const oAnalysis = ready({
  session_id: O_ID,
  agent: 'claude',
  model: 'claude-sonnet-5',
  branch: 'main',
  project: PROJECTS.infra,
  login_emails: [USERS.lee.login_email],
  // 첫 요청도 제목도 없다.
  title: { text: null, source: 'none', state: 'none' },
  name: null,
  category: { key: 'recent', as_of: ago(0.5) },
  turns: oTurns,
  cost_source: 'otel',
  unattached_usd: 0,
  subagents_running: 0,
  active_seconds: 420,
  tags: { change_area: { value: null, state: 'observed' }, task_type: null, rules_files: [] },
  plan_flags: { has_plan_file: false, used_plan_mode: false },
  signals: {
    turn: { state: 'observed', value: 'ended' },
    question: { state: 'observed', value: 0 },
    background: { state: 'observed', value: 0, parts: null },
    uncommitted: { state: 'undeterminable', value: null, reason: 'no_git' },
    todo: { state: 'undeterminable', value: null, reason: 'no_list' },
  },
  outputs: {
    edited_files: { state: 'observed', active_total: 0, abandoned_total: 0, truncated: false, items: [] },
    lines: null,
    prs: { items: [], truncated: false },
  },
  recaps: { state: 'none', items: [], absent_reason: 'headless' },
  inputs: inputsOf(oTurns),
  computed_ago: 2,
});

/* ============================================================
   P. 아주 긴 세션 — 턴 배열이 응답 상한(2,000 · 500)에서 잘렸다. 수는 전체 기준이다.
   ============================================================ */

const P_ID = '6f2d80c7-1e94-4b3a-8a57-d3c9e0b41a68';

/**
 * P의 일정: 나흘(화~금) 서울 09:00~19:00에 하루 703턴씩, 모두 2,812턴(`counts.turns`)이다. 분석 응답은 앞의 2,000턴만 담고(상한),
 * 캘린더는 같은 일정으로 전체 턴과 작업 중 입력을 만든다(캘린더 픽스처가 이 값을 쓴다). 한 줄로 이어지는 닷새짜리 띠가 되지 않도록
 * 하루 일을 따로 두었다.
 */
export const P_SCHEDULE = {
  /** 2026-10-06(화)부터 하루씩, 서울 09:00 = 00:00Z. */
  dayStartsMs: [0, 1, 2, 3].map((d) => Date.UTC(2026, 9, 6 + d, 0, 0, 0)),
  /** 하루 일하는 시간(분). */
  windowMin: 600,
  perDay: 703,
  totalTurns: 2_812,
  totalInputs: 4_236,
  /** 턴 i의 시작(절대 시각, ms). */
  turnAtMs: (i: number): number => {
    const day = Math.floor(i / 703);
    const j = i % 703;
    return Date.UTC(2026, 9, 6 + day, 0, 0, 0) + Math.round(j * (600 / 703) * 60_000);
  },
};
/** 턴 i의 시작이 지금부터 몇 분 전인가. */
const pAgo = (i: number): number => (PREVIEW_NOW - P_SCHEDULE.turnAtMs(i)) / 60_000;
const pTurns = Array.from({ length: 2_000 }, (_, i) =>
  turn({
    key: `tp${i + 1}`, no: i + 1, at: pAgo(i), took: 0.7, request_no: i + 1,
    status: i % 97 === 96 ? 'interrupted' : 'ended',
    preview: text(`로그 샘플 ${i + 1}번 묶음을 정리하고 요약 표를 갱신해줘`),
    api_requests: 3, tokens: tok(4_000 + (i % 7) * 300, 900, 120_000 + (i % 11) * 4_000, 2_000),
    cost: otel(0.05 + (i % 5) * 0.01),
    context: ctxObserved(80_000 + (i % 13) * 9_000, 1_000_000), tools: { calls: 4, subagents: 0 },
  }),
);
const pAbandoned = Array.from({ length: 500 }, (_, i) =>
  turn({
    key: `tpa${i + 1}`, no: 2_001 + i, at: pAgo(Math.floor(i * 4.386)) - 0.3, took: 0.3, branch: 'abandoned',
    preview: text(`(되감음) 다른 방식으로 ${i + 1}번 묶음을 다시 시도`),
    api_requests: 2, tokens: tok(3_200, 700, 90_000, 1_000), cost: otel(0.04),
    context: ctxObserved(70_000, 1_000_000), tools: { calls: 3, subagents: 0 },
  }),
);
// 입력 4,236개(턴을 연 입력 2,812 + 작업 중 입력 1,424) 중 상한(4,000)까지만 온다. 일정(P_SCHEDULE)대로 시각 순이다.
const pInputs: AnalysisInput[] = (() => {
  const all: { at: number; kind: AnalysisInput['kind']; turnIndex: number }[] = [];
  for (let i = 0; i < P_SCHEDULE.totalTurns; i++) all.push({ at: P_SCHEDULE.turnAtMs(i), kind: 'typed', turnIndex: i });
  // 작업 중 입력: 짝수 턴 1,406개 + 앞의 홀수 턴 18개 = 1,424개.
  for (let i = 0; i < P_SCHEDULE.totalTurns; i += 2) all.push({ at: P_SCHEDULE.turnAtMs(i) + 25_000, kind: 'queued', turnIndex: i });
  for (let i = 1; i < 36; i += 2) all.push({ at: P_SCHEDULE.turnAtMs(i) + 25_000, kind: 'queued', turnIndex: i });
  all.sort((a, b) => a.at - b.at);
  return all.slice(0, 4_000).map((x, n) => ({
    uuid: `u_tp_${n + 1}`,
    ts: new Date(x.at).toISOString(),
    n: n + 1,
    kind: x.kind,
    turn_key: `tp${Math.min(x.turnIndex + 1, 2_000)}`,
    images: 0,
  }));
})();
const pAnalysis = ready({
  session_id: P_ID,
  agent: 'claude',
  model: 'claude-opus-5',
  branch: 'chore/log-triage',
  project: PROJECTS.pipeline,
  login_emails: [USERS.kim.login_email],
  title: { text: '로그 샘플 정리와 요약 표 갱신', source: 'ai', state: 'ok' },
  name: null,
  category: { key: 'recent', as_of: ago(0.5) },
  turns: pTurns,
  abandoned: pAbandoned,
  counts: { requests: 2_760, inputs: 4_236, turns: 2_812, abandoned_turns: 641, api_requests: 8_977 },
  inputs: pInputs,
  cost_source: 'otel',
  unattached_usd: 4.2,
  large_window_seen: true,
  subagents_running: 0,
  // 나흘 × 10시간. 마지막 활동은 금요일 19:00(서울)이다 — 분석 응답의 마지막 턴은 상한(2,000)에서 잘려 목요일에 있다.
  active_seconds: 4 * 600 * 60,
  last_activity_ago: (PREVIEW_NOW - Date.UTC(2026, 9, 9, 10, 0, 0)) / 60_000,
  tags: {
    // 편집한 파일이 없으니 변경 영역도 없다.
    change_area: { value: null, state: 'observed' },
    task_type: { value: 'operations', share: 0.9, basis: 'classifier' },
    rules_files: rulesFiles(6, 2),
  },
  plan_flags: { has_plan_file: false, used_plan_mode: false },
  signals: {
    turn: { state: 'observed', value: 'ended' },
    question: { state: 'observed', value: 0 },
    // 수집할 때 필드 모양이 예상과 달라 버렸다.
    background: { state: 'undeterminable', value: null, reason: 'dropped', parts: null },
    uncommitted: { state: 'observed', value: 0, at_least: false, changed_at: ago(900), reevaluating: true },
    todo: { state: 'observed', value: 0 },
  },
  outputs: {
    edited_files: { state: 'observed', active_total: 0, abandoned_total: 0, truncated: false, items: [] },
    lines: { added: 0, removed: 0, source: 'cost_state', partial: false },
    // PR이 100개를 넘어 목록이 잘렸다.
    prs: {
      items: Array.from({ length: 100 }, (_, i) => ({
        url: `https://git.example.com/acme/data-pipeline/pull/${700 + i}`,
        number: 700 + i,
        repository: 'acme/data-pipeline',
        first_seen_at: ago(60 * 24 * 6 - i * 80),
        turn_key: i % 3 === 0 ? `tp${i * 10 + 1}` : null,
      })),
      truncated: true,
    },
  },
  recaps: {
    state: 'observed',
    items: Array.from({ length: 50 }, (_, i) => ({
      ts: ago(60 * 24 * 6 - i * 170),
      text: `${i + 1}번째 recap: 로그 묶음 ${i * 40 + 1}~${i * 40 + 40}번을 정리했고 요약 표를 갱신했습니다.`,
      repeats: i % 9 === 0 ? 2 : 1,
    })),
  },
  truncated: { turns: true, abandoned: true, inputs: true, recaps: true },
  computed_ago: 6,
});

/* ============================================================
   목록과 카탈로그
   ============================================================ */

const fixture = (
  id: string,
  label: string,
  covers: string[],
  meta: Parameters<typeof rowOf>[0],
  analysis: SessionAnalysis | null,
  rowAnalysis: SessionRowAnalysis | undefined,
  identities: AnalysisIdentities,
): SessionFixture => ({
  id,
  label,
  covers,
  row: rowOf(meta, analysis ?? { session_id: id, short_id: id.slice(0, 8), state: 'none', owner_view: true }, rowAnalysis),
  analysis,
  identities,
});

export const PREVIEW_SESSIONS: SessionFixture[] = [
  fixture(
    A_ID,
    '풍부한 Claude 세션. OTEL 비용, 모든 턴 상태, 버려진 분기, 플랜 승인·반려·대기, 열린 질문, PR.',
    ['ready', '응답 대기', 'OTEL 비용', '귀속 불가(P0 이전·공유 prompt_id)', '버려진 분기', '명령·알림 턴', '플랜', 'PR', '신호 혼합'],
    { session_id: A_ID, ...USERS.kim },
    aAnalysis,
    rowAnalysisOf(aAnalysis),
    identitiesOf(USERS.kim),
  ),
  fixture(
    K_ID,
    '열린 질문. 마지막 턴은 끝났고 답하지 않은 질문이 남았다. 창 크기는 모른다.',
    ['열린 질문', '창 크기 모름(토큰 수만)', '신호 값 있음'],
    { session_id: K_ID, ...USERS.han },
    kAnalysis,
    rowAnalysisOf(kAnalysis),
    identitiesOf(USERS.han),
  ),
  fixture(
    D_ID,
    'Codex 세션. 신호는 턴 하나(나머지는 해당 없음), Codex 턴 상태(중단·오류·끝 기록 없음), 관측한 창 크기.',
    ['Codex', '진행 중', '해당 없음 신호 키 없음', '판단 불가 턴(no_end_line)', '추정 비용'],
    { session_id: D_ID, ...USERS.lee },
    dAnalysis,
    rowAnalysisOf(dAnalysis),
    identitiesOf(USERS.lee),
  ),
  fixture(
    E_ID,
    '복사 세션. 원본(A)을 볼 수 있다. 미커밋은 "N개 이상", 제목은 보류됨(첫 요청), 복사된 입력으로 열린 턴.',
    ['복사본(원본 보임)', '제목 보류됨', '미커밋 N개 이상', '줄 수 일부'],
    { session_id: E_ID, ...USERS.kim },
    eAnalysis,
    rowAnalysisOf(eAnalysis),
    identitiesOf(USERS.kim),
  ),
  fixture(
    L_ID,
    '오래된 생존 행. 실행 중이라고 보고된 상태가 7시간 전이라 진행 중으로 세지 않는다.',
    ['판단 불가(오래된 생존 행)', '실시간 stale'],
    { session_id: L_ID, ...USERS.park },
    lAnalysis,
    rowAnalysisOf(lAnalysis),
    identitiesOf(USERS.park),
  ),
  fixture(
    M_ID,
    '수집 지연. 100일에 걸친 세션이라 OTEL 일부가 만료됐다(만료, OTEL 일부, OTEL 턴). 세션 길이가 일 단위.',
    ['수집 지연', 'OTEL 일부(otel_partial)', '만료(expired)', '실행 인스턴스 없음'],
    { session_id: M_ID, ...USERS.kim, has_sync: true },
    mAnalysis,
    rowAnalysisOf(mAnalysis),
    identitiesOf(USERS.kim),
  ),
  fixture(
    F_ID,
    '분석을 만드는 중(building). 롤업 머리만 있다. 캐시 토큰과 비용의 나눔은 아직 모른다.',
    ['building', '롤업 머리만'],
    { session_id: F_ID, ...USERS.han },
    fAnalysis,
    { state: 'building' },
    identitiesOf(USERS.han),
  ),
  fixture(
    B_ID,
    '추정 비용 세션(stale). 이틀에 걸침, P0 이전 앞부분, 보류·가려진·없는 첫 줄, 요율 없음, 추정 반영 대기, 추정 계정, 복사 판정 생략.',
    ['stale', '추정 비용', 'estimate_pending', 'no_rate', 'pre_collect 신호', '미리보기 withheld/redacted/none', '플랜 본문 withheld/redacted', 'recap pre_collect', '줄 수 없음', 'copies skipped_large_group'],
    { session_id: B_ID, ...USERS.park },
    bAnalysis,
    rowAnalysisOf(bAnalysis),
    identitiesOf(USERS.park),
  ),
  fixture(
    C_ID,
    '가려진 세션. 턴 경계 판단 불가(요청·입력·턴 수 모름), 제목 가려짐, 편집 경로 가려짐, 계정 둘, 원본을 볼 수 없는 복사본.',
    ['turn_boundaries undeterminable', '제목 redacted', '편집 파일 redacted', '컨텍스트 창 모름', '계정 2', 'copies(원본 안 보임)'],
    { session_id: C_ID, ...USERS.choi, account_count: 2 },
    cAnalysis,
    rowAnalysisOf(cAnalysis),
    identitiesOf(USERS.choi),
  ),
  fixture(
    N_ID,
    '활동 없음. 신호가 모두 관측된 0이다(판단 불가가 아님). OTEL이 모두 만료되어 추정으로 대신했다.',
    ['inactive', '관측된 0', 'otel_expired', '미커밋 재확인 중단'],
    { session_id: N_ID, ...USERS.lee },
    nAnalysis,
    rowAnalysisOf(nAnalysis),
    identitiesOf(USERS.lee),
  ),
  fixture(
    O_ID,
    '요청이 하나도 없는 세션. "요청 0"은 모름이 아니다. 명령·예약·다른 세션·자동 이어가기로 열린 턴, 제목 없음(none).',
    ['요청 0(관측)', '제목 none', '턴 출처: 명령·예약 실행·다른 세션·자동 이어가기', '규칙 파일 없음'],
    { session_id: O_ID, ...USERS.lee },
    oAnalysis,
    rowAnalysisOf(oAnalysis),
    identitiesOf(USERS.lee),
  ),
  fixture(
    P_ID,
    '아주 긴 세션. 활성 턴 2,000 · 버려진 턴 500에서 응답이 잘렸다(truncated.turns·abandoned). 수는 전체 기준이고 "더 보기"로 나눠 그린다.',
    ['truncated.turns', 'truncated.abandoned', '더 보기', '2,500행'],
    { session_id: P_ID, ...USERS.kim },
    pAnalysis,
    rowAnalysisOf(pAnalysis),
    identitiesOf(USERS.kim),
  ),
  fixture(
    G_ID,
    '너무 큰 세션(truncated). 요청·신호는 계산하지 않았고 합계만 있다.',
    ['truncated'],
    { session_id: G_ID, ...USERS.kim },
    gAnalysis,
    { state: 'truncated' },
    identitiesOf(USERS.kim),
  ),
  fixture(
    H_ID,
    '지원하지 않는 에이전트(unsupported). gjc 세션.',
    ['unsupported'],
    { session_id: H_ID, ...USERS.lee },
    hAnalysis,
    { state: 'unsupported' },
    identitiesOf(USERS.lee),
  ),
  fixture(
    I_ID,
    '읽을 수 있는 분석이 없는 세션(none).',
    ['none'],
    { session_id: I_ID, ...USERS.han },
    iAnalysis,
    { state: 'none' },
    identitiesOf(USERS.han),
  ),
  fixture(
    J_ID,
    '신원이 섞인 세션. 목록에서만 보류(withheld)이고 분석 API는 404다.',
    ['withheld(목록)', '분석 404'],
    { session_id: J_ID, ...USERS.choi },
    null,
    { state: 'withheld', reason: 'mixed_identity' },
    identitiesOf(USERS.choi, true),
  ),
];

/** 보는 사람이 세션 주인인가, 주인이 아닌 관리자인가. */
export type ViewerRole = 'owner' | 'admin';

export const rowFor = (f: SessionFixture, role: ViewerRole): SessionOverview => (role === 'owner' ? f.row : asNonOwnerRow(f.row));

export const analysisFor = (f: SessionFixture, role: ViewerRole): SessionAnalysis | null => {
  if (f.analysis === null) return null;
  return role === 'owner' ? f.analysis : asNonOwnerAdminView(f.analysis, f.identities);
};

/** 범주 칩의 개수. 주인 범위에서만 센다. */
export const categoryCountsOf = (rows: readonly SessionOverview[]): SessionCategoryCounts => {
  const counts: SessionCategoryCounts = {};
  for (const row of rows) {
    const key: SessionCategoryKey | undefined = row.analysis?.category;
    if (key) counts[key] = (counts[key] ?? 0) + 1;
  }
  return counts;
};

export { A_ID, B_ID, C_ID, D_ID, E_ID, F_ID, G_ID, H_ID, I_ID, J_ID, K_ID, L_ID, M_ID, N_ID, O_ID, P_ID };
