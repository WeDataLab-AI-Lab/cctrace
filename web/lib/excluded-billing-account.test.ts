import { describe, expect, it } from 'vitest';
import { canSubmitBillingExclusion, isValidBillingKey, normalizeBillingKey } from './excluded-billing-account';

// The client predicts the key the server stores, so the two normalizations have
// to agree. They differ between the two halves on purpose.
describe('normalizeBillingKey', () => {
  it('folds the provider but keeps the account id as issued', () => {
    expect(normalizeBillingKey('  OpenAI  ', '  AcctCodex  ')).toEqual({
      provider: 'openai',
      accountID: 'AcctCodex',
    });
  });
});

describe('isValidBillingKey', () => {
  it('requires both halves', () => {
    expect(isValidBillingKey('openai', 'acct')).toBe(true);
    expect(isValidBillingKey('', 'acct')).toBe(false);
    expect(isValidBillingKey('openai', '   ')).toBe(false);
  });

  // An account id is whatever the provider issued. Rejecting one for looking
  // unusual would block a valid exclusion, so shape is not checked.
  it('accepts an account id of any shape', () => {
    expect(isValidBillingKey('openai', '77cf-not-a-uuid_x')).toBe(true);
  });
});

describe('canSubmitBillingExclusion', () => {
  const base = {
    provider: 'openai',
    accountID: 'acct',
    reason: 'left the org',
    pending: false,
    confirmedTyped: 'EXCLUDE',
    requiredConfirmation: 'EXCLUDE',
  };

  it('allows a complete, confirmed submission', () => {
    expect(canSubmitBillingExclusion(base)).toBe(true);
  });

  // Fail closed: every missing piece keeps the button disabled.
  it('blocks on a missing reason, a mistyped confirmation, or a request in flight', () => {
    expect(canSubmitBillingExclusion({ ...base, reason: '  ' })).toBe(false);
    expect(canSubmitBillingExclusion({ ...base, confirmedTyped: 'exclude' })).toBe(false);
    expect(canSubmitBillingExclusion({ ...base, pending: true })).toBe(false);
    expect(canSubmitBillingExclusion({ ...base, accountID: '' })).toBe(false);
  });
});
