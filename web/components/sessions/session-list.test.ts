import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import { SessionList, decideIntersectionLoad, isWithinListLoadMargin, requestSessionPage } from './session-list';
import type { SessionOverview } from '@/lib/types';
import { fetchSessionOverview } from '@/lib/api';
import { QueryClient } from '@tanstack/react-query';

const overview = (id: string): SessionOverview => ({
  session_id: id,
  profile_email: 'owner@example.com',
  model: '',
  agent: 'claude',
  start_time: '2026-10-01T00:00:00Z',
  end_time: '2026-10-01T01:00:00Z',
  input_tokens: 10,
  output_tokens: 20,
  cost_usd: 0.1,
  event_count: 3,
  has_sync: true,
});

describe('private overview values', () => {
  const renderRows = (sessions: SessionOverview[]) => renderToStaticMarkup(createElement(SessionList, {
    sessions,
    isLoading: false,
    selectedId: null,
    onSelect: () => undefined,
  }));

  it('shares date formatters across a full first page and its rerender without changing period text', () => {
    const start = '2026-10-07T01:50:00Z';
    const end = '2026-10-16T06:06:00Z';
    const day = (date: Date) => date.toLocaleString(undefined, { month: '2-digit', day: '2-digit' });
    const clock = (date: Date) => date.toLocaleString(undefined, { hour: '2-digit', minute: '2-digit' });
    const expected = `${day(new Date(start))} ${clock(new Date(start))} – ${day(new Date(end))} ${clock(new Date(end))}`;
    const sessions = Array.from({ length: 100 }, (_, index) => ({ ...overview(`row-${index}`), start_time: start, end_time: end }));
    const localeCalls = vi.spyOn(Date.prototype, 'toLocaleString');
    const NativeDateTimeFormat = Intl.DateTimeFormat;
    const formatters = vi.spyOn(Intl, 'DateTimeFormat').mockImplementation(function (...args) {
      return new NativeDateTimeFormat(...args);
    });
    try {
      for (let render = 0; render < 2; render++) {
        const html = renderRows(sessions);
        expect(html.match(/aria-pressed=/g)).toHaveLength(100);
        expect(html.split(expected)).toHaveLength(101);
      }
      expect(localeCalls.mock.calls.length).toBe(0);
      expect(formatters.mock.calls.length).toBe(4);
    } finally {
      localeCalls.mockRestore();
      formatters.mockRestore();
    }
  });

  it('renders the provided owner classification and note first line on the card', () => {
    const html = renderRows([{ ...overview('mine'), classification: 'working', first_line: 'private memo first line' }]);
    expect(html).toContain('작업 중');
    expect(html).toContain('private memo first line');
  });

  it('renders observed analysis metadata without manufacturing missing values', () => {
    const html = renderRows([
      { ...overview('ready'), analysis: { state: 'ready', category: 'waiting', requests: 0, inputs: 2,
        title: { text: 'Observed title', source: 'ai', state: 'ok' }, plan_flags: { has_plan_file: true, used_plan_mode: false } } },
      { ...overview('building'), analysis: { state: 'building' } },
      { ...overview('hidden'), analysis: { state: 'withheld', reason: 'mixed_identity' } },
    ]);
    expect(html).toContain('Observed title');
    expect(html).toContain('요청 0건');
    expect(html).toContain('입력 2건');
    expect(html).toContain('플랜 파일');
    expect(html).toContain('응답 대기');
    expect(html).not.toContain('분석을 만드는 중');
    expect(html).toContain('분석 보류');
    expect(html).not.toContain('분석 building');
    expect(html).not.toContain('요청 0건</span><span>입력 0건');
    expect(html).not.toContain('undefined');
  });

  it('renders the whole list when the server returns a truncated fallback row beside a ready one', async () => {
    const base = { profile_email: 'owner@example.com', user_id: 'u1', login_email: '', account_count: 1,
      login_email_inferred: false, model: '', agent: 'claude', start_time: '2026-10-01T00:00:00Z',
      end_time: '2026-10-01T01:00:00Z', input_tokens: 10, output_tokens: 20, cost_usd: 0.1, event_count: 3,
      has_sync: true, project_hash: '', project_name: '', entrypoint: '', has_enriched: false };
    const body = [
      { ...base, session_id: 'ready-row', analysis: { state: 'ready', title: { text: 'Observed ready title', source: 'ai', state: 'ok' },
        requests: 3, inputs: 4, context: { percent: 12.5, tokens: 25000, source: 'observed', large_window_seen: false },
        plan_flags: { has_plan_file: false, used_plan_mode: true } } },
      { ...base, session_id: 'truncated-row', analysis: { state: 'truncated', title: { text: null, source: '', state: '' },
        requests: null, inputs: null, context: { percent: null, tokens: null, source: '', large_window_seen: false },
        plan_flags: { has_plan_file: false, used_plan_mode: false } } },
    ];
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })));
    try {
      const html = renderRows(await fetchSessionOverview({ analysis: true }));
      expect(html).toContain('Observed ready title');
      expect(html).toContain('요청 3건');
      expect(html).toContain('플랜 모드');
      expect(html).toContain('분석 범위 제한');
      expect(html.match(/aria-pressed=/g)).toHaveLength(2);
      expect(html).toContain('>ready-ro<');
      expect(html).toContain('>truncate<');
      expect(html).not.toContain('undefined');
      expect(html).not.toContain('요청 0건');
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('keeps owner and bare rows separate on one list', () => {
    const html = renderRows([
      { ...overview('owner-only'), classification: 'consulting', first_line: 'owner-only-private-note' },
      { ...overview('bare-row'), profile_email: 'other@example.com' },
    ]);
    const cards = html.match(/<button\b[^>]*aria-pressed="false"[^>]*>[\s\S]*?<\/button>/g) ?? [];
    expect(cards).toHaveLength(2);
    expect(cards[0]).toContain('상담 중');
    expect(cards[0]).toContain('owner-only-private-note');
    expect(cards[1]).toContain('other@example.com');
    expect(cards[1]).not.toContain('상담 중');
    expect(cards[1]).not.toContain('owner-only-private-note');
  });

  it('does not invent private values for rows without owner fields', () => {
    const html = renderRows([overview('other')]);
    expect(html).not.toContain('미분류');
    expect(html).not.toContain('private memo first line');
  });

  it('hides an unknown classification but keeps the session metrics', () => {
    const row = overview('unknown');
    const html = renderRows([{ ...row, classification: 'invalid-server-value' as SessionOverview['classification'] }]);
    expect(html).not.toContain('invalid-server-value');
    expect(html).toContain('In:');
    expect(html).toContain('Out:');
    expect(html).toContain('Cost:');
  });

  it('does not render an inherited classification key as a React child', () => {
    const html = renderRows([{ ...overview('inherited'), classification: '__proto__' as SessionOverview['classification'] }]);
    expect(html).not.toContain('__proto__');
    expect(html).toContain('In:');
    expect(html).toContain('Out:');
    expect(html).toContain('Cost:');
  });

  it('omits the note preview when its first line is empty', () => {
    const html = renderRows([{ ...overview('mine'), classification: 'done', first_line: '' }]);
    expect(html).toContain('완료');
    expect(html).not.toContain('title=""');
  });

  it('keeps each row a selectable button with its selected state', () => {
    const html = renderToStaticMarkup(createElement(SessionList, {
      sessions: [{ ...overview('mine'), classification: 'working', first_line: 'memo' }, overview('other')],
      isLoading: false,
      selectedId: 'mine',
      onSelect: () => undefined,
    }));
    const cards = html.match(/<button\b[^>]*aria-pressed="(?:true|false)"[^>]*>[\s\S]*?<\/button>/g) ?? [];
    expect(cards).toHaveLength(2);
    expect(cards[0]).toContain('aria-pressed="true"');
    expect(cards[1]).toContain('aria-pressed="false"');
  });

  it('renders only the new owner response after an account switch', () => {
    const previous = [{ ...overview('same-session'), classification: 'working' as const, first_line: 'old owner note' }];
    const current = [overview('same-session')];
    expect(renderRows(previous)).toContain('old owner note');
    const html = renderRows(current);
    expect(html).not.toContain('old owner note');
    expect(html).not.toContain('작업 중');
  });
});

