import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { answer, indexEntries, routeOf, shardOf } from './replayer';
import type { RecordedEntry, ReplaySource } from './replayer';
import type { Facts } from './compute';

const RECORDED_AT = '2026-10-24T07:40:00Z';
const now = Date.parse(RECORDED_AT);
const iso = (minutesFromNow: number) => new Date(now + minutesFromNow * 60000).toISOString();

const shards: Record<string, RecordedEntry[]> = {
  'api.accounts.shard.json': [{ key: 'GET /api/accounts?', status: 200, body: 'accounts' }],
  'api.stats.timeseries-by-model.shard.json': [
    { key: 'GET /api/stats/timeseries-by-model?granularity=minute&since=@-180m', status: 200, body: 'minute' },
  ],
  'api.weekly-insights.shard.json': [{ key: 'GET /api/weekly-insights?since=@-16840m&until=@-6760m', status: 200, body: 'week' }],
  'api.sessions._.personal.shard.json': [{ key: 'GET /api/sessions/0059ea86-0e27-4572-bcfd-40c46414051e/personal?', status: 404, body: 'none' }],
  'api.sessions._.trace.index.shard.json': [{ key: 'GET /api/sessions/0059ea86-0e27-4572-bcfd-40c46414051e/trace/index?', status: 200, body: 'trace' }],
};

const emptyFacts: Facts = {
  strings: [], ts: [], model: [], user_id: [], profile_email: [], login_email: [], user_team: [], agent: [],
  billing_provider: [], cost_usd: [], input_tokens: [], output_tokens: [],
};

const source: ReplaySource = {
  now,
  shard: async (name) => (shards[name] ? indexEntries(shards[name]) : null),
  facts: async () => emptyFacts,
  quota: async () => [],
};
const at = (path: string) => new URL(path, 'https://example.com');

describe('answer', () => {
  it('replays an exact recording, status included', async () => {
    expect(await answer(source, 'GET', at('/api/accounts'))).toEqual({ kind: 'recorded', entry: shards['api.accounts.shard.json'][0] });
    expect(await answer(source, 'GET', at('/api/sessions/0059ea86-0e27-4572-bcfd-40c46414051e/personal'))).toMatchObject({
      kind: 'recorded', entry: { status: 404 },
    });
  });

  it('finds a week computed in another time zone', async () => {
    // Monday 00:00 in New York is 13 hours after Monday 00:00 in Seoul.
    const url = at(`/api/weekly-insights?since=${iso(-16840 + 780)}&until=${iso(-6760 + 780)}&tz=America/New_York`);
    expect(await answer(source, 'GET', url)).toMatchObject({ kind: 'recorded', entry: { body: 'week' } });
  });

  it('does not answer the previous week with this one', async () => {
    const url = at(`/api/weekly-insights?since=${iso(-16840 - 10080)}&until=${iso(-6760 - 10080)}`);
    expect(await answer(source, 'GET', url)).toMatchObject({ kind: 'missing' });
  });

  // A window the chart moved to is not lent the recorded one; it is computed.
  it('computes a trend window that was not recorded', async () => {
    const url = at(`/api/stats/timeseries-by-model?granularity=minute&since=${iso(-360)}&until=${iso(-180)}&tz=Asia/Seoul`);
    expect(await answer(source, 'GET', url)).toEqual({ kind: 'computed', body: [] });
  });

  it('leaves an endpoint it cannot compute unanswered', async () => {
    const url = at(`/api/stats/timeseries-by-model?granularity=minute&since=${iso(-360)}&project_hashes=a,b`);
    expect(await answer(source, 'GET', url)).toMatchObject({ kind: 'missing' });
  });

  it('answers a session opened under an account scope with its unscoped recording', async () => {
    const url = at('/api/sessions/0059ea86-0e27-4572-bcfd-40c46414051e/trace/index?login_email=marcus.fablewright@example.com');
    expect(await answer(source, 'GET', url)).toMatchObject({ kind: 'recorded', entry: { body: 'trace' } });
    expect(await answer(source, 'GET', at('/api/accounts?login_email=marcus.fablewright@example.com'))).toMatchObject({ kind: 'missing' });
  });

  it('refuses writes but keeps the session signed in', async () => {
    expect(await answer(source, 'PUT', at('/api/sessions/abc/personal'))).toEqual({ kind: 'read-only' });
    expect(await answer(source, 'POST', at('/api/auth/refresh'))).toEqual({ kind: 'refresh' });
  });
});

describe('routeOf', () => {
  const page = 'https://example.com';

  it('answers the own API of the page and leaves its assets to the network', () => {
    expect(routeOf(new URL('/api/auth/me', page), page)).toBe('api');
    expect(routeOf(new URL('/cctrace/demo/_next/static/app.js', page), page)).toBe('network');
  });

  it('keeps a public page away from a daemon on the machine of the visitor', () => {
    expect(routeOf(new URL('http://127.0.0.1:12345/v1/profiles'), page)).toBe('blocked');
    expect(routeOf(new URL('http://localhost:12345/v1/sessions'), page)).toBe('blocked');
  });

  // A demo served from a loopback address while checking it locally is still
  // the page's own origin, not a daemon.
  it('treats a loopback page origin as the page itself', () => {
    const local = 'http://127.0.0.1:3000';
    expect(routeOf(new URL('/api/auth/me', local), local)).toBe('api');
  });
});

describe('shardOf', () => {
  const cases: { path: string; shard: string }[] = JSON.parse(
    readFileSync(join(__dirname, '../../../cmd/demo-seed/testdata/replay-shards.json'), 'utf8'),
  );
  it.each(cases)('$path', (c) => {
    expect(shardOf(c.path)).toBe(c.shard);
  });
});
