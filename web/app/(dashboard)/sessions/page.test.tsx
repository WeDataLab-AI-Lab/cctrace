// @vitest-environment jsdom
import { state, row, buckets, mount, waitFor, SessionsPage } from './page-test-fixture';
import { createElement } from 'react';
import { QueryClientProvider } from '@tanstack/react-query';
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { fetchMaintenanceStatus, fetchSessionCalendar, fetchSessionOverview, fetchSessionOverviewCategories, fetchSessionOverviewCount } from '@/lib/api';
import { fetchCompleteSessionGroup, fetchSessionGroups, updateSessionGroupDismissals } from '@/lib/session-groups-api';
import { readSessionsReturnState } from '@/lib/sessions-return-state';
import { describe } from 'vitest';

describe('sessions scope and loading regression 1.1', () => {
it('restores selected session, search, frozen order and cached pages after explicit detail navigation', async () => {
    const { client, view } = mount();
    await userEvent.click(await screen.findByRole('button', { name: /other-ro/ }));
    await userEvent.type(screen.getByRole('textbox', { name: '세션 검색' }), 'row');
    act(() => state.onOpenAnalysis?.());
    const snapshot = readSessionsReturnState(7);
    expect(snapshot?.selectedId).toBe('other-row');
    expect(snapshot?.search).toBe('row');
    expect(snapshot?.committedIds).toEqual(['working-row', 'other-row']);
    const calls = vi.mocked(fetchSessionOverview).mock.calls.length;
    client.setDefaultOptions({ queries: { retry: false, staleTime: 60_000 } });
    view.unmount();
    render(createElement(QueryClientProvider, { client }, createElement(SessionsPage)));
    await userEvent.click(screen.getByRole('button', { name: '추가 필터 펼치기' }));
    expect(screen.getByRole('textbox', { name: '세션 검색' })).toHaveProperty('value', 'row');
    expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'other-row');
    expect(vi.mocked(fetchSessionOverview).mock.calls.length).toBe(calls);
  });

it('rereads the current list, totals and groups while preserving the selected viewer', async () => {
    vi.mocked(fetchSessionOverview).mockResolvedValue([row('refresh-row')]);
    mount();
    await waitFor(() => expect(screen.getByTestId('viewer-session').textContent).toBe('refresh-row'));
    const instance = screen.getByTestId('viewer-instance').textContent;
    const readTime = screen.getByLabelText('세션 목록 읽은 시각');
    expect(Number.isFinite(Date.parse(readTime.getAttribute('dateTime') ?? ''))).toBe(true);
    await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalled());
    const countReads = vi.mocked(fetchSessionOverviewCount).mock.calls.length;
    await userEvent.click(screen.getByRole('button', { name: '세션 목록 다시 읽기' }));
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(vi.mocked(fetchSessionOverviewCount).mock.calls.length).toBeGreaterThan(countReads));
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledTimes(2));
    expect(screen.getByTestId('viewer-session').textContent).toBe('refresh-row');
    expect(screen.getByTestId('viewer-instance').textContent).toBe(instance);
  });

it('does not mislabel personal buckets as counts for a narrowed classification', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    vi.mocked(fetchSessionOverviewCount).mockImplementation(async (params) => ({
      count: params?.plan === 'file' ? 1 : params?.classification ? 3 : 9,
      unattributed: 0, classification_counts: buckets,
    }));
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '세션 요약' }));
    await userEvent.click(await screen.findByRole('tab', { name: '작업 중3' }));
    await userEvent.click(screen.getByRole('button', { name: '플랜 파일' }));
    await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalledWith(expect.objectContaining({
      analysis: true, classification: 'working', plan: 'file',
    }), expect.any(AbortSignal)));
    await waitFor(() => expect(fetchSessionOverviewCategories).toHaveBeenCalledWith(expect.objectContaining({ classification: 'working' })));
    expect(screen.getByRole('tab', { name: '작업 중' })).toBeTruthy();
    expect(screen.queryByRole('tab', { name: '작업 중3' })).toBeNull();
    expect(screen.getByRole('tab', { name: '전체1' })).toBeTruthy();
  });

