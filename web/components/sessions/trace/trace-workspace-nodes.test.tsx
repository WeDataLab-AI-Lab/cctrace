// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import type { TraceNode, TraceNodeDetail } from '@/lib/session-trace-node-types';
import { SessionTraceWorkspace } from './trace-workspace';

const state = vi.hoisted(() => ({
  query: '', replace: vi.fn(), push: vi.fn(), detailCalls: [] as { enabled: boolean; nodeId?: string }[], listCalls: [] as { enabled: boolean; parentId?: string; view?: string; sessionId?: string }[],
  branches: {} as Record<string, { nodes: TraceNode[]; isPending: boolean; isError: boolean; hasNextPage: boolean; fetchNextPage: ReturnType<typeof vi.fn>; refresh: ReturnType<typeof vi.fn>; meta?: { revision?: number; epoch?: number } }>,
  detail: undefined as TraceNodeDetail | undefined,
}));

const rootNode: TraceNode = {
  node_id: 'turn-1', parent_id: null, kind: 'request', title: '검토 요청', timestamp: '2026-10-18T08:32:14.000Z',
  started_at: null, ended_at: null, status: 'completed', has_children: true, child_count: 1, source_ref: 'request-1', evidence: ['session_log'], availability: 'observed',
};
const childNode: TraceNode = {
  node_id: 'tool-1', parent_id: 'turn-1', kind: 'tool', title: 'go test ./internal/sessiontrace', timestamp: '2026-10-18T08:32:39.000Z',
  started_at: '2026-10-18T08:32:18.000Z', ended_at: '2026-10-18T08:32:39.000Z', status: 'error', has_children: false, child_count: 0, source_ref: 'call-1', evidence: ['tool_call'], availability: 'observed',
};
const usageNode: TraceNode = {
  node_id: 'usage-1', parent_id: 'turn-1', kind: 'usage', title: '사용량 관측', timestamp: '2026-10-18T08:32:40.000Z',
  started_at: null, ended_at: null, status: 'unknown', has_children: false, child_count: null, source_ref: 'usage-1', evidence: ['session_log'], availability: 'observed',
};
const detail: TraceNodeDetail = {
  session_id: 'A', node: childNode, input: { state: 'observed', value: { command: 'go test ./internal/sessiontrace' } },
  output: { state: 'not_collected' }, context: { state: 'not_queried' }, usage: null, raw_records: [], notices: [],
};

vi.mock('next/navigation', () => ({
  useSearchParams: () => new URLSearchParams(state.query), usePathname: () => '/sessions/',
  useRouter: () => ({ replace: state.replace, push: state.push }),
}));
vi.mock('@/components/common/auth-context', () => ({ useAuth: () => ({ user: { id: 7 }, isAdmin: false }) }));
vi.mock('@/components/common/account-context', () => ({ useAccount: () => ({ selectedAccount: '' }) }));
vi.mock('@/lib/use-session-trace', () => ({ usePageVisible: () => true, useSessionTrace: () => ({ overview: {}, sample: {}, events: {}, context: {}, retry: vi.fn() }) }));
vi.mock('@/lib/use-turn-context', () => ({ useTurnContext: () => ({ records: [], loading: false, refetch: vi.fn(), fetchNextPage: vi.fn() }) }));
vi.mock('@/lib/use-trace-nodes', () => ({
  traceNodesKey: () => ['trace-nodes'],
  traceNodesViewKey: () => ['trace-nodes', 'children', 'execution'],
  useTraceNodes: (options: { identity: { sessionId: string }; enabled: boolean; parentId?: string; view?: string }) => {
    state.listCalls.push({ ...options, sessionId: options.identity.sessionId });
    return { ...state.branches[options.parentId ?? 'root'], coverage: undefined, isLoading: false, isFetchingNextPage: false, refresh: vi.fn() };
  },
  useTraceNodeDetail: (options: { enabled: boolean; nodeId?: string }) => {
    state.detailCalls.push({ ...options, enabled: options.enabled && !!options.nodeId });
    return { data: state.detail, isPending: false, isError: false, refetch: vi.fn() };
  },
}));

