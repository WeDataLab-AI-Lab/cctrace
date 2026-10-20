/**
 * 대표 세션: 레퍼런스 화면(요청 14건, 입력 15건, 커밋 10건, 변경 파일 16건, 작업 항목 17개)과 같은 밀도의 합성 세션이다.
 * 소재는 지어낸 웹 앱 "pulse-web"의 알림 센터에 필터와 읽음 처리를 넣는 작업이다. 제목, 경로, 문장은 모두 지어낸 것이다.
 *
 * 판정은 레퍼런스와 같다: 턴 종료 통과 · 답 대기 없음 통과 · 백그라운드 작업은 판단 불가(수집 규칙 이전) · 커밋됨 통과 · 작업 내용 완료
 * 통과. 살아 있는(쉬는 중) 세션이라 상태 칩은 "실행 중"이고, 판단 불가가 남아 완료 버킷에는 넣지 않는다.
 */

import type {
  AnalysisPlan,
  AnalysisQuestion,
  AnalysisRecaps,
  EditedFileItem,
  PullRequest,
  SessionTasks,
} from '@/lib/session-analysis-types';
import {
  S,
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
} from './builders';

export const FLAGSHIP_ID = 'c8e41d27-6a53-4f90-b2d7-e19a05f3c648';

const USER = { user_id: 'usr-a21e5c', profile_email: 'seo.dahye@example.com', login_email: 'dahye.sub@example.com' };

const PROJECT = { hash: '-Users-dev-work-pulse-web', name: 'pulse-web' };
const REPO = 'acme/pulse-web';
const BRANCH = 'feat/inbox-filters';

const PR: PullRequest = {
  url: 'https://git.example.com/acme/pulse-web/pull/231',
  number: 231,
  repository: REPO,
  first_seen_at: ago(6),
  turn_key: 't14',
};

/* ---------------- 턴 열네 개 ---------------- */

const TURN_EDITED = (names: string[], count = names.length) => ({ count, sample: names, abandoned: 0 });

