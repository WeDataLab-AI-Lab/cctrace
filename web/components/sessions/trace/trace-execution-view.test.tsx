// @vitest-environment jsdom
import { createElement, type ComponentProps } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import type { TraceNode } from '@/lib/session-trace-node-types';
import { TraceExecutionView } from './trace-execution-view';

const hookState = vi.hoisted(() => ({ branches: {} as Record<string, unknown> }));
vi.mock('@/lib/use-trace-nodes', async importOriginal => ({
  ...await importOriginal<typeof import('@/lib/use-trace-nodes')>(),
  useTraceNodes: ({ parentId }: { parentId?: string }) => hookState.branches[parentId ?? 'root'],
}));

const rootNode: TraceNode = {
  node_id: 'turn-1', parent_id: null, kind: 'request', title: '검토 계획 요청', timestamp: '2026-10-18T08:32:14.000Z',
  started_at: null, ended_at: null, status: 'completed', has_children: true, child_count: 1, source_ref: 'request-ref', evidence: [], availability: 'collected',
};
const childNode: TraceNode = {
  node_id: 'tool-1', parent_id: 'turn-1', kind: 'tool', title: 'go test ./internal/serverpolicy', timestamp: '2026-10-18T08:32:39.000Z',
  started_at: '2026-10-18T08:32:18.000Z', ended_at: '2026-10-18T08:32:39.000Z', status: 'error', has_children: false, child_count: 0, source_ref: 'call-1', evidence: ['tool_call', 'tool_result'], availability: 'collected',
};
const usageNode: TraceNode = {
  node_id: 'usage-1', parent_id: 'turn-1', kind: 'usage', title: '사용량 관측', timestamp: '2026-10-18T08:32:40.000Z',
  started_at: null, ended_at: null, status: 'unknown', has_children: false, child_count: null, source_ref: 'usage-ref', evidence: ['session_log'], availability: 'observed',
};

const branch = (nodes: TraceNode[], overrides: Record<string, unknown> = {}) => ({
  nodes, coverage: undefined, isPending: false, isLoading: false, isError: false, hasNextPage: false,
  isFetchingNextPage: false, fetchNextPage: vi.fn(), refresh: vi.fn(), ...overrides,
});

const setup = (overrides: Partial<ComponentProps<typeof TraceExecutionView>> = {}) => {
  const props: ComponentProps<typeof TraceExecutionView> = {
    identity: { userId: 7, access: 'owner', sessionId: 'session-1' },
    enabled: true,
    nodesView: 'execution',
    selectedNodeId: undefined,
    viewMode: 'tree',
    expandedNodeIds: [],
    expandedUsageGroupIds: [],
    onViewModeChange: vi.fn(),
    onNodesViewChange: vi.fn(),
    onSelectNode: vi.fn(),
    onToggleNode: vi.fn(),
    onToggleUsageGroup: vi.fn(),
    ...overrides,
  };
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(createElement(QueryClientProvider, { client }, createElement(TraceExecutionView, props)));
  return props;
};

const renderView = (props: ComponentProps<typeof TraceExecutionView>, client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) => {
  return render(createElement(QueryClientProvider, { client }, createElement(TraceExecutionView, props)));
};

beforeEach(() => { hookState.branches = { root: branch([rootNode]), 'turn-1': branch([childNode]) }; });
afterEach(() => cleanup());

it('loads a child branch only after expansion and selects its actual node id', () => {
  const props = setup();
  const tree = within(screen.getByRole('tree', { name: '실행 트리' }));
  expect(tree.getByRole('button', { name: '검토 계획 요청' })).toBeTruthy();
  expect(tree.queryByRole('button', { name: 'go test ./internal/serverpolicy' })).toBeNull();

  fireEvent.click(tree.getByRole('button', { name: '검토 계획 요청 하위 실행 펼치기' }));
  expect(props.onToggleNode).toHaveBeenCalledWith('turn-1');

  cleanup();
  const expandedProps = { ...props, expandedNodeIds: ['turn-1'] };
  renderView(expandedProps);
  const expandedTree = within(screen.getByRole('tree', { name: '실행 트리' }));
  fireEvent.click(expandedTree.getByRole('button', { name: 'go test ./internal/serverpolicy' }));
  expect(props.onSelectNode).toHaveBeenCalledWith('tool-1');
});

