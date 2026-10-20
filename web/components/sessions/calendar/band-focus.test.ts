import { describe, expect, it } from 'vitest';
import { gridKey, moveFocus, moveOfKey, tabStopKey, type FocusDay } from './band-focus';

const band = (key: string, start: number, over: { sessionId?: string; faint?: boolean } = {}) => ({
  key,
  sessionId: over.sessionId ?? key,
  faint: over.faint ?? false,
  start,
});

// 월: a(9시) b(10시) c(14시) / 화: 비어 있음 / 수: d(8시) e(10시, 흐림) f(15시) / 목: g(9시)
// 열 시작은 0으로 두어 시작 시각이 곧 "자정부터 흐른 시간(시)"이다.
const DAYS: FocusDay[] = [
  { columnKey: 'mon', start: 0, bands: [band('a', 9), band('b', 10), band('c', 14)] },
  { columnKey: 'tue', start: 0, bands: [] },
  { columnKey: 'wed', start: 0, bands: [band('d', 8), band('e', 10, { faint: true }), band('f', 15)] },
  { columnKey: 'thu', start: 0, bands: [band('g', 9)] },
];

const k = (column: string, key: string): string => gridKey(column, key);

describe('방향키 → 움직임', () => {
  it('방향키와 Home/End만 움직임이다', () => {
    expect(['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'Home', 'End'].map(moveOfKey)).toEqual(['up', 'down', 'left', 'right', 'home', 'end']);
    expect(moveOfKey('Tab')).toBeNull();
    expect(moveOfKey('a')).toBeNull();
    expect(moveOfKey('Enter')).toBeNull();
  });
});

describe('Tab 정지점 (roving tabindex)', () => {
  it('마지막에 포커스가 있던 띠가 아직 있으면 그것', () => {
    expect(tabStopKey(DAYS, k('wed', 'f'), 'a')).toBe(k('wed', 'f'));
  });

  it('없어졌으면(다른 주로 넘어감) 고른 세션의 첫 활동 띠', () => {
    expect(tabStopKey(DAYS, k('old', 'x'), 'f')).toBe(k('wed', 'f'));
  });

  it('고른 세션이 흐린 띠뿐이면 그 띠도 정지점이 될 수 있다', () => {
    expect(tabStopKey(DAYS, null, 'e')).toBe(k('wed', 'e'));
  });

  it('고른 것이 없으면 첫 활동 띠', () => {
    expect(tabStopKey(DAYS, null, null)).toBe(k('mon', 'a'));
  });

  it('활동 띠가 없고 흐린 띠만 있으면 그 첫 띠, 띠가 아예 없으면 null', () => {
    expect(tabStopKey([{ columnKey: 'mon', start: 0, bands: [band('z', 1, { faint: true })] }], null, null)).toBe(k('mon', 'z'));
    expect(tabStopKey([{ columnKey: 'mon', start: 0, bands: [] }], null, null)).toBeNull();
    expect(tabStopKey([], null, null)).toBeNull();
  });
});

describe('열 안에서 위/아래', () => {
  it('시간 순으로 앞/뒤 띠', () => {
    expect(moveFocus(DAYS, k('mon', 'a'), 'down')).toBe(k('mon', 'b'));
    expect(moveFocus(DAYS, k('mon', 'b'), 'down')).toBe(k('mon', 'c'));
    expect(moveFocus(DAYS, k('mon', 'c'), 'up')).toBe(k('mon', 'b'));
  });

  it('끝에서는 더 가지 않는다(돌아 나오지 않는다)', () => {
    expect(moveFocus(DAYS, k('mon', 'c'), 'down')).toBeNull();
    expect(moveFocus(DAYS, k('mon', 'a'), 'up')).toBeNull();
  });

  it('Home/End는 그 열의 처음/끝', () => {
    expect(moveFocus(DAYS, k('wed', 'e'), 'home')).toBe(k('wed', 'd'));
    expect(moveFocus(DAYS, k('wed', 'e'), 'end')).toBe(k('wed', 'f'));
    expect(moveFocus(DAYS, k('wed', 'd'), 'home')).toBeNull();
  });
});

describe('이웃 열로 왼쪽/오른쪽', () => {
  it('시작 시각이 가장 가까운 띠로 간다', () => {
    expect(moveFocus(DAYS, k('thu', 'g'), 'left')).toBe(k('wed', 'd')); // 9시 → 수요일에서 8시(d)가 가장 가깝다
    expect(moveFocus(DAYS, k('mon', 'c'), 'right')).toBe(k('wed', 'f')); // 14시 → 15시(f), 비어 있는 화요일은 건너뛴다
  });

  it('띠가 없는 열은 건너뛰고, 더 갈 곳이 없으면 null', () => {
    expect(moveFocus(DAYS, k('mon', 'a'), 'left')).toBeNull();
    expect(moveFocus(DAYS, k('thu', 'g'), 'right')).toBeNull();
    expect(moveFocus(DAYS, k('mon', 'a'), 'right')).toBe(k('wed', 'd'));
  });

  it('시각이 같게 가까우면 활동 띠를 흐린 띠보다 먼저', () => {
    const days: FocusDay[] = [
      { columnKey: 'mon', start: 0, bands: [band('a', 10)] },
      { columnKey: 'tue', start: 0, bands: [band('faint', 10, { faint: true }), band('real', 10)] },
    ];
    expect(moveFocus(days, k('mon', 'a'), 'right')).toBe(k('tue', 'real'));
  });

  it('열마다 시작 시각이 다르다: 절대 시각이 아니라 열 안의 높이로 견준다', () => {
    const DAY = 24;
    const days: FocusDay[] = [
      { columnKey: 'mon', start: 0, bands: [band('a', 9)] },
      // 화요일 08:00과 21:00. 절대 시각만 보면 화요일 08:00(=32)이 월요일 09:00에 더 가깝지만 높이로는 09:00에 가까운 8시여야 한다.
      { columnKey: 'tue', start: DAY, bands: [band('t8', DAY + 8), band('t21', DAY + 21)] },
    ];
    expect(moveFocus(days, k('mon', 'a'), 'right')).toBe(k('tue', 't8'));
    // 반대 방향: 화요일 21시에서 월요일로 → 월요일에는 9시뿐이라 그것.
    expect(moveFocus(days, k('tue', 't21'), 'left')).toBe(k('mon', 'a'));
    const wide: FocusDay[] = [
      { columnKey: 'mon', start: 0, bands: [band('m9', 9), band('m22', 22)] },
      { columnKey: 'tue', start: DAY, bands: [band('t21', DAY + 21)] },
    ];
    expect(moveFocus(wide, k('tue', 't21'), 'left')).toBe(k('mon', 'm22'));
  });
});

describe('키', () => {
  it('열마다 같은 키(자정을 넘는 조각)도 격자에서는 다른 키다', () => {
    expect(gridKey('mon', 'seg1')).not.toBe(gridKey('tue', 'seg1'));
    const days: FocusDay[] = [
      { columnKey: 'mon', start: 0, bands: [band('seg1', 23)] },
      { columnKey: 'tue', start: 0, bands: [band('seg1', 0)] },
    ];
    expect(moveFocus(days, gridKey('mon', 'seg1'), 'right')).toBe(gridKey('tue', 'seg1'));
    expect(moveFocus(days, gridKey('tue', 'seg1'), 'left')).toBe(gridKey('mon', 'seg1'));
  });

  it('모르는 키는 어디로도 가지 않는다', () => {
    expect(moveFocus(DAYS, 'nope|x', 'down')).toBeNull();
  });
});
