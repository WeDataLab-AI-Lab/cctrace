// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { updateDashboardUser } from '@/lib/api';
import type { DashboardUserInfo } from '@/lib/types';
import { EditUserDialog } from './edit-user-dialog';

vi.mock('@/lib/api', () => ({ updateDashboardUser: vi.fn().mockResolvedValue({}) }));
vi.mock('./otel-user-ids', () => ({ fetchOtelUserIDs: vi.fn().mockResolvedValue([]) }));

// The first admin from /setup has no team, and `cctrace init` refuses a profile
// without one (#763). Edit is the only place left to give that account a team.
const setupAdmin: DashboardUserInfo = {
  id: 1,
  email: 'admin@example.com',
  role: 'admin',
  name: 'Admin',
  team: '',
  is_active: true,
  created_at: '2026-01-01T00:00:00Z',
  cctrace_user_id: '',
};

const teamedUser: DashboardUserInfo = { ...setupAdmin, id: 2, team: 'platform', cctrace_user_id: 'alice' };

const renderDialog = (user: DashboardUserInfo = setupAdmin) => {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } });
  render(
    createElement(
      QueryClientProvider,
      { client },
      createElement(EditUserDialog, { user, onClose: () => {}, onSuccess: () => {} }),
    ),
  );
};

afterEach(cleanup);
beforeEach(() => vi.mocked(updateDashboardUser).mockClear());

describe('EditUserDialog team', () => {
  it('saves the team typed for an account that has none, trimmed', async () => {
    renderDialog();

    await userEvent.type(screen.getByLabelText('Team'), '  platform ');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(updateDashboardUser).toHaveBeenCalledWith(1, expect.objectContaining({ team: 'platform' })),
    );
  });

  // The server refuses an empty team, so an untouched empty team is left out
  // of the request instead of failing an edit to the name.
  it('does not send an unchanged team', async () => {
    renderDialog();

    await userEvent.type(screen.getByLabelText('Name'), ' Doe');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(updateDashboardUser).toHaveBeenCalledTimes(1));
    expect(vi.mocked(updateDashboardUser).mock.calls[0][1].team).toBeUndefined();
  });

  it('will not save a cleared team', async () => {
    renderDialog(teamedUser);

    await userEvent.clear(screen.getByLabelText('Team'));
    const save = screen.getByRole('button', { name: 'Save' });

    expect(save).toHaveProperty('disabled', true);
    expect(screen.getByText('Team is required.')).not.toBeNull();
    await userEvent.click(save);
    expect(updateDashboardUser).not.toHaveBeenCalled();
  });
});