it('loads more nodes through the cursor owned by the root or expanded parent', () => {
  const rootNext = vi.fn(), childNext = vi.fn();
  hookState.branches = {
    root: branch([rootNode], { hasNextPage: true, fetchNextPage: rootNext }),
    'turn-1': branch([childNode], { hasNextPage: true, fetchNextPage: childNext }),
  };
  setup({ expandedNodeIds: ['turn-1'] });
  fireEvent.click(screen.getByRole('button', { name: '하위 실행 더 읽기: 검토 계획 요청' }));
  fireEvent.click(screen.getByRole('button', { name: '실행 더 읽기' }));
  expect(childNext).toHaveBeenCalledTimes(1);
  expect(rootNext).toHaveBeenCalledTimes(1);
});

it('groups usage records under their observed parent, selects the source node, and keeps the read-range count explicit', () => {
  hookState.branches = { root: branch([rootNode]), 'turn-1': branch([childNode, usageNode]) };
  const props = setup({ expandedNodeIds: ['turn-1'] });
  const tree = within(screen.getByRole('tree', { name: '실행 트리' }));
  expect(tree.getByRole('button', { name: 'go test ./internal/serverpolicy' })).toBeTruthy();
  expect(tree.queryByRole('button', { name: '사용량 관측' })).toBeNull();
  const usageGroup = tree.getByRole('button', { name: '사용량 기록 · 읽은 범위 1건' });
  expect(usageGroup.getAttribute('aria-expanded')).toBe('false');

  fireEvent.click(usageGroup);
  expect(props.onToggleUsageGroup).toHaveBeenCalledWith('turn-1');
  cleanup();
  renderView({ ...props, expandedUsageGroupIds: ['turn-1'] });
  const expandedTree = within(screen.getByRole('tree', { name: '실행 트리' }));
  expect(expandedTree.getByText('이 수는 읽은 범위의 기록 수이며 전체 합계가 아닙니다.')).toBeTruthy();
  fireEvent.click(expandedTree.getByRole('button', { name: '사용량 관측' }));
  expect(props.onSelectNode).toHaveBeenCalledWith('usage-1');
});

it('moves unknown status and missing time out of repeated list rows', () => {
  hookState.branches = { root: branch([{ ...rootNode, status: 'unknown', timestamp: null }]) };
  setup();
  const row = screen.getByRole('button', { name: '검토 계획 요청' });
  expect(row.textContent).not.toContain('—');
  expect(row.textContent).not.toContain('시각 확인 불가');
  expect(row.textContent).not.toContain('완료');
});

it('labels input wrappers by their observed grouping basis and omits normal collection labels', () => {
  hookState.branches = { root: branch([{ ...rootNode, kind: 'turn', grouping_basis: 'source_sequence', title: '설정 검토를 요청했습니다', availability: 'observed' }]) };
  setup();
  const tree = screen.getByRole('tree', { name: '실행 트리' });
  expect(within(tree).getByText('입력 구간')).toBeTruthy();
  expect(within(tree).getByRole('note', { name: /같은 원천의 입력부터 다음 입력 전까지/ })).toBeTruthy();
  expect(tree.textContent).not.toContain('자료 수집됨');
});

it('shows one error marker when status and availability carry the same failure', () => {
  hookState.branches = { root: branch([{ ...rootNode, status: 'error', availability: 'error' }]) };
  setup();
  const row = screen.getByRole('button', { name: '검토 계획 요청' });
  expect(within(row).getAllByText('오류')).toHaveLength(1);
});

