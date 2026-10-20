import { describe, expect, it } from 'vitest';
import { isFullAnalysis } from '@/components/sessions/analysis/guards';
import { generateDemoRecord } from './record-generation';
import { DETAIL_NOW, DETAIL_SESSIONS, FLAGSHIP_ID } from './fixtures';

describe('합성 기록 생성의 근거 경계', () => {
  it('기존 내용은 보존하면서 반영 시각과 상태만 갱신한다', () => {
    const fixture = DETAIL_SESSIONS.find((item) => item.id === FLAGSHIP_ID);
    if (!fixture?.analysis || !isFullAnalysis(fixture.analysis)) throw new Error('flagship missing');
    const record = generateDemoRecord(fixture.analysis, '시연', DETAIL_NOW);
    expect(record.state).toBe('ready');
    expect(record.generated_at).toBe(new Date(DETAIL_NOW).toISOString());
    expect(record.covers_until).toBe(fixture.analysis.header.last_activity_at);
    if (record.state !== 'ready') throw new Error('ready record expected');
    expect(record.work).toEqual(fixture.analysis.record && 'work' in fixture.analysis.record ? fixture.analysis.record.work : []);
  });

  it('본문이 없는 기록의 요청은 완료 여부를 지어내지 않는다', () => {
    const fixture = DETAIL_SESSIONS.find((item) => item.analysis && isFullAnalysis(item.analysis) && item.analysis.record?.state === 'none');
    if (!fixture?.analysis || !isFullAnalysis(fixture.analysis)) throw new Error('none fixture missing');
    const record = generateDemoRecord(fixture.analysis, '시연', DETAIL_NOW);
    if (record.state !== 'ready') throw new Error('ready record expected');
    expect(record.headline).toContain('합성 기록 생성 시연');
    expect(record.work.every((item) => item.done === null)).toBe(true);
    expect(record.bullets).toEqual([]);
    expect(record.review).toEqual([]);
    expect(record.work).toHaveLength(fixture.analysis.turns.filter((turn) => turn.is_request).length);
  });

  it('가려지거나 보류된 기록을 새 내용으로 덮지 않는다', () => {
    for (const state of ['redacted', 'withheld'] as const) {
      const fixture = DETAIL_SESSIONS.find((item) => item.analysis && isFullAnalysis(item.analysis) && item.analysis.record?.state === state);
      if (!fixture?.analysis || !isFullAnalysis(fixture.analysis)) throw new Error(`${state} fixture missing`);
      expect(generateDemoRecord(fixture.analysis, '시연', DETAIL_NOW)).toBe(fixture.analysis.record);
    }
  });
});
