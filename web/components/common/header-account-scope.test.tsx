// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ role: 'admin', path: '/sessions', quotaUsage: { accounts: [] as unknown[] }, routerPush: vi.fn(), sessionSearch: vi.fn(),
  selectedAccount: '', setSelectedAccount: vi.fn(), ownAccountsOnly: false }));

vi.mock('next/navigation', () => ({ usePathname: () => mocks.path, useRouter: () => ({ push: mocks.routerPush }) }));
vi.mock('./auth-context', () => ({
  useAuth: () => ({ user: { id: 1, email: 'a@example.com', name: 'A', role: mocks.role }, logout: async () => {} }),
}));
// The own-accounts toggle is real state inside the header's render, so the mock keeps it in
// useState; the selected account stays a spy because only the reset call matters here.
vi.mock('./account-context', async () => {
  const { useState } = await import('react');
  return {
    useAccount: () => {
      const [ownAccountsOnly, setOwnAccountsOnly] = useState(mocks.ownAccountsOnly);
      return { selectedAccount: mocks.selectedAccount, setSelectedAccount: mocks.setSelectedAccount, ownAccountsOnly, setOwnAccountsOnly };
    },
  };
});
vi.mock('./agent-context', () => ({ useAgent: () => ({ selectedAgent: '', setSelectedAgent: () => {} }) }));
// jsdom 에는 ResizeObserver 가 없다. 스크롤 페이드 동작은 이 테스트의 관심사가 아니다.
vi.mock('@/components/common/scroll-fade-row', () => ({
  ScrollFadeRow: ({ children }: { children: unknown }) => createElement('div', null, children as never),
}));
vi.mock('@/lib/api', () => ({
  fetchAccounts: async (scope?: string) => (scope === 'mine' ? ['a@example.com'] : ['a@example.com', 'b@example.com']),
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
  mocks.selectedAccount = '';
  mocks.setSelectedAccount.mockReset();
  mocks.ownAccountsOnly = false;
});

// 서버가 role=user 요청의 login_email 을 버리는 곳은 세션 목록 경로뿐이다(#811).
// 비용·도구·이벤트·통계 핸들러는 login_email 을 그대로 적용하므로 그 화면에서는 칩을 지키고,
// 눌러도 효과 없는 세션 화면에서만 그 사실을 문안으로 밝힌다.
describe('Header Account scope', () => {
  it('role=user 에게는 Account 칩 대신 세션 목록 범위 안내를 보인다', async () => {
    renderHeader('user');

    expect(await screen.findByText('세션 목록은 본인 계정 전체 기준입니다')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'b' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Mine' })).toBeNull();
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

// 공동 화면이라 Account 칩은 기본으로 팀 전체 계정이다. "Mine"은 일반 사용자가 고르는
// 표시 설정으로, 서버의 scope=mine 목록으로 칩을 좁힌다. 관리자 목록은 서버도 좁히지 않는다.
describe('Header own-accounts toggle', () => {
  it('role=user 가 켜면 본인 계정 칩만 남고, 하나만 남아도 다시 끌 수 있다', async () => {
    renderHeader('user', '/');
    const user = userEvent.setup();

    expect(await screen.findByRole('button', { name: 'b' })).toBeTruthy();
    const toggle = screen.getByRole('button', { name: 'Mine' });
    expect(toggle.getAttribute('aria-pressed')).toBe('false');
    expect(toggle.querySelector('svg.lucide-square')).not.toBeNull();

    await user.click(toggle);

    await waitFor(() => expect(screen.queryByRole('button', { name: 'b' })).toBeNull());
    expect(screen.getByRole('button', { name: 'a' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Mine' }).getAttribute('aria-pressed')).toBe('true');
    // 계정 칩과 같은 알약 안에서도 켜고 끄는 스위치로 읽히도록 체크 상자를 함께 그린다.
    expect(screen.getByRole('button', { name: 'Mine' }).querySelector('svg.lucide-square-check')).not.toBeNull();
  });

  it('계정이 선택된 채로 켜면 선택을 All 로 되돌리고, 끌 때는 선택을 건드리지 않는다', async () => {
    mocks.selectedAccount = 'b@example.com';
    renderHeader('user', '/');
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'Mine' }));
    expect(mocks.setSelectedAccount).toHaveBeenCalledWith('');

    mocks.setSelectedAccount.mockReset();
    await user.click(screen.getByRole('button', { name: 'Mine' }));
    expect(mocks.setSelectedAccount).not.toHaveBeenCalled();
  });

  // 칩 줄은 min-w-0 이라 한 줄 헤더에서 0 폭까지 줄어든다. 토글(shrink-0)이 붙은 일반 사용자 줄은
  // 그러면 칩이 사라지고 토글이 옆 Agent 라벨 위로 넘친다(v0.7.71, 1200px). 최소 폭이 있어야
  // 줄이 다음 줄로 넘어간다. 관리자 줄은 이전 배치를 그대로 둔다.
  it('role=user 의 Account 줄은 최소 폭을 갖고, admin 줄은 이전 배치를 유지한다', async () => {
    renderHeader('user', '/');
    const userRow = (await screen.findByRole('button', { name: 'Mine' })).parentElement;
    expect(userRow?.className).toContain('min-w-56');

    cleanup();
    renderHeader('admin', '/');
    const adminRow = (await screen.findByRole('button', { name: 'b' })).closest('div.flex-1');
    expect(adminRow?.className).not.toContain('min-w-56');
  });

  // 같은 브라우저에 일반 사용자가 켜 둔 설정이 남아 있어도 관리자 칩은 좁히지 않는다.
  it('admin 에게는 토글이 없고, 저장된 설정이 켜져 있어도 전체 계정을 보인다', async () => {
    mocks.ownAccountsOnly = true;
    renderHeader('admin', '/');

    expect(await screen.findByRole('button', { name: 'b' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Mine' })).toBeNull();
  });
});