describe('authenticated overview cache scope', () => {
  it('uses a separate overview key for a new signed-in user and revalidates through the existing prefix', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const ownerA = ['session-overview', 1, '', 'interactive', '', '', true];
    const ownerB = ['session-overview', 2, '', 'interactive', '', '', true];
    const fetchA = vi.fn(async () => [{ ...overview('same'), first_line: 'owner A note' }]);
    const fetchB = vi.fn(async () => [overview('same')]);
    await client.fetchQuery({ queryKey: ownerA, queryFn: fetchA });
    expect(client.getQueryData<SessionOverview[]>(ownerB)).toBeUndefined();
    await client.fetchQuery({ queryKey: ownerB, queryFn: fetchB });
    expect(fetchB).toHaveBeenCalledTimes(1);
    expect(client.getQueryData<SessionOverview[]>(ownerB)?.[0].first_line).toBeUndefined();
    await client.invalidateQueries({ queryKey: ['session-overview'] });
    expect(client.getQueryState(ownerA)?.isInvalidated).toBe(true);
    expect(client.getQueryState(ownerB)?.isInvalidated).toBe(true);
    client.clear();
  });
});

describe('empty filtered session results', () => {
  it('offers another page without automatically loading it on render', () => {
    let loadRequests = 0;
    const html = renderToStaticMarkup(createElement(SessionList, {
      sessions: [],
      isLoading: false,
      selectedId: null,
      onSelect: () => undefined,
      hasMore: true,
      onLoadMore: () => { loadRequests += 1; },
    }));

    expect(loadRequests).toBe(0);
    expect(html).toContain('data-testid="empty-session-load-more"');

    requestSessionPage(false, () => { loadRequests += 1; });
    expect(loadRequests).toBe(1);
  });

  it('does not offer another page when the result set is exhausted', () => {
    const html = renderToStaticMarkup(createElement(SessionList, {
      sessions: [],
      isLoading: false,
      selectedId: null,
      onSelect: () => undefined,
      hasMore: false,
      onLoadMore: () => undefined,
    }));

    expect(html).not.toContain('data-testid="empty-session-load-more"');
  });

  it('does not duplicate an explicit request while a page is loading', () => {
    let loadRequests = 0;

    requestSessionPage(true, () => { loadRequests += 1; });

    expect(loadRequests).toBe(0);
  });
});

