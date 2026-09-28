import type { SessionRecord } from '@/lib/types';

// startTs = ts of the record that issued the tool_use (call time); endTs = ts of the
// record carrying the matching tool_result (completion time). endTs is absent when no
// result has landed yet (call still in flight) — the timeline renders those as open-ended.
// id = the tool_use_id this pair was matched on (absent for orphan tool_results, which
// have no tool_use to pair with). Kept for stable render keys — the pairing already
// knows it, so nothing new is parsed.
interface ToolPair { id?: string; name: string; input: string; result: string; isError: boolean; startTs: string; endTs?: string }
// ts = the record timestamp this item came from. anchorId = the record's view anchor_id
// (its uuid, or a server-computed stable hash when it has none); render keys and scroll
// anchors both use it.
interface ConversationItem { role: 'user' | 'assistant'; text: string; tools: ToolPair[]; toolUseIds?: string[]; command?: string; commandArgs?: string; compactSummary?: boolean; agentTask?: boolean; anchorId: string; ts?: string }

const fmt = (n?: number): string => {
  if (n == null || n === 0) return '-';
  return n >= 1_000_000 ? `${(n / 1_000_000).toFixed(1)}M` : n >= 1000 ? `${(n / 1000).toFixed(1)}K` : String(n);
};

const fmtCost = (n?: number): string => {
  if (n == null || n === 0) return '-';
  return `$${n.toFixed(4)}`;
};

// A session counts as "recently active" while its newest known timestamp is younger than
// this — shared by the session list's LIVE badge and the detail view's short-poll trigger,
// so both agree on what "live" means. The CLI sync daemon's own default poll interval is
// 30s (cmd/cctrace/sync.go), so anything shorter here just means we notice a synced update
// within one tick instead of waiting for the next visit/refocus — not true sub-second live.
const ACTIVE_WINDOW_MS = 5 * 60_000;
const isRecentlyActive = (ts?: string): boolean => {
  if (!ts) return false;
  const age = Date.now() - Date.parse(ts);
  return age >= 0 && age < ACTIVE_WINDOW_MS;
};

const projectLabel = (s: { project_name?: string; project_hash?: string }): string => {
  if (s.project_name) return s.project_name;
  if (!s.project_hash) return '';
  // Hash is path-based: -Users-username-Documents-GitHub-project-name
  const prefixes = [
    /^-(?:Users|home)-[^-]+-Documents-GitHub-/,
    /^-(?:Users|home)-[^-]+-Documents-/,
    /^-(?:Users|home)-[^-]+-/,
  ];
  for (const re of prefixes) {
    const m = s.project_hash.match(re);
    if (m && m[0].length < s.project_hash.length) return s.project_hash.slice(m[0].length);
  }
  return s.project_hash;
};

