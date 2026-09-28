import { describe, it, expect } from 'vitest';
import type { OtelMetric } from '@/lib/types';
import { metricKeys } from './metrics-table';

const metric = (over: Partial<OtelMetric>): OtelMetric => ({
  ts: '2026-08-11T00:00:00Z',
  metric_name: 'claude_code.token.usage',
  model: 'claude-opus-4',
  profile_email: 'a@example.com',
  session_id: 's1',
  value_int: 10,
  ...over,
} as OtelMetric);

describe('metricKeys', () => {
  it('keeps every key unchanged when a metric arrives at the head', () => {
    const older = [metric({ ts: 't2', value_int: 2 }), metric({ ts: 't1', value_int: 1 })];
    const newer = [metric({ ts: 't3', value_int: 3 }), ...older];

    const before = metricKeys(older);
    const after = metricKeys(newer);

    expect(after.slice(1)).toEqual(before);
  });

  it('distinguishes rows that collide on every displayed field', () => {
    const keys = metricKeys([metric({}), metric({}), metric({})]);
    expect(new Set(keys).size).toBe(3);
  });

  it('separates metrics that share a timestamp but differ in value', () => {
    const [a, b] = metricKeys([metric({ value_int: 1 }), metric({ value_int: 2 })]);
    expect(a).not.toBe(b);
  });
});