beforeEach(() => {
  state.query = 'session_id=A&detail=analysis&from=session-list&trace_view=tree';
  state.replace.mockReset().mockImplementation((url: string) => { state.query = url.split('?')[1] ?? ''; });
  state.push.mockReset().mockImplementation((url: string) => { state.query = url.split('?')[1] ?? ''; });
  state.detailCalls = [];
  state.listCalls = [];
  state.branches = {
    root: { nodes: [rootNode], isPending: false, isError: false, hasNextPage: false, fetchNextPage: vi.fn(), refresh: vi.fn() },
    'turn-1': { nodes: [childNode, usageNode], isPending: false, isError: false, hasNextPage: false, fetchNextPage: vi.fn(), refresh: vi.fn() },
  };
  state.detail = detail;
});
afterEach(() => cleanup());

const renderWorkspace = (props: Partial<React.ComponentProps<typeof SessionTraceWorkspace>> = {}) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return { client, ...render(createElement(QueryClientProvider, { client }, createElement(SessionTraceWorkspace, { sessionId: 'A', ...props }))) };
};

it('uses node ids as the event_id alias and preserves session filters while selecting a child', () => {
  state.query += '&trace_expanded=turn-1&trace_expanded=keep';
  const onSelectNode = vi.fn();
  renderWorkspace({ onSelectNode });
  fireEvent.click(screen.getByRole('button', { name: '검토 요청 하위 실행 접기' }));
  const collapsed = new URL(state.replace.mock.calls.at(-1)![0], 'https://test.invalid');
  expect(collapsed.searchParams.get('from')).toBe('session-list');
  expect(collapsed.searchParams.getAll('trace_expanded')).toEqual(['keep']);

  state.query = 'session_id=A&detail=analysis&from=session-list&trace_view=tree&trace_expanded=turn-1&trace_from=2026-10-18T08%3A32%3A00Z&trace_to=2026-10-18T08%3A33%3A00Z';
  cleanup();
  const workspace = renderWorkspace({ onSelectNode });
  state.replace.mockClear();
  fireEvent.click(screen.getByRole('button', { name: 'go test ./internal/sessiontrace' }));
  workspace.rerender(createElement(QueryClientProvider, { client: workspace.client }, createElement(SessionTraceWorkspace, { sessionId: 'A', onSelectNode })));
  expect(state.detailCalls.at(-1)).toMatchObject({ enabled: true, nodeId: 'tool-1' });
  const selection = new URL(state.replace.mock.calls.at(-1)![0], 'https://test.invalid');
  expect(selection.searchParams.get('event_id')).toBe('tool-1');
  expect(selection.searchParams.get('detail')).toBe('trace');
  expect(selection.searchParams.get('from')).toBe('session-list');
  expect(selection.searchParams.get('trace_expanded')).toBe('turn-1');
  expect(selection.searchParams.has('trace_from')).toBe(false);
  expect(selection.searchParams.has('trace_to')).toBe(false);
  expect(onSelectNode).toHaveBeenCalledTimes(1);
  expect(state.replace).toHaveBeenCalledTimes(1);
});

