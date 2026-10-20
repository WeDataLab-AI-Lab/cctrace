// Keeps the local daemon pairing in the browser tab so the next visit reconnects
// without pasting the endpoint and token again. It lives in sessionStorage, not
// localStorage: closing the tab forgets it and asks for a fresh pairing. The
// token is never written to the page URL, a server request, or a log.

export interface LocalConnectionPair {
  origin: string;
  token: string;
}

const KEY = 'cctrace:local-connection';
export const LOCAL_CONNECTION_CHANGED = 'cctrace:local-connection-changed';

let activePair: LocalConnectionPair | null = null;

const available = (): boolean => typeof window !== 'undefined' && !!window.sessionStorage;

const saveConnection = (pair: LocalConnectionPair): void => {
  if (!available() || !pair.origin || !pair.token) return;
  activePair = { ...pair };
  try {
    window.sessionStorage.setItem(KEY, JSON.stringify(pair));
  } catch {
    // A full or blocked storage keeps the connection manual rather than failing.
  }
  window.dispatchEvent(new Event(LOCAL_CONNECTION_CHANGED));
};

const setActiveConnection = (pair: LocalConnectionPair | null): void => {
  activePair = pair ? { ...pair } : null;
  if (typeof window !== 'undefined') window.dispatchEvent(new Event(LOCAL_CONNECTION_CHANGED));
};

const loadConnection = (): LocalConnectionPair | null => {
  if (!available()) return null;
  if (activePair?.origin && activePair.token) return { ...activePair };
  try {
    const raw = window.sessionStorage.getItem(KEY);
    if (!raw) return null;
    const value: unknown = JSON.parse(raw);
    if (typeof value !== 'object' || value === null) return null;
    const pair = value as Partial<LocalConnectionPair>;
    if (typeof pair.origin !== 'string' || typeof pair.token !== 'string') return null;
    if (!pair.origin || !pair.token) return null;
    return { origin: pair.origin, token: pair.token };
  } catch {
    return null;
  }
};

const clearConnection = (): void => {
  activePair = null;
  if (!available()) return;
  try {
    window.sessionStorage.removeItem(KEY);
  } catch {
    // Nothing to recover; the stale value is ignored on the next load anyway.
  }
  window.dispatchEvent(new Event(LOCAL_CONNECTION_CHANGED));
};

export { saveConnection, setActiveConnection, loadConnection, clearConnection };
