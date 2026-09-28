import { describe, expect, it } from 'vitest';
import { POLL_LIVE } from './query-config';
import { keepKnownRebuildPending, usageRebuildPollInterval } from './usage-rebuild';

// The lists that show "charts are catching up" poll only while that is true,
// so the note goes away on its own once the rebuild finishes.
describe('usageRebuildPollInterval', () => {
  it('polls while a usage rebuild is pending', () => {
    expect(usageRebuildPollInterval({ accounts: [], usage_rebuild_pending: true })).toBe(POLL_LIVE);
  });

  it('stops polling once the rebuild is done', () => {
    expect(usageRebuildPollInterval({ accounts: [], usage_rebuild_pending: false })).toBe(false);
  });

  it('does not poll before the list has loaded', () => {
    expect(usageRebuildPollInterval(undefined)).toBe(false);
  });
});

// The server leaves the flag out when it could not read it. Unknown is not
// "finished": the list keeps the last state it knew, and with it the note and
// the polling.
describe('keepKnownRebuildPending', () => {
  it('keeps the last known state when the flag is missing', () => {
    expect(keepKnownRebuildPending({ accounts: [] }, { accounts: [], usage_rebuild_pending: true })).toEqual({
      accounts: [],
      usage_rebuild_pending: true,
    });
    expect(
      keepKnownRebuildPending({ accounts: [], usage_rebuild_pending: null }, { accounts: [], usage_rebuild_pending: true })
        .usage_rebuild_pending,
    ).toBe(true);
  });

  it('takes a known state as it is', () => {
    expect(
      keepKnownRebuildPending({ accounts: [], usage_rebuild_pending: false }, { accounts: [], usage_rebuild_pending: true })
        .usage_rebuild_pending,
    ).toBe(false);
  });

  it('stays unknown with nothing known before', () => {
    expect(keepKnownRebuildPending({ accounts: [] }, undefined).usage_rebuild_pending).toBeUndefined();
  });
});
