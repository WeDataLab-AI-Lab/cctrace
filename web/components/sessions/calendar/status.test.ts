import { describe, expect, it } from 'vitest';
import { STATUS_CHIPS, STATUS_META, chipOfStatus, statusChipCounts } from './status';

describe('완료 여부와 상태 칩', () => {
  it('색 기준에 판정 불가를 독립적으로 표시한다', () => {
    expect(Object.values(STATUS_META).map((m) => m.label)).toEqual(['완료', '하다 만 것', '도중 종료', '실행 중', '판정 불가']);
  });

  it('네 칩의 순서는 완료, 하다 만 것, 도중 종료, 판정 불가', () => {
    expect(STATUS_CHIPS.map((c) => c.label)).toEqual(['완료', '하다 만 것', '도중 종료', '판정 불가']);
  });

  it('실행 중만 하다 만 것에 세고 판정 없음은 독립된 칩에 센다', () => {
    expect(chipOfStatus('done')).toBe('done');
    expect(chipOfStatus('midway')).toBe('midway');
    expect(chipOfStatus('leftover')).toBe('leftover');
    expect(chipOfStatus('running')).toBe('leftover');
    expect(chipOfStatus('unknown')).toBe('unknown');
    expect(chipOfStatus(null)).toBe('unknown');
    expect(chipOfStatus(undefined)).toBe('unknown');
  });

  it('네 칩의 합은 세션 수와 같고 실행 중과 판정 누락은 따로 알린다', () => {
    const counts = statusChipCounts(['done', 'done', 'leftover', 'running', 'midway', null, undefined, 'leftover', 'unknown']);
    expect(counts.counts).toEqual({ done: 2, leftover: 3, midway: 1, unknown: 3 });
    expect(Object.values(counts.counts).reduce((sum, count) => sum + count, 0)).toBe(9);
    expect(counts.total).toBe(9);
    expect(counts.running).toBe(1);
    expect(counts.unjudged).toBe(2);
  });

  it('세션이 없으면 모두 0', () => {
    expect(statusChipCounts([])).toEqual({ counts: { done: 0, leftover: 0, midway: 0, unknown: 0 }, running: 0, unjudged: 0, total: 0 });
  });

  it('판정 불가와 실행 중의 설명은 구분된다', () => {
    expect(STATUS_CHIPS.find((c) => c.key === 'leftover')?.hint).toContain('실행 중');
    expect(STATUS_CHIPS.find((c) => c.key === 'unknown')?.hint).toContain('판정 자료가 없거나');
  });
});
