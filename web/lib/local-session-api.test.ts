import { afterEach, describe, expect, it, vi } from 'vitest';
import { createLocalSessionApi, LocalSessionApiError, validateLocalOrigin } from './local-session-api';

const overview = {
  local_id: 'local:one', canonical_id: 'canonical', native_session_id: 'native',
  profile_fingerprint: 'fingerprint', revision: 'r1', agent: 'claude', model: 'model',
  start_time: '2026-10-09T00:00:00Z', end_time: '2026-10-09T01:00:00Z',
  input_tokens: 0, output_tokens: 0, cost_usd: 0, event_count: 0,
};
const wireAnalysis = {
  local_id: 'local:one', revision: 'r1', rules_version: 2,
  summary: {
    agent: 'claude', model: null, title: null, title_state: 'none',
    start_time: '2026-10-09T00:00:00Z', end_time: '2026-10-09T01:00:00Z',
    active_seconds: 0, input_tokens: null, output_tokens: 0,
    cache_read_tokens: null, cache_create_tokens: null, cost_usd: null,
    cost_source: 'none', turn_boundaries: 'undeterminable',
    turn_boundaries_reason: 'pre_collect', turn_count: null, request_count: null,
    input_count: null, tool_calls: 0,
  },
  turns: [{
    turn_key: 'turn-1', ordinal: 0, request_no: null, status: 'undeterminable',
    status_reason: 'pre_collect', branch: 'active', input_kind: 'typed', preview: null,
    preview_state: 'redacted', started_at: '2026-10-09T00:00:00Z', ended_at: null,
    input_tokens: null, output_tokens: 0, cache_read_tokens: null,
    cache_create_tokens: null, cost_usd: null, cost_source: 'none',
    tool_calls: 0, subagents: 0,
  }],
  timeline: {
    state: 'undeterminable', reason: 'pre_collect', rows: [{
      ts: '2026-10-09T00:00:00Z', uuid: '', record_type: 'user',
      turn_key: 'turn-1', roles: ['user'], unmapped: '',
    }],
  },
};
const response = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status });
const mock = vi.fn<typeof fetch>();
const api = () => createLocalSessionApi({ origin: 'http://127.0.0.1:5080', token: 'test-secret' }, mock);

afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers(); mock.mockReset(); });

describe('loopback origin', () => {
  // Derive noncanonical IPv4 spellings from the reserved loopback fixture.
  const loopback = '127.0.0.1';
  const [firstOctet, ...remainingOctets] = loopback.split('.');
  const shorthandLoopback = loopback.replace('.0.0.', '.');
  const octalLoopback = [`0${Number(firstOctet).toString(8)}`, ...remainingOctets].join('.');
  const percentEncodedLoopback = [
    [...firstOctet].map((digit) => `%${digit.charCodeAt(0).toString(16)}`).join(''),
    ...remainingOctets,
  ].join('.');

  it('canonicalizes only an explicit-port loopback HTTP origin', () => {
    expect(validateLocalOrigin('http://127.0.0.1:5080/')).toBe('http://127.0.0.1:5080');
    expect(validateLocalOrigin('http://[::1]:5080')).toBe('http://[::1]:5080');
  });

  it('rewrites localhost to the 127.0.0.1 literal the daemon was issued for', () => {
    expect(validateLocalOrigin('http://localhost:4567')).toBe('http://127.0.0.1:4567');
    expect(validateLocalOrigin('http://LOCALHOST:5080/')).toBe('http://127.0.0.1:5080');
    // URL.origin drops a default port, as it did before localhost was rewritten.
    expect(validateLocalOrigin('http://localhost:80')).toBe('http://127.0.0.1');
  });

  it('sends requests to the rewritten origin when given localhost', async () => {
    mock.mockResolvedValue(response({ status: 'ok', api_version: 1, profile_fingerprint: 'fingerprint' }));
    await createLocalSessionApi({ origin: 'http://localhost:4567', token: 'test-secret' }, mock).health();
    expect(mock.mock.calls[0][0]).toBe('http://127.0.0.1:4567/v1/health');
  });

  it.each([
    'https://localhost:5080', 'http://localhost', 'http://localhost:0',
    'http://localhost:65536', 'http://localhost.evil.example.com:5080', 'http://0.0.0.0:5080',
    `http://${shorthandLoopback}:5080`, `http://${octalLoopback}:5080`, `http://${percentEncodedLoopback}:5080`,
    'http://user:password@localhost:5080', 'http://localhost:5080/path',
    'http://localhost:5080/?token=secret', 'http://localhost:5080/#secret',
    'http://localhost:5080\\@evil.test', 'http://localhost:5080.:5080',
    'http://localhost.:5080',
  ])('rejects %s before fetching', (origin) => {
    expect(() => createLocalSessionApi({ origin, token: 'test-secret' }, mock))
      .toThrowError(new LocalSessionApiError('invalid_origin'));
    expect(mock).not.toHaveBeenCalled();
  });
});

