'use client';

import { useEffect, useRef, useState } from 'react';
import Link from 'next/link';
import { ArrowLeft } from 'lucide-react';
import { cn } from '@/lib/utils';
import { groupSameIdRows, localSessionSelectionKey, mergeLocalSessionOverviews, sameIdGroupingCandidates } from '@/lib/local-session-merge';
import type { LocalSessionOverview, SignedInOwnerScope } from '@/lib/local-session-types';
import type { LocalSessionAnalysis } from '@/lib/local-analysis-types';
import type { SessionOverview, SessionRecord } from '@/lib/types';
import { createLocalSessionApi, LocalSessionApiError, validateLocalOrigin } from '@/lib/local-session-api';
import { clearConnection, loadConnection, saveConnection, setActiveConnection } from '@/lib/local-session-connection';
import { LocalSessionDetail } from './local-session-detail';

type LocalConnection = ReturnType<typeof createLocalSessionApi>;

export interface LocalSessionsPanelProps {
  embedded?: boolean;
  serverSessions?: readonly SessionOverview[];
  ownerScope?: SignedInOwnerScope;
  onSelectServer?: (id: string) => void;
}

const errorMessage = (error: unknown, connecting = false): string => {
  if (!(error instanceof LocalSessionApiError)) return '로컬 응답을 처리하지 못했습니다.';
  switch (error.code) {
    case 'unauthorized': return '로컬 연결 인증에 실패했습니다. 토큰을 확인해 주세요.';
    case 'forbidden': return '로컬 세션에 접근할 권한이 없습니다. 발급된 주소를 그대로 입력했는지 확인해 주세요.';
    case 'not_found': return '로컬 서비스 또는 세션을 찾지 못했습니다.';
    case 'revision_conflict': return '원본이 바뀌어 기록을 처음부터 다시 불러옵니다.';
    case 'unreachable': return connecting
      ? '로컬 서비스에 연결할 수 없습니다. 발급된 주소를 그대로 입력했는지, --origin으로 준 주소의 페이지에서 열었는지, 서비스가 실행 중인지 확인해 주세요.'
      : '로컬 서비스와의 연결이 끊겼습니다. 서비스가 실행 중인지 확인해 주세요.';
    case 'invalid_origin': return '발급된 주소(127.0.0.1 또는 [::1]과 포트)를 그대로 입력해 주세요.';
    case 'malformed': return '로컬 서비스가 올바르지 않은 응답을 보냈습니다.';
    default: return '로컬 요청을 완료하지 못했습니다.';
  }
};

const sessionTitle = (session: LocalSessionOverview): string =>
  session.title || session.name || session.first_line || session.project_name || '제목 없는 로컬 세션';

