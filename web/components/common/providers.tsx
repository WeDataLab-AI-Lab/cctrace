'use client';

import { QueryClient } from '@tanstack/react-query';
import { PersistQueryClientProvider } from '@tanstack/react-query-persist-client';
import { createAsyncStoragePersister } from '@tanstack/query-async-storage-persister';
import { useState } from 'react';
import { AuthProvider } from './auth-context';
import { AccountProvider } from './account-context';
import { AgentProvider } from './agent-context';
import { PendingActionProvider } from './pending-action-context';
import { AppearanceProvider } from './appearance-context';
import {
  shouldPersistWeeklyQuery,
  skipPersistedMutations,
  WEEKLY_PERSIST_MAX_AGE,
  WEEKLY_QUERY_CACHE_BUSTER,
  WEEKLY_QUERY_STORAGE_KEY,
} from '@/lib/weekly-query';

const Providers = ({ children }: { children: React.ReactNode }) => {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            // No global refetchInterval: polling is a per-query decision, declared with the
            // POLL_* constants in @/lib/query-config. A global interval polled even the
            // queries that had declared `staleTime: Infinity`.
            staleTime: 60 * 1000,
          },
        },
      })
  );
  const [queryPersister] = useState(() =>
    createAsyncStoragePersister({
      storage: typeof window === 'undefined' ? undefined : window.localStorage,
      key: WEEKLY_QUERY_STORAGE_KEY,
    }),
  );

  return (
    <PersistQueryClientProvider
      client={queryClient}
      persistOptions={{
        persister: queryPersister,
        maxAge: WEEKLY_PERSIST_MAX_AGE,
        buster: WEEKLY_QUERY_CACHE_BUSTER,
        dehydrateOptions: {
          shouldDehydrateQuery: shouldPersistWeeklyQuery,
          shouldDehydrateMutation: skipPersistedMutations,
        },
      }}
    >
      <AuthProvider>
        <AccountProvider>
          <AgentProvider>
            <AppearanceProvider>
              <PendingActionProvider>{children}</PendingActionProvider>
            </AppearanceProvider>
          </AgentProvider>
        </AccountProvider>
      </AuthProvider>
    </PersistQueryClientProvider>
  );
};

export { Providers };
