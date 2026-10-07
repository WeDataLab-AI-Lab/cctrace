import type { AuthUser, CostSummary } from '@/lib/types';

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

/**
 * 서버는 role=user 의 세션 조회를 호출자 본인으로 덮어쓴다(#799). 비본인 카드에서
 * 세션을 요청하면 보는 사람 본인 세션이 카드 주인 것처럼 보이므로, 요청 자체를 막는 판별.
 * user_id 가 비어 본인 여부를 알 수 없으면 숨기는 쪽(false)이 안전하다.
 */
const canViewUserSessions = (
  me: Pick<AuthUser, 'email' | 'role' | 'cctrace_user_id'> | null,
  card: { profile_email: string; user_id?: string },
): boolean => {
  if (!me) return false;
  if (me.role === 'admin') return true;
  if (me.cctrace_user_id) return card.user_id === me.cctrace_user_id;
  return card.profile_email === me.email;
};

export { AVATAR_COLORS, resolveDisplayName, daysAgo, fmt, userKey, canViewUserSessions };
export type { DisplayName };
