/**
 * The time axis the session navigator draws on, and what goes on it.
 *
 * This was a view once — a separate Timeline mode with its own detail list. That mode is
 * gone: finding an interesting stretch of a run and then switching tabs to read it threw
 * away the place you had found, so the axis moved on top of the conversation instead (see
 * timeline-navigator.tsx) and the conversation itself became the detail. What is left here
 * is the part that was never about presentation: where things sit in time.
 */
import { dedupeKeys } from './session-utils';
import type { AssembledNode, ConversationItem, ToolPair } from './session-utils';

interface Bar {
  key: string;
  lane: string;
  startMs: number;
  endMs: number;
  openEnded: boolean;
  pair: ToolPair;
  /** uuid of the conversation item this call belongs to, for scrolling the body to it. */
  anchor?: string;
}

const MAIN_LANE = '메인 대화';

// A bar's key must survive a poll that inserts calls above it: a running sequence number
// would renumber every later bar and move the selection highlight to a different call.
// tool_use_id is unique per call; name@startTs is the fallback for orphan pairs, made
// unique below. Lane-scoped so the same call in an aside can't collide with the main thread.
const barKey = (lane: string, pair: ToolPair): string =>
  `${lane}:${pair.id ?? `${pair.name}@${pair.startTs}`}`;

/**
 * Tools whose "duration" is a person deciding, not a machine working.
 *
 * The axis measures a call from the assistant record to the tool_result record, and for
 * these two that span is the human reading a plan or answering a question. Drawing it as
 * activity gave one bar most of the width and squeezed every real call into a sliver --
 * and no idle threshold could reach it, because the collapse only ever applied BETWEEN
 * calls and this wait is inside one.
 *
 * Zeroing the duration puts the wait back where it belongs: the next real activity starts
 * later, so the stretch becomes an ordinary gap and gets collapsed and reported like any
 * other. The call itself still draws, as a point.
 *
 * This is a list of known names, not a rule. A tool that waits on a person without being
 * on it reads as a long call, which is what everything did before -- the failure mode
 * does not get worse for being unlisted.
 */
const HUMAN_GATED_TOOLS = new Set(['ExitPlanMode', 'AskUserQuestion']);

/**
 * What the body can be found by. An item's anchor id is its record uuid when it has
 * one, so links minted elsewhere (fork pointers, the Work Segments jump) keep
 * resolving; records without one (Codex) get a stable server hash. An aside has no
 * record of its own and is found by its node key. The body stamps the same value
 * into data-anchor.
 */
const anchorOf = (node: AssembledNode): string =>
  node.kind === 'item' ? node.item.anchorId : node.key;

const collectBars = (nodes: AssembledNode[]): Bar[] => {
  const bars: Bar[] = [];
  const seen = new Map<string, number>();
  const uniqueKey = (key: string): string => {
    const count = (seen.get(key) ?? 0) + 1;
    seen.set(key, count);
    return count === 1 ? key : `${key}#${count}`;
  };
  const addPairs = (lane: string, tools: ToolPair[], anchor?: string) => {
    for (const pair of tools) {
      const startMs = Date.parse(pair.startTs);
      if (Number.isNaN(startMs)) continue;
      const parsedEnd = pair.endTs ? Date.parse(pair.endTs) : NaN;
      // A human-gated call has no duration worth drawing; see HUMAN_GATED_TOOLS. It is
      // still a completed call, so it is not openEnded.
      const gated = HUMAN_GATED_TOOLS.has(pair.name);
      const endMs = gated ? startMs : parsedEnd;
      bars.push({
        key: uniqueKey(barKey(lane, pair)),
        lane,
        startMs,
        endMs: Number.isNaN(endMs) ? startMs : endMs,
        openEnded: !gated && Number.isNaN(parsedEnd),
        pair,
        anchor,
      });
    }
  };
  for (const node of nodes) {
    if (node.kind === 'item') addPairs(MAIN_LANE, node.item.tools, anchorOf(node));
    else if (node.kind === 'aside') for (const item of node.items) addPairs(node.label, item.tools, anchorOf(node));
  }
  return bars;
};


