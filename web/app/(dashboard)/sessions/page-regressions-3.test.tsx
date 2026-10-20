// @vitest-environment jsdom
import { state, row, buckets, mount, pageCalendar, waitFor, SessionsPage } from './page-test-fixture';
import { createElement } from 'react';
import { QueryClientProvider } from '@tanstack/react-query';
import { act, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { fetchMaintenanceStatus, fetchSessionCalendar, fetchSessionOverview, fetchSessionOverviewCategories, fetchSessionOverviewCount } from '@/lib/api';
import type { SessionCalendarResponse } from '@/lib/api';
import { fetchPersonalNoteOverview } from '@/lib/personal-state-api';
import { fetchCompleteSessionGroup, fetchSessionGroups, updateSessionGroupDismissals } from '@/lib/session-groups-api';
import { describe } from 'vitest';

describe('sessions scope and loading regression 1.2', () => {
it('does not show auxiliary read errors while a count query is retrying', async () => {
    const maintenance = { token_rule_pending: false, step: '', session_analysis: { enabled: true } };
    let resolveRetry!: (count: { count: number; unattributed: number; classification_counts: typeof buckets }) => void;
    vi.mocked(fetchSessionOverviewCount)
      .mockRejectedValueOnce(new Error('temporary timeout'))
      .mockImplementationOnce(() => new Promise((resolve) => { resolveRetry = resolve; }));
    mount(maintenance, 1);

    await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalledTimes(2));
    expect(screen.queryByText('세션 판정 개수를 읽지 못했습니다.')).toBeNull();
    expect(screen.queryByText('전체 범주 개수를 읽지 못했습니다.')).toBeNull();
    expect(screen.queryByText(/세션 묶음을 읽지 못했습니다/)).toBeNull();
    await act(async () => resolveRetry({ count: 9, unattributed: 0, classification_counts: buckets }));
  });

it.each(['error', 'inconsistent'] as const)('shows hidden count as indeterminate on %s source totals', async (caseName) => {
    vi.mocked(fetchSessionOverviewCount).mockImplementation(async (params) => {
      if (caseName === 'error' && params?.source === 'all') throw new Error('source total unavailable');
      const count = caseName === 'inconsistent' && params?.source === 'all' ? 2 : 5;
      return { count, unattributed: 0, classification_counts: buckets };
    });
    mount();

    await waitFor(() => expect(screen.getByTestId('hidden-session-count').textContent).toBe('unknown'));
  });

it('keeps the server sort order for grouped rows only while analysis reorders the list', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    vi.mocked(fetchSessionOverview).mockImplementation(async (params) => params?.sort === 'requests'
      ? [row('ccc-row'), row('aaa-row'), row('bbb-row')] : [row('aaa-row'), row('bbb-row'), row('ccc-row')]);
    vi.mocked(fetchSessionGroups).mockResolvedValue({
      scope: 'filtered_total', grouping_version: 1, truncated: false, overview_total: 9,
      running: [{ session_id: 'aaa-row', category: 'running', bucket: 'processing' }],
      ended: [{ key: 'group-ab', ended_at: '2026-10-01T01:00:00Z', total: 2, items: [
        { session_id: 'aaa-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false },
        { session_id: 'bbb-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false },
      ] }], counts: { running: 1, ended: 2, ended_groups: 1, running_by: { processing: 1 } },
    });
    const order = () => screen.getAllByRole('button').flatMap((node) => node.textContent?.match(/^(aaa|bbb|ccc)-row/)?.[0] ?? []);
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '세션 요약' }));
    await waitFor(() => expect(screen.getByRole('group', { name: '함께 끝난 세션 2건' })).toBeTruthy());
    await userEvent.click(screen.getByRole('button', { name: '요청 수' }));
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledWith(expect.objectContaining({ sort: 'requests' }), expect.any(AbortSignal)));
    await waitFor(() => expect(order()).toEqual(['ccc-row', 'aaa-row', 'bbb-row']));
    expect(screen.getByRole('group', { name: '함께 끝난 세션 2건' })).toBeTruthy();
    expect(screen.getByText(/전체 범위 9건 · 실행 1건 · 종료 2건 · 현재 목록 3건/)).toBeTruthy();
    expect(screen.getAllByRole('button', { name: '떼기' })).toHaveLength(2);
    expect(screen.getByRole('button', { name: '한꺼번에 떼기' })).toBeTruthy();
  });

