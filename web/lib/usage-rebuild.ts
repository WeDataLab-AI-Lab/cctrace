import { POLL_LIVE } from './query-config';
import type { ExclusionList } from './types';

/** Shown while an exclusion change is still waiting for its usage rebuild. */
const USAGE_REBUILD_NOTE =
  'Usage charts are catching up with the latest exclusion change. Cost and usage totals may lag for a few minutes.';

/**
 * An exclusion change answers before the usage charts are rebuilt (the server
 * runs the rebuild in the background). The lists that make those changes poll
 * only while that rebuild is pending, so the note clears on its own and the
 * list stops polling once it is done.
 */
const usageRebuildPollInterval = (list: ExclusionList<unknown> | undefined): number | false =>
  list?.usage_rebuild_pending === true ? POLL_LIVE : false;

/**
 * The server leaves usage_rebuild_pending out when it could not read it. That is
 * "not known", not "finished" -- reading it as false would refetch the charts,
 * stop polling and hide the note while the rebuild still runs. So a list without
 * the flag carries the last state the cache knew, and with it the polling.
 */
const keepKnownRebuildPending = <T,>(next: ExclusionList<T>, previous: ExclusionList<T> | undefined): ExclusionList<T> => {
  if (typeof next.usage_rebuild_pending === 'boolean') return next;
  return { ...next, usage_rebuild_pending: previous?.usage_rebuild_pending };
};

export { USAGE_REBUILD_NOTE, usageRebuildPollInterval, keepKnownRebuildPending };
