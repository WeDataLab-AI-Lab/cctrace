import { afterEach, expect, it, vi } from 'vitest';
import { fetchSessionOverviewCount } from './api';

afterEach(() => vi.unstubAllGlobals());

it('sends a count-only opt-out explicitly while retaining ownership filters and cancellation', async () => {
  const fetch = vi.fn(async () => Response.json({ count: 3, unattributed: 0 }));
  vi.stubGlobal('fetch', fetch);
  const controller = new AbortController();
  const result = await fetchSessionOverviewCount({ includeClassificationCounts: false, classification: 'working', loginEmail: 'account', agent: 'codex', assembled: true, source: 'interactive', projectHashes: ['p'], since: '2026-10-01', until: '2026-10-02' }, controller.signal);
  const [url, init] = fetch.mock.calls[0] as unknown as [string, RequestInit];
  const params = new URL(url).searchParams;
  expect(params.get('include_classification_counts')).toBe('0');
  for (const [key, value] of Object.entries({ classification: 'working', login_email: 'account', agent: 'codex', assembled: '1', source: 'interactive', project_hashes: 'p', since: '2026-10-01', until: '2026-10-02' })) expect(params.get(key)).toBe(value);
  expect(init.signal).toBe(controller.signal);
  expect(result.classification_counts).toBeUndefined();
});

it('keeps the legacy default/full request and response', async () => {
  const buckets = { working: 3, unsorted: 0, consulting: 0, later: 0, done: 0 };
  const fetch = vi.fn(async () => Response.json({ count: 9, unattributed: 0, classification_counts: buckets }));
  vi.stubGlobal('fetch', fetch);
  expect((await fetchSessionOverviewCount()).classification_counts).toEqual(buckets);
  expect(new URL((fetch.mock.calls[0] as unknown as [string])[0]).searchParams.has('include_classification_counts')).toBe(false);
  await fetchSessionOverviewCount({ includeClassificationCounts: true });
  expect(new URL((fetch.mock.calls[1] as unknown as [string])[0]).searchParams.get('include_classification_counts')).toBe('1');
});
