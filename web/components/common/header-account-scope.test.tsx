// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ role: 'admin', path: '/sessions', quotaUsage: { accounts: [] as unknown[] }, routerPush: vi.fn(), sessionSearch: vi.fn() }));

vi.mock('next/navigation', () => ({ usePathname: () => mocks.path, useRouter: () => ({ push: mocks.routerPush }) }));
vi.mock('./auth-context', () => ({
  useAuth: () => ({ user: { id: 1, email: 'a@example.com', name: 'A', role: mocks.role }, logout: async () => {} }),
}));
vi.mock('./account-context', () => ({ useAccount: () => ({ selectedAccount: '', setSelectedAccount: () => {} }) }));
vi.mock('./agent-context', () => ({ useAgent: () => ({ selectedAgent: '', setSelectedAgent: () => {} }) }));
// jsdom 에는 ResizeObserver 가 없다. 스크롤 페이드 동작은 이 테스트의 관심사가 아니다.
vi.mock('@/components/common/scroll-fade-row', () => ({
  ScrollFadeRow: ({ children }: { children: unknown }) => createElement('div', null, children as never),
}));
vi.mock('@/lib/api', () => ({
  fetchAccounts: async () => ['a@example.com', 'b@example.com'],
  fetchMyQuotaUsage: async () => mocks.quotaUsage,
  fetchSessionRecordSearch: mocks.sessionSearch,
}));

import { Header } from './header';

const renderHeader = (role: string, path = '/sessions') => {
  mocks.role = role;
  mocks.path = path;
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(createElement(QueryClientProvider, { client }, createElement(Header)));
};

afterEach(() => {
  cleanup();
  mocks.quotaUsage = { accounts: [] };
  mocks.routerPush.mockReset();
  mocks.sessionSearch.mockReset();
});

// 서버가 role=user 요청의 login_email 을 버리는 곳은 세션 목록 경로뿐이다(#811).
// 비용·도구·이벤트·통계 핸들러는 login_email 을 그대로 적용하므로 그 화면에서는 칩을 지키고,
// 눌러도 효과 없는 세션 화면에서만 그 사실을 문안으로 밝힌다.
describe('Header Account scope', () => {
  it('role=user 에게는 Account 칩 대신 세션 목록 범위 안내를 보인다', async () => {
    renderHeader('user');

    expect(await screen.findByText('세션 목록은 본인 계정 전체 기준입니다')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'b' })).toBeNull();
  });

  it.each(['/', '/cost'])('role=user 도 %s 에서는 Account 칩을 쓴다', async (path) => {
    renderHeader('user', path);

    expect(await screen.findByRole('button', { name: 'b' })).toBeTruthy();
    expect(screen.queryByText('세션 목록은 본인 계정 전체 기준입니다')).toBeNull();
  });

  it('admin 에게는 Account 칩을 보인다', async () => {
    renderHeader('admin');

    expect(await screen.findByRole('button', { name: 'b' })).toBeTruthy();
    expect(screen.queryByText('세션 목록은 본인 계정 전체 기준입니다')).toBeNull();
  });

  it('전역 헤더의 A1 미터는 private quota API의 실제 측정값을 쓴다', async () => {
    mocks.quotaUsage = { accounts: [{
      billing_provider: 'anthropic', account_id: 'acct-fixture', label: 'Claude · …fixture', plan: '', binding_status: 'approved',
      windows: {
        five_hour: { used_pct: 42, resets_at: new Date(Date.now() + 3_600_000).toISOString(), sampled_at: new Date().toISOString(), attribution: 'observed', state: 'measured', source: 'client_reported' },
        seven_day: { used_pct: 0, resets_at: new Date(Date.now() + 24 * 3_600_000).toISOString(), sampled_at: new Date().toISOString(), attribution: 'observed', state: 'measured', source: 'client_reported' },
      },
    }] };
    renderHeader('user', '/');
    expect(await screen.findByText('42%')).toBeTruthy();
    expect(screen.getByText('0%')).toBeTruthy();
    expect(screen.getByText(/5H · .*뒤 재설정/)).toBeTruthy();
    expect(screen.getByText(/7D · .*뒤 재설정/)).toBeTruthy();
    expect(screen.queryByRole('radio')).toBeNull();
    mocks.quotaUsage = { accounts: [] };
  });

  it('실제 전역 검색은 /api/session-search의 턴 키로 세션 페이지에 이동한다', async () => {
    mocks.sessionSearch.mockResolvedValue([{ session_id: 'session-1', title: '세션 검색 연결', last_activity_at: null,
      matches: [{ field: 'work', text: '검색 결과를 해당 요청에 연결', turn_key: 'turn-9' }] }]);
    renderHeader('admin');
    const user = userEvent.setup();
    await user.type(screen.getByRole('searchbox', { name: '전체 검색' }), '검색');
    await user.click(await screen.findByRole('button', { name: /작업 항목 일치: 검색 결과를 해당 요청에 연결/ }));
    expect(mocks.routerPush).toHaveBeenCalledWith('/sessions?session_id=session-1&session_search=1&turn_key=turn-9');
  });
});
