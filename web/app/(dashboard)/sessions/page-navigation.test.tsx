// @vitest-environment jsdom
import { mount, waitFor, state, buckets } from './page-test-fixture';
import { fireEvent, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { fetchMaintenanceStatus, fetchSessionCalendar, fetchSessionOverviewCount, fetchProjects } from '@/lib/api';
import userEvent from '@testing-library/user-event';

describe('sessions navigation and pagination', () => {
it('reads the administrator themself by default, then one user in the identity the access mode reads', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '', session_analysis: { enabled: true } });
    state.accessMode = true;
    state.isAdmin = true;
    state.account = 'login@example.test';
    const { client } = mount();
    await userEvent.click(await screen.findByRole('button', { name: '캘린더' }));
    await waitFor(() => expect(fetchSessionCalendar).toHaveBeenCalled());
    const own = vi.mocked(fetchSessionCalendar).mock.lastCall![0];
    expect(own).not.toHaveProperty('userId');
    expect(own.loginEmail).toBe('login@example.test');
    expect(screen.getByText(/상단 계정 필터\(login@example.test\)는 사용자를 고르지 않고/)).toBeTruthy();
    const users = await screen.findByRole('combobox', { name: '캘린더 사용자' });
    await waitFor(() => expect(screen.getByRole('option', { name: 'other@example.test' })).toBeTruthy());
    expect((screen.getByRole('option', { name: 'legacy@example.test (사용자 ID 없음)' }) as HTMLOptionElement).disabled).toBe(true);
    await userEvent.selectOptions(users, 'uid:other-uid');
    await waitFor(() => expect(fetchSessionCalendar).toHaveBeenLastCalledWith(expect.objectContaining({ userId: 'other-uid', loginEmail: 'login@example.test' })));
    expect(vi.mocked(fetchSessionCalendar).mock.lastCall![0]).not.toHaveProperty('profileEmail');
    const keys = client.getQueryCache().findAll({ queryKey: ['session-calendar'] }).map(({ queryKey }) => queryKey[3]);
    expect(new Set(keys)).toEqual(new Set(['', 'uid:other-uid']));
  });

it('counts hidden sessions from source=all with the same filtered scope and refreshes per source', async () => {
    state.account = 'member@example.test';
    state.agent = 'codex';
    state.since = '2026-10-01T00:00:00Z';
    state.until = '2026-10-02T00:00:00Z';
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue({ token_rule_pending: false, step: '',
      session_analysis: { enabled: true }, session_records: { enabled: true, prompt_version: 1, state: 'running', paused_reason: '' } });
    vi.mocked(fetchProjects).mockResolvedValue([{
      project_hash: 'project-hash', project_name: 'Project A', git_remote_url: '', repository_id: 'github.com/example/repo',
      repository_name: 'repo', repo_subpath: 'worktree', updated_at: '2026-10-01T00:00:00Z', last_session_at: '2026-10-01T01:00:00Z',
    }]);
    const countResult = (count: number) => ({ count, unattributed: 0, classification_counts: buckets,
      judgement_bucket_counts: { done: 1, leftover: 2, ended_midway: 3, unknown: 4 } });
    vi.mocked(fetchSessionOverviewCount).mockImplementation(async (params) => {
      if (params?.source === 'all') return countResult(params.bucket ? 20 : 30);
      if (params?.source === 'headless') return countResult(params.bucket ? 8 : 15);
      return countResult(params?.bucket ? 5 : 10);
    });
    const { client } = mount();
    await userEvent.click(await screen.findByRole('button', { name: '세션 요약' }));

    await userEvent.type(await screen.findByRole('textbox', { name: '세션 검색' }), 'calendar');
    await userEvent.click(await screen.findByRole('tab', { name: /작업 중/ }));
    await userEvent.click(await screen.findByRole('button', { name: /종료.*11/ }));
    await userEvent.click(screen.getByRole('button', { name: '플랜 파일' }));
    expect(screen.queryByRole('button', { name: '하다 만 것 2' })).toBeNull();
    await waitFor(() => expect(fetchProjects).toHaveBeenCalled());
    fireEvent.click(screen.getByTestId('select-first-project'));

    await waitFor(() => expect(screen.getByTestId('hidden-session-count').textContent).toBe('20 hidden'));
    await waitFor(() => expect(fetchSessionOverviewCount).toHaveBeenCalledWith(expect.objectContaining({
      source: 'all', loginEmail: 'member@example.test', projectHashes: ['project-hash'], agent: 'codex', assembled: true,
      classification: 'working', since: state.since, until: state.until, analysis: true, query: 'calendar',
      category: 'ended', plan: 'file',
    }), expect.any(AbortSignal)));
    const currentScopeCount = vi.mocked(fetchSessionOverviewCount).mock.calls
      .map(([params]) => params)
      .find((params) => params?.source === 'interactive' && params.projectHashes?.[0] === 'project-hash');
    expect(currentScopeCount).toMatchObject({
      source: 'interactive', loginEmail: 'member@example.test', projectHashes: ['project-hash'], agent: 'codex', assembled: true,
      classification: 'working', since: state.since, until: state.until, analysis: true, query: 'calendar',
      category: 'ended', plan: 'file',
    });
    const scopedKeys = client.getQueryCache().findAll({ queryKey: ['session-overview-count'] })
      .map(({ queryKey }) => queryKey)
      .filter((key) => key.includes('hidden-source-all'));
    const interactiveKey = scopedKeys.find((key) => key.includes('interactive') && key.includes('project-hash'));
    expect(interactiveKey).toEqual(expect.arrayContaining([
      7, 'member@example.test', 'interactive', 'project-hash', 'codex', true, 'working', state.since, state.until,
      true, 'calendar', 'ended', 'file',
    ]));

    fireEvent.click(screen.getByTestId('select-headless'));
    await waitFor(() => expect(screen.getByTestId('hidden-session-count').textContent).toBe('15 hidden'));
    const headlessKey = client.getQueryCache().findAll({ queryKey: ['session-overview-count'] })
      .map(({ queryKey }) => queryKey)
      .find((key) => key.includes('hidden-source-all') && key.includes('headless'));
    expect(headlessKey).toBeTruthy();
    const allSourceReads = vi.mocked(fetchSessionOverviewCount).mock.calls.filter(([params]) => params?.source === 'all').length;
    await userEvent.click(screen.getByRole('button', { name: '세션 목록 다시 읽기' }));
    await waitFor(() => expect(vi.mocked(fetchSessionOverviewCount).mock.calls.filter(([params]) => params?.source === 'all').length).toBeGreaterThan(allSourceReads));

    fireEvent.click(screen.getByTestId('select-all'));
    await waitFor(() => expect(screen.getByTestId('source-selection').textContent).toBe('all'));
    expect(screen.getByTestId('hidden-session-count').textContent).toBe('0 hidden');
  });
});
