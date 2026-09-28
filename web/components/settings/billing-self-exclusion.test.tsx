// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BillingSelfExclusion } from './billing-self-exclusion';
import { listObservedBillingAccounts, selfExcludeBillingAccount } from '@/lib/api';
import { USAGE_REBUILD_NOTE } from '@/lib/usage-rebuild';

vi.mock('@/lib/api', () => ({
  listObservedBillingAccounts: vi.fn(),
  selfExcludeBillingAccount: vi.fn(),
  removeSelfExcludedBillingAccount: vi.fn(),
}));

const renderSection = () => {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } });
  render(createElement(QueryClientProvider, { client }, createElement(BillingSelfExclusion)));
};

const EMPTY_TEXT = 'No billing accounts seen in your data yet.';

beforeEach(() => {
  vi.mocked(listObservedBillingAccounts).mockReset();
  vi.mocked(selfExcludeBillingAccount).mockReset().mockResolvedValue({ status: 'excluded' });
});
afterEach(cleanup);

describe('BillingSelfExclusion', () => {
  // A failed read says nothing about whether accounts exist; claiming there are
  // none would read as "nothing of yours is collected".
  it('does not claim the list is empty when reading it failed', async () => {
    vi.mocked(listObservedBillingAccounts).mockRejectedValue(new Error('Failed to list your billing accounts'));
    renderSection();

    expect(await screen.findByText('Failed to list your billing accounts')).not.toBeNull();
    expect(screen.queryByText(EMPTY_TEXT)).toBeNull();
  });

  // Excluding hides the account's data for everyone and refuses what arrives
  // from it, so one click must not do it.
  it('asks for confirmation before excluding', async () => {
    vi.mocked(listObservedBillingAccounts).mockResolvedValue({
      accounts: [
        { billing_provider: 'openai', account_id: 'acct-mine', shared: false, excluded: false, self_registered: false },
      ],
      usage_rebuild_pending: false,
    });
    renderSection();

    await userEvent.click(await screen.findByRole('button', { name: 'Exclude' }));
    expect(selfExcludeBillingAccount).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(selfExcludeBillingAccount).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole('button', { name: 'Exclude' }));
    await userEvent.click(screen.getByRole('button', { name: 'Confirm exclusion' }));
    expect(selfExcludeBillingAccount).toHaveBeenCalledWith('openai', 'acct-mine');
  });

  // An exclusion answers before the usage charts are rebuilt; until they are,
  // the section says so, and says nothing once they have caught up.
  it('says the charts are catching up only while the rebuild is pending', async () => {
    vi.mocked(listObservedBillingAccounts).mockResolvedValue({ accounts: [], usage_rebuild_pending: true });
    renderSection();
    expect(await screen.findByText(USAGE_REBUILD_NOTE)).not.toBeNull();
    cleanup();

    vi.mocked(listObservedBillingAccounts).mockResolvedValue({ accounts: [], usage_rebuild_pending: false });
    renderSection();
    expect(await screen.findByText(EMPTY_TEXT)).not.toBeNull();
    expect(screen.queryByText(USAGE_REBUILD_NOTE)).toBeNull();
  });
});
