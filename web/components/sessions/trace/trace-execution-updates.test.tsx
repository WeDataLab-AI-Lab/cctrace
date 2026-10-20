// @vitest-environment jsdom
import { createElement } from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { fetchTraceNodeDetail, fetchTraceNodes } from '@/lib/session-trace-nodes';
import type { TraceNode } from '@/lib/session-trace-node-types';
import { TraceExecutionView } from './trace-execution-view';

vi.mock('@/lib/session-trace-nodes', () => ({ fetchTraceNodes: vi.fn(), fetchTraceNodeDetail: vi.fn() }));
vi.mock('@/lib/use-session-trace', () => ({ usePageVisible: () => true }));
beforeEach(() => { vi.mocked(fetchTraceNodes).mockReset(); vi.mocked(fetchTraceNodeDetail).mockReset(); });
afterEach(() => { cleanup(); vi.useRealTimers(); });

it('uses observed stamps without adding initial requests or periodic polling', async () => {
  const node: TraceNode = { node_id: 'turn-1', parent_id: null, kind: 'turn', title: 'Read actual record', timestamp: null, started_at: null, ended_at: null, status: 'unknown', has_children: false, child_count: null, source_ref: 'source', evidence: [], availability: 'observed' };
  vi.mocked(fetchTraceNodes).mockResolvedValue({ session_id: 's', nodes: [node], next_cursor: null, revision: 1, epoch: 0, coverage: { state: 'complete', from: null, to: null, processed_rows: 1, row_limit: 20, reason: null } });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const props = { identity: { userId: 7, access: 'owner' as const, sessionId: 's' }, enabled: true, nodesView: 'flow' as const, viewMode: 'tree' as const, selectedNodeId: 'turn-1', expandedNodeIds: [], expandedUsageGroupIds: [], onViewModeChange: vi.fn(), onNodesViewChange: vi.fn(), onSelectNode: vi.fn(), onToggleNode: vi.fn(), onToggleUsageGroup: vi.fn() };
  const view = render(createElement(QueryClientProvider, { client }, createElement(TraceExecutionView, props)));
  await waitFor(() => expect(screen.getByRole('button', { name: 'Read actual record' })).toBeTruthy());
  expect(screen.queryByText('기록이 갱신되었습니다')).toBeNull();
  expect(fetchTraceNodes).toHaveBeenCalledTimes(1);
  expect(fetchTraceNodeDetail).not.toHaveBeenCalled();
  vi.useFakeTimers();
  view.rerender(createElement(QueryClientProvider, { client }, createElement(TraceExecutionView, { ...props, observedStamp: { revision: 2, epoch: 0 } })));
  expect(screen.getByText('기록이 갱신되었습니다')).toBeTruthy();
  await act(async () => { await vi.advanceTimersByTimeAsync(180_000); });
  expect(fetchTraceNodes).toHaveBeenCalledTimes(1);
  expect(fetchTraceNodeDetail).not.toHaveBeenCalled();
  expect(props.onSelectNode).not.toHaveBeenCalled();
  client.clear();
});

it('hides cached rows on observed epoch change without fetching until explicit policy reload', async () => {
 const oldNode:TraceNode={node_id:'turn-1',parent_id:null,kind:'turn',title:'Old policy title',timestamp:null,started_at:null,ended_at:null,status:'unknown',has_children:false,child_count:null,source_ref:'source',evidence:[],availability:'observed'};
 const freshNode={...oldNode,title:'New policy title'};
 const coverage={state:'complete' as const,from:null,to:null,processed_rows:1,row_limit:20,reason:null};
 vi.mocked(fetchTraceNodes).mockResolvedValueOnce({session_id:'s',nodes:[oldNode],next_cursor:'more-old',prev_cursor:'previous-old',revision:1,epoch:0,request_total:{state:'verified',value:99,definition:'work_requests',scope:'session',revision:1},coverage})
 .mockResolvedValueOnce({session_id:'s',nodes:[freshNode],next_cursor:null,revision:1,epoch:1,coverage});
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
 const props={identity:{userId:7,access:'owner' as const,sessionId:'s'},enabled:true,nodesView:'flow' as const,viewMode:'tree' as const,selectedNodeId:'turn-1',query:'Read',expandedNodeIds:[],expandedUsageGroupIds:[],onViewModeChange:vi.fn(),onNodesViewChange:vi.fn(),onSelectNode:vi.fn(),onToggleNode:vi.fn(),onToggleUsageGroup:vi.fn(),onRootCursor:vi.fn()};
 const view=render(createElement(QueryClientProvider,{client},createElement(TraceExecutionView,props)));
 await waitFor(()=>expect(screen.getByRole('button',{name:'Old policy title'})).toBeTruthy());
 expect(screen.getByText('작업 요청 99개')).toBeTruthy();
 view.rerender(createElement(QueryClientProvider,{client},createElement(TraceExecutionView,{...props,observedStamp:{revision:1,epoch:1}})));
 expect(screen.queryByRole('button',{name:'Old policy title'})).toBeNull();
 expect(screen.queryByText('작업 요청 99개')).toBeNull();
 expect(screen.queryByRole('button',{name:'이전 요청 페이지'})).toBeNull();
 expect(screen.queryByRole('button',{name:'실행 더 읽기'})).toBeNull();
 expect(fetchTraceNodes).toHaveBeenCalledTimes(1);
 expect(fetchTraceNodeDetail).not.toHaveBeenCalled();
 view.rerender(createElement(QueryClientProvider,{client},createElement(TraceExecutionView,props)));
 expect(screen.queryByRole('button',{name:'Old policy title'})).toBeNull();
 expect(fetchTraceNodes).toHaveBeenCalledTimes(1);
 fireEvent.click(screen.getByRole('button',{name:'다시 읽기'}));
 await waitFor(()=>expect(screen.getByRole('button',{name:'New policy title'})).toBeTruthy());
 expect(fetchTraceNodes).toHaveBeenCalledTimes(2);
 expect(screen.queryByRole('button',{name:'Old policy title'})).toBeNull();
 expect(screen.queryByText('기록이 갱신되었습니다')).toBeNull();
 expect(props.onSelectNode).not.toHaveBeenCalled();
 expect((screen.getByRole('searchbox') as HTMLInputElement).value).toBe('Read');
 client.clear();
});