interface Interval {
  startMs: number;
  endMs: number;
}

/** A stretch of wall-clock the timeline does not draw, and how much it swallowed. */
interface CollapsedGap {
  atRatio: number;
  skippedMs: number;
}

interface TimeScale {
  toRatio: (ms: number) => number;
  fromRatio: (ratio: number) => number;
  gaps: CollapsedGap[];
  activeMs: number;
  totalMs: number;
}

/**
 * Idle time longer than this is collapsed. Below it a gap reads as pacing — the pause
 * between a call finishing and the next starting — and squeezing those out would make
 * a burst of quick calls look like one solid block.
 */
const IDLE_COLLAPSE_MS = 5_000;

/**
 * A single call longer than this has its middle collapsed too.
 *
 * IDLE_COLLAPSE_MS only ever looked BETWEEN calls, so one long-running call kept its full
 * width no matter what that threshold was set to -- a forty-minute agent run is one
 * interval, and an interval's own length was never a candidate for collapsing. That is
 * how a session with two minutes of real work still drew as two minutes of bars in an
 * hour of one bar.
 *
 * The head and tail stay at true scale so the call still reads as starting and finishing
 * where it did; only the featureless middle goes. What went is reported as a gap, the
 * same as any other skipped stretch -- nothing is dropped silently.
 */
const LONG_CALL_MS = 120_000;
/** How much of a collapsed call stays drawn at each end. */
const LONG_CALL_EDGE_MS = 30_000;

/**
 * The width an instant gets on the axis.
 *
 * A call whose result never arrived has no duration -- endMs equals startMs -- and a
 * session where none of them paired up has no duration at all. The axis divides the
 * width in proportion to activeMs, so activeMs of zero left nothing to divide:
 * toRatio answered 0 or 1 and nothing else, pinning the first event to the left
 * edge, the last to the right, and leaving the whole middle blank. That reads as
 * "the gap was not collapsed" when in fact it was, and what is left is not time --
 * it is nothing.
 *
 * Giving each instant a floor makes the events share the width like any others. It
 * is a drawing minimum, not a claim about how long anything took: totalMs still
 * reports the real wall clock.
 */
const MIN_EVENT_MS = 250;

/**
 * Builds the x-axis for the waterfall.
 *
 * A coding session is mostly waiting: the person reads, thinks, types; the agent sits
 * idle. Laying wall-clock directly on x gives all that width to nothing happening, and
 * the calls that are the point of the view get squeezed into slivers — an hour-long
 * session with two minutes of work is two minutes of bars in an hour of blank.
 *
 * So time runs at true scale inside activity and collapses between: every interval keeps
 * its real duration relative to the others, and each skipped stretch is reported so the
 * view can mark where it was. Nothing is hidden silently.
 */