it('switches to the real calendar without remounting the viewer and opens a session outside the loaded list', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    state.account = 'login@example.test';
    mount();
    await userEvent.click(await screen.findByRole('button', { name: /other-ro/ }));
    const instance = screen.getByTestId('viewer-instance').textContent;
    await userEvent.click(screen.getByRole('button', { name: '캘린더' }));
    await userEvent.selectOptions(screen.getByRole('combobox', { name: '캘린더 시간대' }), 'Asia/Seoul');
    await waitFor(() => expect(fetchSessionCalendar).toHaveBeenLastCalledWith(expect.objectContaining({
      source: 'interactive', assembled: true, agent: undefined, projectHashes: undefined, classification: undefined })));
    const params = vi.mocked(fetchSessionCalendar).mock.lastCall![0];
    expect(params).not.toHaveProperty('userId');
    expect(params).not.toHaveProperty('profileEmail');
    expect(params.loginEmail).toBeUndefined();
    expect(Date.parse(params.to) - Date.parse(params.from)).toBe(7 * 24 * 3_600_000);
    expect(screen.getByText(/상단 계정 필터\(login@example.test\)는 적용하지 않습니다/)).toBeTruthy();
    expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'other-row');
    expect(screen.getByTestId('viewer-instance').textContent).toBe(instance);
    await waitFor(() => expect(document.querySelector('[data-session-id="building-row"]')).toBeTruthy());
    await userEvent.click(document.querySelector('[data-session-id="building-row"]') as HTMLElement);
    expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'building-row');
    await userEvent.click(screen.getByRole('button', { name: '목록' }));
    expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'building-row');
    expect(screen.getByTestId('viewer-instance').textContent).toBe(instance);
  });

it('moves the visible week with the clock while the calendar is open', async () => {
    vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] });
    vi.setSystemTime(new Date('2026-10-11T14:59:30Z'));
    try {
      vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
      mount();
      await userEvent.click(await screen.findByRole('button', { name: '캘린더' }));
      await userEvent.selectOptions(screen.getByRole('combobox', { name: '캘린더 시간대' }), 'Asia/Seoul');
      await waitFor(() => expect(fetchSessionCalendar).toHaveBeenLastCalledWith(expect.objectContaining({ from: '2026-10-04T15:00:00.000Z' })));
      await act(async () => { vi.advanceTimersByTime(60_000); });
      await waitFor(() => expect(fetchSessionCalendar).toHaveBeenLastCalledWith(expect.objectContaining({ from: '2026-10-11T15:00:00.000Z' })));
    } finally {
      vi.useRealTimers();
    }
  });

it('does not render a former user conversation during the first scope-change render', async () => {
    const { client, view } = mount();
    await userEvent.click(await screen.findByRole('button', { name: /other-ro/ }));
    expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'other-row');
    state.viewerRenders = [];
    state.userId = 8;
    view.rerender(createElement(QueryClientProvider, { client }, createElement(SessionsPage)));
    expect(state.viewerRenders).not.toContain('other-row');
    expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'none');
  });

it('queries server groups in the selected account for an admin', async () => {
    state.isAdmin = true;
    state.account = 'member@example.test';
    mount();
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledWith(expect.objectContaining({
      source: 'interactive', projectHashes: undefined, agent: '', assembled: true,
      classification: undefined, loginEmail: 'member@example.test',
    })));
    expect(screen.queryByText(/계정 필터에서 세션 묶음은 아직 제공되지 않습니다/)).toBeNull();
  });

it('passes the selected account to the analysis viewer', async () => {
    state.account = 'member@example.test';
    mount();
    await screen.findByTestId('viewer-session');
    expect(state.analysisLoginEmail).toBe('member@example.test');
  });

it('shows server membership counts without inventing rows outside the loaded list', async () => {
    const member = { session_id: 'working-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false };
    vi.mocked(fetchSessionGroups).mockResolvedValue({
      scope: 'filtered_total', grouping_version: 1, truncated: true, overview_total: 501,
      running: [], ended: [{ key: 'large-group', ended_at: member.ended_at, total: 501,
        items: [member, { ...member, session_id: 'not-loaded' }] }],
      counts: { running: 0, ended: 501, ended_groups: 1, running_by: {} },
    });
    mount();
    expect(await screen.findByText(/함께 끝난 501건/)).toBeTruthy();
    expect(screen.getByText(/현재 목록 1건 표시 · 서버 반환 2건/)).toBeTruthy();
    expect(screen.queryByText('not-loaded')).toBeNull();
    expect(screen.getByText(/일부 묶음 미표시/)).toBeTruthy();
  });

it('refuses group-wide writes when full membership cannot be confirmed', async () => {
    vi.mocked(fetchSessionGroups).mockResolvedValue({
      scope: 'filtered_total', grouping_version: 1, truncated: true, overview_total: 102,
      running: [], ended: [{ key: 'group-one', ended_at: '2026-10-01T01:00:00Z', total: 102, items: [
        { session_id: 'working-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false },
      ] }], counts: { running: 0, ended: 102, ended_groups: 1, running_by: {} },
    });
    vi.mocked(fetchCompleteSessionGroup).mockRejectedValue(new Error('incomplete membership'));
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '한꺼번에 떼기' }));
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', expect.stringContaining('변경이 저장되지 않았을 수 있으니'));
    expect(updateSessionGroupDismissals).not.toHaveBeenCalled();
  });
});
