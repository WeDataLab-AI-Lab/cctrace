// @vitest-environment jsdom
import { state, row, buckets, mount, waitFor } from './page-test-fixture';
import { act, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { fetchMaintenanceStatus, fetchSessionCalendar, fetchSessionOverview, fetchSessionOverviewCategories, fetchSessionOverviewCount, SessionCalendarRequestError } from '@/lib/api';
import { fetchCompleteSessionGroup, fetchSessionGroups, updateSessionGroupDismissals } from '@/lib/session-groups-api';
import type { SessionOverview } from '@/lib/types';
import { describe } from 'vitest';

describe('sessions scope and loading regression 2.1', () => {
it('renders basic rows and totals before explicitly opening a pending analysis view', async () => {
    const maintenance = { token_rule_pending: false, step: '', session_analysis: { enabled: true },
      session_records: { enabled: true, prompt_version: 1, state: 'running', paused_reason: '' } };
    let resolveAnalysis!: (rows: SessionOverview[]) => void;
    vi.mocked(fetchSessionOverview).mockImplementation((params) => params?.analysis
      ? new Promise((resolve) => { resolveAnalysis = resolve; })
      : Promise.resolve([row('fast-first-page')]));
    mount(maintenance);

    await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalled());
    expect(fetchSessionOverview).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId('viewer-session').textContent).toBe('fast-first-page');
    expect(screen.getByRole('button', { name: /fast-fir/ })).toBeTruthy();
    expect(fetchSessionOverviewCategories).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole('button', { name: '세션 요약' }));
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledTimes(2));
    const requests = vi.mocked(fetchSessionOverview).mock.calls.map(([params]) => params);
    expect(requests[0]).toMatchObject({ limit: 100, offset: 0, source: 'interactive', assembled: true });
    expect(requests[0]?.analysis).toBeUndefined();
    expect(requests[1]).toMatchObject({ analysis: true, sort: 'activity', offset: 0 });
    expect(screen.getByTestId('viewer-session').textContent).toBe('fast-first-page');
    expect(screen.queryByRole('button', { name: /fast-fir/ })).toBeNull();
    expect(fetchSessionOverviewCategories).not.toHaveBeenCalled();

    await act(async () => resolveAnalysis([{
      ...row('fast-first-page'),
      analysis: { state: 'ready', title: { text: 'analysis ready', source: 'ai', state: 'ok' },
        requests: 1, inputs: 1, plan_flags: { has_plan_file: false, used_plan_mode: false } },
    }]));
    await screen.findByText('analysis ready');
    await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalled());
    await waitFor(() => expect(fetchSessionOverviewCategories).toHaveBeenCalled());
  });

it('shows observed signals instead of the retired completion buckets and sends no bucket filter', async () => {
    state.since = '2026-10-01T00:00:00Z';
    state.until = '2026-10-02T00:00:00Z';
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '',
      session_analysis: { enabled: true }, session_records: { enabled: true, prompt_version: 1, state: 'running', paused_reason: '' } });
    const judgementCounts = { done: 1, leftover: 2, ended_midway: 3, unknown: 4 };
    vi.mocked(fetchSessionOverview).mockResolvedValue([row('bucket-row')]);
    vi.mocked(fetchSessionOverviewCount).mockImplementation(async (params) => ({
      count: params?.bucket ? 2 : 10, unattributed: 0, classification_counts: buckets, judgement_bucket_counts: judgementCounts,
    }));
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '세션 요약' }));
    expect(screen.queryByRole('button', { name: /하다 만 것/ })).toBeNull();
    expect(screen.queryByRole('button', { name: /판정 불가/ })).toBeNull();
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledWith(expect.not.objectContaining({ bucket: 'leftover' }), expect.any(AbortSignal)));
    await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalledWith(expect.not.objectContaining({ bucket: 'leftover' }), expect.any(AbortSignal)));
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledWith(expect.not.objectContaining({ bucket: 'leftover' })));
  });

