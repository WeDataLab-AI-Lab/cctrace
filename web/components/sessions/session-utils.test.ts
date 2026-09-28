import { describe, expect, it } from 'vitest';
import { fmtSessionPeriod, buildAssembled, buildConversation, collectUserPrompts, dedupeRecords, isLegacySession, keyRecords, needsAccountNotice, recordKey, anchorAtOrAfter } from './session-utils';
import type { AssembledNode, ConversationItem } from './session-utils';
import type { SessionRecord, SessionRecordView } from '@/lib/types';

let anchorSeq = 0;
const record = (over: Partial<SessionRecord> = {}): SessionRecord =>
  ({ session_id: 's', ts: '2026-08-10T00:00:00Z', record_type: 'user', raw: {}, ...over }) as SessionRecord;

// A record carrying the server-built view. anchor_id defaults to the uuid, as the server
// does, and to a unique stand-in for the stable hash otherwise; dedupe_key defaults to a
// unique value so records only merge when a test says they are the same line.
const viewRec = (view: Omit<SessionRecordView, 'anchor_id' | 'dedupe_key'> & { anchor_id?: string; dedupe_key?: string }, over: Partial<SessionRecord> = {}): SessionRecord =>
  record({ ...over, view: { anchor_id: over.uuid ?? `h${++anchorSeq}`, dedupe_key: `d${++anchorSeq}`, ...view } });

const userRec = (ts: string, text: string, over: Partial<SessionRecord> = {}): SessionRecord =>
  viewRec({ kind: 'message', role: 'user', text }, { ts, record_type: 'user', ...over });

// An assistant turn that issues one tool_use, so an aside can anchor on its id.
const toolRec = (ts: string, toolUseId: string, over: Partial<SessionRecord> = {}): SessionRecord =>
  viewRec(
    { kind: 'message', role: 'assistant', tool_calls: [{ id: toolUseId, name: 'Task', input: '{"a":1}' }] },
    { ts, record_type: 'assistant', ...over },
  );

const resultRec = (ts: string, id: string, output: string, over: Partial<SessionRecord> = {}): SessionRecord =>
  viewRec({ kind: 'tool_result', role: 'user', tool_results: [{ id, output }] }, { ts, record_type: 'user', ...over });

const keysOf = (nodes: { key: string }[]): string[] => nodes.map(n => n.key);

describe('isLegacySession', () => {
  it('flags records collected before the source_file enrichment', () => {
    expect(isLegacySession([record()])).toBe(true);
  });

  it('does not flag records that carry source_file', () => {
    expect(isLegacySession([record({ source_file: 'chat.jsonl' })])).toBe(false);
  });

  // A session with no records is not "pre-update" — it is an otel-only session, or
  // one whose JSONL has not synced yet. The empty-state message already says so;
  // adding the legacy badge asserts a cause that is not true and makes real legacy
  // sessions indistinguishable.
  it('does not flag a session that has no records at all', () => {
    expect(isLegacySession([])).toBe(false);
  });

  it('does not flag a mixed session where only some records are enriched', () => {
    expect(isLegacySession([record(), record({ source_file: 'chat.jsonl' })])).toBe(false);
  });

  // The loaded page is a window over the session, not the session. A session whose
  // enriched records all sit on a later page reads as legacy under the record
  // heuristic; the server's whole-session aggregate must win over that guess.
  it('trusts the session-wide flag over the loaded page', () => {
    expect(isLegacySession([record()], true)).toBe(false);
    expect(isLegacySession([record({ source_file: 'chat.jsonl' })], false)).toBe(true);
  });

  it('flags a session the server reports unenriched before its records load', () => {
    expect(isLegacySession([], false)).toBe(true);
  });

  it('falls back to the record heuristic when the overview is unknown', () => {
    expect(isLegacySession([record()], undefined)).toBe(true);
    expect(isLegacySession([record({ source_file: 'chat.jsonl' })], undefined)).toBe(false);
  });
});

