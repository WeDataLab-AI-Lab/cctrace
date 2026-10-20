// @vitest-environment jsdom
import { state, row, mount, waitFor } from './page-test-fixture';
import { act, fireEvent, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { fetchMaintenanceStatus, fetchSessionCalendar, fetchSessionOverview, fetchSessionOverviewCategories, fetchSessionOverviewCount } from '@/lib/api';
import { fetchCompleteSessionGroup, fetchSessionGroups, updateSessionGroupDismissals } from '@/lib/session-groups-api';
import type { SessionOverview } from '@/lib/types';
import { describe } from 'vitest';

describe('sessions scope and loading regression 3.2', () => {
it('uses the exact analysis scope without a basic placeholder when a filter is active', async () => {
    const maintenance = { token_rule_pending: false, step: '', session_analysis: { enabled: true } };
    let resolveBasic!: (rows: SessionOverview[]) => void;
    let resolveAnalysis!: (rows: SessionOverview[]) => void;
    vi.mocked(fetchSessionOverview).mockImplementation((params) => params?.analysis
      ? new Promise((resolve) => { resolveAnalysis = resolve; })
      : new Promise((resolve) => { resolveBasic = resolve; }));
    mount(maintenance);

    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledTimes(1));
    expect(vi.mocked(fetchSessionOverview).mock.calls[0][0]?.analysis).toBeUndefined();
    fireEvent.change(screen.getByRole('textbox', { name: '세션 검색' }), { target: { value: 'needle' } });
    fireEvent.click(screen.getByRole('button', { name: '세션 요약' }));
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledTimes(2));
    expect(fetchSessionOverview).toHaveBeenLastCalledWith(expect.objectContaining({ analysis: true, query: 'needle', offset: 0 }), expect.any(AbortSignal));
    expect(screen.queryByRole('button', { name: /raw-filt/ })).toBeNull();
    expect(fetchSessionOverviewCount).not.toHaveBeenCalled();
    expect(fetchSessionOverviewCategories).not.toHaveBeenCalled();

    await act(async () => resolveBasic([row('raw-filtered-scope')]));
    expect(screen.queryByRole('button', { name: /raw-filt/ })).toBeNull();
    expect(fetchSessionOverviewCount).not.toHaveBeenCalled();
    expect(fetchSessionOverviewCategories).not.toHaveBeenCalled();
    await act(async () => resolveAnalysis([row('analysis-filtered-scope')]));
    await waitFor(() => expect(screen.getByRole('button', { name: /analysis/ })).toBeTruthy());
  });

it('preserves group actions for matching analysis rows without changing the server total', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    vi.mocked(fetchSessionOverview).mockImplementation(async (params) => params?.plan === 'file'
      ? [row('working-row')] : [row('working-row'), row('other-row')]);
    vi.mocked(fetchSessionGroups).mockResolvedValue({
      scope: 'filtered_total', grouping_version: 1, truncated: false, overview_total: 9,
      running: [], ended: [{ key: 'group-one', ended_at: '2026-10-01T01:00:00Z', total: 2, items: [
        { session_id: 'working-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false },
        { session_id: 'other-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false },
      ] }], counts: { running: 0, ended: 2, ended_groups: 1, running_by: {} },
    });
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '세션 요약' }));
    await userEvent.click(await screen.findByRole('button', { name: '플랜 파일' }));
    await waitFor(() => expect(screen.getByRole('group', { name: '함께 끝난 세션 2건' })).toBeTruthy());
    expect(screen.getByText(/전체 범위 9건 · 실행 0건 · 종료 2건 · 현재 목록 1건/)).toBeTruthy();
    expect(screen.getByText(/현재 목록 1건 표시 · 서버 반환 2건/)).toBeTruthy();
    expect(screen.getByRole('button', { name: '한꺼번에 떼기' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /other-ro/ })).toBeNull();
  });

it('sends the profile email in the legacy access mode even for a user with an ID', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    state.accessMode = false;
    state.isAdmin = true;
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '캘린더' }));
    const users = await screen.findByRole('combobox', { name: '캘린더 사용자' });
    await waitFor(() => expect(screen.getByRole('option', { name: 'other@example.test' })).toBeTruthy());
    await userEvent.selectOptions(users, 'email:other@example.test');
    await waitFor(() => expect(fetchSessionCalendar).toHaveBeenLastCalledWith(expect.objectContaining({ profileEmail: 'other@example.test' })));
    expect(vi.mocked(fetchSessionCalendar).mock.lastCall![0]).not.toHaveProperty('userId');
  });

