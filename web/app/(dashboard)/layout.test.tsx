// @vitest-environment jsdom
import { createElement, type ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import DashboardLayout from './layout';
import { fetchMaintenanceStatus } from '@/lib/api';
import { POLL_LIVE } from '@/lib/query-config';
import { TOKEN_RULE_NOTE } from '@/lib/token-rule';
import type { MaintenanceStatus } from '@/lib/types';
import { useSessionLayout } from '@/components/common/session-layout-context';

const navigation = vi.hoisted(() => ({ pathname: '/', menuRenders: [] as boolean[] }));
vi.mock('next/navigation', () => ({
  useRouter: () => ({ replace: () => {} }),
  usePathname: () => navigation.pathname,
}));
vi.mock('@/components/common/sidebar', () => ({ Sidebar: ({ collapsed }: { collapsed?: boolean }) => {
  navigation.menuRenders.push(collapsed === true);
  return <aside data-collapsed={collapsed}>site menu</aside>;
} }));
vi.mock('@/components/common/header', () => ({ Header: () => 'site header' }));
const auth = vi.hoisted(() => ({
  user: null as { id: number; email: string; must_change_password: boolean } | null,
}));
vi.mock('@/components/common/auth-context', () => ({
  useAuth: () => ({ user: auth.user, isLoading: false, needsSetup: false }),
}));
vi.mock('@/lib/api', () => ({ fetchMaintenanceStatus: vi.fn() }));

const KEY = ['maintenance-status'];
const PENDING: MaintenanceStatus = { token_rule_pending: true, step: 'session_records_message_id_v1' };
const SETTLED: MaintenanceStatus = { token_rule_pending: false, step: '' };
const unreadable = () => new Error('Failed to fetch maintenance status');

const renderLayout = (children: ReactNode = createElement('p', null, 'page')) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidate = vi.spyOn(client, 'invalidateQueries').mockResolvedValue();
  const page = () => createElement(QueryClientProvider, { client }, createElement(DashboardLayout, null, children));
  const view = render(page());
  const navigate = (pathname: string) => {
    navigation.pathname = pathname;
    view.rerender(page());
  };
  return { client, invalidate, view, navigate };
};

beforeEach(() => {
  localStorage.clear();
  navigation.pathname = '/';
  navigation.menuRenders = [];
  auth.user = { id: 7, email: 'user@example.com', must_change_password: false };
  vi.mocked(fetchMaintenanceStatus).mockReset();
});
afterEach(cleanup);

it.each(['/sessions', '/sessions/'])('starts with a collapsed app menu on %s', (pathname) => {
  navigation.pathname = pathname;
  vi.mocked(fetchMaintenanceStatus).mockResolvedValue(SETTLED);
  renderLayout();
  expect(screen.getByText('site menu').getAttribute('data-collapsed')).toBe('true');
});

it.each(['/', '/cost', '/sessions-extra'])('keeps the ordinary app menu open on %s', (pathname) => {
  navigation.pathname = pathname;
  vi.mocked(fetchMaintenanceStatus).mockResolvedValue(SETTLED);
  renderLayout();
  expect(screen.getByText('site menu').getAttribute('data-collapsed')).toBe('false');
});

const ChromeControls = () => {
  const layout = useSessionLayout();
  return <><button onClick={layout.toggleMenu}>menu toggle</button><button onClick={() => layout.setExpanded(!layout.expanded)}>expand toggle</button><input aria-label="viewer draft" /></>;
};

