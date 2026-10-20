import { describe, expect, it } from 'vitest';
import { MIN_LANE_MS, assignLanes, type LaneItem } from './lanes';

const MIN = 60_000;
const item = (key: string, startMin: number, endMin: number): LaneItem => ({ key, start: startMin * MIN, end: endMin * MIN });
const lanesOf = (items: LaneItem[]) => {
  const placed = assignLanes(items);
  return Object.fromEntries([...placed.entries()].map(([k, v]) => [k, `${v.lane}/${v.lanes}`]));
};

describe('겹침 배치(C10)', () => {
  it('혼자인 띠는 0번 레인이고 열을 다 쓴다', () => {
    expect(lanesOf([item('a', 0, 60)])).toEqual({ a: '0/1' });
  });

  it('겹치는 두 띠는 레인 둘로 나뉜다', () => {
    expect(lanesOf([item('a', 0, 60), item('b', 30, 90)])).toEqual({ a: '0/2', b: '1/2' });
  });

  it('겹치지 않는 띠는 각자 열 너비를 다 쓴다(묶음이 다르다)', () => {
    const placed = assignLanes([item('a', 0, 60), item('b', 120, 180)]);
    expect(placed.get('a')).toMatchObject({ lane: 0, lanes: 1, group: 0 });
    expect(placed.get('b')).toMatchObject({ lane: 0, lanes: 1, group: 1 });
  });

  it('맞닿는 띠(앞이 끝나는 순간 뒤가 시작)는 겹침이 아니다 — 같은 레인, 다른 묶음', () => {
    const placed = assignLanes([item('a', 0, 60), item('b', 60, 120)]);
    expect(placed.get('a')).toMatchObject({ lane: 0, lanes: 1, group: 0 });
    expect(placed.get('b')).toMatchObject({ lane: 0, lanes: 1, group: 1 });
  });

  it('시작 순으로 처음 비는 레인에 넣는다: 0번이 먼저 풀리면 0번으로 돌아온다', () => {
    // a(0-30) b(10-90) c(40-70): c가 올 때 0번(a)은 비었고 1번(b)은 차 있다.
    expect(lanesOf([item('a', 0, 30), item('b', 10, 90), item('c', 40, 70)])).toEqual({ a: '0/2', b: '1/2', c: '0/2' });
  });

  it('레인 수는 이어 겹치는 묶음 전체의 최댓값이다: 서로 겹치지 않는 a와 c도 b를 통해 한 묶음이다', () => {
    const placed = assignLanes([item('a', 0, 60), item('b', 30, 90), item('c', 80, 120)]);
    expect(placed.get('a')).toMatchObject({ lane: 0, lanes: 2, group: 0 });
    expect(placed.get('b')).toMatchObject({ lane: 1, lanes: 2, group: 0 });
    expect(placed.get('c')).toMatchObject({ lane: 0, lanes: 2, group: 0 });
  });

  it('레인 수는 묶음마다 따로다: 한 묶음이 3레인이어도 다음 묶음은 1레인이다', () => {
    const placed = assignLanes([item('a', 0, 100), item('b', 10, 100), item('c', 20, 100), item('d', 200, 220)]);
    expect([placed.get('a')?.lanes, placed.get('b')?.lanes, placed.get('c')?.lanes]).toEqual([3, 3, 3]);
    expect(placed.get('d')).toMatchObject({ lane: 0, lanes: 1 });
  });

  it('다섯 띠가 한꺼번에 겹치면 레인 다섯', () => {
    const placed = assignLanes([0, 1, 2, 3, 4].map((i) => item(`s${i}`, i * 5, 120)));
    expect([...placed.values()].map((p) => p.lane)).toEqual([0, 1, 2, 3, 4]);
    expect([...placed.values()].every((p) => p.lanes === 5)).toBe(true);
  });

  it('짧은 띠는 최소 20분으로 센다: 5분짜리 띠가 끝난 뒤 10분에 시작한 띠와도 겹친다', () => {
    expect(MIN_LANE_MS).toBe(20 * MIN);
    // 20분으로 세지 않으면 a(0-5)와 b(10-60)은 겹치지 않아 같은 레인이다.
    expect(lanesOf([item('a', 0, 5), item('b', 10, 60)])).toEqual({ a: '0/2', b: '1/2' });
    // 정확히 20분 뒤에 시작하면 비어 있다.
    expect(lanesOf([item('a', 0, 5), item('b', 20, 60)])).toEqual({ a: '0/1', b: '0/1' });
  });

  it('같은 세션의 순차 조각은 실제 종료 뒤 가상 20분 안에서도 같은 레인을 재사용한다', () => {
    const placed = assignLanes([
      { ...item('a', 0, 2), sessionId: 'one' },
      { ...item('b', 3, 5), sessionId: 'one' },
      { ...item('other', 4, 30), sessionId: 'two' },
    ]);
    expect(placed.get('a')).toMatchObject({ lane: 0, lanes: 2 });
    expect(placed.get('b')).toMatchObject({ lane: 0, lanes: 2 });
    expect(placed.get('other')).toMatchObject({ lane: 1, lanes: 2 });
  });

  it('짧은 순차 턴 수십 개를 한 세션의 동시 레인 수십 개로 오인하지 않는다', () => {
    const turns = Array.from({ length: 40 }, (_, index) => ({ ...item(`turn-${index}`, index, index + 1), sessionId: 'one' }));
    const placed = assignLanes([...turns, { ...item('other', 10, 60), sessionId: 'two' }]);
    expect(turns.every((turn) => placed.get(turn.key)?.lane === 0)).toBe(true);
    expect(placed.get('other')?.lane).toBe(1);
    expect([...placed.values()].every((placement) => placement.lanes === 2)).toBe(true);
  });

  it('같은 세션의 실제로 겹친 조각은 별도 레인에 배치한다', () => {
    const placed = assignLanes([
      { ...item('a', 0, 10), sessionId: 'one' },
      { ...item('b', 5, 12), sessionId: 'one' },
    ]);
    expect(placed.get('a')?.lane).not.toBe(placed.get('b')?.lane);
    expect(placed.get('a')?.lanes).toBe(2);
  });

  it('다른 세션의 가상 겹침은 여전히 20분 동안 레인을 분리한다', () => {
    const placed = assignLanes([
      { ...item('a', 0, 2), sessionId: 'one' },
      { ...item('b', 3, 5), sessionId: 'two' },
    ]);
    expect(placed.get('b')?.lane).toBe(1);
  });

  it('길이 0인 띠도 20분을 차지한다', () => {
    expect(lanesOf([item('a', 0, 0), item('b', 19, 40)])).toEqual({ a: '0/2', b: '1/2' });
  });

  it('입력 순서와 상관없이 같은 결과다', () => {
    const items = [item('c', 40, 70), item('a', 0, 30), item('b', 10, 90), item('d', 120, 150)];
    const forward = lanesOf(items);
    const reversed = lanesOf([...items].reverse());
    expect(reversed).toEqual(forward);
  });

  it('시작이 같으면 키 순으로 결정한다', () => {
    expect(lanesOf([item('b', 0, 60), item('a', 0, 60)])).toEqual({ a: '0/2', b: '1/2' });
  });

  it('띠가 없으면 빈 결과', () => {
    expect(assignLanes([]).size).toBe(0);
  });
});
