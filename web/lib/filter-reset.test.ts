import { describe, expect, it } from 'vitest';
import { OWN_ACCOUNTS_KEY, isFilterKey } from './filter-reset';

describe('isFilterKey', () => {
  it('claims every filter:* key', () => {
    expect(isFilterKey('filter:viewMode')).toBe(true);
    expect(isFilterKey('filter:modelCategory')).toBe(true);
    expect(isFilterKey('filter:groupBy')).toBe(true);
    expect(isFilterKey('filter:modelFilter')).toBe(true);
    expect(isFilterKey('filter:userFilter')).toBe(true);
  });

  it('claims the account and agent scopes', () => {
    expect(isFilterKey('cctrace.selectedAccount')).toBe(true);
    expect(isFilterKey('cctrace.selectedAgent')).toBe(true);
  });

  // Appearance and auth are user settings, not a view of the data. Resetting them on a
  // logo click would throw away the theme the user chose, or log them out.
  it('does not claim appearance or auth state', () => {
    expect(isFilterKey('cctrace.appearance')).toBe(false);
    expect(isFilterKey('cctrace.token')).toBe(false);
  });

  // "Mine" trims which chips are offered; it is how the viewer wants the header to
  // look, not a question asked of the data, so a reset keeps it.
  it('does not claim the own-accounts chip setting', () => {
    expect(isFilterKey(OWN_ACCOUNTS_KEY)).toBe(false);
  });

  // A remembered tab is where the user is, not what they are filtering by.
  it('does not claim navigation state', () => {
    expect(isFilterKey('users:activeTab')).toBe(false);
  });

  it('does not claim a key that merely mentions a filter', () => {
    expect(isFilterKey('cctrace.filter')).toBe(false);
    expect(isFilterKey('myfilter:viewMode')).toBe(false);
  });
});
