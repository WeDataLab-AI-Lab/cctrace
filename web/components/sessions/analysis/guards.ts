import type { SessionAnalysis, SessionAnalysisFull } from '@/lib/session-analysis-types';

/**
 * 사실이 있는 응답(`ready`·`stale`)인가. 한 변수에서 두 `state` 리터럴을 이어 `!==`로 걸러 내면 타입스크립트가
 * 나머지 유니온을 좁히지 못하므로, 좁히는 일은 이 판별 함수가 맡는다.
 */
const isFullAnalysis = (a: SessionAnalysis): a is SessionAnalysisFull => a.state === 'ready' || a.state === 'stale';

export { isFullAnalysis };
