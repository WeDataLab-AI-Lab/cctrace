/**
 * 겹침 배치(C10). 같은 날 열에서 시간이 겹치는 띠를 나란히 놓는다.
 *
 * - 띠를 시작 순으로 놓고, 각 띠를 **처음 비는 레인**에 넣는다.
 * - 레인 수는 서로 이어 겹치는 묶음마다 정한다. 겹치지 않는 띠는 열 너비를 다 쓴다.
 * - 레인을 셀 때는 짧은 띠를 **최소 20분**으로 본다. 확대·축소하면 짧은 띠의 높이가 바뀌어 겹침이 달라 보이는데,
 *   레인은 시간으로만 정하므로 줌을 바꿔도 띠가 다른 레인으로 옮겨 가지 않는다.
 *
 * 이 계산은 시간만 본다. 픽셀을 모른다.
 */

/** 레인을 셀 때 한 띠가 차지하는 시간의 최솟값(ms). */
const MIN_LANE_MS = 20 * 60_000;

export interface LaneItem {
  key: string;
  sessionId?: string;
  start: number;
  end: number;
}

export interface LanePlacement {
  /** 0부터. 왼쪽이 0이다. */
  lane: number;
  /** 이 띠가 속한 묶음의 레인 수. 너비는 열의 1/lanes 다. */
  lanes: number;
  /** 묶음 번호(시간 순). 같은 묶음은 서로 이어 겹친다. */
  group: number;
}

const compare = (a: LaneItem, b: LaneItem): number =>
  a.start - b.start || (a.key < b.key ? -1 : a.key > b.key ? 1 : 0);

const assignLanes = (items: readonly LaneItem[]): Map<string, LanePlacement> => {
  const placements = new Map<string, LanePlacement>();
  const sorted = [...items].sort(compare);
  let group = -1;
  let groupEnd = Number.NEGATIVE_INFINITY;
  let laneEnds: number[] = [];
  let laneActualEnds: number[] = [];
  let laneSessions: (string | undefined)[] = [];
  let members: string[] = [];

  const closeGroup = () => {
    for (const key of members) {
      const placed = placements.get(key);
      if (placed) placed.lanes = laneEnds.length;
    }
  };

  for (const item of sorted) {
    const effectiveEnd = Math.max(item.end, item.start + MIN_LANE_MS);
    // 앞 묶음의 어느 띠와도 겹치지 않으면(맞닿는 것도 겹침이 아니다) 새 묶음이다.
    if (item.start >= groupEnd) {
      closeGroup();
      group += 1;
      laneEnds = [];
      laneActualEnds = [];
      laneSessions = [];
      members = [];
      groupEnd = Number.NEGATIVE_INFINITY;
    }
    // 같은 세션의 순차 턴은 실제로 끝났으면 20분짜리 가상 최소 간격과 관계없이 같은 레인을 재사용한다.
    // 다른 세션과 실제로 겹치는 턴은 아래의 빈 레인 규칙대로 나란히 둔다.
    let lane = item.sessionId === undefined ? -1 : laneEnds.findIndex((end, index) => end > item.start && laneActualEnds[index] <= item.start && laneSessions[index] === item.sessionId);
    if (lane < 0) lane = laneEnds.findIndex((end) => end <= item.start);
    if (lane < 0) {
      lane = laneEnds.length;
      laneEnds.push(effectiveEnd);
      laneActualEnds.push(item.end);
      laneSessions.push(item.sessionId);
    } else {
      laneEnds[lane] = effectiveEnd;
      laneActualEnds[lane] = item.end;
      laneSessions[lane] = item.sessionId;
    }
    groupEnd = Math.max(groupEnd, effectiveEnd);
    placements.set(item.key, { lane, lanes: 0, group });
    members.push(item.key);
  }
  closeGroup();
  return placements;
};

export { MIN_LANE_MS, assignLanes };
