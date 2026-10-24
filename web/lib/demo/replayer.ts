import { compute, computable } from './compute';
import type { Facts, QuotaSample } from './compute';
import { replayKey } from './replay-key';
import type { ReplayKey } from './replay-key';

// The static demo answers the dashboard's API calls from responses recorded
// against a server seeded with a fictitious team (cmd/demo-seed). This module is
// the part that needs no browser: matching a request to a recording, or failing
// that computing it, and deciding what to answer.

interface RecordedEntry {
  key: string;
  status: number;
  body: string; // content hash; the file is bodies/<hash>.json
  headers?: Record<string, string>;
}

interface Candidate {
  offsets: number[];
  entry: RecordedEntry;
}

interface ShardIndex {
  exact: Map<string, RecordedEntry>;
  near: Map<string, Candidate[]>;
}

// Where the recordings come from: the browser runtime fetches them, tests hand
// them over.
interface ReplaySource {
  now: number;
  // shard returns the recordings of one endpoint, or null when it has none.
  shard(name: string): Promise<ShardIndex | null>;
  facts(): Promise<Facts>;
  quota(): Promise<QuotaSample[]>;
  // body returns a recorded response body by its content hash.
  body(hash: string): Promise<string>;
}

type ReplayAnswer =
  | { kind: 'recorded'; entry: RecordedEntry }
  | { kind: 'computed'; body: unknown }
  | { kind: 'read-only' }
  | { kind: 'refresh' }
  | { kind: 'missing'; key: string };

const CLOCK_VALUE = /=@(-?\d+)m/g;

const indexEntries = (entries: RecordedEntry[]): ShardIndex => {
  const exact = new Map<string, RecordedEntry>();
  const near = new Map<string, Candidate[]>();
  for (const entry of entries) {
    exact.set(entry.key, entry);
    // A recorded key already spells its clock values as relative minutes.
    const offsets = Array.from(entry.key.matchAll(CLOCK_VALUE), (m) => Number(m[1]));
    if (offsets.length === 0) continue;
    const template = entry.key.replace(CLOCK_VALUE, '=@');
    near.set(template, [...(near.get(template) ?? []), { offsets, entry }]);
  }
  return { exact, near };
};

// The clock is frozen, so a window measured back from "now" matches exactly. What
// does not is a range the browser aligns to its own calendar -- a week from Monday
// 00:00, say -- in a time zone other than the recording's. Such a range moves
// whole: every end by the same amount, less than a day. A range moved further,
// or by different amounts, is a different range (the previous week, a zoom) and
// is not lent this one.
const SHIFT_LIMIT = 24 * 60;
const DRIFT = 2;

// distance is how far the requested clock values are from a recording's, or null
// when they are not the same request.
const distance = (requested: number[], recorded: number[]): number | null => {
  const deltas = requested.map((value, i) => value - recorded[i]);
  const spread = Math.max(...deltas) - Math.min(...deltas);
  if (deltas.length > 1 && spread <= DRIFT && Math.abs(deltas[0]) <= SHIFT_LIMIT) return Math.abs(deltas[0]);
  return deltas.every((d) => Math.abs(d) <= DRIFT) ? Math.max(...deltas.map(Math.abs)) : null;
};

const lookup = (index: ShardIndex, key: ReplayKey): RecordedEntry | null => {
  const hit = index.exact.get(key.key);
  if (hit) return hit;
  let best: RecordedEntry | null = null;
  let bestDistance = Infinity;
  for (const c of index.near.get(key.template) ?? []) {
    if (c.offsets.length !== key.offsets.length) continue;
    const d = distance(key.offsets, c.offsets);
    if (d !== null && d < bestDistance) {
      best = c.entry;
      bestDistance = d;
    }
  }
  return best;
};

const LOOPBACK = new Set(['127.0.0.1', 'localhost', '[::1]']);

// routeOf sorts a fetch the page makes: the dashboard's own API is answered from
// the recordings, a cctrace daemon on the visitor's machine -- another origin on a
// loopback address -- is reported unreachable, and anything else (the page's
// own assets) goes to the network. Same-origin comes first: a demo served from
// a loopback address is still the page's own origin.
const routeOf = (url: URL, pageOrigin: string): 'api' | 'blocked' | 'network' => {
  if (url.origin === pageOrigin) return url.pathname.startsWith('/api/') ? 'api' : 'network';
  return LOOPBACK.has(url.hostname) ? 'blocked' : 'network';
};

