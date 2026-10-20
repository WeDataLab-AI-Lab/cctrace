// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { SessionAnalysisWorkspace } from './trace-workspace';
import { fetchSessionAnalysis, fetchSessionRecordConsent, regenerateSessionRecord } from '@/lib/api';
import { demandSessionTrace, fetchTraceOverview, fetchTraceContext, fetchTraceEvents } from '@/lib/session-trace-api';
import { fetchTurnContext } from '@/lib/turn-context-api';
import type { SessionAnalysisFull } from '@/lib/session-analysis-types';
import { flagship } from '@/app/test/session-analysis/detail/flagship';
import { toolContextRecords } from './trace-context-test-fixture';

const state = vi.hoisted(() => ({ query: 'session_id=A&from=list-filter&detail=analysis&expanded=1', replace: vi.fn() }));
vi.mock('next/navigation', () => ({ useSearchParams: () => new URLSearchParams(state.query), usePathname: () => '/sessions/', useRouter: () => ({ replace: state.replace }) }));
vi.mock('@/components/common/auth-context', () => ({ useAuth: () => ({ user: { id: 7, role: 'user' }, isAdmin: false }) }));
vi.mock('@/components/common/account-context', () => ({ useAccount: () => ({ selectedAccount: '' }) }));
vi.mock('@/lib/api', async importOriginal => ({ ...await importOriginal<typeof import('@/lib/api')>(), fetchSessionAnalysis: vi.fn(), fetchSessionRecordConsent: vi.fn(), regenerateSessionRecord: vi.fn() }));
vi.mock('@/lib/session-trace-api', () => ({ demandSessionTrace: vi.fn(), releaseSessionTrace: vi.fn().mockResolvedValue(undefined), fetchTraceOverview: vi.fn(), fetchTraceEvents: vi.fn(), fetchTraceContext: vi.fn(), TraceRequestError: class extends Error {} }));
vi.mock('@/lib/turn-context-api', () => ({ fetchTurnContext: vi.fn(), TurnContextRequestError: class extends Error {} }));
const source = (): SessionAnalysisFull => ({ ...flagship().analysis as SessionAnalysisFull, session_id: 'A', turn_context_revision: 'revision' });
const setup = (enabled = true) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: React.ReactNode }) => createElement(QueryClientProvider, { client }, children);
  return { client, ...render(createElement(SessionAnalysisWorkspace, { sessionId: 'A', enabled }), { wrapper }) };
};
beforeEach(() => {
  state.query = 'session_id=A&from=list-filter&detail=analysis&expanded=1';
  state.replace.mockReset().mockImplementation((url: string) => { state.query = url.split('?')[1]; });
  vi.mocked(fetchSessionAnalysis).mockReset().mockResolvedValue(source());
  vi.mocked(fetchSessionRecordConsent).mockResolvedValue({ runtime_key: 'test', disclosure_version: '1', granted: false, granted_at: null,
    runtime: 'test', provider: 'Test provider', provider_retention: '확인되지 않음', disclosure: '합성 고지', sends: [], not_sends: [], enabled: true, runtime_available: true });
  vi.mocked(regenerateSessionRecord).mockReset();
  vi.mocked(demandSessionTrace).mockReset().mockResolvedValue({ session_id: 'A', lease_id: 'lease', snapshot_id: 'snapshot', state: 'building', expires_at: '' });
  vi.mocked(fetchTraceOverview).mockReset().mockResolvedValue({ session_id: 'A', snapshot_id: 'snapshot', source_revision: '1', state: 'building', from: null, to: null, events_count: 0, bins: [], coverage: { state: 'partial', from: null, to: null, processed_rows: 0, row_limit: 200000, reason: null } });
  vi.mocked(fetchTraceContext).mockReset(); vi.mocked(fetchTraceEvents).mockReset();
  vi.mocked(fetchTurnContext).mockReset().mockImplementation(async (session, turn) => ({ session_id: session, turn_key: turn, state: 'ready', revision: 'revision', records: toolContextRecords(), next_cursor: null }));
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null);
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it('retains explicit owner consent and regeneration controls without automatically granting consent', async () => {
  vi.mocked(fetchSessionAnalysis).mockResolvedValue({ ...source(), record: { state: 'none', generated_at: null, covers_until: null, reason: 'no_record' } });
  vi.mocked(regenerateSessionRecord).mockResolvedValue({ state: 'waiting_budget', retry_at: '2026-10-14T00:00:00Z' });
  setup();
  expect(await screen.findByRole('button', { name: '동의 검토' })).toBeTruthy();
  fireEvent.click(await screen.findByRole('button', { name: '다시 만들기' }));
  await waitFor(() => expect(regenerateSessionRecord).toHaveBeenCalledWith('A'));
  expect(await screen.findByText(/일일 토큰 한도로 대기 중/)).toBeTruthy();
  expect(screen.getByText('전체 실행 기록')).toBeTruthy();
});
it('creates no analysis, trace or canonical context request in an inactive embedded tab', () => {
  setup(false);
  expect(fetchSessionAnalysis).not.toHaveBeenCalled(); expect(demandSessionTrace).not.toHaveBeenCalled(); expect(fetchTurnContext).not.toHaveBeenCalled();
});
it('shows canonical selected originals while the execution trace is still building', async () => {
  state.query += `&turn_key=${encodeURIComponent(source().turns[1].key)}`;
  setup();
  expect(await screen.findByText('printf trace-fixture', { selector: 'pre' })).toBeTruthy();
  expect(fetchTraceContext).not.toHaveBeenCalled();
  expect(vi.mocked(fetchTurnContext).mock.calls[0][1]).toBe(source().turns[1].key);
  expect(screen.queryByRole('alert')).toBeNull(); expect(screen.queryByText('raw fixture must stay hidden')).toBeNull();
});
it('pins the first turn once and preserves it when the analysis head changes', async () => {
  const first = source().turns.find(turn => turn.is_request)!.key;
  const { client } = setup();
  await waitFor(() => expect(fetchTurnContext).toHaveBeenCalled());
  expect(vi.mocked(fetchTurnContext).mock.calls[0][1]).toBe(first);
  vi.mocked(fetchSessionAnalysis).mockResolvedValue({ ...source(), turns: source().turns.slice().reverse() });
  await act(async () => { await client.invalidateQueries({ queryKey: ['session-analysis'] }); });
  expect((screen.getByRole('combobox', { name: '턴 선택' }) as HTMLSelectElement).value).toBe(first);
});
it('selects previous/next turns in the current sessions URL while preserving list filters and shell state', async () => {
  setup();
  await waitFor(() => expect(screen.getByRole('button', { name: '다음 턴' }).hasAttribute('disabled')).toBe(false));
  fireEvent.click(screen.getByRole('button', { name: '다음 턴' }));
  await waitFor(() => expect(state.replace).toHaveBeenCalled());
  const url = new URL(state.replace.mock.calls.at(-1)![0], 'https://test.invalid');
  expect(url.pathname).toBe('/sessions/'); expect(url.searchParams.get('from')).toBe('list-filter');
  expect(url.searchParams.get('detail')).toBe('analysis'); expect(url.searchParams.get('expanded')).toBe('1');
  expect(url.searchParams.get('turn_key')).toBe(source().turns[1].key);
  expect(url.searchParams.has('trace_from')).toBe(true); expect(url.searchParams.has('trace_to')).toBe(true);
});
it.each([900, 1440, 1600])('keeps selected originals in responsive document flow at %ipx without a duplicate calendar', async width => {
  Object.defineProperty(window, 'innerWidth', { value: width, configurable: true }); setup();
  const original = await screen.findByRole('complementary', { name: '선택 원문' });
  expect(original.className).not.toContain('absolute'); expect(screen.queryByRole('complementary', { name: '활동 캘린더' })).toBeNull();
  expect(screen.queryByRole('button', { name: '집중 모드' })).toBeNull(); expect(screen.queryByRole('button', { name: '캘린더' })).toBeNull();
  expect(await within(original).findByText('trace-fixture stdout', { selector: 'pre' })).toBeTruthy();
});
it('aborts analysis and canonical original reads when the shell hides its retained instance', async () => {
  vi.mocked(fetchSessionAnalysis).mockImplementation(() => new Promise(() => undefined)); state.query += '&turn_key=direct-link';
  vi.mocked(fetchTurnContext).mockImplementation(() => new Promise(() => undefined));
  const { rerender } = setup(); await waitFor(() => expect(fetchTurnContext).toHaveBeenCalled());
  const analysisSignal = vi.mocked(fetchSessionAnalysis).mock.calls[0][2], contextSignal = vi.mocked(fetchTurnContext).mock.calls[0][4];
  rerender(createElement(SessionAnalysisWorkspace, { sessionId: 'A', enabled: false }));
  expect(analysisSignal?.aborted).toBe(true); expect(contextSignal.aborted).toBe(true);
});
it('keeps the existing event context path for events with no canonical turn mapping', async () => {
  state.query += '&event_id=tool-event';
  vi.mocked(fetchSessionAnalysis).mockResolvedValue({ session_id: 'A', short_id: 'A', owner_view: true, state: 'building' });
  vi.mocked(demandSessionTrace).mockResolvedValue({ session_id: 'A', lease_id: 'lease', snapshot_id: 'snapshot', state: 'ready', expires_at: '' });
  vi.mocked(fetchTraceOverview).mockResolvedValue({ session_id: 'A', snapshot_id: 'snapshot', source_revision: '1', state: 'ready', from: '2026-10-13T00:00:00Z', to: '2026-10-13T00:01:00Z', events_count: 0, bins: [], coverage: { state: 'complete', from: null, to: null, processed_rows: 2, row_limit: 200000, reason: null } });
  vi.mocked(fetchTraceContext).mockResolvedValue({ session_id: 'A', snapshot_id: 'snapshot', event_id: 'tool-event', records: toolContextRecords(), next_cursor: null });
  setup(); expect(await screen.findByText('printf trace-fixture', { selector: 'pre' })).toBeTruthy();
  expect(fetchTraceContext).toHaveBeenCalled(); expect(fetchTurnContext).not.toHaveBeenCalled();
});