// Renders the server-built view only (internal/sessionview): every agent runtime's raw
// layout is already normalized there, so nothing here reads `raw`. A record without a
// view (a server that predates it) is treated as hidden.
const buildConversation = (records: SessionRecord[]): ConversationItem[] => {
  // Build tool call id → result map from every record's tool_results
  const resultMap = new Map<string, { content: string; isError: boolean; ts: string }>();
  // Inferred results (recovered from SDK-harness prose) are a fallback source only — a
  // genuine result for the same id always wins regardless of record order, so they are
  // collected first and overwritten by the rest.
  const collectResults = (inferred: boolean) => {
    for (const r of records) {
      for (const tr of r.view?.tool_results ?? []) {
        if (!tr.id || !!tr.inferred !== inferred) continue;
        resultMap.set(tr.id, { content: tr.output ?? '', isError: !!tr.is_error, ts: r.ts });
      }
    }
  };
  collectResults(true);
  collectResults(false);

  // Every call id in the session, gathered up front: records sharing a ts have no
  // defined order (Codex rows carry no uuid), so a result can come before its call and
  // must not be taken for an orphan.
  const callIds = new Set<string>();
  for (const r of records) {
    const v = r.view;
    if (v?.kind !== 'tool_call' && !(v?.kind === 'message' && v.role === 'assistant')) continue;
    for (const c of v.tool_calls ?? []) if (c.id) callIds.add(c.id);
  }

  const items: ConversationItem[] = [];

  for (const r of records) {
    const v = r.view;
    // reasoning has no UI yet; hidden is scaffolding/bookkeeping.
    if (!v || v.kind === 'hidden' || v.kind === 'reasoning') continue;
    const base = { anchorId: v.anchor_id, ts: r.ts };
    const text = v.text ?? '';

    // A Codex tool_call record is an assistant turn with calls and no text.
    if (v.kind === 'tool_call' || (v.kind === 'message' && v.role === 'assistant')) {
      const calls = v.tool_calls ?? [];
      const pairs: ToolPair[] = calls.map(c => {
        const id = c.id || '';
        const res = resultMap.get(id);
        return {
          id: id || undefined,
          name: c.name || 'tool',
          input: c.input ?? '',
          result: res?.content || '',
          isError: res?.isError || false,
          startTs: r.ts,
          endTs: res?.ts,
        };
      });
      if (text || pairs.length > 0) {
        const toolUseIds = calls.map(c => c.id || '').filter(Boolean);
        items.push({ role: 'assistant', text, tools: pairs, toolUseIds, ...base });
      }
      continue;
    }

    if (v.compact_summary) {
      // The /compact context summary — shown as a collapsible block, not a user turn.
      items.push({ role: 'user', text, tools: [], compactSummary: true, ...base });
      continue;
    }
    if (v.command) {
      items.push({ role: 'user', text: '', tools: [], command: v.command, commandArgs: v.command_args, ...base });
      continue;
    }
    if (text) {
      // agentTask: a Codex subagent's instruction from its parent agent — a user-role
      // turn distinguished by the flag (see ConversationItem role/flag convention).
      items.push({ role: 'user', text, tools: [], agentTask: v.agent_task, ...base });
      continue;
    }
    // tool_results are merged into the assistant items; only genuine orphans with no
    // matching tool call surface on their own. An inferred result may name a call from
    // another session, so it never does.
    const orphans: ToolPair[] = (v.tool_results ?? [])
      .filter(tr => tr.id && !tr.inferred && !callIds.has(tr.id))
      .map(tr => ({ id: tr.id, name: 'result', input: '', result: tr.output ?? '', isError: !!tr.is_error, startTs: r.ts, endTs: r.ts }));
    if (orphans.length > 0) items.push({ role: 'user', text: '', tools: orphans, ...base });
  }
  return items;
};

// --- Lineage: individual (per-file) and assembled (merged thread) views ---

interface FileGroup {
  key: string;
  sourceFile: string;
  sessionId: string;
  agentId?: string;
  isSidechain: boolean;
  records: SessionRecord[];
}

// Seam encodes the lineage relationship at a segment change:
//  aside = nested side thread in the same session (/btw, Task); fork = jump to a
//  different session (/branch, /clear); main = back to the primary thread.
type SeamKind = 'aside' | 'fork' | 'main' | 'compaction';

// key: a render key that survives insertion. buildAssembled splices asides *between*
// existing nodes, so an array index would shift every later node on the next poll and
// reattach open AsideGroup / CompactSummary state to the wrong row.
interface AssembledBoundary { kind: 'boundary'; seam: SeamKind; label: string; key: string }
interface AssembledItem { kind: 'item'; seam: SeamKind; item: ConversationItem; key: string }
// Async side threads (btw/subagent) collapse into one foldable chip at their spawn
// point, so the main thread reads cleanly and the parallel work stays out of the way.
interface AssembledAside { kind: 'aside'; label: string; items: ConversationItem[]; key: string }
type AssembledNode = AssembledBoundary | AssembledItem | AssembledAside;

// A record's file identity: same session_id + same source_file = same jsonl file.
const fileKey = (r: SessionRecord): string => `${r.session_id ?? ''}\u0000${r.source_file ?? ''}`;