it('clears a deleted search deep link while preserving filters and selecting another session', async () => {
    state.urlSearch = new URLSearchParams([
      ['session_id', 'deleted-search'],
      ['session_search', '1'],
      ['turn_key', 'turn-8'],
      ['from', '2026-10-09T12:30:00Z'],
      ['since', '2026-10-01T00:00:00Z'],
      ['until', '2026-10-10T00:00:00Z'],
      ['project_hash', 'project-stays'],
    ]).toString();
    vi.mocked(fetchSessionOverview)
      .mockResolvedValueOnce([row('deleted-search'), row('other-row')])
      .mockResolvedValue([row('other-row')]);
    const { client, view } = mount();
    await waitFor(() => {
      expect(screen.getByTestId('viewer-session').textContent).toBe('deleted-search');
      expect(screen.getByTestId('viewer-search-target').textContent).toBe('deleted-search:turn-8');
    });

    client.setQueriesData<{ pages: SessionOverview[][]; pageParams: unknown[] }>(
      { queryKey: ['session-overview'] },
      (old) => old && ({ ...old, pages: old.pages.map((page) => page.filter((session) => session.session_id !== 'deleted-search')) }),
    );
    await act(async () => { state.onDeleted?.('deleted-search'); });

    const [pathname, query = ''] = state.replacedUrl.split('?');
    expect(pathname).toBe('/sessions');
    const remainingParams = new URLSearchParams(query);
    expect(remainingParams.get('session_id')).toBeNull();
    expect(remainingParams.get('session_search')).toBeNull();
    expect(remainingParams.get('turn_key')).toBeNull();
    expect(remainingParams.get('from')).toBeNull();
    expect(remainingParams.get('since')).toBe('2026-10-01T00:00:00Z');
    expect(remainingParams.get('until')).toBe('2026-10-10T00:00:00Z');
    expect(remainingParams.get('project_hash')).toBe('project-stays');
    await waitFor(() => {
      expect(screen.getByTestId('viewer-session').textContent).toBe('other-row');
      expect(screen.getByTestId('viewer-search-target').textContent).toBe('none');
    });

    view.unmount();
    mount();
    await waitFor(() => {
      expect(screen.getByTestId('viewer-session').textContent).toBe('other-row');
      expect(screen.getByTestId('viewer-search-target').textContent).toBe('none');
    });
  });

it('sends no analysis options while the server flag is disabled', async () => {
    mount();
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalled());
    expect(vi.mocked(fetchSessionOverview).mock.calls.every(([options]) => !options?.analysis && !options?.category && !options?.plan && !options?.query && !options?.sort)).toBe(true);
    expect(fetchSessionOverviewCategories).not.toHaveBeenCalled();
    expect(screen.queryByRole('group', { name: '관측 범주' })).toBeNull();
  });

it('does not show a group failure for an admin without a personal source identity', async () => {
    state.isAdmin = true;
    state.accessMode = true;
    state.cctraceUserId = '';
    mount();
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalled());
    expect(fetchSessionGroups).not.toHaveBeenCalled();
    expect(screen.queryByText(/세션 묶음을 읽거나 변경하지 못했습니다/)).toBeNull();
  });

it('uses only complete server membership and 100-member batches for a capped group', async () => {
    const groupItems = Array.from({ length: 501 }, (_, index) => ({ session_id: `member-${index}`, ended_at: '2026-10-01T01:00:00Z', dismissed: false }));
    vi.mocked(fetchSessionGroups).mockResolvedValue({
      scope: 'filtered_total', grouping_version: 1, truncated: true, overview_total: 501,
      running: [], ended: [{ key: 'group-one', ended_at: '2026-10-01T01:00:00Z', total: 501, items: [
        { session_id: 'working-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false },
        { session_id: 'other-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false },
      ] }], counts: { running: 0, ended: 501, ended_groups: 1, running_by: {} },
    });
    vi.mocked(fetchCompleteSessionGroup).mockResolvedValue(groupItems);
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '한꺼번에 떼기' }));
    await waitFor(() => expect(updateSessionGroupDismissals).toHaveBeenCalledTimes(6));
    expect(fetchCompleteSessionGroup).toHaveBeenCalledWith(expect.objectContaining({ source: 'interactive' }), 'group-one', 1, 501);
    expect(vi.mocked(updateSessionGroupDismissals).mock.calls.map((call) => call[2].length)).toEqual([100, 100, 100, 100, 100, 1]);
    expect(vi.mocked(updateSessionGroupDismissals).mock.calls.map((call) => call[4])).toEqual(Array(6).fill('group-one'));
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledTimes(2));
  });

it('stops old-scope writes when membership returns after a filter change', async () => {
    const member = { session_id: 'working-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false };
    let resolveMembers!: (members: typeof member[]) => void;
    vi.mocked(fetchSessionGroups).mockResolvedValue({
      scope: 'filtered_total', grouping_version: 1, truncated: false, overview_total: 1,
      running: [], ended: [{ key: 'old-group', ended_at: member.ended_at, total: 1, items: [member] }],
      counts: { running: 0, ended: 1, ended_groups: 1, running_by: {} },
    });
    vi.mocked(fetchCompleteSessionGroup).mockImplementation(() => new Promise((resolve) => { resolveMembers = resolve; }));
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '한꺼번에 떼기' }));
    await waitFor(() => expect(fetchCompleteSessionGroup).toHaveBeenCalledTimes(1));
    await userEvent.click(screen.getByRole('tab', { name: '작업 중3' }));
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledWith(expect.objectContaining({ classification: 'working' })));
    act(() => resolveMembers([member]));
    await waitFor(() => expect(screen.getByRole('button', { name: '한꺼번에 떼기' }).hasAttribute('disabled')).toBe(false));
    expect(updateSessionGroupDismissals).not.toHaveBeenCalled();
    expect(screen.queryByRole('alert')).toBeNull();
  });

it('removes a reclassified row after the save ACK without leaving it in the frozen list', async () => {
    const { client } = mount();
    await screen.findByRole('tab', { name: '작업 중3' });
    await userEvent.click(screen.getByRole('tab', { name: '작업 중3' }));
    await screen.findByText('1 / 1개');
    act(() => state.onClassificationSaved?.('working-row', 'done'));
    await waitFor(() => expect(client.getQueryData<{ pages: SessionOverview[][] }>(['session-overview', 7, '', 'interactive', '', '', true, 'working', '', ''])?.pages[0]).toEqual([]));
    await waitFor(() => expect(screen.queryByRole('button', { name: /working-r/ })).toBeNull());
    expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'working-row');
  });
});