describe('decideIntersectionLoad', () => {
  it('loads at most one page per new user-scroll-caused intersection', () => {
    // IntersectionObserver reports the already-visible sentinel on initial observe.
    expect(decideIntersectionLoad(false, true)).toEqual({ load: false, scrollPending: false });

    // A user scroll may trigger one page, but settling that page while the sentinel
    // remains visible must not automatically drain another page.
    expect(decideIntersectionLoad(true, true)).toEqual({ load: true, scrollPending: false });
    expect(decideIntersectionLoad(false, true)).toEqual({ load: false, scrollPending: false });
  });

  it('re-evaluates geometry on scroll when no second observer callback occurs', () => {
    const root = { getBoundingClientRect: () => ({ top: 100, bottom: 500 }) };
    const sentinel = { getBoundingClientRect: () => ({ top: 700, bottom: 720 }) };

    // The observer's sole callback was the ignored initial intersection. A later scroll
    // must determine the still-intersecting state directly instead of waiting for IO.
    expect(decideIntersectionLoad(false, true).load).toBe(false);
    const stillWithinMargin = isWithinListLoadMargin(root, sentinel);
    expect(stillWithinMargin).toBe(true);
    expect(decideIntersectionLoad(true, stillWithinMargin)).toEqual({ load: true, scrollPending: false });
  });

  it('consumes a scroll signal when direct geometry is outside the load margin', () => {
    const root = { getBoundingClientRect: () => ({ top: 100, bottom: 500 }) };
    const sentinel = { getBoundingClientRect: () => ({ top: 801, bottom: 821 }) };

    expect(decideIntersectionLoad(true, isWithinListLoadMargin(root, sentinel)))
      .toEqual({ load: false, scrollPending: false });
  });
});
