// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  role: null as 'admin' | 'user' | null,
  // 서버의 세션 분석 기능. 꺼져 있으면 페이지는 기존 목록·개수·프로젝트 쿼리만 쓴다.
  analysis: false,
  fetchSessionOverview: vi.fn(async () => []),
  fetchSessionOverviewCount: vi.fn(async () => ({ total: 0 })),
  fetchProjects: vi.fn(async () => []),
  fetchSessionOverviewCategories: vi.fn(async () => ({})),
  fetchSessionGroups: vi.fn(async () => ({
    scope: 'filtered_total' as const, grouping_version: 1, truncated: false, overview_total: 0, running: [], ended: [],
    counts: { running: 0, ended: 0, ended_groups: 0, running_by: {} },
  })),
}));

vi.mock('next/navigation', () => ({
  useSearchParams: () => new URLSearchParams(),
  useRouter: () => ({ replace: vi.fn() }),
  usePathname: () => '/sessions',
}));
vi.mock('@/components/common/auth-context', () => ({ useAuth: () => ({ user: mocks.role ? { id: 1, role: mocks.role, email: 'owner@example.test', cctrace_user_id: 'owner-uid', user_id_access_control: true } : null, isAdmin: mocks.role === 'admin' }),
}));
vi.mock('@/components/common/account-context', () => ({
  useAccount: () => ({ selectedAccount: 'b@example.com', setSelectedAccount: () => {} }),
}));
vi.mock('@/components/common/agent-context', () => ({
  useAgent: () => ({ selectedAgent: '', setSelectedAgent: () => {} }),
}));
vi.mock('@/lib/api', async () => ({
  fetchSessionOverview: mocks.fetchSessionOverview,
  fetchSessionOverviewCount: mocks.fetchSessionOverviewCount,
  fetchProjects: mocks.fetchProjects,
  fetchMaintenanceStatus: async () => ({ session_analysis: { enabled: mocks.analysis } }),
  fetchSessionOverviewCategories: mocks.fetchSessionOverviewCategories,
  fetchSessionCalendar: vi.fn(),
  fetchUsers: vi.fn(),
  SessionCalendarRequestError: (await vi.importActual<typeof import('@/lib/api')>('@/lib/api')).SessionCalendarRequestError,
}));
vi.mock('@/lib/personal-state-api', () => ({ fetchPersonalNoteOverview: vi.fn() }));
vi.mock('@/lib/session-groups-api', () => ({
  fetchSessionGroups: mocks.fetchSessionGroups,
  fetchCompleteSessionGroup: vi.fn(),
  updateSessionGroupDismissals: vi.fn(),
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

// 세션 요약 컨트롤의 가로 스크롤 줄이 ResizeObserver 를 쓴다. jsdom 에는 없다.
beforeEach(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
  mocks.analysis = false;
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

  // 세션 분석이 켜지면 목록 쿼리에 분석 범위가 붙고 범주 개수·세션 묶음 쿼리가 더 나간다.
  // 남아 있는 Account 선택은 그 어느 쪽에도 실리지 않는다.
  it('role=user 는 분석이 켜져 있어도 Account 선택을 새 쿼리에 싣지 않는다', async () => {
    mocks.analysis = true;
    renderPage('user');
    fireEvent.click(await screen.findByRole('button', { name: '세션 요약' }));

    await waitFor(() => {
      expect(mocks.fetchSessionOverviewCategories).toHaveBeenCalled();
      expect(mocks.fetchSessionGroups).toHaveBeenCalled();
      expect((mocks.fetchSessionOverview.mock.calls as unknown[][]).some(([arg]) => (arg as { analysis?: boolean } | undefined)?.analysis === true)).toBe(true);
    });
    for (const fn of [mocks.fetchSessionOverview, mocks.fetchSessionOverviewCount, mocks.fetchSessionOverviewCategories, mocks.fetchSessionGroups]) {
      for (const [arg] of fn.mock.calls as unknown[][]) {
        expect((arg as { loginEmail?: string } | undefined)?.loginEmail).toBeUndefined();
      }
    }
  });

  it('admin 의 세션 묶음 쿼리는 Account 선택을 싣는다', async () => {
    renderPage('admin');

    await waitFor(() => expect(mocks.fetchSessionGroups).toHaveBeenCalled());
    expect(loginEmailOf(mocks.fetchSessionGroups)).toBe('b@example.com');
  });

  // 서버 role 은 admin|user 뿐이다. 대시보드 레이아웃은 인증 확정 전에는 이 페이지를 마운트하지 않으므로
  // user=null 은 실제 화면에 없는 방어 경로다. user 가 아닌 쪽을 제한 사용자로 보지 않는 기준만 고정한다.
  it('user 가 없으면 제한 사용자로 보지 않아 Account 선택을 싣는다', async () => {
    renderPage(null);

    await waitFor(() => expect(mocks.fetchSessionOverview).toHaveBeenCalled());
    expect(loginEmailOf(mocks.fetchSessionOverview)).toBe('b@example.com');
  });
});
