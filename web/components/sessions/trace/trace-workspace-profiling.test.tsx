// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import type { TraceNode, TraceNodeDetail } from '@/lib/session-trace-node-types';
import { SessionTraceWorkspace } from './trace-workspace';
import { toolContextRecords } from './trace-context-test-fixture';

const state = vi.hoisted(() => ({
  query: '', replace: vi.fn(), push: vi.fn(), analysis: vi.fn(), trace: vi.fn(),
  listCalls: [] as { enabled: boolean; parentId?: string }[], detailCalls: [] as { enabled: boolean; nodeId?: string }[],
  branches: {} as Record<string, { nodes: TraceNode[]; isPending: boolean; isError: boolean; hasNextPage: boolean; fetchNextPage: ReturnType<typeof vi.fn>; refresh: ReturnType<typeof vi.fn> }>,
  detail: undefined as TraceNodeDetail | undefined,
}));

const turn: TraceNode = {
  node_id: 'turn-1', parent_id: null, kind: 'turn', grouping_basis: 'exact_turn_id', title: '문서 검토 요청', timestamp: '2026-10-13T00:00:02Z',
  started_at: null, ended_at: null, status: 'completed', has_children: true, child_count: 1, source_ref: 'turn-ref', evidence: ['session_log'], availability: 'observed',
};
const tool: TraceNode = {
  node_id: 'tool-1', parent_id: 'turn-1', kind: 'tool', title: 'Bash · go test ./internal/sessiontrace', timestamp: '2026-10-13T00:00:11Z',
  started_at: '2026-10-13T00:00:10Z', ended_at: '2026-10-13T00:00:11Z', status: 'completed', has_children: false, child_count: 0,
  source_ref: 'opaque-source-reference', evidence: ['tool_call', 'tool_result'], availability: 'observed',
};

vi.mock('next/navigation', () => ({
  useSearchParams: () => new URLSearchParams(state.query), usePathname: () => '/sessions/',
  useRouter: () => ({ replace: state.replace, push: state.push }),
}));
vi.mock('@/components/common/auth-context', () => ({ useAuth: () => ({ user: { id: 7, role: 'user' }, isAdmin: false }) }));
vi.mock('@/components/common/account-context', () => ({ useAccount: () => ({ selectedAccount: '' }) }));
vi.mock('@/lib/api', async importOriginal => ({ ...await importOriginal<typeof import('@/lib/api')>(), fetchSessionAnalysis: state.analysis }));
vi.mock('@/lib/use-session-trace', () => ({ usePageVisible: () => true, useSessionTrace: state.trace }));
vi.mock('@/lib/use-turn-context', () => ({ useTurnContext: () => ({ records: [], loading: false, updating: false, error: null, state: 'ready' }) }));
vi.mock('@/lib/use-trace-nodes', () => ({
  traceNodesKey: () => ['trace-nodes'],
  traceNodesViewKey: () => ['trace-nodes', 'children', 'execution'],
  useTraceNodes: (options: { enabled: boolean; parentId?: string }) => {
    state.listCalls.push(options);
    return { ...state.branches[options.parentId ?? 'root'], coverage: undefined, isLoading: false, isFetchingNextPage: false, refresh: vi.fn() };
  },
  useTraceNodeDetail: (options: { enabled: boolean; nodeId?: string }) => {
    state.detailCalls.push(options);
    return { data: state.detail, isPending: false, isError: false, refetch: vi.fn() };
  },
}));

const setup = (enabled = true) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const records = toolContextRecords().map(record => ({ ...record, source_file: 'actual-rollout.jsonl' }));
  state.detail = {
    session_id: 'A', node: tool,
    input: { state: 'observed', value: { command: 'go test ./internal/sessiontrace' } },
    output: { state: 'observed', value: 'ok\ttracefixture' },
    context: { state: 'not_collected', reason: 'instruction_context_not_recorded' },
    usage: null, raw_records: records, notices: [],
  };
  state.branches = {
    root: { nodes: [turn], isPending: false, isError: false, hasNextPage: false, fetchNextPage: vi.fn(), refresh: vi.fn() },
    'turn-1': { nodes: [tool], isPending: false, isError: false, hasNextPage: false, fetchNextPage: vi.fn(), refresh: vi.fn() },
  };
  return render(createElement(QueryClientProvider, { client }, createElement(SessionTraceWorkspace, { sessionId: 'A', enabled })));
};

beforeEach(() => {
  state.query = 'session_id=A&detail=trace&event_id=tool-1&trace_expanded=turn-1';
  state.replace.mockReset(); state.push.mockReset(); state.listCalls = []; state.detailCalls = [];
  state.analysis.mockReset().mockResolvedValue({ session_id: 'A' });
  state.trace.mockReset().mockReturnValue({ overview: {}, sample: {}, events: {}, context: {}, rangeState: 'ready', error: null, updating: false, retry: vi.fn() });
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it('renders the execution tree and selected node source instead of the former analysis waterfall', () => {
  setup();
  const tree = screen.getByRole('tree', { name: '실행 트리' });
  expect(within(tree).getByRole('button', { name: '문서 검토 요청' })).toBeTruthy();
  const selected = within(tree).getByRole('button', { name: 'Bash · go test ./internal/sessiontrace' });
  expect(selected.getAttribute('aria-pressed')).toBe('true');
  expect(screen.getByRole('heading', { name: 'Bash · go test ./internal/sessiontrace' })).toBeTruthy();
  expect(screen.getByRole('tabpanel', { name: '입력·결과' }).textContent).toContain('go test ./internal/sessiontrace');
  expect(state.detailCalls.at(-1)).toMatchObject({ enabled: true, nodeId: 'tool-1' });
  expect(state.analysis).not.toHaveBeenCalled();
  expect(screen.queryByText('세션 성과')).toBeNull();
  expect(screen.queryByText('선택 턴 지표')).toBeNull();
});

it('shows source records only after opening the raw-data section', () => {
  setup();
  fireEvent.click(screen.getByText(/^연결된 원자료 [0-9]+건$/));
  const raw = screen.getByRole('tabpanel', { name: '입력·결과' });
  const sourceSummary = within(raw).getByText(/tool_call · .*actual-rollout\.jsonl/);
  expect(sourceSummary).toBeTruthy();
  fireEvent.click(sourceSummary);
  expect(within(raw).getAllByText(/private_fixture/)).toHaveLength(2);
  expect(within(raw).queryByText('opaque-source-reference')).toBeNull();
});

it('keeps list and selected-detail reads disabled when the retained trace tab is hidden', () => {
  setup(false);
  expect(state.analysis).not.toHaveBeenCalled();
  expect(state.trace.mock.calls.at(-1)?.[0].enabled).toBe(false);
  expect(state.listCalls.at(-1)).toMatchObject({ enabled: false });
  expect(state.detailCalls.at(-1)).toMatchObject({ enabled: false });
});