describe('AssembledNode.key', () => {
  const main = [
    toolRec('2026-08-10T00:00:01Z', 'tu1', { uuid: 'u1', source_file: 'main.jsonl' }),
    userRec('2026-08-10T00:00:02Z', 'second', { uuid: 'u2', source_file: 'main.jsonl' }),
    userRec('2026-08-10T00:00:03Z', 'third', { uuid: 'u3', source_file: 'main.jsonl' }),
  ];
  const aside = userRec('2026-08-10T00:00:01Z', 'side work', {
    uuid: 'a1',
    source_file: 'aside.jsonl',
    is_sidechain: true,
    tool_use_id: 'tu1',
  });

  // The core regression: buildAssembled splices asides *between* existing nodes, so an
  // index-based key would shift every node after the insertion point and reattach open
  // AsideGroup / CompactSummary state to the wrong row on the next poll.
  it('keeps keys of later nodes unchanged when an aside is inserted before them', () => {
    const before = keysOf(buildAssembled(main));
    const after = keysOf(buildAssembled([...main, aside]));
    expect(after.length).toBe(before.length + 1);
    const inserted = after.filter(k => !before.includes(k));
    expect(inserted.length).toBe(1);
    expect(inserted[0].startsWith('a:')).toBe(true);
    expect(after.filter(k => k !== inserted[0])).toEqual(before);
  });

  it('keys items by the anchor id', () => {
    expect(keysOf(buildAssembled(main))).toEqual(['i:u1', 'i:u2', 'i:u3']);
  });

  it('keeps keys unique when records share an anchor id', () => {
    const same = [
      userRec('2026-08-10T00:00:01Z', 'one', { uuid: 'dup' }),
      record({ ts: '2026-08-10T00:00:01Z', view: { kind: 'message', role: 'user', text: 'one', anchor_id: 'dup', dedupe_key: 'x' } }),
    ];
    const keys = keysOf(buildAssembled(same));
    expect(keys.length).toBe(2);
    expect(new Set(keys).size).toBe(2);
  });

  // A side thread with no tool_use_id to anchor on lands after the last main item at or
  // before its start — which needs the item's own ts, uuid or not.
  it('places an unanchored aside by ts even when main records carry no uuid', () => {
    const nodes = buildAssembled([
      userRec('2026-08-10T00:00:01Z', 'first'),
      userRec('2026-08-10T00:00:05Z', 'second'),
      userRec('2026-08-10T00:00:02Z', 'side', { is_sidechain: true, source_file: 'aside.jsonl' }),
    ]);
    expect(nodes.map(n => n.kind)).toEqual(['item', 'aside', 'item']);
  });
});

