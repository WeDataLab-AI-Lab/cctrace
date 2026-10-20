// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fetchMyQuotaUsage, fetchMyQuotaWindowComparison } from '@/lib/api';
import { clearConnection, setActiveConnection } from '@/lib/local-session-connection';
import { createLocalSessionApi } from '@/lib/local-session-api';
import { PersonalQuotaUsage } from './personal-quota-usage';

vi.mock('@/lib/api', () => ({
  fetchMyQuotaUsage: vi.fn(),
  fetchMyQuotaWindowComparison: vi.fn(),
}));

vi.mock('@/lib/local-session-api', () => ({
  createLocalSessionApi: vi.fn(),
  LocalSessionApiError: class LocalSessionApiError extends Error { code = 'unreachable'; },
}));

const usage = {
  from: '2026-10-02T00:00:00Z', to: '2026-10-09T00:00:00Z', accounts: [
    {
      billing_provider: 'anthropic' as const, account_id: 'acct-approved-fixture', label: 'Claude · …fixture', plan: 'max', binding_status: 'approved' as const,
      windows: {
        five_hour: { used_pct: 0, resets_at: '2026-10-09T05:00:00Z', sampled_at: '2026-10-09T00:00:00Z', attribution: 'observed' as const, state: 'measured' as const, source: 'client_reported' as const },
        seven_day: { used_pct: null, resets_at: null, sampled_at: null, attribution: 'unknown' as const, state: 'unmeasured' as const, source: 'unknown' as const, reason: 'reset_not_yet_observed' },
      },
    },
  ],
};

const comparison = (windowMinutes: 300 | 10080) => ({
  billing_provider: 'anthropic' as const, account_id: 'acct-approved-fixture', window_minutes: windowMinutes,
  current: null, history: [], quantiles: [], history_count: 0, truncated: false, source: 'client_reported' as const, reason: 'no_observations',
});

const renderPanel = () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(createElement(QueryClientProvider, { client }, createElement(PersonalQuotaUsage)));
};

beforeEach(() => {
  vi.mocked(fetchMyQuotaUsage).mockResolvedValue(usage);
  vi.mocked(fetchMyQuotaWindowComparison).mockImplementation(async (_provider, _account, minutes) => comparison(minutes));
  vi.mocked(createLocalSessionApi).mockReturnValue({
    profileCatalog: vi.fn().mockResolvedValue({ revision: 7, profiles: [
      { agent: 'claude', profile_name: 'work', billing_provider: 'anthropic', account_id: 'acct-approved-fixture', selected: false },
    ] }),
    selectProfile: vi.fn().mockResolvedValue({ version: 1, revision: 8, claude: 'work' }),
  } as unknown as ReturnType<typeof createLocalSessionApi>);
});

afterEach(() => {
  cleanup();
  clearConnection();
  vi.clearAllMocks();
});

describe('PersonalQuotaUsage', () => {
  it('keeps measured zero, unmeasured unknown, and omits the local selection radio without a bridge', async () => {
    renderPanel();
    expect(await screen.findByText('Claude · …fixture')).toBeTruthy();
    expect(screen.getByText('0%')).toBeTruthy();
    expect(screen.getByRole('img', { name: '사용률 알 수 없음' })).toBeTruthy();
    expect(screen.queryByRole('radio')).toBeNull();
    expect(screen.getByText(/로컬 브리지가 연결되지 않아/)).toBeTruthy();
  });

  it('offers profile selection only for an exact approved local account match', async () => {
    setActiveConnection({ origin: 'http://127.0.0.1:5080', token: 'fake-loopback-token' });
    const profileCatalog = vi.fn().mockResolvedValue({ revision: 7, profiles: [
      { agent: 'claude', profile_name: 'work', billing_provider: 'anthropic', account_id: 'acct-approved-fixture', selected: false },
    ] });
    const selectProfile = vi.fn().mockResolvedValue({ version: 1, revision: 8, claude: 'work' });
    vi.mocked(createLocalSessionApi).mockReturnValue({ profileCatalog, selectProfile } as unknown as ReturnType<typeof createLocalSessionApi>);
    renderPanel();
    const radio = await screen.findByRole('radio', { name: 'Claude · …fixture' });
    await userEvent.click(radio);
    await waitFor(() => expect(selectProfile).toHaveBeenCalledWith(expect.objectContaining({ profile_name: 'work' }), 7));
    expect(fetchMyQuotaWindowComparison).toHaveBeenCalledWith('anthropic', 'acct-approved-fixture', 300);
    expect(await screen.findByText('다음 실행에 쓸 로컬 프로필을 저장했습니다.')).toBeTruthy();
  });
});
