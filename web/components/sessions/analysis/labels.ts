/**
 * 분석 화면의 한국어 문안과 열거값 라벨.
 *
 * 문장과 용어는 계획 §5.2(관측 신호)와 §5.3(관측 범주)에서 그대로 가져왔다. 툴팁은 기존 화면의
 * 어조("~입니다/~습니다")를 따른다. 계약에 없는 값이 오면 화면이 깨지지 않도록 호출하는 쪽이
 * `?? 원문`으로 받는다(라벨 표는 닫힌 유니온이지만 서버는 그보다 새 값을 낼 수 있다).
 */

import type {
  ContextSource,
  CostSource,
  InputKind,
  NameSource,
  PlanDecision,
  QuestionStatus,
  RecapAbsentReason,
  SessionCategoryKey,
  SessionSort,
  TitleSource,
  TurnCostReason,
  TurnCostSource,
  TurnStatus,
  TurnStatusReason,
  WaitingKind,
} from '@/lib/session-analysis-types';

/** 모르는 값을 숫자 0과 구별하는 표기. */
const UNKNOWN_TEXT = '알 수 없음';

/* ---------------- 관측 범주 (계획 §5.3) ---------------- */

type CategoryTone = 'attention' | 'pending' | 'live' | 'quiet' | 'idle' | 'unknown' | 'faded';

interface CategoryMeta {
  /** 계획 §5.3의 이름 그대로. */
  label: string;
  /** 좁은 자리(목록 행)에서 쓰는 줄임말. 같으면 label 과 같다. */
  shortLabel: string;
  hint: string;
  tone: CategoryTone;
}

/** 범주 SQL의 판정 순서(§8.4). 칩도 이 순서로 놓는다. */
const CATEGORY_ORDER: readonly SessionCategoryKey[] = [
  'waiting',
  'running',
  'ended',
  'stale_live',
  'open_question',
  'idle',
  'collector_delayed',
  'recent',
  'inactive',
];

const CATEGORY_META: Record<SessionCategoryKey, CategoryMeta> = {
  waiting: {
    label: '응답 대기',
    shortLabel: '응답 대기',
    hint: '살아 있는 실행 인스턴스가 응답을 기다리고 있습니다.',
    tone: 'attention',
  },
  running: {
    label: '진행 중',
    shortLabel: '진행 중',
    hint: '살아 있는 실행 인스턴스가 일하는 중입니다. 실행 정보가 없는 세션은 마지막 턴이 열려 있고 최근에 활동이 있었을 때입니다.',
    tone: 'live',
  },
  ended: {
    label: '종료됨',
    shortLabel: '종료됨',
    hint: '세션 종료 표식이나 인스턴스의 사라짐이 마지막 활동보다 뒤입니다. 종료 뒤에 resume으로 활동이 오면 종료는 무효가 됩니다.',
    tone: 'quiet',
  },
  stale_live: {
    label: '판단 불가(오래된 생존 행)',
    shortLabel: '판단 불가',
    hint: '실행 중이라고 보고된 상태가 오래되어 진행 중·응답 대기로 세지 않았습니다. 이미 끝난 세션이 실행 중으로 남은 것일 수 있습니다.',
    tone: 'unknown',
  },
  open_question: {
    label: '열린 질문',
    shortLabel: '열린 질문',
    hint: '답하지 않은 질문이 있고 그 뒤에 새 턴이 없습니다.',
    tone: 'pending',
  },
  idle: {
    label: '쉬는 중',
    shortLabel: '쉬는 중',
    hint: '살아 있는 실행 인스턴스가 입력을 기다리며 쉬고 있습니다.',
    tone: 'idle',
  },
  collector_delayed: {
    label: '수집 지연',
    shortLabel: '수집 지연',
    hint: '최근 활동이 있지만 이 사용자의 수집기가 한동안 응답하지 않아 최신 상태를 알 수 없습니다.',
    tone: 'unknown',
  },
  recent: {
    label: '최근 활동',
    shortLabel: '최근 활동',
    hint: '실행 정보는 없지만 최근에 활동이 있었습니다.',
    tone: 'quiet',
  },
  inactive: {
    label: '24시간 이상 활동 없음',
    shortLabel: '활동 없음',
    hint: '마지막 활동이 24시간보다 오래됐습니다.',
    tone: 'faded',
  },
};