it('does not apply the previous session URL selection during a fallback session render', () => {
  state.query = 'session_id=A&detail=trace&login_email=scope%40test&profile_email=profile%40test&user_id=member-1&filter=preserve&event_id=tool-1&trace_view=timeline&trace_group=old-turn&trace_expanded=old-root&trace_cursor=old-cursor';
  const workspace = renderWorkspace();
  expect(state.detailCalls.at(-1)).toMatchObject({ enabled: true, nodeId: 'tool-1' });

  state.detailCalls = [];
  state.listCalls = [];
  workspace.rerender(createElement(QueryClientProvider, { client: workspace.client }, createElement(SessionTraceWorkspace, { sessionId: 'B' })));

  expect(state.detailCalls.at(-1)).toMatchObject({ enabled: false, nodeId: undefined });
  expect(state.listCalls.at(-1)).toMatchObject({ enabled: true, parentId: undefined, view: 'flow', sessionId: 'B' });
  expect(screen.getByRole('button', { name: '검토 요청 하위 실행 펼치기' })).toBeTruthy();
  expect(state.replace).not.toHaveBeenCalled();

  fireEvent.click(screen.getByRole('button', { name: '검토 요청 하위 실행 펼치기' }));
  const expanded = new URL(state.replace.mock.calls.at(-1)![0], 'https://test.invalid');
  expect(expanded.searchParams.get('session_id')).toBe('B');
  expect(expanded.searchParams.get('login_email')).toBe('scope@test');
  expect(expanded.searchParams.get('profile_email')).toBe('profile@test');
  expect(expanded.searchParams.get('user_id')).toBe('member-1');
  expect(expanded.searchParams.get('filter')).toBe('preserve');
  expect(expanded.searchParams.getAll('trace_expanded')).toEqual(['turn-1']);
  for (const key of ['event_id', 'trace_view', 'trace_group', 'trace_cursor']) expect(expanded.searchParams.has(key)).toBe(false);
});

it('insets every trace pane inside the session frame', () => {
  const { container } = renderWorkspace({ inspectionPanel: <div>inspection</div> });
  const inset = container.querySelector('[data-trace-pane-inset="true"]');
  expect(inset?.className).toContain('p-2');
  expect(inset?.className).toContain('gap-2');
  expect(inset?.children.length).toBeGreaterThan(1);
});

it('opens timing from the selected detail and returns without losing its request, expansion or filters', () => {
  state.query += '&event_id=tool-1&trace_expanded=turn-1&trace_expanded=keep&trace_pane=detail';
  state.detail = { ...detail, node: { ...childNode, group_id: 'turn-1' }, navigation: { state: 'observed', group_id: 'turn-1', ancestor_ids: [], root_node_id: 'turn-1', revision: 1, epoch: 0 } };
  const workspace = renderWorkspace();
  expect(state.listCalls.at(-1)).toMatchObject({ view: 'flow' });
  expect(screen.queryByRole('button', { name: '지시문·설정' })).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: '시간 분석' }));
  const pushed = new URL(state.push.mock.calls.at(-1)![0], 'https://test.invalid');
  expect(pushed.searchParams.get('trace_group')).toBe('turn-1');
  expect(pushed.searchParams.get('trace_view')).toBe('timeline');
  expect(pushed.searchParams.has('trace_pane')).toBe(false);
  expect(pushed.searchParams.get('event_id')).toBe('tool-1');
  expect(pushed.searchParams.getAll('trace_expanded')).toEqual(['turn-1', 'keep']);
  workspace.rerender(createElement(QueryClientProvider, { client: workspace.client }, createElement(SessionTraceWorkspace, { sessionId: 'A' })));
  expect(state.listCalls.some(call => call.view === 'timing')).toBe(true);
  fireEvent.click(screen.getByRole('button', { name: '요청으로 돌아가기' }));
  const returned = new URL(state.push.mock.calls.at(-1)![0], 'https://test.invalid');
  expect(returned.searchParams.get('trace_view')).toBe('tree');
  expect(returned.searchParams.get('event_id')).toBe('tool-1');
  expect(returned.searchParams.get('from')).toBe('session-list');
});

it('disables root and detail reads while the retained trace tab is inactive', () => {
  renderWorkspace({ enabled: false, inspectionActive: true, inspectionPanel: createElement('div', null, '기존 기록 검사') });
  expect(state.listCalls.at(-1)).toMatchObject({ enabled: false });
  expect(state.detailCalls.at(-1)).toMatchObject({ enabled: false });
});

