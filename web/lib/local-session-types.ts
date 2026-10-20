import type { SessionOverview } from './types';

/** A local ID is opaque. Neither its spelling nor the revision is a server identity. */
export interface LocalSessionOverview
  extends Pick<
    SessionOverview,
    | 'agent'
    | 'project_hash'
  > {
  local_id: string;
  canonical_id: string;
  native_session_id: string | null;
  profile_fingerprint: string;
  revision: string;
  model: string | null;
  start_time: string | null;
  end_time: string | null;
  input_tokens: number | null;
  output_tokens: number | null;
  cost_usd: number | null;
  event_count: number | null;
  project_name?: string | null;
  entrypoint?: string | null;
  name?: string | null;
  title?: string | null;
  first_line?: string;
  /** A trusted local source's explicit claim, checked against the signed-in server scope and row before merging. */
  server_match?: ServerMatchEvidence;
}

/** Both variants bind the local profile to the actual server profile, owner, and agent. */
export type ServerMatchEvidence =
  | {
      kind: 'server_id';
      server_session_id: string;
      owner_user_id: string;
      profile_fingerprint: string;
      server_profile_email: string;
      agent: string;
    }
  | {
      kind: 'owner_native';
      owner_user_id: string;
      profile_fingerprint: string;
      server_profile_email: string;
      agent: string;
      native_session_id: string;
    };

/** The caller must obtain this from authenticated server state, not a local response. */
export interface SignedInOwnerScope {
  user_id: string;
}

export type LocalSessionResult =
  | { status: 'ready'; sessions: readonly LocalSessionOverview[] }
  | { status: 'unreachable' }
  | { status: 'unauthorized' }
  | { status: 'not_found' }
  | { status: 'forbidden' }
  | { status: 'malformed' };

/** Local fields remain primary; a matched server row is fallback metadata, never an overwrite. */
export type SourceAwareSessionRow =
  | { source: 'local'; selection_key: string; local: LocalSessionOverview; server?: SessionOverview }
  | { source: 'server'; selection_key: string; server: SessionOverview };

export type SourceAwareSessionList =
  | { status: 'ready' | 'unreachable'; rows: SourceAwareSessionRow[] }
  | { status: 'unauthorized' | 'not_found' | 'forbidden' | 'malformed' };
