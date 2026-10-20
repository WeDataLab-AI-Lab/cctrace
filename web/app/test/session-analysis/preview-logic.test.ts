import { describe, expect, it } from 'vitest';
import { PREVIEW_NOW } from './builders';
import { A_ID, B_ID, C_ID, D_ID, E_ID, F_ID, K_ID, PREVIEW_SESSIONS, rowFor } from './fixtures';
import { isLiveAt, queryRows } from './preview-logic';

const rows = PREVIEW_SESSIONS.map((f) => rowFor(f, 'owner'));
const ids = (list: readonly { session_id: string }[]): string[] => list.map((r) => r.session_id);

describe('queryRows', () => {
  it('필터가 없으면 전부 남고, 입력 배열은 바뀌지 않는다', () => {
    const before = ids(rows);
    const out = queryRows(rows, { category: null, plan: null, sort: 'start' });
    expect(out).toHaveLength(rows.length);
    expect(ids(rows)).toEqual(before);
  });

  it('범주 필터는 그 범주의 세션만 남긴다', () => {
    expect(ids(queryRows(rows, { category: 'waiting', plan: null, sort: 'start' }))).toEqual([A_ID]);
    expect(ids(queryRows(rows, { category: 'open_question', plan: null, sort: 'start' }))).toEqual([K_ID]);
  });

  it('플랜 필터: 파일과 모드는 다른 사실이라 다른 세션이 남는다', () => {
    const file = ids(queryRows(rows, { category: null, plan: 'file', sort: 'start' }));
    const mode = ids(queryRows(rows, { category: null, plan: 'mode', sort: 'start' }));
    expect(file).toContain(E_ID);
    expect(file).not.toContain(B_ID);
    expect(mode).toContain(B_ID);
    expect(mode).not.toContain(E_ID);
    expect(file).toContain(A_ID);
    expect(mode).toContain(A_ID);
  });

  it('분석이 없는 행은 플랜 필터에서 빠진다', () => {
    expect(ids(queryRows(rows, { category: null, plan: 'file', sort: 'start' }))).not.toContain(F_ID);
  });

  it('시작·활동 정렬은 최신이 먼저', () => {
    const start = queryRows(rows, { category: null, plan: null, sort: 'start' });
    for (let i = 1; i < start.length; i++) {
      expect(Date.parse(start[i - 1].start_time)).toBeGreaterThanOrEqual(Date.parse(start[i].start_time));
    }
    const activity = queryRows(rows, { category: null, plan: null, sort: 'activity' });
    for (let i = 1; i < activity.length; i++) {
      expect(Date.parse(activity[i - 1].end_time)).toBeGreaterThanOrEqual(Date.parse(activity[i].end_time));
    }
  });

  it('비용 정렬은 큰 것이 먼저', () => {
    const cost = queryRows(rows, { category: null, plan: null, sort: 'cost' });
    for (let i = 1; i < cost.length; i++) expect(cost[i - 1].cost_usd).toBeGreaterThanOrEqual(cost[i].cost_usd);
  });

  it('요청 수 정렬: 많은 것이 먼저, 요청 수를 모르는 세션은 맨 뒤', () => {
    const out = queryRows(rows, { category: null, plan: null, sort: 'requests' });
    const counts = out.map((r) => r.analysis?.requests);
    const firstUnknown = counts.findIndex((n) => typeof n !== 'number');
    expect(firstUnknown).toBeGreaterThan(0);
    expect(counts.slice(firstUnknown).every((n) => typeof n !== 'number')).toBe(true);
    const known = counts.slice(0, firstUnknown) as number[];
    for (let i = 1; i < known.length; i++) expect(known[i - 1]).toBeGreaterThanOrEqual(known[i]);
    // 턴 경계를 판단할 수 없는 세션(C)의 요청 수 null은 0이 아니라 모름이라 0건인 세션보다 뒤다.
    const zero = out.findIndex((r) => r.analysis?.requests === 0);
    expect(zero).toBeGreaterThanOrEqual(0);
    expect(out.findIndex((r) => r.session_id === C_ID)).toBeGreaterThan(zero);
  });
});

describe('isLiveAt', () => {
  it('마지막 기록이 5분 이내일 때만 LIVE (기준 시각으로 계산)', () => {
    const f = rows.find((r) => r.session_id === F_ID);
    const d = rows.find((r) => r.session_id === D_ID);
    expect(f && isLiveAt(f, PREVIEW_NOW)).toBe(true);
    expect(d && isLiveAt(d, PREVIEW_NOW)).toBe(true);
    const old = rows.find((r) => r.session_id === B_ID);
    expect(old && isLiveAt(old, PREVIEW_NOW)).toBe(false);
    // 미래의 시각은 LIVE가 아니다.
    expect(f && isLiveAt(f, PREVIEW_NOW - 3_600_000)).toBe(false);
  });
});
