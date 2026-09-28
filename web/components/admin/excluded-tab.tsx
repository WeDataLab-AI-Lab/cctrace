'use client';

import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { listExcludedAccounts, listExcludedBillingAccounts } from '@/lib/api';
import { cn } from '@/lib/utils';
import { linkedBillingAccountsLabel } from '@/lib/excluded-account';
import { keepKnownRebuildPending, usageRebuildPollInterval } from '@/lib/usage-rebuild';
import { useRefreshWhenRebuilt } from '@/lib/use-refresh-when-rebuilt';
import { fmtCost } from '@/components/sessions/session-utils';
import { CollectingLoader } from '@/components/common/collecting-loader';
import { UsageRebuildNote } from '@/components/common/usage-rebuild-note';
import { useAuth } from '@/components/common/auth-context';
import { ExcludeAccountDialog } from './exclude-account-dialog';
import { ExcludeBillingAccountDialog } from './exclude-billing-account-dialog';
import { UnexcludeBillingDialog } from './unexclude-billing-dialog';
import { UnexcludeDialog } from './unexclude-dialog';
import type { ExcludedAccount, ExcludedBillingAccount, ExclusionList } from '@/lib/types';

const GRID_CLASS = 'grid grid-cols-[1.5fr_1.5fr_1fr_110px_110px_110px] items-center';
// A billing account hides quota readings and, since the views learned the
// billing key, usage events too; both counts are shown so the exclusion never
// becomes an unexplained gap.
const BILLING_GRID_CLASS = 'grid grid-cols-[1fr_1.6fr_1.4fr_1fr_130px_120px_110px] items-center';

const EXCLUDED_KEY = ['excluded-accounts'] as const;