it('keeps a group-wide action for a default group with no rows in the filtered list', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    vi.mocked(fetchSessionOverview).mockImplementation(async (params) => params?.plan === 'file'
      ? [row('ccc-row')] : [row('aaa-row'), row('bbb-row'), row('ccc-row')]);
    vi.mocked(fetchSessionGroups).mockResolvedValue({
      scope: 'filtered_total', grouping_version: 1, truncated: false, overview_total: 9,
      running: [], ended: [{ key: 'group-ab', ended_at: '2026-10-01T01:00:00Z', total: 2, items: [
        { session_id: 'aaa-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false },
        { session_id: 'bbb-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false },
      ] }], counts: { running: 0, ended: 2, ended_groups: 1, running_by: {} },
    });
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '세션 요약' }));
    await userEvent.click(await screen.findByRole('button', { name: '플랜 파일' }));
    await waitFor(() => expect(screen.queryByRole('button', { name: /aaa-row/ })).toBeNull());
    expect(screen.getByRole('group', { name: '함께 끝난 세션 2건' })).toBeTruthy();
    expect(screen.getByText(/현재 목록 0건 표시 · 서버 반환 2건/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: '떼기' })).toBeNull();
    expect(screen.getByText(/세션 묶음은 현재 검색·플랜·범주·기간·프로젝트·계정 범위 전체에서 계산/)).toBeTruthy();
    expect(screen.queryByText(/현재 목록과 겹치는 행만/)).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: '한꺼번에 떼기' }));
    await waitFor(() => expect(updateSessionGroupDismissals).toHaveBeenCalled());
  });

it('shows a disabled calendar honestly and refreshes the calendar after a delete', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    vi.mocked(fetchSessionCalendar).mockRejectedValueOnce(new SessionCalendarRequestError(404, 'session_analysis_disabled'));
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '캘린더' }));
    expect((await screen.findByRole('alert')).textContent).toContain('세션 분석이 서버에서 꺼져 있어');
    expect(fetchSessionCalendar).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByRole('button', { name: '다시 읽기' }));
    await waitFor(() => expect(document.querySelector('[data-session-id="ready-owned"]')).toBeTruthy());
    const before = vi.mocked(fetchSessionCalendar).mock.calls.length;
    await act(async () => { state.onDeleted?.('ready-owned'); });
    await waitFor(() => expect(vi.mocked(fetchSessionCalendar).mock.calls.length).toBeGreaterThan(before));
  });

it('does not send owner category requests from an admin view', async () => {
    state.isAdmin = true;
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '세션 요약' }));
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledWith(expect.objectContaining({ analysis: true }), expect.any(AbortSignal)));
    expect(fetchSessionOverviewCategories).not.toHaveBeenCalled();
    expect(screen.queryByRole('group', { name: '관측 범주' })).toBeNull();
    expect(vi.mocked(fetchSessionOverview).mock.calls.every(([params]) => !params?.category)).toBe(true);
  });

it('uses the exact analysis and period filters for both list rows and server groups', async () => {
    state.since = '2026-10-01T00:00:00Z';
    state.until = '2026-10-08T00:00:00Z';
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '세션 요약' }));
    await userEvent.type(await screen.findByRole('textbox', { name: '세션 검색' }), 'calendar');
    await userEvent.click(screen.getByRole('button', { name: '플랜 파일' }));
    await userEvent.click(screen.getByRole('button', { name: /진행 중/ }));
    await userEvent.click(screen.getByRole('button', { name: '비용' }));
    const expected = { analysis: true, query: 'calendar', plan: 'file', category: 'running', sort: 'cost',
      since: state.since, until: state.until };
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledWith(expect.objectContaining(expected), expect.any(AbortSignal)));
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledWith(expect.objectContaining({
      includeAnalysis: true, query: 'calendar', plan: 'file', category: 'running', sort: 'cost',
      since: state.since, until: state.until, source: 'interactive', assembled: true,
    })));
  });