// answer decides what the demo returns for one API request.
const answer = async (source: ReplaySource, method: string, url: URL): Promise<ReplayAnswer> => {
  const verb = method.toUpperCase();
  if (verb !== 'GET') {
    // The session stays signed in: a refresh simply succeeds.
    if (url.pathname === '/api/auth/refresh') return { kind: 'refresh' };
    return { kind: 'read-only' };
  }
  const key = replayKey(verb, url.pathname, url.searchParams, source.now);
  const index = await source.shard(shardOf(url.pathname));
  const hit = index ? lookup(index, key) : null;
  if (hit) return { kind: 'recorded', entry: hit };
  if (computable(url.pathname, url.searchParams)) {
    const quota = url.pathname === '/api/quota-samples' ? await source.quota() : [];
    return { kind: 'computed', body: compute(url.pathname, url.searchParams, await source.facts(), quota, source.now) };
  }
  const unscoped = withoutSessionScope(url);
  if (unscoped && index) {
    const plain = lookup(index, replayKey(verb, unscoped.pathname, unscoped.searchParams, source.now));
    if (plain) return { kind: 'recorded', entry: plain };
  }
  const page = index ? await conversationPage(source, index, unscoped ?? url) : null;
  if (page) return { kind: 'computed', body: page };
  const latest = index ? latestWindow(source, index, unscoped ?? url) : null;
  if (latest) return { kind: 'recorded', entry: latest };
  return { kind: 'missing', key: key.key };
};

// The parameters that place a conversation window at a request, a turn or a
// time rather than at an offset. Those windows are not recorded.
const PLACED = ['start', 'anchor', 'anchor_ts', 'turn_key', 'direction', 'source_file'];

// latestWindow opens a conversation placed at a message, a turn or a time (a
// reader reopening the session where it last read, or a link into it) at the
// session's latest request, which is recorded. Without it the whole
// conversation turns into "the selected message was not found". Stepping to an
// adjacent request (direction) is left unanswered: the reader keeps its window.
const latestWindow = (source: ReplaySource, index: ShardIndex, url: URL): RecordedEntry | null => {
  const params = url.searchParams;
  if (url.pathname !== '/api/sessions' || params.get('view') !== 'conversation' || params.get('direction')) return null;
  if (!['anchor', 'anchor_ts', 'turn_key'].some((name) => params.get(name))) return null;
  const latest = new URL(url);
  for (const name of PLACED) latest.searchParams.delete(name);
  latest.searchParams.set('start', 'latest-request');
  latest.searchParams.set('offset', '0');
  return lookup(index, replayKey('GET', latest.pathname, latest.searchParams, source.now));
};

// conversationPage answers an unrecorded offset of a conversation the way the
// server pages it, from the recorded first page when that holds the whole
// conversation (it is shorter than its limit). A recently active session polls
// its pages, and the poll can ask for one past the end, which is empty.
const conversationPage = async (source: ReplaySource, index: ShardIndex, url: URL): Promise<unknown[] | null> => {
  const params = url.searchParams;
  if (url.pathname !== '/api/sessions' || params.get('view') !== 'conversation' || PLACED.some((name) => params.get(name))) return null;
  const offset = Number(params.get('offset') ?? 0);
  const limit = Number(params.get('limit') ?? 1000);
  if (!Number.isInteger(offset) || offset <= 0 || !Number.isInteger(limit) || limit <= 0) return null;
  const first = new URL(url);
  first.searchParams.set('offset', '0');
  const entry = lookup(index, replayKey('GET', first.pathname, first.searchParams, source.now));
  if (!entry || entry.status !== 200) return null;
  const records: unknown = JSON.parse(await source.body(entry.body));
  if (!Array.isArray(records) || records.length >= limit) return null;
  return records.slice(offset, offset + limit);
};

// An admin who picked an account opens a session with that account as scope
// (login_email, profile_email, user_id). The scope narrows a session that ran
// under several accounts to one; every session in the demo ran under one, so the
// unscoped recording is the same answer.
const SESSION_SCOPE = ['login_email', 'profile_email', 'user_id'];

const withoutSessionScope = (url: URL): URL | null => {
  if (url.pathname !== '/api/sessions' && !url.pathname.startsWith('/api/sessions/')) return null;
  if (!SESSION_SCOPE.some((name) => url.searchParams.has(name))) return null;
  const plain = new URL(url);
  for (const name of SESSION_SCOPE) plain.searchParams.delete(name);
  return plain;
};

const ID_SEGMENT = /^([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9]+)$/;

// shardOf names the manifest file holding a path's recordings; it must equal
// shardOf in cmd/demo-seed/record.go (testdata/replay-shards.json), which says
// why the name ends in ".shard.json".
const shardOf = (path: string): string =>
  `${path.replace(/^\/+|\/+$/g, '').split('/').map((seg) => (ID_SEGMENT.test(seg) ? '_' : seg)).join('.')}.shard.json`;

export { answer, indexEntries, routeOf, shardOf };
export type { RecordedEntry, ReplayAnswer, ReplaySource, ShardIndex };