const specs: TurnSpec[] = [
  {
    key: 't01',
    text: '알림 센터에 종류별 필터를 넣고 싶습니다.',
    prompt:
      '알림 센터에 종류별 필터를 넣고 싶습니다.\n지금은 `app/inbox/` 아래에 모든 알림이 한 줄로 쌓여서 결제·배포·멘션을 구분하기 어렵습니다.\n\n이미지처럼 목록 위에 필터 칩을 두고, 읽음 처리까지 되면 좋겠어요.\n읽음 상태를 어디에 저장할지는 모르겠으니 제안해 주세요.\n\n일단 읽음/안 읽음 두 상태만 먼저 보고 싶습니다.',
    at: 319,
    took: 17,
    tokens: 6_200_000,
    usd: 2.26,
    ctx: 180_000,
    window: 1_000_000,
    images: 1,
    tools: 58,
    questionHeaders: [{ tool_use_id: 'toolu_q1', headers: ['표시 위치', '읽음 저장'], status: 'answered' }],
    edited: TURN_EDITED(['filter-chips.tsx', 'use-inbox-filters.ts', 'read-state.ts', 'page.tsx'], 5),
    progress: ['먼저 `app/inbox/`의 구조와 알림 타입 정의를 읽어 보겠습니다.', '알림 종류는 결제, 배포, 멘션, 시스템 네 가지입니다. 이 기준으로 칩을 나누겠습니다.'],
    reply:
      '필터 칩과 읽음 상태의 첫 구현을 마쳤습니다.\n\n- 목록 위에 종류별 칩 줄(`filter-chips.tsx`)을 두었습니다\n- 읽음 상태는 서버 표 `inbox_reads`에 저장하고, 로컬은 낙관적 캐시로만 씁니다\n- 읽음/안 읽음 두 상태 필터가 동작합니다',
  },
  {
    key: 't02',
    text: '필터와 목록 보기 전환은 툴바 맨 왼쪽에 두세요',
    at: 298,
    took: 2,
    tokens: 1_600_000,
    usd: 0.39,
    ctx: 190_000,
    window: 1_000_000,
    tools: 14,
    edited: TURN_EDITED(['page.tsx', 'filter-chips.tsx']),
    reply: '툴바 맨 왼쪽으로 옮겼습니다. 키보드 이동 순서도 같이 맞췄습니다.',
  },
  {
    key: 't03',
    text: '· 종류',
    prompt: '· 종류\n· 읽음 여부\n· 기간\n으로 걸러 볼 수 있게 해줘',
    at: 295.5,
    took: 2,
    tokens: 2_200_000,
    usd: 0.59,
    ctx: 200_000,
    window: 1_000_000,
    tools: 21,
    edited: TURN_EDITED(['use-inbox-filters.ts', 'query.ts', 'filter-chips.tsx']),
    reply: '세 기준을 모두 걸 수 있게 했습니다. 기간은 오늘, 7일, 30일 세 가지 프리셋입니다.',
  },
  {
    key: 't04',
    text: '칩이 많으면 한 줄에 다 안 들어가니까 접어서 보여줘',
    at: 292,
    took: 2,
    tokens: 814_700,
    usd: 0.19,
    ctx: 200_000,
    window: 1_000_000,
    tools: 9,
    edited: TURN_EDITED(['filter-chips.tsx']),
    reply: '넘치는 칩은 "더 보기" 뒤로 접습니다. 접힌 칩 안에 선택된 것이 있으면 개수를 표시합니다.',
  },
  {
    key: 't05',
    text: '안 읽음 점을 숫자 배지로 바꿀 수 있어요?',
    at: 289,
    took: 1.5,
    tokens: 412_300,
    usd: 0.27,
    ctx: 210_000,
    window: 1_000_000,
    tools: 6,
    edited: TURN_EDITED(['unread-badge.tsx']),
    reply: '점 대신 숫자 배지로 바꿨습니다.',
  },
  {
    key: 't06',
    text: '읽음 상태를 얼마나 오래 어디에 둘지 정리해 봅시다',
    at: 287,
    took: 8,
    tokens: 1_050_000,
    usd: 0.31,
    ctx: 220_000,
    window: 1_000_000,
    mode: 'plan',
    tools: 12,
    progress: ['보관 기간 후보를 정리하고 있습니다.'],
    reply: '계획대로 진행하면 됩니다. 서버에 30일 보관하고 로컬은 캐시로만 씁니다. 마이그레이션은 다음 요청에서 추가하겠습니다.',
  },
  {
    key: 't07',
    text: '배지가 99를 넘으면 99+로 줄이고 키보드로도 필터를 바꾸게 하고 싶어요',
    at: 270,
    took: 6,
    tokens: 1_900_000,
    usd: 0.52,
    ctx: 230_000,
    window: 1_000_000,
    tools: 24,
    edited: TURN_EDITED(['unread-badge.tsx', 'use-inbox-filters.ts', 'inbox-list.tsx']),
    reply: '99를 넘으면 `99+`로 줄입니다. 칩 줄에서 화살표 키로 필터를 옮길 수 있습니다.',
  },
  {
    key: 't08',
    text: '알림을 누르면 오른쪽 패널에서 상세가 열렸으면 좋겠어요',
    at: 252,
    took: 7,
    tokens: 2_300_000,
    usd: 0.58,
    ctx: 240_000,
    window: 1_000_000,
    tools: 33,
    edited: TURN_EDITED(['detail-panel.tsx', 'inbox-list.tsx', 'page.tsx']),
    reply: '알림을 누르면 오른쪽에 상세 패널이 열리고 자동으로 읽음 처리됩니다. 열린 패널에서는 이전·다음 알림으로 넘어갑니다.',
  },
  {
    key: 't09',
    text: '모바일 폭에서는 칩이 가로로 스크롤되게 해줘. 선은 그대로 두는 것도 괜찮아요',
    at: 233,
    took: 5,
    tokens: 2_200_000,
    usd: 0.55,
    ctx: 250_000,
    window: 1_000_000,
    tools: 18,
    edited: TURN_EDITED(['filter-chips.tsx', 'inbox-list.tsx']),
    reply: '모바일 폭에서는 칩이 가로로 스크롤됩니다. 구분선은 기존 모양 그대로 뒀습니다.',
  },
  {
    key: 't10',
    text: '읽음 처리 API가 실패하면 되돌리는 낙관적 업데이트도 넣어줄 수 있나요?',
    at: 205,
    took: 9,
    tokens: 2_100_000,
    usd: 0.49,
    ctx: 260_000,
    window: 1_000_000,
    tools: 27,
    edited: TURN_EDITED(['optimistic.ts', 'route.ts', 'read-state.ts']),
    reply: '읽음 처리를 먼저 화면에 반영하고, 서버가 실패하면 이전 상태로 되돌린 뒤 토스트로 알립니다.',
  },
  {
    key: 't11',
    text: '알림이 많을 때 느리니까 가상 스크롤이나 캐시로 빠르게 해주세요',
    at: 170,
    took: 12,
    tokens: 1_350_000,
    usd: 0.31,
    ctx: 270_000,
    window: 1_000_000,
    tools: 29,
    edited: TURN_EDITED(['inbox-list.tsx', 'query.ts', 'inbox-list.test.tsx']),
    progress: ['알림 5천 건으로 렌더 시간을 먼저 재 보겠습니다.'],
    reply: '목록을 가상화하고 쿼리 결과를 캐시했습니다. 5천 건에서 첫 렌더가 1.4초에서 0.2초로 줄었습니다.',
  },
  {
    key: 't12',
    text: '필터 상태를 URL 쿼리에 남겨서 새로고침해도 유지되게 해줘',
    at: 128,
    took: 8,
    tokens: 1_900_000,
    usd: 0.45,
    ctx: 280_000,
    window: 1_000_000,
    tools: 16,
    edited: TURN_EDITED(['use-inbox-filters.ts', 'query.test.ts']),
    reply: '필터 상태를 `?kind=&read=&range=` 쿼리에 남깁니다. 잘못된 값은 무시하고 기본값으로 돌아갑니다.',
  },
  {
    key: 't13',
    text: '접근성: 칩은 스크린리더가 읽을 수 있게 해주고, 그 밖엔 일단 괜찮아요',
    at: 70,
    took: 9,
    tokens: 1_300_000,
    usd: 0.32,
    ctx: 290_000,
    window: 1_000_000,
    tools: 15,
    edited: TURN_EDITED(['filter-chips.tsx', 'unread-badge.tsx']),
    reply: '칩에 `role`과 `aria-pressed`를 붙이고, 배지에는 읽는 이름을 달았습니다. 나머지는 손대지 않았습니다.',
  },
  {
    key: 't14',
    text: '커밋하고 BOARD.md랑 issues.md도 갱신해줘',
    at: 15,
    took: 9,
    tokens: 973_000,
    usd: 0.21,
    ctx: 300_000,
    window: 1_000_000,
    tools: 11,
    edited: TURN_EDITED(['BOARD.md', 'issues.md', 'inbox-filters.md'], 3),
    prs: [PR],
    reply: '커밋했습니다. BOARD.md와 issues.md는 task-123이 이미 완료 상태라 바꿀 것이 없었습니다.',
  },
];