it.each([false, true])('keeps session menu choices separate from the existing %s dashboard preference across navigation', (outsideCollapsed) => {
  localStorage.setItem('appearance:session-menu:7', JSON.stringify(outsideCollapsed));
  navigation.pathname = '/sessions/';
  vi.mocked(fetchMaintenanceStatus).mockResolvedValue(SETTLED);
  const { navigate, view } = renderLayout(<ChromeControls />);
  expect(screen.getByText('site menu').getAttribute('data-collapsed')).toBe('true');
  fireEvent.click(screen.getByText('menu toggle'));
  expect(screen.getByText('site menu').getAttribute('data-collapsed')).toBe('false');
  expect(localStorage.getItem('appearance:session-menu:7')).toBe(JSON.stringify(outsideCollapsed));
  const draft = screen.getByRole('textbox', { name: 'viewer draft' });
  fireEvent.change(draft, { target: { value: '세션 메뉴 유지' } });

  navigation.menuRenders = [];
  navigate('/cost');
  expect(new Set(navigation.menuRenders)).toEqual(new Set([outsideCollapsed]));
  navigation.menuRenders = [];
  navigate('/sessions');
  expect(new Set(navigation.menuRenders)).toEqual(new Set([false]));
  expect(screen.getByRole('textbox', { name: 'viewer draft' })).toBe(draft);
  expect(draft).toHaveProperty('value', '세션 메뉴 유지');
  view.unmount();

  renderLayout(<ChromeControls />);
  expect(screen.getByText('site menu').getAttribute('data-collapsed')).toBe('false');
  fireEvent.click(screen.getByText('menu toggle'));
  expect(screen.getByText('site menu').getAttribute('data-collapsed')).toBe('true');
});

it('isolates session menu choices when the signed-in user changes', () => {
  navigation.pathname = '/sessions';
  vi.mocked(fetchMaintenanceStatus).mockResolvedValue(SETTLED);
  const { navigate } = renderLayout(<ChromeControls />);
  expect(screen.getByText('site menu').getAttribute('data-collapsed')).toBe('true');
  fireEvent.click(screen.getByText('menu toggle'));
  expect(screen.getByText('site menu').getAttribute('data-collapsed')).toBe('false');

  auth.user = { id: 8, email: 'other@example.com', must_change_password: false };
  navigation.menuRenders = [];
  navigate('/sessions/');
  expect(new Set(navigation.menuRenders)).toEqual(new Set([true]));
  navigate('/cost');
  expect(screen.getByText('site menu').getAttribute('data-collapsed')).toBe('false');
  fireEvent.click(screen.getByText('menu toggle'));
  expect(screen.getByText('site menu').getAttribute('data-collapsed')).toBe('true');
  navigate('/sessions');
  expect(screen.getByText('site menu').getAttribute('data-collapsed')).toBe('true');

  auth.user = { id: 7, email: 'user@example.com', must_change_password: false };
  navigate('/sessions');
  expect(screen.getByText('site menu').getAttribute('data-collapsed')).toBe('false');
  navigate('/cost');
  expect(screen.getByText('site menu').getAttribute('data-collapsed')).toBe('false');
});

it('temporarily hides all chrome during expansion and preserves the viewer and menu preference', async () => {
  vi.mocked(fetchMaintenanceStatus).mockResolvedValue(SETTLED);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><DashboardLayout><ChromeControls /></DashboardLayout></QueryClientProvider>);
  const draft = screen.getByRole('textbox', { name: 'viewer draft' });
  fireEvent.change(draft, { target: { value: '보존' } });
  fireEvent.click(screen.getByText('menu toggle'));
  expect(screen.getByText('site menu').getAttribute('data-collapsed')).toBe('true');
  expect(screen.getByText('site menu').closest('[hidden]')).toBeNull();
  fireEvent.click(screen.getByText('expand toggle'));
  expect(screen.getByText('site header').closest('[hidden]')).toBeTruthy();
  fireEvent.click(screen.getByText('expand toggle'));
  expect(screen.getByText('site header').closest('[hidden]')).toBeNull();
  expect(screen.getByText('site menu').getAttribute('data-collapsed')).toBe('true');
  expect(screen.getByText('site menu').closest('[hidden]')).toBeNull();
  expect(screen.getByRole('textbox', { name: 'viewer draft' })).toBe(draft);
  expect(draft).toHaveProperty('value', '보존');
});

