// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fetchSessionRecordsPage, SESSION_PAGE_SIZE } from '@/lib/api';
import type { SessionRecord } from '@/lib/types';
import type { SessionAnalysis } from '@/lib/session-analysis-types';
import type { AssembledNode, ConversationItem } from './session-utils';
vi.mock('@/lib/api', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/lib/api')>(),
  fetchSessionRecordsPage: vi.fn(),
}));
vi.mock('./timeline-navigator', () => ({ TimelineNavigator: () => null }));

import {
  anchorIndex,
  centerScrollTop,
  detailActiveAnchor,
  needsAnalysisAnchorPage,
  SessionDetail,
  decideDetailIntersectionAdvance,
  isWithinDetailAdvanceMargin,
  shouldAdvanceDetailWithoutScroll,
} from './session-detail';

describe('decideDetailIntersectionAdvance', () => {
  it('does not advance beyond the initial page without a new user scroll', () => {
    // The initial observe callback may report an already-visible sentinel.
    expect(decideDetailIntersectionAdvance(false, true)).toEqual({ advance: false, scrollPending: false });

    // One user scroll can advance once. Observer callbacks caused by render/page
    // settlement cannot advance again while the sentinel remains visible.
    expect(decideDetailIntersectionAdvance(true, true)).toEqual({ advance: true, scrollPending: false });
    expect(decideDetailIntersectionAdvance(false, true)).toEqual({ advance: false, scrollPending: false });
  });

  it('re-evaluates geometry on scroll when the sentinel stayed intersecting', () => {
    const root = { getBoundingClientRect: () => ({ top: 100, bottom: 500 }) };
    const sentinel = { getBoundingClientRect: () => ({ top: 850, bottom: 870 }) };

    // No observer callback follows the ignored initial callback in this sequence.
    expect(decideDetailIntersectionAdvance(false, true).advance).toBe(false);
    const stillWithinMargin = isWithinDetailAdvanceMargin(root, sentinel);
    expect(stillWithinMargin).toBe(true);
    expect(decideDetailIntersectionAdvance(true, stillWithinMargin))
      .toEqual({ advance: true, scrollPending: false });
  });

  it('advances on downward intent when the initial chunk cannot scroll', () => {
    const root = {
      scrollTop: 0,
      clientHeight: 500,
      scrollHeight: 400,
      getBoundingClientRect: () => ({ top: 100, bottom: 600 }),
    };
    const sentinel = { getBoundingClientRect: () => ({ top: 550, bottom: 570 }) };

    // Initial IO is ignored, and wheel/touch/keyboard intent cannot change scrollTop,
    // so no scroll event follows. The explicit forward intent must authorize one advance.
    expect(decideDetailIntersectionAdvance(false, true).advance).toBe(false);
    expect(shouldAdvanceDetailWithoutScroll(true, root)).toBe(true);
    expect(decideDetailIntersectionAdvance(
      true,
      isWithinDetailAdvanceMargin(root, sentinel),
    )).toEqual({ advance: true, scrollPending: false });

    expect(shouldAdvanceDetailWithoutScroll(false, root)).toBe(false);
    expect(shouldAdvanceDetailWithoutScroll(true, { ...root, scrollHeight: 800 })).toBe(false);
  });

  it('consumes a scroll signal when direct geometry is outside the advance margin', () => {
    const root = { getBoundingClientRect: () => ({ top: 100, bottom: 500 }) };
    const sentinel = { getBoundingClientRect: () => ({ top: 901, bottom: 921 }) };

    expect(decideDetailIntersectionAdvance(true, isWithinDetailAdvanceMargin(root, sentinel)))
      .toEqual({ advance: false, scrollPending: false });
  });
});

