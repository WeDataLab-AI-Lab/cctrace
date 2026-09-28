import type { ObservedBillingAccount } from './types';

type SelfExclusionState = 'collecting' | 'self-excluded' | 'admin-excluded' | 'shared';

/**
 * What the Settings row may offer for one account, matching what the server
 * accepts (handlers_self_excluded_billing_accounts.go): exclude an unshared
 * account that is not excluded yet, include one the viewer excluded
 * themselves. An exclusion someone else made, and a shared account, are
 * read-only -- offering a toggle the server refuses would only fail on click.
 */
const selfExclusionState = (a: ObservedBillingAccount): SelfExclusionState => {
  if (a.excluded) return a.self_registered ? 'self-excluded' : 'admin-excluded';
  if (a.shared) return 'shared';
  return 'collecting';
};

export { selfExclusionState };
export type { SelfExclusionState };
