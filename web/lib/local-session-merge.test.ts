import { describe, expect, it } from 'vitest';

import { groupSameIdRows, localSessionSelectionKey, mergeLocalSessionOverviews, sameIdGroupingCandidates, serverSessionSelectionKey } from './local-session-merge';
import type { LocalSessionOverview, ServerMatchEvidence } from './local-session-types';
import type { SessionOverview } from './types';

const owner = { user_id: 'owner-one' };

const local = (overrides: Partial<LocalSessionOverview> = {}): LocalSessionOverview => ({
  local_id: 'row-one',
  canonical_id: 'canonical-one',
  native_session_id: 'native-one',
  profile_fingerprint: 'profile-one',
  revision: 'v1',
  agent: 'claude',
  model: 'model-one',
  start_time: '2026-01-01T00:00:00Z',
  end_time: '2026-01-01T01:00:00Z',
  input_tokens: 3,
  output_tokens: 2,
  cost_usd: 1,
  event_count: 5,
  ...overrides,
});

const server = (overrides: Partial<SessionOverview> = {}): SessionOverview => ({
  session_id: 'native-one',
  user_id: owner.user_id,
  profile_email: 'profile-one@example.invalid',
  agent: 'claude',
  model: 'model-one',
  start_time: '2026-01-01T00:00:00Z',
  end_time: '2026-01-01T01:00:00Z',
  input_tokens: 3,
  output_tokens: 2,
  cost_usd: 1,
  event_count: 5,
  has_sync: true,
  ...overrides,
});

const evidence = (overrides: Partial<Extract<ServerMatchEvidence, { kind: 'owner_native' }>> = {}): ServerMatchEvidence => ({
  kind: 'owner_native',
  owner_user_id: owner.user_id,
  profile_fingerprint: 'profile-one',
  server_profile_email: 'profile-one@example.invalid',
  agent: 'claude',
  native_session_id: 'native-one',
  ...overrides,
});

