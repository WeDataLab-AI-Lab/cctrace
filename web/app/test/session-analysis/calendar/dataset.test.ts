import { describe, expect, it } from 'vitest';
import { assignColors, COLOR_KEYS } from '@/components/sessions/calendar/colors';
import { weekColumns, weekWindow, type DayColumn } from '@/components/sessions/calendar/day-grid';
import { layoutWeek } from '@/components/sessions/calendar/layout';
import { prepareSessions } from '@/components/sessions/calendar/prepare';
import { baseFilter, statusFilter } from '@/components/sessions/calendar/session-filters';
import { statusChipCounts } from '@/components/sessions/calendar/status';
import { shiftWeek, todayWeekStart } from '@/components/sessions/calendar/week';
import { hasSegments, type CalendarSession } from '@/lib/session-calendar-types';
import { PREVIEW_NOW } from '../builders';
import { A_ID, J_ID, P_ID, PREVIEW_SESSIONS } from '../fixtures';
import { BLUEPRINTS, CALENDAR_SESSIONS, DATASET, ENTRY_BY_ID, filterableOf } from './dataset';
import { generateBlueprints } from './generate';
import { readCalendar } from './read-calendar';

const TZ = 'Asia/Seoul';
const thisWeek = todayWeekStart(TZ, PREVIEW_NOW);
const weekOf = (weeksBack: number) => {
  const start = shiftWeek(thisWeek, -weeksBack);
  const window = weekWindow(TZ, start);
  const data = readCalendar(CALENDAR_SESSIONS, new Date(window.from).toISOString(), new Date(window.to).toISOString(), 'owner');
  const columns = weekColumns(TZ, start, PREVIEW_NOW);
  const layout = layoutWeek({ timeZone: TZ, columns, sessions: prepareSessions(data.sessions), pxPerHour: 30, nowMs: PREVIEW_NOW });
  return { data, columns, layout, start };
};
const sessionsOnDay = (layout: ReturnType<typeof weekOf>['layout'], day: number): Set<string> =>
  new Set(layout.days[day].bands.map((b) => b.sessionId));

