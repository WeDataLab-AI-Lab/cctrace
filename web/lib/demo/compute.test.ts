import { describe, expect, it } from 'vitest';
import { compute } from './compute';
import type { Facts, QuotaSample } from './compute';

// Rows are listed one per line and turned into the columnar facts file shape.
interface Row {
  ts: string;
  model: string | null;
  user: string | null;
  profile: string | null;
  login: string | null;
  agent: string | null;
  billing: string | null;
  cost: number;
}

const factsOf = (rows: Row[]): Facts => {
  const strings: string[] = [];
  const ref = (s: string | null) => {
    if (s === null) return -1;
    const i = strings.indexOf(s);
    return i >= 0 ? i : strings.push(s) - 1;
  };
  return {
    strings,
    ts: rows.map((r) => Date.parse(r.ts) * 1000 + 250),
    model: rows.map((r) => ref(r.model)),
    user_id: rows.map((r) => ref(r.user)),
    profile_email: rows.map((r) => ref(r.profile)),
    login_email: rows.map((r) => ref(r.login)),
    user_team: rows.map(() => ref('Data')),
    agent: rows.map((r) => ref(r.agent)),
    billing_provider: rows.map((r) => ref(r.billing)),
    cost_usd: rows.map((r) => r.cost),
    input_tokens: rows.map(() => 10),
    output_tokens: rows.map(() => 2),
  };
};

const IVY = { user: 'ivy', profile: 'ivy@example.com', login: 'ivy@example.com' };
const facts = factsOf([
  // 23:30 UTC on Sunday is 08:30 Monday in Seoul.
  { ts: '2026-10-18T23:30:00Z', model: 'claude-opus-5-5', ...IVY, agent: 'claude', billing: 'anthropic', cost: 1 },
  { ts: '2026-10-18T23:40:00Z', model: 'gpt-6.1-sol', ...IVY, agent: 'codex', billing: 'openai', cost: 2 },
  { ts: '2026-10-18T23:45:00Z', model: '', ...IVY, agent: 'claude', billing: 'anthropic', cost: 0 },
  { ts: '2026-10-19T00:10:00Z', model: 'claude-opus-5-5', user: 'yuki', profile: 'yuki@example.com', login: 'yuki2@example.com', agent: 'claude', billing: 'anthropic', cost: 4 },
  { ts: '2026-10-19T00:20:00Z', model: 'claude-opus-5-5', user: null, profile: 'weekly@example.com', login: '', agent: 'weekly', billing: 'anthropic', cost: 8 },
  { ts: '2026-10-19T00:30:00Z', model: 'local-llm', ...IVY, agent: null, billing: null, cost: 16 },
]);
const NOW = Date.parse('2026-10-24T00:00:00Z');
const run = (path: string, query: string, quota: QuotaSample[] = []) => compute(path, new URLSearchParams(query), facts, quota, NOW);
const SINCE = 'since=2026-10-18T00:00:00.000Z&until=2026-10-20T00:00:00.000Z';

describe('timeseries', () => {
  it('buckets in the requested time zone and keys the weekly runs apart', () => {
    expect(run('/api/stats/timeseries-by-user', `granularity=hour&${SINCE}&tz=Asia/Seoul`)).toEqual([
      { date: '2026-10-19T08:00:00', user_id: 'ivy', profile_email: 'ivy@example.com', cost_usd: 3, input_tokens: 30, output_tokens: 6, event_count: 3 },
      { date: '2026-10-19T09:00:00', user_id: 'ivy', profile_email: 'ivy@example.com', cost_usd: 16, input_tokens: 10, output_tokens: 2, event_count: 1 },
      { date: '2026-10-19T09:00:00', user_id: 'weekly', profile_email: 'weekly@example.com', cost_usd: 8, input_tokens: 10, output_tokens: 2, event_count: 1 },
      { date: '2026-10-19T09:00:00', user_id: 'yuki', profile_email: 'yuki@example.com', cost_usd: 4, input_tokens: 10, output_tokens: 2, event_count: 1 },
    ]);
  });

  it('starts a week on Monday in the requested time zone', () => {
    const seoul = run('/api/stats/timeseries-by-model', `granularity=week&${SINCE}&tz=Asia/Seoul`) as { date: string }[];
    const utc = run('/api/stats/timeseries-by-model', `granularity=week&${SINCE}&tz=UTC`) as { date: string }[];
    expect(new Set(seoul.map((r) => r.date))).toEqual(new Set(['2026-10-19']));
    expect(new Set(utc.map((r) => r.date))).toEqual(new Set(['2026-10-12', '2026-10-19']));
  });

  it('reads "other" and "compatible" as the server does, without NULLs', () => {
    expect(run('/api/stats/timeseries-by-model', `granularity=day&${SINCE}&agent=other&tz=UTC`)).toEqual([]);
    expect(run('/api/stats/timeseries-by-model', `granularity=day&${SINCE}&model_category=compatible&tz=UTC`)).toEqual([]);
  });
});

describe('cost', () => {
  it('groups by user and model with the server defaults for a missing agent', () => {
    const rows = run('/api/cost/by-user', SINCE) as { user_id?: string; model: string; agent: string; billing_provider: string; total_cost: number; login_emails?: string[] }[];
    expect(rows.map((r) => [r.user_id, r.model, r.agent, r.billing_provider, r.total_cost, r.login_emails])).toEqual([
      ['ivy', 'local-llm', 'claude', 'anthropic', 16, ['ivy@example.com']],
      ['weekly', 'claude-opus-5-5', 'weekly', 'anthropic', 8, undefined],
      ['yuki', 'claude-opus-5-5', 'claude', 'anthropic', 4, ['yuki2@example.com']],
      ['ivy', 'gpt-6.1-sol', 'codex', 'openai', 2, ['ivy@example.com']],
      ['ivy', 'claude-opus-5-5', 'claude', 'anthropic', 1, ['ivy@example.com']],
    ]);
  });
});

describe('latest activity', () => {
  it('reports the newest row in the time format the server writes', () => {
    expect(run('/api/stats/latest-activity', 'agent=codex')).toEqual({ has_data: true, latest_ts: '2026-10-18T23:40:00.00025Z' });
    expect(run('/api/stats/latest-activity', 'agent=other')).toEqual({ has_data: false });
  });
});

describe('quota samples', () => {
  const sample = (at: string, pct: number): QuotaSample => ({ billing_provider: 'anthropic', account_id: 'a1', window_key: 'session', sampled_at: at, used_pct: pct });
  const all = [sample('2026-10-19T00:00:00Z', 1), sample('2026-10-19T00:05:00Z', 2), sample('2026-10-19T00:10:00Z', 3), sample('2026-10-19T09:00:00Z', 9)];

  it('returns every reading in an open range', () => {
    expect(run('/api/quota-samples', 'from=2026-10-19T00:05:00Z', all)).toEqual(all.slice(1));
  });

  // 10 hours over 1000 points is 36-second buckets: each reading keeps its own.
  // 30 days is 2592-second buckets: the first three readings share one, and the last wins.
  it('keeps the last reading of each bucket when both ends are given', () => {
    expect(run('/api/quota-samples', 'from=2026-10-19T00:00:00Z&to=2026-10-19T10:00:00Z', all)).toHaveLength(4);
    expect(run('/api/quota-samples', 'from=2026-10-01T00:00:00Z&to=2026-10-31T00:00:00Z', all)).toEqual([all[2], all[3]]);
  });
});
