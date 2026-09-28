import { afterEach, describe, expect, it, vi } from 'vitest';
import { consumeAIReportStream, createSSEParser, mergeToolCallEvent } from './ai-report-stream';
import type { AIReportStreamEvent } from './ai-report-stream';
import { openAIReportRunEvents } from './api';
import type { AIToolCall } from './types';

const encoder = new TextEncoder();

const streamResponse = (chunks: readonly (string | Uint8Array)[], init: ResponseInit = {}): Response =>
  new Response(
    new ReadableStream<Uint8Array>({
      start(controller) {
        for (const chunk of chunks) controller.enqueue(typeof chunk === 'string' ? encoder.encode(chunk) : chunk);
        controller.close();
      },
    }),
    { status: 200, headers: { 'Content-Type': 'text/event-stream' }, ...init },
  );

const collect = async (res: Response) => {
  const events: AIReportStreamEvent[] = [];
  const outcome = await consumeAIReportStream(res, (event) => events.push(event));
  return { events, outcome };
};

describe('createSSEParser', () => {
  it('joins a frame split across chunks, mid-line and mid-separator', () => {
    const parser = createSSEParser();

    expect(parser.push('event: tool_call\nda')).toEqual([]);
    expect(parser.push('ta: {"seq":1,')).toEqual([]);
    expect(parser.push('"tool":"query_segments","args":{}}\nid: 1\n')).toEqual([]);
    expect(parser.push('\n')).toEqual([
      { event: 'tool_call', data: '{"seq":1,"tool":"query_segments","args":{}}', id: '1' },
    ]);
  });

  it('reads CRLF line endings and several frames in one chunk', () => {
    const parser = createSSEParser();

    expect(parser.push('event: a\r\ndata: 1\r\n\r\nevent: b\r\ndata: 2\r\n\r\n')).toEqual([
      { event: 'a', data: '1', id: null },
      { event: 'b', data: '2', id: null },
    ]);
  });

  it('skips ping comments and joins multi-line data', () => {
    const parser = createSSEParser();

    expect(parser.push(': ping\n\n')).toEqual([]);
    expect(parser.push('data: first\ndata: second\n\n')).toEqual([{ event: 'message', data: 'first\nsecond', id: null }]);
  });
});

describe('consumeAIReportStream', () => {
  it('delivers events in order and ends as terminal after a finished status', async () => {
    const res = streamResponse([
      'id: 1\nevent: tool_call\ndata: {"seq":1,"tool":"query_segments","args":{"limit":20}}\n\n',
      'id: 1\nevent: tool_result\ndata: {"seq":1,"status":"ok","result_rows":20,"duration_ms":41}\n',
      '\n: ping\n\nevent: status\ndata: {"status":"completed"}\n\nevent: report\ndata: {"run_id":12}\n\n',
    ]);

    const { events, outcome } = await collect(res);

    expect(outcome).toBe('terminal');
    expect(events.map((event) => event.type)).toEqual(['tool_call', 'tool_result', 'status', 'report']);
    expect(events[1]).toEqual({ type: 'tool_result', data: { seq: 1, status: 'ok', result_rows: 20, duration_ms: 41 } });
  });

  // A Korean argument split between two bytes of one character must not turn
  // into replacement characters.
  it('decodes a multi-byte character split across chunks', async () => {
    const bytes = encoder.encode('event: tool_call\ndata: {"seq":2,"tool":"read_segment","args":{"q":"한"}}\n\nevent: status\ndata: {"status":"failed"}\n\n');
    const cut = bytes.indexOf(0xed) + 1;

    const { events } = await collect(streamResponse([bytes.slice(0, cut), bytes.slice(cut)]));

    expect(events[0]).toEqual({ type: 'tool_call', data: { seq: 2, tool: 'read_segment', args: { q: '한' } } });
  });

  // Polling takes over whenever the stream cannot say the run ended.
  it('reads a final frame closed with bare CRs at the end of the stream', async () => {
    const { outcome } = await collect(streamResponse(['event: status\rdata: {"status":"completed"}\r\r']));

    expect(outcome).toBe('terminal');
  });

  it('releases the body of a response it will not read', async () => {
    const res = streamResponse(['x'], { status: 503 });

    await collect(res);

    await expect(res.body!.getReader().read()).resolves.toEqual({ done: true, value: undefined });
  });

  it('signals fallback when the stream closes before a terminal status', async () => {
    const { outcome } = await collect(streamResponse(['event: status\ndata: {"status":"running"}\n\n']));

    expect(outcome).toBe('fallback');
  });

  it('signals fallback for a non-2xx response or a body that is not a stream', async () => {
    expect((await collect(streamResponse([], { status: 404 }))).outcome).toBe('fallback');
    expect((await collect(new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))).outcome).toBe('fallback');
  });

  it('signals fallback on a frame whose data is not JSON', async () => {
    const { outcome } = await collect(streamResponse(['event: tool_call\ndata: {oops\n\n']));

    expect(outcome).toBe('fallback');
  });

  it('ignores event names it does not know', async () => {
    const { events, outcome } = await collect(
      streamResponse(['event: text_delta\ndata: {"text":"x"}\n\nevent: status\ndata: {"status":"canceled"}\n\n']),
    );

    expect(events.map((event) => event.type)).toEqual(['status']);
    expect(outcome).toBe('terminal');
  });
});