const { turns, extras } = turnsOf(specs);

/* ---------------- 질문과 플랜 ---------------- */

const QUESTION: AnalysisQuestion = {
  tool_use_id: 'toolu_q1',
  turn_key: 't01',
  ts: ago(318.6),
  status: 'answered',
  state: 'observed',
  items: [
    {
      header: '표시 위치',
      question: '필터 줄을 어디에 둘까요?',
      multi_select: false,
      options: [
        {
          label: '목록 위의 한 줄',
          description: '목록 바로 위에 칩을 한 줄로 둡니다. 스크롤해도 따라오게 고정하고, 칩이 넘치면 접어서 보입니다.',
          recommended: true,
        },
        {
          label: '왼쪽 사이드바',
          description: '카테고리 목록처럼 세로로 둡니다. 칩이 많아져도 되지만 목록 폭이 좁아지고 모바일에서는 따로 만들어야 합니다.',
          recommended: false,
        },
      ],
      answer: { labels: ['목록 위의 한 줄'], free_text: null },
    },
    {
      header: '읽음 저장',
      question: '읽음 상태를 어디에 저장할까요? 기기를 바꿔도 이어지길 원하는지가 기준입니다.',
      multi_select: false,
      options: [
        { label: '로컬 저장소만', description: '브라우저에만 저장합니다. 가장 단순하지만 기기를 바꾸면 읽음이 사라집니다.', recommended: false },
        {
          label: '서버 표에 저장',
          description: '사용자별 읽음을 `inbox_reads`에 저장합니다. 기기 사이에 이어지지만 읽음 처리마다 요청이 나갑니다.',
          recommended: true,
        },
        {
          label: '둘 다',
          description: '서버가 기준이고 로컬은 낙관적 캐시로만 씁니다. 응답이 빠르지만 실패했을 때 되돌리는 코드가 필요합니다.',
          recommended: false,
        },
      ],
      answer: { labels: ['둘 다'], free_text: null },
    },
  ],
};

