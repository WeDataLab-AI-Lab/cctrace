import type { QueryClient } from '@tanstack/react-query';
import { isWeeklyUsageKey } from './weekly-usage';

const DASHBOARD_ANALYTICS_ROOTS = new Set([
  'cost-by-user',
  'cost-by-model',
  'timeseries-by-model',
  'timeseries-by-user',
  'latest-activity',
  // The burn chart polls on its own five-minute schedule, which is slow enough
  // that Refresh not reaching it was a visible hole: the reader pressed refresh,
  // every card around it updated, and this one kept last tick's readings.
  'quota-samples',
]);

const dashboardQueriesEnabled = (accountHydrated: boolean, agentHydrated: boolean): boolean =>
  accountHydrated && agentHydrated;

const overviewSummaryQueriesEnabled = (
  scopesHydrated: boolean,
  trendSince: string,
): boolean => scopesHydrated && trendSince !== '';

// The weekly report line is a user key, not a person: its rows carry no user_id to
// filter by (the Agent pill selects it instead), and it is nobody to count.
//
// Rows with no user_id are left out on purpose too, though their cost still reaches
// the totals and the Cost by User chart as an "Unknown user" bar. They are not one
// person -- any number of unattributed clients land there -- so counting them would
// add a phantom user. And they cannot be a filter option: the server reads an empty
// user_id filter as "no filter", so picking it would show everyone's usage under
// the unknown user's name.
const peopleFromCostRows = (rows: readonly { user_id?: string }[]): Set<string> => {
  const users = new Set<string>();
  for (const row of rows) {
    if (row.user_id && !isWeeklyUsageKey(row.user_id)) users.add(row.user_id);
  }
  return users;
};

const userFilterOptionsFromCostRows = (rows: readonly { user_id?: string }[]): string[] =>
  [...peopleFromCostRows(rows)];

const countActiveUsers = (rows: readonly { user_id?: string }[]): number =>
  peopleFromCostRows(rows).size;

const refreshActiveDashboardQueries = (queryClient: QueryClient): Promise<void> =>
  queryClient.invalidateQueries({
    predicate: (query) =>
      query.isActive() && DASHBOARD_ANALYTICS_ROOTS.has(String(query.queryKey[0] ?? '')),
  });

export {
  countActiveUsers,
  dashboardQueriesEnabled,
  overviewSummaryQueriesEnabled,
  refreshActiveDashboardQueries,
  userFilterOptionsFromCostRows,
};