// `shown` grows to cover anchorIndex, so anything this misses is a target the body
// never renders and the jump silently lands nowhere. An item-only test missed every
// aside node.
describe('anchorIndex', () => {
  const itemNode = (anchorId: string): AssembledNode =>
    ({ kind: 'item', seam: 'main', key: `i:${anchorId}`, item: { role: 'user', text: '', anchorId, tools: [] } as ConversationItem });
  const asideNode = (key: string): AssembledNode =>
    ({ kind: 'aside', label: 'sub', key, items: [] } as unknown as AssembledNode);

  const nodes = [itemNode('u-1'), itemNode('h-codex'), asideNode('a:1'), itemNode('u-2')];

  it('finds a record by its anchor id, so uuid links minted elsewhere keep resolving', () => {
    expect(anchorIndex(nodes, 'u-1')).toBe(0);
    expect(anchorIndex(nodes, 'u-2')).toBe(3);
  });

  it('finds a record without a uuid by its hashed anchor id', () => {
    expect(anchorIndex(nodes, 'h-codex')).toBe(1);
  });

  it('finds an aside node, which the item-only test skipped', () => {
    expect(anchorIndex(nodes, 'a:1')).toBe(2);
  });

  it('reports -1 for an anchor that is not in the list', () => {
    expect(anchorIndex(nodes, 'nope')).toBe(-1);
  });
});

describe('detailActiveAnchor', () => {
  const item = (id: string, ts: string): AssembledNode =>
    ({ kind: 'item', seam: 'main', key: `i:${id}`, item: { role: 'user', text: '', anchorId: id, ts, tools: [] } as ConversationItem });
  const nodes = [item('old', '2026-10-08T10:00:00Z'), item('turn', '2026-10-08T12:00:00Z')];

  it('takes an analysis turn over an older navigation anchor and prior time link', () => {
    expect(detailActiveAnchor(nodes, true, undefined, '2026-10-08T11:00:00Z', 'old', '2026-10-08T09:00:00Z')).toBe('turn');
    expect(detailActiveAnchor([nodes[0]], true, undefined, '2026-10-08T11:00:00Z', 'old')).toBeUndefined();
  });

  it('keeps explicit prompt navigation and does not resolve analysis time in raw mode', () => {
    expect(detailActiveAnchor(nodes, true, 'old', '2026-10-08T11:00:00Z')).toBe('old');
    expect(detailActiveAnchor(nodes, false, undefined, '2026-10-08T11:00:00Z', 'old')).toBe('old');
  });
});

