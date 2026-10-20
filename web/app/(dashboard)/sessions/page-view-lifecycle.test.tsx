// @vitest-environment jsdom
import { mount, row, waitFor, state, SessionsPage } from './page-test-fixture';
import { act, fireEvent, screen } from '@testing-library/react';
import { createElement } from 'react';
import { QueryClientProvider } from '@tanstack/react-query';
import { beforeEach, expect, it, vi } from 'vitest';
import { fetchProjects, fetchSessionOverview, fetchSessionOverviewCount } from '@/lib/api';
import { POLL_NORMAL } from '@/lib/query-config';
import { saveSessionsReturnState } from '@/lib/sessions-return-state';

const renders = vi.hoisted(() => [] as string[][]);
vi.mock('@/components/sessions/session-list', async importOriginal => {
  const { SessionList } = await importOriginal<typeof import('@/components/sessions/session-list')>();
  return { SessionList: (props: Parameters<typeof SessionList>[0]) => {
    renders.push(props.sessions.map(session => session.session_id));
    return createElement(SessionList, props);
  } };
});
const maintenance = { token_rule_pending: false, step: '', session_analysis: { enabled: true } };
beforeEach(() => { renders.length = 0; });

it('never renders rows remembered from the other view while its scope changes', async () => {
  vi.mocked(fetchSessionOverview).mockImplementation(async params => [row('shared-row'), row(params?.analysis ? 'analysis-only' : 'basic-only')]);
  mount(maintenance);
  await screen.findByText('basic-on');
  fireEvent.click(screen.getByRole('button', { name: '세션 요약' }));
  await screen.findByText('analysis');
  renders.length = 0;
  fireEvent.click(screen.getByRole('button', { name: '목록' }));
  await screen.findByText('basic-on');
  expect(renders.length).toBeGreaterThan(0);
  expect(renders.flat()).not.toContain('analysis-only');
});

it('cancels a pending analysis count and still refreshes basic totals on its independent queue', async () => {
  let resolveCount!: (value: { count: number; unattributed: number }) => void;
  let analysisSignal: AbortSignal | undefined;
  vi.mocked(fetchSessionOverviewCount).mockImplementation((params, signal) => params?.analysis
    ? new Promise(resolve => { resolveCount = resolve; analysisSignal = signal; })
    : Promise.resolve({ count: 9, unattributed: 0 }));
  try {
    mount(maintenance);
    await screen.findByText('2 / 9개');
    fireEvent.click(screen.getByRole('button', { name: '세션 요약' }));
    await waitFor(() => expect(analysisSignal).toBeInstanceOf(AbortSignal));
    fireEvent.click(screen.getByRole('button', { name: '목록' }));
    expect(analysisSignal?.aborted).toBe(true);
    await waitFor(() => expect(screen.getByRole('button', { name: '세션 목록 다시 읽기' })).toHaveProperty('disabled', false));
    vi.mocked(fetchSessionOverviewCount).mockClear();
    fireEvent.click(screen.getByRole('button', { name: '세션 목록 다시 읽기' }));
    await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalledWith(expect.not.objectContaining({ analysis: true }), expect.any(AbortSignal)));
  } finally {
    await act(async () => resolveCount?.({ count: 999, unattributed: 0 }));
  }
  expect(screen.queryByText(/999개/)).toBeNull();
});

it('ignores restored analysis filters in basic view and keeps polling, selection, scroll, and frozen order', async () => {
  saveSessionsReturnState(7, { scopeKey: JSON.stringify(['owner', '', '']), sessionsView: 'list',
    analysisCategory: 'ended', analysisPlan: 'file', analysisSort: 'cost' });
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  const { view } = mount({ ...maintenance, session_records: { enabled: true, prompt_version: 1, state: 'running', paused_reason: '' } });
  await waitFor(() => expect(screen.getByTestId('viewer-session').textContent).toBe('working-row'));
  const scroller = document.querySelector<HTMLElement>('[data-session-return-scroll="list"]')!;
  scroller.scrollTop = 75;
  const previousReads = vi.mocked(fetchSessionOverview).mock.calls.length;
  vi.mocked(fetchSessionOverview).mockResolvedValue([row('new-head'), row('other-row'), row('working-row')]);
  try {
    await act(async () => { await vi.advanceTimersByTimeAsync(POLL_NORMAL); });
    await waitFor(() => expect(screen.getByRole('button', { name: '새 세션 1개' })).toBeTruthy());
    expect(fetchSessionOverview).toHaveBeenCalledTimes(previousReads + 1);
    expect(screen.getByTestId('viewer-session').textContent).toBe('working-row');
    expect(scroller.scrollTop).toBe(75);
    expect(renders.at(-1)).toEqual(['working-row', 'other-row']);
    for (const [params] of vi.mocked(fetchSessionOverview).mock.calls) {
      for (const key of ['analysis', 'category', 'plan', 'sort', 'query', 'bucket']) expect(params).not.toHaveProperty(key);
    }
  } finally {
    view.unmount();
    vi.useRealTimers();
  }
  expect(state.viewerRenders.at(-1)).toBe('working-row');
});

