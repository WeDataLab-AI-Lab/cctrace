import { describe, it, expect } from 'vitest';
import { buildTimeScale, collectBars, collectMessages } from './timeline-scale';
import type { AssembledNode, ConversationItem, ToolPair } from './session-utils';

const pair = (id: string | undefined, name: string, startTs: string): ToolPair =>
  ({ id, name, input: '', result: '', isError: false, startTs });

const item = (key: string, tools: ToolPair[]): AssembledNode => ({
  kind: 'item',
  seam: 'main',
  key,
  item: { role: 'assistant', text: '', tools } as ConversationItem,
});

describe('collectBars', () => {
  it('keys bars by tool_use_id so an inserted bar leaves the others unchanged', () => {
    const before = collectBars([
      item('i:1', [pair('tu_a', 'Bash', '2026-01-01T00:00:00Z')]),
      item('i:3', [pair('tu_c', 'Read', '2026-01-01T00:00:02Z')]),
    ]);
    const after = collectBars([
      item('i:1', [pair('tu_a', 'Bash', '2026-01-01T00:00:00Z')]),
      item('i:2', [pair('tu_b', 'Edit', '2026-01-01T00:00:01Z')]),
      item('i:3', [pair('tu_c', 'Read', '2026-01-01T00:00:02Z')]),
    ]);
    expect(before.map(b => b.key)).toEqual(['메인 대화:tu_a', '메인 대화:tu_c']);
    expect(after.map(b => b.key)).toEqual(['메인 대화:tu_a', '메인 대화:tu_b', '메인 대화:tu_c']);
  });

  it('falls back to name@startTs when a pair carries no tool_use_id', () => {
    const bars = collectBars([item('i:1', [pair(undefined, 'Bash', '2026-01-01T00:00:00Z')])]);
    expect(bars[0].key).toBe('메인 대화:Bash@2026-01-01T00:00:00Z');
  });

  it('keeps fallback keys unique when two pairs collide on every keyed field', () => {
    const bars = collectBars([
      item('i:1', [pair(undefined, 'Bash', '2026-01-01T00:00:00Z'), pair(undefined, 'Bash', '2026-01-01T00:00:00Z')]),
    ]);
    expect(new Set(bars.map(b => b.key)).size).toBe(2);
  });

  it('scopes keys per lane so an aside cannot collide with the main thread', () => {
    const bars = collectBars([
      item('i:1', [pair('tu_a', 'Bash', '2026-01-01T00:00:00Z')]),
      {
        kind: 'aside',
        key: 'a:g1',
        label: '서브에이전트 x',
        items: [{ role: 'assistant', text: '', tools: [pair('tu_a', 'Bash', '2026-01-01T00:00:00Z')] } as ConversationItem],
      },
    ]);
    expect(bars.map(b => b.key)).toEqual(['메인 대화:tu_a', '서브에이전트 x:tu_a']);
  });
});

const msgItem = (key: string, role: 'user' | 'assistant', text: string, ts: string, tools: ToolPair[] = []): AssembledNode => ({
  kind: 'item',
  seam: 'main',
  key,
  item: { role, text, tools, ts } as ConversationItem,
});