describe('analysis turn across unloaded records', () => {
  const record = (index: number): SessionRecord => ({
    ts: `2026-10-08T${String(Math.floor(index / 60)).padStart(2, '0')}:${String(index % 60).padStart(2, '0')}:00Z`,
    session_id: 'long-session', project_hash: 'project', record_type: 'user', profile_email: 'owner@example.test', model: 'claude',
    view: { kind: 'message', role: 'user', text: `Message ${index}`, anchor_id: `item-${index}`, dedupe_key: `item-${index}` },
  });
  const firstPage = Array.from({ length: SESSION_PAGE_SIZE }, (_, i) => record(i));
  const secondPage = [record(SESSION_PAGE_SIZE), record(SESSION_PAGE_SIZE + 1)];
  const target = secondPage[0].ts;
  const mount = (active: boolean, analysisAnchorTs = target) => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = render(createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'long-session', mode: 'assembled', onNavigate: vi.fn(), analysisAnchorTs, active,
    })));
    return { client, ...view };
  };

  afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.mocked(fetchSessionRecordsPage).mockReset(); });

  it('loads the next records page only when an active analysis timestamp is beyond the loaded nodes', () => {
    const first = [{ kind: 'item', item: { ts: firstPage[0].ts, anchorId: 'first' } }] as AssembledNode[];
    expect(needsAnalysisAnchorPage(first, true, true, target, undefined, true, false)).toBe(true);
    expect(needsAnalysisAnchorPage(first, true, false, target, undefined, true, false)).toBe(false);
    expect(needsAnalysisAnchorPage(first, true, true, target, 'prompt', true, false)).toBe(false);
    expect(needsAnalysisAnchorPage(first, true, true, target, undefined, true, true)).toBe(false);
    expect(needsAnalysisAnchorPage(first, true, true, target, undefined, true, false, true)).toBe(false);
    expect(needsAnalysisAnchorPage(first, false, true, target, undefined, true, false)).toBe(false);
    expect(needsAnalysisAnchorPage(first, true, true, target, undefined, false, false)).toBe(false);
  });

  it('fetches until the target arrives, then scrolls only when its conversation tab is visible', async () => {
    vi.stubGlobal('IntersectionObserver', class { observe() {} disconnect() {} });
    vi.stubGlobal('CSS', { escape: (value: string) => value });
    const fetch = vi.mocked(fetchSessionRecordsPage);
    fetch.mockImplementation(async ({ offset }) => offset === 0 ? firstPage : secondPage);
    const { client, rerender, container } = mount(false);
    expect(fetch).not.toHaveBeenCalled();
    rerender(createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'long-session', mode: 'assembled', onNavigate: vi.fn(), analysisAnchorTs: target, active: true,
    })));
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    expect(fetch).toHaveBeenNthCalledWith(1, { sessionId: 'long-session', offset: 0, lineage: true, signal: expect.any(AbortSignal) });
    expect(client.getQueryData(['session-records', 'long-session', true])).toBeTruthy();
    await waitFor(() => expect(container.querySelector('[data-anchor="item-1000"]')).not.toBeNull());
    expect(fetch).toHaveBeenNthCalledWith(2, { sessionId: 'long-session', offset: SESSION_PAGE_SIZE, lineage: true, signal: expect.any(AbortSignal) });
    expect(container.querySelector('[data-anchor="item-1000"]')?.classList.contains('ring-brand')).toBe(true);
  });

  it('keeps conversation paging bounded after the standalone timeline is removed', async () => {
    vi.stubGlobal('IntersectionObserver', class { observe() {} disconnect() {} });
    vi.stubGlobal('CSS', { escape: (value: string) => value });
    const first = firstPage.map((row, index) => index === 0 ? { ...row, uuid: 'witnessed-input', view: {
      kind: 'message' as const, role: 'user' as const, text: 'API witnessed prompt', anchor_id: 'witnessed-input', dedupe_key: '',
    } } : row);
    const final = { ...record(SESSION_PAGE_SIZE), record_type: 'assistant', uuid: 'final-output', message_id: 'turn-final', view: {
      kind: 'message' as const, role: 'assistant' as const, text: 'API response', anchor_id: 'final-output', dedupe_key: '',
    } };
    const fetch = vi.mocked(fetchSessionRecordsPage);
    fetch.mockImplementation(async ({ offset }) => offset === 0 ? first : [final]);
    const analysis = {
      session_id: 'long-session', short_id: 'long-ses', owner_view: true, state: 'ready', rules_version: 3, computed_at: '2026-10-08T00:00:00Z',
      header: { title: { text: '실제 API 세션' }, counts: { inputs: 1 } }, turns: [], abandoned: [],
      inputs: [{ uuid: 'witnessed-input', ts: first[0].ts, n: 1, kind: 'typed', turn_key: 'turn-one', images: 0 }],
      questions: [], plans: [], truncated: { turns: false, abandoned: false, inputs: false, questions: false, plans: false, recaps: false, bodies: false },
    } as unknown as SessionAnalysis;
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'long-session', mode: 'assembled', onNavigate: vi.fn(), ...{ timelineOpen: true, analysis },
    })));
    // The newest window renders from its tail, so the last message is on screen.
    await screen.findAllByText('Message 999');
    await act(async () => {});
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('complementary', { name: '타임라인' })).toBeNull();

  });

  it('reopens the same target without remounting after it loads from a later page', async () => {
    vi.stubGlobal('IntersectionObserver', class { observe() {} disconnect() {} });
    vi.stubGlobal('CSS', { escape: (value: string) => value });
    const fetch = vi.mocked(fetchSessionRecordsPage);
    fetch.mockImplementation(async ({ offset }) => offset === 0 ? firstPage : secondPage);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const detail = (active: boolean, analysisAnchorSequence: number) => createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'long-session', mode: 'assembled', onNavigate: vi.fn(), analysisAnchorTs: target, analysisAnchorSequence, active,
    }));
    const { rerender, container } = render(detail(false, 1));
    expect(fetch).not.toHaveBeenCalled();
    rerender(detail(true, 1));
    await waitFor(() => expect(container.querySelector('[data-anchor="item-1000"]')?.classList.contains('ring-brand')).toBe(true));
    const targetElement = container.querySelector('[data-anchor="item-1000"]');
    if (!(targetElement instanceof HTMLElement)) throw new Error('missing conversation target');
    rerender(detail(false, 1));
    targetElement.classList.remove('ring-brand');
    expect(container.querySelector('[data-anchor="item-1000"]')).toBe(targetElement);
    rerender(detail(true, 2));
    await waitFor(() => expect(targetElement.classList.contains('ring-brand')).toBe(true));
    expect(container.querySelector('[data-anchor="item-1000"]')).toBe(targetElement);
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it('stops fetching an unresolved DOM anchor after page failure and still opens a later valid target', async () => {
    vi.stubGlobal('IntersectionObserver', class { observe() {} disconnect() {} });
    vi.stubGlobal('CSS', { escape: (value: string) => value });
    const fetch = vi.mocked(fetchSessionRecordsPage);
    fetch.mockImplementation(async ({ offset }) => {
      if (offset === 0) return firstPage;
      throw new Error('next page unavailable');
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const detail = (scrollAnchor: string) => createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'long-session', mode: 'assembled', onNavigate: vi.fn(), scrollAnchor,
    }));
    const { rerender, container } = render(detail('missing-anchor'));
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(client.getQueryState(['session-records', 'long-session', true])?.fetchStatus).toBe('idle'));
    expect(client.getQueryState(['session-records', 'long-session', true])?.status).toBe('error');
    rerender(detail('item-10'));
    await waitFor(() => expect(container.querySelector('[data-anchor="item-10"]')?.classList.contains('ring-brand')).toBe(true));
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it('loads and marks an analysis target only after the conversation tab becomes visible', async () => {
    vi.stubGlobal('IntersectionObserver', class { observe() {} disconnect() {} });
    vi.stubGlobal('CSS', { escape: (value: string) => value });
    const fetch = vi.mocked(fetchSessionRecordsPage);
    fetch.mockResolvedValue([record(1)]);
    const { client, rerender, container } = mount(false, record(1).ts);
    expect(fetch).not.toHaveBeenCalled();
    expect(container.querySelector('[data-anchor="item-1"]')).toBeNull();
    rerender(createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'long-session', mode: 'assembled', onNavigate: vi.fn(), analysisAnchorTs: record(1).ts, active: true,
    })));
    await waitFor(() => expect(container.querySelector('[data-anchor="item-1"]')).not.toBeNull());
    await waitFor(() => expect(container.querySelector('[data-anchor="item-1"]')?.classList.contains('ring-brand')).toBe(true));
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});

