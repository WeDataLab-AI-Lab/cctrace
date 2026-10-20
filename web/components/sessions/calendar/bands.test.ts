import { describe, expect, it } from 'vitest';
import { calSeg, calSession } from '@/app/test/session-analysis/calendar/calendar-builders';
import { connectorsFor, dayBands, overlapsColumn, sessionBands } from './bands';
import { weekColumns } from './day-grid';
import { prepareSession, prepareSessions } from './prepare';

const t = (iso: string): number => Date.parse(iso);
/** 서울 2026-10-06 열: 10/05 15:00Z ~ 10/06 15:00Z. */
const day = { start: t('2026-10-05T15:00:00Z'), end: t('2026-10-06T15:00:00Z') };

describe('열에 걸치는가', () => {
  it('안에 든 조각, 앞뒤로 걸친 조각', () => {
    expect(overlapsColumn(day.start + 1000, day.start + 5000, day)).toBe(true);
    expect(overlapsColumn(day.start - 5000, day.start + 5000, day)).toBe(true);
    expect(overlapsColumn(day.end - 5000, day.end + 5000, day)).toBe(true);
    expect(overlapsColumn(day.start - 9000, day.end + 9000, day)).toBe(true);
  });

  it('열이 시작하는 순간에 끝난 조각은 앞 열의 것, 열이 끝나는 순간에 시작한 조각은 다음 열의 것', () => {
    expect(overlapsColumn(day.start - 5000, day.start, day)).toBe(false);
    expect(overlapsColumn(day.end, day.end + 5000, day)).toBe(false);
  });

  it('길이 0인 조각(기록 한 줄)은 시작이 열 안이면 그 열의 것이다 — 열 시작 순간은 그 열, 열 끝 순간은 다음 열', () => {
    expect(overlapsColumn(day.start, day.start, day)).toBe(true);
    expect(overlapsColumn(day.start + 1, day.start + 1, day)).toBe(true);
    expect(overlapsColumn(day.end, day.end, day)).toBe(false);
    expect(overlapsColumn(day.start - 1, day.start - 1, day)).toBe(false);
  });
});

describe('조각 → 띠', () => {
  it('일요일 자정 조각은 그 주 마지막 열에만 있고 다음 월요일 자정 조각은 다음 주로 간다', () => {
    const columns = weekColumns('Asia/Seoul', { year: 2026, month: 10, day: 5 }, 0);
    const sundayStart = columns[6].start;
    const mondayStart = columns[6].end;
    const atSunday = prepareSession(calSession('sun', { segments: [calSeg(sundayStart, sundayStart)] }));
    const atMonday = prepareSession(calSession('mon', { segments: [calSeg(mondayStart, mondayStart)] }));
    expect(sessionBands(atSunday, columns[5])).toHaveLength(0);
    expect(sessionBands(atSunday, columns[6])).toHaveLength(1);
    expect(sessionBands(atMonday, columns[6])).toHaveLength(0);
    expect(sessionBands(atMonday, { start: mondayStart, end: mondayStart + 24 * 3_600_000 })).toHaveLength(1);
  });

  it('열 안의 조각은 그대로 하나의 띠다', () => {
    const s = prepareSession(calSession('s1', { segments: [calSeg('2026-10-06T01:00:00Z', '2026-10-06T02:30:00Z')] }));
    const bands = sessionBands(s, day);
    expect(bands).toHaveLength(1);
    expect(bands[0]).toMatchObject({
      sessionId: 's1',
      segmentIndex: 0,
      faint: false,
      start: t('2026-10-06T01:00:00Z'),
      end: t('2026-10-06T02:30:00Z'),
      continuesBefore: false,
      continuesAfter: false,
    });
  });

  it('자정을 넘는 조각은 두 열에 한 띠씩 — 자른 쪽 끝에 이어짐 표시, 원래 시각은 보존', () => {
    const s = prepareSession(calSession('s1', { segments: [calSeg('2026-10-06T14:00:00Z', '2026-10-06T16:30:00Z')] }));
    const next = { start: day.end, end: day.end + 24 * 3_600_000 };
    const first = sessionBands(s, day)[0];
    const second = sessionBands(s, next)[0];
    expect(first).toMatchObject({ end: day.end, continuesAfter: true, continuesBefore: false, segmentEnd: t('2026-10-06T16:30:00Z') });
    expect(second).toMatchObject({ start: next.start, continuesBefore: true, continuesAfter: false, segmentStart: t('2026-10-06T14:00:00Z') });
  });

  it('여러 날에 걸친 조각은 사이 열을 통째로 채운다', () => {
    const s = prepareSession(calSession('s1', { segments: [calSeg('2026-10-05T10:00:00Z', '2026-10-07T20:00:00Z')] }));
    const bands = sessionBands(s, day)[0];
    expect(bands).toMatchObject({ start: day.start, end: day.end, continuesBefore: true, continuesAfter: true });
  });

  it('열과 겹치지 않는 조각은 띠가 없다', () => {
    const s = prepareSession(calSession('s1', { segments: [calSeg('2026-10-07T01:00:00Z', '2026-10-07T02:00:00Z')] }));
    expect(sessionBands(s, day)).toEqual([]);
  });

  it('조각이 없는 세션(truncated 등)은 범위 전체가 흐린 띠 하나다', () => {
    const s = prepareSession(calSession('g', { facts: 'truncated', range: ['2026-10-05T10:00:00Z', '2026-10-07T03:00:00Z'] }));
    const bands = sessionBands(s, day);
    expect(bands).toEqual([
      expect.objectContaining({ faint: true, segmentIndex: -1, start: day.start, end: day.end, continuesBefore: true, continuesAfter: true, records: null }),
    ]);
  });

  it('읽을 수 없는 시각은 걸러 내고 센다(NaN 좌표를 만들지 않는다)', () => {
    const bad = calSession('s1', { segments: [calSeg('2026-10-06T01:00:00Z', '2026-10-06T02:00:00Z')] });
    bad.segments.push({ start: 'not-a-time', end: 'also-bad', records: 1, human_inputs: 0 });
    const s = prepareSession(bad);
    expect(s.segments).toHaveLength(1);
    expect(s.skipped).toBe(1);
  });
});

