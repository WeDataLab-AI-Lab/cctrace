// @vitest-environment jsdom
import { state, row, mount, waitFor, SessionsPage } from './page-test-fixture';
import { act, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { fetchMaintenanceStatus, fetchSessionCalendar, fetchSessionOverview, fetchSessionOverviewCategories, fetchSessionOverviewCount, fetchUsers } from '@/lib/api';
import { fetchCompleteSessionGroup, fetchSessionGroups, updateSessionGroupDismissals } from '@/lib/session-groups-api';
import { describe } from 'vitest';
import { QueryClientProvider } from '@tanstack/react-query';

describe('sessions scope and loading regression 2.2', () => {
it('uses server analysis filters and unpaged owner category totals when enabled', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    const { client } = mount();
    await userEvent.click(await screen.findByRole('button', { name: '세션 요약' }));
    await waitFor(() => expect(fetchSessionOverviewCategories).toHaveBeenCalledWith(expect.objectContaining({ source: 'interactive', assembled: true, classification: undefined })));
    await waitFor(() => expect(screen.getByRole('button', { name: /종료.*11/ })).toBeTruthy());
    expect(screen.getByRole('button', { name: /종료.*11/ })).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: /종료.*11/ }));
    await userEvent.click(screen.getByRole('button', { name: '플랜 파일' }));
    await userEvent.click(screen.getByRole('button', { name: '요청 수' }));
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledWith(expect.objectContaining({ analysis: true,
      category: 'ended', plan: 'file', sort: 'requests', offset: 0 }), expect.any(AbortSignal)));
    await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalledWith(expect.objectContaining({ analysis: true,
      category: 'ended', plan: 'file', sort: 'requests' }), expect.any(AbortSignal)));
    expect(client.getQueryData(['session-overview', 7, '', 'interactive', '', '', true, 'all', '', '', true, '', 'ended', 'file', 'requests'])).toBeTruthy();
    expect(fetchSessionOverviewCategories).toHaveBeenLastCalledWith(expect.not.objectContaining({ category: 'ended' }));
    expect(screen.getByText(/범주 개수는 현재 개인 분류/)).toBeTruthy();
    expect(screen.getByText(/세션 묶음은 현재 검색·플랜·범주·기간·프로젝트·계정 범위 전체/)).toBeTruthy();
    expect(screen.getByText(/전체 범위 9건/)).toBeTruthy();
    expect(screen.getByText(/분류별 개수를 표시하지 않습니다/)).toBeTruthy();
  });

it('keeps server sorted results and the selected conversation while analysis filters change', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    vi.mocked(fetchSessionOverview).mockImplementation(async (params) => params?.plan === 'file'
      ? [{ ...row('zero-metric'), event_count: 0, input_tokens: 0, output_tokens: 0, cost_usd: 0 }]
      : [row('working-row'), row('other-row')]);
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '세션 요약' }));
    await userEvent.click(await screen.findByRole('button', { name: /other-ro/ }));
    expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'other-row');
    await userEvent.click(screen.getByRole('button', { name: '플랜 파일' }));
    await waitFor(() => expect(screen.getByRole('button', { name: /zero-met/ })).toBeTruthy());
    expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'other-row');
    expect(screen.queryByRole('button', { name: /working-/ })).toBeNull();
  });

it('keeps an administrator on their own calendar when the server reports no access mode', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    state.isAdmin = true;
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '캘린더' }));
    await waitFor(() => expect(fetchSessionCalendar).toHaveBeenCalled());
    expect((screen.getByRole('combobox', { name: '캘린더 사용자' }) as HTMLSelectElement).disabled).toBe(true);
    expect(fetchUsers).not.toHaveBeenCalled();
    expect(vi.mocked(fetchSessionCalendar).mock.calls.every(([params]) => !('userId' in params) && !('profileEmail' in params))).toBe(true);
  });

it('uses the headless source for list, count, groups, and category totals', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '세션 요약' }));
    await waitFor(() => expect(fetchSessionOverviewCategories).toHaveBeenCalledWith(expect.objectContaining({ source: 'interactive' })));

    await act(async () => { state.onSourceSelect?.('headless'); });

    await waitFor(() => expect(fetchSessionOverview).toHaveBeenLastCalledWith(expect.objectContaining({ source: 'headless' }), expect.any(AbortSignal)));
    await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalledWith(expect.objectContaining({ source: 'headless' }), expect.any(AbortSignal)));
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenLastCalledWith(expect.objectContaining({ source: 'headless' })));
    await waitFor(() => expect(fetchSessionOverviewCategories).toHaveBeenLastCalledWith(expect.objectContaining({ source: 'headless' })));
    expect(screen.queryByText('Headless 소스의 범주 개수는 이 화면에서 제공되지 않습니다.')).toBeNull();
  });

