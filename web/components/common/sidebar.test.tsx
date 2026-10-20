import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import { Sidebar } from './sidebar';

const authMock = vi.hoisted(() => ({ mustChangePassword: false }));

vi.mock('next/navigation', () => ({
  usePathname: () => '/settings',
}));

vi.mock('@tanstack/react-query', () => ({
  useQuery: () => ({ data: 'v0.7.44' }),
}));

vi.mock('@/components/common/auth-context', () => ({
  useAuth: () => ({
    isAdmin: false,
    user: { must_change_password: authMock.mustChangePassword },
  }),
}));

vi.mock('./auth-context', () => ({
  useAuth: () => ({
    isAdmin: false,
    user: { must_change_password: authMock.mustChangePassword },
  }),
}));

vi.mock('./appearance-context', () => ({
  useAppearance: () => ({ showLogoMark: true }),
}));

const renderSidebar = (mustChangePassword: boolean): string => {
  authMock.mustChangePassword = mustChangePassword;
  return renderToStaticMarkup(createElement(Sidebar));
};

// The dashboard layout sends every route but Settings back to Settings while the
// password is temporary. Rows that still render as links look responsive and act
// dead: the click changes nothing and says nothing.
describe('Sidebar while the password is temporary', () => {
  it('draws every route but Settings as disabled', () => {
    const markup = renderSidebar(true);

    expect(markup).toContain('aria-disabled="true"');
    expect(markup).not.toContain('href="/sessions"');
    expect(markup).not.toContain('href="/users"');
    expect(markup).toContain('href="/settings"');
  });

  it('leaves the nav alone once the password is settled', () => {
    const markup = renderSidebar(false);

    expect(markup).not.toContain('aria-disabled="true"');
    expect(markup).toContain('href="/sessions"');
    expect(markup).toContain('href="/users"');
    expect(markup).toContain('href="/settings"');
  });
});

it('retains accessible icon navigation and tooltips in the collapsed rail', () => {
  authMock.mustChangePassword = false;
  const markup = renderToStaticMarkup(createElement(Sidebar, { collapsed: true }));
  expect(markup).toContain('data-collapsed="true"');
  expect(markup).toContain('min-w-16');
  expect(markup).not.toContain('min-w-[248px]');
  expect(markup).toContain('href="/sessions"');
  expect(markup).toContain('aria-label="Sessions"');
  expect(markup).toContain('title="Sessions"');
  expect(markup).toContain('sr-only');
});
