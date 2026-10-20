import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import { Providers } from './providers';

const state = vi.hoisted(() => ({ pathname: '/local-sessions', authMounts: 0 }));
vi.mock('next/navigation', () => ({ usePathname: () => state.pathname }));
vi.mock('@tanstack/react-query', () => ({ QueryClient: class {} }));
vi.mock('@tanstack/react-query-persist-client', () => ({
  PersistQueryClientProvider: ({ children }: { children: React.ReactNode }) => children,
}));
vi.mock('@tanstack/query-async-storage-persister', () => ({ createAsyncStoragePersister: () => ({}) }));
vi.mock('./auth-context', () => ({ AuthProvider: ({ children }: { children: React.ReactNode }) => {
  state.authMounts++;
  return children;
} }));
vi.mock('./account-context', () => ({ AccountProvider: ({ children }: { children: React.ReactNode }) => children }));
vi.mock('./agent-context', () => ({ AgentProvider: ({ children }: { children: React.ReactNode }) => children }));
vi.mock('./appearance-context', () => ({ AppearanceProvider: ({ children }: { children: React.ReactNode }) => children }));
vi.mock('./pending-action-context', () => ({ PendingActionProvider: ({ children }: { children: React.ReactNode }) => children }));

const render = (pathname: string) => {
  state.pathname = pathname;
  state.authMounts = 0;
  return renderToStaticMarkup(createElement(Providers, null, 'content'));
};

describe('local-only providers', () => {
  it('does not mount server authentication for the local-only route', () => {
    expect(render('/local-sessions')).toContain('content');
    expect(state.authMounts).toBe(0);
    render('/local-sessions/');
    expect(state.authMounts).toBe(0);
  });

  it('keeps server authentication on dashboard and similarly named routes', () => {
    for (const pathname of ['/sessions', '/local-sessions-other', '/local-sessions/private']) {
      render(pathname);
      expect(state.authMounts).toBe(1);
    }
  });
});
