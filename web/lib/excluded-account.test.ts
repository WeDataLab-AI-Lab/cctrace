import { describe, expect, it } from 'vitest';
import { canSubmitExclusion, isValidLoginEmail, linkedBillingAccountsLabel, normalizeLoginEmail } from './excluded-account';

describe('normalizeLoginEmail', () => {
  it('trims and lowercases, matching the Go server normalizer', () => {
    expect(normalizeLoginEmail('  User@Example.com  ')).toBe('user@example.com');
  });
});

describe('isValidLoginEmail', () => {
  it('accepts a normalized email-shaped string', () => {
    expect(isValidLoginEmail('user@example.com')).toBe(true);
    expect(isValidLoginEmail('  User@Example.com  ')).toBe(true);
  });

  it('rejects empty or non-email input', () => {
    expect(isValidLoginEmail('')).toBe(false);
    expect(isValidLoginEmail('   ')).toBe(false);
    expect(isValidLoginEmail('not-an-email')).toBe(false);
  });
});

describe('canSubmitExclusion', () => {
  const base = {
    loginEmail: 'user@example.com',
    reason: 'spam account',
    pending: false,
    confirmedTyped: 'EXCLUDE',
    requiredConfirmation: 'EXCLUDE',
  };

  it('allows submit when email, reason, and typed confirmation all check out', () => {
    expect(canSubmitExclusion(base)).toBe(true);
  });

  // Fail-closed: any one of these missing must block submit.
  it('blocks submit on invalid email', () => {
    expect(canSubmitExclusion({ ...base, loginEmail: 'not-an-email' })).toBe(false);
  });

  it('blocks submit on empty reason', () => {
    expect(canSubmitExclusion({ ...base, reason: '   ' })).toBe(false);
  });

  it('blocks submit while a mutation is pending', () => {
    expect(canSubmitExclusion({ ...base, pending: true })).toBe(false);
  });

  it('blocks submit when the typed confirmation does not match', () => {
    expect(canSubmitExclusion({ ...base, confirmedTyped: 'exclude' })).toBe(false);
    expect(canSubmitExclusion({ ...base, confirmedTyped: '' })).toBe(false);
  });
});

// #715: an excluded address also hides the billing accounts it was seen with.
// The row has to say so, or the admin cannot tell what the exclusion reaches.
describe('linkedBillingAccountsLabel', () => {
  it('names each linked account by provider and id', () => {
    expect(linkedBillingAccountsLabel([
      { billing_provider: 'openai', account_id: 'ab0a9694-6830' },
      { billing_provider: 'anthropic', account_id: 'acct-9' },
    ])).toBe('Also hides Codex ab0a9694-6830, Claude acct-9');
  });

  it('is empty when the address reaches no billing account', () => {
    expect(linkedBillingAccountsLabel([])).toBe('');
    expect(linkedBillingAccountsLabel(undefined)).toBe('');
  });

  it('keeps an unknown provider as-is', () => {
    expect(linkedBillingAccountsLabel([{ billing_provider: 'gjc', account_id: 'x' }])).toBe('Also hides gjc x');
  });
});
