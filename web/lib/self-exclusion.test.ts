import { describe, expect, it } from 'vitest';
import { selfExclusionState } from './self-exclusion';

const base = { billing_provider: 'openai', account_id: 'acct', shared: false, excluded: false, self_registered: false };

// The toggle may only offer what the server will accept: exclude an unshared
// account that is not excluded yet, and include one the viewer excluded
// themselves. Everything else is read-only.
describe('selfExclusionState', () => {
  it('offers to exclude an unshared account that is not excluded', () => {
    expect(selfExclusionState(base)).toBe('collecting');
  });

  it('offers to include an account the viewer excluded themselves', () => {
    expect(selfExclusionState({ ...base, excluded: true, self_registered: true })).toBe('self-excluded');
  });

  // An admin's exclusion (or one reached through an excluded address) is not the
  // viewer's to take back.
  it('leaves an exclusion someone else made read-only', () => {
    expect(selfExclusionState({ ...base, excluded: true })).toBe('admin-excluded');
  });

  // Excluding an account that bills several people would hide all of them.
  it('leaves a shared account to an admin', () => {
    expect(selfExclusionState({ ...base, shared: true })).toBe('shared');
  });

  it('reports an excluded shared account as excluded, not shared', () => {
    expect(selfExclusionState({ ...base, shared: true, excluded: true })).toBe('admin-excluded');
  });
});
