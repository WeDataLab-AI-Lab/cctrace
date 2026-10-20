import { describe, expect, it } from 'vitest';
import { calendarOfDetail } from './calendar';
import { DETAIL_SESSIONS, FLAGSHIP_ID } from './fixtures';

describe('상세·캘린더 통합 픽스처', () => {
  it('대표 세션의 요청·입력·태그·비용은 상세와 같은 값이다', () => {
    const fixture = DETAIL_SESSIONS.find((session) => session.id === FLAGSHIP_ID);
    expect(fixture).toBeDefined();
    if (!fixture) throw new Error('flagship fixture missing');
    const calendar = calendarOfDetail(fixture);
    expect(calendar?.session_id).toBe(FLAGSHIP_ID);
    expect(calendar?.requests).toBe(14);
    expect(calendar?.inputs.items).toHaveLength(15);
    expect(calendar?.segments).toHaveLength(14);
    expect(calendar?.colors.screens).toContain('알림 센터');
    expect(calendar?.cost.usd).toBeCloseTo(7.57, 2);
    expect(calendar?.colors.status).toBe('running');
  });

  it('완전하지 않은 분석과 접근 불가를 빈 활동 목록으로 꾸미지 않는다', () => {
    const incomplete = DETAIL_SESSIONS.filter((fixture) => !fixture.analysis || !('turns' in fixture.analysis));
    expect(incomplete.length).toBeGreaterThan(0);
    for (const fixture of incomplete) expect(calendarOfDetail(fixture)).toBeNull();
  });

  it('입력은 상세의 턴을 가리키며 버려진 분기 활동은 표시하지 않는다', () => {
    for (const fixture of DETAIL_SESSIONS) {
      const calendar = calendarOfDetail(fixture);
      const analysis = fixture.analysis;
      if (!calendar || !analysis || !('turns' in analysis)) continue;
      const keys = new Set(analysis.turns.map((turn) => turn.key));
      for (const input of calendar.inputs.items) expect(keys.has(input.turn_key)).toBe(true);
      expect(calendar.inputs.truncated).toBe(analysis.truncated.inputs);
      expect(calendar.title).toEqual(analysis.header.title);
    }
  });
});