it('hides the previous user\'s selection from the first render after an administrator switches calendar user', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    state.accessMode = true;
    state.isAdmin = true;
    const other = pageCalendar();
    other.sessions = [{ ...other.sessions[0], session_id: 'b-session', overview: { ...other.sessions[0].overview, session_id: 'b-session' } }];
    vi.mocked(fetchSessionCalendar).mockImplementation(async ({ from, to, userId }) =>
      ({ ...(userId === 'other-uid' ? other : pageCalendar()), from, to }) as unknown as SessionCalendarResponse);
    mount();
    await userEvent.click(await screen.findByRole('button', { name: /other-ro/ }));
    const instance = screen.getByTestId('viewer-instance').textContent;
    await userEvent.click(screen.getByRole('button', { name: '캘린더' }));
    await waitFor(() => expect(screen.getByRole('option', { name: 'other@example.test' })).toBeTruthy());
    expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'other-row');
    state.viewerRenders = [];
    await userEvent.selectOptions(screen.getByRole('combobox', { name: '캘린더 사용자' }), 'uid:other-uid');
    expect(state.viewerRenders).not.toContain('other-row');
    expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'none');
    await waitFor(() => expect(document.querySelector('[data-session-id="b-session"]')).toBeTruthy());
    expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'none');
    await userEvent.click(document.querySelector('[data-session-id="b-session"]') as HTMLElement);
    expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'b-session');
    expect(screen.getByTestId('viewer-instance').textContent).toBe(instance);
  });

it('uses the full source scope for all-source lists, counts, groups, and category totals', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '세션 요약' }));
    await waitFor(() => expect(fetchSessionOverviewCategories).toHaveBeenCalledWith(expect.objectContaining({ source: 'interactive' })));

    await act(async () => { state.onSourceSelect?.('all'); });

    await waitFor(() => expect(fetchSessionOverviewCategories).toHaveBeenLastCalledWith(expect.objectContaining({ source: 'all' })));
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenLastCalledWith(expect.objectContaining({ source: '' }), expect.any(AbortSignal)));
    await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenLastCalledWith(expect.objectContaining({ source: '' }), expect.any(AbortSignal)));
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenLastCalledWith(expect.objectContaining({ source: '' })));
  });

it('switches the list and count scopes, resets the page and reads full-scope server totals', async () => {
    const { client } = mount();
    expect(await screen.findByRole('tab', { name: '전체9' })).toHaveProperty('id', 'classification-tab-all');
    expect(screen.getByRole('tabpanel').getAttribute('aria-labelledby')).toBe('classification-tab-all');
    await userEvent.click(screen.getByRole('tab', { name: '작업 중3' }));
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledWith(expect.objectContaining({ classification: 'working', offset: 0, limit: 100 }), expect.any(AbortSignal)));
    await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalledWith(expect.objectContaining({ classification: 'working' }), expect.any(AbortSignal)));
    expect(screen.getByRole('tabpanel').getAttribute('aria-labelledby')).toBe('classification-tab-working');
    expect(screen.getByRole('tab', { name: '전체9' })).toBeTruthy();
    expect(client.getQueryData(['session-overview-count', 7, '', 'interactive', '', '', true, 'working', '', ''])).toMatchObject({ count: 1 });
    expect(screen.getByText('1 / 1개')).toBeTruthy();
  });

it('treats an unavailable legacy group API as an unsupported feature, not a failed write', async () => {
    vi.mocked(fetchSessionGroups).mockRejectedValueOnce(Object.assign(new Error('not found'), { status: 404 }));
    mount();
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalled());
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalled());
    expect(screen.queryByText(/세션 묶음을 읽거나 변경하지 못했습니다/)).toBeNull();
  });

