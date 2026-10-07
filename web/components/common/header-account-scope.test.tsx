// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ role: 'admin', path: '/sessions' }));

vi.mock('next/navigation', () => ({ usePathname: () => mocks.path }));
vi.mock('./auth-context', () => ({
  useAuth: () => ({ user: { id: 1, email: 'a@example.com', name: 'A', role: mocks.role }, logout: async () => {} }),
}));
vi.mock('./account-context', () => ({ useAccount: () => ({ selectedAccount: '', setSelectedAccount: () => {} }) }));
vi.mock('./agent-context', () => ({ useAgent: () => ({ selectedAgent: '', setSelectedAgent: () => {} }) }));
// jsdom 에는 ResizeObserver 가 없다. 스크롤 페이드 동작은 이 테스트의 관심사가 아니다.
vi.mock('@/components/common/scroll-fade-row', () => ({
  ScrollFadeRow: ({ children }: { children: unknown }) => createElement('div', null, children as never),
}));
vi.mock('@/lib/api', () => ({ fetchAccounts: async () => ['a@example.com', 'b@example.com'] }));

import { Header } from './header';

const renderHeader = (role: string, path = '/sessions') => {
  mocks.role = role;
  mocks.path = path;
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(createElement(QueryClientProvider, { client }, createElement(Header)));
};

afterEach(cleanup);

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
});
