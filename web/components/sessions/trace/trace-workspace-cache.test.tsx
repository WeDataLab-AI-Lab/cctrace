// @vitest-environment jsdom
import { createElement } from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, expect, it, vi } from 'vitest';
import { fetchSessionAnalysis } from '@/lib/api';
import { fetchTurnContext, TurnContextRequestError } from '@/lib/turn-context-api';
import type { SessionAnalysis } from '@/lib/session-analysis-types';
import type { SessionRecord } from '@/lib/types';
import { SessionAnalysisWorkspace } from './trace-workspace';

vi.mock('next/navigation', () => ({ useSearchParams: () => new URLSearchParams('session_id=A&turn_key=first&detail=analysis'), usePathname: () => '/sessions/', useRouter: () => ({ replace: vi.fn() }) }));
vi.mock('@/components/common/auth-context', () => ({ useAuth: () => ({ user: { id: 7 }, isAdmin: false }) }));
vi.mock('@/components/common/account-context', () => ({ useAccount: () => ({ selectedAccount: '' }) }));
vi.mock('@/lib/api', async original => ({ ...await original<typeof import('@/lib/api')>(), fetchSessionAnalysis: vi.fn() }));
vi.mock('@/lib/turn-context-api', () => ({ fetchTurnContext: vi.fn(), TurnContextRequestError: class extends Error { constructor(readonly status: number) { super(String(status)); } } }));
vi.mock('@/lib/use-session-trace', () => ({ usePageVisible: () => true, useSessionTrace: () => ({ overview: {}, sample: {}, events: {}, context: {}, retry: vi.fn() }) }));
vi.mock('../analysis/analysis-tab', () => ({ SessionAnalysisTab: ({ executionFlow }: { executionFlow: React.ReactNode }) => executionFlow }));
vi.mock('./trace-chart', () => ({ TraceChart: () => null }));
afterEach(cleanup);
it('never re-exposes revoked originals when a late analysis supplies their previously discovered revision', async () => {
  let finish!: (value: SessionAnalysis) => void;
  vi.mocked(fetchSessionAnalysis).mockImplementation(() => new Promise(resolve => { finish = resolve; }));
  const record: SessionRecord = { session_id: 'A', project_hash: '', profile_email: '', model: '', ts: '', record_type: 'user', agent: 'claude',
    view: { kind: 'message', role: 'user', anchor_id: 'original', dedupe_key: '', text: 'revoked original' } };
  vi.mocked(fetchTurnContext).mockResolvedValueOnce({ session_id: 'A', turn_key: 'first', state: 'ready', revision: 'r1', records: [record], next_cursor: 'more' })
    .mockRejectedValue(new TurnContextRequestError(404));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(createElement(QueryClientProvider, { client }, createElement(SessionAnalysisWorkspace, { sessionId: 'A' })));
  expect(await screen.findByText('revoked original')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: '원문 더 읽기' }));
  await waitFor(() => expect(screen.queryByText('revoked original')).toBeNull());
  await act(async () => finish({ session_id: 'A', short_id: 'A', owner_view: true, state: 'building', turn_context_revision: 'r1' }));
  expect(screen.queryByText('revoked original')).toBeNull();
  await waitFor(() => expect(fetchTurnContext).toHaveBeenCalledTimes(3));
  expect(screen.queryByText('revoked original')).toBeNull();
});
