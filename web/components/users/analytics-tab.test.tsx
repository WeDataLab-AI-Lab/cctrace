// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AuthUser } from '@/lib/types';

const mocks = vi.hoisted(() => ({
  user: null as unknown,
  selectedUser: '',
}));

vi.mock('next/navigation', () => ({ useSearchParams: () => new URLSearchParams({ user: mocks.selectedUser }) }));
vi.mock('@/components/common/auth-context', () => ({ useAuth: () => ({ user: mocks.user }) }));
vi.mock('@/components/common/account-context', () => ({ useAccount: () => ({ selectedAccount: '' }) }));
vi.mock('@/components/common/pending-action-context', () => ({
  usePendingAction: () => ({ pendingAction: null, setPendingAction: () => {} }),
}));
vi.mock('@/components/common/filter-bar', () => ({ FilterBar: () => null }));
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>();
  const card = (profile_email: string, user_id: string) => ({
    profile_email, user_id, user_team: '', model: 'claude-x', agent: 'claude', billing_provider: 'anthropic',
    total_cost: 1, total_input_tokens: 1, total_output_tokens: 1, request_count: 1,
  });
  return {
    ...actual,
    fetchCostByUser: async () => [card('a@example.com', 'user-a'), card('b@example.com', 'user-b'), card('b@example.com', 'user-x')],
    fetchCostByModel: async () => [],
    fetchUserNameMap: async () => ({}),
  };
});
// UserDetail 자체는 user-detail*.test 가 지킨다. 여기서는 연결(prop)만 본다.
vi.mock('./user-detail', () => ({
  UserDetail: ({ email, canViewSessions, sessionsAcrossProfiles }: { email: string; canViewSessions: boolean; sessionsAcrossProfiles?: boolean }) =>
    createElement('div', {
      'data-testid': 'detail',
      'data-email': email,
      'data-can-view': String(canViewSessions),
      'data-across': String(sessionsAcrossProfiles),
    }),
}));

import { AnalyticsTab } from './analytics-tab';

const me: AuthUser = { id: 1, email: 'b@example.com', role: 'user', name: 'B', cctrace_user_id: 'user-b' };

let client: QueryClient;

const renderTab = async (selectedUser: string, user: AuthUser | null = me) => {
  mocks.user = user;
  mocks.selectedUser = selectedUser;
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(createElement(QueryClientProvider, { client }, createElement(AnalyticsTab, { isAdmin: false })));
  return screen.findByTestId('detail');
};

afterEach(() => {
  cleanup();
  client?.clear();
});

describe('AnalyticsTab -> UserDetail canViewSessions 연결 (#799)', () => {
  it('타인 카드를 펼치면 canViewSessions=false', async () => {
    const detail = await renderTab('a@example.com');
    expect(detail.dataset.email).toBe('a@example.com');
    expect(detail.dataset.canView).toBe('false');
  });

  it('본인 카드를 펼치면 canViewSessions=true', async () => {
    const detail = await renderTab('user-b');
    expect(detail.dataset.email).toBe('b@example.com');
    expect(detail.dataset.canView).toBe('true');
  });

  it('admin 은 타인 카드도 canViewSessions=true', async () => {
    const detail = await renderTab('a@example.com', { ...me, role: 'admin' });
    expect(detail.dataset.canView).toBe('true');
  });

  // 서버는 role=user 조회를 호출자 user_id 로 덮어쓴다. 이메일만 같고 user_id 가 다른 카드는 내 세션이 아니다.
  it('이메일만 본인과 같고 user_id 가 다른 카드는 canViewSessions=false', async () => {
    const detail = await renderTab('user-x');
    expect(detail.dataset.email).toBe('b@example.com');
    expect(detail.dataset.canView).toBe('false');
  });
});

describe('AnalyticsTab -> UserDetail sessionsAcrossProfiles 연결 (#809)', () => {
  it('role=user 는 true, admin 은 false', async () => {
    const detail = await renderTab('b@example.com');
    expect(detail.dataset.across).toBe('true');
    cleanup();
    const adminDetail = await renderTab('b@example.com', { ...me, role: 'admin' });
    expect(adminDetail.dataset.across).toBe('false');
  });
});