describe('buildConversation', () => {
  it('pairs an assistant tool call with its result and carries the call id', () => {
    const items = buildConversation([
      toolRec('2026-08-10T00:00:01Z', 'tu1', { uuid: 'u1' }),
      resultRec('2026-08-10T00:00:02Z', 'tu1', 'done'),
    ]);
    expect(items.length).toBe(1);
    expect(items[0]).toMatchObject({ role: 'assistant', anchorId: 'u1', toolUseIds: ['tu1'] });
    expect(items[0].tools[0]).toMatchObject({ id: 'tu1', name: 'Task', input: '{"a":1}', result: 'done', startTs: '2026-08-10T00:00:01Z', endTs: '2026-08-10T00:00:02Z' });
  });

  // Records sharing a ts come back ordered by uuid, and Codex rows have none, so a
  // result can precede its call. It must still pair once, not also show as an orphan.
  it('pairs a result that arrives before its call without an orphan copy', () => {
    const call = toolRec('2026-08-10T00:00:01Z', 'c1');
    const result = resultRec('2026-08-10T00:00:01Z', 'c1', 'done');
    for (const order of [[call, result], [result, call]]) {
      const items = buildConversation(order);
      expect(items.length).toBe(1);
      expect(items[0].tools).toMatchObject([{ id: 'c1', result: 'done' }]);
    }
  });

  it('still shows a result whose call is absent', () => {
    const items = buildConversation([resultRec('2026-08-10T00:00:01Z', 'gone', 'late'), toolRec('2026-08-10T00:00:02Z', 'c2')]);
    expect(items.map(i => i.tools.map(t => t.name))).toEqual([['result'], ['Task']]);
  });

  it('keeps assistant text alongside its tool calls', () => {
    const items = buildConversation([
      viewRec({ kind: 'message', role: 'assistant', text: 'looking', tool_calls: [{ id: 't', name: 'Read' }] }, { record_type: 'assistant' }),
    ]);
    expect(items[0]).toMatchObject({ role: 'assistant', text: 'looking' });
    expect(items[0].tools.map(t => t.name)).toEqual(['Read']);
  });

  // Codex stores each call and each output as its own record. They used to vanish because
  // the viewer only understood Claude's content blocks.
  it('renders a Codex tool_call record as an assistant item paired with its tool_result', () => {
    const items = buildConversation([
      viewRec({ kind: 'tool_call', role: 'assistant', tool_calls: [{ id: 'call_1', name: 'exec_command', input: '{"cmd":"ls"}' }] }, { ts: '2026-08-10T00:00:01Z', record_type: 'tool_call' }),
      viewRec({ kind: 'tool_result', tool_results: [{ id: 'call_1', output: 'a.txt', is_error: true }] }, { ts: '2026-08-10T00:00:02Z', record_type: 'tool_output' }),
    ]);
    expect(items.length).toBe(1);
    expect(items[0]).toMatchObject({ role: 'assistant', text: '' });
    expect(items[0].tools[0]).toMatchObject({ id: 'call_1', name: 'exec_command', result: 'a.txt', isError: true });
  });

  it('shows a tool result with no matching call as an orphan', () => {
    const items = buildConversation([resultRec('2026-08-10T00:00:02Z', 'lost', 'out')]);
    expect(items.length).toBe(1);
    expect(items[0]).toMatchObject({ role: 'user', text: '' });
    expect(items[0].tools[0]).toMatchObject({ id: 'lost', name: 'result', result: 'out' });
  });

  it('skips hidden, reasoning and view-less records', () => {
    const items = buildConversation([
      viewRec({ kind: 'hidden' }),
      viewRec({ kind: 'reasoning', text: '' }),
      record({ raw: { message: { content: 'raw is not read' } } }),
    ]);
    expect(items).toEqual([]);
  });

  it('renders a command as a chip with its args', () => {
    const items = buildConversation([viewRec({ kind: 'message', role: 'user', command: 'model', command_args: 'opus' })]);
    expect(items).toMatchObject([{ role: 'user', text: '', command: 'model', commandArgs: 'opus' }]);
  });

  it('flags a compact summary', () => {
    const items = buildConversation([viewRec({ kind: 'message', role: 'user', text: 'summary', compact_summary: true })]);
    expect(items).toMatchObject([{ role: 'user', text: 'summary', compactSummary: true }]);
  });

  it('renders an agent task as a user-role item flagged agentTask', () => {
    const items = buildConversation([viewRec({ kind: 'message', role: 'user', text: 'Message Type: NEW_TASK', agent_task: true }, { record_type: 'agent_task' })]);
    expect(items).toMatchObject([{ role: 'user', agentTask: true, text: 'Message Type: NEW_TASK' }]);
  });
});

describe('recordKey', () => {
  it('uses the anchor id', () => {
    expect(recordKey(userRec('2026-08-10T00:00:01Z', 'x', { uuid: 'u1' }), 'g', 3)).toBe('r:u1');
  });

  it('falls back to group key + ts + index for a record without a view', () => {
    const r = record({ ts: '2026-08-10T00:00:01Z' });
    expect(recordKey(r, 'g', 0)).not.toBe(recordKey(r, 'g', 1));
  });
});

