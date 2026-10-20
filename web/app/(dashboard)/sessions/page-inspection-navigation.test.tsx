// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClientProvider } from '@tanstack/react-query';
import { act, screen } from '@testing-library/react';
import { expect, it } from 'vitest';
import { mount, waitFor, state, SessionsPage } from './page-test-fixture';

it('keeps an administrator own-search independent of the account filter through inspection and conversation links', async () => {
  state.isAdmin = true; state.account = 'external@example.test';
  state.urlSearch = 'session_id=working-row&session_search=1&detail=inspection';
  mount();
  await waitFor(() => expect(state.viewerSearchTarget).toBe('working-row:'));
  act(() => state.onInspectRecord?.({ recordId: 'exact-tool' }));
  let params = new URL(state.replacedUrl, 'https://local.test').searchParams;
  expect(params.get('session_search')).toBe('1');
  expect(params.get('login_email')).toBe('');
  act(() => state.onConversationRecord?.({ recordId: 'exact-tool' }));
  params = new URL(state.replacedUrl, 'https://local.test').searchParams;
  expect(params.get('session_search')).toBe('1');
  expect(params.get('anchor')).toBe('exact-tool');
  expect(params.get('login_email')).toBe('');
});

it('treats a history URL without an anchor as authoritative after viewing another message in the same session', async () => {
  const origin = 'session_id=working-row&detail=inspection&inspect_record=origin';
  state.urlSearch = origin;
  const { client, view } = mount();
  await waitFor(() => expect(state.onConversationRecord).toBeTypeOf('function'));
  act(() => state.onConversationRecord?.({ recordId: 'inspected-B' }));
  expect(screen.getByTestId('viewer-anchor').textContent).toBe('inspected-B');
  state.urlSearch = origin;
  view.rerender(createElement(QueryClientProvider, { client }, createElement(SessionsPage)));
  expect(screen.getByTestId('viewer-anchor').textContent).toBe('');
});

it('clears old instruction and tool filters when trace opens another call', async () => {
  state.urlSearch = 'session_id=working-row&detail=trace&inspection_kind=context&inspection_tool=old-call';
  mount();
  await waitFor(() => expect(state.onInspectRecord).toBeTypeOf('function'));
  act(() => state.onInspectRecord?.({ recordId: 'new-record', callId: 'new-call' }, true));
  const params = new URL(state.replacedUrl, 'https://local.test').searchParams;
  expect(params.get('detail')).toBe('inspection');
  expect(params.get('inspect_record')).toBe('new-record');
  expect(params.get('inspect_call')).toBe('new-call');
  expect(params.get('inspection_kind')).toBe('all');
  expect(params.has('inspection_tool')).toBe(false);
});

it('opens the trace tab pinned to a request turn, clearing the old time range', async () => {
  state.isAdmin = true;
  state.account = 'external@example.test';
  state.urlSearch = 'session_id=working-row&detail=conversation&session_search=1&turn_key=old-turn&event_id=old-event';
  mount();
  await waitFor(() => expect(state.viewerSearchTarget).toBe('working-row:old-turn'));
  act(() => state.onOpenTraceTurn?.('turn-18'));
  const params = new URL(state.replacedUrl, 'https://local.test').searchParams;
  expect(params.get('detail')).toBe('trace');
  expect(params.get('session_id')).toBe('working-row');
  expect(params.get('turn_key')).toBe('turn-18');
  expect(params.has('trace_from')).toBe(false);
  expect(params.has('trace_to')).toBe(false);
  expect(params.has('event_id')).toBe(false);
  expect(params.get('login_email')).toBe('');
});
