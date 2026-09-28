import type { BillingAccountRef } from './types';

/**
 * Mirrors the Go server's normalizeLoginEmail (internal/store/postgres_excluded_accounts.go)
 * so the client can predict the key the server will store/match on, e.g. for
 * duplicate checks before submitting. Ingest does not lowercase login_email,
 * so both sides normalize at the comparison boundary instead.
 */
function normalizeLoginEmail(loginEmail: string): string {
  return loginEmail.trim().toLowerCase();
}

/**
 * Matches the server's validation (handlers_excluded_accounts.go): a login_email
 * must be non-empty after normalizing and contain '@'. Not a full RFC 5322
 * check — the server is the source of truth; this only pre-empts an obviously
 * doomed submission.
 */
function isValidLoginEmail(loginEmail: string): boolean {
  const normalized = normalizeLoginEmail(loginEmail);
  return normalized.length > 0 && normalized.includes('@');
}

/**
 * Fail-closed submit gate: exclusion is a destructive, dashboard-wide action,
 * so the button stays disabled unless the email is valid, a reason is given,
 * and (when the destructive confirm is required) the typed confirmation
 * matches exactly.
 */
function canSubmitExclusion(opts: {
  loginEmail: string;
  reason: string;
  pending: boolean;
  confirmedTyped: string;
  requiredConfirmation: string;
}): boolean {
  return (
    isValidLoginEmail(opts.loginEmail) &&
    opts.reason.trim().length > 0 &&
    !opts.pending &&
    opts.confirmedTyped === opts.requiredConfirmation
  );
}

const PROVIDER_NAMES: Record<string, string> = { openai: 'Codex', anthropic: 'Claude' };

/**
 * Says which billing accounts an excluded address also hides (#715). An
 * address and a billing account are linked when a quota reading carried both,
 * and the exclusion reaches the account's rows that carry no address at all --
 * so the admin row has to name them, or what is hidden cannot be read off it.
 */
function linkedBillingAccountsLabel(links: readonly BillingAccountRef[] | undefined): string {
  if (!links || links.length === 0) return '';
  const names = links.map((l) => `${PROVIDER_NAMES[l.billing_provider] ?? l.billing_provider} ${l.account_id}`);
  return `Also hides ${names.join(', ')}`;
}

export { normalizeLoginEmail, isValidLoginEmail, canSubmitExclusion, linkedBillingAccountsLabel };
