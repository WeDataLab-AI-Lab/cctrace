/**
 * 활동 조각 → 날짜 열 안의 띠(C8)와, 띠 사이를 잇는 가는 선.
 *
 * 한 세션의 조각을 날짜 열마다 그 열의 구간으로 **자른** 것이 띠다. 자정을 넘는 조각은 두 열에 한 띠씩 생기고,
 * 잘린 쪽 끝은 "이어짐"(`continuesBefore`/`continuesAfter`)으로 표시한다. 같은 날 열 안에서 한 세션의 띠 사이에
 * 공백이 있으면 그 공백을 가는 선(연결선)으로 잇는다. 자정을 넘는 공백은 잇지 않는다(열마다 따로 읽기 때문이다).
 *
 * 조각이 없는 세션(`truncated` 등)은 세션 전체 범위를 흐린 띠 하나로 그린다. 연결선이 없다.
 */

import type { DayColumn } from './day-grid';
import type { PreparedSession } from './prepare';

export interface DayBand {
  /** 열 안에서 유일한 키. */
  key: string;
  sessionId: string;
  /** 응답 `segments`의 순번. 범위만 그리는 띠는 -1. */
  segmentIndex: number;
  faint: boolean;
  /** 열 구간으로 자른 시각(ms). */
  start: number;
  end: number;
  /** 자르기 전의 조각 시각(ms). 호버에 쓴다. */
  segmentStart: number;
  segmentEnd: number;
  continuesBefore: boolean;
  continuesAfter: boolean;
  records: number | null;
  humanInputs: number | null;
}

export interface BandConnector {
  sessionId: string;
  /** 앞 띠의 끝(ms). */
  from: number;
  /** 뒤 띠의 시작(ms). */
  to: number;
  fromKey: string;
  toKey: string;
}

type Span = Pick<DayColumn, 'start' | 'end'>;

/**
 * [start, end]가 열 [col.start, col.end)에 걸치는가. 길이 0인 조각(기록 한 줄)은 시작이 열 안이면 그 열의 것이다.
 * 열이 시작하는 순간에 정확히 끝나는 조각은 앞 열의 것이다.
 */
const overlapsColumn = (start: number, end: number, col: Span): boolean =>
  start < col.end && (end > col.start || (start === end && start >= col.start));

const bandOf = (
  session: PreparedSession,
  col: Span,
  segmentIndex: number,
  start: number,
  end: number,
  records: number | null,
  humanInputs: number | null,
): DayBand => ({
  key: `${session.id}#${segmentIndex}`,
  sessionId: session.id,
  segmentIndex,
  faint: session.faint,
  start: Math.max(start, col.start),
  end: Math.min(end, col.end),
  segmentStart: start,
  segmentEnd: end,
  continuesBefore: start < col.start,
  continuesAfter: end > col.end,
  records,
  humanInputs,
});

/** 한 세션이 한 날짜 열에 만드는 띠. */
const sessionBands = (session: PreparedSession, col: Span): DayBand[] => {
  if (session.faint) {
    return overlapsColumn(session.rangeStart, session.rangeEnd, col)
      ? [bandOf(session, col, -1, session.rangeStart, session.rangeEnd, null, null)]
      : [];
  }
  return session.segments
    .filter((seg) => overlapsColumn(seg.start, seg.end, col))
    .map((seg) => bandOf(session, col, seg.index, seg.start, seg.end, seg.records, seg.humanInputs));
};

/** 모든 세션의 그 열의 띠. 시작 순(같으면 긴 것, 그다음 키 순)이다. */
const dayBands = (sessions: readonly PreparedSession[], col: Span): DayBand[] =>
  sessions
    .flatMap((s) => sessionBands(s, col))
    .sort((a, b) => a.start - b.start || b.end - a.end || (a.key < b.key ? -1 : a.key > b.key ? 1 : 0));

/**
 * 같은 세션의 이웃한 띠 사이의 공백을 잇는다. 띠가 겹치거나 맞닿으면 잇지 않는다. 흐린(범위만 그리는) 띠는 하나뿐이다.
 */
const connectorsFor = (bands: readonly DayBand[]): BandConnector[] => {
  const bySession = new Map<string, DayBand[]>();
  for (const band of bands) {
    if (band.faint) continue;
    const list = bySession.get(band.sessionId);
    if (list) list.push(band);
    else bySession.set(band.sessionId, [band]);
  }
  const connectors: BandConnector[] = [];
  for (const [sessionId, list] of bySession) {
    list.sort((a, b) => a.start - b.start);
    // 앞 띠들 중 가장 늦게 끝나는 것을 기준으로 이어야, 안에 든 짧은 띠 때문에 선이 끊기지 않는다.
    let last = list[0];
    for (let i = 1; i < list.length; i++) {
      const next = list[i];
      if (next.start > last.end) {
        connectors.push({ sessionId, from: last.end, to: next.start, fromKey: last.key, toKey: next.key });
      }
      if (next.end > last.end) last = next;
    }
  }
  return connectors.sort((a, b) => a.from - b.from);
};

export { overlapsColumn, sessionBands, dayBands, connectorsFor };