it('opens the parent branch and usage group for a directly selected usage node without losing its selection', () => {
  state.query = 'session_id=A&detail=trace&event_id=usage-1&trace_view=tree&from=session-list';
  state.detail = { ...detail, node: usageNode };
  const workspace = renderWorkspace();
  const opened = new URL(state.replace.mock.calls.at(-1)![0], 'https://test.invalid');
  expect(opened.searchParams.get('event_id')).toBe('usage-1');
  expect(opened.searchParams.get('from')).toBe('session-list');
  expect(opened.searchParams.getAll('trace_expanded')).toContain('turn-1');
  expect(opened.searchParams.getAll('trace_usage_expanded')).toContain('turn-1');

  workspace.rerender(createElement(QueryClientProvider, { client: workspace.client }, createElement(SessionTraceWorkspace, { sessionId: 'A' })));
  expect(screen.getByRole('button', { name: '사용량 기록 · 읽은 범위 1건' }).getAttribute('aria-expanded')).toBe('true');
  expect(screen.getByRole('button', { name: '사용량 관측' }).getAttribute('aria-pressed')).toBe('true');

  fireEvent.click(screen.getByRole('button', { name: '검토 요청 하위 실행 접기' }));
  const collapsed = new URL(state.replace.mock.calls.at(-1)![0], 'https://test.invalid');
  expect(collapsed.searchParams.getAll('trace_expanded')).not.toContain('turn-1');
  workspace.rerender(createElement(QueryClientProvider, { client: workspace.client }, createElement(SessionTraceWorkspace, { sessionId: 'A' })));
  expect(screen.getByRole('button', { name: '검토 요청 하위 실행 펼치기' }).getAttribute('aria-expanded')).toBe('false');
});

it('stores usage-group expansion in a repeatable URL parameter while preserving the selected node and session filters', () => {
  state.query = 'session_id=A&detail=trace&event_id=tool-1&from=session-list&trace_expanded=turn-1';
  const workspace = renderWorkspace();
  fireEvent.click(screen.getByRole('button', { name: '사용량 기록 · 읽은 범위 1건' }));
  const expanded = new URL(state.replace.mock.calls.at(-1)![0], 'https://test.invalid');
  expect(expanded.searchParams.get('event_id')).toBe('tool-1');
  expect(expanded.searchParams.get('from')).toBe('session-list');
  expect(expanded.searchParams.get('trace_expanded')).toBe('turn-1');
  expect(expanded.searchParams.getAll('trace_usage_expanded')).toEqual(['turn-1']);

  workspace.rerender(createElement(QueryClientProvider, { client: workspace.client }, createElement(SessionTraceWorkspace, { sessionId: 'A' })));
  expect(screen.getByRole('button', { name: '사용량 기록 · 읽은 범위 1건' }).getAttribute('aria-expanded')).toBe('true');
  fireEvent.click(screen.getByRole('button', { name: '사용량 기록 · 읽은 범위 1건' }));
  const collapsed = new URL(state.replace.mock.calls.at(-1)![0], 'https://test.invalid');
  expect(collapsed.searchParams.getAll('trace_usage_expanded')).toEqual([]);
});

it('does not reuse the previous timing group after selecting a different request', () => {
  state.query = 'session_id=A&detail=trace&event_id=tool-1&trace_group=old-turn&trace_view=tree';
  state.detail = { ...detail, node: { ...childNode, group_id: 'turn-1' } };
  renderWorkspace();
  fireEvent.click(screen.getByRole('button', { name: '시간 분석' }));
  const pushed = new URL(state.push.mock.calls.at(-1)![0], 'https://test.invalid');
  expect(pushed.searchParams.get('trace_group')).toBe('turn-1');
});

it('restores ancestor child cursors for a selected node outside the first branch page', () => {
  state.query = 'session_id=A&detail=trace&event_id=tool-450';
  state.detail = { ...detail, node: { ...childNode, node_id: 'tool-450', group_id: 'turn-1' }, navigation: { state: 'resolved', group_id: 'turn-1', ancestor_ids: ['request-1'], root_node_id: 'turn-1', root_cursor: 'root-locate', child_cursors: { 'turn-1': 'request-locate', 'request-1': 'tool-locate' }, revision: 1, epoch: 0 } };
  renderWorkspace();
  fireEvent.click(screen.getByRole('button', { name: '트리에서 찾기' }));
  const located = new URL(state.replace.mock.calls.at(-1)![0], 'https://test.invalid');
  expect(JSON.parse(located.searchParams.get('trace_branch_cursors')!)).toEqual({ 'turn-1': 'request-locate', 'request-1': 'tool-locate' });
  expect(located.searchParams.getAll('trace_expanded')).toEqual(['turn-1', 'request-1']);
  expect(located.searchParams.get('event_id')).toBe('tool-450');
});

