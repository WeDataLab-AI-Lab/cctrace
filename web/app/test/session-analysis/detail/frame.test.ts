import { describe, expect, it } from 'vitest';
import type { SeriesPoint, UsageWindowSeries } from '@/lib/session-analysis-types';
import { DETAIL_NOW } from './builders';
import { FRAME_ACCOUNT_LABELS, frameFor, medianAndBand, monotoneCurve, mulberry32, percentile, valueAt } from './frame';

const nonDecreasing = (points: readonly SeriesPoint[]): boolean => points.every((p, i) => i === 0 || p.y >= points[i - 1].y);

const allSeries = (selected: string): UsageWindowSeries[] => {
  const frame = frameFor(selected);
  return [frame.series.five_hour, frame.series.seven_day];
};

describe('백분위수와 곡선 도구', () => {
  it('percentile: 선형 보간, 값이 없으면 0이 아니라 null', () => {
    expect(percentile([], 0.5)).toBeNull();
    expect(percentile([7], 0.25)).toBe(7);
    expect(percentile([1, 2, 3, 4], 0.5)).toBe(2.5);
    expect(percentile([4, 1, 3, 2], 0)).toBe(1);
    expect(percentile([4, 1, 3, 2], 1)).toBe(4);
    expect(percentile([10, 20, 30, 40, 50], 0.25)).toBe(20);
    expect(percentile([10, 20, 30, 40, 50], 0.75)).toBe(40);
  });

  it('percentile: 입력을 바꾸지 않고, 범위 밖의 p는 끝 값으로 접는다', () => {
    const values = [3, 1, 2];
    expect(percentile(values, 2)).toBe(3);
    expect(percentile(values, -1)).toBe(1);
    expect(values).toEqual([3, 1, 2]);
  });

  it('valueAt: 선형으로 읽고 범위 밖은 끝 값, 점이 없으면 null', () => {
    const line: SeriesPoint[] = [
      { x: 0, y: 0 },
      { x: 0.5, y: 10 },
      { x: 1, y: 30 },
    ];
    expect(valueAt(line, 0.25)).toBe(5);
    expect(valueAt(line, 0.75)).toBe(20);
    expect(valueAt(line, -1)).toBe(0);
    expect(valueAt(line, 2)).toBe(30);
    expect(valueAt([], 0.5)).toBeNull();
  });

  it('monotoneCurve: 0에서 시작해 끝 값에 닿고 줄어들지 않으며 0~100 안이다', () => {
    for (const end of [0, 17, 99, 100]) {
      const curve = monotoneCurve(mulberry32(end + 1), end, 0.5, 12);
      expect(curve).toHaveLength(13);
      expect(curve[0]).toEqual({ x: 0, y: 0 });
      expect(curve[curve.length - 1].x).toBe(0.5);
      expect(curve[curve.length - 1].y).toBe(end);
      expect(nonDecreasing(curve)).toBe(true);
      for (const p of curve) expect(p.y >= 0 && p.y <= 100).toBe(true);
    }
  });

  it('mulberry32: 같은 시드는 같은 수열이고 0 이상 1 미만이다', () => {
    const a = mulberry32(42);
    const b = mulberry32(42);
    for (let i = 0; i < 20; i += 1) {
      const v = a();
      expect(v).toBe(b());
      expect(v >= 0 && v < 1).toBe(true);
    }
  });

  it('medianAndBand: 같은 x 격자에서 low <= 중앙값 <= high', () => {
    const past = [10, 30, 50, 70, 90].map((end, i) => monotoneCurve(mulberry32(i + 5), end, 1, 24));
    const { median, band } = medianAndBand(past);
    expect(median).toHaveLength(24);
    expect(band).toHaveLength(24);
    band.forEach((b, i) => {
      expect(b.x).toBe(median[i].x);
      expect(b.low).toBeLessThanOrEqual(median[i].y);
      expect(median[i].y).toBeLessThanOrEqual(b.high);
    });
    // 지난 창이 없으면 선도 범위도 만들지 않는다(0으로 채우지 않는다).
    expect(medianAndBand([])).toEqual({ median: [], band: [] });
  });
});

describe('frameFor: 계정', () => {
  it('계정 셋과 화면 이름', () => {
    expect(FRAME_ACCOUNT_LABELS).toEqual(['메인', '서브', 'Codex']);
    const frame = frameFor('서브');
    expect(frame.accounts.map((a) => [a.label, a.provider])).toEqual([
      ['메인', 'claude'],
      ['서브', 'claude'],
      ['Codex', 'codex'],
    ]);
  });

  it('사용률은 관측한 값이다: 0은 관측한 0이고 null이 없다', () => {
    const [main, sub, codex] = frameFor('서브').accounts;
    expect([main.five_hour_pct, main.seven_day_pct]).toEqual([0, 99]);
    expect([sub.five_hour_pct, sub.seven_day_pct]).toEqual([1, 17]);
    expect([codex.five_hour_pct, codex.seven_day_pct]).toEqual([0, 88]);
  });

  it('고른 Claude 계정만 selected이고, 알 수 없는 이름과 Codex는 서브로 돌아온다', () => {
    const selectedOf = (label: string): string[] => frameFor(label).accounts.filter((a) => a.selected).map((a) => a.label);
    expect(selectedOf('메인')).toEqual(['메인']);
    expect(selectedOf('서브')).toEqual(['서브']);
    expect(selectedOf('Codex')).toEqual(['서브']);
    expect(selectedOf('없는 계정')).toEqual(['서브']);
  });

  it('재설정 시각은 미래이고 창 길이 안이다', () => {
    for (const a of frameFor('메인').accounts) {
      const five = Date.parse(a.resets_at.five_hour ?? '') - DETAIL_NOW;
      const seven = Date.parse(a.resets_at.seven_day ?? '') - DETAIL_NOW;
      expect(five).toBeGreaterThan(0);
      expect(five).toBeLessThanOrEqual(5 * 3_600_000);
      expect(seven).toBeGreaterThan(0);
      expect(seven).toBeLessThanOrEqual(7 * 86_400_000);
    }
  });

  it('저장소 상태', () => {
    expect(frameFor('서브').repo).toEqual({ branch: 'feat/inbox-filters', uncommitted: 5 });
  });
});

