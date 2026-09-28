import type {
  AIReportStreamReport,
  AIReportStreamStatus,
  AIReportStreamToolCall,
  AIReportStreamToolResult,
  AIToolCall,
} from './types';

/** One SSE frame. `event` defaults to "message" as in the SSE format. */
interface SSEMessage {
  event: string;
  data: string;
  id: string | null;
}

interface SSEParser {
  /** Feeds decoded text; returns the frames it completed. `final` marks the end
   *  of the stream. */
  push: (text: string, final?: boolean) => SSEMessage[];
}

type AIReportStreamEvent =
  | { type: 'tool_call'; data: AIReportStreamToolCall }
  | { type: 'tool_result'; data: AIReportStreamToolResult }
  | { type: 'usage'; data: Record<string, unknown> }
  | { type: 'status'; data: AIReportStreamStatus }
  | { type: 'report'; data: AIReportStreamReport };

/** How a stream ended. `fallback` means it could not say the run finished, so
 *  the caller polls; `aborted` means the caller closed it. */
type AIReportStreamOutcome = 'terminal' | 'fallback' | 'aborted';

const KNOWN_EVENTS = new Set<string>(['tool_call', 'tool_result', 'usage', 'status', 'report']);

/** Incremental SSE parser: a frame may arrive split at any byte of any line, so
 *  text is buffered until a blank line closes the frame. */
const createSSEParser = (): SSEParser => {
  let buffer = '';
  let event = '';
  let data: string[] = [];
  let id: string | null = null;

  const takeLine = (line: string): SSEMessage | null => {
    if (line === '') {
      const message = data.length > 0 ? { event: event || 'message', data: data.join('\n'), id } : null;
      event = '';
      data = [];
      id = null;
      return message;
    }
    if (line.startsWith(':')) return null;
    const colon = line.indexOf(':');
    const field = colon === -1 ? line : line.slice(0, colon);
    const value = colon === -1 ? '' : line.slice(colon + 1).replace(/^ /, '');
    if (field === 'event') event = value;
    else if (field === 'data') data.push(value);
    else if (field === 'id') id = value;
    return null;
  };

  const push = (text: string, final = false): SSEMessage[] => {
    buffer += text;
    const messages: SSEMessage[] = [];
    let newline = buffer.search(/\r\n|\n|\r/);
    while (newline !== -1) {
      // A lone \r at the chunk's end may be the first half of \r\n -- unless no
      // more text is coming.
      if (!final && buffer[newline] === '\r' && newline === buffer.length - 1) break;
      const width = buffer.startsWith('\r\n', newline) ? 2 : 1;
      const message = takeLine(buffer.slice(0, newline));
      buffer = buffer.slice(newline + width);
      if (message) messages.push(message);
      newline = buffer.search(/\r\n|\n|\r/);
    }
    return messages;
  };

  return { push };
};

const isTerminal = (event: AIReportStreamEvent): boolean => event.type === 'status' && event.data.status !== 'running';

/** Reads a run's event stream to its end. Any failure to read -- a non-2xx
 *  response, a body that is not a stream, a malformed frame, a close before a
 *  terminal status -- is `fallback`: the database is the source, the stream only
 *  shortens the wait (plan §3). */
const consumeAIReportStream = async (
  res: Response,
  onEvent: (event: AIReportStreamEvent) => void,
  signal?: AbortSignal,
): Promise<AIReportStreamOutcome> => {
  const contentType = res.headers.get('Content-Type') ?? '';
  if (!res.ok || !res.body || !contentType.includes('text/event-stream')) {
    await res.body?.cancel().catch(() => undefined);
    return 'fallback';
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  const parser = createSSEParser();
  let terminal = false;

  try {
    for (;;) {
      const { done, value } = await reader.read();
      // A frame the server did not close with a blank line is dropped, as SSE does.
      const text = done ? decoder.decode() : decoder.decode(value, { stream: true });
      for (const message of parser.push(text, done)) {
        if (!KNOWN_EVENTS.has(message.event)) continue;
        const event = { type: message.event, data: JSON.parse(message.data) } as AIReportStreamEvent;
        onEvent(event);
        if (isTerminal(event)) terminal = true;
      }
      if (done) break;
    }
  } catch {
    if (signal?.aborted) return 'aborted';
    await reader.cancel().catch(() => undefined);
    return 'fallback';
  }
  return terminal ? 'terminal' : 'fallback';
};

/** Folds a stream event into the run's tool calls: one row per seq, ordered by
 *  seq, so replayed events on reconnect never duplicate a row. Returns the same
 *  array when nothing changed. */
const mergeToolCallEvent = (calls: AIToolCall[], event: AIReportStreamEvent): AIToolCall[] => {
  if (event.type === 'tool_call') {
    if (calls.some((call) => call.seq === event.data.seq)) return calls;
    const added: AIToolCall = { seq: event.data.seq, tool: event.data.tool, args: event.data.args, status: 'running' };
    return [...calls, added].sort((a, b) => a.seq - b.seq);
  }
  if (event.type === 'tool_result') {
    const { seq, ...result } = event.data;
    if (!calls.some((call) => call.seq === seq)) return calls;
    return calls.map((call) => (call.seq === seq ? { ...call, ...result } : call));
  }
  return calls;
};

export { consumeAIReportStream, createSSEParser, mergeToolCallEvent };
export type { AIReportStreamEvent, AIReportStreamOutcome, SSEMessage };