describe('mergeToolCallEvent', () => {
  const call = (over: Partial<AIToolCall>): AIToolCall => ({ seq: 1, tool: 'query_segments', args: {}, status: 'ok', ...over });

  it('appends a new call as running and completes it on its result', () => {
    const started = mergeToolCallEvent([], { type: 'tool_call', data: { seq: 1, tool: 'query_segments', args: { limit: 5 } } });
    expect(started).toEqual([{ seq: 1, tool: 'query_segments', args: { limit: 5 }, status: 'running' }]);

    const finished = mergeToolCallEvent(started, { type: 'tool_result', data: { seq: 1, status: 'ok', result_rows: 5, duration_ms: 30 } });
    expect(finished).toEqual([{ seq: 1, tool: 'query_segments', args: { limit: 5 }, status: 'ok', result_rows: 5, duration_ms: 30 }]);
  });

  // The stream replays calls already in the query snapshot on connect.
  it('keeps one row per seq, ordered by seq, when events replay', () => {
    const calls = [call({ seq: 1 }), call({ seq: 3, tool: 'compare_week' })];

    const merged = mergeToolCallEvent(calls, { type: 'tool_call', data: { seq: 2, tool: 'read_segment', args: {} } });
    const replayed = mergeToolCallEvent(merged, { type: 'tool_call', data: { seq: 1, tool: 'query_segments', args: {} } });

    expect(replayed.map((c) => [c.seq, c.status])).toEqual([[1, 'ok'], [2, 'running'], [3, 'ok']]);
  });

  it('leaves the list alone for a result with no call or a non-tool event', () => {
    const calls = [call({ seq: 1 })];

    expect(mergeToolCallEvent(calls, { type: 'tool_result', data: { seq: 9, status: 'ok' } })).toBe(calls);
    expect(mergeToolCallEvent(calls, { type: 'status', data: { status: 'running' } })).toBe(calls);
  });
});

describe('openAIReportRunEvents', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  // EventSource closes on a 401 without refreshing; the reader goes through
  // apiFetch so an expired access token is refreshed and the stream retried.
  it('refreshes an expired session and retries the stream', async () => {
    const urls: string[] = [];
    const responses = [new Response(null, { status: 401 }), new Response(null, { status: 200 }), streamResponse([])];
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      urls.push(String(input));
      return Promise.resolve(responses[urls.length - 1]);
    }));

    const res = await openAIReportRunEvents(12, new AbortController().signal);

    expect(res.status).toBe(200);
    expect(urls[0]).toContain('/api/ai-reports/runs/12/events');
    expect(urls[1]).toContain('/api/auth/refresh');
    expect(urls[2]).toContain('/api/ai-reports/runs/12/events');
  });
});
