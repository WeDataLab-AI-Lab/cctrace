import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { AIUserSchedule } from '@/lib/types';
import { ScheduleLine } from './schedule-line';

const schedule = (over: Partial<AIUserSchedule> = {}): AIUserSchedule => ({
  enabled: true,
  enabled_source: 'admin',
  weekday: 1,
  hour: 6,
  minute: 0,
  when_source: 'admin',
  tz: 'Asia/Seoul',
  next_run: '2026-09-21T06:00:00+09:00',
  ...over,
});

const noop = () => {};

const render = (s: AIUserSchedule) =>
  renderToStaticMarkup(
    createElement(ScheduleLine, { schedule: s, timeZone: 'Asia/Seoul', onOpenSettings: noop }),
  );

describe('ScheduleLine', () => {
  // The point of the line: a user learns a report is coming without pressing
  // anything, and knows when.
  it('names the weekday, the time and the next run', () => {
    const html = render(schedule());

    expect(html).toContain('매주 월요일 06:00');
    expect(html).toContain('9/21 06:00');
  });

  it('pads the minutes so 06:05 does not read as 06:5', () => {
    expect(render(schedule({ minute: 5 }))).toContain('매주 월요일 06:05');
  });

  // Sunday is 0, as it is in the API and in Go's time.Weekday.
  it('counts weekdays from Sunday', () => {
    expect(render(schedule({ weekday: 0 }))).toContain('일요일');
    expect(render(schedule({ weekday: 6 }))).toContain('토요일');
  });

  // Off is a state a user can be in for a long time, so the line says so
  // plainly rather than showing a time that will never come.
  it('says it is off and names no next run', () => {
    const html = render(schedule({ enabled: false, next_run: null }));

    expect(html).toContain('자동 생성 꺼짐');
    expect(html).not.toContain('9/21');
  });

  // A value the user has not chosen is marked, because "the admin set this for
  // everyone" and "I set this" lead to different decisions about changing it.
  it('marks a time that is still the administrator default', () => {
    expect(render(schedule({ when_source: 'admin' }))).toContain('관리자 기본값');
    expect(render(schedule({ when_source: 'default' }))).toContain('관리자 기본값');
  });

  it('does not mark a time the user chose', () => {
    expect(render(schedule({ when_source: 'user' }))).not.toContain('관리자 기본값');
  });

  // The way in to changing it has to be on the line that states it.
  it('offers the settings action', () => {
    expect(render(schedule())).toContain('실행 설정');
  });
});