const PLAN: AnalysisPlan = {
  turn_key: 't06',
  ts: ago(283),
  file_path: '/Users/alice/.claude/plans/inbox-read-retention.md',
  file_updated_at: ago(285),
  decision: 'approved',
  state: 'observed',
  body: '## 읽음 상태 보관 계획\n\n- 서버 `inbox_reads`에 사용자별로 저장하고 30일 뒤 정리한다\n- 로컬에는 낙관적 캐시만 둔다(새로고침하면 서버 기준)\n- 마이그레이션 `0042_inbox_reads.sql`을 추가한다\n\n### 검증\n\n1. 읽음 처리 API가 실패하면 되돌리는가\n2. 오래된 읽음을 정리하는 작업이 도는가',
};

/* ---------------- 커밋 열 개 ---------------- */

const commits = observedCommits([
  commitOf({ short: '441e4b2', subject: 'feat(inbox): 종류별 필터 칩과 읽음 상태 모델을 추가', at: 306, turn: 't01', tasks: ['task-123'], repository: REPO, branch: BRANCH }),
  commitOf({ short: 'f866fbe', subject: 'docs(board): task-123에 커밋 해시를 기록', at: 304, turn: 't01', tasks: ['task-123'], repository: REPO, branch: BRANCH }),
  commitOf({ short: '1105386', subject: 'feat(inbox): 필터와 보기 전환을 툴바 맨 왼쪽으로 옮김', at: 296.5, turn: 't02', tasks: ['task-123'], repository: REPO, branch: BRANCH }),
  commitOf({ short: 'c4483a7', subject: 'feat(inbox): 종류·읽음·기간으로 걸러 보기', at: 293.8, turn: 't03', tasks: ['task-123'], repository: REPO, branch: BRANCH }),
  commitOf({ short: 'cabab59', subject: 'fix(inbox): 칩이 넘치면 접어서 보이기', at: 290.4, turn: 't04', tasks: ['task-123'], repository: REPO, branch: BRANCH }),
  commitOf({ short: '4b2a56e', subject: 'feat(inbox): 안 읽음 점을 숫자 배지로 교체', at: 287.8, turn: 't05', tasks: ['task-123'], repository: REPO, branch: BRANCH }),
  commitOf({ short: '75a8883', subject: 'feat(inbox): 99+ 배지와 키보드 필터 이동, 알림 상세 패널', at: 245.4, turn: 't08', tasks: ['task-123'], repository: REPO, branch: BRANCH }),
  commitOf({ short: 'ae415b0', subject: 'style(inbox): 모바일 폭에서 칩을 가로 스크롤', at: 228.6, turn: 't09', tasks: ['task-123', 'issue-45'], repository: REPO, branch: BRANCH }),
  commitOf({ short: 'c5d9aa2', subject: 'perf(inbox): 목록 가상화, 쿼리 캐시, 읽음 낙관적 갱신', at: 158.4, turn: 't11', tasks: ['task-123'], repository: REPO, branch: BRANCH }),
  commitOf({ short: '3776802', subject: 'feat(inbox): 필터 상태를 URL에 남기고 칩 접근성 보완', at: 6.4, turn: 't14', tasks: ['task-123'], repository: REPO, branch: BRANCH }),
]);

/* ---------------- 작업 항목 열일곱 ---------------- */