describe('empty first conversation page', () => {
  afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.mocked(fetchSessionRecordsPage).mockReset(); });

  it('offers one user-requested next page when the loaded records are all hidden', async () => {
    const auxiliary = Array.from({ length: SESSION_PAGE_SIZE }, (_, i): SessionRecord => ({
      ts: `2026-10-08T00:${String(i % 60).padStart(2, '0')}:00Z`, session_id: 'large', project_hash: 'p',
      record_type: 'system', profile_email: 'owner@example.test', model: 'claude',
      view: { kind: 'hidden', anchor_id: `hidden-${i}`, dedupe_key: '' },
    }));
    const message: SessionRecord = { ...auxiliary[0], ts: '2026-10-09T00:00:00Z', record_type: 'user',
      view: { kind: 'message', role: 'user', text: 'Conversation begins', anchor_id: 'first-message', dedupe_key: '' } };
    const fetch = vi.mocked(fetchSessionRecordsPage);
    let nextPageAttempts = 0;
    fetch.mockImplementation(async ({ offset }) => {
      if (offset === 0) return auxiliary;
      nextPageAttempts++;
      if (nextPageAttempts === 1) throw new Error('temporary page failure');
      return [message];
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'large', mode: 'assembled', onNavigate: vi.fn(),
    })));

    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    expect(screen.queryByText('No conversation data')).toBeNull();
    const readNext = await screen.findByRole('button', { name: '더 읽기' });
    fireEvent.click(readNext);
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    const retry = await screen.findByRole('button', { name: '다시 시도' });
    fireEvent.click(retry);
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(3));
    expect(await screen.findByText('Conversation begins')).toBeTruthy();
    expect(fetch).toHaveBeenNthCalledWith(2, { sessionId: 'large', offset: SESSION_PAGE_SIZE, lineage: true, signal: expect.any(AbortSignal) });
    expect(fetch).toHaveBeenNthCalledWith(3, { sessionId: 'large', offset: SESSION_PAGE_SIZE, lineage: true, signal: expect.any(AbortSignal) });
  });

  it('does not process a cached target while hidden and highlights it when activated', async () => {
    vi.stubGlobal('CSS', { escape: (value: string) => value });
    const fetch = vi.mocked(fetchSessionRecordsPage);
    const cachedTarget: SessionRecord = {
      ts: '2026-10-08T00:00:00Z', session_id: 'long-session', project_hash: 'project', record_type: 'user',
      profile_email: 'owner@example.test', model: 'claude',
      view: { kind: 'message', role: 'user', text: 'Cached conversation', anchor_id: 'cached-target', dedupe_key: '' },
    };
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    client.setQueryData(['session-records', 'long-session', true], { pages: [[cachedTarget]], pageParams: [0] });
    const detail = (active: boolean) => createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'long-session', mode: 'assembled', onNavigate: vi.fn(), active,
      analysisAnchorTs: cachedTarget.ts,
    }));
    const { rerender, container } = render(detail(false));
    expect(fetch).not.toHaveBeenCalled();
    expect(await screen.findByText('Cached conversation')).toBeTruthy();
    expect(container.querySelector('[data-anchor="cached-target"]')?.classList.contains('ring-brand')).toBe(false);
    rerender(detail(true));
    await waitFor(() => expect(container.querySelector('[data-anchor="cached-target"]')?.classList.contains('ring-brand')).toBe(true));
    expect(fetch).not.toHaveBeenCalled();
  });

  it('deduplicates same-tick next-page clicks while the requested page is pending', async () => {
    const auxiliary = Array.from({ length: SESSION_PAGE_SIZE }, (_, i): SessionRecord => ({
      ts: `2026-10-08T00:${String(i % 60).padStart(2, '0')}:00Z`, session_id: 'large', project_hash: 'p',
      record_type: 'system', profile_email: 'owner@example.test', model: 'claude',
      view: { kind: 'hidden', anchor_id: `hidden-${i}`, dedupe_key: '' },
    }));
    const message: SessionRecord = { ...auxiliary[0], ts: '2026-10-09T00:00:00Z', record_type: 'user',
      view: { kind: 'message', role: 'user', text: 'Delayed conversation', anchor_id: 'delayed-message', dedupe_key: '' } };
    let resolveNextPage!: (page: SessionRecord[]) => void;
    const nextPage = new Promise<SessionRecord[]>(resolve => { resolveNextPage = resolve; });
    const fetch = vi.mocked(fetchSessionRecordsPage);
    fetch.mockImplementation(({ offset }) => offset === 0 ? Promise.resolve(auxiliary) : nextPage);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'large', mode: 'assembled', onNavigate: vi.fn(),
    })));

    const readNext = await screen.findByRole('button', { name: '더 읽기' });
    act(() => {
      fireEvent.click(readNext);
      fireEvent.click(readNext);
    });
    expect(fetch.mock.calls.filter(([params]) => params.offset === SESSION_PAGE_SIZE)).toHaveLength(1);
    await waitFor(() => expect(readNext).toHaveProperty('disabled', true));
    await act(async () => { resolveNextPage([message]); await nextPage; });
    expect(await screen.findByText('Delayed conversation')).toBeTruthy();
  });
});

