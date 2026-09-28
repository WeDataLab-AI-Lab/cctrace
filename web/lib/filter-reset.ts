// Explicit filter reset — the one place that decides what "start over" means.
//
// Auto-refresh preserves what the user is looking at; only an explicit act clears it. That
// act is the sidebar logo click. Ctrl+R is left alone: it is the browser's, not ours.
//
// The axis here is user intent, not storage location. A view filter is reset; a user
// setting is not. Appearance (theme, accent) and auth live in localStorage too
// and must survive — they are how the user wants the app to look and who they are, not a
// question they asked of the data. Navigation state (which tab was open) also survives.

/** localStorage keys owned by the account scope and the agent scope. */
const ACCOUNT_KEY = 'cctrace.selectedAccount';
const AGENT_KEY = 'cctrace.selectedAgent';

/** Fired after the filter keys are cleared, so live components drop to their defaults. */
const FILTER_RESET_EVENT = 'cctrace:filter-reset';

/**
 * Whether a localStorage key holds a view filter. `filter:` is the prefix usePersistedState
 * callers use for query filters; the two scope keys are named individually rather than by a
 * `cctrace.` prefix, which would also sweep up appearance and auth.
 */
const isFilterKey = (key: string): boolean =>
  key.startsWith('filter:') || key === ACCOUNT_KEY || key === AGENT_KEY;

/**
 * Clear every stored view filter and tell mounted components to follow.
 *
 * Clearing storage alone is not enough: the values are already in React state and are only
 * read from storage at mount, so a reset issued while the page stays mounted would appear
 * to do nothing until reload. The event is what makes it immediate.
 */
const resetFilters = (): void => {
  try {
    for (const key of Object.keys(localStorage)) {
      if (isFilterKey(key)) localStorage.removeItem(key);
    }
  } catch { /* ignore */ }
  window.dispatchEvent(new Event(FILTER_RESET_EVENT));
};

export { FILTER_RESET_EVENT, ACCOUNT_KEY, AGENT_KEY, isFilterKey, resetFilters };