it('moves a group member only after the server ACK and re-fetch', async () => {
    const before = {
      scope: 'filtered_total' as const, grouping_version: 1, truncated: false, overview_total: 9,
      running: [], ended: [{ key: 'group-one', ended_at: '2026-10-01T01:00:00Z', total: 2, items: [
        { session_id: 'working-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false },
        { session_id: 'other-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false },
      ] }],
      counts: { running: 0, ended: 2, ended_groups: 1, running_by: {} },
    };
    vi.mocked(fetchSessionGroups).mockResolvedValueOnce(before).mockResolvedValueOnce({
      ...before, ended: before.ended.map((group) => ({ ...group, items: group.items.map((item) => ({ ...item, dismissed: true })) })),
    });
    let acknowledge!: () => void;
    vi.mocked(updateSessionGroupDismissals).mockImplementation(() => new Promise<void>((resolve) => { acknowledge = resolve; }));
    mount();
    const button = await screen.findByRole('button', { name: '한꺼번에 떼기' });
    await userEvent.click(button);
    await waitFor(() => expect(fetchCompleteSessionGroup).toHaveBeenCalledWith(
      expect.objectContaining({ source: 'interactive', projectHashes: undefined, agent: '', assembled: true, classification: undefined, loginEmail: undefined }), 'group-one', 1, 2,
    ));
    expect(updateSessionGroupDismissals).toHaveBeenCalledWith(
      expect.objectContaining({ source: 'interactive', projectHashes: undefined, agent: '', assembled: true, classification: undefined, loginEmail: undefined }),
      1,
      [{ session_id: 'working-row', ended_at: '2026-10-01T01:00:00Z' }, { session_id: 'other-row', ended_at: '2026-10-01T01:00:00Z' }],
      true, 'group-one',
    );
    expect(screen.getByRole('button', { name: '한꺼번에 떼기' })).toHaveProperty('disabled', true);
    act(acknowledge);
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByRole('button', { name: '떼기' })).toBeNull());
    expect(screen.getAllByRole('button', { name: '묶음 복원' })).toHaveLength(2);
  });

it('shows a read error without losing an ungrouped loaded row', async () => {
    vi.mocked(fetchSessionGroups).mockRejectedValue(new Error('server unavailable'));
    mount();
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', expect.stringContaining('세션 묶음을 읽지 못했습니다'));
    expect(await screen.findByRole('button', { name: /working-/ })).toBeTruthy();
    expect(screen.queryByRole('button', { name: '한꺼번에 떼기' })).toBeNull();
  });

it('preserves grouped rows and reports when dismissal fails instead of inventing ACK', async () => {
    vi.mocked(fetchSessionGroups).mockResolvedValue({
      scope: 'filtered_total', grouping_version: 1, truncated: false, overview_total: 9,
      running: [], ended: [{ key: 'group-one', ended_at: '2026-10-01T01:00:00Z', total: 2, items: [
        { session_id: 'working-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false },
        { session_id: 'other-row', ended_at: '2026-10-01T01:00:00Z', dismissed: false },
      ] }], counts: { running: 0, ended: 2, ended_groups: 1, running_by: {} },
    });
    vi.mocked(updateSessionGroupDismissals).mockRejectedValue(new Error('save failed'));
    mount();
    await userEvent.click(await screen.findByRole('button', { name: '한꺼번에 떼기' }));
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', expect.stringContaining('변경이 저장되지 않았을 수 있으니'));
    expect(screen.getByRole('button', { name: '한꺼번에 떼기' })).toBeTruthy();
    await waitFor(() => expect(fetchSessionGroups).toHaveBeenCalledTimes(2));
    expect(fetchCompleteSessionGroup).toHaveBeenCalledTimes(1);
  });
});