/** 계약에 없는 범주 키가 와도 라벨을 만든다. */
const categoryMeta = (key: string): CategoryMeta =>
  (CATEGORY_META as Record<string, CategoryMeta | undefined>)[key] ?? {
    label: key,
    shortLabel: key,
    hint: '이 화면이 아직 모르는 범주입니다.',
    tone: 'quiet',
  };

const WAITING_KIND_LABEL: Record<WaitingKind, string> = {
  input: '입력 필요',
  permission: '권한 승인',
  dialog: '대화 상자',
  sandbox: '샌드박스 요청',
  worker: '워커 요청',
  goal: '목표 제안',
  other: '기타',
};

const waitingKindLabel = (kind: string): string =>
  (WAITING_KIND_LABEL as Record<string, string | undefined>)[kind] ?? kind;

/* ---------------- 정렬 (B7) ---------------- */

const SORT_META: Record<SessionSort, { label: string; hint: string }> = {
  start: { label: '시작', hint: '시작 시각이 최신인 순서' },
  activity: { label: '활동', hint: '마지막 활동이 최신인 순서' },
  cost: { label: '비용', hint: '비용이 큰 순서' },
  requests: { label: '요청 수', hint: '요청 수가 많은 순서. 요청 수를 모르는 세션은 맨 뒤에 놓습니다.' },
};

const SORT_ORDER: readonly SessionSort[] = ['start', 'activity', 'cost', 'requests'];

/* ---------------- 제목·이름 (D3, D4) ---------------- */

const TITLE_SOURCE_LABEL: Record<TitleSource, string> = {
  custom: '직접 정한 제목',
  ai: 'AI 제목',
  agent_name: '에이전트 이름',
  first_request: '첫 요청',
  none: '출처 없음',
};

const NAME_SOURCE_LABEL: Record<NameSource, string> = {
  user: '직접 지정',
  derived: '파생',
  auto: '자동',
  custom_title: '사용자 지정 제목',
};

const NAME_SOURCE_HINT: Record<NameSource, string> = {
  user: '사용자가 /rename 으로 정한 이름입니다.',
  derived: '대화에서 파생된 이름입니다.',
  auto: 'Claude Code가 자동으로 붙인 이름입니다.',
  custom_title: '실행 중인 인스턴스가 없어 사용자 지정 제목을 이름으로 보입니다.',
};

/* ---------------- 입력 출처 (E4) ---------------- */

const INPUT_KIND_LABEL: Record<InputKind, string> = {
  typed: '직접 입력',
  answer: '질문 답',
  queued: '작업 중 입력',
  notification: '백그라운드 알림',
  peer: '다른 세션',
  scheduled: '예약 실행',
  command: '명령',
  channel: '외부 채널',
  auto: '자동 이어가기',
  sdk: 'SDK 호출',
  system: '시스템',
  unknown: '출처 모름',
};

const inputKindLabel = (kind: string): string =>
  (INPUT_KIND_LABEL as Record<string, string | undefined>)[kind] ?? kind;

/* ---------------- 턴 상태 (D16) ---------------- */

const TURN_STATUS_LABEL: Record<TurnStatus, string> = {
  ended: '끝남',
  interrupted: '중단',
  error: '오류',
  no_response: '응답 없음',
  open: '진행 중',
  undeterminable: '판단 불가',
};

const TURN_STATUS_HINT: Record<TurnStatus, string> = {
  ended: '이 턴이 끝났습니다.',
  interrupted: '사용자가 이 턴을 중단했습니다.',
  error: '마지막 응답이 API 오류였습니다.',
  no_response: '입력 뒤에 에이전트의 응답 기록이 없습니다.',
  open: '도구 호출이나 결과로 끝나 아직 열려 있는 턴입니다.',
  undeterminable: '필요한 필드가 없어 이 턴의 상태를 판단할 수 없습니다.',
};

