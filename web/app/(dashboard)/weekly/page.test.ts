import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { ReportSummary } from '@/components/weekly/report-summary';
import {
  FiguresHeader,
  resolveWeekId,
  ScopeBlock,
  unreadLabel,
  weekHref,
  weekNavigation,
  weeklyReadState,
  weekRedirect,
} from './page';
import type { WeeklyInsights } from '@/lib/types';

const TZ = 'Asia/Seoul';
// Monday 2026-09-14 15:47 in Seoul -- the first hours of 2026-W38.
const NOW = new Date('2026-09-14T06:47:00Z').getTime();
describe('resolveWeekId', () => {
  // The URL is the way back to a week: a retrospective read on Monday has to open
  // again on Friday (spec §3.1).
  it('opens the week named in the URL', () => {
    expect(resolveWeekId('2026-W37', NOW, TZ)).toBe('2026-W37');
  });

  // The week in progress is half-written: on Monday afternoon it holds one
  // morning's work and reads as a collapse against the week before. A review is
  // of a week that finished, which is also the week the report covers.
  it('opens the week that just ended without a parameter', () => {
    expect(resolveWeekId(null, NOW, TZ)).toBe('2026-W37');
  });

  it('opens the week that just ended for text that names no week', () => {
    expect(resolveWeekId('2026-37', NOW, TZ)).toBe('2026-W37');
    expect(resolveWeekId('2025-W53', NOW, TZ)).toBe('2026-W37');
  });

  // The week in progress stays reachable by name and through the next link.
  it('opens the week in progress when the URL names it', () => {
    expect(resolveWeekId('2026-W38', NOW, TZ)).toBe('2026-W38');
  });

  // A future week has no records; drawing it would read as an idle week.
  it('never opens a week that has not started', () => {
    expect(resolveWeekId('2026-W39', NOW, TZ)).toBe('2026-W37');
  });
});

describe('weekRedirect', () => {
  // The address bar must name the week on screen, or a shared link opens a
  // different week than the one the reader saw.
  it('rewrites a malformed or future week to the one shown', () => {
    expect(weekRedirect('2026-37', '2026-W38')).toBe('/weekly?week=2026-W38');
    expect(weekRedirect('2026-W39', '2026-W38')).toBe('/weekly?week=2026-W38');
  });

  it('leaves a valid week and a bare address alone', () => {
    expect(weekRedirect('2026-W37', '2026-W37')).toBeNull();
    expect(weekRedirect(null, '2026-W38')).toBeNull();
  });
});

describe('weekNavigation', () => {
  it('moves one week either way from a past week', () => {
    expect(weekNavigation('2026-W37', NOW, TZ)).toEqual({ prev: '2026-W36', next: '2026-W38' });
  });

  it('offers no next week from the current one', () => {
    expect(weekNavigation('2026-W38', NOW, TZ)).toEqual({ prev: '2026-W37', next: null });
  });

  it('crosses a year boundary', () => {
    expect(weekNavigation('2026-W01', NOW, TZ).prev).toBe('2025-W52');
  });
});

describe('weekHref', () => {
  it('addresses the week in the query string', () => {
    expect(weekHref('2026-W37')).toBe('/weekly?week=2026-W37');
  });
});

const insights = (over: Partial<WeeklyInsights> = {}): WeeklyInsights => ({
  agent_sessions: [
    { agent: 'claude', session_count: 38 },
    { agent: 'codex', session_count: 4 },
  ],
  segment_count: 58,
  uncovered_session_count: 0,
  excluded_record_count: 0,
  hours: [],
  projects: [],
  tasks: [],
  tools: [],
  typed_turn_count: 0,
  ...over,
});

const renderScope = (data: WeeklyInsights | undefined, readState: 'loading' | 'failed' | 'read', inProgress = false) =>
  renderToStaticMarkup(createElement(ScopeBlock, { weekId: '2026-W37', timeZone: TZ, inProgress, data, readState }));

