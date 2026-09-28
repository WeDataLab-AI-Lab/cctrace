/**
 * Mirrors the Go server's normalizeBillingKey
 * (internal/store/postgres_excluded_billing_accounts.go) so the client can
 * predict the key the server will store and match on.
 *
 * The provider is folded and the account id is not. An address is
 * case-insensitive, which is why normalizeLoginEmail lowercases; an account id
 * is an opaque token from the provider, so two ids differing only in case are
 * two accounts and folding one into the other would exclude an account nobody
 * asked to exclude.
 */
function normalizeBillingKey(provider: string, accountID: string): { provider: string; accountID: string } {
  return { provider: provider.trim().toLowerCase(), accountID: accountID.trim() };
}

/**
 * Matches the server's validation (handlers_excluded_billing_accounts.go): both
 * halves must be non-empty after trimming. There is no shape to check beyond
 * that -- an account id is whatever the provider issued, so rejecting anything
 * that merely looks unusual would block a valid exclusion.
 */
function isValidBillingKey(provider: string, accountID: string): boolean {
  const key = normalizeBillingKey(provider, accountID);
  return key.provider.length > 0 && key.accountID.length > 0;
}

/**
 * Fail-closed submit gate, the same shape as canSubmitExclusion: exclusion is a
 * dashboard-wide action, so the button stays disabled unless the key is valid,
 * a reason is given, and the typed confirmation matches exactly.
 */
function canSubmitBillingExclusion(opts: {
  provider: string;
  accountID: string;
  reason: string;
  pending: boolean;
  confirmedTyped: string;
  requiredConfirmation: string;
}): boolean {
  return (
    isValidBillingKey(opts.provider, opts.accountID) &&
    opts.reason.trim().length > 0 &&
    !opts.pending &&
    opts.confirmedTyped === opts.requiredConfirmation
  );
}

export { normalizeBillingKey, isValidBillingKey, canSubmitBillingExclusion };
