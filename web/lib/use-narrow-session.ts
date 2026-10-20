'use client';

import { useSyncExternalStore } from 'react';

const query = '(max-width: 1023px)';
const subscribe = (change: () => void) => {
  const media = window.matchMedia?.(query);
  media?.addEventListener('change', change);
  return () => media?.removeEventListener('change', change);
};
const snapshot = () => window.matchMedia?.(query).matches ?? false;
const serverSnapshot = () => false;
const useNarrowSession = () => useSyncExternalStore(subscribe, snapshot, serverSnapshot);

export { useNarrowSession };
