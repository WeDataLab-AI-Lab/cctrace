// @vitest-environment jsdom
import { state, buckets, mount, waitFor, SessionsPage } from './page-test-fixture';
import { createElement } from 'react';
import { QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { fetchMaintenanceStatus, fetchSessionCalendar, fetchSessionOverview, fetchSessionOverviewCategories, fetchSessionOverviewCount, fetchSessionRecordStatus, refreshSessionRecordRange } from '@/lib/api';
import { fetchCompleteSessionGroup, fetchSessionGroups, updateSessionGroupDismissals } from '@/lib/session-groups-api';
import { describe } from 'vitest';

describe('sessions scope and loading regression 3.1', () => {
it('serializes count, category, and group reads after the analysis overview settles', async () => {
    const maintenance = { token_rule_pending: false, step: '', session_analysis: { enabled: true } };
    let activeReads = 0;
    let maxActiveReads = 0;
    const trackRead = async <T,>(read: () => Promise<T>) => {
      activeReads += 1;
      maxActiveReads = Math.max(maxActiveReads, activeReads);
      try { return await read(); } finally { activeReads -= 1; }
    };
    vi.mocked(fetchSessionOverviewCount).mockImplementation((params) => trackRead(async () => ({
      count: params?.classification ? 1 : 9, unattributed: 0, classification_counts: buckets,
    })));
    vi.mocked(fetchSessionOverviewCategories).mockImplementation(() => trackRead(async () => ({ ended: 11, running: 2 })));
    vi.mocked(fetchSessionGroups).mockImplementation(() => trackRead(async () => ({
      scope: 'filtered_total', grouping_version: 1, truncated: false, overview_total: 9,
      running: [], ended: [], counts: { running: 0, ended: 0, ended_groups: 0, running_by: {} },
    })));
    mount(maintenance);
    fireEvent.click(screen.getByRole('button', { name: '세션 요약' }));

    await waitFor(() => expect(maxActiveReads).toBeGreaterThan(0));
    await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalled());
    await waitFor(() => expect(fetchSessionOverviewCategories).toHaveBeenCalled());
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalled());
    await waitFor(() => expect(activeReads).toBe(0));
    expect(maxActiveReads).toBe(1);
  });

it('reads C18 status for the selected calendar week and posts that exact range on refresh', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '',
      session_analysis: { enabled: true }, session_records: { enabled: true, prompt_version: 1, state: 'running', paused_reason: '' } });
    vi.mocked(fetchSessionRecordStatus).mockResolvedValue({ state: 'stale', written: 2, total: 5, stale: 3, writing: 0,
      blocked: { consent_required: 1 }, waiting_budget: true });
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '캘린더' }));
    await waitFor(() => expect(fetchSessionRecordStatus).toHaveBeenCalledTimes(1));
    expect(screen.getByRole('button', { name: '개요 다시 만들기' })).toBeTruthy();
    const summary = screen.getByTestId('summary-state');
    expect(summary.textContent).toContain('마지막 범위 요청에서 2건을 썼습니다');
    expect(summary.textContent).toContain('일일 토큰 한도 대기');
    const [from, to] = vi.mocked(fetchSessionRecordStatus).mock.calls[0] ?? [];
    await userEvent.click(screen.getByRole('button', { name: '개요 다시 만들기' }));
    await waitFor(() => expect(refreshSessionRecordRange).toHaveBeenCalledWith(from, to));
  });

it('offers no calendar and sends no calendar request while the server flag is off', async () => {
    mount();
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalled());
    expect(screen.queryByRole('group', { name: '보기 방식' })).toBeNull();
    expect(fetchSessionCalendar).not.toHaveBeenCalled();
  });

it.each([
    ['spring', '2026-03-04T12:00:00Z', 167],
    ['fall', '2026-10-28T12:00:00Z', 169],
  ])('requests the %s DST week in the chosen IANA zone as %i hours', async (_name, now, hours) => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date(now));
    try {
      vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
      mount();
      await userEvent.click(await screen.findByRole('button', { name: '캘린더' }));
      await userEvent.selectOptions(screen.getByRole('combobox', { name: '캘린더 시간대' }), 'America/New_York');
      await waitFor(() => {
        const params = vi.mocked(fetchSessionCalendar).mock.lastCall![0];
        expect((Date.parse(params.to) - Date.parse(params.from)) / 3_600_000).toBe(hours);
      });
      expect(screen.queryByText(/응답 구간이 이 주와 다르거나/)).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

it('does not request a calendar for the weekly report agent scope', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    state.agent = 'weekly';
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '캘린더' }));
    expect(screen.getByText(/주간 AI 보고 실행은 캘린더 대상이 아닙니다/)).toBeTruthy();
    expect(fetchSessionCalendar).not.toHaveBeenCalled();
  });