const ExcludedTab = () => {
  const { isAdmin } = useAuth();
  const queryClient = useQueryClient();
  const [showExclude, setShowExclude] = useState(false);
  const [unexclude, setUnexclude] = useState<ExcludedAccount | null>(null);
  const [showExcludeBilling, setShowExcludeBilling] = useState(false);
  const [unexcludeBilling, setUnexcludeBilling] = useState<ExcludedBillingAccount | null>(null);

  // This list changes only when an admin excludes/removes an account here, which
  // already invalidates the query on success. It polls only while the usage
  // rebuild such a change queues is pending, so the note below clears on its own.
  const fetchExcluded = async () =>
    keepKnownRebuildPending(
      await listExcludedAccounts(),
      queryClient.getQueryData<ExclusionList<ExcludedAccount>>(EXCLUDED_KEY),
    );
  const { data: excluded, isLoading } = useQuery<ExclusionList<ExcludedAccount>>({
    queryKey: EXCLUDED_KEY,
    queryFn: fetchExcluded,
    enabled: isAdmin,
    refetchInterval: (query) => usageRebuildPollInterval(query.state.data),
  });
  const accounts = excluded?.accounts ?? [];
  useRefreshWhenRebuilt(excluded?.usage_rebuild_pending);

  const { data: billingAccounts = [], isLoading: billingLoading } = useQuery<ExcludedBillingAccount[]>({
    queryKey: ['excluded-billing-accounts'],
    queryFn: listExcludedBillingAccounts,
    enabled: isAdmin,
  });

  if (!isAdmin) {
    return <div className="px-4 py-6 text-sm text-ink-3">관리자 전용 페이지입니다.</div>;
  }

  const handleOpenExclude = () => setShowExclude(true);
  const handleCloseExclude = () => setShowExclude(false);
  // Excluding/removing changes company-wide totals everywhere (cost, sessions,
  // users), so a full invalidation is deliberate rather than scoping it to
  // 'excluded-accounts' alone.
  const handleExcludeSuccess = () => {
    queryClient.invalidateQueries();
    setShowExclude(false);
  };
  const handleUnexcludeSuccess = () => {
    queryClient.invalidateQueries();
    setUnexclude(null);
  };
  const handleUnexcludeCancel = () => setUnexclude(null);

  const handleOpenExcludeBilling = () => setShowExcludeBilling(true);
  const handleCloseExcludeBilling = () => setShowExcludeBilling(false);
  const handleExcludeBillingSuccess = () => {
    queryClient.invalidateQueries();
    setShowExcludeBilling(false);
  };
  const handleUnexcludeBillingSuccess = () => {
    queryClient.invalidateQueries();
    setUnexcludeBilling(null);
  };
  const handleUnexcludeBillingCancel = () => setUnexcludeBilling(null);

  return (
    <div className="space-y-4">
      <header className="flex items-center justify-between">
        <h2 className="text-[16px] font-semibold text-ink">Excluded Accounts</h2>
        <button
          type="button"
          onClick={handleOpenExclude}
          className="px-3 py-1.5 text-[12px] font-medium rounded-lg bg-brand text-white hover:bg-brand-hover"
        >
          Exclude account
        </button>
      </header>

      <UsageRebuildNote pending={excluded?.usage_rebuild_pending} />

      <div className="bg-surface border border-border rounded-lg overflow-x-auto">
        <div className="min-w-[820px]">
          <header className={cn(GRID_CLASS, 'px-4 py-2.5 border-b border-surface-sunk bg-canvas')}>
            <span className="text-[11px] font-medium text-ink-3">Login Email</span>
            <span className="text-[11px] font-medium text-ink-3">Reason</span>
            <span className="text-[11px] font-medium text-ink-3">Excluded By</span>
            <span className="text-[11px] font-medium text-ink-3 text-right">Events Hidden</span>
            <span className="text-[11px] font-medium text-ink-3 text-right">Cost Hidden</span>
            <span className="text-[11px] font-medium text-ink-3 text-right">Action</span>
          </header>

          {isLoading && <CollectingLoader className="py-6" />}

          {!isLoading && accounts.length === 0 && (
            <div className="px-4 py-6 text-center text-sm text-ink-3">No accounts excluded</div>
          )}

          {accounts.map((a) => (
            <div
              key={a.login_email}
              className={cn(GRID_CLASS, 'px-4 py-3 border-b border-surface-sunk last:border-b-0 hover:bg-canvas transition-colors')}
            >
              <span className="min-w-0 pr-2">
                <span className="block text-[13px] text-ink font-mono truncate">{a.login_email}</span>
                {a.linked_billing_accounts && a.linked_billing_accounts.length > 0 && (
                  <span className="block text-[11px] text-ink-3 font-mono truncate">
                    {linkedBillingAccountsLabel(a.linked_billing_accounts)}
                  </span>
                )}
              </span>
              <span className="text-[12px] text-ink-2 truncate pr-2">{a.reason || '—'}</span>
              <span className="text-[12px] text-ink-3 truncate pr-2">{a.created_by || '—'}</span>
              <span className="text-[12px] text-ink text-right tabular-nums">{a.event_count.toLocaleString()}</span>
              <span className="text-[12px] text-ink text-right tabular-nums">{fmtCost(a.cost_usd)}</span>
              <span className="text-right">
                <button
                  type="button"
                  onClick={() => setUnexclude(a)}
                  className="text-[11px] font-medium text-brand hover:underline"
                >
                  Remove
                </button>
              </span>
            </div>
          ))}
        </div>
      </div>

      <header className="flex items-center justify-between pt-2">
        <div>
          <h2 className="text-[16px] font-semibold text-ink">Excluded Billing Accounts</h2>
          {/* Said here because the two lists look interchangeable and are not:
              an account with no login email cannot be excluded by the list above
              at all, which is the whole reason this one exists. */}
          <p className="mt-0.5 text-[11px] text-ink-3">
            For accounts with no login email to exclude by — Codex accounts carry none.
          </p>
        </div>
        <button
          type="button"
          onClick={handleOpenExcludeBilling}
          className="px-3 py-1.5 text-[12px] font-medium rounded-lg bg-brand text-white hover:bg-brand-hover"
        >
          Exclude billing account
        </button>
      </header>

      <div className="bg-surface border border-border rounded-lg overflow-x-auto">
        <div className="min-w-[820px]">
          <header className={cn(BILLING_GRID_CLASS, 'px-4 py-2.5 border-b border-surface-sunk bg-canvas')}>
            <span className="text-[11px] font-medium text-ink-3">Provider</span>
            <span className="text-[11px] font-medium text-ink-3">Account ID</span>
            <span className="text-[11px] font-medium text-ink-3">Reason</span>
            <span className="text-[11px] font-medium text-ink-3">Excluded By</span>
            <span className="text-[11px] font-medium text-ink-3 text-right">Readings Hidden</span>
            <span className="text-[11px] font-medium text-ink-3 text-right">Events Hidden</span>
            <span className="text-[11px] font-medium text-ink-3 text-right">Action</span>
          </header>

          {billingLoading && <CollectingLoader className="py-6" />}

          {!billingLoading && billingAccounts.length === 0 && (
            <div className="px-4 py-6 text-center text-sm text-ink-3">No billing accounts excluded</div>
          )}

          {billingAccounts.map((a) => (
            <div
              key={`${a.billing_provider}/${a.account_id}`}
              className={cn(BILLING_GRID_CLASS, 'px-4 py-3 border-b border-surface-sunk last:border-b-0 hover:bg-canvas transition-colors')}
            >
              <span className="text-[12px] text-ink-2 truncate pr-2">{a.billing_provider}</span>
              <span className="text-[13px] text-ink font-mono truncate pr-2">{a.account_id}</span>
              <span className="text-[12px] text-ink-2 truncate pr-2">{a.reason || '—'}</span>
              <span className="min-w-0 pr-2">
                <span className="block text-[12px] text-ink-3 truncate">{a.created_by || '—'}</span>
                {/* Registered by the account's owner from Settings (#716), which they
                    can take back themselves; removing it here overrides them. */}
                {a.self_registered && <span className="block text-[11px] text-ink-3">Self-registered</span>}
              </span>
              <span className="text-[12px] text-ink text-right tabular-nums">{a.sample_count.toLocaleString()}</span>
              <span className="text-[12px] text-ink text-right tabular-nums">{a.event_count.toLocaleString()}</span>
              <span className="text-right">
                <button
                  type="button"
                  onClick={() => setUnexcludeBilling(a)}
                  className="text-[11px] font-medium text-brand hover:underline"
                >
                  Remove
                </button>
              </span>
            </div>
          ))}
        </div>
      </div>

      {showExclude && <ExcludeAccountDialog onSuccess={handleExcludeSuccess} onCancel={handleCloseExclude} />}
      {showExcludeBilling && (
        <ExcludeBillingAccountDialog onSuccess={handleExcludeBillingSuccess} onCancel={handleCloseExcludeBilling} />
      )}
      {unexcludeBilling && (
        <UnexcludeBillingDialog
          account={unexcludeBilling}
          onSuccess={handleUnexcludeBillingSuccess}
          onCancel={handleUnexcludeBillingCancel}
        />
      )}
      {unexclude && (
        <UnexcludeDialog account={unexclude} onSuccess={handleUnexcludeSuccess} onCancel={handleUnexcludeCancel} />
      )}
    </div>
  );
};

export { ExcludedTab };
