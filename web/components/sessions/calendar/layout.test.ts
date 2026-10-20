import { describe, expect, it } from 'vitest';
import { calInput, calSeg, calSession } from '@/app/test/session-analysis/calendar/calendar-builders';
import type { CalendarSession } from '@/lib/session-calendar-types';
import { weekColumns } from './day-grid';
import { BUBBLE_GAP_PX, BUBBLE_MIN_PX_PER_HOUR, MIN_BAND_PX, STRIPE_GAP_PX, layoutWeek, showBubbles, thin } from './layout';
import { prepareSessions } from './prepare';
import { HOUR_MS, type LocalDate } from './time-zone';

const utc = (iso: string): number => Date.parse(iso);
const MIN = 60_000;

const layout = (tz: string, week: LocalDate, sessions: CalendarSession[], px = 30, now = 0) => {
  const columns = weekColumns(tz, week, now);
  return layoutWeek({ timeZone: tz, columns, sessions: prepareSessions(sessions), pxPerHour: px, nowMs: now });
};

const SEOUL_WEEK = { year: 2026, month: 10, day: 5 };

describe('띠의 위치: y는 지역 자정부터 흐른 절대 시간에 비례한다', () => {
  it('서울 월요일 09:00~10:30 → y = 9h, 높이 = 1.5h', () => {
    // 09:00 KST = 00:00Z.
    const s = calSession('s', { segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:30:00Z')] });
    const [mon] = layout('Asia/Seoul', SEOUL_WEEK, [s], 30).days;
    expect(mon.bands).toHaveLength(1);
    expect(mon.bands[0]).toMatchObject({ y: 9 * 30, h: 1.5 * 30, lane: 0, lanes: 1, leftPct: 0, widthPct: 100 });
    expect(mon.height).toBe(24 * 30);
  });

  it('배율이 두 배면 y와 높이가 두 배이고 레인은 그대로다(레인은 시간으로만 정한다)', () => {
    const sessions = [
      calSession('a', { segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T00:03:00Z')] }),
      calSession('b', { segments: [calSeg('2026-10-05T00:10:00Z', '2026-10-05T01:00:00Z')] }),
    ];
    const lanesAt = (px: number) => layout('Asia/Seoul', SEOUL_WEEK, sessions, px).days[0].bands.map((b) => [b.sessionId, b.lane, b.lanes]);
    expect(lanesAt(12)).toEqual(lanesAt(240));
    // a는 3분이지만 20분으로 세므로 b(10분에 시작)와 겹친다.
    expect(lanesAt(30)).toEqual([
      ['a', 0, 2],
      ['b', 1, 2],
    ]);
  });

  it('아주 짧은 띠도 눌러 볼 수 있는 높이를 가진다', () => {
    const s = calSession('s', { segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T00:00:00Z')] });
    const band = layout('Asia/Seoul', SEOUL_WEEK, [s], 12).days[0].bands[0];
    expect(band.h).toBe(MIN_BAND_PX);
  });

  it('자정을 넘는 조각은 두 열에 띠를 만든다', () => {
    // 월요일 23:00 ~ 화요일 01:00 KST.
    const s = calSession('s', { segments: [calSeg('2026-10-05T14:00:00Z', '2026-10-05T16:00:00Z')] });
    const [mon, tue] = layout('Asia/Seoul', SEOUL_WEEK, [s], 30).days;
    expect(mon.bands[0]).toMatchObject({ y: 23 * 30, h: 30, continuesAfter: true, continuesBefore: false });
    expect(tue.bands[0]).toMatchObject({ y: 0, h: 30, continuesBefore: true, continuesAfter: false });
  });

  it('조각이 없는 세션은 흐린 띠로 범위만 그리고 입력선·밀도에 들지 않는다', () => {
    const s = calSession('g', { facts: 'truncated', range: ['2026-10-05T00:00:00Z', '2026-10-05T06:00:00Z'], inputsState: 'undeterminable' });
    const [mon] = layout('Asia/Seoul', SEOUL_WEEK, [s], 30).days;
    expect(mon.bands[0]).toMatchObject({ faint: true, y: 9 * 30, h: 6 * 30, stripes: [], bubbles: [], inputCount: 0 });
    expect(mon.density).toEqual([]);
  });
});

describe('입력 가로선(C9)과 말풍선(C16)', () => {
  const MON_9 = utc('2026-10-05T00:00:00Z');
  const seg = calSeg(MON_9, MON_9 + 60 * MIN);

  it('입력은 반열린 띠 안의 y에 가로선을 긋는다(끝 경계는 제외)', () => {
    const s = calSession('s', { segments: [seg], inputs: [MON_9, MON_9 + 30 * MIN, MON_9 + 60 * MIN] });
    const band = layout('Asia/Seoul', SEOUL_WEEK, [s], 60).days[0].bands[0];
    expect(band.inputCount).toBe(2);
    expect(band.stripes).toEqual([0, 30]);
  });

  it('가까운 입력은 한 줄로 합친다: 선 사이는 늘 STRIPE_GAP_PX 이상이라 아주 바쁜 띠도 줄무늬로 남는다', () => {
    expect(STRIPE_GAP_PX).toBe(3);
    // 30px/h에서 1분 = 0.5px. 0, 1, 2분 → 줄 0, 1(0.5 반올림), 1 → 첫 줄 하나만 남는다.
    const s = calSession('s', { segments: [seg], inputs: [MON_9, MON_9 + MIN, MON_9 + 2 * MIN, MON_9 + 40 * MIN] });
    const band = layout('Asia/Seoul', SEOUL_WEEK, [s], 30).days[0].bands[0];
    expect(band.inputCount).toBe(4);
    expect(band.stripes).toEqual([0, 20]);
    // 입력이 아주 많은 띠: 1분마다 200개 → 선은 줄 수보다 훨씬 적고 간격이 고르다.
    const busy = calSession('b', { segments: [calSeg(MON_9, MON_9 + 200 * MIN)], inputs: Array.from({ length: 200 }, (_, i) => MON_9 + i * MIN) });
    const lines = layout('Asia/Seoul', SEOUL_WEEK, [busy], 30).days[0].bands[0].stripes;
    expect(lines.length).toBeLessThan(200);
    for (let i = 1; i < lines.length; i++) expect(lines[i] - lines[i - 1]).toBeGreaterThanOrEqual(STRIPE_GAP_PX);
  });

  it('띠 밖의 입력은 띠에 그리지 않는다(밀도에는 센다)', () => {
    const outside = MON_9 + 3 * HOUR_MS;
    const s = calSession('s', { segments: [seg], inputs: [MON_9 + 10 * MIN, outside] });
    const [mon] = layout('Asia/Seoul', SEOUL_WEEK, [s], 30).days;
    expect(mon.bands[0].inputCount).toBe(1);
    expect(mon.density.reduce((n, d) => n + d.count, 0)).toBe(2);
  });

  it('표준 배율에서는 말풍선이 없고, 임계 배율부터 가로선 대신 말풍선이다', () => {
    expect(BUBBLE_MIN_PX_PER_HOUR).toBe(96);
    expect(showBubbles(95.9)).toBe(false);
    expect(showBubbles(96)).toBe(true);
    const s = calSession('s', { segments: [seg], inputs: [MON_9, MON_9 + 30 * MIN] });
    expect(layout('Asia/Seoul', SEOUL_WEEK, [s], 60).days[0].bands[0].bubbles).toEqual([]);
    const zoomed = layout('Asia/Seoul', SEOUL_WEEK, [s], 120).days[0].bands[0];
    expect(zoomed.bubbles).toEqual([0, 60]);
  });

  it('말풍선은 서로 겹치지 않게 솎는다: 너무 가까운 입력은 생략한다', () => {
    expect(thin([0, 3, 6, 11, 12, 30], BUBBLE_GAP_PX)).toEqual([0, 11, 30]);
    const dense = Array.from({ length: 30 }, (_, i) => MON_9 + i * MIN);
    const s = calSession('s', { segments: [seg], inputs: dense });
    const { bubbles } = layout('Asia/Seoul', SEOUL_WEEK, [s], 120).days[0].bands[0];
    for (let i = 1; i < bubbles.length; i++) expect(bubbles[i] - bubbles[i - 1]).toBeGreaterThanOrEqual(BUBBLE_GAP_PX);
    expect(bubbles.length).toBeLessThan(dense.length);
    // 솎아도 가로선 쪽 입력 수는 그대로다.
    expect(layout('Asia/Seoul', SEOUL_WEEK, [s], 120).days[0].bands[0].inputCount).toBe(30);
  });

});

describe('발언량 농도 띠(C11)', () => {
  const MON = utc('2026-10-04T15:00:00Z'); // 월요일 자정(서울)

  it('불투명도는 0.15 + 0.85 × (n ÷ 그 주 최댓값), 최댓값은 주 전체에서 정한다', () => {
    const s = calSession('s', {
      segments: [calSeg(MON + 9 * HOUR_MS, MON + 30 * HOUR_MS)],
      // 월요일 09:00 버킷에 3개, 09:10 버킷에 1개, 화요일 09:00 버킷에 2개.
      inputs: [MON + 9 * HOUR_MS, MON + 9 * HOUR_MS + MIN, MON + 9 * HOUR_MS + 2 * MIN, MON + 9 * HOUR_MS + 10 * MIN, MON + 33 * HOUR_MS, MON + 33 * HOUR_MS + MIN],
    });
    const w = layout('Asia/Seoul', SEOUL_WEEK, [s], 30);
    expect(w.maxDensity).toBe(3);
    const [mon, tue] = w.days;
    expect(mon.density.map((d) => [d.count, Number(d.opacity.toFixed(4))])).toEqual([
      [3, 1],
      [1, Number((0.15 + 0.85 / 3).toFixed(4))],
    ]);
    expect(tue.density.map((d) => [d.count, Number(d.opacity.toFixed(4))])).toEqual([[2, Number((0.15 + (0.85 * 2) / 3).toFixed(4))]]);
    expect(mon.density[0]).toMatchObject({ y: 9 * 30, h: 5 });
  });

  it('맞닿은 두 조각의 경계에 있는 입력은 뒤 조각에만 센다', () => {
    const at = MON + 10 * HOUR_MS;
    const s = calSession('s', {
      segments: [calSeg(MON + 9 * HOUR_MS, at), calSeg(at, MON + 11 * HOUR_MS)],
      inputs: [at],
    });
    const bands = layout('Asia/Seoul', SEOUL_WEEK, [s], 30).days[0].bands;
    expect(bands.map((band) => band.inputCount)).toEqual([0, 1]);
  });

  it('입력이 없으면 농도 띠도 없고 최댓값은 0이다', () => {
    const s = calSession('s', { segments: [calSeg(MON + 9 * HOUR_MS, MON + 10 * HOUR_MS)] });
    const w = layout('Asia/Seoul', SEOUL_WEEK, [s], 30);
    expect(w.maxDensity).toBe(0);
    expect(w.days.every((d) => d.density.length === 0)).toBe(true);
  });
});

describe('연결선(C8): 같은 세션의 띠 사이', () => {
  it('앞 띠의 레인 가운데에 세로선을 긋는다', () => {
    const s = calSession('s', { segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z'), calSeg('2026-10-05T03:00:00Z', '2026-10-05T04:00:00Z')] });
    const [mon] = layout('Asia/Seoul', SEOUL_WEEK, [s], 30).days;
    expect(mon.connectors).toHaveLength(1);
    expect(mon.connectors[0]).toMatchObject({ y: 10 * 30, h: 2 * 30, xPct: 50, elbow: null });
  });

  it('뒤 띠가 다른 레인이면 가로로 꺾어 이어 준다', () => {
    // 가운데 낀 다른 세션 때문에 뒤 띠가 1번 레인으로 밀린다.
    const s = calSession('s', { segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z'), calSeg('2026-10-05T03:00:00Z', '2026-10-05T04:00:00Z')] });
    const other = calSession('o', { segments: [calSeg('2026-10-05T02:30:00Z', '2026-10-05T05:00:00Z')] });
    const [mon] = layout('Asia/Seoul', SEOUL_WEEK, [s, other], 30).days;
    const second = mon.bands.find((b) => b.sessionId === 's' && b.segmentIndex === 1);
    expect(second?.lane).toBe(1);
    expect(mon.connectors[0].elbow).not.toBeNull();
    expect(mon.connectors[0].elbow?.y).toBe(second?.y);
  });
});

describe('현재 시각 선(C12)', () => {
  it('오늘 열에만 있고 y는 자정부터 흐른 시간이다', () => {
    const now = utc('2026-10-07T06:40:00Z'); // 수요일 15:40 서울
    const w = layout('Asia/Seoul', SEOUL_WEEK, [], 30, now);
    expect(w.days.map((d) => d.nowY)).toEqual([null, null, (15 + 40 / 60) * 30, null, null, null, null]);
  });
});

describe('DST 전환일의 배치', () => {
  const FALL = { year: 2026, month: 10, day: 26 };
  const SPRING = { year: 2026, month: 3, day: 2 };

  it('가을 전환 주(뉴욕): 일요일 열은 25시간 높이이고 주의 본문 높이는 그 높이다', () => {
    const w = layout('America/New_York', FALL, [], 30);
    expect(w.days.map((d) => d.hours)).toEqual([24, 24, 24, 24, 24, 24, 25]);
    expect(w.days[6].height).toBe(25 * 30);
    expect(w.bodyHeight).toBe(25 * 30);
  });

  it('봄 전환 주(뉴욕): 일요일 열은 23시간', () => {
    const w = layout('America/New_York', SPRING, [], 30);
    expect(w.days[6].height).toBe(23 * 30);
    expect(w.bodyHeight).toBe(24 * 30);
  });

  it('반복된 1시를 지나는 조각: 00:30 EDT ~ 01:30 EST는 절대 시간으로 2시간 길이다', () => {
    // 2026-11-01T04:30Z = 00:30 EDT, 06:30Z = 01:30 EST.
    const s = calSession('s', { segments: [calSeg('2026-11-01T04:30:00Z', '2026-11-01T06:30:00Z')] });
    const sunday = layout('America/New_York', FALL, [s], 60).days[6];
    expect(sunday.bands[0]).toMatchObject({ y: 0.5 * 60, h: 2 * 60 });
  });

  it('건너뛴 시를 지나는 조각: 01:30 EST ~ 03:30 EDT는 절대 시간으로 1시간 길이다', () => {
    // 2026-03-08T06:30Z = 01:30 EST, 07:30Z = 03:30 EDT.
    const s = calSession('s', { segments: [calSeg('2026-03-08T06:30:00Z', '2026-03-08T07:30:00Z')] });
    const sunday = layout('America/New_York', SPRING, [s], 60).days[6];
    expect(sunday.bands[0]).toMatchObject({ y: 1.5 * 60, h: 60 });
  });

  it('전환일 열의 눈금: 반복된 시와 전환 뒤의 눈금은 열 안에 직접 적고, 축과 맞는 눈금은 적지 않는다', () => {
    const fall = layout('America/New_York', FALL, [], 30).days[6].ticks;
    expect(fall).toHaveLength(25);
    expect(fall.filter((t) => t.repeated).map((t) => t.label)).toEqual(['1 UTC-4', '1 UTC-5']);
    // 0시는 축과 같은 자리라 적지 않고, 전환 뒤의 2시부터는 한 시간 밀려 있어 적는다.
    expect(fall.find((t) => t.hour === 0)?.inColumnLabel).toBe(false);
    expect(fall.find((t) => t.hour === 2)?.inColumnLabel).toBe(true);
    const spring = layout('America/New_York', SPRING, [], 30).days[6].ticks;
    expect(spring).toHaveLength(23);
    expect(spring.find((t) => t.hour === 1)?.inColumnLabel).toBe(false);
    expect(spring.find((t) => t.hour === 3)?.inColumnLabel).toBe(true);
  });

  it('평범한 열의 눈금은 열 안에 적지 않는다(시간 축이 대신한다)', () => {
    const w = layout('Asia/Seoul', SEOUL_WEEK, [], 30);
    expect(w.days[0].ticks.every((t) => !t.inColumnLabel)).toBe(true);
    expect(w.axis).toHaveLength(24);
    expect(w.axis[0]).toMatchObject({ hour: 0, y: 0, label: '0' });
  });

  it('시간 축은 전환일 열이 아니라 평범한 24시간 열을 따른다', () => {
    const w = layout('America/New_York', FALL, [], 30);
    expect(w.axis).toHaveLength(24);
    expect(w.axis.every((t) => !t.repeated)).toBe(true);
  });

  it('전환일 밀도 띠도 절대 시간 기준이다: 반복된 1시의 둘째 번 입력은 다른 위치에 선다', () => {
    const first = utc('2026-11-01T05:30:00Z'); // 01:30 EDT
    const second = utc('2026-11-01T06:30:00Z'); // 01:30 EST
    const s = calSession('s', { segments: [calSeg('2026-11-01T04:30:00Z', '2026-11-01T07:30:00Z')], inputs: [first, second] });
    const sunday = layout('America/New_York', FALL, [s], 60).days[6];
    expect(sunday.density.map((d) => d.y)).toEqual([1.5 * 60, 2.5 * 60]);
  });
});

describe('오늘 열 표시', () => {
  it('기준 시각이 든 열만 오늘이다', () => {
    const now = utc('2026-10-07T06:40:00Z');
    const w = layout('Asia/Seoul', SEOUL_WEEK, [], 30, now);
    expect(w.days.map((d) => d.column.isToday)).toEqual([false, false, true, false, false, false, false]);
  });
});

describe('일한 시간대와 첫 스크롤', () => {
  it('데이터의 일한 시간대가 week.working에 있다', () => {
    const s = calSession('s', { segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T03:00:00Z')] }); // 09~12시
    const w = layout('Asia/Seoul', SEOUL_WEEK, [s], 30);
    expect(w.working).toMatchObject({ firstHour: 9, empty: false });
  });

  it('흐린 띠만 있으면 활동이 없는 주로 본다', () => {
    const s = calSession('g', { facts: 'truncated', range: ['2026-10-05T00:00:00Z', '2026-10-05T03:00:00Z'] });
    expect(layout('Asia/Seoul', SEOUL_WEEK, [s], 30).working.empty).toBe(true);
  });
});

describe('입력 목록이 잘린 세션', () => {
  it('입력이 잘려도(truncated) 가진 입력만큼은 그린다', () => {
    const MON_9 = utc('2026-10-05T00:00:00Z');
    const inputs = Array.from({ length: 5 }, (_, i) => calInput(MON_9 + i * 5 * MIN));
    const s = calSession('s', { segments: [calSeg(MON_9, MON_9 + 60 * MIN)], inputs, inputsTruncated: true });
    expect(layout('Asia/Seoul', SEOUL_WEEK, [s], 30).days[0].bands[0].inputCount).toBe(5);
  });
});
