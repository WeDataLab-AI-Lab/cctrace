'use client';

import { useEffect } from 'react';
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query';
import { fetchTraceNodes, fetchTraceNodeDetail } from './session-trace-nodes';
import { TraceRequestError } from './session-trace-api';
import type { TraceNodeQueryIdentity, TraceNodesView, TraceNodesRequest, TraceNodeDetailRequest, TraceNodeDetail, TraceInputItem } from './session-trace-node-types';
import { usePageVisible } from './use-session-trace';

const traceNodesKey = (identity: TraceNodeQueryIdentity) => ['trace-nodes', identity.userId, identity.access,
  identity.scope?.profileEmail ?? '', identity.scope?.loginEmail ?? '', identity.scope?.userID ?? '', identity.sessionId] as const;
const traceNodesViewKey = (identity: TraceNodeQueryIdentity, view: TraceNodesView = 'execution') =>
  [...traceNodesKey(identity), 'children', view] as const;
const traceNodesPageKey = (identity: TraceNodeQueryIdentity, view: TraceNodesView = 'execution', parentId?: string, options?: TraceNodesRequest) =>
  [...traceNodesViewKey(identity, view), parentId ?? '', ...(options ? [options.requestNo ?? '', options.groupId ?? '', options.from ?? '', options.to ?? '', options.query ?? '', options.cursor ?? '', options.revision ?? '', options.epoch ?? ''] : [])] as const;
