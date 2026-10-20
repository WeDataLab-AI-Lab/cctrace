// @vitest-environment jsdom
import { state, buckets, mount, waitFor, SessionsPage } from './page-test-fixture';
import { QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, screen } from '@testing-library/react';
import { beforeEach, expect, it, vi } from 'vitest';
import { fetchProjects, fetchSessionOverviewCount } from '@/lib/api';
import type { SessionOverviewCount } from '@/lib/api';

beforeEach(() => {
  vi.mocked(fetchSessionOverviewCount).mockImplementation(async (params) => ({
    count: params?.classification ? 3 : 9, unattributed: 0,
    ...(params?.includeClassificationCounts === false ? {} : { classification_counts: buckets }),
  }));
});
const summaryCalls = () => vi.mocked(fetchSessionOverviewCount).mock.calls.filter(([params]) => params?.includeClassificationCounts !== false && !params?.analysis);

it('reads current and all-source numbers without hidden classification work, then loads it on filter expansion', async () => {
  mount(undefined, false, false, false);
  await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalledTimes(2));
  expect(summaryCalls()).toHaveLength(0);
  expect(screen.getByTestId('viewer-session').textContent).toBe('working-row');
  const panel = document.getElementById('sessions-list-panel');
  expect(document.getElementById(panel?.getAttribute('aria-labelledby') ?? '')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: '추가 필터 펼치기' }));
  await waitFor(() => expect(screen.getByRole('tab', { name: '작업 중3' })).toBeTruthy());
  expect(summaryCalls()).toHaveLength(1);
  expect(summaryCalls()[0][0]).toMatchObject({ source: 'interactive', assembled: true });
  expect(summaryCalls()[0][0]?.classification).toBeUndefined();
  expect(screen.queryByText('분류와 메모는 세션 주인에게만 제공됩니다')).toBeNull();
});

it('keeps numeric totals available while classification is pending or fails', async () => {
  let rejectSummary!: (reason: Error) => void;
  vi.mocked(fetchSessionOverviewCount).mockImplementation((params) => params?.includeClassificationCounts === false
    ? Promise.resolve({ count: 9, unattributed: 0 })
    : new Promise((_, reject) => { rejectSummary = reject; }));
  const { client } = mount(undefined, false, false, false);
  await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalledTimes(2));
  await waitFor(() => expect(screen.getByText('2 / 9개')).toBeTruthy());
  fireEvent.click(screen.getByRole('button', { name: '추가 필터 펼치기' }));
  await waitFor(() => expect(summaryCalls()).toHaveLength(1));
  expect(screen.getByRole('tab', { name: '작업 중' })).toBeTruthy();
  expect(screen.queryByText('분류와 메모는 세션 주인에게만 제공됩니다')).toBeNull();
  await act(async () => rejectSummary(new Error('summary unavailable')));
  expect(await screen.findByText('개인 분류 개수를 읽지 못했습니다.')).toBeTruthy();
  expect(client.getQueryCache().findAll({ queryKey: ['session-overview-count'] }).filter(q => q.state.data && (q.state.data as SessionOverviewCount).count === 9)).toHaveLength(2);
  expect(screen.queryByText('세션 개수를 읽지 못했습니다.')).toBeNull();
  expect(screen.getByText('2 / 9개')).toBeTruthy();
  expect(screen.getByTestId('viewer-session').textContent).toBe('working-row');
});

it('clears an inactive unfiltered count error after filtered numbers and the summary succeed', async () => {
  vi.mocked(fetchSessionOverviewCount).mockImplementation(async (params) => {
    if (params?.includeClassificationCounts !== false) return { count: 9, unattributed: 0, classification_counts: buckets };
    if (!params.classification && params.source === 'interactive') throw new Error('initial numeric count failed');
    return { count: params.classification ? params.source === 'all' ? 5 : 3 : 9, unattributed: 0 };
  });
  mount();
  expect(await screen.findByText('세션 개수를 읽지 못했습니다.')).toBeTruthy();
  fireEvent.click(await screen.findByRole('tab', { name: '작업 중3' }));
  await waitFor(() => expect(screen.getByText('1 / 3개')).toBeTruthy());
  await waitFor(() => expect(screen.getByTestId('hidden-session-count').textContent).toBe('2 hidden'));
  expect(screen.getByRole('tab', { name: '전체9' })).toBeTruthy();
  expect(screen.queryByText('개인 분류 개수를 읽지 못했습니다.')).toBeNull();
  expect(screen.queryByText('세션 개수를 읽지 못했습니다.')).toBeNull();
});