it('retries only unacknowledged members after a later batch fails', async () => {
    const members = Array.from({ length: 201 }, (_, index) => ({
      session_id: index === 0 ? 'working-row' : index === 1 ? 'other-row' : `member-${index}`,
      ended_at: '2026-10-01T01:00:00Z', dismissed: false,
    }));
    const initial = {
      scope: 'filtered_total' as const, grouping_version: 1, truncated: true, overview_total: 201,
      running: [], ended: [{ key: 'group-one', ended_at: members[0].ended_at, total: 201, items: members.slice(0, 2) }],
      counts: { running: 0, ended: 201, ended_groups: 1, running_by: {} },
    };
    vi.mocked(fetchSessionGroups).mockResolvedValueOnce(initial).mockResolvedValue({
      ...initial, ended: [{ ...initial.ended[0], items: members.slice(0, 2).map((member) => ({ ...member, dismissed: true })) }],
    });
    vi.mocked(fetchCompleteSessionGroup).mockResolvedValueOnce(members).mockResolvedValue(
      members.map((member, index) => ({ ...member, dismissed: index < 100 })),
    );
    vi.mocked(updateSessionGroupDismissals).mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error('second batch failed'))
      .mockResolvedValue(undefined);
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '한꺼번에 떼기' }));
    await waitFor(() => expect(updateSessionGroupDismissals).toHaveBeenCalledTimes(2));
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', expect.stringContaining('변경이 저장되지 않았을 수 있으니'));
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledTimes(2));
    await userEvent.click(screen.getByRole('button', { name: '한꺼번에 떼기' }));
    await waitFor(() => expect(updateSessionGroupDismissals).toHaveBeenCalledTimes(4));
    expect(vi.mocked(updateSessionGroupDismissals).mock.calls.map((call) => call[2].length)).toEqual([100, 100, 100, 1]);
    expect(vi.mocked(updateSessionGroupDismissals).mock.calls[2][2][0].session_id).toBe(members[100].session_id);
    expect(vi.mocked(updateSessionGroupDismissals).mock.calls.every((call) => call[4] === 'group-one')).toBe(true);
  });

it('does not send the second batch after the user changes during the first ACK', async () => {
    const members = Array.from({ length: 101 }, (_, index) => ({ session_id: `member-${index}`, ended_at: '2026-10-01T01:00:00Z', dismissed: false }));
    let resolveAck!: () => void;
    vi.mocked(fetchSessionGroups).mockResolvedValue({
      scope: 'filtered_total', grouping_version: 1, truncated: true, overview_total: 101,
      running: [], ended: [{ key: 'old-group', ended_at: members[0].ended_at, total: 101, items: [members[0]] }],
      counts: { running: 0, ended: 101, ended_groups: 1, running_by: {} },
    });
    vi.mocked(fetchCompleteSessionGroup).mockResolvedValue(members);
    vi.mocked(updateSessionGroupDismissals).mockImplementationOnce(() => new Promise((resolve) => { resolveAck = resolve; }));
    const { client, view } = mount();
    const invalidate = vi.spyOn(client, 'invalidateQueries');
    await userEvent.click(await screen.findByRole('button', { name: '한꺼번에 떼기' }));
    await waitFor(() => expect(updateSessionGroupDismissals).toHaveBeenCalledTimes(1));
    state.userId = 8;
    view.rerender(createElement(QueryClientProvider, { client }, createElement(SessionsPage)));
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledTimes(2));
    act(resolveAck);
    await waitFor(() => expect(screen.getByRole('button', { name: '한꺼번에 떼기' }).hasAttribute('disabled')).toBe(false));
    expect(updateSessionGroupDismissals).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['session-groups', 7, 'owner-uid', null, '', 'interactive', '', '', true, 'all', '', '', false, '', '', '', 'activity'], exact: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ['session-groups', 8, 'owner-uid', null, '', 'interactive', '', '', true, 'all', '', '', false, '', '', '', 'activity'], exact: true });
    expect(screen.queryByRole('alert')).toBeNull();
  });

it('opens a parent outside the classification directly without changing list filters or requiring a personal note', async () => {
    vi.mocked(fetchPersonalNoteOverview).mockRejectedValue(new Error('parent has no personal note'));
    mount();
    await screen.findByRole('tab', { name: '작업 중3' });
    await userEvent.click(screen.getByRole('tab', { name: '작업 중3' }));
    await screen.findByText('1 / 1개');
    await userEvent.click(screen.getByRole('button', { name: '부모로 이동' }));
    await waitFor(() => expect(screen.getByTestId('viewer-search-target').textContent).toBe('parent:'));
    expect(screen.getByRole('tab', { name: '작업 중3' }).getAttribute('aria-selected')).toBe('true');
    expect(screen.getByText('1 / 1개')).toBeTruthy();
    expect(fetchPersonalNoteOverview).not.toHaveBeenCalled();
    expect(screen.queryByText(/현재 목록·필터에서 확인하지 못했습니다/)).toBeNull();
    expect(screen.queryByRole('button', { name: '전체로 보기' })).toBeNull();
    const destination = new URL(state.replacedUrl, 'http://localhost');
    expect(destination.searchParams.get('session_id')).toBe('parent');
    expect(destination.searchParams.get('anchor')).toBe('fork-anchor');
    expect(destination.searchParams.get('detail')).toBe('conversation');
  });
});
