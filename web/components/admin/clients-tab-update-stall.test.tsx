import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import { ClientsTab } from './clients-tab';

// An install that cannot finish its self-update reports the failure on every
// sync (#750). The number alone does not say what to do about it, so the row
// carries the target version, how long it has been failing, and the client's own
// error text.
const queryMock = vi.hoisted(() => ({
  versions: [
    {
      profile_email: 'stalled@example.com',
      name: 'Stalled Client',
      user_id: 'user-stalled',
      client_version: 'v0.7.14',
      client_os: 'darwin',
      client_arch: 'arm64',
      last_seen_at: '2026-09-21T01:00:00Z',
      update_reported: true,
      update_target_version: 'v0.7.56',
      update_fail_count: 9,
      update_first_failed_at: '2026-09-18T01:00:00Z',
      update_fail_reason: 'rename /usr/local/bin/cctrace: permission denied',
    },
    {
      profile_email: 'silent@example.com',
      name: 'Silent Client',
      user_id: 'user-silent',
      client_version: 'v0.6.9',
      client_os: '',
      client_arch: '',
      last_seen_at: '2026-09-21T02:00:00Z',
      update_reported: false,
      update_fail_count: 0,
    },
    {
      profile_email: 'healthy@example.com',
      name: 'Healthy Client',
      user_id: 'user-healthy',
      client_version: 'v0.7.56',
      client_os: 'darwin',
      client_arch: 'arm64',
      last_seen_at: '2026-09-21T03:00:00Z',
      update_reported: true,
      update_fail_count: 0,
    },
  ],
}));

vi.mock('@tanstack/react-query', () => ({
  useQuery: ({ queryKey }: { readonly queryKey: readonly string[] }) =>
    queryKey[0] === 'client-versions'
      ? { data: queryMock.versions, isLoading: false }
      : {
          data: { version: 'v0.7.56', client_version_header_since: 'v0.6.1' },
          isLoading: false,
        },
}));

vi.mock('@/components/common/auth-context', () => ({
  useAuth: () => ({ isAdmin: true }),
}));

const renderClients = (): string => renderToStaticMarkup(createElement(ClientsTab));

describe('ClientsTab self-update reporting', () => {
  it('names the version a stalled client cannot reach, and why', () => {
    const markup = renderClients();

    expect(markup).toContain('v0.7.56');
    expect(markup).toContain('9');
    expect(markup).toContain('permission denied');
  });

  it('says a client too old to report is unknown, not healthy', () => {
    const markup = renderClients();

    expect(markup).toContain('갱신 상태 미보고');
  });

  it('says nothing about a client that reports no failure', () => {
    const markup = renderClients();

    // The healthy row must not carry either notice; matching on the reason text
    // and on the unknown label covers both.
    expect(markup.match(/갱신 상태 미보고/g)?.length ?? 0).toBe(1);
    expect(markup.match(/갱신 실패/g)?.length ?? 0).toBe(1);
  });
});