// --- Render keys ---
//
// The server gives every record a view anchor_id (its uuid, or a stable hash of fields
// that do not change between reads), so keys come from the record set alone and stay
// put when an aside is spliced in above them. Only a record from a server without views
// lacks one; the raw view keys that by ts + the index inside its file group.

/** Key for a record rendered inside a file group (raw view). */
const recordKey = (r: SessionRecord, groupKey: string, i: number): string =>
  r.view ? `r:${r.view.anchor_id}` : `r:${groupKey}\u0000${r.ts}#${i}`;

/** A file group's records with render keys; anchors can collide, so they are deduped. */
const keyRecords = (records: SessionRecord[], groupKey: string): { key: string; record: SessionRecord }[] =>
  dedupeKeys(records.map((record, i) => ({ key: recordKey(record, groupKey, i), record })));

/** Key for a conversation item. Anchors can collide, so lists go through dedupeKeys. */
const conversationItemKey = (item: ConversationItem): string => `i:${item.anchorId}`;

// Last-resort uniqueness guard. Legacy sessions can hold records that collide on every
// field we key by; React would then warn and reuse state across rows. Suffixing keeps
// the stable keys untouched while making the collisions distinct.
const dedupeKeys = <T extends { key: string }>(nodes: T[]): T[] => {
  const seen = new Map<string, number>();
  return nodes.map(n => {
    const count = (seen.get(n.key) ?? 0) + 1;
    seen.set(n.key, count);
    return count === 1 ? n : { ...n, key: `${n.key}#${count}` };
  });
};

// Legacy = collected before lineage fields existed (no source_file on any record).
// A session is legacy when it holds records but none carry source_file — the field
// the enrichment added. No records at all is a different state (otel-only session,
// or JSONL not synced yet); labelling that "pre-update" tells the user the wrong
// story and hides the real one, which the empty-state message already conveys.
/**
 * Whether the session predates the source_file enrichment.
 *
 * `hasEnriched` is the server's whole-session aggregate (bool_or over every record) and is
 * authoritative when present: the records handed in here are only the pages loaded so far,
 * so judging from them alone mislabels a session whose enriched records sit further down.
 * The record heuristic stays as the fallback for callers with no overview to consult.
 */
const isLegacySession = (records: SessionRecord[], hasEnriched?: boolean): boolean => {
  if (hasEnriched !== undefined) return !hasEnriched;
  return records.length > 0 && !records.some(r => !!r.source_file);
};

// A session can hold two copies of the same records: a legacy copy (pre-update, no
// source_file/uuid) and an enriched copy (post-update). This happens when a session
// already stored by an old client is later re-synced by an enriched one — the unique
// key includes uuid, so the empty-uuid legacy rows don't collide and both persist.
// Collapse to the enriched copy for display: drop a legacy record only when an enriched
// record shares its identity, and drop exact uuid duplicates. Identity is the server's
// view.dedupe_key, built from what enrichment never touches (ts, record_type, message
// body) — not the view content, which depends on enrichment-only columns (prompt_source,
// is_meta, is_compact_summary) and so differs between the two copies. A distinct legacy
// record that merely shares a timestamp is never dropped (audit rule: collapse
// duplicates, never lose data), and a record without a key is never merged.
// Fully-legacy/fully-enriched sessions pass through untouched.
const recordSig = (r: SessionRecord): string | undefined => r.view?.dedupe_key || undefined;
const dedupeRecords = (records: SessionRecord[]): SessionRecord[] => {
  const enrichedSigs = new Set<string>();
  for (const r of records) {
    const sig = recordSig(r);
    if (r.source_file && sig !== undefined) enrichedSigs.add(sig);
  }
  const seenUuid = new Set<string>();
  const out: SessionRecord[] = [];
  for (const r of records) {
    const sig = recordSig(r);
    if (!r.source_file && sig !== undefined && enrichedSigs.has(sig)) continue;
    if (r.uuid) {
      if (seenUuid.has(r.uuid)) continue;
      seenUuid.add(r.uuid);
    }
    out.push(r);
  }
  return out;
};

const shortId = (s?: string): string => (s ? s.slice(0, 8) : '');

