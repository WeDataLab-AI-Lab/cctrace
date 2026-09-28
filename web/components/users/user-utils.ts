import type { CostSummary } from '@/lib/types';

/** Avatar colors cycling per user index (from pencil design) */
const AVATAR_COLORS = ['var(--cat-1)', 'var(--cat-2)', 'var(--cat-3)', 'var(--cat-4)', 'var(--cat-5)'];

interface DisplayName {
  label: string;
  shortName: string;
  unconfigured: boolean;
}

const shortId = (id: string): string => id.slice(0, 8);

const resolveDisplayName = (user: { profile_email: string; user_id?: string }): DisplayName => {
  if (user.user_id) return { label: user.user_id, shortName: shortId(user.user_id), unconfigured: !user.profile_email };
  if (user.profile_email) return { label: user.profile_email, shortName: user.profile_email.split('@')[0], unconfigured: false };
  return { label: 'Unknown', shortName: 'Unknown', unconfigured: true };
};

const daysAgo = (n: number): string => new Date(Date.now() - n * 86400 * 1000).toISOString();

const fmt = (n: number): string =>
  n >= 1_000_000 ? `${(n / 1_000_000).toFixed(1)}M` : n >= 1000 ? `${(n / 1000).toFixed(0)}K` : String(n);

const userKey = (u: CostSummary): string => `${u.profile_email}|${u.user_id}`;

export { AVATAR_COLORS, resolveDisplayName, daysAgo, fmt, userKey };
export type { DisplayName };