const work = [
  workOf('w1', 'consultation', true, '알림 센터의 필터 위치와 읽음 상태를 어디에 저장할지', {
    turn: 't01',
    decision: '필터 줄은 목록 바로 위에 두고, 읽음은 서버 표에 저장하되 로컬은 낙관적 캐시로만 쓴다',
  }),
  workOf('w2', 'request', true, '알림 센터에 종류별 필터 칩을 넣고, 읽음 처리까지 되게 해달라', {
    turn: 't01',
    commits: ['441e4b2', 'f866fbe'],
    children: [
      { kind: 'work', done: true, text: '필터 칩 컴포넌트와 읽음 상태 모델을 만들었다' },
      { kind: 'check', done: true, text: '타입 검사와 단위 테스트를 통과했다' },
    ],
  }),
  workOf('w3', 'followup', true, '필터와 목록 보기 전환은 툴바 맨 왼쪽에 두라', { turn: 't02', commits: ['1105386'] }),
  workOf('w4', 'followup', true, '종류, 읽음 여부, 기간으로 걸러 볼 수 있게 하라', { turn: 't03', commits: ['c4483a7'] }),
  workOf('w5', 'followup', true, '칩이 많으면 한 줄에 다 안 들어가니 접어서 보여 달라', { turn: 't04', commits: ['cabab59'] }),
  workOf('w6', 'followup', true, '안 읽음 점을 숫자 배지로 바꿔 달라', { turn: 't05', commits: ['4b2a56e'] }),
  workOf('w7', 'consultation', true, '읽음 상태를 얼마나 오래 어디에 둘지', {
    turn: 't06',
    decision: '서버에 30일 보관하고, 로컬은 새로고침하면 서버 기준으로 다시 맞춘다',
  }),
  workOf('w8', 'followup', true, '배지가 99를 넘으면 99+로 줄이고, 키보드로도 필터를 바꾸게 하라', { turn: 't07', commits: ['75a8883'] }),
  workOf('w9', 'followup', true, '알림을 누르면 오른쪽 패널에서 상세가 열리게 하라', { turn: 't08', commits: ['75a8883'] }),
  workOf('w10', 'followup', true, '모바일 폭에서는 칩이 가로로 스크롤되게 하라', { turn: 't09', commits: ['ae415b0'], note: '구분선은 그대로 두기로 함', tasks: ['issue-45'] }),
  workOf('w11', 'followup', true, '읽음 처리가 실패하면 되돌리는 낙관적 업데이트를 넣어 달라', { turn: 't10', commits: ['c5d9aa2'] }),
  workOf('w12', 'followup', true, '알림이 많을 때 느리니 가상 스크롤이나 캐시로 빠르게 하라', { turn: 't11', commits: ['c5d9aa2'], note: '5천 건 기준 첫 렌더 1.4초에서 0.2초' }),
  workOf('w13', 'followup', true, '필터 상태를 URL 쿼리에 남겨 새로고침해도 유지되게 하라', { turn: 't12', commits: ['3776802'] }),
  workOf('w14', 'followup', true, '칩을 스크린리더가 읽을 수 있게 하라', {
    turn: 't13',
    commits: ['3776802'],
    children: [
      { kind: 'work', done: true, text: '칩에 role과 aria-pressed, 배지에 읽는 이름을 붙였다' },
      { kind: 'check', done: true, text: '스크린리더 낭독 순서를 확인했다' },
    ],
  }),
  workOf('w15', 'followup', true, '그 밖에는 일단 괜찮다'),
  workOf('w16', 'finishing', true, '커밋한다', { turn: 't14', commits: ['3776802'] }),
  workOf('w17', 'finishing', true, 'BOARD.md와 issues.md를 갱신한다', {
    turn: 't14',
    tasks: ['task-123'],
    note: '갱신 없음: task-123은 이미 완료 상태이고 더 바꿀 것이 없음',
  }),
];

/* ---------------- 세션 기록 ---------------- */

