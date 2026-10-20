import type { LocalSessionAnalysis, LocalAnalysisTurn } from './local-analysis-types';
import type { LocalSessionOverview, ServerMatchEvidence } from './local-session-types';
import type { SessionRecord, SessionRecordView } from './types';

export interface LocalSessionApiConfig {
  origin: string;
  token: string;
}

export interface LocalPageOptions {
  limit?: number;
  cursor?: string;
}

export interface LocalSessionListOptions extends LocalPageOptions {
  agent?: string;
  query?: string;
}

export interface LocalRecordsOptions extends LocalPageOptions {
  revision?: string;
}

export interface LocalSessionPage {
  revision: string;
  sessions: LocalSessionOverview[];
  next_cursor?: string | null;
}

export interface LocalRecordsPage {
  revision: string;
  records: SessionRecord[];
  next_cursor?: string | null;
}

export interface LocalHealth {
  api_version: 1;
  status: 'ok';
  profile_fingerprint: string;
}

export interface LocalProfileIdentity {
  agent: 'claude' | 'codex';
  profile_name: string;
  billing_provider: 'anthropic' | 'openai';
  account_id: string;
  selected: boolean;
}

export interface LocalProfileCatalog {
  revision: number;
  profiles: LocalProfileIdentity[];
}

export interface LocalProfileSelection {
  version: 1;
  revision: number;
  claude?: string;
  codex?: string;
}

export type LocalApiErrorCode =
  | 'invalid_origin' | 'invalid_request' | 'aborted' | 'unreachable'
  | 'unauthorized' | 'forbidden' | 'not_found' | 'revision_conflict'
  | 'daemon_error' | 'too_large' | 'malformed';

export class LocalSessionApiError extends Error {
  constructor(public readonly code: LocalApiErrorCode) {
    super(code);
    this.name = 'LocalSessionApiError';
  }
}

const object = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);
const text = (value: unknown): value is string => typeof value === 'string';
const boundedText = (value: unknown, max = 4096): value is string => text(value) && value.length <= max;
const nonempty = (value: unknown, max = 4096): value is string => boundedText(value, max) && value.length > 0;
const count = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0;
const finite = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value) && value >= 0;
const optional = (value: unknown, guard: (input: unknown) => boolean): boolean => value === undefined || guard(value);
const nullable = (value: unknown, guard: (input: unknown) => boolean): boolean => value === null || guard(value);
const timestamp = (value: unknown): value is string =>
  nonempty(value, 64) && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) && Number.isFinite(Date.parse(value));
