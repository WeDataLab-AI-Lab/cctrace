/**
 * 세션 기록(D12~D15)의 순수 로직: 상태별 화면, 인라인 코드 나누기, 검토 사항 접두, 태그 이름.
 *
 * 기록은 서버의 LLM이 대화 원문으로 쓴다. 본문이 있는 상태는 `ready`·`stale`뿐이고, 나머지 다섯 상태는 각자 무슨 일이
 * 있었는지를 적는다(빈칸이 아니다). 기록이 없다고 작업이 없었다는 뜻이 아니다.
 */

import type {
  RecordKindTag,
  RecordState,
  RecordWithContent,
  RelatedKind,
  ReviewKind,
  SessionRecord,
} from '@/lib/session-analysis-types';

const hasRecordContent = (record: SessionRecord | undefined): record is RecordWithContent =>
  record !== undefined && (record.state === 'ready' || record.state === 'stale');

/** 종별 태그의 화면 이름(D14). */
const KIND_TAG_LABEL: Record<RecordKindTag, string> = {
  implementation: '구현',
  research: '조사',
  maintenance: '정비',
  consultation: '상담',
  bugfix: '버그 수정',
  docs: '문서',
};

const kindTagLabel = (kind: string): string => (KIND_TAG_LABEL as Record<string, string | undefined>)[kind] ?? kind;

const REVIEW_PREFIX: Record<ReviewKind, string> = { decision: '결정', interpretation: '해석', unverified: '미확인' };

const REVIEW_HINT: Record<ReviewKind, string> = {
  decision: '대화에서 사용자가 정했거나 합의한 내용입니다.',
  interpretation: '모호한 요청을 모델이 이렇게 읽고 진행한 내용입니다. 사용자가 확인하지 않았습니다.',
  unverified: '확인하지 못한 채로 남은 내용입니다.',
};

const reviewPrefix = (kind: string): string => (REVIEW_PREFIX as Record<string, string | undefined>)[kind] ?? kind;

const RELATED_KIND_LABEL: Record<RelatedKind, string> = { task: 'task', issue: '이슈', pr: 'PR', feature: '기능' };

interface TextSpan {
  code: boolean;
  text: string;
}

/**
 * 글머리표의 백틱(`)을 인라인 코드로 나눈다. 짝이 없는 백틱은 글자 그대로 둔다(내용을 버리지 않는다).
 * 빈 코드(``)는 코드로 치지 않는다.
 */
const splitInlineCode = (text: string): TextSpan[] => {
  const spans: TextSpan[] = [];
  let rest = text;
  while (rest.length > 0) {
    const open = rest.indexOf('`');
    if (open < 0) break;
    const close = rest.indexOf('`', open + 1);
    if (close < 0) break;
    if (open > 0) spans.push({ code: false, text: rest.slice(0, open) });
    const code = rest.slice(open + 1, close);
    if (code.length > 0) spans.push({ code: true, text: code });
    else spans.push({ code: false, text: '``' });
    rest = rest.slice(close + 1);
  }
  if (rest.length > 0) spans.push({ code: false, text: rest });
  return spans;
};

interface RecordNotice {
  title: string;
  body: string;
  /** 이 상태에서 Claude Code의 recap을 대신 보일 수 있는가. 가려졌거나 보류된 상태는 recap도 같은 이유로 비어 있다. */
  recapFallback: boolean;
  tone: 'neutral' | 'pending' | 'warning' | 'danger';
  reason?: string;
  reasonDescription?: string;
}

const RECORD_REASON_DESCRIPTION: Record<string, string> = {
  facts_missing: '세션 분석 사실을 아직 받지 못했습니다.',
  turn_boundaries: '요청과 턴의 경계를 확인할 자료가 없습니다.',
  prompt_policy: '비식별화 정책으로 요청 본문이 가려져 있습니다.',
  too_large: '분석 입력이 상한을 넘어서 기록을 만들지 않았습니다.',
  no_requests: '요청 턴이 없어 기록 생성 대상이 아닙니다.',
  waiting_quiet: '세션이 생성 대기 시간에 아직 도달하지 않았습니다.',
  waiting_budget: '일일 토큰 한도 또는 예약량 때문에 생성이 기다리고 있습니다.',
  queued: '세션 기록 생성 작업이 대기열에 있습니다.',
  private_scope: '사용자가 이 세션 또는 프로젝트를 비공개로 설정했습니다.',
  mixed_identity: '세션에 여러 신원의 기록이 섞여 있어 생성하지 않았습니다.',
  disabled: '관리자가 세션 기록 생성을 껐습니다.',
  no_runtime: '사용할 수 있는 AI API 런타임이 없습니다.',
  runtime_unconfigured: '선택한 AI API 런타임이 구성되지 않았습니다.',
  runtime_unsupported: '선택한 런타임은 세션 기록 생성을 지원하지 않습니다.',
  runtime_unavailable: '선택한 AI API 런타임을 현재 사용할 수 없습니다.',
  consent_required: '현재 런타임과 고지 판에 대한 사용자의 동의가 없습니다.',
  new_activity: '기존 기록 뒤에 새 활동이 있어 아직 반영되지 않았습니다.',
  prompt_version: '생성 규칙이 바뀌어 새 판으로 다시 써야 합니다.',
  schema_violation: '모델 응답이 기록 형식 또는 근거 검증을 통과하지 못했습니다.',
  protocol_error: '모델 응답 프로토콜을 확인하지 못했습니다.',
  output_limit: '모델 응답이 허용된 출력 상한을 넘었습니다.',
  refused: '모델이 요청을 거절했습니다.',
  context_window_exceeded: '모델 입력이 컨텍스트 한도를 넘었습니다.',
  budget_exceeded: '모델 호출 예산 한도를 넘었습니다.',
  runtime_error: 'AI 런타임이 생성을 완료하지 못했습니다.',
  canceled: '생성 요청이 취소되었습니다.',
};

