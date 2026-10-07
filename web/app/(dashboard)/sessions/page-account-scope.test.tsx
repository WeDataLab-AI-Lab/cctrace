// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  role: null as 'admin' | 'user' | null,
  fetchSessionOverview: vi.fn(async () => []),
  fetchSessionOverviewCount: vi.fn(async () => ({ total: 0 })),
  fetchProjects: vi.fn(async () => []),
}));

vi.mock('next/navigation', () => ({ useSearchParams: () => new URLSearchParams() }));
vi.mock('@/components/common/auth-context', () => ({ useAuth: () => ({ user: mocks.role ? { role: mocks.role } : null, isAdmin: mocks.role === 'admin' }),
}));
vi.mock('@/components/common/account-context', () => ({
  useAccount: () => ({ selectedAccount: 'b@example.com', setSelectedAccount: () => {} }),
}));
vi.mock('@/components/common/agent-context', () => ({
  useAgent: () => ({ selectedAgent: '', setSelectedAgent: () => {} }),
}));
vi.mock('@/lib/api', () => ({
  fetchSessionOverview: mocks.fetchSessionOverview,
  fetchSessionOverviewCount: mocks.fetchSessionOverviewCount,
  fetchProjects: mocks.fetchProjects,
}));
// 이 테스트의 관심사는 페이지가 서버로 보내는 login_email 뿐이다.
vi.mock('@/components/common/filter-bar', () => ({ FilterBar: () => null }));
vi.mock('@/components/sessions/session-list', () => ({ SessionList: () => null }));
vi.mock('@/components/sessions/session-viewer', () => ({ SessionViewer: () => null }));
vi.mock('@/components/sessions/project-banner', () => ({ ProjectBanner: () => null }));
vi.mock('@/components/sessions/delete-project-dialog', () => ({ DeleteProjectDialog: () => null }));

import SessionsPage from './page';

const loginEmailOf = (fn: { mock: { calls: unknown[][] } }) =>
  (fn.mock.calls[0][0] as { loginEmail?: string } | undefined)?.loginEmail;

const renderPage = (role: 'admin' | 'user' | null) => {
  mocks.role = role;
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(createElement(QueryClientProvider, { client }, createElement(SessionsPage)));
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

// 다른 화면에서 고른 Account 가 AccountContext 에 남아 있어도, 헤더가 "본인 계정 전체"라고 밝히는
// role=user 세션 화면은 그 선택을 서버로 보내지 않는다(#811).
describe('SessionsPage Account scope', () => {
  it('role=user 는 남아 있는 Account 선택을 쿼리에 싣지 않는다', async () => {
    renderPage('user');

    await waitFor(() => {
      expect(mocks.fetchSessionOverview).toHaveBeenCalled();
      expect(mocks.fetchSessionOverviewCount).toHaveBeenCalled();
      expect(mocks.fetchProjects).toHaveBeenCalled();
    });
    expect(loginEmailOf(mocks.fetchSessionOverview)).toBeUndefined();
    expect(loginEmailOf(mocks.fetchSessionOverviewCount)).toBeUndefined();
    expect(loginEmailOf(mocks.fetchProjects)).toBeUndefined();
  });

  it('admin 은 Account 선택을 그대로 싣는다', async () => {
    renderPage('admin');

    await waitFor(() => expect(mocks.fetchSessionOverview).toHaveBeenCalled());
    expect(loginEmailOf(mocks.fetchSessionOverview)).toBe('b@example.com');
  });

  // 서버 role 은 admin|user 뿐이다. 대시보드 레이아웃은 인증 확정 전에는 이 페이지를 마운트하지 않으므로
  // user=null 은 실제 화면에 없는 방어 경로다. user 가 아닌 쪽을 제한 사용자로 보지 않는 기준만 고정한다.
  it('user 가 없으면 제한 사용자로 보지 않아 Account 선택을 싣는다', async () => {
    renderPage(null);

    await waitFor(() => expect(mocks.fetchSessionOverview).toHaveBeenCalled());
    expect(loginEmailOf(mocks.fetchSessionOverview)).toBe('b@example.com');
  });
});
