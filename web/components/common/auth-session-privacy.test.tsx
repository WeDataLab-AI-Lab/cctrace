// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { rememberReading, readReading, clearReadingMemory } from '@/lib/conversation-reading';
import { AuthProvider, useAuth } from './auth-context';

vi.mock('@/lib/api', () => ({
  fetchSetupStatus: vi.fn().mockResolvedValue({ needs_setup: false }),
  fetchMe: vi.fn().mockResolvedValue({ id: 7, role: 'admin', email: 'qa@example.test' }),
  logout: vi.fn().mockResolvedValue(undefined), login: vi.fn(), refreshToken: vi.fn(), setAuthFailureHandler: vi.fn(),
}));
const Account = () => {
  const { user, logout } = useAuth();
  return <><span>{user ? `user:${user.id}` : 'signed out'}</span><button type="button" onClick={logout}>로그아웃</button></>;
};
afterEach(() => { cleanup(); clearReadingMemory(); });
it('removes inspection source bodies, conversation windows, and reading memory on logout', async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  for (const prefix of ['session-inspection', 'conversation-window', 'session-records']) client.setQueryData([prefix, 'private-A'], { text: 'private source body' });
  rememberReading('7:admin:A', { offset: 1000, visible: 10, scrollTop: 40, promptsOnly: false });
  render(<QueryClientProvider client={client}><AuthProvider><Account /></AuthProvider></QueryClientProvider>);
  await screen.findByText('user:7');
  fireEvent.click(screen.getByRole('button', { name: '로그아웃' }));
  await screen.findByText('signed out');
  await waitFor(() => expect(client.getQueryData(['session-inspection', 'private-A'])).toBeUndefined());
  expect(client.getQueryData(['conversation-window', 'private-A'])).toBeUndefined();
  expect(client.getQueryData(['session-records', 'private-A'])).toBeUndefined();
  expect(readReading('7:admin:A')).toBeUndefined();
  client.clear();
});
