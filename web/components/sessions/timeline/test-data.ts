/**
 * 타임라인 시험이 함께 쓰는 작은 자료. 합성 데이터이고 픽스처 모듈에 기대지 않는다.
 * 시각은 모두 지역 시각으로 만든다(화면이 브라우저 시간대로 그리므로 기대값도 같은 시간대여야 한다).
 */

import type {
  AnalysisPlan,
  AnalysisQuestion,
  QuestionItem,
  QuestionOption,
  TimelineMessage,
} from '@/lib/session-analysis-types';

/** 17:00:00. 아래 메시지는 이보다 5시간쯤 앞이다. */
const NOW = new Date(2026, 9, 3, 17, 0, 0).getTime();

const at = (hour: number, minute: number, second = 0): string => new Date(2026, 9, 3, hour, minute, second).toISOString();

const userMsg = (n: number, of: number, over: Partial<TimelineMessage> = {}): TimelineMessage => ({
  id: `u${n}`,
  ts: at(11, 50 + n),
  role: 'user',
  turn_key: `t${n}`,
  n,
  of,
  source: 'typed',
  text: `사용자 입력 ${n}`,
  ...over,
});

const assistantMsg = (id: string, turn: string, over: Partial<TimelineMessage> = {}): TimelineMessage => ({
  id,
  ts: at(12, 10),
  role: 'assistant',
  turn_key: turn,
  text: `응답 ${id}`,
  ...over,
});

const systemMsg = (id: string, turn: string, text: string): TimelineMessage => ({
  id,
  ts: at(12, 20),
  role: 'system',
  turn_key: turn,
  text,
});

const option = (label: string, over: Partial<QuestionOption> = {}): QuestionOption => ({
  label,
  description: `${label}에 대한 설명`,
  recommended: false,
  ...over,
});

const questionItem = (over: Partial<QuestionItem> = {}): QuestionItem => ({
  header: '표시 위치',
  question: '알림 센터를 어디에 둘까요?',
  multi_select: false,
  options: [option('헤더 드롭다운', { recommended: true }), option('독립 페이지')],
  answer: { labels: ['헤더 드롭다운'], free_text: null },
  ...over,
});

const questionOf = (over: Partial<AnalysisQuestion> = {}): AnalysisQuestion => ({
  tool_use_id: 'tq1',
  turn_key: 't1',
  ts: at(11, 57, 38),
  status: 'answered',
  state: 'observed',
  items: [questionItem()],
  ...over,
});

const planOf = (over: Partial<AnalysisPlan> = {}): AnalysisPlan => ({
  turn_key: 't2',
  ts: at(12, 30),
  file_path: '/plans/inbox-filters.md',
  decision: 'approved',
  body: '1. 필터 칩을 만든다\n2. 읽음 상태를 저장한다',
  state: 'observed',
  ...over,
});

/**
 * 입력 넷의 작은 대화. 입력 1은 이미지 둘과 줄바꿈이 있는 요청, 입력 2는 질문에 답한 입력, 입력 3은 작업 중 입력,
 * 입력 4는 백그라운드 알림이다. 중간 출력 둘(a1, a4), 답하지 않은 질문(a5), 플랜(a6), system 한 줄이 섞여 있다.
 */
const CONVERSATION: TimelineMessage[] = [
  userMsg(1, 4, { ts: at(11, 56, 22), text: '알림 센터에 필터를 넣어 주세요.\n종류별로 걸 수 있으면 좋겠어요', images: 2 }),
  assistantMsg('a1', 't1', { ts: at(11, 57), text: '파일을 먼저 읽어 보겠습니다.', intermediate: true }),
  assistantMsg('a2', 't1', { ts: at(11, 57, 30), text: '구현 방향을 정리했습니다.' }),
  userMsg(2, 4, {
    ts: at(11, 58, 5),
    turn_key: 't1',
    source: 'answer',
    text: undefined,
    question: questionOf(),
  }),
  userMsg(3, 4, { ts: at(12, 17), source: 'queued', text: '작업 중에 덧붙이는 요청' }),
  assistantMsg('a4', 't3', { ts: at(12, 18), text: '테스트를 돌려 보겠습니다.', intermediate: true }),
  assistantMsg('a5', 't3', {
    ts: at(12, 19),
    text: undefined,
    question: questionOf({
      tool_use_id: 'tq2',
      turn_key: 't3',
      status: 'unanswered',
      items: [questionItem({ header: '배포 방식', question: '어떻게 배포할까요?', answer: null })],
    }),
  }),
  assistantMsg('a6', 't3', { ts: at(12, 20), text: undefined, plan: planOf({ turn_key: 't3' }) }),
  systemMsg('s1', 't3', '컨텍스트를 압축했습니다'),
  userMsg(4, 4, { ts: at(12, 40), turn_key: 't4', source: 'notification', text: '백그라운드 작업 완료: 통합 테스트' }),
  assistantMsg('a7', 't4', { ts: at(12, 41), text: '통합 테스트가 모두 통과했습니다.' }),
];

export { NOW, at, userMsg, assistantMsg, systemMsg, option, questionItem, questionOf, planOf, CONVERSATION };
