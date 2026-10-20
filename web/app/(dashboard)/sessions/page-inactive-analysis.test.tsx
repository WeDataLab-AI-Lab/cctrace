// @vitest-environment jsdom
import { mount, state, row, waitFor, SessionsPage } from './page-test-fixture';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { createElement } from 'react';
import { QueryClientProvider } from '@tanstack/react-query';
import { expect, it, vi } from 'vitest';
import { fetchSessionCalendar, fetchSessionOverview, fetchSessionOverviewCount, fetchSessionOverviewCategories } from '@/lib/api';

const maintenance = { token_rule_pending: false, step: '', session_analysis: { enabled: true } };

it('leaves cached analysis queries stale after project deletion without fetching them in basic view', async () => {
  state.isAdmin = true;
  const { client } = mount(maintenance);
  await waitFor(() => expect(screen.getByTestId('viewer-session').textContent).toBe('working-row'));
  const analysisRows = vi.fn(async () => ({ pages: [[{ ...row('analysis-row'), project_hash: 'deleted-project' }, row('keep-row')]], pageParams: [0] }));
  const analysisCount = vi.fn(async () => ({ count: 1, unattributed: 0 }));
  const rowsKey = ['session-overview', 'inactive-analysis', true];
  const countKey = ['session-overview-count', 'inactive-analysis', true];
  await client.fetchQuery({ queryKey: rowsKey, queryFn: analysisRows });
  await client.fetchQuery({ queryKey: countKey, queryFn: analysisCount });
  analysisRows.mockClear();
  analysisCount.mockClear();
  const reads = vi.mocked(fetchSessionOverview).mock.calls.length;
  act(() => state.onProjectDelete?.({ project_hash: 'deleted-project', project_name: 'Deleted project' }));
  expect(state.onProjectDeleted).toBeTypeOf('function');
  act(() => state.onProjectDeleted?.());
  await waitFor(() => expect(vi.mocked(fetchSessionOverview).mock.calls.length).toBeGreaterThan(reads));
  expect(analysisRows).not.toHaveBeenCalled();
  expect(analysisCount).not.toHaveBeenCalled();
  expect(client.getQueryData<{ pages: ReturnType<typeof row>[][] }>(rowsKey)?.pages[0].map(session => session.session_id)).toEqual(['keep-row']);
  expect(client.getQueryState(rowsKey)?.isInvalidated).toBe(true);
  expect(client.getQueryState(countKey)?.isInvalidated).toBe(true);
});

it('restores the basic view, selection, search, and scroll without reading analysis when the capability is on', async () => {
  const { client, view } = mount(maintenance);
  fireEvent.click(await screen.findByRole('button', { name: /other-ro/ }));
  fireEvent.change(screen.getByRole('textbox', { name: '세션 검색' }), { target: { value: 'row' } });
  document.querySelector<HTMLElement>('[data-session-return-scroll="list"]')!.scrollTop = 123;
  act(() => state.onOpenAnalysis?.());
  client.setDefaultOptions({ queries: { retry: false, staleTime: 60_000 } });
  view.unmount();
  vi.mocked(fetchSessionOverview).mockClear();
  render(createElement(QueryClientProvider, { client }, createElement(SessionsPage)));
  expect(screen.getByRole('button', { name: '목록' }).getAttribute('aria-pressed')).toBe('true');
  expect(screen.getByRole('textbox', { name: '세션 검색' })).toHaveProperty('value', 'row');
  expect(screen.getByTestId('viewer-session').textContent).toBe('other-row');
  expect(document.querySelector<HTMLElement>('[data-session-return-scroll="list"]')!.scrollTop).toBe(123);
  expect(fetchSessionOverview).not.toHaveBeenCalled();
  expect(fetchSessionOverviewCategories).not.toHaveBeenCalled();
});

it('keeps analysis-only filters and reads out of the calendar after switching from an analysis list', async () => {
  mount(maintenance);
  fireEvent.click(screen.getByRole('button', { name: '세션 요약' }));
  await waitFor(() => expect(fetchSessionOverviewCategories).toHaveBeenCalled());
  fireEvent.click(screen.getByRole('button', { name: '플랜 파일' }));
  fireEvent.click(screen.getByRole('button', { name: '비용' }));
  await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledWith(expect.objectContaining({ analysis: true, plan: 'file', sort: 'cost' }), expect.any(AbortSignal)));
  vi.mocked(fetchSessionOverview).mockClear();
  vi.mocked(fetchSessionOverviewCount).mockClear();
  vi.mocked(fetchSessionOverviewCategories).mockClear();
  fireEvent.click(screen.getByRole('button', { name: '캘린더' }));
  await waitFor(() => expect(fetchSessionCalendar).toHaveBeenCalled());
  await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalled());
  for (const [params] of [...vi.mocked(fetchSessionCalendar).mock.calls, ...vi.mocked(fetchSessionOverview).mock.calls, ...vi.mocked(fetchSessionOverviewCount).mock.calls]) {
    for (const key of ['analysis', 'category', 'plan', 'sort', 'query', 'bucket']) expect(params).not.toHaveProperty(key);
  }
  expect(fetchSessionOverviewCategories).not.toHaveBeenCalled();
});
