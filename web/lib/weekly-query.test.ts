import { dehydrate, QueryClient, QueryObserver } from '@tanstack/react-query';
import {
  persistQueryClientRestore,
  persistQueryClientSave,
  type PersistedClient,
  type Persister,
} from '@tanstack/react-query-persist-client';
import { describe, expect, it, vi } from 'vitest';
import { POLL_SLOW } from './query-config';
import {
  reconcileWeeklyQueriesAfterAuth,
  shouldPersistWeeklyQuery,
  skipPersistedMutations,
  WEEKLY_PERSIST_MAX_AGE,
  weeklyQueryKey,
  WEEKLY_STALE_TIME,
} from './weekly-query';

describe('weekly query cache policy', () => {
  it('keeps weekly results fresh for the slow-query cadence', () => {
    expect(WEEKLY_STALE_TIME).toBe(POLL_SLOW);
    expect(WEEKLY_STALE_TIME).toBe(300_000);
  });

  it('does not persist mutations from unrelated workflows', () => {
    expect(skipPersistedMutations()).toBe(false);
  });

  it('persists successful weekly data without persisting unrelated queries', () => {
    const client = new QueryClient();
    const scopedKey = weeklyQueryKey({ since: 'since', until: 'until', timeZone: 'UTC', userId: 101 });
    client.setQueryData(scopedKey, { typed_turn_count: 4 });
    client.setQueryData(['weekly-insights', null, 'since', 'until', 'UTC'], { typed_turn_count: 5 });
    client.setQueryData(['weekly-insights', 'since', 'until', 'UTC'], { typed_turn_count: 6 });
    client.setQueryData(['accounts'], ['person@example.com']);

    const dehydrated = dehydrate(client, { shouldDehydrateQuery: shouldPersistWeeklyQuery });

    expect(dehydrated.queries.map((query) => query.queryKey)).toEqual([
      scopedKey,
    ]);
  });

  it('does not restore one authenticated user weekly data for another user', async () => {
    let persistedClient: PersistedClient | undefined;
    const persister: Persister = {
      persistClient: (client) => { persistedClient = client; },
      restoreClient: () => persistedClient,
      removeClient: () => { persistedClient = undefined; },
    };
    const userAKey = weeklyQueryKey({ since: 'since', until: 'until', timeZone: 'UTC', userId: 101 });
    const originalClient = new QueryClient();
    originalClient.setQueryData(userAKey, { typed_turn_count: 4 });
    await persistQueryClientSave({
      queryClient: originalClient,
      persister,
      dehydrateOptions: { shouldDehydrateQuery: shouldPersistWeeklyQuery },
    });
    const restoredClient = new QueryClient();
    const reconciledBeforeRestore = reconcileWeeklyQueriesAfterAuth(restoredClient, {
      authenticatedUserId: 202,
      authResolved: true,
      restoreComplete: false,
    });
    await persistQueryClientRestore({
      queryClient: restoredClient,
      persister,
      maxAge: WEEKLY_PERSIST_MAX_AGE,
    });
    const reconciledAfterRestore = reconcileWeeklyQueriesAfterAuth(restoredClient, {
      authenticatedUserId: 202,
      authResolved: true,
      restoreComplete: true,
    });
    await persistQueryClientSave({
      queryClient: restoredClient,
      persister,
      dehydrateOptions: { shouldDehydrateQuery: shouldPersistWeeklyQuery },
    });
    const userBKey = weeklyQueryKey({ since: 'since', until: 'until', timeZone: 'UTC', userId: 202 });

    expect(reconciledBeforeRestore).toBe(false);
    expect(reconciledAfterRestore).toBe(true);
    expect(userAKey).not.toEqual(userBKey);
    expect(restoredClient.getQueryData(userAKey)).toBeUndefined();
    expect(restoredClient.getQueryData(userBKey)).toBeUndefined();
    expect(persistedClient?.clientState.queries).toEqual([]);
  });

  it('restores fresh weekly data after a browser-style cache restart without refetching', async () => {
    let persistedClient: PersistedClient | undefined;
    const persister: Persister = {
      persistClient: (client) => { persistedClient = client; },
      restoreClient: () => persistedClient,
      removeClient: () => { persistedClient = undefined; },
    };
    const queryKey = weeklyQueryKey({ since: 'since', until: 'until', timeZone: 'UTC', userId: 101 });
    const originalClient = new QueryClient();
    originalClient.setQueryData(queryKey, { typed_turn_count: 4 });
    originalClient.setQueryData(['accounts'], ['person@example.com']);
    await persistQueryClientSave({
      queryClient: originalClient,
      persister,
      dehydrateOptions: { shouldDehydrateQuery: shouldPersistWeeklyQuery },
    });
    const restoredClient = new QueryClient();

    await persistQueryClientRestore({
      queryClient: restoredClient,
      persister,
      maxAge: WEEKLY_PERSIST_MAX_AGE,
    });
    reconcileWeeklyQueriesAfterAuth(restoredClient, {
      authenticatedUserId: 101,
      authResolved: true,
      restoreComplete: true,
    });
    const queryFn = vi.fn().mockResolvedValue({ typed_turn_count: 5 });
    const observer = new QueryObserver(restoredClient, {
      queryKey,
      queryFn,
      staleTime: WEEKLY_STALE_TIME,
    });
    const unsubscribe = observer.subscribe(() => {});

    expect(restoredClient.getQueryData(queryKey)).toEqual({ typed_turn_count: 4 });
    expect(restoredClient.getQueryData(['accounts'])).toBeUndefined();
    expect(queryFn).not.toHaveBeenCalled();

    unsubscribe();
  });
});