describe('centerScrollTop', () => {
  // scrollIntoView scrolls every scrollable ancestor, the document included. The
  // dashboard shell is h-screen over a body with no overflow lock, so a jump moved
  // the whole page and left the app's own background exposed below it -- the sidebar
  // was cut off at the same line as the conversation (#677). Centering has to be
  // computed against the container and applied to the container alone.
  const root = { scrollTop: 1000, top: 200, height: 600 };

  it('centres a target below the viewport without touching ancestors', () => {
    // Element 800px down the viewport, 100px tall: its centre must land at root centre.
    expect(centerScrollTop(root, { top: 800, height: 100 })).toBe(1000 + 600 - 250);
  });

  it('centres a target above the viewport', () => {
    expect(centerScrollTop(root, { top: 0, height: 100 })).toBe(1000 - 200 - 250);
  });

  it('never returns a negative offset', () => {
    // Near the top of a session the ideal centre is above the content start.
    expect(centerScrollTop({ scrollTop: 0, top: 200, height: 600 }, { top: 210, height: 40 })).toBe(0);
  });

  it('leaves an already centred target where it is', () => {
    // top 480 + half of 40 = 500, which is the centre of a 200..800 root.
    expect(centerScrollTop(root, { top: 480, height: 40 })).toBe(1000);
  });
});


