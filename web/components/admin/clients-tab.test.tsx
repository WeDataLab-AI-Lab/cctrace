import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import { ClientsTab } from './clients-tab';

const queryMock = vi.hoisted(() => ({
  versions: [
    {
      profile_email: 'current@example.com',
      name: 'Current Client',
      user_id: 'user-current',
      client_version: 'v0.7.31',
      client_os: 'darwin',
      client_arch: 'arm64',
      last_seen_at: '2026-08-31T12:00:00Z',
    },
  ],
}));

vi.mock('@tanstack/react-query', () => ({
  useQuery: ({ queryKey }: { readonly queryKey: readonly string[] }) =>
    queryKey[0] === 'client-versions'
      ? { data: queryMock.versions, isLoading: false }
      : {
          data: { version: 'v0.7.31', client_version_header_since: 'v0.6.1' },
          isLoading: false,
        },
}));

vi.mock('@/components/common/auth-context', () => ({
  useAuth: () => ({ isAdmin: true }),
}));

const renderClients = (): string => renderToStaticMarkup(createElement(ClientsTab));

describe('ClientsTab client platform', () => {
  it('shows the reported OS and architecture beside the client version', () => {
    const markup = renderClients();

    expect(markup).toContain('darwin');
    expect(markup).toContain('arm64');
  });
});