// An SDK harness re-serialises tool results as prose; the server recovers them as
// tool_results marked inferred. They may name a call from an earlier session, so they
// only fill a pairing no genuine result makes and never surface on their own.
describe('inferred tool results', () => {
  const proseRec = (ts: string, text: string, results: { id: string; output: string; inferred?: boolean }[]): SessionRecord =>
    viewRec({ kind: 'message', role: 'user', text, tool_results: results }, { ts, prompt_source: 'sdk' });
  const inferred = (id: string, output: string) => ({ id, output, inferred: true });

  it('pairs inferred results onto the assistant calls and keeps the request', () => {
    const items = buildConversation([
      toolRec('2026-08-10T00:00:01Z', 'toolu_01a'),
      toolRec('2026-08-10T00:00:02Z', 'toolu_01b'),
      proseRec('2026-08-10T00:00:03Z', 'check issue #4', [inferred('toolu_01a', 'first'), inferred('toolu_01b', 'second')]),
    ]);
    expect(items.filter(i => i.role === 'assistant').map(i => i.tools[0].result)).toEqual(['first', 'second']);
    expect(items.filter(i => i.role === 'user').map(i => i.text)).toEqual(['check issue #4']);
  });

  it('hides a record whose results were all paired', () => {
    const items = buildConversation([
      toolRec('2026-08-10T00:00:01Z', 'toolu_01a'),
      proseRec('2026-08-10T00:00:02Z', '', [inferred('toolu_01a', 'hello')]),
    ]);
    expect(items.length).toBe(1);
  });

  it('never shows an unmatched inferred result as an orphan', () => {
    const items = buildConversation([
      viewRec({ kind: 'tool_result', tool_results: [inferred('toolu_other_session', 'stale')] }),
    ]);
    expect(items).toEqual([]);
  });

  it('lets a genuine result in an earlier record win over later inferred prose', () => {
    const items = buildConversation([
      toolRec('2026-08-10T00:00:01Z', 'toolu_01a'),
      resultRec('2026-08-10T00:00:02Z', 'toolu_01a', 'block wins'),
      proseRec('2026-08-10T00:00:03Z', '', [inferred('toolu_01a', 'prose loses')]),
    ]);
    expect(items[0].tools[0].result).toBe('block wins');
  });

  // The server lists a record's genuine blocks first, then its inferred results.
  it('lets the genuine block win over inferred prose in the same record', () => {
    const items = buildConversation([
      toolRec('2026-08-10T00:00:01Z', 'toolu_01a'),
      proseRec('2026-08-10T00:00:02Z', '', [{ id: 'toolu_01a', output: 'block wins' }, inferred('toolu_01a', 'prose loses')]),
    ]);
    expect(items[0].tools[0].result).toBe('block wins');
  });

  it('still shows an unmatched genuine result from an sdk record', () => {
    const items = buildConversation([proseRec('2026-08-10T00:00:02Z', '', [{ id: 'lost', output: 'out' }])]);
    expect(items[0].tools[0]).toMatchObject({ id: 'lost', result: 'out' });
  });
});

// A session can hold a legacy copy (no uuid/source_file) and an enriched copy of the same
// line. Enrichment-only columns (prompt_source, is_meta, is_compact_summary) make their
// views differ, so identity is the server's dedupe_key, not the view content.
describe('dedupeRecords', () => {
  it('drops the legacy twin of an enriched record even when their views differ', () => {
    const enriched = viewRec(
      { kind: 'message', role: 'user', text: 'summary', compact_summary: true, dedupe_key: 'line-1' },
      { ts: '2026-08-10T00:00:01Z', uuid: 'u1', source_file: 'main.jsonl', is_compact_summary: true },
    );
    const legacy = viewRec({ kind: 'message', role: 'user', text: 'summary', dedupe_key: 'line-1' }, { ts: '2026-08-10T00:00:01Z' });
    expect(dedupeRecords([legacy, enriched])).toEqual([enriched]);
  });

  it('keeps a legacy record with a different dedupe key, even at the same ts', () => {
    const enriched = userRec('2026-08-10T00:00:01Z', 'one', { uuid: 'u1', source_file: 'main.jsonl' });
    const legacy = userRec('2026-08-10T00:00:01Z', 'one');
    expect(dedupeRecords([legacy, enriched])).toHaveLength(2);
  });

  it('never merges records that carry no dedupe key', () => {
    const view = { kind: 'message' as const, role: 'user' as const, text: 'x', anchor_id: 'a', dedupe_key: '' };
    const enriched = record({ source_file: 'main.jsonl', uuid: 'u1', view });
    const legacy = record({ view });
    const bare = record({ source_file: 'main.jsonl' });
    expect(dedupeRecords([legacy, enriched, record(), bare])).toHaveLength(4);
  });

  it('drops exact uuid duplicates', () => {
    const a = userRec('2026-08-10T00:00:01Z', 'x', { uuid: 'u1' });
    expect(dedupeRecords([a, { ...a }])).toHaveLength(1);
  });
});

