import type { ModelCategory } from '@/lib/colors';

interface ModelDatum {
  name: string;
  value: number;
}

interface UserStackDatum {
  name: string;
  [model: string]: number | string;
}

const fmt = (n: number): string =>
  n >= 1_000_000
    ? `${(n / 1_000_000).toFixed(1)}M`
    : n >= 1000
      ? `${(n / 1000).toFixed(0)}K`
      : String(n);

/**
 * Local agent/provider category match — preserves the page's original branching, but mirrors
 * lib/colors.ts matchCategory (which itself mirrors the server's billing_provider-based axis
 * in internal/store/postgres_stats.go and siblings). Keep this in sync with both.
 */
const matchCategory = (agent: string, provider: string, cat: ModelCategory): boolean => {
  if (cat === 'all') return true;
  if (cat === 'anthropic') return provider === 'anthropic';
  if (cat === 'codex') return provider === 'openai';
  if (cat === 'compatible') return provider !== 'anthropic' && provider !== 'openai';
  return true;
};

/** Coarse trend window label derived from `activeSince` against a stable `now`. */
const subtitleFromSince = (activeSince: string, now: number): string => {
  const msAgo = now - new Date(activeSince).getTime();
  const h = msAgo / 3600000;
  if (h <= 4) return 'Last 3h';
  if (h <= 73) return 'Last 72h';
  if (h <= 31 * 24) return 'Last 30d';
  if (h <= 26 * 7 * 24) return 'Last 26w';
  return 'Last 1y';
};

export { fmt, matchCategory, subtitleFromSince };
export type { ModelDatum, UserStackDatum };
