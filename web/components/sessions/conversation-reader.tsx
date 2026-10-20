'use client';

import { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { conversationDestination, destinationReadingKey, readReading, rememberReading, type ConversationDestination, type ReadingPosition, type RequestDirection } from '@/lib/conversation-reading';
import { fetchConversationWindow, type ConversationWindow } from '@/lib/conversation-window';
import type { SessionRecordScope, SessionRecordQueryIdentity } from '@/lib/session-record-scope';
import type { ConversationReturnState } from '@/lib/sessions-return-state';
import type { InspectionTarget } from '@/lib/session-inspection';
import { SessionDetail } from './session-detail';

interface ConversationReaderProps {
  sessionId: string;
  memoryKey: string;
  active: boolean;
  onNavigate: (sessionId: string, anchor?: string) => void;
  onInspect?: (target: InspectionTarget) => void;
  onOpenTraceTurn?: (turnKey: string) => void;
  onReadingAnchor?: (anchor: string) => void;
  scrollAnchor?: string;
  scrollSourceFile?: string;
  scrollFromTs?: string;
  analysisAnchorTs?: string;
  scope?: SessionRecordScope;
  queryIdentity?: SessionRecordQueryIdentity;
  hasEnriched?: boolean;
  restoredState?: ConversationReturnState;
  turnKey?: string;
  onInspectContext?: () => void;
  requestIndex?: import('@/lib/session-trace-index').SessionTraceIndex;
}

interface WindowReaderProps extends ConversationReaderProps {
  destination: ConversationDestination;
  explicitDestination: boolean;
  saved?: ReadingPosition;
}
interface RequestNavigation {
  window?: ConversationWindow;
  destination: ConversationDestination;
  pending: boolean;
  revision: number;
  error?: string;
  boundary?: boolean;
}

// Explicit URL destinations key this reader, so a new link supersedes an in-flight local jump.
const WindowReader = ({ sessionId, memoryKey, active, onNavigate, onInspect, onOpenTraceTurn, onReadingAnchor, scrollAnchor, scope, destination, explicitDestination, saved, onInspectContext, requestIndex, ...props }: WindowReaderProps) => {
  // A hidden remount comes from inspection history. Preserve that entry's origin,
  // while an active explicit link always opens its requested destination.
  const positionKey = destinationReadingKey(memoryKey, destination);
  const [returnPosition] = useState(() => !active ? readReading(positionKey) : undefined);
  const readDestination = returnPosition ? { ...conversationDestination(undefined, returnPosition), sourceFile: destination.sourceFile } : destination;
  const [navigation, setNavigation] = useState<RequestNavigation>();
  const request = useRef<AbortController | null>(null);
  const windowQuery = useQuery({
    queryKey: ['conversation-window', memoryKey, readDestination],
    queryFn: ({ signal }) => fetchConversationWindow({ sessionId, scope, ...readDestination, signal }),
    enabled: active, staleTime: Infinity, retry: false,
  });
  const currentWindow = navigation?.window ?? windowQuery.data;
  const retry = () => { void windowQuery.refetch(); };
  const save = (state: ConversationReturnState) => {
    if (!currentWindow) return;
    const position = { ...state, offset: currentWindow.offset };
    rememberReading(memoryKey, position);
    rememberReading(positionKey, position);
  };
  const navigateRequest = async (next: ConversationDestination) => {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setNavigation(previous => ({ window: previous?.window ?? windowQuery.data, destination: next, revision: previous?.revision ?? 0, pending: true }));
    try {
      const window = await fetchConversationWindow({ sessionId, scope, ...next, signal: controller.signal });
      if (controller.signal.aborted) return;
      setNavigation(previous => ({ window, destination: next, revision: (previous?.revision ?? 0) + 1, pending: false }));
    } catch (error) {
      if (controller.signal.aborted) return;
      const boundary = error instanceof Error && 'code' in error && error.code === 'no_adjacent_request';
      const message = boundary ? `${next.direction === 'previous-request' ? '이전' : '다음'} 요청을 확인할 수 없습니다.`
        : error instanceof Error ? error.message : '요청 위치를 불러오지 못했습니다.';
      setNavigation(previous => ({ window: previous?.window ?? windowQuery.data, destination: next, revision: previous?.revision ?? 0, pending: false, error: message, boundary }));
    } finally {
      if (request.current === controller) {
        request.current = null;
        setNavigation(previous => previous?.pending ? { ...previous, pending: false } : previous);
      }
    }
  };
  const handleLatest = () => { void navigateRequest({ start: 'latest-request' }); };
  const handleRequest = (direction: RequestDirection, anchor: string) => { void navigateRequest({ direction, anchor }); };
  const handleTurn = (turnKey: string) => { void navigateRequest({ turnKey }); };
  const handleOpenAt = (fromTs: string) => { void navigateRequest({ fromTs }); };
  const handleRetryNavigation = () => { if (navigation) void navigateRequest(navigation.destination); };

  /** Cancel bounded navigation reads when the reader is hidden, replaced or unmounted. */
  useEffect(() => () => { request.current?.abort(); }, [active]);

  if (!currentWindow) return windowQuery.isError
    ? <div role="alert" className="p-4 text-sm text-danger"><p>{windowQuery.error.message}</p><button type="button" onClick={retry} className="mt-2 rounded border border-border px-3 py-1.5 text-ink">다시 시도</button></div>
    : <p role="status" className="p-4 text-sm text-ink-3">대화를 불러오는 중…</p>;
  const moved = (navigation?.revision ?? 0) > 0;
  // A jump opens a window that starts at its target; land there and mark it. Only the
  // latest-request window opens at its bottom, like a coding agent's transcript.
  const landAtStart = moved && navigation?.destination.start !== 'latest-request';
  return <>
    {navigation?.error && <div role={navigation.boundary ? 'status' : 'alert'} className="shrink-0 border-b border-border-subtle px-4 py-2 text-xs text-ink-3">{navigation.error}{!navigation.boundary && <button type="button" onClick={handleRetryNavigation} className="ml-2 text-brand underline">다시 시도</button>}</div>}
    <SessionDetail {...props} key={`${memoryKey}:${currentWindow.offset}:${navigation?.revision ?? 0}`} sessionId={sessionId} mode="assembled" onNavigate={onNavigate}
      active={active} scope={scope} scrollAnchor={moved || returnPosition ? undefined : scrollAnchor} scrollFromTs={moved || returnPosition ? undefined : props.scrollFromTs} analysisAnchorTs={moved || returnPosition ? undefined : props.analysisAnchorTs}
      landAtStart={landAtStart}
      initialPage={{ ...currentWindow, revision: navigation?.revision ?? 0 }} onRememberState={save}
      onInspect={onInspect} onOpenTrace={onOpenTraceTurn} onReadingAnchor={onReadingAnchor} onLatestRequest={handleLatest} onRequestNavigate={handleRequest} requestNavigationPending={navigation?.pending}
      onOpenTurn={handleTurn}
      onOpenAt={handleOpenAt}
      onInspectContext={onInspectContext}
      requestIndex={requestIndex}
      restoredState={moved ? undefined : returnPosition ? { ...returnPosition, sessionId, mode: 'assembled' } : explicitDestination ? undefined : saved ? { ...saved, sessionId, mode: 'assembled' } : props.restoredState} abortWhenInactive />
  </>;
};

const ConversationReader = (props: ConversationReaderProps) => {
  const [saved] = useState(() => readReading(props.memoryKey));
  const fromTs = props.analysisAnchorTs ?? props.scrollFromTs;
  const explicitDestination = !!(props.scrollAnchor || props.turnKey || fromTs);
  const destination = props.scrollAnchor ? { anchor: props.scrollAnchor, sourceFile: props.scrollSourceFile } : props.turnKey ? { turnKey: props.turnKey } : fromTs ? { fromTs } : conversationDestination(undefined, saved);
  return <WindowReader key={JSON.stringify([props.memoryKey, destination])} {...props} destination={destination} explicitDestination={explicitDestination} saved={saved} />;
};

export { ConversationReader };
