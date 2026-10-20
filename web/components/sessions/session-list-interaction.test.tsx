// @vitest-environment jsdom
import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SessionOverview } from '@/lib/types';
import { SessionList } from './session-list';

const row: SessionOverview = {
  session_id: 'owner-session',
  profile_email: 'owner@example.com',
  model: '',
  agent: 'claude',
  start_time: '2026-10-01T00:00:00Z',
  end_time: '2026-10-01T01:00:00Z',
  input_tokens: 10,
  output_tokens: 20,
  cost_usd: 0.1,
  event_count: 3,
  has_sync: true,
  classification: 'working',
  first_line: 'private first line',
};

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe('session list interaction', () => {
  it('selects the same row by pointer and keyboard when private values are present', async () => {
    vi.stubGlobal('IntersectionObserver', class {
      observe() {}
      disconnect() {}
    });
    const onSelect = vi.fn();
    render(<SessionList sessions={[row]} isLoading={false} selectedId={null} onSelect={onSelect} hasMore={false} />);
    const card = screen.getByRole('button', { name: /owner-ses/ });
    expect(within(card).getByText('작업 중')).toBeTruthy();
    await userEvent.click(card);
    card.focus();
    await userEvent.keyboard('{Enter}');
    expect(onSelect).toHaveBeenCalledTimes(2);
    expect(onSelect).toHaveBeenNthCalledWith(1, row);
    expect(onSelect).toHaveBeenNthCalledWith(2, row);
  });
});