const segmentLabel = (g: FileGroup, seam: SeamKind): string => {
  if (seam === 'aside') {
    if ((g.agentId ?? '').startsWith('aside_question')) return '곁질문';
    return `서브에이전트${g.agentId ? ` ${g.agentId.slice(0, 12)}` : ''}`;
  }
  if (seam === 'fork') return `분기 ${shortId(g.sessionId)}`;
  return '메인 대화';
};

// Individual view: one block per distinct jsonl file, records in original order.
const groupFilesByFile = (records: SessionRecord[]): FileGroup[] => {
  const byKey = new Map<string, FileGroup>();
  const order: string[] = [];
  for (const r of records) {
    const key = fileKey(r);
    let g = byKey.get(key);
    if (!g) {
      g = { key, sourceFile: r.source_file ?? '', sessionId: r.session_id ?? '', agentId: r.agent_id, isSidechain: !!r.is_sidechain, records: [] };
      byKey.set(key, g);
      order.push(key);
    }
    g.records.push(r);
  }
  return order.flatMap((key) => {
    const group = byKey.get(key);
    return group ? [group] : [];
  });
};

interface PendingAside { label: string; items: ConversationItem[]; anchor: string; ts: string; key: string }

// Assembled view: the main thread flows in ts order; each async side thread
// (btw/subagent) collapses into a chip anchored at its spawn point — the main
// tool_use that launched it (tool_use_id), falling back to ts position.
const buildAssembled = (records: SessionRecord[]): AssembledNode[] => {
  // Split the main thread from async side threads, grouping each side thread by file
  // NON-contiguously: a btw / parallel subagent can interleave with the main thread in
  // ts order, so a contiguous run would split one file into several chips and straddle
  // a tool_use/tool_result pair. Building the whole main thread in one buildConversation
  // call keeps every pair matched regardless of interleaving.
  const mainItems = buildConversation(records.filter(r => !r.is_sidechain));

  const asides: PendingAside[] = [];
  for (const g of groupFilesByFile(records.filter(r => r.is_sidechain))) {
    const items = buildConversation(g.records);
    if (items.length === 0) continue;
    asides.push({ label: segmentLabel(g, 'aside'), items, anchor: g.records[0]?.tool_use_id ?? '', ts: g.records[0]?.ts ?? '', key: g.key });
  }

  // Attach each aside after the main item that spawned it (tool_use_id match), else
  // after the last main item at or before its spawn ts. Index -1 = before all.
  const attach = new Map<number, PendingAside[]>();
  const add = (idx: number, a: PendingAside) => attach.set(idx, [...(attach.get(idx) ?? []), a]);
  for (const a of asides) {
    let idx = a.anchor ? mainItems.findIndex(m => (m.toolUseIds ?? []).includes(a.anchor)) : -1;
    if (idx < 0) {
      for (let k = 0; k < mainItems.length; k++) {
        const t = mainItems[k].ts ?? '';
        if (t && t <= a.ts) idx = k;
      }
    }
    add(idx, a);
  }

  const nodes: AssembledNode[] = [];
  const emit = (idx: number) => {
    for (const a of attach.get(idx) ?? []) nodes.push({ kind: 'aside', label: a.label, items: a.items, key: `a:${a.key}` });
  };
  emit(-1);
  for (let k = 0; k < mainItems.length; k++) {
    nodes.push({ kind: 'item', seam: 'main', item: mainItems[k], key: conversationItemKey(mainItems[k]) });
    emit(k);
  }
  return dedupeKeys(nodes);
};

/** A turn the person actually typed, with the anchor that scrolls back to it. */
interface UserPrompt {
  anchorId: string;
  key: string;
  text: string;
  command?: string;
  commandArgs?: string;
  ts?: string;
}

/**
 * Pulls the person's own turns out of an assembled conversation.
 *
 * Several things carry role 'user' without a person having written them — the summary a
 * compaction leaves behind, the instruction an agent hands its subagent, and the records
 * that exist only to carry tool results. They are user-role because that is how the
 * transcript encodes them, not because someone typed them, and listing them would bury
 * the prompts this view exists to surface.
 *
 * Asides are skipped for the same reason: a subagent's internal turns are not the
 * person's, and the aside is collapsed in the main feed anyway, so an anchor pointing
 * inside one would have nowhere to scroll.
 */
