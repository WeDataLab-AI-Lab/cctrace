// @vitest-environment jsdom

import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { P_ID, P_SCHEDULE } from '../fixtures';
import { CALENDAR_SESSIONS } from '../calendar/dataset';
import { readCalendar } from '../calendar/read-calendar';
import { DetailPreview } from './preview';

beforeAll(() => {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  Element.prototype.scrollIntoView = vi.fn();
});

afterEach(cleanup);

describe('상세 미리보기의 원본 캘린더 활동 보존', () => {
  it('긴 P 세션의 나흘 활동 띠와 원본 입력 키를 유지하고 주간 입력 2,000건 상한을 알린다', () => {
    const source = CALENDAR_SESSIONS.find((session) => session.session_id === P_ID);
    if (!source) throw new Error('P calendar source missing');
    expect(source.segments).toHaveLength(4);
    expect(source.inputs.items).toHaveLength(P_SCHEDULE.totalInputs);
    expect(source.segments.map((segment) => segment.start)).toEqual(P_SCHEDULE.dayStartsMs.map((start) => new Date(start).toISOString()));

    const week = readCalendar([source], '2026-10-04T15:00:00.000Z', '2026-10-11T15:00:00.000Z', 'owner').sessions[0];
    expect(week.inputs.truncated).toBe(true);
    expect(week.inputs.items).toHaveLength(2_000);
    expect(week.inputs.items.map((input) => input.turn_key)).toEqual(source.inputs.items.slice(0, 2_000).map((input) => input.turn_key));

    const { unmount } = render(<DetailPreview />);
    const calendar = within(screen.getByRole('complementary', { name: '주간 세션 캘린더' }));
    const bands = calendar.getAllByRole('button').filter((button) => button.getAttribute('data-session-id') === P_ID && button.hasAttribute('data-calendar-band') && !button.hasAttribute('data-faint'));
    expect(bands).toHaveLength(4);
    expect(new Set(bands.map((band) => band.getAttribute('data-band-key')?.split(':')[0])).size).toBe(4);
    expect(bands.reduce((count, band) => count + Number(band.getAttribute('aria-description')?.match(/입력 (\d+)건/)?.[1] ?? 0), 0)).toBe(2_000);
    expect(calendar.getByText(/확인이 필요한 세션/).textContent).toContain('입력 시각 상한');
    unmount();
  }, 15_000);
});
