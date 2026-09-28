/**
 * Weekly AI report usage — the tokens the server's Codex account spends building
 * weekly reports. It belongs to no person, so the server reports it as its own
 * agent ('weekly') and, in per-user groupings, as a user key of the same name
 * (weeklyUserKeyExpr in internal/store/weekly_usage.go). Keep the two in sync.
 */

export const WEEKLY_USAGE_KEY = 'weekly';

export const WEEKLY_USAGE_HINT =
  '주간 AI 리포트를 만들 때 서버의 Codex 계정이 쓴 토큰입니다. 사용자 개인의 에이전트 사용량과 따로 집계합니다.';

export const isWeeklyUsageKey = (key: string | undefined): boolean => key === WEEKLY_USAGE_KEY;