describe('합성 주는 레퍼런스(138 세션)만큼 붐빈다', () => {
  const current = weekOf(0);
  const previous = weekOf(1);

  it('이번 주와 지난주 모두 백 건을 훌쩍 넘는다', () => {
    expect(current.data.sessions.length).toBeGreaterThan(120);
    expect(previous.data.sessions.length).toBeGreaterThan(120);
  });

  it('평일은 하루 스무 건 넘게 걸쳐 있고, 이번 주 월~토와 지난주 월~토가 모두 채워져 있다', () => {
    for (const week of [current, previous]) {
      for (let day = 0; day < 5; day++) expect(sessionsOnDay(week.layout, day).size, `평일 ${day}`).toBeGreaterThan(18);
      expect(sessionsOnDay(week.layout, 5).size, '토요일').toBeGreaterThan(5);
    }
  });

  it('활동이 없는 날이 있다: 지난주 일요일(지난 날)과 이번 주 일요일(아직 오지 않음)', () => {
    expect(sessionsOnDay(previous.layout, 6).size).toBe(0);
    expect(sessionsOnDay(current.layout, 6).size).toBe(0);
  });

  it('기준 시각은 이번 주 토요일이고, 그 뒤의 활동은 없다', () => {
    expect(current.columns.findIndex((c) => c.isToday)).toBe(5);
    for (const s of CALENDAR_SESSIONS) {
      for (const seg of s.segments) expect(Date.parse(seg.end), s.session_id).toBeLessThanOrEqual(PREVIEW_NOW);
      for (const input of s.inputs.items) expect(Date.parse(input.ts), s.session_id).toBeLessThanOrEqual(PREVIEW_NOW);
    }
  });

  it('밤과 새벽에도 세션이 있다(0~5시 서울)', () => {
    const nightBands = previous.layout.days.flatMap((d) =>
      d.bands.filter((b) => !b.faint && (b.start - d.column.start) / 3_600_000 < 5),
    );
    expect(nightBands.length).toBeGreaterThan(5);
  });

  it('겹침: 두 세션부터 다섯 세션이 한꺼번에 겹치는 묶음까지 있다', () => {
    const lanes = new Set(current.layout.days.flatMap((d) => d.bands.map((b) => b.lanes)));
    for (const n of [1, 2, 3, 4, 5]) expect(lanes.has(n), `레인 ${n}`).toBe(true);
  });

  it('자정을 넘는 세션: 한 조각이 두 열에 걸쳐 이어진다', () => {
    const continues = current.layout.days.flatMap((d, i) =>
      d.bands.filter((b) => b.continuesAfter && !b.faint && current.layout.days[i + 1]?.bands.some((n) => n.sessionId === b.sessionId && n.continuesBefore)),
    );
    expect(continues.length).toBeGreaterThan(0);
  });

  it('여러 날에 걸쳐 긴 공백으로 이어 한 세션이 있다(조각 4개, 서로 다른 날 4곳)', () => {
    const resumed = BLUEPRINTS.find((b) => b.scenario === 'resumed');
    expect(resumed).toBeDefined();
    const days = new Set(resumed?.segments.map((s) => Math.floor((s.start + 9 * 3_600_000) / 86_400_000)));
    expect(days.size).toBeGreaterThanOrEqual(4);
    // 그 세션은 지난주와 이번 주 양쪽에 조각을 가진다.
    expect(previous.data.sessions.some((s) => s.session_id === resumed?.id)).toBe(true);
    expect(current.data.sessions.some((s) => s.session_id === resumed?.id)).toBe(true);
  });

  it('입력이 수백 개인 세션이 있고, 한 주에 2,000개가 넘으면 잘린다(P)', () => {
    const marathon = BLUEPRINTS.find((b) => b.scenario === 'marathon');
    expect(marathon?.inputs.length).toBeGreaterThan(200);
    const p = current.data.sessions.find((s) => s.session_id === P_ID);
    expect(p?.inputs.truncated).toBe(true);
    expect(p?.inputs.items).toHaveLength(2_000);
  });

  it('프로젝트·에이전트·프로필이 여럿이다', () => {
    const projects = new Set(current.data.sessions.map((s) => s.colors.project?.hash));
    const agents = new Set(current.data.sessions.map((s) => s.colors.agent));
    const profiles = new Set(current.data.sessions.map((s) => s.colors.profile));
    expect(projects.size).toBeGreaterThanOrEqual(6);
    expect(agents.size).toBeGreaterThanOrEqual(2);
    expect(profiles.size).toBeGreaterThanOrEqual(3);
  });

  it('사실이 잘린 세션은 범위만 있고, 가려진·보류된 첫 줄도 있다', () => {
    const truncated = current.data.sessions.filter((s) => s.facts === 'truncated');
    expect(truncated.length).toBeGreaterThan(0);
    for (const s of truncated) {
      expect(s.segments).toEqual([]);
      expect(s.inputs.state).toBe('undeterminable');
    }
    const previewStates = new Set(current.data.sessions.flatMap((s) => s.inputs.items.map((i) => i.preview.state)));
    expect(previewStates.has('redacted')).toBe(true);
    expect(previewStates.has('withheld')).toBe(true);
    expect(previewStates.has('ok')).toBe(true);
  });

  it('턴 경계를 모르는 세션(C)은 사람 입력 수가 null이다(0이 아니다)', () => {
    const c = CALENDAR_SESSIONS.find((s) => s.title.state === 'redacted' && s.inputs.state === 'undeterminable' && s.segments.length > 0);
    expect(c).toBeDefined();
    expect(c?.segments.every((seg) => seg.human_inputs === null)).toBe(true);
  });

  it('요청 없는 세션은 요청 0이고 입력선이 없다(관측한 0)', () => {
    const requestless = current.data.sessions.filter((s) => s.requests === 0);
    expect(requestless.length).toBeGreaterThan(5);
    for (const s of requestless) {
      expect(s.inputs.state).toBe('observed');
      expect(s.inputs.items).toEqual([]);
    }
  });
});

