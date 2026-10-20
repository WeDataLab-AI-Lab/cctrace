/**
 * 캘린더 미리보기의 전체 데이터: 패스 A의 열여섯 세션 + 합성 세션 330여 건 + 뉴욕 전환일 시연 세션.
 * 목록과 캘린더는 같은 `DATASET`을 읽는다 — 그래서 두 모드가 같은 세션을 보인다(혼합 신원 J는 목록에만 보류 행으로 있다).
 */

import type { CalendarSession, CalendarStatus } from '@/lib/session-calendar-types';
import type { FilterableSession } from '@/components/sessions/calendar/session-filters';
import { PREVIEW_NOW } from '../builders';
import { PREVIEW_SESSIONS, type SessionFixture } from '../fixtures';
import { calendarSessionOf, fixtureOfBlueprint, withFacts, requestCount } from './blueprint-fixtures';
import { DST_DEMO } from './dst-demo';
import { generateBlueprints, type Blueprint } from './generate';
import { calendarOfFixture, metaOf } from './pass-a-calendar';

export interface DatasetEntry {
  fixture: SessionFixture;
  /** 캘린더 응답에 들어가는 세션(주인 범위). 응답에 없는 세션(혼합 신원)은 null. */
  calendar: CalendarSession | null;
  status: CalendarStatus | null;
  /** 요청 수. 모르면 null, 0은 요청 없는 세션. */
  requests: number | null;
  planFile: boolean;
  /** 설계도에서 온 합성 세션인가(패스 A의 픽스처가 아니다). */
  generated: boolean;
}

const passA = (): DatasetEntry[] =>
  PREVIEW_SESSIONS.map((fixture) => ({
    fixture,
    calendar: calendarOfFixture(fixture),
    status: metaOf(fixture.id).status,
    requests: typeof fixture.row.analysis?.requests === 'number' ? fixture.row.analysis.requests : null,
    planFile: fixture.row.analysis?.plan_flags?.has_plan_file === true,
    generated: false,
  }));

const fromBlueprint = (bp: Blueprint): DatasetEntry => ({
  fixture: fixtureOfBlueprint(bp),
  calendar: calendarSessionOf(bp),
  status: bp.status,
  requests: withFacts(bp) ? (bp.requestless ? 0 : requestCount(bp)) : null,
  planFile: bp.planFile,
  generated: true,
});

const BLUEPRINTS: Blueprint[] = generateBlueprints(PREVIEW_NOW);

const DATASET: DatasetEntry[] = [...passA(), ...BLUEPRINTS.map(fromBlueprint)];

/** 캘린더가 읽는 세션 전부(시연용 포함). */
const CALENDAR_SESSIONS: CalendarSession[] = [
  ...DATASET.flatMap((e) => (e.calendar ? [e.calendar] : [])),
  ...DST_DEMO,
];

const filterableOf = (entry: DatasetEntry): FilterableSession => ({
  id: entry.fixture.id,
  status: entry.status,
  requests: entry.requests,
  planFile: entry.planFile,
  startMs: Date.parse(entry.fixture.row.start_time) || 0,
  endMs: Date.parse(entry.fixture.row.end_time) || 0,
  costUsd: typeof entry.fixture.row.cost_usd === 'number' ? entry.fixture.row.cost_usd : null,
});

const ENTRY_BY_ID: ReadonlyMap<string, DatasetEntry> = new Map(DATASET.map((e) => [e.fixture.id, e]));

export { BLUEPRINTS, DATASET, CALENDAR_SESSIONS, ENTRY_BY_ID, filterableOf };