it('scopes server groups with the same source, project, agent, assembled and classification filters', async () => {
    mount();
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledWith(expect.objectContaining({ source: 'interactive', projectHashes: undefined, agent: '', assembled: true, classification: undefined, loginEmail: undefined, includeAnalysis: false })));
    await userEvent.click(await screen.findByRole('tab', { name: '작업 중3' }));
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledWith(expect.objectContaining({ source: 'interactive', projectHashes: undefined, agent: '', assembled: true, classification: 'working', loginEmail: undefined })));
  });

it('preserves the selected assembled conversation while opening record inspection', async () => {
    vi.mocked(fetchSessionOverview).mockImplementation(async () => [row('working-row'), row('other-row')]);
    const { client, view } = mount();
    await userEvent.click(await screen.findByRole('button', { name: /other-ro/ }));
    const instance = screen.getByTestId('viewer-instance').textContent;
    expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'other-row');
    expect(state.onModeChange).toBeUndefined();
    await userEvent.click(screen.getByRole('button', { name: /^(기록검사|상세 분석)$/ }));
    view.rerender(<QueryClientProvider client={client}><SessionsPage /></QueryClientProvider>);
    await waitFor(() => expect(screen.getByTestId('viewer-tab').textContent).toBe('analysis'));
    expect(screen.getByTestId('viewer-instance').textContent).toBe(instance);
    expect(state.mode).toBe('assembled');
    expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'other-row');
    expect(vi.mocked(fetchSessionOverview).mock.calls.every(([params]) => params?.assembled === true)).toBe(true);
  });

it('sends the group key for individual dismissal and restoration without reading every member', async () => {
    const member = { session_id: 'working-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false };
    const group = { key: 'old-group', ended_at: member.ended_at, total: 1, items: [member] };
    vi.mocked(fetchSessionGroups).mockResolvedValueOnce({
      scope: 'filtered_total', grouping_version: 1, truncated: false, overview_total: 1,
      running: [], ended: [group], counts: { running: 0, ended: 1, ended_groups: 1, running_by: {} },
    }).mockResolvedValue({
      scope: 'filtered_total', grouping_version: 1, truncated: false, overview_total: 1,
      running: [], ended: [{ ...group, items: [{ ...member, dismissed: true }] }],
      counts: { running: 0, ended: 1, ended_groups: 1, running_by: {} },
    });
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '떼기' }));
    await waitFor(() => expect(updateSessionGroupDismissals).toHaveBeenCalledWith(expect.anything(), 1,
      [{ session_id: member.session_id, ended_at: member.ended_at }], true, 'old-group'));
    expect(fetchCompleteSessionGroup).not.toHaveBeenCalled();
    await userEvent.click(await screen.findByRole('button', { name: '묶음 복원' }));
    await waitFor(() => expect(updateSessionGroupDismissals).toHaveBeenCalledWith(expect.anything(), 1,
      [{ session_id: member.session_id, ended_at: member.ended_at }], false, 'old-group'));
    expect(fetchCompleteSessionGroup).not.toHaveBeenCalled();
  });

it('clears a previous group write error after switching the classification scope', async () => {
    const member = { session_id: 'working-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false };
    vi.mocked(fetchSessionGroups).mockResolvedValue({
      scope: 'filtered_total', grouping_version: 1, truncated: false, overview_total: 1,
      running: [], ended: [{ key: 'g', ended_at: member.ended_at, total: 1, items: [member] }],
      counts: { running: 0, ended: 1, ended_groups: 1, running_by: {} },
    });
    vi.mocked(updateSessionGroupDismissals).mockRejectedValueOnce(new Error('write failed'));
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '떼기' }));
    expect(await screen.findByRole('alert')).toBeTruthy();
    await userEvent.click(screen.getByRole('tab', { name: '작업 중3' }));
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledWith(expect.objectContaining({ classification: 'working' })));
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull());
  });

it('omits failed counts rather than painting previous totals or zero buckets', async () => {
    vi.mocked(fetchSessionOverviewCount).mockRejectedValue(new Error('count unavailable'));
    mount();
    await waitFor(() => expect(screen.getByRole('tab', { name: '전체' })).toBeTruthy());
    expect(screen.queryByRole('tab', { name: /전체 0/ })).toBeNull();
    expect(screen.queryByRole('tab', { name: /작업 중 0/ })).toBeNull();
  });
});
