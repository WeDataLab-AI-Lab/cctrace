// @vitest-environment jsdom
import { mount, waitFor, state, SessionsPage } from './page-test-fixture';
import { act, fireEvent, screen } from '@testing-library/react';
import { createElement } from 'react';
import { QueryClientProvider } from '@tanstack/react-query';
import { expect, it, vi } from 'vitest';
import { fetchCompleteSessionGroup, fetchSessionGroups, updateSessionGroupDismissals } from '@/lib/session-groups-api';

it('collapses the navigation independently and preserves its preference after expansion and Escape', async () => {
  mount(undefined, false, true);
  await waitFor(() => expect(screen.getByTestId('viewer-session').textContent).toBe('working-row'));
  const instance = screen.getByTestId('viewer-instance').textContent;
  fireEvent.change(screen.getByRole('textbox', { name: '세션 검색' }), { target: { value: 'row' } });
  fireEvent.click(screen.getByRole('button', { name: '세션 목록 접기' }));
  expect(document.getElementById('session-navigation')?.hidden).toBe(true);
  fireEvent.click(screen.getByRole('button', { name: '상세 영역 확대' }));
  expect(screen.queryByRole('textbox', { name: '세션 검색' })).toBeNull();
  const popup = document.createElement('div');
  popup.setAttribute('role', 'dialog');
  document.body.append(popup);
  fireEvent.keyDown(document, { key: 'Escape' });
  expect(screen.getByRole('button', { name: '상세 영역 복원' })).toBeTruthy();
  popup.remove();
  fireEvent.keyDown(document, { key: 'Escape' });
  expect(screen.getByRole('button', { name: '상세 영역 확대' })).toBeTruthy();
  expect(document.getElementById('session-navigation')?.hidden).toBe(true);
  expect(screen.getByRole('textbox', { name: '세션 검색' })).toHaveProperty('value', 'row');
  fireEvent.click(screen.getByRole('button', { name: '세션 목록 펼치기' }));
  expect(document.getElementById('session-navigation')?.hidden).toBe(false);
  expect(screen.getByTestId('viewer-instance').textContent).toBe(instance);
});

it('opens a legacy analysis selection outside the list in its explicit account scope', async () => {
  state.isAdmin = true;
  state.account = 'different@test';
  state.urlSearch = 'session_id=outside&detail=analysis&expanded=1&login_email=scope%40test&profile_email=profile%40test&turn_key=T&event_id=E&trace_from=start&trace_to=end';
  mount(undefined, false, true);
  await waitFor(() => expect(screen.getByTestId('viewer-search-target').textContent).toBe('outside:T'));
  expect(screen.getByTestId('viewer-tab').textContent).toBe('trace');
  expect(state.analysisLoginEmail).toBe('scope@test');
  expect(screen.getByRole('button', { name: '상세 영역 복원' })).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: '상세 영역 복원' }));
  const params = new URL(state.replacedUrl, 'https://local.test').searchParams;
  expect(params.get('event_id')).toBe('E');
  expect(params.get('trace_from')).toBe('start');
  expect(params.get('trace_to')).toBe('end');
  expect(params.has('from')).toBe(false);
});

it('stops remaining group writes when the authenticated shell unmounts', async () => {
  const members = Array.from({ length: 101 }, (_, index) => ({ session_id: `member-${index}`, ended_at: '2026-10-01T01:00:00Z', dismissed: false }));
  let resolveAck!: () => void;
  vi.mocked(fetchSessionGroups).mockResolvedValue({ scope: 'filtered_total', grouping_version: 1, truncated: true, overview_total: 101,
    running: [], ended: [{ key: 'old-group', ended_at: members[0].ended_at, total: 101, items: [members[0]] }],
    counts: { running: 0, ended: 101, ended_groups: 1, running_by: {} } });
  vi.mocked(fetchCompleteSessionGroup).mockResolvedValue(members);
  vi.mocked(updateSessionGroupDismissals).mockImplementationOnce(() => new Promise(resolve => { resolveAck = resolve; }));
  const { view } = mount(undefined, false, true);
  fireEvent.click(await screen.findByRole('button', { name: '한꺼번에 떼기' }));
  await waitFor(() => expect(updateSessionGroupDismissals).toHaveBeenCalledTimes(1));
  view.unmount();
  await act(async () => { resolveAck(); });
  expect(updateSessionGroupDismissals).toHaveBeenCalledTimes(1);
});

it('clears the previous trace selection on a different session and records the current analysis scope in the URL', async () => {
  state.isAdmin = true;
  state.account = 'scope@test';
  state.urlSearch = 'session_id=working-row&detail=analysis&turn_key=old-turn&event_id=old-event&trace_from=start&trace_to=end';
  const { client, view } = mount(undefined, false, true);
  await waitFor(() => expect(screen.getByTestId('viewer-session').textContent).toBe('working-row'));
  fireEvent.click(await screen.findByRole('button', { name: /other-ro/ }));
  fireEvent.click(screen.getByRole('button', { name: '트레이스' }));
  view.rerender(createElement(QueryClientProvider, { client }, createElement(SessionsPage)));
  const params = new URL(state.replacedUrl, 'https://local.test').searchParams;
  expect(params.get('session_id')).toBe('other-row');
  expect(params.get('detail')).toBe('trace');
  expect(params.get('login_email')).toBe('scope@test');
  for (const key of ['turn_key', 'event_id', 'trace_from', 'trace_to']) expect(params.has(key)).toBe(false);
  expect(screen.getByTestId('viewer-tab').textContent).toBe('trace');
});

it('clears trace navigation state when selecting a different session', async () => {
  state.isAdmin = true;
  state.account = 'scope@test';
  state.urlSearch = 'session_id=working-row&detail=trace&login_email=scope%40test&event_id=old-node&turn_key=old-turn&trace_from=start&trace_to=end&trace_group=old-group&trace_view=tree&trace_pane=detail&trace_expanded=old-root&trace_usage_expanded=old-group&trace_cursor=root-cursor&trace_branch_cursors=%7B%22old-root%22%3A%22child-cursor%22%7D&trace_q=old-search&trace_request_no=5&trace_compare=old-node&trace_locate=old-node';
  mount();
  await waitFor(() => expect(screen.getByTestId('viewer-session').textContent).toBe('working-row'));

  fireEvent.click(await screen.findByRole('button', { name: /other-ro/ }));

  const params = new URL(state.replacedUrl, 'https://local.test').searchParams;
  expect(params.get('session_id')).toBe('other-row');
  expect(params.get('detail')).toBe('trace');
  expect(params.get('login_email')).toBe('scope@test');
  for (const key of ['event_id', 'turn_key', 'trace_from', 'trace_to', 'trace_group', 'trace_view', 'trace_pane', 'trace_expanded', 'trace_usage_expanded', 'trace_cursor', 'trace_branch_cursors', 'trace_q', 'trace_request_no', 'trace_compare', 'trace_locate']) {
    expect(params.has(key), key).toBe(false);
  }
});

it('opens the explicit trace route with its selected event and range', async () => {
  state.urlSearch = 'session_id=working-row&detail=trace&event_id=tool-event&trace_from=start&trace_to=end';
  mount();
  await waitFor(() => expect(screen.getByTestId('viewer-session').textContent).toBe('working-row'));
  expect(screen.getByTestId('viewer-tab').textContent).toBe('trace');
});
