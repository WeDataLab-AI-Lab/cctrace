import type { SessionAnalysisFull, SessionRecord } from '@/lib/session-analysis-types';
import { hasRecordContent } from '@/components/sessions/analysis/record';

/** 실제 LLM 호출 없이 상태 전환만 시연한다. 기록에 근거가 없는 작업 완료는 만들지 않는다. */
export const generateDemoRecord = (analysis: SessionAnalysisFull, title: string, now: number): SessionRecord => {
  const current = analysis.record;
  const generatedAt = new Date(now).toISOString();
  if (current?.state === 'redacted' || current?.state === 'withheld') return current;
  if (current && hasRecordContent(current)) {
    return { ...current, state: 'ready', generated_at: generatedAt, covers_until: analysis.header.last_activity_at };
  }
  return {
    state: 'ready',
    generated_at: generatedAt,
    covers_until: analysis.header.last_activity_at,
    headline: `${title} · 합성 기록 생성 시연`,
    bullets: [],
    review: [],
    tags: { kinds: [], components: [], screens: [], related: [] },
    work: analysis.turns.filter((turn) => turn.is_request).map((turn) => ({
      id: `demo-${turn.key}`,
      kind: 'request',
      done: null,
      text: turn.preview.text ?? '본문을 확인하지 못한 요청',
      turn_key: turn.key,
      commits: [],
      tasks: [],
    })),
  };
};
