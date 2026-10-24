import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { replayKey } from './replay-key';

interface SharedCase {
  name: string;
  now: string;
  method: string;
  path: string;
  query: string;
  key: string;
}

// The same cases the recorder's Go test reads: a key the two sides spell
// differently is a recorded response the demo can never find.
const cases: SharedCase[] = JSON.parse(
  readFileSync(join(__dirname, '../../../cmd/demo-seed/testdata/replay-keys.json'), 'utf8'),
);

describe('replayKey', () => {
  it.each(cases)('$name', (c) => {
    expect(replayKey(c.method, c.path, new URLSearchParams(c.query), Date.parse(c.now)).key).toBe(c.key);
  });

  it('separates clock values from the rest for near matching', () => {
    const now = Date.parse('2026-10-24T03:00:00Z');
    const got = replayKey('GET', '/api/weekly-insights', new URLSearchParams('since=2026-10-12T15:00:00.000Z&until=2026-10-19T15:00:00.000Z'), now);
    expect(got.template).toBe('GET /api/weekly-insights?since=@&until=@');
    expect(got.offsets).toEqual([-16560, -6480]);
  });
});
