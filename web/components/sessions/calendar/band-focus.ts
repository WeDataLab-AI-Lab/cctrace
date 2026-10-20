/**
 * 키보드로 띠 사이를 옮기는 규칙. 한 주에 띠가 수백 개라서 모두를 Tab 정지점으로 두면 격자를 지나가는 데만 수백 번 눌러야
 * 한다. 그래서 정지점은 하나만 두고(roving tabindex) 방향키로 옮긴다.
 *
 *   위/아래  같은 날 열 안에서 시간 순으로 앞/뒤 띠
 *   왼/오른  이웃 열(띠가 있는 가장 가까운 열)에서 시작 시각이 가장 가까운 띠
 *   Home/End 그 열의 처음/끝 띠
 *
 * 띠 키는 열 안에서만 유일하므로(자정을 넘는 조각은 두 열에 같은 키가 생긴다) 격자 전체에서는 "열 키|띠 키"로 부른다.
 */

export interface FocusBand {
  key: string;
  sessionId: string;
  faint: boolean;
  start: number;
}

export interface FocusDay {
  columnKey: string;
  /** 열의 시작 시각(ms). 이웃 열의 띠와는 "자정부터 흐른 시간"(그리는 높이)으로 견준다. */
  start: number;
  /** 시작 순. */
  bands: readonly FocusBand[];
}

export type FocusMove = 'up' | 'down' | 'left' | 'right' | 'home' | 'end';

/** 방향키 → 움직임. 다른 키는 null. */
const MOVE_OF_KEY: Readonly<Record<string, FocusMove>> = {
  ArrowUp: 'up',
  ArrowDown: 'down',
  ArrowLeft: 'left',
  ArrowRight: 'right',
  Home: 'home',
  End: 'end',
};

const moveOfKey = (key: string): FocusMove | null => MOVE_OF_KEY[key] ?? null;

/** 격자 전체에서 유일한 띠 키. */
const gridKey = (columnKey: string, bandKey: string): string => `${columnKey}|${bandKey}`;

const locate = (days: readonly FocusDay[], key: string): { day: number; band: number } | null => {
  for (let d = 0; d < days.length; d++) {
    const b = days[d].bands.findIndex((band) => gridKey(days[d].columnKey, band.key) === key);
    if (b >= 0) return { day: d, band: b };
  }
  return null;
};

/**
 * Tab으로 들어올 때 닿는 단 하나의 띠. 마지막에 포커스가 있던 띠가 아직 있으면 그것, 아니면 고른 세션의 첫 활동 띠, 아니면 첫 활동
 * 띠(범위만 그리는 흐린 띠는 맨 나중)다.
 */
const tabStopKey = (days: readonly FocusDay[], current: string | null, selectedId: string | null): string | null => {
  if (current !== null && locate(days, current)) return current;
  const all = days.flatMap((day) => day.bands.map((band) => ({ key: gridKey(day.columnKey, band.key), band })));
  const pick =
    all.find((x) => x.band.sessionId === selectedId && !x.band.faint) ??
    all.find((x) => x.band.sessionId === selectedId) ??
    all.find((x) => !x.band.faint) ??
    all[0];
  return pick ? pick.key : null;
};

/** `key`의 띠에서 `move`만큼 간 띠의 키. 갈 곳이 없으면 null(끝에서 돌아 나오지 않는다). */
const moveFocus = (days: readonly FocusDay[], key: string, move: FocusMove): string | null => {
  const at = locate(days, key);
  if (!at) return null;
  const day = days[at.day];
  const here = day.bands[at.band];

  if (move === 'up' || move === 'down') {
    const next = day.bands[at.band + (move === 'down' ? 1 : -1)];
    return next ? gridKey(day.columnKey, next.key) : null;
  }
  if (move === 'home' || move === 'end') {
    const target = day.bands[move === 'home' ? 0 : day.bands.length - 1];
    return target && target !== here ? gridKey(day.columnKey, target.key) : null;
  }

  const step = move === 'right' ? 1 : -1;
  for (let d = at.day + step; d >= 0 && d < days.length; d += step) {
    const bands = days[d].bands;
    if (bands.length === 0) continue;
    // 열 안의 높이(자정부터 흐른 시간)가 가장 가까운 띠. 같으면 활동 띠가 흐린 띠보다 먼저다.
    const hereOffset = here.start - day.start;
    let best = bands[0];
    let bestGap = Math.abs(best.start - days[d].start - hereOffset);
    for (const band of bands) {
      const gap = Math.abs(band.start - days[d].start - hereOffset);
      if (gap < bestGap || (gap === bestGap && best.faint && !band.faint)) {
        best = band;
        bestGap = gap;
      }
    }
    return gridKey(days[d].columnKey, best.key);
  }
  return null;
};

export { gridKey, moveFocus, moveOfKey, tabStopKey };