describe('local daemon transport', () => {
  it('does not fetch during construction and rejects an already-aborted request before fetching', async () => {
    const client = api();
    expect(mock).not.toHaveBeenCalled();
    const caller = new AbortController();
    caller.abort();
    await expect(client.health(caller.signal)).rejects.toMatchObject({ code: 'aborted' });
    expect(mock).not.toHaveBeenCalled();
  });

  it('sends Bearer only to the loopback endpoint without credentials, redirect or server headers', async () => {
    mock.mockResolvedValue(response({ revision: 'r1', sessions: [overview], next_cursor: null }));
    expect(await api().listSessions({ agent: 'claude', query: 'hello world', limit: 50, cursor: 'next' }))
      .toMatchObject({ revision: 'r1', sessions: [overview] });
    const [url, init] = mock.mock.calls[0];
    expect(url).toBe('http://127.0.0.1:5080/v1/sessions?limit=50&cursor=next&agent=claude&query=hello+world');
    expect(init).toMatchObject({ method: 'GET', credentials: 'omit', redirect: 'error',
      headers: { Authorization: 'Bearer test-secret', Accept: 'application/json' } });
    expect(String(url)).not.toContain('test-secret');
  });

  it('maps an exact local account and changes only the named next profile with revision CAS', async () => {
    const entry = { agent: 'codex', profile_name: 'work', billing_provider: 'openai', account_id: 'acct-local', selected: false } as const;
    mock.mockResolvedValueOnce(response({ revision: 4, profiles: [entry] }))
      .mockResolvedValueOnce(response({ version: 1, revision: 5, codex: 'work' }));
    const client = api();
    const catalog = await client.profileCatalog();
    expect(catalog).toEqual({ revision: 4, profiles: [entry] });
    await client.selectProfile(entry, 4);
    const [url, init] = mock.mock.calls[1];
    expect(url).toBe('http://127.0.0.1:5080/v1/profiles/select');
    expect(init).toMatchObject({ method: 'POST', credentials: 'omit', redirect: 'error',
      headers: { Authorization: 'Bearer test-secret', Accept: 'application/json', 'Content-Type': 'application/json' } });
    expect(JSON.parse(String(init?.body))).toEqual({
      agent: 'codex', profile_name: 'work', billing_provider: 'openai', account_id: 'acct-local', expected_revision: 4,
    });
    expect(String(url)).not.toContain('test-secret');
  });

  it('rejects profile catalogs with invalid provider mapping or account ids', async () => {
    mock.mockResolvedValue(response({ revision: 0, profiles: [
      { agent: 'codex', profile_name: 'work', billing_provider: 'anthropic', account_id: 'acct-local', selected: false },
    ] }));
    await expect(api().profileCatalog()).rejects.toMatchObject({ code: 'malformed' });
  });

  it('preserves unmeasured list fields instead of fabricating zero or empty strings', async () => {
    mock.mockResolvedValue(response({ revision: 'r1', sessions: [
      { ...overview, canonical_id: 'local:one', model: null, start_time: null, end_time: null,
        input_tokens: null, output_tokens: null, event_count: null, cost_usd: null,
        project_name: null, entrypoint: null, name: null, title: null, server_match: null },
    ], next_cursor: null }));
    const { sessions } = await api().listSessions();
    expect(sessions[0]).toMatchObject({ local_id: 'local:one', model: null, start_time: null, end_time: null,
      input_tokens: null, output_tokens: null, event_count: null, cost_usd: null,
      project_name: null, entrypoint: null, title: null, name: undefined, server_match: undefined });
  });

  it('accepts observed and unobserved native IDs together without changing opaque IDs', async () => {
    const unobserved = { ...overview, local_id: 'opaque:unobserved', canonical_id: 'canonical:unobserved', native_session_id: null };
    mock.mockResolvedValue(response({ revision: 'r1', sessions: [overview, unobserved], next_cursor: null }));
    expect((await api().listSessions()).sessions).toMatchObject([overview, unobserved]);
    mock.mockResolvedValue(response({ revision: 'r1', sessions: [{ ...unobserved, native_session_id: '' }] }));
    await expect(api().listSessions()).rejects.toMatchObject({ code: 'malformed' });
  });

  it('uses only opaque local IDs, URL-encoded, and preserves revision cursor', async () => {
    mock.mockResolvedValue(response({ revision: 'r1', records: [], next_cursor: '' }));
    expect(await api().listRecords('a:b %', { revision: 'r1', limit: 12, cursor: 'page:2' }))
      .toMatchObject({ records: [] });
    expect(mock.mock.calls[0][0]).toBe('http://127.0.0.1:5080/v1/sessions/a%3Ab%20%25/records?limit=12&cursor=page%3A2&revision=r1');
    for (const unsafe of ['/users/private', '../private', '.', '..', 'one?query=leak', 'one\\path']) {
      expect(() => api().listRecords(unsafe)).toThrowError(new LocalSessionApiError('invalid_request'));
    }
    expect(mock).toHaveBeenCalledTimes(1);
  });

  it('accepts a valid empty list, and a valid nullable unknown analysis', async () => {
    mock.mockResolvedValueOnce(response({ revision: 'r1', sessions: [] }))
      .mockResolvedValueOnce(response(wireAnalysis));
    expect((await api().listSessions()).sessions).toEqual([]);
    expect(await api().analysis('local:one')).toMatchObject({
      revision: 'r1', state: 'undeterminable',
      session: { turns: null, input_tokens: null, output_tokens: 0, active_seconds: 0,
        turn_boundaries: { state: 'undeterminable', reason: 'pre_collect' } },
      turns: [{ status: 'undeterminable', preview: null, preview_state: 'redacted', output_tokens: 0 }],
    });
  });

  it.each([
    [{ sessions: [] }, 'malformed'],
    [{ revision: 'r1', sessions: [{ ...overview, input_tokens: '0' }] }, 'malformed'],
    [{ revision: 'r1', sessions: [{ ...overview, start_time: 'not a timestamp' }] }, 'malformed'],
    [{ revision: 'r1', sessions: [], next_cursor: 3 }, 'malformed'],
  ] as const)('rejects invalid list envelope %#', async (body, code) => {
    mock.mockResolvedValue(response(body));
    await expect(api().listSessions()).rejects.toMatchObject({ code });
  });

  it('rejects invalid local record fields instead of trusting a matching response envelope', async () => {
    mock.mockResolvedValue(response({ revision: 'r1', records: [{
      ts: '2026-10-09T00:00:00Z', session_id: 'native', project_hash: '',
      profile_email: '', model: '', record_type: 'message',
      view: { kind: 'not-a-view', anchor_id: '', dedupe_key: '' },
    }] }));
    await expect(api().listRecords('one')).rejects.toMatchObject({ code: 'malformed' });
  });

  it('rejects records with no explicit public view', async () => {
    mock.mockResolvedValue(response({ revision: 'r1', records: [{
      ts: '2026-10-09T00:00:00Z', session_id: 'native', project_hash: '',
      profile_email: '', model: '', record_type: 'message', raw: { text: 'not a public view' },
    }], next_cursor: null }));
    await expect(api().listRecords('local:one')).rejects.toMatchObject({ code: 'malformed' });
  });

  it('preserves long message, command, tool input and output without truncation', async () => {
    const content = 'x'.repeat(262145);
    mock.mockResolvedValue(response({ revision: 'r1', records: [{
      ts: '2026-10-09T00:00:00Z', session_id: 'native', project_hash: '',
      profile_email: '', model: '', record_type: 'assistant',
      view: { kind: 'tool_call', anchor_id: 'anchor', dedupe_key: 'dedupe',
        text: content, command_args: content,
        tool_calls: [{ name: 'Read', input: content }],
        tool_results: [{ output: content, is_error: false, inferred: true }] },
    }], next_cursor: null }));
    const page = await api().listRecords('local:one');
    expect(page.records[0].view?.text).toBe(content);
    expect(page.records[0].view?.command_args).toBe(content);
    expect(page.records[0].view?.tool_calls?.[0].input).toBe(content);
    expect(page.records[0].view?.tool_results?.[0].output).toBe(content);
  });

  it.each([
    { tool_calls: [{ name: 'Read', input: { path: '/private' } }] },
    { tool_results: [{ output: 'text', is_error: 'false' }] },
    { tool_results: [{ output: 'text', inferred: 1 }] },
  ])('rejects invalid tool input and result flags %#', async (fields) => {
    mock.mockResolvedValue(response({ revision: 'r1', records: [{
      ts: '2026-10-09T00:00:00Z', session_id: 'native', project_hash: '',
      profile_email: '', model: '', record_type: 'assistant',
      view: { kind: 'tool_call', anchor_id: 'anchor', dedupe_key: 'dedupe', ...fields },
    }], next_cursor: null }));
    await expect(api().listRecords('local:one')).rejects.toMatchObject({ code: 'malformed' });
  });

  it('rejects mismatched local ID, malformed numeric fields, and fabricated unknown counts', async () => {
    mock.mockResolvedValue(response({ ...wireAnalysis, local_id: 'someone-else' }));
    await expect(api().analysis('local:one')).rejects.toMatchObject({ code: 'malformed' });
    mock.mockResolvedValue(response({ ...wireAnalysis, summary: { ...wireAnalysis.summary, input_tokens: '0' } }));
    await expect(api().analysis('local:one')).rejects.toMatchObject({ code: 'malformed' });
    mock.mockResolvedValue(response({ ...wireAnalysis, summary: { ...wireAnalysis.summary, turn_count: 0 } }));
    await expect(api().analysis('local:one')).rejects.toMatchObject({ code: 'malformed' });
  });

  it('rejects unknown wire states rather than inventing completed or zero metrics', async () => {
    mock.mockResolvedValue(response({ ...wireAnalysis, timeline: { ...wireAnalysis.timeline, state: 'complete' } }));
    await expect(api().analysis('local:one')).rejects.toMatchObject({ code: 'malformed' });
    mock.mockResolvedValue(response({ ...wireAnalysis, turns: [{ ...wireAnalysis.turns[0], status: 'completed' }] }));
    await expect(api().analysis('local:one')).rejects.toMatchObject({ code: 'malformed' });
  });

  it('preserves a withheld preview without rendering the hidden body', async () => {
    mock.mockResolvedValue(response({ ...wireAnalysis,
      turns: [{ ...wireAnalysis.turns[0], preview: null, preview_state: 'withheld' }],
    }));
    expect((await api().analysis('local:one')).turns[0]).toMatchObject({
      preview: null, preview_state: 'withheld',
    });
  });

  it('accepts a timeline row with nil roles and a stated unmapped reason', async () => {
    mock.mockResolvedValue(response({ ...wireAnalysis,
      timeline: { ...wireAnalysis.timeline, rows: [{ ...wireAnalysis.timeline.rows[0],
        turn_key: '', roles: null, unmapped: 'turn_boundaries_pre_collect' }] },
    }));
    expect((await api().analysis('local:one')).state).toBe('undeterminable');
  });

  it('preserves observed zero separately from unknown when adapting daemon analysis', async () => {
    mock.mockResolvedValue(response({
      ...wireAnalysis,
      summary: { ...wireAnalysis.summary, turn_boundaries: 'observed', turn_boundaries_reason: null,
        turn_count: 0, request_count: 0, input_count: 0 },
      turns: [], timeline: { state: 'observed', reason: '', rows: [] },
    }));
    expect(await api().analysis('local:one')).toMatchObject({
      state: 'observed', session: { turns: 0, inputs: 0, input_tokens: null }, turns: [],
    });
  });

  it('requires exact health API version and fingerprint', async () => {
    mock.mockResolvedValue(response({ status: 'ok', api_version: 1, profile_fingerprint: 'fingerprint' }));
    expect(await api().health()).toMatchObject({ api_version: 1 });
    mock.mockResolvedValue(response({ status: 'ok', api_version: '1', profile_fingerprint: 'fingerprint' }));
    await expect(api().health()).rejects.toMatchObject({ code: 'malformed' });
    mock.mockResolvedValue(response({ status: 'ok', api_version: 1 }));
    await expect(api().health()).rejects.toMatchObject({ code: 'malformed' });
  });

  it.each([[401, 'unauthorized'], [403, 'forbidden'], [404, 'not_found'],
    [409, 'revision_conflict'], [413, 'too_large'], [500, 'daemon_error']] as const)('maps HTTP %s to %s', async (status, code) => {
    mock.mockResolvedValue(response({}, status));
    await expect(api().health()).rejects.toMatchObject({ code });
  });

  it('keeps malformed JSON distinct from daemon failures and network outages', async () => {
    mock.mockResolvedValueOnce(new Response('{bad'));
    await expect(api().health()).rejects.toMatchObject({ code: 'malformed' });
    mock.mockRejectedValueOnce(new TypeError('connection refused'));
    await expect(api().health()).rejects.toMatchObject({ code: 'unreachable' });
  });

  it('classifies body read failures as transport errors and honors abort and timeout', async () => {
    const brokenBody = new Response('{}');
    vi.spyOn(brokenBody, 'json').mockRejectedValueOnce(new TypeError('body connection lost'));
    mock.mockResolvedValueOnce(brokenBody);
    await expect(api().health()).rejects.toMatchObject({ code: 'unreachable' });

    const caller = new AbortController();
    mock.mockImplementationOnce((_url, init) => {
      const body = new Response('{}');
      vi.spyOn(body, 'json').mockImplementation(() => new Promise((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')));
      }));
      return Promise.resolve(body);
    });
    const aborted = api().health(caller.signal);
    await Promise.resolve();
    caller.abort();
    await expect(aborted).rejects.toMatchObject({ code: 'aborted' });

    vi.useFakeTimers();
    mock.mockImplementationOnce((_url, init) => {
      const body = new Response('{}');
      vi.spyOn(body, 'json').mockImplementation(() => new Promise((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')));
      }));
      return Promise.resolve(body);
    });
    const timeout = expect(api().health()).rejects.toMatchObject({ code: 'unreachable' });
    await vi.advanceTimersByTimeAsync(8000);
    await timeout;
  });

  it('aborts when the caller aborts and removes listeners', async () => {
    const caller = new AbortController();
    mock.mockImplementation((_url, init) => new Promise((_resolve, reject) => {
      init?.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')));
    }));
    const result = api().health(caller.signal);
    caller.abort();
    await expect(result).rejects.toMatchObject({ code: 'aborted' });
    expect((mock.mock.calls[0][1]?.signal as AbortSignal).aborted).toBe(true);
  });

  it('times out after eight seconds without leaking a token or treating the timeout as empty data', async () => {
    vi.useFakeTimers();
    mock.mockImplementation((_url, init) => new Promise((_resolve, reject) => {
      init?.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')));
    }));
    const result = api().health();
    const assertion = expect(result).rejects.toMatchObject({ code: 'unreachable' });
    await vi.advanceTimersByTimeAsync(8000);
    await assertion;
  });
});