const id = (value: unknown): value is string => nonempty(value, 256) && value !== '.' && value !== '..' && !/[\x00-\x1f\x7f/\\?#]/.test(value);
const revision = (value: unknown): value is string => nonempty(value, 256);
const cursor = (value: unknown): value is string => boundedText(value, 4096);
const arrayOf = (value: unknown, guard: (input: unknown) => boolean): boolean => Array.isArray(value) && value.every((item: unknown) => guard(item));

/** The URL is never inferred from an API response or from the current web origin. */
export function validateLocalOrigin(value: string): string {
  const match = typeof value === 'string' && /^http:\/\/(?:localhost|127\.0\.0\.1|\[::1\]):(\d{1,5})\/?$/i.exec(value);
  if (!match) throw new LocalSessionApiError('invalid_origin');
  try {
    const parsed = new URL(value);
    const port = Number(match[1]);
    if (parsed.protocol !== 'http:' || !Number.isInteger(port) || port < 1 || port > 65535 ||
        !['localhost', '127.0.0.1', '[::1]'].includes(parsed.hostname) ||
        parsed.username || parsed.password || parsed.pathname !== '/' || parsed.search || parsed.hash) {
      throw new LocalSessionApiError('invalid_origin');
    }
    // `local enable` issues the 127.0.0.1 literal and the daemon answers only to the exact address it
    // listens on. A browser may also resolve localhost to ::1 first, which could carry the token to
    // another process there.
    if (parsed.hostname === 'localhost') parsed.hostname = '127.0.0.1';
    return parsed.origin;
  } catch {
    throw new LocalSessionApiError('invalid_origin');
  }
}

function validMatch(value: unknown): value is ServerMatchEvidence {
  if (!object(value) || !['server_id', 'owner_native'].includes(String(value.kind)) ||
      !nonempty(value.owner_user_id) || !nonempty(value.profile_fingerprint) ||
      !nonempty(value.server_profile_email) || !nonempty(value.agent)) return false;
  return value.kind === 'server_id' ? nonempty(value.server_session_id) : nonempty(value.native_session_id);
}

function validOverview(value: unknown): value is LocalSessionOverview {
  if (!object(value)) return false;
  return id(value.local_id) && nonempty(value.canonical_id) && nullable(value.native_session_id, nonempty) &&
    nonempty(value.profile_fingerprint) && revision(value.revision) && nonempty(value.agent) &&
    nullable(value.model, boundedText) && nullable(value.start_time, timestamp) && nullable(value.end_time, timestamp) &&
    nullable(value.input_tokens, count) && nullable(value.output_tokens, count) && nullable(value.cost_usd, finite) &&
    nullable(value.event_count, count) && optional(value.project_hash, boundedText) &&
    optional(value.project_name, (v) => nullable(v, text)) &&
    optional(value.entrypoint, (v) => nullable(v, text)) &&
    optional(value.first_line, text) && optional(value.name, (v) => nullable(v, text)) &&
    optional(value.title, (v) => nullable(v, text)) &&
    optional(value.server_match, (v) => nullable(v, validMatch));
}

function validView(value: unknown): value is SessionRecordView {
  if (!object(value) || !['message', 'tool_call', 'tool_result', 'reasoning', 'hidden'].includes(String(value.kind)) ||
      !boundedText(value.anchor_id) || !boundedText(value.dedupe_key)) return false;
  return optional(value.role, (v) => v === 'user' || v === 'assistant') &&
    optional(value.text, text) &&
    optional(value.command, text) && optional(value.command_args, text) &&
    optional(value.agent_task, (v) => typeof v === 'boolean') &&
    optional(value.compact_summary, (v) => typeof v === 'boolean') &&
    optional(value.tool_calls, (v) => arrayOf(v, (item) => object(item) &&
      optional(item.id, boundedText) && boundedText(item.name) &&
      optional(item.input, text))) &&
    optional(value.tool_results, (v) => arrayOf(v, (item) => object(item) &&
      optional(item.id, boundedText) && optional(item.output, text) &&
      optional(item.is_error, (flag) => typeof flag === 'boolean') &&
      optional(item.inferred, (flag) => typeof flag === 'boolean')));
}

function validRecord(value: unknown): value is SessionRecord {
  if (!object(value)) return false;
  if (!timestamp(value.ts) || !nonempty(value.session_id) || !boundedText(value.project_hash) ||
      !boundedText(value.record_type) || !boundedText(value.profile_email) || !boundedText(value.model)) return false;
  const stringFields = ['repository_id', 'repository_name', 'repo_subpath', 'commit_sha', 'branch',
    'agent', 'tool_name', 'tool_call_id', 'uuid', 'parent_uuid', 'agent_id', 'forked_from_session',
    'forked_from_uuid', 'source_file', 'tool_use_id', 'prompt_source', 'entrypoint'];
  return stringFields.every((key) => optional(value[key], boundedText)) &&
    ['input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_create_tokens'].every((key) => optional(value[key], count)) &&
    ['is_sidechain', 'is_compact_summary', 'is_meta'].every((key) => optional(value[key], (v) => typeof v === 'boolean')) &&
    validView(value.view);
}

const turnStatuses = ['ended', 'interrupted', 'error', 'no_response', 'open', 'undeterminable'] as const;
const previewStates = ['ok', 'withheld', 'redacted', 'none'] as const;
const inputKinds = ['typed', 'queued', 'answer', 'command', 'notification', 'peer', 'channel',
  'auto', 'scheduled', 'sdk', 'system', 'unknown'] as const;
const costSources = ['otel', 'otel_partial', 'expired', 'estimate', 'estimate_pending',
  'no_rate', 'unattributable', 'none'] as const;

function validWireTurn(value: unknown): value is Record<string, unknown> {
  if (!object(value)) return false;
  return nonempty(value.turn_key) && count(value.ordinal) && nullable(value.request_no, count) &&
    turnStatuses.includes(value.status as typeof turnStatuses[number]) &&
    optional(value.status_reason, (v) => nullable(v, boundedText)) &&
    ['active', 'abandoned'].includes(String(value.branch)) &&
    inputKinds.includes(value.input_kind as typeof inputKinds[number]) &&
    timestamp(value.started_at) && nullable(value.ended_at, timestamp) &&
    nullable(value.preview, text) &&
    previewStates.includes(value.preview_state as typeof previewStates[number]) &&
    nullable(value.input_tokens, count) && nullable(value.output_tokens, count) &&
    nullable(value.cache_read_tokens, count) && nullable(value.cache_create_tokens, count) &&
    nullable(value.cost_usd, finite) &&
    costSources.includes(value.cost_source as typeof costSources[number]) &&
    count(value.tool_calls) && count(value.subagents) &&
    (!['withheld', 'redacted', 'none'].includes(String(value.preview_state)) || value.preview === null);
}

function validWireSummary(value: unknown): value is Record<string, unknown> {
  if (!object(value)) return false;
  return nonempty(value.agent) && nullable(value.model, boundedText) && nullable(value.title, text) &&
    ['ok', 'withheld', 'redacted', 'none'].includes(String(value.title_state)) &&
    nullable(value.start_time, timestamp) && nullable(value.end_time, timestamp) &&
    count(value.active_seconds) && nullable(value.input_tokens, count) &&
    nullable(value.output_tokens, count) && nullable(value.cache_read_tokens, count) &&
    nullable(value.cache_create_tokens, count) && nullable(value.cost_usd, finite) &&
    costSources.includes(value.cost_source as typeof costSources[number]) &&
    (value.turn_boundaries === 'observed' || value.turn_boundaries === 'undeterminable') &&
    nullable(value.turn_boundaries_reason, boundedText) &&
    nullable(value.turn_count, count) && nullable(value.request_count, count) &&
    nullable(value.input_count, count) && count(value.tool_calls) &&
    (value.turn_boundaries !== 'undeterminable' ||
      (value.turn_count === null && value.request_count === null && value.input_count === null));
}

function validTimeline(value: unknown): value is Record<string, unknown> {
  if (!object(value) || !['observed', 'undeterminable', 'not_collected'].includes(String(value.state)) ||
      !text(value.reason)) return false;
  return arrayOf(value.rows, (row) => object(row) && timestamp(row.ts) &&
    text(row.uuid) && text(row.record_type) && text(row.turn_key) &&
    nullable(row.roles, (roles) => arrayOf(roles, text)) && text(row.unmapped));
}

function validWireAnalysis(value: unknown): value is Record<string, unknown> {
  return object(value) && id(value.local_id) && revision(value.revision) &&
    count(value.rules_version) && validWireSummary(value.summary) &&
    arrayOf(value.turns, validWireTurn) && validTimeline(value.timeline);
}

function adaptWireAnalysis(value: Record<string, unknown>): LocalSessionAnalysis {
  const summary = value.summary as Record<string, unknown>;
  const timeline = value.timeline as Record<string, unknown>;
  const boundaryState = summary.turn_boundaries as 'observed' | 'undeterminable';
  const timelineState = timeline.state as LocalSessionAnalysis['state'];
  const state = timelineState === 'observed' && boundaryState !== 'observed' ? 'undeterminable' : timelineState;
  return {
    revision: value.revision as string,
    rules_version: value.rules_version as number,
    state,
    session: {
      input_tokens: summary.input_tokens as number | null,
      output_tokens: summary.output_tokens as number | null,
      active_seconds: summary.active_seconds as number | null,
      turns: summary.turn_count as number | null,
      inputs: summary.input_count as number | null,
      cost_usd: summary.cost_usd as number | null,
      turn_boundaries: { state: boundaryState, reason: summary.turn_boundaries_reason as string | null },
    },
    turns: (value.turns as Record<string, unknown>[]).map((turn): LocalAnalysisTurn => ({
      turn_key: turn.turn_key as string,
      ordinal: turn.ordinal as number,
      status: turn.status as LocalAnalysisTurn['status'],
      started_at: turn.started_at as string,
      preview: turn.preview as string | null,
      preview_state: turn.preview_state as LocalAnalysisTurn['preview_state'],
      input_tokens: turn.input_tokens as number | null,
      output_tokens: turn.output_tokens as number | null,
    })),
  };
}

function validSessionPage(value: unknown): value is LocalSessionPage {
  return object(value) && revision(value.revision) && arrayOf(value.sessions, validOverview) &&
    optional(value.next_cursor, (v) => nullable(v, cursor));
}

function validRecordsPage(value: unknown): value is LocalRecordsPage {
  return object(value) && revision(value.revision) && arrayOf(value.records, validRecord) &&
    optional(value.next_cursor, (v) => nullable(v, cursor));
}

function validHealth(value: unknown): value is LocalHealth {
  return object(value) && value.api_version === 1 && value.status === 'ok' &&
    nonempty(value.profile_fingerprint);
}

function validProfileIdentity(value: unknown): value is LocalProfileIdentity {
  if (!object(value)) return false;
  const keys = Object.keys(value);
  if (keys.some((key) => !['agent', 'profile_name', 'billing_provider', 'account_id', 'selected'].includes(key))) return false;
  const providerMatches = value.agent === 'claude' && value.billing_provider === 'anthropic' ||
    value.agent === 'codex' && value.billing_provider === 'openai';
  return providerMatches && nonempty(value.profile_name, 63) &&
    (value.profile_name === 'default' || /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$/.test(value.profile_name)) &&
    nonempty(value.account_id, 256) && !/[\x00-\x1f\x7f@]/.test(value.account_id) &&
    !value.account_id.includes('/') && !value.account_id.includes('\\') &&
    typeof value.selected === 'boolean';
}

function validProfileCatalog(value: unknown): value is LocalProfileCatalog {
  if (!object(value) || !count(value.revision) || !Array.isArray(value.profiles) || !value.profiles.every(validProfileIdentity)) return false;
  const profiles = value.profiles as LocalProfileIdentity[];
  return new Set(profiles.map((entry) => `${entry.agent}:${entry.profile_name}`)).size === profiles.length;
}

function validProfileSelection(value: unknown): value is LocalProfileSelection {
	return object(value) && Object.keys(value).every((key) => ['version', 'revision', 'claude', 'codex'].includes(key)) &&
	  value.version === 1 && count(value.revision) &&
    optional(value.claude, (name) => nonempty(name, 63) && (name === 'default' || /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$/.test(name))) &&
    optional(value.codex, (name) => nonempty(name, 63) && (name === 'default' || /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$/.test(name)));
}

function requestOptions(options: LocalPageOptions, params: URLSearchParams): void {
  if (options.limit !== undefined) {
    if (!Number.isSafeInteger(options.limit) || options.limit < 1 || options.limit > 500) throw new LocalSessionApiError('invalid_request');
    params.set('limit', String(options.limit));
  }
  if (options.cursor !== undefined) {
    if (!cursor(options.cursor)) throw new LocalSessionApiError('invalid_request');
    params.set('cursor', options.cursor);
  }
}

/** This transport never calls the authenticated server apiFetch or sends its cookies. */
export function createLocalSessionApi(config: LocalSessionApiConfig, fetchImpl: typeof fetch = fetch) {
  const origin = validateLocalOrigin(config.origin);
  if (!nonempty(config.token, 4096) || /[^\x21-\x7e]/.test(config.token)) throw new LocalSessionApiError('invalid_request');
  const token = config.token;
  let cachedCatalog: LocalProfileCatalog | null = null;

  async function get<T>(path: string, params: URLSearchParams, guard: (value: unknown) => value is T, signal?: AbortSignal): Promise<T> {
    if (signal?.aborted) throw new LocalSessionApiError('aborted');
    const url = new URL(path, `${origin}/`);
    if (url.origin !== origin || !url.pathname.startsWith('/v1/')) throw new LocalSessionApiError('invalid_request');
    url.search = params.toString();
    const timeout = new AbortController();
    const controller = new AbortController();
    let timedOut = false;
    const onAbort = () => controller.abort();
    signal?.addEventListener('abort', onAbort, { once: true });
    timeout.signal.addEventListener('abort', onAbort, { once: true });
    const timer = setTimeout(() => { timedOut = true; timeout.abort(); }, 8000);
    try {
      const response = await fetchImpl(url.toString(), {
        method: 'GET',
        headers: { Authorization: `Bearer ${token}`, Accept: 'application/json' },
        credentials: 'omit',
        redirect: 'error',
        signal: controller.signal,
      });
      if (response.status === 401) throw new LocalSessionApiError('unauthorized');
      if (response.status === 403) throw new LocalSessionApiError('forbidden');
      if (response.status === 404) throw new LocalSessionApiError('not_found');
      if (response.status === 409) throw new LocalSessionApiError('revision_conflict');
      if (response.status === 413) throw new LocalSessionApiError('too_large');
      if (!response.ok) throw new LocalSessionApiError('daemon_error');
      let body: unknown;
      try { body = await response.json(); } catch (error) {
        if (error instanceof SyntaxError) throw new LocalSessionApiError('malformed');
        throw error;
      }
      if (!guard(body)) throw new LocalSessionApiError('malformed');
      return body;
    } catch (error) {
      if (timedOut) throw new LocalSessionApiError('unreachable');
      if (signal?.aborted) throw new LocalSessionApiError('aborted');
      if (error instanceof LocalSessionApiError) throw error;
      throw new LocalSessionApiError('unreachable');
    } finally {
      clearTimeout(timer);
      signal?.removeEventListener('abort', onAbort);
      timeout.signal.removeEventListener('abort', onAbort);
    }
  }

  async function post<T>(path: string, body: unknown, guard: (value: unknown) => value is T, signal?: AbortSignal): Promise<T> {
    if (signal?.aborted) throw new LocalSessionApiError('aborted');
    const url = new URL(path, `${origin}/`);
    if (url.origin !== origin || !url.pathname.startsWith('/v1/')) throw new LocalSessionApiError('invalid_request');
    const timeout = new AbortController();
    const controller = new AbortController();
    let timedOut = false;
    const onAbort = () => controller.abort();
    signal?.addEventListener('abort', onAbort, { once: true });
    timeout.signal.addEventListener('abort', onAbort, { once: true });
    const timer = setTimeout(() => { timedOut = true; timeout.abort(); }, 8000);
    try {
      const response = await fetchImpl(url.toString(), {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}`, Accept: 'application/json', 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
        credentials: 'omit',
        redirect: 'error',
        signal: controller.signal,
      });
      if (response.status === 401) throw new LocalSessionApiError('unauthorized');
      if (response.status === 403) throw new LocalSessionApiError('forbidden');
      if (response.status === 404) throw new LocalSessionApiError('not_found');
      if (response.status === 409) throw new LocalSessionApiError('revision_conflict');
      if (response.status === 413) throw new LocalSessionApiError('too_large');
      if (!response.ok) throw new LocalSessionApiError('daemon_error');
      let value: unknown;
      try { value = await response.json(); } catch (error) {
        if (error instanceof SyntaxError) throw new LocalSessionApiError('malformed');
        throw error;
      }
      if (!guard(value)) throw new LocalSessionApiError('malformed');
      return value;
    } catch (error) {
      if (timedOut) throw new LocalSessionApiError('unreachable');
      if (signal?.aborted) throw new LocalSessionApiError('aborted');
      if (error instanceof LocalSessionApiError) throw error;
      throw new LocalSessionApiError('unreachable');
    } finally {
      clearTimeout(timer);
      signal?.removeEventListener('abort', onAbort);
      timeout.signal.removeEventListener('abort', onAbort);
    }
  }

  return {
    health(signal?: AbortSignal): Promise<LocalHealth> {
      return get('/v1/health', new URLSearchParams(), validHealth, signal);
    },
    async profileCatalog(signal?: AbortSignal): Promise<LocalProfileCatalog> {
      const catalog = await get('/v1/profiles', new URLSearchParams(), validProfileCatalog, signal);
      cachedCatalog = catalog;
      return catalog;
    },
    async selectProfile(entry: LocalProfileIdentity, expectedRevision: number, signal?: AbortSignal): Promise<LocalProfileSelection> {
      if (!Number.isSafeInteger(expectedRevision) || expectedRevision < 0 || !cachedCatalog || cachedCatalog.revision !== expectedRevision ||
          !cachedCatalog.profiles.some((candidate) => candidate.agent === entry.agent && candidate.profile_name === entry.profile_name &&
            candidate.billing_provider === entry.billing_provider && candidate.account_id === entry.account_id)) {
        throw new LocalSessionApiError('invalid_request');
      }
      const selection = await post('/v1/profiles/select', {
        agent: entry.agent,
        profile_name: entry.profile_name,
        billing_provider: entry.billing_provider,
        account_id: entry.account_id,
        expected_revision: expectedRevision,
      }, validProfileSelection, signal);
      cachedCatalog = null;
      return selection;
    },
    listSessions(options: LocalSessionListOptions = {}, signal?: AbortSignal): Promise<LocalSessionPage> {
      const params = new URLSearchParams();
      requestOptions(options, params);
      if (options.agent !== undefined) {
        if (!nonempty(options.agent, 32)) throw new LocalSessionApiError('invalid_request');
        params.set('agent', options.agent);
      }
      if (options.query !== undefined) {
        if (!boundedText(options.query, 512)) throw new LocalSessionApiError('invalid_request');
        params.set('query', options.query);
      }
      return get('/v1/sessions', params, validSessionPage, signal).then((page) => ({
        ...page,
        sessions: page.sessions.map((session) => ({
          ...session,
          name: session.name ?? undefined,
          server_match: session.server_match ?? undefined,
        })),
      }));
    },
    listRecords(localId: string, options: LocalRecordsOptions = {}, signal?: AbortSignal): Promise<LocalRecordsPage> {
      if (!id(localId)) throw new LocalSessionApiError('invalid_request');
      const params = new URLSearchParams();
      requestOptions(options, params);
      if (options.revision !== undefined) {
        if (!revision(options.revision)) throw new LocalSessionApiError('invalid_request');
        params.set('revision', options.revision);
      }
      return get(`/v1/sessions/${encodeURIComponent(localId)}/records`, params, validRecordsPage, signal);
    },
    async analysis(localId: string, signal?: AbortSignal): Promise<LocalSessionAnalysis> {
      if (!id(localId)) throw new LocalSessionApiError('invalid_request');
      const wire = await get(`/v1/sessions/${encodeURIComponent(localId)}/analysis`, new URLSearchParams(), validWireAnalysis, signal);
      if (wire.local_id !== localId) throw new LocalSessionApiError('malformed');
      return adaptWireAnalysis(wire);
    },
  };
}
