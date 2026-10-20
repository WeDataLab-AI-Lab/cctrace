'use client';

import { useEffect, useRef } from 'react';
import { useQueryClient } from '@tanstack/react-query';

/**
 * Refreshes every query once when a background rewrite of stored numbers
 * finishes: the usage rebuild an exclusion change queues (usage_rebuild_pending
 * on the exclusion lists) or the server's token recount (token_rule_pending on
 * /api/maintenance/status). The charts read what those rewrite, and nothing else
 * tells them it changed, so without this they keep the old numbers until a
 * reload. `pending` is that flag.
 */
const useRefreshWhenRebuilt = (pending: boolean | null | undefined) => {
  const queryClient = useQueryClient();
  // The previous value is what makes true -> false a transition rather than a
  // state; it is not rendered, so it is not state.
  const wasPending = useRef(false);

  /** On pending going from true to false, mark every query stale so the charts refetch. */
  useEffect(() => {
    // Not loaded yet, or not readable on the server, says nothing either way;
    // keep what was last known.
    if (pending === undefined || pending === null) return;
    if (wasPending.current && !pending) void queryClient.invalidateQueries();
    wasPending.current = pending;
  }, [pending, queryClient]);
};

export { useRefreshWhenRebuilt };