it('shows one request tree with server search and no permanent timeline or category tabs', () => {
  const onSearch = vi.fn();
  setup({ onSearch });
  expect(screen.queryByRole('button', { name: '시간축' })).toBeNull();
  expect(screen.queryByRole('button', { name: '지시문·설정' })).toBeNull();
  fireEvent.change(screen.getByRole('searchbox', { name: '요청·메시지·도구 검색' }), { target: { value: 'Read' } });
  fireEvent.submit(screen.getByRole('search'));
  expect(onSearch).toHaveBeenCalledWith('Read');
});

it('pins one initial readable request and never moves an existing selection', () => {
  const onInitialNode = vi.fn();
  setup({ onInitialNode });
  expect(onInitialNode).toHaveBeenCalledWith('turn-1');
  cleanup();
  setup({ onInitialNode, selectedNodeId: 'tool-1' });
  expect(onInitialNode).toHaveBeenCalledTimes(1);
});
it('discards a stale root cursor before retrying the request list', () => {
  const onRootCursor = vi.fn();
  hookState.branches.root = branch([], { isError: true, error: { status: 409 } });
  setup({ rootCursor: 'stale', onRootCursor });
  fireEvent.click(screen.getByRole('button', { name: '다시 시도' }));
  expect(onRootCursor).toHaveBeenCalledWith('');
});

it('reflects restored URL search text after history navigation', () => {
  const props = setup({ query: 'Read' });
  cleanup();
  const client = new QueryClient();
  const view = renderView(props, client);
  view.rerender(createElement(QueryClientProvider, { client }, createElement(TraceExecutionView, { ...props, query: '' })));
  expect((screen.getByRole('searchbox', { name: '요청·메시지·도구 검색' }) as HTMLInputElement).value).toBe('');
});

it('announces observed source changes only at the root and reloads only on explicit action', () => {
  const rootRefresh = vi.fn(), childRefresh = vi.fn(), reset = vi.fn();
  hookState.branches = {
    root: branch([rootNode], { meta: { revision: 1, epoch: 0 }, refresh: rootRefresh }),
    'turn-1': branch([childNode], { meta: { revision: 1, epoch: 0 }, refresh: childRefresh }),
  };
  const props = setup({ observedStamp: { revision: 2, epoch: 0 }, selectedNodeId: 'tool-1', expandedNodeIds: ['turn-1'], query: 'Read', rootCursor: 'old-root', branchCursors: { 'turn-1': 'old-child' }, onResetCursors: reset });
  expect(screen.getAllByText('기록이 갱신되었습니다')).toHaveLength(1);
  expect(rootRefresh).not.toHaveBeenCalled();
  expect(childRefresh).not.toHaveBeenCalled();
  expect(reset).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole('button', { name: '다시 읽기' }));
  expect(reset).toHaveBeenCalledTimes(1);
  expect(rootRefresh).not.toHaveBeenCalled();
  expect(props.onSelectNode).not.toHaveBeenCalled();
  expect(props.onToggleNode).not.toHaveBeenCalled();
  expect((screen.getByRole('searchbox') as HTMLInputElement).value).toBe('Read');
  expect(screen.getByRole('button', { name: 'go test ./internal/serverpolicy' }).getAttribute('aria-pressed')).toBe('true');
});

it.each([
  { list: undefined, observed: { revision: 2, epoch: 0 } },
  { list: { revision: 1, epoch: 0 }, observed: undefined },
  { list: { revision: 1 }, observed: { revision: 2, epoch: 0 } },
  { list: { revision: 0, epoch: 0 }, observed: { revision: 0, epoch: 0 } },
])('does not announce a change without two complete different source stamps: $list / $observed', ({ list, observed }) => {
  hookState.branches.root = branch([rootNode], { meta: list });
  setup({ observedStamp: observed });
  expect(screen.queryByText('기록이 갱신되었습니다')).toBeNull();
});

