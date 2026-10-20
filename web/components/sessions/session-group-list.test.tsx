// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SessionOverview } from '@/lib/types';
import type { SessionGroups } from '@/lib/session-groups-api';
import { partitionSessionGroups, SessionGroupList } from './session-group-list';

const row = (session_id: string): SessionOverview => ({
  session_id, profile_email: 'owner@example.test', model: '', agent: 'claude',
  start_time: '2026-10-08T00:00:00Z', end_time: '2026-10-08T01:00:00Z',
  input_tokens: 1, output_tokens: 2, cost_usd: 0.1, event_count: 1, has_sync: true,
});
const item = (session_id: string, dismissed = false) => ({ session_id, ended_at: '2026-10-08T01:00:00Z', dismissed });
const groups = (truncated = false): SessionGroups => ({
  scope: 'filtered_total', grouping_version: 1, truncated, overview_total: 120,
  running: [{ session_id: 'running', category: 'running', bucket: 'processing' }],
  ended: [{ key: 'server-cluster', ended_at: '2026-10-08T01:00:00Z', total: 3, items: [item('ended'), item('not-loaded'), item('dismissed', true)] }],
  counts: { running: 4, ended: 8, ended_groups: 3, running_by: { processing: 2, done: 2 } },
});

afterEach(cleanup);