it.each(['interactive', 'all'] as const)('keeps an active %s count failure visible after classification selection', async (failedSource) => {
  vi.mocked(fetchSessionOverviewCount).mockImplementation(async (params) => {
    if (params?.includeClassificationCounts !== false) return { count: 9, unattributed: 0, classification_counts: buckets };
    if (params.classification && params.source === failedSource) throw new Error('active numeric count failed');
    return { count: params.classification ? params.source === 'all' ? 5 : 3 : 9, unattributed: 0 };
  });
  mount();
  fireEvent.click(await screen.findByRole('tab', { name: '작업 중3' }));
  expect(await screen.findByText('세션 개수를 읽지 못했습니다.')).toBeTruthy();
  expect(screen.getByTestId('hidden-session-count').textContent).toBe('unknown');
  expect(screen.queryByText('개인 분류 개수를 읽지 못했습니다.')).toBeNull();
});

it('keeps the active all-classification count error visible in analysis view', async () => {
  vi.mocked(fetchSessionOverviewCount).mockImplementation(async (params) => {
    if (params?.analysis && !params.classification && params.source === 'interactive') throw new Error('analysis denominator failed');
    return { count: params?.classification ? params.source === 'all' ? 5 : 3 : 9, unattributed: 0, classification_counts: buckets };
  });
  mount({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
  fireEvent.click(await screen.findByRole('tab', { name: '작업 중3' }));
  await waitFor(() => expect(screen.getByText('1 / 3개')).toBeTruthy());
  fireEvent.click(screen.getByRole('button', { name: '세션 요약' }));
  expect(await screen.findByText('세션 판정 개수를 읽지 못했습니다.')).toBeTruthy();
  await waitFor(() => expect(screen.getByTestId('hidden-session-count').textContent).toBe('2 hidden'));
  expect(screen.getByText('1 / 3개')).toBeTruthy();
});

it('preserves filtered count ownership parameters and the all-classification denominator', async () => {
  state.isAdmin = true;
  state.account = 'account@example.test';
  state.agent = 'codex';
  state.since = '2026-10-01T00:00:00Z';
  state.until = '2026-10-02T00:00:00Z';
  mount();
  fireEvent.click(await screen.findByRole('tab', { name: '작업 중3' }));
  await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalledWith(expect.objectContaining({
    includeClassificationCounts: false, classification: 'working', loginEmail: state.account, agent: 'codex', since: state.since, until: state.until,
  }), expect.any(AbortSignal)));
  expect(screen.getByRole('tab', { name: '전체9' })).toBeTruthy();
  expect(summaryCalls()).toHaveLength(1);
});

it('isolates user scopes and refreshes summaries through the existing count invalidation prefix', async () => {
  const { client, view } = mount();
  await waitFor(() => expect(summaryCalls()).toHaveLength(1));
  await act(async () => { await client.invalidateQueries({ queryKey: ['session-overview-count'] }); });
  await waitFor(() => expect(summaryCalls()).toHaveLength(2));
  state.userId = 8;
  view.rerender(<QueryClientProvider client={client}><SessionsPage /></QueryClientProvider>);
  await waitFor(() => expect(summaryCalls()).toHaveLength(3));
  const summaryKeys = client.getQueryCache().findAll({ queryKey: ['session-overview-count', 'classification-summary'] }).map(q => q.queryKey);
  expect(summaryKeys.some(key => key.includes(7))).toBe(true);
  expect(summaryKeys.some(key => key.includes(8))).toBe(true);
});

it('cancels a pending summary when the panel closes and ignores its late result', async () => {
  let signal: AbortSignal | undefined;
  let resolveSummary!: (value: SessionOverviewCount) => void;
  vi.mocked(fetchSessionOverviewCount).mockImplementation((params, abortSignal) => params?.includeClassificationCounts === false
    ? Promise.resolve({ count: 9, unattributed: 0 })
    : new Promise(resolve => { signal = abortSignal; resolveSummary = resolve; }));
  const { client } = mount();
  await waitFor(() => expect(summaryCalls()).toHaveLength(1));
  fireEvent.click(screen.getByRole('button', { name: '추가 필터 접기' }));
  expect(signal?.aborted).toBe(true);
  await act(async () => resolveSummary({ count: 999, unattributed: 0, classification_counts: { ...buckets, working: 999 } }));
  expect(client.getQueryCache().findAll({ queryKey: ['session-overview-count', 'classification-summary'] })[0]?.state.data).toBeUndefined();
});

it('refreshes an invalidated inactive summary only when its filter panel reopens', async () => {
  const { client } = mount();
  await waitFor(() => expect(summaryCalls()).toHaveLength(1));
  fireEvent.click(screen.getByRole('button', { name: '추가 필터 접기' }));
  await act(async () => { await client.invalidateQueries({ queryKey: ['session-overview-count'] }); });
  expect(summaryCalls()).toHaveLength(1);
  fireEvent.click(screen.getByRole('button', { name: '추가 필터 펼치기' }));
  await waitFor(() => expect(summaryCalls()).toHaveLength(2));
});

it('keeps lazy summary population aligned through filters while conversation assembly stays fixed', async () => {
  state.isAdmin = true;
  vi.mocked(fetchProjects).mockResolvedValue([{ project_hash: 'project-hash', project_name: 'Project', git_remote_url: '',
    repository_id: '', repository_name: '', repo_subpath: '', updated_at: '2026-10-01T00:00:00Z', last_session_at: '2026-10-01T00:00:00Z' }]);
  const { client, view } = mount();
  await waitFor(() => expect(summaryCalls()).toHaveLength(1));
  state.account = 'changed@example.test';
  state.agent = 'codex';
  state.since = '2026-10-01T00:00:00Z';
  state.until = '2026-10-02T00:00:00Z';
  view.rerender(<QueryClientProvider client={client}><SessionsPage /></QueryClientProvider>);
  await waitFor(() => expect(summaryCalls().at(-1)?.[0]).toMatchObject({ loginEmail: state.account, agent: state.agent, since: state.since, until: state.until }));
  fireEvent.click(screen.getByTestId('select-first-project'));
  await waitFor(() => expect(summaryCalls().at(-1)?.[0]?.projectHashes).toEqual(['project-hash']));
  act(() => state.onSourceSelect?.('headless'));
  await waitFor(() => expect(summaryCalls().at(-1)?.[0]?.source).toBe('headless'));
  expect(state.onModeChange).toBeUndefined();
  await waitFor(() => expect(summaryCalls().at(-1)?.[0]).toMatchObject({ assembled: true, source: 'headless', projectHashes: ['project-hash'], loginEmail: state.account, agent: state.agent, since: state.since, until: state.until }));
  for (const [params] of summaryCalls()) {
    expect(params?.classification).toBeUndefined();
    expect(params?.assembled).toBe(true);
  }
});

it('leaves analysis counts full and cancels the basic summary during the view switch', async () => {
  let summarySignal: AbortSignal | undefined;
  let resolveSummary!: (value: SessionOverviewCount) => void;
  vi.mocked(fetchSessionOverviewCount).mockImplementation((params, signal) => params?.includeClassificationCounts === true
    ? new Promise(resolve => { summarySignal = signal; resolveSummary = resolve; })
    : Promise.resolve({ count: 9, unattributed: 0, ...(params?.analysis ? { classification_counts: buckets } : {}) }));
  mount({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
  await waitFor(() => expect(summaryCalls()).toHaveLength(1));
  fireEvent.click(screen.getByRole('button', { name: '세션 요약' }));
  expect(summarySignal?.aborted).toBe(true);
  await act(async () => resolveSummary({ count: 999, unattributed: 0, classification_counts: buckets }));
  await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalledWith(expect.objectContaining({ analysis: true }), expect.any(AbortSignal)));
  for (const [params] of vi.mocked(fetchSessionOverviewCount).mock.calls.filter(([params]) => params?.analysis)) expect(params).not.toHaveProperty('includeClassificationCounts');
  expect(screen.queryByRole('tab', { name: '전체999' })).toBeNull();
});
