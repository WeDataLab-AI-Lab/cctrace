'use client';

import { useMutation } from '@tanstack/react-query';
import { removeExcludedBillingAccount } from '@/lib/api';
import type { ExcludedBillingAccount } from '@/lib/types';
import { UnexcludeConfirmationDialog } from './unexclude-confirmation-dialog';

interface UnexcludeBillingDialogProps {
  account: ExcludedBillingAccount;
  onSuccess: () => void;
  onCancel: () => void;
}

// Like UnexcludeDialog, un-excluding is reversible in one click, so a plain
// confirmation is enough -- no typed phrase.
const UnexcludeBillingDialog = ({ account, onSuccess, onCancel }: UnexcludeBillingDialogProps) => {
  const mutation = useMutation({
    mutationFn: () => removeExcludedBillingAccount(account.billing_provider, account.account_id),
    onSuccess,
  });

  const handleConfirm = () => mutation.mutate();

  return (
    <UnexcludeConfirmationDialog
      description={
        <>
          <span className="font-mono text-ink">{account.account_id}</span> will appear on every screen again,
          including{' '}
          <span className="font-medium text-ink">{account.sample_count.toLocaleString()} quota readings</span> and{' '}
          <span className="font-medium text-ink">{account.event_count.toLocaleString()} usage events</span> it
          currently hides.
        </>
      }
      errorMessage={mutation.error?.message}
      isPending={mutation.isPending}
      onConfirm={handleConfirm}
      onCancel={onCancel}
    />
  );
};

export { UnexcludeBillingDialog };
export type { UnexcludeBillingDialogProps };
