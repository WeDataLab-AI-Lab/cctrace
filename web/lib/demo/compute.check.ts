import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { compute, computable } from './compute';
import type { Facts, QuotaSample } from './compute';
import type { RecordedEntry } from './replayer';

// Every recorded response the demo could also compute must equal the computed
// one: the computation is what answers the windows nobody recorded, and these
// recordings are the server's own answer to compare it with.

const DATA = process.env.DEMO_DATA ?? join(__dirname, '../../public/demo-data');
const COMPUTED_SHARDS = [
  'api.stats.timeseries-by-user.json', 'api.stats.timeseries-by-model.json', 'api.cost.by-user.json',
  'api.cost.by-model.json', 'api.stats.latest-activity.json', 'api.quota-samples.json',
];

const read = <T>(path: string): T => JSON.parse(readFileSync(join(DATA, path), 'utf8')) as T;

// The recorder sent tz=Asia/Seoul, which the key leaves out.
const requestOf = (key: string, recordedAt: number) => {
  const [, rest] = key.split(' ');
  const [path, query] = rest.split('?');
  const params = new URLSearchParams(
    query.replace(/=@(-?\d+)m/g, (_, minutes: string) => `=${new Date(recordedAt + Number(minutes) * 60_000).toISOString()}`),
  );
  if (!params.has('tz')) params.set('tz', 'Asia/Seoul');
  return { path, params };
};

// Sums are added in a different order than the database adds them.
const close = (a: unknown, b: unknown): boolean => {
  if (typeof a === 'number' && typeof b === 'number') return Math.abs(a - b) <= 1e-9 * Math.max(1, Math.abs(a), Math.abs(b));
  if (Array.isArray(a) && Array.isArray(b)) return a.length === b.length && a.every((x, i) => close(x, b[i]));
  if (a && b && typeof a === 'object' && typeof b === 'object') {
    const keys = new Set([...Object.keys(a), ...Object.keys(b)]);
    return [...keys].every((k) => close((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k]));
  }
  return a === b;
};

// Rows that tie on the sort key come back in any order from the database.
const identity = (row: unknown): string =>
  JSON.stringify(Object.entries(row as Record<string, unknown>).filter(([, v]) => typeof v !== 'number').sort(([a], [b]) => (a < b ? -1 : 1)));
const sameRows = (computed: unknown, recorded: unknown): boolean => {
  if (!Array.isArray(computed) || !Array.isArray(recorded)) return close(computed, recorded);
  if (computed.length !== recorded.length) return false;
  const byIdentity = new Map(recorded.map((row) => [identity(row), row]));
  return computed.every((row) => close(row, byIdentity.get(identity(row))));
};

describe('computed answers match the recordings', () => {
  it('has a recording to check', () => {
    expect(existsSync(join(DATA, 'manifest.json')), `no recording at ${DATA}`).toBe(true);
  });

  const manifest = existsSync(join(DATA, 'manifest.json')) ? read<{ recorded_at: string; shards: string[] }>('manifest.json') : null;
  if (!manifest) return;
  const recordedAt = Date.parse(manifest.recorded_at);
  const facts = read<Facts>('facts.json');
  const quotaEntries = read<RecordedEntry[]>('manifest/api.quota-samples.json');
  const fromOf = (key: string) => Number(/[?&]from=@(-?\d+)m/.exec(key)?.[1] ?? Infinity);
  const [whole] = quotaEntries.filter((e) => !/[?&]to=/.test(e.key)).sort((a, b) => fromOf(a.key) - fromOf(b.key));
  const quota = read<QuotaSample[]>(`bodies/${whole.body}.json`);

  for (const shard of COMPUTED_SHARDS) {
    it(shard, () => {
      const mismatches: string[] = [];
      let checked = 0;
      for (const entry of read<RecordedEntry[]>(`manifest/${shard}`)) {
        const { path, params } = requestOf(entry.key, recordedAt);
        if (entry.status !== 200 || !computable(path, params)) continue;
        checked += 1;
        const recorded = read<unknown>(`bodies/${entry.body}.json`);
        if (!sameRows(compute(path, params, facts, quota, recordedAt), recorded)) mismatches.push(entry.key);
      }
      expect(checked).toBeGreaterThan(0);
      expect(mismatches.slice(0, 10)).toEqual([]);
    });
  }
});