describe('색 기준 범례는 어떤 기준에서든 그 주의 세션 수와 합이 같다', () => {
  for (const weeksBack of [0, 1]) {
    for (const { key } of COLOR_KEYS) {
      it(`${weeksBack === 0 ? '이번 주' : '지난주'} · ${key}`, () => {
        const { data } = weekOf(weeksBack);
        const assigned = assignColors(data.sessions, key);
        expect(assigned.legend.reduce((n, e) => n + e.sessions, 0)).toBe(data.sessions.length);
      });
    }
  }

  it('화면 기준은 레퍼런스처럼 열댓 개 값과 "(없음)" 칸이 있다', () => {
    const { data } = weekOf(0);
    const legend = assignColors(data.sessions, 'screen').legend;
    expect(legend.length).toBeGreaterThanOrEqual(12);
    expect(legend.some((e) => e.none)).toBe(true);
    expect(legend.map((e) => e.sessions)).toEqual([...legend.map((e) => e.sessions)].sort((a, b) => b - a));
  });

  it('완료 여부는 네 값이고, 비소유 관리자 범위에는 키가 없다', () => {
    const owner = weekOf(0).data;
    expect(new Set(assignColors(owner.sessions, 'status').legend.filter((e) => !e.none).map((e) => e.key))).toEqual(
      new Set(['done', 'leftover', 'midway', 'running']),
    );
    const window = weekWindow(TZ, thisWeek);
    const admin = readCalendar(CALENDAR_SESSIONS, new Date(window.from).toISOString(), new Date(window.to).toISOString(), 'admin');
    expect(admin.owner_view).toBe(false);
    expect(admin.sessions.every((s) => !('status' in s.colors))).toBe(true);
  });
});

