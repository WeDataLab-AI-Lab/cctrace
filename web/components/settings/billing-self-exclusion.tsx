'use client';

import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { EyeOff } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { listObservedBillingAccounts, removeSelfExcludedBillingAccount, selfExcludeBillingAccount } from '@/lib/api';
import { selfExclusionState } from '@/lib/self-exclusion';
import { keepKnownRebuildPending, usageRebuildPollInterval } from '@/lib/usage-rebuild';
import { useRefreshWhenRebuilt } from '@/lib/use-refresh-when-rebuilt';
import { UsageRebuildNote } from '@/components/common/usage-rebuild-note';
import type { SelfExclusionState } from '@/lib/self-exclusion';
import type { ExclusionList, ObservedBillingAccount } from '@/lib/types';

interface AccountRowProps {
  account: ObservedBillingAccount;
  disabled: boolean;
  onExclude: (account: ObservedBillingAccount) => void;
  onInclude: (account: ObservedBillingAccount) => void;
}

const QUERY_KEY = ['self-exclusions', 'billing-accounts'] as const;

const STATUS_TEXT: Record<SelfExclusionState, string> = {
  collecting: 'Collected',
  'self-excluded': 'Excluded by you',
  'admin-excluded': 'Excluded by an admin',
  shared: 'Shared — only an admin can exclude it',
};

const AccountRow = ({ account, disabled, onExclude, onInclude }: AccountRowProps) => {
  const state = selfExclusionState(account);
  const handleExclude = () => onExclude(account);
  const handleInclude = () => onInclude(account);

  return (
    <li className="flex flex-wrap items-center justify-between gap-3 py-3">
      <div className="min-w-0">
        <p className="text-[12px] text-ink-3">{account.billing_provider}</p>
        <code className="block truncate font-mono text-[13px] text-ink">{account.account_id}</code>
        <p className="mt-0.5 text-[11px] text-ink-3">{STATUS_TEXT[state]}</p>
      </div>
      {state === 'collecting' && (
        <Button type="button" variant="outline" size="sm" disabled={disabled} onClick={handleExclude}>
          Exclude
        </Button>
      )}
      {state === 'self-excluded' && (
        <Button type="button" variant="outline" size="sm" disabled={disabled} onClick={handleInclude}>
          Include again
        </Button>
      )}
    </li>
  );
};

const BillingSelfExclusion = () => {
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState<ObservedBillingAccount | null>(null);
  // Read on demand: the list changes only when the viewer acts on it here. It
  // polls only while the usage rebuild an exclusion queued is pending.
  const fetchAccounts = async () =>
    keepKnownRebuildPending(
      await listObservedBillingAccounts(),
      queryClient.getQueryData<ExclusionList<ObservedBillingAccount>>(QUERY_KEY),
    );
  const accountsQuery = useQuery({
    queryKey: QUERY_KEY,
    queryFn: fetchAccounts,
    staleTime: Infinity,
    refetchInterval: (query) => usageRebuildPollInterval(query.state.data),
  });
  useRefreshWhenRebuilt(accountsQuery.data?.usage_rebuild_pending);

  const handleSettled = () => void queryClient.invalidateQueries({ queryKey: QUERY_KEY });
  const excludeMutation = useMutation({
    mutationFn: (a: ObservedBillingAccount) => selfExcludeBillingAccount(a.billing_provider, a.account_id),
    onSuccess: () => setConfirming(null),
    onSettled: handleSettled,
  });
  const includeMutation = useMutation({
    mutationFn: (a: ObservedBillingAccount) => removeSelfExcludedBillingAccount(a.billing_provider, a.account_id),
    onSettled: handleSettled,
  });

  // Excluding hides the account's data for everyone and refuses what arrives
  // from it, so the row button only asks; the confirmation below does it.
  const handleExclude = (a: ObservedBillingAccount) => setConfirming(a);
  const handleCancelExclude = () => setConfirming(null);
  const handleConfirmExclude = () => {
    if (!confirming) return;
    excludeMutation.mutate(confirming);
  };
  const handleInclude = (a: ObservedBillingAccount) => includeMutation.mutate(a);

  const pending = excludeMutation.isPending || includeMutation.isPending;
  const error = excludeMutation.error ?? includeMutation.error ?? accountsQuery.error;
  const accounts = accountsQuery.data?.accounts ?? [];

  return (
    <section className="mb-5 rounded-lg border border-border bg-surface p-8">
      <header className="mb-4">
        <div className="mb-1 flex items-center gap-2.5">
          <EyeOff size={18} className="text-brand" />
          <h2 className="text-[16px] font-semibold text-ink">Billing Accounts in Your Data</h2>
        </div>
        <p className="ml-[30px] text-[12px] text-ink-3">
          Exclude a personal account used on the same machine. Its sessions, usage and cost are hidden from the
          dashboard for everyone, and session logs synced from it are no longer stored. Data already stored is
          kept hidden and comes back if you include the account again.
        </p>
      </header>

      {accountsQuery.isLoading && <p className="text-[12px] text-ink-3">Loading…</p>}
      <UsageRebuildNote pending={accountsQuery.data?.usage_rebuild_pending} className="mb-4" />
      {confirming && (
        <div className="mb-4 rounded-lg border border-warning/40 bg-warning-soft p-4" role="alert">
          <p className="text-[13px] font-medium text-warning-strong">Exclude “{confirming.account_id}”?</p>
          <p className="mt-1 text-[12px] leading-5 text-warning-strong/80">
            Its data is hidden from everyone on the dashboard, and session logs synced from it are discarded until you
            include it again.
          </p>
          <div className="mt-3 flex gap-2">
            <Button type="button" size="sm" onClick={handleConfirmExclude} disabled={pending}>
              {excludeMutation.isPending ? 'Excluding…' : 'Confirm exclusion'}
            </Button>
            <Button type="button" size="sm" variant="outline" onClick={handleCancelExclude} disabled={pending}>
              Cancel
            </Button>
          </div>
        </div>
      )}

      {!accountsQuery.isLoading && !accountsQuery.isError && accounts.length === 0 && (
        <p className="text-[12px] text-ink-3">No billing accounts seen in your data yet.</p>
      )}
      {accounts.length > 0 && (
        <ul className="divide-y divide-border-subtle">
          {accounts.map((a) => (
            <AccountRow
              key={`${a.billing_provider}/${a.account_id}`}
              account={a}
              disabled={pending}
              onExclude={handleExclude}
              onInclude={handleInclude}
            />
          ))}
        </ul>
      )}
      {error && <p className="mt-3 text-sm text-danger">{error.message}</p>}
    </section>
  );
};

export { BillingSelfExclusion };