export function LocalSessionsPanel({ embedded = false, serverSessions = [], ownerScope, onSelectServer }: LocalSessionsPanelProps = {}) {
  const [origin, setOrigin] = useState('');
  const [token, setToken] = useState('');
  const [remember, setRemember] = useState(false);
  const [connection, setConnection] = useState<LocalConnection | null>(null);
  const connectionAttempt = useRef(0);
  const connectionRef = useRef<LocalConnection | null>(null);
  const selectionRef = useRef<string | null>(null);
  const loadedPages = useRef(1);
  const listOperation = useRef(0);
  const listBusy = useRef(0);
  const detailRequest = useRef(0);
  const [connecting, setConnecting] = useState(false);
  const [list, setList] = useState<readonly LocalSessionOverview[]>([]);
  const [listCursor, setListCursor] = useState<string | null>(null);
  const listCursorRef = useRef<string | null>(null);
  const [listRevision, setListRevision] = useState('');
  const listSnapshot = useRef<readonly LocalSessionOverview[]>([]);
  const listRevisionRef = useRef('');
  const [listLoadingMore, setListLoadingMore] = useState(false);
  const listRequest = useRef(0);
  const [listError, setListError] = useState('');
  const [localUnreachable, setLocalUnreachable] = useState(false);
  const [search, setSearch] = useState('');
  const [agent, setAgent] = useState<'all' | 'claude' | 'codex'>('all');
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [mobileDetailOpen, setMobileDetailOpen] = useState(false);
  const [grouping, setGrouping] = useState<{ owner?: string; source: LocalConnection | null; keys: ReadonlySet<string> }>(() => ({
    owner: ownerScope?.user_id, source: null, keys: new Set(),
  }));
  const [previousOwner, setPreviousOwner] = useState(ownerScope?.user_id);
  if (previousOwner !== ownerScope?.user_id) {
    setPreviousOwner(ownerScope?.user_id);
    setGrouping({ owner: ownerScope?.user_id, source: connection, keys: new Set() });
  }
  const activeGrouping = grouping.owner === ownerScope?.user_id && grouping.source === connection;
  const grouped = activeGrouping ? grouping.keys : new Set<string>();
  const [records, setRecords] = useState<SessionRecord[][]>([]);
  const [recordsRevision, setRecordsRevision] = useState('');
  const [recordsKey, setRecordsKey] = useState<string | null>(null);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [recordsLoading, setRecordsLoading] = useState(false);
  const [recordsError, setRecordsError] = useState('');
  const [analysis, setAnalysis] = useState<LocalSessionAnalysis>();
  const [analysisKey, setAnalysisKey] = useState<string | null>(null);
  const [analysisLoading, setAnalysisLoading] = useState(false);
  const [analysisError, setAnalysisError] = useState('');

  const selected = list.find((row) => localSessionSelectionKey(row) === selectedKey);
  const selectLocal = (key: string) => {
    setMobileDetailOpen(true);
    if (selectionRef.current === key) return;
    selectionRef.current = key;
    detailRequest.current += 1;
    setSelectedKey(key);
  };
  const showLocalList = () => setMobileDetailOpen(false);
  const localId = selected?.local_id;
  const revision = selected?.revision;
  const selectionMissing = !!selectedKey && !selected && !!connection;

  /**
   * Reconnects from a pairing this tab already saved, once on mount. The stored
   * endpoint and token are what `cctrace local enable` printed, so the next visit
   * reaches the same local daemon without pasting them again. A missing or stale
   * pairing leaves the manual form; it is never refreshed or resent to a server.
   */
  useEffect(() => {
    const saved = loadConnection();
    if (!saved) return;
    setOrigin(saved.origin);
    void connectTo(saved.origin, saved.token, true);
    // connectTo is stable enough here: the effect runs only on mount, and its
    // closures read refs plus setState, not render-scoped values.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!connection) return;
    let active = true;
    const refresh = async () => {
      if (listBusy.current) return;
      const operation = ++listOperation.current;
      listBusy.current = operation;
      const current = () => active && connectionRef.current === connection && listOperation.current === operation;
      try {
        const first = await connection.listSessions();
        if (!current()) return;
        const previousRevision = listRevisionRef.current;
        let sessions = first.sessions;
        let cursor = first.next_cursor || null;
        let pages = 1;
        if (first.revision === previousRevision && loadedPages.current > 1) {
          const firstKeys = new Set(sessions.map(localSessionSelectionKey));
          sessions = [...sessions, ...listSnapshot.current.filter((row) => !firstKeys.has(localSessionSelectionKey(row)))];
          pages = loadedPages.current;
          cursor = listCursorRef.current;
        } else if (first.revision !== previousRevision) {
          const targetPages = loadedPages.current;
          while (cursor && pages < targetPages) {
            const page = await connection.listSessions({ cursor });
            if (!current()) return;
            if (page.revision !== first.revision) throw new LocalSessionApiError('revision_conflict');
            const existing = new Set(sessions.map(localSessionSelectionKey));
            sessions = [...sessions, ...page.sessions.filter((row) => !existing.has(localSessionSelectionKey(row)))];
            cursor = page.next_cursor || null;
            pages += 1;
          }
        }
        if (!current()) return;
        listRequest.current += 1;
        loadedPages.current = pages;
        listSnapshot.current = sessions;
        if (selectionRef.current && !sessions.some((row) => localSessionSelectionKey(row) === selectionRef.current)) {
          detailRequest.current += 1;
          setRecordsLoading(false);
          setAnalysisLoading(false);
        }
        listRevisionRef.current = first.revision;
        setList(sessions);
        listCursorRef.current = cursor;
        setListCursor(cursor);
        setListRevision(first.revision);
        setListError('');
        setLocalUnreachable(false);
      } catch (error) {
        if (current()) {
          setListError(errorMessage(error));
          setLocalUnreachable(error instanceof LocalSessionApiError && error.code === 'unreachable');
        }
      } finally { if (listBusy.current === operation) listBusy.current = 0; }
    };
    const interval = window.setInterval(() => { void refresh(); }, 3_000);
    return () => { active = false; window.clearInterval(interval); };
  }, [connection]);

  useEffect(() => {
    if (!connection || !localId || !revision) return;
    let active = true;
    const request = ++detailRequest.current;
    const refresh = async () => {
      const current = () => active && connectionRef.current === connection && detailRequest.current === request && selectionRef.current === selectedKey;
      setRecordsLoading(true);
      setRecordsKey(selectedKey);
      setAnalysisKey(selectedKey);
      setAnalysisLoading(true);
      setRecords([]);
      setRecordsRevision('');
      setNextCursor(null);
      setAnalysis(undefined);
      setRecordsError('');
      setAnalysisError('');
      void connection.listRecords(localId, { revision }).then((page) => {
        if (!current()) return;
        if (page.revision !== revision) return;
        setRecords([page.records]);
        setRecordsKey(selectedKey);
        setRecordsRevision(page.revision);
        setNextCursor(page.next_cursor || null);
      }).catch((error) => { if (current()) setRecordsError(errorMessage(error)); })
        .finally(() => { if (current()) setRecordsLoading(false); });
      void connection.analysis(localId).then((value) => {
        if (current() && value.revision === revision) { setAnalysis(value); setAnalysisKey(selectedKey); }
      }).catch((error) => { if (current()) setAnalysisError(errorMessage(error)); })
        .finally(() => { if (current()) setAnalysisLoading(false); });
    };
    void refresh();
    return () => { active = false; detailRequest.current += 1; };
  }, [connection, localId, revision, selectedKey]);

  const connectTo = async (endpoint: string, pairing: string, persist: boolean) => {
    const attempt = ++connectionAttempt.current;
    setActiveConnection(null);
    setConnecting(true);
    setListError('');
    setLocalUnreachable(false);
    setListLoadingMore(false);
    setGrouping({ owner: ownerScope?.user_id, source: null, keys: new Set() });
    connectionRef.current = null;
    setConnection(null);
    listOperation.current += 1;
    listBusy.current = 0;
    listRequest.current += 1;
    detailRequest.current += 1;
    setRecordsLoading(false);
    setAnalysisLoading(false);
    setList([]);
    listSnapshot.current = [];
    loadedPages.current = 1;
    listCursorRef.current = null;
    setListCursor(null);
    setListRevision('');
    listRevisionRef.current = '';
    selectionRef.current = null;
    setSelectedKey(null);
    try {
      // The rewritten origin is for storage and the input box only. A default port drops its ":port",
      // which createLocalSessionApi would reject on validating again, so it gets the original endpoint.
      const normalized = validateLocalOrigin(endpoint);
      const client = createLocalSessionApi({ origin: endpoint, token: pairing });
      await client.health();
      const first = await client.listSessions();
      if (connectionAttempt.current !== attempt) return;
      listRequest.current += 1;
      listSnapshot.current = first.sessions;
      listRevisionRef.current = first.revision;
      setList(first.sessions);
      listCursorRef.current = first.next_cursor || null;
      setListCursor(first.next_cursor || null);
      setListRevision(first.revision);
      connectionRef.current = client;
      setConnection(client);
      setOrigin(normalized);
      setActiveConnection({ origin: normalized, token: pairing });
      if (persist) saveConnection({ origin: normalized, token: pairing });
      setToken('');
    } catch (error) {
      if (connectionAttempt.current === attempt) {
        setListError(errorMessage(error, true));
        setLocalUnreachable(error instanceof LocalSessionApiError && error.code === 'unreachable');
      }
    } finally {
      if (connectionAttempt.current === attempt) setConnecting(false);
    }
  };

  const handleConnect = async (event: React.FormEvent) => {
    event.preventDefault();
    await connectTo(origin.trim(), token, remember);
  };

  const handleDisconnect = () => {
    connectionAttempt.current += 1;
    connectionRef.current = null;
    setConnection(null);
    setOrigin('');
    setToken('');
    clearConnection();
    setList([]);
    listSnapshot.current = [];
    listCursorRef.current = null;
    setListCursor(null);
    setListRevision('');
    listRevisionRef.current = '';
    selectionRef.current = null;
    setSelectedKey(null);
    setListError('');
    setLocalUnreachable(false);
  };

  const loadMoreSessions = async () => {
    if (!connection || !listCursor || listLoadingMore || listBusy.current) return;
    const request = listRequest.current;
    const operation = ++listOperation.current;
    listBusy.current = operation;
    const current = () => connectionRef.current === connection && listRequest.current === request && listOperation.current === operation;
    setListLoadingMore(true);
    try {
      const page = await connection.listSessions({ cursor: listCursor });
      if (!current()) return;
      if (page.revision !== listRevision) throw new LocalSessionApiError('revision_conflict');
      const existing = new Set(listSnapshot.current.map(localSessionSelectionKey));
      const sessions = [...listSnapshot.current, ...page.sessions.filter((row) => !existing.has(localSessionSelectionKey(row)))];
      listSnapshot.current = sessions;
      loadedPages.current += 1;
      setList(sessions);
      listCursorRef.current = page.next_cursor || null;
      setListCursor(page.next_cursor || null);
      setListError('');
    } catch (error) {
      if (!current()) return;
      if (error instanceof LocalSessionApiError && error.code === 'revision_conflict') {
        try {
          const fresh = await connection.listSessions();
          if (!current()) return;
          let sessions = fresh.sessions;
          let cursor = fresh.next_cursor || null;
          let pages = 1;
          const targetPages = loadedPages.current;
          while (cursor && pages < targetPages) {
            const next = await connection.listSessions({ cursor });
            if (!current()) return;
            if (next.revision !== fresh.revision) throw new LocalSessionApiError('revision_conflict');
            const existing = new Set(sessions.map(localSessionSelectionKey));
            sessions = [...sessions, ...next.sessions.filter((row) => !existing.has(localSessionSelectionKey(row)))];
            cursor = next.next_cursor || null;
            pages += 1;
          }
          listRequest.current += 1;
          loadedPages.current = pages;
          listSnapshot.current = sessions;
          if (selectionRef.current && !sessions.some((row) => localSessionSelectionKey(row) === selectionRef.current)) {
            detailRequest.current += 1;
            setRecordsLoading(false);
            setAnalysisLoading(false);
          }
          listRevisionRef.current = fresh.revision;
          setList(sessions);
          listCursorRef.current = cursor;
          setListCursor(cursor);
          setListRevision(fresh.revision);
          setListError('');
        } catch (retryError) { if (current()) setListError(errorMessage(retryError)); }
      } else setListError(errorMessage(error));
    } finally {
      if (listBusy.current === operation) listBusy.current = 0;
      if (connectionRef.current === connection) setListLoadingMore(false);
    }
  };

  const loadMore = async () => {
    if (!connection || !localId || !revision || !nextCursor || recordsLoading || recordsKey !== selectedKey || recordsRevision !== revision) return;
    const request = detailRequest.current;
    const current = () => detailRequest.current === request && connectionRef.current === connection && selectionRef.current === selectedKey;
    setRecordsLoading(true);
    setRecordsError('');
    try {
      const page = await connection.listRecords(localId, { cursor: nextCursor, revision });
      if (!current()) return;
      if (page.revision !== revision) {
        setRecords([]);
        setRecordsRevision('');
        setNextCursor(null);
        setRecordsError('원본이 변경되었습니다. 최신 목록을 기다리는 중입니다.');
        return;
      }
      setRecords((old) => [...old, page.records]);
      setRecordsRevision(page.revision);
      setNextCursor(page.next_cursor || null);
    } catch (error) {
      if (!current()) return;
      if (error instanceof LocalSessionApiError && error.code === 'revision_conflict') {
        setRecords([]);
        setRecordsRevision('');
        setNextCursor(null);
        try {
          const page = await connection.listRecords(localId, { revision });
          if (!current()) return;
          if (page.revision === revision) {
            setRecords([page.records]);
            setRecordsKey(selectedKey);
            setRecordsRevision(page.revision);
            setNextCursor(page.next_cursor || null);
          }
        } catch (retryError) { if (current()) setRecordsError(errorMessage(retryError)); }
      } else setRecordsError(errorMessage(error));
    } finally {
      if (current()) setRecordsLoading(false);
    }
  };

  const merged = embedded
    ? mergeLocalSessionOverviews(localUnreachable ? { status: 'unreachable' } : { status: 'ready', sessions: list }, serverSessions, ownerScope)
    : null;
  const candidates = !localUnreachable && embedded && connection
    ? sameIdGroupingCandidates(list, serverSessions, ownerScope) : new Map<string, string>();
  const sourceRows = merged && 'rows' in merged ? merged.rows
    : list.map((local) => ({ source: 'local' as const, local, selection_key: localSessionSelectionKey(local) }));
  const filtered = sourceRows.filter((row) => (agent === 'all' || (row.source === 'local' ? row.local.agent : row.server.agent) === agent) &&
    (!search || (row.source === 'local'
      ? [sessionTitle(row.local), row.local.project_name ?? '', row.local.model ?? '', row.local.native_session_id ?? '']
      : [row.server.project_name ?? '', row.server.model, row.server.session_id])
      .some((value) => value.toLowerCase().includes(search.toLowerCase()))));
  const groups = groupSameIdRows(filtered, candidates, grouped);

  return <main className={cn('flex min-w-0 flex-col gap-4 bg-canvas p-4 text-ink sm:p-6', embedded ? 'min-h-full' : 'min-h-screen')}>
    <header className="flex flex-wrap items-center justify-between gap-3">
      <div><h1 className="text-xl font-semibold">내 기기 세션</h1><p className="text-xs text-ink-3">로컬 서비스에서 직접 읽습니다. 서버 로그인이나 동기화가 필요하지 않습니다.</p></div>
      <Link className="text-xs text-brand hover:underline" href="/sessions">서버 세션으로 이동</Link>
    </header>
    <form onSubmit={handleConnect} className="flex flex-wrap items-end gap-3 rounded-lg border border-border bg-surface p-4">
      <label className="flex min-w-0 basis-full flex-col gap-1 text-xs sm:basis-0 sm:flex-1">로컬 서비스 주소
        <input required type="url" value={origin} onChange={(event) => setOrigin(event.target.value)} placeholder="http://127.0.0.1:..." className="min-h-11 min-w-0 rounded border border-border bg-canvas px-3 py-2 text-sm" />
      </label>
      <label className="flex min-w-0 basis-full flex-col gap-1 text-xs sm:basis-0 sm:flex-1">접속 토큰
        <input required type="password" autoComplete="off" value={token} onChange={(event) => setToken(event.target.value)} className="min-h-11 min-w-0 rounded border border-border bg-canvas px-3 py-2 text-sm" />
      </label>
      <label className="flex items-center gap-1.5 text-xs text-ink-2">
        <input type="checkbox" checked={remember} onChange={(event) => setRemember(event.target.checked)} className="h-3.5 w-3.5 rounded border-border" />
        이 탭에 연결 저장
      </label>
      <button type="submit" disabled={connecting} className="min-h-11 rounded bg-brand px-4 py-2 text-sm text-brand-ink disabled:opacity-50">{connecting ? '연결 중' : '연결'}</button>
      {connection && <button type="button" onClick={handleDisconnect} className="min-h-11 rounded border border-border px-4 py-2 text-sm text-ink-2 hover:border-danger hover:text-danger">연결 끊기</button>}
    </form>
    {listError && <p role="alert" className="rounded border border-danger bg-surface p-3 text-sm text-danger">{listError}</p>}
    {embedded && !ownerScope && <p role="alert" className="text-sm text-ink-3">서버 행은 별도 출처로 표시하며, 소유자 범위를 확인할 수 없어 중복을 자동으로 합치지 않습니다.</p>}
    {(connection || embedded) && <div data-mobile-view={mobileDetailOpen && selected ? 'detail' : 'list'} className="flex min-h-[440px] min-w-0 flex-1 flex-col overflow-hidden rounded-lg border border-border bg-surface lg:flex-row">
      <aside className={cn('min-h-48 shrink-0 flex-col border-b border-border lg:flex lg:w-[320px] lg:border-b-0 lg:border-r', mobileDetailOpen && selected ? 'hidden' : 'flex')}>
        <div className="flex gap-2 border-b border-border p-3">
          <input aria-label="로컬 세션 검색" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="제목 또는 프로젝트 검색" className="min-w-0 flex-1 rounded border border-border bg-canvas px-2 py-1.5 text-xs" />
          <select aria-label="에이전트 필터" value={agent} onChange={(event) => setAgent(event.target.value as typeof agent)} className="rounded border border-border bg-canvas px-1 text-xs"><option value="all">전체</option><option value="claude">Claude</option><option value="codex">Codex</option></select>
        </div>
        <div className="max-h-72 overflow-auto md:max-h-none md:flex-1" aria-label="로컬 세션 목록">
          {groups.map((group) => {
            const first = group[0];
            const localKey = first.source === 'local' ? first.selection_key : null;
            const candidate = localKey ? candidates.get(localKey) : undefined;
            const isGrouped = group.length === 2;
            const matchedServer = first.source === 'local' ? first.server : undefined;
            return <div key={first.selection_key} className="border-b border-border">
              {candidate && <div className="flex items-center justify-between gap-2 bg-surface-2 px-3 py-2 text-xs">
                <span>같은 ID의 서버 세션 · 같은 ID만으로 동일 세션 여부를 확인할 수 없어 직접 묶어야 합니다. {isGrouped ? '사용자가 묶어 표시함' : '두 출처를 별도로 표시함'}</span>
                <button type="button" onClick={() => setGrouping((current) => {
                  const keys = new Set(current.owner === ownerScope?.user_id && current.source === connection ? current.keys : []);
                  if (keys.has(localKey!)) keys.delete(localKey!); else keys.add(localKey!);
                  return { owner: ownerScope?.user_id, source: connection, keys };
                })} className="shrink-0 text-brand hover:underline">{isGrouped ? '묶기 해제' : '같은 세션으로 묶기'}</button>
              </div>}
              {group.map((row) => <button key={row.selection_key} type="button" onClick={() => row.source === 'local'
                ? selectLocal(row.selection_key) : onSelectServer?.(row.server.session_id)} aria-current={row.source === 'local' && selectedKey === row.selection_key ? 'true' : undefined}
                className="flex w-full flex-col gap-1 px-3 py-3 text-left text-xs hover:bg-surface-2 aria-current:bg-brand-soft">
                <span className="truncate font-medium">{row.source === 'local' ? sessionTitle(row.local) : row.server.project_name || row.server.session_id || '서버 세션'}</span>
                <span className="text-ink-3">{row.source === 'local' ? '내 기기' : '서버'} · {row.source === 'local' ? row.local.agent : row.server.agent} · {row.source === 'local'
                  ? row.local.project_name || row.local.model || '모델 미관측' : row.server.project_name || row.server.model}</span>
                {row.source === 'local' && <span className="text-ink-3">입력 {row.local.input_tokens ?? '미집계'} · 출력 {row.local.output_tokens ?? '미집계'} · 기록 {row.local.event_count ?? '미집계'} · 비용 {row.local.cost_usd === null ? '미집계' : `$${row.local.cost_usd}`}</span>}
              </button>)}
              {matchedServer && onSelectServer && <button type="button"
                onClick={() => onSelectServer(matchedServer.session_id)}
                className="px-3 pb-2 text-left text-xs text-brand hover:underline">매칭된 서버 세션 보기</button>}
            </div>;
          })}
          {filtered.length === 0 && <p className="p-3 text-xs text-ink-3">표시할 로컬 세션이 없습니다.</p>}
          {listCursor && <button type="button" disabled={listLoadingMore} onClick={() => { void loadMoreSessions(); }} className="w-full border-b border-border p-3 text-left text-xs text-brand disabled:opacity-50">{listLoadingMore ? '목록을 더 불러오는 중' : '세션 더 보기'}</button>}
        </div>
      </aside>
      <div className={cn('min-h-0 min-w-0 flex-1 flex-col', mobileDetailOpen && selected ? 'flex' : 'hidden lg:flex')}>
      {selected && <button type="button" onClick={showLocalList} className="flex min-h-11 shrink-0 items-center gap-2 border-b border-border px-3 text-left text-sm text-ink-2 lg:hidden"><ArrowLeft size={16} aria-hidden="true" />로컬 세션 목록으로</button>}
      {selected ? <LocalSessionDetail key={selectedKey} localId={selected.local_id} title={sessionTitle(selected)} revision={selected.revision}
        records={recordsKey === selectedKey && recordsRevision === selected.revision ? records : []}
        recordsRevision={recordsKey === selectedKey ? recordsRevision : ''}
        recordsLoading={recordsLoading} recordsError={!!recordsError && recordsKey === selectedKey}
        hasMore={recordsKey === selectedKey && recordsRevision === selected.revision && !!nextCursor}
        loadingMore={recordsLoading} onLoadMore={() => { void loadMore(); }}
        analysis={analysisKey === selectedKey ? analysis : undefined}
        analysisLoading={analysisLoading} analysisError={!!analysisError && analysisKey === selectedKey} />
        : <p className="flex flex-1 items-center justify-center p-6 text-sm text-ink-3">{selectionMissing ? '선택한 로컬 세션이 최신 목록에서 사라졌습니다.' : '목록에서 세션을 선택해 주세요.'}</p>}
      </div>
    </div>}
  </main>;
}
