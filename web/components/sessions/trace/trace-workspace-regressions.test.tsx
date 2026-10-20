// @vitest-environment jsdom
import { createElement } from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { fetchSessionAnalysis, regenerateSessionRecord, SessionAnalysisRequestError } from '@/lib/api';
import type { RecordWithContent, SessionAnalysis, SessionAnalysisFull } from '@/lib/session-analysis-types';
import type { TraceScope } from '@/lib/session-trace-types';
import { flagship } from '@/app/test/session-analysis/detail/flagship';
import { SessionAnalysisWorkspace } from './trace-workspace';

const state = vi.hoisted(() => ({ admin: false, query: 'session_id=A' }));
vi.mock('next/navigation', () => ({ useSearchParams: () => new URLSearchParams(state.query), usePathname: () => '/sessions/', useRouter: () => ({ replace: vi.fn() }) }));
vi.mock('@/components/common/auth-context', () => ({ useAuth: () => ({ user: { id: 7 }, isAdmin: state.admin }) }));
vi.mock('@/components/common/account-context', () => ({ useAccount: () => ({ selectedAccount: 'filtered-account' }) }));
vi.mock('@/lib/api', async original => ({ ...await original<typeof import('@/lib/api')>(), fetchSessionAnalysis: vi.fn(), regenerateSessionRecord: vi.fn() }));
const query = { isPending: false, isError: false, isFetching: false, hasNextPage: false, refetch: vi.fn(), fetchNextPage: vi.fn() };
vi.mock('@/lib/use-session-trace', () => ({ usePageVisible: () => true, useSessionTrace: () => ({ overview: query, sample: query, events: query, context: query, retry: vi.fn() }) }));
vi.mock('@/lib/use-turn-context', () => ({ useTurnContext: () => ({ ...query, records: [], loading: false }) }));
vi.mock('./trace-chart', () => ({ TraceChart: () => createElement('p', null, '전체 실행 기록') }));
vi.mock('../analysis/analysis-tab', () => ({ SessionAnalysisTab: ({ analysis, now, recordActionAcceptedMessage, onRegenerateRecord }: { analysis: SessionAnalysis; now: number; recordActionAcceptedMessage?: string; onRegenerateRecord?: () => void }) => createElement('section', null,
  createElement('p', null, `${analysis.session_id}:${analysis.state}`), createElement('p', { 'data-testid': 'clock' }, now),
  createElement('p', null, 'record' in analysis && analysis.record && 'headline' in analysis.record ? analysis.record.headline : undefined), createElement('p', null, recordActionAcceptedMessage),
  createElement('button', { onClick: onRegenerateRecord }, '다시 만들기')) }));
