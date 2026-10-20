// @vitest-environment jsdom
import { createElement } from 'react';
import { act, cleanup, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { TraceRequestError } from './session-trace-api';
import { fetchTraceNodeDetail, fetchTraceNodes } from './session-trace-nodes';
import { traceNodesKey, traceNodesPageKey, useTraceNodeDetail, useTraceNodes } from './use-trace-nodes';
import type { TraceNode, TraceNodeDetail, TraceNodesResponse } from './session-trace-node-types';

vi.mock('./session-trace-nodes', () => ({ fetchTraceNodes: vi.fn(), fetchTraceNodeDetail: vi.fn() }));
vi.mock('./use-session-trace', () => ({ usePageVisible: () => true }));

const identity = { userId: 7, access: 'owner' as const, sessionId: 'A' };
const node: TraceNode = {
  node_id: 'response-1', parent_id: null, kind: 'response', title: 'private response body', timestamp: null,
  started_at: null, ended_at: null, status: 'completed', has_children: false, child_count: 0,
  source_ref: 'message-1', evidence: ['session_log'], availability: 'observed',
};
const page: TraceNodesResponse = { session_id: 'A', nodes: [node], next_cursor: null, coverage: { state: 'complete', from: null, to: null, processed_rows: 1, row_limit: 100, reason: null } };
const detail: TraceNodeDetail = {
  session_id: 'A', node, input: { state: 'observed', value: 'private prompt' }, output: { state: 'observed', value: 'private response body' },
  context: { state: 'not_applicable' }, usage: null, raw_records: [], notices: [],
};

beforeEach(() => {
  vi.mocked(fetchTraceNodes).mockReset().mockResolvedValue(page);
  vi.mocked(fetchTraceNodeDetail).mockReset().mockResolvedValue(detail);
});
afterEach(() => cleanup());

const setup = () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: React.ReactNode }) => createElement(QueryClientProvider, { client }, children);
  return { client, wrapper };
};

it.each([401, 403, 404, 409])('clears cached list pages and sibling details after trace nodes returns %i', async status => {
  vi.mocked(fetchTraceNodes).mockResolvedValueOnce(page).mockRejectedValueOnce(new TraceRequestError(status));
  const { client, wrapper } = setup();
  const queryKey = traceNodesPageKey(identity, 'execution');
  const contextKey = traceNodesPageKey(identity, 'context');
  const otherSessionKey = traceNodesPageKey({ ...identity, sessionId: 'B' }, 'execution');
  const siblingDetailKey = [...traceNodesKey(identity), 'detail', 'response-2'];
  const { result } = renderHook(() => useTraceNodes({ identity }), { wrapper });
  await waitFor(() => expect(result.current.nodes).toEqual([node]));
  client.setQueryData(contextKey, { pages: [page], pageParams: [undefined] });
  client.setQueryData(otherSessionKey, { pages: [page], pageParams: [undefined] });
  client.setQueryData(siblingDetailKey, detail);

  await act(async () => { await result.current.refetch(); });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(result.current.nodes).toEqual([]);
  expect(client.getQueryData(queryKey)).toBeUndefined();
  expect(client.getQueryData(contextKey)).toBeUndefined();
  expect(client.getQueryData(siblingDetailKey)).toBeUndefined();
  expect(client.getQueryData(otherSessionKey)).toBeDefined();
});

it.each([401, 403, 404, 409])('does not retain a selected node body after detail returns %i', async status => {
  vi.mocked(fetchTraceNodeDetail).mockResolvedValueOnce(detail).mockRejectedValueOnce(new TraceRequestError(status));
  const { client, wrapper } = setup();
  const detailKey = [...traceNodesKey(identity), 'detail', node.node_id];
  const listKey = traceNodesPageKey(identity, 'execution');
  client.setQueryData(listKey, { pages: [page], pageParams: [undefined] });
  const { result } = renderHook(() => useTraceNodeDetail({ identity, nodeId: node.node_id }), { wrapper });
  await waitFor(() => expect(result.current.data?.output.value).toBe('private response body'));

  await act(async () => { await result.current.refetch(); });
  expect(fetchTraceNodeDetail).toHaveBeenCalledTimes(2);
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(result.current.data).toBeUndefined();
  expect(client.getQueryData(detailKey)).toBeUndefined();
  expect(client.getQueryData(listKey)).toBeUndefined();
});

it('appends selected request input pages without fetching sibling bodies', async () => {
  vi.mocked(fetchTraceNodeDetail).mockResolvedValueOnce({ ...detail, request: { state: 'observed', digest: { state: 'not_checked' }, missing_refs: [], source_refs: [], input_page: { items: [{ request_path: 'system[0]', kind: 'system', state: 'observed', source_refs: [], value: 'rule' }], next_cursor: 'next', digest: { state: 'not_checked' }, coverage: 'page' } } })
    .mockResolvedValueOnce({ ...detail, request: { state: 'observed', digest: { state: 'not_checked' }, missing_refs: [], source_refs: [], input_page: { items: [{ request_path: 'messages[0].content[0]', kind: 'message', state: 'observed', source_refs: [], value: 'hello' }], next_cursor: null, digest: { state: 'not_checked' }, coverage: 'page' } } });
  const { wrapper } = setup();
  const { result } = renderHook(() => useTraceNodeDetail({ identity, nodeId: node.node_id, options: { requestParts: 'page' } }), { wrapper });
  await waitFor(() => expect(result.current.data?.request?.input_page?.items).toHaveLength(1));
  await act(async () => { await result.current.fetchNextPage(); });
  await waitFor(() => expect(result.current.data?.request?.input_page?.items).toHaveLength(2));
  expect(vi.mocked(fetchTraceNodeDetail).mock.calls[1][4]).toMatchObject({ inputCursor: 'next' });
  expect(fetchTraceNodeDetail).toHaveBeenCalledTimes(2);
});

