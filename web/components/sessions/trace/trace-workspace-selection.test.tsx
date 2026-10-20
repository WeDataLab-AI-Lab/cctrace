// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { SessionAnalysisWorkspace } from './trace-workspace';
import { flagship } from '@/app/test/session-analysis/detail/flagship';
import type { SessionAnalysisFull } from '@/lib/session-analysis-types';

const state = vi.hoisted(() => ({ query: '', replace: vi.fn(), trace: vi.fn(), context: vi.fn() }));
const analysis: SessionAnalysisFull = { ...flagship().analysis as SessionAnalysisFull, session_id: 'A' };
vi.mock('next/navigation', () => ({ useSearchParams: () => new URLSearchParams(state.query), usePathname: () => '/sessions/', useRouter: () => ({ replace: state.replace }) }));
vi.mock('@/components/common/auth-context', () => ({ useAuth: () => ({ user: { id: 7 }, isAdmin: false }) }));
vi.mock('@/components/common/account-context', () => ({ useAccount: () => ({ selectedAccount: '' }) }));
vi.mock('@/lib/api', async original => ({ ...await original<typeof import('@/lib/api')>(), fetchSessionAnalysis: () => Promise.resolve(analysis) }));
vi.mock('@/lib/use-session-trace', () => ({ usePageVisible: () => true, useSessionTrace: state.trace }));
vi.mock('@/lib/use-turn-context', () => ({ useTurnContext: state.context }));
vi.mock('../analysis/analysis-tab', () => ({ SessionAnalysisTab: ({ executionFlow }: { executionFlow: React.ReactNode }) => executionFlow }));
const coverage = { state: 'complete', from: null, to: null, processed_rows: 2, row_limit: 200000, reason: null };
const ready = { session_id: 'A', state: 'ready', snapshot_id: 'snapshot', source_revision: '1', from: analysis.turns[0].anchor_ts, to: analysis.turns[1].ended_at, events_count: 2, bins: [], coverage };
const query = { data: undefined, isPending: false, isError: false, isFetching: false, isFetchingNextPage: false, hasNextPage: false, refetch: vi.fn(), fetchNextPage: vi.fn() };
beforeEach(() => {
  state.query = `session_id=A&detail=analysis&from=list-filter&turn_key=${encodeURIComponent(analysis.turns[0].key)}`;
  state.replace.mockReset().mockImplementation((url: string) => { state.query = url.split('?')[1]; });
  state.trace.mockReset().mockReturnValue({ overview: { ...query, data: ready }, sample: { ...query, data: { events: [] } }, events: query,
    context: { ...query, data: { pages: [{ records: [] }] } }, rangeState: 'ready', error: null, updating: false, retry: vi.fn() });
  state.context.mockReset().mockReturnValue({ ...query, records: [], state: 'ready', error: null, loading: false, updating: false });
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null);
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const setup = () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(createElement(QueryClientProvider, { client }, createElement(SessionAnalysisWorkspace, { sessionId: 'A' })));
};
it('prioritizes the exact selected event context over its canonical turn originals', async () => {
  state.query += '&event_id=exact-tool'; setup();
  await screen.findByRole('combobox', { name: '턴 선택' });
  expect(state.trace.mock.calls.at(-1)![0]).toMatchObject({ eventId: 'exact-tool', contextEnabled: true });
  expect(state.context.mock.calls.at(-1)![0].enabled).toBe(false);
});
it('clears a pinned turn on whole-flow selection and preserves the list date filter', async () => {
  state.query += '&trace_from=2026-10-13T00:00:00Z&trace_to=2026-10-13T00:01:00Z'; setup();
  await waitFor(() => expect(screen.queryByRole('combobox', { name: '턴 선택' })).not.toBeNull());
  fireEvent.click(screen.getByRole('button', { name: '전체 보기' }));
  const params = new URLSearchParams(state.query);
  expect(params.has('turn_key')).toBe(false); expect(params.has('trace_from')).toBe(false); expect(params.has('trace_to')).toBe(false);
  expect(params.get('from')).toBe('list-filter');
  expect(state.trace.mock.calls.at(-1)![0].range).toBeUndefined();
  expect(screen.getByText('턴을 선택하세요')).toBeTruthy();
});
it('renders elapsed ticks for the selected turn query range', async () => {
  setup();
  expect(await screen.findByText('+0.0s')).toBeTruthy();
});

it('keeps the zoomed interval when selecting an execution row inside it', async () => {
  const from = analysis.turns[0].anchor_ts;
  const to = new Date(Date.parse(from) + 1000).toISOString();
  state.query += `&trace_from=${encodeURIComponent(from)}&trace_to=${encodeURIComponent(to)}`;
  const event = { event_id: 'focused-tool', kind: 'tool', timestamp: from, started_at: from, ended_at: to, turn_key: analysis.turns[0].key, parent_event_id: null, agent_id: null, label: 'Focused tool', status: 'completed', evidence: [], source_ref: '' };
  state.trace.mockReturnValue({ ...state.trace(), events: { ...query, data: { pages: [{ events: [event] }] } } });
  setup();
  await waitFor(() => expect(screen.getAllByRole('option').length).toBeGreaterThan(1));
  fireEvent.click(screen.getByRole('button', { name: 'Focused tool' }));
  const params = new URLSearchParams(state.query);
  expect(params.get('event_id')).toBe('focused-tool');
  expect(Date.parse(params.get('trace_from')!)).toBe(Date.parse(from));
  expect(Date.parse(params.get('trace_to')!)).toBe(Date.parse(to));
});

it('keeps an explicit current-position range unpinned after analysis loads and on a retained workspace', async () => {
 const turn=analysis.turns[1];
 const from=turn.anchor_ts,to=new Date(Date.parse(from)+1000).toISOString();
 const event={event_id:'later-source',kind:'tool',timestamp:from,started_at:from,ended_at:to,turn_key:turn.key,parent_event_id:null,agent_id:null,label:'Later exact tool',status:'completed',evidence:[],source_ref:''};
 state.trace.mockReturnValue({...state.trace(),events:{...query,data:{pages:[{events:[event]}]}}});
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
 const view=()=>createElement(QueryClientProvider,{client},createElement(SessionAnalysisWorkspace,{sessionId:'A'}));
 state.query=`session_id=A&detail=trace&trace_from=${encodeURIComponent(from)}&trace_to=${encodeURIComponent(to)}`;
 const {rerender}=render(view());
 await waitFor(()=>expect(screen.getAllByRole('option').length).toBeGreaterThan(1));
 expect((screen.getByRole('combobox',{name:'턴 선택'}) as HTMLSelectElement).value).toBe('');
 expect(screen.getByRole('button',{name:'Later exact tool'})).toBeTruthy();
 // A retained instance can already have a pinned turn before the external range arrives.
 state.query=`session_id=A&detail=trace&turn_key=${encodeURIComponent(analysis.turns[0].key)}`;
 rerender(view());
 expect((screen.getByRole('combobox',{name:'턴 선택'}) as HTMLSelectElement).value).toBe(analysis.turns[0].key);
 state.query=`session_id=A&detail=trace&trace_from=${encodeURIComponent(from)}&trace_to=${encodeURIComponent(to)}`;
 rerender(view());
 expect((screen.getByRole('combobox',{name:'턴 선택'}) as HTMLSelectElement).value).toBe('');
 expect(screen.getByRole('button',{name:'Later exact tool'})).toBeTruthy();
 expect(state.context.mock.calls.at(-1)![0].enabled).toBe(false);
});