const fenceTraceNodeSession = (client: ReturnType<typeof useQueryClient>, identity: TraceNodeQueryIdentity, currentKey: readonly unknown[], error: unknown) => {
  if (!(error instanceof TraceRequestError) || ![401, 403, 404, 409].includes(error.status)) return;
  const identityPrefix = traceNodesKey(identity);
  const sessionQueries = client.getQueryCache().findAll({ predicate: query =>
    query.queryKey[0] === identityPrefix[0] && query.queryKey[1] === identity.userId && query.queryKey[6] === identity.sessionId,
  });
  for (const query of sessionQueries) {
    if (JSON.stringify(query.queryKey) !== JSON.stringify(currentKey)) void client.cancelQueries({ queryKey: query.queryKey, exact: true });
    query.setState({ data: undefined, error, status: 'error' });
  }
};
interface TraceNodesOptions extends TraceNodesRequest { identity: TraceNodeQueryIdentity; enabled?: boolean }
const useTraceNodes = ({ identity, enabled = true, parentId, view = 'execution', ...options }: TraceNodesOptions) => {
  const visible = usePageVisible();
  const active = enabled && visible;
  const client = useQueryClient();
  const key = traceNodesPageKey(identity, view, parentId, Object.keys(options).length ? options : undefined);
  const keyString = JSON.stringify(key);
  // Re-entry rechecks authorization; automatic focus/reconnect refreshes stay
  // disabled so they cannot reorder a tree while the user is reading it.
  const query = useInfiniteQuery({
    queryKey: key,
    queryFn: async ({ signal, pageParam }) => {
      try {
        return await fetchTraceNodes(identity.sessionId, { ...options, view, parentId, cursor: pageParam }, identity.scope, signal);
      } catch (error) {
        if (!signal.aborted) fenceTraceNodeSession(client, identity, key, error);
        throw error;
      }
    },
    initialPageParam: options.cursor as string | undefined,
    getNextPageParam: page => page.next_cursor ?? undefined,
    enabled: active, retry: false, staleTime: 0, refetchOnMount: 'always', refetchOnWindowFocus: false, refetchOnReconnect: false,
  });
  const refresh = () => { void client.resetQueries({ queryKey: key, exact: true }); };
  /** Hidden tree branches cancel their own scoped reads without discarding saved pages. */
  useEffect(() => {
    if (!active) void client.cancelQueries({ queryKey: JSON.parse(keyString), exact: true });
    return () => { void client.cancelQueries({ queryKey: JSON.parse(keyString), exact: true }); };
  }, [active, keyString, client]);
  const authorizedData = active && (!query.isRefetching || query.isFetchingNextPage) ? query.data : undefined;
  return { ...query, data: authorizedData, nodes: authorizedData?.pages.flatMap(page => page.nodes) ?? [], coverage: authorizedData?.pages[0]?.coverage, meta: authorizedData?.pages[0], refresh };
};
const uniqueTraceEvidence = <T,>(items: T[]): T[] => [...new Map(items.map(item => [JSON.stringify(item), item])).values()];
// Pages belong to the same selected request and source revision. Fragments are
// combined only when their recorded byte offsets are contiguous.
const mergeTraceDetails = (pages?: TraceNodeDetail[]): TraceNodeDetail | undefined => {
  const first = pages?.[0];
  if (!first || !pages) return;
  if (!first.request?.input_page || pages.length === 1) return first;
  const items: TraceInputItem[] = [];
  for (const page of pages) for (const item of page.request?.input_page?.items ?? []) {
    const previous = items.at(-1);
    if (previous && previous.request_path === item.request_path && typeof previous.value === 'string' && typeof item.value === 'string' && (item.offset_bytes ?? 0) > 0
      && new TextEncoder().encode(previous.value).length === item.offset_bytes) {
      previous.value += item.value;
      previous.truncated = item.truncated;
      if (!item.truncated) { try { previous.value = JSON.parse(previous.value); } catch { previous.state = 'partial'; } }
    } else items.push({ ...item });
  }
  const last = pages.at(-1)!;
  const requests = pages.flatMap(page => page.request ? [page.request] : []);
  const exceptionalState = ['error', 'withheld', 'redacted', 'truncated', 'partial', 'unresolved', 'not_collected'].find(state => requests.some(request => request.state === state));
  return { ...first, request: { ...first.request, state: exceptionalState ?? first.request.state,
    missing_refs: [...new Set(requests.flatMap(request => request.missing_refs))], source_refs: [...new Set(requests.flatMap(request => request.source_refs))],
    provenance: uniqueTraceEvidence(requests.flatMap(request => request.provenance ?? [])),
    input_page: { ...first.request.input_page, items, next_cursor: last.request?.input_page?.next_cursor ?? null } },
    source_locations: uniqueTraceEvidence(pages.flatMap(page => page.source_locations ?? [])),
  };
};
interface TraceDetailOptions { identity: TraceNodeQueryIdentity; enabled?: boolean; nodeId?: string; options?: TraceNodeDetailRequest }
const useTraceNodeDetail = ({ identity, enabled = true, nodeId, options }: TraceDetailOptions) => {
  const visible = usePageVisible();
  const client = useQueryClient();
  const key = [...traceNodesKey(identity), 'detail', nodeId ?? '', ...(options ? [options.requestParts ?? '', options.inputCursor ?? '', options.inputLimit ?? 20, options.compareTo ?? '', options.sourceRef ?? ''] : [])];
  const keyString = JSON.stringify(key);
  const active = enabled && visible && !!nodeId;
  const query = useInfiniteQuery({
    queryKey: key,
    queryFn: async ({ signal, pageParam }) => {
      try {
        return await fetchTraceNodeDetail(identity.sessionId, nodeId ?? '', identity.scope, signal, { ...options, inputCursor: pageParam });
      } catch (error) {
        if (!signal.aborted) fenceTraceNodeSession(client, identity, key, error);
        throw error;
      }
    },
    initialPageParam: options?.inputCursor as string | undefined,
    getNextPageParam: (page: TraceNodeDetail) => page.request?.input_page?.next_cursor ?? undefined,
    enabled: active, retry: false, staleTime: 0, refetchOnMount: 'always', refetchOnWindowFocus: false, refetchOnReconnect: false,
  });
  /** Only the selected node reads bodies; navigation aborts its request. */
  useEffect(() => {
    if (!active) void client.cancelQueries({ queryKey: JSON.parse(keyString), exact: true });
    return () => { void client.cancelQueries({ queryKey: JSON.parse(keyString), exact: true }); };
  }, [active, keyString, client]);
  const authorizedPages = active && (!query.isRefetching || query.isFetchingNextPage) ? query.data?.pages : undefined;
  return { ...query, data: mergeTraceDetails(authorizedPages) };
};

export { useTraceNodes, useTraceNodeDetail, traceNodesKey, traceNodesViewKey, traceNodesPageKey };
export type { TraceNodesOptions, TraceDetailOptions };