describe('ScopeBlock', () => {
  // A week still running reads as a finished one without it (spec §3.1).
  it('marks the current week as in progress', () => {
    expect(renderScope(insights(), 'read', true)).toContain('진행 중');
    expect(renderScope(insights(), 'read')).not.toContain('진행 중');
  });

  // The figures sit above the report but never outrank it (spec §3.0).
  it('heads the report panel no lower than the figures area', () => {
    const level = (markup: string) => Number(/<h([1-6])/.exec(markup)?.[1]);
    const figures = level(renderToStaticMarkup(createElement(FiguresHeader)));

    const summary = renderToStaticMarkup(createElement(ReportSummary, { summary: 's' }));

    expect(level(summary)).toBeLessThanOrEqual(figures);
  });

  // The scope is a sentence that fixes the population, not a block competing
  // with the report for weight (plan-weekly-redesign §14.4 A).
  it('renders as a sentence without a heading or card', () => {
    const markup = renderScope(insights(), 'read');

    expect(markup).not.toMatch(/<h[1-6]/);
    expect(markup).not.toContain('shadow');
    expect(markup).not.toMatch(/(?<![:\w-])bg-surface(?![-\w])/);
  });

  it('states the period, time zone and whose records are read', () => {
    const markup = renderScope(insights(), 'read');

    expect(markup).toContain('9/7–9/13');
    expect(markup).toContain(TZ);
    expect(markup).toContain('내 기록');
    expect(markup).toContain('규칙 산출');
  });

  it('counts sessions per agent and work segments', () => {
    const markup = renderScope(insights(), 'read');

    expect(markup).toContain('Claude Code 세션 38');
    expect(markup).toContain('Codex 세션 4');
    expect(markup).toContain('작업 구간 58');
  });

  // What was not observed comes before any reading of the week (spec §3.3).
  it('declares what the week cannot see', () => {
    const markup = renderScope(insights({ excluded_record_count: 4 }), 'read');

    expect(markup).toContain('Codex 도구 수는 OTEL 메트릭 기준');
    expect(markup).toContain('TUI·VSCode 실행만 집계');
    expect(markup).toContain('기록 4건');
  });

  // The same rule as the agent caveats: a sentence about exclusions that removed
  // nothing of this reader's is a standing disclaimer, and standing disclaimers
  // are what make the caveats that do apply read as boilerplate.
  it('drops the exclusion line when none of the reader\'s records were excluded', () => {
    const markup = renderScope(insights(), 'read');

    expect(markup).not.toContain('제외');
  });

  // Codex records its tool calls -- the session view reads them out of the same
  // rows. What it does not carry is the success flag, so only the per-tool
  // use/fail figures lose it. Saying "leaves no tool calls" contradicts what the
  // user just saw on the session page (spec §3.3 names the narrow fact).
  it('says what is missing is the outcome, not the call', () => {
    const markup = renderScope(insights(), 'read');

    expect(markup).not.toContain('도구 호출을 남기지 않아');
  });

  // A caveat about an agent that did not run this week explains nothing and
  // makes the caveats that do apply read as boilerplate.
  it('drops an agent caveat when that agent has no sessions', () => {
    const claudeOnly = insights({ agent_sessions: [{ agent: 'claude', session_count: 38 }] });

    expect(renderScope(claudeOnly, 'read')).not.toContain('Codex');
  });

  it('names sessions the segment facts do not cover, only when there are some', () => {
    expect(renderScope(insights({ uncovered_session_count: 3 }), 'read')).toContain('세션 3개');
    expect(renderScope(insights(), 'read')).not.toContain('구간 사실');
  });

  // #670: an unread week must not print as an empty one.
  it('prints no figures before the read completes', () => {
    const loading = renderScope(undefined, 'loading');
    expect(loading).toContain('불러오는 중');
    expect(loading).not.toContain('작업 구간 0');
    expect(loading).toContain('9/7–9/13');

    expect(renderScope(undefined, 'failed')).toContain('불러오지 못함');
  });

  it('says so when no agent recorded a session', () => {
    expect(renderScope(insights({ agent_sessions: [], segment_count: 0 }), 'read')).toContain('세션 0');
  });
});

describe('report panel on the page', () => {
  // The §3.4 state is decided by lib/ai-report-state from the API response; the
  // page no longer carries its own always-unconfigured stand-in.
  it('keeps no report state of its own', async () => {
    const page = await import('./page');

    expect(page).not.toHaveProperty('reportPanelState');
    expect(page).not.toHaveProperty('ReportStatusPanel');
  });
});

describe('weeklyReadState / unreadLabel', () => {
  // The cards read `data?.x ?? []`, so a failed read rendered "No sessions in this
  // period" and "Active projects 0". An unread week and an idle week looked the
  // same (#670).

  it('keeps a failed read apart from an empty one', () => {
    expect(weeklyReadState(false, true, false)).toBe('failed');
    expect(weeklyReadState(false, false, true)).toBe('read');
  });

  // A failed refetch does not unread a week already on screen; the banner says
  // the refresh failed.
  it('keeps a read week read when only the refetch failed', () => {
    expect(weeklyReadState(false, true, true)).toBe('read');
  });

  it('treats a resolved query with no data as still pending', () => {
    // react-query can report !isLoading before data exists on a remount; printing
    // zeros in that window is the same lie, briefly.
    expect(weeklyReadState(false, false, false)).toBe('loading');
  });

  it('gives a reason instead of a bare dash', () => {
    expect(unreadLabel('failed')).toBe('불러오지 못함');
    expect(unreadLabel('loading')).toBe('불러오는 중');
    // A read week has no excuse to print -- the numbers speak.
    expect(unreadLabel('read')).toBeNull();
  });
});
