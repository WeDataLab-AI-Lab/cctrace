'use client';

import { useEffect, useState } from 'react';
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query';
import { POLL_LIVE } from './query-config';
import type { TraceIdentity } from './session-trace-types';
import { usePageVisible } from './use-session-trace';
import { fetchTurnContext, TurnContextRequestError } from './turn-context-api';

const turnContextKey = (identity: TraceIdentity, turnKey: string, revision?: string) => [
  'session-turn-context', identity.userId, identity.access, identity.scope?.profileEmail ?? '',
  identity.scope?.loginEmail ?? '', identity.scope?.userID ?? '', identity.sessionId, turnKey, revision ?? 'latest',
] as const;
interface UseTurnContextOptions { identity: TraceIdentity; turnKey?: string; revision?: string; enabled?: boolean }
interface ContextCursor { cursor: string; revision: string }
const useTurnContext = ({ identity, turnKey = '', revision, enabled = true }: UseTurnContextOptions) => {
  const visible = usePageVisible(), client = useQueryClient();
  const active = enabled && visible && !!turnKey;
  const selection = JSON.stringify(turnContextKey(identity, turnKey).slice(0, -1));
  const [initialRead, setInitialRead] = useState({ selection, discovering: !revision });
  if (initialRead.selection !== selection) setInitialRead({ selection, discovering: !revision });
  const awaitInitialRead = initialRead.selection === selection ? initialRead.discovering : !revision;
  const boundary = JSON.stringify(turnContextKey(identity, turnKey, revision));
  const [discoveryBoundary, setDiscoveryBoundary] = useState<string | null>(null);
  const discovering = discoveryBoundary === boundary;
  const key = turnContextKey(identity, turnKey, discovering || awaitInitialRead ? undefined : revision);
  const keyString = JSON.stringify(key);
  const query = useInfiniteQuery({
    queryKey: key,
    queryFn: async ({ signal, pageParam }) => {
      const requestRevision = pageParam?.revision ?? (discovering || awaitInitialRead ? undefined : revision);
      try {
        const page = await fetchTurnContext(identity.sessionId, turnKey, { revision: requestRevision, cursor: pageParam?.cursor, limit: 20 }, identity.scope, signal);
        if (page.session_id !== identity.sessionId || page.turn_key !== turnKey || page.state === 'ready' && (!page.revision || requestRevision && page.revision !== requestRevision)) throw new TurnContextRequestError(409);
        return page;
      } catch (error) {
        if (!signal.aborted && error instanceof TurnContextRequestError && [401, 403, 404, 409].includes(error.status)) {
          // Missing turns fence only their own aliases; session denial/revision fences every cached turn.
          const missingTurn = error.status === 404 && error.code === 'turn_not_found';
          for (const cached of client.getQueryCache().findAll({ predicate: cached => cached.queryKey[0] === key[0] && cached.queryKey[1] === identity.userId && cached.queryKey[6] === identity.sessionId && (!missingTurn || JSON.stringify(cached.queryKey.slice(0, 8)) === JSON.stringify(key.slice(0, 8))) })) {
            if (JSON.stringify(cached.queryKey) !== keyString) void client.cancelQueries({ queryKey: cached.queryKey, exact: true });
            cached.setState({ data: undefined, error, status: 'error' });
          }
        }
        throw error;
      }
    },
    initialPageParam: undefined as ContextCursor | undefined,
    getNextPageParam: page => page.state === 'ready' && page.next_cursor ? { cursor: page.next_cursor, revision: page.revision } : undefined,
    enabled: active, retry: false, staleTime: revision && !discovering ? Infinity : 0,
    refetchInterval: query => active && query.state.data?.pages[0]?.state === 'pending' ? POLL_LIVE : false,
  });
  const fenced = query.error instanceof TurnContextRequestError && query.error.status === 409;
  const inaccessible = query.error instanceof TurnContextRequestError && [401, 403, 404, 409].includes(query.error.status);
  const first = query.data?.pages[0];
  const records = !inaccessible && first?.state === 'ready' && (revision && !discovering || !query.isFetching || query.isFetchingNextPage)
    ? query.data?.pages.flatMap(page => page.records) ?? [] : [];
  const state = fenced ? undefined : first?.state;
  const refetch = query.refetch;

  /** Only the last inactive observer cancels a shared read; native query cleanup aborts unmounted selections. */
  useEffect(() => {
    const filters = { queryKey: JSON.parse(keyString), exact: true };
    const cancelInactive = () => {
      const shared = client.getQueryCache().find(filters);
      if (shared?.state.fetchStatus === 'fetching' && !shared.isActive()) void client.cancelQueries(filters);
    };
    if (!active) cancelInactive();
    return client.getQueryCache().subscribe(event => {
      if ((event.type === 'observerRemoved' || event.type === 'observerOptionsUpdated') && JSON.stringify(event.query.queryKey) === keyString) cancelInactive();
    });
  }, [active, keyString, client]);
  /** A direct link discovers the canonical revision and seeds the same cache used by known analysis turns. */
  useEffect(() => {
    if (active && awaitInitialRead && revision && query.isError) {
      setInitialRead({ selection, discovering: false });
      return;
    }
    if (first?.state !== 'ready' || !first.revision || query.isError || !query.data || !active) return;
    const revisionKey = [...JSON.parse(keyString).slice(0, -1), first.revision];
    if (JSON.stringify(revisionKey) !== keyString) client.setQueryData(revisionKey, query.data);
    if (awaitInitialRead && revision) setInitialRead({ selection, discovering: false });
  }, [first, query.data, query.isError, active, client, keyString, awaitInitialRead, revision, selection]);
  /** A revision fence discards inaccessible content and discovers current mapping at the shared polling cadence. */
  useEffect(() => {
    if (!active || !fenced) return;
    const timer = setTimeout(() => {
      client.removeQueries({ queryKey: JSON.parse(keyString), exact: true });
      if (discovering) void refetch();
      else setDiscoveryBoundary(boundary);
    }, POLL_LIVE);
    return () => clearTimeout(timer);
  }, [active, fenced, discovering, boundary, keyString, client, refetch]);

  return { ...query, records, state, error: fenced ? undefined : query.error,
    loading: active && (query.isPending || first?.state === 'pending'), updating: fenced };
};

export { useTurnContext, turnContextKey };
export type { UseTurnContextOptions };