const record = recordOf(
  {
    headline: '알림 센터에 종류별 필터와 읽음 처리를 넣었다',
    bullets: [
      { text: '알림 센터에 종류·읽음·기간 필터 칩을 두고, 칩이 넘치면 접어서 보이는 필터 줄을 `app/inbox/` 아래에 만들었다', commits: ['441e4b2', 'cabab59'] },
      { text: '읽음 상태는 서버 표 `inbox_reads`에 저장하고, 서버가 실패하면 되돌리는 낙관적 갱신을 넣었다', commits: ['c5d9aa2'] },
      { text: '안 읽음 점을 숫자 배지로 바꾸고 99를 넘으면 `99+`로 줄였다. 칩 줄은 화살표 키로도 옮긴다', commits: ['4b2a56e', '75a8883'] },
      { text: '필터 상태를 `?kind=&read=&range=` 쿼리에 남겨 새로고침해도 유지된다', commits: ['3776802'] },
      { text: '알림 5천 건에서 첫 렌더를 1.4초에서 0.2초로 줄였고, 타입 검사와 단위 테스트를 마친 뒤 모두 커밋했다', commits: [] },
    ],
    review: [
      { kind: 'decision', text: '필터 줄은 목록 바로 위에 둔다(사용자가 위치 질문에서 고름)' },
      { kind: 'decision', text: '읽음은 서버에 30일 보관하고 로컬은 낙관적 캐시로만 쓴다. 보관 기간을 늘리는 안은 보류했다' },
      { kind: 'interpretation', text: '"접어서 보여줘"를 "더 보기" 뒤로 접는 것으로 읽고 구현했다. 사용자에게 따로 확인하지 않았다' },
      { kind: 'unverified', text: '모바일 Safari의 가로 스크롤 관성은 직접 확인하지 못했다' },
    ],
    tags: {
      kinds: ['implementation', 'consultation'],
      components: ['Next.js', 'Postgres'],
      screens: ['알림 센터'],
      related: [
        { kind: 'task', id: 'task-123' },
        { kind: 'feature', id: 'feature-107' },
        { kind: 'issue', id: 'issue-45', url: 'https://tracker.example.com/pulse-web/issues/45' },
        { kind: 'pr', id: 'pr-231', url: 'https://git.example.com/acme/pulse-web/pull/231' },
      ],
    },
    work,
  },
  { generatedAgo: 4, coversAgo: 6 },
);

const tasks: SessionTasks = {
  items: [
    {
      id: 'task-123',
      kind: 'task',
      title: '알림 센터 필터와 읽음 처리',
      actions: { added: true, completed: true, edited: true, commit_linked: true },
      summary:
        '알림 센터에 종류별 필터와 읽음 처리를 더한다(목록 위의 필터 줄, 서버 읽음 표, 낙관적 갱신, URL 상태 유지를 포함하고 모바일에서는 칩을 가로로 스크롤한다)',
    },
    {
      id: 'issue-45',
      kind: 'issue',
      title: '모바일 폭에서 필터 칩이 잘림',
      url: 'https://tracker.example.com/pulse-web/issues/45',
      actions: { added: true, completed: false, edited: false, commit_linked: true },
      summary: '모바일 폭에서 칩이 잘리는 문제를 가로 스크롤로 풀었다(ae415b0). 실기기 확인이 남아 있어 닫지 않았다',
    },
  ],
};

/* ---------------- 편집 파일 열여섯 ---------------- */

const FILES: Array<[string, number, number]> = [
  ['web/app/inbox/filter-chips.tsx', 11, 1],
  ['web/app/inbox/page.tsx', 9, 0],
  ['web/app/inbox/inbox-list.tsx', 7, 2],
  ['web/app/inbox/detail-panel.tsx', 6, 0],
  ['web/app/inbox/use-inbox-filters.ts', 6, 1],
  ['web/app/inbox/read-state.ts', 5, 0],
  ['web/app/inbox/inbox-list.test.tsx', 5, 0],
  ['web/app/inbox/unread-badge.tsx', 4, 1],
  ['web/lib/inbox/query.ts', 4, 0],
  ['web/app/api/inbox/read/route.ts', 3, 0],
  ['web/lib/inbox/optimistic.ts', 3, 0],
  ['web/lib/inbox/query.test.ts', 3, 0],
  ['db/migrations/0042_inbox_reads.sql', 2, 0],
  ['docs/inbox-filters.md', 2, 0],
  ['BOARD.md', 2, 0],
  ['issues.md', 1, 0],
];

const editedItems: EditedFileItem[] = FILES.map(([path, edits, sub]) => ({ path, edits, subagent_edits: sub, abandoned_edits: 0 }));

