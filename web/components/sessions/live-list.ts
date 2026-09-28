// Reconcile a polling-backed list against the order the user is currently looking at.
//
// One `useInfiniteQuery` stays the background truth; only the *display order* (an id
// array) is frozen. Values are never frozen — each displayed entry is looked up in the
// live page first, so tokens/cost/end_time keep refreshing while the rows stay put.
//
// Pure on purpose: the web app has no jsdom/RTL, so every rule worth verifying lives
// outside the component and is covered by live-list.test.ts.

interface ReconcileInput<T> {
  /** Display order the user is looking at. Empty = nothing committed yet. */
  committedIds: string[];
  /** Newest server truth, in server order (end_time DESC). */
  live: T[];
  /** Last seen value for ids that have since fallen out of `live`. */
  lastKnown: Map<string, T>;
  getId: (item: T) => string;
}

interface ReconcileResult<T> {
  /** Rows to render, in the frozen order, carrying the freshest known values. */
  displayed: T[];
  /** Ids that arrived above the frozen head — surfaced as a badge, not rendered. */
  pendingIds: string[];
  /** Order to persist now: committed order plus absorbed tail growth. */
  nextCommittedIds: string[];
}

/**
 * Rules:
 * 1. no committed order yet (first load / filter reset) -> commit `live` wholesale
 * 2. ids after the last committed anchor = tail growth from fetchNextPage -> absorbed
 * 3. any other id absent from the committed order = head arrival -> pending
 * 4. committed id missing from `live` -> kept in place, rendered from `lastKnown`
 * 5. no committed id survives in `live` (filter changed under us) -> treat as case 1
 */
const reconcileList = <T>({ committedIds, live, lastKnown, getId }: ReconcileInput<T>): ReconcileResult<T> => {
  const liveIds = live.map(getId);
  const liveById = new Map<string, T>();
  for (const item of live) liveById.set(getId(item), item);

  const render = (ids: string[]): T[] => {
    const out: T[] = [];
    for (const id of ids) {
      const value = liveById.get(id) ?? lastKnown.get(id);
      if (value !== undefined) out.push(value);
    }
    return out;
  };

  if (committedIds.length === 0) {
    return { displayed: render(liveIds), pendingIds: [], nextCommittedIds: liveIds };
  }

  const committed = new Set(committedIds);
  let anchorLast = -1;
  for (let i = 0; i < liveIds.length; i++) {
    if (committed.has(liveIds[i])) anchorLast = i;
  }
  if (anchorLast < 0) {
    return { displayed: render(liveIds), pendingIds: [], nextCommittedIds: liveIds };
  }

  const tail = liveIds.slice(anchorLast + 1).filter(id => !committed.has(id));
  const absorbed = new Set(tail);
  const pendingIds = liveIds.filter(id => !committed.has(id) && !absorbed.has(id));
  const nextCommittedIds = [...committedIds, ...tail];
  return { displayed: render(nextCommittedIds), pendingIds, nextCommittedIds };
};

/**
 * The badge click: adopt the live order wholesale, which drops every pending id back
 * into place. Kept separate from `reconcileList` because committing is a user action,
 * while reconcile runs on every poll and must not move rows on its own.
 */
const commitLiveOrder = <T>(live: T[], getId: (item: T) => string): string[] => live.map(getId);

export { reconcileList, commitLiveOrder };
export type { ReconcileInput, ReconcileResult };