const TURN_STATUS_REASON_TEXT: Record<TurnStatusReason, string> = {
  pre_collect: '수집 규칙 이전에 쌓인 기록이라 이 턴이 끝났는지 판단할 수 없습니다.',
  redacted: '비식별화로 응답 내용이 가려져 이 턴이 끝났는지 판단할 수 없습니다.',
  no_lifecycle: 'Codex가 이 턴의 시작·끝 기록을 남기지 않아 판단할 수 없습니다.',
  no_end_line: 'Codex 턴의 끝 기록이 없고 하루가 넘도록 이어지지 않았습니다. 끝나지 않았다고 단정하지 않고 판단 불가로 둡니다.',
};

const QUESTION_STATUS_LABEL: Record<QuestionStatus, string> = {
  answered: '답함',
  rejected: '거절됨',
  unanswered: '답 없음',
};

/* ---------------- 비용 (§4.4) ---------------- */

interface SourceMeta {
  label: string;
  hint: string;
}

const COST_SOURCE_META: Record<CostSource, SourceMeta> = {
  otel: { label: 'OTEL', hint: 'Claude Code가 OTEL로 보낸 측정값의 합입니다.' },
  otel_partial: {
    label: 'OTEL 일부',
    hint: '세션의 앞부분 OTEL 기록이 보존 기간을 넘어 지워졌습니다. 남은 OTEL 기록만 더한 값이라 실제보다 적을 수 있습니다.',
  },
  estimate: { label: '추정', hint: 'OTEL 기록이 없는 세션이라 토큰 수로 추정한 값입니다.' },
  estimate_pending: {
    label: '추정 · 반영 대기',
    hint: '새 기록의 추정 비용이 아직 반영되지 않았습니다. 지금까지 반영된 만큼의 합이라 실제보다 적을 수 있습니다.',
  },
  none: { label: '출처 없음', hint: '붙은 OTEL 기록도 추정 비용도 없어 비용을 알 수 없습니다.' },
};

const COST_NOTE_TEXT = {
  otel_expired: 'OTEL 기록이 모두 보존 기간을 넘어 지워져 추정으로 대신한 값입니다.',
} as const;

const TURN_COST_SOURCE_META: Record<TurnCostSource, SourceMeta> = {
  otel: { label: 'OTEL', hint: 'OTEL 측정값입니다.' },
  otel_partial: { label: 'OTEL 일부', hint: '이 턴의 OTEL 기록 일부가 지워져, 남은 기록만 더한 값입니다.' },
  expired: {
    label: '만료',
    hint: '이 턴의 OTEL 기록이 보존 기간을 넘어 지워져 비용이 남아 있지 않습니다. 세션 합계에서는 0으로 셉니다.',
  },
  estimate: { label: '추정', hint: '토큰 수로 추정한 값입니다.' },
  estimate_pending: { label: '추정 · 대기', hint: '추정 비용이 아직 다 반영되지 않았습니다. 지금까지의 합입니다.' },
  no_rate: { label: '요율 없음', hint: '이 모델의 요율을 몰라 비용을 추정할 수 없습니다.' },
  unattributable: { label: '귀속 불가', hint: '이 턴에 속한 호출을 가를 수 없습니다.' },
  none: { label: 'API 호출 없음', hint: '이 턴에는 API 요청이 없습니다.' },
};

const TURN_COST_REASON_TEXT: Record<TurnCostReason, string> = {
  pre_p0: '턴 구분 정보가 없던 시기(P0 이전)에 수집된 턴이라 어느 호출이 이 턴의 것인지 알 수 없습니다. OTEL 비용은 "턴에 붙지 않은 호출"에 들어 있습니다.',
  shared_prompt_id: '명령 턴과 그 뒤 입력 턴이 같은 prompt_id를 써서 호출을 턴으로 나눌 수 없습니다. 이 비용은 "턴에 붙지 않은 호출"에 한 번만 들어 있습니다.',
};

/** 헤더 비용 칸의 "턴에 붙지 않은 호출" 설명 (§4.2). */
const UNATTACHED_COST_HINT = '제목 생성, 압축, P0 이전 턴, 나눌 수 없는 명령 턴 등 어느 턴에도 붙지 않은 호출의 비용입니다.';

