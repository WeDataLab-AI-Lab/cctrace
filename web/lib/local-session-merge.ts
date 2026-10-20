import type {
  LocalSessionOverview,
  LocalSessionResult,
  SignedInOwnerScope,
  SourceAwareSessionList,
  SourceAwareSessionRow,
} from './local-session-types';
import type { SessionOverview } from './types';

/** Length-delimited JSON tuples keep IDs opaque, including separators inside IDs. */
export const localSessionSelectionKey = (row: LocalSessionOverview): string =>
  `local:${JSON.stringify([row.profile_fingerprint, row.agent, row.canonical_id])}`;

export const serverSessionSelectionKey = (row: SessionOverview): string =>
  `server:${JSON.stringify([row.user_id ?? '', row.profile_email, row.agent, row.session_id])}`;

/** A matching ID is only a grouping hint, never proof of common ownership or identity. */
export const sameIdGroupingCandidates = (
  localRows: readonly LocalSessionOverview[],
  serverRows: readonly SessionOverview[],
  owner?: SignedInOwnerScope,
): ReadonlyMap<string, string> => {
  const candidates = new Map<string, string>();
  if (!owner?.user_id) return candidates;
  const byIdentity = new Map<string, LocalSessionOverview[]>();
  for (const local of localRows) {
    if (local.native_session_id === null) continue;
    const identity = JSON.stringify([local.agent, local.native_session_id]);
    byIdentity.set(identity, [...(byIdentity.get(identity) ?? []), local]);
  }
  for (const locals of byIdentity.values()) {
    if (locals.length !== 1 || locals[0].server_match) continue;
    const local = locals[0];
    const matches = serverRows.filter((server) => server.user_id === owner.user_id &&
      server.agent === local.agent && server.session_id === local.native_session_id);
    if (matches.length !== 1) continue;
    candidates.set(localSessionSelectionKey(local), serverSessionSelectionKey(matches[0]));
  }
  return candidates;
};

/** Keep both source buttons; grouping changes presentation only. */
export const groupSameIdRows = (
  rows: readonly SourceAwareSessionRow[],
  candidates: ReadonlyMap<string, string>,
  grouped: ReadonlySet<string>,
): SourceAwareSessionRow[][] => {
  const byKey = new Map(rows.map((row) => [row.selection_key, row]));
  const pairedServers = new Set<string>();
  for (const row of rows) {
    if (row.source === 'local' && grouped.has(row.selection_key)) {
      const serverKey = candidates.get(row.selection_key);
      if (serverKey && byKey.get(serverKey)?.source === 'server') pairedServers.add(serverKey);
    }
  }
  return rows.flatMap((row) => {
    if (row.source === 'server' && pairedServers.has(row.selection_key)) return [];
    const serverKey = row.source === 'local' && grouped.has(row.selection_key) ? candidates.get(row.selection_key) : undefined;
    const server = serverKey ? byKey.get(serverKey) : undefined;
    return [server?.source === 'server' ? [row, server] : [row]];
  });
};

const verifiedServerMatch = (
  local: LocalSessionOverview,
  server: SessionOverview,
  owner: SignedInOwnerScope,
): boolean => {
  const evidence = local.server_match;
  if (!evidence || !owner.user_id || !server.user_id) return false;
  if (
    evidence.owner_user_id !== owner.user_id ||
    server.user_id !== owner.user_id ||
    evidence.profile_fingerprint !== local.profile_fingerprint ||
    evidence.server_profile_email !== server.profile_email ||
    evidence.agent !== local.agent ||
    server.agent !== local.agent
  ) return false;

  return evidence.kind === 'server_id'
    ? evidence.server_session_id === server.session_id
    : local.native_session_id !== null && evidence.native_session_id === local.native_session_id && local.native_session_id === server.session_id;
};

/**
 * Never infer ownership from a native session ID alone. A local source may be
 * unavailable while the selected server row remains accessible and vice versa.
 */
export const mergeLocalSessionOverviews = (
  localResult: LocalSessionResult,
  serverRows: readonly SessionOverview[],
  owner?: SignedInOwnerScope,
): SourceAwareSessionList => {
  if (localResult.status !== 'ready' && localResult.status !== 'unreachable') {
    return { status: localResult.status };
  }

  const serverKeys = new Set<string>();
  for (const row of serverRows) {
    const key = serverSessionSelectionKey(row);
    if (serverKeys.has(key)) throw new Error('Duplicate server session identity');
    serverKeys.add(key);
  }

  if (localResult.status === 'unreachable') {
    return {
      status: 'unreachable',
      rows: serverRows.map((server) => ({ source: 'server', selection_key: serverSessionSelectionKey(server), server })),
    };
  }

  const localKeys = new Set<string>();
  const matchedServerKeys = new Set<string>();
  const rows: SourceAwareSessionList & { status: 'ready' } = { status: 'ready', rows: [] };
  for (const local of localResult.sessions) {
    const key = localSessionSelectionKey(local);
    if (!local.canonical_id || !local.profile_fingerprint || !local.agent || localKeys.has(key)) {
      throw new Error('Invalid or duplicate local session identity');
    }
    localKeys.add(key);
    const matches = owner ? serverRows.filter((server) => verifiedServerMatch(local, server, owner)) : [];
    if (matches.length > 1) throw new Error('Ambiguous server session match');
    const server = matches[0];
    if (server) {
      const serverKey = serverSessionSelectionKey(server);
      if (matchedServerKeys.has(serverKey)) throw new Error('Duplicate local server match');
      matchedServerKeys.add(serverKey);
    }
    rows.rows.push({ source: 'local', selection_key: key, local, ...(server ? { server } : {}) });
  }

  for (const server of serverRows) {
    const key = serverSessionSelectionKey(server);
    if (!matchedServerKeys.has(key)) rows.rows.push({ source: 'server', selection_key: key, server });
  }
  return rows;
};