describe('source-aware session list', () => {
  it('keeps local metadata primary while retaining the verified server fallback', () => {
    const ownLocal = local({ name: 'local name', server_match: evidence() });
    const ownServer = server({ project_name: 'older server title' });
    const result = mergeLocalSessionOverviews({ status: 'ready', sessions: [ownLocal] }, [ownServer], owner);

    expect(result).toEqual({
      status: 'ready',
      rows: [{ source: 'local', selection_key: localSessionSelectionKey(ownLocal), local: ownLocal, server: ownServer }],
    });
    expect(localSessionSelectionKey(local({ revision: 'v2', local_id: 'row-updated' })))
      .toBe(localSessionSelectionKey(ownLocal));
  });

  it('accepts explicit server ID evidence when the canonical and native IDs differ', () => {
    const ownLocal = local({
      server_match: {
        kind: 'server_id',
        server_session_id: 'server-id',
        owner_user_id: owner.user_id,
        profile_fingerprint: 'profile-one',
        server_profile_email: 'profile-one@example.invalid',
        agent: 'claude',
      },
    });
    const result = mergeLocalSessionOverviews({ status: 'ready', sessions: [ownLocal] }, [server({ session_id: 'server-id' })], owner);
    expect(result.status === 'ready' && result.rows).toHaveLength(1);
  });

  it('does not collapse a matching native ID without evidence or under a different owner, profile, or agent', () => {
    const serverRows = [
      server({ user_id: 'someone-else' }),
      server({ profile_email: 'other-profile@example.invalid' }),
      server({ agent: 'codex' }),
    ];
    for (const ownLocal of [local(), local({ server_match: evidence() })]) {
      const result = mergeLocalSessionOverviews({ status: 'ready', sessions: [ownLocal] }, serverRows, owner);
      expect(result.status === 'ready' && result.rows).toHaveLength(4);
    }
    const wrongProfile = local({ profile_fingerprint: 'another-profile', server_match: evidence() });
    const result = mergeLocalSessionOverviews({ status: 'ready', sessions: [wrongProfile] }, [server()], owner);
    expect(result.status === 'ready' && result.rows).toHaveLength(2);
  });

  it('keeps server rows visible but separate without an owner scope, even when native IDs and evidence match', () => {
    const ownLocal = local({ server_match: evidence(), model: null, start_time: null, end_time: null,
      input_tokens: null, output_tokens: null, cost_usd: null, event_count: null,
      project_name: null, entrypoint: null });
    const ownServer = server();
    expect(mergeLocalSessionOverviews({ status: 'ready', sessions: [ownLocal] }, [ownServer])).toEqual({
      status: 'ready', rows: [
        { source: 'local', selection_key: localSessionSelectionKey(ownLocal), local: ownLocal },
        { source: 'server', selection_key: serverSessionSelectionKey(ownServer), server: ownServer },
      ],
    });
    expect(mergeLocalSessionOverviews({ status: 'unreachable' }, [ownServer])).toEqual({
      status: 'unreachable', rows: [{ source: 'server', selection_key: serverSessionSelectionKey(ownServer), server: ownServer }],
    });
  });

  it('does not use evidence from a different signed-in owner or an unscoped server row', () => {
    for (const ownServer of [server(), server({ user_id: undefined })]) {
      const result = mergeLocalSessionOverviews(
        { status: 'ready', sessions: [local({ server_match: evidence() })] },
        [ownServer], { user_id: 'other-owner' },
      );
      expect(result.status === 'ready' && result.rows).toHaveLength(2);
    }
  });

  it('retains local rows when server rows are absent and falls back only on an unreachable local source', () => {
    const ownLocal = local();
    const ownServer = server();
    expect(mergeLocalSessionOverviews({ status: 'ready', sessions: [ownLocal] }, [], owner)).toEqual({
      status: 'ready',
      rows: [{ source: 'local', selection_key: localSessionSelectionKey(ownLocal), local: ownLocal }],
    });
    expect(mergeLocalSessionOverviews({ status: 'unreachable' }, [ownServer], owner)).toEqual({
      status: 'unreachable',
      rows: [{ source: 'server', selection_key: serverSessionSelectionKey(ownServer), server: ownServer }],
    });
  });

  it.each(['unauthorized', 'forbidden', 'not_found', 'malformed'] as const)(
    'preserves %s instead of showing empty rows or a server fallback', (status) => {
      expect(mergeLocalSessionOverviews({ status }, [server()], owner)).toEqual({ status });
    },
  );

  it('rejects duplicate local canonical identities and ambiguous matches without mutating inputs', () => {
    const first = local({ server_match: evidence() });
    const second = local({ local_id: 'second', revision: 'v2' });
    const locals = Object.freeze([first, second]);
    const servers = Object.freeze([server()]);
    expect(() => mergeLocalSessionOverviews({ status: 'ready', sessions: locals }, servers, owner))
      .toThrow('Invalid or duplicate local session identity');
    expect(locals).toHaveLength(2);
    expect(servers).toHaveLength(1);
    expect(() => mergeLocalSessionOverviews({ status: 'ready', sessions: [first, local({
      canonical_id: 'other-canonical', server_match: evidence(),
    })] }, servers, owner)).toThrow('Duplicate local server match');
  });

  it('offers only a unique owner-scoped same-ID grouping hint and keeps both source rows', () => {
    const ownLocal = local();
    const ownServer = server();
    const candidates = sameIdGroupingCandidates([ownLocal], [ownServer], owner);
    expect([...candidates]).toEqual([[localSessionSelectionKey(ownLocal), serverSessionSelectionKey(ownServer)]]);
    const merged = mergeLocalSessionOverviews({ status: 'ready', sessions: [ownLocal] }, [ownServer], owner);
    if (!('rows' in merged)) throw new Error('Expected rows');
    expect(groupSameIdRows(merged.rows, candidates, new Set())).toHaveLength(2);
    expect(groupSameIdRows(merged.rows, candidates, new Set([localSessionSelectionKey(ownLocal)]))).toEqual([[
      merged.rows[0], merged.rows[1],
    ]]);
    expect(merged.rows).toHaveLength(2);
  });

  it('keeps an unobserved native ID selectable by opaque identity without grouping or owner-native matching', () => {
    const unobserved = local({ local_id: 'opaque:unobserved', canonical_id: 'canonical:unobserved', native_session_id: null,
      server_match: evidence() });
    const ownServer = server();
    const result = mergeLocalSessionOverviews({ status: 'ready', sessions: [unobserved] }, [ownServer], owner);
    expect(result).toEqual({ status: 'ready', rows: [
      { source: 'local', selection_key: localSessionSelectionKey(unobserved), local: unobserved },
      { source: 'server', selection_key: serverSessionSelectionKey(ownServer), server: ownServer },
    ] });
    expect(localSessionSelectionKey(unobserved)).toContain('canonical:unobserved');
    expect(sameIdGroupingCandidates([unobserved], [ownServer], owner).size).toBe(0);
  });

  it('refuses ambiguous, foreign-owner, and evidence-bound ID hints', () => {
    const ownLocal = local();
    const second = local({ local_id: 'another', canonical_id: 'another', profile_fingerprint: 'another-profile' });
    expect(sameIdGroupingCandidates([ownLocal, second], [server()], owner).size).toBe(0);
    expect(sameIdGroupingCandidates([ownLocal], [server(), server({ profile_email: 'duplicate@example.invalid' })], owner).size).toBe(0);
    expect(sameIdGroupingCandidates([ownLocal], [server({ user_id: 'another-owner' })], owner).size).toBe(0);
    expect(sameIdGroupingCandidates([ownLocal], [server({ agent: 'codex' })], owner).size).toBe(0);
    expect(sameIdGroupingCandidates([ownLocal], [server()]).size).toBe(0);
    expect(sameIdGroupingCandidates([local({ server_match: evidence() })], [server()], owner).size).toBe(0);
    expect(sameIdGroupingCandidates([ownLocal, local({ local_id: 'matched', canonical_id: 'matched', server_match: evidence() })], [server()], owner).size).toBe(0);
  });

  it('uses distinct, unambiguous keys for local/server identity and embedded delimiters', () => {
    const a = local({ profile_fingerprint: 'p|q', canonical_id: 'c' });
    const b = local({ profile_fingerprint: 'p', canonical_id: 'q|c' });
    expect(localSessionSelectionKey(a)).not.toBe(localSessionSelectionKey(b));
    expect(localSessionSelectionKey(local({ canonical_id: 'native-one' })))
      .not.toBe(serverSessionSelectionKey(server()));
  });
});