describe('frameFor: 창 겹침 차트', () => {
  it('두 창이 있고 선택한 계정의 값으로 끝난다', () => {
    for (const label of ['메인', '서브']) {
      const frame = frameFor(label);
      const account = frame.accounts.find((a) => a.label === label);
      expect(frame.series.five_hour.window).toBe('five_hour');
      expect(frame.series.seven_day.window).toBe('seven_day');
      const fiveEnd = frame.series.five_hour.current[frame.series.five_hour.current.length - 1];
      const sevenEnd = frame.series.seven_day.current[frame.series.seven_day.current.length - 1];
      expect(fiveEnd.y).toBe(account?.five_hour_pct);
      expect(sevenEnd.y).toBe(account?.seven_day_pct);
    }
  });

  it('지금 창의 곡선은 줄어들지 않고 지난 창들도 그렇다', () => {
    for (const label of FRAME_ACCOUNT_LABELS) {
      for (const series of allSeries(label)) {
        expect(nonDecreasing(series.current)).toBe(true);
        for (const past of series.past) expect(nonDecreasing(past)).toBe(true);
      }
    }
  });

  it('x는 0~1 안이다. 지금 창은 절반 안팎까지만 지났다', () => {
    for (const series of allSeries('서브')) {
      const xs = [...series.current, ...series.past.flat(), ...series.median].map((p) => p.x);
      for (const x of xs) expect(x >= 0 && x <= 1).toBe(true);
      const nowX = series.current[series.current.length - 1].x;
      expect(nowX).toBeGreaterThan(0.3);
      expect(nowX).toBeLessThan(0.7);
      for (const b of series.band) expect(b.x >= 0 && b.x <= 1).toBe(true);
    }
  });

  it('5시간 창은 과거 36개, 7일 창은 17개이고 모두 끝까지 관측된다', () => {
    for (const label of FRAME_ACCOUNT_LABELS) {
      const [five, seven] = allSeries(label);
      expect(five.past).toHaveLength(36);
      expect(seven.past).toHaveLength(17);
      for (const curve of [...five.past, ...seven.past]) {
        expect(curve).toHaveLength(25);
        expect(curve[0].x).toBe(0);
        expect(curve[curve.length - 1].x).toBe(1);
      }
    }
  });

  it('각 창의 중앙값과 25~75% 범위는 모든 지난 곡선에서 직접 계산된다', () => {
    for (const label of FRAME_ACCOUNT_LABELS) {
      for (const series of allSeries(label)) {
        expect({ median: series.median, band: series.band }).toEqual(medianAndBand(series.past));
        for (let index = 0; index < series.median.length; index += 1) {
          const x = series.median[index].x;
          const values = series.past.map((curve) => valueAt(curve, x)).filter((value): value is number => value !== null);
          const expected = [0.25, 0.5, 0.75].map((p) => percentile(values, p));
          expect(expected.every((value) => value !== null)).toBe(true);
          const rounded = expected.map((value) => Math.round((value ?? 0) * 10) / 10);
          expect([series.band[index].low, series.median[index].y, series.band[index].high]).toEqual(rounded);
        }
      }
    }
  });

  it('범위의 순서: low <= 중앙값 <= high, 값은 0~100', () => {
    for (const series of allSeries('메인')) {
      expect(series.band).toHaveLength(series.median.length);
      series.band.forEach((b, i) => {
        expect(b.low).toBeLessThanOrEqual(series.median[i].y);
        expect(series.median[i].y).toBeLessThanOrEqual(b.high);
        expect(b.low).toBeGreaterThanOrEqual(0);
        expect(b.high).toBeLessThanOrEqual(100);
      });
    }
  });

  it('메인의 7일 창은 높다: 지금 99%이고 중앙값도 서브보다 높다', () => {
    const main = frameFor('메인').series.seven_day;
    const sub = frameFor('서브').series.seven_day;
    expect(main.current[main.current.length - 1].y).toBe(99);
    const lastMedian = (s: UsageWindowSeries): number => s.median[s.median.length - 1].y;
    expect(lastMedian(main)).toBeGreaterThan(lastMedian(sub));
  });

  it('같은 입력은 같은 출력이다(난수가 아니다)', () => {
    expect(frameFor('메인')).toEqual(frameFor('메인'));
    expect(JSON.stringify(frameFor('서브'))).not.toMatch(/NaN|undefined|Infinity/);
  });
});