describe('keyRecords', () => {
  it('keeps raw-view keys unique when identical legacy records share an anchor id', () => {
    const view = { kind: 'message' as const, role: 'user' as const, text: 'x', anchor_id: 'same-hash', dedupe_key: 'k' };
    const keys = keyRecords([record({ view }), record({ view })], 'g').map(k => k.key);
    expect(new Set(keys).size).toBe(2);
    expect(keys[0]).toBe('r:same-hash');
  });
});

describe('collectUserPrompts', () => {
  const item = (over: Partial<ConversationItem>): AssembledNode =>
    ({ kind: 'item', seam: 'main', key: over.anchorId ?? 'k', item: { role: 'user', text: '', tools: [], anchorId: 'k', ...over } });

  it('keeps what the person typed', () => {
    const nodes: AssembledNode[] = [
      item({ anchorId: 'u1', text: 'first question' }),
      { kind: 'item', seam: 'main', key: 'a1', item: { role: 'assistant', text: 'answer', tools: [], anchorId: 'a1' } },
      item({ anchorId: 'u2', text: 'second question' }),
    ];
    expect(collectUserPrompts(nodes).map(p => p.anchorId)).toEqual(['u1', 'u2']);
  });

  it('keeps a slash command, which has no text of its own', () => {
    expect(collectUserPrompts([item({ anchorId: 'c1', command: 'deploy', commandArgs: 'dev' })])).toHaveLength(1);
  });

  // These all carry role 'user' because that is how the transcript encodes them, but none
  // of them is a turn the person wrote. Listing them would bury the actual prompts.
  it('drops user-role turns the person did not write', () => {
    const nodes: AssembledNode[] = [
      item({ anchorId: 'x1', text: 'summary of earlier turns', compactSummary: true }),
      item({ anchorId: 'x2', text: 'go research this', agentTask: true }),
      item({ anchorId: 'x3', text: '   ' }),
      item({ anchorId: 'x4' }),
    ];
    expect(collectUserPrompts(nodes)).toEqual([]);
  });

  it('ignores boundaries and asides', () => {
    const nodes: AssembledNode[] = [
      { kind: 'boundary', seam: 'compaction', label: 'compacted', key: 'b1' },
      { kind: 'aside', label: 'subagent', key: 'a1', items: [{ role: 'user', text: 'inside an aside', tools: [], anchorId: 'x' }] },
    ];
    expect(collectUserPrompts(nodes)).toEqual([]);
  });
});