// A session is mostly waiting: the person reads, thinks, types. Mapping wall-clock
// straight onto x means those gaps own most of the width and every actual call is
// squeezed into slivers. The scale keeps real time inside activity and collapses the
// gaps between, so the picture is of what happened rather than of how long nothing did.
describe('buildTimeScale', () => {
  const at = (iso: string) => Date.parse(iso);

  it('leaves a dense session alone', () => {
    const scale = buildTimeScale([
      { startMs: at('2026-01-01T00:00:00Z'), endMs: at('2026-01-01T00:00:10Z') },
      { startMs: at('2026-01-01T00:00:12Z'), endMs: at('2026-01-01T00:00:20Z') },
    ]);
    expect(scale.gaps).toEqual([]);
    // No collapsing, so a point halfway through the span sits halfway across.
    expect(scale.toRatio(at('2026-01-01T00:00:10Z'))).toBeCloseTo(0.5, 2);
  });

  it('collapses a long idle stretch', () => {
    const scale = buildTimeScale([
      { startMs: at('2026-01-01T00:00:00Z'), endMs: at('2026-01-01T00:00:10Z') },
      { startMs: at('2026-01-01T01:00:00Z'), endMs: at('2026-01-01T01:00:10Z') },
    ]);
    expect(scale.gaps).toHaveLength(1);
    expect(scale.gaps[0].skippedMs).toBe(3590_000);
    // Two equal 10s spans share the width evenly, and because the hour between them is
    // collapsed the end of the first and the start of the second land on the same point.
    // That coincidence is what collapsing means.
    expect(scale.toRatio(at('2026-01-01T00:00:10Z'))).toBeCloseTo(0.5, 5);
    expect(scale.toRatio(at('2026-01-01T01:00:00Z'))).toBeCloseTo(0.5, 5);
  });

  it('keeps order and bounds', () => {
    const scale = buildTimeScale([
      { startMs: at('2026-01-01T00:00:00Z'), endMs: at('2026-01-01T00:00:05Z') },
      { startMs: at('2026-01-01T02:00:00Z'), endMs: at('2026-01-01T02:00:05Z') },
    ]);
    expect(scale.toRatio(at('2026-01-01T00:00:00Z'))).toBe(0);
    expect(scale.toRatio(at('2026-01-01T02:00:05Z'))).toBe(1);
    // A timestamp inside a collapsed gap has no width of its own; it must still land
    // between its neighbours rather than jumping to an end.
    const inGap = scale.toRatio(at('2026-01-01T01:00:00Z'));
    expect(inGap).toBeGreaterThanOrEqual(scale.toRatio(at('2026-01-01T00:00:05Z')));
    expect(inGap).toBeLessThanOrEqual(scale.toRatio(at('2026-01-01T02:00:00Z')));
  });

  // The track maps a pointer's x back to a time, so the inverse has to agree with the
  // forward mapping — otherwise dragging selects a range that is not the one under the
  // cursor, and the mismatch only shows up as bars that refuse to highlight.
  it('round-trips a position back to its time', () => {
    const scale = buildTimeScale([
      { startMs: at('2026-01-01T00:00:00Z'), endMs: at('2026-01-01T00:00:10Z') },
      { startMs: at('2026-01-01T01:00:00Z'), endMs: at('2026-01-01T01:00:10Z') },
    ]);
    for (const iso of ['2026-01-01T00:00:00Z', '2026-01-01T00:00:07Z', '2026-01-01T01:00:03Z', '2026-01-01T01:00:10Z']) {
      expect(scale.fromRatio(scale.toRatio(at(iso)))).toBe(at(iso));
    }
  });

  it('survives a session with one instant of activity', () => {
    const scale = buildTimeScale([{ startMs: 1000, endMs: 1000 }]);
    expect(scale.toRatio(1000)).toBe(0);
    expect(Number.isFinite(scale.toRatio(2000))).toBe(true);
  });
});

// The waterfall showed tool calls only, so a session's turns were invisible: you could
// see that something ran without seeing what was asked for.
describe('collectMessages', () => {
  it('collects both roles with their text', () => {
    const msgs = collectMessages([
      msgItem('i:1', 'user', 'run the tests', '2026-01-01T00:00:00Z'),
      msgItem('i:2', 'assistant', 'running them now', '2026-01-01T00:00:03Z'),
    ]);
    expect(msgs.map(m => m.role)).toEqual(['user', 'assistant']);
    expect(msgs[0].text).toBe('run the tests');
    expect(msgs[0].lane).toBe('메인 대화');
  });

  it('drops turns with no timestamp — they cannot be placed on a time axis', () => {
    expect(collectMessages([msgItem('i:1', 'user', 'no ts', '')])).toEqual([]);
  });

  it('drops empty turns, which exist only to carry tool results', () => {
    expect(collectMessages([msgItem('i:1', 'assistant', '   ', '2026-01-01T00:00:00Z')])).toEqual([]);
  });

  it('labels an aside with its own lane so a subagent stays on its row', () => {
    const msgs = collectMessages([
      { kind: 'aside', label: 'subagent', key: 'a:1', items: [{ role: 'assistant', text: 'looking', tools: [], ts: '2026-01-01T00:00:01Z', anchorId: 'u-1' }] },
    ]);
    expect(msgs[0].lane).toBe('subagent');
  });
});

describe('long stretches that are not work', () => {
  const withEnd = (id: string, name: string, startTs: string, endTs: string): ToolPair =>
    ({ id, name, input: '', result: '', isError: false, startTs, endTs });

  it('draws a human-gated call as a point, so the wait becomes a gap instead of width', () => {
    // ExitPlanMode is open from the moment it is called until a person approves. Measured
    // as activity it hands most of the axis to someone reading.
    const [bar] = collectBars([
      item('i:1', [withEnd('tu_a', 'ExitPlanMode', '2026-01-01T00:00:00Z', '2026-01-01T00:20:00Z')]),
    ]);
    expect(bar.endMs).toBe(bar.startMs);
    expect(bar.openEnded).toBe(false);
  });

  it('leaves a machine call of the same length alone', () => {
    const [bar] = collectBars([
      item('i:1', [withEnd('tu_a', 'Bash', '2026-01-01T00:00:00Z', '2026-01-01T00:20:00Z')]),
    ]);
    expect(bar.endMs - bar.startMs).toBe(20 * 60_000);
  });

  it('collapses the middle of one long call and reports what it swallowed', () => {
    const start = Date.parse('2026-01-01T00:00:00Z');
    const scale = buildTimeScale([{ startMs: start, endMs: start + 20 * 60_000 }]);

    // Head and tail stay drawn, so the call still reads as starting and finishing where it
    // did; only the featureless middle goes.
    expect(scale.activeMs).toBe(60_000);
    expect(scale.gaps).toHaveLength(1);
    expect(scale.gaps[0].skippedMs).toBe(19 * 60_000);
    // The wall clock it covers is unchanged -- collapsing is a drawing decision, not a
    // claim that less time passed.
    expect(scale.totalMs).toBe(20 * 60_000);
  });

  it('leaves a call under the threshold whole', () => {
    const start = Date.parse('2026-01-01T00:00:00Z');
    const scale = buildTimeScale([{ startMs: start, endMs: start + 60_000 }]);
    expect(scale.activeMs).toBe(60_000);
    expect(scale.gaps).toHaveLength(0);
  });

  it('still maps a time inside the collapsed middle onto the axis', () => {
    const start = Date.parse('2026-01-01T00:00:00Z');
    const scale = buildTimeScale([{ startMs: start, endMs: start + 20 * 60_000 }]);
    // Ten minutes in is inside the hole. It has to resolve to the boundary rather than
    // running off the end, or clicking there would jump somewhere unrelated.
    const ratio = scale.toRatio(start + 10 * 60_000);
    expect(ratio).toBeGreaterThan(0);
    expect(ratio).toBeLessThan(1);
  });
});