const recaps: AnalysisRecaps = {
  state: 'observed',
  items: [
    { ts: ago(240), text: '알림 센터 필터 구현을 마치고 읽음 처리 연동을 이어가는 중입니다. 다음은 모바일 폭 스타일입니다.', repeats: 1 },
    { ts: ago(110), text: '낙관적 갱신과 가상 스크롤을 넣었습니다. URL 상태 유지와 접근성이 남았습니다.', repeats: 2 },
  ],
};

const analysisBase = ready({
  session_id: FLAGSHIP_ID,
  agent: 'claude',
  model: 'claude-opus-5',
  branch: BRANCH,
  project: PROJECT,
  login_emails: [USER.login_email],
  title: { text: '알림 센터에 필터와 읽음 처리 넣기', source: 'ai', state: 'ok' },
  name: { text: 'Inbox filters and read state', source: 'auto' },
  category: { key: 'idle', as_of: ago(0.4) },
  turns,
  inputs: inputsOf(turns, [{ turn_key: 't01', ts: ago(317.7) }]),
  inputs_count: 15,
  cost_source: 'otel',
  unattached_usd: 0.13,
  large_window_seen: true,
  context_mix: { input: 6_000, cache_read: 262_000, cache_create: 32_000 },
  subagents_running: 0,
  active_seconds: 4_380,
  tags: {
    change_area: { value: 'web/app', state: 'observed' },
    task_type: { value: 'feature', share: 0.64, basis: 'classifier' },
    rules_files: [
      { path: 'AGENTS.md', version: 5, discovered_at: ago(60 * 24 * 4) },
      { path: 'CLAUDE.md', version: 3, discovered_at: ago(60 * 24 * 12) },
    ],
  },
  plan_flags: { has_plan_file: true, used_plan_mode: true },
  signals: {
    turn: S.turn('ended'),
    question: S.question(0),
    background: S.backgroundUnknown('pre_collect'),
    uncommitted: S.uncommitted(0),
    todo: S.todo(0),
  },
  outputs: {
    edited_files: { state: 'observed', active_total: 16, abandoned_total: 0, truncated: false, items: editedItems },
    lines: { added: 1_184, removed: 301, source: 'cost_state', partial: false },
    prs: { items: [PR], truncated: false },
  },
  recaps,
  questions: [QUESTION],
  plans: [PLAN],
  live: {
    instances: [{
      status: 'idle', waiting_kind: '', heartbeat_at: ago(0.4), name: 'inbox-filters', stale: false,
      // 미리보기의 지어낸 프로세스 수치다. 대화 기록의 양을 실제 프로세스 입출력으로 표시하지 않는다.
      process: { pid: 18427, input_lines: null, output_lines: null, input_bytes: null, output_bytes: null, io_source: 'unknown' },
    }],
    collector: { heartbeat_at: ago(0.3), last_stored_at: ago(0.5) },
  },
  computed_ago: 0.6,
});

export const flagship = (): DetailFixture =>
  finalize({
    base: analysisBase,
    meta: { session_id: FLAGSHIP_ID, ...USER },
    label: '대표 세션. 요청 14건, 입력 15건, 커밋 10건, 변경 파일 16건, 작업 항목 17개, 질문 카드 둘과 플랜 카드를 가진 레퍼런스 밀도의 세션.',
    covers: ['실행 중(쉬는 중)', '판정: 통과·통과·판단 불가·통과·통과', '기록 ready', '커밋 10건', 'task 동작 아이콘', '질문 카드 2개', '플랜 승인', '타임라인 15입력'],
    identities: { user_ids: [USER.user_id], profile_emails: [USER.profile_email], login_emails: [USER.login_email], mixed: false },
    alive: true,
    record,
    commits,
    tasks,
    note: '다음 세션에서 키보드 단축키 문서를 확인할 것.\n읽음 보관 기간은 30일로 정했지만 운영 정책과 한 번 더 맞춰 볼 것.',
    classification: 'working',
    extras,
    extraMessages: [
      { id: 'm-compact', ts: ago(180), role: 'system', turn_key: 't11', text: '컨텍스트를 압축했습니다. 앞선 대화는 요약으로 이어집니다.' },
    ],
  });