// While the server recounts token totals under the new rule, stored aggregates
// and live counts can disagree; every dashboard page says so, and stops saying
// so once the recount is done.
describe('DashboardLayout token rule note', () => {
  it('shows the note under the header while the token rule is pending', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue(PENDING);
    const { view } = renderLayout();

    const note = (await screen.findByText(TOKEN_RULE_NOTE)).closest('[role="status"]');
    expect(note?.previousSibling?.textContent).toBe('site header');
    expect(note?.nextSibling).toBe(view.container.querySelector('main'));
  });

  it('shows no note when the token rule is not pending', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue(SETTLED);
    const { client } = renderLayout();

    await waitFor(() => expect(client.getQueryData(KEY)).toEqual(SETTLED));
    expect(screen.queryByText(TOKEN_RULE_NOTE)).toBeNull();
    expect(screen.getByText('page')).not.toBeNull();
  });

  // A status that cannot be read is not known, not finished: with nothing known
  // it shows nothing, and after a known pending it keeps the note and refreshes
  // nothing.
  it('keeps the last known state when the status cannot be read', async () => {
    vi.mocked(fetchMaintenanceStatus).mockRejectedValueOnce(unreadable());
    const { client, invalidate } = renderLayout();
    await waitFor(() => expect(client.getQueryState(KEY)?.status).toBe('error'));
    expect(screen.queryByText(TOKEN_RULE_NOTE)).toBeNull();

    vi.mocked(fetchMaintenanceStatus).mockResolvedValueOnce(PENDING);
    await act(() => client.refetchQueries({ queryKey: KEY }));
    expect(await screen.findByText(TOKEN_RULE_NOTE)).not.toBeNull();

    vi.mocked(fetchMaintenanceStatus).mockRejectedValueOnce(unreadable());
    await act(() => client.refetchQueries({ queryKey: KEY }));
    await waitFor(() => expect(client.getQueryState(KEY)?.status).toBe('error'));
    expect(screen.queryByText(TOKEN_RULE_NOTE)).not.toBeNull();
    expect(invalidate).not.toHaveBeenCalled();
  });

  // The route needs a signed-in user; the layout asks only once there is one.
  it('does not ask before anyone is signed in', async () => {
    auth.user = null;
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue(PENDING);
    renderLayout();
    await act(() => Promise.resolve());
    expect(fetchMaintenanceStatus).not.toHaveBeenCalled();
  });

  // The charts read the aggregates the recount rewrites, and nothing else tells
  // them it finished.
  it('refreshes every query once when the token rule settles', async () => {
    vi.mocked(fetchMaintenanceStatus).mockResolvedValueOnce(PENDING).mockResolvedValue(SETTLED);
    const { client, invalidate } = renderLayout();
    await screen.findByText(TOKEN_RULE_NOTE);
    expect(invalidate).not.toHaveBeenCalled();

    await act(() => client.refetchQueries({ queryKey: KEY }));
    await waitFor(() => expect(screen.queryByText(TOKEN_RULE_NOTE)).toBeNull());
    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledWith();
  });
});

// The status is read again every POLL_LIVE only while the recount is pending --
// a failed read in between does not stop it -- and never once it is not.
describe('DashboardLayout token rule polling', () => {
  // setImmediate stays real: React's act flushes through it.
  const useFakeClock = () => vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] });
  const elapse = (ms: number) => act(() => vi.advanceTimersByTimeAsync(ms));
  afterEach(() => {
    vi.useRealTimers();
  });

  it('polls while the token rule is pending and stops once it is not', async () => {
    useFakeClock();
    vi.mocked(fetchMaintenanceStatus)
      .mockResolvedValueOnce(PENDING)
      .mockRejectedValueOnce(unreadable())
      .mockResolvedValueOnce(PENDING)
      .mockResolvedValue(SETTLED);
    renderLayout();
    await elapse(0);
    expect(fetchMaintenanceStatus).toHaveBeenCalledTimes(1);

    for (const calls of [2, 3, 4]) {
      await elapse(POLL_LIVE);
      expect(fetchMaintenanceStatus).toHaveBeenCalledTimes(calls);
    }
    // React hears of the last answer on the next tick.
    await elapse(1);
    expect(screen.queryByText(TOKEN_RULE_NOTE)).toBeNull();

    await elapse(POLL_LIVE * 10);
    expect(fetchMaintenanceStatus).toHaveBeenCalledTimes(4);
  });

  it('does not poll when the token rule is not pending', async () => {
    useFakeClock();
    vi.mocked(fetchMaintenanceStatus).mockResolvedValue(SETTLED);
    renderLayout();
    await elapse(0);
    await elapse(POLL_LIVE * 10);
    expect(fetchMaintenanceStatus).toHaveBeenCalledTimes(1);
  });
});
