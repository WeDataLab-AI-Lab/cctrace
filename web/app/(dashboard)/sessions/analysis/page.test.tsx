// @vitest-environment jsdom
import { cleanup, render, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import SessionAnalysisPage from './page';

const state = vi.hoisted(() => ({ replace: vi.fn(), traceImports: 0 }));
vi.mock('next/navigation', () => ({
  useRouter: () => ({ replace: state.replace }),
  useSearchParams: () => new URLSearchParams('session_id=A&login_email=scope%40test&turn_key=T&event_id=E&from=start&to=end'),
}));
vi.mock('@/components/sessions/trace/trace-workspace', () => {
  state.traceImports += 1;
  return { TraceWorkspace: () => null };
});
afterEach(cleanup);

it('redirects the old page without importing the analysis chart bundle', async () => {
  render(<SessionAnalysisPage />);
  await waitFor(() => expect(state.replace).toHaveBeenCalledWith('/sessions?session_id=A&login_email=scope%40test&turn_key=T&event_id=E&trace_from=start&trace_to=end&detail=analysis&expanded=1', { scroll: false }));
  expect(state.traceImports).toBe(0);
});