beforeEach(() => {
  state.admin = false; state.query = 'session_id=A';
  vi.mocked(fetchSessionAnalysis).mockReset().mockResolvedValue({ session_id: 'A', short_id: 'A', owner_view: true, state: 'building' });
  vi.mocked(regenerateSessionRecord).mockReset();
});
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); });
const setup = (scope?: TraceScope) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: React.ReactNode }) => createElement(QueryClientProvider, { client }, children);
  return render(createElement(SessionAnalysisWorkspace, { sessionId: 'A', scope }), { wrapper });
};
it('distinguishes a denied or missing analysis from a transient server failure', async () => {
  vi.mocked(fetchSessionAnalysis).mockRejectedValueOnce(new SessionAnalysisRequestError(404)); setup();
  expect(await screen.findByText('이 세션의 분석이 없거나 볼 권한이 없습니다.')).toBeTruthy();
});
it('offers an explicit retry for 503 and restores the independently loaded analysis', async () => {
  vi.mocked(fetchSessionAnalysis).mockRejectedValueOnce(new SessionAnalysisRequestError(503)); setup();
  expect(await screen.findByRole('alert')).toHaveProperty('textContent', expect.stringContaining('분석을 읽지 못했습니다'));
  fireEvent.click(screen.getByRole('button', { name: '다시 시도' }));
  expect(await screen.findByText('A:building')).toBeTruthy();
});
it.each(['stale', 'ready'] as const)('keeps an existing %s record and the server retry time while regeneration waits for budget', async recordState => {
  vi.mocked(fetchSessionAnalysis).mockResolvedValue({ ...flagship().analysis as SessionAnalysisFull, session_id: 'A', record: {
    state: recordState, generated_at: '2026-10-09T09:00:00Z', covers_until: '2026-10-09T08:00:00Z', headline: `기존 ${recordState} 기록 본문`,
    bullets: [], review: [], tags: { kinds: [], components: [], screens: [], related: [] }, work: [],
  } });
  vi.mocked(regenerateSessionRecord).mockResolvedValue({ state: 'waiting_budget', retry_at: '2026-10-10T00:00:00Z' }); setup();
  await screen.findByText(`기존 ${recordState} 기록 본문`); fireEvent.click(screen.getByRole('button', { name: '다시 만들기' }));
  expect(await screen.findByText(/일일 토큰 한도로 대기 중/)).toHaveProperty('textContent', expect.stringContaining('2026'));
  expect(screen.getByText(`기존 ${recordState} 기록 본문`)).toBeTruthy(); expect(regenerateSessionRecord).toHaveBeenCalledWith('A');
});
it('preserves an explicitly empty owner-search scope instead of falling back to the selected admin account', async () => {
  state.admin = true; setup({}); await screen.findByText('A:building');
  expect(fetchSessionAnalysis).toHaveBeenCalledWith('A', {}, expect.any(AbortSignal));
});
it('refreshes the analysis clock on reopening and during visible polling', async () => {
  const now = vi.spyOn(Date, 'now').mockReturnValue(1000);
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  const { rerender } = setup(); await screen.findByText('A:building');
  rerender(createElement(SessionAnalysisWorkspace, { sessionId: 'A', enabled: false })); now.mockReturnValue(61000);
  rerender(createElement(SessionAnalysisWorkspace, { sessionId: 'A', enabled: true }));
  await waitFor(() => expect(screen.getByTestId('clock').textContent).toBe('61000'));
  now.mockReturnValue(121000); await act(async () => { vi.advanceTimersByTime(60000); });
  expect(screen.getByTestId('clock').textContent).toBe('121000');
});

it.each(['generating', 'queued', 'stale'] as const)('refreshes an ended session with a %s record until generation completes', async pending => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  const generated: RecordWithContent = { state: 'ready', generated_at: null, covers_until: null, headline: '새로 생성된 기록',
    bullets: [], review: [], tags: { kinds: [], components: [], screens: [], related: [] }, work: [] };
  const complete: SessionAnalysisFull = { ...flagship().analysis as SessionAnalysisFull, session_id: 'A', state: 'ready', analysis_pending: false,
    live: { instances: [], collector: { heartbeat_at: null, last_stored_at: null } }, record: generated };
  const record = pending === 'generating' ? { state: 'generating' as const, generated_at: null, covers_until: null }
    : { ...generated, state: pending === 'stale' ? 'stale' as const : 'ready' as const, reason: pending === 'queued' ? 'queued' : undefined, headline: '기존 기록' };
  vi.mocked(fetchSessionAnalysis).mockResolvedValue({ ...complete, record });
  setup();
  await screen.findByText('A:ready');
  vi.mocked(fetchSessionAnalysis).mockResolvedValue(complete);
  await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
  await waitFor(() => expect(screen.getByText('새로 생성된 기록')).toBeTruthy());
  const calls = vi.mocked(fetchSessionAnalysis).mock.calls.length;
  await act(async () => { await vi.advanceTimersByTimeAsync(60000); });
  expect(fetchSessionAnalysis).toHaveBeenCalledTimes(calls);
});
