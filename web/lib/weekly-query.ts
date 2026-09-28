import type { Query, QueryClient } from '@tanstack/react-query';
import { POLL_SLOW } from './query-config';

const WEEKLY_QUERY_ROOT = 'weekly-insights';
const WEEKLY_QUERY_STORAGE_KEY = 'cctrace.weekly-query-cache';
const WEEKLY_QUERY_CACHE_BUSTER = 'weekly-v2-user-scoped';
const WEEKLY_STALE_TIME = POLL_SLOW;
const WEEKLY_PERSIST_MAX_AGE = 60 * 60 * 1000;

interface WeeklyQueryScope {
  readonly since: string;
  readonly until: string;
  readonly timeZone: string;
  readonly userId: number | null;
}

interface WeeklyAuthReconciliation {
  readonly authenticatedUserId: number | null;
  readonly authResolved: boolean;
  readonly restoreComplete: boolean;
}

const weeklyQueryKey = ({ since, until, timeZone, userId }: WeeklyQueryScope) =>
  [WEEKLY_QUERY_ROOT, userId, since, until, timeZone] as const;

const shouldPersistWeeklyQuery = (query: Query): boolean =>
  query.queryKey[0] === WEEKLY_QUERY_ROOT &&
  typeof query.queryKey[1] === 'number' &&
  query.state.status === 'success';

const removeOtherUsersWeeklyQueries = (
  queryClient: QueryClient,
  authenticatedUserId: number | null,
): boolean => {
  const hasForeignWeeklyData = queryClient.getQueryCache().findAll({
    predicate: (query) =>
      query.queryKey[0] === WEEKLY_QUERY_ROOT &&
      (authenticatedUserId === null || query.queryKey[1] !== authenticatedUserId),
  }).length > 0;
  if (!hasForeignWeeklyData) return false;

  queryClient.removeQueries({
    predicate: (query) =>
      query.queryKey[0] === WEEKLY_QUERY_ROOT &&
      (authenticatedUserId === null || query.queryKey[1] !== authenticatedUserId),
  });
  return true;
};

const reconcileWeeklyQueriesAfterAuth = (
  queryClient: QueryClient,
  state: WeeklyAuthReconciliation,
): boolean => {
  if (!state.authResolved || !state.restoreComplete) return false;
  return removeOtherUsersWeeklyQueries(queryClient, state.authenticatedUserId);
};

const skipPersistedMutations = (): boolean => false;

export {
  reconcileWeeklyQueriesAfterAuth,
  shouldPersistWeeklyQuery,
  skipPersistedMutations,
  WEEKLY_PERSIST_MAX_AGE,
  weeklyQueryKey,
  WEEKLY_QUERY_CACHE_BUSTER,
  WEEKLY_QUERY_STORAGE_KEY,
  WEEKLY_STALE_TIME,
};
