import { describe, expect, it } from 'vitest';
import { DEFAULT_FIRST_HOUR, DEFAULT_LAST_HOUR, initialScrollHour, workingHours, type WorkingHoursDay } from './working-hours';
import { HOUR_MS } from './time-zone';

const DAY0 = Date.UTC(2026, 9, 4, 15, 0, 0); // 서울 2026-10-05 자정
const at = (hours: number): number => DAY0 + hours * HOUR_MS;
const day = (start: number, bands: [number, number][], faint = false): WorkingHoursDay => ({
  start,
  bands: bands.map(([a, b]) => ({ start: at(a) + (start - DAY0), end: at(b) + (start - DAY0), faint })),
});

describe('일한 시간대(처음 스크롤할 곳)', () => {
  it('띠가 모인 시간대의 처음과 끝', () => {
    const w = workingHours([day(DAY0, [[9, 12], [14, 18]])]);
    expect(w.empty).toBe(false);
    expect(w.firstHour).toBe(9);
    expect(w.lastHour).toBe(17);
    expect(initialScrollHour(w)).toBe(8);
  });

  it('여러 날을 모은다', () => {
    const w = workingHours([day(DAY0, [[10, 12]]), day(DAY0 + 24 * HOUR_MS, [[9, 11]]), day(DAY0 + 48 * HOUR_MS, [[13, 19]])]);
    expect(w.firstHour).toBe(9);
    expect(w.lastHour).toBe(18);
  });

  it('새벽의 짧은 세션 하나가 시작 시각을 끌어당기지 않는다', () => {
    // 하루에 40시간치의 일(5일 × 8시간) 중 새벽 2시에 5분짜리 띠 하나.
    const days = Array.from({ length: 5 }, (_, i) => day(DAY0 + i * 24 * HOUR_MS, [[9, 17]]));
    const withOutlier = [...days, day(DAY0 + 5 * 24 * HOUR_MS, [[2, 2 + 5 / 60]])];
    const w = workingHours(withOutlier);
    expect(w.firstHour).toBe(9);
    expect(initialScrollHour(w)).toBe(8);
  });

  it('전체 시간의 2%를 넘는 단일 새벽 outlier도 피크 시간대보다 희소하면 무시한다', () => {
    const w = workingHours([day(DAY0, [[9, 17]]), day(DAY0 + 24 * HOUR_MS, [[2, 2.5]])]);
    expect(w.firstHour).toBe(9);
    expect(initialScrollHour(w)).toBe(8);
  });

  it('흐린(범위만 그리는) 띠는 활동이 아니라 뺀다', () => {
    const w = workingHours([day(DAY0, [[9, 12]]), day(DAY0, [[0, 24]], true)]);
    expect(w.firstHour).toBe(9);
  });

  it('활동이 하나도 없으면 업무 시간(8~19시)을 기본으로 쓴다', () => {
    const w = workingHours([day(DAY0, [])]);
    expect(w).toEqual({ firstHour: DEFAULT_FIRST_HOUR, lastHour: DEFAULT_LAST_HOUR, empty: true });
    expect(initialScrollHour(w)).toBe(7);
    expect(workingHours([]).empty).toBe(true);
  });

  it('흐린 띠만 있어도 활동이 없는 것이다', () => {
    expect(workingHours([day(DAY0, [[0, 24]], true)]).empty).toBe(true);
  });

  it('0시에 시작하는 일은 0으로 스크롤한다(음수가 되지 않는다)', () => {
    expect(initialScrollHour(workingHours([day(DAY0, [[0, 3]])]))).toBe(0);
  });

  it('길이 0인 띠(기록 한 줄)도 활동이다', () => {
    const w = workingHours([{ start: DAY0, bands: [{ start: at(10), end: at(10), faint: false }] }]);
    expect(w.empty).toBe(false);
    expect(w.firstHour).toBe(10);
  });

  it('25시간 열(가을 전환일)의 마지막 시간까지 센다', () => {
    const w = workingHours([{ start: DAY0, bands: [{ start: at(24.2), end: at(24.8), faint: false }] }]);
    expect(w.firstHour).toBe(24);
  });
});
