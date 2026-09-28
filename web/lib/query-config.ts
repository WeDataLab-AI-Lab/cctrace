// Polling cadences for react-query.
//
// There is deliberately no global `refetchInterval` on the QueryClient. A global interval
// overrides the intent a query already declared: a lookup marked `staleTime: Infinity`
// ("this does not change") was still refetched every 60s, and every page paid for polling
// it never asked for. Each query that wants to refresh opts in with one of these.
//
// A query with no `refetchInterval` never polls; it refreshes on mount/focus and on an
// explicit `invalidateQueries`. Static lookups (user name map, account list, app version)
// and dialog-scoped previews are meant to stay in that group.

/** A run in progress when its stream cannot be read: AI report generation, a queued
 *  usage rebuild after an exclusion change. Only while the run is running -- never as
 *  a standing interval. */
const POLL_LIVE = 5_000;

/** Collected data that grows continuously: session lists, cost rollups, logs, rules. */
const POLL_NORMAL = 60_000;

/** Expensive aggregates and slow-moving inventories: storage report, client versions,
 *  tool/plugin usage rollups. */
const POLL_SLOW = 300_000;

export { POLL_LIVE, POLL_NORMAL, POLL_SLOW };