it('keeps owner-scoped groups available when account selection is ignored by both server lists', async () => {
    state.account = 'member@example.test';
    mount();
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledWith(expect.objectContaining({ loginEmail: 'member@example.test' }), expect.any(AbortSignal)));
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledWith(expect.objectContaining({
      source: 'interactive', projectHashes: undefined, agent: '', assembled: true,
      classification: undefined, loginEmail: undefined,
    })));
    expect(screen.queryByText(/계정 필터에서 세션 묶음은 아직 제공되지 않습니다/)).toBeNull();
  });

it('keeps a single assembled conversation and exposes original records through inspection', async () => {
    mount();
    await waitFor(() => expect(screen.getByTestId('viewer-session')).toHaveProperty('textContent', 'working-row'));
    expect(state.onModeChange).toBeUndefined();
    expect(state.mode).toBe('assembled');
    expect(screen.queryByText('Raw')).toBeNull();
    expect(vi.mocked(fetchSessionOverview).mock.calls.every(([params]) => params?.assembled === true)).toBe(true);
  });

it('refetches and offers the new key after a stale group write is rejected', async () => {
    const member = { session_id: 'working-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false };
    const base = { scope: 'filtered_total' as const, grouping_version: 1, truncated: false, overview_total: 1,
      running: [], counts: { running: 0, ended: 1, ended_groups: 1, running_by: {} } };
    vi.mocked(fetchSessionGroups).mockResolvedValueOnce({ ...base, ended: [{ key: 'old-key', ended_at: member.ended_at, total: 1, items: [member] }] })
      .mockResolvedValue({ ...base, ended: [{ key: 'new-key', ended_at: member.ended_at, total: 1, items: [member] }] });
    vi.mocked(updateSessionGroupDismissals).mockRejectedValueOnce(new Error('404 stale key')).mockResolvedValue(undefined);
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '떼기' }));
    expect(await screen.findByRole('alert')).toBeTruthy();
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledTimes(2));
    await userEvent.click(screen.getByRole('button', { name: '떼기' }));
    await waitFor(() => expect(updateSessionGroupDismissals).toHaveBeenCalledTimes(2));
    expect(vi.mocked(updateSessionGroupDismissals).mock.calls.map((call) => call[4])).toEqual(['old-key', 'new-key']);
  });

it('stops later batches and stale errors after an account change during an ACK', async () => {
    const members = Array.from({ length: 101 }, (_, index) => ({ session_id: `member-${index}`, ended_at: '2026-10-01T01:00:00Z', dismissed: false }));
    let rejectAck!: (error: Error) => void;
    vi.mocked(fetchSessionGroups).mockResolvedValue({
      scope: 'filtered_total', grouping_version: 1, truncated: true, overview_total: 101,
      running: [], ended: [{ key: 'old-group', ended_at: members[0].ended_at, total: 101, items: [members[0]] }],
      counts: { running: 0, ended: 101, ended_groups: 1, running_by: {} },
    });
    vi.mocked(fetchCompleteSessionGroup).mockResolvedValue(members);
    vi.mocked(updateSessionGroupDismissals).mockImplementationOnce(() => new Promise((_, reject) => { rejectAck = reject; }));
    state.isAdmin = true;
    const { client, view } = mount();
    await userEvent.click(await screen.findByRole('button', { name: '한꺼번에 떼기' }));
    await waitFor(() => expect(updateSessionGroupDismissals).toHaveBeenCalledTimes(1));
    state.account = 'other@example.test';
    view.rerender(createElement(QueryClientProvider, { client }, createElement(SessionsPage)));
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledWith(expect.objectContaining({ loginEmail: 'other@example.test' })));
    act(() => rejectAck(new Error('old scope failed')));
    await waitFor(() => expect(screen.getByRole('button', { name: '한꺼번에 떼기' }).hasAttribute('disabled')).toBe(false));
    expect(updateSessionGroupDismissals).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('alert')).toBeNull();
  });

it('explains when personal classification is unavailable without claiming zero buckets', async () => {
    vi.mocked(fetchSessionOverviewCount).mockResolvedValue({ count: 9, unattributed: 0 });
    mount();
    expect(await screen.findByText('분류와 메모는 세션 주인에게만 제공됩니다')).toBeTruthy();
    expect(screen.queryByRole('tab', { name: /작업 중 0/ })).toBeNull();
  });
});