const buildTimeScale = (intervals: Interval[]): TimeScale => {
  const sorted = [...intervals].filter(i => Number.isFinite(i.startMs)).sort((a, b) => a.startMs - b.startMs);
  if (sorted.length === 0) return { toRatio: () => 0, fromRatio: () => 0, gaps: [], activeMs: 0, totalMs: 0 };

  // Real wall clock, captured before the drawing minimum below inflates anything.
  const spanEndMs = sorted.reduce((max, i) => Math.max(max, i.endMs, i.startMs), sorted[0].startMs);

  // Merge overlaps first: two lanes running at once are one stretch of activity, not two.
  const merged: Interval[] = [];
  for (const interval of sorted) {
    const end = Math.max(interval.startMs, interval.endMs, interval.startMs + MIN_EVENT_MS);
    const last = merged[merged.length - 1];
    if (last && interval.startMs <= last.endMs + IDLE_COLLAPSE_MS) last.endMs = Math.max(last.endMs, end);
    else merged.push({ startMs: interval.startMs, endMs: end });
  }

  // Split anything too long to draw whole. From here on a "kept" piece is what actually
  // reaches the axis, and the holes -- between calls and inside them alike -- are gaps.
  const kept: Interval[] = [];
  for (const seg of merged) {
    if (seg.endMs - seg.startMs <= LONG_CALL_MS) {
      kept.push(seg);
      continue;
    }
    kept.push({ startMs: seg.startMs, endMs: seg.startMs + LONG_CALL_EDGE_MS });
    kept.push({ startMs: seg.endMs - LONG_CALL_EDGE_MS, endMs: seg.endMs });
  }

  // Each kept piece maps onto a slice of [0,1] proportional to its real duration.
  const activeMs = kept.reduce((sum, seg) => sum + (seg.endMs - seg.startMs), 0);
  const span = activeMs > 0 ? activeMs : 1;
  const segments: { startMs: number; endMs: number; offsetMs: number }[] = [];
  const gaps: CollapsedGap[] = [];
  let offset = 0;
  for (const [index, seg] of kept.entries()) {
    if (index > 0) {
      const skipped = seg.startMs - kept[index - 1].endMs;
      if (skipped > 0) gaps.push({ atRatio: offset / span, skippedMs: skipped });
    }
    segments.push({ ...seg, offsetMs: offset });
    offset += seg.endMs - seg.startMs;
  }

  const toRatio = (ms: number): number => {
    if (ms <= segments[0].startMs) return 0;
    const last = segments[segments.length - 1];
    if (ms >= last.endMs) return 1;
    for (const seg of segments) {
      if (ms < seg.startMs) return seg.offsetMs / span;  // inside a collapsed gap
      if (ms <= seg.endMs) return (seg.offsetMs + (ms - seg.startMs)) / span;
    }
    return 1;
  };

  // The inverse the track needs: a pointer sits at some fraction of the width and has to
  // resolve to the time drawn there. Without it, dragging would select against the old
  // linear axis while the bars sit on the collapsed one.
  const fromRatio = (ratio: number): number => {
    const target = Math.min(Math.max(ratio, 0), 1) * span;
    for (const seg of segments) {
      const segSpan = seg.endMs - seg.startMs;
      if (target <= seg.offsetMs + segSpan) return seg.startMs + Math.max(target - seg.offsetMs, 0);
    }
    return segments[segments.length - 1].endMs;
  };

  return { toRatio, fromRatio, gaps, activeMs, totalMs: spanEndMs - sorted[0].startMs };
};

interface TimelineMessage {
  key: string;
  lane: string;
  atMs: number;
  role: 'user' | 'assistant';
  text: string;
  /** Anchor of the node the item renders under, for scrolling the body to it. */
  anchor?: string;
}

/**
 * Conversation turns, placed on the same axis as the calls.
 *
 * The waterfall drew tool calls and nothing else, so a session showed that something ran
 * without showing what was asked for. A turn with no timestamp cannot be placed at all,
 * and an empty one exists only to carry tool results — both are dropped rather than
 * stacked at the origin.
 */
const collectMessages = (nodes: AssembledNode[]): TimelineMessage[] => {
  const messages: TimelineMessage[] = [];
  // The anchor comes from the node, not the item: an aside's items have no key of
  // their own, and the body renders them under the node they belong to.
  const add = (lane: string, item: ConversationItem, anchor: string) => {
    const atMs = item.ts ? Date.parse(item.ts) : NaN;
    if (Number.isNaN(atMs)) return;
    const text = item.command ? `/${item.command}` : item.text.trim();
    if (text === '') return;
    messages.push({ key: `${lane}:${item.anchorId}`, lane, atMs, role: item.role, text, anchor });
  };
  for (const node of nodes) {
    if (node.kind === 'item') add(MAIN_LANE, node.item, anchorOf(node));
    else if (node.kind === 'aside') for (const item of node.items) add(node.label, item, anchorOf(node));
  }
  // Anchor ids can collide (identical legacy records hash alike).
  return dedupeKeys(messages);
};


export { anchorOf, buildTimeScale, collectBars, collectMessages };
export type { TimelineMessage, TimeScale };