const recordReasonDescription = (reason: string | null | undefined): string | undefined => {
  if (!reason) return undefined;
  return RECORD_REASON_DESCRIPTION[reason] ?? `서버가 사유 코드 ${reason}를 보고했습니다.`;
};

/** 본문이 없는 다섯 상태의 안내. */
const RECORD_NOTICE: Record<Exclude<RecordState, 'ready' | 'stale'>, RecordNotice> = {
  generating: {
    title: '세션 기록을 만드는 중입니다',
    body: '서버가 대화를 읽고 개요, 검토 사항, 작업 상황을 쓰는 중입니다. 끝나면 이 자리와 아래 작업 상황이 채워지고, 판정의 "작업 내용"도 그때 정해집니다.',
    recapFallback: true,
    tone: 'pending',
  },
  none: {
    title: '아직 세션 기록이 없습니다',
    body: '기록은 대화가 끝난 뒤 서버가 만들어 둡니다. 아직 만들지 않았거나 만들 대상이 아닙니다. 기록이 없어도 아래 턴 상세와 성과는 그대로입니다.',
    recapFallback: true,
    tone: 'neutral',
  },
  withheld: {
    title: '세션 기록을 만들지 않았습니다(보류됨)',
    body: 'AI 입력에 대한 동의나 조직 설정이 없어 대화 본문을 기록 생성에 보내지 않았습니다. 기록이 없다는 뜻이지 작업이 없었다는 뜻이 아닙니다.',
    recapFallback: true,
    tone: 'warning',
  },
  redacted: {
    title: '비식별화로 세션 기록을 만들 수 없습니다',
    body: '프롬프트 비식별화 설정으로 대화 본문이 가려져 있어 개요와 작업 상황을 쓸 수 없습니다.',
    recapFallback: false,
    tone: 'warning',
  },
  failed: {
    title: '세션 기록 생성에 실패했습니다',
    body: '서버가 기록을 쓰지 못했습니다. 판정의 "작업 내용"은 판단 불가로 남고, 다음 재계산 때 다시 시도합니다.',
    recapFallback: true,
    tone: 'danger',
  },
  disabled: {
    title: '관리자가 세션 기록 생성을 껐습니다',
    body: '관리자 설정에서 기능을 켜기 전에는 세션 대화를 기록 생성에 보내지 않습니다.',
    recapFallback: true,
    tone: 'warning',
  },
  missing: {
    title: '세션 기록을 받지 못했습니다',
    body: '서버 응답에 세션 기록이 없습니다. 기록이 없다는 뜻이지 작업이 없었다는 뜻은 아닙니다.',
    recapFallback: true,
    tone: 'neutral',
  },
  blocked: {
    title: '세션 기록 생성이 차단되었습니다',
    body: '동의, 관리자 설정, 비공개 범위 또는 입력 상태 때문에 기록을 만들지 않았습니다. 아래 사유 코드를 확인하세요.',
    recapFallback: true,
    tone: 'warning',
  },
  waiting_budget: {
    title: '일일 토큰 한도로 생성을 기다립니다',
    body: '사용량과 예약량이 한도 안으로 들어올 때까지 기록 생성을 보류합니다.',
    recapFallback: true,
    tone: 'pending',
  },
};

/** 상태를 괄호 안에 짧게 적을 때의 말. */
const RECORD_STATE_WORD: Record<RecordState, string> = {
  ready: '최신',
  stale: '이전 기록',
  generating: '만드는 중',
  none: '아직 없음',
  withheld: '보류됨',
  redacted: '가려짐',
  failed: '생성 실패',
  disabled: '관리자 꺼짐',
  missing: '기록 없음',
  blocked: '생성 차단',
  waiting_budget: '한도 대기',
};

const recordNotice = (state: string, reason?: string | null): RecordNotice | null => {
  const notice = (RECORD_NOTICE as Record<string, RecordNotice | undefined>)[state];
  if (!notice) return null;
  return {
    ...notice,
    ...(reason ? { reason, reasonDescription: recordReasonDescription(reason) } : {}),
  };
};

export {
  hasRecordContent,
  KIND_TAG_LABEL,
  kindTagLabel,
  REVIEW_PREFIX,
  REVIEW_HINT,
  reviewPrefix,
  RELATED_KIND_LABEL,
  splitInlineCode,
  RECORD_NOTICE,
  RECORD_STATE_WORD,
  recordReasonDescription,
  recordNotice,
};
export type { TextSpan, RecordNotice };