describe('a session with no measurable activity', () => {
  it('spreads instants across the width instead of pinning them to the edges', () => {
    // Calls whose tool_result never paired have endMs === startMs. When none of them
    // paired, activeMs was 0, so toRatio answered only 0 or 1: first event hard left,
    // last hard right, nothing in between. The gap WAS collapsed -- the blank middle
    // was not time, it was nothing -- but it read as a failure to collapse.
    const t0 = Date.parse('2026-01-01T00:00:00Z');
    const t1 = t0 + 79 * 60_000;
    const scale = buildTimeScale([
      { startMs: t0, endMs: t0 },
      { startMs: t1, endMs: t1 },
    ]);

    expect(scale.activeMs).toBeGreaterThan(0);
    expect(scale.toRatio(t0)).toBe(0);
    expect(scale.toRatio(t1)).toBeCloseTo(0.5, 5);
    // And the marker sits where the skip is, not at the origin.
    expect(scale.gaps).toHaveLength(1);
    expect(scale.gaps[0].atRatio).toBeCloseTo(0.5, 5);
  });

  it('still reports the real wall clock, not the drawing minimum', () => {
    const t0 = Date.parse('2026-01-01T00:00:00Z');
    const t1 = t0 + 79 * 60_000;
    const scale = buildTimeScale([
      { startMs: t0, endMs: t0 },
      { startMs: t1, endMs: t1 },
    ]);
    // The floor exists so instants can be drawn; it must not inflate the session's
    // reported length.
    expect(scale.totalMs).toBe(79 * 60_000);
  });

  it('leaves a measurable call unchanged', () => {
    const t0 = Date.parse('2026-01-01T00:00:00Z');
    const scale = buildTimeScale([{ startMs: t0, endMs: t0 + 60_000 }]);
    expect(scale.activeMs).toBe(60_000);
  });
});

// Marks and bars must carry the anchor the body stamps into data-anchor, or clicking and
// dragging on the strip moves the conversation nowhere while the tint still paints.
describe('anchors', () => {
  const at = (ms: number) => new Date(ms).toISOString();

  it('anchors main-thread marks on the item anchor id', () => {
    const nodes: AssembledNode[] = [
      {
        kind: 'item', seam: 'main', key: 'i:h-1',
        item: { role: 'user', text: 'ask', ts: at(0), tools: [], anchorId: 'h-1' },
      },
      {
        kind: 'item', seam: 'main', key: 'i:u-2',
        item: { role: 'assistant', text: 'reply', ts: at(10_000), tools: [pair(undefined, 'Bash', at(10_000))], anchorId: 'u-2' },
      },
    ];
    expect(collectMessages(nodes).map(m => m.anchor)).toEqual(['h-1', 'u-2']);
    expect(collectBars(nodes).map(b => b.anchor)).toEqual(['u-2']);
  });

  // An aside renders collapsed under its node, so its items are only reachable through it.
  it('anchors aside marks on the aside node', () => {
    const nodes: AssembledNode[] = [{
      kind: 'aside', label: 'sub', key: 'a:1',
      items: [{ role: 'assistant', text: 'work', ts: at(0), tools: [pair('t1', 'Bash', at(0))], anchorId: 'u-9' }],
    }];
    expect(collectMessages(nodes).map(m => m.anchor)).toEqual(['a:1']);
    expect(collectBars(nodes).map(b => b.anchor)).toEqual(['a:1']);
  });

  it('keeps message keys unique when anchor ids collide', () => {
    const node = (key: string): AssembledNode => ({
      kind: 'item', seam: 'main', key,
      item: { role: 'user', text: 'same', ts: at(0), tools: [], anchorId: 'dup' },
    });
    const keys = collectMessages([node('i:dup'), node('i:dup#2')]).map(m => m.key);
    expect(new Set(keys).size).toBe(2);
  });
});
