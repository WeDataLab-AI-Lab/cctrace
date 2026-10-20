// @vitest-environment jsdom
import { mount, row, waitFor } from './page-test-fixture';
import { act, fireEvent, screen } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import { fetchSessionOverview, fetchSessionOverviewCategories, fetchSessionOverviewCount } from '@/lib/api';
import { fetchSessionGroups } from '@/lib/session-groups-api';
import type { SessionOverview } from '@/lib/types';

const maintenance = { token_rule_pending: false, step: '', session_analysis: { enabled: true } };

it('keeps basic overview and counts independent of analysis capability and a pending group read', async () => {
  let releaseGroup!: () => void;
  vi.mocked(fetchSessionGroups).mockImplementation(() => new Promise(resolve => {
    releaseGroup = () => resolve({ scope: 'filtered_total', grouping_version: 1, truncated: false, overview_total: 9,
      running: [], ended: [], counts: { running: 0, ended: 0, ended_groups: 0, running_by: {} } });
  }));
  try {
    mount(maintenance);
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalled());
    await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalled());
    expect(screen.getByTestId('viewer-session').textContent).toBe('working-row');
    expect(fetchSessionOverview).toHaveBeenCalledTimes(1);
    for (const [params] of [...vi.mocked(fetchSessionOverview).mock.calls, ...vi.mocked(fetchSessionOverviewCount).mock.calls]) {
      for (const key of ['analysis', 'category', 'plan', 'sort', 'query', 'bucket']) expect(params).not.toHaveProperty(key);
    }
    expect(fetchSessionOverviewCategories).not.toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: '플랜 파일' })).toBeNull();
  } finally {
    await act(async () => releaseGroup?.());
  }
});

it('loads analysis only on explicit selection and cancels it without contaminating the basic cache', async () => {
  let resolveAnalysis!: (rows: SessionOverview[]) => void;
  let analysisSignal: AbortSignal | undefined;
  vi.mocked(fetchSessionOverview).mockImplementation((params, signal) => params?.analysis
    ? new Promise(resolve => { resolveAnalysis = resolve; analysisSignal = signal; })
    : Promise.resolve([row('basic-row')]));
  const { client } = mount(maintenance);
  await waitFor(() => expect(screen.getByTestId('viewer-session').textContent).toBe('basic-row'));
  const instance = screen.getByTestId('viewer-instance').textContent;
  fireEvent.click(screen.getByRole('button', { name: '세션 요약' }));
  await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledWith(expect.objectContaining({ analysis: true }), expect.any(AbortSignal)));
  expect(screen.queryByRole('button', { name: /basic-ro/ })).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: '목록' }));
  expect(analysisSignal?.aborted).toBe(true);
  await act(async () => resolveAnalysis([row('late-analysis-row')]));
  expect(screen.queryByRole('button', { name: /late-ana/ })).toBeNull();
  expect(screen.getByTestId('viewer-instance').textContent).toBe(instance);
  const cached = client.getQueryData<{ pages: SessionOverview[][] }>(['session-overview', 7, '', 'interactive', '', '', true, 'all', '', '']);
  expect(cached?.pages[0]).toEqual([row('basic-row')]);
});

it('keeps analysis filters out of basic requests after switching back and refreshing', async () => {
  mount(maintenance);
  fireEvent.click(screen.getByRole('button', { name: '세션 요약' }));
  await waitFor(() => expect(fetchSessionOverviewCategories).toHaveBeenCalled());
  fireEvent.click(screen.getByRole('button', { name: '플랜 파일' }));
  fireEvent.click(screen.getByRole('button', { name: '비용' }));
  fireEvent.click(screen.getByRole('button', { name: /종료.*11/ }));
  await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledWith(expect.objectContaining({ analysis: true, plan: 'file', sort: 'cost', category: 'ended' }), expect.any(AbortSignal)));
  fireEvent.click(screen.getByRole('button', { name: '목록' }));
  await waitFor(() => expect(screen.getByRole('button', { name: '세션 목록 다시 읽기' })).toHaveProperty('disabled', false));
  vi.mocked(fetchSessionOverview).mockClear();
  vi.mocked(fetchSessionOverviewCount).mockClear();
  fireEvent.click(screen.getByRole('button', { name: '세션 목록 다시 읽기' }));
  await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalled());
  await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalled());
  for (const [params] of [...vi.mocked(fetchSessionOverview).mock.calls, ...vi.mocked(fetchSessionOverviewCount).mock.calls]) {
    for (const key of ['analysis', 'category', 'plan', 'sort', 'query', 'bucket']) expect(params).not.toHaveProperty(key);
  }
});
