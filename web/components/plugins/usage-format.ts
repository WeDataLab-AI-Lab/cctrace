import type { CombinedUsage } from './usage-types';

const fmtTokens = (n: number) => {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return String(n);
};

const normalizeName = (name: string) => name.replace(/^\/+/, '') || 'unknown';

const totalCalls = (row: Pick<CombinedUsage, 'pluginCalls' | 'skillCalls'>) =>
  row.pluginCalls + row.skillCalls;

const avgTokens = (totalTokens: number, calls: number) => {
  if (calls <= 0) return '-';
  return fmtTokens(Math.round(totalTokens / calls));
};

/**
 * Identity here is user_id, and only user_id.
 *
 * It used to be `profileEmail || loginEmail || userID`, which substituted one
 * identity for another whenever the first was blank. Those are three different
 * facts -- the machine profile, the billing login, and the user -- and one user
 * carries several of the first two, so the chain did not merely pick an uglier
 * name: it grouped the same person under a different key depending on which
 * columns their row happened to carry. Plugin rows passed no login email and
 * skill rows did, so one person with a blank profile_email split into a
 * user_id bucket under plugins and a login_email bucket under skills, matching
 * neither their ordinary profile_email bucket.
 *
 * user_id is the right key because it is the one that is never blank: measured
 * on the development database, 0 of 3,859,971 session_records and 0 of
 * 1,711,274 otel_events lack it, while 4,307 otel_events lack profile_email.
 */
const userKey = (userID?: string) => userID ?? '';

/**
 * buildUserNames maps each user_id to a human-readable name once, across every
 * row, so the name a person is shown under does not depend on which row is
 * being rendered. This is the same shape as cost-user-stack's userLabel: the
 * key identifies, the name is looked up.
 */
const buildUserNames = (
  rows: { user_id?: string; profile_email?: string }[],
): Map<string, string> => {
  const names = new Map<string, string>();
  for (const r of rows) {
    const id = userKey(r.user_id);
    if (id === '' || !r.profile_email || names.has(id)) continue;
    names.set(id, r.profile_email);
  }
  return names;
};

/**
 * userLabel names a user_id. A user whose rows never carried a profile email
 * falls back to the id itself, shortened when it is a long hash -- an opaque
 * name for a known user, which is not the same as borrowing another identity.
 */
const userLabel = (userID: string, names: Map<string, string>): string => {
  const name = names.get(userID);
  if (name) return name;
  if (userID.length > 16 && /^[0-9a-f]+$/i.test(userID)) return userID.slice(0, 8);
  return userID;
};

export { fmtTokens, normalizeName, totalCalls, avgTokens, buildUserNames, userKey, userLabel };