describe('server session groups in the loaded list', () => {
  it('partitions only loaded members and uses server membership instead of JSONL end times', () => {
    const rows = [row('rest'), row('ended'), row('running'), row('dismissed')];
    const result = partitionSessionGroups(rows, groups());
    expect(result.running.map(({ session_id }) => session_id)).toEqual(['running']);
    expect(result.ended[0].members.map(({ row: { session_id } }) => session_id)).toEqual(['ended']);
    expect(result.rest.map(({ session_id }) => session_id)).toEqual(['rest', 'dismissed']);
    expect(partitionSessionGroups(rows).rest).toEqual(rows);
  });

  it('shows distinct full counts and loaded counts, and acts with the server member epoch', async () => {
    const onDismiss = vi.fn();
    const onRestore = vi.fn();
    render(<SessionGroupList rows={[row('ended'), row('dismissed'), row('running')]} groups={groups()} busy={false} onDismiss={onDismiss} onRestore={onRestore} renderRow={(entry, action) => <div key={entry.session_id}>{entry.session_id}{action}</div>} />);
    expect(screen.getByText(/전체 범위 120건 · 실행 4건 · 종료 8건 · 현재 목록 3건/)).toBeTruthy();
    expect(screen.getByText(/현재 목록 1건 표시 · 서버 반환 3건/)).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: '떼기' }));
    expect(onDismiss).toHaveBeenCalledWith([item('ended')], groups().ended[0]);
    await userEvent.click(screen.getByRole('button', { name: '묶음 복원' }));
    expect(onRestore).toHaveBeenCalledWith([item('dismissed', true)], groups().ended[0]);
    await userEvent.click(screen.getByRole('button', { name: '한꺼번에 떼기' }));
    expect(onDismiss).toHaveBeenLastCalledWith([item('ended'), item('not-loaded')], groups().ended[0], true);
  });

  it('keeps the retry action when all loaded members are dismissed but the group has unseen members', async () => {
    const onDismiss = vi.fn();
    const partial = groups(true);
    partial.ended[0].total = 201;
    partial.ended[0].items = [item('ended', true)];
    render(<SessionGroupList rows={[row('ended')]} groups={partial} busy={false} onDismiss={onDismiss} onRestore={vi.fn()} renderRow={(entry, action) => <div key={entry.session_id}>{entry.session_id}{action}</div>} />);
    expect(screen.getByText(/현재 목록 0건 표시 · 서버 반환 1건/)).toBeTruthy();
    expect(screen.getByRole('button', { name: '묶음 복원' })).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: '한꺼번에 떼기' }));
    expect(onDismiss).toHaveBeenCalledWith([], partial.ended[0], true);
  });

  it('keeps the server row order once each when an analysis filter or sort is active', async () => {
    const onDismiss = vi.fn();
    const onRestore = vi.fn();
    const ordered: SessionGroups = {
      scope: 'filtered_total', grouping_version: 1, truncated: false, overview_total: 40,
      running: [{ session_id: 'A', category: 'running', bucket: 'processing' }],
      ended: [{ key: 'ab', ended_at: '2026-10-08T01:00:00Z', total: 5, items: [item('A'), item('B'), item('unloaded'), item('D', true)] }],
      counts: { running: 6, ended: 9, ended_groups: 2, running_by: { processing: 6 } },
    };
    render(<SessionGroupList preserveOrder rows={[row('C'), row('A'), row('D'), row('B')]} groups={ordered} busy={false} onDismiss={onDismiss} onRestore={onRestore}
      renderRow={(entry, action) => <div key={entry.session_id} data-testid="row">{entry.session_id}{action}</div>} />);
    expect(screen.getAllByTestId('row').map((node) => node.firstChild?.textContent)).toEqual(['C', 'A', 'D', 'B']);
    expect(screen.getByText(/전체 범위 40건 · 실행 6건 · 종료 9건 · 현재 목록 4건/)).toBeTruthy();
    expect(screen.getByRole('group', { name: '함께 끝난 세션 5건' })).toBeTruthy();
    expect(screen.getByText(/현재 목록 2건 표시 · 서버 반환 4건/)).toBeTruthy();
    expect(screen.getAllByText(/현재 검색·플랜·범주·기간·프로젝트·계정 필터가 묶음/).length).toBeGreaterThan(0);
    const dismissButtons = screen.getAllByRole('button', { name: '떼기' });
    expect(dismissButtons).toHaveLength(2);
    await userEvent.click(dismissButtons[0]);
    expect(onDismiss).toHaveBeenLastCalledWith([item('A')], ordered.ended[0]);
    await userEvent.click(dismissButtons[1]);
    expect(onDismiss).toHaveBeenLastCalledWith([item('B')], ordered.ended[0]);
    await userEvent.click(screen.getByRole('button', { name: '묶음 복원' }));
    expect(onRestore).toHaveBeenCalledWith([item('D', true)], ordered.ended[0]);
    await userEvent.click(screen.getByRole('button', { name: '한꺼번에 떼기' }));
    expect(onDismiss).toHaveBeenLastCalledWith([item('A'), item('B'), item('unloaded')], ordered.ended[0], true);
  });

  it('keeps the header and group-wide action in server order mode when no group row is loaded', async () => {
    const onDismiss = vi.fn();
    render(<SessionGroupList preserveOrder rows={[row('rest')]} groups={groups()} busy={false} onDismiss={onDismiss} onRestore={vi.fn()}
      renderRow={(entry, action) => <div key={entry.session_id}>{entry.session_id}{action}</div>} />);
    expect(screen.getByRole('group', { name: '함께 끝난 세션 3건' })).toBeTruthy();
    expect(screen.getByText(/현재 목록 0건 표시 · 서버 반환 3건/)).toBeTruthy();
    expect(screen.getByText(/현재 분석 필터 모집단에서 계산/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: '떼기' })).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: '한꺼번에 떼기' }));
    expect(onDismiss).toHaveBeenCalledWith([item('ended'), item('not-loaded')], groups().ended[0], true);
  });

  it('offers a group-wide action from a capped overview only after server paging', async () => {
    const onDismiss = vi.fn();
    const capped = groups(true);
    capped.ended[0].total = 20;
    render(<SessionGroupList rows={[row('ended')]} groups={capped} busy={false} onDismiss={onDismiss} onRestore={vi.fn()} renderRow={(entry) => <div key={entry.session_id}>{entry.session_id}</div>} />);
    expect(screen.getByText(/서버에서 나머지 멤버를 확인한 뒤/)).toBeTruthy();
    expect(screen.getByText(/서버 반환 한도 도달, 일부 묶음 미표시/)).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: '한꺼번에 떼기' }));
    expect(onDismiss).toHaveBeenCalledWith([item('ended'), item('not-loaded')], capped.ended[0], true);
  });
});
