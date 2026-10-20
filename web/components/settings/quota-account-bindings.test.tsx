// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { approveQuotaAccountBinding, fetchAdminQuotaBindings, fetchMyQuotaBindings, requestQuotaAccountBinding, revokeQuotaAccountBinding } from '@/lib/api';
import { useAuth } from '@/components/common/auth-context';
import { QuotaAccountBindings } from './quota-account-bindings';

vi.mock('@/lib/api', () => ({
  approveQuotaAccountBinding: vi.fn(),
  fetchAdminQuotaBindings: vi.fn(),
  fetchMyQuotaBindings: vi.fn(),
  requestQuotaAccountBinding: vi.fn(),
  revokeQuotaAccountBinding: vi.fn(),
}));

vi.mock('@/components/common/auth-context', () => ({
  useAuth: vi.fn(),
}));

const pending = {
  id: 19, dashboard_user_id: 7, owner_email: 'owner@example.test', billing_provider: 'anthropic' as const,
  account_id: 'acct-fixture', status: 'pending' as const, requested_at: '2026-10-09T00:00:00Z',
};

const renderPanel = () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(createElement(QueryClientProvider, { client }, createElement(QuotaAccountBindings)));
};

beforeEach(() => {
  vi.mocked(useAuth).mockReturnValue({ user: { role: 'admin' } } as ReturnType<typeof useAuth>);
  vi.mocked(fetchMyQuotaBindings).mockResolvedValue([]);
  vi.mocked(fetchAdminQuotaBindings).mockImplementation(async (status) => status === 'pending' ? [pending] : []);
  vi.mocked(approveQuotaAccountBinding).mockResolvedValue({ ...pending, status: 'approved' });
  vi.mocked(revokeQuotaAccountBinding).mockResolvedValue();
  vi.mocked(requestQuotaAccountBinding).mockResolvedValue({ ...pending, status: 'pending' });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('QuotaAccountBindings', () => {
  it('does not enable approval until the administrator confirms independent provider-console verification', async () => {
    renderPanel();
    const approve = await screen.findByRole('button', { name: '확인 승인' });
    expect(approve).toHaveProperty('disabled', true);
    expect(screen.queryByLabelText(/스크린샷|자격/)).toBeNull();
    await userEvent.type(screen.getByLabelText('확인 참조값'), 'VERIFY-19');
    expect(approve).toHaveProperty('disabled', true);
    await userEvent.click(screen.getByLabelText('provider console에서 독립 확인함'));
    expect(approve).toHaveProperty('disabled', false);
    await userEvent.click(approve);
    expect(approveQuotaAccountBinding).toHaveBeenCalledWith(19, 'VERIFY-19');
  });

  it('sends only provider and account id for a user request', async () => {
    vi.mocked(useAuth).mockReturnValue({ user: { role: 'user' } } as ReturnType<typeof useAuth>);
    renderPanel();
    await userEvent.selectOptions(screen.getByLabelText('Provider'), 'openai');
    await userEvent.type(screen.getByLabelText('계정 ID'), 'acct-user-fixture');
    await userEvent.click(screen.getByRole('button', { name: '확인 요청' }));
    expect(requestQuotaAccountBinding).toHaveBeenCalledWith({ billing_provider: 'openai', account_id: 'acct-user-fixture' });
    expect(screen.queryByText(/승인은 provider console/)).toBeNull();
  });
});
