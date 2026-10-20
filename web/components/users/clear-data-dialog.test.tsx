// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { deleteUserData } from '@/lib/api';
import type { DashboardUserInfo } from '@/lib/types';
import { ClearDataDialog } from './clear-data-dialog';

vi.mock('@/lib/api', () => ({ deleteUserData: vi.fn().mockResolvedValue({}) }));

const user: DashboardUserInfo = {
  id: 2,
  email: 'alice@example.com',
  role: 'user',
  name: 'Alice',
  team: 'platform',
  is_active: true,
  created_at: '2026-01-01T00:00:00Z',
  cctrace_user_id: 'alice',
};

const renderDialog = () => {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } });
  render(
    createElement(
      QueryClientProvider,
      { client },
      createElement(ClearDataDialog, { user, onClose: () => {}, onSuccess: () => {} }),
    ),
  );
};

afterEach(cleanup);
beforeEach(() => vi.mocked(deleteUserData).mockClear());

describe('ClearDataDialog', () => {
  // The server deletes the account's privacy settings with the rest, and a session
  // collected afterwards is no longer hidden from AI session records. The dialog is the
  // last place to say so before the data is gone.
  it('says that the privacy settings are deleted too', () => {
    renderDialog();

    expect(screen.getByText(/privacy settings.*AI session records/i)).toBeTruthy();
  });

  it('clears the data of the account it names', async () => {
    renderDialog();

    await userEvent.click(screen.getByRole('button', { name: 'Clear All Data' }));

    await waitFor(() => expect(deleteUserData).toHaveBeenCalledWith('alice@example.com', 'alice'));
  });
});