describe('열의 띠 모음', () => {
  it('여러 세션의 띠를 시작 순으로 놓고, 같으면 긴 것이 먼저다', () => {
    const sessions = prepareSessions([
      calSession('b', { segments: [calSeg('2026-10-06T03:00:00Z', '2026-10-06T04:00:00Z')] }),
      calSession('a', { segments: [calSeg('2026-10-06T01:00:00Z', '2026-10-06T02:00:00Z')] }),
      calSession('c', { segments: [calSeg('2026-10-06T03:00:00Z', '2026-10-06T05:00:00Z')] }),
    ]);
    expect(dayBands(sessions, day).map((b) => b.sessionId)).toEqual(['a', 'c', 'b']);
  });
});

describe('연결선(C8): 같은 날 같은 세션의 띠 사이 공백', () => {
  const bandsOf = (segments: [string, string][]) => {
    const s = prepareSession(calSession('s1', { segments: segments.map(([a, b]) => calSeg(a, b)) }));
    return sessionBands(s, day);
  };

  it('공백이 있으면 앞 띠의 끝에서 뒤 띠의 시작까지 잇는다', () => {
    const bands = bandsOf([
      ['2026-10-06T01:00:00Z', '2026-10-06T02:00:00Z'],
      ['2026-10-06T05:00:00Z', '2026-10-06T06:00:00Z'],
      ['2026-10-06T08:00:00Z', '2026-10-06T08:30:00Z'],
    ]);
    const connectors = connectorsFor(bands);
    expect(connectors).toHaveLength(2);
    expect(connectors[0]).toMatchObject({ from: t('2026-10-06T02:00:00Z'), to: t('2026-10-06T05:00:00Z') });
    expect(connectors[1]).toMatchObject({ from: t('2026-10-06T06:00:00Z'), to: t('2026-10-06T08:00:00Z') });
  });

  it('맞닿거나 겹치는 띠는 잇지 않는다', () => {
    const bands = bandsOf([
      ['2026-10-06T01:00:00Z', '2026-10-06T02:00:00Z'],
      ['2026-10-06T02:00:00Z', '2026-10-06T03:00:00Z'],
      ['2026-10-06T02:30:00Z', '2026-10-06T04:00:00Z'],
    ]);
    expect(connectorsFor(bands)).toEqual([]);
  });

  it('안에 든 짧은 띠 때문에 선이 끊기지 않는다: 가장 늦게 끝나는 띠에서 이어 간다', () => {
    const bands = bandsOf([
      ['2026-10-06T01:00:00Z', '2026-10-06T05:00:00Z'],
      ['2026-10-06T02:00:00Z', '2026-10-06T03:00:00Z'],
      ['2026-10-06T07:00:00Z', '2026-10-06T08:00:00Z'],
    ]);
    const connectors = connectorsFor(bands);
    expect(connectors).toHaveLength(1);
    expect(connectors[0]).toMatchObject({ from: t('2026-10-06T05:00:00Z'), to: t('2026-10-06T07:00:00Z') });
  });

  it('자정을 넘는 공백은 잇지 않는다(열마다 따로 읽는다)', () => {
    const s = prepareSession(
      calSession('s1', { segments: [calSeg('2026-10-06T10:00:00Z', '2026-10-06T11:00:00Z'), calSeg('2026-10-07T01:00:00Z', '2026-10-07T02:00:00Z')] }),
    );
    expect(connectorsFor(sessionBands(s, day))).toEqual([]);
  });

  it('다른 세션의 띠끼리는 잇지 않고, 흐린 띠는 잇지 않는다', () => {
    const sessions = prepareSessions([
      calSession('a', { segments: [calSeg('2026-10-06T01:00:00Z', '2026-10-06T02:00:00Z')] }),
      calSession('b', { segments: [calSeg('2026-10-06T05:00:00Z', '2026-10-06T06:00:00Z')] }),
      calSession('g', { facts: 'truncated', range: ['2026-10-06T00:00:00Z', '2026-10-06T07:00:00Z'] }),
    ]);
    expect(connectorsFor(dayBands(sessions, day))).toEqual([]);
  });
});
