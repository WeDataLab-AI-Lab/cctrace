import { QueryClient, QueryObserver } from '@tanstack/react-query';
import { describe, expect, it, vi } from 'vitest';
import {
  dashboardQueriesEnabled,
  overviewSummaryQueriesEnabled,
  refreshActiveDashboardQueries,
  countActiveUsers,
  userFilterOptionsFromCostRows,
} from './dashboard-queries';

describe('dashboardQueriesEnabled', () => {
  it('waits for both persisted account and agent scopes', () => {
    expect(dashboardQueriesEnabled(false, false)).toBe(false);
    expect(dashboardQueriesEnabled(true, false)).toBe(false);
    expect(dashboardQueriesEnabled(false, true)).toBe(false);
    expect(dashboardQueriesEnabled(true, true)).toBe(true);
  });
});

describe('overviewSummaryQueriesEnabled', () => {
  it('waits for TrendChart to report the initial summary range', () => {
    expect(overviewSummaryQueriesEnabled(true, '')).toBe(false);
    expect(overviewSummaryQueriesEnabled(false, '2026-08-21T00:00:00.000Z')).toBe(false);
    expect(overviewSummaryQueriesEnabled(true, '2026-08-21T00:00:00.000Z')).toBe(true);
  });
});

describe('userFilterOptionsFromCostRows', () => {
  it('derives unique user filter values from analytics rows without a users request', () => {
    expect(userFilterOptionsFromCostRows([
      { user_id: 'user-b' },
      { user_id: '' },
      { user_id: 'user-a' },
      { user_id: 'user-b' },
      { user_id: undefined },
    ])).toEqual(['user-b', 'user-a']);
  });

  it('leaves out the weekly report line, which has no person to filter by', () => {
    expect(userFilterOptionsFromCostRows([
      { user_id: 'weekly' },
      { user_id: 'user-a' },
    ])).toEqual(['user-a']);
  });
});

describe('countActiveUsers', () => {
  it('counts people, not the weekly report line or unattributed rows', () => {
    expect(countActiveUsers([
      { user_id: 'user-a' },
      { user_id: 'weekly' },
      { user_id: '' },
      { user_id: 'user-a' },
      { user_id: 'user-b' },
    ])).toBe(2);
  });
});

describe('refreshActiveDashboardQueries', () => {
  it('refreshes only active dashboard analytics and leaves unrelated and inactive scopes cached', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const activeFetch = vi.fn().mockResolvedValue('fresh');
    const unrelatedFetch = vi.fn().mockResolvedValue('unrelated-fresh');
    const activeObserver = new QueryObserver(client, {
      queryKey: ['cost-by-user', 'active-scope'],
      queryFn: activeFetch,
      enabled: true,
    });
    const unrelatedObserver = new QueryObserver(client, {
      queryKey: ['session-overview', 'active-scope'],
      queryFn: unrelatedFetch,
      enabled: true,
    });
    const unsubscribeActive = activeObserver.subscribe(() => {});
    const unsubscribeUnrelated = unrelatedObserver.subscribe(() => {});
    await Promise.all([
      client.ensureQueryData({ queryKey: ['cost-by-user', 'active-scope'], queryFn: activeFetch }),
      client.ensureQueryData({ queryKey: ['session-overview', 'active-scope'], queryFn: unrelatedFetch }),
      client.ensureQueryData({ queryKey: ['cost-by-user', 'saved-other-scope'], queryFn: async () => 'cached' }),
    ]);
    activeFetch.mockClear();
    unrelatedFetch.mockClear();

    await refreshActiveDashboardQueries(client);

    expect(activeFetch).toHaveBeenCalledTimes(1);
    expect(unrelatedFetch).not.toHaveBeenCalled();
    expect(client.getQueryState(['cost-by-user', 'saved-other-scope'])?.isInvalidated).toBe(false);
    expect(client.getQueryData(['session-overview', 'active-scope'])).toBe('unrelated-fresh');

    unsubscribeActive();
    unsubscribeUnrelated();
    client.clear();
  });
});