describe('목록과 캘린더는 같은 세션을 보인다', () => {
  it('패스 A의 세션은 모두 데이터셋에 있고, 혼합 신원(J)만 캘린더에 없다', () => {
    for (const f of PREVIEW_SESSIONS) {
      expect(ENTRY_BY_ID.has(f.id), f.id).toBe(true);
      expect(ENTRY_BY_ID.get(f.id)?.calendar === null, f.id).toBe(f.id === J_ID);
    }
  });

  it('캘린더 세션은 모두 목록 행(데이터셋)이 있다 — 뉴욕 시연 세션만 예외다', () => {
    const rows = new Set(DATASET.map((e) => e.fixture.id));
    for (const s of CALENDAR_SESSIONS) {
      if (s.session_id.startsWith('dst-')) continue;
      expect(rows.has(s.session_id), s.session_id).toBe(true);
    }
  });

  it('세션 ID는 모두 다르고 앞 8자도 겹치지 않는다', () => {
    const ids = DATASET.map((e) => e.fixture.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(new Set(ids.map((id) => id.slice(0, 8))).size).toBe(ids.length);
  });

  it('캘린더의 요청 수·비용·제목은 목록 행과 같다', () => {
    for (const entry of DATASET) {
      if (!entry.calendar) continue;
      const row = entry.fixture.row;
      if (entry.generated) {
        expect(entry.calendar.requests, row.session_id).toBe(row.analysis?.requests);
        expect(entry.calendar.cost.usd ?? 0, row.session_id).toBeCloseTo(row.cost_usd, 2);
      }
      expect(entry.calendar.title.state, row.session_id).toBe(row.analysis?.title?.state ?? 'none');
    }
  });

  it('판정 불가를 독립 칩으로 세고 네 칩의 합은 세션 수다(필터가 어떻게 걸려도)', () => {
    for (const [showRequestless, planFileOnly] of [
      [false, false],
      [true, false],
      [false, true],
      [true, true],
    ] as const) {
      const base = baseFilter(DATASET.map(filterableOf), { showRequestless, planFileOnly });
      const counts = statusChipCounts(base.map((s) => s.status));
      expect(Object.values(counts.counts).reduce((sum, count) => sum + count, 0)).toBe(base.length);
      for (const chip of ['done', 'leftover', 'midway', 'unknown'] as const) {
        expect(statusFilter(base, chip)).toHaveLength(counts.counts[chip]);
      }
      expect(counts.counts.unknown).toBe(base.filter((s) => s.status === null || s.status === undefined || s.status === 'unknown').length);
    }
  });

  it('요청 없는 세션은 기본으로 목록에서 빠지고 켜면 그만큼 늘어난다', () => {
    const items = DATASET.map(filterableOf);
    const hidden = baseFilter(items, { showRequestless: false, planFileOnly: false });
    const shown = baseFilter(items, { showRequestless: true, planFileOnly: false });
    expect(shown.length).toBeGreaterThan(hidden.length);
    expect(shown.length).toBe(items.length);
  });
});

describe('설계도', () => {
  it('같은 시드는 같은 주를 만든다', () => {
    const again = generateBlueprints(PREVIEW_NOW);
    expect(again.map((b) => b.id)).toEqual(BLUEPRINTS.map((b) => b.id));
    expect(again.map((b) => b.segments.length)).toEqual(BLUEPRINTS.map((b) => b.segments.length));
  });

  it('조각은 시작이 끝보다 앞서고, 공백은 15분 이상이다', () => {
    for (const bp of BLUEPRINTS) {
      const sorted = [...bp.segments].sort((a, b) => a.start - b.start);
      sorted.forEach((s, i) => {
        expect(s.end, bp.id).toBeGreaterThanOrEqual(s.start);
        if (i > 0 && bp.scenario === 'ordinary') expect(s.start - sorted[i - 1].end, bp.id).toBeGreaterThanOrEqual(15 * 60_000);
      });
    }
  });

  it('입력은 자기 조각 안에 있고 시각 순이다', () => {
    for (const bp of BLUEPRINTS) {
      if (!hasSegments(bp.facts)) continue;
      for (const input of bp.inputs) {
        expect(
          bp.segments.some((s) => input.at >= s.start && input.at <= s.end),
          `${bp.id} ${new Date(input.at).toISOString()}`,
        ).toBe(true);
      }
    }
  });

  it('각 열의 띠는 열 안에 있다', () => {
    const { layout } = weekOf(0);
    layout.days.forEach((d: { column: DayColumn; bands: { start: number; end: number }[] }) => {
      for (const b of d.bands) {
        expect(b.start).toBeGreaterThanOrEqual(d.column.start);
        expect(b.end).toBeLessThanOrEqual(d.column.end);
      }
    });
  });

  it('패스 A의 세션도 같은 주에 실려 있다(A는 지금 일하고 있다)', () => {
    const a = weekOf(0).data.sessions.find((s) => s.session_id === A_ID);
    expect(a?.colors.status).toBe('running');
    expect(a?.segments.length).toBeGreaterThan(0);
  });
});

describe('캘린더 읽기: 서버가 지킬 규칙', () => {
  const sessions: CalendarSession[] = CALENDAR_SESSIONS;
  const window = weekWindow(TZ, thisWeek);
  const from = new Date(window.from).toISOString();
  const to = new Date(window.to).toISOString();

  it('구간이 14일을 넘거나 거꾸로면 거절한다', () => {
    expect(() => readCalendar(sessions, '2026-10-01T00:00:00Z', '2026-10-20T00:00:00Z', 'owner')).toThrow(/14/);
    expect(() => readCalendar(sessions, to, from, 'owner')).toThrow(/invalid/);
  });

  it('조각이 구간에 걸치는 세션만 고르고, 조각은 자르지 않는다', () => {
    const data = readCalendar(sessions, from, to, 'owner');
    for (const s of data.sessions) {
      const overlaps = hasSegments(s.facts)
        ? s.segments.some((seg) => Date.parse(seg.start) < Date.parse(to) && Date.parse(seg.end) >= Date.parse(from))
        : Date.parse(s.range.start) < Date.parse(to) && Date.parse(s.range.end) >= Date.parse(from);
      expect(overlaps, s.session_id).toBe(true);
    }
    // 이번 주에 일부만 걸친 세션의 조각은 구간 밖까지 이어진 채로 온다(자르지 않는다).
    expect(data.sessions.some((s) => s.segments.some((seg) => Date.parse(seg.start) < Date.parse(from)))).toBe(true);
  });

  it('입력 시각은 그 구간 것만, 시각 순으로, 세션마다 2,000개까지다', () => {
    const data = readCalendar(sessions, from, to, 'owner');
    for (const s of data.sessions) {
      const times = s.inputs.items.map((i) => Date.parse(i.ts));
      expect(times.every((t) => t >= Date.parse(from) && t < Date.parse(to)), s.session_id).toBe(true);
      expect([...times].sort((a, b) => a - b)).toEqual(times);
      expect(times.length).toBeLessThanOrEqual(2_000);
    }
  });

  it('다른 주의 응답에는 이번 주만의 세션이 없다', () => {
    const lastWeek = weekWindow(TZ, shiftWeek(thisWeek, -1));
    const data = readCalendar(sessions, new Date(lastWeek.from).toISOString(), new Date(lastWeek.to).toISOString(), 'owner');
    expect(data.sessions.some((s) => s.session_id === P_ID)).toBe(false);
  });
});
