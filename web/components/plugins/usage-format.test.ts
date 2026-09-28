import { describe, expect, it } from 'vitest';
import { buildUserNames, userKey, userLabel } from './usage-format';

// Identity here is user_id. The chain this replaces -- profileEmail ||
// loginEmail || userID -- substituted one identity for another whenever the
// first was blank, and the three name different things: the machine profile,
// the billing login, and the user. One user carries several of the first two.
describe('plugin usage identity', () => {
  // The failure this locks out: plugin rows passed no login email and skill
  // rows did, so one person with a blank profile_email landed in a user_id
  // bucket under plugins and a login_email bucket under skills -- two entries
  // for one person, neither matching their ordinary profile_email bucket.
  it('gives one user one key whichever columns their row carries', () => {
    const rows = [
      { user_id: 'u1', profile_email: 'someone@example.test' },
      { user_id: 'u1', profile_email: '' },
      { user_id: 'u1' },
    ];
    const keys = new Set(rows.map((r) => userKey(r.user_id)));
    expect(keys).toEqual(new Set(['u1']));
  });

  // A name is looked up for a key, not swapped in for a missing one, so a row
  // that lost its profile email still shows the name its user is known by.
  it('names every row of a user the same, including rows without a profile email', () => {
    const names = buildUserNames([
      { user_id: 'u1', profile_email: 'someone@example.test' },
      { user_id: 'u1', profile_email: '' },
    ]);
    expect(userLabel('u1', names)).toBe('someone@example.test');
  });

  // A user no row ever named is shown by their own id, shortened when it is a
  // long hash. An opaque name for a known user is not a borrowed identity.
  it('falls back to the shortened id, never to another identity', () => {
    const names = buildUserNames([{ user_id: 'u1', profile_email: 'someone@example.test' }]);
    const hash = 'a'.repeat(8) + 'b'.repeat(56);
    expect(userLabel(hash, names)).toBe('aaaaaaaa');
    expect(userLabel('short-id', names)).toBe('short-id');
  });

  // A row with no user_id is unattributed, and stays out rather than being
  // filed under whatever other column it happened to have.
  it('treats a row without a user_id as having no key', () => {
    expect(userKey(undefined)).toBe('');
    expect(userKey('')).toBe('');
  });
});