describe('fmtSessionPeriod', () => {
  it('shows both ends so the row says how long, not just when', () => {
    const out = fmtSessionPeriod('2026-08-21T01:35:00Z', '2026-08-21T05:52:00Z');
    expect(out).toContain('–');
    // The day appears once: repeating it for a same-day session is noise.
    expect(out.match(/\d{2}\.\s?\d{2}\./g) ?? out.match(/\d{2}\/\d{2}/g)).toHaveLength(1);
  });

  it('repeats the day when the session crossed midnight', () => {
    const out = fmtSessionPeriod('2026-08-21T14:00:00Z', '2026-08-22T02:00:00Z');
    const days = out.match(/\d{2}\.\s?\d{2}\./g) ?? out.match(/\d{2}\/\d{2}/g);
    expect(days).toHaveLength(2);
  });

  it('falls back to the start alone when there is no usable end', () => {
    // A live session, or one whose end never reached the record. Deriving an end from
    // "now" would make the row change on every render.
    const start = fmtSessionPeriod('2026-08-21T01:35:00Z');
    expect(start).not.toContain('–');
    expect(fmtSessionPeriod('2026-08-21T01:35:00Z', 'not-a-date')).toBe(start);
    expect(fmtSessionPeriod('2026-08-21T01:35:00Z', '2026-08-21T01:35:00Z')).toBe(start);
  });

  it('returns empty for an unparseable start rather than "Invalid Date"', () => {
    expect(fmtSessionPeriod('nonsense')).toBe('');
  });
});

describe('anchorAtOrAfter', () => {
  // A Work Segment carries the instant it began, not the message that began it.
  // Resolving the instant to an anchor id is what lets the existing scroll-and-highlight
  // path serve a caller that only knows a time (#434).
  const at = (anchorId: string, ts?: string): AssembledNode =>
    ({ kind: 'item', seam: 'main', key: anchorId, item: { role: 'user', text: '', tools: [], anchorId, ts } });

  const nodes: AssembledNode[] = [
    at('a', '2026-08-24T09:00:00Z'),
    at('b', '2026-08-24T09:05:00Z'),
    at('c', '2026-08-24T09:10:00Z'),
  ];

  it('lands on the first item at or after the instant', () => {
    expect(anchorAtOrAfter(nodes, '2026-08-24T09:05:00Z')).toBe('b');
    expect(anchorAtOrAfter(nodes, '2026-08-24T09:04:59Z')).toBe('b');
  });

  it('lands on the first item when the segment opens before anything loaded', () => {
    expect(anchorAtOrAfter(nodes, '2026-08-24T08:00:00Z')).toBe('a');
  });

  // A segment whose window opens past the end of what is loaded resolves to
  // nothing and the view stays put, rather than scrolling somewhere arbitrary.
  it('resolves to nothing past the end', () => {
    expect(anchorAtOrAfter(nodes, '2026-08-24T23:00:00Z')).toBeUndefined();
  });

  it('is inert without an instant, so ordinary navigation is untouched', () => {
    expect(anchorAtOrAfter(nodes, undefined)).toBeUndefined();
    expect(anchorAtOrAfter(nodes, '')).toBeUndefined();
  });

  // Treating a missing timestamp as time zero would make the first undated item
  // swallow every anchor, which is how a segment link would silently land on the
  // wrong message instead of failing visibly.
  it('skips items with no timestamp', () => {
    const withGap: AssembledNode[] = [at('x'), at('y', '2026-08-24T09:05:00Z')];
    expect(anchorAtOrAfter(withGap, '2026-08-24T09:00:00Z')).toBe('y');
  });
});

describe('needsAccountNotice', () => {
  // The session header is a thin identity bar now: everything the left list already
  // shows was dropped from it. The account signals are the exception, because they
  // are not values but warnings -- "this address was deduced, not observed" and
  // "this session touched more than one account". Dropping them unconditionally
  // would let a reader take an inference for an observation, so they stay, shown
  // only when they apply and costing nothing on the ordinary session.
  it('is silent for an ordinary single-account session', () => {
    expect(needsAccountNotice({ account_count: 1 })).toBe(false);
    expect(needsAccountNotice({})).toBe(false);
  });

  it('speaks up when the login address was inferred rather than observed', () => {
    expect(needsAccountNotice({ login_email_inferred: true, account_count: 1 })).toBe(true);
  });

  it('speaks up when the session spans more than one account', () => {
    expect(needsAccountNotice({ account_count: 2 })).toBe(true);
  });

  it('treats a missing account_count as one account, not as zero', () => {
    // A session with no count is the common case, not a session with no accounts.
    expect(needsAccountNotice({ login_email_inferred: false })).toBe(false);
  });
});