/* ---------------- 컨텍스트 (§3.11) ---------------- */

const CONTEXT_SOURCE_META: Record<ContextSource, SourceMeta> = {
  observed: { label: '관측', hint: '그 요청 시점에 기록된 근거로 창 크기를 정했습니다.' },
  model_default: {
    label: '모델 표기',
    hint: '창 크기가 하나뿐인 모델이라 모델 표의 값을 썼습니다(추정).',
  },
  unknown: {
    label: '모름',
    hint: '창 크기를 알 수 있는 근거가 없어 퍼센트를 내지 않고 토큰 수만 보입니다.',
  },
};

const LARGE_WINDOW_HINT = '이 세션에서 큰 창(1M)을 쓴 흔적이 있습니다. 요청별 창 크기는 아닙니다.';

/* ---------------- 플랜 (F7) ---------------- */

const PLAN_DECISION_LABEL: Record<PlanDecision, string> = {
  approved: '승인',
  rejected: '반려',
  interrupted: '중단',
  pending: '대기 중',
};

const PLAN_DECISION_HINT: Record<PlanDecision, string> = {
  approved: '플랜 승인 요청에 대한 결과가 오류 없이 돌아왔습니다.',
  rejected: '사용자가 플랜을 반려했습니다.',
  interrupted: '플랜 승인 요청이 중단됐습니다.',
  pending: '플랜 승인 요청에 대한 결과가 아직 없습니다.',
};

/** 권한 모드 배지. 계약이 값 집합을 정하지 않아, 모르는 값은 원문을 그대로 보인다. */
const MODE_LABEL: Record<string, string> = {
  plan: '플랜',
  acceptEdits: '편집 자동 승인',
  bypassPermissions: '권한 우회',
  auto: '자동',
  dontAsk: '묻지 않음',
};

/* ---------------- 요약 (D12) ---------------- */

const RECAP_ABSENT_TEXT: Record<RecapAbsentReason, string> = {
  too_few_turns: '턴이 3개 미만인 세션에는 recap이 생기지 않습니다.',
  headless: '헤드리스 세션에는 recap이 생기지 않습니다.',
  not_away: '3분 이상 자리를 비워 터미널 포커스가 빠졌을 때만 recap이 생깁니다. 이 세션에는 그런 구간이 없었습니다.',
  pre_collect: '수집 규칙 이전에 쌓인 기록이라 recap이 있었는지 판단할 수 없습니다.',
  withheld: 'recap 본문을 서버에 올리지 않아 보이지 않습니다(보류됨). recap이 없다는 뜻이 아닙니다.',
  redacted: '비식별화로 recap 본문이 가려졌습니다. recap이 없다는 뜻이 아닙니다.',
};

/** recap이 생기는 조건 (계획 D12). */
const RECAP_CONDITION_TEXT = 'recap은 턴이 3개 이상이고, 3분 이상 자리를 비웠고, 헤드리스가 아닌 세션에서 생깁니다.';

export {
  UNKNOWN_TEXT,
  CATEGORY_ORDER,
  CATEGORY_META,
  categoryMeta,
  WAITING_KIND_LABEL,
  waitingKindLabel,
  SORT_META,
  SORT_ORDER,
  TITLE_SOURCE_LABEL,
  NAME_SOURCE_LABEL,
  NAME_SOURCE_HINT,
  INPUT_KIND_LABEL,
  inputKindLabel,
  TURN_STATUS_LABEL,
  TURN_STATUS_HINT,
  TURN_STATUS_REASON_TEXT,
  QUESTION_STATUS_LABEL,
  COST_SOURCE_META,
  COST_NOTE_TEXT,
  TURN_COST_SOURCE_META,
  TURN_COST_REASON_TEXT,
  UNATTACHED_COST_HINT,
  CONTEXT_SOURCE_META,
  LARGE_WINDOW_HINT,
  PLAN_DECISION_LABEL,
  PLAN_DECISION_HINT,
  MODE_LABEL,
  RECAP_ABSENT_TEXT,
  RECAP_CONDITION_TEXT,
};
export type { CategoryMeta, CategoryTone, SourceMeta };
