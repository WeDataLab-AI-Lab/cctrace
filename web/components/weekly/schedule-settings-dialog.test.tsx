import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { AIScheduleSource } from '@/lib/types';
import { followAdminRequest, ScheduleFields, scheduleRequest } from './schedule-settings-dialog';

const noop = () => {};

interface FieldsOver {
  enabled?: boolean;
  weekday?: number;
  hour?: number;
  minute?: number;
  whenSource?: AIScheduleSource;
}

const render = (over: FieldsOver = {}) =>
  renderToStaticMarkup(
    createElement(ScheduleFields, {
      enabled: true,
      weekday: 1,
      hour: 6,
      minute: 0,
      whenSource: 'admin' as AIScheduleSource,
      onChange: noop,
      ...over,
    }),
  );

describe('ScheduleFields', () => {
  it('offers every weekday, counted from Sunday', () => {
    const html = render();

    for (const day of ['일요일', '월요일', '화요일', '수요일', '목요일', '금요일', '토요일']) {
      expect(html).toContain(day);
    }
    expect(html.match(/<option/g)?.length).toBeGreaterThan(7);
  });

  it('shows the weekday and time now in force as the selected ones', () => {
    const html = render({ weekday: 3, hour: 15, minute: 30 });

    expect(html).toContain('value="3" selected');
    expect(html).toContain('value="15" selected');
    expect(html).toContain('value="30" selected');
  });

  // A value the user has not chosen is marked, because changing it means
  // leaving the administrator's default rather than editing one's own setting.
  it('marks a time that is still the administrator default', () => {
    expect(render({ whenSource: 'admin' })).toContain('관리자 기본값');
    expect(render({ whenSource: 'default' })).toContain('관리자 기본값');
    expect(render({ whenSource: 'user' })).not.toContain('관리자 기본값');
  });

  // Off means the weekday and time decide nothing, so they must not invite an
  // edit that would have no effect.
  // Counted on the attribute, not on the tag: the select's class carries the
  // Tailwind variant `disabled:opacity-50`, so a regex looking for `disabled`
  // anywhere inside the tag matches an enabled element too.
  it('disables the weekday and time while automatic runs are off', () => {
    const html = render({ enabled: false });

    expect(html.match(/disabled=""/g)?.length).toBe(3);
  });

  it('leaves them editable while automatic runs are on', () => {
    expect(render({ enabled: true }).match(/disabled=""/g)).toBeNull();
  });

  it('offers the switch itself', () => {
    expect(render()).toContain('자동 생성');
  });
});

describe('what the dialog sends', () => {
  it('saves the draft together with the zone it was chosen in', () => {
    const req = scheduleRequest({ enabled: true, weekday: 5, hour: 18, minute: 30 }, 'Asia/Seoul');

    expect(req).toEqual({ enabled: true, weekday: 5, hour: 18, minute: 30, tz: 'Asia/Seoul' });
  });

  // "Follow the administrator" has to clear the row, not copy today's values
  // into it. Copying them would freeze the current default as this user's own
  // setting, and a later change by the administrator would never reach them.
  //
  // The zone is the exception: it is the user's own, not part of the default,
  // and an empty one reads as UTC — which would fire at hours the screen never
  // showed them.
  it('clears every field except the zone when handing the schedule back', () => {
    expect(followAdminRequest('Asia/Seoul')).toEqual({
      enabled: null,
      weekday: null,
      hour: null,
      minute: null,
      tz: 'Asia/Seoul',
    });
  });
});