const collectUserPrompts = (nodes: AssembledNode[]): UserPrompt[] => {
  const prompts: UserPrompt[] = [];
  for (const node of nodes) {
    if (node.kind !== 'item') continue;
    const { item } = node;
    if (item.role !== 'user' || item.compactSummary || item.agentTask) continue;
    if (item.text.trim() === '' && !item.command) continue;
    prompts.push({
      anchorId: item.anchorId,
      key: node.key,
      text: item.text,
      command: item.command,
      commandArgs: item.commandArgs,
      ts: item.ts,
    });
  }
  return prompts;
};

/**
 * The span a session covered, not the moment it began.
 *
 * The list showed start_time alone, which answers "when did this start" and leaves
 * "is this the twenty-minute one or the six-hour one" to the detail pane. Both ends
 * are already on the row's data, so the list can answer it.
 *
 * The day is repeated on the right only when the session crossed midnight -- printing
 * it twice for the usual case is noise, and its absence is what marks the unusual one.
 */
const fmtSessionPeriod = (startTime: string, endTime?: string): string => {
  const start = new Date(startTime);
  if (Number.isNaN(start.getTime())) return '';

  const day = (d: Date) => d.toLocaleString(undefined, { month: '2-digit', day: '2-digit' });
  const clock = (d: Date) => d.toLocaleString(undefined, { hour: '2-digit', minute: '2-digit' });
  const startLabel = `${day(start)} ${clock(start)}`;

  const end = endTime ? new Date(endTime) : null;
  // A session still running, or one whose end never made it into the record, has no
  // span to show. Inventing one from "now" would make the row change every render.
  if (!end || Number.isNaN(end.getTime()) || end.getTime() <= start.getTime()) return startLabel;

  const sameDay = day(end) === day(start);
  return `${startLabel} – ${sameDay ? clock(end) : `${day(end)} ${clock(end)}`}`;
};


/**
 * The first conversation item at or after `ts`, as an anchor id the scroll machinery
 * can anchor on.
 *
 * A Work Segment knows when it happened, not which message began it, so the link
 * carries an instant. Resolving it to an anchor id here keeps one scroll-and-highlight
 * path rather than teaching that machinery a second kind of anchor (#434).
 *
 * A segment whose window opens before anything loaded lands on the first item,
 * which is what "at or after" should mean. One whose window opens past the end
 * resolves to nothing and the view stays where it is -- better than scrolling
 * somewhere arbitrary. Items without a timestamp are skipped rather than treated
 * as time zero, which would make them swallow every anchor.
 */
const anchorAtOrAfter = (nodes: AssembledNode[], ts?: string): string | undefined => {
  if (!ts) return undefined;
  for (const n of nodes) {
    if (n.kind !== 'item') continue;
    if (n.item.ts && n.item.ts >= ts) return n.item.anchorId;
  }
  return undefined;
};


/**
 * Whether the session header must warn about how its account was determined.
 *
 * The header is an identity bar: everything the left list already shows was taken
 * out of it. These two survive because they are not values but caveats -- the login
 * address was deduced from surrounding history rather than observed, or the session
 * touched more than one account and the one displayed is a representative. Dropping
 * them with the rest would let a reader take an inference for an observation.
 */
const needsAccountNotice = (s: { login_email_inferred?: boolean; account_count?: number }): boolean =>
  !!s.login_email_inferred || (s.account_count ?? 1) > 1;

export type { ToolPair, ConversationItem, FileGroup, AssembledNode, UserPrompt };
export { recordKey, keyRecords, conversationItemKey, dedupeKeys, fmt, fmtCost, fmtSessionPeriod, needsAccountNotice, projectLabel, buildConversation, buildAssembled, collectUserPrompts, groupFilesByFile, isLegacySession, dedupeRecords, isRecentlyActive, anchorAtOrAfter };