it('merges later request availability and source evidence without duplicating fragment provenance', async () => {
  const provenance = { request_path: 'messages[0].content[0]', source_ref: 'source-a', raw_path: 'message.content[0]', uuid: 'raw-a', state: 'observed' };
  const source = { source_ref: 'source-a', node_id: 'raw-node', raw_path: 'message.content[0]', uuid: 'raw-a', state: 'observed' };
  const first: TraceNodeDetail = { ...detail, source_locations: [source], request: { state: 'observed', digest: { state: 'not_checked' }, missing_refs: [], source_refs: ['source-a'], provenance: [provenance], input_page: { items: [{ request_path: 'messages[0].content[0]', kind: 'message', state: 'observed', source_refs: ['source-a'], value: '{"text":', truncated: true, total_bytes: 16 }], next_cursor: 'next', digest: { state: 'not_checked' }, coverage: 'page' } } };
  const second: TraceNodeDetail = { ...detail, source_locations: [source, { ...source, source_ref: 'source-b', node_id: 'raw-b', uuid: 'raw-b' }], request: { state: 'partial', digest: { state: 'not_checked' }, missing_refs: ['missing-b', 'missing-b'], source_refs: ['source-a', 'source-b'], provenance: [provenance, { ...provenance, request_path: 'messages[1].content', source_ref: 'missing-b', state: 'unresolved' }], input_page: { items: [{ request_path: 'messages[0].content[0]', kind: 'message', state: 'observed', source_refs: ['source-a'], value: '"hello"}', offset_bytes: 8, total_bytes: 16 }, { request_path: 'messages[1].content', kind: 'message', state: 'unresolved', source_refs: ['missing-b'] }], next_cursor: null, digest: { state: 'not_checked' }, coverage: 'page' } } };
  vi.mocked(fetchTraceNodeDetail).mockResolvedValueOnce(first).mockResolvedValueOnce(second);
  const { wrapper } = setup();
  const { result } = renderHook(() => useTraceNodeDetail({ identity, nodeId: node.node_id, options: { requestParts: 'page' } }), { wrapper });
  await waitFor(() => expect(result.current.data?.request?.input_page?.items).toHaveLength(1));
  await act(async () => { await result.current.fetchNextPage(); });
  await waitFor(() => expect(result.current.data?.request?.state).toBe('partial'));
  expect(result.current.data?.request?.missing_refs).toEqual(['missing-b']);
  expect(result.current.data?.request?.source_refs).toEqual(['source-a', 'source-b']);
  expect(result.current.data?.request?.provenance).toHaveLength(2);
  expect(result.current.data?.source_locations).toHaveLength(2);
  expect(result.current.data?.request?.input_page?.items[0].value).toEqual({ text: 'hello' });
  expect(result.current.data?.request?.input_page?.items[0].truncated).toBeFalsy();
  expect(first.request?.input_page?.items[0].value).toBe('{"text":');
});

it.each([403, 404, 409])('rechecks cached selection before exposing its body on A → B → A after %i', async status => {
  const { wrapper } = setup();
  const other = { ...detail, session_id: 'B', output: { state: 'observed', value: 'other session body' } };
  let rejectReentry: (error: Error) => void = () => undefined;
  vi.mocked(fetchTraceNodeDetail).mockResolvedValueOnce(detail).mockResolvedValueOnce(other).mockImplementationOnce(() => new Promise((_resolve, reject) => { rejectReentry = reject; }));
  const { result, rerender } = renderHook(({ sessionId }) => useTraceNodeDetail({ identity: { ...identity, sessionId }, nodeId: node.node_id }), { wrapper, initialProps: { sessionId: 'A' } });
  await waitFor(() => expect(result.current.data?.output.value).toBe('private response body'));
  rerender({ sessionId: 'B' });
  await waitFor(() => expect(result.current.data?.output.value).toBe('other session body'));
  rerender({ sessionId: 'A' });
  expect(result.current.data).toBeUndefined();
  await waitFor(() => expect(fetchTraceNodeDetail).toHaveBeenCalledTimes(3));
  await act(async () => { rejectReentry(new TraceRequestError(status)); });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(result.current.data).toBeUndefined();
});

it('reauthorizes cached list and detail on hidden → enabled without exposing old payload', async () => {
  const { wrapper } = setup();
  let resolveList: (value: TraceNodesResponse) => void = () => undefined;
  let resolveDetail: (value: TraceNodeDetail) => void = () => undefined;
  vi.mocked(fetchTraceNodes).mockResolvedValueOnce(page).mockImplementationOnce(() => new Promise(resolve => { resolveList = resolve; }));
  vi.mocked(fetchTraceNodeDetail).mockResolvedValueOnce(detail).mockImplementationOnce(() => new Promise(resolve => { resolveDetail = resolve; }));
  const { result, rerender } = renderHook(({ enabled }) => ({ list: useTraceNodes({ identity, enabled }), selected: useTraceNodeDetail({ identity, nodeId: node.node_id, enabled }) }), { wrapper, initialProps: { enabled: true } });
  await waitFor(() => expect(result.current.selected.data?.output.value).toBe('private response body'));
  rerender({ enabled: false });
  rerender({ enabled: true });
  expect(result.current.list.nodes).toEqual([]);
  expect(result.current.selected.data).toBeUndefined();
  await waitFor(() => expect(fetchTraceNodeDetail).toHaveBeenCalledTimes(2));
  await act(async () => { resolveList(page); resolveDetail(detail); });
  await waitFor(() => expect(result.current.selected.data?.output.value).toBe('private response body'));
  expect(result.current.list.nodes).toEqual([node]);
});
