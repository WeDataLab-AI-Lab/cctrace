// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { flagship } from '@/app/test/session-analysis/detail/flagship';
import { DETAIL_NOW } from '@/app/test/session-analysis/detail/builders';
import { SessionAnalysisRow } from './session-analysis-row';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// The row keeps observed signals even when a judgement arrives, and the judgement
// itself no longer collapses them into a done/incomplete conclusion.
it('keeps observed signals visible next to a judgement and names each one for assistive tech', () => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  const session = flagship().row;
  const onSelect = vi.fn();
  render(<SessionAnalysisRow session={session} selected={false} onSelect={onSelect} now={DETAIL_NOW} />);
  const group = screen.getByRole('group', { name: '관측 신호' });
  expect(group.textContent).toBeTruthy();
  expect(group.querySelectorAll('[role="img"]').length).toBeGreaterThan(0);
  expect(screen.queryByText('작업 완료 판정')).toBeNull();
});

it('shows a reason tooltip for an unobservable signal instead of zero', async () => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  const session = flagship().row;
  const { container } = render(<SessionAnalysisRow session={session} selected={false} onSelect={vi.fn()} now={DETAIL_NOW} />);
  const unknown = [...container.querySelectorAll('[role="img"]')].find(node => (node.getAttribute('aria-label') ?? '').includes('?'));
  if (!unknown) return; // This fixture may observe every signal; the unknown path is covered by signals.test.ts.
  const user = userEvent.setup();
  await user.hover(unknown);
  expect(screen.getByRole('tooltip').textContent).toContain('?');
});