it('preserves the basic frozen order and pending head across analysis round trips until the badge is clicked', async () => {
  let basicRows = [row('working-row'), row('other-row')];
  vi.mocked(fetchSessionOverview).mockImplementation(async params => params?.analysis ? [row('analysis-only')] : basicRows);
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  mount(maintenance);
  await waitFor(() => expect(renders.at(-1)).toEqual(['working-row', 'other-row']));
  const scroller = document.querySelector<HTMLElement>('[data-session-return-scroll="list"]')!;
  scroller.scrollTop = 75;
  scroller.scrollTo = vi.fn();
  basicRows = [row('new-head'), row('other-row'), row('working-row')];
  await act(async () => { await vi.advanceTimersByTimeAsync(POLL_NORMAL); });
  await screen.findByRole('button', { name: '새 세션 1개' });

  for (let round = 0; round < 2; round++) {
    fireEvent.click(screen.getByRole('button', { name: '세션 요약' }));
    await waitFor(() => expect(renders.at(-1)).toEqual(['analysis-only']));
    if (round === 0) {
      fireEvent.click(screen.getByRole('button', { name: '플랜 파일' }));
      fireEvent.click(screen.getByRole('button', { name: '비용' }));
      await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledWith(
        expect.objectContaining({ analysis: true, plan: 'file', sort: 'cost' }), expect.any(AbortSignal)));
    }
    renders.length = 0;
    fireEvent.click(screen.getByRole('button', { name: '목록' }));
    await waitFor(() => expect(renders.at(-1)).toEqual(['working-row', 'other-row']));
    expect(renders.every(ids => ids.join(',') === 'working-row,other-row')).toBe(true);
    expect(screen.getByRole('button', { name: '새 세션 1개' })).toBeTruthy();
    expect(screen.getByTestId('viewer-session').textContent).toBe('working-row');
    expect(scroller.scrollTop).toBe(75);
  }

  fireEvent.click(screen.getByRole('button', { name: '새 세션 1개' }));
  expect(renders.at(-1)).toEqual(['new-head', 'other-row', 'working-row']);
  expect(screen.queryByRole('button', { name: '새 세션 1개' })).toBeNull();
  expect(scroller.scrollTo).toHaveBeenCalledWith({ top: 0 });
  expect(screen.getByTestId('viewer-session').textContent).toBe('working-row');
});

it.each(['account', 'project', 'source', 'classification'])('resets the hidden basic order when the %s scope changes in analysis view', async scope => {
  state.isAdmin = true;
  if (scope === 'project') vi.mocked(fetchProjects).mockResolvedValue([{
    project_hash: 'project-hash', project_name: 'Project A', git_remote_url: '', repository_id: 'github.com/example/repo',
    repository_name: 'repo', repo_subpath: '', updated_at: '2026-10-01T00:00:00Z', last_session_at: '2026-10-01T01:00:00Z',
  }]);
  let changed = false;
  vi.mocked(fetchSessionOverview).mockImplementation(async params => params?.analysis ? [row('analysis-only')]
    : changed ? [row('other-row'), row('working-row')] : [row('working-row'), row('other-row')]);
  const { client, view } = mount(maintenance);
  await waitFor(() => expect(renders.at(-1)).toEqual(['working-row', 'other-row']));
  fireEvent.click(screen.getByRole('button', { name: '세션 요약' }));
  await waitFor(() => expect(renders.at(-1)).toEqual(['analysis-only']));
  await screen.findByRole('tab', { name: '작업 중3' });
  changed = true;
  await act(async () => {
    if (scope === 'source') state.onSourceSelect?.('headless');
    else if (scope === 'project') fireEvent.click(screen.getByTestId('select-first-project'));
    else if (scope === 'classification') fireEvent.click(screen.getByRole('tab', { name: '작업 중3' }));
    else {
      state.account = 'another-account@example.test';
      view.rerender(createElement(QueryClientProvider, { client }, createElement(SessionsPage)));
    }
  });
  fireEvent.click(screen.getByRole('button', { name: '목록' }));
  await waitFor(() => expect(renders.at(-1)).toEqual(['other-row', 'working-row']));
  expect(screen.queryByRole('button', { name: /새 세션/ })).toBeNull();
});


it('keeps the hidden basic order and selection when opening inspection without changing list scope', async () => {
  vi.mocked(fetchSessionOverview).mockImplementation(async params => params?.analysis
    ? [row('analysis-only')] : [row('working-row'), row('other-row')]);
  const { client, view } = mount(maintenance);
  await waitFor(() => expect(renders.at(-1)).toEqual(['working-row', 'other-row']));
  fireEvent.click(screen.getByRole('button', { name: '세션 요약' }));
  await waitFor(() => expect(renders.at(-1)).toEqual(['analysis-only']));
  expect(state.onModeChange).toBeUndefined();
  fireEvent.click(screen.getByRole('button', { name: /^(기록검사|상세 분석)$/ }));
  view.rerender(createElement(QueryClientProvider, { client }, createElement(SessionsPage)));
  await waitFor(() => expect(screen.getByTestId('viewer-tab').textContent).toBe('analysis'));
  fireEvent.click(screen.getByRole('button', { name: '목록' }));
  await waitFor(() => expect(renders.at(-1)).toEqual(['working-row', 'other-row']));
  expect(screen.getByTestId('viewer-session').textContent).toBe('working-row');
  expect(state.mode).toBe('assembled');
  expect(screen.queryByRole('button', { name: /새 세션/ })).toBeNull();
});
