import { describe, expect, it } from 'vitest';
import { createSerialQueryRunner } from './serial-query';

describe('serial query runner', () => {
  it('runs queued queries in order without overlap', async () => {
    const run = createSerialQueryRunner();
    const events: string[] = [];
    let releaseFirst!: () => void;
    const first = run(() => new Promise<void>((resolve) => {
      events.push('first-start');
      releaseFirst = () => { events.push('first-end'); resolve(); };
    }));
    const second = run(async () => { events.push('second-start'); });

    await Promise.resolve();
    expect(events).toEqual(['first-start']);
    releaseFirst();
    await Promise.all([first, second]);
    expect(events).toEqual(['first-start', 'first-end', 'second-start']);
  });

  it('skips a queued query canceled before it starts', async () => {
    const run = createSerialQueryRunner();
    const controller = new AbortController();
    let releaseFirst!: () => void;
    let canceledQueryCalls = 0;
    const first = run(() => new Promise<void>((resolve) => { releaseFirst = resolve; }));
    const canceled = run(async () => { canceledQueryCalls += 1; }, controller.signal);

    await Promise.resolve();
    controller.abort();
    await expect(canceled).rejects.toMatchObject({ name: 'AbortError' });
    releaseFirst();
    await first;
    await Promise.resolve();
    expect(canceledQueryCalls).toBe(0);
  });
});