it('refreshes the current root on an observed policy change without replacing selection or search', () => {
  const refresh = vi.fn();
  hookState.branches.root = branch([rootNode], { meta: { revision: 1, epoch: 0 }, refresh });
  const props = setup({ observedStamp: { revision: 1, epoch: 1 }, selectedNodeId: 'turn-1', query: 'Read' });
  fireEvent.click(screen.getByRole('button', { name: '다시 읽기' }));
  expect(refresh).toHaveBeenCalledTimes(1);
  expect(props.onSelectNode).not.toHaveBeenCalled();
  cleanup();
  hookState.branches.root = branch([rootNode], { meta: { revision: 1, epoch: 1 }, refresh });
  renderView(props);
  expect(screen.queryByText('기록이 갱신되었습니다')).toBeNull();
});

it('clears branch-only cursors and refreshes the unchanged root key after an observed update', () => {
  const refresh = vi.fn(), reset = vi.fn();
  hookState.branches.root = branch([rootNode], { meta: { revision: 1, epoch: 0 }, refresh });
  setup({ observedStamp: { revision: 2, epoch: 0 }, branchCursors: { 'turn-1': 'old-child' }, onResetCursors: reset });
  fireEvent.click(screen.getByRole('button', { name: '다시 읽기' }));
  expect(reset).toHaveBeenCalledTimes(1);
  expect(refresh).toHaveBeenCalledTimes(1);
});

it('masks every old-policy row, child, total and page control as soon as a different epoch is observed', () => {
  const refresh = vi.fn(), initial = vi.fn();
  hookState.branches = {
    root: branch([rootNode], { meta: { revision: 1, epoch: 0, prev_cursor: 'previous', request_total: { state: 'verified', value: 99 } }, refresh, hasNextPage: true }),
    'turn-1': branch([childNode], { hasNextPage: true }),
  };
  setup({ observedStamp: { revision: 1, epoch: 1 }, expandedNodeIds: ['turn-1'], onInitialNode: initial, onRootCursor: vi.fn() });
  expect(screen.getByText('기록이 갱신되었습니다')).toBeTruthy();
  expect(screen.queryByRole('button', { name: '검토 계획 요청' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'go test ./internal/serverpolicy' })).toBeNull();
  expect(screen.queryByText('작업 요청 99개')).toBeNull();
  expect(screen.queryByRole('button', { name: '이전 요청 페이지' })).toBeNull();
  expect(screen.queryByRole('button', { name: '실행 더 읽기' })).toBeNull();
  expect(screen.queryByRole('button', { name: '하위 실행 더 읽기: 검토 계획 요청' })).toBeNull();
  expect(refresh).not.toHaveBeenCalled();
  expect(initial).not.toHaveBeenCalled();
});

it('keeps an observed policy fence after navigation first arrives and then disappears', () => {
  hookState.branches.root = branch([rootNode], { meta: { revision: 1, epoch: 0 } });
  const props = setup({ selectedNodeId: 'tool-1' });
  cleanup();
  const client = new QueryClient();
  const view = renderView(props, client);
  expect(screen.getByRole('button', { name: '검토 계획 요청' })).toBeTruthy();
  view.rerender(createElement(QueryClientProvider, { client }, createElement(TraceExecutionView, { ...props, observedStamp: { revision: 1, epoch: 2 } })));
  expect(screen.queryByRole('button', { name: '검토 계획 요청' })).toBeNull();
  view.rerender(createElement(QueryClientProvider, { client }, createElement(TraceExecutionView, { ...props, observedStamp: undefined, selectedNodeId: 'tool-2' })));
  expect(screen.getByText('기록이 갱신되었습니다')).toBeTruthy();
  expect(screen.queryByRole('button', { name: '검토 계획 요청' })).toBeNull();
  view.rerender(createElement(QueryClientProvider, { client }, createElement(TraceExecutionView, { ...props, identity: { ...props.identity, sessionId: 'another-session' } })));
  expect(screen.getByRole('button', { name: '검토 계획 요청' })).toBeTruthy();
  expect(screen.queryByText('기록이 갱신되었습니다')).toBeNull();
});
