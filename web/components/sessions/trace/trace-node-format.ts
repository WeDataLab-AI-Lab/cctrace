import type { TraceNode } from '@/lib/session-trace-node-types';

const traceNodeKindLabel = (kind: string): string => ({
  turn: '턴', request: '요청', response: '응답', tool: '도구', agent: '하위 에이전트', hook: '훅', instructions: '지시문',
  context: '실행 설정', metadata: '세션 기록', usage: '사용량', event: '기록 내용',
}[kind] ?? kind);

const traceNodeStatusLabel = (status: string): string => ({
  running: '실행 중', completed: '완료', success: '완료', error: '오류', failed: '실패', interrupted: '중단', unknown: '확인 불가',
}[status] ?? status);

const traceNodeStatusTone = (status: string): string => status === 'error' || status === 'failed'
  ? 'text-danger-strong' : status === 'running' ? 'text-agent-codex' : 'text-ink-3';

const traceNodeAvailabilityLabel = (availability: string): string => ({
  observed: '수집됨', collected: '수집됨', available: '수집됨',
  not_collected: '미수집', missing: '미수집', redacted: '가림', withheld: '가림',
  truncated: '잘림', not_queried: '미조회', unqueried: '미조회', error: '오류', unavailable: '확인 불가', pending: '준비 중',
}[availability] ?? availability);

const traceReasonText = (reason: string): string => ({
  child_context_not_linked: '하위 에이전트가 이어받은 대화 범위를 이 기록과 연결하지 못했습니다.',
  harness_recorded_api_request: '하네스가 실제 요청으로 기록한 API 요청 본문입니다.',
  harness_recorded_api_request_page: '하네스가 기록한 API 요청 본문입니다. 크기가 커서 나눠 읽습니다.',
  multiple_exact_tool_results: '같은 도구 호출에 결과 기록이 여러 개라 하나로 정하지 못했습니다.',
}[reason] ?? reason);

const traceNodeTimeLabel = (value?: string | null): string => {
  if (!value || !Number.isFinite(Date.parse(value))) return '시각 확인 불가';
  return new Intl.DateTimeFormat('ko-KR', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }).format(new Date(value));
};

const formatObservedDuration = (node: TraceNode): string | undefined => {
  if (!node.started_at || !node.ended_at) return;
  const started = Date.parse(node.started_at), ended = Date.parse(node.ended_at);
  if (!Number.isFinite(started) || !Number.isFinite(ended) || ended < started) return;
  const seconds = new Intl.NumberFormat('ko-KR', { minimumFractionDigits: 1, maximumFractionDigits: 1 }).format((ended - started) / 1000);
  return `${seconds}초 관측`;
};

export { traceNodeKindLabel, traceNodeStatusLabel, traceNodeStatusTone, traceNodeAvailabilityLabel, traceReasonText, traceNodeTimeLabel, formatObservedDuration };