describe('window navigation and source inspection', () => {
  afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.mocked(fetchSessionRecordsPage).mockReset(); });
  const message = (id: string, role: 'user' | 'assistant', text: string): SessionRecord => ({
    ts: '2026-10-08T00:00:00Z', session_id: 'window', project_hash: 'p', record_type: role, source_file: 'selected.jsonl',
    profile_email: 'owner@example.test', model: 'claude', uuid: id,
    view: { kind: 'message', role, text, anchor_id: id, dedupe_key: id },
  });
  it('finds the reading message from scroll geometry instead of the old selected anchor', async () => {
    vi.stubGlobal('IntersectionObserver', class { observe() {} disconnect() {} });
    vi.stubGlobal('CSS', { escape: (value: string) => value });
    const onRequestNavigate = vi.fn();
    const onLatestRequest = vi.fn();
    const onInspect = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { container } = render(createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'window', mode: 'assembled', onNavigate: vi.fn(), scrollAnchor: 'request-1', onRequestNavigate, onLatestRequest, onInspect,
      initialPage: { offset: 4000, records: [message('request-1', 'user', 'First request'), message('reply-1', 'assistant', 'First answer'), message('request-2', 'user', 'Second request')] },
    })));
    const scroller = container.querySelector<HTMLDivElement>('[data-session-return-scroll="conversation"]')!;
    const first = container.querySelector<HTMLElement>('[data-anchor="request-1"]')!;
    const reading = container.querySelector<HTMLElement>('[data-anchor="reply-1"]')!;
    vi.spyOn(scroller, 'getBoundingClientRect').mockReturnValue({ top: 0, bottom: 500 } as DOMRect);
    vi.spyOn(first, 'getBoundingClientRect').mockReturnValue({ top: -100, bottom: -20 } as DOMRect);
    vi.spyOn(reading, 'getBoundingClientRect').mockReturnValue({ top: -20, bottom: 100 } as DOMRect);
    fireEvent.click(screen.getByRole('button', { name: '이전 요청' }));
    expect(onRequestNavigate).toHaveBeenCalledWith('previous-request', 'reply-1');
    fireEvent.click(screen.getByRole('button', { name: '다음 요청' }));
    expect(onRequestNavigate).toHaveBeenCalledWith('next-request', 'reply-1');
    fireEvent.click(screen.getByRole('button', { name: '최근으로' }));
    expect(onLatestRequest).toHaveBeenCalledOnce();
    fireEvent.click(screen.getAllByRole('button', { name: '이 메시지 상세 트레이스' })[0]);
    expect(onInspect).toHaveBeenCalledWith({ recordId: 'request-1', sourceFile: 'selected.jsonl' });
  });
  it('shows every message time and one detail-trace action without overlapping the body', () => {
    vi.stubGlobal('IntersectionObserver', class { observe() {} disconnect() {} });
    const ts = '2026-10-08T09:00:00.123+09:00';
    const onInspect = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'window', mode: 'assembled', onNavigate: vi.fn(), onInspect,
      initialPage: { offset: 4000, records: [{ ...message('exact-trace', 'assistant', 'Actual response'), ts }] },
    })));
    const time = document.querySelector('time');
    expect(time?.getAttribute('datetime')).toBe(new Date(ts).toISOString());
    const body = document.querySelector('[data-conversation-role="agent"]');
    expect(body?.querySelector('.absolute')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: '이 메시지 상세 트레이스' }));
    expect(onInspect).toHaveBeenCalledExactlyOnceWith({ recordId: 'exact-trace', sourceFile: 'selected.jsonl' });
    expect(fetchSessionRecordsPage).not.toHaveBeenCalled();
  });
  it('keeps the detail-trace action inside a collapsed side thread', () => {
    vi.stubGlobal('IntersectionObserver', class { observe() {} disconnect() {} });
    const ts = '2026-10-08T00:00:05.123Z';
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'window', mode: 'assembled', onNavigate: vi.fn(), onInspect: vi.fn(),
      initialPage: { offset: 0, records: [{ ...message('side-trace', 'assistant', 'Side response'), ts, source_file: 'side.jsonl', is_sidechain: true }] },
    })));
    expect(screen.queryByRole('button', { name: '이 메시지 상세 트레이스' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { expanded: false }));
    expect(screen.getByRole('button', { name: '이 메시지 상세 트레이스' })).toBeTruthy();
  });
  it.each(['', 'not-a-timestamp'])('omits the time label for invalid timestamp %j', ts => {
    vi.stubGlobal('IntersectionObserver', class { observe() {} disconnect() {} });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'window', mode: 'assembled', onNavigate: vi.fn(), onInspect: vi.fn(),
      initialPage: { offset: 0, records: [{ ...message('invalid-ts', 'user', 'Untimed message'), ts }] },
    })));
    expect(document.querySelector('time')).toBeNull();
    expect(screen.getByRole('button', { name: '이 메시지 상세 트레이스' })).toBeTruthy();
  });
  it('does not duplicate a partial first window when earlier records are prepended', async () => {
    vi.stubGlobal('IntersectionObserver', class { observe() {} disconnect() {} });
    const records = ['Before one', 'Before two', 'Current one', 'Current two'].map((text, index) => ({
      ...message(`h:${index}`, 'user', text), uuid: undefined,
    }));
    vi.mocked(fetchSessionRecordsPage).mockResolvedValue(records);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'window', mode: 'assembled', onNavigate: vi.fn(), initialPage: { offset: 2, records: records.slice(2) },
    })));
    fireEvent.click(screen.getByRole('button', { name: '이전 기록' }));
    await screen.findByText('Before one');
    expect(screen.getAllByText('Current one')).toHaveLength(1);
    expect(screen.getAllByText('Current two')).toHaveLength(1);
  });

  it('keeps request navigation available when a restored window begins inside an answer', () => {
    vi.stubGlobal('IntersectionObserver', class { observe() {} disconnect() {} });
    const onRequestNavigate = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'window', mode: 'assembled', onNavigate: vi.fn(), onRequestNavigate, onLatestRequest: vi.fn(),
      initialPage: { offset: 4000, records: [message('only-answer', 'assistant', 'Restored answer')] },
    })));
    fireEvent.click(screen.getByRole('button', { name: '이전 요청' }));
    expect(onRequestNavigate).toHaveBeenCalledWith('previous-request', 'only-answer');
    expect(screen.getByRole('button', { name: '최근으로' })).toBeTruthy();
  });

  it('keeps request navigation, previous-page loading and context inspection in one toolbar', () => {
    vi.stubGlobal('IntersectionObserver', class { observe() {} disconnect() {} });
    const onInspectContext = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'window', mode: 'assembled', onNavigate: vi.fn(), onRequestNavigate: vi.fn(), onLatestRequest: vi.fn(), onInspectContext,
      initialPage: { offset: 4000, records: [message('only-answer', 'assistant', 'Restored answer')] },
    })));
    const toolbar = screen.getByRole('toolbar', { name: '대화 도구' });
    expect(toolbar.contains(screen.getByRole('button', { name: '이전 요청' }))).toBe(true);
    expect(toolbar.contains(screen.getByRole('button', { name: '지시문·훅' }))).toBe(true);
    expect(screen.getAllByRole('toolbar', { name: '대화 도구' })).toHaveLength(1);
    fireEvent.click(screen.getByRole('button', { name: '지시문·훅' }));
    expect(onInspectContext).toHaveBeenCalledOnce();
  });

});
