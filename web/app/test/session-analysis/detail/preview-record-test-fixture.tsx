// @vitest-environment jsdom

import { act, cleanup, fireEvent, screen, within } from '@testing-library/react';
import { afterEach, beforeAll, beforeEach, expect, vi } from 'vitest';
import { NO_RECORD_ID, RECORD_FAILED_ID, RECORD_STALE_ID } from './variants';

// Keep the detail fixtures and actual tab, but omit the unrelated generated calendar rows.
vi.mock('../calendar/dataset', () => ({ DATASET: [], CALENDAR_SESSIONS: [] }));
// This suite tests record generation and week-scoped controls, not rendering 2,000 input marks.
// The real grid and exact input keys are covered by preview-calendar.integration.test.tsx.
vi.mock('@/components/sessions/calendar/calendar-grid', async importOriginal => ({
  ...await importOriginal<typeof import('@/components/sessions/calendar/calendar-grid')>(), CalendarGrid: () => null,
}));

// Quota charts and account meters do not participate in record generation; keep real global search.
vi.mock('@/components/sessions/frame/usage-mini-chart', () => ({ UsageMiniChart: () => null }));
vi.mock('@/components/sessions/frame/usage-meters', () => ({ UsageMeters: () => null }));

// Exercise the actual state variants needed here without deriving the unrelated 2,000-turn fixture on every interaction.
vi.mock('./fixtures', async importOriginal => {
  const original = await importOriginal<typeof import('./fixtures')>();
  const variants = await import('./variants');
  const ids = new Set([original.FLAGSHIP_ID, variants.NO_RECORD_ID, variants.RECORD_FAILED_ID, variants.RECORD_STALE_ID]);
  return { ...original, FLAGSHIP_ID: variants.NO_RECORD_ID, DETAIL_SESSIONS: original.DETAIL_SESSIONS.filter(fixture => ids.has(fixture.id)) };
});

const main = () => within(screen.getByRole('main'));
const recordState = () => screen.getByRole('main').querySelector<HTMLElement>('[data-record-state]')?.dataset.recordState;

const SEARCH_TERMS: Record<string, string> = {
  [RECORD_FAILED_ID]: '환율 캐시 만료 처리',
  [NO_RECORD_ID]: 'CLI 도움말 문구',
  [RECORD_STALE_ID]: '월말 마감 리포트 CSV',
  'dst-fall-over-the-repeat': '반복된 시를 가로지르는 세션',
};

const selectSession = (id: string) => {
  fireEvent.change(screen.getByRole('searchbox', { name: '전체 검색' }), { target: { value: SEARCH_TERMS[id] } });
  const card = screen.getByRole('list', { name: '세션 카드' }).querySelector<HTMLButtonElement>(`[data-session-id="${id}"]`);
  expect(card).not.toBeNull();
  fireEvent.click(card!);
};

const showList = () => fireEvent.click(screen.getByRole('button', { name: '목록' }));
const advance = (ms: number) => act(() => vi.advanceTimersByTime(ms));

beforeAll(() => {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
});

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn();
  vi.useFakeTimers();
});

afterEach(() => {
  cleanup();
  vi.clearAllTimers();
  vi.useRealTimers();
});


export { main, recordState, selectSession, showList, advance };
