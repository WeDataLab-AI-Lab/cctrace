import { describe, expect, it, vi } from 'vitest';
import type { SessionOverview } from './types';
import { createPersonalNoteNavigator } from './personal-note-navigation';

const overview = (id: string): SessionOverview => ({
  session_id: id,
  profile_email: 'owner@example.com',
  model: '',
  agent: 'claude',
  start_time: '2026-10-01T00:00:00Z',
  end_time: '2026-10-01T01:00:00Z',
  input_tokens: 10,
  output_tokens: 20,
  cost_usd: 0.1,
  event_count: 3,
  has_sync: true,
});
const deferred = () => {
  let resolve!: (value: SessionOverview) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<SessionOverview>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};

describe('personal note navigation', () => {
  it('opens an actual overview even when it is not in the loaded session list', async () => {
    const found = vi.fn();
    const missing = vi.fn();
    const fetch = vi.fn(async () => overview('older'));
    await createPersonalNoteNavigator(fetch, found, missing).open('older');
    expect(fetch).toHaveBeenCalledWith('older');
    expect(found).toHaveBeenCalledWith(overview('older'));
    expect(missing).not.toHaveBeenCalled();
  });

  it('does not allow the late A response to replace the newer B selection', async () => {
    const a = deferred();
    const b = deferred();
    const found = vi.fn();
    const missing = vi.fn();
    const navigator = createPersonalNoteNavigator((id) => id === 'A' ? a.promise : b.promise, found, missing);
    const first = navigator.open('A');
    const second = navigator.open('B');
    b.resolve(overview('B'));
    await second;
    a.resolve(overview('A'));
    await first;
    expect(found).toHaveBeenCalledTimes(1);
    expect(found).toHaveBeenCalledWith(overview('B'));
    expect(missing).not.toHaveBeenCalled();
  });

  it('ignores an old owner response after switching users with unchanged filters', async () => {
    const oldOwner = deferred();
    const found = vi.fn();
    const unavailable = vi.fn();
    const navigator = createPersonalNoteNavigator(() => oldOwner.promise, found, unavailable);
    const pending = navigator.open('old-owner-session');
    navigator.cancel(); // auth owner changed; page reset key now includes the auth user id
    oldOwner.resolve(overview('old-owner-session'));
    await pending;
    expect(found).not.toHaveBeenCalled();
    expect(unavailable).not.toHaveBeenCalled();
  });

  it('leaves selection intact when lookup fails, returns mismatched data, or was cancelled', async () => {
    const found = vi.fn();
    const missing = vi.fn();
    const a = deferred();
    const navigator = createPersonalNoteNavigator(() => a.promise, found, missing);
    const pending = navigator.open('A');
    navigator.cancel();
    a.reject(new Error('denied'));
    await pending;
    expect(found).not.toHaveBeenCalled();
    expect(missing).not.toHaveBeenCalled();

    await createPersonalNoteNavigator(async () => overview('other'), found, missing).open('A');
    await createPersonalNoteNavigator(async () => ({ ...overview('C'), start_time: 'not a date' }), found, missing).open('C');
    await createPersonalNoteNavigator(async () => {
      const incomplete = { ...overview('D') };
      delete (incomplete as Partial<SessionOverview>).cost_usd;
      return incomplete;
    }, found, missing).open('D');
    await createPersonalNoteNavigator(async () => { throw new Error('not found'); }, found, missing).open('B');
    expect(found).not.toHaveBeenCalled();
    expect(missing.mock.calls).toEqual([['A'], ['C'], ['D'], ['B']]);
  });
});