it('restores disclosures before the selected body reading position after reauthorization', () => {
  state.query = 'session_id=A&detail=trace&event_id=tool-1';
  const workspace = renderWorkspace();
  const disclosure = screen.getByText('출처와 연결 근거').closest('details')!;
  disclosure.open = true;
  fireEvent(disclosure, new Event('toggle', { bubbles: true }));
  const panel = screen.getByRole('tabpanel', { name: '입력·결과' });
  panel.scrollTop = 180;
  fireEvent.scroll(panel);
  state.detail = undefined;
  workspace.rerender(createElement(QueryClientProvider, { client: workspace.client }, createElement(SessionTraceWorkspace, { sessionId: 'A' })));
  state.detail = detail;
  workspace.rerender(createElement(QueryClientProvider, { client: workspace.client }, createElement(SessionTraceWorkspace, { sessionId: 'A' })));
  expect(screen.getByText('출처와 연결 근거').closest('details')!.open).toBe(true);
  expect(screen.getByRole('tabpanel', { name: '입력·결과' }).scrollTop).toBe(180);
});

it('pushes exact source drilldown so browser Back can restore the selected request', () => {
  state.query = 'session_id=A&detail=trace&event_id=request-1&from=session-list';
  state.detail = { ...detail, node: { ...childNode, node_id: 'request-1', kind: 'request' }, source_locations: [{ source_ref: 'blob-ref', node_id: 'raw-node', raw_path: 'message.content[0]', state: 'observed' }], request: { state: 'observed', digest: { state: 'not_checked' }, missing_refs: [], source_refs: ['blob-ref'], input_page: { items: [{ request_path: 'messages[0].content[0]', kind: 'message', role: 'user', value: 'actual prompt', state: 'observed', source_refs: ['blob-ref'] }], next_cursor: null, digest: { state: 'not_checked' }, coverage: 'page' } } };
  renderWorkspace();
  fireEvent.click(screen.getByText('원자료 근거 1건'));
  fireEvent.click(screen.getByRole('button', { name: '원문 열기 · blob-ref' }));
  const target = new URL(state.push.mock.calls.at(-1)![0], 'https://test.invalid');
  expect(target.searchParams.get('event_id')).toBe('raw-node');
  expect(target.searchParams.get('from')).toBe('session-list');
});

it('passes observed detail stamps to the root notice and clears only page positions on explicit reload', () => {
 state.query='session_id=A&detail=trace&event_id=tool-1&from=session-list&trace_q=Read&trace_expanded=turn-1&trace_expanded=keep&trace_cursor=old-root&trace_branch_cursors=%7B%22turn-1%22%3A%22old-child%22%7D';
 state.branches.root.meta={revision:1,epoch:0};
 state.detail={...detail,navigation:{group_id:'turn-1',ancestor_ids:[],root_node_id:'turn-1',state:'resolved',revision:2,epoch:0}};
 renderWorkspace();
 expect(screen.getByText('기록이 갱신되었습니다')).toBeTruthy();
 expect(state.replace).not.toHaveBeenCalled();
 fireEvent.click(screen.getByRole('button',{name:'다시 읽기'}));
 const after=new URL(state.replace.mock.calls.at(-1)![0],'https://test.invalid');
 expect(after.searchParams.has('trace_cursor')).toBe(false);
 expect(after.searchParams.has('trace_branch_cursors')).toBe(false);
 expect(after.searchParams.get('event_id')).toBe('tool-1');
 expect(after.searchParams.get('trace_q')).toBe('Read');
 expect(after.searchParams.getAll('trace_expanded')).toEqual(['turn-1','keep']);
 expect(after.searchParams.get('from')).toBe('session-list');
});
