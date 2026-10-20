// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fetchMyQuotaUsage } from '@/lib/api';
import { QuotaHeaderMeters } from './quota-header-meters';

vi.mock('@/lib/api', () => ({ fetchMyQuotaUsage: vi.fn() }));

const account = {
  billing_provider: 'anthropic' as const,
  account_id: 'acct-header-fixture',
  label: 'Claude · …fixture',
  plan: 'max',
  binding_status: 'approved' as const,
  windows: {
    five_hour: { used_pct: 42, resets_at: new Date(Date.now() + 2 * 60 * 60_000).toISOString(), sampled_at: new Date().toISOString(), attribution: 'observed' as const, state: 'measured' as const, source: 'client_reported' as const },
    seven_day: { used_pct: 0, resets_at: new Date(Date.now() + 3 * 24 * 60 * 60_000).toISOString(), sampled_at: new Date().toISOString(), attribution: 'observed' as const, state: 'measured' as const, source: 'client_reported' as const },
  },
};

const renderHeaderMeters = (enabled = true) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(createElement(QueryClientProvider, { client }, createElement(QuotaHeaderMeters, { enabled })));
};

beforeEach(() => {
  vi.mocked(fetchMyQuotaUsage).mockResolvedValue({ from: '', to: '', accounts: [account] });
});
afterEach(() => { cleanup(); vi.clearAllMocks(); });

describe('QuotaHeaderMeters A1', () => {
  it('shows per-account 5H and 7D values and time to reset from the private usage API', async () => {
    renderHeaderMeters();
    expect(await screen.findByText('42%')).toBeTruthy();
    expect(screen.getByText('0%')).toBeTruthy();
    expect(screen.getByText(/5H · .*뒤 재설정/)).toBeTruthy();
    expect(screen.getByText(/7D · .*뒤 재설정/)).toBeTruthy();
    expect(screen.queryByRole('radio')).toBeNull();
    expect(fetchMyQuotaUsage).toHaveBeenCalledTimes(1);
  });

  it('does not read the personal API before dashboard authentication', () => {
    renderHeaderMeters(false);
    expect(fetchMyQuotaUsage).not.toHaveBeenCalled();
  });
});
